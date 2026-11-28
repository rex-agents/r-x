package rux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Projects from a local directory: a git checkout on the authority's own machine. Threads
// get a copy of it in their guests (rox up --local, run inside the checkout), including
// unpushed commits and uncommitted files. Only the local socket and loopback may add one or
// browse for one, never the tailnet web app.

// localOnly reports whether a request came from this machine itself: over the authority's
// unix socket, or straight from loopback. A request that went through `tailscale serve` (or
// any proxy that says so) comes from loopback too, but carries proxy headers, and is refused.
func localOnly(r *http.Request) bool {
	if onSock, _ := r.Context().Value(unixConnKey).(bool); onSock {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	return r.Header.Get("Tailscale-User-Login") == "" && r.Header.Get("X-Forwarded-For") == "" &&
		r.Header.Get("Forwarded") == "" && r.Header.Get("Tailscale-Headers-Info") == ""
}

// userHome is the user's home directory with symlinks resolved, the root of what rux
// browses and accepts for a local project.
func userHome() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "", errors.New("can't find the home directory")
	}
	if r, err := filepath.EvalSymlinks(h); err == nil {
		h = r
	}
	return filepath.Clean(h), nil
}

func underDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveUnderHome makes p absolute (a leading ~ is the home directory), resolves symlinks and
// checks that the result is a directory inside the home directory.
func resolveUnderHome(p string) (string, error) {
	home, err := userHome()
	if err != nil {
		return "", &reqError{500, err.Error()}
	}
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", &reqError{400, "the path must be absolute"}
	}
	abs, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", &reqError{404, p + " doesn't exist"}
		}
		return "", &reqError{400, err.Error()}
	}
	if !underDir(home, abs) {
		return "", &reqError{403, "only directories inside your home directory (" + home + ") are allowed"}
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", &reqError{400, err.Error()}
	}
	if !st.IsDir() {
		return "", &reqError{400, abs + " isn't a directory"}
	}
	return abs, nil
}

func gitOut(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// checkedDir is a validated local project directory.
type checkedDir struct {
	Path string // absolute, symlinks resolved; the checkout's root
	Repo string // the GitHub origin as an https URL, "" if it isn't one
}

// checkProjectDir checks that dir can be a project: inside the home directory, the root of a
// git checkout, with a commit, on a branch and with a remote for the guest to clone from.
// It never changes the directory.
func checkProjectDir(dir string) (*checkedDir, error) {
	abs, err := resolveUnderHome(dir)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, &reqError{500, "git isn't installed on this machine"}
	}
	top, err := gitOut(abs, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return nil, &reqError{400, abs + " is not a git repository; run git init and commit first"}
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	if filepath.Clean(top) != abs {
		return nil, &reqError{400, abs + " is inside the checkout " + top + "; add that directory instead"}
	}
	if _, err := gitOut(abs, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return nil, &reqError{400, abs + " has no commits yet; commit something first"}
	}
	branch, _ := gitOut(abs, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branch == "" {
		return nil, &reqError{400, abs + " has a detached HEAD; check out a branch first"}
	}
	// rox clones the checkout's remote in the guest and then copies the local work over it:
	// the branch's upstream remote, else origin.
	remote, _ := gitOut(abs, "config", "--get", "branch."+branch+".remote")
	if remote == "" || remote == "." {
		remote = "origin"
	}
	url, err := gitOut(abs, "remote", "get-url", remote)
	if err != nil || url == "" {
		return nil, &reqError{400, abs + " has no git remote (" + remote + "); the guest clones it and gets your local work on top. Add one with git remote add origin URL"}
	}
	c := &checkedDir{Path: abs}
	if repo, err := normalizeRepo(url); err == nil && ghRepoURL.MatchString(repo) {
		c.Repo = repo
	}
	return c, nil
}

// ensureDirProject returns the project for a local directory, creating it if needed. Its ID is
// the directory's name, made unique among the existing projects.
func (s *server) ensureDirProject(dir string) (*project, error) {
	c, err := checkProjectDir(dir)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.projects {
		if p.Dir == c.Path && (p.Authority == "" || p.Authority == s.authName()) {
			cp := *p
			return &cp, nil
		}
	}
	base := slug(filepath.Base(c.Path))
	id := base
	for n := 2; s.projects[id] != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	now := time.Now()
	p := &project{ID: id, Name: filepath.Base(c.Path), Repo: c.Repo, Dir: c.Path, Authority: s.authName(), Created: now, Updated: now}
	s.projects[id] = p
	if err := s.saveProjectLocked(p); err != nil {
		delete(s.projects, id)
		return nil, err
	}
	cp := *p
	return &cp, nil
}

var guestDirChars = regexp.MustCompile(`[^a-z0-9]+`)

// guestCheckoutDir is where rox puts a --local checkout in a fresh guest: the home folder, named
// after the checkout (rox's own slug of it, at most 40 characters).
func guestCheckoutDir(dir string) string {
	n := strings.Trim(guestDirChars.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
	if len(n) > 40 {
		n = n[:40]
	}
	if n = strings.TrimRight(n, "-"); n == "" {
		n = "project"
	}
	return `$HOME/` + n
}

// ---- browsing ----

const maxDirEntries = 500

// dirEntry is a subdirectory in a /api/fs/dirs listing.
type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
}

// listDirs lists p's subdirectories (p inside the home directory): dot-directories only with
// hidden, symlinks only when they lead to a directory inside the home directory.
func listDirs(p string, hidden bool) (abs string, dirs []dirEntry, truncated bool, err error) {
	if abs, err = resolveUnderHome(p); err != nil {
		return "", nil, false, err
	}
	home, _ := userHome()
	ents, err := os.ReadDir(abs)
	if err != nil {
		return "", nil, false, &reqError{403, err.Error()}
	}
	dirs = []dirEntry{}
	for _, e := range ents {
		name := e.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(abs, name)
		if e.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(full)
			if err != nil || !underDir(home, target) {
				continue
			}
			if st, err := os.Stat(target); err != nil || !st.IsDir() {
				continue
			}
		} else if !e.IsDir() {
			continue
		}
		if len(dirs) >= maxDirEntries {
			return abs, dirs, true, nil
		}
		_, gerr := os.Stat(filepath.Join(full, ".git"))
		dirs = append(dirs, dirEntry{Name: name, Path: full, Git: gerr == nil})
	}
	return abs, dirs, false, nil
}

// handleFsDirs is GET /api/fs/dirs?path=P&hidden=1, for the TUI's directory picker.
func (s *server) handleFsDirs(w http.ResponseWriter, r *http.Request) {
	if !localOnly(r) {
		httpErr(w, 403, "browsing directories is only allowed from this machine, not over the tailnet")
		return
	}
	home, err := userHome()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	q := r.URL.Query()
	path := cmpOr(strings.TrimSpace(q.Get("path")), home)
	abs, dirs, truncated, err := listDirs(path, q.Get("hidden") == "1")
	if err != nil {
		httpErr(w, errorCode(err), err.Error())
		return
	}
	parent := ""
	if abs != home {
		parent = filepath.Dir(abs)
	}
	writeJSONResp(w, 200, map[string]any{"path": abs, "parent": parent, "home": home, "git": fileExists(filepath.Join(abs, ".git")),
		"dirs": dirs, "truncated": truncated})
}

// checkDirProjectPlacement refuses what a local-directory project can't do: run on another
// authority (the directory only exists on the one that owns it), in a guest that already
// exists, or on a remote runtime (fly, railway), none of which can take a copy of the checkout.
func (s *server) checkDirProjectPlacement(proj *project, req *createReq) error {
	if proj.Authority != "" && proj.Authority != s.authName() {
		return &reqError{400, fmt.Sprintf("project %s is a directory on authority %s; its threads run there", proj.ID, proj.Authority)}
	}
	if a := strings.TrimSpace(req.Placement.Authority); a != "" && a != s.authName() {
		return &reqError{400, fmt.Sprintf("project %s is a directory on authority %s; it can't be placed on %s", proj.ID, s.authName(), a)}
	}
	if req.Guest != "" {
		return &reqError{400, "project " + proj.ID + " is a local directory: its threads each get a copy of it in a new guest, not an existing one"}
	}
	if remoteRuntime(req.Runtime) {
		return &reqError{400, "project " + proj.ID + " is a local directory, and " + req.Runtime + " guests can't take a copy of it; use container, linux or macos"}
	}
	return nil
}

// dirProjectOwned reports whether id is a local-directory project of this authority: its
// threads are placed here, whatever the pack would choose.
func (s *server) dirProjectOwned(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projects[id]
	return p != nil && p.Dir != ""
}
