package rux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

func TestLeadSystemPromptNamesTheLead(t *testing.T) {
	r := testRun(t)
	r.name = "jarvis"
	for _, cli := range []string{"", "claude", "codex"} {
		p := r.leadSystem(cli)
		if !strings.HasPrefix(p, "You are jarvis,") || !strings.Contains(p, "introduce yourself by it") {
			t.Errorf("lead prompt (%q) doesn't say who it is:\n%.200s", cli, p)
		}
	}
	// Without a name the lead is still the lead, with no stray "You are ,".
	r.name = ""
	p := r.leadSystem("")
	if strings.Contains(p, "You are ,") || !strings.Contains(p, "lead agent") {
		t.Errorf("unnamed lead prompt:\n%.200s", p)
	}
	if r.leadName() != "lead" || r.welcomeName() != "rux" {
		t.Errorf("fallback names: %q, %q", r.leadName(), r.welcomeName())
	}
}

func TestSubAgentsKnowTheLeadByName(t *testing.T) {
	r := testRun(t)
	r.name = "jarvis"
	if p := r.subSystem("tests", &subInfo{Dir: "/w", Guest: "g"}); !strings.Contains(p, "jarvis (the lead agent) gave you a task") {
		t.Errorf("sub prompt:\n%.300s", p)
	}
	// Messages the lead sends carry its name as the speaker.
	sub, _ := spawn(t, r, "tests")
	if e := sub.Snapshot().Entries; len(e) == 0 || e[0].From != "jarvis" {
		t.Errorf("first message came from %+v", e)
	}
}

func TestLeadLabelEverywhere(t *testing.T) {
	zone.NewGlobal()
	r := testRun(t)
	r.name = "jarvis"
	r.lead.Label = "jarvis"
	if r.lead.Name != "lead" || r.lead.Display() != "jarvis" {
		t.Fatalf("name %q, display %q: the key must stay %q", r.lead.Name, r.lead.Display(), "lead")
	}
	m := newHarnessModel(r)
	m.w, m.h = 140, 50
	if got := m.input.Placeholder; got != "Tell jarvis what to do" {
		t.Errorf("placeholder = %q", got)
	}
	w := ansi.Strip(m.welcome())
	for _, want := range []string{"Welcome to jarvis", "Tell jarvis what to do"} {
		if !strings.Contains(w, want) {
			t.Errorf("welcome lacks %q:\n%s", want, w)
		}
	}
	if strings.Contains(w, "Welcome to rux") || strings.Contains(w, "the lead") {
		t.Errorf("welcome still says rux/lead:\n%s", w)
	}
	// The agent strip and the control socket's view carry the name too.
	spawn(t, r, "tests")
	strip := ansi.Strip(m.agentStrip())
	if !strings.Contains(strip, "jarvis") || strings.Contains(strip, "lead") {
		t.Errorf("agent strip = %q", strip)
	}
	if in := r.info(r.lead, false); in.Name != "lead" || in.Label != "jarvis" {
		t.Errorf("info = %+v", in)
	}

	// Without a name, today's wording.
	r2 := testRun(t)
	m2 := newHarnessModel(r2)
	m2.w, m2.h = 140, 50
	w2 := ansi.Strip(m2.welcome())
	if !strings.Contains(w2, "Welcome to rux") || !strings.Contains(w2, "Tell the lead what to do") {
		t.Errorf("unnamed welcome:\n%s", w2)
	}
}

func TestHarnessUsesTheSavedMascot(t *testing.T) {
	zone.NewGlobal()
	dir := t.TempDir()
	t.Setenv("RUX_HOME", dir)
	os.WriteFile(filepath.Join(dir, "authority.json"), []byte(`{"name":"jarvis"}`), 0o600)
	m := authapi.Mascot{Mask: goodMask(), Colors: []string{"#00ff00", "#00ff00", "#00ff00"}}
	if err := validateMascot(&m); err != nil {
		t.Fatal(err)
	}
	if err := saveMascot(dir, m); err != nil {
		t.Fatal(err)
	}
	name, mascot := loadPersona(authorityHome())
	if name != "jarvis" || mascot == nil || len(mascot.Mask) != 7 {
		t.Fatalf("persona = %q %+v", name, mascot)
	}
	r := testRun(t)
	r.name, r.mascot = name, mascot
	hm := newHarnessModel(r)
	hm.w, hm.h = 140, 50
	logo := mascotLogo(r.mascot, hm.phase)
	if lipgloss.Height(logo) != 7 || logo == trexLogo(hm.phase) {
		t.Errorf("the harness would draw the T-rex (%d rows)", lipgloss.Height(logo))
	}
	if !strings.Contains(hm.welcome(), strings.Split(logo, "\n")[0]) {
		t.Error("the welcome screen doesn't draw the mascot")
	}
}

func TestFirstRunSetupNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RUX_HOME", dir)
	// No name, but nobody at a terminal (or RUX_NO_SETUP): no setup.
	if needsSetup(dir, false) {
		t.Error("setup wanted without a terminal")
	}
	if !needsSetup(dir, true) {
		t.Error("a terminal and no name should show the setup")
	}
	t.Setenv("RUX_NO_SETUP", "1")
	if needsSetup(dir, true) {
		t.Error("RUX_NO_SETUP ignored")
	}
	t.Setenv("RUX_NO_SETUP", "")
	// A name: never again.
	os.WriteFile(filepath.Join(dir, "authority.json"), []byte(`{"name":"jarvis"}`), 0o600)
	if needsSetup(dir, true) {
		t.Error("setup wanted for a named authority")
	}
	// go test has no terminal: firstRunSetup returns at once and starts no authority.
	os.Remove(filepath.Join(dir, "authority.json"))
	firstRunSetup()
	if _, err := os.Stat(filepath.Join(dir, "authority.pid")); err == nil {
		t.Error("firstRunSetup started an authority without a terminal")
	}
}

func TestThreadsKnowTheirAuthority(t *testing.T) {
	if l := authorityLine("jarvis"); !strings.Contains(l, "jarvis") {
		t.Errorf("authority line = %q", l)
	}
	if p := leadPrompt("jarvis"); !strings.HasPrefix(p, "You are jarvis,") || !strings.Contains(p, "lead agent in rux") {
		t.Errorf("lead brief: %.120s", p)
	}
	if leadPrompt("") != leadBrief {
		t.Error("an unnamed lead's brief changed")
	}
}
