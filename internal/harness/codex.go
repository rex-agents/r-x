package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Codex runs OpenAI's Codex CLI headless (codex exec --json) as an agent's engine, signed
// in the way Codex is: with ChatGPT or an API key. Each turn is one codex exec run that
// resumes the agent's thread, and Codex runs its own tools where it runs.
type Codex struct {
	// Command starts codex with these arguments, here or in a guest; the prompt goes to stdin.
	Command func(args []string) *exec.Cmd
	Model   string
	// Effort is low, medium, high, xhigh or max (max runs as xhigh).
	Effort string
	// System is sent ahead of the first message, since codex exec has no system prompt flag.
	System string
	// Bypass skips Codex's sandbox and approvals (in a rox guest, which is the sandbox).
	// Without it, Codex works in its read-only sandbox and never asks for approval.
	Bypass bool
	// Config are extra -c key=value overrides, such as MCP servers.
	Config []string

	mu     sync.Mutex
	thread string
	cmd    *exec.Cmd
}

func (c *Codex) Name() string {
	if c.Model == "" {
		return "codex"
	}
	return "codex/" + c.Model
}

// Thread is the Codex thread this agent resumes, empty before its first turn.
func (c *Codex) Thread() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.thread
}

// Resume continues an earlier Codex thread instead of starting a new one.
func (c *Codex) Resume(id string) {
	c.mu.Lock()
	c.thread = id
	c.mu.Unlock()
}

func (c *Codex) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
	}
}

func (c *Codex) args() []string {
	var args []string
	if c.thread == "" {
		args = []string{"exec", "--json", "--skip-git-repo-check"}
	} else {
		args = []string{"exec", "resume", c.thread, "--json", "--skip-git-repo-check"}
	}
	if c.Bypass {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	} else {
		args = append(args, "-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`)
	}
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	if c.Effort != "" {
		e := c.Effort
		if e == "max" {
			e = "xhigh"
		}
		args = append(args, "-c", `model_reasoning_effort="`+e+`"`)
	}
	for _, kv := range c.Config {
		args = append(args, "-c", kv)
	}
	return append(args, "-")
}

func (c *Codex) RunTurn(ctx context.Context, text string, s Sink) (string, error) {
	c.mu.Lock()
	first := c.thread == ""
	args := c.args()
	c.mu.Unlock()
	if first && c.System != "" {
		text = c.System + "\n\n---\n\n" + text
	}
	cmd := c.Command(args)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(text)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 8192}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting Codex: %v", err)
	}
	c.mu.Lock()
	c.cmd = cmd
	c.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		time.AfterFunc(3*time.Second, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	})
	defer stop()

	var final, failure string
	done := false
	started := map[string]bool{}
	lastText := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			Type     string          `json:"type"`
			ThreadID string          `json:"thread_id"`
			Message  string          `json:"message"`
			Item     json.RawMessage `json:"item"`
			Usage    struct {
				Input  int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			c.mu.Lock()
			c.thread = ev.ThreadID
			c.mu.Unlock()
		case "item.started", "item.updated", "item.completed":
			var it codexItem
			if json.Unmarshal(ev.Item, &it) != nil {
				continue
			}
			completed := ev.Type == "item.completed"
			switch it.Type {
			case "agent_message":
				if completed && it.Text != "" {
					t := it.Text
					if lastText {
						t = "\n\n" + t
					}
					s.Delta(Delta{Kind: "text", Text: t})
					final, lastText = it.Text, true
				}
			case "reasoning":
				if completed && it.Text != "" {
					s.Delta(Delta{Kind: "thinking", Text: it.Text})
					lastText = false
				}
			case "error":
				if completed {
					failure = it.Message
				}
			default:
				name, label := it.describe()
				if name == "" {
					continue
				}
				if !started[it.ID] {
					started[it.ID] = true
					s.ToolStart(it.ID, name, label)
					lastText = false
				}
				if completed {
					res, isErr := it.result()
					s.ToolDone(it.ID, res, isErr)
				}
			}
		case "turn.completed":
			s.Usage(Usage{Input: ev.Usage.Input, Output: ev.Usage.Output, CacheRead: ev.Usage.Cached})
			done = true
		case "turn.failed":
			failure, done = cmpStr(ev.Error.Message, "the turn failed"), true
		case "error":
			failure = ev.Message
		}
	}
	err = cmd.Wait()
	c.mu.Lock()
	c.cmd = nil
	c.mu.Unlock()
	switch {
	case ctx.Err() != nil:
		return final, ErrInterrupted
	case failure != "":
		return final, fmt.Errorf("Codex: %s", failure)
	case !done:
		msg := lastLineOf(stderr.String())
		return final, fmt.Errorf("Codex stopped (%v)%s", err, map[bool]string{true: ": " + msg, false: ""}[msg != ""])
	}
	return final, nil
}

type codexItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Message          string          `json:"message"`
	Command          string          `json:"command"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Status           string          `json:"status"`
	Changes          []codexChange   `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            json.RawMessage `json:"error"`
	Query            string          `json:"query"`
	Items            []struct {
		Text      string `json:"text"`
		Completed bool   `json:"completed"`
	} `json:"items"`
}

type codexChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// codexShell matches the shell wrapper Codex puts around commands.
var codexShell = regexp.MustCompile(`^\S*(?:ba|z)?sh -lc '(.*)'$`)

func (it *codexItem) describe() (string, string) {
	switch it.Type {
	case "command_execution":
		cmd := it.Command
		if m := codexShell.FindStringSubmatch(cmd); m != nil {
			cmd = strings.ReplaceAll(m[1], `'\''`, `'`)
		}
		return "shell", cmd
	case "file_change":
		var paths []string
		for _, ch := range it.Changes {
			paths = append(paths, ch.Path)
		}
		return "edit", strings.Join(paths, ", ")
	case "mcp_tool_call":
		return it.Tool, ccLabel(it.Tool, it.Arguments)
	case "web_search":
		return "web_search", it.Query
	case "todo_list":
		return "todos", fmt.Sprintf("%d items", len(it.Items))
	}
	return "", ""
}

func (it *codexItem) result() (string, bool) {
	failed := it.Status == "failed" || it.Status == "declined"
	switch it.Type {
	case "command_execution":
		out := it.AggregatedOutput
		if it.ExitCode != nil && *it.ExitCode != 0 {
			return fmt.Sprintf("exit code %d\n%s", *it.ExitCode, out), true
		}
		return out, failed
	case "file_change":
		var lines []string
		for _, ch := range it.Changes {
			lines = append(lines, ch.Kind+" "+ch.Path)
		}
		return strings.Join(lines, "\n"), failed
	case "mcp_tool_call":
		if len(it.Error) > 0 && string(it.Error) != "null" {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(it.Error, &e)
			return cmpStr(e.Message, string(it.Error)), true
		}
		var r struct {
			Content any `json:"content"`
		}
		_ = json.Unmarshal(it.Result, &r)
		return toolResultText(r.Content), failed
	case "todo_list":
		var lines []string
		for _, t := range it.Items {
			mark := "☐"
			if t.Completed {
				mark = "☑"
			}
			lines = append(lines, mark+" "+t.Text)
		}
		return strings.Join(lines, "\n"), false
	}
	return "", failed
}
