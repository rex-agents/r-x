package rux

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The web composer picks a model in three steps: provider type, model, effort. A provider type is
// how a thread runs: Claude Code or Codex (agent CLIs in the guest) or rux's own agent loop on the
// Anthropic or OpenAI API (engine "harness", through the model proxy, which serves only those two).
// Each lists models with the efforts that model takes; no efforts means the effort step is skipped.

type modelProvider struct {
	ID     string        `json:"id"`               // claude-code, codex, anthropic or openai
	Name   string        `json:"name"`             // what the picker shows
	Engine string        `json:"engine,omitempty"` // "harness" for the API providers; the engine create takes
	Models []claudeModel `json:"models,omitempty"` // absent until listed: GET /api/models?provider=ID
	Lazy   bool          `json:"lazy,omitempty"`   // the models come from the provider's API; ask for them
}

var openAIEffortRe = regexp.MustCompile(`^(gpt-5|o\d)`)

// harnessEfforts are the efforts a harness-engine model takes: Anthropic's bigger models, and OpenAI's
// reasoning models (which only know low, medium and high).
func harnessEfforts(provider, id string) []string {
	switch provider {
	case "anthropic":
		if strings.Contains(id, "haiku") {
			return nil
		}
		return allEfforts
	case "openai":
		if openAIEffortRe.MatchString(id) {
			return []string{"low", "medium", "high"}
		}
	}
	return nil
}

// harnessFallback is offered when the provider's model list can't be fetched (no key on this host).
var harnessFallback = map[string][]string{
	"anthropic": {"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"},
	"openai":    {"gpt-5"},
}

type providerModelCache struct {
	mu sync.Mutex
	m  map[string]cachedProviderModels
}

type cachedProviderModels struct {
	models []claudeModel
	err    string
	at     time.Time
}

var providerModels providerModelCache

// listProvider lists one provider's models for the picker (cached for an hour, a few minutes after a failure).
func (s *server) listProvider(ctx context.Context, id string) ([]claudeModel, string) {
	providerModels.mu.Lock()
	defer providerModels.mu.Unlock()
	if c, ok := providerModels.m[id]; ok && time.Since(c.at) < map[bool]time.Duration{true: 5 * time.Minute, false: time.Hour}[c.err != ""] {
		return c.models, c.err
	}
	var out []claudeModel
	errs := ""
	switch id {
	case "claude-code":
		ms, err := s.models.list()
		out = ms
		if err != nil {
			errs = err.Error()
		}
	case "codex":
		out = []claudeModel{codexModel}
		if ids, err := codexModels(ctx); err == nil {
			for _, spec := range ids {
				if spec != codexEngine {
					out = append(out, claudeModel{Value: spec, Name: strings.TrimPrefix(spec, codexEngine+"/"), Efforts: allEfforts})
				}
			}
		} else {
			errs = err.Error()
		}
	case "anthropic", "openai":
		ids, err := (&hrun{cfg: loadHarnessConfig()}).listModels(ctx, id)
		if err != nil || len(ids) == 0 {
			if err != nil {
				errs = err.Error()
			}
			ids = nil
			for _, m := range harnessFallback[id] {
				ids = append(ids, id+"/"+m)
			}
		}
		for _, spec := range ids {
			bare := strings.TrimPrefix(spec, id+"/")
			out = append(out, claudeModel{Value: spec, Name: bare, Efforts: harnessEfforts(id, bare)})
		}
	}
	if providerModels.m == nil {
		providerModels.m = map[string]cachedProviderModels{}
	}
	providerModels.m[id] = cachedProviderModels{out, errs, time.Now()}
	return out, errs
}

// modelProviders is the picker's first step. Claude Code's models come with it; the others are listed on request.
func (s *server) modelProviders(models []claudeModel) []modelProvider {
	return []modelProvider{
		{ID: "claude-code", Name: "Claude Code", Models: models},
		{ID: "codex", Name: "Codex", Lazy: true},
		{ID: "anthropic", Name: "Anthropic API", Engine: "harness", Lazy: true},
		{ID: "openai", Name: "OpenAI API", Engine: "harness", Lazy: true},
	}
}

// applyProvider folds a create or settings request's provider ("claude-code", "codex", "anthropic" or
// "openai") into the engine and model the rest of the code takes: codex/MODEL, or engine harness with
// provider/MODEL. A model that already names its provider is left alone.
func applyProvider(provider, engine, model string) (string, string, error) {
	switch provider = strings.TrimSpace(provider); provider {
	case "", "claude-code":
		return engine, model, nil
	case "codex":
		if model != "" && model != codexEngine && !strings.HasPrefix(model, codexEngine+"/") {
			model = codexEngine + "/" + model
		}
		return engine, cmpOr(model, codexEngine), nil
	case "anthropic", "openai":
		if !strings.Contains(model, "/") && model != "" {
			model = provider + "/" + model
		}
		return "harness", model, nil
	}
	return "", "", errString(`provider must be "claude-code", "codex", "anthropic" or "openai"`)
}

type errString string

func (e errString) Error() string { return string(e) }
