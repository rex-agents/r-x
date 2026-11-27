package quorum

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"
)

// candidate is an authority a placement could go to.
type candidate struct {
	Name string
	URL  string
	Host HostInfo
}

func (c candidate) free() int {
	if c.Host.GuestsMax <= 0 {
		return 0
	}
	return c.Host.GuestsMax - c.Host.GuestsUsed
}

// schedule picks the authority: pinned wins; otherwise the most free guest slots, then the
// lowest load, then the requester (locality), then the name.
func schedule(cands []candidate, req PlaceRequest) (candidate, error) {
	if req.Pinned != "" {
		for _, c := range cands {
			if c.Name == req.Pinned {
				return c, nil
			}
		}
		return candidate{}, ErrUnknownAuthority
	}
	var ok []candidate
	for _, c := range cands {
		if c.Host.GuestsMax <= 0 || c.free() > 0 {
			ok = append(ok, c)
		}
	}
	if len(ok) == 0 {
		return candidate{}, ErrNoCapacity
	}
	sort.SliceStable(ok, func(i, j int) bool {
		a, b := ok[i], ok[j]
		if a.free() != b.free() {
			return a.free() > b.free()
		}
		if a.Host.Load != b.Host.Load {
			return a.Host.Load < b.Host.Load
		}
		if (a.Name == req.Requester) != (b.Name == req.Requester) {
			return a.Name == req.Requester
		}
		return a.Name < b.Name
	})
	return ok[0], nil
}

// errNotReady is a placement the leader can't answer yet; the requester retries.
var errNotReady = errors.New("the pack leader hasn't heard from that authority yet")

// placeAsLeader decides on this node, which must be the leader.
func (n *Node) placeAsLeader(req PlaceRequest) (Placement, error) {
	if !n.IsLeader() {
		return Placement{}, ErrNotLeader
	}
	cfg := n.fsm.get()
	snap := n.pack.Snapshot()
	var cands []candidate
	for _, a := range snap.Authorities {
		if a.Status == "online" {
			cands = append(cands, candidate{a.Name, cfg.Members[a.Name].URL, a.Host})
		}
	}
	if m, ok := cfg.Members[req.Pinned]; ok && req.Pinned != "" {
		found := false
		for _, c := range cands {
			found = found || c.Name == req.Pinned
		}
		if !found {
			_ = m
			return Placement{}, errNotReady
		}
	} else if req.Pinned != "" {
		return Placement{}, ErrUnknownAuthority
	}
	c, err := schedule(cands, req)
	if err != nil {
		return Placement{}, err
	}
	term := n.Term()
	n.observeTerm(term)
	return Placement{ID: randomToken(8), Authority: c.Name, URL: c.URL, Term: term, Requester: req.Requester}, nil
}

// Place asks the pack leader where new work runs: (authority, term). Without a leader the
// request queues and retries with backoff, up to PlaceTimeout, so a failover only delays it.
func (n *Node) Place(ctx context.Context, req PlaceRequest) (Placement, error) {
	if !n.InPack() {
		return Placement{}, ErrNotInPack
	}
	if req.Requester == "" {
		req.Requester = n.Name()
	}
	ctx, cancel := context.WithTimeout(ctx, n.cfg.PlaceTimeout)
	defer cancel()
	delay := 100 * time.Millisecond
	var last error
	for {
		p, err := n.placeOnce(ctx, req)
		if err == nil {
			return p, nil
		}
		last = err
		if !retryable(err) {
			return Placement{}, err
		}
		select {
		case <-ctx.Done():
			if errors.Is(last, errNotReady) {
				return Placement{}, last
			}
			return Placement{}, ErrNoLeader
		case <-time.After(delay):
		}
		if delay *= 2; delay > 2*time.Second {
			delay = 2 * time.Second
		}
	}
}

func retryable(err error) bool {
	if errors.Is(err, ErrNoCapacity) || errors.Is(err, ErrUnknownAuthority) || errors.Is(err, ErrNotInPack) {
		return false
	}
	return true // no leader, leader changed, network errors, errNotReady
}

func (n *Node) placeOnce(ctx context.Context, req PlaceRequest) (Placement, error) {
	if n.IsLeader() {
		return n.placeAsLeader(req)
	}
	_, url := n.Leader()
	if url == "" {
		return Placement{}, ErrNoLeader
	}
	var p Placement
	err := n.call(ctx, url, http.MethodPost, "/quorum/place", req, &p)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusServiceUnavailable && ae.Code == "" {
			return Placement{}, err
		}
		return Placement{}, err
	}
	n.observeTerm(p.Term)
	return p, nil
}

// observeTerm records the newest leader term seen.
func (n *Node) observeTerm(t uint64) {
	for {
		cur := n.maxTerm.Load()
		if t <= cur || n.maxTerm.CompareAndSwap(cur, t) {
			return
		}
	}
}

// Execute runs the placement p on its authority, with payload for the thread or sub-agent.
// The target rejects p if its term is older than one it has already seen.
func (n *Node) Execute(ctx context.Context, p Placement, payload json.RawMessage) (json.RawMessage, error) {
	p.Payload = payload
	if p.Authority == n.Name() || p.URL == "" {
		return n.AcceptPlacement(ctx, p)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	if err := n.call(ctx, p.URL, http.MethodPost, "/quorum/execute", p, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

// AcceptPlacement is the executor: it fences on the term, then calls Config.Execute.
func (n *Node) AcceptPlacement(ctx context.Context, p Placement) (json.RawMessage, error) {
	if err := n.CheckTerm(p.Term); err != nil {
		return nil, err
	}
	if n.cfg.Execute == nil {
		return nil, errors.New("this authority can't execute placements")
	}
	return n.cfg.Execute(ctx, p)
}

// CheckTerm is the fence: a placement from a term older than the newest seen, from a
// placement or from raft itself, is rejected with ErrStaleTerm.
func (n *Node) CheckTerm(term uint64) error {
	if !n.InPack() {
		return nil
	}
	n.observeTerm(n.Term())
	if term < n.maxTerm.Load() {
		return ErrStaleTerm
	}
	n.observeTerm(term)
	return nil
}

func timeNow() time.Time { return time.Now().UTC() }
