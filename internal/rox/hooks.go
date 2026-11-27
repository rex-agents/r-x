package rox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// HookScope limits which hooks run. "all" is the default; "git" runs only the Git hooks,
// for a launch on another host whose credentials come from the issuing machine's vault.
var HookScope = "all"

var gitHooks = map[string]bool{contract.PreFetch: true, contract.PreClone: true, contract.Resume: true, contract.Down: true}

// HookProvider is the program rox calls at lifecycle points, and where that choice came from.
// It's rix on PATH unless the config's "hooks" setting says "off" or names another program.
func HookProvider() (string, string) {
	if v := os.Getenv("ROX_HOOKS"); v != "" {
		return providerFrom(v, "ROX_HOOKS")
	}
	if c, err := LoadConfig(""); err == nil {
		if v, ok := c.Data["hooks"].(string); ok && v != "" {
			return providerFrom(v, "hooks in "+c.Path)
		}
	}
	return providerFrom("rix", "rix on PATH")
}

func providerFrom(value, where string) (string, string) {
	if value == "off" || value == "none" {
		return "", where
	}
	if p, err := sys.R.LookPath(value); err == nil {
		return p, where
	}
	return "", where
}

func hookPayload(event string, g *Guest, launch *contract.Launch) []byte {
	var record contract.Guest
	if g != nil {
		record = g.Record()
	} else {
		record = contract.Guest{Schema: contract.Schema, Name: "-", Agents: map[string]string{}, Projects: []contract.Project{}}
	}
	record.Launch = launch
	data, _ := json.Marshal(record)
	return append(data, '\n')
}

var hookTitles = map[string][2]string{
	contract.PostBoot:      {"Signing in agent CLIs", "rix"},
	contract.PreClone:      {"Starting the Git agent", "rix"},
	contract.PostProvision: {"Syncing agent setup", "rix"},
	contract.Resume:        {"Restoring Git access and credentials", "rix"},
	contract.Down:          {"Stopping the Git agent", "rix"},
}

// runHook calls the provider for event. Without a provider it does nothing. A failed
// pre-clone hook is an error; other failures are warnings.
func runHook(event string, g *Guest, launch *contract.Launch) (*contract.Reply, error) {
	provider, _ := HookProvider()
	if provider == "" || (HookScope == "git" && !gitHooks[event]) {
		return nil, nil
	}
	name := "-"
	if g != nil {
		name = g.Name
	}
	title := hookTitles[event]
	s := ui.Start("%s", title[0])
	s.Note(title[1])
	result, err := sys.Try(sys.Cmd{Argv: []string{provider, "hook", event, name}, Input: string(hookPayload(event, g, launch)),
		Timeout: 10 * time.Minute, Env: map[string]string{"ROX_BIN": selfPath()}, KeepStdin: false})
	if err == nil && result.Code != 0 {
		err = &sys.ExitError{Argv: []string{provider}, Result: result, Message: fmt.Sprintf("%s hook %s failed for %s", sys.Base(provider), event, name)}
	}
	var reply contract.Reply
	if err == nil {
		if jerr := json.Unmarshal([]byte(lastJSONLine(result.Stdout)), &reply); jerr != nil {
			err = fmt.Errorf("%s hook %s printed something other than JSON", sys.Base(provider), event)
		}
	}
	if err != nil {
		if event == contract.PreClone {
			s.Fail(err)
			return nil, err
		}
		s.Warn("%s: %s", title[0], firstLine(err.Error()))
		var out interface{ Output() string }
		if errors.As(err, &out) {
			ui.Tail(out.Output(), 6)
		}
		return nil, nil
	}
	s.End()
	for _, line := range reply.Done {
		ui.Done("%s %s", line, ui.Muted("rix"))
	}
	for _, line := range reply.Warnings {
		ui.Warn("%s", line)
	}
	return &reply, nil
}

func lastJSONLine(out string) string {
	lines := sys.Lines(out)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "{") {
			return lines[i]
		}
	}
	return "{}"
}

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "rox"
	}
	return p
}

// Lease is an SSH agent and Git server pins rix lends the launcher, for as long as rox holds it.
type Lease struct {
	Socket     string
	KnownHosts string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
}

// Close returns the lease, and rix stops its agent.
func (l *Lease) Close() {
	if l == nil || l.cmd == nil {
		return
	}
	l.stdin.Close()
	done := make(chan struct{})
	go func() { _ = l.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = l.cmd.Process.Kill()
	}
}

// fetchLease asks rix for Git access to read a branch on the launcher. rix answers with one
// JSON line, then keeps its agent until rox closes the hook's stdin. Without rix, nil.
func fetchLease(launch *contract.Launch) *Lease {
	provider, _ := HookProvider()
	if provider == "" || (HookScope != "all" && !gitHooks[contract.PreFetch]) {
		return nil
	}
	cmd := exec.Command(provider, "hook", contract.PreFetch, "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil
	}
	if _, err := stdin.Write(hookPayload(contract.PreFetch, nil, launch)); err != nil {
		_ = cmd.Process.Kill()
		return nil
	}
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(60 * time.Second):
	}
	var reply contract.Reply
	if json.Unmarshal([]byte(line), &reply) != nil {
		stdin.Close()
		_ = cmd.Wait()
		if msg := firstLine(stderr.String()); msg != "" {
			ui.Warn("rix couldn't lend Git access for reading the branch: %s", msg)
		}
		return nil
	}
	for _, w := range reply.Warnings {
		ui.Warn("%s", w)
	}
	return &Lease{Socket: reply.AgentSocket, KnownHosts: reply.KnownHosts, cmd: cmd, stdin: stdin}
}

// ShowHooks prints which hook provider is active.
func ShowHooks() error {
	provider, where := HookProvider()
	if provider == "" {
		ui.Card("Hooks", "", []Field{{Label: "Provider", Value: ui.Muted("none"), Note: where}, {Label: "Effect", Value: "Git keys, agent credentials and setup sync are off"}})
		ui.Hint("Install rix to give guests Git access and signed-in agents")
		return nil
	}
	version := "unknown"
	if out, err := sys.OutputWith(sys.Cmd{Argv: []string{provider, "--version"}, Timeout: 10 * time.Second}); err == nil {
		version = firstLine(out)
	}
	ui.Card("Hooks", "active", []Field{{Label: "Provider", Value: ui.Code(provider), Note: where}, {Label: "Version", Value: version},
		{Label: "Events", Value: strings.Join(contract.Events, ", ")}})
	return nil
}

// Field re-exports ui.Field for brevity.
type Field = ui.Field
