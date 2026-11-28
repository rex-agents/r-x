package authtui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// The PACK tab's columns: the leader, the projects' agents, the selected project's threads, and
// (only while it is stepped into) a thread's sub-agents.
const (
	colLeader = iota
	colProject
	colThread
	colSub
	numCols
)

// The MONITOR tab's columns.
const (
	mcClient = iota
	mcHost
	mcGuest
	numMCols
)

var colTitles = [numCols]string{"LEADER", "PROJECT AGENTS", "THREADS", "SUB-AGENTS"}
var monitorTitles = [numMCols]string{"CLIENTS", "HOSTS", "GUESTS"}

const scratchName = "scratch"

// node is one card.
type node struct {
	kind   int
	key    string // identity within its column
	name   string
	status string    // colors the dot
	body   [3]string // the card's rows under its name
	spark  []string  // thread ids whose activity the card sums
	// A card stands for one of these.
	th      *authapi.ThreadSummary // a thread; a project's manager
	sub     *authapi.SubAgentSummary
	host    *authapi.AuthorityInfo
	client  *authapi.ClientInfo
	guest   *authapi.GuestInfo
	project string // for a project card: the project id ("" for scratch)
	scratch bool
	noMgr   bool // a project with threads but no manager
	// project card figures
	workers, working, threads int
	cost                      float64
}

func isWorking(s string) bool { return s == "running" || s == "working" }

// numCols is how many PACK columns can be focused: the sub-agents column only exists while
// it is stepped into.
func (m *Model) packCols() int {
	if m.focus >= colSub {
		return numCols
	}
	return numCols - 1
}

func (m *Model) visible(t authapi.ThreadSummary) bool {
	return m.archived || (t.Status != "archived" && !t.Archived)
}

func (m *Model) allThreads() []authapi.ThreadSummary {
	var out []authapi.ThreadSummary
	for _, a := range m.pack.Authorities {
		out = append(out, a.Threads...)
	}
	return out
}

func (m *Model) projectName(id string) string {
	if id == "" {
		return scratchName
	}
	if p, ok := m.projects[id]; ok && p.Name != "" {
		return p.Name
	}
	return id
}

// projectGroup is a project's manager and its other threads.
type projectGroup struct {
	id      string
	mgr     *authapi.ThreadSummary
	threads []*authapi.ThreadSummary // everything but the manager, newest first
}

// groups sorts every thread in the pack into its project: the manager apart, the rest newest
// first. Scratch threads (no project) go last.
func (m *Model) groups() []*projectGroup {
	by := map[string]*projectGroup{}
	get := func(id string) *projectGroup {
		g := by[id]
		if g == nil {
			g = &projectGroup{id: id}
			by[id] = g
		}
		return g
	}
	var ids []string
	for i := range m.pack.Authorities {
		for j := range m.pack.Authorities[i].Threads {
			t := &m.pack.Authorities[i].Threads[j]
			if !m.visible(*t) {
				continue
			}
			g := get(t.Project)
			if t.Kind == "manager" && t.Project != "" {
				g.mgr = t
			} else {
				g.threads = append(g.threads, t)
			}
		}
	}
	// Projects without threads yet, such as one just added, are listed too.
	for id, p := range m.projects {
		if by[id] == nil && id != "" && id != "scratch" && !p.Closed {
			get(id)
		}
	}
	for id := range by {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if (ids[i] == "") != (ids[j] == "") {
			return ids[j] == "" // scratch last
		}
		return m.projectName(ids[i]) < m.projectName(ids[j])
	})
	out := make([]*projectGroup, len(ids))
	for i, id := range ids {
		g := by[id]
		sort.SliceStable(g.threads, func(a, b int) bool { return g.threads[a].Updated > g.threads[b].Updated })
		out[i] = g
	}
	return out
}

func (m *Model) group(project string) *projectGroup {
	for _, g := range m.groups() {
		if g.id == project {
			return g
		}
	}
	return nil
}

// column returns the nodes of PACK column c, given the selections to its left.
func (m *Model) column(c int) []node {
	switch c {
	case colLeader:
		return m.leaderNodes()
	case colProject:
		var out []node
		for _, g := range m.groups() {
			out = append(out, m.projectNode(g))
		}
		return out
	case colThread:
		p := m.selected(colProject)
		if p == nil {
			return nil
		}
		g := m.group(p.project)
		if g == nil {
			return nil
		}
		var out []node
		for _, t := range g.threads {
			where := cmpOr(t.Authority, "?")
			if t.Guest != "" {
				where += " · " + t.Guest
			} else if t.Placement.Local {
				where += " · local"
			}
			out = append(out, node{kind: colThread, key: t.ID, name: cmpOr(t.Title, t.ID), th: t, status: t.Status, spark: []string{t.ID},
				body: [3]string{t.Status, where, plural(len(t.SubAgents), "sub-agent")}})
		}
		return out
	case colSub:
		t := m.selected(colThread)
		if t == nil {
			return nil
		}
		var out []node
		for i := range t.th.SubAgents {
			s := &t.th.SubAgents[i]
			out = append(out, node{kind: colSub, key: s.ID, name: cmpOr(s.Name, s.ID), sub: s, status: s.Status,
				body: [3]string{s.Status, "on " + cmpOr(s.Authority, "this host"), cmpOr(s.Guest, "no guest")}})
		}
		return out
	}
	return nil
}

func (m *Model) leaderNodes() []node {
	if !m.havePack {
		return nil
	}
	up, work, total := 0, 0, 0
	for _, a := range m.pack.Authorities {
		if a.Status == "up" {
			up++
		}
		for _, t := range a.Threads {
			if !m.visible(t) {
				continue
			}
			total++
			if isWorking(t.Status) {
				work++
			}
		}
	}
	name, st := m.pack.Leader, "down"
	for _, a := range m.pack.Authorities {
		if a.Name == name {
			st = a.Status
		}
	}
	if name == "" {
		name, st = "no leader", "down"
	}
	vault := "vault ok"
	if !m.pack.VaultOK {
		vault = "vault unreachable"
	}
	n := node{kind: colLeader, key: name, name: name, status: st,
		body: [3]string{fmt.Sprintf("term %d · %s", m.pack.Term, vault), fmt.Sprintf("%d/%d authorities up", up, len(m.pack.Authorities)),
			fmt.Sprintf("%d/%d threads working", work, total)}}
	for _, t := range m.allThreads() {
		n.spark = append(n.spark, t.ID)
	}
	return []node{n}
}

func (m *Model) projectNode(g *projectGroup) node {
	n := node{kind: colProject, key: g.id, project: g.id, name: m.projectName(g.id), scratch: g.id == "", th: g.mgr, noMgr: g.mgr == nil}
	n.threads = len(g.threads)
	for _, t := range g.threads {
		n.cost += t.Cost
		n.spark = append(n.spark, t.ID)
		if g.mgr != nil && t.Parent == g.mgr.ID {
			n.workers++
			if isWorking(t.Status) {
				n.working++
			}
		}
	}
	if g.mgr != nil {
		n.cost += g.mgr.Cost
		n.spark = append(n.spark, g.mgr.ID)
	}
	cost := fmt.Sprintf("$%.2f · %s", n.cost, plural(n.threads, "thread"))
	switch {
	case g.mgr != nil:
		n.status = g.mgr.Status
		n.body = [3]string{"manager " + g.mgr.Status, fmt.Sprintf("%d/%d workers working", n.working, n.workers), cost}
	case n.scratch:
		for _, t := range g.threads {
			if isWorking(t.Status) {
				n.working++
			}
		}
		n.status = "idle"
		if n.working > 0 {
			n.status = "running"
		}
		n.body = [3]string{"no project", fmt.Sprintf("%d/%d threads working", n.working, n.threads), cost}
	default:
		n.status = "none"
		n.body = [3]string{"no manager yet", "enter starts one", cost}
		if p, ok := m.projects[g.id]; ok && p.Dir != "" {
			n.body[1] = tailFit(tildePath(p.Dir, m.home), cardW-4)
		}
	}
	return n
}

// selected is the selected node of PACK column c, or nil.
func (m *Model) selected(c int) *node {
	for i := 0; i < c; i++ {
		if len(m.column(i)) == 0 {
			return nil
		}
	}
	col := m.column(c)
	if len(col) == 0 {
		return nil
	}
	i := min(max(m.sel[c], 0), len(col)-1)
	return &col[i]
}

// current is the node under the cursor: the selection of the focused column.
func (m *Model) current() *node { return m.selected(m.focus) }

func (m *Model) clamp() {
	for c := 0; c < numCols; c++ {
		n := len(m.column(c))
		m.sel[c] = min(max(m.sel[c], 0), max(n-1, 0))
	}
	for m.focus > 0 && m.selected(m.focus) == nil {
		m.focus--
	}
	for c := 0; c < numMCols; c++ {
		m.msel[c] = min(max(m.msel[c], 0), max(len(m.monitorColumn(c))-1, 0))
	}
}

// threadByID finds a thread anywhere in the pack.
func (m *Model) threadByID(id string) *authapi.ThreadSummary {
	for i := range m.pack.Authorities {
		for j := range m.pack.Authorities[i].Threads {
			if t := &m.pack.Authorities[i].Threads[j]; t.ID == id {
				return t
			}
		}
	}
	return nil
}

// previewThread is the thread whose events the preview shows: the selected thread, a project's
// manager, or the parent of a selected sub-agent.
func (m *Model) previewThread() string {
	if m.tab == tabMonitor {
		return ""
	}
	if n := m.current(); n != nil {
		switch {
		case n.th != nil:
			return n.th.ID
		case n.kind == colSub:
			if t := m.selected(colThread); t != nil {
				return t.th.ID
			}
		}
	}
	return ""
}

// ---- monitor ----

// monitorColumn returns the nodes of MONITOR column c.
func (m *Model) monitorColumn(c int) []node {
	now := m.opts.Now()
	switch c {
	case mcClient:
		var out []node
		for i := range m.pack.Clients {
			cl := &m.pack.Clients[i]
			since := ""
			if t, err := time.Parse(time.RFC3339, cl.Since); err == nil {
				since = "connected " + uptime(now.Sub(t))
			}
			out = append(out, node{kind: mcClient, key: cl.ID, name: cmpOr(cl.Device, "unknown"), client: cl, status: "up",
				body: [3]string{cl.Kind + " · via " + cmpOr(cl.Authority, "?"), "seen " + ago(now, cl.LastSeen), since}})
		}
		return out
	case mcHost:
		var out []node
		for i := range m.pack.Authorities {
			a := &m.pack.Authorities[i]
			name := a.Name
			role := ""
			if a.Name == m.pack.Leader {
				name += " ★"
				role = "leader"
			}
			if a.Name == m.pack.VaultHost {
				role = strings.TrimPrefix(role+" vault", " ")
			}
			work := 0
			for _, t := range a.Threads {
				if !m.visible(t) {
					continue
				}
				if isWorking(t.Status) {
					work++
				}
			}
			up := ""
			if t, err := time.Parse(time.RFC3339, a.Started); err == nil {
				up = "up " + uptime(now.Sub(t))
			}
			n := node{kind: mcHost, key: a.Name, name: name, host: a, status: a.Status}
			if a.Status == "up" {
				n.body = [3]string{fmt.Sprintf("%s · %d cpu · load %.2f", cmpOr(a.Host.OS, "?"), a.Host.CPUs, a.Host.Load),
					fmt.Sprintf("guests %d/%d · %d working", a.Host.GuestsUsed, a.Host.GuestsMax, work),
					strings.TrimSpace(strings.Join(filled(up, role), " · "))}
			} else {
				n.body = [3]string{a.Status + " · unreachable", cmpOr(a.Host.OS, "?"), role}
			}
			out = append(out, n)
		}
		return out
	case mcGuest:
		var hosts []*authapi.AuthorityInfo
		all := m.mfocus == mcClient
		for i := range m.pack.Authorities {
			a := &m.pack.Authorities[i]
			if all || i == min(max(m.msel[mcHost], 0), len(m.pack.Authorities)-1) {
				hosts = append(hosts, a)
			}
		}
		var out []node
		for _, a := range hosts {
			for i := range a.Guests {
				g := &a.Guests[i]
				thread := "no thread"
				if g.Thread != "" {
					thread = g.Thread
					if t := m.threadByID(g.Thread); t != nil {
						thread = cmpOr(t.Title, t.ID)
					}
				}
				kind := strings.TrimSpace(strings.Join(filled(g.Backend, g.Phase), " · "))
				if all {
					kind = a.Name + " · " + kind
				}
				mem := humanBytes(g.MemBytes)
				if g.MemLimit > 0 {
					mem += "/" + humanBytes(g.MemLimit)
				}
				out = append(out, node{kind: mcGuest, key: a.Name + "/" + g.Name, name: g.Name, guest: g, status: g.Phase,
					body: [3]string{kind, fmt.Sprintf("cpu %.0f%% · mem %s", g.CPU, mem), thread}})
			}
		}
		return out
	}
	return nil
}

func filled(s ...string) []string {
	var out []string
	for _, x := range s {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func ago(now time.Time, ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "never"
	}
	d := now.Sub(t)
	if d < 5*time.Second {
		return "just now"
	}
	return uptime(d) + " ago"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%dK", n>>10)
}

func (m *Model) mselected(c int) *node {
	col := m.monitorColumn(c)
	if len(col) == 0 {
		return nil
	}
	return &col[min(max(m.msel[c], 0), len(col)-1)]
}
