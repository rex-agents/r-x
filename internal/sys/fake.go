package sys

import (
	"errors"
	"io"
	"strings"
	"sync"
)

// Call is one command a Fake ran.
type Call struct {
	Argv  []string
	Stdin string
	Env   map[string]string
}

// Line is the call's command line, joined for matching in tests.
func (c Call) Line() string { return strings.Join(c.Argv, " ") }

// Fake records commands instead of running them. Handle decides each result.
type Fake struct {
	mu     sync.Mutex
	Calls  []Call
	Handle func(Call) Result
	// Tools on the fake PATH. Nil means every tool is there.
	Tools map[string]bool
}

// NewFake returns a Fake whose commands all succeed with no output unless a rule matches.
func NewFake() *Fake { return &Fake{} }

// Install makes f the runner until the returned function restores the previous one.
func (f *Fake) Install() func() {
	previous := R
	R = f
	return func() { R = previous }
}

func (f *Fake) record(c Cmd) Call {
	call := Call{Argv: append([]string(nil), c.Argv...), Env: c.Env}
	switch {
	case c.Stdin != nil:
		data, _ := io.ReadAll(c.Stdin)
		call.Stdin = string(data)
	default:
		call.Stdin = c.Input
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, call)
	f.mu.Unlock()
	return call
}

func (f *Fake) Run(c Cmd) (Result, error) {
	call := f.record(c)
	if f.Handle != nil {
		return f.Handle(call), nil
	}
	return Result{}, nil
}

type fakeProcess struct{}

func (fakeProcess) Pid() int            { return 4242 }
func (fakeProcess) Exited() (bool, int) { return false, 0 }
func (fakeProcess) Kill()               {}

func (f *Fake) Detach(c Cmd, _ string) (Process, error) {
	f.record(c)
	return fakeProcess{}, nil
}

func (f *Fake) Replace(argv []string, env map[string]string) error {
	f.record(Cmd{Argv: argv, Env: env})
	return nil
}

func (f *Fake) LookPath(name string) (string, error) {
	if f.Tools == nil || f.Tools[name] {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

// Ran returns the calls whose command line contains every fragment.
func (f *Fake) Ran(fragments ...string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
outer:
	for _, call := range f.Calls {
		line := call.Line()
		for _, fragment := range fragments {
			if !strings.Contains(line, fragment) {
				continue outer
			}
		}
		out = append(out, call)
	}
	return out
}
