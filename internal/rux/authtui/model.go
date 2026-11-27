package authtui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rex-agents/r-x/internal/rux/authapi"
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
	tabThread
	tabMonitor
)

var tabNames = []string{"PACK", "THREAD", "MONITOR"}

// Node kinds, which are also the graph's columns.
const (
	colHost = iota
	colProject
	colThread
	colSub
	numCols
)

const noProject = "(no project)"

type node struct {
	kind    int
	key     string // identity within its column
	name    string
	summary string
	status  string
	threads int
	host    *authapi.AuthorityInfo
	th      *authapi.ThreadSummary
	sub     *authapi.SubAgentSummary
	project string // for project nodes: the project id ("" for none)
}

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

	pack     authapi.Pack
	havePack bool
	projects map[string]Project
	live     bool
	since    time.Time // when the feed last connected
	err      string
	flash    string

	focus    int
	sel      [numCols]int
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

	form *newForm
	help bool

	feed   chan tea.Msg
	cancel context.CancelFunc
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
	if o.Now == nil {
		o.Now = time.Now
	}
	in := textinput.New()
	in.Prompt = "❯ "
	in.Placeholder = "message this thread"
	return &Model{
		c: c, opts: o, w: 120, h: 40,
		projects: map[string]Project{},
		act:      map[string][]int{},
		events:   map[string][]StoredEvent{},
		input:    in, vp: viewport.New(80, 20), follow: true,
		feed: make(chan tea.Msg, 256),
	}
}

// Run opens the TUI on the authority at baseURL.
func Run(baseURL string, o Options) error {
	m := New(NewClient(baseURL), o)
	p := tea.NewProgram(m, tea.WithAltScreen())
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
	return tea.Batch(m.fetchPack(), m.fetchProjects(), m.waitFeed(), tick())
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

// ---- data ----

func (m *Model) allThreads() []authapi.ThreadSummary {
	var out []authapi.ThreadSummary
	for _, a := range m.pack.Authorities {
		out = append(out, a.Threads...)
	}
	return out
}

func (m *Model) visible(t authapi.ThreadSummary) bool {
	return m.archived || (t.Status != "archived" && !t.Archived)
}

func (m *Model) projectName(id string) string {
	if id == "" {
		return noProject
	}
	if p, ok := m.projects[id]; ok && p.Name != "" {
		return p.Name
	}
	return id
}

// column returns the nodes of column c, given the selections to its left.
func (m *Model) column(c int) []node {
	switch c {
	case colHost:
		var out []node
		for i := range m.pack.Authorities {
			a := &m.pack.Authorities[i]
			n := 0
			for _, t := range a.Threads {
				if m.visible(t) {
					n++
				}
			}
			role := ""
			if a.Name == m.pack.Leader {
				role = " · leader"
			}
			out = append(out, node{kind: colHost, key: a.Name, name: a.Name, host: a, status: a.Status, threads: n,
				summary: fmt.Sprintf("%s · %d cpu · load %.1f%s", cmpOr(a.Host.OS, "?"), a.Host.CPUs, a.Host.Load, role)})
		}
		return out
	case colProject:
		h := m.selected(colHost)
		if h == nil {
			return nil
		}
		type agg struct {
			n, active int
			cost      float64
		}
		by := map[string]*agg{}
		for _, t := range h.host.Threads {
			if !m.visible(t) {
				continue
			}
			a := by[t.Project]
			if a == nil {
				a = &agg{}
				by[t.Project] = a
			}
			a.n++
			a.cost += t.Cost
			if t.Status == "running" || t.Status == "working" {
				a.active++
			}
		}
		var out []node
		for id, a := range by {
			st := "idle"
			if a.active > 0 {
				st = "running"
			}
			sum := m.projects[id].Repo
			if sum == "" {
				sum = fmt.Sprintf("%d active · $%.2f", a.active, a.cost)
			}
			out = append(out, node{kind: colProject, key: id, project: id, name: m.projectName(id), status: st, threads: a.n, summary: sum})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
		return out
	case colThread:
		h, p := m.selected(colHost), m.selected(colProject)
		if h == nil || p == nil {
			return nil
		}
		var out []node
		for i := range h.host.Threads {
			t := &h.host.Threads[i]
			if t.Project != p.project || !m.visible(*t) {
				continue
			}
			where := "vm"
			if t.Placement.Local {
				where = "local"
			}
			out = append(out, node{kind: colThread, key: t.ID, name: cmpOr(t.Title, t.ID), th: t, status: t.Status, threads: len(t.SubAgents),
				summary: fmt.Sprintf("%s · %s · $%.2f", t.Status, where, t.Cost)})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].th.Updated > out[j].th.Updated })
		return out
	case colSub:
		t := m.selected(colThread)
		if t == nil {
			return nil
		}
		var out []node
		for i := range t.th.SubAgents {
			s := &t.th.SubAgents[i]
			out = append(out, node{kind: colSub, key: s.ID, name: cmpOr(s.Name, s.ID), sub: s, status: s.Status,
				summary: cmpOr(s.Guest, "no guest")})
		}
		return out
	}
	return nil
}

// selected is the selected node of column c, or nil.
func (m *Model) selected(c int) *node {
	for i := 0; i < c; i++ {
		if len(m.column(i)) == 0 {
			return nil
		}
	}
	col := m.column(c)
	if len(col) == 0 {
		return nil
	}
	i := min(max(m.sel[c], 0), len(col)-1)
	return &col[i]
}

// current is the node under the cursor: the selection of the focused column.
func (m *Model) current() *node { return m.selected(m.focus) }

func (m *Model) clamp() {
	for c := 0; c < numCols; c++ {
		n := len(m.column(c))
		m.sel[c] = min(max(m.sel[c], 0), max(n-1, 0))
	}
	for m.focus > 0 && m.selected(m.focus) == nil {
		m.focus--
	}
}

// threadByID finds a thread anywhere in the pack.
func (m *Model) threadByID(id string) *authapi.ThreadSummary {
	for i := range m.pack.Authorities {
		for j := range m.pack.Authorities[i].Threads {
			if t := &m.pack.Authorities[i].Threads[j]; t.ID == id {
				return t
			}
		}
	}
	return nil
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

// preview data for the selected thread.
func (m *Model) previewThread() string {
	if n := m.current(); n != nil && n.th != nil {
		return n.th.ID
	}
	if n := m.current(); n != nil && n.kind == colSub {
		if t := m.selected(colThread); t != nil {
			return t.th.ID
		}
	}
	return ""
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
			m.pack, m.havePack, m.err = p, true, ""
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
	case authapi.EventThreadStatus:
		var t authapi.ThreadSummary
		if json.Unmarshal(data, &t) == nil {
			m.upsertThread(t)
			m.bump(t.ID)
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
