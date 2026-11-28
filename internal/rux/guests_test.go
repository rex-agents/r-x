package rux

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRoxOK writes a rox that logs its arguments and succeeds.
func fakeRoxOK(t *testing.T, s *server) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "rox")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.roxBin, s.advertise, s.memory = bin, "127.0.0.1:1", "1g"
	return log
}

func guestCall(s *server, action, name, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/guests/"+name+"/"+action+query, nil)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	s.guestAction(action)(w, r)
	return w
}

func guestServer(t *testing.T) (*server, *thread, string) {
	s, th, _ := testServer(t)
	log := fakeRoxOK(t, s)
	phases := map[string]string{"rv-x-0123456789ab": "running", "spare": "running", "napping": "stopped"}
	s.guestLister = func() []roxGuest {
		var out []roxGuest
		for n, p := range phases {
			out = append(out, roxGuest{Name: n, Backend: "container", Phase: p})
		}
		return out
	}
	s.guestStats = func() []containerStat { return nil }
	th.mu.Lock()
	th.outbox = nil
	th.mu.Unlock()
	return s, th, log
}

func TestGuestSleepThreadBacked(t *testing.T) {
	s, th, log := guestServer(t)
	if w := guestCall(s, "sleep", "rv-x-0123456789ab", ""); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitStatus(t, th, "asleep")
	if countCalls(log, "stop") != 1 {
		t.Errorf("rox stop calls: %d", countCalls(log, "stop"))
	}
	// Sleeping a stopped guest again is fine and runs nothing.
	s.guestLister = func() []roxGuest { return []roxGuest{{Name: "rv-x-0123456789ab", Phase: "stopped"}} }
	if w := guestCall(s, "sleep", "rv-x-0123456789ab", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "already") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if countCalls(log, "stop") != 1 {
		t.Error("stopped twice")
	}
}

func TestGuestSleepBusyThread(t *testing.T) {
	s, th, log := guestServer(t)
	th.mu.Lock()
	th.meta.Status = "running"
	th.mu.Unlock()
	if w := guestCall(s, "sleep", "rv-x-0123456789ab", ""); w.Code != 409 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if countCalls(log, "stop") != 0 {
		t.Error("stopped a working guest")
	}
}

func TestGuestThreadlessSleepWakeDown(t *testing.T) {
	s, _, log := guestServer(t)
	if w := guestCall(s, "sleep", "spare", ""); w.Code != 200 {
		t.Fatalf("sleep %d %s", w.Code, w.Body)
	}
	if w := guestCall(s, "wake", "napping", ""); w.Code != 200 {
		t.Fatalf("wake %d %s", w.Code, w.Body)
	}
	if w := guestCall(s, "down", "spare", ""); w.Code != 200 {
		t.Fatalf("down %d %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(b)); got != "stop spare\nstart napping\ndown spare" {
		t.Errorf("rox calls:\n%s", got)
	}
	if w := guestCall(s, "sleep", "napping", ""); w.Code != 200 || countCalls(log, "stop") != 1 {
		t.Errorf("sleeping a stopped guest: %d", w.Code)
	}
	if w := guestCall(s, "sleep", "nope", ""); w.Code != 404 {
		t.Errorf("unknown guest: %d", w.Code)
	}
}

func TestGuestDownThreadBackedNeedsForceWhenWorking(t *testing.T) {
	s, th, log := guestServer(t)
	th.mu.Lock()
	th.meta.Status = "running"
	th.mu.Unlock()
	w := guestCall(s, "down", "rv-x-0123456789ab", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"busy":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if countCalls(log, "down") != 0 {
		t.Fatal("downed without force")
	}
	r := httptest.NewRequest("POST", "/api/guests/rv-x-0123456789ab/down?force=1", nil)
	r.SetPathValue("name", "rv-x-0123456789ab")
	w = httptest.NewRecorder()
	s.guestAction("down")(w, r)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitStatus(t, th, "destroyed")
	if countCalls(log, "down") != 1 {
		t.Errorf("rox down calls: %d", countCalls(log, "down"))
	}
}

func TestGuestWakeThreadBacked(t *testing.T) {
	s, th, log := guestServer(t)
	th.mu.Lock()
	th.meta.Status = "asleep"
	th.mu.Unlock()
	s.guestLister = func() []roxGuest { return []roxGuest{{Name: "rv-x-0123456789ab", Phase: "stopped"}} }
	if w := guestCall(s, "wake", "rv-x-0123456789ab", ""); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitOutbox(t, "rox start", func() bool { return countCalls(log, "start") == 1 })
}

func TestGuestForwardWithoutPack(t *testing.T) {
	s, _, _ := guestServer(t)
	h := s.forwardGuest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/guests/g/sleep?authority=elsewhere", nil))
	if w.Code != 404 {
		t.Errorf("%d", w.Code)
	}
}
