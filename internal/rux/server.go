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
	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/quorum"
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
	agent      string // default agent CLI for threads with no model: claude or codex
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

	name         string    // this authority's name in the pack
	selfURL      string    // the authority's own HTTP URL, once listening
	started      time.Time // when this authority started
	hub          packHub
	packMu       sync.Mutex
	pack         PackSource // see authority_pack.go
	packAdapter  PackSource // the quorum's view, once this authority is in a pack
	packLocal    PackSource
	clients      clientRegistry
	guestLister  func() []roxGuest      // tests: the host's rox guests
	guestStats   func() []containerStat // tests: their CPU and memory
	vaults       map[string]VaultSource // by name: "local", and "leader" once a quorum adds it
	vaultDefault string                 // the vault source threads use unless they say otherwise
	proxy        *modelProxy
	node         *quorum.Node // this authority's place in a pack; nil without quorum wiring
	inner        http.Handler // the web app's routes, without user checks and federation

	mu       sync.Mutex
	threads  map[string]*thread
	projects map[string]*project
	viewers  viewers
}

type threadMeta struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Project         string    `json:"project"`
	Kind            string    `json:"kind,omitempty"`             // "" for a worker, "manager" for a project's manager
	Parent          string    `json:"parent,omitempty"`           // the manager thread that started this one
	ParentAuthority string    `json:"parent_authority,omitempty"` // where the parent runs; empty: here
	Depth           int       `json:"depth,omitempty"`            // how many threads started this one, directly or not
	Archived        bool      `json:"archived,omitempty"`
	Replied         time.Time `json:"replied,omitempty"` // when claude last finished a turn
	Repo            string    `json:"repo"`
	Branch          string    `json:"branch"`
	LocalDir        string    `json:"local_dir,omitempty"` // the project's directory on this host, copied into the guest by rox up --local
	Model           string    `json:"model,omitempty"`     // claude --model; empty for the default
	Effort          string    `json:"effort,omitempty"`    // claude --effort; empty for the default
	Prompt          string    `json:"prompt"`
	Status          string    `json:"status"` // provisioning, starting, running, idle, failed, destroying, destroyed
	Guest           string    `json:"guest"`
	Backend         string    `json:"backend"`
	Runtime         string    `json:"runtime,omitempty"`      // rox up's backend: container, linux, macos, fly or railway
	SharedGuest     bool      `json:"shared_guest,omitempty"` // runs in an existing guest that rux didn't start, and mustn't stop or remove
	Created         time.Time `json:"created"`
	Updated         time.Time `json:"updated"`
	PromptSent      bool      `json:"prompt_sent"`
	SetupFailed     bool      `json:"setup_failed,omitempty"` // the runner couldn't check out the repo, so claude hasn't run
	Connected       bool      `json:"connected"`
	Token           string    `json:"token,omitempty"`
	Vault           string    `json:"vault,omitempty"`  // which vault source serves this thread's model keys: "local" or "leader"
	Engine          string    `json:"engine,omitempty"` // "" or claude-code: claude -p; "harness": rux's own agent loop (anthropic/… or openai/… models) via the model proxy
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
	mu        sync.Mutex
	dir       string
	meta      threadMeta
	events    [][]byte // events[i] has id i+1
	rseq      int64    // highest runner seq stored
	logf      *os.File
	subs      map[chan sseItem]struct{}
	send      chan wireMsg // current runner connection; nil if none
	cancel    context.CancelFunc
	cost      float64 // summed total_cost_usd of the thread's result events
	authority string  // set on a stand-in for a parent that runs on another authority

	onEvent  func(line []byte) // set by server.attach; called with t.mu held
	onStatus func()            // likewise, on a status change

	upgradeTried bool      // runner upgrade attempted this server run
	lastText     string    // the latest assistant text, for wake-ups and summaries
	outbox       []outItem // user messages the runner hasn't acked; see outbox.go
	outLog       *os.File
	kick         chan struct{} // wakes the current connection to send the outbox
}

func runServe(args []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	exe, _ := os.Executable()
	s := &server{threads: map[string]*thread{}, selfBin: exe}
	listen := flags.String("listen", "127.0.0.1:7700", "web app listen address (front it with `tailscale serve`)")
	runnerListen := flags.String("runner-listen", "0.0.0.0:7701", "listen address for guest runners")
	cidrs := flags.String("guest-cidrs", "", "comma-separated networks allowed to reach the runner listener (default: Apple container's default network; loopback always allowed)")
	flags.StringVar(&s.advertise, "advertise", "", "host:port guests use to reach the runner listener (default: Apple container's gateway, runner port)")
	flags.StringVar(&s.dataDir, "data", authorityHome(), "data directory")
	flags.StringVar(&s.runnerBin, "runner-bin", "", "Linux rux binary served to guests (default: rux-linux-ARCH beside rux, or this version's release)")
	flags.StringVar(&s.roxBin, "rox", "rox", "rox binary")
	flags.StringVar(&s.memory, "memory", "4G", "guest memory")
	flags.DurationVar(&s.sleepAfter, "sleep-after", 15*time.Minute, "stop a thread's guest after it's been idle this long, and start it again on the next message (0 to keep guests running)")
	flags.StringVar(&s.backend, "backend", "rox", `"rox", or "local" to run threads unsandboxed on this host (testing only)`)
	flags.StringVar(&s.allowUser, "allow-user", "", "only allow this Tailscale login through `tailscale serve` (recommended)")
	flags.StringVar(&s.agent, "agent", "claude", "agent CLI new threads run in their guests when no model is chosen: claude or codex (a thread's model 'codex[/MODEL]' or 'claude-code[/MODEL]' picks one)")
	claudeBin := flags.String("claude", "claude", "claude binary on this host, used only to list the models Claude Code offers")
	ghOn := flags.Bool("github", true, "give guests GitHub access and list repos with the token from rix credential github")
	proxyOn := flags.Bool("model-proxy", true, "give guests the model proxy instead of model keys")
	quorumListen := flags.String("quorum-listen", "0.0.0.0:7710", "listen address for other authorities in a pack")
	quorumURLFlag := flags.String("quorum-url", "", "where other authorities reach the quorum listener (default: the tailnet address, port 7710)")
	vaultDef := flags.String("vault", "", `default vault source for model keys: "local" (this host's rix) or "leader" (default: authority.json, else local)`)
	flags.Parse(args)
	if s.agent != "claude" && s.agent != "codex" {
		log.Fatal("rux serve: --agent must be claude or codex")
	}
	s.started = time.Now()
	cfg := loadAuthorityConfig(s.dataDir)
	s.name = cfg.Name
	s.vaultDefault = cmpOr(*vaultDef, cfg.Vault)
	s.SetVaultSource("local", newLocalVault())
	if *proxyOn {
		s.proxy = newModelProxy(s)
	}

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

	app, guest := s.routes()
	s.inner = app
	go s.statsLoop()
	go s.sleepLoop()
	go fetchContainerStats() // warm the cache so the first monitor view is instant
	go func() {
		log.Printf("runner listener on %s (guests use %s)", *runnerListen, s.advertise)
		log.Fatal(http.ListenAndServe(*runnerListen, s.guestOnly(guest)))
	}()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	sockLn, err := listenUnix(authorityPath(s.dataDir, "sock"))
	if err != nil {
		log.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	s.registerAuthority("http://" + net.JoinHostPort(host, port))
	qln, err := net.Listen("tcp", *quorumListen)
	if err != nil {
		log.Printf("warning: no quorum listener on %s (%v); this authority can't join a pack", *quorumListen, err)
	}
	if err := s.startQuorum(context.Background(), quorumOptions{Listener: qln, Advertise: quorumURL(*quorumURLFlag)}); err != nil {
		log.Fatalf("quorum: %v", err)
	}
	if err := s.node.ServeAdmin(context.Background()); err != nil {
		log.Printf("warning: no quorum admin socket (%v), so rux quorum can't manage this authority while it runs", err)
	}
	go func() { log.Fatal(serveUnix(sockLn, s.userOnly(s.federate(app)))) }()
	log.Printf("authority %s: web app on %s and %s (backend %s, data %s)", s.authName(), s.selfURL, sockLn.Addr(), s.backend, s.dataDir)
	log.Fatal(http.Serve(ln, s.userOnly(s.federate(app))))
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
			onSock, _ := r.Context().Value(unixConnKey).(bool)
			direct := (onSock || ip != nil && ip.IsLoopback()) && !proxied
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
		s.attach(t)
	}
	return nil
}

// openThread loads a thread directory, or creates it when meta is non-nil.
func openThread(dir string, meta *threadMeta) (*thread, error) {
	t := &thread{dir: dir, subs: map[chan sseItem]struct{}{}, kick: make(chan struct{}, 1)}
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
			if se.Src == "rux" && bytes.Contains(se.Ev, []byte(`"rux_reprovision"`)) {
				t.rseq = 0 // a new guest's runner counts from 1
			}
			if se.TS > 0 {
				t.meta.Updated = time.UnixMilli(se.TS)
			}
		}
		t.events = append(t.events, line)
		t.addCost(line)
	}
	t.logf = f
	if err := sc.Err(); err != nil {
		return t, err
	}
	return t, t.loadOutbox()
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
	t.addCost(line)
	if t.onEvent != nil {
		t.onEvent(line)
	}
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
	if t.onStatus != nil {
		t.onStatus()
	}
}

// addCost adds a result event's cost to the thread's total.
func (t *thread) addCost(line []byte) {
	if !bytes.Contains(line, []byte(`"total_cost_usd"`)) {
		return
	}
	var se struct {
		Ev struct {
			Type string  `json:"type"`
			Cost float64 `json:"total_cost_usd"`
		} `json:"ev"`
	}
	if json.Unmarshal(line, &se) == nil && se.Ev.Type == "result" {
		t.cost += se.Ev.Cost
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
	// ?authority=NAME lists another authority's threads, and ?authority=all the whole pack's,
	// both as authapi.ThreadSummary.
	if a := r.URL.Query().Get("authority"); a != "" && a != s.authName() {
		if a == "all" {
			a = ""
		}
		sums, err := s.packSource().Threads(r.Context(), a)
		if err != nil {
			httpErr(w, 502, err.Error())
			return
		}
		writeJSONResp(w, 200, sums)
		return
	}
	out := make([]threadView, 0, len(ts))
	for _, t := range ts {
		out = append(out, s.viewOf(t))
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
	writeJSONResp(w, 200, s.viewOf(t))
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
	ParentAuthority                              string
	Depth                                        int
	Title                                        string            `json:"-"` // set by managers; otherwise the prompt's first line
	Placement                                    authapi.Placement `json:"placement"`
	Vault                                        string            `json:"vault"`    // "local" or "leader"; default: authority.json, else local
	Engine                                       string            `json:"engine"`   // "claude-code" (default) or "harness", which takes a provider/model such as anthropic/claude-sonnet-5-5
	Provider                                     string            `json:"provider"` // optional: claude-code, codex, anthropic or openai; sets engine and model prefix
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
	req.Kind, req.Parent, req.ParentAuthority, req.Depth = "", "", "", 0
	view, err := s.createPlaced(r.Context(), req)
	if err != nil {
		httpErr(w, errorCode(err), err.Error())
		return
	}
	writeJSONResp(w, 201, view)
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
		if p == nil && req.ParentAuthority == "" {
			return nil, &reqError{404, "no such project"}
		} else if p == nil {
			req.Project = "" // a sub-agent of another authority's project: this one makes the project from the repo
		}
		if req.Repo == "" {
			req.Repo = proj.Repo
		}
		if req.Repo == proj.Repo {
			req.Branch = cmpOr(req.Branch, proj.Branch)
		}
		if proj.Dir != "" {
			// --local copies the checkout as it is now, so no branch is checked out for the thread.
			req.Repo, req.Branch = proj.Repo, ""
		}
		req.Model, req.Effort = cmpOr(req.Model, proj.Model), cmpOr(req.Effort, proj.Effort)
	}
	if req.Provider != "" {
		var perr error
		if req.Engine, req.Model, perr = applyProvider(req.Provider, req.Engine, req.Model); perr != nil {
			return nil, &reqError{400, perr.Error()}
		}
	}
	engine, err := s.resolveEngine(req.Engine, req.Model)
	if err != nil {
		return nil, &reqError{400, err.Error()}
	}
	if engine == "harness" {
		_, name, _ := splitModel(req.Model)
		if _, req.Effort, err = validSettings(name, req.Effort); err != nil {
			return nil, &reqError{400, err.Error()}
		}
	} else if req.Model, req.Effort, err = validSettings(req.Model, req.Effort); err != nil {
		return nil, &reqError{400, err.Error()}
	}
	if req.Kind == "manager" && s.agentOf(req.Model) == "codex" {
		// A project's manager drives rux's MCP tools through Claude Code; Codex is for workers.
		// A manager asked for Codex, or defaulted to it, runs on Claude Code's default.
		req.Model = claudeCode
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
	if proj.Dir != "" {
		if err := s.checkDirProjectPlacement(&proj, &req); err != nil {
			return nil, err
		}
	}
	local, vault, err := s.resolvePlacement(req.Placement, req.Vault)
	if err != nil {
		return nil, &reqError{400, err.Error()}
	}
	if local && proj.Dir != "" {
		return nil, &reqError{400, "project " + proj.ID + " is a local directory, which threads get a copy of in a guest; it can't run unsandboxed on the host"}
	}
	if local {
		req.Runtime, req.Guest = "", ""
	}
	if remoteRuntime(req.Runtime) && hostTailnetName() == "" {
		return nil, &reqError{400, req.Runtime + " guests reach rux over Tailscale, which isn't running on this Mac"}
	}
	s.mu.Lock()
	if proj.Dir == "" && (req.Project == "" || projectIDFor(req.Repo) != req.Project) {
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
		ID: id, Title: title, Project: req.Project, Kind: req.Kind, Parent: req.Parent, ParentAuthority: req.ParentAuthority, Depth: req.Depth,
		Repo: req.Repo, Branch: req.Branch, Prompt: req.Prompt, Model: req.Model, Effort: req.Effort,
		Status: "provisioning", Backend: s.backend, Created: now, Updated: now, Token: randHex(24),
		Runtime: req.Runtime, Guest: req.Guest, SharedGuest: req.Guest != "", Vault: vault, Engine: engine, LocalDir: proj.Dir,
	}
	if local {
		meta.Backend = "local"
	}
	t, err := openThread(filepath.Join(s.threadsDir(), id), &meta)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.threads[id] = t
	s.mu.Unlock()
	s.attach(t)
	t.mu.Lock()
	t.queuePromptLocked() // the first message waits for the runner, however long the guest takes
	t.mu.Unlock()
	t.event(ruxEv("rux_status", "status", "provisioning", "connected", false))
	if meta.Backend == "local" {
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
	if m.T == "interrupt" { // only for a live turn, so it isn't kept
		t.mu.Lock()
		send, asleep := t.send, asleepStatus(t.meta.Status)
		if send != nil {
			select {
			case send <- m:
				t.mu.Unlock()
				writeJSONResp(w, 202, map[string]bool{"ok": true})
			default:
				t.mu.Unlock()
				httpErr(w, 503, "runner busy")
			}
			return
		}
		t.mu.Unlock()
		if asleep {
			writeJSONResp(w, 202, map[string]bool{"ok": true})
			return
		}
		httpErr(w, 409, errNoRunner.Error())
		return
	}
	waking, err := s.enqueueUser(t, m, false)
	switch {
	case errors.Is(err, errNoRunner), errors.Is(err, errGuestGone):
		httpErr(w, 409, err.Error())
	case err != nil:
		httpErr(w, 500, "saving the message: "+err.Error())
	case waking:
		writeJSONResp(w, 202, map[string]bool{"ok": true, "waking": true})
	default:
		writeJSONResp(w, 202, map[string]bool{"ok": true})
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
	send := make(chan wireMsg, 64) // control messages only (settings, interrupt, upgrade); never filled under t.mu
	acks := hello.Info["acks"] == "1"
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel() // replace any older connection
	}
	t.send, t.cancel = send, cancel
	t.kick = make(chan struct{}, 1)
	kick := t.kick
	t.meta.Connected = true
	have := t.rseq
	t.appendLocked("rux", 0, ruxEv("rux_runner_connected", "hostname", hello.Info["hostname"], "dir", hello.Info["dir"], "session_id", hello.Info["session_id"]))
	switch t.meta.Status {
	case "provisioning", "starting", "failed", "waking", "asleep":
		if !t.meta.SetupFailed {
			t.setStatusLocked("idle")
		}
	}
	t.queuePromptLocked() // normally queued when the thread was created
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
		sent := map[string]bool{} // outbox ids written on this connection
		write := func(m wireMsg) bool {
			if err := writeJSON(ctx, c, m); err != nil {
				cancel()
				return false
			}
			return true
		}
		flush := func() bool {
			t.mu.Lock()
			var todo []outItem
			for _, it := range t.outbox {
				if !sent[it.ID] {
					todo = append(todo, it)
				}
			}
			t.mu.Unlock()
			for _, it := range todo {
				if !write(it.Msg) {
					return false
				}
				sent[it.ID] = true
				if !acks { // an older runner can't ack, so being written is all we know
					t.mu.Lock()
					t.ackLocked(it.ID)
					t.mu.Unlock()
				}
			}
			return true
		}
		t.mu.Lock()
		settings := s.settingsMsg(&t.meta) // the thread's settings win over whatever the runner last had
		t.mu.Unlock()
		if !write(settings) || !flush() {
			return
		}
		for {
			select {
			case m := <-send:
				if !write(m) {
					return
				}
			case <-kick:
				if !flush() {
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
		if m.T == "ack" {
			t.mu.Lock()
			t.ackLocked(m.ID)
			t.mu.Unlock()
			continue
		}
		if m.T != "event" {
			continue
		}
		t.mu.Lock()
		var parent, parentAuth, wake string
		if m.Seq > t.rseq {
			t.rseq = m.Seq
			t.appendLocked("runner", m.Seq, m.Ev)
			t.statusFromEventLocked(m.Ev)
			parent, parentAuth, wake = t.meta.Parent, t.meta.ParentAuthority, t.wakeTextLocked(m.Ev)
		}
		t.mu.Unlock()
		if parent != "" && wake != "" {
			s.wakeParent(parent, parentAuth, wake)
		}
	}
}

func (t *thread) clearSetupFailedLocked() {
	if t.meta.SetupFailed {
		t.meta.SetupFailed = false
		t.saveMeta()
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
		t.clearSetupFailedLocked()
		t.setStatusLocked("starting")
	case "rux_checkout":
		var c struct{ Branch string }
		if json.Unmarshal(ev, &c) == nil && c.Branch != "" && t.meta.Branch != c.Branch {
			t.meta.Branch = c.Branch
			t.saveMeta()
		}
	case "rux_ready":
		t.clearSetupFailedLocked()
		if t.meta.Status == "starting" {
			t.setStatusLocked("idle")
		}
	case "rux_setup_failed":
		t.meta.SetupFailed = true
		t.setStatusLocked("failed")
		t.saveMeta()
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
	if t.LocalDir != "" {
		// rox up --local already put a copy of the checkout here; the runner finds its .git
		// and doesn't clone.
		extra += ` --dir "` + guestCheckoutDir(t.LocalDir) + `"`
	} else {
		if t.Repo != "" {
			extra += " --repo " + shQuote(t.Repo)
		}
		if t.Branch != "" {
			extra += " --branch " + shQuote(t.Branch)
		}
	}
	if t.Engine == "harness" {
		extra += " --engine harness"
	}
	if model := s.threadModel(t); model != "" {
		extra += " --model " + shQuote(model)
	}
	if t.Effort != "" {
		extra += " --effort " + shQuote(t.Effort)
	}
	if t.Kind != "" {
		extra += " --role " + shQuote(t.Kind)
	}
	if s.proxy != nil {
		extra += " --model-proxy " + shQuote("http://"+serverHost)
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
	// The runner clones the repo itself, with the GitHub token rix hands out, so rox
	// starts an empty guest. --no-git-agent keeps rox from forwarding the host's SSH
	// keys into it. --agents installs the thread's agent (Claude Code or Codex), which rix signs in through
	// rox's hooks before the runner starts.
	//
	// A local-directory project is the exception: rox up --local, run in the checkout, copies
	// it (unpushed commits and uncommitted files too) into the guest, and the runner works there.
	runtime := cmpOr(m.Runtime, "container")
	projectFlag, dir := "--no-project", s.dataDir
	if m.LocalDir != "" {
		projectFlag, dir = "--local", m.LocalDir
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.mu.Lock()
			t.appendLocked("rux", 0, ruxEv("rux_error", "text", "the project's directory "+dir+" is gone, so there's nothing to copy into a guest"))
			t.setStatusLocked("failed")
			t.mu.Unlock()
			return
		}
	}
	args := []string{"up", runtime, projectFlag, "--no-git-agent", "--agents", s.agentOf(m.Model)}
	if s.proxy != nil {
		// The guest's models go through the authority's proxy, so no real sign-in goes in.
		args = append(args, "--credentials=false")
	}
	if !remoteRuntime(runtime) {
		// Local guests reach the runner listener on the host's own network; remote ones
		// (Fly, Railway) need the tailnet.
		args = append(args, "--no-tailscale")
	}
	if runtime == "container" {
		args = append(args, "--memory", s.memory)
	}
	args = append(args, "--", "bash", "-lc", s.runnerCommand(&m, s.advertiseFor(runtime)))
	err := s.runLoggedIn(t, dir, s.roxBin, args, func(line string) {
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
	// The guest is the name rox up printed. It's not guessed from a list of guests, which
	// could pick one that another thread was starting at the same moment.
	guest, connected := t.meta.Guest, t.meta.Connected
	t.appendLocked("rux", 0, ruxEv("rux_guest", "name", guest))
	if err != nil {
		t.appendLocked("rux", 0, ruxEv("rux_error", "text", "rox up failed: "+err.Error()))
		parent, parentAuth := t.meta.Parent, t.meta.ParentAuthority
		if !connected {
			t.setStatusLocked("failed")
		}
		t.mu.Unlock()
		if parent != "" && !connected {
			s.wakeParent(parent, parentAuth, fmt.Sprintf("%sworker %s (%q) failed to start: rox up failed: %v", wakePrefix, m.ID, m.Title, err))
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
	return s.runLoggedIn(t, s.dataDir, bin, args, onLine)
}

// runLoggedIn is runLogged with the command's working directory set.
func (s *server) runLoggedIn(t *thread, dir, bin string, args []string, onLine func(string)) error {
	shown := strings.Join(args, " ")
	t.mu.Lock()
	if tok := t.meta.Token; tok != "" {
		shown = strings.ReplaceAll(shown, tok, "…")
	}
	t.mu.Unlock()
	t.event(ruxEv("rux_setup", "text", "$ "+filepath.Base(bin)+" "+shown))
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
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
		"--repo", m.Repo, "--branch", m.Branch, "--model", s.threadModel(&m), "--effort", m.Effort, "--role", m.Kind, "--engine", m.Engine)
	if s.proxy != nil {
		cmd.Args = append(cmd.Args, "--model-proxy", "http://127.0.0.1:"+port)
	}
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

// routes builds the web app's routes and the guest listener's.
func (s *server) routes() (app, guest *http.ServeMux) {
	app = http.NewServeMux()
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
	app.HandleFunc("POST /api/guests/{name}/sleep", s.guestAction("sleep"))
	app.HandleFunc("POST /api/guests/{name}/wake", s.guestAction("wake"))
	app.HandleFunc("POST /api/guests/{name}/down", s.guestAction("down"))
	app.HandleFunc("POST /api/threads/{id}/archive", s.handleThreadArchive)
	app.HandleFunc("DELETE /api/threads/{id}", s.handleThreadDelete)
	app.HandleFunc("POST /api/projects/{id}/cleanup", s.handleProjectCleanup)
	app.HandleFunc("POST /api/projects/{id}/archive", s.handleProjectArchive)
	app.HandleFunc("GET /api/repos", s.handleRepos)
	app.HandleFunc("GET /api/fs/dirs", s.handleFsDirs)
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
	app.HandleFunc("GET /api/threads/{id}/subagents/{sub}/events", s.subAgent(s.handleEvents))
	app.HandleFunc("GET /api/threads/{id}/subagents/{sub}/events/page", s.subAgent(s.handleEventsPage))
	app.HandleFunc("GET /api/pack", s.handlePack)
	app.HandleFunc("GET /api/pack/stream", s.handlePackStream)

	guest = http.NewServeMux()
	guest.HandleFunc("GET /runner", s.handleRunner)
	guest.HandleFunc("GET /git-credential", s.handleGitCredential)
	guest.HandleFunc("POST /assets", s.handleAssetUpload)
	guest.HandleFunc("/mcp", s.handleMCP)
	guest.HandleFunc("POST /ui", s.handleUIPost)
	if s.proxy != nil {
		guest.HandleFunc("/proxy/{provider}/{path...}", s.handleProxy)
	}
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
	return app, guest
}
