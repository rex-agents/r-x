package quorum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// Config is what the authority gives a Node.
type Config struct {
	Dir          string // ~/.rux: quorum.json, raft/, quorum-audit.jsonl
	Name         string // this authority's name (default: the hostname)
	AdvertiseURL string // where members reach this authority's quorum listener
	// LocalReport builds this authority's host report. Called every ReportInterval and on NotifyChange.
	LocalReport func(ctx context.Context) Report
	// Execute runs a placement on this authority (start the thread or sub-agent). It is only
	// called after the placement's term passed fencing.
	Execute func(ctx context.Context, p Placement) (json.RawMessage, error)
	// Vault is where this authority's secrets come from when it is the vault host. Default: rix.
	Vault VaultProvider
	// SSHAgentSocket returns the vault host's ssh-agent socket for a guest (phase 2).
	SSHAgentSocket func(guest string) (string, error)

	HTTPClient     *http.Client  // for dialing other authorities (e.g. with TLS roots)
	ReportInterval time.Duration // default 2s
	VaultTTL       time.Duration // default 10 min
	PlaceTimeout   time.Duration // how long a placement queues without a leader; default 30s
	Raft           func(*raft.Config)
	Logf           func(format string, args ...any)
}

// Node is this authority's membership in the pack.
type Node struct {
	cfg    Config
	client *http.Client
	fsm    *fsm

	mu      sync.RWMutex
	state   *State
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	rs      *raftStack
	wg      sync.WaitGroup

	invMu   sync.Mutex
	invites map[string]time.Time

	maxTerm atomic.Uint64
	changed chan struct{}
	pack    *packHub
	vault   *LeaderVault
	vhost   *vaultHost
	hostMu  sync.Mutex // serializes recovery and leave
}

type raftStack struct {
	r      *raft.Raft
	trans  *raft.NetworkTransport
	stream *streamLayer
	store  *raftboltdb.BoltStore
}

// New makes a Node. Call Start to begin, and mount Handler on the quorum listener.
func New(cfg Config) (*Node, error) {
	if cfg.Dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		cfg.Dir = filepath.Join(home, ".rux")
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	if cfg.ReportInterval == 0 {
		cfg.ReportInterval = 2 * time.Second
	}
	if cfg.VaultTTL == 0 {
		cfg.VaultTTL = 10 * time.Minute
	}
	if cfg.PlaceTimeout == 0 {
		cfg.PlaceTimeout = 30 * time.Second
	}
	if cfg.Vault == nil {
		cfg.Vault = RixVault{}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	st, err := loadState(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", statePath(cfg.Dir), err)
	}
	n := &Node{cfg: cfg, client: cfg.HTTPClient, fsm: newFSM(), state: st, invites: map[string]time.Time{}, changed: make(chan struct{}, 1)}
	n.pack = newPackHub(n)
	n.vault = newLeaderVault(n)
	n.vhost = newVaultHost(n)
	n.fsm.onChange = func() { n.persistSeed(); n.pack.membershipChanged(); n.NotifyChange() }
	return n, nil
}

// Name is this authority's name in the pack.
func (n *Node) Name() string {
	if st := n.getState(); st != nil {
		return st.Name
	}
	return n.cfg.Name
}

func (n *Node) getState() *State {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state
}

// InPack reports whether this authority has created or joined a pack.
func (n *Node) InPack() bool { return n.getState() != nil }

func (n *Node) raftNode() *raft.Raft {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.rs == nil {
		return nil
	}
	return n.rs.r
}

// Start runs the node's background work and, when in a pack, raft.
func (n *Node) Start(ctx context.Context) error {
	n.mu.Lock()
	if n.started {
		n.mu.Unlock()
		return nil
	}
	n.ctx, n.cancel = context.WithCancel(ctx)
	n.started = true
	n.mu.Unlock()
	if err := n.startRaft(nil); err != nil {
		return err
	}
	n.wg.Add(2)
	go func() { defer n.wg.Done(); n.leaderLoop() }()
	go func() { defer n.wg.Done(); n.reportLoop() }()
	return nil
}

// Close stops the node. State stays on disk.
func (n *Node) Close() error {
	n.mu.Lock()
	cancel := n.cancel
	n.started = false
	n.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	n.wg.Wait()
	n.stopRaft()
	return nil
}

func (n *Node) raftDir() string { return filepath.Join(n.cfg.Dir, "raft") }

func (n *Node) dialHeader() http.Header {
	h := http.Header{}
	if st := n.getState(); st != nil {
		h.Set("Authorization", "Bearer "+st.Name+":"+st.Token)
	}
	return h
}

// startRaft opens the raft stack for this member. recover, when set, forces the cluster
// configuration (promote).
func (n *Node) startRaft(recover *raft.Configuration) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.state
	if st == nil || !n.started || n.rs != nil {
		return nil
	}
	dir := n.raftDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	store, err := raftboltdb.NewBoltStore(filepath.Join(dir, "raft.db"))
	if err != nil {
		return err
	}
	snaps, err := raft.NewFileSnapshotStore(dir, 2, io.Discard)
	if err != nil {
		store.Close()
		return err
	}
	stream := newStreamLayer(st.URL, n.dialHeader, n.client)
	trans := raft.NewNetworkTransportWithConfig(&raft.NetworkTransportConfig{
		Stream: stream, MaxPool: 3, Timeout: 10 * time.Second, Logger: hclog.NewNullLogger()})
	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(st.Name)
	rc.Logger = hclog.NewNullLogger()
	rc.SnapshotThreshold = 1024
	if n.cfg.Raft != nil {
		n.cfg.Raft(rc)
	}
	fail := func(err error) error { trans.Close(); store.Close(); return err }
	if recover != nil {
		n.fsm.reset()
		n.fsm.muted.Store(true) // the callback takes n.mu, which startRaft holds
		err := raft.RecoverCluster(rc, n.fsm, store, store, snaps, trans, *recover)
		n.fsm.muted.Store(false)
		if err != nil {
			return fail(err)
		}
	} else if st.Bootstrap {
		has, err := raft.HasExistingState(store, store, snaps)
		if err != nil {
			return fail(err)
		}
		if !has {
			conf := raft.Configuration{Servers: []raft.Server{{Suffrage: raft.Voter, ID: rc.LocalID, Address: raft.ServerAddress(st.URL)}}}
			if err := raft.BootstrapCluster(rc, store, store, snaps, trans, conf); err != nil {
				return fail(err)
			}
		}
		st.Bootstrap = false
		if err := saveState(n.cfg.Dir, st); err != nil {
			return fail(err)
		}
	}
	r, err := raft.NewRaft(rc, n.fsm, store, store, snaps, trans)
	if err != nil {
		return fail(err)
	}
	n.rs = &raftStack{r: r, trans: trans, stream: stream, store: store}
	return nil
}

func (n *Node) stopRaft() {
	n.mu.Lock()
	rs := n.rs
	n.rs = nil
	n.mu.Unlock()
	if rs == nil {
		return
	}
	rs.r.Shutdown().Error()
	rs.stream.Close()
	rs.trans.Close()
	rs.store.Close()
}

func (f *fsm) reset() {
	f.mu.Lock()
	f.cfg = emptyConfig()
	f.mu.Unlock()
}

// Term is the raft term this authority last saw.
func (n *Node) Term() uint64 {
	r := n.raftNode()
	if r == nil {
		return 0
	}
	t, _ := strconv.ParseUint(r.Stats()["term"], 10, 64)
	return t
}

// Leader is the current pack leader's name and URL, or "" without one.
func (n *Node) Leader() (name, url string) {
	r := n.raftNode()
	if r == nil {
		return "", ""
	}
	a, id := r.LeaderWithID()
	return string(id), string(a)
}

// IsLeader reports whether this authority is the pack leader.
func (n *Node) IsLeader() bool {
	r := n.raftNode()
	return r != nil && r.State() == raft.Leader
}

// Config is a copy of the replicated config.
func (n *Node) Config() ClusterConfig { return n.fsm.get() }

// seedMembers is the config as the leader gave it at join, until raft replicates a newer one.
func (n *Node) memberByName(name string) (Member, bool) {
	if m, ok := n.fsm.get().Members[name]; ok {
		return m, true
	}
	if st := n.getState(); st != nil {
		for _, m := range st.Seed {
			if m.Name == name {
				return m, true
			}
		}
	}
	return Member{}, false
}

// authenticate returns the member named by the request's bearer credential.
func (n *Node) authenticate(r *http.Request) (string, bool) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	name, token, ok := strings.Cut(auth, ":")
	if !ok || name == "" {
		return "", false
	}
	m, ok := n.memberByName(name)
	if !ok || !tokenMatches(m.TokenHash, token) {
		return "", false
	}
	return name, true
}

func (n *Node) logf(f string, a ...any) { n.cfg.Logf(f, a...) }

// NotifyChange makes the node push a host report now. Call it when threads, sub-agents or
// guests start, stop or change state.
func (n *Node) NotifyChange() {
	select {
	case n.changed <- struct{}{}:
	default:
	}
}

// ---- config log ----

func (n *Node) applyLocal(c command) error {
	r := n.raftNode()
	if r == nil || r.State() != raft.Leader {
		return ErrNotLeader
	}
	data, _ := json.Marshal(c)
	f := r.Apply(data, 10*time.Second)
	if err := f.Error(); err != nil {
		if errors.Is(err, raft.ErrNotLeader) || errors.Is(err, raft.ErrLeadershipLost) {
			return ErrNotLeader
		}
		return err
	}
	if err, ok := f.Response().(error); ok && err != nil {
		return err
	}
	return nil
}

// apply puts a config change in the log: on the leader directly, from a member through the
// leader. Policy and vault-host changes are only accepted from the vault host.
func (n *Node) apply(ctx context.Context, c command) error {
	if !n.InPack() {
		return ErrNotInPack
	}
	if n.IsLeader() {
		return n.authorizeAndApply(n.Name(), c)
	}
	_, url := n.Leader()
	if url == "" {
		return ErrNoLeader
	}
	return n.call(ctx, url, http.MethodPost, "/quorum/config", c, nil)
}

func (n *Node) authorizeAndApply(from string, c command) error {
	cfg := n.fsm.get()
	switch c.Op {
	case cmdPolicy:
		if from != cfg.VaultHost {
			return fmt.Errorf("vault policy is edited on the vault host, %s", cfg.VaultHost)
		}
	case cmdVaultHost:
		if _, ok := cfg.Members[c.Name]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownAuthority, c.Name)
		}
		leader := n.Name()
		if from != cfg.VaultHost && from != leader {
			return fmt.Errorf("the vault host moves from the vault host, %s, or the leader", cfg.VaultHost)
		}
	case cmdProject, cmdSuccession:
	default:
		return fmt.Errorf("config command %q isn't accepted from members", c.Op)
	}
	if err := n.applyLocal(c); err != nil {
		return err
	}
	return n.reconcile()
}

// SetPolicy changes the vault policy with fn, applied to a copy of the current one. Run it
// on the vault host.
func (n *Node) SetPolicy(ctx context.Context, fn func(*Policy) error) error {
	p := n.fsm.get().Policy
	if err := fn(&p); err != nil {
		return err
	}
	return n.apply(ctx, command{Op: cmdPolicy, Policy: &p})
}

// Allow lets member read the named secrets from the vault host.
func (n *Node) Allow(ctx context.Context, member string, secrets ...string) error {
	return n.SetPolicy(ctx, func(p *Policy) error {
		if _, ok := n.memberByName(member); !ok {
			return fmt.Errorf("%s isn't in the pack", member)
		}
		mp := p.Members[member]
		for _, s := range secrets {
			if !contains(mp.Allow, s) {
				mp.Allow = append(mp.Allow, s)
			}
		}
		p.Members[member] = mp
		return nil
	})
}

// Deny removes the named secrets from member's allowance.
func (n *Node) Deny(ctx context.Context, member string, secrets ...string) error {
	return n.SetPolicy(ctx, func(p *Policy) error {
		mp := p.Members[member]
		for _, s := range secrets {
			mp.Allow = without(mp.Allow, s)
		}
		p.Members[member] = mp
		return nil
	})
}

// SetVaultHost moves the vault host. It never moves on its own.
func (n *Node) SetVaultHost(ctx context.Context, name string) error {
	return n.apply(ctx, command{Op: cmdVaultHost, Name: name})
}

// ---- membership ----

// Create makes this authority the pack leader and vault host of a new pack.
func (n *Node) Create() error {
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	if n.InPack() {
		return errors.New("this authority is already in a pack. Run rux quorum leave first")
	}
	if n.cfg.AdvertiseURL == "" {
		return errors.New("no quorum URL for this authority")
	}
	st := &State{Name: n.cfg.Name, URL: n.cfg.AdvertiseURL, Token: randomToken(24), Bootstrap: true}
	if err := saveState(n.cfg.Dir, st); err != nil {
		return err
	}
	n.mu.Lock()
	n.state = st
	n.mu.Unlock()
	return n.startRaft(nil)
}

// Invite makes a one-time join code, valid for 10 minutes. Only the leader hands them out.
func (n *Node) Invite() (code string, expires time.Time, err error) {
	if !n.InPack() {
		return "", time.Time{}, ErrNotInPack
	}
	if !n.IsLeader() {
		if n.raftNode() == nil {
			return "", time.Time{}, errors.New("the authority isn't running, so it can't hand out join codes")
		}
		name, _ := n.Leader()
		if name == "" {
			return "", time.Time{}, ErrNoLeader
		}
		return "", time.Time{}, fmt.Errorf("%w. Run rux quorum invite on %s", ErrNotLeader, name)
	}
	code = randomToken(5)
	expires = time.Now().Add(10 * time.Minute)
	n.invMu.Lock()
	n.invites[code] = expires
	n.invMu.Unlock()
	return code, expires, nil
}

func (n *Node) consumeInvite(code string) bool {
	n.invMu.Lock()
	defer n.invMu.Unlock()
	exp, ok := n.invites[code]
	delete(n.invites, code)
	for c, e := range n.invites {
		if time.Now().After(e) {
			delete(n.invites, c)
		}
	}
	return ok && time.Now().Before(exp)
}

type joinRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type joinResponse struct {
	Name      string        `json:"name"` // the leader's
	Token     string        `json:"token"`
	LeaderURL string        `json:"leader_url"`
	Config    ClusterConfig `json:"config"`
}

// Join joins the pack whose leader is at host (a URL or host[:port]), with its join code.
func (n *Node) Join(ctx context.Context, host, code string) error {
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	if n.InPack() {
		return errors.New("this authority is already in a pack. Run rux quorum leave first")
	}
	if n.cfg.AdvertiseURL == "" {
		return errors.New("no quorum URL for this authority")
	}
	base := normalizeHost(host)
	var resp joinResponse
	err := n.call(ctx, base, http.MethodPost, "/quorum/join", joinRequest{Code: code, Name: n.cfg.Name, URL: n.cfg.AdvertiseURL}, &resp)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == "not_leader" && ae.Leader != "" {
			return fmt.Errorf("%s isn't the pack leader. Join through %s", host, ae.Leader)
		}
		return err
	}
	st := &State{Name: n.cfg.Name, URL: n.cfg.AdvertiseURL, Token: resp.Token, LeaderURL: resp.LeaderURL}
	for _, m := range resp.Config.Members {
		st.Seed = append(st.Seed, m)
	}
	if err := saveState(n.cfg.Dir, st); err != nil {
		return err
	}
	n.mu.Lock()
	n.state = st
	n.mu.Unlock()
	return n.startRaft(nil)
}

func normalizeHost(h string) string {
	if !strings.Contains(h, "://") {
		if !strings.Contains(h, ":") {
			h = fmt.Sprintf("%s:%d", h, DefaultPort)
		}
		h = "http://" + h
	}
	return strings.TrimRight(h, "/")
}

// Leave takes this authority out of the pack and forgets its credentials.
func (n *Node) Leave(ctx context.Context) error {
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	st := n.getState()
	if st == nil {
		return ErrNotInPack
	}
	cfg := n.fsm.get()
	if r := n.raftNode(); r != nil && len(cfg.Members) > 1 {
		if r.State() == raft.Leader {
			if len(cfg.Members) < 3 {
				return errors.New("this authority is the pack leader. Run rux quorum promote on the other authority first")
			}
			if err := r.LeadershipTransfer().Error(); err != nil {
				return fmt.Errorf("could not hand over leadership: %w", err)
			}
			time.Sleep(n.cfg.ReportInterval / 2)
		}
		_, url := n.Leader()
		if url == "" {
			return ErrNoLeader
		}
		if err := n.call(ctx, url, http.MethodPost, "/quorum/leave", nil, nil); err != nil {
			return err
		}
	}
	n.stopRaft()
	os.RemoveAll(n.raftDir())
	os.Remove(statePath(n.cfg.Dir))
	n.mu.Lock()
	n.state = nil
	n.mu.Unlock()
	n.fsm.reset()
	n.pack.membershipChanged()
	return nil
}

// Promote makes the named member the pack leader. With three or more members and a live
// leader it hands leadership over; with two, or when no leader is reachable, the named
// member forces itself leader. Run it on the member to promote when the leader is down.
func (n *Node) Promote(ctx context.Context, name string) error {
	if !n.InPack() {
		return ErrNotInPack
	}
	cfg := n.fsm.get()
	m, ok := cfg.Members[name]
	if !ok {
		return fmt.Errorf("%s isn't in the pack", name)
	}
	if name != n.Name() {
		return n.call(ctx, m.URL, http.MethodPost, "/quorum/promote", nil, nil)
	}
	return n.promoteSelf(ctx)
}

func (n *Node) promoteSelf(ctx context.Context) error {
	if n.IsLeader() {
		return nil
	}
	cfg := n.fsm.get()
	if _, url := n.Leader(); url != "" && len(cfg.Members) >= 3 {
		return n.call(ctx, url, http.MethodPost, "/quorum/transfer", map[string]string{"to": n.Name()}, nil)
	}
	if _, url := n.Leader(); url != "" {
		// a live leader keeps sending heartbeats, so it has to step down first
		_ = n.call(ctx, url, http.MethodPost, "/quorum/stepdown", map[string]string{"to": n.Name()}, nil)
	}
	return n.forceLeader(n.Name())
}

// forceLeader rewrites this member's raft configuration so voter is the only voter (this
// member when voter is its own name, and then it wins the next election). The others stay as non-voters and follow once they hear the higher term.
func (n *Node) forceLeader(voter string) error {
	n.hostMu.Lock()
	defer n.hostMu.Unlock()
	st := n.getState()
	if st == nil {
		return ErrNotInPack
	}
	cfg := n.fsm.get()
	conf := raft.Configuration{}
	names := make([]string, 0, len(cfg.Members))
	for name := range cfg.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := raft.Nonvoter
		if name == voter {
			s = raft.Voter
		}
		conf.Servers = append(conf.Servers, raft.Server{Suffrage: s, ID: raft.ServerID(name), Address: raft.ServerAddress(cfg.Members[name].URL)})
	}
	if len(conf.Servers) == 0 {
		conf.Servers = []raft.Server{{Suffrage: raft.Voter, ID: raft.ServerID(st.Name), Address: raft.ServerAddress(st.URL)}}
	}
	n.stopRaft()
	if err := n.startRaft(&conf); err != nil {
		return err
	}
	if voter != st.Name {
		return nil
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if n.IsLeader() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("promoted, but this authority has not become leader yet")
}

// reconcile makes raft's server list match the replicated members: with three or more
// members every member votes; with fewer only the leader does, so no election can split.
func (n *Node) reconcile() error {
	r := n.raftNode()
	st := n.getState()
	if r == nil || st == nil || r.State() != raft.Leader {
		return nil
	}
	cfg := n.fsm.get()
	if _, ok := cfg.Members[st.Name]; !ok {
		err := n.applyLocal(command{Op: cmdAddMember, Member: &Member{Name: st.Name, URL: st.URL, TokenHash: hashToken(st.Token), Joined: time.Now()}})
		if err != nil {
			return err
		}
		cfg = n.fsm.get()
	}
	f := r.GetConfiguration()
	if err := f.Error(); err != nil {
		return err
	}
	have := map[string]raft.Server{}
	for _, s := range f.Configuration().Servers {
		have[string(s.ID)] = s
	}
	names := make([]string, 0, len(cfg.Members))
	for name := range cfg.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m := cfg.Members[name]
		voter := len(cfg.Members) >= 3 || name == st.Name
		s, ok := have[name]
		id, addr := raft.ServerID(name), raft.ServerAddress(m.URL)
		var err error
		switch {
		case !ok || s.Address != addr:
			if voter {
				err = r.AddVoter(id, addr, 0, 10*time.Second).Error()
			} else {
				err = r.AddNonvoter(id, addr, 0, 10*time.Second).Error()
			}
		case voter && s.Suffrage != raft.Voter:
			err = r.AddVoter(id, addr, 0, 10*time.Second).Error()
		case !voter && s.Suffrage == raft.Voter:
			err = r.DemoteVoter(id, 0, 10*time.Second).Error()
		}
		if err != nil {
			return err
		}
	}
	for id := range have {
		if _, ok := cfg.Members[id]; !ok && len(cfg.Members) > 0 && id != st.Name {
			if err := r.RemoveServer(raft.ServerID(id), 0, 10*time.Second).Error(); err != nil {
				return err
			}
		}
	}
	return nil
}

// leaderLoop does the leader's housekeeping: raft membership and the pack view.
func (n *Node) leaderLoop() {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-t.C:
		}
		if n.IsLeader() {
			if err := n.reconcile(); err != nil {
				n.logf("quorum: reconcile: %v", err)
			}
		}
		n.observeTerm(n.Term())
		n.pack.tick()
	}
}

// Status describes this authority's place in the pack.
func (n *Node) Status() Status {
	st := n.getState()
	if st == nil {
		return Status{}
	}
	cfg := n.fsm.get()
	s := Status{InPack: true, Name: st.Name, VaultHost: cfg.VaultHost, Term: n.Term(), IsLeader: n.IsLeader(),
		AutoFailover: len(cfg.Members) >= 3, SSHAgent: n.sshAgentStatus(cfg)}
	s.Leader, _ = n.Leader()
	names := make([]string, 0, len(cfg.Members))
	for name := range cfg.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s.Members = append(s.Members, cfg.Members[name])
	}
	if r := n.raftNode(); r != nil {
		if f := r.GetConfiguration(); f.Error() == nil {
			for _, sv := range f.Configuration().Servers {
				if sv.Suffrage == raft.Voter {
					s.Voters = append(s.Voters, string(sv.ID))
				}
			}
		} else if s.Leader == "" {
			s.Voters = nil
		}
	}
	s.VaultOK = n.vault.OK()
	switch {
	case s.Leader == "" && n.raftNode() == nil:
		s.Degraded = "the authority isn't running"
	case s.Leader == "":
		s.Degraded = "no leader: running work continues, new placements queue"
	case !s.VaultOK:
		s.Degraded = "vault unreachable"
	}
	return s
}

func localHost() HostInfo {
	return HostInfo{OS: runtime.GOOS, CPUs: runtime.NumCPU()}
}

// persistSeed keeps the members and their credential hashes in quorum.json whenever the
// replicated config changes. A restarted authority needs them before raft has replayed anything:
// the other members authenticate to it with credentials only the config knows, and without
// them a restarted leader (or any member that missed the change) would refuse its own pack.
func (n *Node) persistSeed() {
	cfg := n.fsm.get()
	if len(cfg.Members) == 0 {
		return
	}
	seed := make([]Member, 0, len(cfg.Members))
	for _, m := range cfg.Members {
		seed = append(seed, m)
	}
	sort.Slice(seed, func(i, j int) bool { return seed[i].Name < seed[j].Name })
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.state
	if st == nil {
		return
	}
	if old, _ := json.Marshal(st.Seed); string(old) == mustJSON(seed) {
		return
	}
	st.Seed = seed
	if err := saveState(n.cfg.Dir, st); err != nil {
		n.logf("quorum: saving the member list: %v", err)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
