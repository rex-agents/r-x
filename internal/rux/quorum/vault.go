package quorum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/rix"
	"github.com/rex-agents/r-x/internal/sys"
)

// Secret is a credential from the vault host. Value is the main secret (a key or token);
// Fields carries the rest (GitHub's username, a Codex auth file).
type Secret struct {
	Name    string            `json:"name"`
	Value   string            `json:"value"`
	Fields  map[string]string `json:"fields,omitempty"`
	Fetched time.Time         `json:"-"`
}

// VaultProvider is where the vault host reads secrets: rix. It returns ErrSecretMissing
// for a name it doesn't have.
type VaultProvider interface {
	Get(ctx context.Context, name string) (Secret, error)
}

// RixVault reads this machine's rix: the GitHub token (rix credential github), the Claude
// and Codex credentials as rix auth stores them, and secrets set with rix secret set.
//
// Names: "github"; "claude" and "codex" (their credential, with Fields naming it);
// ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN and OPENAI_API_KEY (those credentials);
// anything else is a rix secret of that name.
type RixVault struct{}

func (RixVault) Get(ctx context.Context, name string) (Secret, error) {
	cfg, err := rix.LoadConfig("")
	if err != nil {
		return Secret{}, err
	}
	missing := fmt.Errorf("%w: %s", ErrSecretMissing, name)
	file := func(agent, key string) (string, error) {
		agents, err := cfg.Agents()
		if err != nil {
			return "", err
		}
		a := agents[agent]
		if a == nil || a.Credential[key] == "" {
			return "", missing
		}
		return sys.ReadTrim(a.Credential[key])
	}
	switch name {
	case "github":
		c, err := cfg.GitHub()
		if err != nil {
			return Secret{}, fmt.Errorf("%w: %v", ErrSecretMissing, err)
		}
		return Secret{Name: name, Value: c.Password, Fields: map[string]string{"username": c.Username, "source": c.Source}}, nil
	case "claude":
		if v, err := file("claude", "token_file"); err == nil {
			return Secret{Name: name, Value: v, Fields: map[string]string{"env": "CLAUDE_CODE_OAUTH_TOKEN"}}, nil
		}
		v, err := file("claude", "api_key_file")
		return Secret{Name: name, Value: v, Fields: map[string]string{"env": "ANTHROPIC_API_KEY"}}, err
	case "codex":
		agents, err := cfg.Agents()
		if err != nil {
			return Secret{}, err
		}
		a := agents["codex"]
		if a != nil && a.Credential["auth_file"] != "" {
			data, err := os.ReadFile(a.Credential["auth_file"])
			return Secret{Name: name, Fields: map[string]string{"auth_json": string(data)}}, err
		}
		v, err := file("codex", "api_key_file")
		return Secret{Name: name, Value: v, Fields: map[string]string{"env": "OPENAI_API_KEY"}}, err
	case "CLAUDE_CODE_OAUTH_TOKEN":
		if v, err := file("claude", "token_file"); err == nil {
			return Secret{Name: name, Value: v}, nil
		}
	case "ANTHROPIC_API_KEY":
		if v, err := file("claude", "api_key_file"); err == nil {
			return Secret{Name: name, Value: v}, nil
		}
	case "OPENAI_API_KEY":
		if v, err := file("codex", "api_key_file"); err == nil {
			return Secret{Name: name, Value: v}, nil
		}
	}
	secrets, err := rix.LoadSecrets()
	if err != nil {
		return Secret{}, err
	}
	if v, ok := secrets[name]; ok {
		return Secret{Name: name, Value: v}, nil
	}
	return Secret{}, missing
}

// ---- the vault host ----

type vaultRequest struct {
	Thread string `json:"thread"`
	Name   string `json:"name"`
}

// AuditEntry is one line of ~/.rux/quorum-audit.jsonl.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Member string    `json:"member"`
	Thread string    `json:"thread,omitempty"`
	Secret string    `json:"secret"`
	Result string    `json:"result"` // ok, denied, missing or error
}

type vaultHost struct {
	n  *Node
	mu sync.Mutex
}

func newVaultHost(n *Node) *vaultHost { return &vaultHost{n: n} }

func (v *vaultHost) audit(e AuditEntry) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e.Time = time.Now().UTC()
	f, err := os.OpenFile(filepath.Join(v.n.cfg.Dir, "quorum-audit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		v.n.logf("quorum: audit: %v", err)
		return
	}
	defer f.Close()
	data, _ := json.Marshal(e)
	f.Write(append(data, '\n'))
}

// answer is the vault host's side: policy, rix, audit. It never returns a secret the
// policy doesn't allow; the vault host's own authority reads its own vault freely, but audited.
func (v *vaultHost) answer(ctx context.Context, member, thread, name string) (Secret, error) {
	cfg := v.n.fsm.get()
	entry := AuditEntry{Member: member, Thread: thread, Secret: name}
	if member != v.n.Name() && !cfg.Policy.Allows(member, name) {
		entry.Result = "denied"
		v.audit(entry)
		return Secret{}, fmt.Errorf("%w: %s may not read %s", ErrVaultDenied, member, name)
	}
	s, err := v.n.cfg.Vault.Get(ctx, name)
	switch {
	case err == nil:
		entry.Result = "ok"
	case errors.Is(err, ErrSecretMissing):
		entry.Result = "missing"
	default:
		entry.Result = "error"
	}
	v.audit(entry)
	s.Name = name
	return s, err
}

func (n *Node) handleVault(w http.ResponseWriter, r *http.Request) {
	member, ok := n.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if cfg := n.fsm.get(); cfg.VaultHost != n.Name() {
		writeJSON(w, http.StatusMisdirectedRequest, &apiError{Code: "not_vault_host", Msg: "this authority isn't the vault host, " + cfg.VaultHost + " is"})
		return
	}
	var req vaultRequest
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s, err := n.vhost.answer(r.Context(), member, req.Thread, req.Name)
	if err != nil {
		if errorCode(err) == "" {
			writeJSON(w, http.StatusInternalServerError, &apiError{Code: "error", Msg: err.Error()})
			return
		}
		writeErr(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// ---- the member's side ----

// LeaderVault fetches secrets from the vault host over the quorum link. It caches them in
// memory only, for the TTL (10 min by default).
type LeaderVault struct {
	n        *Node
	mu       sync.Mutex
	cache    map[string]Secret
	noted    map[string]time.Time // thread\x00secret -> when the host last heard of its use
	lastOK   time.Time
	lastFail time.Time
	failHost string // the vault host lastFail was about
	now      func() time.Time
}

func newLeaderVault(n *Node) *LeaderVault {
	return &LeaderVault{n: n, cache: map[string]Secret{}, noted: map[string]time.Time{}, now: time.Now}
}

// Vault is the pack's vault, seen from this authority.
func (n *Node) Vault() *LeaderVault { return n.vault }

// Get returns the named secret for thread. A vault host that can't be reached gives an error
// that satisfies errors.Is(err, ErrVaultUnreachable), so the UI can say "vault unreachable".
func (v *LeaderVault) Get(ctx context.Context, thread, name string) (Secret, error) {
	n := v.n
	if !n.InPack() {
		return Secret{}, ErrNotInPack
	}
	v.mu.Lock()
	if s, ok := v.cache[name]; ok && v.now().Sub(s.Fetched) < n.cfg.VaultTTL {
		v.mu.Unlock()
		v.noteUse(thread, name)
		return s, nil
	}
	delete(v.cache, name)
	v.mu.Unlock()

	host := n.fsm.get().VaultHost
	var s Secret
	var err error
	if host == n.Name() {
		s, err = n.vhost.answer(ctx, host, thread, name)
	} else {
		m, ok := n.memberByName(host)
		if !ok {
			return Secret{}, fmt.Errorf("%w: no vault host in the pack", ErrVaultUnreachable)
		}
		err = n.call(ctx, m.URL, http.MethodPost, "/quorum/vault", vaultRequest{Thread: thread, Name: name}, &s)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && (ae.Code == "denied" || ae.Code == "missing") {
			v.lastOK = v.now() // the host answered
			return Secret{}, err
		}
		if errors.Is(err, ErrVaultDenied) || errors.Is(err, ErrSecretMissing) {
			return Secret{}, err
		}
		v.lastFail, v.failHost = v.now(), host
		return Secret{}, fmt.Errorf("%w: %s: %v", ErrVaultUnreachable, host, err)
	}
	v.lastOK = v.now()
	s.Name, s.Fetched = name, v.now()
	v.cache[name] = s
	v.noted[thread+"\x00"+name] = v.now() // the host's answer was audited with this thread
	return s, nil
}

// noteUse tells the vault host that thread used a cached secret, so its audit attributes every
// thread that uses a key, not only the one whose request made the member fetch it. Once per
// thread and secret per TTL; the notice is sent in the background and may be lost.
func (v *LeaderVault) noteUse(thread, name string) {
	n := v.n
	if thread == "" {
		return
	}
	key := thread + "\x00" + name
	v.mu.Lock()
	if t, ok := v.noted[key]; ok && v.now().Sub(t) < n.cfg.VaultTTL {
		v.mu.Unlock()
		return
	}
	v.noted[key] = v.now()
	v.mu.Unlock()
	host := n.fsm.get().VaultHost
	if host == n.Name() {
		n.vhost.audit(AuditEntry{Member: host, Thread: thread, Secret: name, Result: "ok"})
		return
	}
	m, ok := n.memberByName(host)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
		defer cancel()
		if err := n.call(ctx, m.URL, http.MethodPost, "/quorum/vault/audit", vaultRequest{Thread: thread, Name: name}, nil); err != nil {
			n.logf("quorum: vault audit notice: %v", err)
		}
	}()
}

// handleVaultAudit records a member's use of a secret it holds in its cache.
func (n *Node) handleVaultAudit(w http.ResponseWriter, r *http.Request) {
	member, ok := n.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req vaultRequest
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n.vhost.audit(AuditEntry{Member: member, Thread: req.Thread, Secret: req.Name, Result: "ok (cached)"})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Invalidate drops cached secrets (after a policy change, say).
func (v *LeaderVault) Invalidate() {
	v.mu.Lock()
	v.cache = map[string]Secret{}
	v.noted = map[string]time.Time{}
	v.mu.Unlock()
}

// OK reports whether the vault host is reachable as far as this authority knows.
func (v *LeaderVault) OK() bool {
	n := v.n
	if !n.InPack() {
		return true
	}
	host := n.fsm.get().VaultHost
	if host == "" || host == n.Name() {
		return true
	}
	v.mu.Lock()
	failed := v.failHost == host && v.lastFail.After(v.lastOK) && v.now().Sub(v.lastFail) < time.Minute
	v.mu.Unlock()
	if failed {
		return false
	}
	for _, a := range n.pack.Snapshot().Authorities {
		if a.Name == host {
			return a.Status != "unreachable"
		}
	}
	return true
}
