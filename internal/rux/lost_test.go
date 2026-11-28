package rux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRoxGone writes a rox script that logs its arguments and fails "stop", "start" and "exec"
// the way rox does for a guest that doesn't exist.
func fakeRoxGone(t *testing.T, s *server) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "rox")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" +
		"case \"$1\" in stop|start|exec) echo '✗ rox: No managed workspace named '\"$2\"' here or on any known host.' >&2; exit 1;; esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s.roxBin, s.advertise, s.memory = bin, "127.0.0.1:1", "1g"
	return log
}

func countCalls(log, verb string) int {
	b, _ := os.ReadFile(log)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, verb+" ") {
			n++
		}
	}
	return n
}

func waitStatus(t *testing.T, th *thread, want string) {
	t.Helper()
	waitOutbox(t, "status "+want, func() bool { return th.view().Status == want })
}

func TestGuestGone(t *testing.T) {
	for out, want := range map[string]bool{
		"✗ rox: No managed workspace named rv-x here or on any known host.": true,
		"\x1b[31mNo managed workspace named x\x1b[0m":                       true,
		"connection refused": false,
		"":                   false,
	} {
		if got := guestGone(out); got != want {
			t.Errorf("guestGone(%q) = %v", out, got)
		}
	}
}

func TestSleepStopOfVanishedGuestMarksLost(t *testing.T) {
	s, th, _ := testServer(t)
	log := fakeRoxGone(t, s)
	s.sleepAfter = time.Minute
	th.mu.Lock()
	th.meta.Updated = time.Now().Add(-time.Hour)
	th.outbox = nil
	th.mu.Unlock()
	s.sleepThread(th)
	if st := th.view().Status; st != "lost" {
		t.Fatalf("status = %s", st)
	}
	if s.sleepy(th) {
		t.Fatal("a lost thread must not be put to sleep again")
	}
	if n := countCalls(log, "stop"); n != 1 {
		t.Fatalf("stop calls = %d", n)
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	var found bool
	for _, l := range th.events {
		found = found || strings.Contains(string(l), "rux_guest_lost")
	}
	if !found {
		t.Fatal("no rux_guest_lost event")
	}
}

func TestMessageToLostThreadReprovisions(t *testing.T) {
	s, th, _ := testServer(t)
	log := fakeRoxGone(t, s)
	th.mu.Lock()
	th.outbox = nil
	th.appendLocked("runner", 1, ruxEv("rux_user", "text", "first question"))
	th.appendLocked("runner", 2, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"first answer"}]}}`))
	th.meta.Status = "lost"
	th.mu.Unlock()
	if _, err := s.enqueueUser(th, wireMsg{T: "user", ID: "m1", Text: "second question"}, false); err != nil {
		t.Fatal(err)
	}
	waitOutbox(t, "rox up", func() bool { return countCalls(log, "up") == 1 })
	th.mu.Lock()
	defer th.mu.Unlock()
	if len(th.outbox) != 1 {
		t.Fatalf("outbox = %+v", th.outbox)
	}
	txt := th.outbox[0].Msg.Text
	for _, w := range []string{"do it", "first question", "first answer", "second question"} {
		if !strings.Contains(txt, w) {
			t.Errorf("message lacks %q: %s", w, txt)
		}
	}
	if !strings.HasSuffix(txt, "second question") || th.outbox[0].ID != "m1" {
		t.Errorf("recap should come first: %s", txt)
	}
	if th.meta.Guest == "rv-x-0123456789ab" {
		t.Error("old guest kept")
	}
	if th.rseq != 0 {
		t.Errorf("rseq = %d", th.rseq)
	}
}

func TestWakeOfVanishedGuestMarksLostAndReprovisions(t *testing.T) {
	s, th, _ := testServer(t)
	log := fakeRoxGone(t, s)
	th.mu.Lock()
	th.meta.Status = "asleep"
	th.outbox = nil
	th.mu.Unlock()
	if _, err := s.enqueueUser(th, wireMsg{T: "user", Text: "hello again"}, false); err != nil {
		t.Fatal(err)
	}
	waitOutbox(t, "rox up", func() bool { return countCalls(log, "up") == 1 })
	if n := countCalls(log, "start"); n != 1 {
		t.Fatalf("start calls = %d", n)
	}
	th.mu.Lock()
	defer th.mu.Unlock()
	if len(th.outbox) != 1 || !strings.Contains(th.outbox[0].Msg.Text, "hello again") || !strings.Contains(th.outbox[0].Msg.Text, "do it") {
		t.Fatalf("outbox = %+v", th.outbox)
	}
}

func TestLostSharedGuestRefuses(t *testing.T) {
	s, th, _ := testServer(t)
	log := fakeRoxGone(t, s)
	th.mu.Lock()
	th.meta.Status, th.meta.SharedGuest = "lost", true
	th.mu.Unlock()
	if _, err := s.enqueueUser(th, wireMsg{T: "user", Text: "x"}, false); err != errGuestGone {
		t.Fatalf("err = %v", err)
	}
	if countCalls(log, "up") != 0 || th.view().Status != "lost" {
		t.Fatal("a shared guest must not be re-provisioned")
	}
}
