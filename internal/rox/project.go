package rox

import (
	"archive/tar"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

func sortStrings(s []string) { sort.Strings(s) }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Source is the project a launch clones.
type Source struct{ Name, URL, Branch string }

// ProjectArgs are the flags that choose a project.
type ProjectArgs struct {
	Cwd, Project, Remote, Repo, Branch string
	NoProject, Local                   bool
	Host                               string
}

func git(dir string, args ...string) (string, error) {
	return sys.Output(append([]string{"git", "-C", dir}, args...)...)
}

func gitMaybe(dir string, args ...string) string {
	result, _ := sys.Try(sys.Cmd{Argv: append([]string{"git", "-C", dir}, args...)})
	if result.Code != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func inCheckout(cwd string) bool {
	return gitMaybe(cwd, "rev-parse", "--is-inside-work-tree") == "true"
}

var scpLike = regexp.MustCompile(`^(?:[\w.-]+@)?[\w.-]+:[^:\s][^\r\n]*$`)

// ValidateClone checks a branch name and a Git remote rox can clone safely.
func ValidateClone(rawURL, branch string) error {
	if branch != "" && sys.Status("git", "check-ref-format", "refs/heads/"+branch) != 0 {
		return fmt.Errorf("%q is not a valid branch name", branch)
	}
	valid := false
	if u, err := url.Parse(rawURL); err == nil && (u.Scheme == "https" || u.Scheme == "ssh") {
		_, hasPassword := u.User.Password()
		valid = u.Hostname() != "" && !hasPassword && u.RawQuery == "" && u.Fragment == ""
		if u.Scheme == "https" {
			valid = valid && u.User == nil
		}
	} else {
		valid = !strings.Contains(rawURL, "://") && scpLike.MatchString(rawURL)
	}
	for _, r := range rawURL {
		if r < 32 {
			valid = false
		}
	}
	if !valid || strings.HasPrefix(rawURL, "-") {
		return errors.New("Use an SSH or HTTPS Git remote, optionally with --repo")
	}
	return nil
}

func projectFrom(cwd, remote, repo, branch string) (*Source, error) {
	root, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	local := gitMaybe(cwd, "symbolic-ref", "--quiet", "--short", "HEAD")
	upstream := ""
	if local != "" {
		upstream = gitMaybe(cwd, "config", "--get", "branch."+local+".remote")
	}
	selected := remote
	if selected == "" && upstream != "." {
		selected = upstream
	}
	selected = firstNonEmpty(selected, "origin")
	if strings.HasPrefix(selected, "-") {
		return nil, fmt.Errorf("Invalid Git remote %q", selected)
	}
	if branch == "" {
		merge := ""
		if local != "" {
			merge = gitMaybe(cwd, "config", "--get", "branch."+local+".merge")
		}
		branch = local
		if selected == upstream && strings.HasPrefix(merge, "refs/heads/") {
			branch = strings.TrimPrefix(merge, "refs/heads/")
		}
	}
	if branch == "" {
		return nil, errors.New("Choose a remote branch with --branch when starting from a detached HEAD")
	}
	if repo == "" {
		if repo, err = git(cwd, "remote", "get-url", selected); err != nil {
			return nil, err
		}
	}
	if err := ValidateClone(repo, branch); err != nil {
		return nil, err
	}
	return &Source{Name: filepath.Base(root), URL: repo, Branch: branch}, nil
}

// RepoName is the repository's name from its URL.
func RepoName(u string) string {
	parts := regexp.MustCompile(`[/:]`).Split(strings.TrimRight(u, "/"), -1)
	return firstNonEmpty(strings.TrimSuffix(parts[len(parts)-1], ".git"), "project")
}

// ResolveProject returns the launch's project, or nil for a guest without one.
func ResolveProject(a ProjectArgs, required bool) (*Source, error) {
	if a.NoProject {
		if a.Project != "" || a.Repo != "" || a.Remote != "" || a.Branch != "" {
			return nil, errors.New("--no-project can't be combined with --project, --repo, --remote or --branch")
		}
		return nil, nil
	}
	if a.Project != "" {
		// Without --branch, the clone uses the remote's default branch (DefaultBranch).
		if strings.TrimSpace(a.Project) == "" || a.Repo == "" {
			return nil, errors.New("--project needs a name and --repo")
		}
		if err := ValidateClone(a.Repo, a.Branch); err != nil {
			return nil, err
		}
		return &Source{a.Project, a.Repo, a.Branch}, nil
	}
	if inCheckout(a.Cwd) {
		return projectFrom(a.Cwd, a.Remote, a.Repo, a.Branch)
	}
	if a.Repo != "" {
		if err := ValidateClone(a.Repo, a.Branch); err != nil {
			return nil, err
		}
		return &Source{RepoName(a.Repo), a.Repo, a.Branch}, nil
	}
	if required || a.Repo != "" || a.Remote != "" || a.Branch != "" {
		return nil, fmt.Errorf("%s is not inside a Git checkout. Run from a checkout, or pass --repo", a.Cwd)
	}
	return nil, nil
}

// localCheckout is the checkout root --local copies into the guest, or "" without --local.
func localCheckout(a ProjectArgs) (string, error) {
	if !a.Local {
		return "", nil
	}
	if a.Host != "" {
		return "", errors.New("--local copies this machine's checkout, so it can't be combined with --host")
	}
	if a.NoProject || a.Project != "" {
		return "", errors.New("--local copies the current checkout, so it can't be combined with --project or --no-project")
	}
	return checkoutRoot(a.Cwd)
}

func checkoutRoot(cwd string) (string, error) {
	if !inCheckout(cwd) {
		return "", fmt.Errorf("--local needs a Git checkout, and %s is not inside one", cwd)
	}
	root, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if sys.Status("git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}") != 0 {
		return "", fmt.Errorf("--local needs a commit in %s. Commit something first", root)
	}
	return root, nil
}

// localProjectSettings reads the checkout's .rox.local.json, or the copy a remote launch carries.
func localProjectSettings(a ProjectArgs, carried string) (map[string]any, error) {
	if carried != "" {
		var v any
		if err := json.Unmarshal([]byte(carried), &v); err != nil {
			return nil, fmt.Errorf("--local-settings is not valid JSON: %v", err)
		}
		return CheckProjectSettings(v, LocalConfig)
	}
	if a.Project != "" || a.NoProject || !inCheckout(a.Cwd) {
		return map[string]any{}, nil
	}
	root, err := git(a.Cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	settings, err := ReadProjectFile(filepath.Join(root, LocalConfig), LocalConfig)
	if err == nil && len(settings) > 0 {
		ui.Info("Using settings from %s", ui.Code(filepath.Join(root, LocalConfig)))
	}
	return settings, err
}

// Branch is the launched branch as the launch machine read it.
type Branch struct {
	Mirror, Commit, Hook string
	Settings             map[string]any
}

// gitEnv is how the launcher's git reaches a remote: through rix's agent and pins when rix
// lends them, and otherwise through the user's own SSH setup, without prompting.
func gitEnv(lease *Lease, scratch string) (map[string]string, []string, error) {
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_ALLOW_PROTOCOL": gitProtocols}
	if lease == nil {
		env["GIT_SSH_COMMAND"] = "ssh -o BatchMode=yes"
		return env, nil, nil
	}
	ssh := []string{"ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no"}
	if lease.KnownHosts != "" {
		pins := filepath.Join(scratch, "known_hosts")
		if err := os.WriteFile(pins, []byte(lease.KnownHosts), 0o600); err != nil {
			return nil, nil, err
		}
		quoted, _ := json.Marshal(pins)
		ssh = append(ssh, "-o", "UserKnownHostsFile="+string(quoted))
	}
	env["GIT_SSH_COMMAND"] = sys.Quote(ssh...)
	if lease.Socket != "" {
		env["SSH_AUTH_SOCK"] = lease.Socket
		return env, nil, nil
	}
	return env, []string{"SSH_AUTH_SOCK"}, nil
}

// ReadBranch fetches the branch into a cached mirror to find its .rox.sh and .rox.json.
// It returns nil when the launcher can't read it, and the guest then runs .rox.sh at boot.
// Only a broken .rox.json is an error.
func ReadBranch(src *Source, lease *Lease) (*Branch, error) {
	var branch *Branch
	s := ui.Start("Reading %s for .rox.sh and %s", src.Branch, ProjectConfig)
	err := func() error {
		repos := filepath.Join(sys.XDG("XDG_CACHE_HOME", ".cache"), "rox", "repos")
		if err := os.MkdirAll(repos, 0o755); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(src.URL))
		mirror := filepath.Join(repos, hex.EncodeToString(sum[:])[:16]+".git")
		lock, err := sys.WaitLock(mirror + ".lock")
		if err != nil {
			return err
		}
		defer lock.Unlock()
		if !sys.IsFile(filepath.Join(mirror, "HEAD")) {
			if _, err := sys.Output("git", "init", "--quiet", "--bare", mirror); err != nil {
				return err
			}
		}
		scratch, err := os.MkdirTemp("", "rox-git-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(scratch)
		env, unset, err := gitEnv(lease, scratch)
		if err != nil {
			return err
		}
		ref := branchCacheRef(src.Branch)
		if _, err := sys.Run(sys.Cmd{Env: env, Unset: unset, Timeout: 5 * time.Minute, Argv: []string{"git", "-C", mirror,
			"-c", "credential.helper=", "fetch", "--quiet", "--no-tags", "--", src.URL,
			"+refs/heads/" + src.Branch + ":" + ref}}); err != nil {
			return err
		}
		commit, err := git(mirror, "rev-parse", ref+"^{commit}")
		if err != nil {
			return err
		}
		hook := gitMaybe(mirror, "rev-parse", "--verify", "--quiet", commit+":.rox.sh")
		settings := map[string]any{}
		if sys.Status("git", "-C", mirror, "cat-file", "-e", commit+":"+ProjectConfig) == 0 {
			text, err := git(mirror, "show", commit+":"+ProjectConfig)
			if err != nil {
				return err
			}
			var v any
			if err := json.Unmarshal([]byte(text), &v); err != nil {
				return &sys.Fatal{Err: fmt.Errorf("%s on the branch is not valid JSON: %v", ProjectConfig, err)}
			}
			if settings, err = CheckProjectSettings(v, ProjectConfig); err != nil {
				return &sys.Fatal{Err: err}
			}
		}
		branch = &Branch{Mirror: mirror, Commit: commit, Hook: hook, Settings: settings}
		return nil
	}()
	var fatal *sys.Fatal
	if errors.As(err, &fatal) {
		s.Fail(fatal.Err)
		return nil, fatal.Err
	}
	if err != nil {
		s.Warn("Could not read %s here, so .rox.sh runs at boot and %s is ignored", src.Branch, ProjectConfig)
		ui.Detail("%s", firstLine(err.Error()))
		return nil, nil
	}
	found := []string{}
	if branch.Hook != "" {
		found = append(found, ".rox.sh")
	}
	if len(branch.Settings) > 0 {
		found = append(found, ProjectConfig)
	}
	if len(found) == 0 {
		s.Done("Read %s %s", src.Branch, ui.Muted("(no .rox.sh or "+ProjectConfig+")"))
	} else {
		s.Done("Read %s %s", src.Branch, ui.Muted("(found "+strings.Join(found, " and ")+" at "+short(branch.Commit)+")"))
	}
	return branch, nil
}

// branchCacheRef is the mirror's private ref for a branch. Git can't hold refs/rox/a and
// refs/rox/a/b together, so the name is hashed: each branch gets its own flat ref, and
// deleting a branch and re-creating it under a prefix (or the reverse) can't collide.
// Refs from older versions under refs/rox/ are left alone.
func branchCacheRef(branch string) string {
	sum := sha256.Sum256([]byte(branch))
	return "refs/rox-branches/" + hex.EncodeToString(sum[:])
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// gitHostEntries are the launcher's known_hosts lines for the Git server in url.
func gitHostEntries(rawURL string) string {
	knownHosts := filepath.Join(sys.Home(), ".ssh", "known_hosts")
	if !sys.IsFile(knownHosts) {
		return ""
	}
	host := ""
	if u, err := url.Parse(rawURL); err == nil && u.Scheme == "ssh" {
		host = u.Hostname()
		if p := u.Port(); p != "" && p != "22" {
			host = "[" + host + "]:" + p
		}
	} else {
		before, _, _ := strings.Cut(rawURL, ":")
		host = before[strings.LastIndexByte(before, '@')+1:]
	}
	result, _ := sys.Try(sys.Cmd{Argv: []string{"ssh-keygen", "-F", host, "-f", knownHosts}})
	var out strings.Builder
	for _, line := range sys.Lines(result.Stdout) {
		if !strings.HasPrefix(line, "#") {
			out.WriteString(line + "\n")
		}
	}
	return out.String()
}

func isSSHRemote(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return !(err == nil && u.Scheme == "https")
}

// localChanges writes the checkout's uncommitted files to a tar and returns the tracked
// paths it deleted. Gitignored files stay behind, so build output and .env don't travel.
func localChanges(root, archive string) ([]string, int, error) {
	paths := func(args ...string) ([]string, error) {
		out, err := sys.Run(sys.Cmd{Argv: append([]string{"git", "-C", root}, args...)})
		var list []string
		for _, p := range strings.Split(out.Stdout, "\x00") {
			if p != "" {
				list = append(list, p)
			}
		}
		return list, err
	}
	deleted, err := paths("diff", "--name-only", "--no-renames", "-z", "--diff-filter=D", "HEAD", "--")
	if err != nil {
		return nil, 0, err
	}
	changed, err := paths("diff", "--name-only", "--no-renames", "-z", "--diff-filter=d", "HEAD", "--")
	if err != nil {
		return nil, 0, err
	}
	untracked, err := paths("ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Create(archive)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	count := 0
	seen := map[string]bool{}
	for _, p := range append(changed, untracked...) {
		if seen[p] {
			continue
		}
		seen[p] = true
		file := filepath.Join(root, p)
		info, err := os.Lstat(file)
		// A changed submodule shows up as its directory, which a snapshot of files can't carry.
		if err != nil || !(info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			continue
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(file)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil, 0, err
		}
		hdr.Name = p
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, 0, err
		}
		if info.Mode().IsRegular() {
			src, err := os.Open(file)
			if err != nil {
				return nil, 0, err
			}
			_, err = io.Copy(tw, src)
			src.Close()
			if err != nil {
				return nil, 0, err
			}
		}
		count++
	}
	return deleted, count, tw.Close()
}

// sendLocal brings a fresh no-checkout clone in the guest to the launcher's HEAD and uncommitted files.
func sendLocal(g *Guest, workspace, root, branch string) error {
	head, err := git(root, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return err
	}
	// Commits the guest's clone already has, so the bundle only carries what the remote lacks.
	listed, err := guestRun(g, []string{"git", "-C", workspace, "for-each-ref", "--format=%(objectname)"}, "", 60*time.Second)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, c := range strings.Fields(listed) {
		known[c] = true
	}
	var have []string
	if len(known) > 0 {
		var input []string
		for c := range known {
			input = append(input, c)
		}
		sort.Strings(input)
		checked, err := sys.OutputWith(sys.Cmd{Argv: []string{"git", "-C", root, "cat-file", "--batch-check=%(objectname) %(objecttype)"},
			Input: strings.Join(input, "\n") + "\n"})
		if err != nil {
			return err
		}
		for _, line := range sys.Lines(checked) {
			if strings.HasSuffix(line, " commit") {
				have = append(have, strings.Fields(line)[0])
			}
		}
	}
	payload := path.Join(workspace, ".git", "rox-local")
	scratch, err := os.MkdirTemp("", "rox-local-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	uploads := map[string]string{}
	var excluded strings.Builder
	for _, c := range have {
		excluded.WriteString("^" + c + "\n")
	}
	countOut, err := sys.OutputWith(sys.Cmd{Argv: []string{"git", "-C", root, "rev-list", "--count", "--stdin", head}, Input: excluded.String()})
	if err != nil {
		return err
	}
	missing, _ := strconv.Atoi(countOut)
	if missing > 0 {
		bundle := filepath.Join(scratch, "commits.bundle")
		if _, err := sys.Run(sys.Cmd{Argv: []string{"git", "-C", root, "bundle", "create", "--quiet", bundle, "HEAD", "--stdin"}, Input: excluded.String()}); err != nil {
			return err
		}
		uploads["commits.bundle"] = bundle
	}
	deleted, count, err := localChanges(root, filepath.Join(scratch, "files.tar"))
	if err != nil {
		return err
	}
	if count > 0 {
		uploads["files.tar"] = filepath.Join(scratch, "files.tar")
	}
	if len(deleted) > 0 {
		var list strings.Builder
		for _, p := range deleted {
			list.WriteString(p + "\x00")
		}
		_ = os.WriteFile(filepath.Join(scratch, "deleted"), []byte(list.String()), 0o600)
		uploads["deleted"] = filepath.Join(scratch, "deleted")
	}
	for _, name := range sortedKeysString(uploads) {
		f, err := os.Open(uploads[name])
		if err != nil {
			return err
		}
		_, err = sys.Run(sys.Cmd{Argv: remoteCommand(g, []string{"bash", "-c", `umask 077; mkdir -p "${1%/*}" && cat > "$1"`, "rox", payload + "/" + name}, "", false), Stdin: f})
		f.Close()
		if err != nil {
			return err
		}
	}
	if _, err := guestStage(g, "local", stageOpts{}, workspace, payload, branch, head); err != nil {
		return err
	}
	ui.Detail("%s, %s and %s from %s", ui.Plural(missing, "unpushed commit", "unpushed commits"),
		ui.Plural(count, "changed file", "changed files"), ui.Plural(len(deleted), "deletion", "deletions"), root)
	return nil
}

// remoteKey is a Git remote as host/path, so SSH and HTTPS forms of one repository compare equal.
func remoteKey(raw string) string {
	raw = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(raw), "/"), ".git")
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Hostname() != "" {
		return strings.ToLower(u.Hostname()) + "/" + strings.Trim(u.Path, "/")
	}
	host, p, ok := strings.Cut(raw, ":")
	if !ok {
		return raw
	}
	return strings.ToLower(host[strings.LastIndexByte(host, '@')+1:]) + "/" + strings.Trim(p, "/")
}

// checkoutIdentity is what identifies the checkout at cwd in guests: its names and remotes.
type checkoutIdentity struct {
	Names   map[string]bool
	Remotes map[string]bool
	Root    string
}

func currentCheckout(cwd string) *checkoutIdentity {
	if !inCheckout(cwd) {
		return nil
	}
	root, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}
	id := &checkoutIdentity{Names: map[string]bool{filepath.Base(root): true}, Remotes: map[string]bool{}, Root: root}
	for _, line := range sys.Lines(gitMaybe(root, "config", "--get-regexp", `^remote\..*\.url$`)) {
		if _, u, ok := strings.Cut(line, " "); ok {
			id.Names[RepoName(u)] = true
			id.Remotes[remoteKey(u)] = true
		}
	}
	return id
}

// matchingCheckout is the id of an entry's checkout of the wanted project or of the current checkout.
func matchingCheckout(checkouts []checkoutRef, wanted string, current *checkoutIdentity) string {
	if wanted != "" {
		for _, c := range checkouts {
			if c.ID == wanted || c.Name == wanted {
				return c.ID
			}
		}
		return ""
	}
	if current == nil {
		return ""
	}
	// A remote match is surest; a name match also covers older records, which carry no URL.
	for _, c := range checkouts {
		if c.URL != "" && current.Remotes[remoteKey(c.URL)] {
			return c.ID
		}
	}
	for _, c := range checkouts {
		if current.Names[c.ID] || current.Names[c.Name] {
			return c.ID
		}
	}
	return ""
}

type checkoutRef struct{ ID, Name, URL string }

var githubRepoURL = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// DefaultBranch fills in src.Branch, when it's empty, with the remote's default branch. It
// asks the remote with git ls-remote, through rix's SSH lease for SSH remotes, and for a
// GitHub repo Git can't reach falls back to the GitHub API with rix's token.
func DefaultBranch(src *Source, lease *Lease) error {
	if src == nil || src.Branch != "" {
		return nil
	}
	branch, err := lsRemoteHead(src.URL, lease)
	if err != nil || branch == "" {
		if gh, ghErr := githubDefaultBranch(src.URL); ghErr == nil && gh != "" {
			branch, err = gh, nil
		}
	}
	if err != nil || branch == "" {
		why := "it has no default branch"
		if err != nil {
			why = firstLine(err.Error())
		}
		return fmt.Errorf("Couldn't find %s's default branch (%s). Pass --branch", src.URL, why)
	}
	src.Branch = branch
	return nil
}

func lsRemoteHead(url string, lease *Lease) (string, error) {
	scratch, err := os.MkdirTemp("", "rox-git-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	env, unset, err := gitEnv(lease, scratch)
	if err != nil {
		return "", err
	}
	res, err := sys.Run(sys.Cmd{Env: env, Unset: unset, Timeout: time.Minute,
		Argv: []string{"git", "ls-remote", "--symref", "--", url, "HEAD"}})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			if name, _, ok := strings.Cut(ref, "\t"); ok {
				return name, nil
			}
		}
	}
	return "", nil
}

// githubDefaultBranch asks GitHub for a repo's default branch, with the token rix hands out.
func githubDefaultBranch(url string) (string, error) {
	m := githubRepoURL.FindStringSubmatch(url)
	if m == nil {
		return "", errors.New("not a GitHub repo")
	}
	if _, err := sys.R.LookPath("rix"); err != nil {
		return "", err
	}
	out, err := sys.Output("rix", "credential", "github", "--json")
	if err != nil {
		return "", err
	}
	var cred struct{ Password string }
	if err := json.Unmarshal([]byte(out), &cred); err != nil || cred.Password == "" {
		return "", errors.New("rix has no GitHub token")
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+m[1]+"/"+m[2], nil)
	req.Header.Set("Authorization", "Bearer "+cred.Password)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		return "", err
	}
	return repo.DefaultBranch, nil
}

// gitProtocols are the transports launcher-side Git may use. Tests widen it to read a local fixture.
var gitProtocols = "ssh:https"
