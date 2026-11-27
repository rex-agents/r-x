package rux

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const defaultAllowedTools = "Bash Edit Write Read Glob Grep WebFetch WebSearch TodoWrite NotebookEdit Task"

// runner lives inside a guest. It owns the claude process (so the server can
// restart without killing a thread), numbers every event, keeps them on disk,
// and replays whatever the server is missing each time it (re)connects.
type runner struct {
	serverURL, thread, token string
	dir, claudeBin, model    string
	allowedTools, stateDir   string
	repo, branch, effort     string
	notes                    string // project notes, appended to claude's system prompt
	project                  string // project name
	role                     string // "" for a worker, "manager" for a project's manager
	modelProxy               string // the authority's base URL for /proxy/...; when set, claude gets it and the thread token instead of model keys
	engine                   string // "" or claude-code runs claude -p; "harness" runs rux headless (the harness) in its place
	httpBase                 string // the server's runner listener, e.g. http://192.168.65.1:7701

	ready        chan struct{} // closed once the workspace is cloned or created
	retry        chan struct{} // a message arrived; set up the workspace again if it failed
	prepMu       sync.Mutex
	inMu         sync.Mutex
	inQ          []inboxMsg // accepted user messages not yet given to claude
	inSeq        int64
	retryDelay   time.Duration // first wait before redelivering a failed message; 0 is 2s
	inNotify     chan struct{}
	seen, queued map[string]bool
	announced    map[string]bool // ids with a rux_user event already
	gitEnv       []string        // extra environment for git and claude

	mu        sync.Mutex
	seq       int64
	lines     [][]byte // lines[i] is the encoded wire event with seq i+1
	logf      *os.File
	out       chan []byte // live queue of the current connection; nil if none
	outCancel context.CancelFunc

	sessionID string
	started   atomic.Bool // claude has persisted the session, so use --resume
	upgrading atomic.Bool

	procMu   sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{} // closed when cmd has exited
	retiring *exec.Cmd     // cmd is finishing its turn and will exit (effort changed)
}

// runnerLock holds the thread's lock for the life of the process.
var runnerLock *os.File

func runRunner(args []string) {
	fs := flag.NewFlagSet("runner", flag.ExitOnError)
	r := &runner{}
	fs.StringVar(&r.serverURL, "server", "", "server WebSocket URL, e.g. ws://192.168.64.1:7701/runner")
	fs.StringVar(&r.thread, "thread", "", "thread ID")
	fs.StringVar(&r.token, "token", "", "thread token")
	fs.StringVar(&r.dir, "dir", "", "working directory for claude (default $ROX_PROJECT_DIR, else cwd)")
	fs.StringVar(&r.claudeBin, "claude", "claude", "claude binary")
	fs.StringVar(&r.model, "model", "", "claude --model")
	fs.StringVar(&r.effort, "effort", "", "claude --effort")
	fs.StringVar(&r.allowedTools, "allowed-tools", defaultAllowedTools, "claude --allowedTools")
	fs.StringVar(&r.stateDir, "state", "", "state directory (default ~/.rux/THREAD)")
	fs.StringVar(&r.repo, "repo", "", "repository to clone into --dir (default: an empty git repo)")
	fs.StringVar(&r.branch, "branch", "", "branch to clone (default: the remote's default)")
	fs.StringVar(&r.role, "role", "", `"manager" to run a project's manager (rux MCP tools for its workers)`)
	fs.StringVar(&r.engine, "engine", "", `"harness" runs rux's own agent loop instead of claude -p (model: anthropic/… or openai/…)`)
	fs.StringVar(&r.modelProxy, "model-proxy", "", "the authority's base URL, e.g. http://192.168.65.1:7701: claude and codex use its /proxy/... with the thread token instead of model keys")
	fs.Parse(args)
	if r.serverURL == "" || r.thread == "" || r.token == "" {
		log.Fatal("runner: --server, --thread and --token are required")
	}
	if r.dir == "" {
		r.dir = os.Getenv("ROX_PROJECT_DIR")
	}
	home, _ := os.UserHomeDir()
	if r.dir == "" {
		name := "scratch"
		if r.repo != "" {
			name = projectName(r.repo)
		}
		r.dir = filepath.Join(home, "work", name)
	}
	if r.stateDir == "" {
		r.stateDir = filepath.Join(home, ".rux", r.thread)
	}
	// One runner per thread: when a guest starts again, rux relaunches the runner, and the
	// guest's Rex may restore the old one too. Go opens files close-on-exec, so an upgrade's
	// re-exec releases the lock for the new binary.
	if err := os.MkdirAll(r.stateDir, 0o700); err == nil {
		if lf, err := os.OpenFile(filepath.Join(r.stateDir, "runner.lock"), os.O_CREATE|os.O_RDWR, 0o600); err == nil {
			if syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
				log.Printf("runner: another runner has thread %s, so this one exits", r.thread)
				os.Exit(0)
			}
			runnerLock = lf
		}
	}
	r.ready, r.retry, r.inNotify = make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	if err := r.load(); err != nil {
		log.Fatal(err)
	}
	r.loadInbox()
	log.Printf("runner: thread %s, dir %s, session %s, %d stored events", r.thread, r.dir, r.sessionID, r.seq)
	r.setupGitEnv()
	go r.prepare()
	go r.deliverLoop() // messages wait for the workspace, then go to claude in order

	backoff := time.Second
	for {
		connected, err := r.connectOnce(context.Background())
		if connected {
			backoff = time.Second
		}
		log.Printf("runner: disconnected (%v); retrying in %s", err, backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (r *runner) load() error {
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		return err
	}
	if b, err := os.ReadFile(filepath.Join(r.stateDir, "session")); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			r.sessionID = f[0]
			r.started.Store(len(f) > 1 && f[1] == "started")
		}
	}
	if r.sessionID == "" {
		r.sessionID = newUUID()
		r.saveSession()
	}
	if b, err := os.ReadFile(filepath.Join(r.stateDir, "settings.json")); err == nil {
		var st struct{ Model, Effort, Notes, Project string }
		if json.Unmarshal(b, &st) == nil {
			r.model, r.effort, r.notes, r.project = st.Model, st.Effort, st.Notes, st.Project
		}
	}
	f, err := os.OpenFile(filepath.Join(r.stateDir, "events.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), wsReadLimit)
	for sc.Scan() {
		r.lines = append(r.lines, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading events: %w", err)
	}
	r.seq = int64(len(r.lines))
	r.logf = f
	return nil
}

func (r *runner) saveSession() {
	s := r.sessionID
	if r.started.Load() {
		s += " started"
	}
	os.WriteFile(filepath.Join(r.stateDir, "session"), []byte(s+"\n"), 0o600)
}

// emit records an event and forwards it to the server if connected.
func (r *runner) emit(ev []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	line, _ := json.Marshal(wireMsg{T: "event", Seq: r.seq, Ev: ev})
	r.lines = append(r.lines, line)
	r.logf.Write(append(line, '\n'))
	if r.out != nil {
		select {
		case r.out <- line:
		default: // server too slow: drop the connection, it will resync on reconnect
			r.outCancel()
			r.out, r.outCancel = nil, nil
		}
	}
}

func (r *runner) connectOnce(ctx context.Context) (bool, error) {
	u := fmt.Sprintf("%s?thread=%s&token=%s", r.serverURL, url.QueryEscape(r.thread), url.QueryEscape(r.token))
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, _, err := websocket.Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return false, err
	}
	defer c.CloseNow()
	c.SetReadLimit(wsReadLimit)

	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	host, _ := os.Hostname()
	if err := writeJSON(cctx, c, wireMsg{T: "hello", Info: map[string]string{
		"hostname": host, "dir": r.dir, "session_id": r.sessionID, "version": selfVersion(), "acks": "1",
	}}); err != nil {
		return true, err
	}
	var sync wireMsg
	if err := readJSON(cctx, c, &sync); err != nil {
		return true, err
	}
	if sync.T != "sync" {
		return true, fmt.Errorf("expected sync, got %q", sync.T)
	}

	out := make(chan []byte, 4096)
	r.mu.Lock()
	if r.outCancel != nil {
		r.outCancel()
	}
	have := min(max(sync.Have, 0), r.seq)
	if sync.Have > r.seq {
		log.Printf("runner: server has seq %d but runner only %d (state lost?)", sync.Have, r.seq)
	}
	backlog := r.lines[have:r.seq]
	r.out, r.outCancel = out, ccancel
	r.mu.Unlock()
	log.Printf("runner: connected; replaying %d events", len(backlog))

	errc := make(chan error, 4)
	go func() {
		for _, l := range backlog {
			if err := c.Write(cctx, websocket.MessageText, l); err != nil {
				errc <- err
				return
			}
		}
		for {
			select {
			case l := <-out:
				if err := c.Write(cctx, websocket.MessageText, l); err != nil {
					errc <- err
					return
				}
			case <-cctx.Done():
				errc <- cctx.Err()
				return
			}
		}
	}()
	go func() {
		for {
			var m wireMsg
			if err := readJSON(cctx, c, &m); err != nil {
				errc <- err
				return
			}
			r.handle(m, func(a wireMsg) {
				if err := writeJSON(cctx, c, a); err != nil {
					select {
					case errc <- err:
					default:
					}
				}
			})
		}
	}()
	err = <-errc

	r.mu.Lock()
	if r.out == out {
		r.out, r.outCancel = nil, nil
	}
	r.mu.Unlock()
	return true, err
}

func (r *runner) handle(m wireMsg, reply func(wireMsg)) {
	switch m.T {
	case "user":
		if r.accept(m) {
			reply(wireMsg{T: "ack", ID: m.ID})
		}
	case "upgrade":
		go r.upgrade()
	case "settings":
		r.applySettings(m.Info["model"], m.Info["effort"], m.Info["notes"], m.Info["project"])
	case "interrupt":
		r.procMu.Lock()
		if r.cmd != nil && r.cmd.Process != nil {
			r.cmd.Process.Signal(os.Interrupt)
			r.emit(ruxEv("rux_interrupt"))
		}
		r.procMu.Unlock()
	}
}

// sendUser hands a message to claude. It returns nil only once the whole message is written to
// claude's stdin, and then records it as delivered (the rux_delivered event with its id). On any error the
// caller keeps the message and tries again. A crash after the write but before the record means
// the message runs again after a restart: at-least-once, never silently lost.
func (r *runner) sendUser(id, text string, files ...attachment) error {
	r.procMu.Lock()
	defer r.procMu.Unlock()
	for r.upgrading.Load() { // the runner is about to re-exec; this message would be lost
		r.procMu.Unlock()
		time.Sleep(time.Second)
		r.procMu.Lock()
	}
	for r.cmd != nil && r.cmd == r.retiring { // wait for it to exit, then start afresh
		done := r.done
		r.procMu.Unlock()
		<-done
		r.procMu.Lock()
	}
	select { // never run in a directory the checkout didn't produce
	case <-r.ready:
	default:
		return errors.New("the workspace isn't ready; not starting claude")
	}
	if r.cmd == nil {
		if err := r.startClaude(); err != nil {
			return fmt.Errorf("starting claude: %w", err)
		}
	}
	// Attached images go to claude as images, and into the guest as files
	// in case the work needs them (a mockup to match, an icon to use).
	var urls, paths []string
	var content []any
	if len(files) > 0 {
		dir := filepath.Join(r.stateDir, "attachments")
		os.MkdirAll(dir, 0o755)
		for _, f := range files {
			p := filepath.Join(dir, time.Now().Format("150405")+"-"+safeAssetName(f.Name))
			if os.WriteFile(p, f.Data, 0o644) == nil {
				paths = append(paths, p)
			}
			urls = append(urls, f.URL)
			content = append(content, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": f.Mime, "data": base64.StdEncoding.EncodeToString(f.Data)}})
		}
	}
	ctext := text
	if len(paths) > 0 {
		ctext = strings.TrimSpace(text + "\n\n(The user attached " + map[bool]string{true: "an image", false: "images"}[len(paths) == 1] + ", also saved as " + strings.Join(paths, ", ") + ".)")
	}
	content = append(content, map[string]any{"type": "text", "text": ctext})
	b, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	})
	// The user's turn is announced before the first write, so claude's answer, which the
	// stdout reader emits without waiting for us, can't land ahead of it. Once per message,
	// not once per attempt.
	r.inMu.Lock()
	announce := id == "" || !r.announced[id]
	if id != "" {
		r.announced[id] = true
	}
	r.inMu.Unlock()
	if announce {
		r.emit(ruxEv("rux_user", "text", text, "images", urls, "mid", id))
	}
	if _, err := r.stdin.Write(append(b, '\n')); err != nil {
		// Part of the line may have gone through. End this claude, so the half line dies with
		// it and the retry starts a clean process (--resume).
		r.stdin.Close()
		r.retiring = r.cmd
		return fmt.Errorf("writing to claude: %w", err)
	}
	if id != "" {
		r.emit(ruxEv("rux_delivered", "id", id)) // the dedupe record: claude has all of it
	}
	return nil
}

// startClaude must be called with procMu held.
func (r *runner) startClaude() error {
	args := []string{"-p",
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		// No approvals UI yet: anything not pre-allowed is denied instead of hanging.
		"--permission-prompts", "none",
	}
	tools := r.allowedTools
	if r.httpBase != "" && r.engine != "harness" {
		// Every thread gets the authority's tools for starting and managing sub-agents.
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"rux": map[string]any{
			"type": "http", "url": r.httpBase + "/mcp", "headers": map[string]string{"Authorization": "Bearer " + r.token}}}})
		args = append(args, "--mcp-config", string(cfg))
		tools = strings.TrimSpace(tools + " mcp__rux")
	}
	if tools != "" {
		args = append(args, "--allowedTools", tools)
	}
	if r.model != "" {
		args = append(args, "--model", r.model)
	}
	if r.effort != "" {
		args = append(args, "--effort", r.effort)
	}
	prompt := shareSystemPrompt + "\n\n" + spawnSystemPrompt
	if r.role == "manager" {
		prompt = strings.ReplaceAll(managerSystemPrompt, "PROJECT", cmpOr(r.project, "this project")) + "\n\n" + shareSystemPrompt
	}
	if strings.TrimSpace(r.notes) != "" {
		prompt += "\n\nNotes from the user for every thread in this project:\n" + r.notes
	}
	args = append(args, "--append-system-prompt", prompt)
	if r.started.Load() {
		args = append(args, "--resume", r.sessionID)
	} else {
		args = append(args, "--session-id", r.sessionID)
	}
	bin := r.claudeBin
	if r.engine == "harness" {
		// rux's own loop speaks the same stream-json, so nothing downstream changes.
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		bin = exe
		args = []string{"headless", "--model", r.model, "--dir", r.dir, "--system", prompt, "--session-id", r.sessionID,
			"--state", r.stateDir}
		if r.effort != "" {
			args = append(args, "--effort", r.effort)
		}
		if r.modelProxy != "" {
			args = append(args, "--model-proxy", r.modelProxy)
		}
		if r.httpBase != "" {
			args = append(args, "--mcp", r.httpBase+"/mcp")
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = r.dir
	cmd.Env = r.modelEnv(append(os.Environ(), r.gitEnv...))
	cmd.Env = append(cmd.Env, "RUX_THREAD_TOKEN="+r.token)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	r.cmd, r.stdin, r.done = cmd, stdin, done
	r.emit(ruxEv("rux_claude_start", "args", strings.ReplaceAll(strings.Join(args, " "), r.token, "…"), "dir", r.dir))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), wsReadLimit)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			if !json.Valid(b) {
				r.emit(ruxEv("rux_stdout", "text", string(b)))
				continue
			}
			r.noteStarted(b)
			r.emit(b)
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			r.emit(ruxEv("rux_stderr", "text", sc.Text()))
		}
	}()
	go func() {
		wg.Wait()
		err := cmd.Wait()
		r.procMu.Lock()
		if r.cmd == cmd {
			r.cmd, r.stdin = nil, nil
		}
		close(done)
		r.procMu.Unlock()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		r.emit(ruxEv("rux_exit", "code", cmd.ProcessState.ExitCode(), "error", msg))
	}()
	return nil
}

// noteStarted marks the session as resumable once claude has produced a turn.
func (r *runner) noteStarted(b []byte) {
	if r.started.Load() {
		return
	}
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &e) == nil && (e.Type == "assistant" || e.Type == "result") {
		r.started.Store(true)
		r.saveSession()
	}
}

// setupGitEnv points git (for the clone and for everything claude runs) at
// `rux git-credential`, which fetches the host's GitHub token from the server
// when git needs it. It's all environment, so nothing is written to the guest's
// git config and the local backend doesn't touch the host's.
func (r *runner) setupGitEnv() {
	u, err := url.Parse(r.serverURL)
	if err != nil {
		return
	}
	u.Scheme = map[string]string{"wss": "https"}[u.Scheme]
	if u.Scheme == "" {
		u.Scheme = "http"
	}
	u.Path, u.RawQuery = "/git-credential", url.Values{"thread": {r.thread}}.Encode()
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cfg := [][2]string{{"credential.https://github.com.helper", "!" + shQuote(exe) + " git-credential"}}
	if c, err := fetchCredential(u.String(), r.token); err == nil {
		cfg = append(cfg, [2]string{"user.name", c.Name}, [2]string{"user.email", c.Email})
	} else {
		r.emit(ruxEv("rux_setup", "text", "no GitHub access from the host: "+err.Error()))
	}
	cred := u.String()
	r.httpBase = u.Scheme + "://" + u.Host
	u.Path = "/assets"
	assets := u.String()
	u.Path = "/ui"
	r.gitEnv = []string{"GIT_TERMINAL_PROMPT=0", "RUX_CRED_URL=" + cred, "RUX_ASSET_URL=" + assets, "RUX_UI_URL=" + u.String(), "RUX_CRED_TOKEN=" + r.token,
		"PATH=" + r.binDir(exe) + string(os.PathListSeparator) + os.Getenv("PATH"),
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(cfg))}
	for i, kv := range cfg {
		r.gitEnv = append(r.gitEnv, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
}

// prepare clones the repo (or creates an empty one) the first time the runner starts, then
// lets queued messages through. It reports whether the workspace is ready. If the requested
// checkout fails it's reported as rux_setup_failed and nothing runs: claude isn't started in
// an empty directory in its place. It runs again when another message arrives or the server
// resends one. A workspace with no repo requested keeps its fallback of an empty directory.
func (r *runner) prepare() bool {
	r.prepMu.Lock()
	defer r.prepMu.Unlock()
	select {
	case <-r.ready:
		return true
	default:
	}
	marker := filepath.Join(r.stateDir, "prepared")
	if _, err := os.Stat(marker); err == nil {
		close(r.ready)
		return true
	}
	r.emit(ruxEv("rux_preparing"))
	if _, err := os.Stat(filepath.Join(r.dir, ".git")); err != nil {
		var err error
		if r.repo != "" {
			err = r.clone()
		} else {
			err = os.MkdirAll(r.dir, 0o755)
			if err == nil {
				err = r.git(r.dir, "init", "--quiet", "--initial-branch", "main")
			}
		}
		if err != nil && r.repo != "" {
			msg := "checking out " + r.repo + ": " + err.Error()
			r.emit(ruxEv("rux_error", "text", msg))
			r.emit(ruxEv("rux_setup_failed", "text", msg))
			return false
		}
		if err != nil {
			r.emit(ruxEv("rux_error", "text", "setting up the workspace: "+err.Error()))
			os.MkdirAll(r.dir, 0o755) // let claude start anyway, so the thread is still usable
			r.emit(ruxEv("rux_ready"))
			close(r.ready)
			return true
		}
	}
	out, _ := exec.Command("git", "-C", r.dir, "symbolic-ref", "--short", "HEAD").Output()
	r.emit(ruxEv("rux_checkout", "branch", strings.TrimSpace(string(out)), "dir", r.dir))
	os.WriteFile(marker, nil, 0o600)
	r.emit(ruxEv("rux_ready"))
	close(r.ready)
	return true
}

// clone checks the repo out beside r.dir and moves it into place, so a failed or interrupted
// clone leaves nothing behind in r.dir. It refuses a directory that already holds files:
// those aren't ours to replace.
func (r *runner) clone() error {
	if st, err := os.Stat(r.dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s exists and isn't a directory", r.dir)
		}
		if ents, err := os.ReadDir(r.dir); err != nil || len(ents) > 0 {
			return fmt.Errorf("%s already has files and isn't a git checkout, so it was left alone", r.dir)
		}
	}
	parent := filepath.Dir(r.dir)
	tmp := r.dir + ".rux-clone-" + randHex(3)
	args := []string{"clone", "--progress"}
	if r.branch != "" {
		args = append(args, "--branch", r.branch)
	}
	if err := r.git(parent, append(args, "--", r.repo, tmp)...); err != nil {
		os.RemoveAll(tmp) // the partial clone is ours; r.dir is untouched
		return err
	}
	os.Remove(r.dir) // an empty directory; Remove refuses anything else
	if err := os.Rename(tmp, r.dir); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return nil
}

// git runs a git command, streaming its output into the thread.
func (r *runner) git(dir string, args ...string) error {
	os.MkdirAll(dir, 0o755)
	r.emit(ruxEv("rux_setup", "text", "$ git "+strings.Join(args, " ")))
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), r.gitEnv...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Split(scanLinesCR)
		last := ""
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			// Progress lines repeat with a new percentage; keep only the finished ones.
			if line == "" || line == last || (strings.Contains(line, "%") && !strings.Contains(line, "done")) {
				continue
			}
			last = line
			r.emit(ruxEv("rux_setup", "text", line))
		}
		io.Copy(io.Discard, pr)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	return err
}

type gitCred struct {
	Username, Password, Name, Email, Login string
}

func fetchCredential(u, token string) (*gitCred, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var c gitCred
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// runGitCredential implements git's credential helper protocol for "get":
// https://git-scm.com/docs/gitcredentials#_custom_helpers
func runGitCredential(args []string) {
	if len(args) == 0 || args[0] != "get" {
		return // store/erase: nothing to do
	}
	in, _ := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	host := ""
	for _, line := range strings.Split(string(in), "\n") {
		if v, ok := strings.CutPrefix(line, "host="); ok {
			host = v
		}
	}
	if host != "github.com" {
		return
	}
	c, err := fetchCredential(os.Getenv("RUX_CRED_URL"), os.Getenv("RUX_CRED_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "rux git-credential:", err)
		return
	}
	fmt.Printf("username=%s\npassword=%s\n", c.Username, c.Password)
}

// applySettings changes the model and effort for this thread. A model change
// goes to a running claude with the set_model control request. Effort has no
// live switch, so claude finishes its turn and exits, and the next message
// starts it again with --resume and the new effort.
func (r *runner) applySettings(model, effort, notes, project string) {
	r.procMu.Lock()
	defer r.procMu.Unlock()
	if model == r.model && effort == r.effort && notes == r.notes && project == r.project {
		return
	}
	oldModel, oldEffort := r.model, r.effort
	r.model, r.effort, r.notes, r.project = model, effort, notes, project
	b, _ := json.Marshal(map[string]string{"model": model, "effort": effort, "notes": notes, "project": project})
	os.WriteFile(filepath.Join(r.stateDir, "settings.json"), b, 0o600)
	// New notes reach claude the next time it starts.
	if r.cmd == nil || r.cmd == r.retiring || (model == oldModel && effort == oldEffort) {
		return
	}
	if effort != oldEffort {
		r.stdin.Close()
		r.retiring = r.cmd
		return
	}
	if model == "" {
		model = "default"
	}
	req, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "rux-model-" + randHex(4),
		"request": map[string]any{"subtype": "set_model", "model": model}})
	if _, err := r.stdin.Write(append(req, '\n')); err != nil {
		r.emit(ruxEv("rux_error", "text", "switching model: "+err.Error()))
	}
}

// binDir returns a directory holding this binary as `rux`, for claude's PATH,
// so `rux share` and `rux screenshot` work in the guest.
func (r *runner) binDir(exe string) string {
	if filepath.Base(exe) == "rux" {
		return filepath.Dir(exe)
	}
	dir := filepath.Join(r.stateDir, "bin")
	os.MkdirAll(dir, 0o755)
	link := filepath.Join(dir, "rux")
	if cur, err := os.Readlink(link); err != nil || cur != exe {
		os.Remove(link)
		os.Symlink(exe, link)
	}
	return dir
}
