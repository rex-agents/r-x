package rux

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// Archiving hides a thread (its guest is removed) or a project from the app's
// lists; it can be undone. Deleting removes an archived thread's files.

func (s *server) handleThreadArchive(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	var req struct{ Archived *bool }
	json.NewDecoder(r.Body).Decode(&req)
	archive := req.Archived == nil || *req.Archived
	if archive {
		s.archiveThread(t)
	} else {
		t.mu.Lock()
		t.meta.Archived = false
		t.saveMeta()
		t.mu.Unlock()
	}
	writeJSONResp(w, 200, t.view())
}

// archiveThread hides a thread, removing its guest if it still has one.
func (s *server) archiveThread(t *thread) {
	t.mu.Lock()
	live := t.meta.Status != "destroyed" && t.meta.Status != "destroying" && (t.meta.Guest != "" || t.meta.Backend == "local")
	t.meta.Archived = true
	t.meta.Updated = time.Now()
	t.saveMeta()
	t.mu.Unlock()
	if live {
		s.destroyThread(t)
	}
}

func (s *server) handleThreadDelete(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	if t == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	t.mu.Lock()
	m := t.meta
	t.mu.Unlock()
	if !m.Archived {
		httpErr(w, 409, "archive the thread first")
		return
	}
	if m.Status == "destroying" || (m.Status != "destroyed" && m.Guest != "" && m.Status != "failed") {
		httpErr(w, 409, "its guest is still being removed; try again in a moment")
		return
	}
	s.mu.Lock()
	delete(s.threads, m.ID)
	s.mu.Unlock()
	t.mu.Lock()
	if t.logf != nil {
		t.logf.Close()
	}
	t.mu.Unlock()
	if err := os.RemoveAll(t.dir); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSONResp(w, 200, map[string]bool{"ok": true})
}

// handleProjectCleanup archives a project's threads that are done with:
// removed, failed, or (with ?idle=1) idle workers, which also frees their guests.
func (s *server) handleProjectCleanup(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	idle := r.URL.Query().Get("idle") == "1"
	s.mu.Lock()
	var ts []*thread
	for _, t := range s.threads {
		if t.meta.Project == pid && t.meta.Kind == "" {
			ts = append(ts, t)
		}
	}
	s.mu.Unlock()
	n := 0
	for _, t := range ts {
		v := t.view()
		if v.Archived {
			continue
		}
		if v.Status == "destroyed" || v.Status == "failed" || (idle && v.Status == "idle") || (v.Status == "idle" && !v.Connected && time.Since(v.Updated) > 24*time.Hour) {
			s.archiveThread(t)
			n++
		}
	}
	writeJSONResp(w, 200, map[string]int{"archived": n})
}

func (s *server) handleProjectArchive(w http.ResponseWriter, r *http.Request) {
	var req struct{ Archived *bool }
	json.NewDecoder(r.Body).Decode(&req)
	archive := req.Archived == nil || *req.Archived
	s.mu.Lock()
	p := s.projects[r.PathValue("id")]
	if p == nil {
		s.mu.Unlock()
		httpErr(w, 404, "no such project")
		return
	}
	if p.ID == scratchID && archive {
		s.mu.Unlock()
		httpErr(w, 400, "scratch can't be archived")
		return
	}
	p.Archived = archive
	p.Updated = time.Now()
	s.saveProjectLocked(p)
	cp := *p
	s.mu.Unlock()
	if archive { // its manager has nothing to do while archived
		if m := s.managerFor(cp.ID); m != nil {
			s.destroyThread(m)
		}
	}
	writeJSONResp(w, 200, cp)
}
