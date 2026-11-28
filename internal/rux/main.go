// rux runs Claude Code agents in sandboxed Rox guests, driven from a mobile web app.
//
//	rux         - the authority TUI: the pack's hosts, projects, threads and sub-agents (starts the local authority)
//	rux agent [PROMPT] - the harness: a lead agent that runs sub-agents in rox guests, in this context
//	rux agent spawn|ls|read|send|wait|focus|stop - sub-agents in Rox guests, in Rex panes, for an orchestrating Claude Code or Codex session
//	rux lead PROMPT - Claude Code as the lead agent in this Rex pane
//	rux serve   - coordinator + host agent: web app, thread store, launches guests
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
	switch route(os.Args[1:]) {
	case routeTUI:
		tuiFn()
		return 0
	case routeDeprecated:
		rest := strings.Join(os.Args[1:], " ")
		fmt.Fprintf(os.Stderr, "rux: `rux %s` is deprecated; use `rux agent %s`.\n", rest, rest)
		harnessFn(os.Args[1:])
		return 0
	}
	switch os.Args[1] {
	case "lead", "claude":
		// Claude Code as the lead, with sub-agents in Rex panes.
		runLead(os.Args[2:])
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
	case "codex-bridge":
		runCodexBridge(os.Args[2:])
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
	case "theme":
		runTheme(os.Args[2:])
	case "agent":
		if hasFlag(os.Args[2:], "--headless") {
			runHeadless(without(os.Args[2:], "--headless")) // the harness without a UI, in stream-json
		} else if isAgentSubcommand(os.Args[2:]) {
			agentCmdFn(os.Args[2:])
		} else {
			harnessFn(os.Args[2:]) // rux agent [PROMPT] [--model ...]: the harness, in this context
		}
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
		fmt.Fprintf(os.Stderr, "rux: `rux PROMPT` is deprecated; use `rux agent PROMPT`.\n")
		harnessFn(os.Args[1:])
	}
	return 0
}

type routeKind int

const (
	routeCommand    routeKind = iota // a named command: the switch in Main handles it
	routeTUI                         // bare rux: the authority TUI
	routeDeprecated                  // rux PROMPT... or rux --flag ...: the harness, with a notice
)

// route decides how the arguments after "rux" are handled.
func route(args []string) routeKind {
	switch {
	case len(args) == 0:
		return routeTUI
	case strings.HasPrefix(args[0], "-") && args[0] != "--version" && args[0] != "-h" && args[0] != "--help":
		return routeDeprecated
	}
	return routeCommand
}

// agentSubcommands are the first arguments of rux agent that are commands, not a prompt.
var agentSubcommands = map[string]bool{
	"spawn": true, "ls": true, "list": true, "read": true, "send": true, "wait": true,
	"focus": true, "stop": true, "pane": true, "type": true,
}

// isAgentSubcommand reports whether rux agent args name a subcommand (spawn, ls, ...);
// anything else, including no arguments, is the harness: flags and an optional prompt.
func isAgentSubcommand(args []string) bool { return len(args) > 0 && agentSubcommands[args[0]] }

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
		{"The authority", []cmd{
			{"rux", "open the authority TUI: the pack's hosts, projects, threads and sub-agents (starts the local authority)"},
		}},
		{"The harness", []cmd{
			{"rux agent [PROMPT]", "talk to a lead agent that runs sub-agents in rox guests, all in one window, in this context"},
			{"  --model PROVIDER/MODEL", "the lead's model: claude-code[/MODEL], codex[/MODEL], anthropic/…, openai/…, ollama/…"},
			{"  --agent-model PROVIDER/MODEL", "the sub-agents' model (default: the lead's)"},
			{"  --effort LEVEL", "low, medium, high, xhigh or max"},
			{"rux view [NAME]", "show an agent of the running rux session and talk to it (rux opens it with /pane)"},
		}},
		{"Claude Code as the lead, in Rex", []cmd{
			{"rux claude [PROMPT]", "make this Rex pane Claude Code, with sub-agents in panes beside it (alias: lead)"},
			{"rux agent spawn|ls|read|send|wait|focus|stop", "run sub-agents in Rex panes, for an orchestrating Claude Code or Codex"},
			{"rux skill install|show", "install or print the rux skill for Claude Code or Codex"},
		}},
		{"Threads from your phone", []cmd{
			{"rux quorum create|invite|join HOST CODE|leave|status|promote HOST|policy", "join authorities into a pack (see docs/rux/quorum.md)"},
			{"rux authority start|stop|status|logs", "the daemon: web app, threads in rox guests, model proxy (rux serve is a hidden alias of start --foreground)"},
			{"rux authority rename|mascot", "name your authority; draw a custom mascot from a description (--reset: back to the T-rex)"},
			{"rux theme [use PRESET|set TOKEN=#hex...|reset]", "show or change how every rux screen looks (web app and TUIs); works from a thread too"},
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
	fmt.Fprintf(w, "\n%s\n", d("rux PROMPT and rux --model ... still work, with a deprecation notice: use rux agent."))
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

// The entry points Main dispatches to, as variables so tests can observe the routing.
var (
	tuiFn      = runAuthorityTUI
	harnessFn  = runHarness
	agentCmdFn = runAgentCmd
)
