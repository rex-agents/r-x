package authtui

import (
	"fmt"
	"github.com/rex-agents/r-x/internal/rux/authapi"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	cardH     = 5
	gutterW   = 3
	cardW     = 25
	logoW     = 42
	logoH     = 15
	previewW  = 32
	minPrevW  = 90 // narrower terminals drop the preview column
	sparkRune = "▁▂▃▄▅▆▇█"
)

var colTitles = [numCols]string{"HOSTS", "PROJECTS", "THREADS", "SUB-AGENTS"}

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
	var out []string
	out = append(out, m.header())
	banners := m.banners()
	out = append(out, banners...)
	bodyH := m.h - len(out) - 1
	var body []string
	switch {
	case m.form != nil:
		body = center(m.formView(), m.w, bodyH)
	case m.help:
		body = center(m.helpView(), m.w, bodyH)
	case m.tab == tabThread:
		body = m.threadView(bodyH)
	case m.tab == tabMonitor:
		body = m.monitorView(bodyH)
	default:
		body = m.packView(bodyH)
	}
	out = append(out, body...)
	out = append(out, m.footer())
	return strings.Join(out, "\n")
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
	var tabs []string
	for i, n := range tabNames {
		if tab(i) == m.tab {
			tabs = append(tabs, lipgloss.NewStyle().Bold(true).Foreground(hAccent).Underline(true).Render(n))
		} else {
			tabs = append(tabs, stDim.Render(n))
		}
	}
	left := stBold.Render("rux") + "  " + strings.Join(tabs, "  ")
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
		if lipgloss.Width(left)+2+lipgloss.Width(right) <= m.w || len(pieces) <= 1 {
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
	var parts []string
	for c := 0; c <= m.focus && c < numCols; c++ {
		if n := m.selected(c); n != nil {
			parts = append(parts, n.name)
		}
	}
	if m.tab == tabThread {
		if t := m.threadByID(m.convID); t != nil {
			parts = append(parts[:min(len(parts), 2)], cmpOr(t.Title, t.ID))
		}
	}
	if len(parts) == 0 {
		return stDim.Render("pack")
	}
	return stDim.Render("pack › ") + stText.Render(strings.Join(parts, " › "))
}

func chip(k, label string) string {
	return lipgloss.NewStyle().Foreground(hText).Background(lipgloss.Color("237")).Padding(0, 1).Render(k) + " " + stDim.Render(label)
}

func (m *Model) footer() string {
	if m.flash != "" {
		return fit(lipgloss.NewStyle().Foreground(hYellow).Render(m.flash), m.w)
	}
	var chips []string
	switch {
	case m.form != nil:
		chips = []string{chip("enter", "create"), chip("tab", "field"), chip("ctrl+l", "local"), chip("esc", "cancel")}
	case m.tab == tabThread:
		chips = []string{chip("enter", "send"), chip("↑↓", "scroll"), chip("esc", "back"), chip("ctrl+c", "quit")}
	default:
		chips = []string{chip("←↑↓→", "move"), chip("enter", "open"), chip("n", "new thread"), chip("tab", "monitor"),
			chip("a", map[bool]string{true: "hide archived", false: "archived"}[m.archived]), chip("?", "help"), chip("q", "quit")}
	}
	left := m.breadcrumb()
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

// ---- the graph ----

type layout struct {
	cols    []int // visible columns
	widths  map[int]int
	logo    bool
	preview int // preview width, 0 = hidden
}

func (m *Model) plan(bodyH int) layout {
	l := layout{widths: map[int]int{}}
	avail := m.w
	if avail >= minPrevW {
		l.preview = previewW
		if avail >= 170 {
			l.preview = 36
		}
		avail -= l.preview + 1
	}
	l.logo = m.opts.Logo != nil && bodyH >= logoH+cardH+2 && avail >= logoW+gutterW+cardW
	for c := 0; c < numCols; c++ {
		l.widths[c] = cardW
		if l.logo && c == 0 {
			l.widths[c] = logoW
		}
		if avail < cardW+2 { // a very narrow terminal: one full-width column
			l.widths[c] = max(avail, 12)
		}
	}
	used, n := 0, 0
	for c := 0; c < numCols; c++ {
		w := l.widths[c]
		if n > 0 {
			w += gutterW
		}
		if used+w > avail && n > 0 {
			break
		}
		used += w
		n++
	}
	start := max(0, m.focus-n+1)
	for c := start; c < numCols && c < start+n; c++ {
		l.cols = append(l.cols, c)
	}
	// Columns after a collapsed start can use the width col 0 would have taken.
	for c := start + n; start > 0 && c < numCols; c++ {
		w := l.widths[c] + gutterW
		if used+w > avail {
			break
		}
		used += w
		l.cols = append(l.cols, c)
	}
	return l
}

type colRender struct {
	lines []string
	mid   int // body row of the selected card's middle, -1 if none
}

func (m *Model) renderColumn(c, w, h int, logo bool) colRender {
	lines := make([]string, 0, h)
	if logo && c == 0 {
		for _, l := range strings.Split(m.opts.Logo(m.phase), "\n") {
			lines = append(lines, fit(l, w))
		}
		lines = lines[:min(len(lines), h)]
	}
	titleStyle := stFaint
	if c == m.focus {
		titleStyle = stAccent
	}
	lines = append(lines, fit(titleStyle.Render(colTitles[c]), w))
	nodes := m.column(c)
	room := h - len(lines)
	rows := max(room/cardH, 1)
	sel := min(max(m.sel[c], 0), max(len(nodes)-1, 0))
	off := 0
	if len(nodes) > rows {
		off = min(max(sel-rows/2, 0), len(nodes)-rows)
	}
	mid := -1
	if len(nodes) == 0 {
		lines = append(lines, fit(stDim.Render(emptyText(c)), w))
	}
	for i := off; i < len(nodes) && i < off+rows; i++ {
		isSel := i == sel
		if isSel {
			mid = len(lines) + 2
		}
		lines = append(lines, m.card(nodes[i], w, isSel, c == m.focus)...)
	}
	for len(lines) < h {
		lines = append(lines, blank(w))
	}
	return colRender{lines[:h], mid}
}

func emptyText(c int) string {
	switch c {
	case colHost:
		return "no authorities"
	case colProject:
		return "no projects"
	case colThread:
		return "no threads"
	}
	return "no sub-agents"
}

func (m *Model) card(n node, w int, selected, focused bool) []string {
	bc := hFaint
	if selected {
		bc = hAccent
		if !focused {
			bc = hViolet
		}
	}
	bs := lipgloss.NewStyle().Foreground(bc)
	inner := w - 4
	name := stText.Render(ansi.Truncate(n.name, inner-2, "…"))
	if selected {
		name = stBold.Render(ansi.Truncate(n.name, inner-2, "…"))
	}
	l1 := dot(n.status) + " " + name
	l2 := stDim.Render(ansi.Truncate(n.summary, inner, "…"))
	l3 := ""
	switch n.kind {
	case colHost:
		l3 = stDim.Render(plural(n.threads, "thread"))
		if n.host != nil && n.host.Host.GuestsMax > 0 {
			l3 += stFaint.Render(fmt.Sprintf(" · %d/%d vms", n.host.Host.GuestsUsed, n.host.Host.GuestsMax))
		}
	case colProject:
		l3 = stDim.Render(plural(n.threads, "thread"))
	case colThread:
		l3 = stDim.Render(plural(n.threads, "sub-agent"))
	case colSub:
		l3 = stDim.Render(n.status)
	}
	if n.kind <= colThread {
		spark := m.spark(n)
		if pad := inner - lipgloss.Width(l3) - lipgloss.Width(spark); pad > 0 {
			l3 += blank(pad) + spark
		}
	}
	row := func(s string) string { return bs.Render("│") + " " + fit(s, inner) + " " + bs.Render("│") }
	return []string{
		bs.Render("╭" + strings.Repeat("─", w-2) + "╮"),
		row(l1), row(l2), row(l3),
		bs.Render("╰" + strings.Repeat("─", w-2) + "╯"),
	}
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// spark is the node's recent activity: its own for a thread, the sum of its threads' otherwise.
func (m *Model) spark(n node) string {
	sum := make([]int, sparkLen)
	add := func(id string) {
		for i, v := range m.act[id] {
			sum[i] += v
		}
	}
	switch n.kind {
	case colThread:
		add(n.th.ID)
	case colProject:
		if h := m.selected(colHost); h != nil {
			for _, t := range h.host.Threads {
				if t.Project == n.project {
					add(t.ID)
				}
			}
		}
	case colHost:
		for _, t := range n.host.Threads {
			add(t.ID)
		}
	}
	top := 0
	for _, v := range sum {
		top = max(top, v)
	}
	rs := []rune(sparkRune)
	var b strings.Builder
	for _, v := range sum {
		i := 0
		if top > 0 && v > 0 {
			i = 1 + (v-1)*(len(rs)-2)/max(top, 1)
		}
		b.WriteRune(rs[min(i, len(rs)-1)])
	}
	return lipgloss.NewStyle().Foreground(hViolet).Render(b.String())
}

// gutter draws the line from the selected card of one column to the selected card of the next.
func gutter(h, yl, yr int) []string {
	g := make([]string, h)
	for i := range g {
		g[i] = blank(gutterW)
	}
	if yl < 0 || yr < 0 {
		return g
	}
	st := lipgloss.NewStyle().Foreground(hAccent)
	set := func(y int, s string) {
		if y >= 0 && y < h {
			g[y] = st.Render(s)
		}
	}
	switch {
	case yl == yr:
		set(yl, "───")
	case yl < yr:
		set(yl, "─┐ ")
		for y := yl + 1; y < yr; y++ {
			set(y, " │ ")
		}
		set(yr, " └─")
	default:
		set(yl, "─┘ ")
		for y := yr + 1; y < yl; y++ {
			set(y, " │ ")
		}
		set(yr, " ┌─")
	}
	return g
}

func (m *Model) packView(h int) []string {
	if !m.havePack {
		return center(stDim.Render("connecting to "+m.c.Base+"…"), m.w, h)
	}
	l := m.plan(h)
	cols := make([]colRender, len(l.cols))
	for i, c := range l.cols {
		cols[i] = m.renderColumn(c, l.widths[c], h, l.logo)
	}
	rows := make([]string, h)
	for y := range rows {
		var b strings.Builder
		for i, c := range l.cols {
			if i > 0 {
				var yr int = cols[i].mid
				if m.selected(c) == nil {
					yr = -1
				}
				b.WriteString(gutterCell(h, cols[i-1].mid, yr, y))
			}
			b.WriteString(cols[i].lines[y])
		}
		rows[y] = b.String()
	}
	if l.preview > 0 {
		pv := m.previewPanel(l.preview, h)
		for y := range rows {
			rows[y] += " " + pv[y]
		}
	}
	for y := range rows {
		rows[y] = fit(rows[y], m.w)
	}
	return rows
}

func gutterCell(h, yl, yr, y int) string { return gutter(h, yl, yr)[y] }

// ---- preview ----

func (m *Model) previewPanel(w, h int) []string {
	var lines []string
	n := m.current()
	if n != nil {
		lines = m.previewLines(n, w-4)
	}
	if n == nil {
		lines = []string{stDim.Render("nothing selected")}
	}
	bs := lipgloss.NewStyle().Foreground(hFaint)
	out := []string{bs.Render("╭" + strings.Repeat("─", w-2) + "╮")}
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, bs.Render("│")+" "+fit(l, w-4)+" "+bs.Render("│"))
	}
	return append(out, bs.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

func (m *Model) previewLines(n *node, w int) []string {
	kv := func(k, v string) string { return stFaint.Render(fmt.Sprintf("%-8s", k)) + stText.Render(v) }
	head := dot(n.status) + " " + stBold.Render(ansi.Truncate(n.name, w-2, "…"))
	lines := []string{head, ""}
	switch n.kind {
	case colHost:
		a := n.host
		lines = append(lines, kv("status", a.Status), kv("url", a.URL), kv("os", cmpOr(a.Host.OS, "?")),
			kv("cpus", fmt.Sprint(a.Host.CPUs)), kv("load", fmt.Sprintf("%.2f", a.Host.Load)),
			kv("memory", fmt.Sprintf("%.1f GiB", float64(a.Host.Mem)/(1<<30))),
			kv("vms", fmt.Sprintf("%d / %d", a.Host.GuestsUsed, a.Host.GuestsMax)),
			kv("threads", fmt.Sprint(n.threads)))
		if a.Name == m.pack.Leader {
			lines = append(lines, kv("role", "pack leader"))
		}
		if a.Name == m.pack.VaultHost {
			lines = append(lines, kv("role", "vault host"))
		}
	case colProject:
		var cost float64
		counts := map[string]int{}
		if h := m.selected(colHost); h != nil {
			for _, t := range h.host.Threads {
				if t.Project == n.project && m.visible(t) {
					cost += t.Cost
					counts[t.Status]++
				}
			}
		}
		if p, ok := m.projects[n.project]; ok && p.Repo != "" {
			lines = append(lines, kv("repo", p.Repo))
		}
		lines = append(lines, kv("threads", fmt.Sprint(n.threads)), kv("cost", fmt.Sprintf("$%.2f", cost)))
		var st []string
		for s, c := range counts {
			st = append(st, fmt.Sprintf("%d %s", c, s))
		}
		sort.Strings(st)
		lines = append(lines, kv("status", strings.Join(st, ", ")))
	case colThread:
		lines = append(lines, m.threadPreview(n.th.ID, n, w)...)
	case colSub:
		lines = append(lines, kv("status", n.sub.Status), kv("guest", cmpOr(n.sub.Guest, "none")), kv("id", n.sub.ID))
		if t := m.selected(colThread); t != nil {
			lines = append(lines, kv("thread", t.name))
		}
	}
	return lines
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
	var dur, turns int64
	var files []string
	var recent []string
	for _, se := range m.events[id] {
		for _, s := range summarize(se) {
			switch s.kind {
			case "result":
				dur += s.ms
				turns += int64(s.turns)
			case "file":
				files = append(files, s.file)
			}
			switch s.kind {
			case "user", "text", "tool", "error":
				recent = append(recent, oneLine(s, w))
			}
		}
	}
	lines := []string{kv("status", t.Status), kv("on", t.Authority+" · "+where), kv("guest", cmpOr(t.Guest, "none")),
		kv("stats", fmt.Sprintf("%s · %d turns · $%.2f", uptime(durMs(dur)), turns, t.Cost)), "", stFaint.Render("FILES")}
	if len(files) == 0 {
		lines = append(lines, stDim.Render("  none"))
	}
	for _, f := range files[max(len(files)-3, 0):] {
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
	title := m.convID
	if t != nil {
		title = dot(t.Status) + " " + stBold.Render(cmpOr(t.Title, t.ID)) + stDim.Render("  "+t.Authority+" · "+t.Status)
		if m.convSub != "" {
			title += stDim.Render("  › sub-agent ") + stBold.Render(m.subLabel(t))
		}
	}
	out := []string{fit(" "+title, m.w)}
	m.vp.Height = max(h-3, 1)
	for _, l := range strings.Split(m.vp.View(), "\n") {
		out = append(out, fit(" "+l, m.w))
	}
	out = append(out[:min(len(out), h-1)], "")
	out[len(out)-1] = fit(" "+m.input.View(), m.w)
	if m.convSub != "" {
		out[len(out)-1] = fit(stDim.Render(" read-only: a sub-agent's conversation (esc to go back)"), m.w)
	}
	for len(out) < h {
		out = append(out, blank(m.w))
	}
	return out[:h]
}

func (m *Model) monitorView(h int) []string {
	out := []string{fit(stFaint.Render(fmt.Sprintf(" %-18s %-8s %-8s %-6s %-9s %-8s %s", "AUTHORITY", "STATUS", "OS", "CPUS", "LOAD", "VMS", "THREADS (working/total)")), m.w)}
	for _, a := range m.pack.Authorities {
		work := 0
		for _, t := range a.Threads {
			if t.Status == "running" || t.Status == "working" {
				work++
			}
		}
		name := a.Name
		if a.Name == m.pack.Leader {
			name += " ★"
		}
		out = append(out, fit(fmt.Sprintf(" %-18s %s %-6s %-8s %-6d %-9.2f %-8s %d/%d", name, dot(a.Status), a.Status, cmpOr(a.Host.OS, "?"), a.Host.CPUs, a.Host.Load,
			fmt.Sprintf("%d/%d", a.Host.GuestsUsed, a.Host.GuestsMax), work, len(a.Threads)), m.w))
	}
	out = append(out, "", fit(stFaint.Render(" RUNNING"), m.w))
	for _, a := range m.pack.Authorities {
		for _, t := range a.Threads {
			if t.Status == "running" || t.Status == "working" {
				out = append(out, fit(fmt.Sprintf(" %s %-30s %-12s %s  $%.2f", dot(t.Status), ansi.Truncate(cmpOr(t.Title, t.ID), 30, "…"), a.Name, plural(len(t.SubAgents), "sub-agent"), t.Cost), m.w))
			}
		}
	}
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
	proj := cmpOr(f.projLabel, noProject)
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
		{"enter", "open: a thread's conversation, else step into the next column"},
		{"n", "new thread in the selected project (VM by default)"},
		{"tab / 1 2 3", "PACK / THREAD / MONITOR"},
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
