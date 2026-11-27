// Package ui renders rox and rix output: steps with spinners, notes, cards and tables.
//
// Subprocesses never write to the terminal directly. Their output is captured, and the
// user sees our own summary of each step instead: a spinner while it runs, then one line
// saying what happened. A failed step shows the last lines it captured, so a failure
// can still be diagnosed. --verbose (or ROX_VERBOSE=1) streams captured output, dimmed.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

var (
	mu      sync.Mutex
	errOut  io.Writer = os.Stderr
	stdOut  io.Writer = os.Stdout
	fancy             = detectFancy()
	verbose           = os.Getenv("ROX_VERBOSE") == "1" || os.Getenv("RIX_VERBOSE") == "1"
	active  *Step
	program = "rox"
)

// SetProgram names the binary in error and warning prefixes.
func SetProgram(name string) { program = name }

// Program is the binary's name.
func Program() string { return program }

// SetVerbose turns streaming of captured subprocess output on or off.
func SetVerbose(on bool) { verbose = on }

// Verbose reports whether captured output streams to the terminal.
func Verbose() bool { return verbose }

// Fancy reports whether stderr is a color terminal, so spinners and colors are used.
func Fancy() bool { return fancy }

// SetOutput redirects ui output, for tests. Redirected output is plain.
func SetOutput(stdout, stderr io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	stdOut, errOut = stdout, stderr
	fancy = false
	renderer.SetColorProfile(termenv.Ascii)
}

func detectFancy() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stderr.Fd()))
}

var renderer = func() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(os.Stderr)
	if !detectFancy() {
		r.SetColorProfile(termenv.Ascii)
	}
	// Decide light or dark from the environment. Asking the terminal sends escape queries,
	// which leak into pipes and stall over slow connections.
	r.SetHasDarkBackground(darkBackground())
	return r
}()

// darkBackground reads ROX_THEME (light or dark), then COLORFGBG, and assumes dark otherwise.
func darkBackground() bool {
	switch os.Getenv("ROX_THEME") {
	case "light":
		return false
	case "dark":
		return true
	}
	if fgbg := os.Getenv("COLORFGBG"); fgbg != "" {
		parts := strings.Split(fgbg, ";")
		switch parts[len(parts)-1] {
		case "7", "15":
			return false
		}
	}
	return true
}

// The palette. Adaptive colors read well on light and dark terminals.
var (
	accent  = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	good    = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"}
	caution = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	bad     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	muted   = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	faint   = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"}

	styleAccent = renderer.NewStyle().Foreground(accent)
	styleBold   = renderer.NewStyle().Bold(true)
	styleGood   = renderer.NewStyle().Foreground(good)
	styleWarn   = renderer.NewStyle().Foreground(caution)
	styleBad    = renderer.NewStyle().Foreground(bad)
	styleMuted  = renderer.NewStyle().Foreground(muted)
	styleFaint  = renderer.NewStyle().Foreground(faint)
	styleLabel  = renderer.NewStyle().Foreground(muted)
	styleCode   = renderer.NewStyle().Foreground(accent)
)

// Code styles a command or path the user might type or open.
func Code(s string) string { return styleCode.Render(s) }

// Bold styles s in bold.
func Bold(s string) string { return styleBold.Render(s) }

// Muted styles s as secondary text.
func Muted(s string) string { return styleMuted.Render(s) }

// Accent styles s in the accent color.
func Accent(s string) string { return styleAccent.Render(s) }

// Good styles s as a success.
func Good(s string) string { return styleGood.Render(s) }

// Warn styles s as a warning.
func WarnText(s string) string { return styleWarn.Render(s) }

// Bad styles s as a failure.
func Bad(s string) string { return styleBad.Render(s) }

const (
	iconDone = "✓"
	iconFail = "✗"
	iconWarn = "!"
	iconSkip = "○"
	iconInfo = "•"
	iconHead = "◆"
)

// writeLine prints a line to stderr, keeping the active spinner below it.
func writeLine(line string) {
	mu.Lock()
	defer mu.Unlock()
	clearSpinnerLocked()
	fmt.Fprintln(errOut, line)
	drawSpinnerLocked()
}

// Header opens a command's output: "◆ rox up  web (main) → container".
func Header(command string, detail string) {
	line := styleAccent.Render(iconHead) + " " + styleBold.Render(program+" "+command)
	if detail != "" {
		line += "  " + styleMuted.Render(detail)
	}
	writeLine(line)
}

// Blank prints an empty line on stderr.
func Blank() { writeLine("") }

// Done prints a finished step.
func Done(format string, args ...any) {
	writeLine("  " + styleGood.Render(iconDone) + " " + fmt.Sprintf(format, args...))
}

// Info prints a note.
func Info(format string, args ...any) {
	writeLine("  " + styleMuted.Render(iconInfo) + " " + fmt.Sprintf(format, args...))
}

// Skip prints a step that had nothing to do.
func Skip(format string, args ...any) {
	writeLine("  " + styleFaint.Render(iconSkip) + " " + styleMuted.Render(fmt.Sprintf(format, args...)))
}

// Warn prints a problem that doesn't stop the command.
func Warn(format string, args ...any) {
	writeLine("  " + styleWarn.Render(iconWarn) + " " + styleWarn.Render(fmt.Sprintf(format, args...)))
}

// Detail prints an indented line under the previous step.
func Detail(format string, args ...any) {
	writeLine("    " + styleFaint.Render("│") + " " + styleMuted.Render(fmt.Sprintf(format, args...)))
}

// Hint prints a suggested next command.
func Hint(text string) {
	writeLine("  " + styleFaint.Render("→") + " " + text)
}

// Say prints a plain line to stdout, for output that scripts may read.
func Say(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	clearSpinnerLocked()
	fmt.Fprintf(stdOut, format+"\n", args...)
	drawSpinnerLocked()
}

// Stdout is where results go.
func Stdout() io.Writer { return stdOut }

// Stderr is where progress goes.
func Stderr() io.Writer { return errOut }

// Failure prints a command's final error, with the captured output that explains it.
func Failure(err error) {
	mu.Lock()
	if active != nil {
		active.stopLocked()
	}
	mu.Unlock()
	message := err.Error()
	first, rest, _ := strings.Cut(message, "\n")
	writeLine(styleBad.Render(iconFail+" "+program+": ") + first)
	if rest != "" {
		for _, line := range strings.Split(strings.TrimRight(rest, "\n"), "\n") {
			writeLine("  " + styleMuted.Render(line))
		}
	}
	var out interface{ Output() string }
	if errors.As(err, &out) {
		Tail(out.Output(), 12)
	}
}

// Tail shows the last lines of captured output in a dim gutter.
func Tail(output string, lines int) {
	output = strings.TrimRight(StripANSI(output), "\n")
	if strings.TrimSpace(output) == "" {
		return
	}
	all := strings.Split(output, "\n")
	if len(all) > lines {
		writeLine("    " + styleFaint.Render(fmt.Sprintf("┆ … %d earlier lines", len(all)-lines)))
		all = all[len(all)-lines:]
	}
	for _, line := range all {
		writeLine("    " + styleFaint.Render("┆ ") + styleMuted.Render(truncate(line, 160)))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\a]*(\a|\x1b\\)|\r`)

// StripANSI removes terminal escapes, for parsing captured output.
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// Duration formats an elapsed time compactly: 0.8s, 41s, 2m05s.
func Duration(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// Plural returns "1 file" or "3 files".
func Plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
