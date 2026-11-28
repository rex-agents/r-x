package authtui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

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
	nc := m.packCols()
	for c := 0; c < nc; c++ {
		l.widths[c] = cardW
		if l.logo && c == 0 {
			l.widths[c] = logoW
		}
		if avail < cardW+2 { // a very narrow terminal: one full-width column
			l.widths[c] = max(avail, 12)
		}
	}
	l.cols = window(l.widths, nc, m.focus, avail)
	return l
}

// window picks the columns that fit in avail, scrolling so the focused one is visible.
func window(widths map[int]int, nc, focus, avail int) []int {
	used, n := 0, 0
	for c := 0; c < nc; c++ {
		w := widths[c]
		if n > 0 {
			w += gutterW
		}
		if used+w > avail && n > 0 {
			break
		}
		used += w
		n++
	}
	start := max(0, focus-n+1)
	var cols []int
	for c := start; c < nc && c < start+n; c++ {
		cols = append(cols, c)
	}
	// Columns after a collapsed start can use the width col 0 would have taken.
	for c := start + n; start > 0 && c < nc; c++ {
		w := widths[c] + gutterW
		if used+w > avail {
			break
		}
		used += w
		cols = append(cols, c)
	}
	return cols
}

type colRender struct {
	lines []string
	mid   int // body row of the selected card's middle, -1 if none
}

// renderCol draws one column: its title, then its cards, with logo lines pinned to the bottom.
func (m *Model) renderCol(title string, nodes []node, sel int, focused bool, w, h int, logoLines []string, empty string) colRender {
	lines := make([]string, 0, h)
	titleStyle := stFaint
	if focused {
		titleStyle = stAccent
	}
	lines = append(lines, fit(titleStyle.Render(title), w))
	room := h - len(lines) - len(logoLines)
	rows := max(room/cardH, 1)
	sel = min(max(sel, 0), max(len(nodes)-1, 0))
	off := 0
	if len(nodes) > rows {
		off = min(max(sel-rows/2, 0), len(nodes)-rows)
	}
	mid := -1
	if len(nodes) == 0 {
		lines = append(lines, fit(stDim.Render(empty), w))
	}
	for i := off; i < len(nodes) && i < off+rows; i++ {
		isSel := i == sel
		if isSel {
			mid = len(lines) + cardH/2
		}
		lines = append(lines, m.card(nodes[i], w, isSel, focused)...)
	}
	lines = lines[:min(len(lines), h-len(logoLines))]
	for len(lines) < h-len(logoLines) {
		lines = append(lines, blank(w))
	}
	lines = append(lines, logoLines...)
	return colRender{lines[:h], mid}
}

func (m *Model) renderColumn(c, w, h int, logo bool) colRender {
	// The T-rex sits at the bottom of the first column, under the cards.
	var logoLines []string
	if logo && c == 0 {
		for _, l := range strings.Split(m.opts.Logo(m.phase), "\n") {
			logoLines = append(logoLines, fit(l, w))
		}
		logoLines = logoLines[:min(len(logoLines), max(h-1, 0))]
	}
	return m.renderCol(colTitles[c], m.column(c), m.sel[c], c == m.focus, w, h, logoLines, emptyText(c))
}

func emptyText(c int) string {
	switch c {
	case colLeader:
		return "no pack"
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
	rows := []string{dot(n.status) + " " + name}
	for i, b := range n.body {
		s := stDim.Render(ansi.Truncate(b, inner, "…"))
		if i == 2 && len(n.spark) > 0 {
			spark := m.spark(n)
			if pad := inner - lipgloss.Width(s) - lipgloss.Width(spark); pad > 0 {
				s += blank(pad) + spark
			}
		}
		rows = append(rows, s)
	}
	row := func(s string) string { return bs.Render("│") + " " + fit(s, inner) + " " + bs.Render("│") }
	out := []string{bs.Render("╭" + strings.Repeat("─", w-2) + "╮")}
	for _, r := range rows {
		out = append(out, row(r))
	}
	return append(out, bs.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// spark is the node's recent activity: the sum of its threads'.
func (m *Model) spark(n node) string {
	sum := make([]int, sparkLen)
	for _, id := range n.spark {
		for i, v := range m.act[id] {
			sum[i] += v
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

// joinCols lays rendered columns side by side with connectors between them.
func joinCols(cols []colRender, linked []bool, h int) []string {
	rows := make([]string, h)
	for y := range rows {
		var b strings.Builder
		for i := range cols {
			if i > 0 {
				yr := cols[i].mid
				if !linked[i] {
					yr = -1
				}
				b.WriteString(gutter(h, cols[i-1].mid, yr)[y])
			}
			b.WriteString(cols[i].lines[y])
		}
		rows[y] = b.String()
	}
	return rows
}

func (m *Model) packView(h int) []string {
	if !m.havePack {
		return center(stDim.Render("connecting to "+m.c.Base+"…"), m.w, h)
	}
	l := m.plan(h)
	cols := make([]colRender, len(l.cols))
	linked := make([]bool, len(l.cols))
	for i, c := range l.cols {
		cols[i] = m.renderColumn(c, l.widths[c], h, l.logo)
		linked[i] = m.selected(c) != nil
	}
	rows := joinCols(cols, linked, h)
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

// ---- monitor ----

func (m *Model) monitorView(h int) []string {
	if !m.havePack {
		return center(stDim.Render("connecting to "+m.c.Base+"…"), m.w, h)
	}
	widths := map[int]int{}
	cw := min(max((m.w-2*gutterW)/numMCols, 24), 46)
	if m.w < 2*gutterW+numMCols*24 {
		cw = max(min(m.w, 46), 12)
	}
	for c := 0; c < numMCols; c++ {
		widths[c] = cw
	}
	vis := window(widths, numMCols, m.mfocus, m.w)
	cols := make([]colRender, len(vis))
	linked := make([]bool, len(vis))
	empty := [numMCols]string{"no clients connected", "no authorities", "no guests"}
	for i, c := range vis {
		nodes := m.monitorColumn(c)
		cols[i] = m.renderCol(monitorTitles[c], nodes, m.msel[c], c == m.mfocus, widths[c], h, nil, empty[c])
		linked[i] = len(nodes) > 0
	}
	rows := joinCols(cols, linked, h)
	for y := range rows {
		rows[y] = fit(rows[y], m.w)
	}
	return rows
}

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
	case colLeader:
		lines = append(lines, kv("term", fmt.Sprint(m.pack.Term)), kv("vault", cmpOr(m.pack.VaultHost, "none")))
		for _, a := range m.pack.Authorities {
			lines = append(lines, kv(a.Name, a.Status))
		}
	case colProject:
		if p, ok := m.projects[n.project]; ok {
			if p.Dir != "" {
				lines = append(lines, kv("dir", tildePath(p.Dir, m.home)))
			}
			if p.Repo != "" {
				lines = append(lines, kv("repo", p.Repo))
			}
		}
		lines = append(lines, kv("threads", fmt.Sprint(n.threads)), kv("cost", fmt.Sprintf("$%.2f", n.cost)))
		if n.noMgr {
			if !n.scratch {
				lines = append(lines, "", stDim.Render("no manager yet: enter starts one"))
			}
			break
		}
		if n.th != nil {
			lines = append(lines, kv("workers", fmt.Sprintf("%d/%d working", n.working, n.workers)), "")
			lines = append(lines, m.threadPreview(n.th.ID, n, w)...)
		}
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
