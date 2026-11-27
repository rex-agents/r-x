package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// scripted is a Provider that plays back turns.
type scripted struct {
	mu    sync.Mutex
	turns []Turn
	seen  [][]ToolResult
	users []string
}

func (s *scripted) Name() string            { return "test/scripted" }
func (s *scripted) New(string) Conversation { return s }
func (s *scripted) User(t string)           { s.mu.Lock(); s.users = append(s.users, t); s.mu.Unlock() }
func (s *scripted) Results(r []ToolResult, _ string) {
	s.mu.Lock()
	s.seen = append(s.seen, r)
	s.mu.Unlock()
}
func (s *scripted) Step(ctx context.Context, _ []ToolSpec, on func(Delta)) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) == 0 {
		return Turn{Text: "done", Stop: StopEnd}, nil
	}
	t := s.turns[0]
	s.turns = s.turns[1:]
	if t.Text != "" {
		on(Delta{Kind: "text", Text: t.Text})
	}
	return t, nil
}

func TestAgentLoopRunsToolsAndReports(t *testing.T) {
	dir := t.TempDir()
	p := &scripted{turns: []Turn{
		{Stop: StopTools, Calls: []ToolCall{
			{ID: "1", Name: "write_file", Input: MarshalInput(map[string]string{"path": "a/b.txt", "content": "hello\nworld\n"})},
			{ID: "2", Name: "bash", Input: MarshalInput(map[string]string{"command": "exit 3"})},
		}},
		{Stop: StopTools, Calls: []ToolCall{
			{ID: "3", Name: "edit_file", Input: MarshalInput(map[string]string{"path": "a/b.txt", "old_string": "world", "new_string": "there"})},
			{ID: "4", Name: "read_file", Input: MarshalInput(map[string]string{"path": "a/b.txt"})},
			{ID: "5", Name: "nope", Input: MarshalInput(map[string]string{})},
		}},
		{Text: "All done.", Stop: StopEnd},
	}}
	a := NewAgent("x", "x", "sub")
	ended := make(chan string, 1)
	a.OnTurnEnd = func(_ *Agent, text string, err error) { ended <- text }
	a.Attach(p, "", CodingTools(&Local{WorkDir: dir}), "here")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	a.Send("you", "go")
	select {
	case text := <-ended:
		if text != "All done." {
			t.Fatalf("final text %q", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn didn't end")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a/b.txt"))
	if string(b) != "hello\nthere\n" {
		t.Fatalf("file is %q", b)
	}
	if len(p.seen) != 2 || !p.seen[0][1].IsError || !strings.Contains(p.seen[0][1].Content, "exit code 3") {
		t.Fatalf("bash result: %+v", p.seen)
	}
	if !strings.Contains(p.seen[1][1].Content, "2\tthere") || !p.seen[1][2].IsError {
		t.Fatalf("second results: %+v", p.seen[1])
	}
	if st, _ := a.Status(); st != Waiting {
		t.Fatalf("status %s", st)
	}
}

func TestApprovalDenied(t *testing.T) {
	dir := t.TempDir()
	p := &scripted{turns: []Turn{{Stop: StopTools, Calls: []ToolCall{
		{ID: "1", Name: "bash", Input: MarshalInput(map[string]string{"command": "touch ran"})},
	}}}}
	a := NewAgent("l", "lead", "lead")
	a.Approve = func(*Agent, *Tool, string) bool { return false }
	ended := make(chan struct{}, 1)
	a.OnTurnEnd = func(*Agent, string, error) { ended <- struct{}{} }
	a.Attach(p, "", ReadTools(&Local{WorkDir: dir}), "here")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	a.Send("you", "go")
	<-ended
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Fatal("denied command ran")
	}
	if !p.seen[0][0].IsError {
		t.Fatal("denied call wasn't an error")
	}
}

func TestEditNeedsUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x x"), 0o600)
	edit := editTool(&Local{WorkDir: dir})
	_, err := edit.Run(context.Background(), MarshalInput(map[string]string{"path": "f", "old_string": "x", "new_string": "y"}))
	if err == nil || !strings.Contains(err.Error(), "2 places") {
		t.Fatalf("got %v", err)
	}
	if _, err := edit.Run(context.Background(), json.RawMessage(`{"path":"f","old_string":"x"`)); err == nil {
		t.Fatal("cut-off input was accepted")
	}
}

func TestOpenAIStreamingToolCalls(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
			`{"choices":[{"delta":{"content":"Look"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"grep","arguments":"{\"pat"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"tern\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := &OpenAI{Label: "t", Model: "m", BaseURL: srv.URL, APIKey: "k"}
	c := p.New("sys")
	c.User("hi")
	var deltas []Delta
	turn, err := c.Step(context.Background(), []ToolSpec{{Name: "grep", Schema: schema([]string{"pattern"}, map[string]any{"pattern": str("p")})}},
		func(d Delta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if turn.Stop != StopTools || len(turn.Calls) != 1 || string(turn.Calls[0].Input) != `{"pattern":"x"}` || turn.Text != "Look" {
		t.Fatalf("turn %+v", turn)
	}
	if turn.Usage.Input != 10 || len(deltas) != 2 || deltas[0].Kind != "thinking" {
		t.Fatalf("usage %+v deltas %+v", turn.Usage, deltas)
	}
	if msgs := got["messages"].([]any); len(msgs) != 2 || got["tools"] == nil {
		t.Fatalf("request %v", got)
	}
	c.Results([]ToolResult{{ID: "c1", Content: "found"}}, "also this")
	oc := c.(*openaiConv)
	if n := len(oc.messages); n != 5 || oc.messages[3].Role != "tool" || oc.messages[4].Role != "user" {
		t.Fatalf("history %+v", oc.messages)
	}

	bad := &OpenAI{Label: "t", Model: "m", BaseURL: srv.URL, APIKey: "nope"}
	if _, err := bad.New("").Step(context.Background(), nil, func(Delta) {}); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("got %v", err)
	}
}

func TestAnthropicRequestAndToolRoundTrip(t *testing.T) {
	var bodies []map[string]any
	var betas []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		betas = append(betas, r.Header.Get("anthropic-beta"))
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(name, data string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data) }
		ev("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":1}}}`)
		if len(bodies) == 1 {
			ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
			ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`)
			ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`)
			ev("content_block_stop", `{"type":"content_block_stop","index":0}`)
			ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu1","name":"grep","input":{}}}`)
			ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"x\"}"}}`)
			ev("content_block_stop", `{"type":"content_block_stop","index":1}`)
			ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`)
		} else {
			ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Found it."}}`)
			ev("content_block_stop", `{"type":"content_block_stop","index":0}`)
			ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		}
		ev("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()
	p := &Anthropic{Model: "claude-opus-5-5", APIKey: "k", BaseURL: srv.URL}
	c := p.New("be brief")
	c.User("find x")
	tools := []ToolSpec{{Name: "grep", Description: "search", Schema: schema([]string{"pattern"}, map[string]any{"pattern": str("p")})}}
	var thinking string
	turn, err := c.Step(context.Background(), tools, func(d Delta) {
		if d.Kind == "thinking" {
			thinking += d.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Stop != StopTools || len(turn.Calls) != 1 || string(turn.Calls[0].Input) != `{"pattern":"x"}` || thinking != "plan" {
		t.Fatalf("turn %+v thinking %q", turn, thinking)
	}
	b := bodies[0]
	if b["thinking"].(map[string]any)["type"] != "adaptive" || b["fallbacks"] != "default" ||
		b["output_config"].(map[string]any)["effort"] != "high" || b["context_management"] == nil || b["cache_control"] == nil {
		t.Fatalf("request %v", b)
	}
	if !strings.Contains(betas[0], "server-side-fallback-2026-07-01") || !strings.Contains(betas[0], "compact-2026-01-12") {
		t.Fatalf("betas %q", betas[0])
	}
	c.Results([]ToolResult{{ID: "tu1", Content: "a.go:1:x"}}, "")
	turn, err = c.Step(context.Background(), tools, func(Delta) {})
	if err != nil || turn.Text != "Found it." || turn.Stop != StopEnd {
		t.Fatalf("second turn %+v %v", turn, err)
	}
	msgs := bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("history %v", msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "thinking" || asst[0].(map[string]any)["signature"] != "sig" {
		t.Fatalf("thinking block not replayed: %v", asst)
	}
	res := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if res["type"] != "tool_result" || res["tool_use_id"] != "tu1" {
		t.Fatalf("tool result %v", res)
	}
}

func TestInterruptStopsChildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	// The grandchild keeps the output pipe open, as rox exec's ssh does.
	_, _, err := (&Local{WorkDir: t.TempDir()}).Run(ctx, "sh -c 'sleep 30' & sleep 30", nil, time.Minute)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

func TestClaudeCodeEngine(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	// A fake claude: one turn with streamed text, a tool call that needs approval, and a result.
	_ = os.WriteFile(fake, []byte(`#!/bin/sh
echo "$@" > "$(dirname "$0")/args"
read -r user
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"stream_event","event":{"type":"message_start"}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Checking."}}}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make"}}]}}'
echo '{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"make"}}}'
read -r answer
echo "$answer" > "$(dirname "$0")/answer"
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"built"}]}]}}'
echo '{"type":"result","subtype":"success","result":"Built it.","usage":{"input_tokens":5,"output_tokens":7}}'
sleep 5
`), 0o755)
	cc := &ClaudeCode{Command: func(args []string) *exec.Cmd { return exec.Command(fake, args...) }, Model: "opus"}
	a := NewAgent("l", "lead", "lead")
	asked := ""
	a.Approve = func(_ *Agent, tl *Tool, label string) bool { asked = tl.Spec.Name + " " + label; return true }
	ended := make(chan string, 1)
	a.OnTurnEnd = func(_ *Agent, text string, err error) {
		if err != nil {
			text = "error: " + err.Error()
		}
		ended <- text
	}
	a.AttachRunner(cc, "here")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	a.Send("you", "build it")
	select {
	case got := <-ended:
		if got != "Built it." {
			t.Fatalf("final %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn didn't end")
	}
	defer a.Close()
	if asked != "Bash make" {
		t.Fatalf("approval asked %q", asked)
	}
	ans, _ := os.ReadFile(filepath.Join(dir, "answer"))
	if !strings.Contains(string(ans), `"behavior":"allow"`) || !strings.Contains(string(ans), `"request_id":"r1"`) {
		t.Fatalf("answer %s", ans)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "--permission-prompt-tool stdio") || !strings.Contains(string(args), "--model opus") {
		t.Fatalf("args %s", args)
	}
	snap := a.Snapshot()
	var kinds []string
	for _, e := range snap.Entries {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "user,text,tool" || snap.Entries[1].Text != "Checking." || snap.Entries[2].Result != "built" {
		t.Fatalf("entries %+v", snap.Entries)
	}
}

func TestSwitchCarriesContext(t *testing.T) {
	first := &scripted{turns: []Turn{{Text: "one", Stop: StopEnd}}}
	a := NewAgent("x", "x", "lead")
	ended := make(chan struct{}, 2)
	a.OnTurnEnd = func(*Agent, string, error) { ended <- struct{}{} }
	a.Attach(first, "", nil, "here")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	a.Send("you", "hi")
	<-ended
	second := &scripted{}
	a.Attach(second, "", nil, "here")
	a.Carry("earlier: hi / one")
	a.Send("you", "again")
	<-ended
	if len(second.users) != 1 || !strings.HasPrefix(second.users[0], "earlier: hi / one") || !strings.HasSuffix(second.users[0], "again") {
		t.Fatalf("second engine got %q", second.users)
	}
	if a.ModelName() != "test/scripted" {
		t.Fatal(a.ModelName())
	}
}

func TestCodexEngine(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	// A fake codex exec --json: records its arguments and prompt, and plays a turn.
	_ = os.WriteFile(fake, []byte(`#!/bin/sh
d="$(dirname "$0")"
echo "$@" >> "$d/args"
cat > "$d/prompt.$#"
echo '{"type":"thread.started","thread_id":"th1"}'
echo '{"type":"turn.started"}'
echo '{"type":"item.completed","item":{"id":"i0","type":"reasoning","text":"plan"}}'
echo '{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"/bin/zsh -lc '"'"'make test'"'"'","aggregated_output":"","exit_code":null,"status":"in_progress"}}'
echo '{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"/bin/zsh -lc '"'"'make test'"'"'","aggregated_output":"FAIL","exit_code":2,"status":"failed"}}'
echo '{"type":"item.completed","item":{"id":"i2","type":"file_change","changes":[{"path":"a.go","kind":"update"}],"status":"completed"}}'
echo '{"type":"item.completed","item":{"id":"i3","type":"agent_message","text":"Fixed it."}}'
echo '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":3}}'
`), 0o755)
	cx := &Codex{Command: func(args []string) *exec.Cmd { return exec.Command(fake, args...) }, Model: "gpt-x", System: "SYS"}
	a := NewAgent("c", "c", "sub")
	ended := make(chan string, 2)
	a.OnTurnEnd = func(_ *Agent, text string, err error) {
		if err != nil {
			text = "error: " + err.Error()
		}
		ended <- text
	}
	a.AttachRunner(cx, "here")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	a.Send("you", "fix it")
	if got := <-ended; got != "Fixed it." {
		t.Fatalf("final %q", got)
	}
	a.Send("you", "again")
	<-ended
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "exec --json") || !strings.HasPrefix(lines[1], "exec resume th1 --json") ||
		!strings.Contains(lines[0], "-m gpt-x") || !strings.Contains(lines[0], `sandbox_mode="read-only"`) {
		t.Fatalf("args %q", lines)
	}
	snap := a.Snapshot()
	var kinds []string
	for _, e := range snap.Entries {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds[:5], ",") != "user,thinking,tool,tool,text" {
		t.Fatalf("kinds %v", kinds)
	}
	if e := snap.Entries[2]; e.Tool != "shell" || e.Label != "make test" || !e.IsError || !strings.Contains(e.Result, "exit code 2") {
		t.Fatalf("shell entry %+v", e)
	}
	if e := snap.Entries[3]; e.Tool != "edit" || e.Result != "update a.go" {
		t.Fatalf("edit entry %+v", e)
	}
	if snap.Usage.Input != 20 || snap.Usage.CacheRead != 8 {
		t.Fatalf("usage %+v", snap.Usage)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "prompt.*"))
	var prompts []string
	for _, m := range matches {
		b, _ := os.ReadFile(m)
		prompts = append(prompts, string(b))
	}
	joined := strings.Join(prompts, "|")
	if !strings.Contains(joined, "SYS\n\n---\n\nfix it") || strings.Count(joined, "SYS") != 1 {
		t.Fatalf("prompts %q", prompts)
	}
}
