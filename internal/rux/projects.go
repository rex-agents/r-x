package rux

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A project groups threads, usually one per repo. It holds the defaults for
// new threads and notes that every thread in it gets in its system prompt.
// Blank threads belong to the "scratch" project.
type project struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Dir is a checkout on the authority named in Authority: threads get a copy of it
	// (rox up --local) instead of a clone of Repo, which is then only for display.
	Dir       string `json:"dir,omitempty"`
	Authority string `json:"authority,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Archived  bool   `json:"archived,omitempty"`
	// Host tools turned on for the project, the ones that run without asking,
	// and per-tool settings.
	Tools        []string                     `json:"tools,omitempty"`
	ToolsAllowed []string                     `json:"tools_allowed,omitempty"`
	ToolSettings map[string]map[string]string `json:"tool_settings,omitempty"`
	Created      time.Time                    `json:"created"`
	Updated      time.Time                    `json:"updated"`
}

type projectView struct {
	project
	Threads         int       `json:"threads"`  // not removed
	Active          int       `json:"active"`   // running, or being set up
	Activity        time.Time `json:"activity"` // latest thread update, or the project's own
	ArchivedThreads int       `json:"archived_threads"`
	Manager         string    `json:"manager,omitempty"` // the live manager thread's ID
	ManagerStatus   string    `json:"manager_status,omitempty"`
	ManagerReplied  time.Time `json:"manager_replied,omitempty"`
}

const scratchID = "scratch"

var ghRepoURL = regexp.MustCompile(`^https://github\.com/([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// projectIDFor maps a (normalized) repo URL to its project ID.
func projectIDFor(repo string) string {
	if repo == "" {
		return scratchID
	}
	if m := ghRepoURL.FindStringSubmatch(repo); m != nil {
		return slug(m[1] + "-" + m[2])
	}
	return slug(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "www."), ".git"))
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	if out == "" {
		out = "project"
	}
	return out
}

func repoDisplayName(repo string) string {
	if m := ghRepoURL.FindStringSubmatch(repo); m != nil {
		return m[1] + "/" + m[2]
	}
	if m := ghSSH.FindStringSubmatch(repo); m != nil {
		return m[1]
	}
	return strings.TrimSuffix(strings.TrimPrefix(repo, "https://"), ".git")
}

func (s *server) projectsDir() string { return filepath.Join(s.dataDir, "projects") }

func (s *server) saveProjectLocked(p *project) error {
	if err := os.MkdirAll(s.projectsDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	tmp := filepath.Join(s.projectsDir(), p.ID+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.projectsDir(), p.ID+".json"))
}

// loadProjects reads projects and puts every thread into one, creating
// projects for threads made before projects existed.
func (s *server) loadProjects() error {
	s.projects = map[string]*project{}
	ents, err := os.ReadDir(s.projectsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.projectsDir(), e.Name()))
		var p project
		if err != nil || json.Unmarshal(b, &p) != nil || p.ID == "" {
			continue
		}
		s.projects[p.ID] = &p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureProjectLocked("") // scratch always exists
	for _, t := range s.threads {
		t.mu.Lock()
		if t.meta.Project == "" || s.projects[t.meta.Project] == nil {
			t.meta.Project = s.ensureProjectLocked(t.meta.Repo).ID
			t.saveMeta()
		}
		t.mu.Unlock()
	}
	return nil
}

// ensureProjectLocked returns the project for repo, creating it if needed.
func (s *server) ensureProjectLocked(repo string) *project {
	id := projectIDFor(repo)
	if p := s.projects[id]; p != nil {
		return p
	}
	now := time.Now()
	p := &project{ID: id, Name: "scratch", Repo: repo, Created: now, Updated: now}
	if repo != "" {
		p.Name = repoDisplayName(repo)
	}
	s.projects[id] = p
	s.saveProjectLocked(p)
	return p
}

func (s *server) projectLocked(id string) *project { return s.projects[id] }

func (s *server) projectViews() []projectView {
	s.mu.Lock()
	views := map[string]*projectView{}
	for id, p := range s.projects {
		views[id] = &projectView{project: *p, Activity: p.Updated}
	}
	ts := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	for _, t := range ts {
		m := t.view()
		v := views[m.Project]
		if m.Archived && v != nil && m.Kind == "" {
			v.ArchivedThreads++
			continue
		}
		if v == nil || m.Status == "destroyed" || m.Status == "destroying" {
			continue
		}
		if m.Kind == "manager" {
			if v.Manager == "" {
				v.Manager, v.ManagerStatus, v.ManagerReplied = m.ID, m.Status, m.Replied
				if !m.Connected && (m.Status == "idle" || m.Status == "running") {
					v.ManagerStatus = "offline"
				}
			}
			continue
		}
		v.Threads++
		switch m.Status {
		case "running", "provisioning", "starting":
			v.Active++
		}
		if m.Updated.After(v.Activity) {
			v.Activity = m.Updated
		}
	}
	out := make([]projectView, 0, len(views))
	for _, v := range views {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Activity.After(out[j].Activity) })
	return out
}

func (s *server) handleProjects(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, 200, s.projectViews())
}

func (s *server) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	for _, v := range s.projectViews() {
		if v.ID == r.PathValue("id") {
			writeJSONResp(w, 200, v)
			return
		}
	}
	httpErr(w, 404, "no such project")
}

// handleProjectCreate makes (or returns) the project for a repo, or for a directory on this
// machine ({"dir": PATH}), which only the local socket and loopback may ask for.
func (s *server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var req struct{ Repo, Dir string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "bad JSON")
		return
	}
	if strings.TrimSpace(req.Dir) != "" {
		if !localOnly(r) {
			httpErr(w, 403, "projects from a local directory can only be added from this machine (the rux TUI or loopback), not over the tailnet")
			return
		}
		if strings.TrimSpace(req.Repo) != "" {
			httpErr(w, 400, "give repo or dir, not both")
			return
		}
		p, err := s.ensureDirProject(req.Dir)
		if err != nil {
			httpErr(w, errorCode(err), err.Error())
			return
		}
		writeJSONResp(w, 200, p)
		return
	}
	repo := strings.TrimSpace(req.Repo)
	if repo != "" {
		var err error
		if repo, err = normalizeRepo(repo); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
	}
	s.mu.Lock()
	p := *s.ensureProjectLocked(repo)
	s.mu.Unlock()
	writeJSONResp(w, 200, p)
}

// handleProjectUpdate changes a project's name, thread defaults or notes.
// Notes go to the project's connected threads; claude picks them up the next
// time it starts.
func (s *server) handleProjectUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name, Branch, Model, Effort, Notes *string
		Tools                              *[]string
		ToolsAllowed                       *[]string                    `json:"tools_allowed"`
		ToolSettings                       map[string]map[string]string `json:"tool_settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, "bad JSON")
		return
	}
	s.mu.Lock()
	p := s.projects[r.PathValue("id")]
	if p == nil {
		s.mu.Unlock()
		httpErr(w, 404, "no such project")
		return
	}
	next := *p
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		next.Name = strings.TrimSpace(*req.Name)
	}
	if req.Branch != nil {
		next.Branch = strings.TrimSpace(*req.Branch)
	}
	if req.Model != nil || req.Effort != nil {
		m, e := next.Model, next.Effort
		if req.Model != nil {
			m = *req.Model
		}
		if req.Effort != nil {
			e = *req.Effort
		}
		var err error
		if next.Model, next.Effort, err = validSettings(m, e); err != nil {
			s.mu.Unlock()
			httpErr(w, 400, err.Error())
			return
		}
	}
	if req.Notes != nil {
		if len(*req.Notes) > 20000 {
			s.mu.Unlock()
			httpErr(w, 400, "notes are limited to 20,000 characters")
			return
		}
		next.Notes = *req.Notes
	}
	if req.Tools != nil {
		next.Tools = *req.Tools
	}
	if req.ToolsAllowed != nil {
		next.ToolsAllowed = *req.ToolsAllowed
	}
	if req.ToolSettings != nil {
		next.ToolSettings = req.ToolSettings
	}
	if strings.HasPrefix(next.Branch, "-") || strings.ContainsAny(next.Branch, " \t\n~^:?*[\\") {
		s.mu.Unlock()
		httpErr(w, 400, "invalid branch")
		return
	}
	notesChanged := next.Notes != p.Notes
	next.Updated = time.Now()
	*p = next
	s.saveProjectLocked(p)
	var ts []*thread
	for _, t := range s.threads {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	if notesChanged {
		for _, t := range ts {
			t.mu.Lock()
			if t.meta.Project == next.ID && t.send != nil {
				select {
				case t.send <- s.settingsMsg(&t.meta):
				default:
				}
			}
			t.mu.Unlock()
		}
	}
	writeJSONResp(w, 200, next)
}

func (s *server) projectName(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.projects[id]; p != nil {
		return p.Name
	}
	return ""
}

// projectNotes returns a project's notes; safe to call with a thread lock held.
func (s *server) projectNotes(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.projects[id]; p != nil {
		return p.Notes
	}
	return ""
}
