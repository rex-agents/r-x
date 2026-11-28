package rux

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/harness"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// The model picker (ctrl+l, /model, or a click on the model): a few picks with what each is
// good for, every provider's models one step away or a search away, and the effort below.

type pickRow struct {
	spec, name, desc string
	other            bool // the "Other models" row
	provider         string
}

type modelPicker struct {
	agent     *harness.Agent // nil: the model new sub-agents start with
	mode      int            // 0: picks and search, 1: providers, 2: one provider's models
	picks     []pickRow
	catalog   map[string][]string
	loading   map[string]bool
	failed    map[string]string
	providers []providerRow
	provider  string
	filter    string
	cursor    int
	effort    string
	current   string
}

type modelsMsg struct {
	provider string
	models   []string
	err      error
}

func (r *hrun) picks() []pickRow {
	var rows []pickRow
	if _, err := exec.LookPath("claude"); err == nil {
		rows = append(rows,
			pickRow{spec: "claude-code/opus", desc: "Claude subscription · Claude Code's tools · deep feature work"},
			pickRow{spec: "claude-code/sonnet", desc: "Claude subscription · faster everyday work"})
	}
	if _, err := exec.LookPath("codex"); err == nil {
		rows = append(rows, pickRow{spec: "codex", desc: "ChatGPT sign-in · Codex's tools · hard bugs, audits"})
	}
	if key, _ := apiKey("ANTHROPIC_API_KEY"); key != "" {
		rows = append(rows,
			pickRow{spec: "anthropic/claude-opus-5-5", desc: "Anthropic API · rux's tools · billed per token"},
			pickRow{spec: "anthropic/claude-sonnet-5-5", desc: "Anthropic API · faster and cheaper"})
	}
	for i := range rows {
		rows[i].name = prettyModel(rows[i].spec)
	}
	return rows
}

func (m *harnessModel) openPicker(a *harness.Agent) tea.Cmd {
	p := &modelPicker{agent: a, picks: m.r.picks(), catalog: map[string][]string{}, loading: map[string]bool{},
		failed: map[string]string{}, providers: m.r.providerRows()}
	if a != nil {
		p.current, p.effort = a.ModelName(), m.r.effortOf(a)
	} else {
		p.current, p.effort = m.r.agentModel(), m.r.subEffort()
	}
	if !containsPick(p.picks, p.current) {
		p.picks = append([]pickRow{{spec: p.current, name: prettyModel(p.current)}}, p.picks...)
	}
	for i, r := range p.picks {
		if r.spec == p.current {
			p.cursor = i
		}
	}
	m.picker, m.panel = p, panelPicker
	// Every ready provider's models load in the background, for search.
	var cmds []tea.Cmd
	for _, pr := range p.providers {
		if pr.Ready {
			cmds = append(cmds, m.loadModels(p, pr.Name))
		}
	}
	return tea.Batch(cmds...)
}

func containsPick(rows []pickRow, spec string) bool {
	for _, r := range rows {
		if r.spec == spec {
			return true
		}
	}
	return false
}

func (m *harnessModel) loadModels(p *modelPicker, provider string) tea.Cmd {
	if p.loading[provider] || p.catalog[provider] != nil {
		return nil
	}
	p.loading[provider] = true
	return func() tea.Msg {
		models, err := m.r.listModels(m.r.ctx, provider)
		return modelsMsg{provider, models, err}
	}
}

func (m *harnessModel) pickerModels(msg modelsMsg) {
	p := m.picker
	if p == nil {
		return
	}
	p.loading[msg.provider] = false
	p.catalog[msg.provider] = msg.models
	if p.catalog[msg.provider] == nil {
		p.catalog[msg.provider] = []string{}
	}
	if msg.err != nil {
		p.failed[msg.provider] = msg.err.Error()
	}
}

// rows is what the picker lists now.
func (p *modelPicker) rows() []pickRow {
	f := strings.ToLower(strings.TrimSpace(p.filter))
	match := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(strings.ToLower(x), f) {
				return true
			}
		}
		return f == ""
	}
	var out []pickRow
	switch p.mode {
	case 0:
		for _, r := range p.picks {
			if match(r.spec, r.name, r.desc) {
				out = append(out, r)
			}
		}
		if f == "" {
			return append(out, pickRow{name: "Other models", desc: fmt.Sprintf("%d providers ›", len(p.providers)), other: true})
		}
		seen := map[string]bool{}
		for _, r := range out {
			seen[r.spec] = true
		}
		var names []string
		for prov := range p.catalog {
			names = append(names, prov)
		}
		sort.Strings(names)
		for _, prov := range names {
			for _, spec := range p.catalog[prov] {
				if !seen[spec] && match(spec) {
					seen[spec] = true
					out = append(out, pickRow{spec: spec, name: spec, desc: prov})
				}
			}
		}
	case 1:
		for _, pr := range p.providers {
			if match(pr.Name, pr.Status) {
				out = append(out, pickRow{name: pr.Name, desc: pr.Status, provider: pr.Name})
			}
		}
	case 2:
		for _, spec := range p.catalog[p.provider] {
			if match(spec) {
				out = append(out, pickRow{spec: spec, name: strings.TrimPrefix(spec, p.provider+"/")})
			}
		}
	}
	// A model nobody listed: use what was typed.
	if len(out) == 0 && f != "" && p.mode != 1 {
		spec := strings.TrimSpace(p.filter)
		if p.mode == 2 {
			spec = p.provider + "/" + strings.TrimPrefix(spec, p.provider+"/")
		}
		if strings.Contains(spec, "/") || spec == claudeCode || spec == codexEngine {
			out = append(out, pickRow{spec: spec, name: spec, desc: "use this model"})
		}
	}
	return out
}

func (m *harnessModel) choose(row pickRow) tea.Cmd {
	p := m.picker
	switch {
	case row.other:
		p.mode, p.filter, p.cursor = 1, "", 0
	case row.provider != "":
		for _, pr := range p.providers {
			if pr.Name == row.provider && !pr.Ready {
				m.flash = pr.Status
				return nil
			}
		}
		p.mode, p.provider, p.filter, p.cursor = 2, row.provider, "", 0
		return m.loadModels(p, row.provider)
	case row.spec != "":
		m.applyModel(p.agent, row.spec, p.effort)
		m.panel = panelNone
	}
	return nil
}

func (m *harnessModel) pickerKey(k tea.KeyMsg) tea.Cmd {
	p := m.picker
	rows := p.rows()
	switch k.String() {
	case "esc":
		if p.mode > 0 {
			p.mode, p.filter, p.cursor = p.mode-1, "", 0
			return nil
		}
		m.panel = panelNone
	case "ctrl+c", "ctrl+l":
		m.panel = panelNone
	case "up", "ctrl+p":
		p.cursor = max(p.cursor-1, 0)
	case "down", "ctrl+n", "tab":
		p.cursor = min(p.cursor+1, max(len(rows)-1, 0))
	case "left":
		p.effort = effortOrder[(indexOf(effortOrder, p.effort)+len(effortOrder)-1)%len(effortOrder)]
	case "right":
		p.effort = effortOrder[(indexOf(effortOrder, p.effort)+1)%len(effortOrder)]
	case "backspace":
		if p.filter != "" {
			p.filter, p.cursor = p.filter[:len(p.filter)-1], 0
		}
	case "enter":
		if len(rows) > 0 {
			return m.choose(rows[min(p.cursor, len(rows)-1)])
		}
	default:
		if k.Type == tea.KeyRunes {
			p.filter += string(k.Runes)
			p.cursor = 0
		}
	}
	return nil
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return 0
}

func (m *harnessModel) applyModel(a *harness.Agent, spec, effort string) {
	if a == nil {
		if err := m.r.setAgentModel(spec, effort); err != nil {
			m.flash = err.Error()
			return
		}
		m.flash = "New sub-agents will use " + prettyModel(spec) + effortSuffix(effort)
		return
	}
	go func() {
		if err := m.r.switchModel(a, spec, effort); err != nil {
			a.Note("error", "Couldn't switch to "+spec+": "+err.Error())
		}
		m.r.notify()
	}()
}

func (m *harnessModel) pickerView() string {
	p := m.picker
	width := m.w - 12
	nameW := min(26, width/3)
	rows := p.rows()
	visible := min(9, max(m.h/2-6, 4))
	start := max(0, p.cursor-visible+1)
	var lines []string
	if p.agent == nil {
		lines = append(lines, transcript.StDim.Render("For sub-agents started from now on"))
	}
	switch p.mode {
	case 1:
		lines = append(lines, transcript.StDim.Render("Providers"))
	case 2:
		head := p.provider + " models"
		if p.loading[p.provider] {
			head += " " + m.tr.Spin.View()
		}
		lines = append(lines, transcript.StDim.Render(head))
		if e := p.failed[p.provider]; e != "" {
			lines = append(lines, lipgloss.NewStyle().Foreground(transcript.Yellow).Render(transcript.Truncate("Couldn't list its models ("+e+"). Type one.", width)))
		}
	}
	for i := start; i < len(rows) && i < start+visible; i++ {
		r := rows[i]
		mark, st, descSt := "  ", transcript.StText, transcript.StDim
		if i == p.cursor {
			mark, st, descSt = transcript.StText.Render("▸ "), transcript.StBold, transcript.StText
		}
		desc := r.desc
		if r.spec != "" && r.spec == p.current {
			desc = strings.TrimPrefix(desc+" · current", " · ")
		}
		i := i
		line := mark + st.Render(fmt.Sprintf("%-*s", nameW, transcript.Truncate(r.name, nameW-1))) + descSt.Render(transcript.Truncate(desc, width-nameW-2))
		lines = append(lines, m.click(fmt.Sprintf("pick:%d", i), line, func() tea.Cmd {
			p.cursor = i
			return m.choose(rows[i])
		}))
	}
	if len(rows) == 0 {
		lines = append(lines, transcript.StDim.Render("  nothing matches"))
	}
	if more := len(rows) - (start + visible); more > 0 {
		lines = append(lines, transcript.StFaint.Render(fmt.Sprintf("  … %d more", more)))
	}
	lines = append(lines, transcript.StFaint.Render(strings.Repeat("─", width)))
	var opts []string
	for _, e := range effortOrder {
		st := transcript.StDim
		if e == p.effort {
			st = transcript.StBold
		}
		e := e
		opts = append(opts, m.click("effort:"+cmpOr(e, "auto"), st.Render(effortNames[e]), func() tea.Cmd { p.effort = e; return nil }))
	}
	lines = append(lines, "  "+transcript.StText.Render(fmt.Sprintf("%-*s", nameW, "Effort"))+strings.Join(opts, "  "))
	hint := "↑↓ model   ←→ effort   type to search"
	if p.mode > 0 {
		hint += "   esc back"
	}
	lines = append(lines, "  "+transcript.StFaint.Render(hint)+"  "+p.filter+transcript.StAccent.Render("▏"))
	return m.panelBox(transcript.Faint, strings.Join(lines, "\n"))
}
