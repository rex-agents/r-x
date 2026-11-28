package rux

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/rex-agents/r-x/internal/harness"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// rux with no command opens rux's own multi-agent harness: you talk to a lead agent, it
// starts sub-agents in rox guests, and every agent's conversation is live in one window.

func runHarness(args []string) {
	fs := flag.NewFlagSet("rux", flag.ExitOnError)
	model := fs.String("model", "", "the lead's model, as provider/model, claude-code[/MODEL] or codex[/MODEL]")
	agentModel := fs.String("agent-model", "", "sub-agents' model (default: the lead's)")
	effort := fs.String("effort", "", "effort for Claude models: low, medium, high (default), xhigh or max")
	fs.Usage = func() { help(os.Stderr) }
	_ = fs.Parse(args)
	firstRunSetup()
	cfg := loadHarnessConfig()
	if *agentModel != "" {
		cfg.AgentModel = *agentModel
	}
	if *effort != "" {
		cfg.Effort = *effort
	}
	lead := cmpOr(*model, cfg.defaultLead())
	if err := checkModelKey(cfg, lead); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	r, err := newRun(cfg, lead)
	if err != nil {
		fail("rux: %v", err)
	}
	if _, err := r.serveControl(); err != nil {
		fmt.Fprintf(os.Stderr, "rux: no control socket, so panes won't work: %v\n", err)
	}
	zone.NewGlobal()
	m := newHarnessModel(r)
	if prompt := strings.TrimSpace(strings.Join(fs.Args(), " ")); prompt != "" {
		r.lead.Send("you", prompt)
	}
	var kk kittyKeys
	p, run := kk.run(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	go func() {
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-r.changed:
				p.Send(changedMsg{})
				time.Sleep(40 * time.Millisecond)
			case req := <-r.approvals:
				p.Send(approvalMsg{req})
			}
		}
	}()
	final, err := run()
	r.cancel()
	for _, a := range r.agents {
		a.Close()
	}
	if err != nil {
		fail("rux: %v", err)
	}
	fm := final.(*harnessModel)
	saved := saveSession(r)
	if fm.removeGuests {
		for _, g := range r.ownGuests() {
			fmt.Printf("%s Removing %s…\n", transcript.StAccent.Render("◆"), g)
			if out, err := exec.Command("rox", "down", g).CombinedOutput(); err != nil {
				fmt.Printf("  %s %s\n", lipgloss.NewStyle().Foreground(transcript.Red).Render("✗"), lastLine(ansiRe.ReplaceAllString(string(out), "")))
			}
		}
	} else if gs := r.ownGuests(); len(gs) > 0 {
		fmt.Printf("%s Kept %d guest%s: %s\n  Remove with %s\n", transcript.StAccent.Render("◆"), len(gs), plural(len(gs)),
			strings.Join(gs, ", "), transcript.StBold.Render("rox down NAME"))
	}
	if saved != "" {
		fmt.Printf("%s Transcripts saved in %s\n", transcript.StDim.Render("·"), saved)
	}
}

// checkModelKey stops early, with what to do, when the lead's model has no credentials.
func checkModelKey(cfg harnessConfig, spec string) error {
	if cli, _, ok := agentCLI(spec); ok {
		if _, err := exec.LookPath(cli); err != nil {
			if cli == "codex" {
				return fmt.Errorf("%s codex runs Codex, which isn't installed: npm install -g @openai/codex", transcript.StAccent.Render("◆"))
			}
			return fmt.Errorf("%s claude-code runs Claude Code, which isn't installed: https://code.claude.com/docs/en/setup", transcript.StAccent.Render("◆"))
		}
		return nil
	}
	name, _, err := splitModel(spec)
	if err != nil {
		return err
	}
	pc, ok := cfg.provider(name)
	if !ok || pc.Key == "" {
		return nil
	}
	if key, _ := apiKey(pc.Key); key != "" {
		return nil
	}
	if pc.Kind == "anthropic" {
		home, _ := os.UserHomeDir()
		if os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
			return nil
		}
		if _, err := os.Stat(filepath.Join(home, ".config", "anthropic")); err == nil {
			return nil
		}
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s rux's agents need a model, and %s has no API key yet.\n\n", transcript.StAccent.Render("◆"), name)
	if name == "anthropic" {
		fmt.Fprintf(b, "  Create a key at %s, then store it with rix:\n\n", transcript.StBold.Render("https://platform.claude.com/settings/keys"))
	} else {
		fmt.Fprintf(b, "  Store its key with rix:\n\n")
	}
	fmt.Fprintf(b, "    %s\n\n", transcript.StBold.Render("rix secret set "+pc.Key))
	fmt.Fprintf(b, "  Or run the agents on Claude Code with your Claude subscription: %s\n", transcript.StBold.Render("rux --model claude-code"))
	fmt.Fprintf(b, "  Or on Codex with your ChatGPT sign-in: %s\n", transcript.StBold.Render("rux --model codex"))
	fmt.Fprintf(b, "  Or use another provider: %s, %s, or a local %s.\n",
		transcript.StBold.Render("rux --model openai/gpt-5"), transcript.StBold.Render("openrouter/…"), transcript.StBold.Render("ollama/qwen3-coder"))
	return fmt.Errorf("%s", b.String())
}

type changedMsg struct{}
type approvalMsg struct{ req *approval }
type logoTick struct{}

// panel is what opens above the prompt.
type panel int

const (
	panelNone panel = iota
	panelPicker
	panelPalette
	panelShortcuts
)

type harnessModel struct {
	r     *hrun
	w, h  int
	sel   int
	vp    viewport.Model
	input textarea.Model
	tr    *transcript.Renderer

	expandAll bool
	expanded  map[string]map[int]bool // by agent ID, the tool calls opened with a click
	follow    bool
	pending   []*approval
	quitting  bool // asking whether to remove guests
	flash     string
	panel     panel
	picker    *modelPicker
	palette   *palette
	phase     float64 // the logo's light
	zones     map[string]func() tea.Cmd

	removeGuests bool
}

func newHarnessModel(r *hrun) *harnessModel {
	ta := textarea.New()
	ta.Placeholder = "Tell " + r.leadRef() + " what to do"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = transcript.StFaint
	ta.BlurredStyle = ta.FocusedStyle
	ta.Focus()
	return &harnessModel{r: r, input: ta, tr: transcript.New(), follow: true, vp: viewport.New(80, 20),
		expanded: map[string]map[int]bool{}, zones: map[string]func() tea.Cmd{}}
}

func (m *harnessModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.tr.Spin.Tick, tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return logoTick{} }))
}

func (m *harnessModel) agents() []*harness.Agent {
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	return append([]*harness.Agent(nil), m.r.agents...)
}

func (m *harnessModel) selected() *harness.Agent {
	as := m.agents()
	if m.sel >= len(as) {
		m.sel = len(as) - 1
	}
	return as[m.sel]
}

func (m *harnessModel) selectAgent(a *harness.Agent) {
	for i, x := range m.agents() {
		if x == a {
			m.sel = i
		}
	}
	m.follow = true
	m.refresh()
}

// welcoming is true before the first message, when rux shows the T-rex.
func (m *harnessModel) welcoming() bool {
	return m.sel == 0 && len(m.agents()) == 1 && len(m.r.lead.Snapshot().Entries) == 0
}

func (m *harnessModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.SetWidth(max(m.w-6, 10))
		m.refresh()
		return m, nil
	case changedMsg:
		m.refresh()
		return m, nil
	case logoTick:
		m.phase += 0.12
		return m, tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return logoTick{} })
	case approvalMsg:
		m.pending = append(m.pending, msg.req)
		m.selectAgent(msg.req.agent)
		return m, nil
	case modelsMsg:
		m.pickerModels(msg)
		return m, nil
	case spinner.TickMsg:
		var c tea.Cmd
		m.tr.Spin, c = m.tr.Spin.Update(msg)
		if m.anyBusy() {
			m.refresh()
		}
		return m, c
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case shiftEnterMsg:
		if len(m.pending) == 0 && !m.quitting && m.panel == panelNone {
			m.input.InsertString("\n")
			m.fitInput()
		}
		return m, nil
	case tea.KeyMsg:
		if c, done := m.key(msg); done {
			return m, c
		}
	}
	var c tea.Cmd
	m.input, c = m.input.Update(msg)
	cmds = append(cmds, c)
	m.fitInput()
	return m, tea.Batch(cmds...)
}

func (m *harnessModel) mouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
		for id, f := range m.zones {
			if zone.Get(id).InBounds(msg) {
				return f()
			}
		}
		return nil
	}
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		var c tea.Cmd
		m.vp, c = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return c
	}
	return nil
}

// click registers a clickable zone for this frame.
func (m *harnessModel) click(id, s string, f func() tea.Cmd) string {
	m.zones[id] = f
	return zone.Mark(id, s)
}

func (m *harnessModel) answer(ans approvalAnswer) {
	if len(m.pending) == 0 {
		return
	}
	m.pending[0].reply <- ans
	m.pending = m.pending[1:]
	m.refresh()
}

// key handles keys that aren't typing. It reports whether the key was used.
func (m *harnessModel) key(k tea.KeyMsg) (tea.Cmd, bool) {
	m.flash = ""
	if len(m.pending) > 0 {
		if ans, ok := map[string]approvalAnswer{"y": allowOnce, "a": allowAlways, "n": deny, "esc": deny}[k.String()]; ok {
			m.answer(ans)
		}
		return nil, true
	}
	if m.quitting {
		switch k.String() {
		case "y":
			m.removeGuests = true
			return tea.Quit, true
		case "n":
			return tea.Quit, true
		case "esc", "ctrl+c":
			m.quitting = false
		}
		return nil, true
	}
	switch m.panel {
	case panelPicker:
		return m.pickerKey(k), true
	case panelPalette:
		return m.paletteKey(k), true
	case panelShortcuts:
		m.panel = panelNone
		if k.String() == "esc" || k.String() == "?" {
			return nil, true
		}
	}
	switch k.String() {
	case "ctrl+c", "ctrl+d":
		if m.input.Value() != "" && k.String() == "ctrl+c" {
			m.input.Reset()
			m.fitInput()
			return nil, true
		}
		return m.quit(), true
	case "ctrl+o", "ctrl+k":
		m.openPalette()
		return nil, true
	case "ctrl+l":
		return m.openPicker(m.selected()), true
	case "?":
		if m.input.Value() == "" {
			m.panel = panelShortcuts
			return nil, true
		}
	case "tab", "ctrl+n":
		m.sel = (m.sel + 1) % len(m.agents())
		m.follow = true
		m.refresh()
		return nil, true
	case "shift+tab", "ctrl+p":
		n := len(m.agents())
		m.sel = (m.sel + n - 1) % n
		m.follow = true
		m.refresh()
		return nil, true
	case "esc":
		if m.selected().Interrupt() {
			m.flash = "Interrupted " + m.selected().Display()
		}
		return nil, true
	case "ctrl+e":
		m.expandAll = !m.expandAll
		m.refresh()
		return nil, true
	case "pgup", "pgdown", "ctrl+u", "ctrl+b", "ctrl+f", "home", "end":
		switch k.String() {
		case "pgup", "ctrl+u", "ctrl+b":
			m.vp.HalfViewUp()
		case "pgdown", "ctrl+f":
			m.vp.HalfViewDown()
		case "home":
			m.vp.GotoTop()
		case "end":
			m.vp.GotoBottom()
		}
		m.follow = m.vp.AtBottom()
		return nil, true
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		m.input.Reset()
		m.fitInput()
		if text == "" {
			return nil, true
		}
		if strings.HasPrefix(text, "/") {
			return m.command(text), true
		}
		m.selected().Send("you", text)
		m.follow = true
		m.refresh()
		return nil, true
	}
	return nil, false
}

func (m *harnessModel) quit() tea.Cmd {
	if len(m.r.ownGuests()) > 0 {
		m.quitting = true
		return nil
	}
	return tea.Quit
}

func (m *harnessModel) subByName(name string) *harness.Agent {
	if name == "" {
		if a := m.selected(); a.Role == "sub" {
			return a
		}
		return nil
	}
	return m.r.find(name)
}

func (m *harnessModel) command(text string) tea.Cmd {
	f := strings.Fields(text)
	arg := ""
	if len(f) > 1 {
		arg = f[1]
	}
	switch f[0] {
	case "/quit", "/exit", "/q":
		return m.quit()
	case "/stop":
		a := m.subByName(arg)
		if a == nil {
			m.flash = "Usage: /stop NAME (or select a sub-agent)"
			return nil
		}
		m.stopAgent(a)
	case "/model", "/models":
		if arg != "" {
			m.applyModel(m.selected(), arg, "-")
		} else {
			return m.openPicker(m.selected())
		}
	case "/agent-model":
		if arg != "" {
			m.applyModel(nil, arg, "-")
		} else {
			return m.openPicker(nil)
		}
	case "/effort":
		if _, ok := effortNames[arg]; !ok || arg == "" {
			m.flash = "Usage: /effort low|medium|high|xhigh|max|auto"
			if arg == "auto" {
				m.applyModel(m.selected(), m.selected().ModelName(), "")
			}
			return nil
		}
		m.applyModel(m.selected(), m.selected().ModelName(), arg)
	case "/pane", "/view":
		m.flash = m.openPane(m.subOrLead(arg))
	case "/shell":
		a := m.subByName(arg)
		if a == nil {
			m.flash = "Usage: /shell NAME (or select a sub-agent)"
			return nil
		}
		m.flash = m.openShell(a)
	case "/help":
		m.panel = panelShortcuts
	default:
		m.flash = "Unknown command. ctrl+o lists them all; ? shows shortcuts"
	}
	return nil
}

func (m *harnessModel) subOrLead(name string) *harness.Agent {
	if name == "lead" {
		return m.r.lead
	}
	if a := m.subByName(name); a != nil {
		return a
	}
	if name == "" {
		return m.selected()
	}
	return nil
}

func (m *harnessModel) stopAgent(a *harness.Agent) {
	m.flash = "Stopping " + a.Name + "…"
	go func() {
		out, err := m.r.stop(a.Name, true)
		if err != nil {
			out = err.Error()
		}
		m.r.lead.Note("note", out)
	}()
}

func (m *harnessModel) anyBusy() bool {
	for _, a := range m.agents() {
		if st, _ := a.Status(); st == harness.Working || st == harness.Starting {
			return true
		}
	}
	return false
}

func (m *harnessModel) fitInput() {
	lines := strings.Count(m.input.Value(), "\n") + 1
	if w := m.input.Width(); w > 0 {
		lines = max(lines, (lipgloss.Width(m.input.Value())+w-1)/w)
	}
	if h := min(max(lines, 1), 8); h != m.input.Height() {
		m.input.SetHeight(h)
	}
}

func (m *harnessModel) refresh() {
	if m.w == 0 {
		return
	}
	a := m.selected()
	if a.Role == "lead" {
		m.input.Placeholder = "Tell " + m.r.leadRef() + " what to do"
	} else {
		m.input.Placeholder = "Message " + a.Display()
	}
	if m.welcoming() {
		return
	}
	m.vp.Width = m.w - 4
	ex := m.expanded[a.ID]
	m.vp.SetContent(m.tr.Render(a.Snapshot(), m.vp.Width, m.expandAll, ex, "tool:"+a.ID+":"))
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *harnessModel) toggleEntry(a *harness.Agent, i int) tea.Cmd {
	if m.expanded[a.ID] == nil {
		m.expanded[a.ID] = map[int]bool{}
	}
	m.expanded[a.ID][i] = !m.expanded[a.ID][i]
	m.follow = false
	m.refresh()
	return nil
}

// ---- view ----

func (m *harnessModel) View() string {
	if m.w == 0 {
		return ""
	}
	m.zones = map[string]func() tea.Cmd{}
	a := m.selected()

	// Bottom up: the prompt, the agents, whatever panel is open.
	prompt := m.promptBox(a)
	var above []string
	switch {
	case len(m.pending) > 0:
		above = append(above, m.approvalPanel())
	case m.quitting:
		above = append(above, m.quitPanel())
	case m.panel == panelPicker:
		above = append(above, m.pickerView())
	case m.panel == panelPalette:
		above = append(above, m.paletteView())
	case m.panel == panelShortcuts:
		above = append(above, m.shortcutsView())
	}
	if strip := m.agentStrip(); strip != "" {
		above = append(above, strip)
	}
	if m.flash != "" {
		above = append(above, lipgloss.PlaceHorizontal(m.w, lipgloss.Right, transcript.StDim.Render(transcript.Truncate(m.flash, m.w-4))+"  "))
	}
	bottom := lipgloss.JoinVertical(lipgloss.Left, append(above, prompt)...)
	rest := max(m.h-lipgloss.Height(bottom), 3)

	var body string
	if m.welcoming() {
		body = lipgloss.Place(m.w, rest, lipgloss.Center, lipgloss.Center, m.welcome())
	} else {
		if m.vp.Height != rest-1 {
			m.vp.Height = rest - 1
			if m.follow {
				m.vp.GotoBottom()
			}
		}
		// Each tool call in view opens with a click.
		snap := a.Snapshot()
		for i, e := range snap.Entries {
			if transcript.Opens(e) {
				i := i
				m.zones[fmt.Sprintf("tool:%s:%d", a.ID, i)] = func() tea.Cmd { return m.toggleEntry(a, i) }
			}
		}
		body = "\n" + lipgloss.NewStyle().PaddingLeft(2).Render(m.vp.View())
		body = lipgloss.NewStyle().Height(rest).MaxHeight(rest).Render(body)
	}
	return zone.Scan(lipgloss.JoinVertical(lipgloss.Left, body, bottom))
}

func (m *harnessModel) welcome() string {
	r := m.r
	key := func(k string) string { return transcript.StBold.Render(k) }
	bar := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(transcript.Pink).PaddingLeft(1)
	row := func(k, v string) string {
		return transcript.StDim.Render(fmt.Sprintf("%-11s", k)) + transcript.StText.Render(v)
	}
	info := []string{
		row(r.leadName(), prettyModel(r.lead.ModelName())+effortSuffix(r.effortOf(r.lead))),
		row("sub-agents", prettyModel(r.agentModel())),
		row("directory", tildePath(r.cwd)),
	}
	if r.repo != "" {
		info = append(info, row("repo", repoDisplayName(r.repo)))
	}
	text := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Foreground(transcript.Pink).Bold(true).Render("Welcome to "+r.welcomeName()),
		m.click("w:palette", key("ctrl+o"), func() tea.Cmd { m.openPalette(); return nil })+transcript.StDim.Render(" for commands · ")+
			m.click("w:keys", key("?"), func() tea.Cmd { m.panel = panelShortcuts; return nil })+transcript.StDim.Render(" for shortcuts"),
		"",
		bar.Render(lipgloss.JoinVertical(lipgloss.Left,
			transcript.StDim.Render("Tell "+r.leadRef()+" what to do. It plans, then starts"),
			transcript.StDim.Render("sub-agents in their own rox guests."),
			"",
			strings.Join(info, "\n"),
		)),
	)
	logo := mascotLogo(r.mascot, m.phase)
	switch {
	case m.w >= lipgloss.Width(logo)+lipgloss.Width(text)+8:
		return lipgloss.JoinHorizontal(lipgloss.Center, logo, "    ", text)
	case m.h >= lipgloss.Height(logo)+lipgloss.Height(text)+8 && m.w >= lipgloss.Width(logo)+2:
		return lipgloss.JoinVertical(lipgloss.Center, logo, "", text)
	}
	return text
}

func tildePath(p string) string {
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// promptBox is the input, in a rounded box with the model on its top border and where the
// agent works on its bottom border. A click on the model opens the picker.
func (m *harnessModel) promptBox(a *harness.Agent) string {
	border := transcript.StFaint
	if len(m.pending) > 0 {
		border = lipgloss.NewStyle().Foreground(transcript.Yellow)
	}
	model := strings.ToLower(prettyModel(a.ModelName()))
	if e := m.r.effortOf(a); e != "" {
		model += " · " + strings.ToLower(effortNames[e])
	}
	left := ""
	if a.Role == "sub" {
		left = transcript.StText.Render(a.Name)
	}
	mark := func(s string) string {
		return m.click("prompt:model", s, func() tea.Cmd { return m.openPicker(m.selected()) })
	}
	return transcript.PromptBox(m.w, border, left, model, mark, m.whereLabel(a), m.input.View())
}

func (m *harnessModel) whereLabel(a *harness.Agent) string {
	if a.Role == "lead" {
		w := tildePath(m.r.cwd)
		if b := gitBranch(m.r.cwd); b != "" {
			w += " (" + b + ")"
		}
		return transcript.Truncate(w, m.w/2)
	}
	m.r.mu.Lock()
	s := m.r.subs[a.Name]
	m.r.mu.Unlock()
	if s == nil || s.Guest == "" {
		return "setting up"
	}
	w := s.Guest
	if s.Host != "" {
		w += " on " + s.Host
	}
	if s.Repo != "" {
		w += " (rux/" + a.Name + ")"
	}
	return transcript.Truncate(w, m.w/2)
}

var branchCache = map[string]string{}

func gitBranch(dir string) string {
	if b, ok := branchCache[dir]; ok {
		return b
	}
	out, _ := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	branchCache[dir] = strings.TrimSpace(string(out))
	return branchCache[dir]
}

func (m *harnessModel) glyph(st harness.Status, queued int) string {
	switch st {
	case harness.Working, harness.Starting:
		return m.tr.Spin.View()
	case harness.Waiting:
		if queued > 0 {
			return m.tr.Spin.View()
		}
		return lipgloss.NewStyle().Foreground(transcript.Green).Render("●")
	case harness.Failed:
		return lipgloss.NewStyle().Foreground(transcript.Red).Render("✗")
	}
	return transcript.StFaint.Render("○")
}

// agentStrip lists the agents above the prompt, once there are sub-agents. Click one to
// see it; the selected sub-agent has buttons for a Rex pane and a shell in its guest.
func (m *harnessModel) agentStrip() string {
	agents := m.agents()
	if len(agents) < 2 {
		return ""
	}
	var parts []string
	for i, a := range agents {
		snap := a.Snapshot()
		name := transcript.StDim.Render(a.Display())
		if i == m.sel {
			name = lipgloss.NewStyle().Foreground(transcript.Text).Bold(true).Underline(true).Render(a.Display())
		}
		item := m.glyph(snap.Status, snap.Queued) + " " + name
		if snap.Status == harness.Working && i != m.sel {
			item += transcript.StFaint.Render(" " + transcript.Truncate(snap.Detail, 18))
		}
		i := i
		parts = append(parts, m.click(fmt.Sprintf("agent:%d", i), item, func() tea.Cmd {
			m.sel = i
			m.follow = true
			m.refresh()
			return nil
		}))
	}
	strip := " " + strings.Join(parts, "   ")
	var btns []string
	if a := m.selected(); a.Role == "sub" {
		if os.Getenv("REX_SESSION") != "" {
			btns = append(btns, m.click("btn:pane", transcript.StDim.Render("⧉ pane"), func() tea.Cmd { m.flash = m.openPane(a); return nil }))
			btns = append(btns, m.click("btn:shell", transcript.StDim.Render("› shell"), func() tea.Cmd { m.flash = m.openShell(a); return nil }))
		}
		btns = append(btns, m.click("btn:stop", transcript.StDim.Render("■ stop"), func() tea.Cmd { m.stopAgent(a); return nil }))
	}
	right := strings.Join(btns, "  ") + "  "
	gap := m.w - lipgloss.Width(strip) - lipgloss.Width(right)
	if gap < 2 {
		return transcript.Truncate(strip, m.w)
	}
	return strip + strings.Repeat(" ", gap) + right
}

func button(label string, primary bool) string {
	st := lipgloss.NewStyle().Padding(0, 1).Foreground(transcript.Text).Background(lipgloss.Color("237"))
	if primary {
		st = st.Foreground(lipgloss.Color("#1a1a1a")).Background(transcript.Pink).Bold(true)
	}
	return st.Render(label)
}

func (m *harnessModel) panelBox(color lipgloss.Color, body string) string {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1).
		Width(m.w - 6).MarginLeft(2).Render(body)
}

func (m *harnessModel) approvalPanel() string {
	req := m.pending[0]
	head := lipgloss.NewStyle().Foreground(transcript.Yellow).Render(req.agent.Display() + " wants to run " + req.tool + " on this machine")
	btns := m.click("ap:yes", button("Allow  y", true), func() tea.Cmd { m.answer(allowOnce); return nil }) + " " +
		m.click("ap:all", button("Allow all this session  a", false), func() tea.Cmd { m.answer(allowAlways); return nil }) + " " +
		m.click("ap:no", button("Deny  n", false), func() tea.Cmd { m.answer(deny); return nil })
	return m.panelBox(transcript.Yellow, head+"\n"+transcript.StBold.Render(transcript.WrapText(req.label, m.w-12))+"\n\n"+btns)
}

func (m *harnessModel) quitPanel() string {
	gs := m.r.ownGuests()
	head := fmt.Sprintf("Remove the %d guest%s rux started? Unpushed work in them is lost.", len(gs), plural(len(gs)))
	btns := m.click("q:yes", button("Remove and quit  y", true), func() tea.Cmd { m.removeGuests = true; return tea.Quit }) + " " +
		m.click("q:no", button("Keep them and quit  n", false), func() tea.Cmd { return tea.Quit }) + " " +
		m.click("q:esc", button("Cancel  esc", false), func() tea.Cmd { m.quitting = false; return nil })
	return m.panelBox(transcript.Yellow, transcript.StText.Render(head)+"\n"+transcript.StFaint.Render(strings.Join(gs, ", "))+"\n\n"+btns)
}

func (m *harnessModel) shortcutsView() string {
	keys := [][2]string{
		{"enter", "send to the selected agent"}, {"shift+enter / alt+enter", "new line"},
		{"tab / shift+tab", "next / previous agent"}, {"esc", "interrupt the selected agent"},
		{"ctrl+o", "commands"}, {"ctrl+l", "model and effort"},
		{"ctrl+e", "open every tool call (or click one)"}, {"pgup / pgdn", "scroll (or the mouse wheel)"},
		{"ctrl+c", "clear the prompt, or quit"},
		{"/model /agent-model", "change models"}, {"/effort LEVEL", "change effort"},
		{"/pane [NAME]", "an agent in a Rex pane"}, {"/shell [NAME]", "a shell in its guest"},
		{"/stop [NAME]", "stop a sub-agent"},
	}
	var lines []string
	for _, k := range keys {
		lines = append(lines, transcript.StBold.Render(fmt.Sprintf("%-20s", k[0]))+transcript.StDim.Render(k[1]))
	}
	return m.panelBox(transcript.Faint, strings.Join(lines, "\n")+"\n"+transcript.StFaint.Render("any key closes"))
}

// saveSession writes every agent's transcript to ~/.rux/sessions/TIME/.
func saveSession(r *hrun) string {
	agents := r.agents
	used := false
	for _, a := range agents {
		if len(a.Snapshot().Entries) > 0 {
			used = true
		}
	}
	if !used {
		return ""
	}
	dir := filepath.Join(harnessLogDir(), time.Now().Format("2006-01-02T15-04-05"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	for _, a := range agents {
		snap := a.Snapshot()
		_ = saveJSON(filepath.Join(dir, a.Name+".json"), map[string]any{
			"name": a.Name, "role": a.Role, "model": a.ModelName(), "where": a.Where, "status": snap.Status,
			"usage": snap.Usage, "entries": snap.Entries,
		})
		_ = os.WriteFile(filepath.Join(dir, a.Name+".md"), []byte(transcriptText(snap.Entries, len(snap.Entries))), 0o600)
	}
	return dir
}
