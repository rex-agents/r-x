// Package roxrex is the rox-rex hook provider. rox calls it at fixed points in a guest's life
// (docs/rox/rex-hook.md), and it does everything Rex: installs and starts Rex in the guest,
// tunnels its socket to the launcher, creates sessions, registers the guest as a Rex host and
// opens it in the Rex app. It reaches guests only through rox exec and rox inspect.
package roxrex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

// roxBin is the rox that called the hook, or rox on PATH.
func roxBin() string {
	if p := os.Getenv("ROX_BIN"); p != "" {
		return p
	}
	if p, err := sys.R.LookPath("rox"); err == nil {
		return p
	}
	return "rox"
}

const guestPath = `export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; exec "$@"`

// execArgv is rox exec NAME -- argv.
func execArgv(name string, stdin bool, argv ...string) []string {
	out := []string{roxBin(), "exec", name}
	if stdin {
		out = append(out, "--stdin")
	}
	return append(append(out, "--"), argv...)
}

// guestRun runs argv in the guest through rox exec and returns its stdout.
func guestRun(g *contract.Guest, input string, timeout time.Duration, argv ...string) (string, error) {
	return sys.OutputWith(sys.Cmd{Argv: execArgv(g.Name, input != "", argv...), Input: input, Timeout: timeout})
}

// guestRex is rex for the guest's own server, run in the guest.
func guestRex(g *contract.Guest, args ...string) []string {
	return append([]string{"sh", "-c", guestPath, "rox", "rex", "--autostart=false", "-S", "unix://" + g.Socket}, args...)
}

// guestScript runs one action of rex-guest.sh in the guest.
func guestScript(g *contract.Guest, timeout time.Duration, action string, args ...string) (sys.Result, error) {
	argv := execArgv(g.Name, false, append([]string{"bash", "-c", assets.RexGuest(), "rox-rex", action}, args...)...)
	return sys.Try(sys.Cmd{Argv: argv, Timeout: timeout})
}

// endpoint is the launcher's address for the guest's Rex server.
func endpoint(g *contract.Guest) string { return "unix://" + filepath.Join(g.Runtime, "rex.sock") }

// rexOn is rex aimed at the guest's server through the launcher's socket.
func rexOn(g *contract.Guest, args ...string) []string {
	return append([]string{"rex", "--autostart=false", "--timeout", "5s", "-S", endpoint(g)}, args...)
}

func reachable(g *contract.Guest) bool {
	if g.Runtime == "" {
		return false
	}
	result, err := sys.Try(sys.Cmd{Argv: rexOn(g, "ls", "--json"), Timeout: 8 * time.Second})
	return err == nil && result.Code == 0
}

// Per-guest state, kept beside rox's own in the guest's state folder.

// Window is a Guest tab rox-rex opened, for down to close.
type Window struct {
	Session string `json:"session"`
	Window  string `json:"window"`
}

type state struct {
	Registered bool     `json:"registered"`
	Shared     bool     `json:"shared"`
	Windows    []Window `json:"windows"`
}

func statePath(g *contract.Guest) string { return filepath.Join(g.Dir, "rex.json") }

// loadState reads rex.json, falling back to the fields rox kept in guest.json before Rex moved
// into this hook.
func loadState(g *contract.Guest) *state {
	s := &state{}
	if g.Dir == "" {
		return s
	}
	if data, err := os.ReadFile(statePath(g)); err == nil {
		_ = json.Unmarshal(data, s)
		return s
	}
	var legacy struct {
		Registered   bool     `json:"registered"`
		ShellWindows []Window `json:"shell_windows"`
	}
	if data, err := os.ReadFile(filepath.Join(g.Dir, "guest.json")); err == nil && json.Unmarshal(data, &legacy) == nil {
		s.Registered, s.Shared, s.Windows = legacy.Registered, legacy.Registered, legacy.ShellWindows
	}
	return s
}

func (s *state) save(g *contract.Guest) error {
	if g.Dir == "" {
		return nil
	}
	data, err := sys.MarshalIndent(s)
	if err != nil {
		return err
	}
	return sys.WriteAtomic(statePath(g), data, 0o600, true)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func rexJSON(timeout time.Duration, argv ...string) (map[string]any, error) {
	out, err := sys.OutputWith(sys.Cmd{Argv: argv, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("%s printed something other than JSON", strings.Join(argv[:min(3, len(argv))], " "))
	}
	return v, nil
}
