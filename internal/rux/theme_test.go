package rux

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

func themeServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	s, _, _ := testServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/theme", s.handleTheme)
	mux.HandleFunc("PUT /api/theme", s.handleThemeUpdate)
	mux.HandleFunc("DELETE /api/theme", s.handleThemeReset)
	mux.HandleFunc("/settings/{path...}", s.handleGuestSettings)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func TestThemeAPI(t *testing.T) {
	s, srv := themeServer(t)
	var info authapi.ThemeInfo
	if code := call(t, "GET", srv.URL+"/api/theme", nil, &info); code != 200 || !info.Default || info.Preset != "rux" || info.Resolved["accent"] != "#e378b3" {
		t.Fatalf("default theme: %d %+v", code, info.Theme)
	}
	if len(info.Presets) < 2 || len(info.Tokens) != len(authapi.ThemeTokens) {
		t.Fatalf("presets %d tokens %d", len(info.Presets), len(info.Tokens))
	}

	ch, cancel := s.hub.subscribe()
	defer cancel()
	if code := call(t, "PUT", srv.URL+"/api/theme", authapi.ThemeUpdate{Preset: "matrix"}, &info); code != 200 || info.Default || info.Preset != "matrix" || info.Resolved["accent"] != "#00ff41" {
		t.Fatalf("use matrix: %d %+v", code, info.Theme)
	}
	select {
	case ev := <-ch:
		if ev.Type != authapi.EventTheme {
			t.Fatalf("event %q", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no theme event")
	}

	// Overrides sit on the preset, normalized; "" drops one.
	call(t, "PUT", srv.URL+"/api/theme", authapi.ThemeUpdate{Colors: map[string]string{"accent": "#ABC", "ok": "112233"}}, &info)
	if info.Preset != "matrix" || info.Colors["accent"] != "#aabbcc" || info.Resolved["ok"] != "#112233" || info.Resolved["bg"] != "#050a05" {
		t.Fatalf("overrides: %+v", info.Theme)
	}
	info = authapi.ThemeInfo{}
	call(t, "PUT", srv.URL+"/api/theme", authapi.ThemeUpdate{Colors: map[string]string{"ok": ""}}, &info)
	if _, ok := info.Colors["ok"]; ok || info.Colors["accent"] != "#aabbcc" {
		t.Fatalf("drop override: %+v", info.Theme)
	}
	// It's saved.
	if got := loadTheme(s.dataDir); got.Preset != "matrix" || got.Colors["accent"] != "#aabbcc" {
		t.Fatalf("saved: %+v", got)
	}
	// A new preset drops the overrides.
	info = authapi.ThemeInfo{}
	call(t, "PUT", srv.URL+"/api/theme", authapi.ThemeUpdate{Preset: "ocean"}, &info)
	if info.Preset != "ocean" || len(info.Colors) != 0 {
		t.Fatalf("switch preset: %+v", info.Theme)
	}

	for _, bad := range []authapi.ThemeUpdate{{Preset: "nope"}, {Colors: map[string]string{"accent": "pink"}}, {Colors: map[string]string{"nope": "#fff"}}} {
		var e struct{ Error string }
		if code := call(t, "PUT", srv.URL+"/api/theme", bad, &e); code != 400 || e.Error == "" {
			t.Errorf("%+v: %d %q", bad, code, e.Error)
		}
	}

	if code := call(t, "DELETE", srv.URL+"/api/theme", nil, &info); code != 200 || !info.Default {
		t.Fatalf("reset: %d %+v", code, info.Theme)
	}
	if _, err := os.Stat(themePath(s.dataDir)); !os.IsNotExist(err) {
		t.Fatalf("the default theme leaves no file: %v", err)
	}
}

func TestGuestSettings(t *testing.T) {
	_, srv := themeServer(t)
	do := func(method, path, tok string, in any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"preset":"ember"}`))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := do("PUT", "/settings/theme?thread=t1", "wrong", nil); code != 403 {
		t.Fatalf("wrong token: %d", code)
	}
	if code := do("PUT", "/settings/theme?thread=t1", "tok", nil); code != 200 {
		t.Fatalf("thread token: %d", code)
	}
	if code := do("GET", "/settings/threads?thread=t1", "tok", nil); code != 403 {
		t.Fatalf("only settings: %d", code)
	}
	var info authapi.ThemeInfo
	call(t, "GET", srv.URL+"/api/theme", nil, &info)
	if info.Preset != "ember" {
		t.Fatalf("guest change: %+v", info.Theme)
	}
}

func TestGuestSettingsBase(t *testing.T) {
	t.Setenv("RUX_UI_URL", "http://192.168.65.1:7701/ui?thread=t1")
	t.Setenv("RUX_CRED_TOKEN", "tok")
	base, q, tok, ok := guestSettingsBase()
	if !ok || base != "http://192.168.65.1:7701/settings" || q != "?thread=t1" || tok != "tok" {
		t.Fatalf("%q %q %q %v", base, q, tok, ok)
	}
}
