package authtui

import (
	"fmt"
	"github.com/rex-agents/r-x/internal/rux/authapi"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"

	"github.com/rex-agents/r-x/internal/rux/transcript"
)

const (
	cardH     = 6
	gutterW   = 3
	cardW     = 25
	logoW     = 42
	logoH     = 15
	previewW  = 32
	minPrevW  = 90 // narrower terminals drop the preview column
	sparkRune = "▁▂▃▄▅▆▇█"
	boxH      = 3 // the thread's input box
)

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func blank(w int) string { return strings.Repeat(" ", max(w, 0)) }

func (m *Model) View() string {
	if m.w < 24 || m.h < 7 {
		return "rux: terminal too small"
	}
	m.zones = map[string]func(){}
	var out []string
	out = append(out, m.header())
	banners := m.banners()
	out = append(out, banners...)
	out = append(out, blank(m.w))   // space under the top bar
	bodyH := max(m.h-len(out)-2, 1) // the footer, and a blank row above it
	var body []string
	switch {
	case m.setup != nil:
		body = center(m.setup.view(m.w), m.w, bodyH)
	case m.pform != nil:
		body = center(m.projFormView(), m.w, bodyH)
	case m.form != nil:
		body = center(m.formView(), m.w, bodyH)
	case m.help:
		body = center(m.helpView(), m.w, bodyH)
	case m.tab == tabThread:
		body = m.threadView(bodyH)
	case m.tab == tabMonitor:
		body = m.monitorView(bodyH)
	case m.tab == tabSettings:
		body = m.settingsView(bodyH)
	default:
		body = m.packView(bodyH)
	}
	out = append(out, body...)
	out = append(out, blank(m.w), m.footer())
	return zone.Scan(strings.Join(out, "\n"))
}

func center(box string, w, h int) []string {
	lines := strings.Split(box, "\n")
	top := max((h-len(lines))/2, 0)
	left := max((w-lipgloss.Width(box))/2, 0)
	out := make([]string, h)
	for i := range out {
		out[i] = blank(w)
		if j := i - top; j >= 0 && j < len(lines) {
			out[i] = fit(blank(left)+lines[j], w)
		}
	}
	return out
}

// ---- header and footer ----

func (m *Model) header() string {
	state, st := "live", "up"
	switch {
	case !m.live:
		state, st = "offline", "down"
	case m.pack.Degraded:
		state, st = "degraded", "provisioning"
	}
	vault := "vault ok"
	vst := "up"
	if m.havePack && !m.pack.VaultOK {
		vault, vst = "vault unreachable", "down"
	}
	// Right side pieces, dropped from the end when the width runs out.
	pieces := []string{dot(st) + " " + stText.Render(state)}
	if m.havePack {
		if m.pack.Leader != "" {
			pieces = append(pieces, stDim.Render("leader ")+stText.Render(m.pack.Leader))
		}
		pieces = append(pieces, stDim.Render(fmt.Sprintf("term %d", m.pack.Term)), dot(vst)+" "+stDim.Render(vault))
	}
	if up := m.authorityUptime(); up != "" {
		pieces = append(pieces, stDim.Render("up "+up))
	}
	pieces = append(pieces, stFaint.Render(m.c.Base))
	for {
		right := strings.Join(pieces, "  ")
		left, ok := m.headerLeft(m.w - 2 - lipgloss.Width(right))
		if ok || len(pieces) <= 1 {
			gap := max(m.w-lipgloss.Width(left)-lipgloss.Width(right), 1)
			return fit(left+blank(gap)+right, m.w)
		}
		// Keep the live dot first; drop the URL, then uptime, then the rest from the end.
		pieces = pieces[:len(pieces)-1]
	}
}

func (m *Model) banners() []string {
	var out []string
	warn := lipgloss.NewStyle().Foreground(hYellow)
	bad := lipgloss.NewStyle().Foreground(hRed)
	if m.pack.Degraded {
		out = append(out, warn.Render("▲ degraded: no pack leader is reachable, showing this authority only"))
	}
	if m.havePack && !m.pack.VaultOK {
		out = append(out, bad.Render("▲ vault unreachable: cached keys work until their TTL, new credentialed work will fail"))
	}
	if !m.live {
		out = append(out, warn.Render("▲ live feed disconnected, reconnecting…"))
	}
	if m.err != "" && !m.havePack {
		out = append(out, bad.Render("▲ "+m.err))
	}
	for i := range out {
		out[i] = fit(out[i], m.w)
	}
	return out
}

func (m *Model) breadcrumb() string {
	if m.tab == tabSettings {
		return stDim.Render("settings")
	}
	if m.tab == tabMonitor {
		var parts []string
		for c := 0; c <= m.mfocus; c++ {
			if n := m.mselected(c); n != nil {
				parts = append(parts, n.name)
			}
		}
		return stDim.Render("monitor") + stText.Render(strings.Join(append([]string{""}, parts...), " › "))
	}
	var parts []string
	for c := 0; c <= m.focus && c < numCols; c++ {
		if n := m.selected(c); n != nil {
			parts = append(parts, n.name)
		}
	}
	if len(parts) == 0 {
		return stDim.Render("pack")
	}
	return stDim.Render("pack › ") + stText.Render(strings.Join(parts, " › "))
}

func chip(k, label string) string {
	return lipgloss.NewStyle().Foreground(hText).Background(transcript.ChipBg).Padding(0, 1).Render(k) + " " + stDim.Render(label)
}

func (m *Model) footer() string {
	if m.setup != nil {
		return blank(m.w)
	}
	if m.downAsk != nil {
		return fit(lipgloss.NewStyle().Foreground(hYellow).Bold(true).Render(m.downAsk.text()), m.w)
	}
	if m.flash != "" {
		return fit(lipgloss.NewStyle().Foreground(hYellow).Render(m.flash), m.w)
	}
	var chips []string
	switch {
	case m.pform != nil && m.pform.tab == projLocal:
		chips = []string{chip("enter", "open"), chip("ctrl+a", "add"), chip("←", "up"), chip("tab", "github"), chip("esc", "cancel")}
	case m.pform != nil:
		chips = []string{chip("enter", "add"), chip("↑↓", "pick"), chip("tab", "local directory"), chip("esc", "cancel")}
	case m.form != nil:
		chips = []string{chip("enter", "create"), chip("tab", "field"), chip("ctrl+l", "local"), chip("esc", "cancel")}
	case m.tab == tabThread:
		esc := chip("esc", "back")
		if m.working() {
			esc = chip("esc", "interrupt")
		}
		chips = []string{chip("enter", "send"), esc, chip("ctrl+e", "expand"), chip("↑↓", "scroll"), chip("tab", "monitor"), chip("ctrl+c", "quit")}
	case m.tab == tabMonitor:
		chips = m.monitorChips()
	case m.tab == tabSettings:
		chips = m.settingsChips()
	default:
		chips = []string{chip("←↑↓→", "move"), chip("enter", "open"), chip("n", "new thread"), chip("p", "new project"), chip("tab", "monitor"),
			chip("a", map[bool]string{true: "hide archived", false: "archived"}[m.archived]), chip("?", "help"), chip("q", "quit")}
	}
	left := m.breadcrumb()
	if m.tab == tabThread { // the header already shows where we are
		left = ""
	}
	for len(chips) > 0 {
		right := strings.Join(chips, " ")
		if lipgloss.Width(left)+2+lipgloss.Width(right) <= m.w {
			gap := m.w - lipgloss.Width(left) - lipgloss.Width(right)
			return left + blank(gap) + right
		}
		chips = chips[:len(chips)-1]
	}
	return fit(left, m.w)
}

func (m *Model) threadPreview(id string, n *node, w int) []string {
	t := n.th
	kv := func(k, v string) string {
		return stFaint.Render(fmt.Sprintf("%-7s", k)) + stText.Render(ansi.Truncate(v, w-7, "…"))
	}
	where := "vm"
	if t.Placement.Local {
		where = "local"
	}
	d := decodeThread(m.events[id])
	var recent []string
	for _, e := range d.entries {
		if l := oneLine(e, w); l != "" {
			recent = append(recent, l)
		}
	}
	lines := []string{kv("status", t.Status), kv("on", t.Authority+" · "+where), kv("guest", cmpOr(t.Guest, "none")),
		kv("stats", fmt.Sprintf("%s · %d turns · $%.2f", uptime(durMs(d.ms)), d.turns, t.Cost)), "", stFaint.Render("FILES")}
	if len(d.files) == 0 {
		lines = append(lines, stDim.Render("  none"))
	}
	for _, f := range d.files[max(len(d.files)-3, 0):] {
		lines = append(lines, stText.Render("  ▤ "+ansi.Truncate(f, w-4, "…")))
	}
	lines = append(lines, "", stFaint.Render("RECENT"))
	if len(recent) == 0 {
		lines = append(lines, stDim.Render("  no events"))
	}
	for _, r := range recent[max(len(recent)-8, 0):] {
		lines = append(lines, r)
	}
	return lines
}

// ---- other tabs ----

func (m *Model) threadView(h int) []string {
	t := m.threadByID(m.convID)
	// The header's breadcrumb carries the title; this line is the status.
	title := ""
	where := ""
	if g := m.mgr; g != nil {
		st := "not started"
		if g.starting {
			st = "starting"
		}
		title = dot(st) + " " + stDim.Render(st)
	}
	if t != nil {
		title = dot(t.Status) + " " + stDim.Render(t.Authority+" · "+t.Status)
		if s := m.conv.statsLine(); s != "" {
			title += stDim.Render(" · " + s)
		}
		where = t.Authority
		if t.Guest != "" {
			where += " · " + t.Guest
		}
	}
	out := []string{fit(" "+title, m.w), blank(m.w)}
	m.vp.Width = max(m.w-4, 10)
	m.vp.Height = max(h-2-boxH, 1)
	// Each tool call in view opens with a click.
	slot := m.convSlot()
	for i, e := range m.conv.entries {
		if transcript.Opens(e) {
			m.zones[fmt.Sprintf("tool:%s:%d", slot, i)] = func() { m.toggleEntry(i) }
		}
	}
	for _, l := range strings.Split(m.vp.View(), "\n") {
		out = append(out, fit("  "+l, m.w))
	}
	left, ph := "", "Message this thread"
	if m.mgr != nil {
		ph = "Tell the manager what to do"
	}
	if m.convSub != "" {
		if t != nil {
			left = m.subLabel(t)
		}
		ph = "Read-only: a sub-agent's conversation (esc to go back)"
	}
	m.input.Placeholder = ph
	out = append(out, strings.Split(transcript.PromptBox(m.w, stFaint, left, "", func(s string) string { return s }, transcript.Truncate(where, m.w/2), m.input.View()), "\n")...)
	for len(out) < h {
		out = append(out, blank(m.w))
	}
	return out[:h]
}

func (m *Model) formView() string {
	f := m.form
	row := func(i int, label, val string) string {
		mark := "  "
		if f.field == i {
			mark = stAccent.Render("▸ ")
		}
		return mark + stFaint.Render(fmt.Sprintf("%-10s", label)) + val
	}
	place := "● VM   ○ local"
	if f.local {
		place = "○ VM   ● local"
	}
	proj := cmpOr(f.projLabel, scratchName)
	body := []string{
		stBold.Render("New thread") + stDim.Render("  in "+proj),
		"",
		row(0, "prompt", f.prompt.View()),
		row(1, "placement", stText.Render(place)),
		row(2, "authority", stText.Render("‹ "+f.choices[f.auth]+" ›")),
		"",
		stDim.Render("enter creates · tab changes field · ←→ changes the choice · esc cancels"),
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(hAccent).Padding(1, 2).
		Width(min(m.w-4, 74)).Render(strings.Join(body, "\n"))
}

func (m *Model) helpView() string {
	keys := [][2]string{
		{"← ↑ ↓ →  h j k l", "move between and within columns"},
		{"enter", "open a thread or a project's manager; a missing manager opens as an empty conversation; step into the next column"},
		{"→ on a thread", "its sub-agents, read-only; enter opens one"},
		{"n", "new thread in the selected project (VM by default)"},
		{"p", "new project: from one of your GitHub repos, or from a git checkout on this machine (ctrl+a adds the highlighted folder)"},
		{"tab / 1 2", "PACK / MONITOR (clients, hosts, guests)"},
		{"enter  o", "on a MONITOR guest or host: enter opens a shell on it (a new Rex pane when inside Rex); o opens the guest's thread"},
		{"s  d", "on a MONITOR guest: s sleeps it (stops it, keeps its disk) or wakes it if it's asleep; d downs it (removes it) after a y/n"},
		{"a", "show or hide archived threads"},
		{"esc", "back to the pack"},
		{"q  ctrl+c", "quit"},
	}
	var b []string
	b = append(b, stBold.Render("Keys"), "")
	for _, k := range keys {
		b = append(b, stAccent.Render(fmt.Sprintf("%-18s", k[0]))+stText.Render(k[1]))
	}
	b = append(b, "", stDim.Render("press any key to close"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(hFaint).Padding(1, 2).Render(strings.Join(b, "\n"))
}

// authorityUptime is how long the attached authority has been up (its "started" time), or
// how long the feed has been connected when the authority doesn't say.
func (m *Model) authorityUptime() string {
	for _, a := range m.pack.Authorities {
		if !a.Local {
			continue
		}
		if t, err := time.Parse(time.RFC3339, a.Started); err == nil {
			return uptime(m.opts.Now().Sub(t))
		}
	}
	if m.live && !m.since.IsZero() {
		return uptime(m.opts.Now().Sub(m.since))
	}
	return ""
}

// subLabel names the open sub-agent: its name and where it runs.
func (m *Model) subLabel(t *authapi.ThreadSummary) string {
	for _, s := range t.SubAgents {
		if s.ID == m.convSub {
			return cmpOr(s.Name, s.ID) + stDim.Render(" on "+cmpOr(s.Authority, t.Authority))
		}
	}
	return m.convSub
}

// headerLeft is the top bar's left side in at most w cells: the PACK and MONITOR tabs, or, while
// a conversation is open, a breadcrumb that continues from PACK. ok is false if it can't fit.
func (m *Model) headerLeft(w int) (string, bool) {
	lead := stBold.Render(m.brand()) + "  "
	if m.tab != tabThread {
		var tabs []string
		for i, n := range tabNames {
			if tab(i) == m.tab {
				tabs = append(tabs, lipgloss.NewStyle().Bold(true).Foreground(hAccent).Underline(true).Render(n))
			} else {
				tabs = append(tabs, stDim.Render(n))
			}
		}
		s := lead + strings.Join(tabs, "  ")
		return s, lipgloss.Width(s) <= w
	}
	crumbs := m.crumbs()
	const sep = " › "
	budget := w - lipgloss.Width(lead)
	plainW := func(c []string) int {
		n := 0
		for i, s := range c {
			n += lipgloss.Width(s)
			if i > 0 {
				n += lipgloss.Width(sep)
			}
		}
		return n
	}
	if plainW(crumbs) > budget && len(crumbs) > 2 {
		crumbs = []string{crumbs[0], crumbs[len(crumbs)-1]} // drop the middle crumbs first
	}
	if over := plainW(crumbs) - budget; over > 0 {
		last := len(crumbs) - 1
		keep := lipgloss.Width(crumbs[last]) - over
		if keep < 6 {
			return "", false
		}
		crumbs[last] = ansi.Truncate(crumbs[last], keep, "…")
	}
	active := lipgloss.NewStyle().Bold(true).Foreground(hAccent).Underline(true)
	var parts []string
	for i, c := range crumbs {
		switch {
		case i == len(crumbs)-1:
			parts = append(parts, active.Render(c))
		case i == 0:
			parts = append(parts, zone.Mark("crumb:pack", stDim.Render(c)))
		default:
			parts = append(parts, stDim.Render(c))
		}
	}
	m.zones["crumb:pack"] = m.closeConv
	return lead + strings.Join(parts, stFaint.Render(sep)), true
}

// crumbs are the open conversation's path from PACK: its project, its title, its sub-agent.
func (m *Model) crumbs() []string {
	out := []string{"PACK"}
	if g := m.mgr; g != nil {
		return append(out, cmpOr(g.name, m.projectName(g.project)), "manager")
	}
	t := m.threadByID(m.convID)
	if t == nil {
		return append(out, m.convID)
	}
	out = append(out, m.projectName(t.Project), cmpOr(t.Title, t.ID))
	if m.convSub != "" {
		name := m.convSub
		for _, s := range t.SubAgents {
			if s.ID == m.convSub {
				name = cmpOr(s.Name, s.ID)
			}
		}
		out = append(out, "sub-agent "+name)
	}
	return out
}

// closeConv leaves the open conversation for the pack.
func (m *Model) closeConv() {
	m.mgr, m.tab = nil, tabPack
	m.input.Blur()
}

// monitorChips are the MONITOR tab's key hints for the selected node.
func (m *Model) monitorChips() []string {
	chips := []string{chip("←↑↓→", "move")}
	if n := m.mselected(m.mfocus); n != nil {
		if argv, _ := m.shellArgv(n); len(argv) > 0 || n.host != nil {
			chips = append(chips, chip("enter", "shell"))
		}
		if _, _, thread, phase, ok := m.selectedGuest(); ok {
			if thread != "" {
				chips = append(chips, chip("o", "open thread"))
			}
			sl := "sleep"
			if guestAsleep(phase) {
				sl = "wake"
			}
			chips = append(chips, chip("s", sl), chip("d", "down"))
		}
	}
	return append(chips, chip("tab", "settings"), chip("?", "help"), chip("q", "quit"))
}
