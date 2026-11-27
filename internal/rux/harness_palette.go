package rux

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The command menu (ctrl+o): everything rux can do, to pick with the keys, by typing, or
// with a click.

type paletteItem struct {
	label, hint string
	run         func() tea.Cmd
}

type palette struct {
	items  []paletteItem
	filter string
	cursor int
}

func (m *harnessModel) openPalette() {
	a := m.selected()
	p := &palette{}
	add := func(label, hint string, run func() tea.Cmd) { p.items = append(p.items, paletteItem{label, hint, run}) }
	add("Model and effort for "+a.Name, "ctrl+l", func() tea.Cmd { return m.openPicker(a) })
	add("Model for new sub-agents", "/agent-model", func() tea.Cmd { return m.openPicker(nil) })
	inRex := os.Getenv("REX_SESSION") != ""
	for _, x := range m.agents() {
		x := x
		add("Go to "+x.Name, "tab", func() tea.Cmd { m.selectAgent(x); return nil })
		if inRex {
			add("Open "+x.Name+" in a Rex pane", "/pane", func() tea.Cmd { m.flash = m.openPane(x); return nil })
		}
		if x.Role == "sub" {
			if inRex {
				add("Shell into "+x.Name+"'s guest", "/shell", func() tea.Cmd { m.flash = m.openShell(x); return nil })
			}
			add("Stop "+x.Name+" and remove its guest", "/stop", func() tea.Cmd { m.stopAgent(x); return nil })
		}
		if x.Busy() {
			add("Interrupt "+x.Name, "esc", func() tea.Cmd { x.Interrupt(); return nil })
		}
	}
	add(map[bool]string{true: "Close every tool call", false: "Open every tool call"}[m.expandAll], "ctrl+e",
		func() tea.Cmd { m.expandAll = !m.expandAll; m.refresh(); return nil })
	add("Shortcuts", "?", func() tea.Cmd { m.panel = panelShortcuts; return nil })
	add("Quit", "ctrl+c", func() tea.Cmd { return m.quit() })
	m.palette, m.panel = p, panelPalette
}

func (p *palette) shown() []paletteItem {
	if p.filter == "" {
		return p.items
	}
	var out []paletteItem
	for _, it := range p.items {
		if strings.Contains(strings.ToLower(it.label), strings.ToLower(p.filter)) {
			out = append(out, it)
		}
	}
	return out
}

func (m *harnessModel) runPaletteItem(it paletteItem) tea.Cmd {
	m.panel = panelNone
	return it.run()
}

func (m *harnessModel) paletteKey(k tea.KeyMsg) tea.Cmd {
	p := m.palette
	items := p.shown()
	switch k.String() {
	case "esc", "ctrl+c", "ctrl+o", "ctrl+k":
		m.panel = panelNone
	case "up", "ctrl+p":
		p.cursor = max(p.cursor-1, 0)
	case "down", "ctrl+n", "tab":
		p.cursor = min(p.cursor+1, max(len(items)-1, 0))
	case "backspace":
		if p.filter != "" {
			p.filter, p.cursor = p.filter[:len(p.filter)-1], 0
		}
	case "enter":
		if len(items) > 0 {
			return m.runPaletteItem(items[min(p.cursor, len(items)-1)])
		}
	default:
		if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
			p.filter += string(k.Runes)
			p.cursor = 0
		}
	}
	return nil
}

func (m *harnessModel) paletteView() string {
	p := m.palette
	items := p.shown()
	rows := min(10, max(m.h/2-4, 4))
	start := max(0, p.cursor-rows+1)
	var lines []string
	width := m.w - 12
	for i := start; i < len(items) && i < start+rows; i++ {
		it := items[i]
		mark, st := "  ", stText
		if i == p.cursor {
			mark, st = stAccent.Render("▸ "), stBold
		}
		label := st.Render(it.label)
		gap := width - lipgloss.Width(label) - lipgloss.Width(it.hint) - 2
		i := i
		lines = append(lines, m.click(fmt.Sprintf("pal:%d", i), mark+label+strings.Repeat(" ", max(gap, 1))+stFaint.Render(it.hint),
			func() tea.Cmd { return m.runPaletteItem(items[i]) }))
	}
	if len(items) == 0 {
		lines = append(lines, stDim.Render("  nothing matches"))
	}
	foot := stFaint.Render("↑↓ choose · enter run · type to search") + "  " + p.filter + stAccent.Render("▏")
	return m.panelBox(hFaint, strings.Join(lines, "\n")+"\n"+stFaint.Render(strings.Repeat("─", width))+"\n"+foot)
}
