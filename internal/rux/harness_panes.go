package rux

import (
	"os"
	"os/exec"

	"github.com/rex-agents/r-x/internal/harness"
)

// In Rex, rux opens an agent in a pane beside it (rux view, which shows the agent live and
// talks to it through rux's control socket) or a shell in a sub-agent's guest.

func rexSplit(label string, argv ...string) string {
	sess, block := os.Getenv("REX_SESSION"), os.Getenv("REX_BLOCK")
	if sess == "" || block == "" {
		return "Panes need Rex (REX_SESSION isn't set). Read the agent with rux agent read"
	}
	args := append([]string{"split", "-s", sess, "-b", block, "--split=right", "--ratio", "45", "--label", label, "--focus=false", "--"}, argv...)
	if out, err := exec.Command("rex", args...).CombinedOutput(); err != nil {
		return "rex split failed: " + lastLine(string(out))
	}
	return "Opened " + label + " in a pane"
}

func (m *harnessModel) openPane(a *harness.Agent) string {
	if a == nil {
		return "No agent by that name"
	}
	if m.r.sock == "" {
		return "rux's control socket isn't running"
	}
	return rexSplit("rux · "+a.Name, selfExe(), "view", "--sock", m.r.sock, a.Name)
}

func (m *harnessModel) openShell(a *harness.Agent) string {
	m.r.mu.Lock()
	s := m.r.subs[a.Name]
	m.r.mu.Unlock()
	if s == nil || s.Guest == "" {
		return a.Name + "'s guest isn't up yet"
	}
	argv := []string{"rox", "shell"}
	if s.Host != "" {
		argv = append(argv, "-S", s.Host)
	}
	argv = append(argv, s.Guest)
	if s.Project != "" {
		argv = append(argv, s.Project)
	}
	return rexSplit(a.Name+" · "+s.Guest, argv...)
}
