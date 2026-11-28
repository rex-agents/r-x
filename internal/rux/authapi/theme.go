package authapi

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The theme is how every rux screen looks: the web app's colors and the terminal UIs' palette.
// It's a preset plus optional per-token overrides, kept by the authority in ~/.rux/theme.json.
//
//	GET    /api/theme   ThemeInfo: the theme, its resolved colors, the tokens and the presets
//	PUT    /api/theme   ThemeUpdate: change the preset and/or override tokens; answers ThemeInfo
//	DELETE /api/theme   back to the default theme; answers ThemeInfo
//
// A change is also sent on the pack feed as a "theme" event carrying the new ThemeInfo.

// Theme is what's saved: the preset and the tokens that differ from it.
type Theme struct {
	Preset string            `json:"preset"`
	Colors map[string]string `json:"colors,omitempty"`
}

// ThemeUpdate is the body of PUT /api/theme. A Preset switches to that preset and drops the
// overrides, unless Colors comes with it. Colors set tokens ("#rrggbb" or "#rgb"); an empty value
// drops that token's override.
type ThemeUpdate struct {
	Preset string            `json:"preset,omitempty"`
	Colors map[string]string `json:"colors,omitempty"`
}

// ThemeInfo is GET /api/theme.
type ThemeInfo struct {
	Theme
	Default  bool              `json:"default"`  // the built-in look: no preset chosen, nothing overridden
	Resolved map[string]string `json:"resolved"` // every token's color
	Tokens   []ThemeToken      `json:"tokens"`
	Presets  []ThemePreset     `json:"presets"`
}

// ThemeToken is one color a theme sets.
type ThemeToken struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ThemePreset is a named, complete set of colors.
type ThemePreset struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Light       bool              `json:"light,omitempty"`
	Colors      map[string]string `json:"colors"`
}

// ThemeTokens are the colors a theme sets, in display order.
var ThemeTokens = []ThemeToken{
	{"bg", "page background"},
	{"panel", "cards, sidebar and composer"},
	{"raise", "menus and raised controls"},
	{"line", "borders and rules"},
	{"text", "body text"},
	{"bright", "headings and emphasis"},
	{"muted", "secondary text"},
	{"faint", "hints and timestamps"},
	{"accent", "links, selection and primary actions"},
	{"accent2", "the second accent (highlights, the TUI's violet)"},
	{"ok", "working and success"},
	{"warn", "starting and warnings"},
	{"err", "failures and errors"},
	{"ink", "text on accent-colored backgrounds"},
}

// DefaultThemePreset is the preset an authority uses until someone picks another.
const DefaultThemePreset = "rux"

// ThemePresets are the built-in themes.
var ThemePresets = []ThemePreset{
	{Name: "rux", Description: "quiet greys with a pink accent (the default)", Colors: map[string]string{
		"bg": "#313438", "panel": "#2b2e32", "raise": "#383b40", "line": "#3b3e43", "text": "#d4d8dc", "bright": "#ececec",
		"muted": "#9a9ea4", "faint": "#7c8187", "accent": "#e378b3", "accent2": "#a78bfa", "ok": "#77db89", "warn": "#ea8d79",
		"err": "#e77975", "ink": "#1f2124"}},
	{Name: "matrix", Description: "black and phosphor green", Colors: map[string]string{
		"bg": "#050a05", "panel": "#081208", "raise": "#0c1a0c", "line": "#143d14", "text": "#8dff8d", "bright": "#d8ffd8",
		"muted": "#52c452", "faint": "#2f8a2f", "accent": "#00ff41", "accent2": "#7dffb2", "ok": "#00ff41", "warn": "#c8ff00",
		"err": "#ff5555", "ink": "#001a00"}},
	{Name: "ember", Description: "warm dark browns with orange", Colors: map[string]string{
		"bg": "#1c1512", "panel": "#241a16", "raise": "#2e221c", "line": "#3d2d25", "text": "#e8d9cc", "bright": "#fff3e8",
		"muted": "#b09a88", "faint": "#85715f", "accent": "#ff8a3d", "accent2": "#ffc861", "ok": "#9bd36b", "warn": "#ffc861",
		"err": "#ff6b5b", "ink": "#1c1512"}},
	{Name: "ocean", Description: "deep blue with cyan", Colors: map[string]string{
		"bg": "#0f1b26", "panel": "#13222f", "raise": "#1a2c3b", "line": "#24394b", "text": "#cfe3f0", "bright": "#f0f8ff",
		"muted": "#8aa7bb", "faint": "#62809a", "accent": "#4cc9f0", "accent2": "#a78bfa", "ok": "#5ee6a8", "warn": "#ffd166",
		"err": "#ff6b6b", "ink": "#0b1620"}},
	{Name: "paper", Description: "light: warm white with magenta", Light: true, Colors: map[string]string{
		"bg": "#f6f5f1", "panel": "#ffffff", "raise": "#ffffff", "line": "#dedbd2", "text": "#2b2a27", "bright": "#111111",
		"muted": "#66635b", "faint": "#8f8b80", "accent": "#c2185b", "accent2": "#6d4bd8", "ok": "#1e8e3e", "warn": "#a35a00",
		"err": "#c5221f", "ink": "#ffffff"}},
}

// FindThemePreset returns the preset named name, or nil.
func FindThemePreset(name string) *ThemePreset {
	for i := range ThemePresets {
		if ThemePresets[i].Name == name {
			return &ThemePresets[i]
		}
	}
	return nil
}

// IsThemeToken says whether name is one of ThemeTokens.
func IsThemeToken(name string) bool {
	for _, t := range ThemeTokens {
		if t.Name == name {
			return true
		}
	}
	return false
}

var hexColorRe = regexp.MustCompile(`^#?([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// NormalizeColor turns "#abc", "abc" or "#AABBCC" into "#aabbcc".
func NormalizeColor(c string) (string, error) {
	m := hexColorRe.FindStringSubmatch(strings.TrimSpace(c))
	if m == nil {
		return "", fmt.Errorf("%q isn't a hex color like #e378b3", c)
	}
	h := strings.ToLower(m[1])
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	return "#" + h, nil
}

// Resolve gives every token's color: the preset's, with the overrides on top. An unknown preset
// resolves as the default one.
func (t Theme) Resolve() map[string]string {
	p := FindThemePreset(t.Preset)
	if p == nil {
		p = FindThemePreset(DefaultThemePreset)
	}
	out := make(map[string]string, len(ThemeTokens))
	for k, v := range p.Colors {
		out[k] = v
	}
	for k, v := range t.Colors {
		if IsThemeToken(k) {
			out[k] = v
		}
	}
	return out
}

// IsDefault says the theme is the built-in look.
func (t Theme) IsDefault() bool {
	return (t.Preset == "" || t.Preset == DefaultThemePreset) && len(t.Colors) == 0
}

// Apply returns t changed by u, or why u can't apply.
func (t Theme) Apply(u ThemeUpdate) (Theme, error) {
	out := Theme{Preset: t.Preset, Colors: map[string]string{}}
	for k, v := range t.Colors {
		out.Colors[k] = v
	}
	if u.Preset != "" {
		if FindThemePreset(u.Preset) == nil {
			return t, fmt.Errorf("no preset %q; the presets are %s", u.Preset, strings.Join(PresetNames(), ", "))
		}
		out.Preset = u.Preset
		out.Colors = map[string]string{}
	}
	keys := make([]string, 0, len(u.Colors))
	for k := range u.Colors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !IsThemeToken(k) {
			return t, fmt.Errorf("no theme token %q; the tokens are %s", k, strings.Join(TokenNames(), ", "))
		}
		v := strings.TrimSpace(u.Colors[k])
		if v == "" {
			delete(out.Colors, k)
			continue
		}
		c, err := NormalizeColor(v)
		if err != nil {
			return t, fmt.Errorf("%s: %v", k, err)
		}
		out.Colors[k] = c
	}
	if out.Preset == "" {
		out.Preset = DefaultThemePreset
	}
	if len(out.Colors) == 0 {
		out.Colors = nil
	}
	return out, nil
}

// Info is the ThemeInfo for t.
func (t Theme) Info() ThemeInfo {
	if t.Preset == "" {
		t.Preset = DefaultThemePreset
	}
	return ThemeInfo{Theme: t, Default: t.IsDefault(), Resolved: t.Resolve(), Tokens: ThemeTokens, Presets: ThemePresets}
}

// PresetNames lists the presets' names.
func PresetNames() []string {
	var out []string
	for _, p := range ThemePresets {
		out = append(out, p.Name)
	}
	return out
}

// TokenNames lists the tokens' names.
func TokenNames() []string {
	var out []string
	for _, t := range ThemeTokens {
		out = append(out, t.Name)
	}
	return out
}

// ParseRGB splits "#rrggbb" into its parts.
func ParseRGB(c string) (r, g, b uint8, ok bool) {
	if len(c) != 7 || c[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(c[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}

// MixColors blends a toward b by t (0 is a, 1 is b). Bad input gives a.
func MixColors(a, b string, t float64) string {
	ar, ag, ab, ok1 := ParseRGB(a)
	br, bg, bb, ok2 := ParseRGB(b)
	if !ok1 || !ok2 {
		return a
	}
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb))
}
