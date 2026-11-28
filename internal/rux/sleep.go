package rux

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Threads spin down when they're inactive and up when they're needed. A thread whose
// agent has been idle for --sleep-after, with no action running for it, has its guest
// stopped with rox stop: the guest keeps its disk (checkout, claude session, event log)
// and uses no CPU or memory. The next message, interrupt or manager wake-up starts it
// with rox start and relaunches the runner, which resends any events the server is
// missing and resumes claude with --resume.
//
// Statuses: idle -> sleeping -> asleep -> waking -> idle.

func asleepStatus(st string) bool { return st == "sleeping" || st == "asleep" || st == "waking" }

// sleepLoop checks every minute for threads to put to sleep.
func (s *server) sleepLoop() {
	if s.sleepAfter <= 0 || s.backend != "rox" {
		return
	}
	for range time.Tick(time.Minute) {
		for _, t := range s.allThreads() {
			if s.sleepy(t) {
				go s.sleepThread(t)
			}
		}
	}
}

func (s *server) allThreads() []*thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		out = append(out, t)
	}
	return out
}

// sleepy reports whether t's guest should be stopped now.
func (s *server) sleepy(t *thread) bool {
	t.mu.Lock()
	m := t.meta
	pending := len(t.outbox)
	t.mu.Unlock()
	// A guest rux didn't start may be doing other work, so it's never stopped.
	if m.Backend != "rox" || m.Guest == "" || m.SharedGuest || m.Status != "idle" || pending > 0 {
		return false
	}
	if time.Since(m.Updated) < s.sleepAfter {
		return false
	}
	return !s.tools.activeFor(m.ID)
}

// activeFor reports whether an action run for thread hasn't ended.
func (h *toolHost) activeFor(thread string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runs {
		r.mu.Lock()
		ended := r.ended
		r.mu.Unlock()
		if r.Thread == thread && !ended {
			return true
		}
	}
	return false
}

func (s *server) sleepThread(t *thread) {
	t.mu.Lock()
	if t.meta.Status != "idle" {
		t.mu.Unlock()
		return
	}
	guest := t.meta.Guest
	t.setStatusLocked("sleeping")
	t.mu.Unlock()
	t.event(ruxEv("rux_sleep", "text", fmt.Sprintf("idle for %s, so stopping guest %s until the next message", s.sleepAfter.Round(time.Minute), guest)))
	out, err := exec.Command(s.roxBin, "stop", guest).CombinedOutput()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.meta.Status != "sleeping" {
		return // removed meanwhile
	}
	if err != nil {
		if guestGone(string(out)) {
			t.markLostLocked()
			return
		}
		t.appendLocked("rux", 0, ruxEv("rux_error", "text", "couldn't stop the guest: "+outputLine(string(out), err)))
		t.setStatusLocked("idle")
		return
	}
	t.appendLocked("rux", 0, ruxEv("rux_asleep", "guest", guest))
	t.setStatusLocked("asleep")
}

// wakeIfAsleep starts t's guest and runner when it's asleep, and reports whether t is
// asleep or waking, so callers queue their message for when the runner connects.
func (s *server) wakeIfAsleep(t *thread) bool {
	t.mu.Lock()
	st := t.meta.Status
	if st == "asleep" {
		t.setStatusLocked("waking")
	}
	m := t.meta
	t.mu.Unlock()
	switch st {
	case "asleep":
		go s.wakeThread(t, m)
		return true
	case "sleeping", "waking":
		if st == "sleeping" {
			// Stopping is under way: wake it once it's asleep.
			go func() {
				for i := 0; i < 120; i++ {
					time.Sleep(time.Second)
					if t.view().Status != "sleeping" {
						break
					}
				}
				s.wakeIfAsleep(t)
			}()
		}
		return true
	}
	return false
}

func (s *server) wakeThread(t *thread, m threadMeta) {
	t.event(ruxEv("rux_wake", "text", "starting guest "+m.Guest))
	fail := func(what string, out []byte, err error) {
		t.mu.Lock()
		if guestGone(string(out)) {
			// The queued message, if any, starts a new guest.
			lost := t.meta.Status == "waking"
			if lost {
				t.markLostLocked()
			}
			t.mu.Unlock()
			if lost {
				s.reprovision(t)
			}
			return
		}
		defer t.mu.Unlock()
		t.appendLocked("rux", 0, ruxEv("rux_error", "text", what+": "+outputLine(string(out), err)+". Send another message to try again."))
		if t.meta.Status == "waking" {
			t.setStatusLocked("asleep")
		}
	}
	if out, err := exec.Command(s.roxBin, "start", m.Guest).CombinedOutput(); err != nil {
		fail("couldn't start the guest", out, err)
		return
	}
	// The runner died with the guest. It takes a lock on its state, so a second copy, such
	// as one the guest's Rex restores, exits at once.
	script := `set -a; . "$HOME/.rox/agents.env" 2>/dev/null || true; set +a; ` +
		`mkdir -p "$HOME/.rux"; nohup bash -lc ` + shQuote(s.runnerCommand(&m, s.advertiseFor(m.Runtime))) +
		` >>"$HOME/.rux/runner.log" 2>&1 </dev/null &`
	if out, err := exec.Command(s.roxBin, "exec", m.Guest, "--", "sh", "-c", script).CombinedOutput(); err != nil {
		fail("couldn't restart the runner", out, err)
		return
	}
	// handleRunner moves the thread to idle when the runner connects.
	time.Sleep(3 * time.Minute)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.meta.Status == "waking" && !t.meta.Connected {
		t.appendLocked("rux", 0, ruxEv("rux_error", "text", "the runner didn't reconnect after the guest started; see ~/.rux/runner.log in the guest"))
		t.setStatusLocked("asleep")
	}
}

func outputLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(ansiRe.ReplaceAllString(out, "")), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return strings.TrimPrefix(l, "✗ rox: ")
	}
	return err.Error()
}

// handleSleep is POST /api/threads/{id}/sleep: stop an idle thread's guest now.
func (s *server) handleSleep(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	m := t.view()
	switch {
	case s.backend != "rox" || m.Guest == "":
		httpErr(w, 409, "this thread has no guest to stop")
	case m.SharedGuest:
		httpErr(w, 409, "this thread runs in a guest rux didn't start, so rux leaves it running")
	case m.Status != "idle":
		httpErr(w, 409, "only a thread that's waiting for you can sleep (it's "+m.Status+")")
	case s.tools.activeFor(m.ID):
		httpErr(w, 409, "an action is running for this thread")
	default:
		go s.sleepThread(t)
		writeJSONResp(w, 202, map[string]bool{"ok": true})
	}
}

// handleWake is POST /api/threads/{id}/wake: start an asleep thread's guest without a message.
func (s *server) handleWake(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	if !s.wakeIfAsleep(t) {
		httpErr(w, 409, "this thread isn't asleep")
		return
	}
	writeJSONResp(w, 202, map[string]bool{"ok": true})
}
