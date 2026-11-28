package rix

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

// GitIdentity is the name and email agents in a guest commit as.
type GitIdentity struct {
	Name   string
	Email  string
	Source string // "git config" or "GitHub"
}

// hostGitConfig reads one value of the user's global Git config on this machine.
func hostGitConfig(key string) string {
	result, err := sys.Try(sys.Cmd{Argv: []string{"git", "config", "--global", "--get", key}, Timeout: 30 * time.Second})
	if err != nil || result.Code != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// githubUser asks GitHub who a token belongs to. Tests replace it.
var githubUser = func(token string) (id int64, login, name string, err error) {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, "", "", fmt.Errorf("GitHub user: %s", resp.Status)
	}
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return 0, "", "", err
	}
	return u.ID, u.Login, u.Name, nil
}

// ResolveGitIdentity picks the identity for guests: the user's own global Git config first
// (both name and email), else the one behind rix's GitHub credential (the same name and
// noreply address rux uses). It returns nil when neither is available.
func ResolveGitIdentity(c *Config) *GitIdentity {
	if name, email := hostGitConfig("user.name"), hostGitConfig("user.email"); name != "" && email != "" {
		return &GitIdentity{Name: name, Email: email, Source: "git config"}
	}
	cred, err := c.GitHub()
	if err != nil || cred == nil || cred.Password == "" {
		return nil
	}
	id, login, name, err := githubUser(cred.Password)
	if err != nil || login == "" {
		return nil
	}
	if name == "" {
		name = login
	}
	return &GitIdentity{Name: name, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", id, login), Source: "GitHub"}
}

// PushGitIdentity sets the guest user's global git user.name and user.email, so every agent
// and shell in it commits as the host user. The values travel in base64 on stdin. The guest
// keeps an identity its user set themselves: rix only replaces its own earlier one.
func PushGitIdentity(c *Config, g *contract.Guest, out *Outcome) error {
	id := ResolveGitIdentity(c)
	if id == nil {
		out.warn("%s gets no Git identity: set one with git config --global user.name and user.email, or sign in to GitHub with rix auth github. Until then its commits have no author", g.Name)
		return nil
	}
	if strings.ContainsAny(id.Name+id.Email, "\n\r") {
		return errors.New("The Git name or email has a line break")
	}
	enc := base64.StdEncoding
	input := enc.EncodeToString([]byte(id.Name)) + "\n" + enc.EncodeToString([]byte(id.Email)) + "\n"
	if _, err := guestScript(g.Name, input, "git-identity"); err != nil {
		return err
	}
	out.done("Set the Git identity %s <%s> (%s)", id.Name, id.Email, id.Source)
	return nil
}
