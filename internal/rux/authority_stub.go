package rux

import (
	"fmt"
	"os"
	"strings"
)

// ensureAuthority returns the base URL of the local authority, starting it if needed.
//
// INTEGRATION STUB: the real implementation is EnsureAuthority() on branch rux/authority
// (internal/rux/authority.go). When that branch is merged, replace the body with
//
//	return EnsureAuthority()
//
// Until then RUX_AUTHORITY_URL points the TUI at an authority that is already running.
func ensureAuthority() (string, error) {
	if u := strings.TrimSpace(os.Getenv("RUX_AUTHORITY_URL")); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("the authority daemon isn't part of this build yet; set RUX_AUTHORITY_URL to a running authority")
}
