package rux

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// rux view NAME shows one agent of a running rux session, live, and talks to it: what you
// type goes to the agent, and esc interrupts it. rux opens it in a Rex pane with /pane.

type ctlClient struct{ http *http.Client }

func newCtlClient(sock string) *ctlClient {
	return &ctlClient{http: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}}
}

func (c *ctlClient) get(path string, v any) error {
	resp, err := c.http.Get("http://rux" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *ctlClient) post(path, body string) error {
	resp, err := c.http.Post("http://rux"+path, "text/plain", strings.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// newestSocket is the most recent rux session's socket.
func newestSocket() string {
	matches, _ := filepath.Glob(filepath.Join(runSocketDir(), "*.sock"))
	sort.Slice(matches, func(i, j int) bool {
		a, _ := os.Stat(matches[i])
		b, _ := os.Stat(matches[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func runView(args []string) {
	fs := flag.NewFlagSet("rux view", flag.ExitOnError)
	sock := fs.String("sock", "", "the rux session's control socket (default: the newest)")
	_ = fs.Parse(parseInterleaved(fs, args))
	if *sock == "" {
		*sock = newestSocket()
	}
	if *sock == "" {
		fail("rux view: no rux session is running")
	}
	name := "lead"
	if fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	c := newCtlClient(*sock)
	var info agentInfo
	if err := c.get("/agents/"+name, &info); err != nil {
		fail("rux view: %s: %v", name, err)
	}
	zone.NewGlobal()
	ta := textarea.New()
	ta.Placeholder = "Message " + cmpOr(info.Label, name)
	ta.ShowLineNumbers, ta.Prompt, ta.CharLimit = false, "", 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = transcript.StFaint
	ta.Focus()
	m := &viewModel{c: c, name: name, info: info, input: ta, tr: transcript.New(), vp: viewport.New(80, 20),
		follow: true, expanded: map[int]bool{}}
	var kk kittyKeys
	_, run := kk.run(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := run(); err != nil {
		fail("rux view: %v", err)
	}
}

type viewPoll struct{}

type viewModel struct {
	c         *ctlClient
	name      string
	info      agentInfo
	gone      bool
	w, h      int
	vp        viewport.Model
	input     textarea.Model
	tr        *transcript.Renderer
	follow    bool
	expandAll bool
	expanded  map[int]bool
	flash     string
}

func (m *viewModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.tr.Spin.Tick, m.poll())
}

func (m *viewModel) poll() tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return viewPoll{} })
}

func (m *viewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.SetWidth(max(m.w-6, 10))
		m.refresh()
		return m, nil
	case viewPoll:
		var info agentInfo
		if err := m.c.get("/agents/"+m.name, &info); err != nil {
			m.gone = true
		} else {
			m.info, m.gone = info, false
		}
		m.refresh()
		return m, m.poll()
	case spinner.TickMsg:
		var c tea.Cmd
		m.tr.Spin, c = m.tr.Spin.Update(msg)
		return m, c
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft && m.info.Snap != nil {
			for i := range m.info.Snap.Entries {
				if zone.Get(fmt.Sprintf("v:%d", i)).InBounds(msg) {
					m.expanded[i] = !m.expanded[i]
					m.follow = false
					m.refresh()
				}
			}
			return m, nil
		}
		var c tea.Cmd
		m.vp, c = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, c
	case shiftEnterMsg:
		m.input.InsertString("\n")
		return m, nil
	case tea.KeyMsg:
		m.flash = ""
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			if m.input.Value() != "" && msg.String() == "ctrl+c" {
				m.input.Reset()
				return m, nil
			}
			return m, tea.Quit
		case "esc":
			_ = m.c.post("/agents/"+m.name+"/interrupt", "")
			m.flash = "Interrupted"
			return m, nil
		case "ctrl+e":
			m.expandAll = !m.expandAll
			m.refresh()
			return m, nil
		case "pgup", "ctrl+u":
			m.vp.HalfViewUp()
			m.follow = false
			return m, nil
		case "pgdown", "ctrl+f":
			m.vp.HalfViewDown()
			m.follow = m.vp.AtBottom()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			if text != "" {
				if err := m.c.post("/agents/"+m.name+"/send", text); err != nil {
					m.flash = "rux isn't running anymore"
				}
				m.follow = true
			}
			return m, nil
		}
	}
	var c tea.Cmd
	m.input, c = m.input.Update(msg)
	return m, c
}

func (m *viewModel) refresh() {
	if m.w == 0 || m.info.Snap == nil {
		return
	}
	m.vp.Width = m.w - 4
	m.vp.SetContent(m.tr.Render(*m.info.Snap, m.vp.Width, m.expandAll, m.expanded, "v:"))
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *viewModel) View() string {
	if m.w == 0 {
		return ""
	}
	in := m.info
	border := transcript.StFaint
	model := strings.ToLower(prettyModel(in.Model))
	if in.Effort != "" {
		model += " · " + strings.ToLower(effortNames[in.Effort])
	}
	where := in.Where
	if in.Dir != "" {
		where += ":" + in.Dir
	}
	where = transcript.Truncate(where, m.w/2)
	status := string(in.Status)
	if m.gone {
		status = "rux has ended"
	}
	left := transcript.StText.Render(cmpOr(in.Label, in.Name)) + " " + transcript.StFaint.Render(status)
	prompt := transcript.PromptBox(m.w, border, left, model, func(s string) string { return s }, where, m.input.View())
	if m.flash != "" {
		prompt = lipgloss.PlaceHorizontal(m.w, lipgloss.Right, transcript.StDim.Render(m.flash)+"  ") + "\n" + prompt
	}
	rest := max(m.h-lipgloss.Height(prompt), 3)
	m.vp.Height = rest - 1
	if m.follow {
		m.vp.GotoBottom()
	}
	body := "\n" + lipgloss.NewStyle().PaddingLeft(2).Render(m.vp.View())
	body = lipgloss.NewStyle().Height(rest).MaxHeight(rest).Render(body)
	return zone.Scan(lipgloss.JoinVertical(lipgloss.Left, body, prompt))
}
