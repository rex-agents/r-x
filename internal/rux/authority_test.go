package rux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

type fakeVault struct {
	cred  Credential
	err   error
	calls atomic.Int32
}

func (f *fakeVault) Credential(ctx context.Context, provider string) (Credential, error) {
	f.calls.Add(1)
	return f.cred, f.err
}

// proxyServer is an authority with one thread (token "tok"), a vault and a fake upstream.
func proxyServer(t *testing.T, v VaultSource) (*server, *httptest.Server, *httptest.Server, chan *http.Request) {
	t.Helper()
	seen := make(chan *http.Request, 4)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: ok\n\n"))
	}))
	t.Cleanup(up.Close)
	s, _, _ := testServer(t)
	s.vaults = map[string]VaultSource{"local": v}
	s.proxy = newModelProxy(s)
	for _, p := range []string{"anthropic", "openai"} {
		s.proxy.upstreams[p], _ = url.Parse(up.URL + "/base")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/proxy/{provider}/{path...}", s.handleProxy)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv, up, seen
}

func do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		sb.WriteString(sc.Text() + "\n")
	}
	return resp, sb.String()
}

func TestProxyRejectsBadToken(t *testing.T) {
	v := &fakeVault{cred: Credential{Kind: "api_key", Value: "sk-ant-api03-REAL"}}
	_, srv, _, seen := proxyServer(t, v)
	for _, hdr := range []map[string]string{nil, {"x-api-key": "nope"}, {"Authorization": "Bearer nope"}} {
		req, _ := http.NewRequest("POST", srv.URL+"/proxy/anthropic/v1/messages", strings.NewReader("{}"))
		for k, val := range hdr {
			req.Header.Set(k, val)
		}
		resp, _ := do(t, req)
		if resp.StatusCode != 401 {
			t.Errorf("headers %v: status %d, want 401", hdr, resp.StatusCode)
		}
	}
	if len(seen) != 0 || v.calls.Load() != 0 {
		t.Error("a request with a bad token reached the vault or upstream")
	}
	req, _ := http.NewRequest("POST", srv.URL+"/proxy/other/v1/x", nil)
	req.Header.Set("x-api-key", "tok")
	if resp, _ := do(t, req); resp.StatusCode != 404 {
		t.Errorf("unknown provider: status %d, want 404", resp.StatusCode)
	}
}

func TestProxyInjectsKeys(t *testing.T) {
	cases := []struct {
		name, provider string
		cred           Credential
		check          func(t *testing.T, h http.Header)
	}{
		{"anthropic api key", "anthropic", Credential{"api_key", "sk-ant-api03-REAL"}, func(t *testing.T, h http.Header) {
			if h.Get("x-api-key") != "sk-ant-api03-REAL" || h.Get("Authorization") != "" {
				t.Errorf("headers %v", h)
			}
		}},
		{"anthropic subscription token", "anthropic", Credential{"oauth", "sk-ant-oat01-REAL"}, func(t *testing.T, h http.Header) {
			if h.Get("Authorization") != "Bearer sk-ant-oat01-REAL" || h.Get("x-api-key") != "" {
				t.Errorf("headers %v", h)
			}
			if b := h.Get("anthropic-beta"); !strings.Contains(b, "oauth-2025-04-20") || !strings.Contains(b, "prompt-caching-2024-07-31") {
				t.Errorf("anthropic-beta = %q, want the client's beta plus oauth", b)
			}
		}},
		{"openai", "openai", Credential{"api_key", "sk-REAL"}, func(t *testing.T, h http.Header) {
			if h.Get("Authorization") != "Bearer sk-REAL" || h.Get("x-api-key") != "" {
				t.Errorf("headers %v", h)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := &fakeVault{cred: c.cred}
			_, srv, _, seen := proxyServer(t, v)
			req, _ := http.NewRequest("POST", srv.URL+"/proxy/"+c.provider+"/v1/messages?beta=true", strings.NewReader(`{"a":1}`))
			req.Header.Set("x-api-key", "tok") // the SDK sends the thread token as its key
			req.Header.Set("Cookie", "session=1")
			req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
			resp, body := do(t, req)
			if resp.StatusCode != 200 || !strings.Contains(body, "data: ok") {
				t.Fatalf("status %d body %q", resp.StatusCode, body)
			}
			up := <-seen
			if up.URL.Path != "/base/v1/messages" || up.URL.RawQuery != "beta=true" {
				t.Errorf("upstream got %s?%s", up.URL.Path, up.URL.RawQuery)
			}
			c.check(t, up.Header)
			if up.Header.Get("Cookie") != "" {
				t.Error("cookie forwarded upstream")
			}
			for k, vals := range up.Header {
				for _, val := range vals {
					if val == "tok" || strings.Contains(val, "Bearer tok") {
						t.Errorf("thread token reached upstream in %s", k)
					}
				}
			}
			// The client never sees the real key.
			for k, vals := range resp.Header {
				for _, val := range vals {
					if strings.Contains(val, c.cred.Value) {
						t.Errorf("key leaked to the client in %s", k)
					}
				}
			}
			if strings.Contains(body, c.cred.Value) {
				t.Error("key leaked to the client in the body")
			}
		})
	}
}

func TestProxyCachesAndFailsClean(t *testing.T) {
	v := &fakeVault{cred: Credential{"api_key", "sk-REAL"}}
	s, srv, _, seen := proxyServer(t, v)
	now := time.Now()
	s.proxy.now = func() time.Time { return now }
	get := func() *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/proxy/anthropic/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer tok")
		resp, _ := do(t, req)
		return resp
	}
	get()
	get()
	<-seen
	<-seen
	if v.calls.Load() != 1 {
		t.Errorf("vault asked %d times within the TTL, want 1", v.calls.Load())
	}
	now = now.Add(vaultTTL + time.Second)
	get()
	<-seen
	if v.calls.Load() != 2 {
		t.Errorf("vault asked %d times after the TTL, want 2", v.calls.Load())
	}
	// A vault error surfaces as a 502 and never as a credential.
	v.err = errors.New("rix is down")
	s.proxy.forget("local")
	if resp := get(); resp.StatusCode != 502 {
		t.Errorf("status %d, want 502", resp.StatusCode)
	}
	// A destroyed thread's token stops working.
	s.threads["t1"].setStatus("destroyed")
	if resp := get(); resp.StatusCode != 401 {
		t.Errorf("destroyed thread: status %d, want 401", resp.StatusCode)
	}
}

func TestLocalVaultOrder(t *testing.T) {
	v := &localVault{
		secrets: func(names ...string) (map[string]string, error) { return map[string]string{}, nil },
		agent:   func(a, s string) string { return "" },
		env:     func(string) string { return "" },
	}
	if _, err := v.Credential(context.Background(), "anthropic"); err == nil || !strings.Contains(err.Error(), "rix secret set") {
		t.Errorf("missing key error = %v", err)
	}
	v.agent = func(a, s string) string {
		if a == "claude" && s == "token_file" {
			return "sk-ant-oat01-X"
		}
		return ""
	}
	if c, err := v.Credential(context.Background(), "anthropic"); err != nil || c.Kind != "oauth" {
		t.Errorf("rix auth claude token: %v %v", c, err)
	}
	v.secrets = func(names ...string) (map[string]string, error) {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api03-Y"}, nil
	}
	if c, _ := v.Credential(context.Background(), "anthropic"); c.Kind != "api_key" || c.Value != "sk-ant-api03-Y" {
		t.Errorf("a rix secret should win over the agent file: %v", c)
	}
	v.env = func(n string) string { return map[string]string{"OPENAI_API_KEY": "sk-O"}[n] }
	if c, _ := v.Credential(context.Background(), "openai"); c.Value != "sk-O" {
		t.Errorf("env fallback: %v", c)
	}
}

func TestRunnerModelEnv(t *testing.T) {
	r := &runner{token: "tok", modelProxy: "http://h:7701/"}
	env := r.modelEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=real", "CLAUDE_CODE_OAUTH_TOKEN=real2"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "real") {
		t.Errorf("real keys survive: %v", env)
	}
	for _, want := range []string{"ANTHROPIC_BASE_URL=http://h:7701/proxy/anthropic", "ANTHROPIC_AUTH_TOKEN=tok", "OPENAI_BASE_URL=http://h:7701/proxy/openai/v1", "OPENAI_API_KEY=tok", "PATH=/bin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}
}

func TestResolvePlacement(t *testing.T) {
	s := &server{name: "mac"}
	cases := []struct {
		p       authapi.Placement
		vault   string
		local   bool
		wantErr bool
	}{
		{authapi.Placement{}, "", false, false},
		{authapi.Placement{Local: true}, "local", true, false},
		{authapi.Placement{Authority: "mac"}, "leader", false, false},
		{authapi.Placement{Authority: "other"}, "", false, true},
		{authapi.Placement{}, "bogus", false, true},
	}
	for _, c := range cases {
		local, _, err := s.resolvePlacement(c.p, c.vault)
		if (err != nil) != c.wantErr || local != c.local {
			t.Errorf("%+v vault %q: local=%v err=%v", c.p, c.vault, local, err)
		}
	}
	var req createReq
	if err := json.Unmarshal([]byte(`{"prompt":"x","placement":{"local":true,"authority":"mac"},"vault":"leader"}`), &req); err != nil ||
		!req.Placement.Local || req.Placement.Authority != "mac" || req.Vault != "leader" {
		t.Errorf("createReq JSON: %+v %v", req, err)
	}
}

func TestPackShape(t *testing.T) {
	s, th, _ := testServer(t)
	s.name = "mac"
	s.selfURL = "http://127.0.0.1:7700"
	s.attach(th)
	child, err := openThread(filepath.Join(s.dataDir, "threads", "t2"), &threadMeta{ID: "t2", Parent: "t1", Title: "worker", Status: "running", Token: "x", Backend: "local"})
	if err != nil {
		t.Fatal(err)
	}
	s.threads["t2"] = child
	th.mu.Lock()
	th.appendLocked("runner", 1, json.RawMessage(`{"type":"result","total_cost_usd":0.5}`))
	th.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/pack", s.handlePack)
	mux.HandleFunc("GET /api/pack/stream", s.handlePackStream)
	mux.HandleFunc("GET /api/threads", s.handleList)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var p authapi.Pack
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Leader != "mac" || p.Term != 0 || p.VaultHost != "mac" || !p.VaultOK || len(p.Authorities) != 1 {
		t.Fatalf("pack = %+v", p)
	}
	a := p.Authorities[0]
	if !a.Local || a.Status != "up" || a.Name != "mac" || a.Host.OS == "" || a.Host.CPUs < 1 || a.Host.GuestsMax < 1 || a.Host.GuestsUsed != 1 {
		t.Errorf("authority = %+v", a)
	}
	var parent authapi.ThreadSummary
	for _, ts := range a.Threads {
		if ts.ID == "t1" {
			parent = ts
		}
	}
	if parent.Authority != "mac" || parent.Guest == "" || parent.Cost != 0.5 || len(parent.SubAgents) != 1 || parent.SubAgents[0].ID != "t2" || parent.Updated == "" {
		t.Errorf("thread = %+v", parent)
	}
	for _, ts := range a.Threads {
		if ts.ID == "t2" && !ts.Placement.Local {
			t.Errorf("a local-backend thread should say placement.local: %+v", ts)
		}
	}
	// Wire names are the contract.
	raw, _ := json.Marshal(p)
	for _, k := range []string{`"leader"`, `"term"`, `"vault_host"`, `"vault_ok"`, `"authorities"`, `"guests_max"`, `"guests_used"`, `"subagents"`, `"placement"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("pack JSON lacks %s: %s", k, raw)
		}
	}

	// /api/threads gains the pack fields beside the web app's.
	resp2, err := http.Get(srv.URL + "/api/threads")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var list []map[string]any
	json.NewDecoder(resp2.Body).Decode(&list)
	if len(list) != 2 || list[0]["authority"] != "mac" || list[0]["title"] == nil || list[0]["subagents"] == nil {
		t.Errorf("threads = %v", list)
	}

	// The stream opens with a pack snapshot, then a thread.status on a change.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/pack/stream", nil)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	sc := bufio.NewScanner(sresp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	next := func() (string, authapi.PackEvent) {
		var name string
		for sc.Scan() {
			l := sc.Text()
			if strings.HasPrefix(l, "event: ") {
				name = strings.TrimPrefix(l, "event: ")
			}
			if strings.HasPrefix(l, "data: ") {
				var ev struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}
				json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &ev)
				return name, authapi.PackEvent{Type: ev.Type, Data: ev.Data}
			}
		}
		t.Fatal("stream ended")
		return "", authapi.PackEvent{}
	}
	if name, ev := next(); name != "pack" || ev.Type != authapi.EventPack {
		t.Fatalf("first event %q %q", name, ev.Type)
	}
	th.setStatus("running")
	for {
		name, ev := next()
		if name == "thread.status" {
			var sum authapi.ThreadSummary
			json.Unmarshal(ev.Data.(json.RawMessage), &sum)
			if sum.ID != "t1" || sum.Status != "running" {
				t.Errorf("status event = %+v", sum)
			}
			break
		}
	}
}
