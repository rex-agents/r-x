package authtui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// The SETTINGS tab: the authority's name and mascot, and the theme. Every change goes through
// the API, so the web app and other TUIs repaint too (the feed's "persona" and "theme" events).

// Row kinds of the settings list.
const (
	setRowName = iota
	setRowMascot
	setRowPreset
	setRowColor
	setRowReset
)

type setRow struct {
	kind   int
	preset string
}

// settings is the SETTINGS tab's state.
type settings struct {
	sel     int
	editing int // setRowName or setRowColor while a field is open, else -1
	input   textinput.Model
	err     string
}

func newSettings() *settings { return &settings{editing: -1} }

type settingsDoneMsg struct {
	what string
	err  error
	p    *authapi.Persona
	t    *authapi.ThemeInfo
}

// ResetTheme is DELETE /api/theme.
func (c *Client) ResetTheme() (authapi.ThemeInfo, error) {
	var t authapi.ThemeInfo
	err := c.do("DELETE", "/api/theme", nil, &t)
	return t, err
}

// ResetMascot is DELETE /api/persona/mascot.
func (c *Client) ResetMascot() (authapi.Persona, error) {
	var p authapi.Persona
	err := c.do("DELETE", "/api/persona/mascot", nil, &p)
	return p, err
}

func (m *Model) settingsRows() []setRow {
	rows := []setRow{{kind: setRowName}, {kind: setRowMascot}}
	presets := m.theme.Presets
	if len(presets) == 0 {
		presets = authapi.ThemePresets
	}
	for _, p := range presets {
		rows = append(rows, setRow{kind: setRowPreset, preset: p.Name})
	}
	return append(rows, setRow{kind: setRowColor}, setRow{kind: setRowReset})
}

// settingsEditing says a field is open, so keys go to it rather than to the tab bar.
func (m *Model) settingsEditing() bool { return m.set != nil && m.set.editing >= 0 }

func (m *Model) settingsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := m.set
	rows := m.settingsRows()
	if st.editing >= 0 {
		switch k.String() {
		case "esc":
			st.editing, st.err = -1, ""
			return m, nil
		case "enter":
			return m, m.settingsSubmit()
		}
		var c tea.Cmd
		st.input, c = st.input.Update(k)
		return m, c
	}
	st.err = ""
	switch k.String() {
	case "up", "k":
		st.sel = max(st.sel-1, 0)
	case "down", "j":
		st.sel = min(st.sel+1, len(rows)-1)
	case "x":
		if rows[st.sel].kind == setRowMascot {
			c := m.c
			return m, func() tea.Msg {
				p, err := c.ResetMascot()
				return settingsDoneMsg{what: "back to the T-rex", err: err, p: &p}
			}
		}
	case "enter", " ":
		switch r := rows[st.sel]; r.kind {
		case setRowName:
			st.input = textinput.New()
			st.input.Prompt, st.input.CharLimit = "❯ ", 32
			st.input.SetValue(m.persona.Name)
			st.input.Focus()
			st.editing = setRowName
			return m, textinput.Blink
		case setRowMascot:
			m.setup = newSetup(m.c, m.opts, m.persona)
			m.setup.step = setupDescribe
			m.setup.name.Blur()
			m.setup.desc.Focus()
			m.setup.fromSettings = true
			return m, m.setup.Init()
		case setRowPreset:
			c, name := m.c, r.preset
			return m, func() tea.Msg {
				t, err := c.SetTheme(authapi.ThemeUpdate{Preset: name})
				return settingsDoneMsg{what: "theme: " + name, err: err, t: &t}
			}
		case setRowColor:
			st.input = textinput.New()
			st.input.Prompt, st.input.CharLimit = "❯ ", 200
			st.input.Placeholder = "accent=#ff8a3d ok=#9bd36b"
			st.input.PlaceholderStyle = stFaint
			st.input.Focus()
			st.editing = setRowColor
			return m, textinput.Blink
		case setRowReset:
			c := m.c
			return m, func() tea.Msg {
				t, err := c.ResetTheme()
				return settingsDoneMsg{what: "default theme", err: err, t: &t}
			}
		}
	}
	return m, nil
}

func (m *Model) settingsSubmit() tea.Cmd {
	st, c := m.set, m.c
	val := strings.TrimSpace(st.input.Value())
	switch st.editing {
	case setRowName:
		if !nameRe.MatchString(val) {
			st.err = "a name is 1 to 32 lowercase letters, digits and hyphens, like jarvis or dev-box"
			return nil
		}
		return func() tea.Msg {
			p, err := c.SetName(val)
			return settingsDoneMsg{what: "renamed to " + val, err: err, p: &p}
		}
	case setRowColor:
		colors := map[string]string{}
		for _, f := range strings.Fields(val) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				st.err = fmt.Sprintf("%q isn't TOKEN=#hex", f)
				return nil
			}
			colors[k] = v
		}
		if len(colors) == 0 {
			st.editing = -1
			return nil
		}
		return func() tea.Msg {
			t, err := c.SetTheme(authapi.ThemeUpdate{Colors: colors})
			return settingsDoneMsg{what: "theme updated", err: err, t: &t}
		}
	}
	return nil
}

func (m *Model) settingsDone(msg settingsDoneMsg) {
	if m.set == nil {
		m.set = newSettings()
	}
	if msg.err != nil {
		m.set.err = errMessage(msg.err)
		return
	}
	m.set.editing, m.set.err = -1, ""
	if msg.p != nil {
		m.persona = *msg.p
	}
	if msg.t != nil {
		m.applyTheme(*msg.t)
	}
	m.flash = msg.what
}

func (m *Model) settingsView(h int) []string {
	st := m.set
	rows := m.settingsRows()
	st.sel = min(st.sel, len(rows)-1)
	accent := lipgloss.NewStyle().Foreground(hAccent).Bold(true)
	head := func(s string) string { return accent.Render(s) }
	line := func(i int, label, value, hint string) string {
		mark, lst := "  ", stText
		if i == st.sel {
			mark, lst = stAccent.Render("▸ "), stBold
		}
		s := mark + lst.Render(label) + blank(max(10-lipgloss.Width(label), 0)) + " " + value
		if hint != "" && i == st.sel {
			s += "  " + stFaint.Render(hint)
		}
		return s
	}
	cur := m.theme.Preset
	if cur == "" {
		cur = authapi.DefaultThemePreset
	}
	var out []string
	out = append(out, head("Authority"))
	for i, r := range rows {
		switch r.kind {
		case setRowName:
			v := stBold.Render(m.brand())
			if st.editing == setRowName {
				v = st.input.View()
			}
			out = append(out, line(i, "Name", v, "enter rename"))
		case setRowMascot:
			v := stText.Render("the T-rex")
			if mc := m.persona.Mascot; mc != nil {
				v = stText.Render(cmpOr(truncRunes(mc.Prompt, 40), "custom"))
			}
			out = append(out, line(i, "Mascot", v, "enter draw a new one · x the T-rex"), "", head("Theme"))
		case setRowPreset:
			p := authapi.FindThemePreset(r.preset)
			on := "○"
			if r.preset == cur {
				on = stAccent.Render("●")
			}
			desc := ""
			if p != nil {
				desc = paletteSwatch(p.Colors) + "  " + stDim.Render(p.Description)
			}
			out = append(out, line(i, on+" "+r.preset, desc, "enter use"))
		case setRowColor:
			v := stDim.Render(overridesText(m.theme))
			if st.editing == setRowColor {
				v = st.input.View()
			}
			out = append(out, line(i, "Colors", v, "enter set tokens, like accent=#ff8a3d"))
		case setRowReset:
			out = append(out, line(i, "Reset", stDim.Render("back to the default theme"), "enter reset"))
		}
	}
	if st.err != "" {
		out = append(out, "", lipgloss.NewStyle().Foreground(hRed).Render("▲ "+st.err))
	}
	out = append(out, "", head("Tokens"))
	toks := m.theme.Tokens
	if len(toks) == 0 {
		toks = authapi.ThemeTokens
	}
	for _, t := range toks {
		c := m.theme.Resolved[t.Name]
		over := " "
		if _, ok := m.theme.Colors[t.Name]; ok {
			over = stAccent.Render("*")
		}
		out = append(out, "  "+lipgloss.NewStyle().Background(lipgloss.Color(c)).Render("  ")+over+fmt.Sprintf(" %-8s ", t.Name)+stDim.Render(c+"  "+t.Description))
	}
	box := strings.Join(out, "\n")
	if m.haveLogo() && m.w >= 110 {
		box = lipgloss.JoinHorizontal(lipgloss.Top, box, "    ", m.logo(m.phase))
	}
	return center(box, m.w, h)
}

func (m *Model) settingsChips() []string {
	if m.settingsEditing() {
		return []string{chip("enter", "save"), chip("esc", "cancel")}
	}
	return []string{chip("↑↓", "move"), chip("enter", "change"), chip("tab", "pack"), chip("?", "help"), chip("q", "quit")}
}

func paletteSwatch(cs map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"bg", "panel", "text", "accent", "accent2", "ok", "warn", "err"} {
		b.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(cs[k])).Render(" "))
	}
	return b.String()
}

func overridesText(t authapi.ThemeInfo) string {
	if len(t.Colors) == 0 {
		return "no overrides"
	}
	var parts []string
	for _, tok := range authapi.ThemeTokens {
		if v, ok := t.Colors[tok.Name]; ok {
			parts = append(parts, tok.Name+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
