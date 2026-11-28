package rux

import (
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Guest-level actions, for the monitor's GUESTS column. A guest that a thread uses goes through
// the thread's own sleep, wake and destroy paths, so the thread's status stays right; a guest no
// thread uses (a spare) is stopped, started or removed with rox directly.
//
//	POST /api/guests/{name}/sleep   rox stop: keep the disk, free the CPU and memory
//	POST /api/guests/{name}/wake    rox start
//	POST /api/guests/{name}/down    rox down: remove the guest (?force=1 when a turn is running)
//
// ?authority=NAME names the host the guest is on; a request for another member of the pack is
// forwarded to it over the quorum link, like a thread's requests.

func guestStopped(phase string) bool {
	return phase == "stopped" || phase == "exited" || phase == "asleep"
}

// threadOfGuest is the thread using a rox guest, if any.
func (s *server) threadOfGuest(name string) *thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.threads {
		t.mu.Lock()
		ok := t.meta.Guest == name && t.meta.Status != "destroyed"
		t.mu.Unlock()
		if ok {
			return t
		}
	}
	return nil
}

// guestAction is the handler for the three guest actions.
func (s *server) guestAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var phase string
		found := false
		for _, g := range s.localGuests() {
			if g.Name == name {
				phase, found = g.Phase, true
			}
		}
		th := s.threadOfGuest(name)
		if !found && th == nil {
			httpErr(w, 404, "no guest "+name+" on this host")
			return
		}
		defer func() { guestCache.Lock(); guestCache.at = time.Time{}; guestCache.Unlock() }()
		switch action {
		case "sleep":
			s.guestSleep(w, name, phase, th)
		case "wake":
			s.guestWake(w, name, phase, th)
		default:
			s.guestDown(w, r, name, th)
		}
	}
}

func (s *server) guestSleep(w http.ResponseWriter, name, phase string, th *thread) {
	if guestStopped(phase) {
		writeJSONResp(w, 200, map[string]any{"ok": true, "note": "already stopped"})
		return
	}
	if th != nil {
		m := th.view()
		switch {
		case asleepStatus(m.Status):
			writeJSONResp(w, 200, map[string]any{"ok": true, "note": "already asleep"})
		case m.SharedGuest:
			httpErr(w, 409, "this guest runs a thread's shared runner that rux didn't start, so rux leaves it running")
		case m.Status != "idle":
			httpErr(w, 409, "thread "+m.ID+" is "+m.Status+": a guest can sleep once its thread is waiting for you")
		case s.tools.activeFor(m.ID):
			httpErr(w, 409, "an action is running for thread "+m.ID)
		default:
			go s.sleepThread(th)
			writeJSONResp(w, 202, map[string]any{"ok": true, "thread": m.ID})
		}
		return
	}
	if out, err := exec.Command(s.roxBin, "stop", name).CombinedOutput(); err != nil {
		httpErr(w, 502, "rox stop "+name+": "+outputLine(string(out), err))
		return
	}
	writeJSONResp(w, 200, map[string]bool{"ok": true})
}

func (s *server) guestWake(w http.ResponseWriter, name, phase string, th *thread) {
	if th != nil {
		if !s.wakeIfAsleep(th) {
			if !guestStopped(phase) {
				writeJSONResp(w, 200, map[string]any{"ok": true, "note": "already running"})
				return
			}
			httpErr(w, 409, "thread "+th.view().ID+" isn't asleep ("+th.view().Status+")")
			return
		}
		writeJSONResp(w, 202, map[string]any{"ok": true, "thread": th.view().ID})
		return
	}
	if !guestStopped(phase) {
		writeJSONResp(w, 200, map[string]any{"ok": true, "note": "already running"})
		return
	}
	if out, err := exec.Command(s.roxBin, "start", name).CombinedOutput(); err != nil {
		httpErr(w, 502, "rox start "+name+": "+outputLine(string(out), err))
		return
	}
	writeJSONResp(w, 200, map[string]bool{"ok": true})
}

func (s *server) guestDown(w http.ResponseWriter, r *http.Request, name string, th *thread) {
	force := r.URL.Query().Get("force") != ""
	if th != nil {
		m := th.view()
		if !force && (m.Status == "running" || s.tools.activeFor(m.ID)) {
			writeJSONResp(w, 409, map[string]any{"error": "thread " + m.ID + " is working in this guest; downing it ends the turn", "busy": true, "thread": m.ID})
			return
		}
		s.destroyThread(th)
		writeJSONResp(w, 202, map[string]any{"ok": true, "thread": m.ID})
		return
	}
	out, err := exec.Command(s.roxBin, "down", name).CombinedOutput()
	if err != nil && !guestGone(string(out)) {
		httpErr(w, 502, "rox down "+name+": "+outputLine(string(out), err))
		return
	}
	writeJSONResp(w, 200, map[string]bool{"ok": true})
}

// forwardGuest sends a guest request for another authority's host to it.
func (s *server) forwardGuest(app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/guests/") {
			if auth := r.URL.Query().Get("authority"); auth != "" && auth != s.authName() {
				if s.node == nil || !s.node.InPack() {
					httpErr(w, 404, "no authority "+auth+" in this pack")
					return
				}
				s.proxyTo(auth, w, r)
				return
			}
		}
		app.ServeHTTP(w, r)
	})
}
