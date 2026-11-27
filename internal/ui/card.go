package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Field is one labelled line of a Card.
type Field struct {
	Label string
	Value string
	// Note is dim text after the value.
	Note string
}

// Card prints a titled block of aligned fields to stdout:
//
//	● rv-web-3f2a9c1b7d4e  ready
//	  Workspace  rv-web-3f2a9c1b7d4e
//	  Session    session:01…
//
// Each field line reads "Label  value" after its indent, so other tools can parse it.
func Card(title, status string, fields []Field) {
	mu.Lock()
	defer mu.Unlock()
	clearSpinnerLocked()
	defer drawSpinnerLocked()
	if title != "" {
		head := styleAccent.Render("●") + " " + styleBold.Render(title)
		if status != "" {
			head += "  " + styleGood.Render(status)
		}
		fmt.Fprintln(stdOut, "\n"+head)
	}
	widest := 0
	for _, f := range fields {
		if f.Value != "" && len(f.Label) > widest {
			widest = len(f.Label)
		}
	}
	for _, f := range fields {
		if f.Value == "" {
			continue
		}
		label := f.Label + strings.Repeat(" ", widest-len(f.Label))
		line := "  " + styleLabel.Render(label) + "  " + f.Value
		if f.Note != "" {
			line += "  " + styleFaint.Render(f.Note)
		}
		fmt.Fprintln(stdOut, line)
	}
}

// Table prints rows with a header to stdout, as an aligned table on a terminal and as
// tab-separated values otherwise, so scripts can read it.
func Table(headers []string, rows [][]string) {
	mu.Lock()
	defer mu.Unlock()
	clearSpinnerLocked()
	defer drawSpinnerLocked()
	if !term.IsTerminal(int(os.Stdout.Fd())) || stdOut != os.Stdout {
		for _, row := range rows {
			fmt.Fprintln(stdOut, strings.Join(row, "\t"))
		}
		return
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && lipgloss.Width(cell) > widths[i] {
				widths[i] = lipgloss.Width(cell)
			}
		}
	}
	pad := func(cell string, w int) string {
		return cell + strings.Repeat(" ", max(0, w-lipgloss.Width(cell)))
	}
	var head []string
	for i, h := range headers {
		head = append(head, styleMuted.Render(pad(strings.ToUpper(h), widths[i])))
	}
	fmt.Fprintln(stdOut, "  "+strings.Join(head, "   "))
	total := 0
	for _, w := range widths {
		total += w + 3
	}
	fmt.Fprintln(stdOut, "  "+styleFaint.Render(strings.Repeat("─", max(0, total-3))))
	for _, row := range rows {
		var cells []string
		for i, cell := range row {
			if i < len(widths) {
				cells = append(cells, pad(cell, widths[i]))
			}
		}
		fmt.Fprintln(stdOut, "  "+strings.Join(cells, "   "))
	}
}

// Phase colors a guest or project phase for tables and cards.
func Phase(phase string) string {
	switch phase {
	case "ready", "running", "up", "active":
		return styleGood.Render(phase)
	case "failed", "missing", "down":
		return styleBad.Render(phase)
	case "stopped", "-":
		return styleMuted.Render(phase)
	default:
		return styleWarn.Render(phase)
	}
}

// IsTerminal reports whether stdin is a terminal, so prompts can be asked.
func IsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// Confirm asks a yes/no question that defaults to yes.
func Confirm(question string) bool {
	var answer string
	_ = Suspend(func() error {
		fmt.Fprint(errOut, "  "+styleAccent.Render("?")+" "+question+" "+styleMuted.Render("[Y/n]")+" ")
		answer, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return nil
	})
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "" || answer == "y" || answer == "yes"
}

// Ask reads a line of input after a prompt.
func Ask(question string) string {
	var answer string
	_ = Suspend(func() error {
		fmt.Fprint(errOut, "  "+styleAccent.Render("?")+" "+question+" ")
		answer, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return nil
	})
	return strings.TrimSpace(answer)
}

// Secret reads a value without echoing it, or a line from stdin when it's piped.
func Secret(prompt, help string) (string, error) {
	var value string
	err := Suspend(func() error {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && line == "" {
				return nil
			}
			value = line
			return nil
		}
		if help != "" {
			for _, line := range strings.Split(help, "\n") {
				fmt.Fprintln(errOut, "  "+styleMuted.Render(line))
			}
		}
		fmt.Fprint(errOut, "  "+styleAccent.Render("?")+" "+prompt+" ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(errOut)
		value = string(raw)
		return err
	})
	return strings.TrimSpace(value), err
}

// width is the terminal's width, or 100 when unknown.
func width() int {
	if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && w > 0 {
		return w
	}
	return 100
}
