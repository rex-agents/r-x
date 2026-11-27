// Package contract is the interface between rox and rix: the guest record rox prints with
// rox inspect --json and passes to rix hooks, and what a hook answers.
//
// rix never opens SSH itself. It reaches guests only through rox exec and reads them
// through rox inspect --json and rox ls --json. rox calls rix hook EVENT NAME at fixed
// points in a guest's life, with the guest record on stdin.
package contract

// Schema is the version of the records below. rox bumps it when a field changes meaning.
const Schema = 1

// Hook events, in the order a launch reaches them.
const (
	// PreFetch: the launcher is about to read the branch. rix answers with an SSH agent for
	// the fetch and keeps it until rox closes the hook's stdin. Name is "-".
	PreFetch = "pre-fetch"
	// PostBoot: the guest is reachable and any agents the project lists are installed.
	PostBoot = "post-boot"
	// PreClone: rix starts the guest's Git agent and answers with its socket.
	PreClone = "pre-clone"
	// PostProvision: projects are set up. rix syncs agent setup and pushes new credentials.
	PostProvision = "post-provision"
	// Resume: after start, reconnect or a service restore.
	Resume = "resume"
	// Down: the guest is going away. rix stops its Git agent and forgets it.
	Down = "down"
)

// Events lists every hook event.
var Events = []string{PreFetch, PostBoot, PreClone, PostProvision, Resume, Down}

// Project is one checkout in a guest.
type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Branch    string `json:"branch"`
	Workspace string `json:"workspace"`
	Phase     string `json:"phase"`
	Session   string `json:"session,omitempty"`
}

// Guest is what rox inspect --json prints and what each hook reads on stdin.
type Guest struct {
	Schema  int    `json:"schema"`
	Name    string `json:"name"`
	Backend string `json:"backend"`
	Engine  string `json:"engine,omitempty"`
	Phase   string `json:"phase"`
	Stopped bool   `json:"stopped"`
	User    string `json:"user"`
	// Home is the guest user's home, and Base is rox's state folder in it.
	Home string `json:"home"`
	Base string `json:"base"`
	// Socket is the guest's Rex server socket, inside the guest.
	Socket   string    `json:"socket"`
	Projects []Project `json:"projects"`
	// Agents are the agent CLIs rox installed, as {agent: version}.
	Agents   map[string]string `json:"agents"`
	Tailnet  string            `json:"tailnet,omitempty"`
	Endpoint string            `json:"endpoint"`
	// Launch carries this launch's choices to the hooks that need them.
	Launch *Launch `json:"launch,omitempty"`
}

// Launch is what rox up or rox add was asked for that rix acts on.
type Launch struct {
	// URL is the repository being fetched or cloned.
	URL string `json:"url,omitempty"`
	// GitKeys are --git-key paths, which replace rix's configured keys for this launch.
	GitKeys []string `json:"git_keys,omitempty"`
	// GitKnownHosts is a --git-known-hosts path for this launch.
	GitKnownHosts string `json:"git_known_hosts,omitempty"`
	// NoGitAgent keeps the Git keys for the clone only, as rox up --no-git-agent asks.
	NoGitAgent bool `json:"no_git_agent,omitempty"`
	// NewAgents are the agents this step installed, which need credentials now.
	NewAgents []string `json:"new_agents,omitempty"`
}

// Reply is what rix hook prints on stdout, as one JSON object.
type Reply struct {
	Schema int `json:"schema"`
	// AgentSocket is an SSH agent socket on the launcher that rox forwards into the guest.
	AgentSocket string `json:"agent_socket,omitempty"`
	// Forward asks rox to keep AgentSocket forwarded for the guest's life, not just the clone.
	Forward bool `json:"forward,omitempty"`
	// KnownHosts are pinned Git server keys, in known_hosts format, for the clone.
	KnownHosts string `json:"known_hosts,omitempty"`
	// Done lines say what the hook did; rox shows each as a finished step.
	Done []string `json:"done,omitempty"`
	// Warnings are problems that didn't stop the hook.
	Warnings []string `json:"warnings,omitempty"`
}

// List is what rox ls --json prints.
type List struct {
	Schema int     `json:"schema"`
	Guests []Entry `json:"guests"`
}

// Entry is one guest in rox ls --json. Host is set when listing other Rex hosts.
type Entry struct {
	Host      string            `json:"host,omitempty"`
	Name      string            `json:"name"`
	Backend   string            `json:"backend"`
	Engine    string            `json:"engine"`
	Phase     string            `json:"phase"`
	IP        string            `json:"ip"`
	Tailnet   string            `json:"tailnet"`
	Projects  string            `json:"projects"`
	Socket    string            `json:"socket"`
	Home      string            `json:"home"`
	User      string            `json:"user"`
	Agents    map[string]string `json:"agents,omitempty"`
	Checkouts []Checkout        `json:"checkouts"`
	Funnels   []FunnelEntry     `json:"funnels"`
	Sessions  map[string]string `json:"sessions"`
}

// Checkout is a project checkout in an Entry.
type Checkout struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Workspace string `json:"workspace"`
}

// FunnelEntry is a public URL in an Entry.
type FunnelEntry struct {
	URL  string `json:"url"`
	Port int    `json:"port"`
}
