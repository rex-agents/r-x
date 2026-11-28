package authtui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// Options configure the TUI.
type Options struct {
	// Logo renders the animated T-rex at the given shimmer phase (rux passes trexLogo).
	// Nil means a plain wordmark.
	Logo func(phase float64) string
	// Now is the clock (tests set it).
	Now func() time.Time
}

type tab int

const (
	tabPack tab = iota
	tabMonitor
	// tabThread is an open conversation (a thread, a project's manager or a sub-agent). It has no
	// header tab: the header shows it as a crumb after PACK.
	tabThread
)

var tabNames = []string{"PACK", "MONITOR"}

// Messages.
type (
	packMsg     struct{ pack authapi.Pack }
	projectsMsg struct{ ps []Project }
	feedMsg     struct{ ev SSE }
	liveMsg     bool
	tickMsg     time.Time
	errMsg      struct{ err error }
	eventsMsg   struct {
		id  string
		evs []StoredEvent
	}
	mgrStartedMsg struct {
		mgr *mgrConv
		id  string
		err error
	}
	createdMsg struct {
		id  string
		err error
	}
	sentMsg struct{ err error }
)

// Model is the bubbletea model of the authority TUI.
type Model struct {
	c    *Client
	opts Options

	w, h int
	tab  tab

	pack     authapi.Pack // Clients come from pack and pack.clients events
	havePack bool
	projects map[string]Project
	live     bool
	since    time.Time // when the feed last connected
	err      string
	flash    string

	focus    int
	sel      [numCols]int
	mfocus   int // the MONITOR tab's focused column
	msel     [numMCols]int
	archived bool

	act    map[string][]int // thread id → activity buckets
	events map[string][]StoredEvent
	phase  float64

	// conversation
	convID  string
	convSub string // a sub-agent of convID: read-only
	vp      viewport.Model
	input   textinput.Model
	follow  bool
	conv    threadData // the open conversation, decoded
	tr      *transcript.Renderer
	// expandAll opens every tool call; expanded opens single ones, by conversation slot.
	expandAll bool
	expanded  map[string]map[int]bool
	zones     map[string]func() // clickable entries of the frame

	mgr   *mgrConv // a project's manager conversation that has no thread yet
	form  *newForm
	pform *projForm // the new-project modal
	home  string    // the authority's home directory, once the directory browser has said
	help  bool

	feed   chan tea.Msg
	cancel context.CancelFunc
}

// mgrConv is the conversation with a project's manager before the manager exists: empty
// until the first message is sent, then starting until the API returns the thread.
type mgrConv struct {
	project, name string
	text          string // the first message, once sent
	starting      bool
	err           string
}

type newForm struct {
	prompt    textinput.Model
	field     int // 0 prompt, 1 placement, 2 authority
	local     bool
	auth      int // index into authChoices; 0 = let the leader place
	choices   []string
	project   string
	projLabel string
}

// New returns the TUI model for the authority behind c.
func New(c *Client, o Options) *Model {
	zone.NewGlobal() // click zones for the thread view
	if o.Now == nil {
		o.Now = time.Now
	}
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "Message this thread"
	in.PlaceholderStyle = stFaint
	return &Model{
		c: c, opts: o, w: 120, h: 40, mfocus: mcHost,
		projects: map[string]Project{},
		act:      map[string][]int{},
		events:   map[string][]StoredEvent{},
		input:    in, vp: viewport.New(80, 20), follow: true,
		tr: transcript.New(), expanded: map[string]map[int]bool{}, zones: map[string]func(){},
		feed: make(chan tea.Msg, 256),
	}
}

// Run opens the TUI on the authority at baseURL.
func Run(baseURL string, o Options) error {
	m := New(NewClient(baseURL), o)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	m.Close()
	return err
}

// Close stops the live feed.
func (m *Model) Close() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Model) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go m.follow_(ctx)
	return tea.Batch(m.fetchPack(), m.fetchProjects(), m.waitFeed(), tick(), m.tr.Spin.Tick)
}

func tick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// follow_ keeps GET /api/pack/stream open, reconnecting until ctx ends.
func (m *Model) follow_(ctx context.Context) {
	for ctx.Err() == nil {
		m.feed <- liveMsg(true)
		err := m.c.Stream(ctx, "/api/pack/stream", func(e SSE) { m.feed <- feedMsg{e} })
		if ctx.Err() != nil {
			return
		}
		m.feed <- liveMsg(false)
		_ = err
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (m *Model) waitFeed() tea.Cmd {
	return func() tea.Msg { return <-m.feed }
}

func (m *Model) fetchPack() tea.Cmd {
	return func() tea.Msg {
		p, err := m.c.Pack()
		if err != nil {
			return errMsg{err}
		}
		return packMsg{p}
	}
}

func (m *Model) fetchProjects() tea.Cmd {
	return func() tea.Msg {
		ps, err := m.c.Projects()
		if err != nil {
			return nil // optional: names fall back to ids
		}
		return projectsMsg{ps}
	}
}

func (m *Model) fetchEvents(id, sub string) tea.Cmd {
	return func() tea.Msg {
		var evs []StoredEvent
		var err error
		key := id
		if sub != "" {
			key = id + "/" + sub
			evs, err = m.c.SubAgentEvents(id, sub, 80)
		} else {
			evs, err = m.c.ThreadEvents(id, 80)
		}
		if err != nil {
			return errMsg{err}
		}
		return eventsMsg{key, evs}
	}
}

func (m *Model) bump(id string) {
	b := m.act[id]
	if len(b) == 0 {
		b = make([]int, sparkLen)
	}
	b[len(b)-1]++
	m.act[id] = b
}

const sparkLen = 8

func (m *Model) shiftActivity() {
	for id, b := range m.act {
		copy(b, b[1:])
		b[len(b)-1] = 0
		m.act[id] = b
	}
}

func (m *Model) applyFeed(e SSE) tea.Cmd {
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(e.Data), &env) != nil {
		return nil
	}
	name, data := env.Type, []byte(env.Data)
	switch name {
	case authapi.EventPack:
		var p authapi.Pack
		if json.Unmarshal(data, &p) == nil {
			m.setPack(p)
			m.noteUpdates()
		}
	case authapi.EventPackDelta:
		var a authapi.AuthorityInfo
		if json.Unmarshal(data, &a) == nil {
			found := false
			for i := range m.pack.Authorities {
				if m.pack.Authorities[i].Name == a.Name {
					if a.Threads == nil {
						a.Threads = m.pack.Authorities[i].Threads
					}
					m.pack.Authorities[i], found = a, true
				}
			}
			if !found {
				m.pack.Authorities = append(m.pack.Authorities, a)
			}
		}
	case authapi.EventPackClients:
		var cs []authapi.ClientInfo
		if json.Unmarshal(data, &cs) == nil {
			m.pack.Clients = cs
		}
	case authapi.EventThreadStatus:
		var t authapi.ThreadSummary
		if json.Unmarshal(data, &t) == nil {
			m.upsertThread(t)
			m.bump(t.ID)
			if t.ID == m.convID {
				m.repaint()
			}
		}
	case authapi.EventThreadEvent:
		var te authapi.ThreadEvent
		if json.Unmarshal(data, &te) == nil {
			m.bump(te.Thread)
			if se, ok := decodeStored(te.Event); ok {
				m.events[te.Thread] = appendEvent(m.events[te.Thread], se)
				if m.convSlot() == te.Thread {
					m.refreshConversation()
				}
			}
		}
	}
	m.clamp()
	return nil
}

// noteUpdates counts threads whose Updated changed as activity.
func (m *Model) noteUpdates() {}

func appendEvent(evs []StoredEvent, e StoredEvent) []StoredEvent {
	if e.ID != 0 && len(evs) > 0 && evs[len(evs)-1].ID >= e.ID {
		return evs
	}
	evs = append(evs, e)
	if len(evs) > 400 {
		evs = evs[len(evs)-400:]
	}
	return evs
}

// decodeStored reads a stored event record: {"id","ts","ev"}.
func decodeStored(v any) (StoredEvent, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return StoredEvent{}, false
	}
	var se StoredEvent
	return se, json.Unmarshal(b, &se) == nil && len(se.Ev) > 0
}

func (m *Model) upsertThread(t authapi.ThreadSummary) {
	for i := range m.pack.Authorities {
		a := &m.pack.Authorities[i]
		for j := range a.Threads {
			if a.Threads[j].ID == t.ID {
				if t.Authority == a.Name || t.Authority == "" {
					a.Threads[j] = t
					return
				}
				a.Threads = append(a.Threads[:j], a.Threads[j+1:]...)
				break
			}
		}
	}
	for i := range m.pack.Authorities {
		if a := &m.pack.Authorities[i]; a.Name == t.Authority {
			a.Threads = append(a.Threads, t)
			return
		}
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func trim(s string) string { return strings.TrimSpace(s) }

// convSlot is the key of the open conversation in m.events: the thread id, or id/sub-agent.
func (m *Model) convSlot() string {
	if m.convSub != "" {
		return m.convID + "/" + m.convSub
	}
	return m.convID
}

// subSummary: where a sub-agent runs, and its guest when it has one.
func subSummary(s authapi.SubAgentSummary) string {
	where := "on " + cmpOr(s.Authority, "this host")
	if s.Guest != "" {
		return where + " · " + s.Guest
	}
	return where
}

// setPack takes a pack snapshot. The first one selects the authority we're attached to, as the
// web app does.
func (m *Model) setPack(p authapi.Pack) {
	first := !m.havePack
	m.pack, m.havePack, m.err = p, true, ""
	if first {
		for i, a := range p.Authorities {
			if a.Local {
				m.msel[mcHost] = i
			}
		}
	}
}
