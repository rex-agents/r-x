package rix

import (
	"os"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	got, err := ParseEnvFile(strings.NewReader("# comment\n\nexport A=1\nB = 'two words'\nC=\"x=y\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two words", "C": "x=y"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, err := ParseEnvFile(strings.NewReader("not a pair\n")); err == nil {
		t.Error("expected an error for a line without =")
	}
}

func TestSecretsArePrivate(t *testing.T) {
	isolate(t)
	if err := UpdateSecrets(func(s map[string]string) error { s["BUTLER_API_KEY"] = "k1"; return nil }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(secretsPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode %v, %v", info.Mode().Perm(), err)
	}
	got, err := LoadSecrets()
	if err != nil || got["BUTLER_API_KEY"] != "k1" {
		t.Fatalf("LoadSecrets = %v, %v", got, err)
	}
	if err := os.Chmod(secretsPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecrets(); err == nil {
		t.Error("a secrets file others can read should be refused")
	}
}

func TestGitHubFromVault(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, map[string]any{"github": map[string]any{"token_file": secret(t, dir, "gh", "ghp_example")}})
	c, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := c.GitHub()
	if err != nil || cred.Password != "ghp_example" || cred.Source != "vault" || cred.Username != "x-access-token" {
		t.Fatalf("GitHub() = %+v, %v", cred, err)
	}
	writeConfig(t, map[string]any{"github": map[string]any{"off": true}})
	c, _ = LoadConfig("")
	if _, err := c.GitHub(); err == nil {
		t.Error("GitHub access turned off should refuse")
	}
}
