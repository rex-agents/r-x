package rux

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testServer(t *testing.T) (*server, *thread, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	s := &server{threads: map[string]*thread{}, backend: "rox", dataDir: dir,
		models: &modelCache{claudeBin: "/nonexistent/claude", dir: dir}, tools: newToolHost(dir)}
	th, err := openThread(filepath.Join(dir, "threads", "t1"), &threadMeta{ID: "t1", Token: "tok", Status: "idle", Prompt: "do it", Backend: "rox", Guest: "rv-x-0123456789ab"})
	if err != nil {
		t.Fatal(err)
	}
	th.mu.Lock()
	th.queuePromptLocked()
	th.mu.Unlock()
	s.threads["t1"] = th
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runner", s.handleRunner)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, th, srv
}

// fakeRunner connects like a runner does and collects the user messages the server writes.
type fakeRunner struct {
	c    *websocket.Conn
	user chan wireMsg
}

func dialRunner(t *testing.T, srv *httptest.Server, acks bool) *fakeRunner {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/runner?thread=t1&token=" + url.QueryEscape("tok")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	info := map[string]string{"version": "x"}
	if acks {
		info["acks"] = "1"
	}
	if err := writeJSON(ctx, c, wireMsg{T: "hello", Info: info}); err != nil {
		t.Fatal(err)
	}
	var sync wireMsg
	if err := readJSON(ctx, c, &sync); err != nil || sync.T != "sync" {
		t.Fatalf("sync: %v %+v", err, sync)
	}
	fr := &fakeRunner{c: c, user: make(chan wireMsg, 1000)}
	c.SetReadLimit(wsReadLimit)
	go func() {
		for {
			var m wireMsg
			if err := readJSON(context.Background(), c, &m); err != nil {
				return
			}
			if m.T == "user" {
				fr.user <- m
			}
		}
	}()
	t.Cleanup(func() { c.CloseNow() })
	return fr
}

func (fr *fakeRunner) take(t *testing.T, n int) []wireMsg {
	t.Helper()
	var out []wireMsg
	for len(out) < n {
		select {
		case m := <-fr.user:
			out = append(out, m)
		case <-time.After(10 * time.Second):
			t.Fatalf("got %d of %d messages", len(out), n)
		}
	}
	return out
}

func (fr *fakeRunner) ack(t *testing.T, ms []wireMsg) {
	t.Helper()
	for _, m := range ms {
		if err := writeJSON(context.Background(), fr.c, wireMsg{T: "ack", ID: m.ID}); err != nil {
			t.Fatal(err)
		}
	}
}

func outboxLen(th *thread) int {
	th.mu.Lock()
	defer th.mu.Unlock()
	return len(th.outbox)
}

func waitOutbox(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A backlog above the old 64-slot channel is queued while no runner is connected, delivered
// on connect without blocking, kept when the connection drops before acks, resent on the
// next connection under the same ids, and gone once acked.
func TestOutboxBacklogReplayAndAck(t *testing.T) {
	_, th, srv := testServer(t)
	const n = 150
	th.mu.Lock()
	for i := 0; i < n; i++ {
		if err := th.queueLocked(wireMsg{T: "user", Text: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	th.mu.Unlock()

	done := make(chan struct{})
	var first []wireMsg
	var fr *fakeRunner
	go func() {
		fr = dialRunner(t, srv, true)
		first = fr.take(t, n+1) // the prompt, then the 150
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("connect with a large backlog hung")
	}
	if first[0].Text != "do it" || first[0].ID != "prompt" || first[1].Text != "m0" || first[n].Text != fmt.Sprintf("m%d", n-1) {
		t.Fatalf("order or ids wrong: %q %q %q", first[0].Text, first[1].Text, first[n].Text)
	}
	// Disconnect before any ack: nothing is lost.
	fr.c.CloseNow()
	waitOutbox(t, "disconnect", func() bool { th.mu.Lock(); defer th.mu.Unlock(); return !th.meta.Connected })
	if got := outboxLen(th); got != n+1 {
		t.Fatalf("outbox after disconnect = %d, want %d", got, n+1)
	}
	fr2 := dialRunner(t, srv, true)
	second := fr2.take(t, n+1)
	for i := range second {
		if second[i].ID != first[i].ID {
			t.Fatalf("message %d came back with id %q, was %q", i, second[i].ID, first[i].ID)
		}
	}
	// A new message while connected goes out without a reconnect.
	if _, err := (&server{}).enqueueUser(th, wireMsg{T: "user", Text: "live"}, false); err != nil {
		t.Fatal(err)
	}
	live := fr2.take(t, 1)
	if live[0].Text != "live" {
		t.Fatalf("live message = %q", live[0].Text)
	}
	fr2.ack(t, append(second, live...))
	waitOutbox(t, "acks", func() bool { return outboxLen(th) == 0 })
}

// The outbox survives a server restart, and acked messages don't come back.
func TestOutboxReload(t *testing.T) {
	_, th, _ := testServer(t)
	th.mu.Lock()
	for _, txt := range []string{"a", "b", "c"} {
		th.queueLocked(wireMsg{T: "user", ID: "id-" + txt, Text: txt, Files: []attachment{{Name: "x.png", Mime: "image/png", Data: []byte{1, 2, 3}, URL: "u"}}})
	}
	th.queueLocked(wireMsg{T: "user", ID: "id-a", Text: "dup"}) // same id: not added twice
	th.ackLocked("id-b")
	th.mu.Unlock()

	re, err := openThread(th.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(re.outbox) != 3 || re.outbox[1].ID != "id-a" || re.outbox[1].Msg.Text != "a" || re.outbox[2].ID != "id-c" {
		t.Fatalf("reloaded outbox = %+v", re.outbox)
	}
	if f := re.outbox[1].Msg.Files; len(f) != 1 || string(f[0].Data) != "\x01\x02\x03" {
		t.Fatalf("attachment lost on reload: %+v", f)
	}
	// And once more after compaction.
	re2, err := openThread(th.dir, nil)
	if err != nil || len(re2.outbox) != 3 {
		t.Fatalf("second reload: %v %+v", err, re2.outbox)
	}
}

// A runner that doesn't ack (one from before acks) gets each message once, as before.
func TestOutboxLegacyRunnerNoAck(t *testing.T) {
	_, th, srv := testServer(t)
	fr := dialRunner(t, srv, false)
	fr.take(t, 1) // the prompt
	waitOutbox(t, "implicit ack", func() bool { return outboxLen(th) == 0 })
}

// An asleep thread keeps a message for the runner that connects after the wake.
func TestEnqueueWhileAsleep(t *testing.T) {
	s, th, srv := testServer(t)
	th.mu.Lock()
	th.meta.Status = "failed"
	th.meta.Connected = false
	th.mu.Unlock()
	if _, err := s.enqueueUser(th, wireMsg{T: "user", Text: "x"}, false); err != errNoRunner {
		t.Fatalf("a failed thread with no runner should refuse, got %v", err)
	}
	th.mu.Lock()
	th.meta.Status = "asleep"
	th.mu.Unlock()
	s.roxBin = "/nonexistent/rox" // the wake fails; the message must stay queued
	if _, err := s.enqueueUser(th, wireMsg{T: "user", Text: "while asleep"}, false); err != nil {
		t.Fatal(err)
	}
	fr := dialRunner(t, srv, true)
	got := fr.take(t, 2)
	if got[1].Text != "while asleep" {
		t.Fatalf("got %+v", got)
	}
}

func newTestRunner(t *testing.T, repo, dir string) *runner {
	t.Helper()
	r := &runner{thread: "t1", stateDir: filepath.Join(t.TempDir(), "state"), dir: dir, repo: repo}
	r.ready, r.retry, r.inNotify = make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	if err := r.load(); err != nil {
		t.Fatal(err)
	}
	r.loadInbox()
	return r
}

func TestRunnerAcceptDedupeAndRestart(t *testing.T) {
	r := newTestRunner(t, "", filepath.Join(t.TempDir(), "w"))
	if !r.accept(wireMsg{T: "user", ID: "m1", Text: "one"}) || !r.accept(wireMsg{T: "user", ID: "m1", Text: "one"}) {
		t.Fatal("accept should ack, including a repeat")
	}
	if r.accept(wireMsg{T: "user", Text: "no id"}) {
		t.Error("a message with no id can't be acked")
	}
	for i := 0; i < 200; i++ { // never blocks, however many arrive before the workspace is ready
		r.accept(wireMsg{T: "user", ID: fmt.Sprintf("b%d", i), Text: "x"})
	}
	if len(r.inQ) != 202 {
		t.Fatalf("queue = %d", len(r.inQ))
	}
	// A restart restores what wasn't delivered, in order, and still ignores repeats.
	r2 := &runner{stateDir: r.stateDir, dir: r.dir}
	r2.ready, r2.retry, r2.inNotify = make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	if err := r2.load(); err != nil {
		t.Fatal(err)
	}
	r2.loadInbox()
	if len(r2.inQ) != 201 || r2.inQ[0].msg.ID != "m1" {
		t.Fatalf("restored %d messages, first %q", len(r2.inQ), r2.inQ[0].msg.ID)
	}
	if !r2.accept(wireMsg{T: "user", ID: "m1", Text: "one"}) || len(r2.inQ) != 201 {
		t.Fatal("a resend of a queued message must not queue it again")
	}
	// Delivered messages are in the events as rux_user with their id, so they stay seen.
	im := r2.head()
	r2.emit(ruxEv("rux_user", "text", "one", "id", im.msg.ID))
	r2.finish(im)
	if _, err := os.Stat(im.file); !os.IsNotExist(err) {
		t.Error("the inbox file should be gone once delivered")
	}
	r3 := &runner{stateDir: r.stateDir, dir: r.dir}
	r3.ready, r3.retry, r3.inNotify = make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	if err := r3.load(); err != nil {
		t.Fatal(err)
	}
	r3.loadInbox()
	if !r3.seen["m1"] || len(r3.inQ) != 200 {
		t.Fatalf("seen m1 = %v, queue %d", r3.seen["m1"], len(r3.inQ))
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func setupEvents(r *runner) (failed, ready bool) {
	for _, l := range r.lines {
		s := string(l)
		failed = failed || strings.Contains(s, "rux_setup_failed")
		ready = ready || strings.Contains(s, `rux_ready`)
	}
	return
}

// A failed clone is terminal: no rux_ready, nothing runs, no empty directory is left in its
// place. The same runner retries into the same path once the repo can be cloned.
func TestPrepareCloneFailureThenRetry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "work", "proj")
	missing := filepath.Join(root, "no-such-repo")
	r := newTestRunner(t, missing, dir)
	if r.prepare() {
		t.Fatal("prepare succeeded for a repo that doesn't exist")
	}
	if failed, ready := setupEvents(r); !failed || ready {
		t.Fatalf("failed=%v ready=%v", failed, ready)
	}
	select {
	case <-r.ready:
		t.Fatal("ready closed after a failed checkout")
	default:
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a failed clone left a directory for claude to run in")
	}
	// claude doesn't start while the workspace isn't ready.
	r.claudeBin = "/nonexistent/claude"
	r.sendUser("m", "hi")
	if r.cmd != nil {
		t.Fatal("started claude in a workspace that failed to check out")
	}

	repo := filepath.Join(root, "src")
	os.MkdirAll(repo, 0o755)
	gitInit(t, repo)
	r.repo = repo
	if !r.prepare() {
		t.Fatal("retry should succeed once the repo exists")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("no checkout after retry: %v", err)
	}
	if _, ready := setupEvents(r); !ready {
		t.Error("no rux_ready after a good retry")
	}
	if ents, _ := filepath.Glob(dir + ".rux-clone-*"); len(ents) != 0 {
		t.Errorf("temporary clone left behind: %v", ents)
	}
}

// A directory that already has someone's files is never replaced by a retry.
func TestPrepareKeepsExistingFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	os.MkdirAll(dir, 0o755)
	keep := filepath.Join(dir, "notes.txt")
	os.WriteFile(keep, []byte("mine"), 0o644)
	repo := filepath.Join(root, "src")
	os.MkdirAll(repo, 0o755)
	gitInit(t, repo)
	r := newTestRunner(t, repo, dir)
	if r.prepare() {
		t.Fatal("prepare should refuse a directory with files")
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "mine" {
		t.Fatalf("user file touched: %v %q", err, b)
	}
	// An empty directory is fine to clone into.
	os.Remove(keep)
	if !r.prepare() {
		t.Fatal("an empty directory should work")
	}
}

// With no repo asked for, the workspace is a fresh repo, as before.
func TestPrepareRepoless(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scratch")
	r := newTestRunner(t, "", dir)
	if !r.prepare() {
		t.Fatal("repo-less workspace failed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
}

// The server shows a checkout failure as a failed thread, tells the manager, and a runner
// reconnect doesn't turn it back into idle.
func TestSetupFailedStatus(t *testing.T) {
	_, th, srv := testServer(t)
	th.meta.Parent = "mgr"
	th.mu.Lock()
	th.meta.Status = "starting"
	th.statusFromEventLocked(ruxEv("rux_setup_failed", "text", "boom"))
	wake := th.wakeTextLocked(ruxEv("rux_setup_failed", "text", "boom"))
	st, sf := th.meta.Status, th.meta.SetupFailed
	th.mu.Unlock()
	if st != "failed" || !sf || !strings.Contains(wake, "boom") {
		t.Fatalf("status %q setup_failed %v wake %q", st, sf, wake)
	}
	dialRunner(t, srv, true)
	waitOutbox(t, "connect", func() bool { th.mu.Lock(); defer th.mu.Unlock(); return th.meta.Connected })
	if th.view().Status != "failed" {
		t.Fatalf("reconnect changed a failed checkout to %q", th.view().Status)
	}
	th.mu.Lock()
	th.statusFromEventLocked(ruxEv("rux_preparing"))
	th.statusFromEventLocked(ruxEv("rux_ready"))
	th.mu.Unlock()
	if th.view().Status != "idle" {
		t.Fatalf("after a good retry status = %q", th.view().Status)
	}
}

// A torn tail from a crash doesn't swallow the next record, and queueing the prompt again
// after a crash (outbox committed, PromptSent not saved) adds no second initial message.
func TestOutboxTornTailAndPromptWindow(t *testing.T) {
	_, th, _ := testServer(t)
	th.mu.Lock()
	th.queueLocked(wireMsg{T: "user", ID: "a", Text: "a"})
	th.mu.Unlock()
	f, _ := os.OpenFile(th.outboxPath(), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"op":"add","id":"torn","msg":{"t":"us`) // crash mid-write
	f.Close()
	re, err := openThread(th.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	re.mu.Lock()
	re.queueLocked(wireMsg{T: "user", ID: "b", Text: "b"})
	re.meta.PromptSent = false // the crash came before the flag was saved
	re.queuePromptLocked()
	re.mu.Unlock()
	re2, _ := openThread(th.dir, nil)
	ids := []string{}
	for _, it := range re2.outbox {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "prompt,a,b" {
		t.Fatalf("outbox ids = %v", ids)
	}
}

// A new runner still takes legacy messages that carry no id (from a server before acks),
// runs them once, and doesn't ack them.
func TestRunnerLegacyServerMessage(t *testing.T) {
	r := newTestRunner(t, "", filepath.Join(t.TempDir(), "w"))
	if r.accept(wireMsg{T: "user", Text: "old style"}) {
		t.Error("no id, so nothing to ack")
	}
	if len(r.inQ) != 1 || r.inQ[0].msg.Text != "old style" {
		t.Fatalf("queue %+v", r.inQ)
	}
}
