package authtui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/rex-agents/r-x/internal/harness"
)

// threadData is a thread's stored events as the harness shows them, with the totals the
// preview and the title line use.
type threadData struct {
	entries []harness.Entry
	files   []string
	ms      int64
	turns   int64
	cost    float64
}

// worker names the agent a "[rux] worker ID ("name") ..." message comes from.
var workerName = regexp.MustCompile(`^worker \S+ \("(.*?)"\)`)

// decodeThread turns stored events into transcript entries. Claude Code's stream-json goes
// through the harness's own parser; rux's events become user, note and error entries.
func decodeThread(evs []StoredEvent) threadData {
	var d threadData
	var r harness.Replay
	for _, se := range evs {
		var ev map[string]any
		if json.Unmarshal(se.Ev, &ev) != nil {
			continue
		}
		str := func(k string) string { s, _ := ev[k].(string); return s }
		num := func(k string) float64 { f, _ := ev[k].(float64); return f }
		switch str("type") {
		case "rux_user":
			text, from := str("text"), "you"
			if rest, ok := strings.CutPrefix(text, "[rux] "); ok {
				text, from = rest, "rux"
				if m := workerName.FindStringSubmatch(rest); m != nil {
					from = m[1]
				}
			}
			r.Add(harness.Entry{Kind: "user", From: from, Text: text})
		case "rux_error":
			r.Add(harness.Entry{Kind: "error", Text: str("text")})
		case "rux_asset":
			if quiet, _ := ev["quiet"].(bool); !quiet {
				d.files = append(d.files, str("name"))
				r.Add(harness.Entry{Kind: "note", Text: "▤ " + cmpOr(str("caption"), str("name"))})
			}
		case "rux_interrupt":
			r.Add(harness.Entry{Kind: "note", Text: "Interrupted"})
		case "rux_asleep":
			r.Add(harness.Entry{Kind: "note", Text: "asleep · the guest is stopped until the next message"})
		case "result":
			d.ms += int64(num("duration_ms"))
			d.turns += int64(num("num_turns"))
			d.cost += num("total_cost_usd")
		default:
			r.Event(ev)
		}
	}
	d.entries = r.Entries
	return d
}

// snapshot is what the transcript renderer draws for a thread (or sub-agent) with the
// given status.
func (d *threadData) snapshot(status string) harness.Snapshot {
	snap := harness.Snapshot{Entries: append([]harness.Entry(nil), d.entries...)}
	switch status {
	case "running":
		snap.Status, snap.Detail = harness.Working, "thinking"
		for _, e := range snap.Entries {
			if e.Kind == "tool" && e.Running {
				snap.Detail = e.Tool + " " + e.Label
			}
		}
	case "starting", "waking":
		snap.Status, snap.Detail = harness.Starting, status
	case "failed":
		snap.Status = harness.Failed
	case "asleep", "sleeping", "destroyed":
		snap.Status = harness.Stopped
	default:
		snap.Status = harness.Waiting
	}
	if snap.Status != harness.Working {
		for i := range snap.Entries {
			snap.Entries[i].Running = false // no result is coming for these
		}
	}
	return snap
}

func (m *Model) layoutConversation() {
	m.vp.Width = max(m.w-4, 10)
	m.vp.Height = max(m.h-8, 3)
	m.input.Width = max(m.w-6, 10)
}

// convStatus is the status of the open thread, or of the open sub-agent.
func (m *Model) convStatus() string {
	t := m.threadByID(m.convID)
	if t == nil {
		return ""
	}
	if m.convSub != "" {
		for _, s := range t.SubAgents {
			if s.ID == m.convSub {
				return s.Status
			}
		}
	}
	return t.Status
}

// busy reports whether the open conversation shows a spinner.
func (m *Model) busy() bool {
	switch m.convStatus() {
	case "running", "starting", "waking":
		return true
	}
	return false
}

// working reports whether the open thread is running a turn, which esc interrupts.
func (m *Model) working() bool { return m.convSub == "" && m.convStatus() == "running" }

// refreshConversation decodes the open thread's events and draws them.
func (m *Model) refreshConversation() {
	if m.convID == "" {
		return
	}
	m.conv = decodeThread(m.events[m.convSlot()])
	m.repaint()
}

// repaint draws the decoded thread into the viewport.
func (m *Model) repaint() {
	if m.convID == "" {
		return
	}
	m.tr.ReadOnly = m.convSub != ""
	slot := m.convSlot()
	m.vp.SetContent(m.tr.Render(m.conv.snapshot(m.convStatus()), m.vp.Width, m.expandAll, m.expanded[slot], "tool:"+slot+":"))
	if m.follow {
		m.vp.GotoBottom()
	}
}

// toggleEntry opens or closes entry i of the open conversation.
func (m *Model) toggleEntry(i int) {
	slot := m.convSlot()
	if m.expanded[slot] == nil {
		m.expanded[slot] = map[int]bool{}
	}
	m.expanded[slot][i] = !m.expanded[slot][i]
	m.follow = false
	m.repaint()
}

// oneLine is an entry as a single line, for the preview; empty for the kinds it skips.
func oneLine(e harness.Entry, w int) string {
	first := strings.Join(strings.Fields(e.Text), " ")
	switch e.Kind {
	case "user":
		if e.From != "you" {
			return stDim.Render(ansi.Truncate("◂ "+e.From+" "+first, w, "…"))
		}
		return stAccent.Render("❯ ") + stText.Render(ansi.Truncate(first, w-2, "…"))
	case "text":
		return stDim.Render("  " + ansi.Truncate(first, w-2, "…"))
	case "tool":
		d := lipgloss.NewStyle().Foreground(hGreen)
		if e.IsError {
			d = lipgloss.NewStyle().Foreground(hRed)
		}
		label := strings.Join(strings.Fields(e.Label), " ")
		return d.Render("● ") + stBold.Render(e.Tool) + " " + stDim.Render(ansi.Truncate(label, max(w-lipgloss.Width(e.Tool)-3, 4), "…"))
	case "error":
		return lipgloss.NewStyle().Foreground(hRed).Render("✗ " + ansi.Truncate(first, w-2, "…"))
	}
	return ""
}

// statsLine is the totals shown next to the title: "3 turns · 12s · ~$0.42".
func (d *threadData) statsLine() string {
	var bits []string
	if d.turns > 0 {
		bits = append(bits, fmt.Sprintf("%d turns", d.turns))
	}
	if d.ms > 0 {
		bits = append(bits, fmt.Sprintf("%ds", d.ms/1000))
	}
	if d.cost > 0 {
		bits = append(bits, fmt.Sprintf("~$%.2f", d.cost))
	}
	return strings.Join(bits, " · ")
}
