package rux

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rex-agents/r-x/internal/harness"
)

const fakeGuest = "rv-fake-0123456789ab"

// fakeRox puts a rox on PATH that logs its calls to a file. MODE picks what rox up does.
func fakeRox(t *testing.T, mode string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "$@" >> "` + log + `"
case "$1" in
up)
  echo "Starting ` + fakeGuest + `"
  case "` + mode + `" in
  hang) sleep 1; echo "Started ` + fakeGuest + `"; exit 0 ;;
  fail) echo "boom"; exit 1 ;;
  ok) echo "Started ` + fakeGuest + `"; exit 0 ;;
  silent) echo "an unrelated rv-other-aaaaaaaaaaaa guest exists"; exit 0 ;;
  esac ;;
inspect) echo '{"name":"` + fakeGuest + `","home":"/home/x"}' ;;
down) [ -f "` + dir + `/downfail" ] && { echo nope; exit 1; }; exit 0 ;;
esac
exit 0
`
	if mode == "silent" {
		script = strings.Replace(script, `echo "Starting `+fakeGuest+`"`, ":", 1)
	}
	if err := os.WriteFile(filepath.Join(dir, "rox"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func testRun(t *testing.T) *hrun {
	t.Helper()
	r := &hrun{subs: map[string]*subInfo{}, watching: map[string]int{}, effort: map[string]string{},
		changed: make(chan struct{}, 1), approvals: make(chan *approval)}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	t.Cleanup(r.cancel)
	lead := harness.NewAgent("lead", "lead", "lead")
	r.lead, r.agents = lead, []*harness.Agent{lead}
	return r
}

func spawn(t *testing.T, r *hrun, name string) (*harness.Agent, *subInfo) {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"name": name, "task": "do it", "repo": "none", "model": "claude-code"})
	if _, err := r.spawnTool(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	return r.find(name), r.subOf(r.find(name))
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readLog(log string) string { b, _ := os.ReadFile(log); return string(b) }

func TestLaunchedGuest(t *testing.T) {
	for line, want := range map[string]string{
		"Starting " + fakeGuest:                      fakeGuest,
		"◐ Starting " + fakeGuest + " …":             fakeGuest,
		"✓ Started " + fakeGuest + " · container":    fakeGuest,
		"Workspace " + fakeGuest:                     fakeGuest,
		"\x1b[1mStarting\x1b[0m \x1b[1m" + fakeGuest: fakeGuest,
		"Registered Rex host " + fakeGuest + " here": fakeGuest,
		"Using the rv-other-aaaaaaaaaaaa guest":      "",
		"rv-other-aaaaaaaaaaaa is reachable":         "",
		"Cloning something into " + fakeGuest:        "",
		"Starting rv-short":                          "",
	} {
		if got := launchedGuest(line); got != want {
			t.Errorf("launchedGuest(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestStopDuringProvisionRemovesGuestAndStaysStopped(t *testing.T) {
	log := fakeRox(t, "hang")
	r := testRun(t)
	a, s := spawn(t, r, "w1")
	waitFor(t, "guest named", func() bool { return r.guestOf(s) == fakeGuest })
	msg, err := r.stop("w1", true)
	if err != nil || !strings.Contains(msg, "removed") {
		t.Fatalf("stop = %q, %v", msg, err)
	}
	if !strings.Contains(readLog(log), "down "+fakeGuest) {
		t.Errorf("rox down not called: %s", readLog(log))
	}
	<-s.done
	time.Sleep(100 * time.Millisecond)
	if st, _ := a.Status(); st != harness.Stopped {
		t.Errorf("status = %s, want stopped", st)
	}
	if r.guestOf(s) != "" {
		t.Errorf("guest still recorded after removal")
	}
}

func TestStopRightAfterSpawnNeverAttaches(t *testing.T) {
	fakeRox(t, "ok")
	r := testRun(t)
	a, s := spawn(t, r, "w2")
	if _, err := r.stop("w2", true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if st, _ := a.Status(); st != harness.Stopped {
		t.Errorf("status = %s, want stopped", st)
	}
	if s.exec != nil && r.agentAttached(a) {
		t.Errorf("a stopped agent was attached")
	}
}

func (r *hrun) agentAttached(a *harness.Agent) bool { return a.Where != "" }

func TestFailedUpKeepsGuestForCleanup(t *testing.T) {
	log := fakeRox(t, "fail")
	r := testRun(t)
	a, s := spawn(t, r, "w3")
	waitFor(t, "failed", func() bool { st, _ := a.Status(); return st == harness.Failed })
	if r.guestOf(s) != fakeGuest {
		t.Fatalf("guest = %q, want it recorded", r.guestOf(s))
	}
	if !strings.Contains(a.Snapshot().Err, fakeGuest) {
		t.Errorf("error doesn't name the guest: %q", a.Snapshot().Err)
	}
	if _, err := r.stop("w3", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(log), "down "+fakeGuest) {
		t.Errorf("failed guest not removed: %s", readLog(log))
	}
}

func TestUnnamedGuestIsNotGuessed(t *testing.T) {
	log := fakeRox(t, "silent")
	r := testRun(t)
	a, s := spawn(t, r, "w4")
	waitFor(t, "failed", func() bool { st, _ := a.Status(); return st == harness.Failed })
	if g := r.guestOf(s); g != "" {
		t.Errorf("guest = %q, want none", g)
	}
	r.stop("w4", true)
	if strings.Contains(readLog(log), "down") {
		t.Errorf("removed a guest it couldn't verify: %s", readLog(log))
	}
}

func TestStopKeepsGuestWhenDownFails(t *testing.T) {
	log := fakeRox(t, "hang")
	r := testRun(t)
	_, s := spawn(t, r, "w5")
	waitFor(t, "guest named", func() bool { return r.guestOf(s) == fakeGuest })
	os.WriteFile(filepath.Join(filepath.Dir(log), "downfail"), nil, 0o600)
	if _, err := r.stop("w5", true); err == nil {
		t.Fatal("stop succeeded though rox down failed")
	}
	if r.guestOf(s) != fakeGuest {
		t.Fatalf("guest forgotten after failed removal")
	}
	os.Remove(filepath.Join(filepath.Dir(log), "downfail"))
	if _, err := r.stop("w5", true); err != nil || r.guestOf(s) != "" {
		t.Errorf("retry: %v, guest %q", err, r.guestOf(s))
	}
}

func TestSuccessfulProvisionAttaches(t *testing.T) {
	fakeRox(t, "ok")
	r := testRun(t)
	a, s := spawn(t, r, "w6")
	waitFor(t, "attached", func() bool { st, _ := a.Status(); return st == harness.Waiting })
	if r.guestOf(s) != fakeGuest || s.Dir != "/home/x" {
		t.Errorf("guest %q dir %q", r.guestOf(s), s.Dir)
	}
}

func TestWaitIgnoresQueuedMessagesOfTerminalAgents(t *testing.T) {
	fakeRox(t, "fail")
	r := testRun(t)
	a, _ := spawn(t, r, "w7")
	waitFor(t, "failed", func() bool { st, _ := a.Status(); return st == harness.Failed })
	if a.Snapshot().Queued == 0 {
		t.Fatal("the initial task should still be queued")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := r.waitTool(ctx, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "failed") || ctx.Err() != nil {
		t.Fatalf("wait all = %q, %v", out, err)
	}
	out, err = r.waitTool(ctx, json.RawMessage(`{"agents":["w7"]}`))
	if err != nil || !strings.Contains(out, "Error:") || !strings.Contains(out, "never read") || ctx.Err() != nil {
		t.Fatalf("wait w7 = %q, %v", out, err)
	}
	// message_agent says so, too.
	for _, tl := range r.leadTools() {
		if tl.Spec.Name == "message_agent" {
			if _, err := tl.Run(ctx, json.RawMessage(`{"agent":"w7","message":"hi"}`)); err == nil || !strings.Contains(err.Error(), "for good") {
				t.Errorf("message_agent error = %v", err)
			}
		}
	}
}

func cliAgent(t *testing.T, mut func(*subAgent)) *subAgent {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("REX_SESSION", "s1")
	dir := sessionDir("s1")
	os.MkdirAll(dir, 0o700)
	a := &subAgent{ID: "c1", Name: "c1", Agent: "claude", Session: "s1", Phase: "provisioning", Guest: fakeGuest, GuestType: "container",
		Created: time.Now(), path: filepath.Join(dir, "c1.json")}
	if mut != nil {
		mut(a)
	}
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAgentStopRemovesNamedGuestAndMarksStopped(t *testing.T) {
	log := fakeRox(t, "ok")
	a := cliAgent(t, func(a *subAgent) { a.Host = "edge" })
	agentStop([]string{"c1"})
	got, _ := loadAgent(a.path)
	if got.Phase != "stopped" || got.Guest != "" || !got.StopDone {
		t.Errorf("state = %+v", got)
	}
	if !strings.Contains(readLog(log), "down -S edge "+fakeGuest) {
		t.Errorf("rox down didn't route to the host: %s", readLog(log))
	}
}

func TestAgentStopKeepsSharedGuestAndFailedRemovalKeepsRecord(t *testing.T) {
	log := fakeRox(t, "ok")
	a := cliAgent(t, func(a *subAgent) { a.Shared = true })
	agentStop([]string{"c1"})
	if strings.Contains(readLog(log), "down") {
		t.Errorf("removed a shared guest")
	}
	a = cliAgent(t, nil)
	os.WriteFile(filepath.Join(filepath.Dir(log), "downfail"), nil, 0o600)
	if err := removeGuest(a); err == nil {
		t.Fatal("expected an error")
	}
	if got, _ := loadAgent(a.path); got.Guest != fakeGuest {
		t.Errorf("guest forgotten after failed removal: %+v", got)
	}
}

func TestStopLetsStartedUpFinishAndNeverAttaches(t *testing.T) {
	log := fakeRox(t, "hang")
	r := testRun(t)
	a, s := spawn(t, r, "w8")
	waitFor(t, "guest named", func() bool { return r.guestOf(s) == fakeGuest })
	start := time.Now()
	if _, err := r.stop("w8", true); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Errorf("stop didn't wait for rox up to finish")
	}
	if !strings.Contains(readLog(log), "down "+fakeGuest) || strings.Contains(readLog(log), "inspect") {
		t.Errorf("log: %s", readLog(log))
	}
	if st, _ := a.Status(); st != harness.Stopped || a.Where != "" {
		t.Errorf("status %s, where %q", st, a.Where)
	}
}
