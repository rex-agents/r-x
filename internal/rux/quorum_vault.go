package rux

import (
	"context"
	"errors"
	"fmt"

	"github.com/rex-agents/r-x/internal/rux/quorum"
)

// leaderVault is the VaultSource named "leader": model credentials from the pack's vault
// host, over the quorum link. Register it with SetVaultSource("leader", ...) and the pack's
// view with SetPackSource(quorum.NewPackAdapter(node)).
type leaderVault struct {
	node   *quorum.Node
	thread string // audit label when the proxy doesn't say which thread asks
}

// NewLeaderVault returns the "leader" VaultSource for a quorum node.
func NewLeaderVault(n *quorum.Node) VaultSource { return &leaderVault{node: n, thread: "proxy"} }

func (v *leaderVault) Credential(ctx context.Context, provider string) (Credential, error) {
	var names []string
	switch provider {
	case "anthropic":
		names = []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
	case "openai":
		names = []string{"OPENAI_API_KEY"}
	default:
		return Credential{}, fmt.Errorf("unknown provider %q", provider)
	}
	var last error
	for _, n := range names {
		s, err := v.node.Vault().Get(ctx, v.thread, n)
		if err == nil && s.Value != "" {
			return credentialFor(s.Value), nil
		}
		// Unreachable or denied end the search: trying another name would hit the same wall.
		if err != nil && !isMissing(err) {
			return Credential{}, err
		}
		last = err
	}
	return Credential{}, fmt.Errorf("the vault host has none of %v: %w", names, last)
}

func isMissing(err error) bool { return errors.Is(err, quorum.ErrSecretMissing) }
