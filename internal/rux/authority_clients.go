package rux

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// clientRegistry tracks the UIs connected to this authority: each live GET /api/pack/stream
// connection is one client. In a pack the authority reports its clients to the leader with its
// host report, so the leader lists every client once, under the authority it connected through.
type clientRegistry struct {
	mu      sync.Mutex
	next    int
	clients map[string]*liveClient
	subs    map[chan struct{}]struct{}
}

type liveClient struct {
	info authapi.ClientInfo
}

// clientKey is the part of a ClientInfo that makes the set differ; LastSeen alone doesn't.
func clientKey(cs []authapi.ClientInfo) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.ID + "|" + c.Kind + "|" + c.Device + "|" + c.Authority + "\n")
	}
	return b.String()
}

// clientOf describes the client behind r. The TUI says what it is with X-Rux-Client and
// X-Rux-Device; anything else is the web app. A member that proxies the stream to the leader
// sets X-Rux-Via to the authority the client came through.
func (s *server) clientOf(r *http.Request) authapi.ClientInfo {
	now := time.Now().UTC().Format(time.RFC3339)
	c := authapi.ClientInfo{Kind: "web", Authority: s.authName(), Since: now, LastSeen: now}
	if r.Header.Get("X-Rux-Client") == "tui" {
		c.Kind = "tui"
	}
	if via := strings.TrimSpace(r.Header.Get("X-Rux-Via")); via != "" {
		c.Authority = via
	}
	c.Device = strings.TrimSpace(r.Header.Get("X-Rux-Device"))
	if c.Device == "" {
		c.Device = peerName(r)
	}
	return c
}

// peerName is the tailnet peer name or the remote host of a request.
func peerName(r *http.Request) string {
	host := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if host == "" {
		host, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if host == "" {
		return "unknown"
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return "localhost"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if names, err := net.DefaultResolver.LookupAddr(ctx, host); err == nil && len(names) > 0 {
			n := strings.TrimSuffix(names[0], ".")
			if first, _, _ := strings.Cut(n, "."); first != "" {
				return first
			}
		}
	}
	return host
}

// add registers a connected client and returns its ID and a function that removes it.
func (g *clientRegistry) add(c authapi.ClientInfo) (string, func()) {
	g.mu.Lock()
	if g.clients == nil {
		g.clients = map[string]*liveClient{}
	}
	g.next++
	c.ID = "c" + strconv.Itoa(g.next) + "-" + strconv.FormatInt(time.Now().UnixNano()%100000, 36)
	g.clients[c.ID] = &liveClient{info: c}
	g.mu.Unlock()
	g.notify()
	return c.ID, func() {
		g.mu.Lock()
		delete(g.clients, c.ID)
		g.mu.Unlock()
		g.notify()
	}
}

// touch marks a client as seen now.
func (g *clientRegistry) touch(id string) {
	g.mu.Lock()
	if c := g.clients[id]; c != nil {
		c.info.LastSeen = time.Now().UTC().Format(time.RFC3339)
	}
	g.mu.Unlock()
}

// list is every connected client, oldest first. Clients connected now are seen now.
func (g *clientRegistry) list() []authapi.ClientInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]authapi.ClientInfo, 0, len(g.clients))
	for _, c := range g.clients {
		out = append(out, c.info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since != out[j].Since {
			return out[i].Since < out[j].Since
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// watch signals (without blocking) whenever the set of clients changes.
func (g *clientRegistry) watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	g.mu.Lock()
	if g.subs == nil {
		g.subs = map[chan struct{}]struct{}{}
	}
	g.subs[ch] = struct{}{}
	g.mu.Unlock()
	return ch, func() { g.mu.Lock(); delete(g.subs, ch); g.mu.Unlock() }
}

func (g *clientRegistry) notify() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for ch := range g.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ---- guests ----

// guestList caches `rox ls`, which is slow, and refreshes it in the background.
var guestCache struct {
	sync.Mutex
	at         time.Time
	v          []roxGuest
	refreshing bool
}

func cachedRoxGuests() []roxGuest {
	guestCache.Lock()
	defer guestCache.Unlock()
	if time.Since(guestCache.at) > 5*time.Second && !guestCache.refreshing {
		if guestCache.at.IsZero() { // first call: wait once rather than show nothing
			guestCache.Unlock()
			v, _ := listRoxGuests()
			guestCache.Lock()
			guestCache.v, guestCache.at = v, time.Now()
		} else {
			guestCache.refreshing = true
			go func() {
				v, err := listRoxGuests()
				guestCache.Lock()
				if err == nil {
					guestCache.v = v
				}
				guestCache.at, guestCache.refreshing = time.Now(), false
				guestCache.Unlock()
			}()
		}
	}
	return guestCache.v
}

// localGuests are this host's rox guests with their CPU and memory, and the thread using each.
func (s *server) localGuests() []authapi.GuestInfo {
	var gs []roxGuest
	if s.guestLister != nil {
		gs = s.guestLister()
	} else {
		gs = cachedRoxGuests()
	}
	stats := map[string]containerStat{}
	var st []containerStat
	if s.guestStats != nil {
		st = s.guestStats()
	} else {
		st = cachedContainerStats()
	}
	for _, c := range st {
		stats[c.ID] = c
	}
	byGuest := map[string]string{}
	s.mu.Lock()
	for _, t := range s.threads {
		t.mu.Lock()
		if t.meta.Guest != "" && t.meta.Status != "destroyed" {
			byGuest[t.meta.Guest] = t.meta.ID
		}
		t.mu.Unlock()
	}
	s.mu.Unlock()
	out := make([]authapi.GuestInfo, 0, len(gs))
	for _, g := range gs {
		gi := authapi.GuestInfo{Name: g.Name, Phase: g.Phase, Backend: g.Backend, Thread: byGuest[g.Name]}
		if c, ok := stats[g.Name]; ok {
			gi.CPU, gi.MemBytes, gi.MemLimit = c.CPU*100, c.MemBytes, c.MemLimit
		}
		out = append(out, gi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
