package rix

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

// hostGit makes the fake answer git config --global --get with the given values.
func hostGit(values map[string]string) func(sys.Call) sys.Result {
	return func(c sys.Call) sys.Result {
		line := c.Line()
		if strings.HasPrefix(line, "git config --global --get ") {
			if v, ok := values[line[strings.LastIndex(line, " ")+1:]]; ok {
				return sys.Result{Stdout: v + "\n"}
			}
			return sys.Result{Code: 1}
		}
		return sys.Result{}
	}
}

func withGitHubUser(t *testing.T, fn func(string) (int64, string, string, error)) {
	t.Helper()
	previous := githubUser
	githubUser = fn
	t.Cleanup(func() { githubUser = previous })
}

func TestResolveGitIdentityPrefersHostGitConfig(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, map[string]any{"github": map[string]any{"token_file": secret(t, dir, "gh", "ghp_x")}})
	withGitHubUser(t, func(string) (int64, string, string, error) { t.Error("asked GitHub"); return 0, "", "", nil })
	fake := sys.NewFake()
	fake.Handle = hostGit(map[string]string{"user.name": "Ada L", "user.email": "ada@example.com"})
	defer fake.Install()()
	c, _ := LoadConfig("")
	id := ResolveGitIdentity(c)
	if id == nil || id.Name != "Ada L" || id.Email != "ada@example.com" || id.Source != "git config" {
		t.Fatalf("identity %+v", id)
	}
}

func TestResolveGitIdentityFallsBackToGitHub(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, map[string]any{"github": map[string]any{"token_file": secret(t, dir, "gh", "ghp_x")}})
	var token string
	withGitHubUser(t, func(tok string) (int64, string, string, error) { token = tok; return 42, "ada", "", nil })
	fake := sys.NewFake()
	fake.Handle = hostGit(map[string]string{"user.name": "Only A Name"}) // email unset
	defer fake.Install()()
	c, _ := LoadConfig("")
	id := ResolveGitIdentity(c)
	if token != "ghp_x" || id == nil || id.Name != "ada" || id.Email != "42+ada@users.noreply.github.com" || id.Source != "GitHub" {
		t.Fatalf("identity %+v, token %q", id, token)
	}
}

func TestResolveGitIdentityNone(t *testing.T) {
	isolate(t)
	writeConfig(t, map[string]any{"github": map[string]any{"off": true}})
	fake := sys.NewFake()
	fake.Handle = hostGit(nil)
	defer fake.Install()()
	c, _ := LoadConfig("")
	if id := ResolveGitIdentity(c); id != nil {
		t.Fatalf("identity %+v", id)
	}
	// GitHub errors count as none, too.
	dir := t.TempDir()
	writeConfig(t, map[string]any{"github": map[string]any{"token_file": secret(t, dir, "gh", "ghp_x")}})
	withGitHubUser(t, func(string) (int64, string, string, error) { return 0, "", "", errors.New("offline") })
	c, _ = LoadConfig("")
	if id := ResolveGitIdentity(c); id != nil {
		t.Fatalf("identity %+v", id)
	}
	out := &Outcome{}
	if err := PushGitIdentity(c, &contract.Guest{Name: "rv-web-0123456789ab"}, out); err != nil {
		t.Fatal(err)
	}
	if len(fake.Ran("rix git-identity")) != 0 || len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "no Git identity") {
		t.Errorf("calls %+v, warnings %v", fake.Calls, out.Warnings)
	}
}

func TestPushGitIdentitySendsBase64OnStdin(t *testing.T) {
	isolate(t)
	t.Setenv("ROX_BIN", "/bin/rox")
	fake := sys.NewFake()
	fake.Handle = hostGit(map[string]string{"user.name": `O'Brien "Bob" $(x)`, "user.email": "bob@example.com"})
	defer fake.Install()()
	c, _ := LoadConfig("")
	out := &Outcome{}
	if err := PushGitIdentity(c, &contract.Guest{Name: "rv-web-0123456789ab"}, out); err != nil {
		t.Fatal(err)
	}
	calls := fake.Ran("/bin/rox exec rv-web-0123456789ab --stdin", "rix git-identity")
	if len(calls) != 1 {
		t.Fatalf("calls %+v", fake.Calls)
	}
	want := base64.StdEncoding.EncodeToString([]byte(`O'Brien "Bob" $(x)`)) + "\n" + base64.StdEncoding.EncodeToString([]byte("bob@example.com")) + "\n"
	if calls[0].Stdin != want {
		t.Errorf("stdin %q, want %q", calls[0].Stdin, want)
	}
	if strings.Contains(calls[0].Line(), "O'Brien") || strings.Contains(calls[0].Line(), "bob@example.com") {
		t.Error("the identity appeared in argv")
	}
}

func TestGuestGitIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	input := func(name, email string) []byte {
		e := base64.StdEncoding.EncodeToString
		return []byte(e([]byte(name)) + "\n" + e([]byte(email)) + "\n")
	}
	get := func(key string) string {
		data, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), " = "); ok && k == key {
				return v
			}
		}
		return ""
	}
	if out, err := guestSh(t, home, input(`A "q" B`, "a@x.io"), "git-identity"); err != nil {
		t.Fatal(err, out)
	}
	if get("name") != `A \"q\" B` || get("email") != "a@x.io" {
		t.Fatalf("config %q", mustRead(filepath.Join(home, ".gitconfig")))
	}
	// rix's own earlier identity is replaced, even after an agent changed it.
	_ = os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = rux pack\n\temail = x@anthropic.com\n"), 0o600)
	if out, err := guestSh(t, home, input("New", "n@x.io"), "git-identity"); err != nil || get("name") != "New" {
		t.Fatal(err, out)
	}
	// An identity the guest's user chose, with no rix marker, stays.
	home2 := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home2, ".gitconfig"))
	_ = os.WriteFile(filepath.Join(home2, ".gitconfig"), []byte("[user]\n\tname = Mine\n\temail = m@x.io\n"), 0o600)
	if out, err := guestSh(t, home2, input("New", "n@x.io"), "git-identity"); err != nil {
		t.Fatal(err, out)
	}
	if data := mustRead(filepath.Join(home2, ".gitconfig")); !strings.Contains(data, "Mine") || strings.Contains(data, "New") {
		t.Errorf("overwrote the guest's own identity: %s", data)
	}
}

func mustRead(p string) string { data, _ := os.ReadFile(p); return string(data) }
