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
	"io"
	"os"
	"regexp"
	"strings"
)

// Version is this build's release, such as v0.2.0, or "development".
var Version = "development"

// Main runs rux with args, the command line without the program name.
func Main(version string, args []string) int {
	Version = version
	os.Args = append([]string{"rux"}, args...)
	if len(os.Args) == 2 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		help(os.Stdout)
		return 0
	}
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
	case "view":
		runView(os.Args[2:])
	case "statusline":
		runStatusLine()
	case "--version", "version":
		fmt.Println("rux", Version)
	case "quorum":
		runQuorum(os.Args[2:])
	case "headless":
		runHeadless(os.Args[2:])
	case "authority":
		runAuthority(os.Args[2:])
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
	case "help", "--help", "-h":
		help(os.Stdout)
		return 0
	default:
		// rux PROMPT... opens the harness on that prompt. A lone word is more likely a
		// mistyped command than a prompt.
		if len(os.Args) == 2 && commandLike.MatchString(os.Args[1]) {
			fmt.Fprintf(os.Stderr, "rux: there's no command %q. To start on a one-word prompt, add a word or quote it with a space.\n\n", os.Args[1])
			usage()
		}
		runHarness(os.Args[1:])
	}
	return 0
}

var commandLike = regexp.MustCompile(`^[a-z][a-z-]*$`)

func usage() {
	help(os.Stderr)
	os.Exit(2)
}

// help lists every rux command.
func help(w io.Writer) {
	b := func(s string) string { return "\x1b[1m" + s + "\x1b[0m" }
	d := func(s string) string { return "\x1b[2m" + s + "\x1b[0m" }
	if os.Getenv("NO_COLOR") != "" || !isTTY(w) {
		b = func(s string) string { return s }
		d = b
	}
	type cmd struct{ use, what string }
	groups := []struct {
		title string
		cmds  []cmd
	}{
		{"The harness", []cmd{
			{"rux [PROMPT]", "talk to a lead agent that runs sub-agents in rox guests, all in one window"},
			{"  --model PROVIDER/MODEL", "the lead's model: claude-code[/MODEL], codex[/MODEL], anthropic/…, openai/…, ollama/…"},
			{"  --agent-model PROVIDER/MODEL", "the sub-agents' model (default: the lead's)"},
			{"  --effort LEVEL", "low, medium, high, xhigh or max"},
			{"rux view [NAME]", "show an agent of the running rux session and talk to it (rux opens it with /pane)"},
		}},
		{"Claude Code as the lead, in Rex", []cmd{
			{"rux claude [PROMPT]", "make this Rex pane Claude Code, with sub-agents in panes beside it (alias: lead)"},
			{"rux monitor", "watch this Rex session's sub-agents, guests and host"},
			{"rux agent spawn|ls|read|send|wait|focus|stop", "run sub-agents in Rex panes (rux agent for details)"},
			{"rux skill install|show", "install or print the rux skill for Claude Code or Codex"},
		}},
		{"Threads from your phone", []cmd{
			{"rux quorum create|invite|join HOST CODE|leave|status|promote HOST|policy", "join authorities into a pack (see docs/rux/quorum.md)"},
			{"rux authority start|stop|status|logs", "the daemon: web app, threads in rox guests, model proxy (rux serve is a hidden alias of start --foreground)"},
		}},
		{"Tools for agents", []cmd{
			{"rux share FILE... [--caption TEXT]", "show files in the thread (images and videos inline)"},
			{"rux screenshot URL", "screenshot a page and share it in the thread"},
			{"rux ui FILE|-", "show a rich UI (charts, tables, forms) in the thread; --catalog lists components"},
			{"rux diff [GIT DIFF ARGS]", "show a diff in the thread (default: uncommitted changes)"},
			{"rux action NAME [--ARG VALUE]...", "run an action from tool.json, with secrets from rix (alias: tool)"},
			{"rux sandbox DIR -- CMD", "run CMD with no network and writes only in DIR (macOS)"},
		}},
		{"Other", []cmd{
			{"rux version", "print the version"},
			{"rux help", "this help"},
		}},
		{"Used by rux itself", []cmd{
			{"rux runner", "the process in each guest that runs a thread"},
			{"rux statusline", "Claude Code's status line for rux claude"},
			{"rux git-credential", "the Git credential helper guests use"},
		}},
	}
	width := 0
	for _, g := range groups {
		for _, c := range g.cmds {
			width = max(width, len(c.use))
		}
	}
	fmt.Fprintf(w, "%s %s\n", b("rux"), d(Version+" · a multi-agent harness on rox and rix"))
	for _, g := range groups {
		fmt.Fprintf(w, "\n%s\n", b(g.title+":"))
		for _, c := range g.cmds {
			fmt.Fprintf(w, "  %-*s  %s\n", width, c.use, d(c.what))
		}
	}
	fmt.Fprintf(w, "\nIn the harness, %s opens the commands, %s the shortcuts, and %s the model picker.\n", b("ctrl+o"), b("?"), b("ctrl+l"))
	fmt.Fprintf(w, "Credentials come from rix: %s\n", b("rix secret set ANTHROPIC_API_KEY")+d(", rix auth claude, rix auth github"))
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
