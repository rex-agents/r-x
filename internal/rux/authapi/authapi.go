// Package authapi holds the wire types shared by the rux authority HTTP API
// and its clients (the authority TUI and the web app). JSON tags are the wire
// format; the web app mirrors them. See docs/rux/authority.md.
package authapi

// Pack is the GET /api/pack response: the leader's view of every authority.
type Pack struct {
	Leader      string          `json:"leader"`
	Term        uint64          `json:"term"`
	VaultHost   string          `json:"vault_host"`
	VaultOK     bool            `json:"vault_ok"`
	Degraded    bool            `json:"degraded,omitempty"` // local view served because no leader is reachable
	Authorities []AuthorityInfo `json:"authorities"`
	// Clients are the UIs connected to the pack right now (live GET /api/pack/stream connections).
	Clients []ClientInfo `json:"clients"`
}

// ClientInfo is a UI connected to the pack.
type ClientInfo struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`      // "tui" or "web"
	Device    string `json:"device"`    // machine or device name
	Authority string `json:"authority"` // the authority it connected through
	Since     string `json:"since"`     // RFC 3339
	LastSeen  string `json:"last_seen"` // RFC 3339
}

// GuestInfo is a rox guest on an authority's host.
type GuestInfo struct {
	Name     string  `json:"name"`
	Phase    string  `json:"phase"`             // running, stopped, starting, ...
	Backend  string  `json:"backend,omitempty"` // container, linux, macos, fly, railway
	CPU      float64 `json:"cpu"`               // percent of one CPU
	MemBytes int64   `json:"mem_bytes"`
	MemLimit int64   `json:"mem_limit,omitempty"`
	Thread   string  `json:"thread,omitempty"` // the thread ID using it
}

// AuthorityInfo is one authority in the pack.
type AuthorityInfo struct {
	Name    string          `json:"name"`
	URL     string          `json:"url"`
	Status  string          `json:"status"` // "up", "down", "joining"
	Host    HostInfo        `json:"host"`
	Local   bool            `json:"local"`             // the authority answering the request
	Started string          `json:"started,omitempty"` // RFC 3339 time the authority started, for uptime
	Threads []ThreadSummary `json:"threads,omitempty"`
	Guests  []GuestInfo     `json:"guests,omitempty"` // rox guests on this host
}

// HostInfo describes the machine an authority runs on.
type HostInfo struct {
	OS         string  `json:"os"`
	CPUs       int     `json:"cpus"`
	Mem        uint64  `json:"mem"` // total bytes
	Load       float64 `json:"load"`
	GuestsMax  int     `json:"guests_max"`
	GuestsUsed int     `json:"guests_used"`
}

// Placement says where a thread or sub-agent runs.
type Placement struct {
	Local     bool   `json:"local,omitempty"`     // on the host, not in a guest
	Authority string `json:"authority,omitempty"` // pin to this authority
}

// SubAgentSummary is a sub-agent of a thread.
type SubAgentSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"`
	Guest  string `json:"guest,omitempty"`
	// Authority is where the sub-agent runs; it can differ from its parent's.
	Authority string `json:"authority,omitempty"`
}

// ThreadSummary is a thread as shown in the pack view.
type ThreadSummary struct {
	ID      string `json:"id"`
	Project string `json:"project,omitempty"`
	Title   string `json:"title"`
	// Status is the thread's status, or "archived" for an archived thread.
	Status    string            `json:"status"`
	Archived  bool              `json:"archived,omitempty"`
	Authority string            `json:"authority"`
	Guest     string            `json:"guest,omitempty"`
	Placement Placement         `json:"placement"`
	SubAgents []SubAgentSummary `json:"subagents"`
	// Parent is the thread that started this one as a sub-agent, on ParentAuthority.
	Parent          string  `json:"parent,omitempty"`
	ParentAuthority string  `json:"parent_authority,omitempty"`
	Cost            float64 `json:"cost"`
	Updated         string  `json:"updated"`        // RFC 3339
	Kind            string  `json:"kind,omitempty"` // "manager" for a project's manager, else ""
}

// Event types carried by PackEvent.
const (
	EventPack         = "pack"
	EventPackDelta    = "pack.delta"
	EventThreadEvent  = "thread.event"
	EventThreadStatus = "thread.status"
	EventPackClients  = "pack.clients" // payload: []ClientInfo, sent when the set changes
)

// PackEvent is the envelope of every SSE event on GET /api/pack/stream. There is one form:
//
//	event: <Type>
//	data: {"type":"<Type>","data":<payload>}
//
// The payload is Pack for "pack", AuthorityInfo (without threads) for "pack.delta",
// ThreadEvent for "thread.event" and ThreadSummary for "thread.status".
type PackEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// ThreadEvent is a conversation event of a thread. Event is the stored event record exactly
// as GET /api/threads/{id}/events and /events/page return it: {"id","ts","src","rseq","ev"}.
// A sub-agent's conversation is at /api/threads/{thread}/subagents/{id}/events (and /events/page).
type ThreadEvent struct {
	Thread    string `json:"thread"`
	Authority string `json:"authority,omitempty"`
	Event     any    `json:"event"`
}
