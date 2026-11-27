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
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/harness"
)

// rux with no command opens rux's own multi-agent harness: you talk to a lead agent, it
// starts sub-agents in rox guests, and every agent's conversation is live in one window.

var (
	hAccent  = lipgloss.Color("141")
	hGreen   = lipgloss.Color("78")
	hYellow  = lipgloss.Color("221")
	hRed     = lipgloss.Color("203")
	hDim     = lipgloss.Color("244")
	hFaint   = lipgloss.Color("238")
	hText    = lipgloss.Color("252")
	stDim    = lipgloss.NewStyle().Foreground(hDim)
	stFaint  = lipgloss.NewStyle().Foreground(hFaint)
	stAccent = lipgloss.NewStyle().Foreground(hAccent)
	stBold   = lipgloss.NewStyle().Bold(true).Foreground(hText)
)

func runHarness(args []string) {
	fs := flag.NewFlagSet("rux", flag.ExitOnError)
	model := fs.String("model", "", "the lead's model, as provider/model or claude-code[/MODEL] (default: "+defaultModel+" with an API key, else claude-code)")
	agentModel := fs.String("agent-model", "", "sub-agents' model (default: the lead's)")
	effort := fs.String("effort", "", "effort for Claude models: low, medium, high (default), xhigh or max")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: rux [--model PROVIDER/MODEL] [--agent-model PROVIDER/MODEL] [PROMPT...]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
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
	m := newHarnessModel(r)
	if prompt := strings.TrimSpace(strings.Join(fs.Args(), " ")); prompt != "" {
		r.lead.Send("you", prompt)
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
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
	final, err := p.Run()
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
			fmt.Printf("%s Removing %s…\n", stAccent.Render("◆"), g)
			if out, err := exec.Command("rox", "down", g).CombinedOutput(); err != nil {
				fmt.Printf("  %s %s\n", lipgloss.NewStyle().Foreground(hRed).Render("✗"), lastLine(ansiRe.ReplaceAllString(string(out), "")))
			}
		}
	} else if gs := r.ownGuests(); len(gs) > 0 {
		fmt.Printf("%s Kept %d guest%s: %s\n  Remove with %s\n", stAccent.Render("◆"), len(gs), plural(len(gs)),
			strings.Join(gs, ", "), stBold.Render("rox down NAME"))
	}
	if saved != "" {
		fmt.Printf("%s Transcripts saved in %s\n", stDim.Render("·"), saved)
	}
}

// checkModelKey stops early, with what to do, when the lead's model has no credentials.
func checkModelKey(cfg harnessConfig, spec string) error {
	if _, ok := claudeCodeModel(spec); ok {
		if _, err := exec.LookPath("claude"); err != nil {
			return fmt.Errorf("%s claude-code runs Claude Code, which isn't installed: https://code.claude.com/docs/en/setup", stAccent.Render("◆"))
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
	fmt.Fprintf(b, "%s rux's agents need a model, and %s has no API key yet.\n\n", stAccent.Render("◆"), name)
	if name == "anthropic" {
		fmt.Fprintf(b, "  Create a key at %s, then store it with rix:\n\n", stBold.Render("https://platform.claude.com/settings/keys"))
	} else {
		fmt.Fprintf(b, "  Store its key with rix:\n\n")
	}
	fmt.Fprintf(b, "    %s\n\n", stBold.Render("rix secret set "+pc.Key))
	fmt.Fprintf(b, "  Or run the agents on Claude Code with your Claude subscription: %s\n", stBold.Render("rux --model claude-code"))
	fmt.Fprintf(b, "  Or use another provider: %s, %s, or a local %s.\n",
		stBold.Render("rux --model openai/gpt-5"), stBold.Render("openrouter/…"), stBold.Render("ollama/qwen3-coder"))
	return fmt.Errorf("%s", b.String())
}

type changedMsg struct{}
type approvalMsg struct{ req *approval }

type harnessModel struct {
	r        *hrun
	w, h     int
	sel      int
	vp       viewport.Model
	input    textarea.Model
	spin     spinner.Model
	expand   bool
	follow   bool
	pending  []*approval
	quitting bool // asking whether to remove guests
	flash    string
	md       *glamour.TermRenderer
	mdWidth  int
	mdCache  map[string]string

	removeGuests bool
}

func newHarnessModel(r *hrun) *harnessModel {
	ta := textarea.New()
	ta.Placeholder = "Message the lead…"
	ta.ShowLineNumbers = false
	ta.Prompt = "❯ "
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Prompt = stAccent
	ta.FocusedStyle.Placeholder = stFaint
	ta.Focus()
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(hYellow)))
	return &harnessModel{r: r, input: ta, spin: sp, follow: true, vp: viewport.New(80, 20), mdCache: map[string]string{}}
}

func (m *harnessModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spin.Tick)
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

func (m *harnessModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
	case changedMsg:
		m.refresh()
	case approvalMsg:
		m.pending = append(m.pending, msg.req)
		for i, a := range m.agents() {
			if a == msg.req.agent {
				m.sel = i
			}
		}
		m.refresh()
	case spinner.TickMsg:
		var c tea.Cmd
		m.spin, c = m.spin.Update(msg)
		cmds = append(cmds, c)
		if m.anyBusy() {
			m.refresh()
		}
	case tea.MouseMsg:
		var c tea.Cmd
		m.vp, c = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, c
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

// key handles keys that aren't typing. It reports whether the key was used.
func (m *harnessModel) key(k tea.KeyMsg) (tea.Cmd, bool) {
	m.flash = ""
	if len(m.pending) > 0 {
		req := m.pending[0]
		ans, ok := map[string]approvalAnswer{"y": allowOnce, "a": allowAlways, "n": deny, "esc": deny}[k.String()]
		if ok {
			req.reply <- ans
			m.pending = m.pending[1:]
			m.refresh()
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
	switch k.String() {
	case "ctrl+c", "ctrl+d":
		if m.input.Value() != "" && k.String() == "ctrl+c" {
			m.input.Reset()
			return nil, true
		}
		if len(m.r.ownGuests()) > 0 {
			m.quitting = true
			return nil, true
		}
		return tea.Quit, true
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
			m.flash = "Interrupted " + m.selected().Name
		}
		return nil, true
	case "ctrl+o":
		m.expand = !m.expand
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

func (m *harnessModel) command(text string) tea.Cmd {
	f := strings.Fields(text)
	switch f[0] {
	case "/quit", "/exit", "/q":
		if len(m.r.ownGuests()) > 0 {
			m.quitting = true
			return nil
		}
		return tea.Quit
	case "/stop":
		name := ""
		if len(f) > 1 {
			name = f[1]
		} else if a := m.selected(); a.Role == "sub" {
			name = a.Name
		}
		if name == "" {
			m.flash = "Usage: /stop NAME"
			return nil
		}
		go func() {
			out, err := m.r.stop(name, true)
			if err != nil {
				out = err.Error()
			}
			m.r.lead.Note("note", out)
		}()
		m.flash = "Stopping " + name + "…"
	case "/models":
		m.flash = "Providers: " + strings.Join(m.r.cfg.providerNames(), ", ") + ". Start with rux --model PROVIDER/MODEL"
	default:
		m.flash = "Commands: /stop [NAME], /models, /quit"
	}
	return nil
}

func (m *harnessModel) anyBusy() bool {
	for _, a := range m.agents() {
		if st, _ := a.Status(); st == harness.Working || st == harness.Starting {
			return true
		}
	}
	return false
}

func (m *harnessModel) sidebarWidth() int {
	switch {
	case m.w >= 110:
		return 30
	case m.w >= 80:
		return 24
	}
	return 0
}

func (m *harnessModel) fitInput() {
	lines := strings.Count(m.input.Value(), "\n") + 1
	if w := m.input.Width(); w > 0 {
		lines = max(lines, (len(m.input.Value())+w-1)/w)
	}
	h := min(max(lines, 1), 6)
	if h != m.input.Height() {
		m.input.SetHeight(h)
		m.layout()
	}
}

func (m *harnessModel) layout() {
	if m.w == 0 {
		return
	}
	main := m.w - m.sidebarWidth()
	m.input.SetWidth(m.w - 4)
	// header 1, input box (height+2), footer 1, tabs line when narrow
	used := 1 + m.input.Height() + 2 + 1
	if m.sidebarWidth() == 0 {
		used++
	}
	m.vp.Width, m.vp.Height = main-2, max(m.h-used, 3)
	if m.md == nil || m.mdWidth != m.vp.Width {
		style := styles.DarkStyleConfig
		zero := uint(0)
		style.Document.Margin = &zero
		style.Document.BlockPrefix, style.Document.BlockSuffix = "", ""
		style.Code.Prefix, style.Code.Suffix = "", ""
		m.md, _ = glamour.NewTermRenderer(glamour.WithStyles(style), glamour.WithWordWrap(max(m.vp.Width-4, 20)))
		m.mdWidth = m.vp.Width
		m.mdCache = map[string]string{}
	}
	m.refresh()
}

func (m *harnessModel) refresh() {
	if m.w == 0 {
		return
	}
	a := m.selected()
	m.vp.SetContent(m.renderTranscript(a))
	if m.follow {
		m.vp.GotoBottom()
	}
	if a.Role == "lead" {
		m.input.Placeholder = "Message the lead…"
	} else {
		m.input.Placeholder = "Message " + a.Name + " directly…"
	}
}

func (m *harnessModel) markdown(s string) string {
	if out, ok := m.mdCache[s]; ok {
		return out
	}
	out := s
	if m.md != nil {
		if r, err := m.md.Render(s); err == nil {
			out = strings.Trim(r, "\n")
		}
	}
	m.mdCache[s] = out
	return out
}

func wrapText(s string, w int) string {
	return lipgloss.NewStyle().Width(max(w, 10)).Render(s)
}

func hangIndent(s, first, rest string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = first + lines[i]
		} else {
			lines[i] = rest + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func (m *harnessModel) renderTranscript(a *harness.Agent) string {
	snap := a.Snapshot()
	w := m.vp.Width
	var out []string
	if len(snap.Entries) == 0 {
		if a.Role == "lead" {
			out = append(out, m.welcome())
		} else {
			out = append(out, stDim.Render("  "+cmpOr(snap.Detail, "starting")))
		}
	}
	for i, e := range snap.Entries {
		last := i == len(snap.Entries)-1
		switch e.Kind {
		case "user":
			from := cmpOr(e.From, "you")
			label := stAccent.Render("❯ ")
			body := wrapText(e.Text, w-4)
			if from != "you" {
				label = stDim.Render("◂ "+from) + "\n" + stAccent.Render("  ")
				if !m.expand && strings.Count(body, "\n") > 12 {
					body = strings.Join(strings.Split(body, "\n")[:12], "\n") + "\n" + stFaint.Render("… ctrl+o to expand")
				}
			}
			out = append(out, lipgloss.NewStyle().Foreground(hText).Render(hangIndent(body, label, "  ")))
		case "text":
			text := strings.TrimSpace(e.Text)
			if text == "" {
				continue
			}
			var body string
			if last && snap.Status == harness.Working {
				body = wrapText(text, w-4) // still streaming
			} else {
				body = m.markdown(text)
			}
			out = append(out, hangIndent(strings.TrimLeft(body, " "), lipgloss.NewStyle().Foreground(hText).Render("⏺ "), "  "))
		case "thinking":
			text := strings.TrimSpace(e.Text)
			head := stDim.Italic(true).Render("✻ Thinking")
			if text == "" {
				if last && snap.Status == harness.Working {
					out = append(out, head+stDim.Render("…"))
				}
				continue
			}
			body := wrapText(text, w-4)
			if !m.expand {
				lines := strings.Split(body, "\n")
				if len(lines) > 2 {
					body = strings.Join(lines[len(lines)-2:], "\n")
				}
			}
			out = append(out, head+"\n"+stFaint.Italic(true).Render(hangIndent(body, "  ", "  ")))
		case "tool":
			dot := lipgloss.NewStyle().Foreground(hGreen).Render("⏺")
			if e.Running {
				dot = m.spin.View()
			} else if e.IsError {
				dot = lipgloss.NewStyle().Foreground(hRed).Render("⏺")
			}
			label := strings.ReplaceAll(e.Label, "\n", " ")
			if lw := w - len(e.Tool) - 6; len(label) > lw && lw > 10 && !m.expand {
				label = label[:lw-1] + "…"
			}
			line := dot + " " + stBold.Render(e.Tool) + stDim.Render("("+label+")")
			if !e.Running {
				res := strings.TrimRight(e.Result, "\n")
				lines := strings.Split(res, "\n")
				show := 3
				if m.expand {
					show = 200
				}
				more := ""
				if len(lines) > show {
					more = stFaint.Render(fmt.Sprintf("… +%d lines (ctrl+o to expand)", len(lines)-show))
					lines = lines[:show]
				}
				for j := range lines {
					if len(lines[j]) > w-8 {
						lines[j] = lines[j][:max(w-9, 1)] + "…"
					}
				}
				st := stDim
				if e.IsError {
					st = lipgloss.NewStyle().Foreground(hRed)
				}
				body := st.Render(strings.Join(lines, "\n"))
				if more != "" {
					body += "\n" + more
				}
				line += "\n" + hangIndent(body, stFaint.Render("  ⎿  "), "     ")
			}
			out = append(out, line)
		case "note":
			out = append(out, stDim.Render(hangIndent(wrapText(e.Text, w-4), "  · ", "    ")))
		case "error":
			out = append(out, lipgloss.NewStyle().Foreground(hRed).Render(hangIndent(wrapText(e.Text, w-4), "  ✗ ", "    ")))
		}
	}
	if snap.Status == harness.Working {
		detail := cmpOr(snap.Detail, "working")
		if len(detail) > w-20 && w > 30 {
			detail = detail[:w-21] + "…"
		}
		out = append(out, m.spin.View()+" "+stDim.Render(detail+" · esc to interrupt"))
	} else if snap.Status == harness.Starting && len(snap.Entries) > 0 {
		out = append(out, m.spin.View()+" "+stDim.Render(cmpOr(snap.Detail, "starting")))
	}
	return strings.Join(out, "\n\n") + "\n"
}

func (m *harnessModel) welcome() string {
	r := m.r
	lines := []string{
		stAccent.Bold(true).Render("◆ rux") + stDim.Render("  multi-agent harness"),
		"",
		stDim.Render("Tell the lead what you want done. It plans the work and starts sub-agents, " +
			"each in its own sandboxed rox guest, and you watch them all here."),
		"",
		stDim.Render("  lead       ") + r.lead.Model,
		stDim.Render("  sub-agents ") + cmpOr(r.cfg.AgentModel, r.lead.Model),
		stDim.Render("  directory  ") + r.cwd,
	}
	if r.repo != "" {
		lines = append(lines, stDim.Render("  repo       ")+r.repo)
	}
	return wrapText(strings.Join(lines, "\n"), m.vp.Width-2)
}

func (m *harnessModel) statusGlyph(st harness.Status, queued int) string {
	switch st {
	case harness.Working, harness.Starting:
		return m.spin.View()
	case harness.Waiting:
		if queued > 0 {
			return m.spin.View()
		}
		return lipgloss.NewStyle().Foreground(hGreen).Render("●")
	case harness.Failed:
		return lipgloss.NewStyle().Foreground(hRed).Render("✗")
	default:
		return stFaint.Render("○")
	}
}

func (m *harnessModel) sidebar(height int) string {
	w := m.sidebarWidth()
	var rows []string
	rows = append(rows, stDim.Render(" AGENTS"), "")
	for i, a := range m.agents() {
		snap := a.Snapshot()
		name := a.Name
		if len(name) > w-6 {
			name = name[:w-7] + "…"
		}
		nameSt := lipgloss.NewStyle().Foreground(hText)
		if i == m.sel {
			nameSt = nameSt.Bold(true).Foreground(hAccent)
		}
		marker := " "
		if i == m.sel {
			marker = stAccent.Render("▌")
		}
		rows = append(rows, marker+m.statusGlyph(snap.Status, snap.Queued)+" "+nameSt.Render(name))
		sub := string(snap.Status)
		switch {
		case snap.Status == harness.Waiting && a.Role == "lead":
			sub = "ready"
		case snap.Status == harness.Waiting && snap.Turns > 0:
			sub = "done · waiting"
		case snap.Detail != "":
			sub = snap.Detail
		}
		if len(sub) > w-5 {
			sub = sub[:w-6] + "…"
		}
		rows = append(rows, "   "+stDim.Render(sub))
		if a.Role == "sub" {
			m.r.mu.Lock()
			s := m.r.subs[a.Name]
			guest := ""
			if s != nil {
				guest = s.Guest
			}
			m.r.mu.Unlock()
			meta := modelShort(a.Model)
			if guest != "" {
				meta += " · " + strings.TrimPrefix(guest, "rv-")
			}
			if len(meta) > w-5 {
				meta = meta[:w-6] + "…"
			}
			rows = append(rows, "   "+stFaint.Render(meta))
		}
		rows = append(rows, "")
	}
	tokens := int64(0)
	for _, a := range m.agents() {
		u := a.Snapshot().Usage
		tokens += u.Input + u.Output
	}
	for len(rows) < height-1 {
		rows = append(rows, "")
	}
	rows = rows[:max(height-1, 0)]
	rows = append(rows, stFaint.Render(fmt.Sprintf(" %s tokens", humanCount(tokens))))
	return lipgloss.NewStyle().Width(w).Height(height).BorderStyle(lipgloss.NormalBorder()).BorderRight(true).
		BorderForeground(hFaint).Render(strings.Join(rows, "\n"))
}

func modelShort(m string) string {
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		return m[i+1:]
	}
	return m
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func (m *harnessModel) tabs() string {
	var parts []string
	for i, a := range m.agents() {
		snap := a.Snapshot()
		name := a.Name
		if i == m.sel {
			name = stAccent.Bold(true).Render(name)
		} else {
			name = stDim.Render(name)
		}
		parts = append(parts, m.statusGlyph(snap.Status, snap.Queued)+" "+name)
	}
	return " " + strings.Join(parts, stFaint.Render("  │  "))
}

func (m *harnessModel) View() string {
	if m.w == 0 {
		return ""
	}
	a := m.selected()
	working, total := 0, 0
	for _, x := range m.agents()[1:] {
		total++
		if st, _ := x.Status(); st == harness.Working || st == harness.Starting {
			working++
		}
	}
	left := stAccent.Bold(true).Render(" ◆ rux") + stDim.Render("  "+a.Name+" · "+a.Model+" · "+cmpOr(a.Where, "setting up"))
	right := ""
	if total > 0 {
		right = stDim.Render(fmt.Sprintf("%d sub-agent%s · %d working ", total, plural(total), working))
	}
	if lipgloss.Width(left)+lipgloss.Width(right) >= m.w {
		left = lipgloss.NewStyle().MaxWidth(max(m.w-lipgloss.Width(right)-1, 10)).Render(left)
	}
	header := left + strings.Repeat(" ", max(m.w-lipgloss.Width(left)-lipgloss.Width(right), 1)) + right

	var bottom string
	switch {
	case len(m.pending) > 0:
		req := m.pending[0]
		label := req.label
		if len(label) > m.w-20 {
			label = label[:m.w-21] + "…"
		}
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(hYellow).Padding(0, 1).Width(m.w - 2)
		bottom = box.Render(lipgloss.NewStyle().Foreground(hYellow).Render(req.agent.Name+" wants to run "+req.tool+" on this machine:") +
			"\n" + stBold.Render(label) + "\n" + stDim.Render("y allow · a allow all this session · n deny"))
	case m.quitting:
		gs := m.r.ownGuests()
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(hYellow).Padding(0, 1).Width(m.w - 2)
		bottom = box.Render(fmt.Sprintf("Remove the %d guest%s rux started (%s)? Unpushed work in them is lost.\n", len(gs), plural(len(gs)), strings.Join(gs, ", ")) +
			stDim.Render("y remove and quit · n keep them and quit · esc cancel"))
	default:
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(hFaint).Padding(0, 1)
		bottom = box.Render(m.input.View())
	}
	footer := m.flash
	if footer == "" {
		hints := []string{"enter send", "tab switch agent", "esc interrupt", "ctrl+o details", "pgup/pgdn scroll", "/stop", "ctrl+c quit"}
		for len(hints) > 2 && len(strings.Join(hints, " · "))+1 > m.w {
			hints = append(hints[:len(hints)-2], hints[len(hints)-1])
		}
		footer = strings.Join(hints, " · ")
	}
	footer = stFaint.Render(wrapText(" "+footer, m.w))

	// The transcript gets the rows the rest leaves.
	vh := m.h - 1 - lipgloss.Height(bottom) - lipgloss.Height(footer)
	if m.sidebarWidth() == 0 {
		vh--
	}
	if vh = max(vh, 3); vh != m.vp.Height {
		m.vp.Height = vh
		if m.follow {
			m.vp.GotoBottom()
		}
	}
	body := lipgloss.NewStyle().PaddingLeft(1).Render(m.vp.View())
	if sw := m.sidebarWidth(); sw > 0 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebar(m.vp.Height), body)
	} else {
		body = m.tabs() + "\n" + body
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, body, bottom, footer)
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
			"name": a.Name, "role": a.Role, "model": a.Model, "where": a.Where, "status": snap.Status,
			"usage": snap.Usage, "entries": snap.Entries,
		})
		_ = os.WriteFile(filepath.Join(dir, a.Name+".md"), []byte(transcriptText(snap.Entries, len(snap.Entries))), 0o600)
	}
	return dir
}
