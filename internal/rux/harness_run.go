package rux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/harness"
)

// hrun is one rux session: the lead agent on this machine and the sub-agents it starts,
// each in its own rox guest. Every model loop runs here, so model keys never leave this
// machine; only tool calls run in the guests.
type hrun struct {
	cfg  harnessConfig
	mcp  *leadMCP
	sock string // the control socket, for rux view
	cwd  string
	repo string // this directory's origin, if any

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	lead     *harness.Agent
	agents   []*harness.Agent // lead first
	subs     map[string]*subInfo
	watching map[string]int    // sub-agents a wait_for_agents call covers
	effort   map[string]string // each agent's effort, by ID; "" is the engine's default
	// subEffortV is the effort new sub-agents start with, once it's been set.
	subEffortV   string
	subEffortSet bool
	allowBash    bool
	stopWait     time.Duration // how long stop waits for a rox up; 0 is ten minutes

	// changed coalesces change notifications for the UI.
	changed chan struct{}
	// approvals are tool calls waiting for the user.
	approvals chan *approval
}

type subInfo struct {
	Guest    string
	Shared   bool // an existing guest, which stop never removes
	Repo     string
	Branch   string
	Dir      string
	GuestTyp string
	Task     string
	// Host is the Rex remote host the guest runs on; empty for this machine.
	Host string
	// Project is the guest's checkout, for rox shell.
	Project string
	// CLI is the agent CLI (claude or codex) a sub-agent runs in its guest, signed in by
	// rix; empty for model APIs, whose loop runs here.
	CLI string
	// Model is the engine it was started with, as provider/model or claude-code[/MODEL].
	Model string
	exec  *harness.Guest

	// ctx covers this sub-agent's provisioning and ends when it's stopped.
	ctx    context.Context
	cancel context.CancelFunc
	// done closes when provisioning has returned, so stop knows the guest name is final.
	done chan struct{}
	// stopped is sticky: once set, provisioning attaches nothing and reports nothing.
	stopped bool
	// removeWanted: the stop asked to remove the guest, so whoever sees it last does.
	removeWanted bool
	reaped       bool // the guest was removed
	// cleanup serializes stops, so a retry can't race a stop that's removing the guest.
	cleanup sync.Mutex
}

type approval struct {
	agent *harness.Agent
	tool  string
	label string
	reply chan approvalAnswer
}

type approvalAnswer int

const (
	deny approvalAnswer = iota
	allowOnce
	allowAlways
)

func newRun(cfg harnessConfig, leadModel string) (*hrun, error) {
	cwd, _ := os.Getwd()
	r := &hrun{cfg: cfg, cwd: cwd, subs: map[string]*subInfo{}, watching: map[string]int{}, effort: map[string]string{},
		changed: make(chan struct{}, 1), approvals: make(chan *approval)}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	if out, err := exec.Command("git", "-C", cwd, "remote", "get-url", "origin").Output(); err == nil {
		r.repo = sshRepo(strings.TrimSpace(string(out)))
	}
	lead := harness.NewAgent("lead", "lead", "lead")
	r.wire(lead)
	r.lead, r.agents = lead, []*harness.Agent{lead}
	if err := r.attachLead(leadModel); err != nil {
		return nil, err
	}
	go lead.Run(r.ctx)
	return r, nil
}

func (r *hrun) notify() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *hrun) wire(a *harness.Agent) {
	a.OnChange = func(*harness.Agent) { r.notify() }
	a.Approve = func(a *harness.Agent, t *harness.Tool, label string) bool {
		r.mu.Lock()
		always := r.allowBash
		r.mu.Unlock()
		if always {
			return true
		}
		req := &approval{agent: a, tool: t.Spec.Name, label: label, reply: make(chan approvalAnswer, 1)}
		select {
		case r.approvals <- req:
		case <-r.ctx.Done():
			return false
		}
		r.notify()
		ans := <-req.reply
		if ans == allowAlways {
			r.mu.Lock()
			r.allowBash = true
			r.mu.Unlock()
		}
		return ans != deny
	}
}

func (r *hrun) leadSystem(cli string) string {
	own := "Your own tools look around here: read_file, grep and list_files run freely; bash asks the user each time."
	switch cli {
	case "claude":
		own = "You're Claude Code, run by rux. Read, Grep and Glob run freely here; Bash, edits and other tools ask the user " +
			"each time. Your rux tools (spawn_agent and the rest) start and follow the sub-agents."
	case "codex":
		own = "You're Codex, run by rux, in Codex's read-only sandbox here: you can read files and run commands that " +
			"only look, but not change anything on this machine. Your rux MCP tools (spawn_agent and the rest) start and follow the sub-agents."
	}
	repo := r.repo
	if repo == "" {
		repo = "(none: this directory has no origin remote)"
	}
	return `You are the lead agent in rux, a multi-agent harness. The user talks to you in rux's terminal UI,
which shows your conversation and every sub-agent's beside it, live.

You run on the user's machine, in ` + r.cwd + `. This directory's repo: ` + repo + `.
` + own + `

The work itself goes to sub-agents. Each is an agent like you with a shell and file tools, in its own
sandboxed rox guest with the repo cloned on a fresh branch rux/NAME. They can install, build, test, and
push their branch; they can't touch the user's machine. Only the user merges.

How to work:
- Talk with the user. For a question you can answer by reading code here, answer it yourself.
- For changes, plan the split, then start sub-agents with spawn_agent. Settle what matters and is unclear
  first, in one short question with your suggested defaults (which repo and branch, how many agents),
  unless the user already said. Start only as many agents as the work needs; parallel agents should
  work on separate parts.
- Write each task so an agent with no other context can finish it: the goal, where to look, constraints,
  and how to check the work.
- When a sub-agent finishes a turn, its report arrives as a message from it. Use wait_for_agents when
  you have nothing else to do until they finish. Answer their questions with message_agent, or ask the user.
- Report back briefly: what each agent did, its branch, how it was checked, and anything the user must decide.
- stop_agent when the user has what they need; it removes the guest, so make sure the work was pushed.
- Credentials come from rix. Never ask for or handle tokens or keys.`
}

func (r *hrun) subSystem(name string, s *subInfo) string {
	where := "Your working directory is " + s.Dir + " in the guest " + s.Guest + "."
	repo := "There's no repo checked out."
	if s.Repo != "" {
		repo = "It holds a clone of " + s.Repo + ", on a new branch rux/" + name + " made from " + cmpOr(s.Branch, "the default branch") + "."
	}
	return `You are ` + name + `, a sub-agent in rux. The lead agent gave you a task, and you work on it alone in a
sandboxed Linux guest (a rox guest) with a shell and file tools. ` + where + ` ` + repo + `

- Work until the task is done: read the code, make the change, and check it (build, tests, run it).
  Install what you need. You have no access to the user's machine.
- Commit as you go. When done, push your branch: git push -u origin rux/` + name + `. Git signs in with the
  user's key through rix; never ask for credentials.
- Messages from the lead or the user may arrive while you work; take them into account.
- End your turn with a short report for the lead: what you changed, the branch, how you checked it, and
  anything left open. If you're blocked on a decision, end your turn with the question instead.`
}

// leadTools are the lead's tools for running sub-agents.
func (r *hrun) leadTools() []harness.Tool {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	obj := func(req []string, props map[string]any) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(req) > 0 {
			s["required"] = req
		}
		return s
	}
	field := func(in json.RawMessage, k string) string {
		var m map[string]any
		_ = json.Unmarshal(in, &m)
		s, _ := m[k].(string)
		return s
	}
	return []harness.Tool{
		{
			Spec: harness.ToolSpec{Name: "spawn_agent", Description: "Start a sub-agent on a task, in its own new rox guest " +
				"(or an existing guest). It returns at once; the agent sets up its guest (a minute or two) and then works. " +
				"Its report arrives as a message when it ends its turn.",
				Schema: obj([]string{"name", "task"}, map[string]any{
					"name":       str("short lowercase name, such as fix-auth-tests"),
					"task":       str("the complete task, written for an agent with no other context"),
					"repo":       str("repo to clone: owner/name, a git URL, or \"none\". Default: this directory's origin"),
					"branch":     str("branch to start from. Default: the repo's default branch"),
					"guest_type": str("container (default, Apple container), linux (Tart VM), macos (Tart VM), fly or railway"),
					"guest":      str("an existing guest's name to work in instead of a new one (see list_guests)"),
					"host":       str("a Rex remote host (see list_hosts) to start the new guest on, instead of this machine"),
					"model":      str("provider/model for this agent. Default: " + cmpOr(r.cfg.AgentModel, "the lead's model")),
				})},
			Label: func(in json.RawMessage) string { return field(in, "name") },
			Run:   r.spawnTool,
		},
		{
			Spec: harness.ToolSpec{Name: "message_agent", Description: "Send a sub-agent a message: an answer, a correction or a new task. " +
				"A working agent reads it after its current tool calls.",
				Schema: obj([]string{"agent", "message"}, map[string]any{"agent": str("its name"), "message": str("the message")})},
			Label: func(in json.RawMessage) string { return field(in, "agent") },
			Run: func(_ context.Context, in json.RawMessage) (string, error) {
				var a struct{ Agent, Message string }
				if err := json.Unmarshal(in, &a); err != nil {
					return "", err
				}
				sub := r.find(a.Agent)
				if sub == nil {
					return "", fmt.Errorf("no sub-agent named %s; agent_status lists them", a.Agent)
				}
				if err := sub.TrySend("lead", a.Message); err != nil {
					return "", err
				}
				return "Sent to " + sub.Name, nil
			},
		},
		{
			ReadOnly: true,
			Spec: harness.ToolSpec{Name: "wait_for_agents", Description: "Wait until sub-agents end their turns (finish, ask " +
				"something, or fail), and return their reports. Returns early if the user sends you a message.",
				Schema: obj(nil, map[string]any{
					"agents":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "names; default all that are working"},
					"timeout_seconds": map[string]any{"type": "integer", "description": "default 1800"},
				})},
			Label: func(in json.RawMessage) string {
				var a struct{ Agents []string }
				_ = json.Unmarshal(in, &a)
				if len(a.Agents) == 0 {
					return "all"
				}
				return strings.Join(a.Agents, ", ")
			},
			Run: r.waitTool,
		},
		{
			ReadOnly: true,
			Spec: harness.ToolSpec{Name: "agent_status", Description: "List the sub-agents: status, guest, model, and the start of their last report.",
				Schema: obj(nil, map[string]any{})},
			Label: func(json.RawMessage) string { return "" },
			Run:   func(context.Context, json.RawMessage) (string, error) { return r.statusText(0), nil },
		},
		{
			ReadOnly: true,
			Spec: harness.ToolSpec{Name: "read_agent", Description: "Read the end of a sub-agent's transcript: its messages and tool calls.",
				Schema: obj([]string{"agent"}, map[string]any{"agent": str("its name"), "entries": map[string]any{"type": "integer", "description": "how many recent entries (default 30)"}})},
			Label: func(in json.RawMessage) string { return field(in, "agent") },
			Run: func(_ context.Context, in json.RawMessage) (string, error) {
				var a struct {
					Agent   string
					Entries int
				}
				if err := json.Unmarshal(in, &a); err != nil {
					return "", err
				}
				sub := r.find(a.Agent)
				if sub == nil {
					return "", fmt.Errorf("no sub-agent named %s", a.Agent)
				}
				return transcriptText(sub.Snapshot().Entries, max(a.Entries, 30)), nil
			},
		},
		{
			Spec: harness.ToolSpec{Name: "stop_agent", Description: "Stop a sub-agent and remove the guest rux started for it " +
				"(an existing guest is kept). Unpushed work in a removed guest is lost.",
				Schema: obj([]string{"agent"}, map[string]any{"agent": str("its name"),
					"keep_guest": map[string]any{"type": "boolean", "description": "keep the guest running"}})},
			Label: func(in json.RawMessage) string { return field(in, "agent") },
			Run: func(_ context.Context, in json.RawMessage) (string, error) {
				var a struct {
					Agent     string
					KeepGuest bool `json:"keep_guest"`
				}
				if err := json.Unmarshal(in, &a); err != nil {
					return "", err
				}
				return r.stop(a.Agent, !a.KeepGuest)
			},
		},
		{
			ReadOnly: true,
			Spec: harness.ToolSpec{Name: "list_hosts", Description: "List the user's Rex hosts: this machine and remote hosts, " +
				"which spawn_agent can start guests on with host.",
				Schema: obj(nil, map[string]any{})},
			Label: func(json.RawMessage) string { return "" },
			Run:   func(context.Context, json.RawMessage) (string, error) { return rexHostsText() },
		},
		{
			ReadOnly: true,
			Spec: harness.ToolSpec{Name: "list_guests", Description: "List the user's rox guests, which spawn_agent can reuse with guest.",
				Schema: obj(nil, map[string]any{})},
			Label: func(json.RawMessage) string { return "" },
			Run: func(context.Context, json.RawMessage) (string, error) {
				gs, err := listRoxGuests()
				if err != nil {
					return "", err
				}
				if len(gs) == 0 {
					return "No guests", nil
				}
				var b strings.Builder
				for _, g := range gs {
					fmt.Fprintf(&b, "%s  %s  %s  projects: %s\n", g.Name, g.Backend, g.Phase, cmpOr(g.Projects, "none"))
				}
				return b.String(), nil
			},
		},
	}
}

func (r *hrun) find(name string) *harness.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents[1:] {
		if a.Name == name || a.ID == name {
			return a
		}
	}
	return nil
}

func (r *hrun) uniqueName(name string) string {
	base := slug(name)
	if base == "" {
		base = "agent"
	}
	if len(base) > 24 {
		base = strings.Trim(base[:24], "-")
	}
	n := base
	for i := 2; r.find(n) != nil; i++ {
		n = fmt.Sprintf("%s-%d", base, i)
	}
	return n
}

func (r *hrun) spawnTool(_ context.Context, in json.RawMessage) (string, error) {
	var a struct {
		Name, Task, Repo, Branch, Guest, Model, Host string
		GuestType                                    string `json:"guest_type"`
	}
	if err := json.Unmarshal(in, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Task) == "" {
		return "", errors.New("task is empty")
	}
	repo := a.Repo
	switch {
	case repo == "none":
		repo = ""
	case repo == "":
		repo = r.repo
	case !strings.Contains(repo, ":") && strings.Count(repo, "/") == 1:
		repo = "git@github.com:" + strings.TrimSuffix(repo, ".git") + ".git"
	default:
		repo = sshRepo(repo)
	}
	model := cmpOr(a.Model, r.agentModel())
	var p harness.Provider
	cli, _, isCLI := agentCLI(model)
	if !isCLI {
		var err error
		if p, err = r.cfg.newProvider(model); err != nil {
			return "", err
		}
	}
	gt := cmpOr(a.GuestType, "container")
	if a.Guest == "" {
		if _, ok := runtimes[gt]; !ok {
			return "", fmt.Errorf("guest_type %q isn't one of container, linux, macos, fly, railway", gt)
		}
	}
	name := r.uniqueName(a.Name)
	sub := harness.NewAgent(name, name, "sub")
	r.wire(sub)
	engine := model
	if p != nil {
		engine = p.Name()
	}
	sub.Model = engine
	info := &subInfo{Repo: repo, Branch: a.Branch, Guest: a.Guest, Shared: a.Guest != "", GuestTyp: gt, Task: a.Task, Host: a.Host,
		CLI: cli, Model: model}
	sub.OnTurnEnd = func(s *harness.Agent, text string, err error) { r.subTurnEnded(s, text, err) }
	effort := r.subEffort()
	info.ctx, info.cancel = context.WithCancel(r.ctx)
	info.done = make(chan struct{})
	r.mu.Lock()
	r.agents = append(r.agents, sub)
	r.subs[name] = info
	r.effort[sub.ID] = effort
	r.mu.Unlock()
	sub.Send("lead", a.Task)
	go sub.Run(r.ctx)
	go func() {
		defer close(info.done)
		r.provision(sub, info)
		// A stop that gave up waiting leaves the removal to us: rox up has now returned
		// and named its guest, if it made one.
		r.mu.Lock()
		reap := info.stopped && info.removeWanted
		r.mu.Unlock()
		if reap {
			if _, err := r.reap(info); err != nil {
				sub.Note("error", err.Error()+" (stop_agent retries)")
			}
		}
	}()
	where := "a new " + gt + " guest"
	if a.Host != "" {
		where += " on " + a.Host
	}
	if info.Shared {
		where = "the guest " + info.Guest
	}
	return fmt.Sprintf("Started %s (%s) in %s, with %s. It's setting up; its report arrives as a message from it.",
		name, engine, where, cmpOr(repo, "no repo")), nil
}

// provision sets up a sub-agent's guest, then hands it its tools.
func (r *hrun) provision(a *harness.Agent, s *subInfo) {
	fail := func(format string, args ...any) {
		if r.isStopped(s) {
			return // the user stopped it; stop reports and cleans up
		}
		msg := fmt.Sprintf(format, args...)
		if g := r.guestOf(s); g != "" && !s.Shared {
			msg += " (guest " + g + " was kept; stop_agent removes it)"
		}
		a.SetStatus(harness.Failed, msg)
		a.Note("error", msg)
		r.subTurnEnded(a, "", errors.New(msg))
	}
	rox, err := exec.LookPath("rox")
	if err != nil {
		fail("rox isn't installed")
		return
	}
	project := ""
	if s.Repo != "" {
		project = projectName(s.Repo)
	}
	defer func() {
		r.mu.Lock()
		s.Project = project
		r.mu.Unlock()
	}()
	if s.Shared {
		if s.Repo != "" {
			project += "-" + a.Name
			args := []string{"add", s.Guest, "--repo", s.Repo, "--project", project}
			if s.Host != "" {
				args = append(args, "-S", s.Host)
			}
			if s.CLI != "" {
				args = append(args, "--agents", s.CLI)
			}
			if s.Branch != "" {
				args = append(args, "--branch", s.Branch)
			}
			a.SetStatus(harness.Starting, "cloning into "+s.Guest)
			if _, err := r.roxStream(a, rox, args, nil); err != nil {
				fail("rox add failed: %v", err)
				return
			}
		}
	} else {
		args := []string{"up", s.GuestTyp}
		if s.Host != "" {
			args = append(args, "-S", s.Host)
		}
		if s.Repo != "" {
			args = append(args, "--repo", s.Repo, "--project", project)
			if s.Branch != "" {
				args = append(args, "--branch", s.Branch)
			}
		} else {
			args = append(args, "--no-project")
		}
		if s.GuestTyp == "container" {
			args = append(args, "--memory", "4G")
		}
		if s.CLI != "" {
			// rox installs the agent CLI, and rix signs it in through rox's hooks.
			args = append(args, "--agents", s.CLI)
		}
		// A guest on another host is reached over the tailnet, so it needs Tailscale.
		if !remoteRuntime(s.GuestTyp) && s.Host == "" {
			args = append(args, "--no-tailscale")
		}
		a.SetStatus(harness.Starting, "starting a "+s.GuestTyp+" guest"+map[bool]string{true: " on " + s.Host, false: ""}[s.Host != ""])
		// The guest is recorded the moment rox names it, so stop can remove it even
		// if rox up fails or is cancelled later. Only a name rox up itself printed
		// counts as ours; nothing is guessed from the list of guests.
		_, err := r.roxStream(a, rox, args, func(name string) {
			r.mu.Lock()
			if s.Guest == "" {
				s.Guest = name
			}
			r.mu.Unlock()
		})
		if err != nil {
			fail("rox up failed: %v", err)
			return
		}
		if r.guestOf(s) == "" {
			fail("rox up finished but didn't say which guest it started; check rox ls before starting again")
			return
		}
	}
	if r.isStopped(s) {
		return
	}

	a.SetStatus(harness.Starting, "connecting to "+s.Guest)
	var g contract.Guest
	out, err := exec.CommandContext(s.ctx, rox, "inspect", s.Guest, "--json").Output()
	if err != nil || json.Unmarshal(out, &g) != nil {
		fail("couldn't read %s's record: %v", s.Guest, err)
		return
	}
	s.Dir = g.Home
	for _, pr := range g.Projects {
		if project != "" && (pr.Name == project || pr.ID == project) {
			s.Dir = pr.Workspace
		}
	}
	ex := &harness.Guest{Rox: rox, Name: s.Guest, WorkDir: s.Dir}
	if rix, err := rixBin(); err == nil {
		if sock, err := exec.Command(rix, "git-agent", "sock", s.Guest).Output(); err == nil {
			if p := strings.TrimSpace(string(sock)); p != "" {
				if _, err := os.Stat(p); err == nil {
					ex.AgentSock = p
				}
			}
		}
	}
	if s.Repo != "" {
		br := "rux/" + a.Name
		if out, code, err := ex.Run(s.ctx, "git switch -c "+harness.ShellQuote(br)+" 2>&1 || git switch "+harness.ShellQuote(br), nil, time.Minute); err != nil || code != 0 {
			if r.isStopped(s) {
				return
			}
			a.Note("note", "Couldn't make the branch "+br+": "+lastLine(out))
		}
	}
	r.mu.Lock()
	if s.stopped {
		r.mu.Unlock()
		return
	}
	s.exec = ex
	r.mu.Unlock()
	if err := r.attachSub(a, s, s.Model); err != nil {
		fail("%v", err)
		return
	}
	if r.isStopped(s) {
		return
	}
	a.Note("note", fmt.Sprintf("Working in %s:%s", s.Guest, s.Dir))
}

// roxStream runs rox, showing its latest line as the agent's status, and returns the
// guest name it printed.
func (r *hrun) roxStream(a *harness.Agent, rox string, args []string, onGuest func(string)) (string, error) {
	// A rox up that has started owns a guest nobody can name until it prints the
	// launch line, so stop lets it finish (cancelling only with the whole session);
	// other commands end as soon as the agent is stopped.
	ctx := r.ctx
	if s := r.subOf(a); s != nil && !(len(args) > 0 && args[0] == "up") {
		ctx = s.ctx
	}
	cmd := exec.CommandContext(ctx, rox, args...)
	cmd.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	guest := ""
	var tail []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			line := strings.TrimSpace(ansiRe.ReplaceAllString(sc.Text(), ""))
			if line == "" {
				continue
			}
			if name := launchedGuest(line); guest == "" && name != "" {
				guest = name
				if onGuest != nil {
					onGuest(name)
				}
			}
			tail = append(tail, line)
			if len(tail) > 8 {
				tail = tail[1:]
			}
			st, _ := a.Status()
			a.SetStatus(st, line)
		}
	}()
	err := cmd.Run()
	pw.Close()
	<-done
	if err != nil && len(tail) > 0 {
		err = fmt.Errorf("%v: %s", err, tail[len(tail)-1])
	}
	return guest, err
}

// subTurnEnded passes a sub-agent's report to the lead, unless a wait_for_agents call
// is already collecting it.
func (r *hrun) subTurnEnded(a *harness.Agent, text string, err error) {
	r.mu.Lock()
	watched := r.watching[a.ID] > 0
	r.mu.Unlock()
	r.notify()
	if watched {
		return
	}
	msg := "Ended its turn:\n" + cmpOr(strings.TrimSpace(text), "(no report)")
	switch {
	case errors.Is(err, harness.ErrInterrupted):
		msg = "The user interrupted it. It waits for a message."
	case err != nil:
		msg = "Stopped with an error: " + err.Error()
	}
	r.lead.Send(a.Name, msg)
}

func (r *hrun) waitTool(ctx context.Context, in json.RawMessage) (string, error) {
	var a struct {
		Agents  []string
		Timeout int `json:"timeout_seconds"`
	}
	_ = json.Unmarshal(in, &a)
	var targets []*harness.Agent
	if len(a.Agents) == 0 {
		r.mu.Lock()
		for _, s := range r.agents[1:] {
			if st, _ := s.Status(); st == harness.Working || st == harness.Starting || (!s.Terminal() && s.Snapshot().Queued > 0) {
				targets = append(targets, s)
			}
		}
		r.mu.Unlock()
	} else {
		for _, n := range a.Agents {
			s := r.find(n)
			if s == nil {
				return "", fmt.Errorf("no sub-agent named %s", n)
			}
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		return "No sub-agents are working.\n" + r.statusText(400), nil
	}
	r.mu.Lock()
	for _, t := range targets {
		r.watching[t.ID]++
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		for _, t := range targets {
			r.watching[t.ID]--
		}
		r.mu.Unlock()
	}()
	timeout := time.Duration(max(a.Timeout, 0)) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	why := ""
	for why == "" {
		busy := false
		for _, t := range targets {
			snap := t.Snapshot()
			if snap.Status == harness.Starting || snap.Status == harness.Working || (snap.Queued > 0 && snap.Status != harness.Stopped && snap.Status != harness.Failed) {
				busy = true
			}
		}
		switch {
		case !busy:
			why = "done"
		case r.lead.Snapshot().Queued > 0:
			why = "The user (or another agent) sent you a message, so the wait ended early."
		case time.Now().After(deadline):
			why = "Timed out; they're still working."
		}
		if why != "" {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
	var b strings.Builder
	if why != "done" {
		b.WriteString(why + "\n\n")
	}
	for _, t := range targets {
		snap := t.Snapshot()
		fmt.Fprintf(&b, "## %s (%s)\n", t.Name, snap.Status)
		switch {
		case snap.Status == harness.Failed || snap.Err != "":
			b.WriteString("Error: " + cmpOr(snap.Err, snap.Detail) + "\n")
			if snap.Queued > 0 {
				fmt.Fprintf(&b, "%d queued message(s) were never read.\n", snap.Queued)
			}
			b.WriteString("\n")
		case snap.Status == harness.Stopped:
			b.WriteString("Error: stopped before it finished; it won't report.\n")
			if snap.Queued > 0 {
				fmt.Fprintf(&b, "%d queued message(s) were never read.\n", snap.Queued)
			}
			if snap.Last != "" {
				b.WriteString("Its last report:\n" + snap.Last + "\n")
			}
			b.WriteString("\n")
		case snap.Last != "":
			b.WriteString(snap.Last + "\n\n")
		default:
			b.WriteString("(no report yet)\n\n")
		}
	}
	return b.String(), nil
}

func (r *hrun) statusText(excerpt int) string {
	r.mu.Lock()
	agents := append([]*harness.Agent(nil), r.agents[1:]...)
	r.mu.Unlock()
	if len(agents) == 0 {
		return "No sub-agents yet."
	}
	if excerpt == 0 {
		excerpt = 300
	}
	var b strings.Builder
	for _, a := range agents {
		snap := a.Snapshot()
		r.mu.Lock()
		s := r.subs[a.Name]
		r.mu.Unlock()
		fmt.Fprintf(&b, "- %s: %s", a.Name, snap.Status)
		if snap.Detail != "" {
			fmt.Fprintf(&b, " (%s)", snap.Detail)
		}
		fmt.Fprintf(&b, ", %s, guest %s", a.ModelName(), cmpOr(s.Guest, "not up yet"))
		if s.Repo != "" {
			fmt.Fprintf(&b, ", branch rux/%s", a.Name)
		}
		b.WriteString("\n")
		if last := strings.TrimSpace(snap.Last); last != "" {
			if len(last) > excerpt {
				last = last[:excerpt] + "…"
			}
			b.WriteString("  last report: " + strings.ReplaceAll(last, "\n", "\n  ") + "\n")
		}
		if snap.Err != "" {
			b.WriteString("  error: " + snap.Err + "\n")
		}
	}
	return b.String()
}

// isStopped reports whether stop was called for a sub-agent.
func (r *hrun) isStopped(s *subInfo) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return s.stopped
}

func (r *hrun) guestOf(s *subInfo) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return s.Guest
}

func (r *hrun) subOf(a *harness.Agent) *subInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.subs[a.Name]
}

// stop stops a sub-agent for good and, unless it worked in an existing guest, removes
// its guest. Stopping is sticky: provisioning that's still running is cancelled and
// can't revive the agent. The guest is removed only if rox up named it for this agent;
// if removal fails, its name is kept so stop can be run again.
func (r *hrun) stop(name string, remove bool) (string, error) {
	a := r.find(name)
	if a == nil {
		return "", fmt.Errorf("no sub-agent named %s", name)
	}
	s := r.subOf(a)
	if s == nil {
		return "", fmt.Errorf("no sub-agent named %s", name)
	}
	r.mu.Lock()
	s.stopped = true
	s.removeWanted = remove
	wait := r.stopWait
	r.mu.Unlock()
	if wait == 0 {
		wait = 10 * time.Minute
	}
	a.Stop()
	s.cancel()
	a.Close()
	// A rox up that already started is allowed to finish, so the guest it made is
	// named before we decide what to remove.
	select {
	case <-s.done:
	case <-time.After(wait):
		if remove && !s.Shared {
			return "Stopped " + a.Name + ". rox is still starting its guest; rux removes it as soon as rox finishes.", nil
		}
		return "Stopped " + a.Name + ". rox is still starting its guest, which is kept.", nil
	}
	if !remove || s.Shared {
		g := r.guestOf(s)
		return "Stopped " + a.Name + map[bool]string{true: "; kept its guest " + g, false: ""}[g != ""], nil
	}
	g, err := r.reap(s)
	if err != nil {
		return "", fmt.Errorf("stopped %s, but %v (stop_agent again retries)", a.Name, err)
	}
	if g == "" && !r.reapedOf(s) {
		return "Stopped " + a.Name, nil
	}
	return "Stopped " + a.Name + " and removed its guest", nil
}

func (r *hrun) reapedOf(s *subInfo) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return s.reaped
}

// reap removes the guest rox up named for this sub-agent, once. Stop and the provisioning
// goroutine may both call it; the lock and clearing the name make the second a no-op.
// If rox down fails the name stays, so a later stop can retry.
func (r *hrun) reap(s *subInfo) (string, error) {
	s.cleanup.Lock()
	defer s.cleanup.Unlock()
	guest := r.guestOf(s)
	if guest == "" || s.Shared {
		return "", nil
	}
	down := []string{"down"}
	if s.Host != "" {
		down = append(down, "-S", s.Host)
	}
	out, err := exec.Command("rox", append(down, guest)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rox down %s failed: %s", guest, lastLine(ansiRe.ReplaceAllString(string(out), "")))
	}
	r.mu.Lock()
	s.Guest, s.reaped = "", true
	r.mu.Unlock()
	return guest, nil
}

// ownGuests are the guests rux started in this session that are still up.
func (r *hrun) ownGuests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, a := range r.agents[1:] {
		if s := r.subs[a.Name]; s != nil && s.Guest != "" && !s.Shared {
			out = append(out, s.Guest)
		}
	}
	return out
}

// transcriptText renders the last n entries as plain text, for the lead.
func transcriptText(entries []harness.Entry, n int) string {
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	var b strings.Builder
	for _, e := range entries {
		switch e.Kind {
		case "user":
			fmt.Fprintf(&b, "[%s] %s\n\n", cmpOr(e.From, "you"), e.Text)
		case "text":
			fmt.Fprintf(&b, "%s\n\n", e.Text)
		case "tool":
			res := e.Result
			if len(res) > 600 {
				res = res[:600] + "…"
			}
			status := "ok"
			if e.IsError {
				status = "error"
			} else if e.Running {
				status = "running"
			}
			fmt.Fprintf(&b, "> %s %s (%s)\n%s\n\n", e.Tool, e.Label, status, res)
		case "note", "error":
			fmt.Fprintf(&b, "(%s) %s\n\n", e.Kind, e.Text)
		}
	}
	return b.String()
}

// harnessLogDir is where a session's transcripts are saved when it ends.
func harnessLogDir() string { return filepath.Join(ruxHome(), "sessions") }

// rexHostsText lists Rex's hosts for the lead.
func rexHostsText() (string, error) {
	var out struct {
		Hosts []struct {
			Host struct {
				Label     string   `json:"label"`
				Endpoints []string `json:"endpoints"`
			} `json:"host"`
		} `json:"hosts"`
	}
	if os.Getenv("REX_SESSION") == "" {
		// Without Rex, guests start on this machine, or on the host --host names over SSH.
		return "this machine (default)\n", nil
	}
	if err := rexJSON(&out, "hosts", "--json"); err != nil {
		return "", fmt.Errorf("rex hosts: %v", err)
	}
	var b strings.Builder
	b.WriteString("this machine (default)\n")
	for _, h := range out.Hosts {
		name := cmpOr(h.Host.Label, strings.Join(h.Host.Endpoints, ", "))
		// rox registers its guests as Rex hosts too; those aren't places to start guests.
		if name != "" && !guestNameRe.MatchString(name) {
			fmt.Fprintf(&b, "%s  %s\n", name, strings.Join(h.Host.Endpoints, ", "))
		}
	}
	return b.String(), nil
}
