package rux

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/rix"
	"github.com/rex-agents/r-x/internal/sys"
)

// The model proxy. Guests never hold model keys: they get ANTHROPIC_BASE_URL and
// OPENAI_BASE_URL pointing at the authority's guest listener, and their thread token as the
// API key. The proxy checks the token, swaps in the real key from the thread's vault source,
// and forwards the request. Real keys live in memory only, for vaultTTL.

const (
	vaultTTL      = 10 * time.Minute
	oauthBeta     = "oauth-2025-04-20"
	anthropicHost = "https://api.anthropic.com"
	openaiHost    = "https://api.openai.com"
)

// Credential is a model credential a vault source returns.
type Credential struct {
	// Kind is "api_key" (sent as x-api-key for Anthropic, Bearer for OpenAI) or "oauth" (a
	// Claude Code subscription token, sk-ant-oat…, sent as Bearer with the oauth beta header).
	Kind  string
	Value string
}

// VaultSource gives the authority model credentials. The local one reads rix on this host; the
// quorum adds one that asks the vault host's authority ("leader"). Provider is "anthropic"
// or "openai". Implementations needn't cache: the proxy holds results for vaultTTL.
type VaultSource interface {
	Credential(ctx context.Context, thread, provider string) (Credential, error)
}

// SetVaultSource registers a vault source under a name ("local", "leader").
func (s *server) SetVaultSource(name string, v VaultSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vaults == nil {
		s.vaults = map[string]VaultSource{}
	}
	s.vaults[name] = v
	if s.proxy != nil {
		s.proxy.forget(name)
	}
}

type cachedCred struct {
	cred Credential
	at   time.Time
}

type modelProxy struct {
	s         *server
	upstreams map[string]*url.URL // provider -> base URL
	now       func() time.Time

	mu    sync.Mutex
	cache map[string]cachedCred // vault+"/"+provider
}

func newModelProxy(s *server) *modelProxy {
	a, _ := url.Parse(anthropicHost)
	o, _ := url.Parse(openaiHost)
	return &modelProxy{s: s, upstreams: map[string]*url.URL{"anthropic": a, "openai": o}, now: time.Now, cache: map[string]cachedCred{}}
}

func (p *modelProxy) forget(vault string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.cache {
		if strings.HasPrefix(k, vault+"/") {
			delete(p.cache, k)
		}
	}
}

func (p *modelProxy) credential(ctx context.Context, vault, thread, provider string) (Credential, error) {
	key := vault + "/" + provider + "/" + thread
	p.mu.Lock()
	c, ok := p.cache[key]
	p.mu.Unlock()
	if ok && p.now().Sub(c.at) < vaultTTL {
		return c.cred, nil
	}
	p.s.mu.Lock()
	src := p.s.vaults[vault]
	p.s.mu.Unlock()
	if src == nil {
		return Credential{}, fmt.Errorf("vault source %q isn't available on this authority", vault)
	}
	cred, err := src.Credential(ctx, thread, provider)
	if err != nil {
		return Credential{}, err
	}
	if cred.Value == "" {
		return Credential{}, errors.New("the vault returned an empty credential")
	}
	p.mu.Lock()
	p.cache[key] = cachedCred{cred, p.now()}
	p.mu.Unlock()
	return cred, nil
}

// threadForToken finds the live thread a token belongs to.
func (s *server) threadForToken(tok string) *thread {
	if tok == "" {
		return nil
	}
	s.mu.Lock()
	ts := make([]*thread, 0, len(s.threads))
	for _, t := range s.threads {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	var found *thread
	for _, t := range ts {
		t.mu.Lock()
		ok := subtle.ConstantTimeCompare([]byte(t.meta.Token), []byte(tok)) == 1 && t.meta.Status != "destroyed" && t.meta.Status != "destroying"
		t.mu.Unlock()
		if ok {
			found = t
		}
	}
	return found
}

// clientToken is the thread token as the SDKs send it.
func clientToken(r *http.Request) string {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
	}
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	return r.Header.Get("api-key")
}

// handleProxy serves /proxy/anthropic/... and /proxy/openai/....
func (s *server) handleProxy(w http.ResponseWriter, r *http.Request) {
	p := s.proxy
	provider := r.PathValue("provider")
	base := p.upstreams[provider]
	if base == nil {
		http.NotFound(w, r)
		return
	}
	t := s.threadForToken(clientToken(r))
	if t == nil {
		http.Error(w, "invalid thread token", http.StatusUnauthorized)
		return
	}
	t.mu.Lock()
	meta := t.meta
	t.mu.Unlock()
	cred, err := p.credential(r.Context(), s.vaultFor(&meta), meta.ID, provider)
	if err != nil {
		log.Printf("model proxy: thread %s, %s: %v", meta.ID, provider, err)
		http.Error(w, "rux has no "+provider+" credential for this thread: "+err.Error(), http.StatusBadGateway)
		return
	}
	rest := "/" + r.PathValue("path")
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(base)
			pr.Out.URL.Path = strings.TrimSuffix(base.Path, "/") + rest
			pr.Out.URL.RawPath = ""
			pr.Out.Host = base.Host
			h := pr.Out.Header
			for _, k := range []string{"Authorization", "X-Api-Key", "Api-Key", "Cookie", "Proxy-Authorization"} {
				h.Del(k)
			}
			injectCredential(h, provider, cred)
		},
		FlushInterval: -1, // stream server-sent events as they arrive
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("model proxy: thread %s, %s: %v", meta.ID, provider, err)
			http.Error(w, "upstream request failed", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// injectCredential sets the real credential on an upstream request's headers.
func injectCredential(h http.Header, provider string, c Credential) {
	switch {
	case provider == "anthropic" && c.Kind == "oauth":
		h.Set("Authorization", "Bearer "+c.Value)
		beta := h.Values("anthropic-beta")
		have := false
		for _, v := range beta {
			for _, f := range strings.Split(v, ",") {
				if strings.TrimSpace(f) == oauthBeta {
					have = true
				}
			}
		}
		if !have {
			h.Set("anthropic-beta", strings.Join(append(beta, oauthBeta), ","))
		}
	case provider == "anthropic":
		h.Set("x-api-key", c.Value)
	default:
		h.Set("Authorization", "Bearer "+c.Value)
	}
}

// ---- local vault: this host's rix ----

// localVault reads credentials from rix on this host: rix secret get NAME first, then the
// credential files `rix auth claude|codex` saved, then this process's environment.
type localVault struct {
	secrets func(names ...string) (map[string]string, error) // rix secret get
	agent   func(agent, setting string) string               // a rix agent credential file's contents
	env     func(string) string
}

func newLocalVault() *localVault {
	return &localVault{
		secrets: func(names ...string) (map[string]string, error) {
			v, _, err := rixSecrets(names)
			return v, err
		},
		agent: rixAgentCredential,
		env:   os.Getenv,
	}
}

// rixAgentCredential reads the file rix auth saved for an agent's setting, such as claude's
// token_file (a subscription token) or api_key_file.
func rixAgentCredential(agent, setting string) string {
	cfg, err := rix.LoadConfig("")
	if err != nil {
		return ""
	}
	agents, err := cfg.Agents()
	if err != nil || agents[agent] == nil {
		return ""
	}
	path := agents[agent].Credential[setting]
	if path == "" {
		return ""
	}
	v, _ := sys.ReadTrim(path)
	return v
}

func credentialFor(value string) Credential {
	if strings.HasPrefix(value, "sk-ant-oat") {
		return Credential{Kind: "oauth", Value: value}
	}
	return Credential{Kind: "api_key", Value: value}
}

func (v *localVault) Credential(ctx context.Context, thread, provider string) (Credential, error) {
	var names []string
	switch provider {
	case "anthropic":
		names = []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
	case "openai":
		names = []string{"OPENAI_API_KEY"}
	default:
		return Credential{}, fmt.Errorf("unknown provider %q", provider)
	}
	if got, err := v.secrets(names...); err == nil {
		for _, n := range names {
			if got[n] != "" {
				return credentialFor(got[n]), nil
			}
		}
	}
	if provider == "anthropic" {
		for _, setting := range []string{"api_key_file", "token_file"} {
			if val := v.agent("claude", setting); val != "" {
				return credentialFor(val), nil
			}
		}
	} else if val := v.agent("codex", "api_key_file"); val != "" {
		return credentialFor(val), nil
	}
	for _, n := range names {
		if val := v.env(n); val != "" {
			return credentialFor(val), nil
		}
	}
	return Credential{}, fmt.Errorf("none of %s is in rix. Store one with: rix secret set %s", strings.Join(names, ", "), names[0])
}

// modelEnv points a child process (claude) at the authority's model proxy: the thread token is
// its API key, and the model keys a guest may have are removed. Without a proxy it's env as is.
func (r *runner) modelEnv(env []string) []string {
	if r.modelProxy == "" {
		return env
	}
	drop := map[string]bool{"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
		"ANTHROPIC_BASE_URL": true, "OPENAI_API_KEY": true, "OPENAI_BASE_URL": true}
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			out = append(out, kv)
		}
	}
	base := strings.TrimSuffix(r.modelProxy, "/")
	return append(out,
		"ANTHROPIC_BASE_URL="+base+"/proxy/anthropic",
		"ANTHROPIC_AUTH_TOKEN="+r.token,
		"OPENAI_BASE_URL="+base+"/proxy/openai/v1",
		"OPENAI_API_KEY="+r.token)
}
