package quorum

import (
	"context"
	"encoding/json"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// PackAdapter adapts a Node's pack view to the authority's PackSource interface
// (internal/rux/authority_pack.go): Pack, Subscribe and Threads in authapi types. Mount it with
// server.SetPackSource(quorum.NewPackAdapter(node)).
type PackAdapter struct{ n *Node }

// NewPackAdapter returns the authority's PackSource for n.
func NewPackAdapter(n *Node) *PackAdapter { return &PackAdapter{n} }

func toAPIStatus(s string) string {
	if s == "online" {
		return "up"
	}
	return "down" // stale or unreachable
}

func toAPIAuthority(a AuthorityInfo, withThreads bool) authapi.AuthorityInfo {
	out := authapi.AuthorityInfo{Name: a.Name, URL: a.URL, Status: toAPIStatus(a.Status), Local: a.Local,
		Started: a.Started,
		Host:    authapi.HostInfo{OS: a.Host.OS, CPUs: a.Host.CPUs, Mem: a.Host.Mem, Load: a.Host.Load, GuestsMax: a.Host.GuestsMax, GuestsUsed: a.Host.GuestsUsed}}
	if withThreads {
		out.Threads = decodeThreads(a)
	}
	return out
}

func decodeThreads(a AuthorityInfo) []authapi.ThreadSummary {
	var out []authapi.ThreadSummary
	for _, raw := range a.Threads {
		var t authapi.ThreadSummary
		if json.Unmarshal(raw, &t) == nil {
			if t.Authority == "" {
				t.Authority = a.Name
			}
			out = append(out, t)
		}
	}
	return out
}

func toAPIPack(s PackSnapshot) authapi.Pack {
	p := authapi.Pack{Leader: s.Leader, Term: s.Term, VaultHost: s.VaultHost, VaultOK: s.VaultOK, Degraded: s.Degraded, Authorities: []authapi.AuthorityInfo{}}
	for _, a := range s.Authorities {
		p.Authorities = append(p.Authorities, toAPIAuthority(a, true))
	}
	return p
}

// Pack is the current snapshot.
func (p *PackAdapter) Pack(ctx context.Context) (authapi.Pack, error) {
	return toAPIPack(p.n.Pack().Snapshot()), nil
}

// Threads lists the threads of one authority ("" for the whole pack).
func (p *PackAdapter) Threads(ctx context.Context, authority string) ([]authapi.ThreadSummary, error) {
	var out []authapi.ThreadSummary
	for _, a := range p.n.Pack().Snapshot().Authorities {
		if authority == "" || a.Name == authority {
			out = append(out, decodeThreads(a)...)
		}
	}
	return out, nil
}

// Subscribe streams "pack" snapshots (leader, term, vault or membership changes) and "pack.delta"
// events (one authority, without threads) until ctx ends. A gap in deltas is covered by the
// next "pack" snapshot, which the adapter sends when it notices one.
func (p *PackAdapter) Subscribe(ctx context.Context) (<-chan authapi.PackEvent, error) {
	in, cancel := p.n.Pack().Subscribe()
	out := make(chan authapi.PackEvent, 64)
	go func() {
		defer close(out)
		defer cancel()
		var last uint64
		send := func(e authapi.PackEvent) bool {
			select {
			case out <- e:
				return true
			case <-ctx.Done():
				return false
			default: // slow reader: drop; it resyncs on the next snapshot
				return true
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case d, ok := <-in:
				if !ok {
					return
				}
				gap := last != 0 && d.Seq != last+1
				last = d.Seq
				switch {
				case d.Type == "pack" && d.Pack != nil:
					send(authapi.PackEvent{Type: authapi.EventPack, Data: toAPIPack(*d.Pack)})
				case gap:
					send(authapi.PackEvent{Type: authapi.EventPack, Data: toAPIPack(p.n.Pack().Snapshot())})
				case d.Type == "authority" && d.Authority != nil:
					send(authapi.PackEvent{Type: authapi.EventPackDelta, Data: toAPIAuthority(*d.Authority, false)})
				case d.Type == "authority.remove":
					send(authapi.PackEvent{Type: authapi.EventPack, Data: toAPIPack(p.n.Pack().Snapshot())})
				}
			}
		}
	}()
	return out, nil
}
