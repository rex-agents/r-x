package rux

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// GitHub access comes from rix (rix credential github): the token rix stores, or gh's
// login on this machine. The repo picker lists what that account can see, and guests fetch
// the same token on demand through a git credential helper (rux git-credential), so it's
// never written into a guest's disk, config or command line.
type github struct {
	off bool

	mu       sync.Mutex
	token    string
	tokenAt  time.Time
	ident    *ghIdent
	repos    []ghRepo
	reposAt  time.Time
	reposErr error
	loading  chan struct{} // closed when the in-flight fetch finishes
}

type ghIdent struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type ghRepo struct {
	Name          string `json:"name"` // owner/name
	Private       bool   `json:"private"`
	Pushed        string `json:"pushed"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description,omitempty"`
}

// hostGitIdentity is the user's global git user.name and user.email on this machine,
// empty when either is unset. It's a variable so tests can replace it.
var hostGitIdentity = func() (name, email string) {
	get := func(key string) string {
		out, err := exec.Command("git", "config", "--global", "--get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}

func (g *github) enabled() bool { return g != nil && !g.off }

var ghClient = &http.Client{Timeout: 30 * time.Second}

// api GETs a GitHub REST path (or a full URL) with token, decoding JSON into v, and
// returns the next page's URL from the Link header.
func ghAPI(token, path string, v any) (string, error) {
	u := path
	if !strings.HasPrefix(u, "https://") {
		u = "https://api.github.com/" + strings.TrimPrefix(path, "/")
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := ghClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct{ Message string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return "", fmt.Errorf("GitHub %s: %s", strings.SplitN(strings.TrimPrefix(path, "/"), "?", 2)[0], e.Message)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return "", fmt.Errorf("GitHub: %w", err)
	}
	return nextLink(resp.Header.Get("Link")), nil
}

func nextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		if url, rel, ok := strings.Cut(part, ";"); ok && strings.Contains(rel, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(url), "<>")
		}
	}
	return ""
}

// credential returns the token from rix (asked again every few minutes, in case it
// changed or gh refreshed it) and the commit identity for that account.
func (g *github) credential() (string, *ghIdent, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token == "" || time.Since(g.tokenAt) > 5*time.Minute {
		c, err := rixGitHub()
		if err != nil {
			return "", nil, err
		}
		if c.Password != g.token {
			g.ident = nil
		}
		g.token, g.tokenAt = c.Password, time.Now()
	}
	if g.ident == nil {
		var u struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Name  string `json:"name"`
		}
		if _, err := ghAPI(g.token, "user", &u); err != nil {
			return "", nil, err
		}
		name := u.Name
		if name == "" {
			name = u.Login
		}
		g.ident = &ghIdent{Login: u.Login, Name: name, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", u.ID, u.Login)}
	}
	return g.token, g.ident, nil
}

// listRepos returns the cached repo list, refreshing it in the background once
// it's a few minutes old. The first call waits for the fetch.
func (g *github) listRepos() ([]ghRepo, error) {
	g.mu.Lock()
	if time.Since(g.reposAt) > 3*time.Minute && g.loading == nil {
		done := make(chan struct{})
		g.loading = done
		go func() { g.fetchRepos(); close(done) }()
	}
	if wait := g.loading; g.repos == nil && wait != nil {
		g.mu.Unlock()
		<-wait
		g.mu.Lock()
	}
	defer g.mu.Unlock()
	return g.repos, g.reposErr
}

func (g *github) fetchRepos() {
	repos, err := func() ([]ghRepo, error) {
		token, _, err := g.credential()
		if err != nil {
			return nil, err
		}
		repos := []ghRepo{}
		next := "user/repos?sort=pushed&per_page=100&affiliation=owner,collaborator,organization_member"
		for page := 0; next != "" && page < 50; page++ {
			var batch []struct {
				FullName      string `json:"full_name"`
				Private       bool   `json:"private"`
				PushedAt      string `json:"pushed_at"`
				DefaultBranch string `json:"default_branch"`
				Description   string `json:"description"`
			}
			if next, err = ghAPI(token, next, &batch); err != nil {
				return nil, err
			}
			for _, r := range batch {
				repos = append(repos, ghRepo{Name: r.FullName, Private: r.Private, Pushed: r.PushedAt,
					DefaultBranch: r.DefaultBranch, Description: r.Description})
			}
		}
		return repos, nil
	}()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loading = nil
	if err != nil {
		log.Printf("listing GitHub repos: %v", err)
		g.reposErr = err
		if g.repos == nil {
			g.reposAt = time.Time{}
		}
		return
	}
	g.repos, g.reposAt, g.reposErr = repos, time.Now(), nil
}

func (s *server) handleRepos(w http.ResponseWriter, r *http.Request) {
	if !s.gh.enabled() {
		writeJSONResp(w, 200, map[string]any{"enabled": false, "repos": []ghRepo{}})
		return
	}
	repos, err := s.gh.listRepos()
	resp := map[string]any{"enabled": true, "repos": repos}
	if repos == nil {
		resp["repos"] = []ghRepo{}
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSONResp(w, 200, resp)
}

// handleGitCredential serves the runner's git credential helper. It's on the
// runner listener and needs the thread's token, like /runner.
func (s *server) handleGitCredential(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.URL.Query().Get("thread"))
	if t == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	t.mu.Lock()
	tok, gone := t.meta.Token, t.meta.Status == "destroyed"
	t.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if gone || subtle.ConstantTimeCompare([]byte(auth), []byte(tok)) != 1 {
		http.Error(w, "forbidden", 403)
		return
	}
	if !s.gh.enabled() {
		http.Error(w, "GitHub access is off (rux serve --github=false)", 404)
		return
	}
	token, ident, err := s.gh.credential()
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	name, email := ident.Name, ident.Email
	if n, e := hostGitIdentity(); n != "" && e != "" {
		name, email = n, e // the user's own git config beats the GitHub-derived identity
	}
	writeJSONResp(w, 200, map[string]any{"username": "x-access-token", "password": token,
		"name": name, "email": email, "login": ident.Login})
}
