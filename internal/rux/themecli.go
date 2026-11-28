package rux

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/transcript"
)

const themeUsage = `usage: rux theme                          the current theme, token by token
       rux theme presets                  the built-in themes
       rux theme use PRESET [TOKEN=#hex]  switch to a preset (overrides are dropped, except those given)
       rux theme set TOKEN=#hex...        override tokens on the current preset (TOKEN= drops one)
       rux theme reset                    back to the default theme
       rux theme --json                   the theme as JSON (GET /api/theme)

The theme colors the web app and the terminal UIs, on every screen at once. It works on the
authority's host and from inside a thread, so an agent can change it. Tokens:
`

// runTheme is `rux theme`.
func runTheme(args []string) {
	usage := func() {
		fmt.Fprint(os.Stderr, themeUsage)
		for _, t := range authapi.ThemeTokens {
			fmt.Fprintf(os.Stderr, "  %-8s %s\n", t.Name, t.Description)
		}
		os.Exit(2)
	}
	cmd := "show"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var info authapi.ThemeInfo
	call := func(method string, in any) {
		if err := callAuthority(method, "/api/theme", in, &info); err != nil {
			fmt.Fprintln(os.Stderr, "rux theme:", err)
			os.Exit(1)
		}
	}
	colors := func(args []string) map[string]string {
		out := map[string]string{}
		for _, a := range args {
			k, v, ok := strings.Cut(a, "=")
			if !ok {
				fmt.Fprintf(os.Stderr, "rux theme: %q isn't TOKEN=#hex\n", a)
				usage()
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		return out
	}
	switch cmd {
	case "show":
		call("GET", nil)
		printTheme(info)
	case "--json", "json":
		call("GET", nil)
		b, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(b))
	case "presets":
		call("GET", nil)
		for _, p := range info.Presets {
			mark := "  "
			if p.Name == info.Preset {
				mark = transcript.StAccent.Render("▸ ")
			}
			fmt.Printf("%s%-8s %s  %s\n", mark, p.Name, swatches(p.Colors), transcript.StDim.Render(p.Description))
		}
	case "use":
		if len(args) < 1 || strings.Contains(args[0], "=") {
			usage()
		}
		call("PUT", authapi.ThemeUpdate{Preset: args[0], Colors: colors(args[1:])})
		fmt.Printf("%s Theme: %s. Open screens repaint now.\n", transcript.StAccent.Render("◆"), info.Preset)
		printTheme(info)
	case "set":
		if len(args) == 0 {
			usage()
		}
		call("PUT", authapi.ThemeUpdate{Colors: colors(args)})
		fmt.Printf("%s Theme updated. Open screens repaint now.\n", transcript.StAccent.Render("◆"))
		printTheme(info)
	case "reset":
		call("DELETE", nil)
		fmt.Printf("%s Back to the default theme.\n", transcript.StAccent.Render("◆"))
	default:
		usage()
	}
}

func printTheme(info authapi.ThemeInfo) {
	title := info.Preset
	if len(info.Colors) > 0 {
		title += fmt.Sprintf(" with %d override(s)", len(info.Colors))
	}
	fmt.Println(transcript.StBold.Render(title))
	for _, t := range info.Tokens {
		c := info.Resolved[t.Name]
		mark := " "
		if _, ok := info.Colors[t.Name]; ok {
			mark = transcript.StAccent.Render("*")
		}
		fmt.Printf("  %s %s %-8s %s  %s\n", swatch(c), mark, t.Name, c, transcript.StDim.Render(t.Description))
	}
	if len(info.Colors) > 0 {
		fmt.Println(transcript.StFaint.Render("  * overrides the preset"))
	}
}

func swatch(c string) string { return lipgloss.NewStyle().Background(lipgloss.Color(c)).Render("   ") }

func swatches(cs map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"bg", "panel", "text", "accent", "accent2", "ok", "warn", "err"} {
		b.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(cs[k])).Render(" "))
	}
	return b.String()
}
