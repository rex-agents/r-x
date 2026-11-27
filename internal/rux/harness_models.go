package rux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rex-agents/r-x/internal/harness"
)

// rux's harness talks to models through providers. Each is the Anthropic API or an
// OpenAI-compatible chat completions API, and its key is a rix secret, so credentials stay
// with rix on this machine and never enter a guest. A model is named provider/model, such
// as anthropic/claude-opus-5-5, openai/gpt-5 or ollama/qwen3-coder.

type providerConf struct {
	// Kind is anthropic or openai (any OpenAI-compatible API).
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url,omitempty"`
	// Key is the rix secret (or, failing that, environment variable) with the API key.
	Key     string            `json:"key,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

var builtinProviders = map[string]providerConf{
	"anthropic":  {Kind: "anthropic", Key: "ANTHROPIC_API_KEY"},
	"openai":     {Kind: "openai", BaseURL: "https://api.openai.com/v1", Key: "OPENAI_API_KEY"},
	"openrouter": {Kind: "openai", BaseURL: "https://openrouter.ai/api/v1", Key: "OPENROUTER_API_KEY"},
	"groq":       {Kind: "openai", BaseURL: "https://api.groq.com/openai/v1", Key: "GROQ_API_KEY"},
	"deepseek":   {Kind: "openai", BaseURL: "https://api.deepseek.com/v1", Key: "DEEPSEEK_API_KEY"},
	"xai":        {Kind: "openai", BaseURL: "https://api.x.ai/v1", Key: "XAI_API_KEY"},
	"together":   {Kind: "openai", BaseURL: "https://api.together.xyz/v1", Key: "TOGETHER_API_KEY"},
	"gemini":     {Kind: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Key: "GEMINI_API_KEY"},
	"mistral":    {Kind: "openai", BaseURL: "https://api.mistral.ai/v1", Key: "MISTRAL_API_KEY"},
	"ollama":     {Kind: "openai", BaseURL: "http://localhost:11434/v1"},
	"lmstudio":   {Kind: "openai", BaseURL: "http://localhost:1234/v1"},
}

// harnessConfig is ~/.rux/harness.json.
type harnessConfig struct {
	LeadModel  string                  `json:"lead_model,omitempty"`
	AgentModel string                  `json:"agent_model,omitempty"`
	Effort     string                  `json:"effort,omitempty"`
	Providers  map[string]providerConf `json:"providers,omitempty"`
}

const defaultModel = "anthropic/claude-opus-5-5"

// claudeCode is the engine that runs Claude Code itself, signed in the way Claude Code is
// (a Claude subscription works). claude-code/MODEL picks its model, such as claude-code/opus.
const claudeCode = "claude-code"

func claudeCodeModel(spec string) (string, bool) {
	spec = strings.TrimSpace(spec)
	if spec == claudeCode {
		return "", true
	}
	if m, ok := strings.CutPrefix(spec, claudeCode+"/"); ok {
		return m, true
	}
	return "", false
}

// codexEngine runs OpenAI's Codex CLI itself, signed in the way Codex is (ChatGPT works).
// codex/MODEL picks its model.
const codexEngine = "codex"

// agentCLI reports whether spec runs an agent CLI (claude for claude-code, codex for codex)
// rather than a model API, and with which model.
func agentCLI(spec string) (cli, model string, ok bool) {
	if m, ok := claudeCodeModel(spec); ok {
		return "claude", m, true
	}
	spec = strings.TrimSpace(spec)
	if spec == codexEngine {
		return "codex", "", true
	}
	if m, ok := strings.CutPrefix(spec, codexEngine+"/"); ok {
		return "codex", m, true
	}
	return "", "", false
}

// defaultLead is the lead's model: the configured one, else Claude through the API when
// there's a key, else Claude Code when it's installed.
func (c harnessConfig) defaultLead() string {
	if c.LeadModel != "" {
		return c.LeadModel
	}
	if key, _ := apiKey("ANTHROPIC_API_KEY"); key != "" {
		return defaultModel
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return claudeCode
	}
	if _, err := exec.LookPath("codex"); err == nil {
		return codexEngine
	}
	return defaultModel
}

func harnessConfigPath() string {
	if p := os.Getenv("RUX_HARNESS_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(ruxHome(), "harness.json")
}

func ruxHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rux")
}

func loadHarnessConfig() harnessConfig {
	var c harnessConfig
	_ = loadJSON(harnessConfigPath(), &c)
	return c
}

func (c harnessConfig) provider(name string) (providerConf, bool) {
	if p, ok := c.Providers[name]; ok {
		if p.Kind == "" {
			p.Kind = "openai"
		}
		return p, true
	}
	p, ok := builtinProviders[name]
	return p, ok
}

func (c harnessConfig) providerNames() []string {
	seen := map[string]bool{}
	for n := range builtinProviders {
		seen[n] = true
	}
	for n := range c.Providers {
		seen[n] = true
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// splitModel turns "provider/model" into its parts. A bare claude-* model is Anthropic's.
func splitModel(spec string) (string, string, error) {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexByte(spec, '/'); i > 0 {
		return spec[:i], spec[i+1:], nil
	}
	if strings.HasPrefix(spec, "claude-") {
		return "anthropic", spec, nil
	}
	return "", "", fmt.Errorf("name the model as provider/model, such as anthropic/claude-opus-5-5 or openai/gpt-5 (got %q)", spec)
}

// apiKey finds a provider's key in rix's secrets, then the environment.
func apiKey(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if vals, _, err := rixSecrets([]string{name}); err == nil && vals[name] != "" {
		return vals[name], nil
	}
	return os.Getenv(name), nil
}

// newProvider resolves a model to a provider with its key.
func (c harnessConfig) newProvider(spec string) (harness.Provider, error) {
	return c.newProviderEffort(spec, c.Effort)
}

// newProviderEffort is newProvider with an effort for this agent.
func (c harnessConfig) newProviderEffort(spec, effort string) (harness.Provider, error) {
	name, model, err := splitModel(spec)
	if err != nil {
		return nil, err
	}
	pc, ok := c.provider(name)
	if !ok {
		return nil, fmt.Errorf("no provider named %s. Known: %s; add others to %s", name, strings.Join(c.providerNames(), ", "), harnessConfigPath())
	}
	key, err := apiKey(pc.Key)
	if err != nil {
		return nil, err
	}
	switch pc.Kind {
	case "anthropic":
		// Without a key, the SDK falls back to ANTHROPIC_AUTH_TOKEN or an ant auth login profile.
		return &harness.Anthropic{Model: model, APIKey: key, BaseURL: pc.BaseURL, Effort: effort}, nil
	case "openai":
		if pc.Key != "" && key == "" {
			return nil, fmt.Errorf("%s needs an API key. Store it with rix: rix secret set %s", name, pc.Key)
		}
		return &harness.OpenAI{Label: name, Model: model, BaseURL: pc.BaseURL, APIKey: key, Headers: pc.Headers, Effort: effort}, nil
	}
	return nil, fmt.Errorf("provider %s has kind %q; use anthropic or openai", name, pc.Kind)
}
