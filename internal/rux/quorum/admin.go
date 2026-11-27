package quorum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The admin API is how `rux quorum` talks to the running authority: a unix socket,
// ~/.rux/quorum.sock, mode 0600. It is not reachable from the network.

// AdminSocket is where a node's admin API listens.
func AdminSocket(dir string) string { return filepath.Join(dir, "quorum.sock") }

// ServeAdmin serves AdminHandler on the admin socket until ctx ends.
func (n *Node) ServeAdmin(ctx context.Context) error {
	path := AdminSocket(n.cfg.Dir)
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	srv := &http.Server{Handler: n.AdminHandler()}
	go func() { <-ctx.Done(); srv.Close(); os.Remove(path) }()
	go srv.Serve(l)
	return nil
}

type adminRequest struct {
	Host    string   `json:"host,omitempty"`
	Code    string   `json:"code,omitempty"`
	Member  string   `json:"member,omitempty"`
	Secrets []string `json:"secrets,omitempty"`
	Action  string   `json:"action,omitempty"`
}

// InviteInfo is the answer to invite.
type InviteInfo struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	URL     string    `json:"url"`
}

// AdminHandler is the admin API: POST /admin/{create,invite,join,leave,promote,policy,vault-host},
// GET /admin/status, GET /admin/policy.
func (n *Node) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	do := func(path string, f func(ctx context.Context, req adminRequest) (any, error)) {
		mux.HandleFunc("POST /admin/"+path, func(w http.ResponseWriter, r *http.Request) {
			var req adminRequest
			json.NewDecoder(r.Body).Decode(&req)
			out, err := f(r.Context(), req)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, &apiError{Msg: err.Error()})
				return
			}
			if out == nil {
				out = map[string]bool{"ok": true}
			}
			writeJSON(w, http.StatusOK, out)
		})
	}
	do("create", func(ctx context.Context, _ adminRequest) (any, error) { return nil, n.Create() })
	do("invite", func(ctx context.Context, _ adminRequest) (any, error) {
		code, exp, err := n.Invite()
		return InviteInfo{Code: code, Expires: exp, URL: n.cfg.AdvertiseURL}, err
	})
	do("join", func(ctx context.Context, r adminRequest) (any, error) { return nil, n.Join(ctx, r.Host, r.Code) })
	do("leave", func(ctx context.Context, _ adminRequest) (any, error) { return nil, n.Leave(ctx) })
	do("promote", func(ctx context.Context, r adminRequest) (any, error) { return nil, n.Promote(ctx, r.Member) })
	do("vault-host", func(ctx context.Context, r adminRequest) (any, error) { return nil, n.SetVaultHost(ctx, r.Member) })
	do("policy", func(ctx context.Context, r adminRequest) (any, error) {
		switch r.Action {
		case "allow":
			return nil, n.Allow(ctx, r.Member, r.Secrets...)
		case "deny":
			return nil, n.Deny(ctx, r.Member, r.Secrets...)
		}
		return nil, fmt.Errorf("unknown policy action %q", r.Action)
	})
	mux.HandleFunc("GET /admin/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, n.Status()) })
	mux.HandleFunc("GET /admin/policy", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, n.Config().Policy) })
	return mux
}

// Admin is a client of a running authority's admin socket.
type Admin struct{ c *http.Client }

// DialAdmin returns a client for the admin socket in dir, or an error if no authority is running.
func DialAdmin(dir string) (*Admin, error) {
	path := AdminSocket(dir)
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return nil, errors.New("the authority isn't running")
	}
	conn.Close()
	return &Admin{&http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}, Timeout: 60 * time.Second}}, nil
}

// Do calls an admin endpoint with a JSON body and decodes the JSON answer into out.
func (a *Admin) Do(method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		json.NewEncoder(&body).Encode(in)
	}
	req, err := http.NewRequest(method, "http://quorum"+path, &body)
	if err != nil {
		return err
	}
	resp, err := a.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e apiError
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Msg == "" {
			e.Msg = resp.Status
		}
		return errors.New(e.Msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
