package rux

import (
	"strings"
	"testing"
)

func TestHeadlessWithoutRex(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	if s := currentSession(""); s != headlessSession {
		t.Errorf("session %q", s)
	}
	if text, err := rexHostsText(); err != nil || !strings.HasPrefix(text, "this machine") {
		t.Errorf("hosts %q %v", text, err)
	}
	a := &subAgent{Name: "api", Agent: "claude", Prompt: "do it", Session: headlessSession, path: "/x/api.json"}
	if !headless(a) || a.logPath() != "/x/api.log" {
		t.Errorf("headless %v %s", headless(a), a.logPath())
	}
	if cmd := headlessCommand(a); !strings.HasPrefix(cmd, "claude -p --dangerously-skip-permissions") {
		t.Errorf("claude %s", cmd)
	}
	a.Agent = "codex"
	if cmd := headlessCommand(a); !strings.HasPrefix(cmd, "codex exec --dangerously-bypass") {
		t.Errorf("codex %s", cmd)
	}
	if msg := rexSplit("x", "true"); !strings.Contains(msg, "Panes need Rex") {
		t.Errorf("split %q", msg)
	}
}
