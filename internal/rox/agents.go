package rox

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/rix"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// AgentOrder lists the agent CLIs rox can install.
var AgentOrder = []string{"claude", "codex"}

// AgentNames are their product names.
var AgentNames = map[string]string{"claude": "Claude Code", "codex": "Codex"}

var agentLatest = map[string]string{
	"claude": "https://downloads.claude.ai/claude-code-releases/latest",
	"codex":  "https://registry.npmjs.org/@openai/codex/latest",
}

const agentVersionTTL = 6 * time.Hour

// VersionPattern matches agent versions such as 1.2.3 or 1.2.3-beta.1.
var VersionPattern = rix.VersionPattern

// WithConfiguredVersions pins each agent a project asked for at "latest" to the version
// set in rix's config as agents.NAME.version. A version the project or --agents chose wins.
func WithConfiguredVersions(spec AgentSpec) (AgentSpec, error) {
	if len(spec) == 0 {
		return spec, nil
	}
	c, err := rix.LoadConfig("")
	if err != nil {
		return nil, err
	}
	agents, err := c.Agents()
	if err != nil {
		return nil, err
	}
	out := AgentSpec{}
	for agent, version := range spec {
		if a := agents[agent]; a != nil && a.Version != "" && version == "latest" {
			version = a.Version
		}
		out[agent] = version
	}
	return out, nil
}

// AgentSpec is what a project asks for: each agent with "latest" or a pinned version.
type AgentSpec map[string]string

// ParseAgentSpec checks a project's agents setting: {"claude": true, "codex": "1.2.3"}.
// true means the latest release; a version string pins one. Anything else is refused,
// so a branch can only choose between rox's own installers and their versions.
func ParseAgentSpec(v any, where string) (AgentSpec, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`Expected an object such as {"claude": true} for agents in %s`, where)
	}
	spec := AgentSpec{}
	for _, agent := range sortedKeys(m) {
		if AgentNames[agent] == "" {
			return nil, fmt.Errorf("Unknown agent %s in %s. Use claude or codex", agent, where)
		}
		switch value := m[agent].(type) {
		case bool:
			if !value {
				return nil, fmt.Errorf("Expected true or a version such as 1.2.3 for agents.%s in %s. Leave %s out to skip it", agent, where, agent)
			}
			spec[agent] = "latest"
		case string:
			if !VersionPattern.MatchString(value) {
				return nil, fmt.Errorf("Expected true or a version such as 1.2.3 for agents.%s in %s", agent, where)
			}
			spec[agent] = value
		default:
			return nil, fmt.Errorf("Expected true or a version such as 1.2.3 for agents.%s in %s", agent, where)
		}
	}
	return spec, nil
}

// ParseAgentFlag reads --agents claude,codex or claude=1.2.3,codex.
func ParseAgentFlag(value string) (AgentSpec, error) {
	spec := AgentSpec{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		agent, version, pinned := strings.Cut(item, "=")
		if AgentNames[agent] == "" {
			return nil, fmt.Errorf("Unknown agent %s for --agents. Use claude or codex", agent)
		}
		if !pinned {
			version = "latest"
		} else if !VersionPattern.MatchString(version) {
			return nil, fmt.Errorf("Expected a version such as 1.2.3 for %s in --agents", agent)
		}
		spec[agent] = version
	}
	return spec, nil
}

// Merge layers specs; later ones win.
func MergeAgents(specs ...AgentSpec) AgentSpec {
	out := AgentSpec{}
	for _, s := range specs {
		for agent, version := range s {
			out[agent] = version
		}
	}
	return out
}

func projectAgents(settings map[string]any, where string) AgentSpec {
	if v, ok := settings["agents"]; ok {
		spec, err := ParseAgentSpec(v, where)
		if err == nil {
			return spec
		}
	}
	return AgentSpec{}
}

func latestAgentVersion(agent string) (string, error) {
	req, _ := http.NewRequest("GET", agentLatest[agent], nil)
	req.Header.Set("User-Agent", "rox")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(body))
	if agent == "codex" {
		var doc struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return "", err
		}
		version = doc.Version
	}
	if !VersionPattern.MatchString(version) {
		return "", fmt.Errorf("unexpected version %.40q", version)
	}
	return version, nil
}

// LatestAgent is overridden in tests.
var LatestAgent = latestAgentVersion

// AgentVersions resolves each agent to a version: its pin, or the latest release, which
// rox checks at most every six hours. A failed check never stops a launch.
func AgentVersions(spec AgentSpec, refresh bool) map[string]string {
	path := filepath.Join(StateRoot(), "agent-versions.json")
	cache := map[string]struct {
		Version string  `json:"version"`
		Checked float64 `json:"checked"`
	}{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	versions := map[string]string{}
	changed := false
	now := float64(time.Now().Unix())
	for _, agent := range sortedAgents(spec) {
		if spec[agent] != "latest" {
			versions[agent] = spec[agent]
			continue
		}
		cached := cache[agent]
		if cached.Version != "" && !refresh && now-cached.Checked < agentVersionTTL.Seconds() {
			versions[agent] = cached.Version
			continue
		}
		version, err := LatestAgent(agent)
		if err != nil {
			versions[agent] = firstNonEmpty(cached.Version, "latest")
			ui.Warn("Could not check for a newer %s (%v). Installing %s", AgentNames[agent], err, versions[agent])
			continue
		}
		versions[agent] = version
		cache[agent] = struct {
			Version string  `json:"version"`
			Checked float64 `json:"checked"`
		}{version, now}
		changed = true
	}
	if changed {
		data, _ := sys.MarshalIndent(cache)
		_ = sys.WritePrivate(path, string(data))
	}
	return versions
}

func sortedAgents[V any](m map[string]V) []string {
	var out []string
	for agent := range m {
		out = append(out, agent)
	}
	sort.Strings(out)
	return out
}

// AgentSpecs are the bootstrap's AGENT=VERSION arguments.
func AgentSpecs(versions map[string]string) []string {
	var out []string
	for _, agent := range sortedAgents(versions) {
		out = append(out, agent+"="+versions[agent])
	}
	return out
}

// DescribeAgents is "Claude Code 2.1.3, Codex 0.9.0".
func DescribeAgents(versions map[string]string) string {
	var out []string
	for _, agent := range sortedAgents(versions) {
		out = append(out, AgentNames[agent]+" "+versions[agent])
	}
	return strings.Join(out, ", ")
}

// installAgents installs any agent whose version the guest lacks, and records what it has.
// It returns the agents that are new to the guest, which need credentials.
func installAgents(g *Guest, versions map[string]string) ([]string, error) {
	var fresh []string
	for _, agent := range sortedAgents(versions) {
		if _, had := g.Agents[agent]; !had {
			fresh = append(fresh, agent)
		}
	}
	err := ui.Run("Installing "+DescribeAgents(versions), "Installed "+DescribeAgents(versions), func(s *ui.Step) error {
		_, err := guestStage(g, "agents", stageOpts{}, AgentSpecs(versions)...)
		return err
	})
	if err != nil {
		return nil, err
	}
	for agent, version := range versions {
		g.Agents[agent] = version
	}
	g.MustSave()
	return fresh, nil
}
