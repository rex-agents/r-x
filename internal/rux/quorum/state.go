package quorum

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// State is ~/.rux/quorum.json (0600): this authority's identity in the pack and its bearer
// credential. Seed lets a joiner authenticate the leader before the config reaches it.
type State struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Token     string   `json:"token"`
	LeaderURL string   `json:"leader_url,omitempty"` // where this authority joined; a hint only
	Bootstrap bool     `json:"bootstrap,omitempty"`  // created the pack, raft not yet bootstrapped
	Seed      []Member `json:"seed,omitempty"`
}

func statePath(dir string) string { return filepath.Join(dir, "quorum.json") }

func loadState(dir string) (*State, error) {
	data, err := os.ReadFile(statePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveState(dir string, s *State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp, err := os.CreateTemp(dir, ".quorum-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), statePath(dir))
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func tokenMatches(hash, token string) bool {
	return subtle.ConstantTimeCompare([]byte(hash), []byte(hashToken(token))) == 1
}
