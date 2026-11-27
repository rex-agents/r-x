package rux

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// rux monitor follows the sub-agents in this Rex session through Rex (program status and
// screen captures), with the guests and the host beside them. rux opens it in a short pane
// below the lead agent. rux never stands in for the agents: to talk to one, go to its pane.

// runMonitor runs the monitor.
func runMonitor() {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		usage()
	}
	if _, err := tea.NewProgram(newMonitor(), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "rux:", err)
		os.Exit(1)
	}
}

// ---- styles, matching rox and rix ----

var (
	cAccent  = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	cGood    = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"}
	cCaution = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	cBad     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	cMuted   = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	cFaint   = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"}

	sAccent = lipgloss.NewStyle().Foreground(cAccent)
	sBold   = lipgloss.NewStyle().Bold(true)
	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sGood   = lipgloss.NewStyle().Foreground(cGood)
	sWarn   = lipgloss.NewStyle().Foreground(cCaution)
	sBad    = lipgloss.NewStyle().Foreground(cBad)
	sMuted  = lipgloss.NewStyle().Foreground(cMuted)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sSel    = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sKey    = lipgloss.NewStyle().Foreground(cMuted).Bold(true)
	sBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(0, 1)
)

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func shortRepo(r string) string {
	if r == "" {
		return ""
	}
	return repoDisplayName(r)
}

func clip(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// keysLine is the key help as one string; View wraps it to the width.
func keys(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, sKey.Render(pairs[i])+" "+sMuted.Render(pairs[i+1]))
	}
	return strings.Join(parts, keySep)
}

var keySep = sFaint.Render("  ·  ")

// wrapKeys breaks key help between items so it fits width.
func wrapKeys(help string, width int) string {
	var lines []string
	line := ""
	for _, part := range strings.Split(help, keySep) {
		if line != "" && lipgloss.Width(line+keySep+part) > width {
			lines = append(lines, line)
			line = part
			continue
		}
		if line == "" {
			line = part
		} else {
			line += keySep + part
		}
	}
	return strings.Join(append(lines, line), "\n  ")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n > 0:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return "-"
}

func indent(s, p string) string { return p + strings.ReplaceAll(s, "\n", "\n"+p) }

var agentNames = map[string]string{"claude": "Claude Code", "codex": "Codex"}

// agentState is an agent's state in words, and the dot that shows it.
func agentState(a *subAgent) (string, string) {
	switch a.Phase {
	case "provisioning":
		return sWarn.Render("◐"), "starting its guest"
	case "connecting":
		return sWarn.Render("◐"), "starting " + agentNames[a.Agent]
	case "failed":
		return sBad.Render("●"), "failed"
	case "stopped":
		return sFaint.Render("·"), "stopped"
	case "exited":
		return sFaint.Render("·"), "pane closed"
	}
	switch {
	case a.state == "working":
		return sGood.Render("●"), "working"
	case a.state == "idle":
		return sGood.Render("○"), "waiting for you"
	case strings.HasPrefix(a.state, "blocked"):
		return sWarn.Render("●"), "needs you"
	case a.state == "done":
		return sGood.Render("✓"), "done"
	case a.state == "error":
		return sBad.Render("●"), "error"
	}
	return sMuted.Render("○"), "running"
}

func agentLive(a *subAgent) bool { return a.Phase != "stopped" && a.Phase != "exited" }

// ---- model ----

type tuiModel struct {
	sess, block string // the Rex session and this pane
	w, h        int
	spin        spinner.Model
	flash       string
	flashBad    bool
	fatal       string

	agents     []*subAgent
	hidden     int
	guests     []roxGuest
	stats      map[string]containerStat
	host       ovHost
	loaded     bool
	monSel     int
	monGuests  bool
	preview    string
	confirming string // the agent being stopped
	messaging  bool
	msg        textinput.Model
}

func newMonitor() *tuiModel {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = sAccent
	msg := textinput.New()
	msg.Placeholder = "a line to type into the agent's pane"
	msg.Prompt = sAccent.Render("› ")
	msg.CharLimit = 0
	m := &tuiModel{sess: os.Getenv("REX_SESSION"), block: os.Getenv("REX_BLOCK"), spin: sp, msg: msg}
	if m.sess == "" {
		m.fatal = "rux monitor follows the sub-agents in a Rex session, so run it in a Rex terminal."
	}
	return m
}

// ---- messages ----

type monitorMsg struct {
	hidden  int // finished agents too old to list
	agents  []*subAgent
	guests  []roxGuest
	stats   map[string]containerStat
	host    ovHost
	preview string
}
type tickMsg struct{}
type doneMsg struct {
	text string
	err  error
}

func (m *tuiModel) Init() tea.Cmd {
	if m.fatal != "" {
		return nil
	}
	return tea.Batch(m.spin.Tick, m.refresh(), tick())
}

// refresh reads the agents' states from Rex, the guests, the host, and the selected
// agent's screen.
func (m *tuiModel) refresh() tea.Cmd {
	sess := m.sess
	selected := ""
	if a := m.selectedAgent(); a != nil {
		selected = a.ID
	}
	return func() tea.Msg {
		out := monitorMsg{stats: map[string]containerStat{}}
		agents := listAgents(sess)
		done := make(chan struct{}, len(agents))
		for _, a := range agents {
			go func(a *subAgent) { a.refresh(); done <- struct{}{} }(a)
		}
		var guests []roxGuest
		guestsDone := make(chan struct{})
		go func() { guests, _ = listRoxGuests(); close(guestsDone) }()
		out.host = hostInfo()
		for _, st := range cachedContainerStats() {
			out.stats[st.ID] = st
		}
		go fetchContainerStats() // fresh numbers for the next refresh
		for range agents {
			<-done
		}
		<-guestsDone
		sort.SliceStable(agents, func(i, j int) bool {
			if agentLive(agents[i]) != agentLive(agents[j]) {
				return agentLive(agents[i])
			}
			return agents[i].Created.After(agents[j].Created)
		})
		// Finished agents stay listed for an hour; older ones are counted, not shown.
		var shown []*subAgent
		for _, a := range agents {
			if agentLive(a) || time.Since(cmpTime(a.Started, a.Created)) < time.Hour {
				shown = append(shown, a)
			} else {
				out.hidden++
			}
		}
		out.agents, out.guests = shown, guests
		if selected == "" && len(agents) > 0 {
			selected = agents[0].ID
		}
		for _, a := range agents {
			if a.ID == selected && a.Block != "" && agentLive(a) {
				if screen, err := rexOut("capture", "-s", sess, "-b", a.Block, "--trim"); err == nil {
					out.preview = screen
				}
			}
		}
		return out
	}
}

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *tuiModel) say(text string, bad bool) { m.flash, m.flashBad = text, bad }

// ruxCmd runs this rux with args, as rux agent's commands exit on errors.
func ruxCmd(stdin string, args ...string) (string, error) {
	cmd := exec.Command(selfExe(), args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", fmt.Errorf("%s", strings.TrimPrefix(lastLine(text), "rux: "))
	}
	return text, nil
}

// ---- update ----

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.msg.Width = msg.Width - 6
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())
	case monitorMsg:
		m.loaded = true
		m.agents, m.stats, m.host, m.preview, m.hidden = msg.agents, msg.stats, msg.host, msg.preview, msg.hidden
		if msg.guests != nil {
			m.guests = msg.guests
		}
		m.monSel = min(m.monSel, max(m.listLen()-1, 0))
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.say(msg.err.Error(), true)
		} else if msg.text != "" {
			m.say(msg.text, false)
		}
		return m, m.refresh()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || m.fatal != "" {
			return m, tea.Quit
		}
		return m.updateMonitor(msg)
	}
	return m, nil
}

// ---- monitor ----

func (m *tuiModel) listLen() int {
	if m.monGuests {
		return len(m.guests)
	}
	return len(m.agents)
}

func (m *tuiModel) selectedAgent() *subAgent {
	if !m.monGuests && m.monSel < len(m.agents) {
		return m.agents[m.monSel]
	}
	return nil
}

func (m *tuiModel) agentFor(guest string) *subAgent {
	for _, a := range m.agents {
		if a.Guest == guest && agentLive(a) {
			return a
		}
	}
	return nil
}

func (m *tuiModel) updateMonitor(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.messaging {
		switch k.String() {
		case "esc":
			m.messaging = false
			m.msg.Blur()
			return m, nil
		case "enter":
			a := m.selectedAgent()
			text := strings.TrimSpace(m.msg.Value())
			m.messaging = false
			m.msg.Blur()
			m.msg.SetValue("")
			if a == nil || text == "" {
				return m, nil
			}
			sess, id, name := m.sess, a.ID, a.Name
			return m, func() tea.Msg {
				if _, err := ruxCmd("", "agent", "send", "--session", sess, id, text); err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{text: "Sent to " + name}
			}
		}
		var cmd tea.Cmd
		m.msg, cmd = m.msg.Update(k)
		return m, cmd
	}
	if m.confirming != "" {
		id := m.confirming
		m.confirming = ""
		args := []string{"agent", "stop", "--session", m.sess, id}
		switch k.String() {
		case "y":
		case "k":
			args = append(args, "--keep-guest")
		default:
			m.say("Kept it running.", false)
			return m, nil
		}
		m.say("Stopping "+id+"…", false)
		return m, func() tea.Msg {
			out, err := ruxCmd("", args...)
			return doneMsg{text: lastLine(out), err: err}
		}
	}
	a := m.selectedAgent()
	switch k.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		m.monSel = max(m.monSel-1, 0)
		m.preview = ""
		return m, m.refresh()
	case "down", "j":
		m.monSel = min(m.monSel+1, max(m.listLen()-1, 0))
		m.preview = ""
		return m, m.refresh()
	case "tab":
		m.monGuests, m.monSel = !m.monGuests, 0
	case "n":
		if lead := leadBlock(m.sess); lead != "" && rexBlockAlive(m.sess, lead) {
			sess := m.sess
			return m, func() tea.Msg {
				if _, err := rexOut("focus", "-s", sess, lead); err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{}
			}
		}
		m.say("Start sub-agents from a lead: run rux in a Rex pane, and tell it what to do.", false)
	case "enter":
		if a != nil && a.Block != "" && agentLive(a) {
			sess, block := m.sess, a.Block
			return m, func() tea.Msg {
				if _, err := rexOut("focus", "-s", sess, block); err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{}
			}
		}
	case "m":
		if a != nil && a.Block != "" && agentLive(a) {
			m.messaging = true
			return m, m.msg.Focus()
		}
	case "x":
		if a != nil && agentLive(a) {
			m.confirming = a.ID
			if a.Shared || a.Guest == "" {
				m.say("Stop "+a.Name+" and close its pane? y to stop · esc to keep it", true)
			} else {
				m.say("Stop "+a.Name+"? y to stop and remove "+a.Guest+" · k to stop and keep the guest · esc to keep it", true)
			}
		}
	case "o":
		guest := ""
		if m.monGuests && m.monSel < len(m.guests) {
			guest = m.guests[m.monSel].Name
		} else if a != nil {
			guest = a.Guest
		}
		if guest != "" {
			return m, tea.ExecProcess(exec.Command("rox", "shell", guest), func(err error) tea.Msg {
				if err != nil {
					return doneMsg{err: fmt.Errorf("rox shell %s: %v", guest, err)}
				}
				return doneMsg{}
			})
		}
	case "r":
		return m, m.refresh()
	}
	return m, nil
}

// ---- view ----

func (m *tuiModel) View() string {
	if m.w == 0 {
		return ""
	}
	if m.fatal != "" {
		return "\n  " + sBad.Render("✗ "+m.fatal) + "\n\n  " + sMuted.Render("Press any key to quit.") + "\n"
	}
	body := m.viewMonitor()
	foot := keys("↑↓", "move", "enter", "go to pane", "m", "message", "x", "stop", "o", "shell", "n", "lead", "tab", "agents/guests", "q", "quit")
	if m.messaging {
		foot = m.msg.View() + "\n  " + keys("enter", "send", "esc", "cancel")
	}
	flash := ""
	if m.flash != "" {
		st := sGood
		if m.flashBad {
			st = sBad
		}
		flash = "  " + st.Render(clip(m.flash, m.w-4))
	}
	if m.messaging {
		parts := strings.SplitN(foot, "\n  ", 2)
		foot = parts[0] + "\n  " + wrapKeys(parts[1], m.w-4)
	} else {
		foot = wrapKeys(foot, m.w-4)
	}
	lines := strings.Count(body, "\n") + strings.Count(foot, "\n") + 1
	gap := max(m.h-lines-3, 0)
	return body + strings.Repeat("\n", gap) + "\n" + flash + "\n  " + foot
}

func (m *tuiModel) viewMonitor() string {
	var b strings.Builder
	fit := lipgloss.NewStyle().MaxWidth(max(m.w-2, 10))
	b.WriteString("\n  " + fit.Render(sTitle.Render("◆ rux")+"  "+m.hostLine()) + "\n")

	// agents
	live, needs := 0, 0
	for _, a := range m.agents {
		if agentLive(a) {
			live++
		}
		if strings.HasPrefix(a.state, "blocked") || a.state == "idle" {
			needs++
		}
	}
	title := fmt.Sprintf("Agents  %d", live)
	if needs > 0 {
		title += sWarn.Render(fmt.Sprintf("  %d waiting for you", needs))
	}
	b.WriteString("\n  " + m.sectionTitle(title, !m.monGuests) + "\n")
	if !m.loaded {
		b.WriteString("    " + m.spin.View() + sMuted.Render(" reading this Rex session…") + "\n")
	} else if len(m.agents) == 0 {
		b.WriteString("    " + sMuted.Render(clip("No agents running here. Press n to start one.", m.w-6)) + "\n")
	}

	room := max(min(len(m.agents), m.h/3), 1)
	start := 0
	if !m.monGuests && m.monSel >= room {
		start = m.monSel - room + 1
	}
	wide := m.w >= 110
	nameW := min(max(m.w-50, 12), 24)
	for i := start; i < len(m.agents) && i < start+room; i++ {
		a := m.agents[i]
		dot, state := agentState(a)
		cursor, st := "  ", lipgloss.NewStyle()
		if !m.monGuests && i == m.monSel {
			cursor, st = sSel.Render("› "), sSel
		}
		if !agentLive(a) {
			st = sFaint
		}
		where := a.Guest
		if where == "" {
			where = "new " + a.GuestType
		}
		detail := a.activity
		if a.Error != "" {
			detail = a.Error
		}
		if detail == "" {
			detail = shortRepo(a.Repo)
		}
		line := "  " + cursor + dot + " " + pad(st.Render(clip(a.Name, nameW)), nameW) + "  "
		if wide {
			line += sMuted.Render(pad(agentNames[a.Agent], 11)) + "  "
		}
		line += pad(clip(state, 16), 16) + "  " + sFaint.Render(pad(ago(cmpTime(a.Started, a.Created)), 4))
		if wide {
			line += "  " + sFaint.Render(pad(clip(where, 22), 22))
		}
		if rest := m.w - lipgloss.Width(line) - 4; rest > 10 {
			line += "  " + sMuted.Render(clip(detail, rest))
		}
		b.WriteString(line + "\n")
	}
	if m.hidden > 0 {
		b.WriteString("    " + sFaint.Render(clip(fmt.Sprintf("%d finished agents from earlier aren't shown", m.hidden), m.w-6)) + "\n")
	}

	// the selected agent's screen
	if a := m.selectedAgent(); a != nil && agentLive(a) {
		h := max(m.h-16-min(len(m.guests), 6)-room, 4)
		screen := tailLines(strings.TrimRight(m.preview, "\n"), h)
		if strings.TrimSpace(screen) == "" {
			screen = sMuted.Render("(nothing on its screen yet)")
		}
		var rows []string
		for _, l := range strings.Split(screen, "\n") {
			rows = append(rows, clip(l, m.w-8))
		}
		box := sBox.Width(m.w - 6).Render(strings.Join(rows, "\n"))
		b.WriteString("\n  " + sMuted.Render(a.Name+"'s pane") + "\n" + indent(box, "  ") + "\n")
	}

	// guests
	b.WriteString("\n  " + m.sectionTitle(fmt.Sprintf("Guests  %d", len(m.guests)), m.monGuests) + "\n")
	if m.loaded && len(m.guests) == 0 {
		b.WriteString("    " + sMuted.Render("No rox guests on this machine.") + "\n")
	}
	limit := 6
	if m.monGuests {
		limit = len(m.guests)
	}
	for i, g := range m.guests {
		if i >= limit {
			b.WriteString(sFaint.Render(fmt.Sprintf("    … %d more (tab to browse)", len(m.guests)-i)) + "\n")
			break
		}
		cursor, st := "  ", lipgloss.NewStyle()
		if m.monGuests && i == m.monSel {
			cursor, st = sSel.Render("› "), sSel
		}
		cpu, mem := "  -  ", "-"
		if s, ok := m.stats[g.Name]; ok {
			cpu, mem = fmt.Sprintf("%4.0f%%", s.CPU*100), humanBytes(s.MemBytes)
		}
		dot := sMuted.Render("◌")
		if g.Phase == "ready" {
			dot = sGood.Render("●")
		}
		used := sFaint.Render("no agent")
		if a := m.agentFor(g.Name); a != nil {
			used = sMuted.Render("agent " + a.Name)
		}
		line := "  " + cursor + dot + " " + pad(st.Render(g.Name), 22) + "  "
		if m.w >= 100 {
			line += sMuted.Render(pad(g.Backend, 10))
		}
		line += sMuted.Render(pad(g.Phase, 8)) + cpu + "  " + pad(mem, 8)
		if rest := m.w - lipgloss.Width(line) - 4; rest > 8 {
			line += "  " + clip(used, rest)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m *tuiModel) sectionTitle(t string, focused bool) string {
	if focused {
		return sBold.Render(t)
	}
	return sMuted.Render(t)
}

func (m *tuiModel) hostLine() string {
	h := m.host
	if h.Name == "" {
		return sMuted.Render("reading the stack…")
	}
	parts := []string{sBold.Render(h.Name)}
	if len(h.Load) > 0 && h.CPUs > 0 {
		st := sGood
		if h.Load[0]/float64(h.CPUs) > 0.8 {
			st = sWarn
		}
		parts = append(parts, st.Render(fmt.Sprintf("load %.1f", h.Load[0]))+sMuted.Render(fmt.Sprintf(" on %d CPUs", h.CPUs)))
	}
	if h.MemBytes > 0 {
		parts = append(parts, sMuted.Render(humanBytes(h.MemBytes)+" memory"))
	}
	// Drop details from the end until the line fits.
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, keySep))+10 > m.w {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, keySep)
}
