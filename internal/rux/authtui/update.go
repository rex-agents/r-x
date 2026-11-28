package authtui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

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
		m.setPack(msg.pack)
		m.clamp()
		return m, m.wantPreview()
	case personaMsg:
		if msg.err == nil {
			m.persona = msg.p
			if !msg.p.Setup {
				m.setup = newSetup(m.c, m.opts, msg.p)
				return m, m.setup.Init()
			}
		}
	case setupNamedMsg, setupDrawnMsg, setupKeptMsg, setupTickMsg:
		if m.setup == nil {
			return m, nil
		}
		cmd := m.setup.update(msg)
		m.finishSetup()
		return m, cmd
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
		if m.convSlot() == msg.id {
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
	case mgrStartedMsg:
		msg.mgr.starting = false
		if msg.err != nil {
			msg.mgr.err = "the manager did not start: " + msg.err.Error()
			if m.mgr == msg.mgr {
				m.input.SetValue(msg.mgr.text)
				m.input.CursorEnd()
				m.flash = msg.mgr.err
				m.repaint()
			}
			return m, nil
		}
		cmds := []tea.Cmd{m.fetchPack()}
		if m.mgr == msg.mgr && msg.id != "" {
			m.mgr = nil
			cmds = append(cmds, m.openThread(msg.id))
		}
		return m, tea.Batch(cmds...)
	case guestActMsg:
		if msg.err != nil {
			m.flash = "guest: " + errMessage(msg.err)
		} else {
			m.flash = msg.done
		}
		return m, m.fetchPack()
	case shellDoneMsg:
		if msg.err != nil {
			m.flash = "shell: " + errMessage(msg.err)
		} else {
			m.flash = ""
		}
	case sentMsg:
		if msg.err != nil {
			m.flash = "send failed: " + msg.err.Error()
		}
	case spinner.TickMsg:
		var c tea.Cmd
		m.tr.Spin, c = m.tr.Spin.Update(msg)
		if m.setup != nil {
			c = tea.Batch(c, m.setup.update(msg))
		}
		if m.tab == tabThread && m.busy() {
			m.repaint()
		}
		return m, c
	case reposMsg, dirsMsg, projectMsg:
		cmd, _ := m.projUpdate(msg)
		return m, cmd
	case tea.MouseMsg:
		return m, m.mouse(msg)
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
	return m.fetchEvents(id, "")
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		m.Close()
		return m, tea.Quit
	}
	m.flash = ""
	if m.setup != nil {
		return m.setupKey(k)
	}
	switch {
	case m.pform != nil:
		return m.projKey(k)
	case m.form != nil:
		return m.formKey(k)
	case m.help:
		m.help = false
		return m, nil
	case m.downAsk != nil:
		return m, m.downKey(s)
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
		m.tab = tabMonitor
	case "a":
		m.archived = !m.archived
		m.clamp()
	case "n":
		m.openForm()
		return m, textinput.Blink
	case "p":
		if m.tab == tabPack {
			return m, m.openProjForm()
		}
	case "esc":
		m.tab = tabPack
	}
	if m.tab == tabMonitor {
		cmd := m.monitorKey(s)
		return m, cmd
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
		if m.focus+1 < numCols && m.selected(m.focus+1) != nil {
			m.focus++
		}
	case "enter":
		n := m.current()
		switch {
		case n == nil:
		case n.th != nil:
			return m, m.openThread(n.th.ID)
		case n.kind == colProject && n.noMgr && !n.scratch:
			m.openManagerConv(n, "")
			return m, textinput.Blink
		case n.sub != nil:
			if t := m.selected(colThread); t != nil {
				return m, m.openConv(t.th.ID, n.sub.ID)
			}
		case m.focus+1 < numCols && m.selected(m.focus+1) != nil:
			m.focus++
		}
	default:
		// Typing on a project that has no manager starts one with what's typed.
		if n := m.current(); len(k.Runes) > 0 && m.focus == colProject && n != nil && n.noMgr && !n.scratch && !strings.ContainsRune("q?123an", k.Runes[0]) {
			m.openManagerConv(n, string(k.Runes))
			return m, textinput.Blink
		}
	}
	return m, m.wantPreview()
}

// monitorKey moves the MONITOR tab's cursor; enter on a guest opens its thread.
func (m *Model) monitorKey(s string) tea.Cmd {
	switch s {
	case "up", "k", "down", "j":
		d := 1
		if s == "up" || s == "k" {
			d = -1
		}
		if n := len(m.monitorColumn(m.mfocus)); n > 0 {
			m.msel[m.mfocus] = min(max(m.msel[m.mfocus]+d, 0), n-1)
		}
		switch m.mfocus {
		case mcClient: // the client's host is the one it connected through
			if c := m.mselected(mcClient); c != nil {
				for i, a := range m.pack.Authorities {
					if a.Name == c.client.Authority {
						m.msel[mcHost] = i
					}
				}
			}
			m.msel[mcGuest] = 0
		case mcHost:
			m.msel[mcGuest] = 0
		}
	case "left", "h":
		m.mfocus = max(m.mfocus-1, 0)
	case "right", "l":
		if m.mfocus+1 < numMCols && len(m.monitorColumn(m.mfocus+1)) > 0 {
			m.mfocus++
		}
	case "s", "d":
		return m.guestKey(s)
	case "enter":
		return m.shellKey()
	case "o":
		if n := m.mselected(m.mfocus); n != nil && n.guest != nil && n.guest.Thread != "" {
			return m.openThread(n.guest.Thread)
		}
	}
	m.clamp()
	return nil
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

func (m *Model) openThread(id string) tea.Cmd { return m.openConv(id, "") }

// openConv opens a thread's conversation, or one of its sub-agents' (read-only).
func (m *Model) openConv(id, sub string) tea.Cmd {
	m.convID, m.convSub, m.tab, m.follow, m.mgr = id, sub, tabThread, true, nil
	m.input.SetValue("")
	m.input.Focus()
	m.layoutConversation()
	m.refreshConversation()
	if sub != "" {
		m.input.Blur()
	}
	if len(m.events[m.convSlot()]) > 0 {
		return textinput.Blink
	}
	return tea.Batch(m.fetchEvents(id, sub), textinput.Blink)
}

// convKey takes the harness's keys: esc interrupts a working thread, ctrl+e opens every tool
// call, pgup, pgdown, home and end scroll. A sub-agent's conversation is read-only.
func (m *Model) convKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mgr != nil && k.String() == "esc" {
		m.mgr, m.tab = nil, tabPack
		m.input.Blur()
		return m, nil
	}
	switch k.String() {
	case "esc":
		if m.working() {
			id := m.convID
			m.flash = "Interrupted " + id
			return m, func() tea.Msg { return sentMsg{m.c.Interrupt(id)} }
		}
		m.tab = tabPack
		m.input.Blur()
		return m, nil
	case "tab":
		m.tab = tabMonitor
		return m, nil
	case "ctrl+e":
		m.expandAll = !m.expandAll
		m.repaint()
		return m, nil
	case "pgup", "ctrl+u", "ctrl+b":
		m.vp.HalfViewUp()
	case "pgdown", "ctrl+f":
		m.vp.HalfViewDown()
	case "home":
		m.vp.GotoTop()
	case "end":
		m.vp.GotoBottom()
	case "up", "down":
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		m.follow = m.vp.AtBottom()
		return m, cmd
	case "enter":
		text := trim(m.input.Value())
		if text == "" || m.convSub != "" {
			return m, nil
		}
		if g := m.mgr; g != nil {
			if g.starting {
				return m, nil
			}
			g.text, g.starting, g.err = text, true, ""
			m.input.SetValue("")
			m.follow = true
			m.repaint()
			return m, func() tea.Msg {
				id, err := m.c.StartManager(g.project, text)
				return mgrStartedMsg{g, id, err}
			}
		}
		m.input.SetValue("")
		id := m.convID
		m.follow = true
		return m, func() tea.Msg { return sentMsg{m.c.Send(id, text)} }
	default:
		if m.convSub != "" {
			return m, nil // read-only
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	}
	m.follow = m.vp.AtBottom()
	return m, nil
}

// mouse opens a tool call that's clicked, and scrolls the open conversation with the wheel.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if m.tab != tabThread || m.form != nil || m.pform != nil || m.help {
		return nil
	}
	if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
		for id, f := range m.zones {
			if zone.Get(id).InBounds(msg) {
				f()
				break
			}
		}
		return nil
	}
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		var c tea.Cmd
		m.vp, c = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return c
	}
	return nil
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

// openManagerConv opens the conversation with a project's manager, which has no thread yet;
// seed goes into the input.
func (m *Model) openManagerConv(n *node, seed string) {
	m.mgr = &mgrConv{project: n.project, name: n.name}
	m.convID, m.convSub, m.tab, m.follow = "", "", tabThread, true
	m.input.SetValue(seed)
	m.input.CursorEnd()
	m.input.Focus()
	m.layoutConversation()
	m.repaint()
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
