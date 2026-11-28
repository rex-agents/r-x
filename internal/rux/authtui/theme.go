package authtui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// The theme (GET /api/theme): the TUI takes the palette from it at start, and again whenever the
// feed sends a "theme" event, so a change made anywhere (the web app, `rux theme`, an agent)
// repaints this screen.

// Theme is GET /api/theme.
func (c *Client) Theme() (authapi.ThemeInfo, error) {
	var t authapi.ThemeInfo
	err := c.do("GET", "/api/theme", nil, &t)
	return t, err
}

// SetTheme is PUT /api/theme.
func (c *Client) SetTheme(u authapi.ThemeUpdate) (authapi.ThemeInfo, error) {
	var t authapi.ThemeInfo
	err := c.do("PUT", "/api/theme", u, &t)
	return t, err
}

type themeMsg struct {
	t   authapi.ThemeInfo
	err error
}

func (m *Model) fetchTheme() tea.Cmd {
	return func() tea.Msg {
		t, err := m.c.Theme()
		return themeMsg{t, err}
	}
}

// applyTheme recolors the TUI.
func (m *Model) applyTheme(t authapi.ThemeInfo) {
	m.theme = t
	transcript.ApplyTheme(t)
	m.repaint()
}
