package quorum

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type entry struct {
	rep  Report
	seen time.Time
}

// packHub is the pack view: on the leader, built from member reports; on a member, the
// leader's snapshot as returned to its last report.
type packHub struct {
	n    *Node
	mu   sync.Mutex
	seq  uint64
	subs map[chan PackDelta]struct{}

	reports  map[string]*entry
	local    Report
	cached   *PackSnapshot
	cachedAt time.Time
	meta     string
	infos    map[string]string
}

func newPackHub(n *Node) *packHub {
	return &packHub{n: n, subs: map[chan PackDelta]struct{}{}, reports: map[string]*entry{}, infos: map[string]string{}}
}

func (h *packHub) status(e *entry) string {
	if e == nil {
		return "unreachable"
	}
	age := time.Since(e.seen)
	iv := h.n.cfg.ReportInterval
	switch {
	case age <= 3*iv:
		return "online"
	case age <= 8*iv:
		return "stale"
	}
	return "unreachable"
}

func (h *packHub) infoLocked(name string, url string) AuthorityInfo {
	self := name == h.n.Name()
	e := h.reports[name]
	if self {
		e = &entry{rep: h.local, seen: time.Now()}
	}
	info := AuthorityInfo{Name: name, URL: url, Status: h.status(e), Local: self}
	if e != nil {
		info.Host, info.Threads, info.Subagents = e.rep.Host, e.rep.Threads, e.rep.Subagents
	}
	return info
}

func (h *packHub) leaderSnapshotLocked() PackSnapshot {
	n := h.n
	cfg := n.fsm.get()
	leader, _ := n.Leader()
	snap := PackSnapshot{Seq: h.seq, Leader: leader, Term: n.Term(), VaultHost: cfg.VaultHost, Authorities: []AuthorityInfo{}}
	names := make([]string, 0, len(cfg.Members))
	for name := range cfg.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		snap.Authorities = append(snap.Authorities, h.infoLocked(name, cfg.Members[name].URL))
	}
	snap.VaultOK = cfg.VaultHost == n.Name()
	for _, a := range snap.Authorities {
		if a.Name == cfg.VaultHost {
			snap.VaultOK = a.Status != "unreachable"
		}
	}
	return snap
}

func (h *packHub) localOnlyLocked(degraded bool) PackSnapshot {
	n := h.n
	name := n.Name()
	snap := PackSnapshot{Seq: h.seq, Degraded: degraded, VaultOK: true, Authorities: []AuthorityInfo{h.infoLocked(name, n.cfg.AdvertiseURL)}}
	if st := n.getState(); st != nil {
		snap.VaultHost = n.fsm.get().VaultHost
		snap.VaultOK = snap.VaultHost == name
	}
	return snap
}

func (h *packHub) snapshotLocked() PackSnapshot {
	n := h.n
	switch {
	case !n.InPack():
		return h.localOnlyLocked(false)
	case n.IsLeader():
		return h.leaderSnapshotLocked()
	case h.cached != nil && time.Since(h.cachedAt) <= 3*n.cfg.ReportInterval+time.Second:
		s := *h.cached
		for i := range s.Authorities {
			s.Authorities[i].Local = s.Authorities[i].Name == n.Name()
		}
		return s
	}
	return h.localOnlyLocked(true)
}

// Snapshot is the pack as this authority sees it. On a member it is the leader's view as of
// the last report exchange, or, with no leader, just this authority, flagged Degraded.
func (h *packHub) Snapshot() PackSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

// Subscribe returns a delta channel. A full channel drops deltas; the Seq gap tells the
// receiver to take a new Snapshot.
func (h *packHub) Subscribe() (<-chan PackDelta, func()) {
	ch := make(chan PackDelta, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *packHub) publishLocked(d PackDelta) {
	h.seq++
	d.Seq = h.seq
	for ch := range h.subs {
		select {
		case ch <- d:
		default:
		}
	}
}

func (h *packHub) publishPackLocked() {
	snap := h.snapshotLocked()
	snap.Seq = h.seq + 1
	h.publishLocked(PackDelta{Type: "pack", Pack: &snap})
}

func (h *packHub) setLocal(rep Report) {
	h.mu.Lock()
	h.local = rep
	h.mu.Unlock()
}

// ingest records a member's report (leader only).
func (h *packHub) ingest(rep Report) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if rep.Authority == h.n.Name() {
		h.local = rep
	} else {
		h.reports[rep.Authority] = &entry{rep: rep, seen: time.Now()}
	}
	h.publishChangedLocked(rep.Authority)
}

func (h *packHub) publishChangedLocked(name string) {
	m, ok := h.n.fsm.get().Members[name]
	if !ok {
		return
	}
	info := h.infoLocked(name, m.URL)
	data, _ := json.Marshal(info)
	if prev := h.infos[name]; prev == string(data) {
		return
	}
	h.infos[name] = string(data)
	h.publishLocked(PackDelta{Type: "authority", Name: name, Authority: &info})
}

// setCached is a member storing the leader's snapshot.
func (h *packHub) setCached(s PackSnapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.cached
	h.cached, h.cachedAt = &s, time.Now()
	a, _ := json.Marshal(old)
	s.Seq = 0
	b, _ := json.Marshal(s)
	if old != nil {
		o := *old
		o.Seq = 0
		a, _ = json.Marshal(o)
	}
	if string(a) != string(b) {
		h.publishPackLocked()
	}
}

func (h *packHub) membershipChanged() { h.tick() }

// tick publishes status transitions and leader, term, vault and membership changes.
func (h *packHub) tick() {
	n := h.n
	h.mu.Lock()
	defer h.mu.Unlock()
	if !n.InPack() {
		h.meta = ""
		return
	}
	cfg := n.fsm.get()
	leader, _ := n.Leader()
	names := make([]string, 0, len(cfg.Members))
	for name := range cfg.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	meta := fmt.Sprintf("%s|%d|%s|%s", leader, n.Term(), cfg.VaultHost, strings.Join(names, ","))
	changed := meta != h.meta
	h.meta = meta
	if n.IsLeader() {
		for name := range h.infos {
			if _, ok := cfg.Members[name]; !ok {
				delete(h.infos, name)
				delete(h.reports, name)
				h.publishLocked(PackDelta{Type: "authority.remove", Name: name})
			}
		}
		if changed {
			h.publishPackLocked()
		}
		for _, name := range names {
			h.publishChangedLocked(name)
		}
	} else if changed {
		h.publishPackLocked()
	}
}

// Pack is this node's PackSource.
func (n *Node) Pack() PackSource { return n.pack }

func (h *packHub) fresh(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status(h.reports[name]) == "online"
}

// ---- reports ----

func (n *Node) buildReport(ctx context.Context) Report {
	var rep Report
	if n.cfg.LocalReport != nil {
		rep = n.cfg.LocalReport(ctx)
	} else {
		rep.Host = localHost()
	}
	rep.Authority = n.Name()
	rep.Term = n.Term()
	return rep
}

func (n *Node) reportLoop() {
	t := time.NewTicker(n.cfg.ReportInterval)
	defer t.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-t.C:
		case <-n.changed:
		}
		n.pushReport(n.ctx)
	}
}

func (n *Node) pushReport(ctx context.Context) {
	rep := n.buildReport(ctx)
	n.pack.setLocal(rep)
	if !n.InPack() {
		return
	}
	if n.IsLeader() {
		n.pack.ingest(rep)
		return
	}
	_, url := n.Leader()
	if url == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var snap PackSnapshot
	if err := n.call(ctx, url, http.MethodPost, "/quorum/report", rep, &snap); err != nil {
		n.logf("quorum: report to %s: %v", url, err)
		return
	}
	n.pack.setCached(snap)
}
