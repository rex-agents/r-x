package quorum

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

type fakeVault struct {
	mu    sync.Mutex
	vals  map[string]string
	calls atomic.Int32
}

func (f *fakeVault) Get(ctx context.Context, name string) (Secret, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.vals[name]; ok {
		return Secret{Name: name, Value: v}, nil
	}
	return Secret{}, ErrSecretMissing
}

type tnode struct {
	name     string
	n        *Node
	srv      *httptest.Server
	dir      string
	vault    *fakeVault
	executed chan Placement
	slots    int
	load     float64
}

func newTNode(t *testing.T, name string) *tnode {
	t.Helper()
	tn := &tnode{name: name, dir: t.TempDir(), slots: 4, executed: make(chan Placement, 16),
		vault: &fakeVault{vals: map[string]string{"ANTHROPIC_API_KEY": "sk-test", "github": "ghp"}}}
	tn.srv = httptest.NewUnstartedServer(nil)
	url := "http://" + tn.srv.Listener.Addr().String()
	n, err := New(Config{
		Dir: tn.dir, Name: name, AdvertiseURL: url, ReportInterval: 100 * time.Millisecond,
		PlaceTimeout: 10 * time.Second, Vault: tn.vault,
		LocalReport: func(ctx context.Context) Report {
			return Report{Host: HostInfo{OS: "linux", CPUs: 4, GuestsMax: tn.slots, Load: tn.load}}
		},
		Execute: func(ctx context.Context, p Placement) (json.RawMessage, error) {
			tn.executed <- p
			return json.RawMessage(`"started"`), nil
		},
		Raft: func(c *raft.Config) {
			c.HeartbeatTimeout = 150 * time.Millisecond
			c.ElectionTimeout = 150 * time.Millisecond
			c.LeaderLeaseTimeout = 100 * time.Millisecond
			c.CommitTimeout = 20 * time.Millisecond
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tn.n = n
	tn.srv.Config.Handler = n.Handler()
	tn.srv.Start()
	if err := n.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tn.stop)
	return tn
}

func (tn *tnode) stop() {
	tn.n.Close()
	tn.srv.CloseClientConnections()
	tn.srv.Close()
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func join(t *testing.T, leader, m *tnode) {
	t.Helper()
	code, _, err := leader.n.Invite()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.n.Join(context.Background(), leader.srv.URL, code); err != nil {
		t.Fatal(err)
	}
}

func createPack(t *testing.T, names ...string) []*tnode {
	t.Helper()
	var nodes []*tnode
	for _, name := range names {
		nodes = append(nodes, newTNode(t, name))
	}
	if err := nodes[0].n.Create(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first leader", 10*time.Second, func() bool { return nodes[0].n.IsLeader() })
	for _, m := range nodes[1:] {
		waitFor(t, "invite", 10*time.Second, func() bool { _, _, err := nodes[0].n.Invite(); return err == nil })
		join(t, nodes[0], m)
	}
	for _, m := range nodes {
		waitFor(t, m.name+" sees all members", 10*time.Second, func() bool { return len(m.n.Config().Members) == len(nodes) })
	}
	return nodes
}

func leaderOf(nodes []*tnode) *tnode {
	for _, n := range nodes {
		if n.n.IsLeader() {
			return n
		}
	}
	return nil
}

func TestThreeNodeElectionAndFailover(t *testing.T) {
	nodes := createPack(t, "a", "b", "c")
	waitFor(t, "three voters", 10*time.Second, func() bool { return len(nodes[0].n.Status().Voters) == 3 })
	if !nodes[0].n.Status().AutoFailover {
		t.Fatal("three members should fail over automatically")
	}
	first := leaderOf(nodes)
	if first == nil || first.name != "a" {
		t.Fatalf("leader = %v", first)
	}
	term := first.n.Term()
	// the leader's pack view lists everyone, online, with their host reports
	waitFor(t, "all online", 10*time.Second, func() bool {
		s := first.n.Pack().Snapshot()
		if len(s.Authorities) != 3 {
			return false
		}
		for _, a := range s.Authorities {
			if a.Status != "online" || a.Host.CPUs != 4 {
				return false
			}
		}
		return true
	})
	// a member's view comes from the leader
	waitFor(t, "member view", 10*time.Second, func() bool {
		s := nodes[2].n.Pack().Snapshot()
		return !s.Degraded && s.Leader == "a" && len(s.Authorities) == 3
	})

	first.stop()
	rest := nodes[1:]
	waitFor(t, "new leader", 15*time.Second, func() bool { return leaderOf(rest) != nil })
	second := leaderOf(rest)
	if second.n.Term() <= term {
		t.Fatalf("term did not advance: %d -> %d", term, second.n.Term())
	}
	// the config survived on the new leader, and placement works on the two left
	if len(second.n.Config().Members) != 3 || second.n.Config().VaultHost != "a" {
		t.Fatalf("config lost: %+v", second.n.Config())
	}
	waitFor(t, "survivors online", 10*time.Second, func() bool {
		s := second.n.Pack().Snapshot()
		on := 0
		for _, a := range s.Authorities {
			if a.Status == "online" {
				on++
			}
		}
		return on == 2
	})
	p, err := rest[0].n.Place(context.Background(), PlaceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Term != second.n.Term() {
		t.Fatalf("placement term %d, leader term %d", p.Term, second.n.Term())
	}
}

func TestPlaceQueuesDuringFailover(t *testing.T) {
	nodes := createPack(t, "a", "b", "c")
	waitFor(t, "three voters", 10*time.Second, func() bool { return len(nodes[0].n.Status().Voters) == 3 })
	nodes[0].stop()
	var p Placement
	var err error
	done := make(chan struct{})
	go func() {
		p, err = nodes[1].n.Place(context.Background(), PlaceRequest{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("placement never completed")
	}
	if err != nil {
		t.Fatal(err)
	}
	if p.Authority == "a" {
		t.Fatalf("placed on the dead leader")
	}
}

func TestFencing(t *testing.T) {
	nodes := createPack(t, "a", "b")
	a, b := nodes[0], nodes[1]
	p, err := b.n.Place(context.Background(), PlaceRequest{Pinned: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.n.Execute(context.Background(), p, json.RawMessage(`{"x":1}`)); err != nil {
		// executes on b through /quorum/execute
		t.Fatal(err)
	}
	select {
	case got := <-b.executed:
		if got.Term != p.Term || string(got.Payload) != `{"x":1}` {
			t.Fatalf("executed %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not executed on b")
	}
	// a placement from an older term is refused, locally and remotely
	stale := p
	stale.Term = p.Term - 1
	if _, err := b.n.AcceptPlacement(context.Background(), stale); !errors.Is(err, ErrStaleTerm) {
		t.Fatalf("local: %v", err)
	}
	if _, err := a.n.Execute(context.Background(), stale, nil); !errors.Is(err, ErrStaleTerm) {
		t.Fatalf("remote: %v", err)
	}
	if len(b.executed) != 0 {
		t.Fatal("stale placement ran")
	}
	// a newer term moves the fence up
	newer := p
	newer.Term = p.Term + 5
	if _, err := b.n.AcceptPlacement(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if _, err := b.n.AcceptPlacement(context.Background(), p); !errors.Is(err, ErrStaleTerm) {
		t.Fatalf("after newer term: %v", err)
	}
}

func TestTwoNodeManualPromote(t *testing.T) {
	nodes := createPack(t, "a", "b")
	a, b := nodes[0], nodes[1]
	if got := a.n.Status(); got.AutoFailover || len(got.Voters) != 1 {
		t.Fatalf("two members must have one voter and no failover: %+v", got)
	}
	term := a.n.Term()
	a.stop()
	time.Sleep(time.Second)
	if b.n.IsLeader() {
		t.Fatal("two members must not fail over by themselves")
	}
	if _, err := b.n.Place(shortCtx(t), PlaceRequest{}); err == nil {
		t.Fatal("placement without a leader should fail after queueing")
	}
	if err := b.n.Promote(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if !b.n.IsLeader() || b.n.Term() <= term {
		t.Fatalf("promote: leader=%v term=%d (was %d)", b.n.IsLeader(), b.n.Term(), term)
	}
	if len(b.n.Config().Members) != 2 {
		t.Fatalf("config lost: %+v", b.n.Config())
	}
	waitFor(t, "self online", 5*time.Second, func() bool {
		_, err := b.n.Place(context.Background(), PlaceRequest{})
		return err == nil
	})
}

func shortCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestPromoteLiveLeaderTwoNodes(t *testing.T) {
	nodes := createPack(t, "a", "b")
	a, b := nodes[0], nodes[1]
	term := a.n.Term()
	if err := a.n.Promote(context.Background(), "b"); err != nil { // asks b to take over
		t.Fatal(err)
	}
	waitFor(t, "b leads", 15*time.Second, func() bool { return b.n.IsLeader() })
	waitFor(t, "a follows", 15*time.Second, func() bool { return !a.n.IsLeader() })
	if b.n.Term() <= term {
		t.Fatal("term did not advance")
	}
}

func TestScheduler(t *testing.T) {
	c := func(name string, used, max int, load float64) candidate {
		return candidate{Name: name, Host: HostInfo{GuestsUsed: used, GuestsMax: max, Load: load}}
	}
	cands := []candidate{c("a", 2, 4, 0.1), c("b", 1, 4, 0.9), c("c", 1, 4, 0.2), c("d", 4, 4, 0)}
	cases := []struct {
		req  PlaceRequest
		want string
	}{
		{PlaceRequest{}, "c"},                            // 3 free slots, lower load than b
		{PlaceRequest{Pinned: "d"}, "d"},                 // pinned wins, even when full
		{PlaceRequest{Requester: "b"}, "c"},              // load beats locality
		{PlaceRequest{Pinned: "a", Requester: "b"}, "a"}, // pinned beats everything
	}
	for _, tc := range cases {
		got, err := schedule(cands, tc.req)
		if err != nil || got.Name != tc.want {
			t.Errorf("%+v: got %v, %v want %s", tc.req, got.Name, err, tc.want)
		}
	}
	tie := []candidate{c("a", 1, 4, 0.5), c("b", 1, 4, 0.5), c("c", 1, 4, 0.5)}
	if got, _ := schedule(tie, PlaceRequest{Requester: "b"}); got.Name != "b" {
		t.Errorf("tie should go to the requester, got %s", got.Name)
	}
	if got, _ := schedule(tie, PlaceRequest{}); got.Name != "a" {
		t.Errorf("tie without requester should be stable, got %s", got.Name)
	}
	if _, err := schedule([]candidate{c("a", 4, 4, 0)}, PlaceRequest{}); !errors.Is(err, ErrNoCapacity) {
		t.Errorf("full pack: %v", err)
	}
	if _, err := schedule(cands, PlaceRequest{Pinned: "zzz"}); !errors.Is(err, ErrUnknownAuthority) {
		t.Errorf("unknown pin: %v", err)
	}
}

func TestPlacementUsesReports(t *testing.T) {
	nodes := createPack(t, "a", "b")
	a, b := nodes[0], nodes[1]
	a.slots, b.slots = 2, 6
	waitFor(t, "b has more room", 10*time.Second, func() bool {
		p, err := a.n.Place(context.Background(), PlaceRequest{Requester: "a"})
		return err == nil && p.Authority == "b"
	})
	p, err := b.n.Place(context.Background(), PlaceRequest{Pinned: "a"})
	if err != nil || p.Authority != "a" {
		t.Fatalf("pinned: %+v %v", p, err)
	}
	if _, err := b.n.Place(context.Background(), PlaceRequest{Pinned: "nope"}); !errors.Is(err, ErrUnknownAuthority) {
		t.Fatalf("unknown pin: %v", err)
	}
}

func TestInviteIsOneTime(t *testing.T) {
	nodes := createPack(t, "a")
	b := newTNode(t, "b")
	c := newTNode(t, "c")
	code, exp, err := nodes[0].n.Invite()
	if err != nil || time.Until(exp) < 9*time.Minute {
		t.Fatalf("invite: %v %v", exp, err)
	}
	if err := b.n.Join(context.Background(), nodes[0].srv.URL, "wrong"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := b.n.Join(context.Background(), nodes[0].srv.URL, code); err != nil {
		t.Fatal(err)
	}
	if err := c.n.Join(context.Background(), nodes[0].srv.URL, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("reused code: %v", err)
	}
	info, err := os.Stat(statePath(b.dir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("quorum.json: %v %v", info, err)
	}
	waitFor(t, "b sees the leader", 10*time.Second, func() bool { l, _ := b.n.Leader(); return l != "" })
	// a member can't hand out codes
	if _, _, err := b.n.Invite(); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("member invite: %v", err)
	}
}

func TestLeave(t *testing.T) {
	nodes := createPack(t, "a", "b")
	if err := nodes[1].n.Leave(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a forgets b", 10*time.Second, func() bool { return len(nodes[0].n.Config().Members) == 1 })
	if nodes[1].n.InPack() {
		t.Fatal("b should be out of the pack")
	}
	if _, err := os.Stat(statePath(nodes[1].dir)); !os.IsNotExist(err) {
		t.Fatal("quorum.json should be gone")
	}
	if err := nodes[0].n.Leave(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestVaultPolicyAuditAndCache(t *testing.T) {
	nodes := createPack(t, "a", "b")
	a, b := nodes[0], nodes[1]
	ctx := context.Background()
	if _, err := b.n.Vault().Get(ctx, "t1", "ANTHROPIC_API_KEY"); !errors.Is(err, ErrVaultDenied) {
		t.Fatalf("no policy should deny: %v", err)
	}
	if b.n.Vault().OK() == false {
		t.Fatal("a denial is an answer: the vault is reachable")
	}
	// policy is edited on the vault host only
	if err := b.n.Allow(ctx, "b", "ANTHROPIC_API_KEY"); err == nil {
		t.Fatal("a member must not edit the policy")
	}
	if err := a.n.Allow(ctx, "b", "ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "policy replicated", 10*time.Second, func() bool { return b.n.Config().Policy.Allows("b", "ANTHROPIC_API_KEY") })
	s, err := b.n.Vault().Get(ctx, "t1", "ANTHROPIC_API_KEY")
	if err != nil || s.Value != "sk-test" {
		t.Fatalf("allowed: %+v %v", s, err)
	}
	if _, err := b.n.Vault().Get(ctx, "t1", "github"); !errors.Is(err, ErrVaultDenied) {
		t.Fatalf("github isn't allowed: %v", err)
	}
	// TTL cache: the second Get doesn't reach the vault host
	before := a.vault.calls.Load()
	if _, err := b.n.Vault().Get(ctx, "t2", "ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if a.vault.calls.Load() != before {
		t.Fatal("cached secret should not ask the vault host")
	}
	// past the TTL it asks again
	now := time.Now().Add(11 * time.Minute)
	b.n.Vault().now = func() time.Time { return now }
	if _, err := b.n.Vault().Get(ctx, "t3", "ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if a.vault.calls.Load() != before+1 {
		t.Fatalf("expired secret should be fetched again (%d -> %d)", before, a.vault.calls.Load())
	}
	// deny takes it away again
	if err := a.n.Deny(ctx, "b", "ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "deny replicated", 10*time.Second, func() bool { return !a.n.Config().Policy.Allows("b", "ANTHROPIC_API_KEY") })
	b.n.Vault().Invalidate()
	if _, err := b.n.Vault().Get(ctx, "t4", "ANTHROPIC_API_KEY"); !errors.Is(err, ErrVaultDenied) {
		t.Fatalf("after deny: %v", err)
	}
	// missing secret
	if err := a.n.Allow(ctx, "b", "NOPE"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replicated", 10*time.Second, func() bool { return b.n.Config().Policy.Allows("b", "NOPE") })
	if _, err := b.n.Vault().Get(ctx, "t5", "NOPE"); !errors.Is(err, ErrSecretMissing) {
		t.Fatalf("missing: %v", err)
	}
	// the audit log has every request, with member, thread, secret and time, and no values
	data, err := os.ReadFile(filepath.Join(a.dir, "quorum-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-test") {
		t.Fatal("audit log holds a secret")
	}
	var entries []AuditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 6 {
		t.Fatalf("%d audit entries: %s", len(entries), data)
	}
	if e := entries[0]; e.Member != "b" || e.Thread != "t1" || e.Secret != "ANTHROPIC_API_KEY" || e.Result != "denied" || e.Time.IsZero() {
		t.Fatalf("first entry: %+v", e)
	}
	if info, _ := os.Stat(filepath.Join(a.dir, "quorum-audit.jsonl")); info.Mode().Perm() != 0o600 {
		t.Fatal("audit log should be 0600")
	}
}

func TestVaultUnreachable(t *testing.T) {
	nodes := createPack(t, "a", "b")
	a, b := nodes[0], nodes[1]
	ctx := context.Background()
	if err := a.n.Allow(ctx, "b", "ANTHROPIC_API_KEY", "github"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "policy replicated", 10*time.Second, func() bool { return b.n.Config().Policy.Allows("b", "github") })
	if _, err := b.n.Vault().Get(ctx, "t", "ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	a.stop()
	// the cached key keeps working, a new one fails distinctly
	if _, err := b.n.Vault().Get(ctx, "t", "ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("cached key: %v", err)
	}
	if _, err := b.n.Vault().Get(ctx, "t", "github"); !errors.Is(err, ErrVaultUnreachable) {
		t.Fatalf("uncached: %v", err)
	}
	if b.n.Vault().OK() {
		t.Fatal("vault should be reported unreachable")
	}
	if got := b.n.Status().Degraded; !strings.Contains(got, "vault unreachable") && !strings.Contains(got, "no leader") {
		t.Fatalf("status degraded = %q", got)
	}
}

func TestVaultHostItselfIsAudited(t *testing.T) {
	nodes := createPack(t, "a")
	s, err := nodes[0].n.Vault().Get(context.Background(), "t", "github")
	if err != nil || s.Value != "ghp" {
		t.Fatalf("%+v %v", s, err)
	}
	data, _ := os.ReadFile(filepath.Join(nodes[0].dir, "quorum-audit.jsonl"))
	if !strings.Contains(string(data), `"member":"a"`) {
		t.Fatalf("audit: %s", data)
	}
}

func TestSSHAgentRelay(t *testing.T) {
	nodes := createPack(t, "a", "b")
	a, b := nodes[0], nodes[1]
	sock := filepath.Join(t.TempDir(), "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				c.Write([]byte("agent:" + string(buf[:n])))
				c.Close()
			}()
		}
	}()
	a.n.cfg.SSHAgentSocket = func(guest string) (string, error) { return sock, nil }
	ctx := context.Background()
	local := filepath.Join(t.TempDir(), "guest.sock")
	// denied until the policy allows it
	ln, err := b.n.ListenSSHAgent(ctx, "g1", local)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	try := func() string {
		c, err := net.Dial("unix", local)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Write([]byte("hello"))
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		return string(buf[:n])
	}
	if got := try(); got != "" {
		t.Fatalf("relay without policy answered %q", got)
	}
	if err := a.n.Allow(ctx, "b", "ssh-agent"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "policy", 10*time.Second, func() bool { return b.n.Config().Policy.Allows("b", "ssh-agent") })
	if got := try(); got != "agent:hello" {
		t.Fatalf("relay answered %q", got)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	nodes := createPack(t, "a", "b")
	b := nodes[1]
	b.stop()
	b2, err := New(Config{Dir: b.dir, Name: "b", AdvertiseURL: b.n.cfg.AdvertiseURL, Raft: b.n.cfg.Raft, ReportInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !b2.InPack() || b2.Name() != "b" {
		t.Fatal("state lost")
	}
}

func TestPackAdapter(t *testing.T) {
	nodes := createPack(t, "a", "b")
	ad := NewPackAdapter(nodes[0].n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev, _ := ad.Subscribe(ctx)
	nodes[1].load = 3
	nodes[1].n.NotifyChange()
	waitFor(t, "b up", 10*time.Second, func() bool {
		p, _ := ad.Pack(ctx)
		return len(p.Authorities) == 2 && p.Authorities[1].Status == "up" && p.Leader == "a" && p.VaultHost == "a"
	})
	select {
	case e := <-ev:
		if e.Type != "pack" && e.Type != "pack.delta" {
			t.Fatalf("event %q", e.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	if _, err := ad.Threads(ctx, ""); err != nil {
		t.Fatal(err)
	}
}
