package rux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// The authority is the rux daemon on a host: `rux serve`, with a pidfile, a log and a unix
// socket in the data directory (default ~/.rux), so the CLI can find it and start it.
//
//	~/.rux/authority.pid   the daemon's pid
//	~/.rux/authority.url   its HTTP base URL, written once it's listening
//	~/.rux/authority.sock  the same HTTP API on a unix socket (trusted like loopback)
//	~/.rux/authority.log   its output when started without --foreground
//	~/.rux/authority.json  optional settings: {"name": "...", "vault": "local|leader"}

// authorityConfig is ~/.rux/authority.json.
type authorityConfig struct {
	Name  string `json:"name,omitempty"`  // this authority's name in the pack (default: the host name)
	Vault string `json:"vault,omitempty"` // default vault source for threads: local or leader
}

func authorityHome() string {
	if d := os.Getenv("RUX_HOME"); d != "" {
		return d
	}
	return ruxHome()
}

func authorityPath(dir, name string) string { return filepath.Join(dir, "authority."+name) }

func loadAuthorityConfig(dir string) authorityConfig {
	var c authorityConfig
	if b, err := os.ReadFile(authorityPath(dir, "json")); err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			fmt.Fprintf(os.Stderr, "rux: ignoring %s: %v\n", authorityPath(dir, "json"), err)
		}
	}
	return c
}

// resolvePlacement checks a thread's requested placement and vault. It returns whether the
// thread runs on the host (not in a guest) and the vault source name to record ("" = the
// authority's default). Pinning to another authority needs a quorum, so only this authority's
// own name is accepted for now.
func (s *server) resolvePlacement(p authapi.Placement, vault string) (local bool, vaultName string, err error) {
	if a := strings.TrimSpace(p.Authority); a != "" && a != s.authName() {
		return false, "", fmt.Errorf("this authority (%s) isn't in a quorum with %q, so it can't place work there", s.authName(), a)
	}
	vault = strings.TrimSpace(vault)
	switch vault {
	case "", "local", "leader":
	default:
		return false, "", errors.New(`vault must be "local" or "leader"`)
	}
	return p.Local, vault, nil
}

// vaultFor is the vault source name a thread's model requests use.
func (s *server) vaultFor(m *threadMeta) string {
	if d := cmpOr(m.Vault, s.vaultDefault); d != "" {
		return d
	}
	if s.node != nil && s.node.InPack() {
		return "leader" // in a pack the vault host serves model keys
	}
	return "local"
}

// ---- the rux authority command ----

const authorityUsage = `usage: rux authority start [--foreground] [serve flags]
       rux authority stop
       rux authority status
       rux authority logs [-f]
`

func runAuthority(args []string) {
	cmd := "status"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	dir := authorityHome()
	switch cmd {
	case "start":
		foreground := false
		var rest []string
		for _, a := range args {
			if a == "--foreground" || a == "-foreground" {
				foreground = true
			} else {
				rest = append(rest, a)
			}
		}
		if foreground {
			runServe(rest)
			return
		}
		url, err := startAuthority(dir, rest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "rux:", err)
			os.Exit(1)
		}
		fmt.Println("authority running at", url)
	case "stop":
		if err := stopAuthority(dir); err != nil {
			fmt.Fprintln(os.Stderr, "rux:", err)
			os.Exit(1)
		}
	case "status":
		os.Exit(statusAuthority(dir, os.Stdout))
	case "logs":
		follow := len(args) > 0 && (args[0] == "-f" || args[0] == "--follow")
		if err := showLogs(dir, os.Stdout, follow); err != nil {
			fmt.Fprintln(os.Stderr, "rux:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, authorityUsage)
		os.Exit(2)
	}
}

// runServe's alias: `rux serve ...` is `rux authority start --foreground ...`.

func readPid(dir string) int {
	b, err := os.ReadFile(authorityPath(dir, "pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0
	}
	return pid
}

// AuthoritySocket is the path of the local authority's unix socket.
func AuthoritySocket() string { return authorityPath(authorityHome(), "sock") }

// AuthorityClient is an HTTP client that talks to the local authority over its unix socket.
// Use it with any base URL, such as http://authority.
func AuthorityClient() *http.Client { return socketClient(AuthoritySocket()) }

func socketClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", sock)
		},
	}}
}

func authorityHealthy(dir string) bool {
	c := socketClient(authorityPath(dir, "sock"))
	c.Timeout = 3 * time.Second
	resp, err := c.Get("http://authority/api/pack")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func authorityURL(dir string) string {
	b, _ := os.ReadFile(authorityPath(dir, "url"))
	return strings.TrimSpace(string(b))
}

// EnsureAuthority starts the local authority, detached, if it isn't running, waits until it
// answers, and returns its HTTP base URL (for example http://127.0.0.1:7700). A client can also
// reach it over AuthoritySocket.
func EnsureAuthority() (baseURL string, err error) {
	return startAuthority(authorityHome(), nil)
}

func startAuthority(dir string, args []string) (string, error) {
	if authorityHealthy(dir) {
		if u := authorityURL(dir); u != "" {
			return u, nil
		}
	}
	if pid := readPid(dir); pid != 0 {
		// Running, but not answering yet (starting up) or stuck.
		if url, err := waitAuthority(dir, 20*time.Second); err == nil {
			return url, nil
		}
		return "", fmt.Errorf("an authority (pid %d) is running but doesn't answer; see `rux authority logs`", pid)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	logf, err := os.OpenFile(authorityPath(dir, "log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	os.Remove(authorityPath(dir, "url"))
	cmd := exec.Command(exe, append([]string{"authority", "start", "--foreground"}, args...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), "RUX_HOME="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	url, err := waitAuthorityOr(dir, 30*time.Second, exited)
	if err != nil {
		return "", fmt.Errorf("%v; see %s", err, authorityPath(dir, "log"))
	}
	return url, nil
}

func waitAuthority(dir string, d time.Duration) (string, error) {
	return waitAuthorityOr(dir, d, nil)
}

func waitAuthorityOr(dir string, d time.Duration, exited <-chan struct{}) (string, error) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if authorityHealthy(dir) {
			if u := authorityURL(dir); u != "" {
				return u, nil
			}
		}
		select {
		case <-exited:
			return "", errors.New("the authority exited while starting")
		case <-time.After(150 * time.Millisecond):
		}
	}
	return "", errors.New("the authority didn't become healthy in time")
}

func stopAuthority(dir string) error {
	pid := readPid(dir)
	if pid == 0 {
		fmt.Println("authority isn't running")
		os.Remove(authorityPath(dir, "pid"))
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			fmt.Println("authority stopped")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("authority (pid %d) didn't stop; kill it by hand", pid)
}

func statusAuthority(dir string, w io.Writer) int {
	pid := readPid(dir)
	if pid == 0 {
		fmt.Fprintln(w, "authority: not running")
		return 1
	}
	fmt.Fprintf(w, "authority: running, pid %d, %s\n", pid, cmpOr(authorityURL(dir), "no URL yet"))
	fmt.Fprintf(w, "socket:    %s\nlog:       %s\n", authorityPath(dir, "sock"), authorityPath(dir, "log"))
	c := socketClient(authorityPath(dir, "sock"))
	c.Timeout = 5 * time.Second
	resp, err := c.Get("http://authority/api/pack")
	if err != nil {
		fmt.Fprintf(w, "health:    not answering (%v)\n", err)
		return 1
	}
	defer resp.Body.Close()
	var p authapi.Pack
	if json.NewDecoder(resp.Body).Decode(&p) != nil {
		fmt.Fprintln(w, "health:    unexpected reply")
		return 1
	}
	n := 0
	for _, a := range p.Authorities {
		n += len(a.Threads)
	}
	fmt.Fprintf(w, "pack:      leader %s, term %d, %d authorities, %d threads\n", p.Leader, p.Term, len(p.Authorities), n)
	return 0
}

func showLogs(dir string, w io.Writer, follow bool) error {
	f, err := os.Open(authorityPath(dir, "log"))
	if err != nil {
		return err
	}
	defer f.Close()
	// The last 200 lines.
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > 200 {
			lines = lines[1:]
		}
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if !follow {
		return nil
	}
	r := bufio.NewReader(f)
	for {
		l, err := r.ReadString('\n')
		if l != "" {
			fmt.Fprint(w, l)
		}
		if err != nil {
			time.Sleep(300 * time.Millisecond)
		}
	}
}

// registerAuthority records the running daemon's pid and URL, and removes them on exit.
func (s *server) registerAuthority(url string) {
	dir := s.dataDir
	s.selfURL = url
	os.WriteFile(authorityPath(dir, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	os.WriteFile(authorityPath(dir, "url"), []byte(url+"\n"), 0o600)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		for _, n := range []string{"pid", "url", "sock"} {
			os.Remove(authorityPath(dir, n))
		}
		os.Exit(0)
	}()
}

// listenUnix listens on a unix socket, replacing a stale one.
func listenUnix(path string) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another authority already listens on %s", path)
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	return ln, nil
}

type ctxKey string

const unixConnKey ctxKey = "unix"

// serveUnix serves h on a unix socket. Requests on it are marked trusted, like loopback.
func serveUnix(ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, unixConnKey, true)
	}}
	return srv.Serve(ln)
}

// threadModel is the model a thread's runner starts with: the harness engine's provider/model
// as set, else Claude Code's, defaulting to the host's.
func (s *server) threadModel(m *threadMeta) string {
	if m.Engine == "harness" {
		return m.Model
	}
	return s.effectiveModel(m.Model)
}

// resolveEngine checks a thread's engine and model. The harness engine needs an explicit
// anthropic/MODEL or openai/MODEL, the providers the model proxy serves.
func (s *server) resolveEngine(engine, model string) (string, error) {
	engine = strings.TrimSpace(engine)
	switch engine {
	case "", "claude-code":
		return "", nil
	case "harness":
		name, _, err := splitModel(model)
		if err != nil || (name != "anthropic" && name != "openai") {
			return "", errors.New("the harness engine takes a model as anthropic/MODEL or openai/MODEL")
		}
		return "harness", nil
	}
	return "", errors.New(`engine must be "claude-code" or "harness"`)
}

// spawnSystemPrompt tells a thread it can start sub-agents.
const spawnSystemPrompt = `You are a project agent: you can start sub-agents, each in its own sandboxed guest with this project's repo checked out, with the rux tools (start_thread, list_threads, send_message, read_thread, interrupt_thread, remove_thread, thread_stats). Use them to run independent work in parallel or to keep risky work out of your own checkout. Write each sub-agent's prompt so that an agent with no other context can do the task. You're told when a sub-agent finishes a turn; read its transcript with read_thread, and remove its guest once its work is pushed. Sub-agents can push branches; only the user merges.`

// maxAgentDepth limits how deep sub-agents can start sub-agents.
const maxAgentDepth = 3

// depth counts the threads above t.
func (s *server) depth(t *thread) int {
	n := 0
	for id := t.meta.Parent; id != "" && n < 16; n++ {
		p := s.get(id)
		if p == nil {
			break
		}
		id = p.meta.Parent
	}
	return n
}

// inScope reports whether owner's MCP tools may act on t. A project's manager manages every
// worker in the project; any other thread manages the sub-agents it started.
func (s *server) inScope(owner, t *thread) bool {
	if t == owner {
		return false
	}
	if owner.meta.Kind == "manager" {
		return t.meta.Project == owner.meta.Project && t.meta.Kind == ""
	}
	return t.meta.Parent == owner.meta.ID
}
