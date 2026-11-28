package quorum

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/raft"
)

// Config log commands.
const (
	cmdAddMember    = "add_member"
	cmdRemoveMember = "remove_member"
	cmdVaultHost    = "vault_host"
	cmdPolicy       = "policy"
	cmdSuccession   = "succession"
	cmdProject      = "project_defaults"
)

type command struct {
	Op      string          `json:"op"`
	Member  *Member         `json:"member,omitempty"`
	Name    string          `json:"name,omitempty"`
	Policy  *Policy         `json:"policy,omitempty"`
	Names   []string        `json:"names,omitempty"`
	Project string          `json:"project,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

// fsm is the replicated ClusterConfig.
type fsm struct {
	mu       sync.RWMutex
	cfg      ClusterConfig
	onChange func()
	muted    atomic.Bool
}

func newFSM() *fsm { return &fsm{cfg: emptyConfig()} }

func emptyConfig() ClusterConfig {
	return ClusterConfig{Members: map[string]Member{}, Policy: Policy{Members: map[string]MemberPolicy{}}}
}

func (f *fsm) get() ClusterConfig {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, _ := json.Marshal(f.cfg)
	var c ClusterConfig
	json.Unmarshal(data, &c)
	if c.Members == nil {
		c.Members = map[string]Member{}
	}
	if c.Policy.Members == nil {
		c.Policy.Members = map[string]MemberPolicy{}
	}
	return c
}

func (f *fsm) Apply(l *raft.Log) any {
	var c command
	if err := json.Unmarshal(l.Data, &c); err != nil {
		return err
	}
	f.mu.Lock()
	err := f.apply(c)
	f.mu.Unlock()
	if err == nil && f.onChange != nil && !f.muted.Load() {
		go f.onChange() // async: raft calls us while the node holds its lock
	}
	return err
}

func (f *fsm) apply(c command) error {
	cfg := &f.cfg
	switch c.Op {
	case cmdAddMember:
		if c.Member == nil || c.Member.Name == "" {
			return errors.New("member needs a name")
		}
		cfg.Members[c.Member.Name] = *c.Member
		if _, ok := cfg.Policy.Members[c.Member.Name]; !ok {
			// A new member may read the model providers' credentials and GitHub; named
			// secrets stay deny-by-default until `rux quorum policy allow`.
			cfg.Policy.Members[c.Member.Name] = MemberPolicy{Allow: append([]string(nil), DefaultAllow...)}
		}
		if !contains(cfg.Succession, c.Member.Name) {
			cfg.Succession = append(cfg.Succession, c.Member.Name)
		}
		if cfg.VaultHost == "" {
			cfg.VaultHost = c.Member.Name
		}
	case cmdRemoveMember:
		delete(cfg.Members, c.Name)
		cfg.Succession = without(cfg.Succession, c.Name)
		delete(cfg.Policy.Members, c.Name)
	case cmdVaultHost:
		if _, ok := cfg.Members[c.Name]; !ok {
			return fmt.Errorf("%s isn't in the pack", c.Name)
		}
		cfg.VaultHost = c.Name
	case cmdPolicy:
		if c.Policy != nil {
			cfg.Policy = *c.Policy
		}
	case cmdSuccession:
		cfg.Succession = c.Names
	case cmdProject:
		if cfg.ProjectDefaults == nil {
			cfg.ProjectDefaults = map[string]json.RawMessage{}
		}
		cfg.ProjectDefaults[c.Project] = c.Value
	default:
		return fmt.Errorf("unknown config command %q", c.Op)
	}
	return nil
}

type snapshot struct{ data []byte }

func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, err := json.Marshal(f.cfg)
	return &snapshot{data}, err
}

func (f *fsm) Restore(r io.ReadCloser) error {
	defer r.Close()
	cfg := emptyConfig()
	if err := json.NewDecoder(r).Decode(&cfg); err != nil {
		return err
	}
	if cfg.Members == nil {
		cfg.Members = map[string]Member{}
	}
	if cfg.Policy.Members == nil {
		cfg.Policy.Members = map[string]MemberPolicy{}
	}
	f.mu.Lock()
	f.cfg = cfg
	f.mu.Unlock()
	if f.onChange != nil {
		go f.onChange() // async: raft calls us while the node holds its lock
	}
	return nil
}

func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}
func (s *snapshot) Release() {}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func without(l []string, s string) []string {
	var out []string
	for _, x := range l {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

var _ = time.Second
