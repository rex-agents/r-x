package rux

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// homeDir points HOME at a fresh directory, symlinks resolved.
func homeDir(t *testing.T) string {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", h)
	return h
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newCheckout makes a git checkout with a commit and, if remote isn't empty, an origin.
func newCheckout(t *testing.T, dir, remote string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "--quiet", "--initial-branch", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "first")
	if remote != "" {
		runGit(t, dir, "remote", "add", "origin", remote)
	}
	return dir
}

func dirServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	return &server{threads: map[string]*thread{}, projects: map[string]*project{}, backend: "rox", dataDir: dir, name: "mac",
		started: time.Now(), roxBin: "/nonexistent/rox", advertise: "127.0.0.1:1", memory: "1g",
		models: &modelCache{claudeBin: "/nonexistent/claude", dir: dir}, tools: newToolHost(dir)}
}

func TestCheckProjectDir(t *testing.T) {
	home := homeDir(t)
	good := newCheckout(t, filepath.Join(home, "code", "app"), "git@github.com:acme/app.git")
	c, err := checkProjectDir(good)
	if err != nil || c.Path != good || c.Repo != "https://github.com/acme/app.git" {
		t.Fatalf("good checkout: %+v, %v", c, err)
	}
	if c, err = checkProjectDir("~/code/app"); err != nil || c.Path != good {
		t.Fatalf("~ path: %+v, %v", c, err)
	}
	nonGH := newCheckout(t, filepath.Join(home, "other"), "https://example.com/x/y.git")
	if c, err = checkProjectDir(nonGH); err != nil || c.Repo != "" {
		t.Fatalf("non-GitHub remote: %+v, %v", c, err)
	}

	file := filepath.Join(home, "file.txt")
	os.WriteFile(file, nil, 0o644)
	plain := filepath.Join(home, "plain")
	os.MkdirAll(plain, 0o755)
	empty := filepath.Join(home, "empty")
	os.MkdirAll(empty, 0o755)
	runGit(t, empty, "init", "--quiet")
	noRemote := newCheckout(t, filepath.Join(home, "noremote"), "")
	detached := newCheckout(t, filepath.Join(home, "detached"), "git@github.com:acme/d.git")
	runGit(t, detached, "checkout", "--quiet", "--detach")
	outside := newCheckout(t, filepath.Join(t.TempDir(), "elsewhere"), "git@github.com:acme/e.git")
	link := filepath.Join(home, "sneaky")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct{ dir, want string }{
		"missing":        {filepath.Join(home, "nope"), "doesn't exist"},
		"a file":         {file, "isn't a directory"},
		"relative":       {"code/app", "must be absolute"},
		"not git":        {plain, "not a git repository; run git init and commit first"},
		"no commits":     {empty, "no commits"},
		"no remote":      {noRemote, "no git remote"},
		"detached":       {detached, "detached HEAD"},
		"subdirectory":   {filepath.Join(good, ".git", ".."), ""}, // the root itself: fine
		"outside home":   {outside, "inside your home directory"},
		"symlink escape": {link, "inside your home directory"},
		"home itself":    {home, "not a git repository"},
	} {
		_, err := checkProjectDir(tc.dir)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	os.MkdirAll(filepath.Join(good, "src"), 0o755)
	if _, err := checkProjectDir(filepath.Join(good, "src")); err == nil || !strings.Contains(err.Error(), "add that directory instead") {
		t.Errorf("subdirectory of a checkout: %v", err)
	}
	// Nothing was written into the user's directories.
	if _, err := os.Stat(filepath.Join(plain, ".git")); err == nil {
		t.Error("a non-git directory was initialised")
	}
}

func TestDirProjectIDs(t *testing.T) {
	home := homeDir(t)
	s := dirServer(t)
	a := newCheckout(t, filepath.Join(home, "a", "My App"), "git@github.com:acme/app.git")
	b := newCheckout(t, filepath.Join(home, "b", "My App"), "git@github.com:acme/app2.git")
	s.ensureProjectLocked("scratch-not-a-repo") // an unrelated project
	s.projects["my-app"] = &project{ID: "my-app", Name: "taken"}

	p1, err := s.ensureDirProject(a)
	if err != nil || p1.ID != "my-app-2" || p1.Name != "My App" || p1.Dir != a || p1.Authority != "mac" || p1.Repo != "https://github.com/acme/app.git" {
		t.Fatalf("first: %+v, %v", p1, err)
	}
	again, err := s.ensureDirProject(a)
	if err != nil || again.ID != p1.ID {
		t.Fatalf("same dir twice: %+v, %v", again, err)
	}
	p2, err := s.ensureDirProject(b)
	if err != nil || p2.ID != "my-app-3" {
		t.Fatalf("same base name: %+v, %v", p2, err)
	}
	// It's saved, and survives a restart.
	s2 := dirServer(t)
	s2.dataDir = s.dataDir
	if err := s2.loadProjects(); err != nil || s2.projects["my-app-2"] == nil || s2.projects["my-app-2"].Dir != a {
		t.Fatalf("reload: %v %+v", err, s2.projects["my-app-2"])
	}
}

func TestFsDirs(t *testing.T) {
	home := homeDir(t)
	for _, d := range []string{"zeta", "alpha/inner", ".hidden", "repo/.git"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	os.WriteFile(filepath.Join(home, "file"), nil, 0o644)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(home, "escape"))
	os.Symlink(filepath.Join(home, "alpha"), filepath.Join(home, "inlink"))

	abs, dirs, trunc, err := listDirs(home, false)
	if err != nil || abs != home || trunc {
		t.Fatalf("list: %v %v %v", abs, trunc, err)
	}
	var names []string
	for _, d := range dirs {
		names = append(names, d.Name)
		if d.Git != (d.Name == "repo") {
			t.Errorf("%s: git = %v", d.Name, d.Git)
		}
	}
	if got := strings.Join(names, ","); got != "alpha,inlink,repo,zeta" {
		t.Errorf("dirs = %s (no files, hidden, or links out of home)", got)
	}
	_, dirs, _, _ = listDirs(home, true)
	if len(dirs) != 5 || dirs[0].Name != ".hidden" {
		t.Errorf("hidden=1: %+v", dirs)
	}
	for _, p := range []string{"/", outside, filepath.Join(home, "escape"), filepath.Join(home, "..")} {
		if _, _, _, err := listDirs(p, false); err == nil || errorCode(err) != 403 {
			t.Errorf("%s: err = %v", p, err)
		}
	}
	if _, _, _, err := listDirs(filepath.Join(home, "file"), false); err == nil || errorCode(err) != 400 {
		t.Errorf("a file: %v", err)
	}
	if _, _, _, err := listDirs(filepath.Join(home, "missing"), false); err == nil || errorCode(err) != 404 {
		t.Errorf("missing: %v", err)
	}
	// The cap.
	big := filepath.Join(home, "big")
	for i := 0; i < maxDirEntries+5; i++ {
		os.MkdirAll(filepath.Join(big, "d"+strings.Repeat("0", 4-len(itoa(i)))+itoa(i)), 0o755)
	}
	_, dirs, trunc, err = listDirs(big, false)
	if err != nil || len(dirs) != maxDirEntries || !trunc {
		t.Errorf("cap: %d entries, truncated %v, %v", len(dirs), trunc, err)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestLoopbackOnly(t *testing.T) {
	home := homeDir(t)
	co := newCheckout(t, filepath.Join(home, "app"), "git@github.com:acme/app.git")
	s := dirServer(t)
	app, _ := s.routes()

	do := func(method, path, body, remote string, hdr map[string]string, onSock bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		if onSock {
			r = r.WithContext(context.WithValue(r.Context(), unixConnKey, true))
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	body := `{"dir":` + string(mustJSON(co)) + `}`
	cases := []struct {
		name   string
		remote string
		hdr    map[string]string
		sock   bool
		want   int
	}{
		{"unix socket", "@", nil, true, 200},
		{"loopback", "127.0.0.1:5000", nil, false, 200},
		{"ipv6 loopback", "[::1]:5000", nil, false, 200},
		{"tailnet peer", "100.64.0.9:5000", nil, false, 403},
		{"tailscale serve", "127.0.0.1:5000", map[string]string{"Tailscale-User-Login": "me@x"}, false, 403},
		{"forwarded", "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "100.64.0.9"}, false, 403},
	}
	for _, c := range cases {
		if w := do("POST", "/api/projects", body, c.remote, c.hdr, c.sock); w.Code != c.want {
			t.Errorf("POST dir, %s: %d %s", c.name, w.Code, w.Body)
		}
		want := c.want
		if w := do("GET", "/api/fs/dirs", "", c.remote, c.hdr, c.sock); w.Code != want {
			t.Errorf("GET fs, %s: %d %s", c.name, w.Code, w.Body)
		}
	}
	// A repo project is still open to the tailnet.
	if w := do("POST", "/api/projects", `{"repo":"acme/web"}`, "100.64.0.9:5000", nil, false); w.Code != 200 {
		t.Errorf("repo project over the tailnet: %d %s", w.Code, w.Body)
	}
	// Errors from the directory check come back as 400 with the message.
	plain := filepath.Join(home, "plain")
	os.MkdirAll(plain, 0o755)
	w := do("POST", "/api/projects", `{"dir":`+string(mustJSON(plain))+`}`, "127.0.0.1:1", nil, false)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "not a git repository") {
		t.Errorf("non-git dir: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/projects", `{"dir":"/tmp","repo":"a/b"}`, "127.0.0.1:1", nil, false); w.Code != 400 {
		t.Errorf("dir and repo: %d", w.Code)
	}
	// The listing's shape.
	w = do("GET", "/api/fs/dirs", "", "127.0.0.1:1", nil, false)
	var out struct {
		Path, Parent, Home string
		Dirs               []dirEntry
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Path != home || out.Parent != "" || out.Home != home || len(out.Dirs) != 2 {
		t.Errorf("listing: %+v", out)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// fakeRoxCwd logs each rox call with the directory it ran in.
func fakeRoxCwd(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$PWD|$@\" >> " + log + "\necho \"Started " + fakeGuest + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "rox"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func TestDirProjectThreadUsesLocalCopy(t *testing.T) {
	home := homeDir(t)
	co := newCheckout(t, filepath.Join(home, "My App"), "git@github.com:acme/app.git")
	s := dirServer(t)
	log := fakeRoxCwd(t)
	s.roxBin = filepath.Join(filepath.Dir(log), "rox")
	s.proxy = newModelProxy(s)
	p, err := s.ensureDirProject(co)
	if err != nil {
		t.Fatal(err)
	}
	s.projects[p.ID].Branch = "feature" // ignored: --local copies the checkout as it is

	th, err := s.createThread(createReq{Project: p.ID, Prompt: "hello", Branch: "other"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "rox up", func() bool { return strings.Contains(readLog(log), "up ") })
	line := strings.TrimSpace(readLog(log))
	cwd, args, _ := strings.Cut(line, "|")
	if r, _ := filepath.EvalSymlinks(cwd); r != co {
		t.Errorf("rox ran in %q, want %q", cwd, co)
	}
	for _, want := range []string{"up container --local --no-git-agent --agents claude", "--credentials=false", "--no-tailscale", "--memory 1g",
		`--dir "$HOME/my-app"`, "--role", "--thread " + th.meta.ID} {
		if want == "--role" {
			continue // only managers
		}
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	for _, bad := range []string{"--no-project", "--repo", "--branch"} {
		if strings.Contains(args, bad) {
			t.Errorf("args have %q:\n%s", bad, args)
		}
	}
	if th.meta.LocalDir != co || th.meta.Branch != "" || th.meta.Repo != "https://github.com/acme/app.git" {
		t.Errorf("meta: dir %q branch %q repo %q", th.meta.LocalDir, th.meta.Branch, th.meta.Repo)
	}
	if th.meta.Project != p.ID {
		t.Errorf("the thread moved to project %q", th.meta.Project)
	}

	// A manager takes the same path.
	mgr, err := s.createThread(createReq{Project: p.ID, Prompt: "plan", Kind: "manager"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second rox up", func() bool { return strings.Count(readLog(log), "up container --local") == 2 })
	if !strings.Contains(readLog(log), "--role 'manager'") || mgr.meta.LocalDir != co {
		t.Errorf("manager: %s", readLog(log))
	}

	// A repo project still uses --no-project in the data directory.
	rp, _ := s.createThread(createReq{Repo: "acme/web", Prompt: "x"})
	waitFor(t, "third rox up", func() bool { return strings.Count(readLog(log), "up container --no-project") == 1 })
	if !strings.Contains(readLog(log), s.dataDir+"|up container --no-project") || rp.meta.LocalDir != "" {
		t.Errorf("repo project: %s", readLog(log))
	}
}

func TestDirProjectRefusals(t *testing.T) {
	home := homeDir(t)
	co := newCheckout(t, filepath.Join(home, "app"), "git@github.com:acme/app.git")
	s := dirServer(t)
	p, _ := s.ensureDirProject(co)
	for name, tc := range map[string]struct {
		req  createReq
		want string
	}{
		"fly":       {createReq{Runtime: "fly"}, "fly guests can't take a copy"},
		"railway":   {createReq{Runtime: "railway"}, "railway guests can't take a copy"},
		"guest":     {createReq{Guest: "rv-x-0123456789ab"}, "not an existing one"},
		"unsandbox": {createReq{Placement: authapi.Placement{Local: true}}, "can't run unsandboxed"},
		"elsewhere": {createReq{Placement: authapi.Placement{Authority: "studio"}}, "can't be placed on studio"},
	} {
		tc.req.Project, tc.req.Prompt = p.ID, "x"
		_, err := s.createThread(tc.req)
		if err == nil || !strings.Contains(err.Error(), tc.want) || errorCode(err) != 400 {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Another authority's directory project.
	s.projects[p.ID].Authority = "studio"
	if _, err := s.createThread(createReq{Project: p.ID, Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "on authority studio") {
		t.Errorf("foreign owner: %v", err)
	}
	if len(s.threads) != 0 {
		t.Errorf("refused requests left %d threads", len(s.threads))
	}
}
