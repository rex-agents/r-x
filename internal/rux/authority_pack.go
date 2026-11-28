package rux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/rex-agents/r-x/internal/rux/quorum"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// PackSource is where /api/pack, /api/pack/stream and /api/threads?authority= get the pack's
// state. The authority ships localPack, a pack of one (leader = self, term 0, vault host =
// self) built from this authority's own threads. The quorum replaces it with the pack-wide
// view when this authority is the leader (or a proxy to the leader on a member) by calling
// SetPackSource. Implementations must be safe for concurrent use.
type PackSource interface {
	// Pack is the current snapshot.
	Pack(ctx context.Context) (authapi.Pack, error)
	// Subscribe streams live events (pack.delta, thread.event, thread.status) until ctx ends.
	// The channel is closed when the subscription ends. Slow readers may miss events.
	Subscribe(ctx context.Context) (<-chan authapi.PackEvent, error)
	// Threads lists the threads of one authority ("" for every authority in the pack).
	Threads(ctx context.Context, authority string) ([]authapi.ThreadSummary, error)
}

// A pack-wide PackSource (the quorum's) must keep the per-thread endpoints working for threads on
// other authorities: GET /api/threads/{id}/events, /events/page and /subagents/{sub}/events[/page]
// are plain /api requests, so the leader proxies them to the authority that owns the thread (found
// from ThreadSummary.authority) and streams the reply through unchanged.

// SetPackSource swaps in another pack view, such as the quorum's.
func (s *server) SetPackSource(p PackSource) {
	s.packMu.Lock()
	s.pack = p
	s.packMu.Unlock()
}

func (s *server) packSource() PackSource {
	s.packMu.Lock()
	defer s.packMu.Unlock()
	if s.pack != nil { // set by SetPackSource
		return s.pack
	}
	if s.node != nil && s.node.InPack() {
		if s.packAdapter == nil {
			s.packAdapter = &quorumPack{s: s, PackAdapter: quorum.NewPackAdapter(s.node)}
		}
		return s.packAdapter
	}
	if s.packLocal == nil {
		s.packLocal = &localPack{s: s}
	}
	return s.packLocal
}

// packHub fans events of this authority's threads out to subscribers.
type packHub struct {
	mu   sync.Mutex
	subs map[chan authapi.PackEvent]struct{}
}

func (h *packHub) subscribe() (chan authapi.PackEvent, func()) {
	ch := make(chan authapi.PackEvent, 256)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan authapi.PackEvent]struct{}{}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

func (h *packHub) active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) > 0
}

func (h *packHub) publish(ev authapi.PackEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // slow client: drop; it resyncs on the next snapshot
		}
	}
}

// localPack is the PackSource of an authority outside a quorum.
type localPack struct{ s *server }

func (p *localPack) Pack(ctx context.Context) (authapi.Pack, error) {
	return p.s.localPackView(), nil
}

func (p *localPack) Threads(ctx context.Context, authority string) ([]authapi.ThreadSummary, error) {
	if authority != "" && authority != p.s.authName() {
		return []authapi.ThreadSummary{}, nil
	}
	return p.s.threadSummaries(), nil
}

func (p *localPack) Subscribe(ctx context.Context) (<-chan authapi.PackEvent, error) {
	in, cancel := p.s.hub.subscribe()
	out := make(chan authapi.PackEvent, 256)
	go func() {
		defer close(out)
		defer cancel()
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		var last []byte
		cw, stopCW := p.s.clients.watch()
		defer stopCW()
		lastClients := clientKey(p.s.clients.list())
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- ev:
				default:
				}
			case <-cw:
				cs := p.s.clients.list()
				if k := clientKey(cs); k != lastClients {
					lastClients = k
					select {
					case out <- authapi.PackEvent{Type: authapi.EventPackClients, Data: cs}:
					default:
					}
				}
			case <-tick.C:
				info := p.s.localAuthority()
				info.Threads = nil
				b, _ := json.Marshal(info)
				if bytes.Equal(b, last) {
					continue
				}
				last = b
				select {
				case out <- authapi.PackEvent{Type: authapi.EventPackDelta, Data: info}:
				default:
				}
			}
		}
	}()
	return out, nil
}

// authName is this authority's name in the pack: ~/.rux/authority.json "name", else the host name.
func (s *server) authName() string {
	if s.name != "" {
		return s.name
	}
	h, _ := os.Hostname()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	if h == "" {
		h = "local"
	}
	return h
}

// localHost reads this machine's OS, CPUs, memory and load, and counts guest slots.
func (s *server) localHost() authapi.HostInfo {
	h := authapi.HostInfo{OS: runtime.GOOS, CPUs: runtime.NumCPU()}
	switch runtime.GOOS {
	case "linux":
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
					kb, _ := strconv.ParseUint(f[1], 10, 64)
					h.Mem = kb << 10
				}
			}
		}
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				h.Load, _ = strconv.ParseFloat(f[0], 64)
			}
		}
	default:
		oh := hostInfo()
		if oh.CPUs > 0 {
			h.CPUs = oh.CPUs
		}
		h.Mem = uint64(max(oh.MemBytes, 0))
		if len(oh.Load) > 0 {
			h.Load = oh.Load[0]
		}
	}
	// One guest per CPU, and per 4 GiB of memory (the default guest size).
	h.GuestsMax = h.CPUs
	if h.Mem > 0 {
		h.GuestsMax = min(h.GuestsMax, int(h.Mem>>32))
	}
	h.GuestsMax = max(h.GuestsMax, 1)
	s.mu.Lock()
	for _, t := range s.threads {
		t.mu.Lock()
		if t.meta.Guest != "" && t.meta.Backend != "local" && t.meta.Status != "destroyed" && t.meta.Status != "destroying" {
			h.GuestsUsed++
		}
		t.mu.Unlock()
	}
	s.mu.Unlock()
	return h
}

func (s *server) localAuthority() authapi.AuthorityInfo {
	return authapi.AuthorityInfo{
		Name: s.authName(), URL: s.selfURL, Status: "up", Host: s.localHost(), Local: true, Started: s.started.UTC().Format(time.RFC3339),
		Threads: s.threadSummaries(),
		Guests:  s.localGuests(),
	}
}

func (s *server) localPackView() authapi.Pack {
	name := s.authName()
	return authapi.Pack{
		Leader: name, Term: 0, VaultHost: name, VaultOK: true,
		Authorities: []authapi.AuthorityInfo{s.localAuthority()}, Clients: s.clients.list(),
	}
}

// threadSummaries lists every thread, newest first, with managers' workers as sub-agents.
func (s *server) threadSummaries() []authapi.ThreadSummary {
	s.mu.Lock()
	ts := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	metas := make([]threadMeta, len(ts))
	costs := make([]float64, len(ts))
	for i, t := range ts {
		t.mu.Lock()
		metas[i], costs[i] = t.meta, t.cost
		t.mu.Unlock()
	}
	children := map[string][]authapi.SubAgentSummary{}
	for _, m := range metas {
		if m.Parent != "" && (m.ParentAuthority == "" || m.ParentAuthority == s.authName()) {
			children[m.Parent] = append(children[m.Parent], authapi.SubAgentSummary{ID: m.ID, Name: m.Title, Status: m.Status, Guest: m.Guest, Authority: s.authName()})
		}
	}
	out := make([]authapi.ThreadSummary, len(metas))
	order := make([]int, len(metas))
	for i := range metas {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return metas[order[a]].Updated.After(metas[order[b]].Updated) })
	for n, i := range order {
		out[n] = s.summarize(metas[i], costs[i], children[metas[i].ID])
	}
	return out
}

func (s *server) summarize(m threadMeta, cost float64, subs []authapi.SubAgentSummary) authapi.ThreadSummary {
	if subs == nil {
		subs = []authapi.SubAgentSummary{}
	}
	parentAuth := ""
	if m.Parent != "" {
		parentAuth = cmpOr(m.ParentAuthority, s.authName())
	}
	status := m.Status
	if m.Archived {
		status = "archived"
	}
	return authapi.ThreadSummary{
		ID: m.ID, Project: m.Project, Title: m.Title, Status: status, Archived: m.Archived,
		Authority: s.authName(), Guest: m.Guest, Placement: authapi.Placement{Local: m.Backend == "local", Authority: s.authName()},
		SubAgents: subs, Cost: cost, Updated: m.Updated.UTC().Format(time.RFC3339),
		Parent: m.Parent, ParentAuthority: parentAuth, Kind: m.Kind,
	}
}

// summaryOf summarizes one thread.
func (s *server) summaryOf(t *thread) authapi.ThreadSummary {
	t.mu.Lock()
	m, cost := t.meta, t.cost
	t.mu.Unlock()
	var subs []authapi.SubAgentSummary
	s.mu.Lock()
	for _, c := range s.threads {
		if c == t {
			continue
		}
		c.mu.Lock()
		if c.meta.Parent == m.ID && (c.meta.ParentAuthority == "" || c.meta.ParentAuthority == s.authName()) {
			subs = append(subs, authapi.SubAgentSummary{ID: c.meta.ID, Name: c.meta.Title, Status: c.meta.Status, Guest: c.meta.Guest, Authority: s.authName()})
		}
		c.mu.Unlock()
	}
	s.mu.Unlock()
	return s.summarize(m, cost, subs)
}

// attach wires a thread's events and status changes into the pack stream.
func (s *server) attach(t *thread) {
	t.mu.Lock()
	id := t.meta.ID
	t.onEvent = func(line []byte) {
		if !s.hub.active() {
			return
		}
		s.hub.publish(authapi.PackEvent{Type: authapi.EventThreadEvent, Data: authapi.ThreadEvent{
			Thread: id, Authority: s.authName(), Event: json.RawMessage(line)}})
	}
	t.onStatus = func() {
		if !s.hub.active() {
			return
		}
		go func() { // the caller holds t.mu, which summaryOf takes
			s.hub.publish(authapi.PackEvent{Type: authapi.EventThreadStatus, Data: s.summaryOf(t)})
		}()
	}
	t.mu.Unlock()
}

// ---- HTTP ----

func (s *server) handlePack(w http.ResponseWriter, r *http.Request) {
	p, err := s.packSource().Pack(r.Context())
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	writeJSONResp(w, 200, p)
}

// handlePackStream is the SSE feed: a `pack` snapshot, then `pack.delta`, `thread.event` and
// `thread.status`. Each SSE event's name is its type and its data is the PackEvent envelope.
func (s *server) handlePackStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, 500, "streaming unsupported")
		return
	}
	id, gone := s.clients.add(s.clientOf(r))
	defer gone()
	monitorWatchers.Add(1) // the pack view shows guest CPU and memory
	defer monitorWatchers.Add(-1)
	s.clientsChanged()
	defer s.clientsChanged()
	src := s.packSource()
	ch, err := src.Subscribe(r.Context())
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	snap, err := src.Pack(r.Context())
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(ev authapi.PackEvent) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
		fl.Flush()
	}
	send(authapi.PackEvent{Type: authapi.EventPack, Data: snap})
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			send(ev)
		case <-keep.C:
			s.clients.touch(id)
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// threadView is a thread in GET /api/threads: the web app's fields plus the pack fields.
type threadView struct {
	threadMeta
	Authority string                    `json:"authority"`
	Placement authapi.Placement         `json:"placement"`
	SubAgents []authapi.SubAgentSummary `json:"subagents"`
	Cost      float64                   `json:"cost"`
}

func (s *server) viewOf(t *thread) threadView {
	sum := s.summaryOf(t)
	sum.SubAgents = append(sum.SubAgents, s.remoteChildren(context.Background())[sum.ID]...)
	return threadView{threadMeta: t.view(), Authority: sum.Authority, Placement: sum.Placement, SubAgents: sum.SubAgents, Cost: sum.Cost}
}

// subAgent serves a thread's sub-agent through one of the thread handlers: the sub-agent must
// have been started by the thread in the URL.
func (s *server) subAgent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		child := s.get(r.PathValue("sub"))
		if child == nil {
			// A sub-agent on another authority: its owner serves it.
			parent := s.get(r.PathValue("id"))
			if parent != nil {
				if owner := s.remoteChild(r.Context(), parent, r.PathValue("sub")); owner != "" {
					r2 := r.Clone(r.Context())
					r2.URL.Path = "/api/threads/" + r.PathValue("sub") + strings.TrimPrefix(r.URL.Path, "/api/threads/"+r.PathValue("id")+"/subagents/"+r.PathValue("sub"))
					s.proxyTo(owner, w, r2)
					return
				}
			}
			httpErr(w, 404, "no such sub-agent")
			return
		}
		child.mu.Lock()
		parent := child.meta.Parent
		child.mu.Unlock()
		if parent == "" || parent != r.PathValue("id") {
			httpErr(w, 404, "no such sub-agent")
			return
		}
		r.SetPathValue("id", child.meta.ID)
		h(w, r)
	}
}

// clientsChanged makes a member tell the leader about its clients now.
func (s *server) clientsChanged() {
	if s.node != nil && s.node.InPack() {
		s.node.NotifyChange()
	}
}
