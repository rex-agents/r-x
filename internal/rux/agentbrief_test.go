package rux

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSubAgentBriefSaysIdentityIsSet(t *testing.T) {
	cmd := agentCommand(&subAgent{Name: "x", Agent: "claude", Prompt: "p"})
	// The brief travels as base64 inside printf %s.
	for _, f := range strings.Fields(cmd) {
		if d, err := base64.StdEncoding.DecodeString(f); err == nil && strings.Contains(string(d), "git identity is already set") {
			return
		}
	}
	t.Errorf("brief lacks the identity line: %s", cmd)
}
