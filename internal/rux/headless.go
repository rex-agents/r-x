package rux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rex-agents/r-x/internal/harness"
)

// The headless engine is rux's own harness (internal/harness) run without a UI, as a drop-in
// for `claude -p --input-format stream-json --output-format stream-json`: `rux runner` starts it
// in a thread's guest instead of claude, and the thread's events, status and cost reach the
// authority exactly as before. It's `rux headless` here; the harness's `rux agent --headless`
// is this same function.
//
// Its model calls go to the authority's model proxy (the thread token is its API key), so the
// guest holds no model keys. Its tools are the harness's coding tools in the thread's working
// directory, plus every tool the authority's /mcp offers (start_thread, read_thread, ...), so a
// thread can start sub-agents whatever engine it runs.
//
// Limits: only anthropic/MODEL and openai/MODEL go through the proxy; the conversation isn't
// resumable across restarts (the last turns' text is replayed as context); cost isn't tracked.

const headlessHistoryTurns = 20

type headless struct {
	out     *json.Encoder
	session string
	conv    harness.Conversation
	tools   []harness.Tool
	specs   []harness.ToolSpec
	history string // path of the text transcript kept across restarts
	replay  string // earlier conversation, sent ahead of the first message
	turns   int
}

func runHeadless(args []string) {
	fs := flag.NewFlagSet("headless", flag.ExitOnError)
	model := fs.String("model", "", "provider/model: anthropic/… or openai/…")
	dir := fs.String("dir", ".", "working directory")
	effort := fs.String("effort", "", "effort for Claude models")
	system := fs.String("system", "", "system prompt")
	proxy := fs.String("model-proxy", "", "the authority's base URL, for /proxy/...")
	token := fs.String("token", os.Getenv("RUX_THREAD_TOKEN"), "the thread token")
	mcpURL := fs.String("mcp", "", "the authority's MCP URL, whose tools the agent gets")
	session := fs.String("session-id", "", "session id")
	state := fs.String("state", "", "directory for the text history kept across restarts")
	fs.Parse(args)

	prov, err := proxiedProvider(*model, *effort, *proxy, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rux headless:", err)
		os.Exit(1)
	}
	h := &headless{out: json.NewEncoder(os.Stdout), session: *session}
	ex := &harness.Local{WorkDir: *dir}
	h.tools = harness.CodingTools(ex)
	if *mcpURL != "" {
		mt, err := mcpTools(context.Background(), *mcpURL, *token)
		if err != nil {
			fmt.Fprintln(os.Stderr, "rux headless: no sub-agent tools:", err)
		}
		h.tools = append(h.tools, mt...)
	}
	for _, t := range h.tools {
		h.specs = append(h.specs, t.Spec)
	}
	h.conv = prov.New(*system)
	if *state != "" {
		h.history = filepath.Join(*state, "headless-history.jsonl")
		h.replay = loadHistory(h.history)
	}
	h.emit(map[string]any{"type": "system", "subtype": "init", "session_id": h.session, "model": prov.Name(), "cwd": *dir,
		"tools": toolNames(h.tools)})

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	var cancel context.CancelFunc = func() {}
	go func() {
		for range sigs {
			cancel()
		}
	}()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		text := userText(sc.Bytes())
		if text == "" {
			continue
		}
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		h.turn(ctx, text)
		cancel()
	}
}

func toolNames(ts []harness.Tool) []string {
	var n []string
	for _, t := range ts {
		n = append(n, t.Spec.Name)
	}
	return n
}

// proxiedProvider builds a provider that calls the model through the authority's proxy.
func proxiedProvider(spec, effort, proxy, token string) (harness.Provider, error) {
	name, model, err := splitModel(spec)
	if err != nil {
		return nil, err
	}
	if proxy == "" || token == "" {
		return nil, errors.New("--model-proxy and --token are required: a guest has no model keys of its own")
	}
	base := strings.TrimSuffix(proxy, "/")
	switch name {
	case "anthropic":
		return &harness.Anthropic{Model: model, APIKey: token, BaseURL: base + "/proxy/anthropic", Effort: effort}, nil
	case "openai":
		return &harness.OpenAI{Label: "openai", Model: model, BaseURL: base + "/proxy/openai/v1", APIKey: token, Effort: effort}, nil
	}
	return nil, fmt.Errorf("the authority's model proxy serves anthropic and openai models, not %s", name)
}

// userText is the text of a stream-json user message.
func userText(line []byte) string {
	var m struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Message.Content, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct{ Type, Text string }
	if json.Unmarshal(m.Message.Content, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n\n"))
}

func (h *headless) emit(v map[string]any) { h.out.Encode(v) }

// turn runs one user message to the model's final answer, emitting stream-json events.
func (h *headless) turn(ctx context.Context, text string) {
	start := time.Now()
	if h.replay != "" {
		text = "(You were restarted. Your conversation so far, text only:)\n" + h.replay + "\n\n(New message:)\n" + text
		h.replay = ""
	}
	h.conv.User(text)
	h.appendHistory("user", text)
	byName := map[string]*harness.Tool{}
	for i := range h.tools {
		byName[h.tools[i].Spec.Name] = &h.tools[i]
	}
	finish := func(subtype, final string, isErr bool) {
		h.turns++
		h.emit(map[string]any{"type": "result", "subtype": subtype, "is_error": isErr, "result": final,
			"duration_ms": time.Since(start).Milliseconds(), "num_turns": h.turns, "session_id": h.session, "total_cost_usd": 0})
	}
	for step := 0; step < 200; step++ {
		t, err := h.conv.Step(ctx, h.specs, func(harness.Delta) {})
		if err != nil {
			if ctx.Err() != nil {
				finish("error_during_execution", "interrupted", true)
			} else {
				h.emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
					"content": []any{map[string]any{"type": "text", "text": "Model error: " + err.Error()}}}})
				finish("error_during_execution", err.Error(), true)
			}
			return
		}
		var blocks []any
		if strings.TrimSpace(t.Text) != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": t.Text})
		}
		for _, c := range t.Calls {
			var in any = map[string]any{}
			json.Unmarshal(c.Input, &in)
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": in})
		}
		if len(blocks) > 0 {
			h.emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": blocks}})
		}
		if t.Stop == harness.StopRefusal {
			finish("error_during_execution", "the model declined this request", true)
			return
		}
		if len(t.Calls) == 0 {
			h.appendHistory("assistant", t.Text)
			finish("success", t.Text, false)
			return
		}
		var results []harness.ToolResult
		var rblocks []any
		for _, c := range t.Calls {
			res := harness.ToolResult{ID: c.ID}
			if tool := byName[c.Name]; tool == nil {
				res.Content, res.IsError = "unknown tool "+c.Name, true
			} else if out, err := tool.Run(ctx, c.Input); err != nil {
				res.Content, res.IsError = strings.TrimSpace(out+"\n"+err.Error()), true
			} else {
				res.Content = out
			}
			results = append(results, res)
			rblocks = append(rblocks, map[string]any{"type": "tool_result", "tool_use_id": c.ID, "content": res.Content, "is_error": res.IsError})
		}
		h.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": rblocks}})
		h.conv.Results(results, "")
		if ctx.Err() != nil {
			finish("error_during_execution", "interrupted", true)
			return
		}
	}
	finish("error_max_turns", "stopped after 200 steps in one turn", true)
}

// ---- text history, so a restarted agent isn't blank ----

func (h *headless) appendHistory(role, text string) {
	if h.history == "" || strings.TrimSpace(text) == "" {
		return
	}
	f, err := os.OpenFile(h.history, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]string{"role": role, "text": text})
	f.Write(append(b, '\n'))
}

func loadHistory(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e struct{ Role, Text string }
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Text != "" {
			lines = append(lines, e.Role+": "+clipText(e.Text, 2000))
		}
	}
	if len(lines) > headlessHistoryTurns {
		lines = lines[len(lines)-headlessHistoryTurns:]
	}
	return strings.Join(lines, "\n\n")
}

// ---- the authority's MCP tools as harness tools ----

func mcpCall(ctx context.Context, url, token, method string, params any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var r struct {
		Result json.RawMessage
		Error  *struct{ Message string }
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, errors.New(r.Error.Message)
	}
	return r.Result, nil
}

func mcpTools(ctx context.Context, url, token string) ([]harness.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := mcpCall(ctx, url, token, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var list struct {
		Tools []struct {
			Name, Description string
			InputSchema       map[string]any `json:"inputSchema"`
		}
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	var out []harness.Tool
	for _, t := range list.Tools {
		name := t.Name
		out = append(out, harness.Tool{
			Spec:  harness.ToolSpec{Name: name, Description: t.Description, Schema: t.InputSchema},
			Label: func(in json.RawMessage) string { return name },
			Run: func(ctx context.Context, in json.RawMessage) (string, error) {
				var args any = map[string]any{}
				if len(in) > 0 {
					json.Unmarshal(in, &args)
				}
				raw, err := mcpCall(ctx, url, token, "tools/call", map[string]any{"name": name, "arguments": args})
				if err != nil {
					return "", err
				}
				var res struct {
					Content []struct{ Type, Text string }
					IsError bool `json:"isError"`
				}
				if err := json.Unmarshal(raw, &res); err != nil {
					return "", err
				}
				var parts []string
				for _, c := range res.Content {
					if c.Type == "text" {
						parts = append(parts, c.Text)
					} else {
						parts = append(parts, "["+c.Type+" omitted]")
					}
				}
				text := strings.Join(parts, "\n")
				if res.IsError {
					return "", errors.New(text)
				}
				return text, nil
			},
		})
	}
	return out, nil
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func without(args []string, name string) []string {
	var out []string
	for _, a := range args {
		if a != name {
			out = append(out, a)
		}
	}
	return out
}
