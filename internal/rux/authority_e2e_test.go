package rux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/quorum"
)

// fakeRix is the vault host's secrets.
type fakeRix struct {
	mu   sync.Mutex
	vals map[string]string
}

func (f *fakeRix) Get(ctx context.Context, name string) (quorum.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.vals[name]; ok {
		return quorum.Secret{Name: name, Value: v}, nil
	}
	return quorum.Secret{}, quorum.ErrSecretMissing
}

// testAuthority is a whole authority in process: the web app, the guest listener with the
// model proxy, and the quorum listener, each on its own port, with its own data directory.
type testAuthority struct {
	s       *server
	app     *httptest.Server
	guest   *httptest.Server
	qURL    string
	cancel  context.CancelFunc
	qln     net.Listener
	closeMu sync.Once
}

func newTestAuthority(t *testing.T, name string, vault quorum.VaultProvider) *testAuthority {
	t.Helper()
	dir := t.TempDir()
	s := &server{threads: map[string]*thread{}, projects: map[string]*project{}, backend: "rox", dataDir: dir, name: name,
		started: time.Now(), roxBin: "/nonexistent/rox", advertise: "127.0.0.1:1",
		models: &modelCache{claudeBin: "/nonexistent/claude", dir: dir}, tools: newToolHost(dir)}
	s.SetVaultSource("local", newLocalVault())
	s.proxy = newModelProxy(s)
	app, guest := s.routes()
	s.inner = app
	ta := &testAuthority{s: s, app: httptest.NewServer(s.federate(app)), guest: httptest.NewServer(guest)}
	qln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ta.qln = qln
	ta.qURL = "http://" + qln.Addr().String()
	s.selfURL = ta.app.URL
	ctx, cancel := context.WithCancel(context.Background())
	ta.cancel = cancel
	err = s.startQuorum(ctx, quorumOptions{Listener: qln, Advertise: ta.qURL, Vault: vault, Logf: func(string, ...any) {},
		Tune: func(c *quorum.Config) {
			c.ReportInterval = 100 * time.Millisecond
			c.PlaceTimeout = 10 * time.Second
			c.Raft = func(rc *raft.Config) {
				rc.HeartbeatTimeout = 150 * time.Millisecond
				rc.ElectionTimeout = 150 * time.Millisecond
				rc.LeaderLeaseTimeout = 100 * time.Millisecond
				rc.CommitTimeout = 20 * time.Millisecond
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ta.kill)
	return ta
}

// kill stops the authority: its quorum node and every listener.
func (ta *testAuthority) kill() {
	ta.closeMu.Do(func() {
		ta.cancel()
		ta.s.node.Close()
		ta.qln.Close()
		ta.app.CloseClientConnections()
		ta.app.Close()
		ta.guest.Close()
	})
}

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil {
		json.NewDecoder(resp.Body).Decode(v)
	}
	return resp.StatusCode
}

func waitUntil(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (ta *testAuthority) pack(t *testing.T) authapi.Pack {
	var p authapi.Pack
	getJSON(t, ta.app.URL+"/api/pack", &p)
	return p
}

// createThread posts to this authority's API.
func (ta *testAuthority) createThread(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(ta.app.URL+"/api/threads", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestPackEndToEnd(t *testing.T) {
	rix := &fakeRix{vals: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api03-FAKE"}}
	a := newTestAuthority(t, "a", &hostVault{base: rix, local: newLocalVault()})
	b := newTestAuthority(t, "b", rix)
	c := newTestAuthority(t, "c", rix)
	ctx := context.Background()

	// Create the pack on a, join b and c.
	if err := a.s.node.Create(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "a leads", 10*time.Second, func() bool { return a.s.node.IsLeader() })
	for _, m := range []*testAuthority{b, c} {
		waitUntil(t, "invite", 10*time.Second, func() bool { _, _, err := a.s.node.Invite(); return err == nil })
		code, _, _ := a.s.node.Invite()
		if err := m.s.node.Join(ctx, a.qURL, code); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "three authorities online on the leader", 15*time.Second, func() bool {
		p := a.pack(t)
		if len(p.Authorities) != 3 {
			return false
		}
		for _, x := range p.Authorities {
			if x.Status != "up" {
				return false
			}
		}
		return true
	})
	if p := a.pack(t); p.Leader != "a" || p.Term == 0 || p.VaultHost != "a" || p.Degraded {
		t.Fatalf("pack = %+v", p)
	}

	// Threads: one local to a, one pinned to b through a, one pinned to c through member b.
	code, ta := a.createThread(t, `{"prompt":"on a","placement":{"authority":"a"}}`)
	if code != 201 {
		t.Fatalf("create on a: %d %v", code, ta)
	}
	code, tb := a.createThread(t, `{"prompt":"on b","placement":{"authority":"b"}}`)
	if code != 201 || tb["authority"] != "b" {
		t.Fatalf("create pinned to b: %d %v", code, tb)
	}
	code, tc := b.createThread(t, `{"prompt":"on c","placement":{"authority":"c"}}`)
	if code != 201 || tc["authority"] != "c" {
		t.Fatalf("create pinned to c through b: %d %v", code, tc)
	}
	idA, idB, idC := ta["id"].(string), tb["id"].(string), tc["id"].(string)
	if b.s.get(idB) == nil || a.s.get(idB) != nil || c.s.get(idC) == nil || b.s.get(idC) != nil {
		t.Fatal("threads weren't created on the authorities they were placed on")
	}
	// Unpinned placement goes somewhere in the pack.
	if code, u := a.createThread(t, `{"prompt":"anywhere"}`); code != 201 || u["authority"] == nil {
		t.Fatalf("unpinned create: %d %v", code, u)
	}
	// An unknown authority is refused.
	if code, _ := a.createThread(t, `{"prompt":"x","placement":{"authority":"nope"}}`); code != 404 {
		t.Errorf("pinning to an unknown authority: %d", code)
	}

	// The leader's pack lists the threads of every authority.
	waitUntil(t, "threads of each authority in the leader's pack", 10*time.Second, func() bool {
		seen := map[string]string{}
		for _, x := range a.pack(t).Authorities {
			for _, th := range x.Threads {
				seen[th.ID] = x.Name
			}
		}
		return seen[idA] == "a" && seen[idB] == "b" && seen[idC] == "c"
	})

	// A thread on b is readable through the leader, and through member c.
	for _, via := range []*testAuthority{a, c} {
		var page struct{ Events []json.RawMessage }
		waitUntil(t, "events of b's thread via "+via.s.authName(), 10*time.Second, func() bool {
			page.Events = nil
			getJSON(t, via.app.URL+"/api/threads/"+idB+"/events/page", &page)
			return len(page.Events) > 0
		})
	}
	// Messages and archive reach the owner too.
	resp, _ := http.Post(a.app.URL+"/api/threads/"+idB+"/archive", "application/json", strings.NewReader(`{}`))
	if resp.StatusCode/100 != 2 {
		t.Errorf("archive through the leader: %d", resp.StatusCode)
	}
	resp.Body.Close()
	waitUntil(t, "b's thread archived on b", 5*time.Second, func() bool { return b.s.get(idB).view().Archived })

	// vault: leader. b's proxy asks the vault host for the key, and the audit names the thread.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Key", r.Header.Get("x-api-key"))
		w.Write([]byte("ok"))
	}))
	defer up.Close()
	b.s.proxy.upstreams["anthropic"], _ = parseTestURL(up.URL)
	if err := a.s.node.Allow(ctx, "b", "ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "policy on b", 10*time.Second, func() bool { return b.s.node.Config().Policy.Allows("b", "ANTHROPIC_API_KEY") })
	tok := b.s.get(idB).meta.Token
	req, _ := http.NewRequest("POST", b.guest.URL+"/proxy/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", tok)
	pr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()
	if pr.StatusCode != 200 || pr.Header.Get("X-Seen-Key") != "sk-ant-api03-FAKE" {
		t.Fatalf("proxy through the leader's vault: %d, upstream saw key %q", pr.StatusCode, pr.Header.Get("X-Seen-Key"))
	}
	audit, _ := os.ReadFile(filepath.Join(a.s.dataDir, "quorum-audit.jsonl"))
	if !bytes.Contains(audit, []byte(`"member":"b"`)) || !bytes.Contains(audit, []byte(`"thread":"`+idB+`"`)) || !bytes.Contains(audit, []byte(`"secret":"ANTHROPIC_API_KEY"`)) {
		t.Errorf("audit doesn't attribute the thread:\n%s", audit)
	}
	// A member the policy doesn't allow is refused, and the proxy says so without leaking anything.
	tokC := c.s.get(idC).meta.Token
	req, _ = http.NewRequest("POST", c.guest.URL+"/proxy/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", tokC)
	pr, _ = http.DefaultClient.Do(req)
	pr.Body.Close()
	if pr.StatusCode != 502 {
		t.Errorf("a member the policy denies: status %d, want 502", pr.StatusCode)
	}

	// Fencing: a placement from the leader's term is good now and stale after a failover.
	p, err := a.s.node.Place(ctx, quorum.PlaceRequest{Pinned: "b"})
	if err != nil || p.Term == 0 {
		t.Fatalf("place: %+v %v", p, err)
	}
	oldTerm := p.Term
	p.Term = oldTerm - 1
	if _, err := b.s.node.Execute(ctx, p, json.RawMessage(`{"prompt":"stale"}`)); !errors.Is(err, quorum.ErrStaleTerm) {
		t.Errorf("a placement from an older term: %v", err)
	}

	// Kill the leader: a new one is elected, the pack comes back live without it.
	a.kill()
	survivors := []*testAuthority{b, c}
	var leader *testAuthority
	waitUntil(t, "a new leader", 20*time.Second, func() bool {
		for _, x := range survivors {
			if x.s.node.IsLeader() {
				leader = x
			}
		}
		return leader != nil
	})
	if leader.s.node.Term() <= oldTerm {
		t.Errorf("term %d didn't advance past %d", leader.s.node.Term(), oldTerm)
	}
	waitUntil(t, "both survivors report a live pack with the new leader", 15*time.Second, func() bool {
		for _, x := range survivors {
			p := x.pack(t)
			if p.Degraded || p.Leader != leader.s.authName() || p.Term != leader.s.node.Term() {
				return false
			}
		}
		return true
	})
	// The stale placement is still refused, and a fresh one works on the survivors.
	p.Term = oldTerm
	if _, err := b.s.node.Execute(ctx, p, json.RawMessage(`{"prompt":"stale"}`)); !errors.Is(err, quorum.ErrStaleTerm) {
		t.Errorf("a pre-failover placement after the failover: %v", err)
	}
	other := b
	if leader == b {
		other = c
	}
	if code, out := leader.createThread(t, `{"prompt":"after failover","placement":{"authority":"`+other.s.authName()+`"}}`); code != 201 || out["authority"] != other.s.authName() {
		t.Errorf("placement after the failover: %d %v", code, out)
	}
	// A thread on c is still readable through b after the failover.
	var page struct{ Events []json.RawMessage }
	waitUntil(t, "c's events through b after the failover", 10*time.Second, func() bool {
		page.Events = nil
		getJSON(t, b.app.URL+"/api/threads/"+idC+"/events/page", &page)
		return len(page.Events) > 0
	})

	// Lose the majority too: the last authority serves its own view, flagged degraded.
	leader.kill()
	last := other
	waitUntil(t, "a degraded local view", 20*time.Second, func() bool {
		p := last.pack(t)
		return p.Degraded && len(p.Authorities) == 1 && p.Authorities[0].Name == last.s.authName()
	})
	if code := getJSON(t, last.app.URL+"/api/threads", nil); code != 200 {
		t.Errorf("a degraded authority still serves its threads: %d", code)
	}
}

func parseTestURL(s string) (*url.URL, error) { return url.Parse(s) }
