package rux

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// rux with no command makes this Rex pane Claude Code, as the lead agent. You talk to it
// as you would to any Claude Code session. It delegates the work to sub-agents with rux
// agent spawn: each is Claude Code or Codex itself, in its own rox guest and Rex pane, and
// the lead watches them and reports back. A rux monitor pane below shows the stack, and
// Claude Code's status line shows the sub-agents' states.

const leadBrief = `You are the lead agent in rux. You're running in a Rex pane on the user's machine, and the
work happens in sub-agents you start with rux. Each sub-agent is Claude Code or Codex itself, in
its own sandboxed rox guest, in a Rex pane that opens beside yours. The user can watch and type
into every pane. The rux skill has the details; follow it.

How to work:
- Talk with the user like any Claude Code session. Read code, explore and plan here as you like,
  but delegate changes to sub-agents: they run in sandboxes with the repo cloned, and push their
  work to branches named rux/ID. Only the user merges.
- Before you start sub-agents, settle what matters and is unclear, in one short question with
  your suggested defaults: the repo (default: this directory's origin; or owner/name, or none),
  the branch to start from, where they run (a new guest: --guest-type container (default), linux,
  macos, fly or railway; or an existing one from rox ls --json: --guest NAME), and the agent
  (--agent claude (default) or codex). Don't ask about anything the user already said.
- Start each with: rux agent spawn --name SHORT-NAME [options] --prompt-file - <<'EOF' ... EOF
  Write prompts an agent with no other context can finish: the goal, where to look, constraints,
  and how to check the work. Start only as many agents as the work needs.
- Then follow them: rux agent wait (blocks until they stop working), rux agent read ID,
  rux agent send ID "...", rux agent ls. Report back briefly: what each did, its branch, and
  anything it needs from the user.
- Stop agents with rux agent stop ID once the user has what they need. Ask first if a guest
  might hold work that isn't pushed.
- Credentials (Claude Code, Codex, GitHub, secrets) come from rix. Never ask for or handle tokens.`

// leadTools may run without a permission prompt: rux's own commands and read-only status.
var leadTools = []string{
	"Bash(rux agent:*)", "Bash(rux monitor:*)", "Bash(rox ls:*)", "Bash(rox inspect:*)",
	"Bash(rix status:*)", "Bash(rex hosts:*)", "Bash(git remote:*)", "Bash(git status:*)", "Bash(git log:*)",
}

func runLead(args []string) {
	fs := flag.NewFlagSet("rux", flag.ExitOnError)
	noMonitor := fs.Bool("no-monitor", false, "don't open the rux monitor pane")
	model := fs.String("model", "", "model for the lead Claude Code")
	fs.Parse(args)
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))

	sess, block := os.Getenv("REX_SESSION"), os.Getenv("REX_BLOCK")
	if sess == "" || block == "" {
		fail("rux runs Claude Code as your lead agent in Rex, so its sub-agents can open panes beside it. Run it in a Rex terminal")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		fail("rux's lead agent is Claude Code, which isn't installed. Install it: %s", "https://code.claude.com/docs/en/setup")
	}
	refreshSkill()
	dir := sessionDir(sess)
	_ = os.MkdirAll(dir, 0o700)
	_ = saveJSON(filepath.Join(dir, ".lead"), map[string]string{"block": block})
	if !*noMonitor {
		openMonitor(sess, block, dir)
	}

	settings, _ := json.Marshal(map[string]any{
		"statusLine": map[string]any{"type": "command", "command": shQuote(selfExe()) + " statusline", "padding": 0},
	})
	argv := []string{"claude"}
	if prompt != "" {
		argv = append(argv, prompt)
	}
	argv = append(argv, "-n", "rux", "--append-system-prompt", leadBrief,
		"--allowedTools", strings.Join(leadTools, ","), "--settings", string(settings))
	if *model != "" {
		argv = append(argv, "--model", *model)
	}
	if err := syscall.Exec(claude, argv, os.Environ()); err != nil {
		fail("starting Claude Code: %v", err)
	}
}

// openMonitor opens rux monitor in a short pane below the lead, once per session.
func openMonitor(sess, block, dir string) {
	state := filepath.Join(dir, ".monitor")
	var mon struct{ Block string }
	if loadJSON(state, &mon) == nil && mon.Block != "" && rexBlockAlive(sess, mon.Block) {
		return
	}
	var out struct {
		BlockIDs []string `json:"block_ids"`
	}
	err := rexJSON(&out, "split", "-s", sess, "-b", block, "--split=below", "--ratio", "30", "--label", "rux monitor",
		"--focus=false", "--json", "--", selfExe(), "monitor")
	if err == nil && len(out.BlockIDs) > 0 {
		_ = saveJSON(state, map[string]string{"block": out.BlockIDs[0]})
	}
}

// refreshSkill installs or updates the rux skill for Claude Code when it differs from this
// build's, so the lead always reads the commands this rux has.
func refreshSkill() {
	home, _ := os.UserHomeDir()
	dest := filepath.Join(home, ".claude", "skills", "rux", "SKILL.md")
	want, _ := fs.ReadFile(skillFS, "skills/rux/SKILL.md")
	have, err := os.ReadFile(dest)
	if err == nil && bytes.Equal(have, want) {
		return
	}
	if err == nil && !bytes.Contains(have, []byte("name: rux")) {
		return // someone else's skill folder; rux skill install --force replaces it
	}
	cmd := exec.Command(selfExe(), "skill", "install", "--agent", "claude")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Run()
}

// runStatusLine prints Claude Code's status line for the lead: the sub-agents' states.
func runStatusLine() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	sess := os.Getenv("REX_SESSION")
	counts := map[string]int{}
	live := 0
	for _, a := range listAgents(sess) {
		if !agentLive(a) || a.Phase == "failed" && time.Since(cmpTime(a.Started, a.Created)) > time.Hour {
			continue
		}
		a.refresh()
		live++
		switch {
		case a.Phase == "provisioning" || a.Phase == "connecting":
			counts["starting"]++
		case a.Phase == "failed" || a.state == "error":
			counts["failed"]++
		case a.state == "working":
			counts["working"]++
		case a.state == "idle" || strings.HasPrefix(a.state, "blocked"):
			counts["waiting"]++
		case a.state == "done":
			counts["done"]++
		}
	}
	const (
		purple = "\x1b[38;5;141m"
		green  = "\x1b[38;5;78m"
		yellow = "\x1b[38;5;221m"
		red    = "\x1b[38;5;203m"
		dim    = "\x1b[2m"
		reset  = "\x1b[0m"
	)
	line := purple + "◆ rux" + reset
	if live == 0 {
		fmt.Println(line + dim + "  no sub-agents yet" + reset)
		return
	}
	parts := []string{fmt.Sprintf("%d sub-agent%s", live, plural(live))}
	add := func(n int, color, text string) {
		if n > 0 {
			parts = append(parts, color+fmt.Sprintf("%d %s", n, text)+reset)
		}
	}
	add(counts["working"], green, "working")
	add(counts["waiting"], yellow, "waiting for you")
	add(counts["starting"], dim, "starting")
	add(counts["done"], green, "done")
	add(counts["failed"], red, "failed")
	fmt.Println(line + "  " + strings.Join(parts, dim+" · "+reset))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// leadBlock is the lead's pane in sess, if rux started one.
func leadBlock(sess string) string {
	var lead struct{ Block string }
	if loadJSON(filepath.Join(sessionDir(sess), ".lead"), &lead) == nil {
		return lead.Block
	}
	return ""
}
