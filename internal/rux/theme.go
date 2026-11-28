package rux

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// The theme: how the web app and the terminal UIs look (wire types and presets: authapi/theme.go).
// It lives in ~/.rux/theme.json. Every change goes out on the pack feed as a "theme" event, so
// open screens repaint without a reload.
//
//	GET    /api/theme
//	PUT    /api/theme   authapi.ThemeUpdate
//	DELETE /api/theme

func themePath(dir string) string { return filepath.Join(dir, "theme.json") }

// loadTheme reads the saved theme; a missing or broken file is the default theme.
func loadTheme(dir string) authapi.Theme {
	var t authapi.Theme
	if b, err := os.ReadFile(themePath(dir)); err == nil {
		json.Unmarshal(b, &t)
	}
	if authapi.FindThemePreset(t.Preset) == nil {
		t.Preset = authapi.DefaultThemePreset
	}
	for k, v := range t.Colors {
		if c, err := authapi.NormalizeColor(v); err != nil || !authapi.IsThemeToken(k) {
			delete(t.Colors, k)
		} else {
			t.Colors[k] = c
		}
	}
	return t
}

func saveTheme(dir string, t authapi.Theme) error {
	if t.IsDefault() {
		if err := os.Remove(themePath(dir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return writeFileAtomic(themePath(dir), append(b, '\n'))
}

func (s *server) handleTheme(w http.ResponseWriter, r *http.Request) {
	writeJSONResp(w, 200, loadTheme(s.dataDir).Info())
}

func (s *server) handleThemeUpdate(w http.ResponseWriter, r *http.Request) {
	var u authapi.ThemeUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&u); err != nil {
		httpErr(w, 400, "bad request: send {\"preset\": NAME} and/or {\"colors\": {TOKEN: \"#rrggbb\"}}")
		return
	}
	s.themeMu.Lock()
	defer s.themeMu.Unlock()
	t, err := loadTheme(s.dataDir).Apply(u)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	s.setTheme(w, t)
}

func (s *server) handleThemeReset(w http.ResponseWriter, r *http.Request) {
	s.themeMu.Lock()
	defer s.themeMu.Unlock()
	s.setTheme(w, authapi.Theme{Preset: authapi.DefaultThemePreset})
}

func (s *server) setTheme(w http.ResponseWriter, t authapi.Theme) {
	if err := saveTheme(s.dataDir, t); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	info := loadTheme(s.dataDir).Info()
	s.hub.publish(authapi.PackEvent{Type: authapi.EventTheme, Data: info})
	writeJSONResp(w, 200, info)
}

// personaChanged tells open screens the name or mascot changed.
func (s *server) personaChanged() {
	s.hub.publish(authapi.PackEvent{Type: authapi.EventPersona, Data: s.persona()})
}

// guestSettings are the authority settings an agent in a thread may read and change, through
// /settings/... on the runner listener with its thread's token: the theme, the name and the
// mascot. `rux theme` and `rux authority rename|mascot` use it inside a guest.
var guestSettings = map[string]bool{
	"GET /api/theme": true, "PUT /api/theme": true, "DELETE /api/theme": true,
	"GET /api/persona": true, "PUT /api/persona/name": true,
	"POST /api/persona/mascot": true, "PUT /api/persona/mascot": true, "DELETE /api/persona/mascot": true,
}

func (s *server) handleGuestSettings(w http.ResponseWriter, r *http.Request) {
	if s.guestThread(w, r) == nil {
		return
	}
	path := "/api/" + r.PathValue("path")
	if !guestSettings[r.Method+" "+path] {
		httpErr(w, 403, fmt.Sprintf("%s %s isn't a setting agents can change", r.Method, path))
		return
	}
	switch path {
	case "/api/theme":
		switch r.Method {
		case "GET":
			s.handleTheme(w, r)
		case "PUT":
			s.handleThemeUpdate(w, r)
		default:
			s.handleThemeReset(w, r)
		}
	case "/api/persona":
		s.handlePersona(w, r)
	case "/api/persona/name":
		s.handlePersonaName(w, r)
	default: // /api/persona/mascot
		switch r.Method {
		case "POST":
			s.handleMascotGenerate(w, r)
		case "PUT":
			s.handleMascotSave(w, r)
		default:
			s.handleMascotReset(w, r)
		}
	}
}

// guestSettingsBase is where a CLI inside a thread reaches the authority's settings: the runner
// listener's /settings, with the thread's token. ok is false outside a thread.
func guestSettingsBase() (base, query, tok string, ok bool) {
	u, tok := os.Getenv("RUX_UI_URL"), os.Getenv("RUX_CRED_TOKEN")
	if u == "" || tok == "" {
		return "", "", "", false
	}
	i := strings.Index(u, "/ui")
	if i < 0 {
		return "", "", "", false
	}
	q := ""
	if j := strings.IndexByte(u, '?'); j >= 0 {
		q = u[j:]
	}
	return u[:i] + "/settings", q, tok, true
}
