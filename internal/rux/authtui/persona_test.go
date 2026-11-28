package authtui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// A fake authority that serves the pack and a persona.
type personaFake struct {
	mu      sync.Mutex
	persona authapi.Persona
	drawn   []string // prompts it was asked to draw
	kept    []authapi.Mascot
	names   []string
	failure string // the answer to a draw, when set
	srv     *httptest.Server
}

func catMascot() authapi.Mascot {
	return authapi.Mascot{
		Mask:   []string{"  ##    ##  ", " ########## ", "############", "## ###### ##", "############", " ########## ", "  ##    ##  "},
		Colors: []string{"#00ff88", "#00aaff", "#aa00ff"}, Model: "claude-haiku-5-5",
	}
}

func newPersonaFake(t *testing.T, p authapi.Persona) *personaFake {
	f := &personaFake{persona: p}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/pack", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(fixture()) })
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	mux.HandleFunc("GET /api/persona", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.persona)
	})
	mux.HandleFunc("PUT /api/persona/name", func(w http.ResponseWriter, r *http.Request) {
		var b authapi.PersonaName
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		if b.Name == "taken" {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":"this authority is in a pack"}`)
			return
		}
		f.names = append(f.names, b.Name)
		f.persona.Name, f.persona.Setup = b.Name, true
		json.NewEncoder(w).Encode(f.persona)
	})
	mux.HandleFunc("POST /api/persona/mascot", func(w http.ResponseWriter, r *http.Request) {
		var b authapi.MascotRequest
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.drawn = append(f.drawn, b.Prompt)
		if f.failure != "" {
			w.WriteHeader(502)
			fmt.Fprintf(w, `{"error":%q}`, f.failure)
			return
		}
		m := catMascot()
		m.Prompt = b.Prompt
		json.NewEncoder(w).Encode(m)
	})
	mux.HandleFunc("PUT /api/persona/mascot", func(w http.ResponseWriter, r *http.Request) {
		var m authapi.Mascot
		json.NewDecoder(r.Body).Decode(&m)
		f.mu.Lock()
		f.kept = append(f.kept, m)
		f.persona.Mascot = &m
		f.mu.Unlock()
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// mascotRenderer draws a mascot as its mask with "@" for dots, so a test can find it.
func mascotRenderer(m *authapi.Mascot, phase float64) string {
	if m == nil {
		return logo(phase)
	}
	rows := make([]string, len(m.Mask))
	for i, r := range m.Mask {
		rows[i] = strings.ReplaceAll(r, "#", "@")
	}
	return strings.Join(rows, "\n")
}

// bootPersona builds a model and loads the pack and the persona, as Init does.
func bootPersona(t *testing.T, f *personaFake, w, h int) *Model {
	t.Helper()
	m := New(NewClient(f.srv.URL), Options{Logo: logo, Mascot: mascotRenderer, Now: func() time.Time { return time.Unix(1000, 0) }})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	p, _ := m.c.Pack()
	m.Update(packMsg{p})
	m.Update(liveMsg(true))
	runCmd(m, m.fetchPersona())
	return m
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) // typing only blinks the cursor
	}
}

func TestFirstRunScreenOnlyWithoutName(t *testing.T) {
	// No name: the setup screen is the whole body.
	f := newPersonaFake(t, authapi.Persona{HostName: "My Mac.local"})
	m := bootPersona(t, f, 120, 40)
	v := plain(m.View())
	for _, want := range []string{"Welcome to rux", "Name your authority", "my-mac-local"} {
		if !strings.Contains(v, want) {
			t.Errorf("setup screen lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "LEADER") || strings.Contains(v, "PACK") && strings.Contains(v, "MONITOR") && strings.Contains(v, "fix the login") {
		t.Errorf("the pack shows through the setup screen:\n%s", v)
	}

	// A name: no setup screen.
	f2 := newPersonaFake(t, authapi.Persona{Name: "jarvis", Setup: true, HostName: "mac"})
	m2 := bootPersona(t, f2, 120, 40)
	v2 := plain(m2.View())
	if strings.Contains(v2, "Name your authority") || m2.setup != nil {
		t.Errorf("setup shown for a named authority:\n%s", v2)
	}
	if !strings.Contains(v2, "jarvis") {
		t.Errorf("the header lacks the name:\n%s", v2)
	}

	// An authority without a persona API (an older one) doesn't get the screen either.
	old := newFake(t)
	m3 := boot(t, old, 120, 40)
	runCmd(m3, m3.fetchPersona())
	if m3.setup != nil || strings.Contains(plain(m3.View()), "Name your authority") {
		t.Error("setup shown against an authority without /api/persona")
	}
}

func TestFirstRunFlowAcceptsMascot(t *testing.T) {
	f := newPersonaFake(t, authapi.Persona{HostName: "mac"})
	m := bootPersona(t, f, 120, 40)

	// Name: the default is the host name; replace it.
	clearField(m)
	typeText(m, "jarvis")
	press(m, "enter")
	if len(f.names) != 1 || f.names[0] != "jarvis" {
		t.Fatalf("names = %v", f.names)
	}
	v := plain(m.View())
	if !strings.Contains(v, "Describe your agent (blank keeps the T-rex)") || !strings.Contains(v, "jarvis") {
		t.Fatalf("describe step:\n%s", v)
	}
	// Describe, then the preview shows the drawn mascot.
	typeText(m, "a cat")
	press(m, "enter")
	if len(f.drawn) != 1 || f.drawn[0] != "a cat" {
		t.Fatalf("drawn = %v", f.drawn)
	}
	v = plain(m.View())
	if !strings.Contains(v, "@@@@@@@@@@@@") || !strings.Contains(v, "accept") || !strings.Contains(v, "regenerate") || !strings.Contains(v, "keep the T-rex") {
		t.Fatalf("preview:\n%s", v)
	}
	// Regenerate asks again.
	press(m, "r")
	if len(f.drawn) != 2 {
		t.Errorf("regenerate didn't draw again: %v", f.drawn)
	}
	press(m, "enter") // accept
	if len(f.kept) != 1 || f.kept[0].Prompt != "a cat" {
		t.Fatalf("kept = %+v", f.kept)
	}
	if m.setup != nil || m.persona.Name != "jarvis" || m.persona.Mascot == nil {
		t.Errorf("setup should be over: setup=%v persona=%+v", m.setup, m.persona)
	}
	if v = plain(m.View()); strings.Contains(v, "Name your authority") {
		t.Errorf("still on the setup screen:\n%s", v)
	}
}

func TestFirstRunBlankKeepsTrex(t *testing.T) {
	f := newPersonaFake(t, authapi.Persona{HostName: "mac"})
	m := bootPersona(t, f, 120, 40)
	press(m, "enter") // the default name
	if len(f.names) != 1 || f.names[0] != "mac" {
		t.Fatalf("names = %v", f.names)
	}
	press(m, "enter") // a blank description
	if m.setup != nil || len(f.drawn) != 0 || len(f.kept) != 0 {
		t.Errorf("blank should keep the T-rex: setup=%v drawn=%v kept=%v", m.setup, f.drawn, f.kept)
	}
}

func TestFirstRunKeepTrexFromPreview(t *testing.T) {
	f := newPersonaFake(t, authapi.Persona{HostName: "mac"})
	m := bootPersona(t, f, 120, 40)
	press(m, "enter")
	typeText(m, "a cat")
	press(m, "enter")
	press(m, "k")
	if m.setup != nil || len(f.kept) != 0 || m.persona.Mascot != nil {
		t.Errorf("keep the T-rex saved a mascot: %+v kept=%v", m.persona.Mascot, f.kept)
	}
}

func TestFirstRunErrors(t *testing.T) {
	f := newPersonaFake(t, authapi.Persona{HostName: "mac"})
	m := bootPersona(t, f, 120, 40)
	// A bad name never reaches the authority.
	clearField(m)
	typeText(m, "Bad Name")
	press(m, "enter")
	if len(f.names) != 0 || !strings.Contains(plain(m.View()), "lowercase letters") {
		t.Errorf("bad name: names=%v\n%s", f.names, plain(m.View()))
	}
	// The authority's own refusal is shown.
	clearField(m)
	typeText(m, "taken")
	press(m, "enter")
	if !strings.Contains(plain(m.View()), "this authority is in a pack") {
		t.Errorf("refusal not shown:\n%s", plain(m.View()))
	}
	clearField(m)
	typeText(m, "jarvis")
	press(m, "enter")
	// A failed drawing says so and keeps the describe step.
	f.failure = "couldn't draw the mascot: the model answered 529. The T-rex stays"
	typeText(m, "a cat")
	press(m, "enter")
	v := plain(m.View())
	if !strings.Contains(v, "The T-rex stays") || !strings.Contains(v, "Describe your agent") {
		t.Errorf("after a failed draw:\n%s", v)
	}
	// Esc keeps the T-rex.
	press(m, "esc")
	if m.setup != nil {
		t.Error("esc should finish the setup with the T-rex")
	}
}

// A snapshot of the pack view with a custom mascot where the T-rex was.
func TestPackShowsCustomMascot(t *testing.T) {
	cat := catMascot()
	f := newPersonaFake(t, authapi.Persona{Name: "jarvis", Setup: true, Mascot: &cat})
	m := bootPersona(t, f, 140, 44)
	v := plain(m.View())
	if !strings.Contains(v, "  @@    @@  ") || !strings.Contains(v, "@@ @@@@@@ @@") {
		t.Errorf("the mascot isn't drawn:\n%s", v)
	}
	if strings.Contains(v, "T-REX") {
		t.Errorf("the T-rex still shows:\n%s", v)
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.Split(v, "\n")[0]), "jarvis") {
		t.Errorf("header should lead with the name:\n%s", strings.Split(v, "\n")[0])
	}

	// With no mascot, the T-rex.
	f2 := newPersonaFake(t, authapi.Persona{Name: "jarvis", Setup: true})
	v2 := plain(bootPersona(t, f2, 140, 44).View())
	if !strings.Contains(v2, "T-REX") || strings.Contains(v2, "@@@@") {
		t.Errorf("default logo missing:\n%s", v2)
	}
}

func TestRunSetupSkipsNamedAuthority(t *testing.T) {
	f := newPersonaFake(t, authapi.Persona{Name: "jarvis", Setup: true})
	p, ok, err := RunSetup(f.srv.URL, Options{})
	if err != nil || !ok || p.Name != "jarvis" {
		t.Errorf("p=%+v ok=%v err=%v", p, ok, err)
	}
}

func TestSuggestName(t *testing.T) {
	for in, want := range map[string]string{"My Mac.local": "my-mac-local", "mac": "mac", "": "rux", "---": "rux", "Ünï": "n", strings.Repeat("a", 40): strings.Repeat("a", 32)} {
		if got := suggestName(in); got != want {
			t.Errorf("suggestName(%q) = %q, want %q", in, got, want)
		}
	}
}

// clearField empties the focused text field.
func clearField(m *Model) {
	for i := 0; i < 40; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
}
