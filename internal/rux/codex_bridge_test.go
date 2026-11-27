package rux

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/harness"
)

func TestAgentSelection(t *testing.T) {
	for _, c := range []struct{ spec, cli, model string }{
		{"", "claude", ""},
		{"opus", "claude", "opus"},
		{"claude-opus-5-5", "claude", "claude-opus-5-5"},
		{"claude-code", "claude", ""},
		{"claude-code/sonnet", "claude", "sonnet"},
		{"codex", "codex", ""},
		{"codex/gpt-5", "codex", "gpt-5"},
	} {
		if cli, model := agentFor(c.spec); cli != c.cli || model != c.model {
			t.Errorf("agentFor(%q) = %q, %q; want %q, %q", c.spec, cli, model, c.cli, c.model)
		}
	}
}

func TestServeAgentDefault(t *testing.T) {
	s := &server{} // no --agent: Claude
	if got := s.agentOf(""); got != "claude" {
		t.Errorf("default agent = %q, want claude", got)
	}
	if got := s.agentOf("codex/gpt-5"); got != "codex" {
		t.Errorf("a codex model runs on %q", got)
	}
	if got := s.agentOf("sonnet"); got != "claude" {
		t.Errorf("a Claude model runs on %q", got)
	}
	s = &server{agent: "codex"} // rux serve --agent codex
	if got := s.agentOf(""); got != "codex" {
		t.Errorf("--agent codex: default agent = %q", got)
	}
	if got := s.effectiveModel(""); got != "codex" {
		t.Errorf("--agent codex: effective model = %q", got)
	}
	if got := s.agentOf("claude-code/opus"); got != "claude" {
		t.Errorf("--agent codex, but the thread chose Claude Code: %q", got)
	}
}

func TestValidSettingsAgentNames(t *testing.T) {
	for _, ok := range []string{"codex", "codex/gpt-5", "claude-code/opus", "opus", "default"} {
		if _, _, err := validSettings(ok, ""); err != nil {
			t.Errorf("validSettings(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"codex/", "codex/a b", "foo/bar", "codex/../x"} {
		if _, _, err := validSettings(bad, ""); err == nil {
			t.Errorf("validSettings(%q) accepted it", bad)
		}
	}
}

func TestRunnerCodexBridgeCommand(t *testing.T) {
	r := &runner{stateDir: "/state", effort: "high", notes: "be brief"}
	bin, args := r.codexBridge("gpt-5")
	if bin == "" || args[0] != "codex-bridge" {
		t.Fatalf("command %q %v", bin, args)
	}
	j := strings.Join(args, " ")
	for _, want := range []string{"--state /state/codex-thread", "--model gpt-5", "--effort high", "be brief"} {
		if !strings.Contains(j, want) {
			t.Errorf("args lack %q: %v", want, args)
		}
	}
}

func TestCodexBridgeTurn(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	os.WriteFile(fake, []byte(`#!/bin/sh
echo "$@" >> "$0.args"
cat > /dev/null
echo '{"type":"thread.started","thread_id":"th1"}'
echo '{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"ls"}}'
echo '{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"ls","aggregated_output":"a.txt","exit_code":0,"status":"completed"}}'
echo '{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"done"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`), 0o755)
	c := &harness.Codex{Model: "gpt-x", Bypass: true, Command: func(a []string) *exec.Cmd { return exec.Command(fake, a...) }}
	in := strings.NewReader(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"list files"}]}}` + "\n")
	var out bytes.Buffer
	saved := 0
	if err := bridgeLoop(in, &out, c, "gpt-x", func(context.CancelFunc) {}, func() { saved++ }); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`"subtype":"init"`, `"type":"tool_use"`, `"name":"shell"`, `"command":"ls"`,
		`"type":"tool_result"`, `"text":"a.txt"`, `"text":"done"`, `"type":"result"`, `"subtype":"success"`, `"is_error":false`} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %s:\n%s", want, got)
		}
	}
	if c.Thread() != "th1" || saved != 1 {
		t.Errorf("thread %q, saved %d", c.Thread(), saved)
	}
}
