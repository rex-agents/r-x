package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TurnRunner is an engine that runs whole turns itself, tools included, such as Claude
// Code. The agent hands it each batch of messages and shows what it reports to the sink.
type TurnRunner interface {
	Name() string
	RunTurn(ctx context.Context, text string, s Sink) (final string, err error)
	Close()
}

// Sink takes what a TurnRunner reports during a turn.
type Sink interface {
	Delta(d Delta)
	ToolStart(id, name, label string)
	ToolDone(id, result string, isError bool)
	Usage(u Usage)
	// Approve asks the user whether a tool call may run.
	Approve(tool, label string) bool
}

// ClaudeCode runs Claude Code headless (claude -p with stream-json in and out) as an
// agent's engine, signed in the way Claude Code is: with the user's Claude subscription
// or whatever else it's set up with. Claude Code runs its own tools where it runs.
type ClaudeCode struct {
	// Command starts claude with these arguments, here or in a guest.
	Command func(args []string) *exec.Cmd
	Model   string
	// Effort is low, medium, high, xhigh or max.
	Effort string
	// System is appended to Claude Code's system prompt.
	System string
	// AllowedTools run without asking; with SkipPermissions everything does. Anything
	// else is asked of the user through the sink.
	AllowedTools    []string
	SkipPermissions bool
	// MCPConfig is a --mcp-config JSON value.
	MCPConfig string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	events  chan map[string]any
	session string
	dead    error
	wmu     sync.Mutex
}

func (c *ClaudeCode) Name() string {
	if c.Model == "" {
		return "claude-code"
	}
	return "claude-code/" + c.Model
}

func (c *ClaudeCode) args() []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages"}
	if c.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args, "--permission-prompts", "host", "--permission-prompt-tool", "stdio")
		if len(c.AllowedTools) > 0 {
			args = append(args, "--allowedTools", strings.Join(c.AllowedTools, ","))
		}
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if c.System != "" {
		args = append(args, "--append-system-prompt", c.System)
	}
	if c.MCPConfig != "" {
		args = append(args, "--mcp-config", c.MCPConfig)
	}
	if c.session != "" {
		args = append(args, "--resume", c.session)
	}
	return args
}

// start runs claude if it isn't running; it picks up the last session after a crash.
func (c *ClaudeCode) start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.dead == nil {
		return nil
	}
	cmd := c.Command(c.args())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting Claude Code: %v", err)
	}
	events := make(chan map[string]any, 256)
	c.cmd, c.stdin, c.events, c.dead = cmd, stdin, events, nil
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var ev map[string]any
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				events <- ev
			}
		}
		err := cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		c.mu.Lock()
		c.dead = fmt.Errorf("Claude Code exited (%v)%s", err, map[bool]string{true: ": " + lastLineOf(msg), false: ""}[msg != ""])
		c.mu.Unlock()
		close(events)
	}()
	return nil
}

func (c *ClaudeCode) write(v any) error {
	b, _ := json.Marshal(v)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.stdin.Write(append(b, '\n'))
	return err
}

func (c *ClaudeCode) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.stdin.Close()
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
	}
}

func (c *ClaudeCode) RunTurn(ctx context.Context, text string, s Sink) (string, error) {
	if err := c.start(); err != nil {
		return "", err
	}
	c.mu.Lock()
	events := c.events
	c.mu.Unlock()
	if err := c.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}); err != nil {
		return "", c.deadErr(err)
	}
	interrupted := false
	sawDelta := false
	var final strings.Builder
	done := ctx.Done()
	var giveUp <-chan time.Time
	for {
		select {
		case <-done:
			done = nil
			interrupted = true
			_ = c.write(map[string]any{"type": "control_request", "request_id": "rux-int-" + fmt.Sprint(time.Now().UnixNano()),
				"request": map[string]any{"subtype": "interrupt"}})
			giveUp = time.After(15 * time.Second)
			continue
		case <-giveUp:
			c.Close()
			return final.String(), ErrInterrupted
		case ev, ok := <-events:
			if !ok {
				if interrupted {
					return final.String(), ErrInterrupted
				}
				return final.String(), c.deadErr(nil)
			}
			switch ev["type"] {
			case "system":
				if id, _ := ev["session_id"].(string); id != "" {
					c.mu.Lock()
					c.session = id
					c.mu.Unlock()
				}
			case "stream_event":
				e, _ := ev["event"].(map[string]any)
				switch e["type"] {
				case "message_start":
					sawDelta = false
				case "content_block_delta":
					d, _ := e["delta"].(map[string]any)
					switch d["type"] {
					case "text_delta":
						t, _ := d["text"].(string)
						sawDelta = true
						s.Delta(Delta{Kind: "text", Text: t})
					case "thinking_delta":
						t, _ := d["thinking"].(string)
						s.Delta(Delta{Kind: "thinking", Text: t})
					}
				}
			case "assistant":
				msg, _ := ev["message"].(map[string]any)
				blocks, _ := msg["content"].([]any)
				for _, b := range blocks {
					blk, _ := b.(map[string]any)
					switch blk["type"] {
					case "text":
						if !sawDelta {
							t, _ := blk["text"].(string)
							s.Delta(Delta{Kind: "text", Text: t})
						}
					case "tool_use":
						id, _ := blk["id"].(string)
						name, _ := blk["name"].(string)
						input, _ := json.Marshal(blk["input"])
						s.ToolStart(id, ccToolName(name), ccLabel(name, input))
					}
				}
			case "user":
				msg, _ := ev["message"].(map[string]any)
				blocks, _ := msg["content"].([]any)
				for _, b := range blocks {
					blk, _ := b.(map[string]any)
					if blk["type"] != "tool_result" {
						continue
					}
					id, _ := blk["tool_use_id"].(string)
					isErr, _ := blk["is_error"].(bool)
					s.ToolDone(id, toolResultText(blk["content"]), isErr)
				}
			case "control_request":
				c.answer(ev, s)
			case "result":
				if u, ok := ev["usage"].(map[string]any); ok {
					num := func(k string) int64 { f, _ := u[k].(float64); return int64(f) }
					s.Usage(Usage{Input: num("input_tokens") + num("cache_read_input_tokens") + num("cache_creation_input_tokens"),
						Output: num("output_tokens"), CacheRead: num("cache_read_input_tokens")})
				}
				r, _ := ev["result"].(string)
				final.WriteString(r)
				if interrupted {
					return final.String(), ErrInterrupted
				}
				if isErr, _ := ev["is_error"].(bool); isErr {
					sub, _ := ev["subtype"].(string)
					return final.String(), fmt.Errorf("Claude Code: %s", cmpStr(r, sub))
				}
				return final.String(), nil
			}
		}
	}
}

// answer replies to Claude Code's control requests: permission prompts go to the user.
func (c *ClaudeCode) answer(ev map[string]any, s Sink) {
	id, _ := ev["request_id"].(string)
	req, _ := ev["request"].(map[string]any)
	reply := func(resp map[string]any) {
		_ = c.write(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": id, "response": resp}})
	}
	switch req["subtype"] {
	case "can_use_tool":
		name, _ := req["tool_name"].(string)
		input, _ := json.Marshal(req["input"])
		if s.Approve(ccToolName(name), ccLabel(name, input)) {
			reply(map[string]any{"behavior": "allow", "updatedInput": req["input"]})
		} else {
			reply(map[string]any{"behavior": "deny", "message": "The user didn't allow this. Ask them, or try another way."})
		}
	default:
		_ = c.write(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": id, "error": "rux doesn't handle " + fmt.Sprint(req["subtype"])}})
	}
}

func (c *ClaudeCode) deadErr(err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return c.dead
	}
	if err != nil {
		return err
	}
	return errors.New("Claude Code stopped")
}

// ccToolName shows mcp__rux__spawn_agent as spawn_agent.
func ccToolName(n string) string {
	if strings.HasPrefix(n, "mcp__") {
		if i := strings.LastIndex(n, "__"); i > 4 {
			return n[i+2:]
		}
	}
	return n
}

func ccLabel(name string, input []byte) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	for _, k := range []string{"command", "file_path", "pattern", "path", "url", "query", "name", "agent", "description", "prompt"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	if name == "TodoWrite" {
		return "update todos"
	}
	return ""
}

func toolResultText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				} else if m["type"] == "image" {
					parts = append(parts, "[image]")
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func lastLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

type limitWriter struct {
	w interface{ WriteString(string) (int, error) }
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.WriteString(string(p[:k]))
		l.n -= k
	}
	return len(p), nil
}
