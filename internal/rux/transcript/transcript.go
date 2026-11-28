// Package transcript draws an agent's transcript: the one look shared by the rux agent harness
// and the threads of the authority TUI. It holds the palette, the transcript renderer and a
// few text helpers.
package transcript

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/rex-agents/r-x/internal/harness"
)

// The look: quiet greys, with the Rex icon's orange, pink and violet for accents.
var (
	Orange   = lipgloss.Color("#ff9a3c")
	Pink     = lipgloss.Color("#f472b6")
	Violet   = lipgloss.Color("#a78bfa")
	Accent   = Pink
	Green    = lipgloss.Color("#4ade80")
	Yellow   = lipgloss.Color("#facc15")
	Red      = lipgloss.Color("#f87171")
	Dim      = lipgloss.Color("244")
	Faint    = lipgloss.Color("239")
	Text     = lipgloss.Color("252")
	StDim    = lipgloss.NewStyle().Foreground(Dim)
	StFaint  = lipgloss.NewStyle().Foreground(Faint)
	StAccent = lipgloss.NewStyle().Foreground(Accent)
	StBold   = lipgloss.NewStyle().Bold(true).Foreground(Text)
	StText   = lipgloss.NewStyle().Foreground(Text)
)

// Renderer draws an agent's transcript, for rux, rux view and the authority TUI's threads.
type Renderer struct {
	Spin spinner.Model
	// ReadOnly leaves out the hint that esc interrupts a working agent.
	ReadOnly bool
	md       *glamour.TermRenderer
	mdWidth  int
	cache    map[string]string
}

// New makes a renderer.
func New() *Renderer {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(Pink)))
	return &Renderer{Spin: sp, cache: map[string]string{}}
}

func (t *Renderer) markdown(s string, width int) string {
	if t.md == nil || t.mdWidth != width {
		style := styles.DarkStyleConfig
		zero := uint(0)
		style.Document.Margin = &zero
		style.Document.BlockPrefix, style.Document.BlockSuffix = "", ""
		style.Code.Prefix, style.Code.Suffix = "", ""
		t.md, _ = glamour.NewTermRenderer(glamour.WithStyles(style), glamour.WithWordWrap(max(width, 20)))
		t.mdWidth, t.cache = width, map[string]string{}
	}
	if out, ok := t.cache[s]; ok {
		return out
	}
	out := s
	if r, err := t.md.Render(s); err == nil {
		out = strings.Trim(r, "\n")
	}
	t.cache[s] = out
	return out
}

// Render draws snap at width w. expandAll shows everything; expanded opens single tool
// calls by index. Tool calls are marked as zones prefix+index, so a click can open them.
func (t *Renderer) Render(snap harness.Snapshot, w int, expandAll bool, expanded map[int]bool, prefix string) string {
	var out []string
	for i, e := range snap.Entries {
		last := i == len(snap.Entries)-1
		open := expandAll || expanded[i]
		switch e.Kind {
		case "user":
			from := cmp.Or(e.From, "you")
			body := WrapText(e.Text, w-4)
			if from == "you" {
				out = append(out, StText.Render(HangIndent(body, StAccent.Render("❯ "), "  ")))
				continue
			}
			if !open && strings.Count(body, "\n") > 10 {
				body = strings.Join(strings.Split(body, "\n")[:10], "\n") + "\n" + StFaint.Render("…")
			}
			head := StDim.Render("◂ " + from)
			if !open {
				head = zone.Mark(fmt.Sprintf("%s%d", prefix, i), head)
			}
			out = append(out, head+"\n"+StText.Render(HangIndent(body, "  ", "  ")))
		case "text":
			text := strings.TrimSpace(e.Text)
			if text == "" {
				continue
			}
			var body string
			if last && snap.Status == harness.Working {
				body = WrapText(text, w-2) // still streaming
			} else {
				body = t.markdown(text, w-2)
			}
			out = append(out, HangIndent(strings.TrimLeft(body, " "), "  ", "  "))
		case "thinking":
			text := strings.TrimSpace(e.Text)
			if text == "" {
				if last && snap.Status == harness.Working {
					out = append(out, StDim.Italic(true).Render("  thinking…"))
				}
				continue
			}
			body := WrapText(text, w-4)
			if !open {
				lines := strings.Split(body, "\n")
				if len(lines) > 2 {
					body = "… " + strings.Join(lines[len(lines)-2:], "\n")
				}
			}
			out = append(out, zone.Mark(fmt.Sprintf("%s%d", prefix, i), StFaint.Italic(true).Render(HangIndent(body, "  ", "  "))))
		case "tool":
			dot := lipgloss.NewStyle().Foreground(Green).Render("●")
			switch {
			case e.Running:
				dot = t.Spin.View()
			case e.IsError:
				dot = lipgloss.NewStyle().Foreground(Red).Render("●")
			}
			label := strings.ReplaceAll(e.Label, "\n", " ")
			if lw := w - lipgloss.Width(e.Tool) - 8; lipgloss.Width(label) > lw && lw > 10 && !open {
				label = Truncate(label, lw)
			}
			head := dot + " " + StBold.Render(e.Tool) + " " + StDim.Render(label)
			line := zone.Mark(fmt.Sprintf("%s%d", prefix, i), head)
			if !e.Running {
				res := strings.TrimRight(e.Result, "\n")
				lines := strings.Split(res, "\n")
				show := 3
				if open {
					show = 400
				}
				more := ""
				if len(lines) > show {
					more = StFaint.Render(fmt.Sprintf("… %d more lines, click to open", len(lines)-show))
					lines = lines[:show]
				}
				for j := range lines {
					if !open {
						lines[j] = Truncate(lines[j], w-8)
					}
				}
				st := StDim
				if e.IsError {
					st = lipgloss.NewStyle().Foreground(Red)
				}
				body := st.Render(strings.Join(lines, "\n"))
				if open {
					body = st.Render(WrapText(strings.Join(lines, "\n"), w-6))
				}
				if more != "" {
					body += "\n" + more
				}
				if strings.TrimSpace(res) != "" {
					line += "\n" + HangIndent(body, StFaint.Render("  └ "), "    ")
				}
			}
			out = append(out, line)
		case "note":
			out = append(out, StFaint.Render(HangIndent(WrapText(e.Text, w-4), "  · ", "    ")))
		case "error":
			out = append(out, lipgloss.NewStyle().Foreground(Red).Render(HangIndent(WrapText(e.Text, w-4), "  ✗ ", "    ")))
		}
	}
	switch {
	case snap.Status == harness.Working:
		detail := Truncate(cmp.Or(snap.Detail, "working"), max(w-24, 10))
		hint := ""
		if !t.ReadOnly {
			hint = StFaint.Render("  esc to interrupt")
		}
		out = append(out, t.Spin.View()+" "+StDim.Render(detail)+hint)
	case snap.Status == harness.Starting:
		out = append(out, t.Spin.View()+" "+StDim.Render(Truncate(cmp.Or(snap.Detail, "starting"), max(w-4, 10))))
	}
	return strings.Join(out, "\n\n") + "\n"
}

// WrapText wraps s to w columns.
func WrapText(s string, w int) string {
	return lipgloss.NewStyle().Width(max(w, 10)).Render(s)
}

// HangIndent prefixes the first line of s with first and the others with rest.
func HangIndent(s, first, rest string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = first + lines[i]
		} else {
			lines[i] = rest + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// Truncate cuts s to w columns, ending in … when it cut.
func Truncate(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// Opens reports whether a click on the entry opens it: tool calls, thinking and messages
// from other agents. Render marks these as zones.
func Opens(e harness.Entry) bool {
	return e.Kind == "tool" || e.Kind == "thinking" || (e.Kind == "user" && e.From != "you" && e.From != "")
}

// PromptBox draws the input (the rendered lines of a text field) in a rounded box, with
// who it's for at the top left, the model on its top border and where the agent works on
// its bottom border. The model may be empty. mark wraps it in a click zone.
func PromptBox(w int, border lipgloss.Style, left, model string, mark func(string) string, where, input string) string {
	top := border.Render("╭─")
	if left != "" {
		top += " " + left + " "
	}
	seg, segW := "", 0
	if model != "" {
		seg, segW = " "+mark(StAccent.Render(model))+" ", lipgloss.Width(model)+2
	}
	fill := w - lipgloss.Width(top) - segW - 2
	top += border.Render(strings.Repeat("─", max(fill, 1))) + seg + border.Render("─╮")
	bfill := w - lipgloss.Width(where) - 5
	bottom := border.Render("╰"+strings.Repeat("─", max(bfill, 1))) + " " + StDim.Render(where) + " " + border.Render("─╯")
	var mid []string
	for _, line := range strings.Split(input, "\n") {
		pad := w - 4 - lipgloss.Width(line)
		mid = append(mid, border.Render("│")+" "+line+strings.Repeat(" ", max(pad, 0))+" "+border.Render("│"))
	}
	return strings.Join(append(append([]string{top}, mid...), bottom), "\n")
}
