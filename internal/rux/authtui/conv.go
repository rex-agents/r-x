package authtui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// evSummary is a decoded thread event, reduced to what the TUI shows.
type evSummary struct {
	kind  string // user, text, tool, result, error, note
	text  string
	tool  string
	turns int
	ms    int64
	cost  float64
	file  string
}

// summarize turns one stored event into zero or more summaries, like the web app's thread page.
func summarize(se StoredEvent) []evSummary {
	var ev struct {
		Type    string  `json:"type"`
		Subtype string  `json:"subtype"`
		Text    string  `json:"text"`
		Name    string  `json:"name"`
		Caption string  `json:"caption"`
		Quiet   bool    `json:"quiet"`
		IsError bool    `json:"is_error"`
		Dur     int64   `json:"duration_ms"`
		Turns   int     `json:"num_turns"`
		Cost    float64 `json:"total_cost_usd"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(se.Ev, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "rux_user":
		if strings.HasPrefix(ev.Text, "[rux] ") {
			return []evSummary{{kind: "note", text: strings.TrimPrefix(ev.Text, "[rux] ")}}
		}
		return []evSummary{{kind: "user", text: ev.Text}}
	case "rux_error":
		return []evSummary{{kind: "error", text: ev.Text}}
	case "rux_asset":
		if ev.Quiet {
			return nil
		}
		return []evSummary{{kind: "file", file: ev.Name, text: cmpOr(ev.Caption, ev.Name)}}
	case "rux_interrupt":
		return []evSummary{{kind: "note", text: "stopped"}}
	case "rux_asleep":
		return []evSummary{{kind: "note", text: "asleep · the guest is stopped until the next message"}}
	case "assistant":
		var parts []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(ev.Message.Content, &parts)
		var out []evSummary
		for _, p := range parts {
			switch p.Type {
			case "text":
				if strings.TrimSpace(p.Text) != "" {
					out = append(out, evSummary{kind: "text", text: strings.TrimSpace(p.Text)})
				}
			case "tool_use":
				out = append(out, evSummary{kind: "tool", tool: p.Name, text: toolArg(p.Input)})
			}
		}
		return out
	case "result":
		return []evSummary{{kind: "result", turns: ev.Turns, ms: ev.Dur, cost: ev.Cost, text: ev.Subtype}}
	}
	return nil
}

func toolArg(in json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description", "query", "prompt"} {
		if s, ok := m[k].(string); ok && s != "" {
			return strings.Join(strings.Fields(s), " ")
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// wrap breaks s at word boundaries to width w.
func wrap(s string, w int) []string {
	w = max(w, 8)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for lipgloss.Width(word) > w {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, ansi.Truncate(word, w, ""))
				word = ansi.TruncateLeft(word, w, "")
			}
			switch {
			case line == "":
				line = word
			case lipgloss.Width(line)+1+lipgloss.Width(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

func (m *Model) layoutConversation() {
	m.vp.Width = max(m.w-2, 10)
	m.vp.Height = max(m.h-5, 3)
	m.input.Width = max(m.w-6, 10)
}

// refreshConversation re-renders the open thread into the viewport.
func (m *Model) refreshConversation() {
	if m.convID == "" {
		return
	}
	w := max(m.w-4, 20)
	var lines []string
	for _, se := range m.events[m.convSlot()] {
		for _, s := range summarize(se) {
			lines = append(lines, renderSummary(s, w)...)
		}
	}
	if len(lines) == 0 {
		lines = []string{stDim.Render("  no events yet")}
	}
	m.vp.SetContent(strings.Join(lines, "\n"))
	if m.follow {
		m.vp.GotoBottom()
	}
}

func renderSummary(s evSummary, w int) []string {
	var out []string
	hang := func(first, rest string, st lipgloss.Style, text string) {
		for i, l := range wrap(text, w-2) {
			p := rest
			if i == 0 {
				p = first
			}
			out = append(out, p+st.Render(l))
		}
	}
	switch s.kind {
	case "user":
		out = append(out, "")
		hang(stAccent.Render("❯ "), "  ", stText, s.text)
	case "text":
		hang("  ", "  ", stText, s.text)
	case "tool":
		label := ansi.Truncate(s.text, max(w-lipgloss.Width(s.tool)-6, 8), "…")
		out = append(out, lipgloss.NewStyle().Foreground(hGreen).Render("● ")+stBold.Render(s.tool)+" "+stDim.Render(label))
	case "result":
		bits := []string{}
		if s.text != "" && s.text != "success" {
			bits = append(bits, s.text)
		}
		if s.ms > 0 {
			bits = append(bits, fmt.Sprintf("%ds", s.ms/1000))
		}
		if s.turns > 0 {
			bits = append(bits, fmt.Sprintf("%d turns", s.turns))
		}
		if s.cost > 0 {
			bits = append(bits, fmt.Sprintf("~$%.2f", s.cost))
		}
		out = append(out, stFaint.Render("  "+strings.Join(bits, " · ")))
	case "error":
		hang(lipgloss.NewStyle().Foreground(hRed).Render("✗ "), "  ", lipgloss.NewStyle().Foreground(hRed), s.text)
	case "note":
		hang(stFaint.Render("# "), "  ", stDim, s.text)
	case "file":
		out = append(out, stDim.Render("▤ "+s.text))
	}
	return out
}

// oneLine is an event as a single line, for the preview.
func oneLine(s evSummary, w int) string {
	first := strings.Join(strings.Fields(s.text), " ")
	switch s.kind {
	case "user":
		return stAccent.Render("❯ ") + stText.Render(ansi.Truncate(first, w-2, "…"))
	case "text":
		return stDim.Render("  " + ansi.Truncate(first, w-2, "…"))
	case "tool":
		return lipgloss.NewStyle().Foreground(hGreen).Render("● ") + stBold.Render(s.tool) + " " + stDim.Render(ansi.Truncate(first, max(w-lipgloss.Width(s.tool)-3, 4), "…"))
	case "error":
		return lipgloss.NewStyle().Foreground(hRed).Render("✗ " + ansi.Truncate(first, w-2, "…"))
	}
	return ""
}
