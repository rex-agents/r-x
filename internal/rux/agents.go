package rux

// rux agent: sub-agents for an orchestrating Claude Code or Codex session in Rex.
//
// The orchestrator is whatever agent the user is already talking to, with the
// rux skill (rux skill install). It runs these commands; rux keeps no daemon.
// Each sub-agent is a Rox guest in a Rex pane beside the orchestrator: the
// pane runs `rux agent pane`, which starts the guest with rox up and then
// becomes `rox shell` into it, while a detached `rux agent type` types the
// agent CLI's command in. From then on rux only drives panes through Rex
// (send, capture, program_status); it never parses the agent's output.
//
// State is one JSON file per agent in ~/.rux/agents/<rex session>/.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

type subAgent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Agent     string    `json:"agent"` // claude or codex
	Prompt    string    `json:"prompt"`
	Repo      string    `json:"repo,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Push      string    `json:"push_branch,omitempty"`
	Host      string    `json:"host,omitempty"` // Rex host that runs the guest ("" = this machine)
	GuestType string    `json:"guest_type"`
	Shared    bool      `json:"shared_guest,omitempty"` // runs in an existing guest, which stop leaves alone
	Project   string    `json:"project,omitempty"`      // the checkout's project name in the guest
	Memory    string    `json:"memory,omitempty"`
	Model     string    `json:"model,omitempty"`
	Perm      string    `json:"permission_mode,omitempty"`
	Session   string    `json:"session"`
	Parent    string    `json:"parent_block,omitempty"` // the orchestrator's pane
	Block     string    `json:"block,omitempty"`
	Guest     string    `json:"guest,omitempty"`
	Pane      int       `json:"pane_pid,omitempty"`  // the pane process, so stop can wait for it
	Cleanup   bool      `json:"cleanup,omitempty"`   // stop asked to remove the guest
	StopDone  bool      `json:"stop_done,omitempty"` // stop has finished, so the pane cleans up after itself
	Phase     string    `json:"phase"`               // provisioning, connecting, running, failed, stopped, exited
	Error     string    `json:"error,omitempty"`
	Created   time.Time `json:"created"`
	Started   time.Time `json:"started,omitempty"`
	Input     time.Time `json:"last_input,omitempty"` // last rux agent send

	path     string
	state    string // live: working, idle, blocked, done, error; "" if the pane reports none
	activity string
}

func agentsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rux", "agents")
}

func sessionDir(sess string) string {
	return filepath.Join(agentsRoot(), strings.NewReplacer(":", "_", "/", "_").Replace(sess))
}

func loadAgent(path string) (*subAgent, error) {
	var a subAgent
	if err := loadJSON(path, &a); err != nil {
		return nil, err
	}
	a.path = path
	return &a, nil
}

func (a *subAgent) save() error { return saveJSON(a.path, a) }

// update re-reads the agent's file, applies f, and saves it, so writers in
// different processes don't drop each other's fields.
func (a *subAgent) update(f func(*subAgent)) {
	if cur, err := loadAgent(a.path); err == nil {
		*a = *cur
	}
	f(a)
	a.save()
}

func listAgents(sess string) []*subAgent {
	dir := sessionDir(sess)
	ents, _ := os.ReadDir(dir)
	var out []*subAgent
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			if a, err := loadAgent(filepath.Join(dir, e.Name())); err == nil {
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// panelessSession is the session of sub-agents started outside a Rex terminal (REX_SESSION unset).
const panelessSession = "headless"

func paneless(a *subAgent) bool { return a.Session == panelessSession }

// logPath is where a headless agent writes what its pane would show.
func (a *subAgent) logPath() string { return strings.TrimSuffix(a.path, ".json") + ".log" }

func currentSession(flagVal string) string {
	if s := cmpOr(flagVal, os.Getenv("REX_SESSION")); s != "" {
		return s
	}
	// Outside Rex, sub-agents run headless: no pane, output in a log that rux agent read prints.
	return panelessSession
}

func findAgent(sess, id string) *subAgent {
	for _, a := range listAgents(sess) {
		if a.ID == id || a.Name == id {
			return a
		}
	}
	fail("rux agent: no agent %q in this Rex session (rux agent ls lists them)", id)
	return nil
}

func runAgentCmd(args []string) {
	const usage = `usage: rux agent COMMAND
  spawn --name NAME [flags] PROMPT   start a sub-agent in a new Rox guest, in a pane beside this one
  ls [--json] [--watch]              list this session's sub-agents and their states
  read ID [-n LINES]                 print the end of a sub-agent's screen
  send ID [TEXT] [--key KEY]         type into a sub-agent (TEXT is submitted with Enter; - reads stdin)
  wait [ID...] [--any] [--timeout D] wait until sub-agents stop working
  focus ID                           focus a sub-agent's pane
  stop ID [--keep-guest]             close its pane and remove its guest`
	if len(args) == 0 {
		usageExit(usage)
	}
	rest := args[1:]
	switch args[0] {
	case "spawn":
		agentSpawn(rest)
	case "ls", "list":
		agentLs(rest)
	case "read":
		agentRead(rest)
	case "send":
		agentSend(rest)
	case "wait":
		agentWait(rest)
	case "focus":
		agentFocus(rest)
	case "stop":
		agentStop(rest)
	case "pane":
		agentPane(rest)
	case "type":
		agentType(rest)
	default:
		usageExit(usage)
	}
}

// parseInterleaved lets flags follow positional arguments (rux agent read ID -n 40).
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		if args[0] == "--" {
			return append(pos, args[1:]...)
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// ---- spawn ----

func agentSpawn(args []string) {
	fs := flag.NewFlagSet("rux agent spawn", flag.ExitOnError)
	name := fs.String("name", "", "short name, shown on the pane and used as the id (e.g. api-tests)")
	repo := fs.String("repo", "", "repo to clone: owner/name or https URL (default: this checkout's origin; none for an empty guest)")
	branch := fs.String("branch", "", "branch to start from (default: the repo's default)")
	push := fs.String("push", "", "branch the agent pushes its work to (default: rux/ID)")
	host := fs.String("host", "", "Rex host that runs the guest (default: this machine)")
	agent := fs.String("agent", "claude", "agent CLI in the guest: claude or codex")
	model := fs.String("model", "", "model for the agent CLI")
	memory := fs.String("memory", "", "guest memory, such as 8G (default: 4G for a container, rox's for other runtimes)")
	guestType := fs.String("guest-type", "container", "rox runtime for a new guest: container, linux, macos, fly or railway")
	existing := fs.String("guest", "", "run in this existing rox guest instead of a new one (stop leaves it running)")
	perm := fs.String("permission-mode", "", "claude --permission-mode (default: skip permission prompts, since the guest is a sandbox)")
	promptFile := fs.String("prompt-file", "", "read the prompt from this file (- for stdin)")
	session := fs.String("session", "", "Rex session (default: $REX_SESSION)")
	parent := fs.String("beside", "", "pane to open beside (default: $REX_BLOCK)")
	pos := parseInterleaved(fs, args)
	prompt := strings.TrimSpace(strings.Join(pos, " "))
	if *promptFile != "" {
		var b []byte
		var err error
		if *promptFile == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(*promptFile)
		}
		if err != nil {
			fail("rux agent spawn: %v", err)
		}
		prompt = strings.TrimSpace(string(b))
	}
	if prompt == "" || *name == "" {
		usageExit("usage: rux agent spawn --name NAME [flags] PROMPT  (rux agent spawn -h for flags)")
	}
	if *agent != "claude" && *agent != "codex" {
		fail("rux agent spawn: --agent must be claude or codex")
	}
	sess := currentSession(*session)
	beside := cmpOr(*parent, os.Getenv("REX_BLOCK"))

	a := &subAgent{Name: *name, Agent: *agent, Prompt: prompt, Branch: *branch, Host: *host, GuestType: *guestType,
		Memory: *memory, Model: *model, Perm: *perm, Session: sess, Parent: beside, Phase: "provisioning", Created: time.Now()}
	if *existing != "" {
		if !guestNameOK.MatchString(*existing) {
			fail("rux agent spawn: --guest: invalid guest name %q", *existing)
		}
		a.Guest, a.Shared, a.GuestType = *existing, true, ""
	}
	switch *repo {
	case "none":
	case "":
		if out, err := exec.Command("git", "remote", "get-url", "origin").Output(); err == nil {
			if u, err := normalizeRepo(strings.TrimSpace(string(out))); err == nil {
				a.Repo = sshRepo(u)
			}
		}
	default:
		u, err := normalizeRepo(*repo)
		if err != nil {
			fail("rux agent spawn: --repo: %v", err)
		}
		a.Repo = sshRepo(u)
	}
	if a.Repo == "" {
		a.Branch = ""
	}
	dir := sessionDir(sess)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail("rux agent spawn: %v", err)
	}
	a.ID = cmpOr(slug(*name), "agent")
	// Claim the ID with an exclusive create, so parallel spawns can't take the same one.
	for n, base := 2, a.ID; ; n++ {
		f, err := os.OpenFile(filepath.Join(dir, a.ID+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !errors.Is(err, os.ErrExist) {
			fail("rux agent spawn: %v", err)
		}
		a.ID = fmt.Sprintf("%s-%d", base, n)
	}
	if a.Repo != "" {
		a.Push = cmpOr(*push, "rux/"+a.ID)
		a.Project = projectName(a.Repo)
		if a.Shared {
			// Other work may have this project in the guest, so the agent gets its own checkout.
			a.Project += "-" + a.ID
		}
	}
	a.path = filepath.Join(dir, a.ID+".json")
	if err := a.save(); err != nil {
		fail("rux agent spawn: %v", err)
	}
	if paneless(a) {
		spawnPaneless(a)
		return
	}

	// Beside the orchestrator first, then stacked under the newest live agent.
	anchor, split, ratio := beside, "right", "55"
	live := listAgents(sess)
	for i := len(live) - 1; i >= 0; i-- {
		if b := live[i]; b.ID != a.ID && b.Block != "" && b.Phase != "stopped" && b.Phase != "exited" && rexBlockAlive(sess, b.Block) {
			anchor, split, ratio = b.Block, "below", "50"
			break
		}
	}
	home, _ := os.UserHomeDir()
	rexArgs := []string{"split", "-s", sess}
	if anchor != "" {
		rexArgs = append(rexArgs, "-b", anchor)
	}
	rexArgs = append(rexArgs, "--split="+split, "--ratio", ratio, "--label", "agent "+a.Name, "--keep-open", "--focus=false",
		"-c", home, "--json", "--", selfExe(), "agent", "pane", a.path)
	var out struct {
		BlockIDs []string `json:"block_ids"`
	}
	err := rexJSON(&out, rexArgs...)
	if err == nil && len(out.BlockIDs) == 0 {
		err = errors.New("rex returned no block")
	}
	if err != nil {
		a.update(func(a *subAgent) { a.Phase, a.Error = "failed", "opening its pane: "+err.Error() })
		fail("rux agent spawn: opening a pane: %v", err)
	}
	a.update(func(a *subAgent) { a.Block = out.BlockIDs[0] })
	if a.Phase == "stopped" {
		// Stopped while its pane was opening; the stop couldn't see the pane yet.
		rexOut("block", "close", "-s", a.Session, a.Block)
		fail("rux agent spawn: %s was stopped while it started", a.ID)
	}
	where := "this machine"
	if a.Host != "" {
		where = a.Host
	}
	if a.Shared {
		fmt.Printf("spawned %s: %s in the existing guest %s on %s", a.ID, a.Agent, a.Guest, where)
	} else {
		fmt.Printf("spawned %s: %s in a new %s guest on %s", a.ID, a.Agent, a.GuestType, where)
	}
	if a.Repo != "" {
		fmt.Printf(", cloning %s", repoDisplayName(a.Repo))
		if a.Branch != "" {
			fmt.Printf(" @ %s", a.Branch)
		}
		fmt.Printf(", pushing %s", a.Push)
	}
	fmt.Printf("\npane %s. The guest takes a minute or two; follow it with: rux agent wait %s\n", a.Block, a.ID)
}

// ---- the pane: provision a guest, then become a shell in it ----

func agentPane(args []string) {
	if len(args) != 1 {
		usageExit("usage: rux agent pane STATE_FILE (internal)")
	}
	a, err := loadAgent(args[0])
	if err != nil {
		fail("rux: %v", err)
	}
	failed := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		a.update(func(a *subAgent) {
			if a.Phase == "stopped" {
				return // a stop is sticky; it owns the report and the cleanup
			}
			if a.Guest != "" && !a.Shared {
				msg += fmt.Sprintf(" (guest %s was kept; rux agent stop %s removes it)", a.Guest, a.ID)
			}
			a.Phase, a.Error = "failed", msg
		})
		fmt.Fprintln(os.Stderr, "\nrux: "+msg)
		os.Exit(1)
	}
	rox, err := exec.LookPath("rox")
	if err != nil {
		failed("rox isn't on PATH")
	}
	// A stop or a closed pane cancels rox add, but not rox up: an up that has begun owns
	// a guest only its launch line names, so it finishes and the result is recorded.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGPIPE)
	defer cancel()
	stopped := false
	a.update(func(a *subAgent) {
		stopped = a.Phase == "stopped"
		if !stopped {
			a.Pane = os.Getpid()
		}
	})
	if stopped {
		os.Exit(0)
	}
	var guest string
	if a.Shared {
		guest = a.Guest
		fmt.Printf("\x1b[1mrux agent %s\x1b[0m: %s in the existing guest %s\n", a.Name, a.Agent, guest)
		if a.Repo != "" {
			// rox add clones the repo and installs the agent CLI, which rix signs in.
			add := []string{"add", guest, "--repo", a.Repo, "--project", a.Project, "--agents", a.Agent}
			if a.Host != "" {
				add = append(add, "-S", a.Host)
			}
			if a.Branch != "" {
				add = append(add, "--branch", a.Branch)
			}
			if _, err := runShown(ctx, rox, add, nil); err != nil {
				failed("rox add failed: %v", err)
			}
		} else {
			check := []string{"exec", guest, "--", "sh", "-lc", "command -v " + a.Agent}
			if out, err := exec.CommandContext(ctx, rox, check...).CombinedOutput(); err != nil {
				failed("%s has no %s (%s). Pick a guest started with it, or a new guest", guest, a.Agent, lastLine(string(out)))
			}
		}
	} else {
		fmt.Printf("\x1b[1mrux agent %s\x1b[0m: starting a %s guest for %s", a.Name, a.GuestType, a.Agent)
		if a.Host != "" {
			fmt.Printf(" on %s", a.Host)
		}
		fmt.Println()
		up := []string{"up", a.GuestType}
		if a.Host != "" {
			up = append(up, "-S", a.Host)
		}
		if a.Repo != "" {
			up = append(up, "--repo", a.Repo, "--project", cmpOr(a.Project, projectName(a.Repo)))
			if a.Branch != "" {
				up = append(up, "--branch", a.Branch)
			}
		} else {
			up = append(up, "--no-project")
		}
		// An agent and a project's installs need more than Apple container's 1G default.
		if a.Memory == "" && a.GuestType == "container" {
			a.Memory = "4G"
		}
		if a.Memory != "" {
			up = append(up, "--memory", a.Memory)
		}
		// rox installs the agent's CLI, and rix signs it in through rox's hooks.
		up = append(up, "--agents", a.Agent)
		if !remoteRuntime(a.GuestType) {
			up = append(up, "--no-tailscale")
		}
		// The guest is recorded as soon as rox names it, so a failure or a stop
		// afterwards can still remove it. Only a name this rox up printed counts.
		guest, err = runShown(context.WithoutCancel(ctx), rox, up, func(name string) {
			a.update(func(a *subAgent) {
				if a.Guest == "" {
					a.Guest = name
				}
			})
		})
		if err != nil {
			failed("rox up failed: %v", err)
		}
		if guest == "" {
			failed("rox up finished but didn't say which guest it started; check rox ls before starting again")
		}
	}
	stopped = false
	a.update(func(a *subAgent) {
		stopped = a.Phase == "stopped"
		if !stopped {
			a.Guest, a.Phase = guest, "connecting"
		}
	})
	if stopped {
		// Stopped while the guest was coming up. This pane started it and knows its name,
		// so it removes the guest itself, even if stop stopped waiting.
		if a.Cleanup && !a.Shared {
			if err := removeGuest(a); err != nil {
				fmt.Fprintln(os.Stderr, "rux: "+err.Error())
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	if ctx.Err() != nil {
		failed("the pane was closed while it started")
	}
	if paneless(a) {
		runPaneless(ctx, a, rox, guest)
		return
	}
	// Type the agent's command in once the guest's shell is up. Detached, since
	// this process becomes rox shell.
	typer := exec.Command(selfExe(), "agent", "type", a.path)
	typer.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := typer.Start(); err != nil {
		failed("starting the typer: %v", err)
	}
	typer.Process.Release()
	fmt.Printf("\n\x1b[1mrux\x1b[0m: guest %s is up; connecting, then starting %s\n\n", guest, a.Agent)
	argv := []string{"rox", "shell"}
	if a.Host != "" {
		argv = append(argv, "-S", a.Host)
	}
	argv = append(argv, guest)
	if a.Project != "" {
		argv = append(argv, a.Project)
	}
	// From here the pane is rox shell, which a stop can close without waiting.
	a.update(func(a *subAgent) { a.Pane = 0 })
	if err := syscall.Exec(rox, argv, os.Environ()); err != nil {
		failed("rox shell: %v", err)
	}
}

var promptRe = regexp.MustCompile(`[$#%>❯]\s*$`)

func agentType(args []string) {
	if len(args) != 1 {
		usageExit("usage: rux agent type STATE_FILE (internal)")
	}
	a, err := loadAgent(args[0])
	if err != nil {
		os.Exit(1)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := rexOut("capture", "-s", a.Session, "-b", a.Block, "--trim"); err == nil {
			if last := lastLine(out); promptRe.MatchString(last) && !strings.Contains(last, "rux") {
				break
			}
		}
		time.Sleep(time.Second)
	}
	err = rexSendLine(a.Session, a.Block, agentCommand(a))
	if err == nil {
		acceptStartupDialogs(a)
	}
	a.update(func(a *subAgent) {
		if err != nil {
			a.Phase, a.Error = "failed", "typing the agent command: "+err.Error()
		} else if a.Phase == "connecting" {
			a.Phase, a.Started = "running", time.Now()
		}
	})
}

// startupDialogs are the agent CLI's first-run questions about the folder and
// permissions. The guest is a fresh sandbox rux made for this agent, so rux
// answers yes (the second choice) rather than leave the agent stuck.
var startupDialogs = []string{"Yes, I trust this folder", "Yes, I accept", "Yes, allow Codex to work in this folder"}

func acceptStartupDialogs(a *subAgent) {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		out, err := rexOut("capture", "-s", a.Session, "-b", a.Block, "--trim")
		if err != nil {
			return
		}
		screen := tailLines(out, 30)
		answered := false
		for _, d := range startupDialogs {
			if strings.Contains(screen, d) {
				rexOut("send-key", "-s", a.Session, "-b", a.Block, "down")
				time.Sleep(200 * time.Millisecond)
				rexOut("send-key", "-s", a.Session, "-b", a.Block, "enter")
				answered = true
				break
			}
		}
		if answered {
			continue
		}
		// Past its startup screens: claude's input footer, or (for other CLIs) a
		// status report a while after starting.
		for _, m := range []string{"bypass permissions on", "for shortcuts", "esc to interrupt"} {
			if strings.Contains(screen, m) {
				return
			}
		}
		if time.Until(deadline) < 35*time.Second {
			if recs, err := rexProgramStatus(a.Session, a.Block); err == nil && len(recs) > 0 {
				return
			}
		}
	}
}

// agentCommand is the shell line that starts the agent CLI in the guest. The
// texts go in base64 so no quoting can break them.
func agentCommand(a *subAgent) string {
	brief := fmt.Sprintf("You are rux sub-agent %q. An orchestrating agent started you and reads your terminal. "+
		"Work on your task without asking questions unless you're truly blocked. ", a.Name)
	if a.Push != "" {
		brief += fmt.Sprintf("Commit your work and push it to a new branch named %s (never the default branch). ", a.Push)
	}
	brief += "When you finish, end with a short summary that starts with DONE:. If you can't continue, start your last message with BLOCKED: and say what you need."
	b64 := func(s string) string {
		return fmt.Sprintf(`"$(printf %%s %s | base64 -d)"`, base64.StdEncoding.EncodeToString([]byte(s)))
	}
	if a.Agent == "codex" {
		cmd := "codex --dangerously-bypass-approvals-and-sandbox"
		if a.Model != "" {
			cmd += " -m " + shQuote(a.Model)
		}
		return cmd + " " + b64(brief+"\n\nYour task:\n\n"+a.Prompt)
	}
	cmd := "claude"
	if a.Perm == "" {
		cmd += " --dangerously-skip-permissions"
	} else {
		cmd += " --permission-mode " + shQuote(a.Perm)
	}
	if a.Model != "" {
		cmd += " --model " + shQuote(a.Model)
	}
	return cmd + " --append-system-prompt " + b64(brief) + " " + b64(a.Prompt)
}

// ---- live state ----

type programRecord struct {
	State        string `json:"state"`
	App          string `json:"app"`
	EffectiveApp string `json:"effective_app"`
	Kind         string `json:"kind"`
	Msg          string `json:"msg"`
	UpdatedAt    string `json:"updated_at"`
}

func rexProgramStatus(sess, block string) ([]programRecord, error) {
	var out struct {
		Records []programRecord `json:"records"`
	}
	err := rexJSON(&out, "block", "call", "-s", sess, "-b", block, "program_status")
	return out.Records, err
}

func rexBlockAlive(sess, block string) bool {
	_, err := rexProgramStatus(sess, block)
	return err == nil
}

// refresh reads the pane's program status. A closed pane marks the agent exited.
func (a *subAgent) refresh() {
	a.state, a.activity = "", ""
	if a.Block == "" || a.Phase == "stopped" || a.Phase == "exited" || a.Phase == "failed" {
		return
	}
	recs, err := rexProgramStatus(a.Session, a.Block)
	if err != nil {
		if msg := strings.ToLower(err.Error()); strings.Contains(msg, "not found") {
			a.update(func(a *subAgent) {
				if a.Phase != "stopped" {
					a.Phase = "exited"
				}
			})
		}
		return
	}
	// Prefer the agent CLI's own report, then the newest.
	isAgent := func(r *programRecord) bool {
		app := cmpOr(r.EffectiveApp, r.App)
		return app == "claude-code" || strings.Contains(app, "codex")
	}
	var rec *programRecord
	for i := range recs {
		r := &recs[i]
		if rec == nil || (isAgent(r) && !isAgent(rec)) || (isAgent(r) == isAgent(rec) && r.UpdatedAt > rec.UpdatedAt) {
			rec = r
		}
	}
	if rec != nil && a.Phase == "running" {
		a.state, a.activity = rec.State, rec.Msg
		if rec.State == "blocked" && rec.Kind != "" {
			a.state = "blocked:" + rec.Kind
		}
	}
}

func (a *subAgent) display() string {
	if a.Phase != "running" {
		return a.Phase
	}
	return cmpOr(a.state, "running")
}

func printTable(w io.Writer, agents []*subAgent) {
	if len(agents) == 0 {
		fmt.Fprintln(w, "No sub-agents in this Rex session.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tAGENT\tSTATE\tHOST\tGUEST\tAGE\tPUSHES\tACTIVITY")
	for _, a := range agents {
		host := cmpOr(a.Host, "local")
		act := a.activity
		if a.Error != "" {
			act = a.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.Agent, a.display(), host, cmpOr(a.Guest, "-"),
			ageOf(cmpTime(a.Started, a.Created)), cmpOr(a.Push, "-"), clipText(act, 60))
	}
	tw.Flush()
}

func ageOf(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func cmpTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	return a
}

// ---- ls, read, send, focus, stop ----

func agentLs(args []string) {
	fs := flag.NewFlagSet("rux agent ls", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	watch := fs.Bool("watch", false, "refresh every 2 seconds (for a monitor pane or tab)")
	session := fs.String("session", "", "Rex session (default: $REX_SESSION)")
	fs.Parse(args)
	sess := currentSession(*session)
	for {
		agents := listAgents(sess)
		for _, a := range agents {
			a.refresh()
		}
		if *asJSON {
			type row struct {
				*subAgent
				State    string `json:"state"`
				Activity string `json:"activity,omitempty"`
			}
			rows := []row{}
			for _, a := range agents {
				rows = append(rows, row{a, a.display(), a.activity})
			}
			b, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(b))
			return
		}
		if !*watch {
			printTable(os.Stdout, agents)
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "\x1b[H\x1b[2J\x1b[1mrux agents\x1b[0m  %s  \x1b[2m(%s)\x1b[0m\n\n", time.Now().Format("15:04:05"), sess)
		printTable(&b, agents)
		os.Stdout.WriteString(b.String())
		time.Sleep(2 * time.Second)
	}
}

func agentRead(args []string) {
	fs := flag.NewFlagSet("rux agent read", flag.ExitOnError)
	n := fs.Int("n", 80, "lines from the end")
	session := fs.String("session", "", "Rex session (default: $REX_SESSION)")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		usageExit("usage: rux agent read ID [-n LINES]")
	}
	a := findAgent(currentSession(*session), pos[0])
	if a.Block == "" {
		if paneless(a) {
			fmt.Printf("[%s: %s]\n%s\n", a.ID, a.Phase, tailLines(panelessLog(a), *n))
			return
		}
		fail("rux agent read: %s has no pane", a.ID)
	}
	a.refresh()
	out, err := rexOut("capture", "-s", a.Session, "-b", a.Block, "--trim")
	if err != nil {
		fail("rux agent read: %v", err)
	}
	fmt.Printf("[%s: %s]\n%s\n", a.ID, a.display(), tailLines(out, *n))
}

func agentSend(args []string) {
	fs := flag.NewFlagSet("rux agent send", flag.ExitOnError)
	key := fs.String("key", "", "press this key after the text instead of Enter (enter, escape, up, down, tab, ctrl+c, 1…)")
	noSubmit := fs.Bool("no-submit", false, "don't press Enter after the text")
	session := fs.String("session", "", "Rex session (default: $REX_SESSION)")
	pos := parseInterleaved(fs, args)
	if len(pos) < 1 {
		usageExit("usage: rux agent send ID [TEXT | -] [--key KEY] [--no-submit]")
	}
	a := findAgent(currentSession(*session), pos[0])
	text := strings.Join(pos[1:], " ")
	if text == "-" {
		b, _ := io.ReadAll(os.Stdin)
		text = strings.TrimRight(string(b), "\n")
	}
	if text == "" && *key == "" {
		fail("rux agent send: give TEXT or --key")
	}
	if a.Block == "" {
		fail("rux agent send: %s has no pane", a.ID)
	}
	if text != "" {
		if strings.Contains(text, "\n") {
			text = "\x1b[200~" + text + "\x1b[201~" // bracketed paste, so newlines don't submit
		}
		if _, err := rexOut("send", "-s", a.Session, "-b", a.Block, text); err != nil {
			fail("rux agent send: %v", err)
		}
	}
	k := *key
	if k == "" && text != "" && !*noSubmit {
		k = "enter"
		time.Sleep(150 * time.Millisecond)
	}
	if k != "" {
		if _, err := rexOut("send-key", "-s", a.Session, "-b", a.Block, k); err != nil {
			fail("rux agent send: %v", err)
		}
	}
	a.update(func(a *subAgent) { a.Input = time.Now() })
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func agentFocus(args []string) {
	fs := flag.NewFlagSet("rux agent focus", flag.ExitOnError)
	session := fs.String("session", "", "Rex session (default: $REX_SESSION)")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		usageExit("usage: rux agent focus ID")
	}
	a := findAgent(currentSession(*session), pos[0])
	if _, err := rexOut("focus", "-s", a.Session, a.Block); err != nil {
		fail("rux agent focus: %v", err)
	}
}

func agentStop(args []string) {
	fs := flag.NewFlagSet("rux agent stop", flag.ExitOnError)
	keep := fs.Bool("keep-guest", false, "leave the guest running (rox shell GUEST to get back in)")
	session := fs.String("session", "", "Rex session (default: $REX_SESSION)")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		usageExit("usage: rux agent stop ID [--keep-guest]")
	}
	a := findAgent(currentSession(*session), pos[0])
	// Mark it stopped first, so a pane that's still provisioning can't move it on.
	a.update(func(a *subAgent) {
		a.Phase, a.Cleanup, a.StopDone = "stopped", !*keep, false
	})
	// A pane that's running rox up isn't closed yet: it finishes the launch, records
	// the guest, sees the stop and exits. Closing it sooner could lose the guest's name.
	// (Everything else, including rox add in a shared guest, ends with the pane.)
	launching := a.Pane > 0 && !a.Shared
	if a.Block != "" && !launching {
		rexOut("block", "close", "-s", a.Session, a.Block)
	}
	if paneless(a) && a.Pane > 0 && !launching {
		// No pane to close: end the process that runs the agent.
		_ = syscall.Kill(a.Pane, syscall.SIGTERM)
	}
	if a.Pane > 0 {
		if launching {
			fmt.Printf("%s is still starting its guest; waiting for rox to finish so the guest can be removed\n", a.ID)
		}
		deadline := time.Now().Add(30 * time.Second)
		if launching {
			deadline = time.Now().Add(launchWait)
		}
		for paneAlive(a.Pane) && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if paneAlive(a.Pane) && launching {
			// The pane started this guest and knows its name: it removes the guest itself
			// when rox up returns, so a slow launch needs no second stop.
			fmt.Printf("stopped %s; rox is still starting its guest, and the pane removes it when rox finishes\n", a.ID)
			return
		}
		if paneAlive(a.Pane) {
			fail("rux agent stop: it is still setting up the guest; run rux agent stop %s again later", a.ID)
		}
		if launching && a.Block != "" {
			rexOut("block", "close", "-s", a.Session, a.Block)
		}
	}
	if cur, err := loadAgent(a.path); err == nil {
		*a = *cur
	}
	if *keep || a.Guest == "" || a.Shared {
		a.update(func(a *subAgent) { a.StopDone = true })
		if a.Shared {
			fmt.Printf("stopped %s; %s keeps running, since it was there before the agent\n", a.ID, a.Guest)
		} else if a.Guest != "" {
			fmt.Printf("stopped %s; guest %s keeps running\n", a.ID, a.Guest)
		} else {
			fmt.Printf("stopped %s\n", a.ID)
		}
		return
	}
	guest := a.Guest
	if err := removeGuest(a); err != nil {
		fail("rux agent stop: closed the pane, but %v\nrun rux agent stop %s again to retry", err, a.ID)
	}
	a.update(func(a *subAgent) { a.StopDone = true })
	fmt.Printf("stopped %s and removed guest %s\n", a.ID, guest)
}

// launchWait is how long stop waits for a pane's rox up before leaving the cleanup to the pane.
var launchWait = 10 * time.Minute

func paneAlive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// ---- wait ----

func agentWait(args []string) {
	fs := flag.NewFlagSet("rux agent wait", flag.ExitOnError)
	anyOne := fs.Bool("any", false, "return when any one of them stops working")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up after this long (then run it again)")
	lines := fs.Int("n", 25, "screen lines to print for each agent that stopped")
	session := fs.String("session", "", "Rex session (default: $REX_SESSION)")
	ids := parseInterleaved(fs, args)
	sess := currentSession(*session)
	all := listAgents(sess)
	var watch []*subAgent
	if len(ids) == 0 {
		for _, a := range all {
			if a.Phase == "provisioning" || a.Phase == "connecting" || a.Phase == "running" {
				watch = append(watch, a)
			}
		}
	} else {
		for _, id := range ids {
			watch = append(watch, findAgent(sess, id))
		}
	}
	if len(watch) == 0 {
		fmt.Println("No sub-agents are running.")
		return
	}
	type track struct {
		hash       uint64
		lastChange time.Time
		sawWorking bool
	}
	tr := map[string]*track{}
	busy := func(a *subAgent) bool {
		t := tr[a.ID]
		if t == nil {
			t = &track{lastChange: time.Now()}
			tr[a.ID] = t
		}
		if cur, err := loadAgent(a.path); err == nil {
			*a = *cur
		}
		a.refresh()
		if paneless(a) {
			return a.Phase == "provisioning" || a.Phase == "connecting" || a.Phase == "running"
		}
		switch a.Phase {
		case "provisioning", "connecting":
			return true
		case "running":
		default:
			return false
		}
		// Right after it starts or gets input, it may still show the last state.
		sinceStart := time.Since(cmpTime(laterOf(a.Input, a.Started), a.Created))
		switch base, _, _ := strings.Cut(a.state, ":"); base {
		case "working":
			t.sawWorking = true
			return true
		case "idle", "done", "blocked", "error":
			// It reports idle while it starts up; give it a moment to take the prompt.
			return !t.sawWorking && sinceStart < 20*time.Second
		}
		// No program status from this pane: go by whether the screen changes.
		if out, err := rexOut("capture", "-s", a.Session, "-b", a.Block, "--trim"); err == nil {
			h := fnv.New64a()
			io.WriteString(h, tailLines(out, 60))
			if s := h.Sum64(); s != t.hash {
				t.hash, t.lastChange = s, time.Now()
			}
			a.activity = lastLine(out)
		}
		if time.Since(t.lastChange) < 15*time.Second || sinceStart < 20*time.Second {
			return true
		}
		a.state = "quiet"
		return false
	}
	deadline := time.Now().Add(*timeout)
	timedOut := false
	for {
		n := 0
		for _, a := range watch {
			if busy(a) {
				n++
			}
		}
		if n == 0 || (*anyOne && n < len(watch)) {
			break
		}
		if time.Now().After(deadline) {
			timedOut = true
			break
		}
		time.Sleep(3 * time.Second)
	}
	if timedOut {
		fmt.Println("Timed out; some agents are still working. Run rux agent wait again to keep waiting.")
		fmt.Println()
	}
	printTable(os.Stdout, watch)
	for _, a := range watch {
		if paneless(a) {
			fmt.Printf("\n--- %s (%s), last lines ---\n%s\n", a.ID, a.Phase, tailLines(panelessLog(a), *lines))
			continue
		}
		if a.Phase == "running" && a.state != "working" && a.Block != "" {
			if out, err := rexOut("capture", "-s", a.Session, "-b", a.Block, "--trim"); err == nil {
				fmt.Printf("\n--- %s (%s), last lines ---\n%s\n", a.ID, a.display(), tailLines(out, *lines))
			}
		}
	}
}

// ---- rex and small helpers ----

func rexOut(args ...string) (string, error) {
	cmd := exec.Command("rex", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), errors.New(msg)
		}
		return string(out), err
	}
	return string(out), nil
}

func rexJSON(v any, args ...string) error {
	out, err := rexOut(args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), v)
}

func rexSendLine(sess, block, text string) error {
	if _, err := rexOut("send", "-s", sess, "-b", block, text); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	_, err := rexOut("send-key", "-s", sess, "-b", block, "enter")
	return err
}

func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "rux"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		return r
	}
	return exe
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n "), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// runShown runs a rox command with its output in this pane, and returns the guest
// rox up or rox add said it started, if it did. onGuest is called when it first does.
func runShown(ctx context.Context, rox string, args []string, onGuest func(string)) (string, error) {
	fmt.Printf("\x1b[2m$ rox %s\x1b[0m\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, rox, args...)
	cmd.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr, cmd.Stdin = pw, pw, os.Stdin
	guest := ""
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			line := sc.Text()
			fmt.Println(line)
			if name := launchedGuest(line); guest == "" && name != "" {
				guest = name
				if onGuest != nil {
					onGuest(name)
				}
			}
		}
	}()
	err := cmd.Run()
	pw.Close()
	<-done
	return guest, err
}

// launchedLineRe matches the lines rox up prints about the guest it is starting: its
// "Starting NAME" and "Started NAME" steps, the "Workspace NAME" card and its
// "Workspace NAME was kept" warning, and a remote launch's "Registered Rex host NAME".
// Other lines can mention other guests (a reused one, a listing), so they don't count.
var launchedLineRe = regexp.MustCompile(`^\W*(?:Starting|Started|Workspace|Registered Rex host)\s+(rv-[a-z0-9][a-z0-9-]*[0-9a-f]{8,})(?:\s|$)`)

// launchedGuest is the guest a line of rox up output says it is starting, or "".
func launchedGuest(line string) string {
	m := launchedLineRe.FindStringSubmatch(strings.TrimSpace(ansiRe.ReplaceAllString(line, "")))
	if m == nil {
		return ""
	}
	return m[1]
}

// removeGuest runs rox down for the agent's own guest and, once it's gone, forgets it.
// On failure the guest stays recorded so a stop can be retried.
func removeGuest(a *subAgent) error {
	cur, err := loadAgent(a.path)
	if err != nil {
		return err
	}
	if cur.Guest == "" || cur.Shared {
		return nil
	}
	down := []string{"down"}
	if cur.Host != "" {
		down = append(down, "-S", cur.Host)
	}
	out, err := exec.Command("rox", append(down, cur.Guest)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rox down %s failed: %v\n%s", cur.Guest, err, strings.TrimSpace(string(out)))
	}
	a.update(func(a *subAgent) { a.Guest = "" })
	return nil
}

// sshRepo turns a GitHub HTTPS URL into its SSH form. Git in a guest signs in with the
// user's SSH key through rix's Git agent, which rox forwards; HTTPS would have no
// credentials, so private repos couldn't be cloned or pushed.
func sshRepo(u string) string {
	if m := ghRepoURL.FindStringSubmatch(u); m != nil {
		return "git@github.com:" + m[1] + "/" + m[2] + ".git"
	}
	return u
}

// ---- headless sub-agents: no Rex, no pane ----

func panelessLog(a *subAgent) string {
	b, _ := os.ReadFile(a.logPath())
	return string(b)
}

// spawnPaneless runs the agent's pane process detached, with its output in a log. It provisions
// the guest as the pane does, then runs the agent CLI non-interactively; rux agent read prints
// the log, and wait and stop work as they do for panes.
func spawnPaneless(a *subAgent) {
	log, err := os.OpenFile(a.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fail("rux agent spawn: %v", err)
	}
	defer log.Close()
	cmd := exec.Command(selfExe(), "agent", "pane", a.path)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		a.update(func(a *subAgent) { a.Phase, a.Error = "failed", "starting the agent: "+err.Error() })
		fail("rux agent spawn: %v", err)
	}
	cmd.Process.Release()
	fmt.Printf("spawned %s headless (not in a Rex terminal, so no pane): %s in a guest, cloning %s\n", a.ID, a.Agent, repoDisplayName(a.Repo))
	fmt.Printf("The guest takes a minute or two; follow it with: rux agent wait %s, then rux agent read %s\n", a.ID, a.ID)
}

// panelessCommand is agentCommand in the agent CLI's non-interactive mode.
func panelessCommand(a *subAgent) string {
	cmd := agentCommand(a)
	if a.Agent == "codex" {
		return strings.Replace(cmd, "codex ", "codex exec ", 1)
	}
	return strings.Replace(cmd, "claude ", "claude -p ", 1)
}

// runHeadless runs the agent CLI in the guest through rox exec and records how it ended.
func runPaneless(ctx context.Context, a *subAgent, rox, guest string) {
	a.update(func(a *subAgent) {
		if a.Phase != "stopped" {
			a.Guest, a.Phase, a.Started = guest, "running", time.Now()
		}
	})
	script := `cd "$HOME"/` + shQuote(a.Project) + ` 2>/dev/null; ` + panelessCommand(a)
	cmd := exec.CommandContext(ctx, rox, "exec", guest, "--", "sh", "-lc", script)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err := cmd.Run()
	a.update(func(a *subAgent) {
		a.Pane = 0
		switch {
		case a.Phase == "stopped":
		case err != nil:
			a.Phase, a.Error = "failed", err.Error()
		default:
			a.Phase = "exited"
		}
	})
	if err != nil {
		os.Exit(1)
	}
}
