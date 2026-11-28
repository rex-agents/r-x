package rux

import (
	"os"

	"golang.org/x/term"

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

// authorityLine tells a thread whose authority it works for.
func authorityLine(name string) string {
	return "You work for " + name + ", this user's rux authority. If asked who runs you, say so."
}

// needsSetup says whether to show the first-run screen: the authority has no name yet, and a
// person is at the keyboard. A run without a terminal (a script, a guest's runner, --headless)
// never waits on it.
func needsSetup(dir string, interactive bool) bool {
	name, _ := loadPersona(dir)
	return name == "" && interactive && os.Getenv("RUX_NO_SETUP") == ""
}

// firstRunSetup shows the first-run screen (name the authority, describe its mascot) before the
// harness opens. If the authority can't start, the harness just opens.
func firstRunSetup() {
	if !needsSetup(authorityHome(), term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))) {
		return
	}
	url, err := ensureAuthority()
	if err != nil {
		return
	}
	_, _, _ = authtui.RunSetup(url, authtui.Options{Logo: trexLogo, Mascot: mascotLogo})
}

// leadRef is the lead as a sentence refers to it: its name, else "the lead".
func (r *hrun) leadRef() string { return cmpOr(r.name, "the lead") }

// leadSubject is who a sub-agent's prompt says gave it its task.
func (r *hrun) leadSubject() string {
	if r.name == "" {
		return "The lead agent"
	}
	return r.name + " (the lead agent)"
}
