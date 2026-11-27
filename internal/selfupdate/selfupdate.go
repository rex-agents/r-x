// Package selfupdate replaces the running rox or rix with the latest release.
//
// Releases are GitHub releases of rex-agents/r-x, built by goreleaser: one archive per
// OS and architecture holding both binaries, and a checksums.txt of their SHA-256s.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Releases is where releases are published.
var Releases = "https://github.com/rex-agents/r-x/releases"

var releasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

const checkTTL = 6 * time.Hour

// Updater updates one binary.
type Updater struct {
	Program  string // rox or rix
	Version  string // this build's version, such as v1.2.3, or "development"
	StateDir string // where the last check is cached
	// Mode returns auto, notify or off.
	Mode func() string
	// After runs once a new release is in place, such as to refresh the skill.
	After func(tag string)
}

// Released reports whether this binary came from a release, so it can update itself.
func (u *Updater) Released() bool { return releasePattern.MatchString(u.Version) }

func versionKey(v string) [3]int {
	var key [3]int
	for i, part := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		key[i], _ = strconv.Atoi(part)
	}
	return key
}

func newer(a, b string) bool {
	ka, kb := versionKey(a), versionKey(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return ka[i] > kb[i]
		}
	}
	return false
}

func latestRelease() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("HEAD", Releases+"/latest", nil)
	req.Header.Set("User-Agent", "r-x")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	location := resp.Header.Get("Location")
	m := regexp.MustCompile(`/releases/tag/(v[0-9]+\.[0-9]+\.[0-9]+)$`).FindStringSubmatch(location)
	if m == nil {
		return "", fmt.Errorf("unexpected latest release page %.80s", location)
	}
	return m[1], nil
}

// Newer returns a newer release than this one, or "", and whether GitHub was asked just now.
func (u *Updater) Newer(refresh bool) (string, bool, error) {
	if !u.Released() {
		return "", false, nil
	}
	path := filepath.Join(u.StateDir, "update-check.json")
	var cache struct {
		Latest  *string `json:"latest"`
		Checked float64 `json:"checked"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	latest := ""
	if cache.Latest != nil && releasePattern.MatchString(*cache.Latest) {
		latest = *cache.Latest
	}
	now := float64(time.Now().Unix())
	fresh := cache.Checked > 0 && now-cache.Checked >= 0 && now-cache.Checked < checkTTL.Seconds()
	asked := refresh || !fresh
	var err error
	if asked {
		latest, err = latestRelease()
		// A failed check waits for the next one too, so being offline doesn't slow every command.
		record := map[string]any{"latest": nil, "checked": now}
		if latest != "" {
			record["latest"] = latest
		}
		data, _ := sys.MarshalIndent(record)
		_ = sys.WritePrivate(path, string(data))
	}
	if latest != "" && newer(latest, u.Version) {
		return latest, asked, err
	}
	return "", asked, err
}

func archiveName(tag string) string {
	return fmt.Sprintf("r-x_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

func download(url string, w io.Writer) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "r-x")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// blocker says why this binary can't replace itself, or "" when it can.
func blocker(self string) string {
	dir := filepath.Dir(self)
	if syscall.Access(dir, 2) != nil {
		return fmt.Sprintf("Can't write to %s. Reinstall from %s or with your package manager", dir, Releases)
	}
	if strings.Contains(self, "/Cellar/") || strings.Contains(self, "/homebrew/") {
		return "This install is managed by Homebrew. Run brew upgrade r-x"
	}
	return ""
}

// Install downloads tag, checks its SHA-256 against the release's checksums, and swaps this binary for its copy.
func (u *Updater) Install(tag string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if b := blocker(self); b != "" {
		return errors.New(b)
	}
	lock, err := sys.WaitLock(filepath.Join(u.StateDir, "update.lock"))
	if err != nil {
		return err
	}
	defer lock.Unlock()
	name := archiveName(tag)
	var sums strings.Builder
	if err := download(fmt.Sprintf("%s/download/%s/checksums.txt", Releases, tag), &sums); err != nil {
		return err
	}
	expected := ""
	for _, line := range strings.Split(sums.String(), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			expected = strings.ToLower(f[0])
		}
	}
	if expected == "" {
		return fmt.Errorf("The %s release has no %s", tag, name)
	}
	archive, err := os.CreateTemp(filepath.Dir(self), "."+u.Program+"-update-")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	hash := sha256.New()
	if err := download(fmt.Sprintf("%s/download/%s/%s", Releases, tag, name), io.MultiWriter(archive, hash)); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("The %s download doesn't match its published SHA-256, so %s stays at %s", tag, u.Program, u.Version)
	}
	if _, err := archive.Seek(0, 0); err != nil {
		return err
	}
	staged := self + ".new"
	if err := extract(archive, u.Program, staged); err != nil {
		os.Remove(staged)
		return err
	}
	// Rename is atomic, so a running copy keeps its old inode and the next run gets the new one.
	return os.Rename(staged, self)
}

func extract(r io.Reader, program, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("The release archive has no %s binary", program)
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != program || hdr.Typeflag != tar.TypeReg {
			continue
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 512<<20)); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
}

// Auto runs before a command: it installs a newer release and reruns the command with it,
// or says one is out. A failed check or download never stops the command.
func (u *Updater) Auto(argv []string) {
	env := strings.ToUpper(u.Program) + "_UPDATED"
	if os.Getenv(env) != "" || !u.Released() || len(argv) == 0 {
		return
	}
	switch argv[0] {
	case "self-update", "-h", "--help", "--version", "help", "__relay", "hook", "exec", "inspect":
		return
	}
	if len(argv) > 1 && argv[0] == "service" && argv[1] == "run" {
		return
	}
	mode := u.Mode()
	if mode == "off" {
		return
	}
	latest, asked, err := u.Newer(false)
	if err != nil && latest == "" {
		return
	}
	if latest == "" {
		return
	}
	self, _ := os.Executable()
	if b := blocker(self); mode == "notify" || b != "" {
		if asked { // Say so once per check, not before every command.
			hint := b
			if hint == "" {
				hint = "Run " + u.Program + " self-update to install it"
			}
			ui.Info("%s %s is out (this is %s). %s", u.Program, latest, u.Version, hint)
		}
		return
	}
	s := ui.Start("Updating %s from %s to %s", u.Program, u.Version, latest)
	if err := u.Install(latest); err != nil {
		s.Warn("Could not update to %s (%s). Continuing with %s", latest, err, u.Version)
		return
	}
	s.Done("Updated %s to %s", u.Program, latest)
	if u.After != nil {
		u.After(latest)
	}
	os.Setenv(env, latest)
	_ = syscall.Exec(self, append([]string{self}, argv...), os.Environ())
}

// SelfUpdate is the self-update command.
func (u *Updater) SelfUpdate(check bool) error {
	if !u.Released() {
		return fmt.Errorf("This %s is a %s build, so it doesn't update itself. Install a release from %s", u.Program, u.Version, Releases)
	}
	s := ui.Start("Checking for a newer %s", u.Program)
	latest, _, err := u.Newer(true)
	if err != nil {
		s.Fail(err)
		return err
	}
	if latest == "" {
		s.Done("%s %s is the latest release", u.Program, u.Version)
		return nil
	}
	if check {
		s.Done("%s %s is out %s", u.Program, latest, ui.Muted("(this is "+u.Version+")"))
		ui.Hint("Run " + ui.Code(u.Program+" self-update") + " to install it")
		return nil
	}
	s.Title("Updating %s from %s to %s", u.Program, u.Version, latest)
	if err := u.Install(latest); err != nil {
		s.Fail(err)
		return err
	}
	s.Done("Updated %s to %s", u.Program, latest)
	if u.After != nil {
		u.After(latest)
	}
	return nil
}
