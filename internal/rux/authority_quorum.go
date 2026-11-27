package rux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

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
		mux.HandleFunc("POST /quorum/wake", s.handleQuorumWake)
		mux.HandleFunc("POST /quorum/mcp", s.handleQuorumMCP)
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
		Threads: []json.RawMessage{}, Subagents: []json.RawMessage{}, Started: s.started.UTC().Format(time.RFC3339)}
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
	req.Kind = "" // a placed create keeps its parent: the leader's members are trusted
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

// Pack is the quorum's pack with sub-agents linked to their parents across authorities.
func (p *quorumPack) Pack(ctx context.Context) (authapi.Pack, error) {
	pk, err := p.PackAdapter.Pack(ctx)
	linkSubagents(&pk)
	return pk, err
}

func (p *quorumPack) Threads(ctx context.Context, authority string) ([]authapi.ThreadSummary, error) {
	pk, err := p.Pack(ctx)
	var out []authapi.ThreadSummary
	for _, a := range pk.Authorities {
		if authority == "" || a.Name == authority {
			out = append(out, a.Threads...)
		}
	}
	return out, err
}

func (p *quorumPack) Subscribe(ctx context.Context) (<-chan authapi.PackEvent, error) {
	qc, err := p.PackAdapter.Subscribe(ctx)
	if err != nil {
		return nil, err
	}
	hub, cancel := p.s.hub.subscribe()
	out := make(chan authapi.PackEvent, 256)
	send := func(e authapi.PackEvent) {
		select {
		case out <- e:
		default: // a slow client resyncs on the next snapshot
		}
	}
	go func() {
		defer close(out)
		defer cancel()
		// Other authorities' thread status changes, seen as differences between pack views.
		// Their conversation events stay on their own authority; clients open a thread to read them.
		known := map[string]string{}
		diff := func() {
			pk, err := p.Pack(ctx)
			if err != nil {
				return
			}
			first := len(known) == 0
			for _, a := range pk.Authorities {
				if a.Name == p.s.authName() {
					continue
				}
				for _, t := range a.Threads {
					if old, ok := known[t.ID]; (!ok && !first) || (ok && old != t.Status) {
						send(authapi.PackEvent{Type: authapi.EventThreadStatus, Data: t})
					}
					known[t.ID] = t.Status
				}
			}
		}
		diff()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				diff()
			case e, ok := <-qc:
				if !ok {
					return
				}
				if pk, isPack := e.Data.(authapi.Pack); isPack {
					linkSubagents(&pk)
					e.Data = pk
				}
				send(e)
			case e, ok := <-hub:
				if !ok {
					return
				}
				send(e)
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

// wakeParent sends a message to the thread that started a sub-agent: here, or on the authority
// the parent runs on, over the quorum link.
func (s *server) wakeParent(parent, authority, text string) {
	if authority == "" || authority == s.authName() || s.node == nil {
		s.wake(parent, text)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var out map[string]any
		if err := s.quorumCall(ctx, authority, "/quorum/wake", map[string]string{"thread": parent, "text": text}, &out); err != nil {
			log.Printf("waking %s on %s: %v", parent, authority, err)
		}
	}()
}

// quorumCall posts JSON to another authority's endpoint on the quorum listener.
func (s *server) quorumCall(ctx context.Context, authority, path string, in, out any) error {
	base, ok := s.node.MemberURL(authority)
	if !ok {
		return fmt.Errorf("no route to authority %s", authority)
	}
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.node.AuthHeader() {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// handleQuorumWake delivers a wake-up from a sub-agent's authority to a thread here.
func (s *server) handleQuorumWake(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.node.Authenticate(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct{ Thread, Text string }
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil || s.get(req.Thread) == nil {
		httpErr(w, 404, "no such thread")
		return
	}
	s.wake(req.Thread, req.Text)
	writeJSONResp(w, 200, map[string]bool{"ok": true})
}

// remoteTools are the MCP tools that act on one thread by id, so they can reach a sub-agent
// that runs on another authority.
var remoteTools = map[string]bool{"send_message": true, "read_thread": true, "view_file": true, "interrupt_thread": true, "remove_thread": true}

// handleQuorumMCP runs one of those tools for a parent on another authority, on this
// authority's sub-agent of that parent.
func (s *server) handleQuorumMCP(w http.ResponseWriter, r *http.Request) {
	from, ok := s.node.Authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Parent    string
		Name      string
		Arguments json.RawMessage
		ThreadID  string `json:"thread_id"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req) != nil || !remoteTools[req.Name] {
		httpErr(w, 400, "bad request")
		return
	}
	child := s.get(req.ThreadID)
	if child == nil || child.meta.Parent != req.Parent || child.meta.ParentAuthority != from {
		httpErr(w, 404, "no such sub-agent")
		return
	}
	// The parent lives elsewhere: act for it with a stand-in that only has its id.
	parent := &thread{meta: threadMeta{ID: req.Parent, Project: child.meta.Project}, authority: from}
	content, err := s.callManagerTool(parent, req.Name, req.Arguments)
	if err != nil {
		writeJSONResp(w, 200, map[string]any{"error": err.Error()})
		return
	}
	writeJSONResp(w, 200, map[string]any{"content": content})
}

// callRemoteTool runs a tool on a sub-agent's authority.
func (s *server) callRemoteTool(owner string, parent *thread, name string, raw json.RawMessage, id string) ([]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out struct {
		Content []any
		Error   string
	}
	err := s.quorumCall(ctx, owner, "/quorum/mcp", map[string]any{"parent": parent.meta.ID, "name": name, "arguments": raw, "thread_id": id}, &out)
	if err != nil {
		return nil, fmt.Errorf("authority %s: %v", owner, err)
	}
	if out.Error != "" {
		return nil, errors.New(out.Error)
	}
	return out.Content, nil
}

// ---- sub-agents across the pack ----

// linkSubagents adds each sub-agent to its parent's subagents, wherever either runs.
func linkSubagents(p *authapi.Pack) {
	type key struct{ authority, id string }
	kids := map[key][]authapi.SubAgentSummary{}
	for _, a := range p.Authorities {
		for _, t := range a.Threads {
			if t.Parent != "" {
				k := key{cmpOr(t.ParentAuthority, a.Name), t.Parent}
				kids[k] = append(kids[k], authapi.SubAgentSummary{ID: t.ID, Name: t.Title, Status: t.Status, Guest: t.Guest, Authority: a.Name})
			}
		}
	}
	for i := range p.Authorities {
		a := &p.Authorities[i]
		for j := range a.Threads {
			t := &a.Threads[j]
			have := map[string]bool{}
			for _, sa := range t.SubAgents {
				have[sa.ID] = true
			}
			for _, sa := range kids[key{a.Name, t.ID}] {
				if !have[sa.ID] {
					t.SubAgents = append(t.SubAgents, sa)
				}
			}
			if t.SubAgents == nil {
				t.SubAgents = []authapi.SubAgentSummary{}
			}
		}
	}
}

// remoteChildren are the sub-agents of this authority's threads that run on other authorities.
func (s *server) remoteChildren(ctx context.Context) map[string][]authapi.SubAgentSummary {
	if s.node == nil || !s.node.InPack() {
		return nil
	}
	p, err := s.packSource().Pack(ctx)
	if err != nil {
		return nil
	}
	out := map[string][]authapi.SubAgentSummary{}
	for _, a := range p.Authorities {
		if a.Name == s.authName() {
			continue
		}
		for _, t := range a.Threads {
			if t.Parent != "" && cmpOr(t.ParentAuthority, a.Name) == s.authName() {
				out[t.Parent] = append(out[t.Parent], authapi.SubAgentSummary{ID: t.ID, Name: t.Title, Status: t.Status, Guest: t.Guest, Authority: a.Name})
			}
		}
	}
	return out
}

// remoteChild finds a sub-agent of parent that runs on another authority.
func (s *server) remoteChild(ctx context.Context, parent *thread, id string) (owner string) {
	for _, kid := range s.remoteChildren(ctx)[parent.meta.ID] {
		if kid.ID == id {
			return kid.Authority
		}
	}
	return ""
}
