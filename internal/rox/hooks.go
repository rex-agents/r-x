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

// DefaultProviders are the hook programs rox looks for on PATH when nothing says otherwise.
var DefaultProviders = []string{"rix", "rox-rex"}

// Provider is one program that answers rox's hook events.
type Provider struct {
	// Name is the program's base name, such as rix or rox-rex.
	Name string
	// Path is the program's location.
	Path string
}

// providerEvents are the events rox sends a provider it knows, so rix keeps working unchanged
// and rox-rex isn't started for events it has nothing to do with. Any other provider gets
// every event and answers {} to the ones it doesn't know.
var providerEvents = map[string][]string{
	"rix":     contract.LegacyEvents,
	"rox-rex": {contract.PostBoot, contract.PostProvision, contract.Resume, contract.Down, contract.OpenShell},
}

// wants reports whether rox sends the provider this event.
func (p Provider) wants(event string, hasAgents bool) bool {
	events, ok := providerEvents[p.Name]
	if !ok {
		return true
	}
	if !contains(events, event) {
		return false
	}
	// rix has nothing to sign in until agents were requested.
	return !(p.Name == "rix" && event == contract.PostBoot && !hasAgents)
}

// ParseProviderNames reads the "hooks" setting: a comma-separated string or a list of names.
// "off" or "none" alone means no providers. ok is false when the value isn't a string or list.
func ParseProviderNames(value any) (names []string, ok bool) {
	var raw []string
	switch v := value.(type) {
	case string:
		raw = strings.Split(v, ",")
	case []any:
		for _, item := range v {
			s, isString := item.(string)
			if !isString {
				return nil, false
			}
			raw = append(raw, s)
		}
	case []string:
		raw = v
	default:
		return nil, false
	}
	for _, name := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if name == "off" || name == "none" {
			return []string{}, true
		}
		names = append(names, name)
	}
	return names, true
}

// HookProviders are the programs rox calls at lifecycle points, in call order, and where that
// choice came from. The order is ROX_HOOKS, then "hooks" in the config (a string or a list),
// then every default provider found on PATH. A named provider that isn't installed is skipped.
func HookProviders() ([]Provider, string) {
	if v := os.Getenv("ROX_HOOKS"); v != "" {
		names, _ := ParseProviderNames(v)
		return resolveProviders(names), "ROX_HOOKS"
	}
	if c, err := LoadConfig(""); err == nil {
		if names, ok := ParseProviderNames(c.Data["hooks"]); ok && c.Data["hooks"] != "" {
			return resolveProviders(names), "hooks in " + c.Path
		}
	}
	return resolveProviders(DefaultProviders), strings.Join(DefaultProviders, " and ") + " on PATH"
}

func resolveProviders(names []string) []Provider {
	providers := []Provider{}
	seen := map[string]bool{}
	for _, name := range names {
		path, err := sys.R.LookPath(name)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		providers = append(providers, Provider{Name: sys.Base(path), Path: path})
	}
	return providers
}

// providerNamed finds an active provider by name.
func providerNamed(name string) (Provider, bool) {
	providers, _ := HookProviders()
	for _, p := range providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// MergeReplies combines the replies of providers called in order: done and warnings are
// concatenated, and the first non-empty agent_socket (with its forward flag), known_hosts,
// session and endpoint win.
func MergeReplies(replies []*contract.Reply) *contract.Reply {
	merged := &contract.Reply{Schema: contract.Schema}
	for _, r := range replies {
		if r == nil {
			continue
		}
		merged.Done = append(merged.Done, r.Done...)
		merged.Warnings = append(merged.Warnings, r.Warnings...)
		if merged.AgentSocket == "" && r.AgentSocket != "" {
			merged.AgentSocket, merged.Forward = r.AgentSocket, r.Forward
		}
		if merged.KnownHosts == "" {
			merged.KnownHosts = r.KnownHosts
		}
		if merged.Session == "" {
			merged.Session = r.Session
		}
		if merged.Endpoint == "" {
			merged.Endpoint = r.Endpoint
		}
	}
	return merged
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

// payloadFor is the stdin a hook gets for a record that isn't a guest on this machine.
func payloadFor(record contract.Guest, launch *contract.Launch) []byte {
	record.Launch = launch
	data, _ := json.Marshal(record)
	return append(data, '\n')
}

// hookTitles are the steps rox shows while a provider answers an event. Providers without an
// entry get a generic title.
var hookTitles = map[string]map[string]string{
	"rix": {
		contract.PostBoot:      "Signing in agent CLIs",
		contract.PreClone:      "Starting the Git agent",
		contract.PostProvision: "Syncing agent setup",
		contract.Resume:        "Restoring Git access and credentials",
		contract.Down:          "Stopping the Git agent",
	},
	"rox-rex": {
		contract.PostBoot:      "Starting Rex in the guest",
		contract.PostProvision: "Setting up Rex sessions and hosts",
		contract.Resume:        "Reconnecting to Rex",
		contract.Down:          "Removing the Rex host",
		contract.OpenShell:     "Opening the guest in Rex",
	},
}

func hookTitle(p Provider, event string) string {
	if t := hookTitles[p.Name][event]; t != "" {
		return t
	}
	return fmt.Sprintf("Running the %s %s hook", p.Name, event)
}

// runHook calls each provider that wants event, in order, and merges their replies. Without a
// provider it does nothing and returns nil. A failed pre-clone hook is an error; other
// failures are warnings.
func runHook(event string, g *Guest, launch *contract.Launch) (*contract.Reply, error) {
	name := "-"
	if g != nil {
		name = g.Name
	}
	return runHookPayload(event, name, hookPayload(event, g, launch), g != nil && len(g.Agents) > 0)
}

// runHookPayload is runHook for a prepared record, which may describe a guest on another host.
func runHookPayload(event, name string, payload []byte, hasAgents bool) (*contract.Reply, error) {
	if HookScope == "git" && !gitHooks[event] {
		return nil, nil
	}
	providers, _ := HookProviders()
	var replies []*contract.Reply
	for _, p := range providers {
		if !p.wants(event, hasAgents) {
			continue
		}
		reply, err := callProvider(p, event, name, payload)
		if err != nil && event == contract.PreClone {
			return nil, err
		}
		if reply != nil {
			replies = append(replies, reply)
		}
	}
	if len(replies) == 0 {
		return nil, nil
	}
	return MergeReplies(replies), nil
}

// callProvider runs one provider's hook and shows what it did. A failure is shown as a warning
// and returned for pre-clone, which the caller makes fatal.
func callProvider(p Provider, event, name string, payload []byte) (*contract.Reply, error) {
	title := hookTitle(p, event)
	s := ui.Start("%s", title)
	s.Note(p.Name)
	result, err := sys.Try(sys.Cmd{Argv: []string{p.Path, "hook", event, name}, Input: string(payload),
		Timeout: 10 * time.Minute, Env: map[string]string{"ROX_BIN": selfPath()}, KeepStdin: false})
	if err == nil && result.Code != 0 {
		err = &sys.ExitError{Argv: []string{p.Path}, Result: result, Message: fmt.Sprintf("%s hook %s failed for %s", p.Name, event, name)}
	}
	var reply contract.Reply
	if err == nil {
		if jerr := json.Unmarshal([]byte(lastJSONLine(result.Stdout)), &reply); jerr != nil {
			err = fmt.Errorf("%s hook %s printed something other than JSON", p.Name, event)
		}
	}
	if err != nil {
		if event == contract.PreClone {
			s.Fail(err)
			return nil, err
		}
		s.Warn("%s: %s", title, firstLine(err.Error()))
		var out interface{ Output() string }
		if errors.As(err, &out) {
			ui.Tail(out.Output(), 6)
		}
		return nil, nil
	}
	s.End()
	for _, line := range reply.Done {
		ui.Done("%s %s", line, ui.Muted(p.Name))
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

// fetchLease asks the first provider that handles pre-fetch (rix) for Git access to read a
// branch on the launcher. It answers with one JSON line, then keeps its agent until rox closes
// the hook's stdin. Without such a provider, nil.
func fetchLease(launch *contract.Launch) *Lease {
	if HookScope != "all" && !gitHooks[contract.PreFetch] {
		return nil
	}
	providers, _ := HookProviders()
	for _, p := range providers {
		if !p.wants(contract.PreFetch, false) {
			continue
		}
		if lease := fetchLeaseFrom(p, launch); lease != nil {
			return lease
		}
	}
	return nil
}

func fetchLeaseFrom(p Provider, launch *contract.Launch) *Lease {
	cmd := exec.Command(p.Path, "hook", contract.PreFetch, "-")
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
			ui.Warn("%s couldn't lend Git access for reading the branch: %s", p.Name, msg)
		}
		return nil
	}
	for _, w := range reply.Warnings {
		ui.Warn("%s", w)
	}
	if reply.AgentSocket == "" && reply.KnownHosts == "" {
		stdin.Close()
		_ = cmd.Wait()
		return nil
	}
	return &Lease{Socket: reply.AgentSocket, KnownHosts: reply.KnownHosts, cmd: cmd, stdin: stdin}
}

// ShowHooks prints which hook providers are active, in call order.
func ShowHooks() error {
	providers, where := HookProviders()
	if len(providers) == 0 {
		ui.Card("Hooks", "", []Field{{Label: "Providers", Value: ui.Muted("none"), Note: where},
			{Label: "Effect", Value: "Git keys, agent credentials, setup sync and Rex are off"}})
		ui.Hint("Install rix to give guests Git access and signed-in agents, and rox-rex for Rex")
		return nil
	}
	fields := []Field{{Label: "Order", Value: strings.Join(providerNames(providers), ", "), Note: where}}
	for _, p := range providers {
		version := "unknown"
		if out, err := sys.OutputWith(sys.Cmd{Argv: []string{p.Path, "--version"}, Timeout: 10 * time.Second}); err == nil {
			version = firstLine(out)
		}
		events := contract.Events
		if known, ok := providerEvents[p.Name]; ok {
			events = known
		}
		fields = append(fields, Field{Label: p.Name, Value: ui.Code(p.Path), Note: version},
			Field{Label: "  events", Value: strings.Join(events, ", ")})
	}
	ui.Card("Hooks", "active", fields)
	return nil
}

func providerNames(providers []Provider) []string {
	var names []string
	for _, p := range providers {
		names = append(names, p.Name)
	}
	return names
}

// Field re-exports ui.Field for brevity.
type Field = ui.Field

// hasProvider reports whether a provider of that name is active.
func hasProvider(name string) bool {
	_, ok := providerNamed(name)
	return ok
}
