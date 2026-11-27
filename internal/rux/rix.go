package rux

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Every credential rux uses comes from rix: guests' Claude Code and Codex sign-ins through
// rox's hooks (rox up --agents), the GitHub token from rix credential github, and the
// secrets actions list from rix secret get. rux keeps none of them on disk.

// rixBin is rix from RIX_BIN, PATH, or beside this binary.
func rixBin() (string, error) {
	if p := os.Getenv("RIX_BIN"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("rix"); err == nil {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), "rix"); isExecutable(p) {
			return p, nil
		}
	}
	return "", errors.New("rix isn't installed, and rux gets every credential from it. Install rix beside rux")
}

func isExecutable(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// rixJSON runs rix with args and decodes its JSON output into v.
func rixJSON(v any, args ...string) error {
	bin, err := rixBin()
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("rix %s took too long", args[0])
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		msg = strings.TrimPrefix(msg, "✗ rix: ")
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return json.Unmarshal(stdout.Bytes(), v)
}

type rixGitHubCred struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Source   string `json:"source"`
}

// rixGitHub is the GitHub token rix hands out: the stored one, or gh's login.
func rixGitHub() (*rixGitHubCred, error) {
	var c rixGitHubCred
	if err := rixJSON(&c, "credential", "github", "--json"); err != nil {
		return nil, err
	}
	if c.Password == "" {
		return nil, errors.New("rix returned no GitHub token")
	}
	return &c, nil
}

// rixSecrets returns the named secrets rix keeps, and the names it doesn't have.
func rixSecrets(names []string) (map[string]string, []string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil, nil
	}
	var out struct {
		Values  map[string]string `json:"values"`
		Missing []string          `json:"missing"`
	}
	if err := rixJSON(&out, append([]string{"secret", "get", "--json"}, names...)...); err != nil {
		return nil, nil, err
	}
	if out.Values == nil {
		out.Values = map[string]string{}
	}
	return out.Values, out.Missing, nil
}

// rixSecretNames is the names of the secrets rix keeps.
func rixSecretNames() (map[string]bool, error) {
	var out struct {
		Names []string `json:"names"`
	}
	if err := rixJSON(&out, "secret", "ls", "--json"); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range out.Names {
		have[n] = true
	}
	return have, nil
}

// secretsHint is what the app and agents are told to run to add a secret.
const secretsHint = "rix secret set NAME"
