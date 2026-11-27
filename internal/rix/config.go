// Package rix is the launcher-side vault for the secrets and settings agents and Git use
// inside rox guests: Git SSH keys, Claude Code and Codex credentials, and synced skills,
// instructions, MCP servers and settings. rix installs nothing.
//
// rix never opens SSH itself. It reaches guests only through rox exec, reads them with
// rox inspect --json, and secrets reach a guest only on the stdin of a rox exec.
package rix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rex-agents/r-x/internal/sys"
)

// Agents rix signs in.
var AgentOrder = []string{"claude", "codex"}

// AgentNames are their product names.
var AgentNames = map[string]string{"claude": "Claude Code", "codex": "Codex"}

// Credentials each agent takes. A variable name means the file holds that variable's value
// for the guest; "" means rix handles it another way: codex's auth_file is copied to
// ~/.codex/auth.json, and its api_key_file goes to codex login.
var Credentials = map[string]map[string]string{
	"claude": {"token_file": "CLAUDE_CODE_OAUTH_TOKEN", "api_key_file": "ANTHROPIC_API_KEY"},
	"codex":  {"auth_file": "", "api_key_file": ""},
}

// SecretFiles are where rix auth saves each kind of credential, next to the config.
var SecretFiles = map[[2]string]string{
	{"claude", "token_file"}: "claude-token", {"claude", "api_key_file"}: "anthropic-api-key",
	{"codex", "api_key_file"}: "openai-api-key",
}

// SyncDefaults are what rix copies from the launcher's agent setup unless told otherwise.
var SyncDefaults = Sync{Skills: true, Instructions: true}

// Sync chooses what of the launcher's own agent setup reaches guests.
type Sync struct {
	Skills       bool
	Instructions bool
	Settings     bool
	// MCP is every remote MCP server when MCPAll, or the named ones.
	MCPAll   bool
	MCPNames []string
}

// Agent is one agent's settings in the vault.
type Agent struct {
	// Credential maps a credential setting to its file, at most one.
	Credential map[string]string
	Sync       Sync
}

// Config is rix's config, ~/.config/rix/config.json, kept as JSON so unknown keys survive.
type Config struct {
	Path string
	Data map[string]any
}

// ConfigLocation is the config path: value when given, or ~/.config/rix/config.json.
func ConfigLocation(value string) string {
	if value != "" {
		return sys.Expand(value)
	}
	return filepath.Join(sys.XDG("XDG_CONFIG_HOME", ".config"), "rix", "config.json")
}

// ConfigDir is the folder that holds the config and the secret files rix saves.
func (c *Config) Dir() string { return filepath.Dir(c.Path) }

// LoadConfig reads the config.
func LoadConfig(value string) (*Config, error) {
	path := ConfigLocation(value)
	data, err := sys.ReadJSONObject(path)
	if err != nil {
		return nil, err
	}
	return &Config{Path: path, Data: data}, nil
}

// Update locks the config, rereads it, lets fn change it and writes it back.
func Update(path string, fn func(data map[string]any) error) error {
	return UpdateThen(path, fn, nil)
}

// UpdateThen is Update, and after the change is committed, still holding the lock, runs after.
// A command that retires files the config named does it there, so no other command can
// reselect and rewrite one of those names before it is gone.
func UpdateThen(path string, fn func(data map[string]any) error, after func()) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := sys.WaitLock(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Unlock()
	data, err := sys.ReadJSONObject(path)
	if err != nil {
		return err
	}
	if err := fn(data); err != nil {
		return err
	}
	if !sys.IsFile(path) {
		if err := sys.WriteAtomic(path, []byte("{}\n"), 0o600, false); err != nil {
			return err
		}
	}
	if err := sys.WriteJSON(path, data); err != nil {
		return err
	}
	if after != nil {
		after()
	}
	return nil
}

// GitKeys are the configured key paths, or nil when none are configured, which means ssh's defaults.
func (c *Config) GitKeys() ([]string, bool, error) {
	raw, ok := c.Data["git_keys"]
	if !ok || raw == nil {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, fmt.Errorf("Expected a list of Git key paths in %s", c.Path)
	}
	var keys []string
	for _, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, true, fmt.Errorf("Expected a list of Git key paths in %s", c.Path)
		}
		keys = append(keys, s)
	}
	return keys, true, nil
}

// GitAgent reports whether guests keep Git keys for their whole life, not just the clone.
func (c *Config) GitAgent() (bool, error) {
	raw, ok := c.Data["git_agent"]
	if !ok {
		return true, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return true, fmt.Errorf("Expected true or false for git_agent in %s", c.Path)
	}
	return b, nil
}

// KnownHostsFile is the configured file of pinned Git server keys, or "".
func (c *Config) KnownHostsFile() string {
	s, _ := c.Data["git_known_hosts"].(string)
	return s
}

// Agents reads the agents section. Agents without credentials still get synced setup.
func (c *Config) Agents() (map[string]*Agent, error) {
	agents := map[string]*Agent{}
	raw, ok := c.Data["agents"]
	section := map[string]any{}
	if ok && raw != nil {
		if section, ok = raw.(map[string]any); !ok {
			return nil, fmt.Errorf("Expected an object for agents in %s", c.Path)
		}
	}
	for name := range section {
		if Credentials[name] == nil {
			return nil, fmt.Errorf("Unknown agent %s in %s. Use claude or codex", name, c.Path)
		}
	}
	for _, name := range AgentOrder {
		value := section[name]
		settings, _ := value.(map[string]any)
		if value != nil && settings == nil {
			return nil, fmt.Errorf("Expected an object for agents.%s in %s", name, c.Path)
		}
		agent := &Agent{Credential: map[string]string{}, Sync: SyncDefaults}
		allowed := []string{"sync", "version"}
		for setting := range Credentials[name] {
			allowed = append(allowed, setting)
		}
		for _, key := range sortedKeys(settings) {
			if !containsString(allowed, key) {
				sort.Strings(allowed)
				return nil, fmt.Errorf("Unknown setting agents.%s.%s in %s. Use %s", name, key, c.Path, strings.Join(allowed, ", "))
			}
		}
		for setting := range Credentials[name] {
			v, present := settings[setting]
			if !present {
				continue
			}
			s, ok := v.(string)
			if !ok || s == "" {
				return nil, fmt.Errorf("Expected a file path for agents.%s.%s in %s", name, setting, c.Path)
			}
			path, err := sys.ConfiguredFile(s)
			if err != nil {
				return nil, err
			}
			if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
				return nil, fmt.Errorf("%s holds a credential for %s, so only you may read it. Run chmod 600 %s", path, AgentNames[name], path)
			}
			agent.Credential[setting] = path
		}
		if len(agent.Credential) > 1 {
			return nil, fmt.Errorf("Choose one credential for agents.%s in %s", name, c.Path)
		}
		if s, err := parseSync(name, settings["sync"], c.Path); err != nil {
			return nil, err
		} else {
			agent.Sync = s
		}
		agents[name] = agent
	}
	return agents, nil
}

func parseSync(agent string, v any, where string) (Sync, error) {
	s := SyncDefaults
	if v == nil {
		return s, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return s, fmt.Errorf("Expected an object for agents.%s.sync in %s", agent, where)
	}
	for _, key := range sortedKeys(m) {
		switch key {
		case "skills", "instructions", "settings":
			b, ok := m[key].(bool)
			if !ok {
				return s, fmt.Errorf("Expected true or false for agents.%s.sync.%s in %s", agent, key, where)
			}
			switch key {
			case "skills":
				s.Skills = b
			case "instructions":
				s.Instructions = b
			default:
				s.Settings = b
			}
		case "mcp":
			switch mcp := m[key].(type) {
			case bool:
				s.MCPAll, s.MCPNames = mcp, nil
			case []any:
				s.MCPAll, s.MCPNames = false, nil
				for _, item := range mcp {
					name, ok := item.(string)
					if !ok || name == "" {
						return s, fmt.Errorf("Expected true, false or a list of server names for agents.%s.sync.mcp in %s", agent, where)
					}
					s.MCPNames = append(s.MCPNames, name)
				}
			default:
				return s, fmt.Errorf("Expected true, false or a list of server names for agents.%s.sync.mcp in %s", agent, where)
			}
		default:
			return s, fmt.Errorf("Unknown setting agents.%s.sync.%s in %s. Use skills, instructions, mcp, settings", agent, key, where)
		}
	}
	return s, nil
}

// JSON is the sync setting as the config stores it.
func (s Sync) JSON() map[string]any {
	out := map[string]any{"skills": s.Skills, "instructions": s.Instructions, "settings": s.Settings}
	switch {
	case len(s.MCPNames) > 0:
		names := make([]any, len(s.MCPNames))
		for i, n := range s.MCPNames {
			names[i] = n
		}
		out["mcp"] = names
	default:
		out["mcp"] = s.MCPAll
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// DefaultSSHKeys are the identities ssh tries when none is configured, in its own order.
var DefaultSSHKeys = []string{"id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk"}

func defaultKeys() []string {
	var keys []string
	for _, name := range DefaultSSHKeys {
		if p := filepath.Join(sys.Home(), ".ssh", name); sys.IsFile(p) {
			keys = append(keys, p)
		}
	}
	return keys
}

// resolveKeys are the Git keys for a launch: its --git-key paths, the configured keys, or ssh's defaults.
func resolveKeys(c *Config, override []string) ([]string, bool, error) {
	keys, configured, err := c.GitKeys()
	if err != nil {
		return nil, false, err
	}
	usedDefault := false
	switch {
	case len(override) > 0:
		keys = override
	case !configured:
		keys, usedDefault = defaultKeys(), true
	}
	var out []string
	for _, k := range keys {
		p, err := sys.ConfiguredFile(k)
		if err != nil {
			return nil, false, err
		}
		out = append(out, p)
	}
	return out, usedDefault, nil
}

// knownHosts are the pinned Git server keys for a launch, from --git-known-hosts or the config.
func knownHosts(c *Config, override string) (string, error) {
	file := override
	if file == "" {
		file = c.KnownHostsFile()
	}
	if file == "" {
		return "", nil
	}
	path, err := sys.ConfiguredFile(file)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	return string(data), err
}

// StateRoot is rix's per-guest state folder.
func StateRoot() string { return filepath.Join(sys.XDG("XDG_STATE_HOME", ".local/state"), "rix") }

// GuestState is what rix remembers about one guest.
type GuestState struct {
	// GitKeys are the keys the guest's agent holds.
	GitKeys []string `json:"git_keys"`
	// Forward is whether the guest keeps the keys for its whole life.
	Forward bool `json:"forward"`
	// AgentPid is the guest's ssh-agent.
	AgentPid int `json:"agent_pid,omitempty"`
	// AgentStart is when that process started. A record without it predates it.
	AgentStart string `json:"agent_start,omitempty"`
	// Credentials are the agents whose credentials rix pushed.
	Credentials []string `json:"credentials,omitempty"`
	// Synced is when rix last synced agent setup, as RFC 3339.
	Synced string `json:"synced,omitempty"`
	// SyncedWhat describes the last sync.
	SyncedWhat string `json:"synced_what,omitempty"`
}

func statePath(name string) string { return filepath.Join(StateRoot(), name, "state.json") }

func loadState(name string) (*GuestState, error) {
	data, err := os.ReadFile(statePath(name))
	if errors.Is(err, os.ErrNotExist) {
		return &GuestState{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s GuestState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("rix's state for %s is unreadable: %v", name, err)
	}
	return &s, nil
}

func saveState(name string, s *GuestState) error {
	if _, err := sys.PrivateDir(filepath.Dir(statePath(name))); err != nil {
		return err
	}
	data, err := sys.MarshalIndent(s)
	if err != nil {
		return err
	}
	return sys.WritePrivate(statePath(name), string(data))
}

// runtimeDir holds a guest's agent socket. Socket paths must stay short, so on macOS, which
// has no XDG_RUNTIME_DIR, it's under /tmp, keyed by the guest name's last part.
func runtimeDir(name string) string {
	id := name
	if i := strings.LastIndexByte(name, '-'); i >= 0 {
		id = name[i+1:]
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "rix", id)
	}
	return filepath.Join(fmt.Sprintf("/tmp/rix-%d", os.Getuid()), id)
}

func agentSocket(name string) string { return filepath.Join(runtimeDir(name), "agent.sock") }
