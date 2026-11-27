package rux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/quorum"
)

// The authority's side of the quorum (internal/rux/quorum): the Node, its listener, the host
// report, placement and the proxy to the authority that owns a thread.

// quorumOptions are what startQuorum needs beyond the server itself.
type quorumOptions struct {
	Listener  net.Listener         // the quorum listener (default port 7710)
	Advertise string               // where other authorities reach it
	Vault     quorum.VaultProvider // the vault host's secrets (default: this host's rix)
	Logf      func(string, ...any) // default log.Printf
	Tune      func(*quorum.Config) // tests: faster reports, rafts
}

// startQuorum creates the Node, starts it (joining or leading its pack when ~/.rux/quorum.json
// says it's in one; `rux quorum create|join` later bring it up without a restart through the
// admin socket), and serves /quorum/* on the listener.
func (s *server) startQuorum(ctx context.Context, o quorumOptions) error {
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	if o.Vault == nil {
		o.Vault = &hostVault{base: quorum.RixVault{}, local: newLocalVault()}
	}
	cfg := quorum.Config{Dir: s.dataDir, Name: s.authName(), AdvertiseURL: o.Advertise,
		LocalReport: s.quorumReport, Execute: s.executePlacement, Vault: o.Vault, Logf: o.Logf}
	if o.Tune != nil {
		o.Tune(&cfg)
	}
	node, err := quorum.New(cfg)
	if err != nil {
		return err
	}
	s.node = node
	s.SetVaultSource("leader", NewLeaderVault(node))
	if err := node.Start(ctx); err != nil {
		return err
	}
	if o.Listener != nil {
		mux := http.NewServeMux()
		mux.Handle("/quorum/", node.Handler())
		mux.HandleFunc("/quorum/proxy/", s.handleQuorumProxy)
		srv := &http.Server{Handler: mux}
		go srv.Serve(o.Listener)
		go func() { <-ctx.Done(); srv.Close() }()
	}
	return nil
}

// quorumReport is the host report this authority pushes to the leader.
func (s *server) quorumReport(ctx context.Context) quorum.Report {
	h := s.localHost()
	rep := quorum.Report{Host: quorum.HostInfo{OS: h.OS, CPUs: h.CPUs, Mem: h.Mem, Load: h.Load, GuestsMax: h.GuestsMax, GuestsUsed: h.GuestsUsed},
		Threads: []json.RawMessage{}, Subagents: []json.RawMessage{}}
	for _, t := range s.threadSummaries() {
		if b, err := json.Marshal(t); err == nil {
			rep.Threads = append(rep.Threads, b)
		}
	}
	return rep
}

// executePlacement runs a placement the leader chose for this authority: it starts the thread.
// The quorum has already checked the placement's term.
func (s *server) executePlacement(ctx context.Context, p quorum.Placement) (json.RawMessage, error) {
	var req createReq
	if err := json.Unmarshal(p.Payload, &req); err != nil {
		return nil, fmt.Errorf("bad placement payload: %v", err)
	}
	req.Kind, req.Parent = "", ""
	req.Placement.Authority = s.authName()
	t, err := s.createThread(req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s.viewOf(t))
}

// createPlaced starts a thread where the pack puts it: here, or on another authority, which
// the leader chooses unless the request pins one. It returns the thread as /api/threads does.
func (s *server) createPlaced(ctx context.Context, req createReq) (any, error) {
	inPack := s.node != nil && s.node.InPack()
	if !inPack || (req.Placement.Local && req.Placement.Authority == "") {
		t, err := s.createThread(req)
		if err != nil {
			return nil, err
		}
		return s.viewOf(t), nil
	}
	p, err := s.node.Place(ctx, quorum.PlaceRequest{Pinned: req.Placement.Authority})
	if err != nil {
		return nil, placementError(err)
	}
	if p.Authority == s.authName() {
		if err := s.node.CheckTerm(p.Term); err != nil {
			return nil, placementError(err)
		}
		req.Placement.Authority = s.authName()
		t, err := s.createThread(req)
		if err != nil {
			return nil, err
		}
		return s.viewOf(t), nil
	}
	req.Placement.Authority = p.Authority
	payload, _ := json.Marshal(req)
	res, err := s.node.Execute(ctx, p, payload)
	if err != nil {
		return nil, placementError(err)
	}
	return json.RawMessage(res), nil
}

func placementError(err error) error {
	code := 502
	switch {
	case errors.Is(err, quorum.ErrUnknownAuthority):
		code = 404
	case errors.Is(err, quorum.ErrStaleTerm):
		code = 409
	case errors.Is(err, quorum.ErrNoCapacity), errors.Is(err, quorum.ErrNoLeader):
		code = 503
	}
	return &reqError{code, err.Error()}
}

// ---- proxy to the owning authority ----

var threadPath = regexp.MustCompile(`^/api/threads/([^/]+)(/|$)`)

// federate wraps the web app: a request for a thread this authority doesn't have goes to the
// authority that does (events, events/page, messages, interrupt, sub-agent paths, archive,
// destroy: everything under /api/threads/{id}/), over the quorum link.
func (s *server) federate(app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := threadPath.FindStringSubmatch(r.URL.Path); m != nil && s.node != nil && s.node.InPack() && s.get(m[1]) == nil {
			if owner := s.ownerOf(r.Context(), m[1]); owner != "" && owner != s.authName() {
				s.proxyTo(owner, w, r)
				return
			}
		}
		app.ServeHTTP(w, r)
	})
}

// ownerOf finds the authority that has a thread, from the pack view.
func (s *server) ownerOf(ctx context.Context, id string) string {
	pack, err := s.packSource().Pack(ctx)
	if err != nil {
		return ""
	}
	for _, a := range pack.Authorities {
		for _, t := range a.Threads {
			if t.ID == id {
				return a.Name
			}
		}
	}
	return ""
}

func (s *server) proxyTo(owner string, w http.ResponseWriter, r *http.Request) {
	base, ok := s.node.MemberURL(owner)
	if !ok {
		httpErr(w, 502, "no route to authority "+owner)
		return
	}
	target, err := url.Parse(base)
	if err != nil {
		httpErr(w, 502, "bad URL for authority "+owner)
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/quorum/proxy" + r.URL.Path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = r.URL.RawQuery
			for k := range pr.Out.Header {
				if strings.HasPrefix(k, "Tailscale-") || k == "Cookie" {
					pr.Out.Header.Del(k)
				}
			}
			for k, v := range s.node.AuthHeader() {
				pr.Out.Header[k] = v
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy to authority %s: %v", owner, err)
			httpErr(w, 502, "authority "+owner+" isn't reachable")
		},
	}
	rp.ServeHTTP(w, r)
}

// handleQuorumProxy serves a member's proxied request for one of this authority's threads.
func (s *server) handleQuorumProxy(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.node.Authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/quorum/proxy")
	if !strings.HasPrefix(path, "/api/threads/") {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path, r2.URL.RawPath = path, ""
	r2.RequestURI = ""
	r2.Header.Del("Authorization")
	s.inner.ServeHTTP(w, r2)
}

// ---- the pack view in a quorum ----

// quorumPack is the quorum's pack view plus this authority's own thread events, which don't
// pass through the leader. A client opens another authority's thread through the proxy.
type quorumPack struct {
	s *server
	*quorum.PackAdapter
}

func (p *quorumPack) Subscribe(ctx context.Context) (<-chan authapi.PackEvent, error) {
	qc, err := p.PackAdapter.Subscribe(ctx)
	if err != nil {
		return nil, err
	}
	hub, cancel := p.s.hub.subscribe()
	out := make(chan authapi.PackEvent, 256)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-qc:
				if !ok {
					return
				}
				out <- e
			case e, ok := <-hub:
				if !ok {
					return
				}
				out <- e
			}
		}
	}()
	return out, nil
}

// ---- the vault host's secrets ----

// hostVault is the vault host's provider: rix as quorum.RixVault reads it, and for the model
// credentials the local vault's order (rix secret, rix auth files, this process's environment).
type hostVault struct {
	base  quorum.VaultProvider
	local *localVault
}

func (h *hostVault) Get(ctx context.Context, name string) (quorum.Secret, error) {
	s, err := h.base.Get(ctx, name)
	if err == nil || !errors.Is(err, quorum.ErrSecretMissing) {
		return s, err
	}
	provider := map[string]string{"ANTHROPIC_API_KEY": "anthropic", "CLAUDE_CODE_OAUTH_TOKEN": "anthropic", "OPENAI_API_KEY": "openai"}[name]
	if provider == "" {
		return s, err
	}
	if v := h.local.env(name); v != "" {
		return quorum.Secret{Name: name, Value: v}, nil
	}
	return s, err
}
