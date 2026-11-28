!term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd())package rux

import (
	"os"

	"github.com/charmbracelet/x/term"

	"github.com/rex-agents/r-x/internal/rux/authapi"
	"github.com/rex-agents/r-x/internal/rux/authtui"
)

// The harness's lead goes by the name the user gave their authority, and draws that authority's
// mascot. Both are read from the authority's data directory (authority.json and mascot.json), so
// a rename shows in the next session.

// loadPersona reads the authority's name ("" when it has none) and mascot (nil for the T-rex).
func loadPersona(dir string) (name string, mascot *authapi.Mascot) {
	return loadAuthorityConfig(dir).Name, loadMascot(dir)
}

// leadName is what the lead is called: the authority's name, else "lead".
func (r *hrun) leadName() string { return cmpOr(r.name, "lead") }

// welcomeName is the name on the welcome screen: the authority's, else "rux".
func (r *hrun) welcomeName() string { return cmpOr(r.name, "rux") }

// leadIdentity is the first thing the lead's system prompt says: who it is.
func (r *hrun) leadIdentity() string {
	if r.name == "" {
		return "You are the lead agent of this user's rux."
	}
	return "You are " + r.name + ", this user's rux authority and lead agent. Your name is " + r.name +
		": introduce yourself by it, refer to yourself by it, and answer to it. Sub-agents know you by it too."
}

// firstRunSetup shows the first-run screen (name the authority, describe its mascot) before the
// harness opens, when the authority has no name yet. It never blocks a run without a person at
// the keyboard: no terminal, or the authority can't start, and the harness just opens.
func firstRunSetup() {
	if name, _ := loadPersona(authorityHome()); name != "" || os.Getenv("RUX_NO_SETUP") != "" ||
		!isTTY(os.Stdin) || !isTTY(os.Stdout) {
		return
	}
	url, err := ensureAuthority()
	if err != nil {
		return
	}
	_, _, _ = authtui.RunSetup(url, authtui.Options{Logo: trexLogo, Mascot: mascotLogo})
}

// authorityLine tells a thread whose authority it works for.
func authorityLine(name string) string {
	return "You work for " + name + ", this user's rux authority. If asked who runs you, say so."
}
