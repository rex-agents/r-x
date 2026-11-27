package rux

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"

	"github.com/rex-agents/r-x/internal/harness"
)

// The harness's look: quiet greys, with the Rex icon's orange, pink and violet for accents.
var (
	hOrange  = lipgloss.Color("#ff9a3c")
	hPink    = lipgloss.Color("#f472b6")
	hViolet  = lipgloss.Color("#a78bfa")
	hAccent  = hPink
	hGreen   = lipgloss.Color("#4ade80")
	hYellow  = lipgloss.Color("#facc15")
	hRed     = lipgloss.Color("#f87171")
	hDim     = lipgloss.Color("244")
	hFaint   = lipgloss.Color("239")
	hText    = lipgloss.Color("252")
	stDim    = lipgloss.NewStyle().Foreground(hDim)
	stFaint  = lipgloss.NewStyle().Foreground(hFaint)
	stAccent = lipgloss.NewStyle().Foreground(hAccent)
	stBold   = lipgloss.NewStyle().Bold(true).Foreground(hText)
	stText   = lipgloss.NewStyle().Foreground(hText)
)

// transcriptRenderer draws an agent's transcript, for rux and for rux view.
type transcriptRenderer struct {
	spin    spinner.Model
	md      *glamour.TermRenderer
	mdWidth int
	cache   map[string]string
}

func newTranscriptRenderer() *transcriptRenderer {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(hPink)))
	return &transcriptRenderer{spin: sp, cache: map[string]string{}}
}

func (t *transcriptRenderer) markdown(s string, width int) string {
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

// render draws snap at width w. expandAll shows everything; expanded opens single tool
// calls by index. Tool calls are marked as zones prefix+index, so a click can open them.
func (t *transcriptRenderer) render(snap harness.Snapshot, w int, expandAll bool, expanded map[int]bool, prefix string) string {
	var out []string
	for i, e := range snap.Entries {
		last := i == len(snap.Entries)-1
		open := expandAll || expanded[i]
		switch e.Kind {
		case "user":
			from := cmpOr(e.From, "you")
			body := wrapText(e.Text, w-4)
			if from == "you" {
				out = append(out, stText.Render(hangIndent(body, stAccent.Render("❯ "), "  ")))
				continue
			}
			if !open && strings.Count(body, "\n") > 10 {
				body = strings.Join(strings.Split(body, "\n")[:10], "\n") + "\n" + stFaint.Render("…")
			}
			head := stDim.Render("◂ " + from)
			if !open {
				head = zone.Mark(fmt.Sprintf("%s%d", prefix, i), head)
			}
			out = append(out, head+"\n"+stText.Render(hangIndent(body, "  ", "  ")))
		case "text":
			text := strings.TrimSpace(e.Text)
			if text == "" {
				continue
			}
			var body string
			if last && snap.Status == harness.Working {
				body = wrapText(text, w-2) // still streaming
			} else {
				body = t.markdown(text, w-2)
			}
			out = append(out, hangIndent(strings.TrimLeft(body, " "), "  ", "  "))
		case "thinking":
			text := strings.TrimSpace(e.Text)
			if text == "" {
				if last && snap.Status == harness.Working {
					out = append(out, stDim.Italic(true).Render("  thinking…"))
				}
				continue
			}
			body := wrapText(text, w-4)
			if !open {
				lines := strings.Split(body, "\n")
				if len(lines) > 2 {
					body = "… " + strings.Join(lines[len(lines)-2:], "\n")
				}
			}
			out = append(out, zone.Mark(fmt.Sprintf("%s%d", prefix, i), stFaint.Italic(true).Render(hangIndent(body, "  ", "  "))))
		case "tool":
			dot := lipgloss.NewStyle().Foreground(hGreen).Render("●")
			switch {
			case e.Running:
				dot = t.spin.View()
			case e.IsError:
				dot = lipgloss.NewStyle().Foreground(hRed).Render("●")
			}
			label := strings.ReplaceAll(e.Label, "\n", " ")
			if lw := w - lipgloss.Width(e.Tool) - 8; lipgloss.Width(label) > lw && lw > 10 && !open {
				label = truncate(label, lw)
			}
			head := dot + " " + stBold.Render(e.Tool) + " " + stDim.Render(label)
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
					more = stFaint.Render(fmt.Sprintf("… %d more lines, click to open", len(lines)-show))
					lines = lines[:show]
				}
				for j := range lines {
					if !open {
						lines[j] = truncate(lines[j], w-8)
					}
				}
				st := stDim
				if e.IsError {
					st = lipgloss.NewStyle().Foreground(hRed)
				}
				body := st.Render(strings.Join(lines, "\n"))
				if open {
					body = st.Render(wrapText(strings.Join(lines, "\n"), w-6))
				}
				if more != "" {
					body += "\n" + more
				}
				if strings.TrimSpace(res) != "" {
					line += "\n" + hangIndent(body, stFaint.Render("  └ "), "    ")
				}
			}
			out = append(out, line)
		case "note":
			out = append(out, stFaint.Render(hangIndent(wrapText(e.Text, w-4), "  · ", "    ")))
		case "error":
			out = append(out, lipgloss.NewStyle().Foreground(hRed).Render(hangIndent(wrapText(e.Text, w-4), "  ✗ ", "    ")))
		}
	}
	switch {
	case snap.Status == harness.Working:
		detail := truncate(cmpOr(snap.Detail, "working"), max(w-24, 10))
		out = append(out, t.spin.View()+" "+stDim.Render(detail)+stFaint.Render("  esc to interrupt"))
	case snap.Status == harness.Starting:
		out = append(out, t.spin.View()+" "+stDim.Render(truncate(cmpOr(snap.Detail, "starting"), max(w-4, 10))))
	}
	return strings.Join(out, "\n\n") + "\n"
}

func wrapText(s string, w int) string {
	return lipgloss.NewStyle().Width(max(w, 10)).Render(s)
}

func hangIndent(s, first, rest string) string {
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

func truncate(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func modelShort(m string) string {
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		return m[i+1:]
	}
	return m
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

var claudeID = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-(\d+)-(\d+)$`)

// prettyModel names an engine for people: "Claude Code · Opus", "Codex", "Opus 5.5".
func prettyModel(spec string) string {
	title := func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	}
	if cli, model, ok := agentCLI(spec); ok {
		name := map[string]string{"claude": "Claude Code", "codex": "Codex"}[cli]
		if model == "" {
			return name
		}
		if cli == "claude" {
			model = title(model)
		}
		return name + " · " + model
	}
	m := modelShort(spec)
	if mm := claudeID.FindStringSubmatch(m); mm != nil {
		return title(mm[1]) + " " + mm[2] + "." + mm[3]
	}
	return m
}

var effortNames = map[string]string{"": "Auto", "low": "Low", "medium": "Medium", "high": "High", "xhigh": "Extra High", "max": "Max"}
var effortOrder = []string{"", "low", "medium", "high", "xhigh", "max"}
