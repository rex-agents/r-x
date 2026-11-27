package rux

import (
	"fmt"
	"os"

	"github.com/rex-agents/r-x/internal/rux/authtui"
)

// runAuthorityTUI is bare `rux`: make sure the local authority runs, then open the authority
// TUI on it. The TUI is only a client of the authority's HTTP API.
func runAuthorityTUI() {
	url, err := ensureAuthority()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rux: %v\n", err)
		os.Exit(1)
	}
	if err := authtui.Run(url, authtui.Options{Logo: trexLogo}); err != nil {
		fmt.Fprintf(os.Stderr, "rux: %v\n", err)
		os.Exit(1)
	}
}
