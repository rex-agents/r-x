package rux

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

//go:embed web
var webFS embed.FS

type server struct {
	dataDir    string
	advertise  string // host:port guests use to reach the runner listener
	runnerBin  string
	roxBin     string
	sleepAfter time.Duration // stop a thread's guest after this long idle; 0 never
	memory     string
	backend    string // "rox" or "local" (no sandbox; for testing the app itself)
	allowUser  string
	guestNets  []*net.IPNet
	selfBin    string
	gh         *github
	models     *modelCache
	tools      *toolHost

	mu       sync.Mutex
	threads  map[string]*thread
	projects map[string]*project
	viewers  viewers
}

type threadMeta struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Project     string    `json:"project"`
	Kind        string    `json:"kind,omitempty"`   // "" for a worker, "manager" for a project's manager
	Parent      string    `json:"parent,omitempty"` // the manager thread that started this one
	Archived    bool      `json:"archived,omitempty"`
	Replied     time.Time `json:"replied,omitempty"` // when claude last finished a turn
	Repo        string    `json:"repo"`
	Branch      string    `json:"branch"`
	Model       string    `json:"model,omitempty"`  // claude --model; empty for the default
	Effort      string    `json:"effort,omitempty"` // claude --effort; empty for the default
	Prompt      string    `json:"prompt"`
	Status      string    `json:"status"` // provisioning, starting, running, idle, failed, destroying, destroyed
	Guest       string    `json:"guest"`
	Backend     string    `json:"backend"`
	Runtime     string    `json:"runtime,omitempty"`      // rox up's backend: container, linux, macos, fly or railway
	SharedGuest bool      `json:"shared_guest,omitempty"` // runs in an existing guest that rux didn't start, and mustn't stop or remove
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	PromptSent  bool      `json:"prompt_sent"`
	Connected   bool      `json:"connected"`
	Token       string    `json:"token,omitempty"`
}

type storedEvent struct {
	ID   int64           `json:"id"`
	TS   int64           `json:"ts"` // unix ms
	Src  string          `json:"src"`
	RSeq int64           `json:"rseq,omitempty"`
	Ev   json.RawMessage `json:"ev"`
}

type sseItem struct {
	id   int64
	line []byte
}

type thread struct {
	mu     sync.Mutex
	dir    string
	meta   threadMeta
	events [][]byte // events[i] has id i+1
	rseq   int64    // highest runner seq stored
	logf   *os.File
	subs   map[chan sseItem]struct{}
	send   chan wireMsg // current runner connection; nil if none
	cancel context.CancelFunc

	upgradeTried bool      // runner upgrade attempted this server run
	lastText     string    // the latest assistant text, for wake-ups and summaries
	pendingWake  []string  // wake-ups for this (manager) thread, sent when its runner connects
	pendingMsgs  []wireMsg // messages that arrived while the thread was asleep, sent when its runner connects
}

func runServe(args []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	s := &server{threads: map[string]*thread{}, selfBin: exe}
	listen := flags.String("listen", "127.0.0.1:7700", "web app listen address (front it with `tailscale serve`)")
	runnerListen := flags.String("runner-listen", "0.0.0.0:7701", "listen address for guest runners")
	cidrs := flags.String("guest-cidrs", "", "comma-separated networks allowed to reach the runner listener (default: Apple container's default network; loopback always allowed)")
	flags.StringVar(&s.advertise, "advertise", "", "host:port guests use to reach the runner listener (default: Apple container's gateway, runner port)")
	flags.StringVar(&s.dataDir, "data", filepath.Join(home, ".rux"), "data directory")
	flags.StringVar(&s.runnerBin, "runner-bin", "", "Linux rux binary served to guests (default: rux-linux-ARCH beside rux, or this version's release)")
	flags.StringVar(&s.roxBin, "rox", "rox", "rox binary")
	flags.StringVar(&s.memory, "memory", "4G", "guest memory")
	flags.DurationVar(&s.sleepAfter, "sleep-after", 15*time.Minute, "stop a thread's guest after it's been idle this long, and start it again on the next message (0 to keep guests running)")
	flags.StringVar(&s.backend, "backend", "rox", `"rox", or "local" to run threads unsandboxed on this host (testing only)`)
	flags.StringVar(&s.allowUser, "allow-user", "", "only allow this Tailscale login through `tailscale serve` (recommended)")
	claudeBin := flags.String("claude", "claude", "claude binary on this host, used only to list the models Claude Code offers")
	ghOn := flags.Bool("github", true, "give guests GitHub access and list repos with the token from rix credential github")
	flags.Parse(args)

	if s.advertise == "" || *cidrs == "" {
		gw, subnet, err := containerNetwork()
		if err != nil && s.backend == "rox" {
			log.Fatalf("can't detect Apple container's network (%v); pass --advertise and --guest-cidrs", err)
		}
		_, port, _ := net.SplitHostPort(*runnerListen)
		if s.advertise == "" {
			s.advertise = net.JoinHostPort(gw, port)
			if gw == "" {
				s.advertise = net.JoinHostPort("127.0.0.1", port)
			}
		}
		if *cidrs == "" {
			*cidrs = subnet
		}
	}
	for _, c := range strings.Split(*cidrs, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			log.Fatalf("bad --guest-cidrs entry %q: %v", c, err)
		}
		s.guestNets = append(s.guestNets, n)
	}
	s.allowTailnet()
	s.models = &modelCache{claudeBin: *claudeBin, dir: filepath.Join(s.dataDir, "tmp")}
	go s.models.list()
	s.gh = &github{off: !*ghOn}
	s.tools = newToolHost(s.dataDir)
	if s.gh.enabled() {
		go s.gh.listRepos() // warm the repo picker
	}
	if err := s.loadThreads(); err != nil {
		log.Fatal(err)
	}
	if err := s.loadProjects(); err != nil {
		log.Fatal(err)
	}
	if old := filepath.Join(s.dataDir, "secrets.env"); fileExists(old) {
		log.Printf("rux gets action secrets from rix now, and ignores %s. Move them with: rix secret import %s", old, old)
	}
	if _, err := rixBin(); err != nil {
		log.Printf("warning: %v", err)
	}
	if s.allowUser == "" {
		log.Print("warning: --allow-user not set; anyone who can reach the web app can use it")
	}

	app := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	app.Handle("GET /", http.FileServerFS(static))
	app.HandleFunc("GET /api/threads", s.handleList)
	app.HandleFunc("POST /api/threads", s.handleCreate)
	app.HandleFunc("GET /api/threads/{id}", s.handleGet)
	app.HandleFunc("GET /api/threads/{id}/events", s.handleEvents)
	app.HandleFunc("GET /api/threads/{id}/events/page", s.handleEventsPage)
	app.HandleFunc("POST /api/threads/{id}/messages", s.handleMessage)
	app.HandleFunc("POST /api/threads/{id}/interrupt", s.handleInterrupt)
	app.HandleFunc("POST /api/threads/{id}/sleep", s.handleSleep)
	app.HandleFunc("POST /api/threads/{id}/wake", s.handleWake)
	app.HandleFunc("POST /api/threads/{id}/destroy", s.handleDestroy)
	app.HandleFunc("POST /api/threads/{id}/archive", s.handleThreadArchive)
	app.HandleFunc("DELETE /api/threads/{id}", s.handleThreadDelete)
	app.HandleFunc("POST /api/projects/{id}/cleanup", s.handleProjectCleanup)
	app.HandleFunc("POST /api/projects/{id}/archive", s.handleProjectArchive)
	app.HandleFunc("GET /api/repos", s.handleRepos)
	app.HandleFunc("GET /api/projects", s.handleProjects)
	app.HandleFunc("POST /api/projects", s.handleProjectCreate)
	app.HandleFunc("GET /api/projects/{id}", s.handleProjectGet)
	app.HandleFunc("PATCH /api/projects/{id}", s.handleProjectUpdate)
	app.HandleFunc("POST /api/projects/{id}/manager", s.handleManagerMessage)
	app.HandleFunc("POST /api/threads/{id}/ui/{ui}/answer", s.handleUIAnswer)
	app.HandleFunc("GET /api/models", s.handleModels)
	app.HandleFunc("GET /api/tools", s.handleToolsList)
	app.HandleFunc("POST /api/threads/{id}/settings", s.handleSettings)
	app.HandleFunc("GET /api/threads/{id}/assets/{file}", s.handleAssetGet)
	app.HandleFunc("GET /api/overview", s.handleOverview)
	app.HandleFunc("GET /api/overview/stream", s.handleOverviewStream)

	guest := http.NewServeMux()
	guest.HandleFunc("GET /runner", s.handleRunner)
	guest.HandleFunc("GET /git-credential", s.handleGitCredential)
	guest.HandleFunc("POST /assets", s.handleAssetUpload)
	guest.HandleFunc("/mcp", s.handleMCP)
	guest.HandleFunc("POST /ui", s.handleUIPost)
	guest.HandleFunc("GET /tools", s.handleGuestTools)
	guest.HandleFunc("POST /tools/run", s.handleGuestToolRun)
	guest.HandleFunc("GET /tools/runs/{run}", s.handleGuestToolStatus)
	guest.HandleFunc("GET /dl/rux-runner", func(w http.ResponseWriter, r *http.Request) {
		path, err := s.runnerFor(guestOS(r.URL.Query().Get("os")), guestArch(r.URL.Query().Get("arch")))
		if err != nil {
			log.Printf("serving the runner: %v", err)
			http.Error(w, err.Error(), 503)
			return
		}
		http.ServeFile(w, r, path)
	})

	go s.statsLoop()
	go s.sleepLoop()
	go fetchContainerStats() // warm the cache so the first monitor view is instant
	go func() {
		log.Printf("runner listener on %s (guests use %s)", *runnerListen, s.advertise)
		log.Fatal(http.ListenAndServe(*runnerListen, s.guestOnly(guest)))
	}()
	log.Printf("web app on http://%s (backend %s, data %s)", *listen, s.backend, s.dataDir)
	log.Fatal(http.ListenAndServe(*listen, s.userOnly(app)))
}

// containerNetwork reads the gateway and subnet of Apple container's default network.
func containerNetwork() (gateway, subnet string, err error) {
	out, err := exec.Command("container", "network", "inspect", "default").Output()
	if err != nil {
		return "", "", err
	}
	var nets []struct {
		Status struct {
			IPv4Gateway string `json:"ipv4Gateway"`
			IPv4Subnet  string `json:"ipv4Subnet"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &nets); err != nil {
		return "", "", err
	}
	if len(nets) == 0 || nets[0].Status.IPv4Gateway == "" {
		return "", "", errors.New("no default network gateway")
	}
	return nets[0].Status.IPv4Gateway, nets[0].Status.IPv4Subnet, nil
}

// userOnly enforces --allow-user for requests proxied by `tailscale serve`,
// which sets Tailscale-User-Login. Only unproxied requests from loopback skip
// the check (e.g. curl on this machine).
func (s *server) userOnly(h http.Handler) http.Handler {
	var seen sync.Map
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login := r.Header.Get("Tailscale-User-Login")
		if s.allowUser != "" {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			proxied := login != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Tailscale-Headers-Info") != ""
			direct := ip != nil && ip.IsLoopback() && !proxied
			if !direct && !strings.EqualFold(login, s.allowUser) {
				if _, dup := seen.LoadOrStore("deny:"+login, true); !dup {
					log.Printf("denied web request from %q (login %q)", r.RemoteAddr, login)
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		s.viewers.note(r)
		if login != "" {
			if _, dup := seen.LoadOrStore(login, true); !dup {
				log.Printf("web app used by %s", login)
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (s *server) guestOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		ok := ip != nil && ip.IsLoopback()
		for _, n := range s.guestNets {
			if ip != nil && n.Contains(ip) {
				ok = true
			}
		}
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ---- thread store ----

func (s *server) threadsDir() string { return filepath.Join(s.dataDir, "threads") }

func (s *server) loadThreads() error {
	if err := os.MkdirAll(s.threadsDir(), 0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(s.threadsDir())
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.threadsDir(), e.Name())
		t, err := openThread(dir, nil)
		if err != nil {
			log.Printf("skipping thread %s: %v", e.Name(), err)
			continue
		}
		t.meta.Connected = false
		if t.meta.Status == "sleeping" || t.meta.Status == "waking" {
			t.meta.Status = "asleep" // the next message starts it, whichever way the stop or start went
		}
		if t.meta.Status == "provisioning" {
			t.meta.Status = "starting" // its runner may still dial in
		}
		s.threads[t.meta.ID] = t
	}
	return nil
}

// openThread loads a thread directory, or creates it when meta is non-nil.
func openThread(dir string, meta *threadMeta) (*thread, error) {
	t := &thread{dir: dir, subs: map[chan sseItem]struct{}{}}
	if meta != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		t.meta = *meta
		if err := t.saveMeta(); err != nil {
			return nil, err
		}
	} else {
		b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &t.meta); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), wsReadLimit)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		var se storedEvent
		if json.Unmarshal(line, &se) == nil {
			t.rseq = max(t.rseq, se.RSeq)
			if se.TS > 0 {
				t.meta.Updated = time.UnixMilli(se.TS)
			}
		}
		t.events = append(t.events, line)
	}
	t.logf = f
	return t, sc.Err()
}

func (t *thread) saveMeta() error {
	b, _ := json.MarshalIndent(t.meta, "", "  ")
	tmp := filepath.Join(t.dir, "meta.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(t.dir, "meta.json"))
}

// appendLocked stores an event and fans it out to SSE subscribers. Hold t.mu.
func (t *thread) appendLocked(src string, rseq int64, ev json.RawMessage) {
	now := time.Now()
	se := storedEvent{ID: int64(len(t.events)) + 1, TS: now.UnixMilli(), Src: src, RSeq: rseq, Ev: ev}
	line, _ := json.Marshal(se)
	t.events = append(t.events, line)
	t.logf.Write(append(line, '\n'))
	t.meta.Updated = now
	for ch := range t.subs {
		select {
		case ch <- sseItem{se.ID, line}:
		default: // slow client; it reconnects with Last-Event-ID
			delete(t.subs, ch)
			close(ch)
		}
	}
}

func (t *thread) event(ev json.RawMessage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.appendLocked("rux", 0, ev)
}

func (t *thread) setStatusLocked(st string) {
	if t.meta.Status == st {
		return
	}
	t.meta.Status = st
	t.appendLocked("rux", 0, ruxEv("rux_status", "status", st, "connected", t.meta.Connected))
	if err := t.saveMeta(); err != nil {
		log.Printf("thread %s: saving meta: %v", t.meta.ID, err)
	}
}

func (t *thread) setStatus(st string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.setStatusLocked(st)
}

func (t *thread) view() threadMeta {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.meta
	m.Token = ""
	return m
}

func (s *server) get(id string) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threads[id]
}

// ---- web API ----

func writeJSONResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSONResp(w, code, map[string]string{"error": msg})
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ts := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	out := make([]threadMeta, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	writeJSONResp(w, 200, out)
}

func (s *server) handleGet(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	writeJSONResp(w, 200, t.view())
}

var (
	ghShort = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
	ghSSH   = regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/]([\w.-]+/[\w.-]+?)(?:\.git)?/?$`)
)

// normalizeRepo turns owner/name and GitHub SSH remotes into HTTPS URLs, which
// the guest clones with the host's gh credentials. Guests get no SSH keys.
func normalizeRepo(repo string) (string, error) {
	if ghShort.MatchString(repo) {
		return "https://github.com/" + strings.TrimSuffix(repo, ".git") + ".git", nil
	}
	if m := ghSSH.FindStringSubmatch(repo); m != nil {
		return "https://github.com/" + m[1] + ".git", nil
	}
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(repo, " \t\n'\"\\") {
		return "", errors.New("use owner/name or an https:// git URL")
	}
	return repo, nil
}

// createReq describes a new thread. Kind is "" for a worker or "manager" for
// a project's manager; Parent is the manager that started a worker.
type createReq struct {
	Project, Repo, Branch, Prompt, Model, Effort string
	Runtime                                      string // rox up's backend for a new guest (default container)
	Guest                                        string // an existing rox guest to run in instead
	Kind, Parent                                 string
	Title                                        string `json:"-"` // set by managers; otherwise the prompt's first line
}

type reqError struct {
	code int
	msg  string
}

func (e *reqError) Error() string { return e.msg }

func errorCode(err error) int {
	var re *reqError
	if errors.As(err, &re) {
		return re.code
	}
	return 500
}

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "bad JSON")
		return
	}
	req.Kind, req.Parent = "", ""
	t, err := s.createThread(req)
	if err != nil {
		httpErr(w, errorCode(err), err.Error())
		return
	}
	writeJSONResp(w, 201, t.view())
}

func (s *server) createThread(req createReq) (*thread, error) {
	req.Repo, req.Branch, req.Prompt = strings.TrimSpace(req.Repo), strings.TrimSpace(req.Branch), strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return nil, &reqError{400, "prompt is required"}
	}
	// A project supplies the repo and the defaults; otherwise the repo picks
	// (or creates) the project.
	var proj project
	if req.Project != "" {
		s.mu.Lock()
		p := s.projects[req.Project]
		if p != nil {
			proj = *p
		}
		s.mu.Unlock()
		if p == nil {
			return nil, &reqError{404, "no such project"}
		}
		if req.Repo == "" {
			req.Repo = proj.Repo
		}
		if req.Repo == proj.Repo {
			req.Branch = cmpOr(req.Branch, proj.Branch)
		}
		req.Model, req.Effort = cmpOr(req.Model, proj.Model), cmpOr(req.Effort, proj.Effort)
	}
	var err error
	if req.Model, req.Effort, err = validSettings(req.Model, req.Effort); err != nil {
		return nil, &reqError{400, err.Error()}
	}
	if req.Repo != "" {
		if req.Repo, err = normalizeRepo(req.Repo); err != nil {
			return nil, &reqError{400, err.Error()}
		}
	} else {
		req.Branch = ""
	}
	if strings.HasPrefix(req.Branch, "-") || strings.ContainsAny(req.Branch, " \t\n~^:?*[\\") {
		return nil, &reqError{400, "invalid branch"}
	}
	req.Runtime, req.Guest = strings.TrimSpace(req.Runtime), strings.TrimSpace(req.Guest)
	if req.Guest != "" {
		if !guestNameOK.MatchString(req.Guest) {
			return nil, &reqError{400, "invalid guest name"}
		}
		req.Runtime = ""
	} else if req.Runtime == "" {
		req.Runtime = "container"
	} else if !runtimes[req.Runtime] {
		return nil, &reqError{400, "runtime must be container, linux, macos, fly or railway"}
	}
	if remoteRuntime(req.Runtime) && hostTailnetName() == "" {
		return nil, &reqError{400, req.Runtime + " guests reach rux over Tailscale, which isn't running on this Mac"}
	}
	s.mu.Lock()
	if req.Project == "" || projectIDFor(req.Repo) != req.Project {
		p := s.ensureProjectLocked(req.Repo)
		req.Project, proj = p.ID, *p
	}
	s.mu.Unlock()
	now := time.Now()
	title := req.Prompt
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80]) + "…"
	}
	if t := strings.TrimSpace(req.Title); t != "" {
		title = clipText(strings.ReplaceAll(t, "\n", " "), 80)
	}
	if req.Kind == "manager" {
		title = proj.Name + " · manager"
	}
	id := now.UTC().Format("20060102-150405") + "-" + randHex(3)
	meta := threadMeta{
		ID: id, Title: title, Project: req.Project, Kind: req.Kind, Parent: req.Parent,
		Repo: req.Repo, Branch: req.Branch, Prompt: req.Prompt, Model: req.Model, Effort: req.Effort,
		Status: "provisioning", Backend: s.backend, Created: now, Updated: now, Token: randHex(24),
		Runtime: req.Runtime, Guest: req.Guest, SharedGuest: req.Guest != "",
	}
	t, err := openThread(filepath.Join(s.threadsDir(), id), &meta)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.threads[id] = t
	s.mu.Unlock()
	t.event(ruxEv("rux_status", "status", "provisioning", "connected", false))
	if s.backend == "local" {
		go s.provisionLocal(t)
	} else {
		go s.provisionRox(t)
	}
	return t, nil
}

// handleEventsPage is GET /api/threads/{id}/events/page?before=ID&min=N&from=ID:
// stored events before ID (default: all), at least N of them (default 200)
// and reaching back to ID from if given, starting at a user message so turns
// come whole. The app loads a thread's latest page, then earlier ones as you
// scroll up.
func (s *server) handleEventsPage(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	q := r.URL.Query()
	t.mu.Lock()
	n := int64(len(t.events))
	before := n + 1
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && v > 0 && v <= n+1 {
		before = v
	}
	want := int64(200)
	if v, err := strconv.ParseInt(q.Get("min"), 10, 64); err == nil && v > 0 {
		want = min(v, 5000)
	}
	start := max(before-want, 1) // first id in the page
	if v, err := strconv.ParseInt(q.Get("from"), 10, 64); err == nil && v > 0 && v < start {
		start = max(v, before-3000) // to the first unseen event, within reason
	}
	// Back up to the start of a turn (a user message), but not forever.
	for limit := start - 2000; start > 1 && start > limit; start-- {
		var se struct {
			Ev struct{ Type string } `json:"ev"`
		}
		if json.Unmarshal(t.events[start-1], &se) == nil && se.Ev.Type == "rux_user" {
			break
		}
	}
	page := make([]json.RawMessage, 0, before-start)
	for id := start; id < before; id++ {
		page = append(page, t.events[id-1])
	}
	t.mu.Unlock()
	writeJSONResp(w, 200, map[string]any{"start": start, "events": page})
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, 500, "streaming unsupported")
		return
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if q := r.URL.Query().Get("after"); q != "" && after == 0 {
		after, _ = strconv.ParseInt(q, 10, 64)
	}
	ch := make(chan sseItem, 1024)
	t.mu.Lock()
	after = min(max(after, 0), int64(len(t.events)))
	backlog := t.events[after:]
	t.subs[ch] = struct{}{}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		if _, ok := t.subs[ch]; ok {
			delete(t.subs, ch)
			close(ch)
		}
		t.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	var buf bytes.Buffer
	last := after
	for i, line := range backlog {
		last = after + int64(i) + 1
		fmt.Fprintf(&buf, "id: %d\ndata: %s\n\n", last, line)
	}
	buf.WriteString("event: caughtup\ndata: {}\n\n")
	w.Write(buf.Bytes())
	fl.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case it, ok := <-ch:
			if !ok {
				return
			}
			if it.id <= last {
				continue
			}
			last = it.id
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", it.id, it.line)
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *server) sendToRunner(w http.ResponseWriter, r *http.Request, m wireMsg) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	t.mu.Lock()
	if t.send == nil && asleepStatus(t.meta.Status) {
		// The guest is stopped: start it, and send this when the runner connects.
		if m.T == "interrupt" {
			t.mu.Unlock()
			writeJSONResp(w, 202, map[string]bool{"ok": true})
			return
		}
		t.pendingMsgs = append(t.pendingMsgs, m)
		t.mu.Unlock()
		s.wakeIfAsleep(t)
		writeJSONResp(w, 202, map[string]bool{"ok": true, "waking": true})
		return
	}
	defer t.mu.Unlock()
	if t.send == nil {
		httpErr(w, 409, "the thread's runner isn't connected")
		return
	}
	select {
	case t.send <- m:
		writeJSONResp(w, 202, map[string]bool{"ok": true})
	default:
		httpErr(w, 503, "runner busy")
	}
}

func (s *server) handleMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string
		Images []struct{ Name, Mime, Data string }
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
		httpErr(w, 400, "bad JSON (or the images are over 64 MB together)")
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Images) == 0 {
		httpErr(w, 400, "text is required")
		return
	}
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	if len(req.Images) > 10 {
		httpErr(w, 400, "at most 10 images per message")
		return
	}
	m := wireMsg{T: "user", Text: req.Text}
	for _, im := range req.Images {
		switch im.Mime {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			httpErr(w, 400, "images must be PNG, JPEG, GIF or WebP")
			return
		}
		data, err := base64.StdEncoding.DecodeString(im.Data)
		if err != nil || len(data) == 0 {
			httpErr(w, 400, "bad image data")
			return
		}
		if len(data) > 5<<20 {
			httpErr(w, 400, "each image must be under 5 MB")
			return
		}
		name := safeAssetName(cmpOr(im.Name, "image"))
		if ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[im.Mime]; !strings.HasSuffix(strings.ToLower(name), ext) {
			name = strings.TrimSuffix(name, filepath.Ext(name)) + ext
		}
		u, err := s.storeAssetBytes(t, data, name)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		m.Files = append(m.Files, attachment{Name: name, Mime: im.Mime, Data: data, URL: u})
	}
	s.sendToRunner(w, r, m)
}

func (s *server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	s.sendToRunner(w, r, wireMsg{T: "interrupt"})
}

func (s *server) handleDestroy(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	s.destroyThread(t)
	writeJSONResp(w, 202, map[string]bool{"ok": true})
}

// destroyThread removes a thread's guest in the background. The thread and
// its transcript stay.
func (s *server) destroyThread(t *thread) {
	t.mu.Lock()
	guest, backend := t.meta.Guest, t.meta.Backend
	if t.cancel != nil {
		t.cancel()
	}
	t.setStatusLocked("destroying")
	t.mu.Unlock()
	go func() {
		if backend == "local" {
			s.stopLocal(t)
			t.setStatus("destroyed")
			return
		}
		if t.view().SharedGuest {
			s.stopSharedRunner(t)
			t.setStatus("destroyed")
			return
		}
		if guest == "" {
			t.event(ruxEv("rux_error", "text", "don't know this thread's guest name; remove it by hand with `rox ls` / `rox down NAME`"))
			t.setStatus("failed")
			return
		}
		err := s.runLogged(t, s.roxBin, []string{"down", guest}, nil)
		if err != nil {
			t.event(ruxEv("rux_error", "text", "rox down: "+err.Error()))
			t.setStatus("failed")
			return
		}
		t.setStatus("destroyed")
	}()
}

// ---- runner connections ----

func (s *server) handleRunner(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.URL.Query().Get("thread"))
	if t == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	t.mu.Lock()
	tok := t.meta.Token
	t.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(tok)) != 1 {
		http.Error(w, "forbidden", 403)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(wsReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var hello wireMsg
	if err := readJSON(ctx, c, &hello); err != nil || hello.T != "hello" {
		return
	}
	send := make(chan wireMsg, 64)
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel() // replace any older connection
	}
	t.send, t.cancel = send, cancel
	t.meta.Connected = true
	have := t.rseq
	t.appendLocked("rux", 0, ruxEv("rux_runner_connected", "hostname", hello.Info["hostname"], "dir", hello.Info["dir"], "session_id", hello.Info["session_id"]))
	switch t.meta.Status {
	case "provisioning", "starting", "failed", "waking", "asleep":
		t.setStatusLocked("idle")
	}
	send <- s.settingsMsg(&t.meta) // the thread's settings win over whatever the runner last had
	if !t.meta.PromptSent {
		t.meta.PromptSent = true
		send <- wireMsg{T: "user", Text: t.meta.Prompt}
	}
	for _, w := range t.pendingWake {
		send <- wireMsg{T: "user", Text: w}
	}
	t.pendingWake = nil
	for _, m := range t.pendingMsgs {
		send <- m
	}
	t.pendingMsgs = nil
	t.saveMeta()
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		if t.send == send {
			t.send, t.cancel = nil, nil
			t.meta.Connected = false
			t.appendLocked("rux", 0, ruxEv("rux_runner_disconnected"))
		}
		t.mu.Unlock()
	}()

	if err := writeJSON(ctx, c, wireMsg{T: "sync", Have: have}); err != nil {
		return
	}
	s.maybeUpgrade(t, hello.Info["version"])
	go func() {
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case m := <-send:
				if err := writeJSON(ctx, c, m); err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, pc := context.WithTimeout(ctx, 15*time.Second)
				err := c.Ping(pctx)
				pc()
				if err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		var m wireMsg
		if err := readJSON(ctx, c, &m); err != nil {
			return
		}
		if m.T != "event" {
			continue
		}
		t.mu.Lock()
		var parent, wake string
		if m.Seq > t.rseq {
			t.rseq = m.Seq
			t.appendLocked("runner", m.Seq, m.Ev)
			t.statusFromEventLocked(m.Ev)
			parent, wake = t.meta.Parent, t.wakeTextLocked(m.Ev)
		}
		t.mu.Unlock()
		if parent != "" && wake != "" {
			s.wake(parent, wake)
		}
	}
}

func (t *thread) statusFromEventLocked(ev json.RawMessage) {
	var e struct{ Type string }
	if json.Unmarshal(ev, &e) != nil {
		return
	}
	if t.meta.Status == "destroying" || t.meta.Status == "destroyed" {
		return
	}
	switch e.Type {
	case "rux_preparing":
		t.setStatusLocked("starting")
	case "rux_checkout":
		var c struct{ Branch string }
		if json.Unmarshal(ev, &c) == nil && c.Branch != "" && t.meta.Branch != c.Branch {
			t.meta.Branch = c.Branch
			t.saveMeta()
		}
	case "rux_ready":
		if t.meta.Status == "starting" {
			t.setStatusLocked("idle")
		}
	case "rux_user":
		t.setStatusLocked("running")
	case "result", "rux_exit":
		if e.Type == "result" {
			t.meta.Replied = time.Now()
			t.saveMeta()
		}
		t.setStatusLocked("idle")
	case "assistant":
		if txt := assistantText(ev); txt != "" {
			t.lastText = txt
		}
	}
}

// ---- provisioning ----

var guestNameRe = regexp.MustCompile(`\brv-[a-z0-9][a-z0-9-]*[0-9a-f]{8,}\b`)
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

func projectName(repo string) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimRight(repo, "/")), ".git")
	if i := strings.LastIndexByte(base, ':'); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if s := strings.Trim(b.String(), "-"); s != "" {
		return s
	}
	return "project"
}

func (s *server) runnerCommand(t *threadMeta, serverHost string) string {
	extra := ""
	if t.Repo != "" {
		extra += " --repo " + shQuote(t.Repo)
	}
	if t.Branch != "" {
		extra += " --branch " + shQuote(t.Branch)
	}
	if model := s.effectiveModel(t.Model); model != "" {
		extra += " --model " + shQuote(model)
	}
	if t.Effort != "" {
		extra += " --effort " + shQuote(t.Effort)
	}
	if t.Kind != "" {
		extra += " --role " + shQuote(t.Kind)
	}
	if t.SharedGuest {
		// Other work lives in this guest, so the thread gets a folder of its own.
		extra += ` --dir "$HOME/work/rux-` + t.ID + `"`
	}
	// Download beside the binary and rename over it: other threads' runners in the same
	// guest may be running it, and Linux refuses to overwrite a running executable.
	return fmt.Sprintf(`set -e; mkdir -p "$HOME/.local/bin"; bin="$HOME/.local/bin/rux-runner"; `+
		`curl -fsSL "http://%[1]s/dl/rux-runner?os=$(uname -s)&arch=$(uname -m)" -o "$bin.$$"; chmod +x "$bin.$$"; mv -f "$bin.$$" "$bin"; `+
		`exec "$bin" runner --server ws://%[1]s/runner --thread %[2]s --token %[3]s%[4]s`,
		serverHost, t.ID, t.Token, extra)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (s *server) provisionRox(t *thread) {
	t.mu.Lock()
	m := t.meta
	t.mu.Unlock()
	if m.SharedGuest {
		s.provisionShared(t, m)
		return
	}
	before := s.roxNames()
	// The runner clones the repo itself, with the GitHub token rix hands out, so rox
	// starts an empty guest. --no-git-agent keeps rox from forwarding the host's SSH
	// keys into it. --agents claude installs Claude Code, which rix signs in through
	// rox's hooks before the runner starts.
	runtime := cmpOr(m.Runtime, "container")
	args := []string{"up", runtime, "--no-project", "--no-git-agent", "--agents", "claude"}
	if !remoteRuntime(runtime) {
		// Local guests reach the runner listener on the host's own network; remote ones
		// (Fly, Railway) need the tailnet.
		args = append(args, "--no-tailscale")
	}
	if runtime == "container" {
		args = append(args, "--memory", s.memory)
	}
	args = append(args, "--", "bash", "-lc", s.runnerCommand(&m, s.advertiseFor(runtime)))
	err := s.runLogged(t, s.roxBin, args, func(line string) {
		if name := guestNameRe.FindString(line); name != "" {
			t.mu.Lock()
			if t.meta.Guest == "" {
				t.meta.Guest = name
				t.saveMeta()
			}
			t.mu.Unlock()
		}
	})
	t.mu.Lock()
	if t.meta.Guest == "" {
		for n := range s.roxNames() {
			if !before[n] {
				t.meta.Guest = n
				t.saveMeta()
				break
			}
		}
	}
	guest, connected := t.meta.Guest, t.meta.Connected
	t.appendLocked("rux", 0, ruxEv("rux_guest", "name", guest))
	if err != nil {
		t.appendLocked("rux", 0, ruxEv("rux_error", "text", "rox up failed: "+err.Error()))
		parent := t.meta.Parent
		if !connected {
			t.setStatusLocked("failed")
		}
		t.mu.Unlock()
		if parent != "" && !connected {
			s.wake(parent, fmt.Sprintf("%sworker %s (%q) failed to start: rox up failed: %v", wakePrefix, m.ID, m.Title, err))
		}
		return
	}
	if !connected {
		t.setStatusLocked("starting")
	}
	t.mu.Unlock()
	s.watchConnect(t)
}

func (s *server) watchConnect(t *thread) {
	time.Sleep(2 * time.Minute)
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.meta.Connected && t.meta.Status == "starting" {
		t.appendLocked("rux", 0, ruxEv("rux_error", "text",
			fmt.Sprintf("the guest's runner hasn't connected after 2 minutes; check the guest can reach %s (rox shell %s)", s.advertiseFor(t.meta.Runtime), t.meta.Guest)))
	}
}

// runLogged runs a command, streaming its output into the thread as rux_setup events.
func (s *server) runLogged(t *thread, bin string, args []string, onLine func(string)) error {
	shown := strings.Join(args, " ")
	t.mu.Lock()
	if tok := t.meta.Token; tok != "" {
		shown = strings.ReplaceAll(shown, tok, "…")
	}
	t.mu.Unlock()
	t.event(ruxEv("rux_setup", "text", "$ "+filepath.Base(bin)+" "+shown))
	cmd := exec.Command(bin, args...)
	cmd.Dir = s.dataDir
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			line := strings.TrimRight(ansiRe.ReplaceAllString(sc.Text(), ""), " \t")
			if line == "" {
				continue
			}
			if onLine != nil {
				onLine(line)
			}
			t.event(ruxEv("rux_setup", "text", line))
		}
		io.Copy(io.Discard, pr)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	return err
}

// scanLinesCR splits on \n or \r so progress output becomes separate lines.
func scanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (s *server) roxNames() map[string]bool {
	out := map[string]bool{}
	b, err := exec.Command(s.roxBin, "ls", "--all-projects", "--json").Output()
	if err != nil {
		return out
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return out
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if n, ok := x["name"].(string); ok {
				out[n] = true
			}
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// ---- local backend (no sandbox; for exercising the app without Rox) ----

var localProcs sync.Map // thread ID -> *exec.Cmd

func (s *server) provisionLocal(t *thread) {
	t.mu.Lock()
	m := t.meta
	t.mu.Unlock()
	work := filepath.Join(t.dir, "work")
	_, port, _ := net.SplitHostPort(s.advertise)
	cmd := exec.Command(s.selfBin, "runner", "--server", "ws://127.0.0.1:"+port+"/runner",
		"--thread", m.ID, "--token", m.Token, "--dir", work, "--state", filepath.Join(t.dir, "runner"),
		"--repo", m.Repo, "--branch", m.Branch, "--model", s.effectiveModel(m.Model), "--effort", m.Effort, "--role", m.Kind)
	logf, _ := os.Create(filepath.Join(t.dir, "runner.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	t.event(ruxEv("rux_setup", "text", "starting local runner (unsandboxed) in "+work))
	if err := cmd.Start(); err != nil {
		t.event(ruxEv("rux_error", "text", "starting runner: "+err.Error()))
		t.setStatus("failed")
		return
	}
	localProcs.Store(m.ID, cmd)
	t.mu.Lock()
	if !t.meta.Connected {
		t.setStatusLocked("starting")
	}
	t.mu.Unlock()
	go cmd.Wait()
}

func (s *server) stopLocal(t *thread) {
	if v, ok := localProcs.LoadAndDelete(t.meta.ID); ok {
		v.(*exec.Cmd).Process.Kill()
	}
}
