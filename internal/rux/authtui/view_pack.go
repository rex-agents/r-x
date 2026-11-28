package authtui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ---- the graph ----

type layout struct {
	cols    []int       // visible columns, left to right
	widths  map[int]int // every column's width
	xs      map[int]int // every column's left edge in the graph area (may be negative)
	avail   int         // width of the graph area (everything left of the preview)
	logo    bool        // there is room for the T-rex, if nothing is in its way
	preview int         // preview width, 0 = hidden
}

// plan lays the columns out so the focused one sits in the horizontal middle of the graph area
// and the rest follow it left and right, gutterW apart. Columns past the edges are clipped.
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
	l.avail = avail
	l.logo = m.haveLogo() && bodyH >= logoH+cardH+2 && avail >= logoW+gutterW+cardW
	nc := m.packCols()
	for c := 0; c < nc; c++ {
		l.widths[c] = cardW
		if avail < cardW+2 { // a very narrow terminal: one full-width column
			l.widths[c] = max(avail, 12)
		}
	}
	l.xs, l.cols = centreXs(l.widths, nc, m.focus, avail)
	return l
}

// centreXs puts column focus in the horizontal middle of avail and the rest left and right of it,
// gutterW apart. It returns every column's left edge (may be negative) and the columns that are
// at least partly visible.
func centreXs(widths map[int]int, nc, focus, avail int) (map[int]int, []int) {
	xs := map[int]int{}
	f := min(max(focus, 0), nc-1)
	xs[f] = (avail - widths[f]) / 2
	for c := f - 1; c >= 0; c-- {
		xs[c] = xs[c+1] - gutterW - widths[c]
	}
	for c := f + 1; c < nc; c++ {
		xs[c] = xs[c-1] + widths[c-1] + gutterW
	}
	var vis []int
	for c := 0; c < nc; c++ {
		if xs[c]+widths[c] > 0 && xs[c] < avail {
			vis = append(vis, c)
		}
	}
	return xs, vis
}

type colRender struct {
	lines []string
	mid   int // body row of the selected card's middle, -1 if none
}

// renderCentred draws one column h rows tall. The title is pinned to the top row; the selected
// card's middle sits on row h/2 and the other cards stack outward from it, clipped at the edges.
// An empty column puts its "no …" text on that middle row.
func (m *Model) renderCentred(title string, nodes []node, sel int, focused bool, w, h int, empty string) colRender {
	lines := make([]string, h)
	for i := range lines {
		lines[i] = blank(w)
	}
	ts := stFaint
	if focused {
		ts = stAccent
	}
	lines[0] = fit(ts.Render(title), w)
	mid := h / 2
	if len(nodes) == 0 {
		lines[mid] = fit(stDim.Render(empty), w)
		return colRender{lines, -1}
	}
	sel = min(max(sel, 0), len(nodes)-1)
	top := mid - cardH/2 - sel*cardH // where card 0 starts
	for i, n := range nodes {
		y0 := top + i*cardH
		if y0+cardH <= 1 || y0 >= h {
			continue
		}
		for j, l := range m.card(n, w, i == sel, focused) {
			if y := y0 + j; y >= 1 && y < h {
				lines[y] = l
			}
		}
	}
	return colRender{lines, mid}
}

func (m *Model) renderColumn(c, w, h int) colRender {
	return m.renderCentred(colTitles[c], m.column(c), m.sel[c], c == m.focus, w, h, emptyText(c))
}

// composeGraph lays the visible columns side by side at their x offsets, with connectors between
// the selected cards, and cuts the result to avail cells.
func composeGraph(vis []int, xs map[int]int, cols []colRender, linked []bool, avail, h int) []string {
	rows := make([]string, h)
	x0 := xs[vis[0]]
	for y := range rows {
		var b strings.Builder
		if x0 > 0 {
			b.WriteString(blank(x0))
		}
		for i := range vis {
			if i > 0 {
				yr := cols[i].mid
				if !linked[i] {
					yr = -1
				}
				b.WriteString(gutter(h, cols[i-1].mid, yr)[y])
			}
			b.WriteString(cols[i].lines[y])
		}
		line := b.String()
		if x0 < 0 {
			line = ansi.Cut(line, -x0, -x0+avail)
		}
		rows[y] = fit(line, avail)
	}
	return rows
}

// logoRows is the T-rex's lines, fitted to its width, or nil if it has none.
func (m *Model) logoRows(h int) []string {
	if !m.haveLogo() {
		return nil
	}
	var out []string
	for _, l := range strings.Split(m.logo(m.phase), "\n") {
		out = append(out, fit(l, logoW))
	}
	return out[:min(len(out), max(h-1, 0))]
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

func (m *Model) packView(h int) []string {
	if !m.havePack {
		return center(stDim.Render("connecting to "+m.c.Base+"…"), m.w, h)
	}
	l := m.plan(h)
	cols := make([]colRender, len(l.cols))
	linked := make([]bool, len(l.cols))
	for i, c := range l.cols {
		cols[i] = m.renderColumn(c, l.widths[c], h)
		linked[i] = m.selected(c) != nil
	}
	rows := composeGraph(l.cols, l.xs, cols, linked, l.avail, h)
	// The T-rex sits at the bottom left of the graph area, if nothing is in its way.
	if lr := m.logoRows(h); l.logo && len(lr) > 0 {
		top := h - len(lr)
		free := true
		for i := range lr {
			if strings.TrimSpace(ansi.Strip(ansi.Cut(rows[top+i], 0, logoW+1))) != "" {
				free = false
				break
			}
		}
		if free {
			for i, g := range lr {
				rows[top+i] = g + ansi.Cut(rows[top+i], logoW, l.avail)
			}
		}
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
	xs, vis := centreXs(widths, numMCols, m.mfocus, m.w)
	cols := make([]colRender, len(vis))
	linked := make([]bool, len(vis))
	empty := [numMCols]string{"no clients connected", "no authorities", "no guests"}
	for i, c := range vis {
		nodes := m.monitorColumn(c)
		cols[i] = m.renderCentred(monitorTitles[c], nodes, m.msel[c], c == m.mfocus, widths[c], h, empty[c])
		linked[i] = len(nodes) > 0
	}
	rows := composeGraph(vis, xs, cols, linked, m.w, h)
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
