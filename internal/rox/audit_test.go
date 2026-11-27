package rox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/sys"
)

// fileGit runs real subprocesses, but lets git use the file transport for local fixtures.
// Production keeps GIT_ALLOW_PROTOCOL=ssh:https.
type fileGit struct{ sys.Real }

func (r fileGit) Run(c sys.Cmd) (sys.Result, error) {
	if len(c.Argv) > 0 && c.Argv[0] == "git" {
		env := map[string]string{}
		for k, v := range c.Env {
			env[k] = v
		}
		env["GIT_ALLOW_PROTOCOL"] = "file"
		c.Env = env
	}
	return r.Real.Run(c)
}

func allowFileGit() func() {
	previous := sys.R
	sys.R = fileGit{}
	return func() { sys.R = previous }
}

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestReadBranchSurvivesPrefixTransitions(t *testing.T) {
	_, errs := isolate(t)
	defer allowFileGit()()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	origin := filepath.Join(t.TempDir(), "origin")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, origin, "init", "--quiet", "-b", "main")
	commit := func(branch, memory string) {
		fixtureGit(t, origin, "checkout", "--quiet", "-B", branch, "main")
		if err := os.WriteFile(filepath.Join(origin, ProjectConfig), []byte(`{"linux":{"memory":"`+memory+`"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, origin, "add", ".")
		fixtureGit(t, origin, "commit", "--quiet", "-m", branch)
	}
	fixtureGit(t, origin, "commit", "--quiet", "--allow-empty", "-m", "root")
	read := func(branch, memory string) {
		t.Helper()
		b, err := ReadBranch(&Source{URL: "file://" + origin, Branch: branch, Name: "p"}, &Lease{})
		if err != nil || b == nil {
			t.Fatalf("%s: %v %v\n%s", branch, b, err, errs)
		}
		if fmt.Sprint(b.Settings["linux"]) != "map[memory:"+memory+"]" {
			t.Fatalf("%s memory = %v, want %s", branch, b.Settings["linux"], memory)
		}
		if got := fixtureGit(t, b.Mirror, "rev-parse", branchCacheRef(branch)); got != b.Commit {
			t.Fatalf("%s cache ref at %s, want %s", branch, got, b.Commit)
		}
	}
	// feature, then feature/x after feature is deleted, then feature again after feature/x is.
	commit("feature", "1G")
	read("feature", "1G")
	fixtureGit(t, origin, "checkout", "--quiet", "main")
	fixtureGit(t, origin, "branch", "-D", "feature")
	commit("feature/x", "2G")
	read("feature/x", "2G")
	fixtureGit(t, origin, "checkout", "--quiet", "main")
	fixtureGit(t, origin, "branch", "-D", "feature/x")
	commit("feature", "3G")
	read("feature", "3G")
}

func TestBranchCacheRefIsFlatAndDistinct(t *testing.T) {
	a, b := branchCacheRef("a"), branchCacheRef("a/b")
	if a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/") || strings.Count(a, "/") != 2 {
		t.Errorf("refs %s %s", a, b)
	}
	if a != branchCacheRef("a") {
		t.Error("not deterministic")
	}
}

func stopFixture(t *testing.T, name string, running bool, stopCode int) (*sys.Fake, *Guest) {
	t.Helper()
	isolate(t)
	g := NewGuest(name, "linux", "img", "workspace", "docker")
	g.Phase, g.GuestSocket, g.ContainerID = "ready", "/run/rex.sock", "cid"
	if err := os.MkdirAll(g.Directory(), 0o700); err != nil {
		t.Fatal(err)
	}
	g.MustSave()
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		switch {
		case strings.Contains(c.Line(), "docker inspect"):
			if running {
				return sys.Result{Stdout: "true\n"}
			}
			return sys.Result{Stdout: "false\n"}
		case strings.Contains(c.Line(), "docker stop"):
			return sys.Result{Code: stopCode, Stderr: "boom"}
		}
		return sys.Result{}
	}
	t.Cleanup(fake.Install())
	return fake, g
}

func TestStopFailureKeepsSavedState(t *testing.T) {
	fake, g := stopFixture(t, sampleName, true, 1)
	if err := Stop(g.Name); err == nil {
		t.Fatal("expected an error")
	}
	got, lerr := LoadGuest(g.Name)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if got.Stopped {
		t.Error("failed stop was saved as stopped")
	}
	if len(fake.Ran("-O exit")) != 0 {
		t.Errorf("tunnel was torn down: %+v", fake.Calls)
	}
}

func TestStopSucceedsAndSavesState(t *testing.T) {
	fake, g := stopFixture(t, sampleName, true, 0)
	if err := Stop(g.Name); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadGuest(g.Name); !got.Stopped || got.Phase != "ready" {
		t.Errorf("record %+v", got)
	}
	if len(fake.Ran("docker stop cid")) != 1 {
		t.Errorf("calls %+v", fake.Calls)
	}
}

func TestStopAlreadyStoppedBackend(t *testing.T) {
	_, g := stopFixture(t, sampleName, false, 1)
	if err := Stop(g.Name); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadGuest(g.Name); !got.Stopped {
		t.Error("not saved as stopped")
	}
}

func TestReconnectRefusesIncompleteBoot(t *testing.T) {
	for _, phase := range []string{"failed", "starting"} {
		fake, g := stopFixture(t, sampleName, true, 0)
		g.Phase = phase
		g.Stopped = true
		g.MustSave()
		if err := Reconnect(g.Name, false); err == nil {
			t.Fatalf("%s: reconnect promoted an incomplete guest", phase)
		}
		got, _ := LoadGuest(g.Name)
		if got.Phase != phase || !got.Stopped {
			t.Errorf("%s: record changed: %+v", phase, got)
		}
		if len(fake.Calls) != 0 {
			t.Errorf("%s: ran %+v", phase, fake.Calls)
		}
	}
}

func TestStopErrorThenObservedStoppedIsSuccess(t *testing.T) {
	fake, g := stopFixture(t, sampleName, true, 1)
	stopped := false
	inner := fake.Handle
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "docker stop") {
			stopped = true // the backend timed out after it had stopped the container
		}
		if strings.Contains(c.Line(), "docker inspect") && stopped {
			return sys.Result{Stdout: "false\n"}
		}
		return inner(c)
	}
	if err := Stop(g.Name); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadGuest(g.Name); !got.Stopped {
		t.Error("reconciled stop was not saved")
	}
}
