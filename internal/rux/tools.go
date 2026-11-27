package rux

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Host tools are small packages that run on the rux host (the Mac), which
// agents in guests can call with `rux action`: publishing a build to itch.io,
// signing, anything that needs the host or secrets the guests mustn't see.
//
// A tool is a folder ~/.rux/tools/NAME with a tool.json manifest and a script.
// The guest uploads the files the tool needs, the user approves the run in the
// thread (unless the project always allows it), and the script runs on the
// host with the secrets it lists, which come from rix (rix secret get). Its output streams into the
// thread, files it writes to $RUX_OUT are shared there, and the agent gets the
// result. Tools are off until turned on for a project.

type toolArg struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string (default), file (a file or folder from the guest), bool
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type toolSetting struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
}

type toolSpec struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Args        []toolArg     `json:"args,omitempty"`
	Settings    []toolSetting `json:"settings,omitempty"` // set per project in the app
	Approval    string        `json:"approval,omitempty"` // ask (default), every-run, none
	Secrets     []string      `json:"secrets,omitempty"`
	Concurrency int           `json:"concurrency,omitempty"`
	Timeout     string        `json:"timeout,omitempty"`
	MaxUploadMB int64         `json:"max_upload_mb,omitempty"`
	Run         string        `json:"run"`

	Dir     string   `json:"dir"`
	Error   string   `json:"error,omitempty"`   // why the manifest can't be used
	Missing []string `json:"missing,omitempty"` // secrets not in the secrets file
	timeout time.Duration
}

var toolNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type toolHost struct {
	dir, runsDir, stateDir string

	mu        sync.Mutex
	sems      map[string]chan struct{}
	runs      map[string]*toolRun
	approvals map[string]chan string // UI id -> the user's answer
}

func newToolHost(dataDir string) *toolHost {
	return &toolHost{
		dir: filepath.Join(dataDir, "actions"), runsDir: filepath.Join(dataDir, "action-runs"),
		stateDir: filepath.Join(dataDir, "action-state"),
		sems:     map[string]chan struct{}{}, runs: map[string]*toolRun{}, approvals: map[string]chan string{},
	}
}

func envKey(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}

// load reads every tool's manifest. Broken ones come back with Error set.
func (h *toolHost) load() []*toolSpec {
	ents, _ := os.ReadDir(h.dir)
	// Only the names: values are fetched from rix when an action runs.
	sec, secErr := rixSecretNames()
	if secErr != nil {
		log.Printf("reading secret names from rix: %v", secErr)
	}
	var out []*toolSpec
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(h.dir, e.Name())
		t := &toolSpec{Name: e.Name(), Dir: dir}
		b, err := os.ReadFile(filepath.Join(dir, "tool.json"))
		if err == nil {
			err = json.Unmarshal(b, t)
		}
		t.Name, t.Dir = e.Name(), dir // the folder name wins
		t.Approval = cmpOr(t.Approval, "ask")
		t.timeout = 15 * time.Minute
		if t.Timeout != "" {
			if d, perr := time.ParseDuration(t.Timeout); perr == nil && d > 0 {
				t.timeout = d
			} else if err == nil {
				err = fmt.Errorf("bad timeout %q", t.Timeout)
			}
		}
		switch {
		case err != nil:
		case !toolNameRE.MatchString(t.Name):
			err = errors.New("the folder name must be lowercase letters, digits and dashes")
		case strings.TrimSpace(t.Run) == "":
			err = errors.New(`"run" is required`)
		case t.Approval != "ask" && t.Approval != "every-run" && t.Approval != "none":
			err = fmt.Errorf(`approval must be "ask", "every-run" or "none", not %q`, t.Approval)
		}
		for _, a := range t.Args {
			if err == nil && (!envNameRE.MatchString(envKey(a.Name)) || a.Name == "wait" || a.Name == "help") {
				err = fmt.Errorf("bad argument name %q", a.Name)
			}
			if err == nil && a.Type != "" && a.Type != "string" && a.Type != "file" && a.Type != "bool" {
				err = fmt.Errorf("argument %s: type must be string, file or bool", a.Name)
			}
		}
		for _, k := range t.Secrets {
			if err == nil && !envNameRE.MatchString(k) {
				err = fmt.Errorf("bad secret name %q", k)
			}
			if !sec[k] {
				t.Missing = append(t.Missing, k)
			}
		}
		if err != nil {
			t.Error = err.Error()
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *toolHost) get(name string) *toolSpec {
	for _, t := range h.load() {
		if t.Name == name {
			return t
		}
	}
	return nil
}

func (h *toolHost) sem(t *toolSpec) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := max(t.Concurrency, 1)
	if s := h.sems[t.Name]; s != nil && cap(s) == n {
		return s
	}
	s := make(chan struct{}, n)
	h.sems[t.Name] = s
	return s
}

// ---- runs ----

type toolRun struct {
	ID, Tool, Thread string

	mu       sync.Mutex
	lines    []string
	notify   chan struct{} // closed and replaced on every change
	state    string        // approval, queued, running, done, failed, denied
	code     int
	summary  string
	detached bool // the caller stopped waiting; tell the thread when it ends
	ended    bool
}

func (r *toolRun) changed() {
	close(r.notify)
	r.notify = make(chan struct{})
}

func (r *toolRun) log(s string) {
	r.mu.Lock()
	r.lines = append(r.lines, s)
	if len(r.lines) > 5000 {
		r.lines = r.lines[len(r.lines)-5000:]
	}
	r.changed()
	r.mu.Unlock()
}

func (r *toolRun) setState(st string) {
	r.mu.Lock()
	r.state = st
	r.changed()
	r.mu.Unlock()
}

// ---- project settings ----

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// toolsFor returns the tools enabled for a project, and its settings for them.
func (s *server) toolsFor(pid string) ([]*toolSpec, *project) {
	s.mu.Lock()
	p := s.projects[pid]
	var pc project
	if p != nil {
		pc = *p
	}
	s.mu.Unlock()
	if p == nil {
		return nil, nil
	}
	var out []*toolSpec
	for _, t := range s.tools.load() {
		if t.Error == "" && contains(pc.Tools, t.Name) {
			out = append(out, t)
		}
	}
	return out, &pc
}

// handleToolsList is GET /api/tools: every installed tool, for the app.
func (s *server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, 200, map[string]any{"dir": s.tools.dir, "secrets_hint": secretsHint, "tools": s.tools.load()})
}

// ---- guest API ----

func (s *server) guestThread(w http.ResponseWriter, r *http.Request) *thread {
	t := s.get(r.URL.Query().Get("thread"))
	if t != nil {
		t.mu.Lock()
		tok, gone := t.meta.Token, t.meta.Status == "destroyed"
		t.mu.Unlock()
		if !gone && subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(tok)) == 1 {
			return t
		}
	}
	http.Error(w, "forbidden", 403)
	return nil
}

// handleGuestTools is GET /tools?thread=ID: the tools this thread can use.
func (s *server) handleGuestTools(w http.ResponseWriter, r *http.Request) {
	t := s.guestThread(w, r)
	if t == nil {
		return
	}
	tools, _ := s.toolsFor(t.view().Project)
	type out struct {
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Args        []toolArg `json:"args"`
		Approval    string    `json:"approval"`
	}
	list := []out{}
	for _, tl := range tools {
		list = append(list, out{tl.Name, tl.Description, tl.Args, tl.Approval})
	}
	writeJSONResp(w, 200, list)
}

type runRequest struct {
	Args map[string]string               `json:"args"`
	Git  struct{ Branch, Commit string } `json:"git"`
}

// handleGuestToolRun is POST /tools/run?thread=ID&tool=NAME&wait=SECONDS. The
// body is a gzipped tar: rux-run.json first, then files/ARG/... for file
// arguments. The response streams NDJSON: {"log": line}..., then {"end": ...}.
func (s *server) handleGuestToolRun(w http.ResponseWriter, r *http.Request) {
	t := s.guestThread(w, r)
	if t == nil {
		return
	}
	m := t.view()
	tools, proj := s.toolsFor(m.Project)
	var spec *toolSpec
	for _, tl := range tools {
		if tl.Name == r.URL.Query().Get("tool") {
			spec = tl
		}
	}
	if spec == nil {
		http.Error(w, fmt.Sprintf("no action %q is enabled for this project (rux action lists them; the user turns actions on in the project's settings)", r.URL.Query().Get("tool")), 404)
		return
	}
	if len(spec.Missing) > 0 {
		http.Error(w, fmt.Sprintf("tool %s needs the secret %s, which rix on the host doesn't have; ask the user to run rix secret set %s", spec.Name, strings.Join(spec.Missing, ", "), spec.Missing[0]), 412)
		return
	}
	id := time.Now().Format("20060102-150405") + "-" + randHex(3)
	work := filepath.Join(s.tools.runsDir, id)
	in := filepath.Join(work, "in")
	if err := os.MkdirAll(in, 0o700); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	limit := int64(2048) << 20
	if spec.MaxUploadMB > 0 {
		limit = spec.MaxUploadMB << 20
	}
	req, files, err := readRunUpload(http.MaxBytesReader(w, r.Body, limit), in, spec)
	if err != nil {
		os.RemoveAll(work)
		http.Error(w, err.Error(), 400)
		return
	}
	// Check the arguments against the manifest.
	vals := map[string]string{}
	for _, a := range spec.Args {
		v, ok := req.Args[a.Name]
		if a.Type == "file" {
			v, ok = files[a.Name]
		}
		if !ok && a.Required {
			os.RemoveAll(work)
			http.Error(w, fmt.Sprintf("%s needs --%s (rux action %s --help)", spec.Name, a.Name, spec.Name), 400)
			return
		}
		if ok {
			vals[a.Name] = v
		}
	}
	for k := range req.Args {
		if _, ok := vals[k]; !ok {
			os.RemoveAll(work)
			http.Error(w, fmt.Sprintf("%s has no argument %q (rux action %s --help)", spec.Name, k, spec.Name), 400)
			return
		}
	}

	run := &toolRun{ID: id, Tool: spec.Name, Thread: m.ID, notify: make(chan struct{}), state: "starting"}
	s.tools.mu.Lock()
	s.tools.runs[id] = run
	s.tools.mu.Unlock()
	go s.execRun(run, spec, proj, t, work, vals, req)

	// Stream until the run ends, the wait is up, or the caller goes away.
	wait := 90 * time.Second
	if n, err := time.ParseDuration(r.URL.Query().Get("wait") + "s"); err == nil && n >= 0 {
		wait = min(n, 9*time.Minute)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(200)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	enc := json.NewEncoder(w)
	enc.Encode(map[string]any{"run": id})
	flush()
	deadline := time.After(wait)
	sent := 0
	for {
		run.mu.Lock()
		lines, notify, ended := run.lines[min(sent, len(run.lines)):], run.notify, run.ended
		sent = len(run.lines)
		end := map[string]any{"run": id, "state": run.state, "code": run.code, "summary": run.summary}
		run.mu.Unlock()
		for _, l := range lines {
			enc.Encode(map[string]string{"log": l})
		}
		if ended {
			enc.Encode(map[string]any{"end": end})
			flush()
			return
		}
		flush()
		select {
		case <-notify:
			continue
		case <-deadline:
		case <-r.Context().Done():
		}
		run.mu.Lock()
		if !run.ended {
			run.detached = true
			end["state"] = run.state
			run.mu.Unlock()
			end["detached"] = true
			enc.Encode(map[string]any{"end": end})
			flush()
			return
		}
		run.mu.Unlock()
	}
}

// handleGuestToolStatus is GET /tools/runs/RUN?thread=ID.
func (s *server) handleGuestToolStatus(w http.ResponseWriter, r *http.Request) {
	t := s.guestThread(w, r)
	if t == nil {
		return
	}
	s.tools.mu.Lock()
	run := s.tools.runs[r.PathValue("run")]
	s.tools.mu.Unlock()
	if run == nil || run.Thread != t.meta.ID {
		http.Error(w, "no such run (runs are kept until the server restarts)", 404)
		return
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	writeJSONResp(w, 200, map[string]any{"run": run.ID, "tool": run.Tool, "state": run.state, "code": run.code, "summary": run.summary,
		"log": run.lines[max(0, len(run.lines)-40):]})
}

// readRunUpload reads the upload, extracting file arguments under in/ARG/.
// Returns the request and the host path of each file argument.
func readRunUpload(body io.Reader, in string, spec *toolSpec) (*runRequest, map[string]string, error) {
	zr, err := gzip.NewReader(body)
	if err != nil {
		return nil, nil, fmt.Errorf("bad upload: %v", err)
	}
	tr := tar.NewReader(zr)
	var req *runRequest
	files := map[string]string{}
	isFile := map[string]bool{}
	for _, a := range spec.Args {
		isFile[a.Name] = a.Type == "file"
	}
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("bad upload: %v", err)
		}
		if req == nil {
			if hd.Name != "rux-run.json" {
				return nil, nil, errors.New("bad upload: rux-run.json must come first")
			}
			req = &runRequest{}
			if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(req); err != nil {
				return nil, nil, fmt.Errorf("bad rux-run.json: %v", err)
			}
			continue
		}
		rest, ok := strings.CutPrefix(hd.Name, "files/")
		arg, rel, _ := strings.Cut(rest, "/")
		if !ok || !isFile[arg] {
			return nil, nil, fmt.Errorf("unexpected file %q in upload", hd.Name)
		}
		rel = filepath.Clean(filepath.FromSlash(strings.TrimSuffix(rel, "/")))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("bad path %q in upload", hd.Name)
		}
		root := filepath.Join(in, arg)
		dst := filepath.Join(root, rel)
		if top := strings.SplitN(rel, string(filepath.Separator), 2)[0]; files[arg] == "" {
			files[arg] = filepath.Join(root, top)
		} else if files[arg] != filepath.Join(root, top) {
			return nil, nil, fmt.Errorf("argument %s got more than one file", arg)
		}
		if err := noSymlinkParents(root, dst); err != nil {
			return nil, nil, err
		}
		switch hd.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return nil, nil, err
			}
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(dst), 0o755)
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(hd.Mode)&0o755|0o600)
			if err != nil {
				return nil, nil, err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("upload failed: %v", err)
			}
		case tar.TypeSymlink:
			// Allowed when it stays inside the argument, as app bundles' do.
			target := filepath.Clean(filepath.Join(filepath.Dir(dst), hd.Linkname))
			if filepath.IsAbs(hd.Linkname) || !strings.HasPrefix(target, root+string(filepath.Separator)) {
				return nil, nil, fmt.Errorf("symlink %q points outside the upload", hd.Name)
			}
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.Symlink(hd.Linkname, dst); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("unsupported file type in upload: %q", hd.Name)
		}
	}
	if req == nil {
		return nil, nil, errors.New("bad upload: empty")
	}
	if req.Args == nil {
		req.Args = map[string]string{}
	}
	return req, files, nil
}

// noSymlinkParents makes sure writing to dst can't go through a symlink.
func noSymlinkParents(root, dst string) error {
	rel, _ := filepath.Rel(root, filepath.Dir(dst))
	p := root
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		if c == "." || c == "" {
			continue
		}
		p = filepath.Join(p, c)
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("upload writes through a symlink at %s", p)
		}
	}
	if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("upload overwrites a symlink at %s", dst)
	}
	return nil
}

// execRun approves, runs and reports a tool run.
func (s *server) execRun(run *toolRun, spec *toolSpec, proj *project, t *thread, work string, vals map[string]string, req *runRequest) {
	start := time.Now()
	ev := func(kv ...any) { t.event(ruxEv("rux_tool", append([]any{"run", run.ID, "tool", spec.Name}, kv...)...)) }
	finish := func(state string, code int, summary string) {
		run.mu.Lock()
		run.state, run.code, run.summary, run.ended = state, code, summary, true
		detached := run.detached
		tail := run.lines[max(0, len(run.lines)-30):]
		run.changed()
		run.mu.Unlock()
		ev("state", state, "code", code, "summary", summary, "ms", time.Since(start).Milliseconds())
		os.RemoveAll(filepath.Join(work, "in"))
		if detached {
			msg := fmt.Sprintf("%sAction run %s (%s) ended: %s", wakePrefix, run.ID, spec.Name, state)
			if state == "failed" {
				msg += fmt.Sprintf(" (exit %d)", code)
			}
			if summary != "" {
				msg += "\n\n" + clipText(summary, 2000)
			}
			if len(tail) > 0 {
				msg += "\n\nLast output:\n" + clipText(strings.Join(tail, "\n"), 4000)
			}
			s.wake(t.meta.ID, msg)
		}
	}

	ev("state", "starting", "args", vals, "branch", req.Git.Branch, "commit", req.Git.Commit)

	// Approval.
	needs := spec.Approval == "every-run" || (spec.Approval == "ask" && !contains(proj.ToolsAllowed, spec.Name))
	if needs {
		run.setState("approval")
		run.log("waiting for the user to approve this run in the app")
		answer := s.askApproval(t, run, spec, vals, req)
		switch answer {
		case "always":
			s.mu.Lock()
			if p := s.projects[proj.ID]; p != nil && !contains(p.ToolsAllowed, spec.Name) {
				p.ToolsAllowed = append(p.ToolsAllowed, spec.Name)
				p.Updated = time.Now()
				s.saveProjectLocked(p)
			}
			s.mu.Unlock()
			fallthrough
		case "approve":
			run.log("approved")
		case "":
			finish("denied", -1, "nobody answered the approval request in time")
			return
		default:
			finish("denied", -1, "the user denied this run")
			return
		}
	}

	sem := s.tools.sem(spec)
	select {
	case sem <- struct{}{}:
	default:
		run.setState("queued")
		run.log("waiting for another " + spec.Name + " run to finish")
		ev("state", "queued")
		sem <- struct{}{}
	}
	defer func() { <-sem }()

	out := filepath.Join(work, "out")
	tmp := filepath.Join(work, "tmp")
	state := filepath.Join(s.tools.stateDir, spec.Name)
	for _, d := range []string{out, tmp, state} {
		os.MkdirAll(d, 0o700)
	}
	result := filepath.Join(work, "result.txt")
	home, _ := os.UserHomeDir()
	env := []string{
		"HOME=" + home, "USER=" + os.Getenv("USER"), "LANG=en_US.UTF-8", "TMPDIR=" + tmp,
		"PATH=" + strings.Join([]string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, "bin"), filepath.Join(home, ".local/bin"), os.Getenv("PATH"), "/usr/bin:/bin:/usr/sbin:/sbin"}, ":"),
		"RUX_BIN=" + s.selfBin, "RUX_TOOL=" + spec.Name, "RUX_TOOL_DIR=" + spec.Dir, "RUX_RUN=" + run.ID, "RUX_WORK=" + work, "RUX_OUT=" + out,
		"RUX_STATE=" + state, "RUX_RESULT=" + result, "RUX_THREAD=" + t.meta.ID, "RUX_PROJECT=" + proj.ID, "RUX_REPO=" + proj.Repo,
		"RUX_GIT_BRANCH=" + req.Git.Branch, "RUX_GIT_COMMIT=" + req.Git.Commit,
	}
	for k, v := range vals {
		env = append(env, "RUX_ARG_"+envKey(k)+"="+v)
	}
	for _, st := range spec.Settings {
		env = append(env, "RUX_SETTING_"+envKey(st.Name)+"="+cmpOr(proj.ToolSettings[spec.Name][st.Name], st.Default))
	}
	sec, missing, secErr := rixSecrets(spec.Secrets)
	if secErr == nil && len(missing) > 0 {
		secErr = fmt.Errorf("rix has no secret %s; run rix secret set %s", strings.Join(missing, ", "), missing[0])
	}
	if secErr != nil {
		finish("failed", -1, "couldn't get its secrets from rix: "+secErr.Error())
		return
	}
	var secretVals []string
	for _, k := range spec.Secrets {
		env = append(env, k+"="+sec[k])
		if len(sec[k]) >= 6 {
			secretVals = append(secretVals, sec[k])
		}
	}
	scrub := func(l string) string {
		for _, v := range secretVals {
			l = strings.ReplaceAll(l, v, "…")
		}
		return l
	}

	ctx, cancel := context.WithTimeout(context.Background(), spec.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", spec.Run)
	cmd.Dir, cmd.Env = spec.Dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	logf, _ := os.Create(filepath.Join(work, "log.txt"))
	run.setState("running")
	ev("state", "running")
	if err := cmd.Start(); err != nil {
		pw.Close()
		finish("failed", -1, "couldn't start: "+err.Error())
		return
	}

	// Lines go to the caller as they come and to the thread in batches.
	var pending []string
	var pmu sync.Mutex
	stopFlush := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		emitted := 0
		for {
			var stop bool
			select {
			case <-tick.C:
			case <-stopFlush:
				stop = true
			}
			pmu.Lock()
			batch := pending
			pending = nil
			pmu.Unlock()
			if len(batch) > 0 && emitted < 200<<10 {
				text := strings.Join(batch, "\n")
				emitted += len(text)
				ev("log", text)
			}
			if stop {
				return
			}
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l := scrub(sc.Text())
			if logf != nil {
				logf.WriteString(l + "\n")
			}
			run.log(l)
			pmu.Lock()
			pending = append(pending, l)
			pmu.Unlock()
		}
		io.Copy(io.Discard, pr)
	}()
	err := cmd.Wait()
	pw.Close()
	<-readDone
	close(stopFlush)
	<-flushed
	if logf != nil {
		logf.Close()
	}

	summary := ""
	if b, rerr := os.ReadFile(result); rerr == nil {
		summary = scrub(strings.TrimSpace(string(b)))
	}
	// Share what the tool made.
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		if e.Type().IsRegular() {
			if u, serr := s.storeAsset(t, filepath.Join(out, e.Name()), e.Name(), spec.Name+" · "+e.Name(), false); serr != nil {
				run.log("couldn't share " + e.Name() + ": " + serr.Error())
			} else {
				run.log("shared " + e.Name() + " (" + u + ")")
			}
		}
	}
	code := cmd.ProcessState.ExitCode()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		finish("failed", code, cmpOr(summary, "timed out after "+spec.timeout.String()))
	case err != nil:
		finish("failed", code, summary)
	default:
		finish("done", 0, summary)
	}
}

// askApproval shows an approval card in the thread and waits for the answer:
// approve, always, deny, or "" if nobody answers within a day.
func (s *server) askApproval(t *thread, run *toolRun, spec *toolSpec, vals map[string]string, req *runRequest) string {
	uiID := "ui-" + randHex(4)
	ch := make(chan string, 1)
	s.tools.mu.Lock()
	s.tools.approvals[uiID] = ch
	s.tools.mu.Unlock()
	defer func() {
		s.tools.mu.Lock()
		delete(s.tools.approvals, uiID)
		s.tools.mu.Unlock()
	}()

	var items []map[string]string
	for _, a := range spec.Args {
		v, ok := vals[a.Name]
		if !ok {
			continue
		}
		if a.Type == "file" {
			v = filepath.Base(v) + " (" + fmtBytes(dirSize(v)) + ")"
		}
		items = append(items, map[string]string{"key": a.Name, "value": v})
	}
	if req.Git.Branch != "" || req.Git.Commit != "" {
		items = append(items, map[string]string{"key": "from", "value": strings.TrimSpace(req.Git.Branch + " " + req.Git.Commit)})
	}
	kids := []any{}
	if spec.Description != "" {
		kids = append(kids, map[string]any{"type": "Text", "props": map[string]any{"text": spec.Description, "tone": "muted"}})
	}
	if len(items) > 0 {
		kids = append(kids, map[string]any{"type": "KeyValue", "props": map[string]any{"items": items}})
	}
	btn := func(label, variant, action string) map[string]any {
		return map[string]any{"type": "Button", "props": map[string]any{"label": label, "variant": variant}, "on": map[string]any{"press": map[string]any{"action": action}}}
	}
	buttons := []any{btn("Run it", "primary", "approve")}
	if spec.Approval == "ask" {
		buttons = append(buttons, btn("Always allow here", "default", "always"))
	}
	buttons = append(buttons, btn("Deny", "danger", "deny"))
	kids = append(kids, map[string]any{"type": "Buttons", "children": buttons})
	spec2, _ := json.Marshal(map[string]any{"type": "Card", "props": map[string]any{"title": "Run " + spec.Name + " on the host?"}, "children": kids})
	flat, err := normalizeSpec(spec2)
	if err != nil {
		return ""
	}
	t.event(ruxEv("rux_ui", "ui", uiID, "title", "action", "ask", true, "spec", flat, "run", run.ID))
	select {
	case a := <-ch:
		return a
	case <-time.After(24 * time.Hour):
		return ""
	}
}

// answerApproval delivers a UI answer to a waiting tool run. It reports
// whether ui was a tool approval.
func (s *server) answerApproval(ui, action string) bool {
	s.tools.mu.Lock()
	ch := s.tools.approvals[ui]
	s.tools.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- action:
	default:
	}
	return true
}

func dirSize(p string) int64 {
	var n int64
	filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// storeAssetBytes saves data as one of the thread's files without showing it
// on its own in the thread (the message it came with shows it).
func (s *server) storeAssetBytes(t *thread, data []byte, name string) (string, error) {
	if err := os.MkdirAll(t.assetsDir(), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(t.assetsDir(), ".upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	f.Close()
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entries, _ := os.ReadDir(t.assetsDir())
	file := fmt.Sprintf("%03d-%s", len(entries), safeAssetName(name))
	if err := os.Rename(f.Name(), filepath.Join(t.assetsDir(), file)); err != nil {
		return "", err
	}
	return "/api/threads/" + url.PathEscape(t.meta.ID) + "/assets/" + url.PathEscape(file), nil
}

// storeAsset copies a host file into the thread's shared files.
func (s *server) storeAsset(t *thread, src, name, caption string, quiet bool) (string, error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if st.Size() > maxAssetBytes {
		return "", fmt.Errorf("%s is over the %d MB limit", fmtBytes(st.Size()), maxAssetBytes>>20)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(t.assetsDir(), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(t.assetsDir(), ".upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	n, err := io.Copy(f, in)
	f.Close()
	if err != nil {
		return "", err
	}
	name = safeAssetName(name)
	t.mu.Lock()
	defer t.mu.Unlock()
	entries, _ := os.ReadDir(t.assetsDir())
	file := fmt.Sprintf("%03d-%s", len(entries), name)
	if err := os.Rename(f.Name(), filepath.Join(t.assetsDir(), file)); err != nil {
		return "", err
	}
	ctype, inline := assetType(file)
	u := "/api/threads/" + url.PathEscape(t.meta.ID) + "/assets/" + url.PathEscape(file)
	t.appendLocked("rux", 0, ruxEv("rux_asset", "file", file, "name", name, "caption", caption,
		"mime", strings.Split(ctype, ";")[0], "inline", inline, "size", n, "url", u, "quiet", quiet))
	return u, nil
}
