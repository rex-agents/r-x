package rux

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rex-agents/r-x/internal/harness"
)

// A rux session listens on a Unix socket in ~/.rux/run (private to this user), so another
// process can show an agent and talk to it: rux view NAME, in a Rex pane beside rux.

type agentInfo struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Role   string            `json:"role"`
	Model  string            `json:"model"`
	Effort string            `json:"effort"`
	Where  string            `json:"where"`
	Guest  string            `json:"guest,omitempty"`
	Dir    string            `json:"dir,omitempty"`
	Status harness.Status    `json:"status"`
	Detail string            `json:"detail"`
	Snap   *harness.Snapshot `json:"snapshot,omitempty"`
}

func (r *hrun) info(a *harness.Agent, full bool) agentInfo {
	snap := a.Snapshot()
	in := agentInfo{ID: a.ID, Name: a.Name, Role: a.Role, Model: a.ModelName(), Effort: r.effortOf(a), Where: a.Where,
		Status: snap.Status, Detail: snap.Detail}
	r.mu.Lock()
	if s := r.subs[a.Name]; s != nil {
		in.Guest, in.Dir = s.Guest, s.Dir
	}
	r.mu.Unlock()
	if full {
		in.Snap = &snap
	}
	return in
}

func runSocketDir() string { return filepath.Join(ruxHome(), "run") }

// serveControl starts the socket and returns its path.
func (r *hrun) serveControl() (string, error) {
	dir := runSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, fmt.Sprintf("%d.sock", os.Getpid()))
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	mux := http.NewServeMux()
	agent := func(w http.ResponseWriter, req *http.Request) *harness.Agent {
		name := req.PathValue("name")
		if name == "lead" {
			return r.lead
		}
		if a := r.find(name); a != nil {
			return a
		}
		http.Error(w, "no agent named "+name, 404)
		return nil
	}
	mux.HandleFunc("GET /agents", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		agents := append([]*harness.Agent(nil), r.agents...)
		r.mu.Unlock()
		out := make([]agentInfo, 0, len(agents))
		for _, a := range agents {
			out = append(out, r.info(a, false))
		}
		writeHTTPJSON(w, out)
	})
	mux.HandleFunc("GET /agents/{name}", func(w http.ResponseWriter, req *http.Request) {
		if a := agent(w, req); a != nil {
			writeHTTPJSON(w, r.info(a, true))
		}
	})
	mux.HandleFunc("POST /agents/{name}/send", func(w http.ResponseWriter, req *http.Request) {
		if a := agent(w, req); a != nil {
			b, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
			if len(b) > 0 {
				a.Send("you", string(b))
			}
			w.WriteHeader(204)
		}
	})
	mux.HandleFunc("POST /agents/{name}/interrupt", func(w http.ResponseWriter, req *http.Request) {
		if a := agent(w, req); a != nil {
			a.Interrupt()
			w.WriteHeader(204)
		}
	})
	go func() { _ = http.Serve(ln, mux) }()
	go func() { <-r.ctx.Done(); ln.Close(); os.Remove(path) }()
	r.sock = path
	return path, nil
}

func writeHTTPJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
