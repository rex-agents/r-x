package rux

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/harness"
)

type fakeConv struct {
	steps   []harness.Turn
	results [][]harness.ToolResult
	users   []string
}

func (f *fakeConv) User(t string) { f.users = append(f.users, t) }
func (f *fakeConv) Results(r []harness.ToolResult, note string) {
	f.results = append(f.results, r)
}
func (f *fakeConv) Step(ctx context.Context, _ []harness.ToolSpec, _ func(harness.Delta)) (harness.Turn, error) {
	t := f.steps[0]
	f.steps = f.steps[1:]
	return t, nil
}

func TestHeadlessStreamJSON(t *testing.T) {
	var buf bytes.Buffer
	conv := &fakeConv{steps: []harness.Turn{
		{Text: "starting", Calls: []harness.ToolCall{{ID: "c1", Name: "echo", Input: json.RawMessage(`{"x":1}`)}, {ID: "c2", Name: "nope"}}},
		{Text: "all done", Stop: harness.StopEnd},
	}}
	h := &headless{out: json.NewEncoder(&buf), session: "s1", conv: conv, history: filepath.Join(t.TempDir(), "h.jsonl")}
	h.tools = []harness.Tool{{Spec: harness.ToolSpec{Name: "echo"}, Run: func(ctx context.Context, in json.RawMessage) (string, error) { return "got " + string(in), nil }}}
	h.turn(context.Background(), "hello")

	var evs []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, m)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e["type"].(string))
	}
	if strings.Join(types, ",") != "assistant,user,assistant,result" {
		t.Fatalf("events %v", types)
	}
	res := evs[3]
	if res["subtype"] != "success" || res["result"] != "all done" || res["is_error"] != false {
		t.Errorf("result = %v", res)
	}
	tr := evs[1]["message"].(map[string]any)["content"].([]any)
	if tr[0].(map[string]any)["content"] != `got {"x":1}` || tr[1].(map[string]any)["is_error"] != true {
		t.Errorf("tool results = %v", tr)
	}
	// A restart replays the text history.
	if got := loadHistory(h.history); !strings.Contains(got, "user: hello") || !strings.Contains(got, "assistant: all done") {
		t.Errorf("history = %q", got)
	}
	if userText([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"image"},{"type":"text","text":"hi"}]}}`)) != "hi" {
		t.Error("userText")
	}
	if _, err := proxiedProvider("ollama/x", "", "http://h", "tok"); err == nil {
		t.Error("only anthropic and openai go through the proxy")
	}
	if _, err := proxiedProvider("anthropic/claude-sonnet-5-5", "", "", ""); err == nil {
		t.Error("a guest with no proxy has no keys")
	}
	_ = os.Remove
}

// mcpServer is an authority with a plain thread t1 (the agent) and a manager, behind /mcp.
func mcpServer(t *testing.T) (*server, *httptest.Server) {
	s, _, _ := testServer(t)
	s.roxBin = "/nonexistent/rox"
	s.projects = map[string]*project{}
	s.name = "mac"
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func TestAnyThreadSpawnsSubAgents(t *testing.T) {
	s, srv := mcpServer(t)
	agent := s.threads["t1"]
	repo := "https://github.com/o/r.git"
	pid := projectIDFor(repo)
	agent.meta.Project = pid
	agent.meta.Engine = "harness"
	agent.meta.Model = "anthropic/claude-sonnet-5-5"
	s.projects[pid] = &project{ID: pid, Name: "r", Repo: repo}
	other, _ := openThread(filepath.Join(s.dataDir, "threads", "t9"), &threadMeta{ID: "t9", Token: "other", Project: pid, Status: "idle"})
	s.threads["t9"] = other

	call := func(tok, method string, params any) map[string]any {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if resp.StatusCode != 200 {
			return map[string]any{"status": float64(resp.StatusCode)}
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if call("bad", "tools/list", nil)["status"] != float64(403) {
		t.Error("an unknown token must be refused")
	}
	if l := call("tok", "tools/list", nil)["result"]; l == nil || !strings.Contains(toJSON(l), "start_thread") {
		t.Fatalf("a plain thread should get the spawn tools: %v", l)
	}
	r := call("tok", "tools/call", map[string]any{"name": "start_thread", "arguments": map[string]any{"prompt": "do the thing", "title": "child"}})
	if toJSON(r["result"]) == "" || strings.Contains(toJSON(r["result"]), `"isError":true`) {
		t.Fatalf("start_thread: %v", r)
	}
	var child *thread
	s.mu.Lock()
	for _, c := range s.threads {
		if c.meta.Parent == "t1" {
			child = c
		}
	}
	s.mu.Unlock()
	if child == nil {
		t.Fatal("no child thread")
	}
	if child.meta.Engine != "harness" || child.meta.Model != "anthropic/claude-sonnet-5-5" || child.meta.Project != pid {
		t.Errorf("child = %+v", child.meta)
	}
	// It shows up in the parent's subagents, and only the parent manages it.
	var sub int
	for _, ts := range s.threadSummaries() {
		if ts.ID == "t1" {
			sub = len(ts.SubAgents)
		}
	}
	if sub != 1 {
		t.Errorf("subagents = %d, want 1", sub)
	}
	own := call("tok", "tools/call", map[string]any{"name": "read_thread", "arguments": map[string]any{"thread_id": child.meta.ID}})
	if strings.Contains(toJSON(own["result"]), `"isError":true`) {
		t.Errorf("a parent reads its child: %v", own)
	}
	foreign := call("tok", "tools/call", map[string]any{"name": "read_thread", "arguments": map[string]any{"thread_id": "t9"}})
	if !strings.Contains(toJSON(foreign["result"]), `"isError":true`) {
		t.Errorf("a thread mustn't manage another's threads: %v", foreign)
	}
	// Depth is bounded.
	grand := &thread{meta: threadMeta{ID: "g", Parent: child.meta.ID}}
	if d := s.depth(grand); d != 2 {
		t.Errorf("depth = %d, want 2", d)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestResolveEngine(t *testing.T) {
	s := &server{}
	for _, c := range []struct {
		engine, model, want string
		err                 bool
	}{
		{"", "", "", false}, {"claude-code", "opus", "", false},
		{"harness", "anthropic/claude-sonnet-5-5", "harness", false}, {"harness", "openai/gpt-5", "harness", false},
		{"harness", "", "", true}, {"harness", "ollama/x", "", true}, {"other", "", "", true},
	} {
		got, err := s.resolveEngine(c.engine, c.model)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
}
