package rix

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for k, v := range map[string]string{"HOME": dir, "XDG_CONFIG_HOME": filepath.Join(dir, "config"),
		"XDG_STATE_HOME": filepath.Join(dir, "state"), "XDG_RUNTIME_DIR": filepath.Join(dir, "run"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": ""} {
		t.Setenv(k, v)
	}
	ui.SetOutput(&bytes.Buffer{}, &bytes.Buffer{})
	return dir
}

func writeConfig(t *testing.T, v map[string]any) {
	t.Helper()
	path := ConfigLocation("")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	data, _ := json.Marshal(v)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func secret(t *testing.T, dir, name, value string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAgentsConfig(t *testing.T) {
	dir := isolate(t)
	token := secret(t, dir, "tok", "abc")
	writeConfig(t, map[string]any{"agents": map[string]any{"claude": map[string]any{"token_file": token,
		"sync": map[string]any{"mcp": []any{"linear"}, "skills": false}}}})
	c, _ := LoadConfig("")
	agents, err := c.Agents()
	if err != nil {
		t.Fatal(err)
	}
	claude := agents["claude"]
	if claude.Credential["token_file"] != sys.Resolve(token) || claude.Sync.Skills || !claude.Sync.Instructions || claude.Sync.MCPNames[0] != "linear" {
		t.Errorf("claude %+v", claude)
	}
	if agents["codex"] == nil || !agents["codex"].Sync.Skills {
		t.Error("codex should get the default sync")
	}
	_ = os.Chmod(token, 0o644)
	if _, err := c.Agents(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("readable credential: %v", err)
	}
	writeConfig(t, map[string]any{"agents": map[string]any{"claude": map[string]any{"oops": 1}}})
	c, _ = LoadConfig("")
	if _, err := c.Agents(); err == nil || !strings.Contains(err.Error(), "agents.claude.oops") {
		t.Errorf("unknown setting: %v", err)
	}
}

func TestAgentEnvironmentOnlyForInstalledAgents(t *testing.T) {
	dir := isolate(t)
	agents := map[string]*Agent{"claude": {Credential: map[string]string{"api_key_file": secret(t, dir, "k", "sk-ant-api'x")}}}
	env, err := agentEnvironment(agents, map[string]string{"claude": "1.0.0"})
	if err != nil || !strings.Contains(env, `export ANTHROPIC_API_KEY='sk-ant-api'"'"'x'`) {
		t.Errorf("env %q %v", env, err)
	}
	env, _ = agentEnvironment(agents, map[string]string{"codex": "1.0.0"})
	if strings.Contains(env, "ANTHROPIC") {
		t.Error("pushed a credential for an agent the guest lacks")
	}
}

func TestPushSendsSecretsOnStdinOnly(t *testing.T) {
	dir := isolate(t)
	t.Setenv("ROX_BIN", "/bin/rox")
	writeConfig(t, map[string]any{"agents": map[string]any{
		"claude": map[string]any{"token_file": secret(t, dir, "tok", "TOKEN123")},
		"codex":  map[string]any{"api_key_file": secret(t, dir, "oai", "OPENAI456")}}})
	fake := sys.NewFake()
	defer fake.Install()()
	c, _ := LoadConfig("")
	out := &Outcome{}
	g := &contract.Guest{Name: "rv-web-0123456789ab", Agents: map[string]string{"claude": "1", "codex": "2"}}
	if err := PushCredentials(c, g, out); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.Calls {
		if strings.Contains(call.Line(), "TOKEN123") || strings.Contains(call.Line(), "OPENAI456") {
			t.Errorf("a secret is in argv: %s", call.Line())
		}
		if call.Argv[0] != "/bin/rox" || call.Argv[1] != "exec" {
			t.Errorf("rix reached the guest without rox exec: %s", call.Line())
		}
	}
	if len(fake.Ran("rix env")) != 1 || !strings.Contains(fake.Ran("rix env")[0].Stdin, "TOKEN123") || !strings.Contains(fake.Ran("rix codex-login")[0].Stdin, "OPENAI456") {
		t.Errorf("calls %+v", fake.Calls)
	}
	if len(out.Done) != 1 || out.Done[0] != "Signed in Claude Code and Codex" {
		t.Errorf("outcome %+v", out)
	}
}

func TestMergeSynced(t *testing.T) {
	current := map[string]any{"theme": "dark", "old": 1, "mcpServers": map[string]any{"mine": 1, "stale": 2}}
	record := MergeSynced(current, map[string]any{"keys": []any{"old"}, "mcp": []any{"stale"}},
		map[string]any{"new": true}, map[string]any{"linear": map[string]any{"url": "u"}}, "mcpServers")
	mcp := current["mcpServers"].(map[string]any)
	if _, ok := current["old"]; ok || current["new"] != true || mcp["mine"] != 1 || mcp["stale"] != nil || mcp["linear"] == nil {
		t.Errorf("merged %v", current)
	}
	if strings.Join(record["mcp"].([]string), ",") != "linear" {
		t.Errorf("record %v", record)
	}
}

func TestPlanSkipsLocalMCPAndRoxSkill(t *testing.T) {
	dir := isolate(t)
	for _, skill := range []string{"rox", "review", "bad name"} {
		_ = os.MkdirAll(filepath.Join(dir, ".claude", "skills", skill), 0o755)
		_ = os.WriteFile(filepath.Join(dir, ".claude", "skills", skill, "SKILL.md"), []byte("x"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"), []byte("be kind"), 0o644)
	state, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"remote": map[string]any{"type": "http", "url": "https://x"},
		"local": map[string]any{"command": "npx"}}})
	_ = os.WriteFile(filepath.Join(dir, ".claude.json"), state, 0o644)
	out := &Outcome{}
	plan := MakePlan(map[string]*Agent{"claude": {Sync: Sync{Skills: true, Instructions: true, MCPAll: true}}}, map[string]string{"claude": "1"}, out)
	if got := plan.Describe(); got != "skills review; CLAUDE.md; Claude Code MCP servers remote" {
		t.Errorf("plan %q", got)
	}
	out = &Outcome{}
	MakePlan(map[string]*Agent{"claude": {Sync: Sync{MCPNames: []string{"local"}}}}, map[string]string{"claude": "1"}, out)
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "runs a program on this machine") {
		t.Errorf("warnings %v", out.Warnings)
	}
}

func hook(t *testing.T, event string, g contract.Guest) contract.Reply {
	t.Helper()
	g.Schema = contract.Schema
	line, _ := json.Marshal(g)
	var out bytes.Buffer
	if err := Hook(event, g.Name, strings.NewReader(string(line)+"\n"), &out); err != nil {
		t.Fatalf("%s: %v", event, err)
	}
	var reply contract.Reply
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatalf("%s reply %q", event, out.String())
	}
	return reply
}

func TestHookContract(t *testing.T) {
	isolate(t)
	name := "rv-web-0123456789ab"
	reply := hook(t, contract.PreClone, contract.Guest{Name: name, Launch: &contract.Launch{URL: "https://github.com/a/b"}})
	if reply.Schema != 1 || reply.AgentSocket != "" {
		t.Errorf("https clone got an agent: %+v", reply)
	}
	reply = hook(t, contract.PreClone, contract.Guest{Name: name, Launch: &contract.Launch{URL: "git@github.com:a/b"}})
	if reply.AgentSocket != "" || len(reply.Warnings) != 1 {
		t.Errorf("no keys: %+v", reply)
	}
	_ = saveState(name, &GuestState{GitKeys: []string{"/k"}, Forward: true})
	reply = hook(t, contract.Down, contract.Guest{Name: name})
	if _, err := os.Stat(statePath(name)); !os.IsNotExist(err) || len(reply.Done) != 1 {
		t.Errorf("down kept state: %+v", reply)
	}
	var out bytes.Buffer
	err := Hook(contract.PostBoot, "rv-other-0123456789ab", strings.NewReader(`{"schema":1,"name":"`+name+`"}`+"\n"), &out)
	if err == nil {
		t.Error("accepted another guest's record")
	}
	if err := Hook(contract.PostBoot, name, strings.NewReader(`{"schema":2,"name":"`+name+`"}`+"\n"), &out); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("schema mismatch: %v", err)
	}
}

func TestMigrate(t *testing.T) {
	dir := isolate(t)
	roxDir := filepath.Join(dir, "config", "rox")
	_ = os.MkdirAll(roxDir, 0o700)
	token := secret(t, roxDir, "claude-token", "abc")
	old := map[string]any{"git_keys": []any{"~/.ssh/k"}, "git_agent": false, "container": map[string]any{"engine": "docker"},
		"agents": map[string]any{"claude": map[string]any{"token_file": token, "version": "1.2.3"}, "codex": false}}
	data, _ := json.Marshal(old)
	_ = os.WriteFile(filepath.Join(roxDir, "config.json"), data, 0o644)
	stateDir := filepath.Join(dir, "state", "rox", "rv-web-0123456789ab")
	_ = os.MkdirAll(stateDir, 0o700)
	_ = os.WriteFile(filepath.Join(stateDir, "guest.json"), []byte(`{"git_keys": ["/k"]}`), 0o600)
	var out bytes.Buffer
	ui.SetOutput(&out, &bytes.Buffer{})
	if err := Migrate("", false); err != nil {
		t.Fatal(err)
	}
	rix, _ := sys.ReadJSONObject(ConfigLocation(""))
	rox, _ := sys.ReadJSONObject(filepath.Join(roxDir, "config.json"))
	claude := rix["agents"].(map[string]any)["claude"].(map[string]any)
	if rix["git_agent"] != false || sys.Resolve(claude["token_file"].(string)) != sys.Resolve(filepath.Join(dir, "config", "rix", "claude-token")) || claude["version"] != nil {
		t.Errorf("rix config %v", rix)
	}
	if _, ok := rox["agents"]; ok || rox["container"] == nil {
		t.Errorf("rox config %v", rox)
	}
	if sys.IsFile(token) || !sys.IsFile(filepath.Join(dir, "config", "rix", "claude-token")) {
		t.Error("the token file didn't move")
	}
	if s, _ := loadState("rv-web-0123456789ab"); len(s.GitKeys) != 1 {
		t.Errorf("guest keys %+v", s)
	}
	if !strings.Contains(out.String(), `{"agents":{"claude":"1.2.3"}}`) {
		t.Errorf("snippet missing from %q", out.String())
	}
}
