package harness

// Replay builds a transcript from Claude Code's stream-json events, as stored, with the
// same parser a live turn uses. Add other entries between them with Add.
type Replay struct {
	Entries []Entry

	parser  StreamParser
	cur     int
	curKind string
	started bool
	tools   map[string]int
}

// Add appends an entry of your own, such as a user message.
func (r *Replay) Add(e Entry) {
	r.Entries = append(r.Entries, e)
	r.started = false
}

// Event takes one stream-json event.
func (r *Replay) Event(ev map[string]any) {
	r.parser.Event(ev, r)
	if ev["type"] == "assistant" {
		r.started = false // the next message starts new entries
	}
}

// Settle ends tool calls that never got a result, such as when the turn was interrupted.
func (r *Replay) Settle() {
	for i := range r.Entries {
		r.Entries[i].Running = false
	}
}

func (r *Replay) Delta(d Delta) {
	if !r.started || r.curKind != d.Kind {
		r.Entries = append(r.Entries, Entry{Kind: d.Kind})
		r.cur, r.curKind, r.started = len(r.Entries)-1, d.Kind, true
	}
	r.Entries[r.cur].Text += d.Text
}

func (r *Replay) ToolStart(id, name, label string) {
	if r.tools == nil {
		r.tools = map[string]int{}
	}
	r.Entries = append(r.Entries, Entry{Kind: "tool", Tool: name, Label: label, Running: true})
	r.tools[id] = len(r.Entries) - 1
	r.started = false
}

func (r *Replay) ToolDone(id, result string, isError bool) {
	if i, ok := r.tools[id]; ok {
		e := &r.Entries[i]
		e.Running, e.Result, e.IsError = false, result, isError
		if e.Result == "" {
			e.Result = "(no output)"
		}
	}
}

func (r *Replay) Usage(Usage)                 {}
func (r *Replay) Approve(string, string) bool { return false }
