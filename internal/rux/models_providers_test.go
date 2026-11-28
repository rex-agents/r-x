package rux

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestApplyProvider(t *testing.T) {
	for _, c := range []struct{ provider, engine, model, wantEngine, wantModel string }{
		{"", "", "opus", "", "opus"},
		{"claude-code", "", "opus", "", "opus"},
		{"codex", "", "", "", "codex"},
		{"codex", "", "gpt-5-codex", "", "codex/gpt-5-codex"},
		{"anthropic", "", "claude-opus-5-5", "harness", "anthropic/claude-opus-5-5"},
		{"openai", "", "openai/gpt-5", "harness", "openai/gpt-5"},
	} {
		e, m, err := applyProvider(c.provider, c.engine, c.model)
		if err != nil || e != c.wantEngine || m != c.wantModel {
			t.Errorf("applyProvider(%q,%q,%q) = %q,%q,%v", c.provider, c.engine, c.model, e, m, err)
		}
	}
	if _, _, err := applyProvider("ollama", "", ""); err == nil {
		t.Error("an unserved provider should be refused")
	}
}

func TestHarnessEfforts(t *testing.T) {
	if len(harnessEfforts("anthropic", "claude-haiku-5-5")) != 0 || len(harnessEfforts("anthropic", "claude-opus-5-5")) == 0 {
		t.Error("anthropic efforts: none for haiku, some for opus")
	}
	if e := harnessEfforts("openai", "gpt-5"); len(e) != 3 || len(harnessEfforts("openai", "gpt-4o")) != 0 {
		t.Errorf("openai efforts: %v", e)
	}
}

func TestHandleModelsProviders(t *testing.T) {
	s := &server{models: &modelCache{models: fallbackModels, at: time.Now()}}
	w := httptest.NewRecorder()
	s.handleModels(w, httptest.NewRequest("GET", "/api/models", nil))
	var resp struct {
		Models    []claudeModel
		Providers []modelProvider
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != len(fallbackModels)+1 {
		t.Errorf("legacy models list changed: %d", len(resp.Models))
	}
	ids := ""
	for _, p := range resp.Providers {
		ids += p.ID + " "
	}
	if ids != "claude-code codex anthropic openai " || len(resp.Providers[0].Models) != len(fallbackModels) {
		t.Errorf("providers: %s", ids)
	}
	// Haiku has no efforts, so the picker hides that step.
	for _, m := range resp.Providers[0].Models {
		if m.Value == "haiku" && len(m.Efforts) != 0 {
			t.Error("haiku should have no efforts")
		}
	}
}
