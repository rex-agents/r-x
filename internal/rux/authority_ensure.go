package rux

import (
	"os"
	"strings"
)

// ensureAuthority returns the base URL of the local authority, starting it if needed.
// RUX_AUTHORITY_URL attaches to an authority that is already running elsewhere instead.
func ensureAuthority() (string, error) {
	if u := strings.TrimSpace(os.Getenv("RUX_AUTHORITY_URL")); u != "" {
		return u, nil
	}
	return EnsureAuthority()
}
