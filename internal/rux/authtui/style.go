package authtui

import "github.com/charmbracelet/lipgloss"

// rux's palette: the same colors as internal/rux/harness_render.go (hOrange, hPink, hViolet,
// hGreen, hYellow, hRed, hDim, hFaint, hText). authtui can't import package rux, so they're
// repeated here; keep them in step.
var (
	hOrange  = lipgloss.Color("#ff9a3c")
	hPink    = lipgloss.Color("#f472b6")
	hViolet  = lipgloss.Color("#a78bfa")
	hAccent  = hPink
	hGreen   = lipgloss.Color("#4ade80")
	hYellow  = lipgloss.Color("#facc15")
	hRed     = lipgloss.Color("#f87171")
	hDim     = lipgloss.Color("244")
	hFaint   = lipgloss.Color("239")
	hText    = lipgloss.Color("252")
	stDim    = lipgloss.NewStyle().Foreground(hDim)
	stFaint  = lipgloss.NewStyle().Foreground(hFaint)
	stAccent = lipgloss.NewStyle().Foreground(hAccent)
	stBold   = lipgloss.NewStyle().Bold(true).Foreground(hText)
	stText   = lipgloss.NewStyle().Foreground(hText)
)

func statusColor(s string) lipgloss.Color {
	switch s {
	case "up", "running", "working", "ok":
		return hGreen
	case "idle", "done", "sleeping", "asleep":
		return hDim
	case "failed", "down", "error", "destroyed":
		return hRed
	case "provisioning", "starting", "joining", "blocked", "destroying":
		return hYellow
	}
	return hDim
}

func dot(status string) string {
	return lipgloss.NewStyle().Foreground(statusColor(status)).Render("●")
}
