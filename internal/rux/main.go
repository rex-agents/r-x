// rux runs Claude Code agents in sandboxed Rox guests, driven from a mobile web app.
//
//	rux         - Claude Code as the lead agent in this Rex pane, which runs sub-agents with rux agent
//	rux lead PROMPT - the same, starting on PROMPT
//	rux monitor - sub-agents, guests and the host, from Rex
//	rux serve   - coordinator + host agent: web app, thread store, launches guests
//	rux agent   - sub-agents in Rox guests, in Rex panes, for an orchestrating Claude Code or Codex session
//	rux skill   - install the rux skill that teaches Claude Code or Codex to orchestrate with rux agent
//	rux runner  - runs inside a guest: owns the claude process and dials home
//	rux git-credential - git credential helper the runner sets up for its children
//	rux share / rux screenshot - used by claude in a guest to show the user files and pages
package rux

import (
	"fmt"
	"os"
	"strings"
)

// Version is this build's release, such as v0.2.0, or "development".
var Version = "development"

// Main runs rux with args, the command line without the program name.
func Main(version string, args []string) int {
	Version = version
	os.Args = append([]string{"rux"}, args...)
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") && os.Args[1] != "--version" {
		runHarness(os.Args[1:])
		return 0
	}
	switch os.Args[1] {
	case "lead", "claude":
		// Claude Code as the lead, with sub-agents in Rex panes.
		runLead(os.Args[2:])
	case "monitor":
		runMonitor()
	case "statusline":
		runStatusLine()
	case "--version", "version":
		fmt.Println("rux", Version)
	case "serve":
		runServe(os.Args[2:])
	case "runner":
		runRunner(os.Args[2:])
	case "git-credential":
		runGitCredential(os.Args[2:])
	case "share":
		runShare(os.Args[2:])
	case "screenshot":
		runScreenshot(os.Args[2:])
	case "ui":
		runUI(os.Args[2:])
	case "diff":
		runDiff(os.Args[2:])
	case "action", "tool":
		runTool(os.Args[2:])
	case "sandbox":
		runSandbox(os.Args[2:])
	case "agent":
		runAgentCmd(os.Args[2:])
	case "skill":
		runSkill(os.Args[2:])
	case "help":
		usage()
	default:
		// rux PROMPT... opens the harness on that prompt.
		runHarness(os.Args[1:])
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rux [--model P/M] [PROMPT] | rux claude [PROMPT] | rux monitor | rux serve [flags] | rux agent COMMAND | rux skill install | rux runner [flags]  (-h for flags)")
	os.Exit(2)
}
