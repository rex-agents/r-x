package rux

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// addThread puts a thread straight into an authority, without provisioning anything.
func addThread(t *testing.T, ta *testAuthority, m threadMeta) {
	t.Helper()
	th, err := openThread(filepath.Join(ta.s.dataDir, "threads", m.ID), &m)
	if err != nil {
		t.Fatal(err)
	}
	ta.s.mu.Lock()
	ta.s.threads[m.ID] = th
	ta.s.mu.Unlock()
}

// openStream opens GET /api/pack/stream as a client with the given headers and returns the
// events it sees; the stream ends when the returned cancel runs.
func openStream(t *testing.T, base string, hdr map[string]string) (<-chan authapi.PackEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/pack/stream", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	evs := make(chan authapi.PackEvent, 64)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data: ") {
				var raw struct {
					Type string
					Data json.RawMessage
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &raw) == nil {
					ev := authapi.PackEvent{Type: raw.Type}
					switch raw.Type {
					case authapi.EventPackClients:
						var cs []authapi.ClientInfo
						json.Unmarshal(raw.Data, &cs)
						ev.Data = cs
					}
					select {
					case evs <- ev:
					default:
					}
				}
			}
		}
	}()
	return evs, cancel
}

// waitClients waits for the pack served at base to list exactly the clients want says it should.
func waitClients(t *testing.T, ta *testAuthority, what string, want func([]authapi.ClientInfo) bool) []authapi.ClientInfo {
	t.Helper()
	var cs []authapi.ClientInfo
	waitUntil(t, what, 10*time.Second, func() bool { cs = ta.pack(t).Clients; return want(cs) })
	return cs
}

func fakeGuests(s *server) {
	const mb = 1 << 20
	s.guestLister = func() []roxGuest {
		return []roxGuest{{Name: "g-1", Backend: "container", Phase: "running"}, {Name: "spare", Backend: "container", Phase: "stopped"}}
	}
	s.guestStats = func() []containerStat {
		return []containerStat{{ID: "g-1", CPU: 0.35, MemBytes: 1200 * mb, MemLimit: 4096 * mb}}
	}
}

// A local authority lists its own guests, mapped to the threads that use them, and its own clients.
func TestLocalPackGuestsKindAndClients(t *testing.T) {
	a := newTestAuthority(t, "solo", nil)
	fakeGuests(a.s)
	addThread(t, a, threadMeta{ID: "m1", Project: "web", Kind: "manager", Status: "idle", Backend: "rox", Guest: "g-1", Updated: time.Now(), Created: time.Now()})
	addThread(t, a, threadMeta{ID: "w1", Project: "web", Parent: "m1", Status: "idle", Backend: "rox", Updated: time.Now(), Created: time.Now()})

	// The wire format: field names exactly as the contract says.
	resp, err := http.Get(a.app.URL + "/api/pack")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Clients     json.RawMessage `json:"clients"`
		Authorities []struct {
			Guests  []map[string]any `json:"guests"`
			Threads []map[string]any `json:"threads"`
		} `json:"authorities"`
	}
	json.NewDecoder(resp.Body).Decode(&raw)
	resp.Body.Close()
	if string(raw.Clients) != "[]" {
		t.Errorf("clients without any stream: %s", raw.Clients)
	}
	g := raw.Authorities[0].Guests
	if len(g) != 2 || g[0]["name"] != "g-1" || g[0]["phase"] != "running" || g[0]["backend"] != "container" || g[0]["cpu"] != 35.0 ||
		g[0]["mem_bytes"] != 1200.0*(1<<20) || g[0]["mem_limit"] != 4096.0*(1<<20) || g[0]["thread"] != "m1" {
		t.Errorf("guest g-1 = %v", g[0])
	}
	if _, has := g[1]["thread"]; has || g[1]["name"] != "spare" {
		t.Errorf("a guest no thread uses has no thread: %v", g[1])
	}
	kinds := map[any]any{}
	for _, th := range raw.Authorities[0].Threads {
		kinds[th["id"]] = th["kind"]
	}
	if kinds["m1"] != "manager" || kinds["w1"] != nil {
		t.Errorf("thread kinds %v", kinds)
	}

	// Clients: a TUI and a plain (web) stream, each listed once.
	evs, stopTUI := openStream(t, a.app.URL, map[string]string{"X-Rux-Client": "tui", "X-Rux-Device": "laptop"})
	_, stopWeb := openStream(t, a.app.URL, nil)
	cs := waitClients(t, a, "both clients", func(cs []authapi.ClientInfo) bool { return len(cs) == 2 })
	byKind := map[string]authapi.ClientInfo{cs[0].Kind: cs[0], cs[1].Kind: cs[1]}
	if c := byKind["tui"]; c.Device != "laptop" || c.Authority != "solo" || c.ID == "" || c.Since == "" || c.LastSeen == "" {
		t.Errorf("tui client %+v", c)
	}
	if c := byKind["web"]; c.Device == "" || c.Authority != "solo" {
		t.Errorf("web client %+v", c)
	}
	// pack.clients arrives on the stream when the set changes.
	stopWeb()
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-evs:
			if cs, ok := ev.Data.([]authapi.ClientInfo); ok && ev.Type == authapi.EventPackClients && len(cs) == 1 {
				done = cs[0].Kind == "tui"
			}
		case <-deadline:
			t.Fatal("no pack.clients event with the web client gone")
		}
	}
	stopTUI()
	waitClients(t, a, "no clients", func(cs []authapi.ClientInfo) bool { return len(cs) == 0 })
}

// A client that connects through a member shows up once in every authority's pack, under the
// member it came through, and guests travel in the member's report.
func TestClientsThroughAMemberAreCountedOnce(t *testing.T) {
	rix := &fakeRix{vals: map[string]string{}}
	a := newTestAuthority(t, "a", &hostVault{base: rix, local: newLocalVault()})
	b := newTestAuthority(t, "b", rix)
	fakeGuests(b.s)
	addThread(t, b, threadMeta{ID: "tb", Project: "web", Status: "idle", Backend: "rox", Guest: "g-1", Updated: time.Now(), Created: time.Now()})
	if err := a.s.node.Create(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "a leads", 10*time.Second, func() bool { return a.s.node.IsLeader() })
	waitUntil(t, "invite", 10*time.Second, func() bool { _, _, err := a.s.node.Invite(); return err == nil })
	code, _, _ := a.s.node.Invite()
	if err := b.s.node.Join(context.Background(), a.qURL, code); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "b up on the leader", 15*time.Second, func() bool {
		p := a.pack(t)
		return len(p.Authorities) == 2 && p.Authorities[0].Status == "up" && p.Authorities[1].Status == "up"
	})

	// Guests: b's guests are on the leader's pack, mapped to b's thread.
	waitUntil(t, "b's guests on the leader", 10*time.Second, func() bool {
		for _, x := range a.pack(t).Authorities {
			if x.Name == "b" && len(x.Guests) == 2 {
				return x.Guests[0].Name == "g-1" && x.Guests[0].Thread == "tb" && x.Guests[0].CPU == 35 && x.Guests[0].Backend == "container" && x.Guests[1].Thread == ""
			}
		}
		return false
	})

	// A TUI on b, and a web client on the leader.
	evs, _ := openStream(t, b.app.URL, map[string]string{"X-Rux-Client": "tui", "X-Rux-Device": "desk"})
	openStream(t, a.app.URL, nil)
	for _, via := range []*testAuthority{a, b} {
		cs := waitClients(t, via, "both clients on "+via.s.authName(), func(cs []authapi.ClientInfo) bool { return len(cs) == 2 })
		got := map[string]string{}
		for _, c := range cs {
			got[c.Kind] = c.Authority
		}
		if got["tui"] != "b" || got["web"] != "a" {
			t.Errorf("%s sees clients %+v", via.s.authName(), cs)
		}
	}
	// Still two a moment later: the leader doesn't add a second copy from b's report.
	time.Sleep(500 * time.Millisecond)
	if cs := a.pack(t).Clients; len(cs) != 2 {
		t.Errorf("clients after a few reports: %+v", cs)
	}
	// The member's stream announces the set.
	deadline := time.After(10 * time.Second)
	for seen := false; !seen; {
		select {
		case ev := <-evs:
			cs, _ := ev.Data.([]authapi.ClientInfo)
			seen = ev.Type == authapi.EventPackClients && len(cs) == 2
		case <-deadline:
			t.Fatal("no pack.clients with both clients on b's stream")
		}
	}
}

// X-Rux-Via names the authority a proxied client came through.
func TestClientViaHeader(t *testing.T) {
	a := newTestAuthority(t, "a", nil)
	openStream(t, a.app.URL, map[string]string{"X-Rux-Client": "tui", "X-Rux-Device": "desk", "X-Rux-Via": "edge"})
	cs := waitClients(t, a, "the client", func(cs []authapi.ClientInfo) bool { return len(cs) == 1 })
	if cs[0].Authority != "edge" || cs[0].Device != "desk" {
		t.Errorf("client %+v", cs[0])
	}
}
