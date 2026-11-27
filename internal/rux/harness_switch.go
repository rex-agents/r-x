package rux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/harness"
)

// Every agent's engine (provider and model) can change during a session. The conversation
// so far goes along as context for the next turn, since engines don't share history.

// effortOf is an agent's effort: its own, else the configured default.
func (r *hrun) effortOf(a *harness.Agent) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.effort[a.ID]; ok {
		return e
	}
	return r.cfg.Effort
}

// agentModel is the model new sub-agents start with.
func (r *hrun) agentModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cmpOr(r.cfg.AgentModel, r.lead.ModelName())
}

func (r *hrun) setAgentModel(spec, effort string) error {
	if err := r.checkSpec(spec); err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg.AgentModel = spec
	if effort != "-" {
		r.subEffortV, r.subEffortSet = effort, true
	}
	r.mu.Unlock()
	return nil
}

// subEffort is the effort new sub-agents start with.
func (r *hrun) subEffort() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subEffortSet {
		return r.subEffortV
	}
	return r.cfg.Effort
}

// checkSpec checks that a model can be used: its provider exists and has a key. An agent
// CLI is checked where it runs, when it's attached.
func (r *hrun) checkSpec(spec string) error {
	if _, _, ok := agentCLI(spec); ok {
		return nil
	}
	_, err := r.cfg.newProvider(spec)
	return err
}

// attachLead gives the lead the engine spec names.
func (r *hrun) attachLead(spec string) error {
	lead := r.lead
	cli, model, isCLI := agentCLI(spec)
	effort := r.effortOf(lead)
	if !isCLI {
		p, err := r.cfg.newProviderEffort(spec, effort)
		if err != nil {
			return err
		}
		lead.Attach(p, r.leadSystem(""), append(harness.ReadTools(&harness.Local{WorkDir: r.cwd}), r.leadTools()...), "this machine")
		return nil
	}
	bin, err := exec.LookPath(cli)
	if err != nil {
		return fmt.Errorf("%s isn't installed on this machine", map[string]string{"claude": "Claude Code", "codex": "Codex"}[cli])
	}
	if r.mcp == nil {
		if r.mcp, err = r.serveLeadMCP(); err != nil {
			return err
		}
	}
	command := func(env ...string) func([]string) *exec.Cmd {
		return func(args []string) *exec.Cmd {
			cmd := exec.Command(bin, args...)
			cmd.Dir = r.cwd
			cmd.Env = append(os.Environ(), env...)
			return cmd
		}
	}
	if cli == "codex" {
		lead.AttachRunner(&harness.Codex{
			Command: command("RUX_MCP_TOKEN=" + r.mcp.token),
			Model:   model,
			Effort:  effort,
			System:  r.leadSystem("codex"),
			Config: []string{
				"mcp_servers.rux.url=" + tomlString(r.mcp.url),
				`mcp_servers.rux.bearer_token_env_var="RUX_MCP_TOKEN"`,
				// rux's own tools run without asking; nobody can approve in codex exec.
				`mcp_servers.rux.default_tools_approval_mode="approve"`,
				// wait_for_agents can take as long as the sub-agents do.
				"mcp_servers.rux.tool_timeout_sec=7200",
			},
		}, "this machine")
		return nil
	}
	lead.AttachRunner(&harness.ClaudeCode{
		// wait_for_agents can take as long as the sub-agents do.
		Command: command("MCP_TOOL_TIMEOUT=7200000"),
		Model:   model,
		Effort:  effort,
		System:  r.leadSystem("claude"),
		// Looking around and the rux tools run freely; Bash, edits and the rest ask you.
		AllowedTools: []string{"Read", "Grep", "Glob", "LS", "TodoWrite", "mcp__rux"},
		MCPConfig:    r.mcp.config(),
	}, "this machine")
	return nil
}

func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// attachSub gives a sub-agent whose guest is up the engine spec names.
func (r *hrun) attachSub(a *harness.Agent, s *subInfo, spec string) error {
	ex := s.exec
	if ex == nil {
		return errors.New(a.Name + "'s guest isn't up yet")
	}
	cli, model, isCLI := agentCLI(spec)
	effort := r.effortOf(a)
	if !isCLI {
		p, err := r.cfg.newProviderEffort(spec, effort)
		if err != nil {
			return err
		}
		a.Attach(p, r.subSystem(a.Name, s), harness.CodingTools(ex), "guest "+s.Guest)
		return nil
	}
	if out, code, err := ex.Run(r.ctx, "command -v "+cli, nil, time.Minute); err != nil || code != 0 {
		return fmt.Errorf("%s has no %s (%s). Add it with: rox add %s --agents %s", s.Guest, cli, cmpOr(lastLine(out), "not found"), s.Guest, cli)
	}
	// The CLI runs in the guest, in the checkout, over rox exec.
	command := func(args []string) *exec.Cmd {
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = harness.ShellQuote(arg)
		}
		ra := []string{"exec", s.Guest, "--stdin"}
		if ex.AgentSock != "" {
			ra = append(ra, "--agent-sock", ex.AgentSock)
		}
		ra = append(ra, "--", "bash", "-lc", "cd "+harness.ShellQuote(s.Dir)+" && exec "+cli+" "+strings.Join(quoted, " "))
		return exec.Command(ex.Rox, ra...)
	}
	if cli == "codex" {
		// Codex in a guest signs in with what rix auth codex stored; say so plainly when it's missing.
		if out, code, err := ex.Run(r.ctx, "codex login status 2>&1", nil, time.Minute); err == nil && code != 0 {
			return fmt.Errorf("Codex in %s isn't signed in (%s). Store your Codex sign-in with rix auth codex, then rix apply %s",
				s.Guest, cmpOr(lastLine(out), "not logged in"), s.Guest)
		}
		a.AttachRunner(&harness.Codex{Command: command, Model: model, Effort: effort, System: r.subSystem(a.Name, s), Bypass: true}, "guest "+s.Guest)
		return nil
	}
	a.AttachRunner(&harness.ClaudeCode{
		Command:         command,
		Model:           model,
		Effort:          effort,
		System:          r.subSystem(a.Name, s),
		SkipPermissions: true, // it's in a sandbox
	}, "guest "+s.Guest)
	return nil
}

// switchModel moves an agent to another engine, or effort, between turns. effort "-"
// keeps the agent's effort.
func (r *hrun) switchModel(a *harness.Agent, spec, effort string) error {
	if a.Busy() {
		return fmt.Errorf("%s is working; interrupt it (esc) or wait, then switch", a.Name)
	}
	if err := r.checkSpec(spec); err != nil {
		return err
	}
	from, fromEffort := a.ModelName(), r.effortOf(a)
	if effort == "-" {
		effort = fromEffort
	}
	if from == spec && effort == fromEffort {
		return nil
	}
	r.mu.Lock()
	r.effort[a.ID] = effort
	r.mu.Unlock()
	entries := a.Snapshot().Entries
	var err error
	if a.Role == "lead" {
		err = r.attachLead(spec)
	} else {
		r.mu.Lock()
		s := r.subs[a.Name]
		r.mu.Unlock()
		if s == nil {
			return errors.New("no such sub-agent")
		}
		if st, _ := a.Status(); st == harness.Starting {
			// Its guest is still coming up: start it on the new engine.
			if cli, _, ok := agentCLI(spec); ok && cli != s.CLI {
				return fmt.Errorf("its guest is being set up without %s; switch once it's up", cli)
			}
			r.mu.Lock()
			s.Model = spec
			r.mu.Unlock()
			a.SetModelName(spec)
			a.Note("note", "Will start on "+spec)
			return nil
		}
		err = r.attachSub(a, s, spec)
	}
	if err != nil {
		return err
	}
	if from == spec {
		// The same engine with another effort: a fresh session, so the context goes along.
		from = from + " at " + cmpOr(fromEffort, "default") + " effort"
	}
	if len(entries) > 0 {
		a.Carry("[rux: you're taking over this conversation from another model (" + from + "). Here's what happened so far, " +
			"most recent last. Carry on from it.]\n\n" + transcriptText(entries, 80))
	}
	a.Note("note", "Switched from "+from+" to "+a.ModelName()+effortSuffix(effort))
	return nil
}

// providerRow is a line in the model picker's provider list.
type providerRow struct {
	Name, Status string
	Ready        bool
}

func (r *hrun) providerRows() []providerRow {
	rows := []providerRow{}
	if _, err := exec.LookPath("claude"); err == nil {
		rows = append(rows, providerRow{claudeCode, "Claude Code itself, with your Claude subscription", true})
	} else {
		rows = append(rows, providerRow{claudeCode, "Claude Code isn't installed", false})
	}
	if _, err := exec.LookPath("codex"); err == nil {
		rows = append(rows, providerRow{codexEngine, "Codex itself, with your ChatGPT sign-in", true})
	} else {
		rows = append(rows, providerRow{codexEngine, "Codex isn't installed", false})
	}
	for _, n := range r.cfg.providerNames() {
		pc, _ := r.cfg.provider(n)
		row := providerRow{Name: n, Ready: true}
		switch {
		case pc.Key == "":
			row.Status = "local · " + pc.BaseURL
		default:
			if key, _ := apiKey(pc.Key); key != "" {
				row.Status = "key from rix"
			} else if n == "anthropic" && checkModelKey(r.cfg, "anthropic/x") == nil {
				row.Status = "signed in"
			} else {
				row.Status, row.Ready = "needs a key: rix secret set "+pc.Key, false
			}
		}
		rows = append(rows, row)
	}
	// The ones ready to use first.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Ready && !rows[j].Ready })
	return rows
}

// listModels lists a provider's models as full specs.
func (r *hrun) listModels(ctx context.Context, provider string) ([]string, error) {
	if provider == claudeCode {
		return []string{claudeCode, claudeCode + "/opus", claudeCode + "/sonnet", claudeCode + "/haiku"}, nil
	}
	if provider == codexEngine {
		return codexModels(ctx)
	}
	pc, ok := r.cfg.provider(provider)
	if !ok {
		return nil, fmt.Errorf("no provider named %s", provider)
	}
	key, _ := apiKey(pc.Key)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var ids []string
	var err error
	if pc.Kind == "anthropic" {
		ids, err = harness.ListAnthropicModels(ctx, key, pc.BaseURL)
	} else {
		ids, err = harness.ListOpenAIModels(ctx, pc.BaseURL, key, pc.Headers)
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = provider + "/" + id
	}
	return out, err
}

// codexModels lists the models Codex offers, from its catalog.
func codexModels(ctx context.Context) ([]string, error) {
	out := []string{codexEngine}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "codex", "debug", "models").Output()
	if err != nil {
		return out, err
	}
	var cat struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		return out, err
	}
	for _, m := range cat.Models {
		if m.Slug != "" && m.Visibility != "hide" {
			out = append(out, codexEngine+"/"+m.Slug)
		}
	}
	return out, nil
}

func effortSuffix(e string) string {
	if e == "" {
		return ""
	}
	return " at " + e + " effort"
}
