package quorum

import (
	"net/http"

	"github.com/hashicorp/raft"
)

// Handler serves /quorum/* for other authorities. Mount it on the quorum listener (default
// port 7710). Everything but /quorum/join needs a member's bearer credential.
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	member := func(h func(w http.ResponseWriter, r *http.Request, from string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			from, ok := n.authenticate(r)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h(w, r, from)
		}
	}
	leader := func(h func(w http.ResponseWriter, r *http.Request, from string)) http.HandlerFunc {
		return member(func(w http.ResponseWriter, r *http.Request, from string) {
			if !n.IsLeader() {
				_, url := n.Leader()
				writeErr(w, ErrNotLeader, url)
				return
			}
			h(w, r, from)
		})
	}
	mux.HandleFunc("POST /quorum/join", n.handleJoin)
	mux.HandleFunc("GET /quorum/raft", member(func(w http.ResponseWriter, r *http.Request, _ string) {
		n.mu.RLock()
		rs := n.rs
		n.mu.RUnlock()
		if rs == nil {
			http.Error(w, "raft isn't running", http.StatusServiceUnavailable)
			return
		}
		rs.stream.serve(w, r)
	}))
	mux.HandleFunc("POST /quorum/report", leader(func(w http.ResponseWriter, r *http.Request, from string) {
		var rep Report
		if err := readJSON(r, &rep); err != nil || rep.Authority != from {
			http.Error(w, "bad report", http.StatusBadRequest)
			return
		}
		n.pack.ingest(rep)
		writeJSON(w, http.StatusOK, n.pack.Snapshot())
	}))
	mux.HandleFunc("POST /quorum/place", leader(func(w http.ResponseWriter, r *http.Request, from string) {
		var req PlaceRequest
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.Requester = from
		p, err := n.placeAsLeader(req)
		if err != nil {
			if err == errNotReady {
				writeJSON(w, http.StatusServiceUnavailable, &apiError{Msg: err.Error()})
				return
			}
			writeErr(w, err, "")
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
	mux.HandleFunc("POST /quorum/execute", member(func(w http.ResponseWriter, r *http.Request, _ string) {
		var p Placement
		if err := readJSON(r, &p); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res, err := n.AcceptPlacement(r.Context(), p)
		if err != nil {
			if errorCode(err) == "" {
				writeJSON(w, http.StatusInternalServerError, &apiError{Code: "error", Msg: err.Error()})
				return
			}
			writeErr(w, err, "")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": res})
	}))
	mux.HandleFunc("POST /quorum/config", leader(func(w http.ResponseWriter, r *http.Request, from string) {
		var c command
		if err := readJSON(r, &c); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := n.authorizeAndApply(from, c); err != nil {
			writeJSON(w, http.StatusForbidden, &apiError{Code: errorCode(err), Msg: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /quorum/leave", leader(func(w http.ResponseWriter, r *http.Request, from string) {
		if err := n.removeMember(from); err != nil {
			writeJSON(w, http.StatusInternalServerError, &apiError{Msg: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /quorum/transfer", leader(func(w http.ResponseWriter, r *http.Request, _ string) {
		var req struct{ To string }
		if err := readJSON(r, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		m, ok := n.fsm.get().Members[req.To]
		if !ok {
			writeErr(w, ErrUnknownAuthority, "")
			return
		}
		if err := n.raftNode().LeadershipTransferToServer(raft.ServerID(req.To), raft.ServerAddress(m.URL)).Error(); err != nil {
			writeJSON(w, http.StatusInternalServerError, &apiError{Msg: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /quorum/stepdown", leader(func(w http.ResponseWriter, r *http.Request, _ string) {
		var req struct{ To string }
		if err := readJSON(r, &req); err != nil || req.To == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := n.forceLeader(req.To); err != nil {
			writeJSON(w, http.StatusInternalServerError, &apiError{Msg: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /quorum/promote", member(func(w http.ResponseWriter, r *http.Request, _ string) {
		if err := n.promoteSelf(r.Context()); err != nil {
			writeJSON(w, http.StatusInternalServerError, &apiError{Msg: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /quorum/pack", member(func(w http.ResponseWriter, r *http.Request, _ string) {
		writeJSON(w, http.StatusOK, n.pack.Snapshot())
	}))
	mux.HandleFunc("POST /quorum/vault", n.handleVault)
	mux.HandleFunc("POST /quorum/vault/audit", n.handleVaultAudit)
	mux.HandleFunc("GET /quorum/ssh-agent/{guest}", n.handleSSHAgent)
	return mux
}

func (n *Node) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := readJSON(r, &req); err != nil || req.Name == "" || req.URL == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !n.IsLeader() {
		_, url := n.Leader()
		writeErr(w, ErrNotLeader, url)
		return
	}
	if !n.consumeInvite(req.Code) {
		writeErr(w, ErrBadCode, "")
		return
	}
	if _, taken := n.fsm.get().Members[req.Name]; taken {
		writeJSON(w, http.StatusConflict, &apiError{Msg: "the pack already has an authority named " + req.Name + ". Rename this one in ~/.rux/authority.json or have it leave first"})
		return
	}
	if err := n.reconcile(); err != nil { // records this leader itself first
		writeJSON(w, http.StatusInternalServerError, &apiError{Msg: err.Error()})
		return
	}
	token := randomToken(24)
	m := Member{Name: req.Name, URL: req.URL, TokenHash: hashToken(token)}
	m.Joined = timeNow()
	if err := n.applyLocal(command{Op: cmdAddMember, Member: &m}); err != nil {
		writeJSON(w, http.StatusInternalServerError, &apiError{Msg: err.Error()})
		return
	}
	if err := n.reconcile(); err != nil {
		n.logf("quorum: reconcile after join: %v", err)
	}
	st := n.getState()
	writeJSON(w, http.StatusOK, joinResponse{Name: st.Name, Token: token, LeaderURL: st.URL, Config: n.fsm.get()})
}

func (n *Node) removeMember(name string) error {
	if err := n.applyLocal(command{Op: cmdRemoveMember, Name: name}); err != nil {
		return err
	}
	return n.reconcile()
}
