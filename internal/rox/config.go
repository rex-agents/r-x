package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Config is the launcher's rox config, kept as JSON so unknown keys survive a rewrite.
type Config struct {
	Path string
	Data map[string]any
}

// ConfigLocation is the config path: value when given, or ~/.config/rox/config.json.
func ConfigLocation(value string) string {
	if value != "" {
		return sys.Expand(value)
	}
	return filepath.Join(sys.XDG("XDG_CONFIG_HOME", ".config"), "rox", "config.json")
}

// LoadConfig reads the config. A named config must exist; the default may not.
func LoadConfig(value string) (*Config, error) {
	path := ConfigLocation(value)
	if value != "" {
		if _, err := sys.ConfiguredFile(path); err != nil {
			return nil, err
		}
	}
	data, err := sys.ReadJSONObject(path)
	if err != nil {
		return nil, err
	}
	return &Config{Path: path, Data: data}, nil
}

// Section returns a backend's settings object.
func (c *Config) Section(name string) (map[string]any, error) {
	v, ok := c.Data[name]
	if !ok || v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Expected an object for %s in %s", name, c.Path)
	}
	return m, nil
}

// Update locks the config, rereads it, lets fn change it and writes it back.
func UpdateConfig(path string, fn func(data map[string]any) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
	return sys.WriteJSON(path, data)
}

// warnMovedSettings points at rix when the config still has settings rix owns now.
func (c *Config) warnMovedSettings() {
	var moved []string
	for _, key := range []string{"git_keys", "git_known_hosts", "git_agent", "agents"} {
		if _, ok := c.Data[key]; ok {
			moved = append(moved, key)
		}
	}
	if len(moved) == 0 {
		return
	}
	ui.Warn("%s in %s %s to rix now, and rox ignores %s", strings.Join(moved, ", "), c.Path,
		map[bool]string{true: "moves", false: "move"}[len(moved) == 1], map[bool]string{true: "it", false: "them"}[len(moved) == 1])
	ui.Hint("Run " + ui.Code("rix migrate") + " to move them")
	if agents, ok := c.Data["agents"].(map[string]any); ok {
		if snippet := LegacyAgentSnippet(agents); snippet != "" {
			ui.Hint("Agents come from each project now. To keep installing them, add to " + ui.Code(LocalConfig) + ": " + snippet)
		}
	}
}

// LegacyAgentSnippet is the .rox.local.json agents setting that reproduces an old config's agents section.
func LegacyAgentSnippet(agents map[string]any) string {
	out := map[string]any{}
	for _, agent := range AgentOrder {
		value, present := agents[agent]
		if value == false {
			continue
		}
		pin := any(true)
		if m, ok := value.(map[string]any); ok {
			if v, ok := m["version"].(string); ok && v != "" && v != "latest" {
				pin = v
			}
		} else if present && value != nil {
			continue
		}
		out[agent] = pin
	}
	if len(out) == 0 {
		return ""
	}
	data, _ := json.Marshal(map[string]any{"agents": out})
	return string(data)
}

// ProjectKeys are what a branch's .rox.json may set for each backend.
var ProjectKeys = map[string][]string{
	"container": {"cpus", "image", "memory", "prebuild"},
	"linux":     {"cpus", "image", "memory", "prebuild"},
	"macos":     {"cpus", "image", "memory", "prebuild"},
	// A branch can't choose what the launcher's cloud account runs or spends.
	"fly":     {},
	"railway": {},
}

// LauncherKeys describe the launch machine, so only its own config may set them.
var LauncherKeys = []string{"engine", "user", "identity", "host_key", "git_keys", "git_known_hosts", "tailscale", "git_agent"}

// CloudKeys are the fly and railway sections' own settings.
var CloudKeys = map[string][]string{
	"fly":     {"app", "org", "region", "disk", "image", "cpus", "memory"},
	"railway": {"project", "environment", "idle_timeout"},
}

// CheckProjectSettings validates .rox.json or .rox.local.json, named by where in errors.
func CheckProjectSettings(v any, where string) (map[string]any, error) {
	settings, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Expected a JSON object in %s", where)
	}
	keys := sortedKeys(settings)
	for _, key := range keys {
		value := settings[key]
		switch {
		case key == "backend":
			if s, ok := value.(string); !ok || !contains(Backends, s) {
				return nil, fmt.Errorf("Expected backend to be one of %s in %s", strings.Join(Backends, ", "), where)
			}
		case key == "agents":
			if _, err := ParseAgentSpec(value, where); err != nil {
				return nil, err
			}
		case ProjectKeys[key] != nil:
			section, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Expected an object for %s in %s", key, where)
			}
			for _, setting := range sortedKeys(section) {
				if contains(LauncherKeys, setting) || contains(CloudKeys[key], setting) {
					return nil, fmt.Errorf("%s.%s can only be set in the rox config on the launch machine, not in %s", key, setting, where)
				}
				if !contains(ProjectKeys[key], setting) {
					allowed := strings.Join(ProjectKeys[key], ", ")
					if allowed == "" {
						allowed = fmt.Sprintf("no settings; set %s guests in the rox config", key)
					}
					return nil, fmt.Errorf("Unknown setting %s.%s in %s. Use %s", key, setting, where, allowed)
				}
			}
		case contains(LauncherKeys, key):
			return nil, fmt.Errorf("%s can only be set in the rox config on the launch machine, not in %s", key, where)
		default:
			return nil, fmt.Errorf("Unknown setting %s in %s. Use backend, agents, %s", key, where, strings.Join(Backends, ", "))
		}
	}
	return settings, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ReadProjectFile reads a .rox.json or .rox.local.json from disk. A missing file is empty.
func ReadProjectFile(path, where string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %v", path, err)
	}
	return CheckProjectSettings(v, where)
}

// Tailnet is the launcher's settings for putting guests on the tailnet.
type Tailnet struct {
	KeyFile    string
	Tags       []string
	ControlURL string
}

var tagPattern = regexp.MustCompile(`^tag:[a-z0-9-]+$`)

// TailnetSettings checks the config's tailscale section, or returns nil to keep guests off the tailnet.
func (c *Config) TailnetSettings(disabled bool) (*Tailnet, error) {
	v, ok := c.Data["tailscale"]
	if !ok || v == nil || disabled {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Expected an object for tailscale in %s", c.Path)
	}
	for _, key := range sortedKeys(m) {
		if !contains([]string{"auth_key_file", "tags", "control_url"}, key) {
			return nil, fmt.Errorf("Unknown tailscale setting %s in %s. Use auth_key_file, tags and control_url", key, c.Path)
		}
	}
	file, _ := m["auth_key_file"].(string)
	key, err := sys.ConfiguredFile(file)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("Set tailscale.auth_key_file in %s to a file holding a Tailscale auth key", c.Path)
	}
	if info, err := os.Stat(key); err == nil && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s holds a Tailscale auth key, so only you may read it. Run chmod 600 %s", key, key)
	}
	t := &Tailnet{KeyFile: key}
	if raw, ok := m["tags"]; ok {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("Expected a list of tags such as [\"tag:rox-guest\"] for tailscale.tags in %s", c.Path)
		}
		for _, item := range list {
			tag, ok := item.(string)
			if !ok || !tagPattern.MatchString(tag) {
				return nil, fmt.Errorf("Expected a list of tags such as [\"tag:rox-guest\"] for tailscale.tags in %s", c.Path)
			}
			t.Tags = append(t.Tags, tag)
		}
	}
	if raw, ok := m["control_url"]; ok {
		url, ok := raw.(string)
		if !ok || (url != "" && !strings.HasPrefix(url, "https://")) {
			return nil, fmt.Errorf("Expected an https:// URL for tailscale.control_url in %s", c.Path)
		}
		t.ControlURL = url
	}
	return t, nil
}

// Key reads the auth key.
func (t *Tailnet) Key() (string, error) { return sys.ReadTrim(t.KeyFile) }

// savedTailnet is the tailnet settings for reconnecting a guest launched on the tailnet.
func savedTailnet(g *Guest, configValue string) *Tailnet {
	if g.TailnetName == "" {
		return nil
	}
	c, err := LoadConfig(configValue)
	if err != nil {
		return nil
	}
	t, err := c.TailnetSettings(false)
	if err != nil {
		return nil
	}
	return t
}

// tailscaleKeysURL is where the Tailscale admin console makes auth keys.
const tailscaleKeysURL = "https://console.tailscale.com/admin/settings/keys"

// ConfigureTailscale saves an auth key beside the config and points the tailscale section at it.
func ConfigureTailscale(configValue string, tags []string, controlURL string) error {
	path := ConfigLocation(configValue)
	if len(tags) == 0 {
		tags = []string{"tag:rox-guest"}
	}
	for _, tag := range tags {
		if !tagPattern.MatchString(tag) {
			return fmt.Errorf("Expected a tag such as tag:rox-guest, not %s", tag)
		}
	}
	if controlURL != "" && !strings.HasPrefix(controlURL, "https://") {
		return errors.New("Expected an https:// URL for --control-url")
	}
	ui.Header("config tailscale", "")
	key, err := ui.Secret("Tailscale auth key:", "Create a reusable, ephemeral auth key tagged for guests in the Tailscale admin console:\n"+tailscaleKeysURL)
	if err != nil {
		return err
	}
	if key == "" || strings.ContainsAny(key, " \t\n") {
		return errors.New("Expected a Tailscale auth key such as tskey-auth-…\nCreate one at " + tailscaleKeysURL)
	}
	var keyPath string
	err = UpdateConfig(path, func(data map[string]any) error {
		current := map[string]any{}
		if v, ok := data["tailscale"]; ok && v != nil {
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("Expected an object for tailscale in %s", path)
			}
			current = m
		}
		keyPath = filepath.Join(filepath.Dir(path), "tailscale-authkey")
		if file, _ := current["auth_key_file"].(string); file != "" {
			keyPath = sys.Expand(file)
		}
		if err := sys.WritePrivate(keyPath, key+"\n"); err != nil {
			return err
		}
		settings := map[string]any{"auth_key_file": keyPath, "tags": tags}
		if controlURL == "" {
			controlURL, _ = current["control_url"].(string)
		}
		if controlURL != "" {
			settings["control_url"] = controlURL
		}
		data["tailscale"] = settings
		return nil
	})
	if err != nil {
		return err
	}
	ui.Done("Saved the auth key to %s", ui.Code(keyPath))
	ui.Done("New guests join the tailnet with %s", strings.Join(tags, ", "))
	ui.Info("Guests that are already running stay off it until you recreate them")
	return nil
}

// EditConfig opens the config in the user's editor, creating it if needed, and checks it parses.
func EditConfig(value string) error {
	path := ConfigLocation(value)
	editor := strings.Fields(firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
	if !sys.Has(editor[0]) {
		return fmt.Errorf("Could not find the editor %s. Set VISUAL or EDITOR to an editor on PATH", editor[0])
	}
	if !sys.IsFile(path) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			return err
		}
	}
	result, err := sys.Try(sys.Cmd{Argv: append(editor, path), Interactive: true})
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("%s exited with status %d", editor[0], result.Code)
	}
	if _, err := sys.ReadJSONObject(path); err != nil {
		return fmt.Errorf("%v. Run rox config to fix it", err)
	}
	ui.Done("Saved %s", ui.Code(path))
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func intSetting(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	case int:
		return n, true
	}
	return 0, false
}
