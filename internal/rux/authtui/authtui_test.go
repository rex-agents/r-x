package authtui

import (
	"encoding/json"
	"errors"
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
	mu         sync.Mutex
	pack       authapi.Pack
	created    []map[string]any
	sent       []string
	stopped    []string            // threads interrupted
	guestCalls []string            // "name:action?query" of guest actions
	managers   []string            // "project:text" of started managers
	made       []map[string]string // POST /api/projects bodies
	fsPaths    []string            // GET /api/fs/dirs queries (path + hidden)
	headers    []http.Header       // of each pack stream request
	sse        chan string
	srv        *httptest.Server
}

func fixture() authapi.Pack {
	mk := func(id, proj, title, st, auth, upd string, local bool, subs ...authapi.SubAgentSummary) authapi.ThreadSummary {
		return authapi.ThreadSummary{ID: id, Project: proj, Title: title, Status: st, Authority: auth, Guest: "g-" + id,
			Placement: authapi.Placement{Local: local}, SubAgents: subs, Cost: 0.42, Updated: upd}
	}
	mgr := mk("m-web", "web", "web app · manager", "running", "mac", "2026-11-27T09:00:00Z", false)
	mgr.Kind = "manager"
	mgr.Cost = 1
	worker := func(t authapi.ThreadSummary) authapi.ThreadSummary {
		t.Parent, t.ParentAuthority = "m-web", "mac"
		return t
	}
	mb := int64(1 << 20)
	return authapi.Pack{Leader: "mac", Term: 3, VaultHost: "mac", VaultOK: true,
		Clients: []authapi.ClientInfo{
			{ID: "c1", Kind: "tui", Device: "laptop", Authority: "mac", Since: "2026-11-27T09:00:00Z", LastSeen: "2026-11-27T09:59:58Z"},
			{ID: "c2", Kind: "web", Device: "phone", Authority: "nuc", Since: "2026-11-27T09:30:00Z", LastSeen: "2026-11-27T09:59:00Z"},
		},
		Authorities: []authapi.AuthorityInfo{
			{Name: "mac", URL: "http://mac:7700", Status: "up", Local: true, Started: "2026-11-27T08:00:00Z",
				Host: authapi.HostInfo{OS: "darwin", CPUs: 10, Mem: 32 << 30, Load: 1.5, GuestsMax: 6, GuestsUsed: 2},
				Guests: []authapi.GuestInfo{
					{Name: "g-t1", Phase: "running", Backend: "container", CPU: 35, MemBytes: 1200 * mb, MemLimit: 4096 * mb, Thread: "t1"},
					{Name: "g-t2", Phase: "stopped", Backend: "container", MemLimit: 4096 * mb, Thread: "t2"},
					{Name: "spare", Phase: "running", Backend: "container", CPU: 1, MemBytes: 100 * mb},
				},
				Threads: []authapi.ThreadSummary{
					mgr,
					worker(mk("t1", "web", "fix the login bug", "running", "mac", "2026-11-27T12:00:00Z", false,
						authapi.SubAgentSummary{ID: "s1", Name: "tests", Status: "running", Guest: "g-s1"},
						authapi.SubAgentSummary{ID: "s2", Name: "lint", Status: "idle"})),
					worker(mk("t2", "web", "add dark mode", "idle", "mac", "2026-11-27T10:00:00Z", false)),
					mk("t6", "web", "hand started", "idle", "mac", "2026-11-27T11:00:00Z", false),
					mk("t3", "api", "rate limiter", "idle", "mac", "2026-11-27T10:00:00Z", true),
					mk("t4", "api", "old thing", "archived", "mac", "2026-11-27T10:00:00Z", false),
					mk("t7", "", "quick question", "idle", "mac", "2026-11-27T10:00:00Z", false),
				}},
			{Name: "nuc", URL: "http://nuc:7700", Status: "up", Started: "2026-11-27T09:30:00Z",
				Host:   authapi.HostInfo{OS: "linux", CPUs: 8, Load: 0.2, GuestsMax: 4, GuestsUsed: 1},
				Guests: []authapi.GuestInfo{{Name: "g-t5", Phase: "running", Backend: "linux", CPU: 80, MemBytes: 2048 * mb, MemLimit: 4096 * mb, Thread: "t5"}},
				Threads: []authapi.ThreadSummary{
					worker(mk("t5", "web", "docs pass", "running", "nuc", "2026-11-27T08:00:00Z", false)),
				}},
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
	mux.HandleFunc("GET /api/repos", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"enabled":true,"repos":[{"name":"acme/web","private":true,"description":"the site"},{"name":"acme/api"},{"name":"acme/newrepo","description":"fresh"},{"name":"zed/webhooks"}]}`)
	})
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.made = append(f.made, b)
		f.mu.Unlock()
		if d := b["dir"]; d != "" {
			if d == "/home/u/docs" || d == "/home/u" {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error":"%s is not a git repository; run git init and commit first"}`, d)
				return
			}
			base := d[strings.LastIndex(d, "/")+1:]
			fmt.Fprintf(w, `{"id":"%s","name":"%s","dir":"%s","repo":"https://github.com/acme/%s.git"}`, base, base, d, base)
			return
		}
		id := strings.ReplaceAll(strings.TrimSuffix(b["repo"], ".git"), "/", "-")
		fmt.Fprintf(w, `{"id":"%s","name":"%s","repo":"%s"}`, id, b["repo"], b["repo"])
	})
	mux.HandleFunc("GET /api/fs/dirs", func(w http.ResponseWriter, r *http.Request) {
		p, hidden := r.URL.Query().Get("path"), r.URL.Query().Get("hidden") == "1"
		f.mu.Lock()
		f.fsPaths = append(f.fsPaths, p+"|"+r.URL.Query().Get("hidden"))
		f.mu.Unlock()
		tree := map[string][]string{
			"":              {"code", "docs", ".config", "notes"},
			"/home/u":       {"code", "docs", ".config", "notes"},
			"/home/u/code":  {"app*", "lib*", "misc"},
			"/home/u/docs":  {},
			"/home/u/notes": {"a"},
		}
		names, ok := tree[p]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error":"%s doesn't exist"}`, p)
			return
		}
		dir := cmpOr(p, "/home/u")
		parent := ""
		if dir != "/home/u" {
			parent = dir[:strings.LastIndex(dir, "/")]
		}
		var ents []string
		for _, n := range names {
			if strings.HasPrefix(n, ".") && !hidden {
				continue
			}
			ents = append(ents, fmt.Sprintf(`{"name":"%s","path":"%s/%s","git":%v}`, strings.TrimSuffix(n, "*"), dir, strings.TrimSuffix(n, "*"), strings.HasSuffix(n, "*")))
		}
		fmt.Fprintf(w, `{"path":"%s","parent":"%s","home":"/home/u","git":false,"dirs":[%s]}`, dir, parent, strings.Join(ents, ","))
	})
	mux.HandleFunc("GET /api/pack/stream", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
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
	mux.HandleFunc("POST /api/guests/{name}/{action}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.guestCalls = append(f.guestCalls, r.PathValue("name")+":"+r.PathValue("action")+"?"+r.URL.RawQuery)
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /api/threads/{id}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.stopped = append(f.stopped, r.PathValue("id"))
		f.mu.Unlock()
	})
	mux.HandleFunc("POST /api/projects/{id}/manager", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Text string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.managers = append(f.managers, r.PathValue("id")+":"+b.Text)
		f.mu.Unlock()
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"m-api"}`)
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
		case "ctrl+a":
			msg = tea.KeyMsg{Type: tea.KeyCtrlA}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
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
	if got := m.selected(colLeader).name; got != "mac" {
		t.Fatalf("leader = %q", got)
	}
	press(m, "right")
	if m.focus != colProject || m.selected(colProject).name != "api" { // by name; scratch last
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
	// Moving the project resets the threads.
	press(m, "left", "left", "down")
	if m.selected(colProject).name != "scratch" || m.sel[colThread] != 0 {
		t.Fatalf("project %v threadSel %d", m.selected(colProject).name, m.sel[colThread])
	}
	v := plain(m.View())
	for _, want := range []string{"PACK", "MONITOR", "● live", "leader mac", "term 3", "vault ok", "LEADER", "PROJECT AGENTS", "THREADS", "pack › mac › scratch", "new thread", "T-REX"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q", want)
		}
	}
	if strings.Contains(v, "HOSTS") || strings.Contains(v, "SUB-AGENTS") {
		t.Error("the PACK tab has no hosts column, and shows sub-agents only once stepped into")
	}
	// The T-rex sits at the bottom of the first column, under the leader.
	if h, r := strings.Index(v, "LEADER"), strings.Index(v, "T-REX"); h < 0 || r < h {
		t.Errorf("T-rex should be below LEADER (LEADER at %d, T-REX at %d)", h, r)
	}
}

func names(ns []node) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.name)
	}
	return out
}

func TestPackColumns(t *testing.T) {
	m := boot(t, newFake(t), 160, 45)
	// LEADER: one card with the pack's figures.
	lead := m.column(colLeader)
	if len(lead) != 1 || lead[0].name != "mac" || lead[0].body != [3]string{"term 3 · vault ok", "2/2 authorities up", "3/7 threads working"} {
		t.Fatalf("leader %+v", lead)
	}
	// PROJECT AGENTS: a card per project; the manager's own card has its workers; scratch last.
	if got := strings.Join(names(m.column(colProject)), ","); got != "api,web app,scratch" {
		t.Fatalf("projects %s", got)
	}
	ps := m.column(colProject)
	web := ps[1]
	if web.th == nil || web.th.ID != "m-web" || web.body[0] != "manager running" || web.body[1] != "2/3 workers working" || web.cost != 2.26 && web.cost < 2.25 {
		t.Errorf("web card %+v", web)
	}
	if !ps[0].noMgr || ps[0].body[0] != "no manager yet" || ps[0].th != nil {
		t.Errorf("api card %+v", ps[0])
	}
	if !ps[2].scratch || ps[2].body[1] != "0/1 threads working" {
		t.Errorf("scratch card %+v", ps[2])
	}
	// THREADS: the manager's workers and hand-started threads, newest first, no manager.
	press(m, "right", "down")
	if got := strings.Join(names(m.column(colThread)), ","); got != "fix the login bug,hand started,add dark mode,docs pass" {
		t.Fatalf("web threads %s", got)
	}
	th := m.column(colThread)[0]
	if th.body != [3]string{"running", "mac · g-t1", "2 sub-agents"} {
		t.Errorf("thread card %+v", th.body)
	}
	press(m, "left", "up") // api: no manager, one live thread (the archived one is behind a)
	if got := strings.Join(names(m.column(colThread)), ","); got != "rate limiter" {
		t.Fatalf("api threads %s", got)
	}
	press(m, "a")
	if got := strings.Join(names(m.column(colThread)), ","); got != "rate limiter,old thing" {
		t.Fatalf("api threads with archived %s", got)
	}
	v := plain(m.View())
	for _, want := range []string{"no manager yet", "enter starts one", "$0.84 · 2 threads"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q\n%s", want, v)
		}
	}
}

func TestEnterOpensTheManager(t *testing.T) {
	m := boot(t, newFake(t), 140, 40)
	press(m, "right", "down", "enter")
	if m.tab != tabThread || m.convID != "m-web" {
		t.Fatalf("tab %v conv %q", m.tab, m.convID)
	}
}

func TestStartAMissingManager(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 140, 40)
	press(m, "right", "enter") // api: no manager
	if m.form != nil || m.mgr == nil || m.tab != tabThread || m.convID != "" {
		t.Fatalf("enter did not open the manager conversation: form %v mgr %v tab %v", m.form, m.mgr, m.tab)
	}
	v := plain(m.View())
	for _, want := range []string{"PACK › api › manager", "not started", "This project has no manager yet", "Tell the manager what to do"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q\n%s", want, v)
		}
	}
	if strings.Contains(v, "Start the manager") {
		t.Error("the old modal is still drawn")
	}
	for _, r := range "triage the backlog" {
		press(m, string(r))
	}
	press(m, "enter")
	f.mu.Lock()
	got := f.managers
	f.mu.Unlock()
	if len(got) != 1 || got[0] != "api:triage the backlog" {
		t.Fatalf("managers %v", got)
	}
	if m.convID != "m-api" || m.tab != tabThread || m.mgr != nil {
		t.Errorf("did not switch to the new manager: %q", m.convID)
	}
	// Typing on the card seeds the input; esc goes back to the pack; scratch has no manager.
	m = boot(t, f, 140, 40)
	press(m, "right", "x")
	if m.mgr == nil || m.input.Value() != "x" {
		t.Fatalf("typing did not seed the conversation: %+v", m.mgr)
	}
	press(m, "esc")
	if m.tab != tabPack || m.mgr != nil {
		t.Error("esc did not return to the pack")
	}
	press(m, "down", "down", "enter")
	if m.mgr != nil {
		t.Error("scratch has no manager to start")
	}
}

func TestStartManagerErrorKeepsTheText(t *testing.T) {
	m := boot(t, newFake(t), 140, 40)
	press(m, "right", "enter")
	g := m.mgr
	m.input.SetValue("do the thing")
	g.text, g.starting = "do the thing", true
	m.input.SetValue("")
	m.Update(mgrStartedMsg{g, "", errors.New("boom")})
	v := plain(m.View())
	if m.input.Value() != "do the thing" || m.mgr != g || m.convID != "" {
		t.Errorf("text %q mgr %v conv %q", m.input.Value(), m.mgr, m.convID)
	}
	if !strings.Contains(v, "boom") || !strings.Contains(v, "❯ do the thing") {
		t.Errorf("error not shown\n%s", v)
	}
}

func TestMonitorColumns(t *testing.T) {
	m := boot(t, newFake(t), 140, 40)
	m.opts.Now = func() time.Time { return time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC) }
	press(m, "2")
	if got := strings.Join(names(m.monitorColumn(mcClient)), ","); got != "laptop,phone" {
		t.Fatalf("clients %s", got)
	}
	if got := strings.Join(names(m.monitorColumn(mcHost)), ","); got != "mac ★,nuc" {
		t.Fatalf("hosts %s", got)
	}
	// HOSTS is where we start: guests of the selected host (mac, where we are attached).
	if got := strings.Join(names(m.monitorColumn(mcGuest)), ","); got != "g-t1,g-t2,spare" {
		t.Fatalf("mac guests %s", got)
	}
	v := plain(m.View())
	for _, want := range []string{"CLIENTS", "HOSTS", "GUESTS", "laptop", "tui · via mac", "seen just now", "phone", "web · via nuc",
		"darwin · 10 cpu · load 1.50", "guests 2/6 · 2 working", "up 2h00m", "container · running", "cpu 35% · mem 1.2G/4.0G", "fix the login bug"} {
		if !strings.Contains(v, want) {
			t.Errorf("monitor lacks %q\n%s", want, v)
		}
	}
	if strings.Contains(v, "docs pass") || strings.Contains(v, "g-t5") {
		t.Error("another host's guests are shown")
	}
	// Moving down in HOSTS shows nuc's guests.
	press(m, "down")
	if m.mfocus != mcHost || strings.Join(names(m.monitorColumn(mcGuest)), ",") != "g-t5" {
		t.Fatalf("focus %d, nuc guests %v", m.mfocus, names(m.monitorColumn(mcGuest)))
	}
	// With CLIENTS focused, every host's guests.
	press(m, "left")
	if m.mfocus != mcClient || strings.Join(names(m.monitorColumn(mcGuest)), ",") != "g-t1,g-t2,spare,g-t5" {
		t.Fatalf("all guests %v", names(m.monitorColumn(mcGuest)))
	}
	// Selecting a client selects the host it came through.
	press(m, "down")
	if m.mselected(mcHost).name != "nuc" {
		t.Errorf("host after selecting phone: %v", m.mselected(mcHost).name)
	}
	// enter on a guest with a thread opens that thread; one without does nothing.
	press(m, "right", "right", "enter")
	if m.tab != tabThread || m.convID != "t5" {
		t.Fatalf("tab %v conv %q", m.tab, m.convID)
	}
	press(m, "esc", "2")
	press(m, "up", "enter") // mac: g-t1
	m.tab = tabMonitor
	m.mfocus, m.msel[mcHost], m.msel[mcGuest] = mcGuest, 0, 2
	press(m, "enter")
	if m.tab != tabMonitor {
		t.Error("enter on a guest without a thread left the monitor")
	}
}

func TestClientsEvent(t *testing.T) {
	m := boot(t, newFake(t), 140, 40)
	m.Update(feedMsg{SSE{Name: "pack.clients", Data: `{"type":"pack.clients","data":[{"id":"x","kind":"tui","device":"desk","authority":"nuc","since":"2026-11-27T09:00:00Z","last_seen":"2026-11-27T09:00:00Z"}]}`}})
	if got := strings.Join(names(m.monitorColumn(mcClient)), ","); got != "desk" {
		t.Fatalf("clients %s", got)
	}
	// A pack.delta keeps the clients.
	m.Update(feedMsg{SSE{Name: "pack.delta", Data: `{"type":"pack.delta","data":{"name":"nuc","url":"u","status":"up","host":{"os":"linux"}}}`}})
	if len(m.pack.Clients) != 1 {
		t.Error("a delta dropped the clients")
	}
}

func TestClientIdentifiesItself(t *testing.T) {
	f := newFake(t)
	c := NewClient(f.srv.URL)
	c.Device = "my-laptop"
	got := make(chan SSE, 1)
	go c.Stream(t.Context(), "/api/pack/stream", func(e SSE) { got <- e })
	<-got
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.headers[0]; h.Get("X-Rux-Client") != "tui" || h.Get("X-Rux-Device") != "my-laptop" {
		t.Errorf("headers %v", h)
	}
}

func TestConnectorsAndPreview(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 160, 45)
	press(m, "right", "down", "right") // thread "fix the login bug"
	v := plain(m.View())
	if !strings.Contains(v, "│───│") {
		t.Error("no straight connector between the selected cards")
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
		for _, tb := range []string{"1", "2", "?"} {
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

// TestRenderCapture writes each tab's view at 140 columns for the report when AUTHTUI_RENDER_OUT is set.
func TestRenderCapture(t *testing.T) {
	out := os.Getenv("AUTHTUI_RENDER_OUT")
	if out == "" {
		t.Skip("set AUTHTUI_RENDER_OUT to capture")
	}
	m := boot(t, newFake(t), 140, 38)
	m.opts.Now = func() time.Time { return time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC) }
	press(m, "right", "down", "right") // the web app's threads
	m.act["t1"] = []int{0, 1, 3, 2, 5, 1, 0, 4}
	var b strings.Builder
	b.WriteString("=== PACK ===\n" + plain(m.View()) + "\n")
	press(m, "enter") // the thread opens
	b.WriteString("=== THREAD ===\n" + plain(m.View()) + "\n")
	press(m, "tab")
	b.WriteString("=== MONITOR ===\n" + plain(m.View()) + "\n")
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
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
	f.pack.Authorities[0].Threads[2].Archived = true // "add dark mode"
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
	m.pack.Authorities[0].Threads[1].Status = "idle"
	press(m, "esc")
	if m.tab != tabPack {
		t.Error("esc did not go back from an idle thread")
	}
}

// The selected card sits in the middle of the graph area, both ways.
func TestPackCentersSelectedCard(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 160, 50)
	press(m, "right", "down", "right")
	lines := strings.Split(plain(m.View()), "\n")
	if len(lines) > 50 {
		t.Fatalf("%d lines overflow the terminal", len(lines))
	}
	bodyTop := 2 // header, blank
	bodyH := 50 - bodyTop - 2
	l := m.plan(bodyH)
	// The focused column is horizontally centred in the graph area.
	f0 := m.focus
	if cx := l.xs[f0] + l.widths[f0]/2; abs(cx-l.avail/2) > 2 {
		t.Errorf("focused column centre %d, graph area centre %d", cx, l.avail/2)
	}
	// Its selected card is drawn around the vertical middle of the body.
	var top, bot = -1, -1
	for y := bodyTop; y < bodyTop+bodyH; y++ {
		r := []rune(lines[y])
		if x := l.xs[f0] + l.widths[f0]/2; x < len(r) && (r[l.xs[f0]] == '╭' || r[l.xs[f0]] == '╰') {
			if r[l.xs[f0]] == '╭' && top < 0 {
				top = y - bodyTop
			}
			if r[l.xs[f0]] == '╰' && y-bodyTop >= bodyH/2 {
				if bot < 0 {
					bot = y - bodyTop
				}
			}
		}
	}
	if top < 0 || bot < 0 {
		t.Fatalf("selected card not found: %d %d\n%s", top, bot, strings.Join(lines, "\n"))
	}
	if mid := (top + bot) / 2; abs(mid-bodyH/2) > 1 {
		t.Errorf("card middle row %d, body middle %d", mid, bodyH/2)
	}
	// No row is wider than the screen, and the preview is intact.
	for i, ln := range lines {
		if w := len([]rune(ln)); w > 160 {
			t.Errorf("row %d is %d wide", i, w)
		}
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func TestPackRenderSizes(t *testing.T) {
	if testing.Short() || os.Getenv("DUMP") == "" {
		t.Skip("set DUMP=1 to print renderings")
	}
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {160, 50}, {200, 60}} {
		m := boot(t, newFake(t), sz[0], sz[1])
		press(m, "right", "down", "right")
		fmt.Printf("=== %dx%d\n%s\n", sz[0], sz[1], plain(m.View()))
	}
}

// On MONITOR too, the selected card is in the middle of the body, and the columns never run
// wider than the screen.
func TestMonitorCentersSelectedCard(t *testing.T) {
	for _, w := range []int{160, 100, 60} {
		m := boot(t, newFake(t), w, 50)
		press(m, "2", "right", "down") // monitor, hosts column, second row if any
		if m.tab != tabMonitor {
			t.Fatalf("tab %v", m.tab)
		}
		lines := strings.Split(plain(m.View()), "\n")
		if len(lines) > 50 {
			t.Fatalf("%d lines overflow", len(lines))
		}
		for i, ln := range lines {
			if n := len([]rune(ln)); n > w {
				t.Errorf("w=%d row %d is %d wide", w, i, n)
			}
		}
		bodyTop, bodyH := 2, 50-2-2
		n := m.mselected(m.mfocus)
		if n == nil {
			t.Fatal("nothing selected")
		}
		// Find the selected card's name on a row near the middle: card rows are mid-2..mid+2.
		found := -1
		for y := bodyTop; y < bodyTop+bodyH; y++ {
			if strings.Contains(lines[y], n.name) {
				found = y - bodyTop
				break
			}
		}
		// The name row is the card's first body row, cardH/2-2 above the middle.
		if found < 0 || abs(found+cardH/2-1-bodyH/2) > 1 {
			t.Errorf("w=%d: %q on body row %d, middle is %d", w, n.name, found, bodyH/2)
		}
		// Horizontally the focused column is centred.
		cw := min(max((w-2*gutterW)/numMCols, 24), 46)
		if w < 2*gutterW+numMCols*24 {
			cw = max(min(w, 46), 12)
		}
		widths := map[int]int{0: cw, 1: cw, 2: cw}
		xs, _ := centreXs(widths, numMCols, m.mfocus, w)
		if abs(xs[m.mfocus]+cw/2-w/2) > 2 {
			t.Errorf("w=%d focused column centre %d", w, xs[m.mfocus]+cw/2)
		}
	}
}

func TestMonitorRenderDump(t *testing.T) {
	if os.Getenv("DUMP") == "" {
		t.Skip("set DUMP=1 to print a rendering")
	}
	m := boot(t, newFake(t), 160, 50)
	press(m, "2", "right")
	fmt.Printf("=== monitor 160x50\n%s\n", plain(m.View()))
}

func TestHeaderBreadcrumb(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 160, 45)
	head := func() string { return strings.Split(plain(m.View()), "\n")[0] }
	if h := head(); strings.Contains(h, "THREAD") || !strings.Contains(h, "PACK") || !strings.Contains(h, "MONITOR") {
		t.Errorf("pack header %q", h)
	}
	press(m, "right", "down", "right", "enter") // fix the login bug
	if m.tab != tabThread {
		t.Fatalf("tab %v", m.tab)
	}
	h := head()
	if !strings.Contains(h, "PACK › web app › fix the login bug") || strings.Contains(h, "THREAD") || strings.Contains(h, "MONITOR") {
		t.Errorf("thread header %q", h)
	}
	if strings.Contains(plain(m.View()), "pack › ") {
		t.Error("the footer still has a breadcrumb on the thread view")
	}
	// A narrow header drops the middle crumb, then truncates the title.
	m.w = 70
	if h := head(); !strings.Contains(h, "PACK › ") || strings.Contains(h, "web app") {
		t.Errorf("narrow header %q", h)
	}
	m.w = 160
	// tab goes to MONITOR; 1 goes to PACK; 2 to MONITOR; 3 does nothing.
	press(m, "tab")
	if m.tab != tabMonitor {
		t.Fatalf("tab from thread: %v", m.tab)
	}
	press(m, "1")
	if m.tab != tabPack {
		t.Fatalf("1: %v", m.tab)
	}
	press(m, "2")
	if m.tab != tabMonitor {
		t.Fatalf("2: %v", m.tab)
	}
	press(m, "3")
	if m.tab != tabMonitor {
		t.Fatalf("3 changed the tab: %v", m.tab)
	}
	press(m, "tab", "right", "down", "right", "right", "enter") // a sub-agent
	if h := head(); !strings.Contains(h, "› sub-agent ") {
		t.Errorf("sub-agent header %q", h)
	}
	press(m, "esc")
	if m.tab != tabPack {
		t.Errorf("esc: %v", m.tab)
	}
}

func TestHeaderPackCrumbReturnsToPack(t *testing.T) {
	m := boot(t, newFake(t), 140, 40)
	press(m, "right", "down", "right", "enter")
	m.View()
	m.zones["crumb:pack"]()
	if m.tab != tabPack {
		t.Errorf("tab %v", m.tab)
	}
}

func TestHeaderDump(t *testing.T) {
	if os.Getenv("DUMP") == "" {
		t.Skip("set DUMP=1")
	}
	m := boot(t, newFake(t), 160, 45)
	first := func(tag string) {
		l := strings.Split(plain(m.View()), "\n")
		fmt.Printf("%-8s|%s\n", tag, l[0])
	}
	first("pack")
	press(m, "2")
	first("monitor")
	press(m, "1", "right", "down", "right", "enter")
	first("thread")
	fmt.Println(strings.Join(strings.Split(plain(m.View()), "\n")[:4], "\n"))
	press(m, "esc", "right", "right", "enter")
	first("sub")
}

func monitorGuest(m *Model, host, guest int) {
	m.tab = tabMonitor
	m.mfocus, m.msel[mcHost], m.msel[mcGuest] = mcGuest, host, guest
}

func (f *fake) guestLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.guestCalls, " ")
}

func TestMonitorGuestSleepWakeDown(t *testing.T) {
	f := newFake(t)
	m := boot(t, f, 160, 50)
	// s on a running guest sleeps it; on a stopped one wakes it.
	monitorGuest(m, 0, 2) // spare: running, no thread
	if !strings.Contains(plain(m.View()), "s  sleep") && !strings.Contains(plain(m.View()), "sleep") {
		t.Error("no sleep chip")
	}
	press(m, "s")
	monitorGuest(m, 0, 1) // g-t2: stopped
	m.flash = ""
	if !strings.Contains(plain(m.View()), "wake") {
		t.Error("no wake chip on a stopped guest")
	}
	press(m, "s")
	if got := f.guestLog(); got != "spare:sleep? g-t2:wake?" {
		t.Fatalf("calls %q", got)
	}
	if !strings.Contains(m.flash, "waking g-t2") {
		t.Errorf("flash %q", m.flash)
	}
	// d asks first; n and esc cancel without a request.
	monitorGuest(m, 0, 2)
	press(m, "d")
	v := plain(m.View())
	if m.downAsk == nil || !strings.Contains(v, "down spare? y/n") {
		t.Fatalf("no prompt:\n%s", v)
	}
	press(m, "n")
	press(m, "d", "esc")
	if m.downAsk != nil || strings.Contains(f.guestLog(), "down") {
		t.Fatalf("cancel sent %q", f.guestLog())
	}
	// y downs it.
	press(m, "d", "y")
	if got := f.guestLog(); !strings.HasSuffix(got, "spare:down?") {
		t.Fatalf("calls %q", got)
	}
	// A guest whose thread is working: the prompt names the thread and the request forces.
	monitorGuest(m, 0, 0) // g-t1: thread t1 is running
	press(m, "d")
	if v := plain(m.View()); !strings.Contains(v, "down g-t1 (thread t1), which is working right now? y/n") {
		t.Fatalf("prompt:\n%s", v)
	}
	press(m, "y")
	if got := f.guestLog(); !strings.HasSuffix(got, "g-t1:down?force=1") {
		t.Fatalf("calls %q", got)
	}
	// A guest on another authority names it.
	monitorGuest(m, 1, 0) // nuc: g-t5
	press(m, "s")
	if got := f.guestLog(); !strings.HasSuffix(got, "g-t5:sleep?authority=nuc") {
		t.Fatalf("calls %q", got)
	}
	// Other columns ignore the keys.
	n := len(f.guestCalls)
	m.mfocus = mcHost
	press(m, "s", "d")
	if len(f.guestCalls) != n || m.downAsk != nil {
		t.Error("s/d acted outside the guests column")
	}
}
