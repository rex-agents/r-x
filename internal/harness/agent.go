package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status is where an agent is.
type Status string

const (
	Starting Status = "starting" // its guest is being set up
	Working  Status = "working"
	Waiting  Status = "waiting" // its turn is over; it waits for a message
	Failed   Status = "failed"
	Stopped  Status = "stopped"
)

// ErrInterrupted is the error OnTurnEnd gets when the user interrupted the turn.
var ErrInterrupted = errors.New("interrupted by the user")

// Entry is one item in an agent's transcript.
type Entry struct {
	// Kind is user, text, thinking, tool, note or error.
	Kind string
	// From is who sent a user message: "you" or an agent's name.
	From string
	Text string
	// For tools:
	Tool    string
	Label   string
	Result  string
	IsError bool
	Running bool
	At      time.Time
}

type message struct{ from, text string }

// Agent is one agent: a conversation with a model, the tools it works with, and its
// transcript. It runs a turn for each batch of messages it gets.
type Agent struct {
	ID   string
	Name string // the key everything finds an agent by; "lead" for the lead
	// Label is what the UI calls the agent, when that isn't Name (the lead goes by its authority's name).
	Label string
	Role  string // lead or sub
	Model string
	Where string

	// OnChange is called after anything about the agent changes, from any goroutine.
	OnChange func(*Agent)
	// Approve asks the user whether a call to a tool marked Approve may run. It blocks.
	Approve func(a *Agent, t *Tool, label string) bool
	// OnTurnEnd is called when a turn ends, with the turn's final text.
	OnTurnEnd func(a *Agent, text string, err error)

	mu       sync.Mutex
	status   Status
	detail   string
	entries  []Entry
	usage    Usage
	conv     Conversation
	runner   TurnRunner
	carry    string
	tools    []Tool
	inbox    []message
	wake     chan struct{}
	cancel   context.CancelFunc
	last     string
	turns    int
	lastErr  string
	started  time.Time
	changed  time.Time
	attached bool
	stopped  bool // Stop was called: the agent stays Stopped and takes no more messages
}

// NewAgent makes an agent that's starting. Attach gives it a model and tools.
func NewAgent(id, name, role string) *Agent {
	return &Agent{ID: id, Name: name, Role: role, status: Starting, wake: make(chan struct{}, 1), started: time.Now(), changed: time.Now()}
}

// Attach gives the agent its model and tools; messages sent before are kept.
func (a *Agent) Attach(p Provider, system string, tools []Tool, where string) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	if a.runner != nil {
		go a.runner.Close()
	}
	a.runner = nil
	a.conv, a.tools, a.Model, a.Where, a.attached = p.New(system), tools, p.Name(), where, true
	if a.status == Starting {
		a.status = Waiting
	}
	a.detail = ""
	a.mu.Unlock()
	a.poke()
	a.changedNow()
}

// AttachRunner gives the agent an engine that runs whole turns itself, such as Claude Code.
func (a *Agent) AttachRunner(r TurnRunner, where string) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		go r.Close()
		return
	}
	if a.runner != nil && a.runner != r {
		go a.runner.Close()
	}
	a.conv = nil
	a.runner, a.Model, a.Where, a.attached = r, r.Name(), where, true
	if a.status == Starting {
		a.status = Waiting
	}
	a.detail = ""
	a.mu.Unlock()
	a.poke()
	a.changedNow()
}

// Carry gives the next turn context the new engine lacks, such as the conversation so far
// with another model. It's sent ahead of the next message.
func (a *Agent) Carry(context string) {
	a.mu.Lock()
	a.carry = context
	a.mu.Unlock()
}

// Busy reports whether a turn is running.
func (a *Agent) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status == Working
}

// SetModelName names the engine an agent that's still starting will get.
func (a *Agent) SetModelName(name string) {
	a.mu.Lock()
	a.Model = name
	a.mu.Unlock()
	a.changedNow()
}

// ModelName is the agent's engine, as provider/model.
func (a *Agent) ModelName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Model
}

// Close stops the agent's engine.
func (a *Agent) Close() {
	a.mu.Lock()
	r := a.runner
	a.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

// SetStatus sets a status and a line of detail, such as what's being set up.
func (a *Agent) SetStatus(s Status, detail string) {
	a.mu.Lock()
	if a.stopped && s != Stopped {
		a.mu.Unlock()
		return
	}
	a.status, a.detail = s, detail
	if s == Failed {
		a.lastErr = detail
	}
	a.mu.Unlock()
	a.changedNow()
}

// Note adds a line to the transcript that isn't part of the conversation.
func (a *Agent) Note(kind, text string) {
	a.mu.Lock()
	a.entries = append(a.entries, Entry{Kind: kind, Text: text, At: time.Now()})
	a.mu.Unlock()
	a.changedNow()
}

// Send queues a message. A working agent reads it after its current tool calls.
func (a *Agent) Send(from, text string) {
	a.mu.Lock()
	a.inbox = append(a.inbox, message{from, text})
	a.entries = append(a.entries, Entry{Kind: "user", From: from, Text: text, At: time.Now()})
	a.mu.Unlock()
	a.poke()
	a.changedNow()
}

// TrySend queues a message unless the agent is stopped or failed, which never read
// their messages again; then it says so.
func (a *Agent) TrySend(from, text string) error {
	a.mu.Lock()
	if a.stopped || a.status == Stopped || a.status == Failed {
		st := a.status
		a.mu.Unlock()
		return fmt.Errorf("%s is %s for good, so it can't read messages; start a new agent instead", a.Name, st)
	}
	a.inbox = append(a.inbox, message{from, text})
	a.entries = append(a.entries, Entry{Kind: "user", From: from, Text: text, At: time.Now()})
	a.mu.Unlock()
	a.poke()
	a.changedNow()
	return nil
}

// Terminal reports whether the agent is stopped or failed, so nothing queued for it
// will ever be read.
func (a *Agent) Terminal() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped || a.status == Stopped || a.status == Failed
}

// Stop ends the agent for good: it cancels a running turn and stays Stopped whatever
// else happens, such as a provisioning step finishing later or the turn ending.
func (a *Agent) Stop() {
	a.mu.Lock()
	a.stopped = true
	if a.cancel != nil {
		a.cancel()
	}
	a.status, a.detail = Stopped, ""
	a.mu.Unlock()
	a.poke()
	a.changedNow()
}

// Interrupt ends the current turn.
func (a *Agent) Interrupt() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil && a.status == Working {
		a.cancel()
		return true
	}
	return false
}

func (a *Agent) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *Agent) changedNow() {
	a.mu.Lock()
	a.changed = time.Now()
	a.mu.Unlock()
	if a.OnChange != nil {
		a.OnChange(a)
	}
}

// Snapshot is a copy of what the UI shows.
type Snapshot struct {
	Status  Status
	Detail  string
	Entries []Entry
	Usage   Usage
	Last    string
	Turns   int
	Err     string
	Started time.Time
	Changed time.Time
	Queued  int
}

func (a *Agent) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{Status: a.status, Detail: a.detail, Entries: append([]Entry(nil), a.entries...), Usage: a.usage,
		Last: a.last, Turns: a.turns, Err: a.lastErr, Started: a.started, Changed: a.changed, Queued: len(a.inbox)}
}

// Status is the agent's status and detail line.
func (a *Agent) Status() (Status, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status, a.detail
}

// Run handles messages until ctx ends.
func (a *Agent) Run(ctx context.Context) {
	defer func() {
		a.mu.Lock()
		if a.status != Failed {
			a.status = Stopped
		}
		a.mu.Unlock()
		a.changedNow()
	}()
	for {
		a.mu.Lock()
		ready := a.attached && len(a.inbox) > 0 && a.status != Failed && a.status != Stopped
		a.mu.Unlock()
		if !ready {
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
				continue
			}
		}
		a.turn(ctx)
		if ctx.Err() != nil {
			return
		}
	}
}

func (a *Agent) drain() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var parts []string
	for _, m := range a.inbox {
		if m.from == "you" || m.from == "" {
			parts = append(parts, m.text)
		} else {
			parts = append(parts, fmt.Sprintf("[message from %s]\n%s", m.from, m.text))
		}
	}
	a.inbox = nil
	text := strings.Join(parts, "\n\n")
	if a.carry != "" {
		text = a.carry + "\n\n---\n\n" + text
		a.carry = ""
	}
	return text
}

const maxSteps = 300

func (a *Agent) turn(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.status, a.detail, a.cancel, a.lastErr = Working, "thinking", cancel, ""
	conv, tools, runner := a.conv, a.tools, a.runner
	a.mu.Unlock()
	a.changedNow()

	if runner != nil {
		a.runnerTurn(ctx, parent, runner)
		return
	}
	conv.User(a.drain())
	specs := make([]ToolSpec, len(tools))
	byName := map[string]*Tool{}
	for i := range tools {
		specs[i] = tools[i].Spec
		byName[tools[i].Spec.Name] = &tools[i]
	}

	var final string
	var turnErr error
	interrupted := false
	for step := 0; ; step++ {
		if step == maxSteps {
			turnErr = fmt.Errorf("stopped after %d steps in one turn", maxSteps)
			break
		}
		cur := -1
		curKind := ""
		t, err := conv.Step(ctx, specs, func(d Delta) {
			a.mu.Lock()
			if cur < 0 || curKind != d.Kind {
				a.entries = append(a.entries, Entry{Kind: d.Kind, At: time.Now()})
				cur, curKind = len(a.entries)-1, d.Kind
			}
			a.entries[cur].Text += d.Text
			if d.Kind == "text" {
				a.detail = "writing"
			}
			a.mu.Unlock()
			a.changedNow()
		})
		if err != nil {
			if ctx.Err() != nil {
				a.Note("note", "Interrupted")
				interrupted = true
			} else {
				turnErr = err
			}
			break
		}
		a.mu.Lock()
		a.usage.Input += t.Usage.Input
		a.usage.Output += t.Usage.Output
		a.usage.CacheRead += t.Usage.CacheRead
		a.mu.Unlock()
		if t.Stop == StopRefusal {
			turnErr = errors.New("the model declined this request" + map[bool]string{true: ": " + t.Detail, false: ""}[t.Detail != ""])
			break
		}
		if len(t.Calls) == 0 {
			final = t.Text
			if t.Stop == StopMaxTokens {
				a.Note("note", "The reply hit the output limit")
			}
			break
		}

		results := a.runCalls(ctx, t, byName)
		note := a.drain()
		conv.Results(results, note)
		if ctx.Err() != nil {
			a.Note("note", "Interrupted")
			interrupted = true
			break
		}
	}

	a.mu.Lock()
	a.cancel = nil
	a.turns++
	if !a.stopped {
		a.status, a.detail = Waiting, ""
	}
	stopped := a.stopped
	if final != "" {
		a.last = final
	}
	if turnErr != nil {
		a.lastErr = turnErr.Error()
		a.entries = append(a.entries, Entry{Kind: "error", Text: turnErr.Error(), At: time.Now()})
	}
	queued := len(a.inbox) > 0
	a.mu.Unlock()
	a.changedNow()
	if interrupted && turnErr == nil && parent.Err() == nil {
		turnErr = ErrInterrupted
	}
	if a.OnTurnEnd != nil && parent.Err() == nil && !stopped {
		a.OnTurnEnd(a, final, turnErr)
	}
	if queued && !stopped {
		a.poke()
	}
}

func (a *Agent) runCalls(ctx context.Context, t Turn, byName map[string]*Tool) []ToolResult {
	results := make([]ToolResult, len(t.Calls))
	idx := make([]int, len(t.Calls))
	labels := make([]string, len(t.Calls))
	for i, c := range t.Calls {
		label := ""
		if tool := byName[c.Name]; tool != nil && tool.Label != nil {
			label = tool.Label(c.Input)
		}
		labels[i] = label
		a.mu.Lock()
		a.entries = append(a.entries, Entry{Kind: "tool", Tool: c.Name, Label: label, Running: true, At: time.Now()})
		idx[i] = len(a.entries) - 1
		a.detail = c.Name + " " + label
		a.mu.Unlock()
	}
	a.changedNow()

	finish := func(i int, out string, err error) {
		r := ToolResult{ID: t.Calls[i].ID, Content: out}
		if err != nil {
			r.Content, r.IsError = err.Error(), true
		}
		if r.Content == "" {
			r.Content = "(no output)"
		}
		results[i] = r
		a.mu.Lock()
		e := &a.entries[idx[i]]
		e.Running, e.Result, e.IsError = false, r.Content, r.IsError
		a.mu.Unlock()
		a.changedNow()
	}

	// Approvals are asked one at a time, then the calls run together.
	run := make([]bool, len(t.Calls))
	for i, c := range t.Calls {
		tool := byName[c.Name]
		switch {
		case t.Stop == StopMaxTokens:
			finish(i, "", errors.New("the call was cut off by the output limit; send it again, smaller"))
		case tool == nil:
			finish(i, "", fmt.Errorf("there's no tool named %q", c.Name))
		case ctx.Err() != nil:
			finish(i, "", errors.New("interrupted by the user"))
		case tool.Approve && a.Approve != nil && !a.Approve(a, tool, labels[i]):
			finish(i, "", errors.New("the user didn't allow this call. Ask them, or try another way"))
		default:
			run[i] = true
		}
	}
	// Read-only calls next to each other run together; anything else runs alone, in order,
	// so a read after a write sees it.
	exec := func(i int) {
		c := t.Calls[i]
		out, err := byName[c.Name].Run(ctx, c.Input)
		if ctx.Err() != nil && err != nil {
			err = errors.New("interrupted by the user")
		}
		finish(i, out, err)
	}
	for i := 0; i < len(t.Calls); {
		if !run[i] {
			i++
			continue
		}
		if !byName[t.Calls[i].Name].ReadOnly {
			exec(i)
			i++
			continue
		}
		var wg sync.WaitGroup
		for ; i < len(t.Calls) && (!run[i] || byName[t.Calls[i].Name].ReadOnly); i++ {
			if run[i] {
				wg.Add(1)
				go func(i int) { defer wg.Done(); exec(i) }(i)
			}
		}
		wg.Wait()
	}
	return results
}

// MarshalInput is a helper for tools that build their input in code.
func MarshalInput(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// runnerTurn runs one turn on a TurnRunner.
func (a *Agent) runnerTurn(ctx, parent context.Context, r TurnRunner) {
	sink := &agentSink{a: a, tools: map[string]int{}}
	final, err := r.RunTurn(ctx, a.drain(), sink)
	interrupted := errors.Is(err, ErrInterrupted)
	if interrupted {
		err = nil
		a.Note("note", "Interrupted")
	}
	a.mu.Lock()
	for _, i := range sink.tools {
		if a.entries[i].Running {
			a.entries[i].Running = false
			if interrupted {
				a.entries[i].Result, a.entries[i].IsError = "interrupted by the user", true
			}
		}
	}
	a.cancel = nil
	a.turns++
	if !a.stopped {
		a.status, a.detail = Waiting, ""
	}
	stopped := a.stopped
	if final != "" {
		a.last = final
	}
	if err != nil {
		a.lastErr = err.Error()
		a.entries = append(a.entries, Entry{Kind: "error", Text: err.Error(), At: time.Now()})
	}
	queued := len(a.inbox) > 0
	a.mu.Unlock()
	a.changedNow()
	if interrupted && parent.Err() == nil {
		err = ErrInterrupted
	}
	if a.OnTurnEnd != nil && parent.Err() == nil && !stopped {
		a.OnTurnEnd(a, final, err)
	}
	if queued && !stopped {
		a.poke()
	}
}

type agentSink struct {
	a       *Agent
	cur     int
	curKind string
	tools   map[string]int
	started bool
}

func (s *agentSink) Delta(d Delta) {
	a := s.a
	a.mu.Lock()
	if !s.started || s.curKind != d.Kind {
		a.entries = append(a.entries, Entry{Kind: d.Kind, At: time.Now()})
		s.cur, s.curKind, s.started = len(a.entries)-1, d.Kind, true
	}
	a.entries[s.cur].Text += d.Text
	if d.Kind == "text" {
		a.detail = "writing"
	} else {
		a.detail = "thinking"
	}
	a.mu.Unlock()
	a.changedNow()
}

func (s *agentSink) ToolStart(id, name, label string) {
	a := s.a
	a.mu.Lock()
	a.entries = append(a.entries, Entry{Kind: "tool", Tool: name, Label: label, Running: true, At: time.Now()})
	s.tools[id] = len(a.entries) - 1
	s.started = false // the next text starts a new entry
	a.detail = name + " " + label
	a.mu.Unlock()
	a.changedNow()
}

func (s *agentSink) ToolDone(id, result string, isError bool) {
	a := s.a
	a.mu.Lock()
	if i, ok := s.tools[id]; ok {
		e := &a.entries[i]
		e.Running, e.Result, e.IsError = false, result, isError
		if e.Result == "" {
			e.Result = "(no output)"
		}
	}
	a.detail = "thinking"
	a.mu.Unlock()
	a.changedNow()
}

func (s *agentSink) Usage(u Usage) {
	s.a.mu.Lock()
	s.a.usage.Input += u.Input
	s.a.usage.Output += u.Output
	s.a.usage.CacheRead += u.CacheRead
	s.a.mu.Unlock()
}

func (s *agentSink) Approve(tool, label string) bool {
	if s.a.Approve == nil {
		return false
	}
	return s.a.Approve(s.a, &Tool{Spec: ToolSpec{Name: tool}, Approve: true}, label)
}

// Display is the name to show for the agent: its Label, else its Name.
func (a *Agent) Display() string {
	if a.Label != "" {
		return a.Label
	}
	return a.Name
}
