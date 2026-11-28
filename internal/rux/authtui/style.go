package authtui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/rex-agents/r-x/internal/rux/transcript"
)

// rux's palette, shared with the agent harness through package transcript. A theme change
// (transcript.ApplyTheme) recolors it: syncPalette copies the new colors here.
var (
	hViolet  = transcript.Violet
	hAccent  = transcript.Accent
	hGreen   = transcript.Green
	hYellow  = transcript.Yellow
	hRed     = transcript.Red
	hDim     = transcript.Dim
	hFaint   = transcript.Faint
	hText    = transcript.Text
	stDim    = transcript.StDim
	stFaint  = transcript.StFaint
	stAccent = transcript.StAccent
	stBold   = transcript.StBold
	stText   = transcript.StText
)

func init() { transcript.OnTheme(syncPalette) }

func syncPalette() {
	hViolet, hAccent, hGreen, hYellow, hRed = transcript.Violet, transcript.Accent, transcript.Green, transcript.Yellow, transcript.Red
	hDim, hFaint, hText = transcript.Dim, transcript.Faint, transcript.Text
	stDim, stFaint, stAccent, stBold, stText = transcript.StDim, transcript.StFaint, transcript.StAccent, transcript.StBold, transcript.StText
	stGit = lipgloss.NewStyle().Foreground(hGreen)
}

func statusColor(s string) lipgloss.Color {
	switch s {
	case "up", "running", "working", "ok":
		return hGreen
	case "idle", "done", "sleeping", "asleep", "lost":
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

// stGit marks a git checkout in the directory browser.
var stGit = lipgloss.NewStyle().Foreground(hGreen)
