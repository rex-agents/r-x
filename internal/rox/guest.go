// Package rox launches Rex guests and the project workspaces inside them.
package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Backends rox can launch guests on.
var Backends = []string{"container", "linux", "macos", "fly", "railway"}

// CloudBackends run guests on a cloud account the launcher pays for.
var CloudBackends = []string{"fly", "railway"}

// Engines run the container backend. "container" is Apple's container CLI, which runs
// each container in its own lightweight VM.
var Engines = []string{"container", "docker", "podman"}

const (
	DefaultContainerImage = "rox:linux"
	ProjectConfig         = ".rox.json"
	LocalConfig           = ".rox.local.json"
	ContainerHome         = "/home/workspace"
)

// NamePattern matches guest names, such as rv-web-3f2a9c1b7d4e.
var NamePattern = regexp.MustCompile(`^rv-[a-z0-9-]{1,24}-[a-f0-9]{12}$`)

var nameInText = `rv-[a-z0-9-]{1,24}-[a-f0-9]{12}`

func isCloud(backend string) bool { return contains(CloudBackends, backend) }

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}

// Error kinds the commands distinguish.
var (
	ErrNotFound = errors.New("no such guest")
	ErrBusy     = errors.New("busy")
)

// StateRoot is rox's state folder.
func StateRoot() string { return filepath.Join(sys.XDG("XDG_STATE_HOME", ".local/state"), "rox") }

var slugChars = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify lowercases value to letters, digits and dashes, at most limit long.
func Slugify(value string, limit int) string {
	s := strings.Trim(slugChars.ReplaceAllString(strings.ToLower(value), "-"), "-")
	if len(s) > limit {
		s = s[:limit]
	}
	return strings.TrimRight(s, "-")
}

// Project is a checkout inside a guest, with the Rex session rooted at it.
type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Branch    string `json:"branch"`
	Workspace string `json:"workspace"`
	SessionID string `json:"session_id"`
	Phase     string `json:"phase"`
}

// Funnel is a guest port that the launcher's Tailscale serves on the internet.
type Funnel struct {
	Port  int    `json:"port"`
	HTTPS int    `json:"https"`
	Path  string `json:"path"`
	Local int    `json:"local"`
	URL   string `json:"url"`
}

// ShellWindow is a Guest tab rox up or rox add opened, for rox down to close.
type ShellWindow struct {
	Session string `json:"session"`
	Window  string `json:"window"`
}

// Guest is rox's record of one guest, stored as guest.json in its state folder.
// The fields and their JSON names match the Python rox's records, which Go rox reads as they are.
type Guest struct {
	Name        string `json:"name"`
	Backend     string `json:"backend"`
	Image       string `json:"image"`
	User        string `json:"user"`
	Engine      string `json:"engine"`
	Identity    string `json:"identity"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	GuestSocket string `json:"guest_socket"`
	// SessionID is the guest's own session, used when it was started without a project.
	SessionID   string `json:"session_id"`
	Registered  bool   `json:"registered"`
	TailnetName string `json:"tailnet_name"`
	Phase       string `json:"phase"`
	ContainerID string `json:"container_id"`
	CPUs        int    `json:"cpus"`
	Memory      string `json:"memory"`
	// Prebuilt means the image already holds a prepared checkout of the first project.
	Prebuilt bool `json:"prebuilt"`
	// Stopped is set by rox stop, so rox and its service leave the guest stopped until rox start.
	Stopped      bool          `json:"stopped"`
	Projects     []*Project    `json:"projects"`
	Funnels      []*Funnel     `json:"funnels"`
	ShellWindows []ShellWindow `json:"shell_windows"`
	// Agents are the agent CLIs rox installed, as {agent: version}.
	Agents map[string]string `json:"agents"`
	// GitKeys is kept from Python rox records, where rox forwarded these keys. rix owns them now.
	GitKeys []string `json:"git_keys"`
	// Fly.io guests: the app, region, the volume that keeps the home directory, and its GB.
	App    string `json:"app"`
	Region string `json:"region"`
	Disk   int    `json:"disk"`
	Volume string `json:"volume"`
	// Railway guests: the project and environment the sandbox runs in.
	RailwayProject     string `json:"railway_project"`
	RailwayEnvironment string `json:"railway_environment"`
	RailwayIdleTimeout *int   `json:"railway_idle_timeout"`

	// dir overrides where the record lives, for build and template guests.
	dir string
}

// NewGuest returns a guest record with Python rox's defaults.
func NewGuest(name, backend, image, user, engine string) *Guest {
	return &Guest{Name: name, Backend: backend, Image: image, User: user, Engine: engine, Port: 22,
		Phase: "starting", Projects: []*Project{}, Funnels: []*Funnel{}, ShellWindows: []ShellWindow{},
		Agents: map[string]string{}, GitKeys: []string{}}
}

// Base is the guest's state folder in the guest, $HOME/.rox/NAME, with the Rex socket inside it.
func (g *Guest) Base() string { return path.Dir(g.GuestSocket) }

// Home is the guest user's home directory.
func (g *Guest) Home() string { return path.Dir(path.Dir(g.Base())) }

// Directory is the guest's state folder on the launcher.
func (g *Guest) Directory() string {
	if g.dir != "" {
		return g.dir
	}
	return filepath.Join(StateRoot(), g.Name)
}

// Runtime holds the guest's sockets. OpenSSH control paths must fit in a Unix socket path.
func (g *Guest) Runtime() string {
	id := g.Name
	if i := strings.LastIndexByte(id, '-'); i >= 0 {
		id = id[i+1:]
	}
	return filepath.Join(fmt.Sprintf("/tmp/rox-%d", os.Getuid()), id)
}

// Endpoint is the address other devices reach the guest at, its tailnet name when it has one.
func (g *Guest) Endpoint() string { return g.TailnetName }

// KnownHosts pins the guest's SSH host key.
func (g *Guest) KnownHosts() string { return filepath.Join(g.Directory(), "known_hosts") }

// Log is a log file in the guest's state folder.
func (g *Guest) Log(name string) string { return filepath.Join(g.Directory(), name) }

// AddProject adds a checkout record with an id unique in the guest.
func (g *Guest) AddProject(name, url, branch string) *Project {
	slug := Slugify(name, 40)
	if slug == "" {
		slug = "project"
	}
	taken := map[string]bool{}
	for _, p := range g.Projects {
		taken[p.ID] = true
	}
	id := slug
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", slug, n)
	}
	p := &Project{ID: id, Name: name, URL: url, Branch: branch, Phase: "cloning"}
	g.Projects = append(g.Projects, p)
	return p
}

// FindProject finds a project by id, then by name.
func (g *Guest) FindProject(key string) (*Project, error) {
	var matches []*Project
	for _, p := range g.Projects {
		if p.ID == key {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		for _, p := range g.Projects {
			if p.Name == key {
				matches = append(matches, p)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	var ids []string
	for _, p := range g.Projects {
		ids = append(ids, p.ID)
	}
	choices := strings.Join(ids, ", ")
	if choices == "" {
		choices = "none"
	}
	problem := "is not a project"
	if len(matches) > 1 {
		problem = "matches several projects"
	}
	return nil, fmt.Errorf("%q %s on %s. Projects: %s", key, problem, g.Name, choices)
}

// Save writes the record atomically.
func (g *Guest) Save() error {
	if g.Projects == nil {
		g.Projects = []*Project{}
	}
	data, err := sys.MarshalIndent(g)
	if err != nil {
		return err
	}
	return sys.WriteAtomic(filepath.Join(g.Directory(), "guest.json"), data, 0o600, true)
}

// MustSave saves, reporting a failure without stopping the command.
func (g *Guest) MustSave() {
	if err := g.Save(); err != nil {
		ui.Warn("Could not save %s's record: %v", g.Name, err)
	}
}

// LoadGuest reads a guest's record, migrating older layouts.
func LoadGuest(name string) (*Guest, error) {
	if !NamePattern.MatchString(name) {
		return nil, fmt.Errorf("Invalid workspace name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(StateRoot(), name, "guest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("No managed workspace named %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return parseGuest(name, data)
}

func parseGuest(name string, data []byte) (*Guest, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("Invalid workspace state for %s: %w", name, err)
	}
	if raw["name"] != name || !contains(Backends, fmt.Sprint(raw["backend"])) {
		return nil, fmt.Errorf("Invalid workspace state for %s", name)
	}
	delete(raw, "register_host") // Older launches could opt out of registration.
	if _, ok := raw["projects"]; !ok {
		// Older records held exactly one project, and its session was the guest's.
		project, _ := raw["project"].(string)
		branch, _ := raw["branch"].(string)
		workspace, _ := raw["workspace"].(string)
		session, _ := raw["session_id"].(string)
		phase, _ := raw["phase"].(string)
		if phase == "" {
			phase = "starting"
		}
		for _, key := range []string{"project", "branch", "workspace", "session_id"} {
			delete(raw, key)
		}
		projects := []any{}
		if project != "" {
			id := Slugify(project, 40)
			if id == "" {
				id = "project"
			}
			projects = append(projects, map[string]any{"id": id, "name": project, "url": "", "branch": branch,
				"workspace": workspace, "session_id": session, "phase": phase})
		}
		raw["projects"] = projects
	}
	migrated, _ := json.Marshal(raw)
	g := NewGuest("", "", "", "", "")
	if err := json.Unmarshal(migrated, g); err != nil {
		return nil, fmt.Errorf("Invalid workspace state for %s: %w", name, err)
	}
	if g.Agents == nil {
		g.Agents = map[string]string{}
	}
	return g, nil
}

// Lock holds a guest's lock so only one command changes it at a time.
func lockGuest(g *Guest, wait bool) (*sys.Lock, error) {
	file := filepath.Join(g.Directory(), "lock")
	l, err := sys.TryLock(file)
	if errors.Is(err, sys.ErrBusy) {
		if !wait {
			return nil, fmt.Errorf("Another command is working on %s: %w", g.Name, ErrBusy)
		}
		ui.Info("Waiting for another command working on %s", g.Name)
		return sys.WaitLock(file)
	}
	return l, err
}

// withGuest loads a guest, takes its lock, reloads it and runs fn.
func withGuest(name string, wait bool, fn func(g *Guest) error) error {
	g, err := LoadGuest(name)
	if err != nil {
		return err
	}
	l, err := lockGuest(g, wait)
	if err != nil {
		return err
	}
	defer l.Unlock()
	if g, err = LoadGuest(name); err != nil {
		return err
	}
	return fn(g)
}

// LocalGuests are this machine's guests, skipping records that can't be read.
func LocalGuests() []*Guest {
	matches, _ := filepath.Glob(filepath.Join(StateRoot(), "*", "guest.json"))
	sort.Strings(matches)
	var guests []*Guest
	for _, m := range matches {
		if g, err := LoadGuest(filepath.Base(filepath.Dir(m))); err == nil {
			guests = append(guests, g)
		}
	}
	return guests
}

// IsLocal reports whether this machine runs the guest.
func IsLocal(name string) bool {
	_, err := LoadGuest(name)
	return err == nil || !errors.Is(err, ErrNotFound)
}

// Record is the guest as the contract describes it, for rox inspect and hooks.
func (g *Guest) Record() contract.Guest {
	r := contract.Guest{Schema: contract.Schema, Name: g.Name, Backend: g.Backend, Engine: g.Engine,
		Phase: g.Phase, Stopped: g.Stopped, User: g.User, Socket: g.GuestSocket, Agents: map[string]string{},
		Tailnet: g.TailnetName, Endpoint: g.Endpoint(), Projects: []contract.Project{}, Session: g.SessionID}
	if g.Name != "" {
		r.Dir, r.Runtime = g.Directory(), g.Runtime()
	}
	if g.GuestSocket != "" {
		r.Home, r.Base = g.Home(), g.Base()
	}
	for agent, version := range g.Agents {
		r.Agents[agent] = version
	}
	for _, p := range g.Projects {
		r.Projects = append(r.Projects, contract.Project{ID: p.ID, Name: p.Name, URL: p.URL, Branch: p.Branch,
			Workspace: p.Workspace, Phase: p.Phase, Session: p.SessionID})
	}
	return r
}

// ProjectSummary is "id:branch" for each project, for listings.
func (g *Guest) ProjectSummary() string {
	var entries []string
	for _, p := range g.Projects {
		entry := p.ID + ":" + p.Branch
		if p.Phase != "ready" {
			entry += " (" + p.Phase + ")"
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return "-"
	}
	return strings.Join(entries, ", ")
}

// Session is the guest's Rex session for a project, its only project, or the guest itself.
func (g *Guest) Session(key string) (string, error) {
	if key != "" {
		p, err := g.FindProject(key)
		if err != nil {
			return "", err
		}
		return p.SessionID, nil
	}
	if len(g.Projects) == 1 {
		return g.Projects[0].SessionID, nil
	}
	if g.SessionID != "" || len(g.Projects) == 0 {
		return g.SessionID, nil
	}
	return g.Projects[0].SessionID, nil
}
