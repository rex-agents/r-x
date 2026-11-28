package authtui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

type fake struct {
	mu      sync.Mutex
	pack    authapi.Pack
	created []map[string]any
	sent    []string
	stopped []string // threads interrupted
	sse     chan string
	srv     *httptest.Server
}

func fixture() authapi.Pack {
	mk := func(id, proj, title, st, auth string, local bool, subs ...authapi.SubAgentSummary) authapi.ThreadSummary {
		return authapi.ThreadSummary{ID: id, Project: proj, Title: title, Status: st, Authority: auth, Guest: "g-" + id,
			Placement: authapi.Placement{Local: local}, SubAgents: subs, Cost: 0.42, Updated: "2026-11-27T10:00:00Z"}
	}
	return authapi.Pack{Leader: "mac", Term: 3, VaultHost: "mac", VaultOK: true, Authorities: []authapi.AuthorityInfo{
		{Name: "mac", URL: "http://mac:7700", Status: "up", Local: true, Host: authapi.HostInfo{OS: "darwin", CPUs: 10, Mem: 32 << 30, Load: 1.5, GuestsMax: 6, GuestsUsed: 2},
			Threads: []authapi.ThreadSummary{
				mk("t1", "web", "fix the login bug", "running", "mac", false,
					authapi.SubAgentSummary{ID: "s1", Name: "tests", Status: "running", Guest: "g-s1"},
					authapi.SubAgentSummary{ID: "s2", Name: "lint", Status: "idle"}),
				mk("t2", "web", "add dark mode", "idle", "mac", false),
				mk("t3", "api", "rate limiter", "idle", "mac", true),
				mk("t4", "api", "old thing", "archived", "mac", false),
			}},
		{Name: "nuc", URL: "http://nuc:7700", Status: "up", Host: authapi.HostInfo{OS: "linux", CPUs: 8, Load: 0.2, GuestsMax: 4},
			Threads: []authapi.ThreadSummary{mk("t5", "web", "docs pass", "running", "nuc", false)}},
	}}
}

func newFake(t *testing.T) *fake {
	f := &fake{pack: fixture(), sse: make(chan string, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/pack", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.pack)
	})
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"web","name":"web app","repo":"acme/web"},{"id":"api","name":"api","repo":"acme/api"}]`)
	})
	mux.HandleFunc("GET /api/pack/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(authapi.PackEvent{Type: authapi.EventPack, Data: f.pack})
		fmt.Fprintf(w, "event: pack\ndata: %s\n\n", b)
		w.(http.Flusher).Flush()
		for {
			select {
			case s := <-f.sse:
				io.WriteString(w, s)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("GET /api/threads/{id}/events/page", func(w http.ResponseWriter, r *http.Request) {
		evs := []string{
			`{"id":1,"ts":1,"ev":{"type":"rux_user","text":"fix the login bug"}}`,
			`{"id":2,"ts":2,"ev":{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the **auth handler**."},{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"auth.go"}}]}}}`,
			`{"id":3,"ts":3,"ev":{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"package auth\nfunc Login() {}"}]}}}`,
			`{"id":4,"ts":4,"ev":{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu2","name":"Bash","input":{"command":"go test ./..."}}]}}}`,
			`{"id":5,"ts":5,"ev":{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu2","is_error":true,"content":"FAIL: login_test.go"}]}}}`,
			`{"id":6,"ts":6,"ev":{"type":"rux_error","text":"runner lost the guest"}}`,
			`{"id":7,"ts":7,"ev":{"type":"rux_asset","name":"shot.png","caption":"screenshot"}}`,
			`{"id":8,"ts":8,"ev":{"type":"result","duration_ms":12000,"num_turns":3,"total_cost_usd":0.42}}`,
		}
		fmt.Fprintf(w, `{"start":1,"events":[%s]}`, strings.Join(evs, ","))
	})
	mux.HandleFunc("GET /api/threads/{id}/subagents/{sub}/events/page", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"start":1,"events":[{"id":1,"ts":1,"ev":{"type":"rux_user","text":"run the tests for %s"}},{"id":2,"ts":2,"ev":{"type":"assistant","message":{"content":[{"type":"text","text":"All green."}]}}}]}`, r.PathValue("sub"))
	})
	mux.HandleFunc("POST /api/threads/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Text string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.sent = append(f.sent, r.PathValue("id")+":"+b.Text)
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /api/threads/{id}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.stopped = append(f.stopped, r.PathValue("id"))
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /api/threads", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.created = append(f.created, b)
		f.mu.Unlock()
		fmt.Fprint(w, `{"id":"t9"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func logo(phase float64) string {
	return strings.Repeat("<<<<<<<<<<<<<<<< T-REX >>>>>>>>>>>>>>>>\n", logoH-1) + "last"
}

// boot builds a model against the fake and loads the pack the way Init would.
func boot(t *testing.T, f *fake, w, h int) *Model {
	t.Helper()
	m := New(NewClient(f.srv.URL), Options{Logo: logo, Now: func() time.Time { return time.Unix(1000, 0) }})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	p, err := m.c.Pack()
	if err != nil {
		t.Fatal(err)
	}
	m.Update(packMsg{p})
	ps, _ := m.c.Projects()
	m.Update(projectsMsg{ps})
	m.Update(liveMsg(true))
	return m
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "ctrl+l":
			msg = tea.KeyMsg{Type: tea.KeyCtrlL}
		case "ctrl+e":
			msg = tea.KeyMsg{Type: tea.KeyCtrlE}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		runCmd(m, cmd)
	}
}

// runCmd executes a command synchronously and feeds its message back (batches included).
func runCmd(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				runCmd(m, c)
			}
		case nil:
		default:
			if _, ok := msg.(tickMsg); ok {
				return
			}
			if fm, ok := msg.(feedMsg); ok {
				m.Update(fm)
				return
			}
			_, next := m.Update(msg)
			runCmd(m, next)
		}
	case <-time.After(300 * time.Millisecond): // blink, waitFeed and other waiters
	}
}

func plain(s string) string { return ansi.Strip(s) }

func TestNavigationAndSelectionPath(t *testing.T) {
	m := boot(t, newFake(t), 160, 45)
	if got := m.selected(colHost).name; got != "mac" {
		t.Fatalf("host = %q", got)
	}
	press(m, "right")
	if m.focus != colProject || m.selected(colProject).name != "api" { // sorted by name, archived t4 hidden but api has t3
		t.Fatalf("focus %d project %v", m.focus, m.selected(colProject))
	}
	press(m, "down")
	if m.selected(colProject).name != "web app" {
		t.Fatalf("project = %q", m.selected(colProject).name)
	}
	press(m, "right")
	if m.focus != colThread || m.selected(colThread).name != "fix the login bug" {
		t.Fatalf("thread = %+v", m.selected(colThread))
	}
	press(m, "right")
	if m.focus != colSub || m.selected(colSub).name != "tests" {
		t.Fatalf("sub = %+v", m.selected(colSub))
	}
	press(m, "down")
	if m.selected(colSub).name != "lint" {
		t.Fatal("down in sub-agents")
	}
	// Moving the host resets the children.
	press(m, "left", "left", "left", "down")
	if m.selected(colHost).name != "nuc" || m.sel[colProject] != 0 {
		t.Fatalf("host %v projSel %d", m.selected(colHost).name, m.sel[colProject])
	}
	v := plain(m.View())
	for _, want := range []string{"PACK", "THREAD", "MONITOR", "● live", "leader mac", "term 3", "vault ok", "HOSTS", "PROJECTS", "THREADS", "SUB-AGENTS", "pack › nuc", "new thread", "T-REX"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q", want)
		}
	}
	// The T-rex sits at the bottom of the first column, under the hosts.
	if h, r := strings.Index(v, "HOSTS"), strings.Index(v, "T-REX"); h < 0 || r < h {
		t.Errorf("T-rex should be below HOSTS (HOSTS at %d, T-REX at %d)", h, r)
	}
}

func TestConnectorsAndPreview(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 160, 45)
	press(m, "right", "down", "right") // thread "fix the login bug"
	v := plain(m.View())
	if !strings.ContainsAny(v, "┐┘┌└") || !strings.Contains(v, "───") {
		t.Error("no connector lines")
	}
	for _, want := range []string{"fix the login bug", "FILES", "shot.png", "RECENT", "Read auth.go", "3 turns", "$0.42", "2 sub-agents", "on     mac · vm"} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q\n%s", want, v)
		}
	}
}

func TestOpenThreadAndSend(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 120, 40)
	press(m, "right", "down", "right", "enter")
	if m.tab != tabThread || m.convID != "t1" {
		t.Fatalf("tab %v conv %q", m.tab, m.convID)
	}
	v := plain(m.View())
	for _, want := range []string{"❯ fix the login bug", "Looking at the auth handler.", "● Read auth.go", "3 turns"} {
		if !strings.Contains(v, want) {
			t.Errorf("conversation lacks %q\n%s", want, v)
		}
	}
	press(m, "h", "i", "enter")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 1 || f.sent[0] != "t1:hi" {
		t.Fatalf("sent %v", f.sent)
	}
}

func TestNewThreadRequest(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 120, 40)
	press(m, "right", "down", "n") // project "web app"
	if m.form == nil {
		t.Fatal("no form")
	}
	for _, r := range "write docs" {
		press(m, string(r))
	}
	press(m, "tab", "ctrl+l", "tab", "right", "right") // local; authority: auto → mac → nuc
	press(m, "enter")
	f.mu.Lock()
	got := f.created
	f.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("created %v", got)
	}
	b, _ := json.Marshal(got[0])
	want := `{"placement":{"authority":"nuc","local":true},"project":"web","prompt":"write docs"}`
	if string(b) != want {
		t.Fatalf("body %s", b)
	}
	if m.convID != "t9" || m.tab != tabThread {
		t.Errorf("didn't open the new thread: %q", m.convID)
	}

	// Default placement is a VM with no pin.
	m = boot(t, f, 120, 40)
	press(m, "n")
	for _, r := range "x" {
		press(m, string(r))
	}
	press(m, "enter")
	f.mu.Lock()
	b, _ = json.Marshal(f.created[1])
	f.mu.Unlock()
	if string(b) != `{"placement":{},"project":"api","prompt":"x"}` && string(b) != `{"placement":{},"prompt":"x"}` {
		t.Fatalf("default body %s", b)
	}
}

func TestDegradedAndVault(t *testing.T) {
	f := newFake(t)
	f.pack.Degraded, f.pack.VaultOK = true, false
	m := boot(t, f, 140, 40)
	v := plain(m.View())
	for _, want := range []string{"degraded", "vault unreachable", "no pack leader is reachable"} {
		if !strings.Contains(v, want) {
			t.Errorf("lacks %q\n%s", want, v)
		}
	}
	m.Update(liveMsg(false))
	if !strings.Contains(plain(m.View()), "offline") {
		t.Error("offline not shown")
	}
}

func TestFeedEvents(t *testing.T) {
	m := boot(t, newFake(t), 140, 40)
	m.Update(feedMsg{SSE{Name: "pack.delta", Data: `{"type":"pack.delta","data":{"name":"nuc","url":"u","status":"down","host":{"os":"linux"}}}`}})
	if m.pack.Authorities[1].Status != "down" || len(m.pack.Authorities[1].Threads) != 1 {
		t.Fatalf("delta %+v", m.pack.Authorities[1])
	}
	// Enveloped form, and a new thread arriving.
	m.Update(feedMsg{SSE{Name: "thread.status", Data: `{"type":"thread.status","data":{"id":"t6","project":"web","title":"fresh","status":"starting","authority":"mac","subagents":[]}}`}})
	press(m, "right", "down", "right")
	found := false
	for _, n := range m.column(colThread) {
		found = found || n.name == "fresh"
	}
	if !found {
		t.Error("new thread missing")
	}
	// Live conversation events append.
	press(m, "enter")
	m.Update(feedMsg{SSE{Name: "thread.event", Data: `{"type":"thread.event","data":{"thread":"` + m.convID + `","event":{"id":9,"ts":1,"src":"runner","ev":{"type":"assistant","message":{"content":[{"type":"text","text":"streamed reply"}]}}}}}`}})
	if !strings.Contains(plain(m.View()), "streamed reply") {
		t.Error("event not shown")
	}
	// Archived toggle.
	press(m, "tab", "1", "a")
	if !m.archived {
		t.Error("archived off")
	}
}

func TestStreamOverHTTP(t *testing.T) {
	f := newFake(t)
	c := NewClient(f.srv.URL)
	got := make(chan SSE, 4)
	go c.Stream(t.Context(), "/api/pack/stream", func(e SSE) { got <- e })
	if e := <-got; e.Name != "pack" || !strings.Contains(e.Data, `"leader":"mac"`) {
		t.Fatalf("first event %+v", e)
	}
	f.sse <- "event: pack.delta\ndata: {\"type\":\"pack.delta\",\"data\":{\"name\":\"mac\"}}\n\n"
	if e := <-got; e.Name != "pack.delta" {
		t.Fatalf("second event %+v", e)
	}
}

func TestNarrowTerminals(t *testing.T) {
	f := newFake(t)
	for _, sz := range [][2]int{{160, 45}, {120, 30}, {100, 24}, {80, 24}, {60, 20}, {40, 12}, {24, 7}} {
		m := boot(t, f, sz[0], sz[1])
		press(m, "right", "down", "right", "right")
		for _, tb := range []string{"1", "3", "?"} {
			press(m, tb)
			lines := strings.Split(m.View(), "\n")
			if len(lines) != sz[1] {
				t.Errorf("%dx%d tab %s: %d lines", sz[0], sz[1], tb, len(lines))
			}
			for i, l := range lines {
				if w := len([]rune(plain(l))); w > sz[0]*2 { // display width checked below
					t.Errorf("line %d too long", i)
				}
			}
			press(m, "esc")
		}
		v := m.View()
		for i, l := range strings.Split(v, "\n") {
			if w := ansi.StringWidth(l); w > sz[0] {
				t.Errorf("%dx%d line %d is %d wide", sz[0], sz[1], i, w)
			}
		}
		if sz[0] >= 90 && !strings.Contains(plain(v), "FILES") && !strings.Contains(plain(v), "status") {
			t.Errorf("%dx%d: no preview", sz[0], sz[1])
		}
		if sz[0] < 90 && strings.Contains(plain(v), "FILES") {
			t.Errorf("%dx%d: preview should collapse", sz[0], sz[1])
		}
		if sz[0] >= 60 && !strings.Contains(plain(v), "THREADS") && !strings.Contains(plain(v), "SUB-AGENTS") {
			t.Errorf("%dx%d: focused column not visible:\n%s", sz[0], sz[1], plain(v))
		}
	}
}

// TestRenderCapture writes the 160x45 view for the report when AUTHTUI_RENDER_OUT is set.
func TestRenderCapture(t *testing.T) {
	out := os.Getenv("AUTHTUI_RENDER_OUT")
	if out == "" {
		t.Skip("set AUTHTUI_RENDER_OUT to capture")
	}
	m := boot(t, newFake(t), 160, 45)
	press(m, "right", "down", "right")
	m.act["t1"] = []int{0, 1, 3, 2, 5, 1, 0, 4}
	if err := os.WriteFile(out, []byte(plain(m.View())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSubAgentConversationAndUptime(t *testing.T) {
	f := newFake(t)
	f.pack.Authorities[0].Started = "2026-11-27T09:00:00Z" // 1000s after the fake clock? set below
	m := boot(t, f, 140, 40)
	m.opts.Now = func() time.Time { return time.Date(2026, 11, 27, 10, 5, 0, 0, time.UTC) }
	press(m, "right", "down", "right", "right", "enter") // sub-agent "tests"
	if m.tab != tabThread || m.convSub != "s1" {
		t.Fatalf("tab %v sub %q", m.tab, m.convSub)
	}
	v := plain(m.View())
	for _, want := range []string{"sub-agent tests", "run the tests for s1", "All green.", "Read-only", "up 1h05m"} {
		if !strings.Contains(v, want) {
			t.Errorf("lacks %q\n%s", want, v)
		}
	}
	press(m, "x", "enter")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 0 {
		t.Errorf("sub-agent conversation sent %v", f.sent)
	}
}

func TestArchivedFlag(t *testing.T) {
	f := newFake(t)
	f.pack.Authorities[0].Threads[1].Archived = true // "add dark mode"
	m := boot(t, f, 140, 40)
	press(m, "right", "down", "right")
	for _, n := range m.column(colThread) {
		if n.name == "add dark mode" {
			t.Error("archived thread shown")
		}
	}
	press(m, "a")
	found := false
	for _, n := range m.column(colThread) {
		found = found || n.name == "add dark mode"
	}
	if !found {
		t.Error("archived thread hidden with the toggle on")
	}
}

func TestThreadRendersMarkdownToolResultsAndErrors(t *testing.T) {
	m := boot(t, newFake(t), 120, 40)
	press(m, "right", "down", "right", "enter")
	v := plain(m.View())
	for _, want := range []string{
		"Looking at the auth handler.", // markdown: the ** are gone
		"● Read auth.go", "└ package auth", "func Login() {}",
		"● Bash go test ./...", "└ FAIL: login_test.go",
		"✗ runner lost the guest", "· ▤ screenshot",
		"esc to interrupt", // a running thread, like the harness
	} {
		if !strings.Contains(v, want) {
			t.Errorf("thread view lacks %q\n%s", want, v)
		}
	}
	if strings.Contains(v, "**") {
		t.Errorf("markdown not rendered\n%s", v)
	}
}

// The thread's body is the harness's: the shared renderer draws the same snapshot the same way.
func TestThreadBodyIsTheSharedRenderer(t *testing.T) {
	m := boot(t, newFake(t), 120, 40)
	press(m, "right", "down", "right", "enter")
	want := plain(transcript.New().Render(m.conv.snapshot("running"), m.vp.Width, false, nil, "tool:t1:"))
	got := plain(m.View())
	for _, l := range strings.Split(want, "\n") {
		if l = strings.TrimRight(l, " "); l != "" && !strings.Contains(got, l) {
			t.Errorf("view lacks the renderer's line %q\n%s", l, got)
		}
	}
}

func TestThreadToolsOpenWithKeyAndClick(t *testing.T) {
	m := boot(t, newFake(t), 120, 40)
	press(m, "right", "down", "right", "enter")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(plain(m.View()), "└ FAIL") == false {
		t.Fatal("short result should show")
	}
	m.toggleEntry(2) // the Read call
	if !m.expanded["t1"][2] {
		t.Error("entry not opened")
	}
	press(m, "ctrl+e")
	if !m.expandAll {
		t.Error("ctrl+e did not expand all")
	}
	// A click on a tool line opens it: find it on screen.
	m.expandAll, m.expanded = false, map[string]map[int]bool{}
	m.repaint()
	lines := strings.Split(plain(m.View()), "\n")
	for y, l := range lines {
		if x := strings.Index(l, "Read auth.go"); x >= 0 {
			m.mouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
			break
		}
	}
	if !m.expanded["t1"][2] {
		t.Error("a click did not open the tool call")
	}
}

func TestEscInterruptsAWorkingThread(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 120, 40)
	press(m, "right", "down", "right", "enter") // t1 is running
	press(m, "esc")
	if m.tab != tabThread {
		t.Error("esc left a working thread")
	}
	f.mu.Lock()
	if len(f.stopped) != 1 || f.stopped[0] != "t1" {
		t.Errorf("interrupted %v", f.stopped)
	}
	f.mu.Unlock()
	m.pack.Authorities[0].Threads[0].Status = "idle"
	press(m, "esc")
	if m.tab != tabPack {
		t.Error("esc did not go back from an idle thread")
	}
}
