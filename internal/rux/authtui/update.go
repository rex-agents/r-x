package authtui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layoutConversation()
	case tickMsg:
		m.phase += 0.25
		if int(m.phase*4)%20 == 0 {
			m.shiftActivity()
		}
		return m, tick()
	case packMsg:
		m.pack, m.havePack, m.err = msg.pack, true, ""
		m.clamp()
		return m, m.wantPreview()
	case projectsMsg:
		for _, p := range msg.ps {
			m.projects[p.ID] = p
		}
	case liveMsg:
		m.live = bool(msg)
		if m.live {
			m.since = m.opts.Now()
		}
		return m, m.waitFeed()
	case feedMsg:
		m.applyFeed(msg.ev)
		return m, tea.Batch(m.waitFeed(), m.wantPreview())
	case errMsg:
		m.err = msg.err.Error()
	case eventsMsg:
		m.events[msg.id] = msg.evs
		if m.convID == msg.id {
			m.refreshConversation()
		}
	case createdMsg:
		if msg.err != nil {
			m.flash = "new thread failed: " + msg.err.Error()
			return m, nil
		}
		m.flash = "thread created"
		cmds := []tea.Cmd{m.fetchPack()}
		if msg.id != "" {
			cmds = append(cmds, m.openThread(msg.id))
		}
		return m, tea.Batch(cmds...)
	case sentMsg:
		if msg.err != nil {
			m.flash = "send failed: " + msg.err.Error()
		}
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// wantPreview fetches the selected thread's events when we don't have them.
func (m *Model) wantPreview() tea.Cmd {
	id := m.previewThread()
	if id == "" {
		return nil
	}
	if _, ok := m.events[id]; ok {
		return nil
	}
	m.events[id] = []StoredEvent{} // mark as requested
	return m.fetchEvents(id)
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		m.Close()
		return m, tea.Quit
	}
	m.flash = ""
	switch {
	case m.form != nil:
		return m.formKey(k)
	case m.help:
		m.help = false
		return m, nil
	case m.tab == tabThread:
		return m.convKey(k)
	}
	switch s {
	case "q":
		m.Close()
		return m, tea.Quit
	case "?":
		m.help = true
	case "tab":
		if m.tab == tabPack {
			m.tab = tabMonitor
		} else {
			m.tab = tabPack
		}
	case "1":
		m.tab = tabPack
	case "2":
		if m.convID != "" {
			m.tab = tabThread
		}
	case "3":
		m.tab = tabMonitor
	case "a":
		m.archived = !m.archived
		m.clamp()
	case "n":
		m.openForm()
		return m, textinput.Blink
	case "esc":
		m.tab = tabPack
	}
	if m.tab != tabPack {
		return m, nil
	}
	switch s {
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "left", "h":
		m.focus = max(m.focus-1, 0)
	case "right", "l":
		if m.selected(m.focus+1) != nil && m.focus+1 < numCols {
			m.focus++
		}
	case "enter":
		n := m.current()
		switch {
		case n == nil:
		case n.th != nil:
			return m, m.openThread(n.th.ID)
		case m.focus+1 < numCols && m.selected(m.focus+1) != nil:
			m.focus++
		}
	}
	return m, m.wantPreview()
}

func (m *Model) move(d int) {
	n := len(m.column(m.focus))
	if n == 0 {
		return
	}
	m.sel[m.focus] = min(max(m.sel[m.focus]+d, 0), n-1)
	// Children of a different parent start at the top.
	for c := m.focus + 1; c < numCols; c++ {
		m.sel[c] = 0
	}
	m.clamp()
}

// ---- conversation ----

func (m *Model) openThread(id string) tea.Cmd {
	m.convID, m.tab, m.follow = id, tabThread, true
	m.input.SetValue("")
	m.input.Focus()
	m.layoutConversation()
	m.refreshConversation()
	if len(m.events[id]) > 0 {
		return textinput.Blink
	}
	return tea.Batch(m.fetchEvents(id), textinput.Blink)
}

func (m *Model) convKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.tab = tabPack
		m.input.Blur()
		return m, nil
	case "tab":
		m.tab = tabMonitor
		return m, nil
	case "pgup", "pgdown", "up", "down":
		m.follow = false
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		if m.vp.AtBottom() {
			m.follow = true
		}
		return m, cmd
	case "enter":
		text := trim(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.SetValue("")
		id := m.convID
		m.follow = true
		return m, func() tea.Msg { return sentMsg{m.c.Send(id, text)} }
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

// ---- new thread ----

func (m *Model) openForm() {
	in := textinput.New()
	in.Prompt = "❯ "
	in.Placeholder = "what should the thread do?"
	in.Focus()
	f := &newForm{prompt: in, choices: []string{"auto (the leader places it)"}}
	for _, a := range m.pack.Authorities {
		f.choices = append(f.choices, a.Name)
	}
	if n := m.selected(colProject); n != nil {
		f.project, f.projLabel = n.project, n.name
	}
	m.form = f
}

func (m *Model) formKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	switch k.String() {
	case "esc":
		m.form = nil
		return m, nil
	case "tab", "down":
		f.field = (f.field + 1) % 3
		f.syncFocus()
		return m, nil
	case "shift+tab", "up":
		f.field = (f.field + 2) % 3
		f.syncFocus()
		return m, nil
	case "ctrl+l":
		f.local = !f.local
		return m, nil
	case "enter":
		prompt := trim(f.prompt.Value())
		if prompt == "" {
			m.flash = "type a prompt first"
			return m, nil
		}
		req := CreateReq{Project: f.project, Prompt: prompt, Placement: authapi.Placement{Local: f.local}}
		if f.auth > 0 {
			req.Placement.Authority = f.choices[f.auth]
		}
		m.form = nil
		return m, func() tea.Msg {
			id, err := m.c.CreateThread(req)
			return createdMsg{id, err}
		}
	}
	switch f.field {
	case 0:
		var cmd tea.Cmd
		f.prompt, cmd = f.prompt.Update(k)
		return m, cmd
	case 1:
		switch k.String() {
		case "left", "right", " ", "h", "l":
			f.local = !f.local
		}
	case 2:
		switch k.String() {
		case "left", "h":
			f.auth = (f.auth + len(f.choices) - 1) % len(f.choices)
		case "right", "l", " ":
			f.auth = (f.auth + 1) % len(f.choices)
		}
	}
	return m, nil
}

func (f *newForm) syncFocus() {
	if f.field == 0 {
		f.prompt.Focus()
	} else {
		f.prompt.Blur()
	}
}

func uptime(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

var _ = strings.TrimSpace

func durMs(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }
