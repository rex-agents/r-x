package rux

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/harness"
)

// The runner owns one agent process per thread and speaks Claude Code's stream-json to it:
// user turns in, assistant/user/result events out. `rux codex-bridge` is that process for a
// Codex thread. It takes the same user lines, runs each as a Codex turn (the harness's Codex
// engine, as `rux agent` and the harness use), and writes the turn back as the events the
// web app already renders.

// agentFor says which agent CLI runs a model setting, and which model it gets. The names
// are the harness's: "codex[/MODEL]" is Codex, "claude-code[/MODEL]" or a bare Claude
// model (opus, sonnet, claude-opus-5-5) is Claude Code.
func agentFor(spec string) (cli, model string) {
	if cli, model, ok := agentCLI(spec); ok {
		return cli, model
	}
	return "claude", strings.TrimSpace(spec)
}

func runCodexBridge(args []string) {
	fs := flag.NewFlagSet("codex-bridge", flag.ExitOnError)
	model := fs.String("model", "", "codex -m")
	effort := fs.String("effort", "", "codex reasoning effort")
	system := fs.String("system", "", "instructions sent ahead of the first message")
	state := fs.String("state", "", "file that keeps the Codex thread ID across restarts")
	bin := fs.String("codex", "codex", "codex binary")
	fs.Parse(args)

	c := &harness.Codex{
		Model: *model, Effort: *effort, System: *system, Bypass: true, // the guest is the sandbox
		Command: func(a []string) *exec.Cmd { return exec.Command(*bin, a...) },
	}
	if *state != "" {
		if b, err := os.ReadFile(*state); err == nil {
			c.Resume(strings.TrimSpace(string(b)))
		}
	}
	saved := c.Thread()
	save := func() {
		if id := c.Thread(); *state != "" && id != "" && id != saved {
			saved = id
			os.WriteFile(*state, []byte(id+"\n"), 0o600)
		}
	}
	// SIGINT (the app's stop button) ends the turn in progress, not the bridge.
	intr := make(chan os.Signal, 1)
	signal.Notify(intr, os.Interrupt)
	var mu sync.Mutex
	var cancel context.CancelFunc
	go func() {
		for range intr {
			mu.Lock()
			if cancel != nil {
				cancel()
			}
			mu.Unlock()
		}
	}()
	setCancel := func(f context.CancelFunc) { mu.Lock(); cancel = f; mu.Unlock() }
	if err := bridgeLoop(os.Stdin, os.Stdout, c, *model, setCancel, save); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// bridgeLoop runs each user line from in as a Codex turn and writes its events to out.
func bridgeLoop(in io.Reader, out io.Writer, c *harness.Codex, model string, setCancel func(context.CancelFunc), saved func()) error {
	var wmu sync.Mutex
	emit := func(v map[string]any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		out.Write(append(b, '\n'))
		wmu.Unlock()
	}
	emit(map[string]any{"type": "system", "subtype": "init", "model": cmpOr(model, "codex")})
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), wsReadLimit)
	for sc.Scan() {
		text := userText(sc.Bytes())
		if text == "" {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		setCancel(cancel)
		start := time.Now()
		sink := &bridgeSink{emit: emit}
		final, err := c.RunTurn(ctx, text, sink)
		cancel()
		setCancel(nil)
		saved()
		res := map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": final,
			"duration_ms": time.Since(start).Milliseconds(), "num_turns": 1}
		if err != nil {
			res["subtype"], res["is_error"] = "error_during_execution", true
			if err == harness.ErrInterrupted {
				res["subtype"] = "interrupted"
			} else {
				res["result"] = err.Error()
				emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
					"content": []any{map[string]any{"type": "text", "text": "Codex failed: " + err.Error()}}}})
			}
		}
		emit(res)
	}
	return sc.Err()
}

// bridgeSink writes a Codex turn as Claude Code stream-json events.
type bridgeSink struct{ emit func(map[string]any) }

func (b *bridgeSink) assistant(part map[string]any) {
	b.emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{part}}})
}

func (b *bridgeSink) Delta(d harness.Delta) {
	if d.Kind == "thinking" {
		b.assistant(map[string]any{"type": "thinking", "thinking": d.Text})
		return
	}
	b.assistant(map[string]any{"type": "text", "text": d.Text})
}

func (b *bridgeSink) ToolStart(id, name, label string) {
	key := "command"
	if name != "shell" {
		key = "target"
	}
	b.assistant(map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{key: label}})
}

func (b *bridgeSink) ToolDone(id, result string, isError bool) {
	b.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isError,
			"content": []any{map[string]any{"type": "text", "text": result}}}}}})
}

func (b *bridgeSink) Usage(harness.Usage)             {}
func (b *bridgeSink) Approve(tool, label string) bool { return true }
