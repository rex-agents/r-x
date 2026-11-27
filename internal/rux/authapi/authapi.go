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
}

// AuthorityInfo is one authority in the pack.
type AuthorityInfo struct {
	Name    string          `json:"name"`
	URL     string          `json:"url"`
	Status  string          `json:"status"` // "up", "down", "joining"
	Host    HostInfo        `json:"host"`
	Local   bool            `json:"local"` // the authority answering the request
	Threads []ThreadSummary `json:"threads,omitempty"`
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
}

// ThreadSummary is a thread as shown in the pack view.
type ThreadSummary struct {
	ID        string            `json:"id"`
	Project   string            `json:"project,omitempty"`
	Title     string            `json:"title"`
	Status    string            `json:"status"`
	Authority string            `json:"authority"`
	Guest     string            `json:"guest,omitempty"`
	Placement Placement         `json:"placement"`
	SubAgents []SubAgentSummary `json:"subagents"`
	Cost      float64           `json:"cost"`
	Updated   string            `json:"updated"` // RFC 3339
}

// Event types carried by PackEvent.
const (
	EventPack         = "pack"
	EventPackDelta    = "pack.delta"
	EventThreadEvent  = "thread.event"
	EventThreadStatus = "thread.status"
)

// PackEvent is the envelope of every SSE event on GET /api/pack/stream
// (the SSE "event:" name equals Type; Data is the JSON of the matching payload:
// Pack for "pack", AuthorityInfo for "pack.delta", ThreadEvent for
// "thread.event", ThreadSummary for "thread.status").
type PackEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// ThreadEvent is a conversation event of a thread, forwarded from the thread's own stream.
type ThreadEvent struct {
	Thread    string `json:"thread"`
	Authority string `json:"authority,omitempty"`
	Event     any    `json:"event"`
}
