package transcript

import (
	"sync"

	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// ChipBg is the background of key chips and the selected tab.
var ChipBg = lipgloss.Color("237")

// builtin is the palette as compiled in, for going back to the default theme: the terminal look
// predates themes and stays exactly as it was until someone picks one.
var builtin = struct{ orange, pink, violet, green, yellow, red, dim, faint, text, chip lipgloss.Color }{
	Orange, Pink, Violet, Green, Yellow, Red, Dim, Faint, Text, ChipBg,
}

var (
	themeMu   sync.Mutex
	themeHook []func()
)

// OnTheme registers fn to run after every ApplyTheme, for packages that derive their own styles
// from the palette.
func OnTheme(fn func()) {
	themeMu.Lock()
	themeHook = append(themeHook, fn)
	themeMu.Unlock()
}

// ApplyTheme recolors the palette from a theme (GET /api/theme). The default theme restores the
// built-in terminal colors. The terminal's own background stays: the palette only sets text colors.
func ApplyTheme(info authapi.ThemeInfo) {
	if info.Default || info.Resolved == nil {
		Orange, Pink, Violet, Green, Yellow, Red = builtin.orange, builtin.pink, builtin.violet, builtin.green, builtin.yellow, builtin.red
		Dim, Faint, Text, ChipBg = builtin.dim, builtin.faint, builtin.text, builtin.chip
	} else {
		c := func(k string) lipgloss.Color { return lipgloss.Color(info.Resolved[k]) }
		Pink, Violet, Green, Yellow, Red = c("accent"), c("accent2"), c("ok"), c("warn"), c("err")
		Orange = c("warn")
		Text, Dim = c("text"), c("muted")
		// Faint draws hints and box borders, so it sits between the hint color and the rules.
		Faint = lipgloss.Color(authapi.MixColors(info.Resolved["faint"], info.Resolved["line"], 0.4))
		ChipBg = c("raise")
		if p := authapi.FindThemePreset(info.Preset); p != nil && p.Light {
			// A light theme's text is dark, which a dark terminal can't show: keep its accents
			// and use the built-in greys for text.
			Text, Dim, Faint, ChipBg = builtin.text, builtin.dim, builtin.faint, builtin.chip
		}
	}
	Accent = Pink
	StDim = lipgloss.NewStyle().Foreground(Dim)
	StFaint = lipgloss.NewStyle().Foreground(Faint)
	StAccent = lipgloss.NewStyle().Foreground(Accent)
	StBold = lipgloss.NewStyle().Bold(true).Foreground(Text)
	StText = lipgloss.NewStyle().Foreground(Text)
	themeMu.Lock()
	hooks := append([]func(){}, themeHook...)
	themeMu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}
