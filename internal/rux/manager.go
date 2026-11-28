package rux

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// Each project can have a manager: a long-lived thread (its own guest, with
// the repo checked out) that the user talks to on the project page. It runs
// worker threads in its project through the rux MCP server below, and is told
// automatically when one of its workers finishes a turn or fails.

const wakePrefix = "[rux] "

// assistantText returns the text parts of an assistant event.
func assistantText(ev json.RawMessage) string {
	var e struct {
		Message struct {
			Content []struct{ Type, Text string }
		}
	}
	if json.Unmarshal(ev, &e) != nil {
		return ""
	}
	var parts []string
	for _, c := range e.Message.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// wakeTextLocked returns the message for the thread's manager when ev is
// worth waking it for: a finished turn, or claude exiting with an error.
func (t *thread) wakeTextLocked(ev json.RawMessage) string {
	var e struct {
		Type, Subtype string
		IsError       bool `json:"is_error"`
		NumTurns      int  `json:"num_turns"`
		Code          int
		Error         string
		Text          string
		DurationMS    int64 `json:"duration_ms"`
	}
	if json.Unmarshal(ev, &e) != nil {
		return ""
	}
	name := fmt.Sprintf("worker %s (%q)", t.meta.ID, t.meta.Title)
	switch {
	case e.Type == "result":
		state := "finished a turn"
		if e.IsError || (e.Subtype != "" && e.Subtype != "success") {
			state = "stopped with " + cmpOr(e.Subtype, "an error")
		}
		last := t.lastText
		if last == "" {
			last = "(no text)"
		}
		return fmt.Sprintf("%s%s %s after %ds. Its last message:\n\n%s\n\nUse read_thread for the full transcript, or send_message to reply to it.",
			wakePrefix, name, state, e.DurationMS/1000, clipText(last, 2000))
	case e.Type == "rux_setup_failed":
		return fmt.Sprintf("%s%s failed to start: %s. It hasn't run anything; a new message makes it try the checkout again.", wakePrefix, name, e.Text)
	case e.Type == "rux_exit" && e.Code != 0:
		return fmt.Sprintf("%s%s: claude exited with code %d (%s). A new message will restart it.", wakePrefix, name, e.Code, e.Error)
	}
	return ""
}

// wake sends a message to a manager thread, or keeps it until it connects.
func (s *server) wake(managerID, text string) {
	m := s.get(managerID)
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.meta.Status == "destroyed" || m.meta.Status == "destroying" {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	// Kept until the manager's runner acks it, connected or not.
	if _, err := s.enqueueUser(m, wireMsg{T: "user", Text: text}, true); err != nil {
		log.Printf("manager %s: queueing a wake-up: %v", managerID, err)
	}
}

// managerFor returns the project's live manager thread, if any.
func (s *server) managerFor(projectID string) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *thread
	for _, t := range s.threads {
		if t.meta.Kind == "manager" && t.meta.Project == projectID && t.meta.Status != "destroyed" && t.meta.Status != "destroying" {
			if best == nil || t.meta.Created.After(best.meta.Created) {
				best = t
			}
		}
	}
	return best
}

// handleManagerMessage is how the user talks to a project's manager. The
// first message starts it (on demand), as a thread of kind "manager".
func (s *server) handleManagerMessage(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	var req struct{ Text string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		httpErr(w, 400, "text is required")
		return
	}
	if m := s.managerFor(pid); m != nil {
		_, err := s.enqueueUser(m, wireMsg{T: "user", Text: req.Text}, false)
		switch {
		case errors.Is(err, errGuestGone):
			httpErr(w, 409, "the manager's guest is gone: "+err.Error())
		case errors.Is(err, errNoRunner):
			if m.view().Status == "failed" {
				httpErr(w, 409, "the manager failed to start; remove it from the menu and try again")
			} else {
				httpErr(w, 409, "the manager is still starting")
			}
		case err != nil:
			httpErr(w, 500, "saving the message: "+err.Error())
		default:
			writeJSONResp(w, 202, m.view())
		}
		return
	}
	t, err := s.createThread(createReq{Project: pid, Prompt: req.Text, Kind: "manager"})
	if err != nil {
		httpErr(w, errorCode(err), err.Error())
		return
	}
	writeJSONResp(w, 201, t.view())
}

// ---- MCP server for managers (streamable HTTP, JSON responses) ----

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

var str = func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var managerTools = []mcpTool{
	{"list_threads", "List the worker threads in this project: id, title, status, branch, model, when it last changed, and the start of its latest message.",
		obj(map[string]any{"include_removed": map[string]any{"type": "boolean", "description": "also list threads whose guests were removed"}})},
	{"start_thread", "Start a worker thread: a new sandboxed guest with the project's repo cloned, running Claude Code on the prompt. It works independently; you're told when it finishes a turn. Workers can push branches; only the user merges.",
		obj(map[string]any{"prompt": str("the task, written so a fresh agent with no other context can do it"), "title": str("a short title for the thread, shown to the user (a few words)"), "branch": str("branch to start from (default: the project's)"),
			"model": str("model alias such as opus, sonnet, haiku, fable (default: the project's)"), "effort": str("low, medium, high, xhigh or max (default: the project's)"),
			"authority": str("run it on this authority (host) of the pack; by default the pack picks the one with the most free capacity")}, "prompt")},
	{"send_message", "Send a message to a worker thread, like a reply from the user. It's queued if the worker is mid-turn.",
		obj(map[string]any{"thread_id": str("worker thread id"), "text": str("the message")}, "thread_id", "text")},
	{"read_thread", "Read a worker thread's transcript, condensed: messages, tool calls, errors and shared files.",
		obj(map[string]any{"thread_id": str("worker thread id"), "max_chars": map[string]any{"type": "integer", "description": "keep only the last this many characters (default 12000)"}}, "thread_id")},
	{"view_file", "View a file a worker shared (a screenshot, log, etc.). Images come back as images.",
		obj(map[string]any{"thread_id": str("worker thread id"), "file": str("the shared file's name, as read_thread lists it")}, "thread_id", "file")},
	{"interrupt_thread", "Stop a worker's current turn.", obj(map[string]any{"thread_id": str("worker thread id")}, "thread_id")},
	{"remove_thread", "Remove a worker's guest (its files are deleted; the transcript stays). Do this once its work is pushed or no longer needed, since each guest uses memory.",
		obj(map[string]any{"thread_id": str("worker thread id")}, "thread_id")},
	{"thread_stats", "Activity data for this project's worker threads, for answering \"how's it going\" and for charts (render them with rux ui: Timeline, Chart, Table, Stat): per thread its status, turns, cost, busy time, tool calls, when it was working (intervals), and tool calls per hour over the last 24 hours; plus project totals.",
		obj(map[string]any{"include_removed": map[string]any{"type": "boolean", "description": "also include threads whose guests were removed"}, "hours": map[string]any{"type": "integer", "description": "how far back the hourly series goes (default 24, max 168)"}})},
	{"get_project", "The project's name, repo, defaults for new threads, and notes.", obj(map[string]any{})},
	{"update_notes", "Replace the project's notes, which every new worker (and you) get in the system prompt. Use them as the project's memory: conventions, how to build and test, decisions. Keep them concise.",
		obj(map[string]any{"notes": str("the full new notes")}, "notes")},
}

func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	mgr := s.threadForToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if mgr == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	var req rpcReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeJSONResp(w, 400, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	if len(req.ID) == 0 { // a notification
		w.WriteHeader(202)
		return
	}
	reply := func(result any) {
		writeJSONResp(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		reply(map[string]any{
			"protocolVersion": cmpOr(p.ProtocolVersion, "2025-06-18"),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "rux", "version": "0.1"},
			"instructions":    "Tools for managing the worker threads in this rux project.",
		})
	case "ping":
		reply(map[string]any{})
	case "tools/list":
		reply(map[string]any{"tools": managerTools})
	case "tools/call":
		var p struct {
			Name      string
			Arguments json.RawMessage
		}
		json.Unmarshal(req.Params, &p)
		content, err := s.callManagerTool(mgr, p.Name, p.Arguments)
		if err != nil {
			reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true})
			return
		}
		reply(map[string]any{"content": content})
	default:
		writeJSONResp(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found: " + req.Method}})
	}
}

func (s *server) threadByToken(tok string) *thread {
	if tok == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.threads {
		if subtle.ConstantTimeCompare([]byte(t.meta.Token), []byte(tok)) == 1 {
			return t
		}
	}
	return nil
}

func textContent(s string) []any { return []any{map[string]any{"type": "text", "text": s}} }

// worker returns a worker thread in the manager's project.
func (s *server) worker(mgr *thread, id string) (*thread, error) {
	t := s.get(id)
	if t == nil || !s.inScope(mgr, t) {
		return nil, fmt.Errorf("no worker thread %q in this project", id)
	}
	return t, nil
}

func (s *server) callManagerTool(mgr *thread, name string, raw json.RawMessage) ([]any, error) {
	var a struct {
		ThreadID       string `json:"thread_id"`
		Text, Prompt   string
		Title          string
		Branch, Model  string
		Effort, File   string
		Notes          *string
		MaxChars       int  `json:"max_chars"`
		IncludeRemoved bool `json:"include_removed"`
		Hours          int
		Authority      string
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("bad arguments: %v", err)
		}
	}
	pid := mgr.meta.Project
	if remoteTools[name] && a.ThreadID != "" && s.get(a.ThreadID) == nil {
		if owner := s.remoteChild(context.Background(), mgr, a.ThreadID); owner != "" {
			return s.callRemoteTool(owner, mgr, name, raw, a.ThreadID) // a sub-agent on another authority
		}
	}
	switch name {
	case "list_threads":
		s.mu.Lock()
		var ts []*thread
		for _, t := range s.threads {
			if s.inScope(mgr, t) {
				ts = append(ts, t)
			}
		}
		s.mu.Unlock()
		sort.Slice(ts, func(i, j int) bool { return ts[i].meta.Updated.After(ts[j].meta.Updated) })
		var b strings.Builder
		for _, t := range ts {
			t.mu.Lock()
			m, last := t.meta, t.lastText
			if last == "" {
				last = t.lastAssistantTextLocked()
			}
			t.mu.Unlock()
			if (m.Status == "destroyed" || m.Archived) && !a.IncludeRemoved {
				continue
			}
			status := m.Status
			if !m.Connected && (status == "idle" || status == "running") {
				status += " (runner offline)"
			}
			if status == "destroyed" {
				status = "removed"
			}
			who := "started by the user"
			if m.Parent == mgr.meta.ID {
				who = "started by you"
			}
			fmt.Fprintf(&b, "- %s %q · %s · branch %s · model %s · %s · updated %s ago\n", m.ID, m.Title, status,
				cmpOr(m.Branch, "default"), cmpOr(m.Model, "default"), who, time.Since(m.Updated).Round(time.Second))
			if last != "" {
				fmt.Fprintf(&b, "  latest: %s\n", strings.ReplaceAll(clipText(last, 300), "\n", " "))
			}
		}
		for _, kid := range s.remoteChildren(context.Background())[mgr.meta.ID] {
			fmt.Fprintf(&b, "- %s %q · %s · on authority %s · started by you\n", kid.ID, kid.Name, kid.Status, kid.Authority)
		}
		if b.Len() == 0 {
			return textContent("No worker threads in this project yet."), nil
		}
		return textContent(b.String()), nil

	case "start_thread":
		req := createReq{Project: pid, Prompt: a.Prompt, Title: a.Title, Branch: a.Branch, Model: a.Model, Effort: a.Effort,
			Parent: mgr.meta.ID, ParentAuthority: s.authName(), Depth: s.depth(mgr) + 1, Vault: mgr.meta.Vault,
			Placement: authapi.Placement{Authority: strings.TrimSpace(a.Authority)}}
		// The repo and defaults travel with the request, since another authority may not have the project.
		s.mu.Lock()
		if p := s.projects[pid]; p != nil {
			req.Repo = p.Repo
			if req.Repo == p.Repo {
				req.Branch = cmpOr(req.Branch, p.Branch)
			}
			req.Model, req.Effort = cmpOr(req.Model, p.Model), cmpOr(req.Effort, p.Effort)
		}
		s.mu.Unlock()
		if mgr.meta.Kind != "manager" {
			// A project agent's sub-agents run the same engine, and can start sub-agents of their own.
			if s.depth(mgr) >= maxAgentDepth {
				return nil, fmt.Errorf("sub-agents can be nested %d deep, and this is the deepest", maxAgentDepth)
			}
			if mgr.meta.Engine == "harness" {
				req.Engine, req.Model = "harness", cmpOr(a.Model, mgr.meta.Model)
			}
		}
		view, err := s.createPlaced(context.Background(), req)
		if err != nil {
			return nil, err
		}
		var v struct{ ID, Title, Authority string }
		b, _ := json.Marshal(view)
		json.Unmarshal(b, &v)
		return textContent(fmt.Sprintf("Started worker %s (%q) on %s. Its guest takes a minute or two to come up; you'll get a message when it finishes its first turn.", v.ID, v.Title, cmpOr(v.Authority, s.authName()))), nil

	case "send_message", "interrupt_thread":
		t, err := s.worker(mgr, a.ThreadID)
		if err != nil {
			return nil, err
		}
		if name == "send_message" {
			if strings.TrimSpace(a.Text) == "" {
				return nil, fmt.Errorf("text is required")
			}
			waking, err := s.enqueueUser(t, wireMsg{T: "user", Text: a.Text}, false)
			if errors.Is(err, errGuestGone) {
				return nil, fmt.Errorf("worker %s: %v", a.ThreadID, err)
			}
			if errors.Is(err, errNoRunner) {
				return nil, fmt.Errorf("worker %s isn't connected (status %s)", a.ThreadID, t.view().Status)
			}
			if err != nil {
				return nil, err
			}
			if waking {
				return textContent(fmt.Sprintf("Worker %s was asleep; its guest is starting, and it gets the message when it's up (about a minute).", a.ThreadID)), nil
			}
			return textContent("Sent."), nil
		}
		t.mu.Lock()
		send, status := t.send, t.meta.Status
		if send != nil {
			select {
			case send <- wireMsg{T: "interrupt"}:
				t.mu.Unlock()
				return textContent("Interrupted."), nil
			default:
			}
		}
		t.mu.Unlock()
		if send == nil && asleepStatus(status) {
			return textContent(fmt.Sprintf("Worker %s is asleep (its guest is stopped), so there's nothing to interrupt.", a.ThreadID)), nil
		}
		if send == nil {
			return nil, fmt.Errorf("worker %s isn't connected (status %s)", a.ThreadID, t.view().Status)
		}
		return nil, fmt.Errorf("worker %s is busy; try again", a.ThreadID)

	case "read_thread":
		t, err := s.worker(mgr, a.ThreadID)
		if err != nil {
			return nil, err
		}
		max := a.MaxChars
		if max <= 0 {
			max = 12000
		}
		return textContent(t.condensed(max)), nil

	case "view_file":
		t, err := s.worker(mgr, a.ThreadID)
		if err != nil {
			return nil, err
		}
		file := filepath.Base(a.File)
		if file != a.File || strings.HasPrefix(file, ".") {
			return nil, fmt.Errorf("bad file name")
		}
		b, err := os.ReadFile(filepath.Join(t.assetsDir(), file))
		if err != nil {
			return nil, fmt.Errorf("no shared file %q in %s", file, a.ThreadID)
		}
		ctype, _ := assetType(file)
		switch {
		case strings.HasPrefix(ctype, "image/") && ctype != "image/svg+xml":
			if len(b) > 5<<20 {
				return nil, fmt.Errorf("image is too large to view (%d MB)", len(b)>>20)
			}
			return []any{map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(b), "mimeType": ctype}}, nil
		case strings.HasPrefix(ctype, "text/") || ctype == "image/svg+xml":
			return textContent(clipText(string(b), 30000)), nil
		}
		return nil, fmt.Errorf("%s is %s; only images and text can be viewed", file, ctype)

	case "remove_thread":
		t, err := s.worker(mgr, a.ThreadID)
		if err != nil {
			return nil, err
		}
		if st := t.view().Status; st == "destroyed" || st == "destroying" {
			return textContent("Already removed."), nil
		}
		s.destroyThread(t)
		return textContent("Removing its guest."), nil

	case "thread_stats":
		hours := a.Hours
		if hours <= 0 {
			hours = 24
		}
		hours = min(hours, 168)
		s.mu.Lock()
		var ts []*thread
		for _, t := range s.threads {
			if s.inScope(mgr, t) {
				ts = append(ts, t)
			}
		}
		s.mu.Unlock()
		now := time.Now()
		start := now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)
		type interval struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		}
		type stat struct {
			ID         string     `json:"id"`
			Title      string     `json:"title"`
			Status     string     `json:"status"`
			StartedBy  string     `json:"started_by"`
			Model      string     `json:"model"`
			Branch     string     `json:"branch"`
			Created    time.Time  `json:"created"`
			Updated    time.Time  `json:"updated"`
			Turns      int        `json:"turns"`
			CostUSD    float64    `json:"cost_usd"`
			BusySec    int64      `json:"busy_seconds"`
			ToolCalls  int        `json:"tool_calls"`
			Errors     int        `json:"errors"`
			Shared     int        `json:"shared_files"`
			Working    []interval `json:"working"`
			HourlyTool []int      `json:"tool_calls_per_hour"`
		}
		var out []stat
		tot := struct {
			Threads, Active, Turns, ToolCalls int
			CostUSD                           float64
			HourlyTool                        []int
		}{HourlyTool: make([]int, hours)}
		for _, t := range ts {
			t.mu.Lock()
			m := t.meta
			lines := make([][]byte, len(t.events))
			copy(lines, t.events)
			t.mu.Unlock()
			if m.Status == "destroyed" && !a.IncludeRemoved {
				continue
			}
			st := stat{ID: m.ID, Title: m.Title, Status: m.Status, Model: s.effectiveModel(m.Model), Branch: m.Branch,
				Created: m.Created, Updated: m.Updated, HourlyTool: make([]int, hours), StartedBy: "user"}
			if st.Status == "destroyed" {
				st.Status = "removed"
			}
			if m.Parent == mgr.meta.ID {
				st.StartedBy = "you"
			}
			var open *time.Time
			for _, l := range lines {
				var se storedEvent
				if json.Unmarshal(l, &se) != nil {
					continue
				}
				ts := time.UnixMilli(se.TS)
				var e struct {
					Type     string
					NumTurns int     `json:"num_turns"`
					Cost     float64 `json:"total_cost_usd"`
					Dur      int64   `json:"duration_ms"`
					Message  struct{ Content json.RawMessage }
				}
				if json.Unmarshal(se.Ev, &e) != nil {
					continue
				}
				switch e.Type {
				case "rux_user":
					if open == nil {
						open = &ts
					}
				case "result":
					st.Turns++
					st.CostUSD += e.Cost
					st.BusySec += e.Dur / 1000
					if open != nil {
						st.Working = append(st.Working, interval{*open, ts})
						open = nil
					}
				case "rux_error":
					st.Errors++
				case "rux_asset":
					st.Shared++
				case "assistant":
					n := bytes.Count(e.Message.Content, []byte(`"type":"tool_use"`))
					st.ToolCalls += n
					if h := int(ts.Sub(start) / time.Hour); n > 0 && h >= 0 && h < hours {
						st.HourlyTool[h] += n
						tot.HourlyTool[h] += n
					}
				}
			}
			if open != nil {
				st.Working = append(st.Working, interval{*open, now}) // still working
			}
			if len(st.Working) > 50 {
				st.Working = st.Working[len(st.Working)-50:]
			}
			tot.Threads++
			if m.Status == "running" || m.Status == "provisioning" || m.Status == "starting" {
				tot.Active++
			}
			tot.Turns += st.Turns
			tot.ToolCalls += st.ToolCalls
			tot.CostUSD += st.CostUSD
			out = append(out, st)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
		b, _ := json.MarshalIndent(map[string]any{"now": now, "hourly_series_start": start, "hours": hours,
			"totals": tot, "threads": out}, "", " ")
		return textContent(string(b)), nil

	case "get_project":
		s.mu.Lock()
		p := s.projects[pid]
		var cp project
		if p != nil {
			cp = *p
		}
		s.mu.Unlock()
		return textContent(fmt.Sprintf("name: %s\nrepo: %s\ndefault branch: %s\ndefault model: %s\ndefault effort: %s\nnotes:\n%s",
			cp.Name, cmpOr(cp.Repo, "(none: blank workspaces)"), cmpOr(cp.Branch, "the repo's default"), cmpOr(cp.Model, "default"),
			cmpOr(cp.Effort, "default"), cmpOr(cp.Notes, "(none)"))), nil

	case "update_notes":
		if a.Notes == nil {
			return nil, fmt.Errorf("notes is required")
		}
		if len(*a.Notes) > 20000 {
			return nil, fmt.Errorf("notes are limited to 20,000 characters")
		}
		s.mu.Lock()
		p := s.projects[pid]
		if p != nil {
			p.Notes, p.Updated = *a.Notes, time.Now()
			s.saveProjectLocked(p)
		}
		s.mu.Unlock()
		return textContent("Notes updated. New workers get them; running ones pick them up when claude next restarts."), nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func (t *thread) lastAssistantTextLocked() string {
	for i := len(t.events) - 1; i >= 0 && i >= len(t.events)-400; i-- {
		var se storedEvent
		if json.Unmarshal(t.events[i], &se) != nil || !bytes.Contains(se.Ev, []byte(`"assistant"`)) {
			continue
		}
		if txt := assistantText(se.Ev); txt != "" {
			return txt
		}
	}
	return ""
}

// condensed renders a thread's transcript as plain text for a manager.
func (t *thread) condensed(max int) string {
	t.mu.Lock()
	lines := make([][]byte, len(t.events))
	copy(lines, t.events)
	m := t.meta
	t.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "thread %s %q · status %s · branch %s\n\n", m.ID, m.Title, m.Status, cmpOr(m.Branch, "default"))
	tools := map[string]string{}
	for _, l := range lines {
		var se storedEvent
		if json.Unmarshal(l, &se) != nil {
			continue
		}
		var e struct {
			Type, Subtype, Text, Name, Caption, File, Branch, Dir string
			NumTurns                                              int     `json:"num_turns"`
			Cost                                                  float64 `json:"total_cost_usd"`
			Message                                               struct {
				Content json.RawMessage
			}
		}
		if json.Unmarshal(se.Ev, &e) != nil {
			continue
		}
		switch e.Type {
		case "rux_user":
			if strings.HasPrefix(e.Text, wakePrefix) {
				fmt.Fprintf(&b, "RUX: %s\n\n", clipText(e.Text, 500))
			} else {
				fmt.Fprintf(&b, "USER: %s\n\n", e.Text)
			}
		case "rux_checkout":
			fmt.Fprintf(&b, "(workspace %s on branch %s)\n", e.Dir, e.Branch)
		case "rux_error":
			fmt.Fprintf(&b, "ERROR: %s\n", e.Text)
		case "rux_asset":
			fmt.Fprintf(&b, "[shared file %s: %s]\n", e.File, e.Caption)
		case "result":
			fmt.Fprintf(&b, "(turn done: %d turns, $%.2f)\n\n", e.NumTurns, e.Cost)
		case "assistant":
			var parts []struct {
				Type, Text, Name, ID string
				Input                map[string]any
			}
			json.Unmarshal(e.Message.Content, &parts)
			for _, p := range parts {
				switch p.Type {
				case "text":
					if strings.TrimSpace(p.Text) != "" {
						fmt.Fprintf(&b, "ASSISTANT: %s\n\n", strings.TrimSpace(p.Text))
					}
				case "tool_use":
					arg := ""
					for _, k := range []string{"command", "file_path", "pattern", "url", "query", "description"} {
						if v, ok := p.Input[k].(string); ok {
							arg = v
							break
						}
					}
					tools[p.ID] = p.Name
					fmt.Fprintf(&b, "  [%s] %s\n", p.Name, clipText(strings.ReplaceAll(arg, "\n", " "), 200))
				}
			}
		case "user":
			var parts []struct {
				Type      string
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
				Content   json.RawMessage
			}
			json.Unmarshal(e.Message.Content, &parts)
			for _, p := range parts {
				if p.Type == "tool_result" && p.IsError {
					var txt string
					if json.Unmarshal(p.Content, &txt) != nil {
						txt = string(p.Content)
					}
					fmt.Fprintf(&b, "  [%s failed] %s\n", tools[p.ToolUseID], clipText(strings.ReplaceAll(txt, "\n", " "), 300))
				}
			}
		}
	}
	out := b.String()
	if len(out) > max {
		out = "…(earlier transcript omitted)\n" + out[len(out)-max:]
	}
	return out
}
