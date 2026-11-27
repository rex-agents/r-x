package rix

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

// Outcome collects what an action did, for rix's own output or a hook's reply.
type Outcome struct {
	Done     []string
	Warnings []string
}

func (o *Outcome) done(format string, args ...any) {
	o.Done = append(o.Done, fmt.Sprintf(format, args...))
}
func (o *Outcome) warn(format string, args ...any) {
	o.Warnings = append(o.Warnings, fmt.Sprintf(format, args...))
}

func shellQuote(s string) string { return sys.QuoteArg(s) }

// agentEnvironment is the guest's ~/.rox/agents.env, which shells and the guest's Rex server read.
func agentEnvironment(agents map[string]*Agent, installed map[string]string) (string, error) {
	lines := []string{"# Written by rix from the launcher's vault. rix replaces this file."}
	for _, name := range AgentOrder {
		agent := agents[name]
		if _, ok := installed[name]; !ok || agent == nil {
			continue
		}
		for setting, variable := range Credentials[name] {
			file, ok := agent.Credential[setting]
			if variable == "" || !ok {
				continue
			}
			value, err := sys.ReadTrim(file)
			if err != nil {
				return "", err
			}
			lines = append(lines, "export "+variable+"="+shellQuote(value))
		}
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// PushCredentials writes each installed agent's credential into the guest, on stdin only.
// It never pushes a credential for an agent the guest doesn't have.
func PushCredentials(c *Config, g *contract.Guest, out *Outcome) error {
	agents, err := c.Agents()
	if err != nil {
		return err
	}
	env, err := agentEnvironment(agents, g.Agents)
	if err != nil {
		return err
	}
	if _, err := guestScript(g.Name, env, "env"); err != nil {
		return err
	}
	var signed []string
	if claude := agents["claude"]; claude != nil && len(claude.Credential) > 0 {
		if _, ok := g.Agents["claude"]; ok {
			signed = append(signed, "claude")
		}
	}
	if codex := agents["codex"]; codex != nil {
		if _, ok := g.Agents["codex"]; ok {
			if file, ok := codex.Credential["auth_file"]; ok {
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				if _, err := guestScript(g.Name, string(data), "codex-auth"); err != nil {
					return err
				}
				signed = append(signed, "codex")
			} else if file, ok := codex.Credential["api_key_file"]; ok {
				key, err := sys.ReadTrim(file)
				if err != nil {
					return err
				}
				if _, err := guestScript(g.Name, key+"\n", "codex-login"); err != nil {
					return err
				}
				signed = append(signed, "codex")
			}
		}
	}
	// Codex that rix signed in earlier, and the vault no longer has a credential for, is signed
	// out again. A login the guest made itself carries no rix marker and stays.
	state, stateErr := loadState(g.Name)
	if !containsString(signed, "codex") {
		owned := stateErr == nil && containsString(state.Credentials, "codex")
		if _, installed := g.Agents["codex"]; installed || owned {
			args := []string{}
			if owned {
				args = append(args, "owned")
			}
			if _, err := guestScript(g.Name, "", "codex-forget", args...); err != nil {
				return err
			}
		}
	}
	var unsigned []string
	for _, name := range AgentOrder {
		if _, ok := g.Agents[name]; ok && !containsString(signed, name) {
			unsigned = append(unsigned, name)
		}
	}
	if len(signed) > 0 {
		var names []string
		for _, n := range signed {
			names = append(names, AgentNames[n])
		}
		out.done("Signed in %s", strings.Join(names, " and "))
	}
	for _, n := range unsigned {
		out.warn("%s has no credential in rix, so it starts signed out. Add one with rix auth %s", AgentNames[n], n)
	}
	if stateErr == nil {
		state.Credentials = signed
		_ = saveState(g.Name, state)
	}
	return nil
}

// Sync of the launcher's agent setup.

// Settings that name programs or paths on the launcher, or hold its secrets, stay behind.
var (
	claudeHostSettings = map[string]bool{"hooks": true, "statusLine": true, "apiKeyHelper": true, "awsAuthRefresh": true,
		"awsCredentialExport": true, "otelHeadersHelper": true, "env": true, "enabledPlugins": true, "extraKnownMarketplaces": true}
	codexHostSettings = map[string]bool{"notify": true, "projects": true, "mcp_servers": true, "hooks": true, "plugins": true,
		"marketplaces": true, "desktop": true}
	// rox isn't installed in guests, so its own skill would only mislead an agent there.
	unsyncedSkills = map[string]bool{"rox": true}
	skillName      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
)

const syncRecord = ".rox/synced.json"

// AgentHome is an agent's config folder on this machine.
func AgentHome(agent string) string {
	variable, fallback := "CLAUDE_CONFIG_DIR", ".claude"
	if agent == "codex" {
		variable, fallback = "CODEX_HOME", ".codex"
	}
	if dir := os.Getenv(variable); dir != "" {
		return sys.Expand(dir)
	}
	return filepath.Join(sys.Home(), fallback)
}

func skillFolders(agent string) [][2]string {
	folders := [][2]string{{filepath.Join(AgentHome(agent), "skills"), "." + agent + "/skills"}}
	if agent == "codex" {
		folders = append(folders, [2]string{filepath.Join(sys.Home(), ".agents", "skills"), ".agents/skills"})
	}
	return folders
}

func launcherSkills(agent string) map[string]string {
	skills := map[string]string{}
	for _, f := range skillFolders(agent) {
		entries, err := os.ReadDir(f[0])
		if err != nil {
			continue
		}
		for _, e := range entries {
			dir := filepath.Join(f[0], e.Name())
			if skillName.MatchString(e.Name()) && !unsyncedSkills[e.Name()] && sys.IsFile(filepath.Join(dir, "SKILL.md")) {
				skills[f[1]+"/"+e.Name()] = sys.Resolve(dir)
			}
		}
	}
	return skills
}

func claudeStateFile() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(sys.Expand(dir), ".claude.json")
	}
	return filepath.Join(sys.Home(), ".claude.json")
}

func readJSONObject(path, what string, out *Outcome) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			out.warn("Could not read %s in %s, so guests don't get it", what, path)
		}
		return map[string]any{}
	}
	var v map[string]any
	if json.Unmarshal(data, &v) != nil {
		out.warn("Could not read %s in %s, so guests don't get it", what, path)
		return map[string]any{}
	}
	return v
}

func readCodexConfig(out *Outcome) map[string]any {
	p := filepath.Join(AgentHome("codex"), "config.toml")
	data, err := os.ReadFile(p)
	if err != nil {
		return map[string]any{}
	}
	var v map[string]any
	if err := toml.Unmarshal(data, &v); err != nil {
		out.warn("Could not read %s, so guests don't get your Codex MCP servers or settings", p)
		return map[string]any{}
	}
	return v
}

// remoteMCP reports whether an MCP server runs over the network, so a guest can reach it too.
func remoteMCP(agent string, server any) bool {
	m, ok := server.(map[string]any)
	if !ok {
		return false
	}
	_, hasURL := m["url"].(string)
	if agent == "claude" {
		return (m["type"] == "http" || m["type"] == "sse") && hasURL
	}
	_, hasCommand := m["command"]
	return hasURL && !hasCommand
}

func launcherMCP(agent string, s Sync, out *Outcome) map[string]any {
	if !s.MCPAll && len(s.MCPNames) == 0 {
		return map[string]any{}
	}
	var servers map[string]any
	if agent == "claude" {
		servers, _ = readJSONObject(claudeStateFile(), "your Claude Code MCP servers", out)["mcpServers"].(map[string]any)
	} else {
		servers, _ = readCodexConfig(out)["mcp_servers"].(map[string]any)
	}
	names := s.MCPNames
	if s.MCPAll {
		names = nil
		for _, name := range sortedKeys(servers) {
			if remoteMCP(agent, servers[name]) {
				names = append(names, name)
			}
		}
	}
	chosen := map[string]any{}
	for _, name := range names {
		server, ok := servers[name]
		switch {
		case !ok:
			out.warn("%s has no MCP server named %s, so guests don't get it", AgentNames[agent], name)
		case !remoteMCP(agent, server):
			out.warn("The %s MCP server runs a program on this machine, so guests don't get it. Install it in the guest image to use it there", name)
		default:
			chosen[name] = server
		}
	}
	return chosen
}

func launcherSettings(agent string, out *Outcome) map[string]any {
	settings := map[string]any{}
	if agent == "claude" {
		for k, v := range readJSONObject(filepath.Join(AgentHome("claude"), "settings.json"), "your Claude Code settings", out) {
			if !claudeHostSettings[k] {
				settings[k] = v
			}
		}
		return settings
	}
	for k, v := range readCodexConfig(out) {
		if !codexHostSettings[k] {
			settings[k] = v
		}
	}
	return settings
}

// Plan is what a sync copies into guests, read from the launcher's own agent setup.
type Plan struct {
	Files    map[string][]byte
	Dirs     map[string]string
	Settings map[string]map[string]any
	MCP      map[string]map[string]any
}

// MakePlan reads what to copy for the agents a guest has.
func MakePlan(agents map[string]*Agent, installed map[string]string, out *Outcome) *Plan {
	p := &Plan{Files: map[string][]byte{}, Dirs: map[string]string{}, Settings: map[string]map[string]any{}, MCP: map[string]map[string]any{}}
	for _, name := range AgentOrder {
		agent := agents[name]
		if _, ok := installed[name]; !ok || agent == nil {
			continue
		}
		if agent.Sync.Skills {
			for k, v := range launcherSkills(name) {
				p.Dirs[k] = v
			}
		}
		if agent.Sync.Instructions {
			file := "CLAUDE.md"
			if name == "codex" {
				file = "AGENTS.md"
			}
			if data, err := os.ReadFile(filepath.Join(AgentHome(name), file)); err == nil {
				p.Files["."+name+"/"+file] = data
			}
		}
		if agent.Sync.Settings {
			p.Settings[name] = launcherSettings(name, out)
		}
		p.MCP[name] = launcherMCP(name, agent.Sync, out)
	}
	return p
}

// Describe says what the plan copies: "skills a, b; CLAUDE.md; Claude Code MCP servers x".
func (p *Plan) Describe() string {
	var parts []string
	if len(p.Dirs) > 0 {
		seen := map[string]bool{}
		var names []string
		for k := range p.Dirs {
			n := path.Base(k)
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		sort.Strings(names)
		parts = append(parts, "skills "+strings.Join(names, ", "))
	}
	var files []string
	for k := range p.Files {
		files = append(files, k)
	}
	sort.Strings(files)
	for _, f := range files {
		parts = append(parts, path.Base(f))
	}
	for _, agent := range AgentOrder {
		if servers := p.MCP[agent]; len(servers) > 0 {
			parts = append(parts, AgentNames[agent]+" MCP servers "+strings.Join(sortedKeys(servers), ", "))
		}
	}
	for _, agent := range AgentOrder {
		if _, ok := p.Settings[agent]; ok {
			parts = append(parts, AgentNames[agent]+" settings")
		}
	}
	return strings.Join(parts, "; ")
}

func safeSyncedPath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) < 2 || path.IsAbs(p) {
		return false
	}
	if parts[0] != ".claude" && parts[0] != ".codex" && parts[0] != ".agents" {
		return false
	}
	for _, part := range parts {
		if part == ".." {
			return false
		}
	}
	return true
}

// MergeSynced replaces what rix synced into a guest config last time with what it syncs now.
func MergeSynced(current, previous map[string]any, keys, servers map[string]any, mcpKey string) map[string]any {
	for _, k := range listOf(previous["keys"]) {
		delete(current, k)
	}
	mcp, _ := current[mcpKey].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	for _, name := range listOf(previous["mcp"]) {
		delete(mcp, name)
	}
	for k, v := range keys {
		current[k] = v
	}
	for k, v := range servers {
		mcp[k] = v
	}
	if len(mcp) > 0 {
		current[mcpKey] = mcp
	} else {
		delete(current, mcpKey)
	}
	return map[string]any{"keys": sortedKeys(keys), "mcp": sortedKeys(servers)}
}

func listOf(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = x
	}
	return out
}

func readGuestFiles(name string, paths []string) (map[string][]byte, error) {
	text, err := guestScript(name, "", "read", paths...)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, line := range sys.Lines(text) {
		p, data, _ := strings.Cut(line, " ")
		if containsString(paths, p) {
			if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
				files[p] = decoded
			}
		}
	}
	return files, nil
}

// SyncSetup copies the launcher's skills, instructions, and chosen MCP servers and settings
// into the guest. A record in the guest lets the next sync remove what the launcher dropped.
func SyncSetup(c *Config, g *contract.Guest, out *Outcome) error {
	agents, err := c.Agents()
	if err != nil {
		return err
	}
	if len(g.Agents) == 0 {
		return nil
	}
	plan := MakePlan(agents, g.Agents, out)
	existing, err := readGuestFiles(g.Name, []string{syncRecord, ".claude.json", ".codex/config.toml"})
	if err != nil {
		return err
	}
	record := map[string]any{}
	_ = json.Unmarshal(existing[syncRecord], &record)
	var recorded []string
	for _, p := range listOf(record["paths"]) {
		if safeSyncedPath(p) {
			recorded = append(recorded, p)
		}
	}
	owned := map[string][]byte{}
	for k, v := range plan.Files {
		owned[k] = v
	}
	if s, ok := plan.Settings["claude"]; ok {
		data, _ := json.MarshalIndent(s, "", "  ")
		owned[".claude/settings.json"] = append(data, '\n')
	}
	var paths []string
	for k := range owned {
		paths = append(paths, k)
	}
	for k := range plan.Dirs {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	newRecord := map[string]any{"paths": paths}
	private := map[string][]byte{}

	claudePrevious, _ := record["claude"].(map[string]any)
	if claudePrevious == nil {
		claudePrevious = map[string]any{}
	}
	if _, ok := g.Agents["claude"]; ok && (len(plan.MCP["claude"]) > 0 || len(listOf(claudePrevious["mcp"])) > 0) {
		state := map[string]any{}
		raw := existing[".claude.json"]
		if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &state) == nil {
			newRecord["claude"] = MergeSynced(state, claudePrevious, nil, plan.MCP["claude"], "mcpServers")
			if _, ok := state["hasCompletedOnboarding"]; !ok {
				state["hasCompletedOnboarding"] = true
			}
			data, _ := json.MarshalIndent(state, "", "  ")
			private[".claude.json"] = append(data, '\n')
		} else {
			out.warn("%s has a ~/.claude.json rix can't read, so it doesn't get your MCP servers", g.Name)
		}
	}
	codexPrevious, _ := record["codex"].(map[string]any)
	if codexPrevious == nil {
		codexPrevious = map[string]any{}
	}
	codexKeys := plan.Settings["codex"]
	if _, ok := g.Agents["codex"]; ok && (len(codexKeys) > 0 || len(plan.MCP["codex"]) > 0 ||
		len(listOf(codexPrevious["keys"])) > 0 || len(listOf(codexPrevious["mcp"])) > 0) {
		config := map[string]any{}
		if err := toml.Unmarshal(existing[".codex/config.toml"], &config); err == nil {
			newRecord["codex"] = MergeSynced(config, codexPrevious, codexKeys, plan.MCP["codex"], "mcp_servers")
			if data, err := toml.Marshal(config); err == nil {
				private[".codex/config.toml"] = data
			} else {
				out.warn("Could not write %s's Codex config (%v)", g.Name, err)
			}
		} else {
			out.warn("Could not read %s's ~/.codex/config.toml, so it doesn't get your Codex MCP servers or settings", g.Name)
		}
	}
	data, _ := json.MarshalIndent(newRecord, "", "  ")
	private[syncRecord] = append(data, '\n')

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	now := time.Now()
	addBytes := func(name string, data []byte, mode int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: mode, ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	for _, target := range sortedStrings(plan.Dirs) {
		folder := plan.Dirs[target]
		err := filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(folder, p)
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			name := path.Join(target, filepath.ToSlash(rel))
			info, err := os.Stat(p) // Links are followed.
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return tw.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o755, ModTime: now, Typeflag: tar.TypeDir})
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return addBytes(name, data, int64(info.Mode().Perm()))
		})
		if err != nil {
			return err
		}
	}
	for _, name := range sortedBytes(owned) {
		mode := int64(0o644)
		if strings.HasSuffix(name, "settings.json") {
			mode = 0o600
		}
		if err := addBytes(name, owned[name], mode); err != nil {
			return err
		}
	}
	for _, name := range sortedBytes(private) {
		if err := addBytes(name, private[name], 0o600); err != nil {
			return err
		}
	}
	// The last entry tells the guest the whole archive arrived.
	if err := addBytes(".rix-complete", nil, 0o600); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	// The guest swaps in install, then removes stale: what the last sync left that this one doesn't.
	install := map[string]bool{}
	for p := range owned {
		install[p] = true
	}
	for p := range plan.Dirs {
		install[p] = true
	}
	for p := range private {
		install[p] = true
	}
	var args []string
	for _, p := range sortedSet(install) {
		args = append(args, p)
	}
	args = append(args, "--")
	for _, p := range recorded {
		if !install[p] {
			args = append(args, p)
		}
	}
	if err := guestScriptBytes(g.Name, archive.Bytes(), "sync", args...); err != nil {
		return err
	}
	what := plan.Describe()
	if what != "" {
		out.done("Copied %s", what)
	}
	if state, err := loadState(g.Name); err == nil {
		state.Synced, state.SyncedWhat = time.Now().Format(time.RFC3339), what
		_ = saveState(g.Name, state)
	}
	return nil
}

func sortedSet(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedBytes(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
