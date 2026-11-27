//go:build integration

// Package integration boots a real Docker guest with the built rox. Run it with
// go test -tags integration ./integration/... on a machine with Docker and Rex.
package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func build(t *testing.T, dir, name string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", out, "../cmd/"+name)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, b)
	}
	return out
}

func TestDockerGuestLifecycle(t *testing.T) {
	for _, tool := range []string{"docker", "rex", "ssh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s", tool)
		}
	}
	dir := t.TempDir()
	rox := build(t, dir, "rox")
	env := append(os.Environ(), "XDG_STATE_HOME="+filepath.Join(dir, "state"), "XDG_CONFIG_HOME="+filepath.Join(dir, "config"),
		"ROX_HOOKS=off", "ROX_UPDATES=off", "NO_COLOR=1")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(rox, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("rox %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("up", "container", "--engine", "docker", "--no-project", "--no-tailscale", "--boot-timeout", "300")
	var list struct {
		Guests []struct{ Name, Phase string } `json:"guests"`
	}
	if err := json.Unmarshal([]byte(run("ls", "--json")), &list); err != nil || len(list.Guests) != 1 {
		t.Fatalf("ls: %v %+v", err, list)
	}
	name := list.Guests[0].Name
	defer run("down", name)
	if list.Guests[0].Phase != "ready" {
		t.Fatalf("phase %s", list.Guests[0].Phase)
	}
	if out := run("exec", name, "--", "uname", "-s"); strings.TrimSpace(out) != "Linux" {
		t.Errorf("exec printed %q", out)
	}
	run("stop", name)
	run("start", name)
	if out := run("exec", name, "--", "sh", "-c", "echo $HOME"); strings.TrimSpace(out) != "/home/workspace" {
		t.Errorf("home %q", out)
	}
}
