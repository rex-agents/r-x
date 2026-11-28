package authtui

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// The persona: the name and mascot the user gave their authority (GET /api/persona). The first
// time an authority opens it has no name, and the TUI shows the setup screen: name it, describe
// what the agent should look like, then accept, redraw or drop the mascot the model drew.

// Persona is GET /api/persona.
func (c *Client) Persona() (authapi.Persona, error) {
	var p authapi.Persona
	err := c.do("GET", "/api/persona", nil, &p)
	return p, err
}

// SetName is PUT /api/persona/name.
func (c *Client) SetName(name string) (authapi.Persona, error) {
	var p authapi.Persona
	err := c.do("PUT", "/api/persona/name", authapi.PersonaName{Name: name}, &p)
	return p, err
}

// DrawMascot is POST /api/persona/mascot without saving: a preview. A model call takes a while.
func (c *Client) DrawMascot(prompt string) (authapi.Mascot, error) {
	var m authapi.Mascot
	slow := *c
	slow.HTTP = &http.Client{Timeout: 3 * time.Minute, Transport: c.HTTP.Transport}
	err := slow.do("POST", "/api/persona/mascot", authapi.MascotRequest{Prompt: prompt}, &m)
	return m, err
}

// KeepMascot is PUT /api/persona/mascot.
func (c *Client) KeepMascot(m authapi.Mascot) error {
	return c.do("PUT", "/api/persona/mascot", m, nil)
}

// MascotRenderer draws a mascot at a shimmer phase; nil draws the T-rex. rux passes it in Options.
type MascotRenderer func(m *authapi.Mascot, phase float64) string

type personaMsg struct {
	p   authapi.Persona
	err error
}

func (m *Model) fetchPersona() tea.Cmd {
	return func() tea.Msg {
		p, err := m.c.Persona()
		return personaMsg{p, err}
	}
}

// brand is what the header calls this authority: its name, else "rux".
func (m *Model) brand() string {
	return cmpOr(m.persona.Name, "rux")
}

// logo draws the authority's logo: its mascot when it has one, else the T-rex.
func (m *Model) logo(phase float64) string {
	if m.persona.Mascot != nil && m.opts.Mascot != nil {
		return m.opts.Mascot(m.persona.Mascot, phase)
	}
	if m.opts.Logo != nil {
		return m.opts.Logo(phase)
	}
	return ""
}

// haveLogo says whether there's a logo to draw.
func (m *Model) haveLogo() bool {
	return m.opts.Logo != nil || (m.persona.Mascot != nil && m.opts.Mascot != nil)
}

// ---- the setup screen ----

const (
	setupName = iota
	setupDescribe
	setupDrawing
	setupPreview
)

var nameRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// setup is the first-run screen. It is a small state machine the authority TUI embeds and
// RunSetup runs by itself (the harness opens it before its own screen).
type setup struct {
	c      *Client
	opts   Options
	step   int
	name   textinput.Model
	desc   textinput.Model
	err    string
	mascot *authapi.Mascot // the preview
	spin   spinner.Model
	phase  float64
	done   bool
	result authapi.Persona
	// fromSettings: opened from the SETTINGS tab to draw a new mascot, not the first run.
	fromSettings bool
}

type (
	setupNamedMsg struct {
		p   authapi.Persona
		err error
	}
	setupDrawnMsg struct {
		m   authapi.Mascot
		err error
	}
	setupKeptMsg struct{ err error }
	setupTickMsg time.Time
)

func newSetup(c *Client, o Options, p authapi.Persona) *setup {
	name := textinput.New()
	name.Prompt = "❯ "
	name.SetValue(suggestName(p.HostName))
	name.CharLimit = 32
	name.Focus()
	desc := textinput.New()
	desc.Prompt = "❯ "
	desc.Placeholder = "a friendly robot butler with a bow tie"
	desc.PlaceholderStyle = stFaint
	desc.CharLimit = 300
	return &setup{c: c, opts: o, name: name, desc: desc, spin: spinner.New(spinner.WithSpinner(spinner.MiniDot)), result: p}
}

// suggestName turns a host name into a valid authority name.
func suggestName(host string) string {
	host = strings.ToLower(host)
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case b.Len() > 0:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	return cmpOr(s, "rux")
}

func (s *setup) Init() tea.Cmd { return tea.Batch(textinput.Blink, s.spin.Tick, s.tick()) }

func (s *setup) tick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg { return setupTickMsg(t) })
}

// busy says the screen is waiting on the authority; keys do nothing then, except to quit.
func (s *setup) busy() bool { return s.step == setupDrawing }

// update handles one message; cmd may be nil. s.done says the setup has finished.
func (s *setup) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case setupTickMsg:
		s.phase += 0.25
		return s.tick()
	case spinner.TickMsg:
		var c tea.Cmd
		s.spin, c = s.spin.Update(msg)
		return c
	case setupNamedMsg:
		if msg.err != nil {
			s.err = errMessage(msg.err)
			return nil
		}
		s.result, s.err, s.step = msg.p, "", setupDescribe
		s.name.Blur()
		s.desc.Focus()
		return textinput.Blink
	case setupDrawnMsg:
		if msg.err != nil {
			s.err, s.step = errMessage(msg.err), setupDescribe
			return nil
		}
		s.mascot, s.err, s.step = &msg.m, "", setupPreview
	case setupKeptMsg:
		if msg.err != nil {
			s.err, s.step = errMessage(msg.err), setupPreview
			return nil
		}
		s.result.Mascot = s.mascot
		s.done = true
	case tea.KeyMsg:
		return s.key(msg)
	}
	if s.step == setupName {
		var c tea.Cmd
		s.name, c = s.name.Update(msg)
		return c
	}
	if s.step == setupDescribe {
		var c tea.Cmd
		s.desc, c = s.desc.Update(msg)
		return c
	}
	return nil
}

func (s *setup) key(k tea.KeyMsg) tea.Cmd {
	switch s.step {
	case setupName:
		if k.String() == "enter" {
			name := strings.TrimSpace(s.name.Value())
			if !nameRe.MatchString(name) {
				s.err = "a name is 1 to 32 lowercase letters, digits and hyphens, like jarvis or dev-box"
				return nil
			}
			c := s.c
			return func() tea.Msg { p, err := c.SetName(name); return setupNamedMsg{p, err} }
		}
		s.err = ""
		var c tea.Cmd
		s.name, c = s.name.Update(k)
		return c
	case setupDescribe:
		switch k.String() {
		case "esc":
			s.done = true // keep the T-rex
		case "enter":
			desc := strings.TrimSpace(s.desc.Value())
			if desc == "" {
				s.done = true
				return nil
			}
			return s.draw(desc)
		default:
			var c tea.Cmd
			s.desc, c = s.desc.Update(k)
			return c
		}
	case setupPreview:
		switch k.String() {
		case "enter", "a", "y":
			m, c := *s.mascot, s.c
			return func() tea.Msg { return setupKeptMsg{c.KeepMascot(m)} }
		case "r":
			return s.draw(strings.TrimSpace(s.desc.Value()))
		case "k", "n":
			s.done = true
		case "esc":
			s.step, s.err = setupDescribe, ""
		}
	}
	return nil
}

func (s *setup) draw(desc string) tea.Cmd {
	s.step, s.err = setupDrawing, ""
	c := s.c
	return func() tea.Msg { m, err := c.DrawMascot(desc); return setupDrawnMsg{m, err} }
}

func (s *setup) draws(m *authapi.Mascot) string {
	switch {
	case m != nil && s.opts.Mascot != nil:
		return s.opts.Mascot(m, s.phase)
	case s.opts.Logo != nil:
		return s.opts.Logo(s.phase)
	}
	return ""
}

// view draws the setup screen in w columns.
func (s *setup) view(w int) string {
	pink := lipgloss.NewStyle().Foreground(transcript.Pink).Bold(true)
	field := func(label string, in textinput.Model, hint string, active bool) string {
		mark := "  "
		if active {
			mark = stAccent.Render("▸ ")
		}
		return mark + stBold.Render(label) + "\n  " + in.View() + "\n  " + stFaint.Render(hint)
	}
	var body []string
	body = append(body, pink.Render("Welcome to rux"), stDim.Render("Let's set up your authority."), "")
	switch s.step {
	case setupName:
		body = append(body, field("Name your authority", s.name, "lowercase letters, digits and hyphens; your agents call themselves this", true))
	case setupDescribe:
		body = append(body, stText.Render("Name  ")+stBold.Render(s.result.Name), "",
			field("Describe your agent (blank keeps the T-rex)", s.desc, "a model draws a mascot from this", true))
	case setupDrawing:
		body = append(body, stText.Render("Name  ")+stBold.Render(s.result.Name), "",
			s.spin.View()+" "+stText.Render("Drawing your mascot…"), stFaint.Render(strings.TrimSpace(s.desc.Value())))
	case setupPreview:
		body = append(body, stText.Render("Name  ")+stBold.Render(s.result.Name), "",
			lipgloss.NewStyle().PaddingLeft(2).Render(s.draws(s.mascot)), "",
			stDim.Render(fmt.Sprintf("%s  ·  %s", s.mascot.Model, strings.Join(s.mascot.Colors, " "))))
	}
	if s.err != "" {
		body = append(body, "", lipgloss.NewStyle().Foreground(hRed).Render("▲ "+s.err))
	}
	var chips []string
	switch s.step {
	case setupName:
		chips = []string{chip("enter", "next"), chip("ctrl+c", "quit")}
	case setupDescribe:
		chips = []string{chip("enter", "draw it"), chip("esc", "keep the T-rex")}
	case setupDrawing:
		chips = []string{chip("ctrl+c", "quit")}
	case setupPreview:
		chips = []string{chip("enter", "accept"), chip("r", "regenerate"), chip("k", "keep the T-rex"), chip("esc", "edit")}
	}
	body = append(body, "", strings.Join(chips, "  "))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(hAccent).Padding(1, 2).
		Width(min(max(w-4, 30), 74)).Render(strings.Join(body, "\n"))
}

// ---- inside the authority TUI ----

func (m *Model) setupKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd := m.setup.update(k)
	m.finishSetup()
	return m, cmd
}

func (m *Model) finishSetup() {
	if m.setup != nil && m.setup.done {
		m.persona = m.setup.result
		if m.setup.fromSettings {
			m.flash = "mascot unchanged"
			if m.persona.Mascot != nil && m.setup.mascot == m.persona.Mascot {
				m.flash = "new mascot saved"
			}
		} else {
			m.flash = "welcome, " + m.persona.Name
		}
		m.setup = nil
	}
}

// ---- on its own, for the harness ----

type setupProgram struct {
	s    *setup
	w, h int
}

func (p *setupProgram) Init() tea.Cmd { return p.s.Init() }

func (p *setupProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		p.w, p.h = ws.Width, ws.Height
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "ctrl+c" {
		return p, tea.Quit
	}
	cmd := p.s.update(msg)
	if p.s.done {
		return p, tea.Quit
	}
	return p, cmd
}

func (p *setupProgram) View() string {
	return lipgloss.Place(p.w, p.h, lipgloss.Center, lipgloss.Center, p.s.view(p.w))
}

// RunSetup shows the first-run screen on its own, against the authority at baseURL, and returns
// the persona the user ended with. It returns ok false when they quit before naming the authority.
func RunSetup(baseURL string, o Options) (p authapi.Persona, ok bool, err error) {
	c := NewClient(baseURL)
	cur, err := c.Persona()
	if err != nil || cur.Setup {
		return cur, cur.Setup, err
	}
	sp := &setupProgram{s: newSetup(c, o, cur), w: 80, h: 24}
	if _, err := tea.NewProgram(sp, tea.WithAltScreen()).Run(); err != nil {
		return cur, false, err
	}
	return sp.s.result, sp.s.result.Setup, nil
}
