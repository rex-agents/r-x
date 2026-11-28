package rux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// personaServer is an authority with only the persona routes, and a fake model that gives the
// next reply of replies each time it's asked.
func personaServer(t *testing.T, replies ...string) (*server, *httptest.Server, *[]string) {
	t.Helper()
	s := &server{dataDir: t.TempDir()}
	var mu sync.Mutex
	var asked []string
	s.askModel = func(ctx context.Context, system, user string) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, user)
		if len(asked) > len(replies) {
			return "", "", errors.New("no more replies")
		}
		return replies[len(asked)-1], "fake-model", nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/persona", s.handlePersona)
	mux.HandleFunc("PUT /api/persona/name", s.handlePersonaName)
	mux.HandleFunc("POST /api/persona/mascot", s.handleMascotGenerate)
	mux.HandleFunc("PUT /api/persona/mascot", s.handleMascotSave)
	mux.HandleFunc("DELETE /api/persona/mascot", s.handleMascotReset)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv, &asked
}

func call(t *testing.T, method, url string, in, out any) int {
	t.Helper()
	var body bytes.Buffer
	if in != nil {
		json.NewEncoder(&body).Encode(in)
	}
	req, _ := http.NewRequest(method, url, &body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestPersonaFirstRunThenName(t *testing.T) {
	s, srv, _ := personaServer(t)
	// A vault setting already in authority.json must survive the rename.
	os.WriteFile(authorityPath(s.dataDir, "json"), []byte(`{"vault":"leader"}`), 0o600)

	var p authapi.Persona
	if code := call(t, "GET", srv.URL+"/api/persona", nil, &p); code != 200 || p.Setup || p.Name != "" || p.Mascot != nil || p.Default == nil {
		t.Fatalf("first run: %d %+v", code, p)
	}
	for _, bad := range []string{"", "Jarvis", "my agent", "a_b", "über", strings.Repeat("a", 33), "you"} {
		var e map[string]string
		if code := call(t, "PUT", srv.URL+"/api/persona/name", authapi.PersonaName{Name: bad}, &e); code != 400 || e["error"] == "" {
			t.Errorf("name %q: %d %v", bad, code, e)
		}
	}
	if code := call(t, "PUT", srv.URL+"/api/persona/name", authapi.PersonaName{Name: "jarvis"}, &p); code != 200 || !p.Setup || p.Name != "jarvis" {
		t.Fatalf("name: %d %+v", code, p)
	}
	if s.authName() != "jarvis" {
		t.Errorf("authName = %q, the rename isn't live", s.authName())
	}
	cfg := loadAuthorityConfig(s.dataDir)
	if cfg.Name != "jarvis" || cfg.Vault != "leader" {
		t.Errorf("authority.json = %+v", cfg)
	}
	call(t, "GET", srv.URL+"/api/persona", nil, &p)
	if !p.Setup || p.Name != "jarvis" {
		t.Errorf("persona after: %+v", p)
	}
	// A name of 32 characters and one with hyphens are fine.
	for _, ok := range []string{strings.Repeat("a", 32), "dev-box-2"} {
		if code := call(t, "PUT", srv.URL+"/api/persona/name", authapi.PersonaName{Name: ok}, nil); code != 200 {
			t.Errorf("name %q: %d", ok, code)
		}
	}
}

func TestPersonaMascotGenerateKeepReset(t *testing.T) {
	good := jsonOf(goodMask(), goodColors)
	s, srv, asked := personaServer(t, good)

	var m authapi.Mascot
	if code := call(t, "POST", srv.URL+"/api/persona/mascot", authapi.MascotRequest{Prompt: "a robot butler"}, &m); code != 200 {
		t.Fatalf("generate: %d", code)
	}
	if m.Prompt != "a robot butler" || m.Model != "fake-model" || m.Created == "" || len(m.Mask) != 7 {
		t.Errorf("mascot = %+v", m)
	}
	if !strings.Contains((*asked)[0], "a robot butler") {
		t.Errorf("the model wasn't told the description: %q", (*asked)[0])
	}
	if loadMascot(s.dataDir) != nil {
		t.Error("a preview was saved")
	}
	// Accept the preview.
	if code := call(t, "PUT", srv.URL+"/api/persona/mascot", m, nil); code != 200 {
		t.Fatalf("keep: %d", code)
	}
	var p authapi.Persona
	call(t, "GET", srv.URL+"/api/persona", nil, &p)
	if p.Mascot == nil || p.Mascot.Prompt != "a robot butler" {
		t.Errorf("persona mascot = %+v", p.Mascot)
	}
	// A bad mascot is refused.
	if code := call(t, "PUT", srv.URL+"/api/persona/mascot", authapi.Mascot{Mask: []string{"###"}, Colors: goodColorsList()}, nil); code != 400 {
		t.Errorf("bad keep: %d", code)
	}
	// Reset goes back to the T-rex.
	p = authapi.Persona{}
	if code := call(t, "DELETE", srv.URL+"/api/persona/mascot", nil, &p); code != 200 || p.Mascot != nil {
		t.Errorf("reset: %d %+v", code, p)
	}
	if _, err := os.Stat(mascotPath(s.dataDir)); !os.IsNotExist(err) {
		t.Errorf("mascot.json still there: %v", err)
	}
	// Resetting twice is fine.
	if code := call(t, "DELETE", srv.URL+"/api/persona/mascot", nil, nil); code != 200 {
		t.Errorf("second reset: %d", code)
	}
}

func goodColorsList() []string { return []string{"#111111", "#222222", "#333333"} }

func TestPersonaMascotSaveFlag(t *testing.T) {
	s, srv, _ := personaServer(t, jsonOf(goodMask(), goodColors))
	if code := call(t, "POST", srv.URL+"/api/persona/mascot", authapi.MascotRequest{Prompt: "x", Save: true}, nil); code != 200 {
		t.Fatal(code)
	}
	if loadMascot(s.dataDir) == nil {
		t.Error("save: true didn't keep it")
	}
}

func TestPersonaMascotRetriesOnce(t *testing.T) {
	_, srv, asked := personaServer(t, "sorry, no", jsonOf(goodMask(), goodColors))
	var m authapi.Mascot
	if code := call(t, "POST", srv.URL+"/api/persona/mascot", authapi.MascotRequest{Prompt: "a cat"}, &m); code != 200 {
		t.Fatalf("retry: %d", code)
	}
	if len(*asked) != 2 || !strings.Contains((*asked)[1], "rejected") {
		t.Errorf("asked = %q", *asked)
	}
}

func TestPersonaMascotFailsKeepsTrex(t *testing.T) {
	s, srv, asked := personaServer(t, "nope", jsonOf(goodMask(), `["red","green","blue"]`), "third time")
	// An old mascot stays when a new one can't be drawn.
	old := authapi.Mascot{Mask: goodMask(), Colors: goodColorsList(), Prompt: "old"}
	validateMascot(&old)
	saveMascot(s.dataDir, old)
	var e map[string]string
	if code := call(t, "POST", srv.URL+"/api/persona/mascot", authapi.MascotRequest{Prompt: "a cat", Save: true}, &e); code != 502 {
		t.Fatalf("failure: %d %v", code, e)
	}
	if !strings.Contains(e["error"], "T-rex stays") || strings.Contains(e["error"], "nope") && len(e["error"]) > 300 {
		t.Errorf("error = %q", e["error"])
	}
	if len(*asked) != 2 {
		t.Errorf("asked %d times, want 2 (one retry)", len(*asked))
	}
	if got := loadMascot(s.dataDir); got == nil || got.Prompt != "old" {
		t.Errorf("mascot after failure = %+v", got)
	}
}

func TestPersonaMascotNeedsPrompt(t *testing.T) {
	_, srv, asked := personaServer(t)
	if code := call(t, "POST", srv.URL+"/api/persona/mascot", authapi.MascotRequest{Prompt: "  "}, nil); code != 400 {
		t.Errorf("blank prompt: %d", code)
	}
	if len(*asked) != 0 {
		t.Error("asked the model for nothing")
	}
}

func TestPersonaModelErrorIsNotRetried(t *testing.T) {
	_, srv, asked := personaServer(t) // no replies: the fake model errors
	if code := call(t, "POST", srv.URL+"/api/persona/mascot", authapi.MascotRequest{Prompt: "x"}, nil); code != 502 {
		t.Errorf("code %d", code)
	}
	if len(*asked) != 1 {
		t.Errorf("asked %d times", len(*asked))
	}
}

func TestAskAnthropicUsesVaultKey(t *testing.T) {
	var gotKey, gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		var body struct{ Model string }
		json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Write([]byte(`{"content":[{"type":"text","text":"hello"}]}`))
	}))
	defer up.Close()
	t.Setenv("RUX_ANTHROPIC_URL", up.URL)
	s := &server{dataDir: t.TempDir()}
	s.vaults = map[string]VaultSource{"local": &fakeVault{cred: Credential{Kind: "api_key", Value: "sk-real"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, model, err := s.askAnthropic(ctx, "sys", "user")
	if err != nil || reply != "hello" || model != mascotModel || gotKey != "sk-real" || gotModel != mascotModel {
		t.Errorf("reply=%q model=%q key=%q sent=%q err=%v", reply, model, gotKey, gotModel, err)
	}
}

// An authority in a pack can't be renamed: the other members know it by its name.
func TestPersonaRenameRefusedInPack(t *testing.T) {
	a := newTestAuthority(t, "alpha", &hostVault{base: &fakeRix{}, local: newLocalVault()})
	// Before the pack exists, a rename goes through to the quorum node too.
	if code := call(t, "PUT", a.app.URL+"/api/persona/name", authapi.PersonaName{Name: "alpha2"}, nil); code != 200 {
		t.Fatalf("rename before the pack: %d", code)
	}
	if got := a.s.node.Name(); got != "alpha2" {
		t.Fatalf("the quorum node still calls itself %q", got)
	}
	if err := a.s.node.Create(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "leader", 10*time.Second, func() bool { return a.s.node.IsLeader() })
	var e map[string]string
	if code := call(t, "PUT", a.app.URL+"/api/persona/name", authapi.PersonaName{Name: "beta"}, &e); code != 409 ||
		!strings.Contains(e["error"], "rux quorum leave") {
		t.Errorf("rename in a pack: %d %v", code, e)
	}
	if a.s.authName() != "alpha2" || a.s.node.Name() != "alpha2" {
		t.Errorf("name changed to %q / %q", a.s.authName(), a.s.node.Name())
	}
	// Saying the same name again is fine.
	if code := call(t, "PUT", a.app.URL+"/api/persona/name", authapi.PersonaName{Name: "alpha2"}, nil); code != 200 {
		t.Errorf("same name: %d", code)
	}
}
