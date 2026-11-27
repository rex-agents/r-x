// Package quorum joins rux authorities into a pack: membership, host reports, leader
// election and the replicated config (hashicorp/raft), placement with term fencing, and a
// vault the pack shares through its vault host. See docs/rux/quorum.md.
//
// The authority constructs a Node with callbacks, mounts Node.Handler on its quorum
// listener (default port 7710) and adapts Node.Pack and Node.Vault to its own interfaces.
package quorum

import (
	"encoding/json"
	"errors"
	"time"
)

// DefaultPort is the quorum listener's default port.
const DefaultPort = 7710

// Errors the authority and UI tell apart.
var (
	ErrNotInPack        = errors.New("this authority isn't in a pack. Run rux quorum create or rux quorum join")
	ErrNoLeader         = errors.New("the pack has no leader right now")
	ErrNotLeader        = errors.New("this authority isn't the pack leader")
	ErrStaleTerm        = errors.New("placement is from an older leader term")
	ErrNoCapacity       = errors.New("no authority in the pack has a free guest slot")
	ErrUnknownAuthority = errors.New("no such authority in the pack")
	ErrVaultUnreachable = errors.New("vault unreachable")
	ErrVaultDenied      = errors.New("the vault host's policy doesn't allow this secret")
	ErrSecretMissing    = errors.New("the vault host doesn't have this secret")
	ErrBadCode          = errors.New("that join code is wrong or has expired")
)

// HostInfo describes a machine, as its authority reports it.
type HostInfo struct {
	OS         string  `json:"os"`
	CPUs       int     `json:"cpus"`
	Mem        uint64  `json:"mem"`
	Load       float64 `json:"load"`
	GuestsMax  int     `json:"guests_max"`
	GuestsUsed int     `json:"guests_used"`
}

// Report is what a member pushes to the leader every 2s and on change. Threads and Subagents
// are opaque to the quorum: the authority's own wire types (internal/rux/authapi).
type Report struct {
	Authority string            `json:"authority"`
	Host      HostInfo          `json:"host"`
	Threads   []json.RawMessage `json:"threads"`
	Subagents []json.RawMessage `json:"subagents"`
	Term      uint64            `json:"term"`
	Started   string            `json:"started,omitempty"` // RFC 3339 time the authority started
}

// AuthorityInfo is one authority in the pack view (GET /api/pack "authorities").
type AuthorityInfo struct {
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Status    string            `json:"status"` // online, stale or unreachable
	Host      HostInfo          `json:"host"`
	Local     bool              `json:"local"`
	Started   string            `json:"started,omitempty"`
	Threads   []json.RawMessage `json:"threads,omitempty"`
	Subagents []json.RawMessage `json:"subagents,omitempty"`
}

// PackSnapshot is the pack view (GET /api/pack).
type PackSnapshot struct {
	Seq         uint64          `json:"seq"`
	Leader      string          `json:"leader"`
	Term        uint64          `json:"term"`
	VaultHost   string          `json:"vault_host"`
	VaultOK     bool            `json:"vault_ok"`
	Degraded    bool            `json:"degraded,omitempty"`
	Authorities []AuthorityInfo `json:"authorities"`
}

// PackDelta is one change on the pack stream. Type is "pack" (Pack is a full snapshot, sent
// on leader, term, vault or membership changes), "authority" (Authority changed) or
// "authority.remove" (Name left). A gap in Seq means the receiver should take a snapshot.
type PackDelta struct {
	Seq       uint64         `json:"seq"`
	Type      string         `json:"type"`
	Name      string         `json:"name,omitempty"`
	Authority *AuthorityInfo `json:"authority,omitempty"`
	Pack      *PackSnapshot  `json:"pack,omitempty"`
}

// PackSource is the pack view the authority serves: a snapshot plus a delta channel.
type PackSource interface {
	Snapshot() PackSnapshot
	Subscribe() (<-chan PackDelta, func())
}

// PlaceRequest asks the leader where new work runs.
type PlaceRequest struct {
	Requester string `json:"requester"`
	Pinned    string `json:"pinned,omitempty"` // "placement": {"authority": NAME}
}

// Placement is a decision, to be executed by Authority. Executors reject a Term older than
// the newest they have seen (fencing).
type Placement struct {
	ID        string          `json:"id,omitempty"`
	Authority string          `json:"authority"`
	URL       string          `json:"url,omitempty"`
	Term      uint64          `json:"term"`
	Requester string          `json:"requester,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Member is one authority in the replicated config.
type Member struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	TokenHash string    `json:"token_hash"` // sha256 of the member's bearer token, hex
	Joined    time.Time `json:"joined"`
}

// MemberPolicy lists the secrets a member may ask the vault host for. "*" allows all.
type MemberPolicy struct {
	Allow []string `json:"allow"`
}

// Policy is the vault policy: {"members": {"NAME": {"allow": ["ANTHROPIC_API_KEY", "github"]}}}.
type Policy struct {
	Members map[string]MemberPolicy `json:"members"`
}

// Allows reports whether member may read secret.
func (p Policy) Allows(member, secret string) bool {
	for _, a := range p.Members[member].Allow {
		if a == "*" || a == secret {
			return true
		}
	}
	return false
}

// ClusterConfig is the small replicated state.
type ClusterConfig struct {
	Members         map[string]Member          `json:"members"`
	Succession      []string                   `json:"succession"`
	VaultHost       string                     `json:"vault_host"`
	Policy          Policy                     `json:"policy"`
	ProjectDefaults map[string]json.RawMessage `json:"project_defaults,omitempty"`
}

// Status is what rux quorum status shows.
type Status struct {
	InPack       bool     `json:"in_pack"`
	Name         string   `json:"name"`
	Leader       string   `json:"leader"`
	Term         uint64   `json:"term"`
	IsLeader     bool     `json:"is_leader"`
	Members      []Member `json:"members"`
	Voters       []string `json:"voters"`
	VaultHost    string   `json:"vault_host"`
	VaultOK      bool     `json:"vault_ok"`
	AutoFailover bool     `json:"auto_failover"` // three or more members
	SSHAgent     string   `json:"ssh_agent"`
	Degraded     string   `json:"degraded,omitempty"`
}
