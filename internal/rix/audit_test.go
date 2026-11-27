package rix

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

// Codex sign-out.

func codexGuest() *contract.Guest {
	return &contract.Guest{Name: "rv-web-0123456789ab", Agents: map[string]string{"codex": "1"}}
}

func TestDisabledCodexCredentialIsReconciled(t *testing.T) {
	dir := isolate(t)
	t.Setenv("ROX_BIN", "/bin/rox")
	g := codexGuest()
	for _, tc := range []struct {
		name   string
		config map[string]any
		owned  bool
	}{
		{"off after auth file", map[string]any{}, true},
		{"off after api key", map[string]any{"agents": map[string]any{"codex": map[string]any{}}}, true},
		{"guest older state", map[string]any{}, false},
	} {
		writeConfig(t, tc.config)
		_ = saveState(g.Name, &GuestState{Credentials: map[bool][]string{true: {"codex"}, false: nil}[tc.owned]})
		fake := sys.NewFake()
		restore := fake.Install()
		c, _ := LoadConfig("")
		if err := PushCredentials(c, g, &Outcome{}); err != nil {
			t.Fatal(tc.name, err)
		}
		restore()
		calls := fake.Ran("rix codex-forget")
		if len(calls) != 1 || strings.HasSuffix(calls[0].Line(), " owned") != tc.owned {
			t.Errorf("%s: calls %+v", tc.name, fake.Calls)
		}
		if s, _ := loadState(g.Name); containsString(s.Credentials, "codex") {
			t.Errorf("%s: state still lists codex", tc.name)
		}
	}
	// A guest without Codex that rix never signed in is left alone.
	_ = saveState(g.Name, &GuestState{})
	fake := sys.NewFake()
	defer fake.Install()()
	c, _ := LoadConfig("")
	_ = PushCredentials(c, &contract.Guest{Name: g.Name, Agents: map[string]string{}}, &Outcome{})
	if len(fake.Ran("rix codex-forget")) != 0 {
		t.Error("touched a guest rix never signed in")
	}
	// A failed sign-out keeps the record, so a retry still knows rix owns the login.
	_ = saveState(g.Name, &GuestState{Credentials: []string{"codex"}})
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "rix codex-forget") {
			return sys.Result{Code: 1}
		}
		return sys.Result{}
	}
	if err := PushCredentials(c, g, &Outcome{}); err == nil {
		t.Error("a failed sign-out was ignored")
	}
	if s, _ := loadState(g.Name); !containsString(s.Credentials, "codex") {
		t.Error("state forgot codex before the guest was signed out")
	}
	_ = dir
}

func TestConfiguredCodexIsNotForgotten(t *testing.T) {
	dir := isolate(t)
	t.Setenv("ROX_BIN", "/bin/rox")
	writeConfig(t, map[string]any{"agents": map[string]any{"codex": map[string]any{"api_key_file": secret(t, dir, "oai", "K")}}})
	_ = saveState("rv-web-0123456789ab", &GuestState{Credentials: []string{"codex"}})
	fake := sys.NewFake()
	defer fake.Install()()
	c, _ := LoadConfig("")
	if err := PushCredentials(c, codexGuest(), &Outcome{}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Ran("rix codex-forget")) != 0 || len(fake.Ran("rix codex-login")) != 1 {
		t.Errorf("calls %+v", fake.Calls)
	}
}

// guestSh runs one action of the guest script for real, with HOME in a temp folder.
func guestSh(t *testing.T, home string, stdin []byte, action string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(shellForTests(), append([]string{"-c", assets.RixGuest(), "rix", action}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestGuestCodexForgetKeepsOwnLogin(t *testing.T) {
	home := t.TempDir()
	auth := filepath.Join(home, ".codex", "auth.json")
	_ = os.MkdirAll(filepath.Dir(auth), 0o700)
	_ = os.WriteFile(auth, []byte("guest's own"), 0o600)
	if out, err := guestSh(t, home, nil, "codex-forget"); err != nil || !sys.IsFile(auth) {
		t.Fatalf("removed a login rix didn't write: %v %s", err, out)
	}
	if out, err := guestSh(t, home, []byte("{}"), "codex-auth"); err != nil {
		t.Fatal(err, out)
	}
	if out, err := guestSh(t, home, nil, "codex-forget"); err != nil || sys.IsFile(auth) {
		t.Fatalf("kept rix's login: %v %s", err, out)
	}
	// Older state, no marker: the launcher's record says rix wrote it.
	_ = os.WriteFile(auth, []byte("old rix"), 0o600)
	if _, err := guestSh(t, home, nil, "codex-forget", "owned"); err != nil || sys.IsFile(auth) {
		t.Fatalf("kept an older rix login: %v", err)
	}
}

// Git agent lifecycle.

type agentProcess struct{ killed bool }

func (p *agentProcess) Pid() int            { return 4242 }
func (p *agentProcess) Exited() (bool, int) { return false, 0 }
func (p *agentProcess) Kill()               { p.killed = true }

type agentRunner struct {
	*sys.Fake
	process *agentProcess
	sock    net.Listener
}

func (r *agentRunner) Detach(c sys.Cmd, _ string) (sys.Process, error) {
	r.Fake.Detach(c, "")
	l, err := net.Listen("unix", c.Argv[len(c.Argv)-1])
	if err != nil {
		return nil, err
	}
	r.sock = l
	return r.process, nil
}

func TestFailedKeyLoadStopsTheAgentItStarted(t *testing.T) {
	isolate(t)
	runner := &agentRunner{Fake: sys.NewFake(), process: &agentProcess{}}
	runner.Handle = func(c sys.Call) sys.Result {
		switch {
		case strings.Contains(c.Line(), "ssh-add -l"):
			if runner.sock == nil {
				return sys.Result{Code: 2}
			}
			return sys.Result{Code: 1}
		case strings.HasPrefix(c.Line(), "ssh-add"):
			return sys.Result{Code: 1, Stderr: "Bad passphrase"}
		}
		return sys.Result{}
	}
	previous := sys.R
	sys.R = runner
	defer func() { sys.R = previous }()
	sock := agentSocket("rv-web-0123456789ab")
	pid, err := startAgent("rv-web-0123456789ab", sock, []string{"/k"}, true)
	if runner.sock != nil {
		defer runner.sock.Close()
	}
	if err == nil || pid != 0 || !runner.process.killed {
		t.Errorf("pid %d err %v killed %v", pid, err, runner.process.killed)
	}
	if sys.IsSocket(sock) && runner.sock == nil {
		t.Error("socket left")
	}
}

func psFake(start, command string) *sys.Fake {
	f := sys.NewFake()
	f.Handle = func(c sys.Call) sys.Result {
		switch {
		case strings.Contains(c.Line(), "lstart="):
			if start == "" {
				return sys.Result{Code: 1}
			}
			return sys.Result{Stdout: start + "\n"}
		case strings.Contains(c.Line(), "command="):
			return sys.Result{Stdout: command + "\n"}
		}
		return sys.Result{}
	}
	return f
}

func TestStopAgentChecksIdentity(t *testing.T) {
	isolate(t)
	name := "rv-web-0123456789ab"
	sock := agentSocket(name)
	var signaled []int
	signalProcess = func(pid int, _ syscall.Signal) error { signaled = append(signaled, pid); return nil }
	defer func() { signalProcess = syscall.Kill }()
	agent := "ssh-agent -D -a " + sock
	for _, tc := range []struct {
		name         string
		start        string
		ps           *sys.Fake
		wantSignaled bool
	}{
		{"same process", "Mon Jan 1 10:00:00 2026", psFake("Mon Jan  1 10:00:00 2026", agent), true},
		{"reused pid", "Mon Jan 1 10:00:00 2026", psFake("Tue Feb  2 11:00:00 2026", agent), false},
		{"reused pid, other program", "Mon Jan 1 10:00:00 2026", psFake("Mon Jan  1 10:00:00 2026", "vim notes"), false},
		{"legacy record, our agent", "", psFake("Mon Jan  1 10:00:00 2026", agent), true},
		{"legacy record, reused pid", "", psFake("Mon Jan  1 10:00:00 2026", "sleep 100"), false},
		{"legacy record, another guest's agent", "", psFake("Mon Jan  1 10:00:00 2026", "ssh-agent -D -a /run/other.sock"), false},
		{"process gone", "x", psFake("", ""), false},
	} {
		signaled = nil
		restore := tc.ps.Install()
		state := &GuestState{AgentPid: 777, AgentStart: tc.start}
		stopAgent(name, state)
		restore()
		if (len(signaled) == 1) != tc.wantSignaled || state.AgentPid != 0 || state.AgentStart != "" {
			t.Errorf("%s: signaled %v state %+v", tc.name, signaled, state)
		}
	}
}

func TestRecordAgentStoresStartTime(t *testing.T) {
	defer psFake("Mon Jan  1 10:00:00 2026", "ssh-agent").Install()()
	state := &GuestState{}
	recordAgent(state, 55)
	if state.AgentPid != 55 || state.AgentStart != "Mon Jan 1 10:00:00 2026" {
		t.Errorf("%+v", state)
	}
}

// Sync, run through the real guest script.

func syncTar(t *testing.T, files map[string]string, complete bool) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	add := func(name, data string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0o644, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(data))
	}
	for n, d := range files {
		add(n, d)
	}
	if complete {
		add(".rix-complete", "")
	}
	_ = tw.Close()
	return b.Bytes()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

func seed(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for n, d := range files {
		p := filepath.Join(home, n)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuestSyncSwapsAndRemovesStaleAfterwards(t *testing.T) {
	home := t.TempDir()
	seed(t, home, map[string]string{".claude/skills/a/SKILL.md": "old a", ".claude/skills/a/gone.md": "x",
		".claude/skills/stale/SKILL.md": "stale", ".claude/settings.json": "{\"mine\":1}", ".claude/guest-only": "keep"})
	tarball := syncTar(t, map[string]string{".claude/skills/a/SKILL.md": "new a", ".claude/skills/b/SKILL.md": "new b"}, true)
	out, err := guestSh(t, home, tarball, "sync", ".claude/skills/a", ".claude/skills/b", "--", ".claude/skills/stale", ".claude/settings.json")
	if err != nil {
		t.Fatal(err, out)
	}
	if readFile(t, home+"/.claude/skills/a/SKILL.md") != "new a" || sys.IsFile(home+"/.claude/skills/a/gone.md") ||
		readFile(t, home+"/.claude/skills/b/SKILL.md") != "new b" || sys.IsFile(home+"/.claude/skills/stale/SKILL.md") ||
		sys.IsFile(home+"/.claude/settings.json") || readFile(t, home+"/.claude/guest-only") != "keep" {
		t.Errorf("tree wrong")
	}
	for _, leftover := range []string{".rox/sync-work", ".rox/sync.lock"} {
		if _, err := os.Stat(filepath.Join(home, leftover)); err == nil {
			t.Errorf("left %s", leftover)
		}
	}
}

func TestGuestSyncKeepsPriorSetupWhenTransferIsBad(t *testing.T) {
	prior := map[string]string{".claude/skills/a/SKILL.md": "old a", ".claude/skills/stale/SKILL.md": "stale", ".claude/CLAUDE.md": "old"}
	good := map[string]string{".claude/skills/a/SKILL.md": "new a", ".claude/CLAUDE.md": "new"}
	full := syncTar(t, good, true)
	for _, tc := range []struct {
		name  string
		input []byte
		args  []string
	}{
		{"no end marker", syncTar(t, good, false), nil},
		{"cut short", full[:len(full)/2], nil},
		{"empty", nil, nil},
		{"missing path", syncTar(t, map[string]string{".claude/CLAUDE.md": "new"}, true), []string{".claude/skills/a", ".claude/CLAUDE.md", "--", ".claude/skills/stale"}},
		{"unsafe install path", full, []string{"../escape", "--"}},
		{"unsafe stale path", full, []string{".claude/CLAUDE.md", "--", "/etc/passwd"}},
	} {
		home := t.TempDir()
		seed(t, home, prior)
		args := tc.args
		if args == nil {
			args = []string{".claude/skills/a", ".claude/CLAUDE.md", "--", ".claude/skills/stale"}
		}
		if out, err := guestSh(t, home, tc.input, "sync", args...); err == nil {
			t.Errorf("%s: sync succeeded: %s", tc.name, out)
		}
		for n, d := range prior {
			if readFile(t, filepath.Join(home, n)) != d {
				t.Errorf("%s: lost %s", tc.name, n)
			}
		}
	}
}

func TestGuestSyncRollsBackAFailedSwap(t *testing.T) {
	home := t.TempDir()
	seed(t, home, map[string]string{".claude/CLAUDE.md": "old", "blocker": "a file where a folder must go"})
	// The second path can't be installed: its parent is a file. The first must be put back.
	tarball := syncTar(t, map[string]string{".claude/CLAUDE.md": "new", "blocker/x": "n"}, true)
	if out, err := guestSh(t, home, tarball, "sync", ".claude/CLAUDE.md", "blocker/x", "--", ".claude/old"); err == nil {
		t.Fatalf("sync succeeded: %s", out)
	}
	if readFile(t, home+"/.claude/CLAUDE.md") != "old" || readFile(t, home+"/blocker") != "a file where a folder must go" {
		t.Error("prior setup not restored")
	}
}

func TestGuestSyncRecoversFromAKilledSwap(t *testing.T) {
	home := t.TempDir()
	// What a sync killed mid-swap leaves: one path moved aside, one not yet touched.
	seed(t, home, map[string]string{".claude/CLAUDE.md": "new half", "AGENTS.md": "untouched",
		".rox/sync-work/undo/data/.claude/CLAUDE.md": "old", ".rox/sync.lock/pid": "999999"})
	seed(t, home, map[string]string{".rox/sync-work/undo/plan": "E .claude/CLAUDE.md\nE AGENTS.md\nA new/file\n", "new/file": "half"})
	tarball := syncTar(t, map[string]string{"x": "1"}, true)
	// The next sync restores first (here it then fails, so the restore is what's left).
	if _, err := guestSh(t, home, tarball, "sync", "x", "--"); err != nil {
		t.Fatal(err)
	}
	if readFile(t, home+"/.claude/CLAUDE.md") != "old" || readFile(t, home+"/AGENTS.md") != "untouched" || sys.IsFile(home+"/new/file") || readFile(t, home+"/x") != "1" {
		t.Error("recovery wrong")
	}
}

func TestSyncSetupSendsInstallThenStale(t *testing.T) {
	dir := isolate(t)
	t.Setenv("ROX_BIN", "/bin/rox")
	_ = os.MkdirAll(filepath.Join(dir, ".claude", "skills", "review"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".claude", "skills", "review", "SKILL.md"), []byte("x"), 0o644)
	record := `{"paths": [".claude/skills/review", ".claude/skills/dropped"]}`
	fake := sys.NewFake()
	defer fake.Install()()
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "rix read") {
			return sys.Result{Stdout: ".rox/synced.json " + b64(record) + "\n"}
		}
		return sys.Result{}
	}
	c, _ := LoadConfig("")
	g := &contract.Guest{Name: "rv-web-0123456789ab", Agents: map[string]string{"claude": "1"}}
	if err := SyncSetup(c, g, &Outcome{}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Ran("rix sync")
	if len(calls) != 1 {
		t.Fatalf("calls %+v", fake.Calls)
	}
	line := calls[0].Line()
	if !strings.HasSuffix(line, ".claude/skills/review .rox/synced.json -- .claude/skills/dropped") {
		t.Errorf("args %s", line)
	}
	tr := tar.NewReader(strings.NewReader(calls[0].Stdin))
	last := ""
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		last = h.Name
	}
	if last != ".rix-complete" {
		t.Errorf("last entry %q", last)
	}
}

func TestAuthKeepsOldSecretWhenReplacementFails(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "config", "rix")
	_ = os.MkdirAll(cfg, 0o700)
	old := filepath.Join(cfg, "anthropic-api-key")
	_ = os.WriteFile(old, []byte("sk-ant-api03-old\n"), 0o600)
	writeConfig(t, map[string]any{"agents": map[string]any{"claude": map[string]any{"api_key_file": old}}})
	// The new token can't be saved, because its file's name is taken by a folder.
	_ = os.Mkdir(filepath.Join(cfg, "claude-token"), 0o700)
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("sk-ant-oat01-new\n")
	w.Close()
	stdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = stdin }()
	if err := Auth("claude", false, "", false); err == nil {
		t.Fatal("expected saving the new token to fail")
	}
	if !sys.IsFile(old) {
		t.Error("the old secret was deleted before the replacement was saved")
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// shellForTests prefers dash, the strictest common /bin/sh, so the script stays portable.
func shellForTests() string {
	if p, err := exec.LookPath("dash"); err == nil {
		return p
	}
	return "sh"
}

func TestGuestSyncKeepsBackupsWhenRollbackFails(t *testing.T) {
	home := t.TempDir()
	// A sync died mid-swap; one path can't be put back yet because its folder is read-only.
	seed(t, home, map[string]string{"d/x": "new half", ".rox/sync-work/undo/data/d/x": "the only prior copy",
		".rox/sync-work/undo/plan": "E d/x\n"})
	_ = os.Chmod(filepath.Join(home, "d"), 0o500)
	defer os.Chmod(filepath.Join(home, "d"), 0o700)
	tarball := syncTar(t, map[string]string{"y": "1"}, true)
	if out, err := guestSh(t, home, tarball, "sync", "y", "--"); err == nil {
		t.Fatalf("sync went on over an unrestored path: %s", out)
	}
	if readFile(t, home+"/.rox/sync-work/undo/data/d/x") != "the only prior copy" || !sys.IsFile(home+"/.rox/sync-work/undo/plan") {
		t.Fatal("the backup or journal was removed")
	}
	if sys.IsFile(home + "/y") {
		t.Error("installed over unrestored state")
	}
	// Once the folder is writable again, the next run restores and syncs.
	_ = os.Chmod(filepath.Join(home, "d"), 0o700)
	if out, err := guestSh(t, home, tarball, "sync", "y", "--"); err != nil {
		t.Fatal(err, out)
	}
	if readFile(t, home+"/d/x") != "the only prior copy" || readFile(t, home+"/y") != "1" {
		t.Error("recovery wrong")
	}
}

func TestGuestSyncUndoesInstallsWhenStaleRemovalFails(t *testing.T) {
	home := t.TempDir()
	seed(t, home, map[string]string{".claude/CLAUDE.md": "old", "old/dir/f": "stale"})
	_ = os.Chmod(filepath.Join(home, "old/dir"), 0o500)
	defer os.Chmod(filepath.Join(home, "old/dir"), 0o700)
	tarball := syncTar(t, map[string]string{".claude/CLAUDE.md": "new"}, true)
	if out, err := guestSh(t, home, tarball, "sync", ".claude/CLAUDE.md", "--", "old/dir"); err == nil {
		t.Fatalf("sync succeeded: %s", out)
	}
	if readFile(t, home+"/.claude/CLAUDE.md") != "old" {
		t.Error("install not undone after a failure past the swap")
	}
}

func TestGuestSyncLocks(t *testing.T) {
	tarball := syncTar(t, map[string]string{"x": "1"}, true)
	age := func(path string, d time.Duration) {
		if d == 0 {
			return
		}
		stamp := time.Now().Add(-d).Format("200601021504.05")
		if out, err := exec.Command("touch", "-h", "-t", stamp, path).CombinedOutput(); err != nil {
			t.Skip("touch -h unsupported:", string(out))
		}
	}
	// A lock as this rix makes it: one symlink, whole from the moment it exists.
	link := func(t *testing.T, pid string, d time.Duration) string {
		home := t.TempDir()
		_ = os.MkdirAll(filepath.Join(home, ".rox"), 0o755)
		lock := filepath.Join(home, ".rox/sync.lock")
		_ = os.Symlink(pid+":1", lock)
		age(lock, d)
		return home
	}
	// A lock an older rix made: a folder, whose pid file may not be written yet.
	folder := func(t *testing.T, pid string, d time.Duration) string {
		home := t.TempDir()
		seed(t, home, map[string]string{".rox/sync.lock/pid": pid})
		if pid == "" {
			_ = os.Remove(filepath.Join(home, ".rox/sync.lock/pid"))
		}
		age(filepath.Join(home, ".rox/sync.lock"), d)
		return home
	}
	me := strconv.Itoa(os.Getpid())
	// A live or still-initializing lock is never removed, and nothing is changed under it.
	for name, home := range map[string]string{
		"live owner":               link(t, me, 0),
		"live legacy owner":        folder(t, me, 0),
		"initializing legacy lock": folder(t, "", 0),
	} {
		if _, err := guestSh(t, home, tarball, "sync", "x", "--"); err == nil || sys.IsFile(home+"/x") {
			t.Errorf("%s: ran beside a live sync", name)
		}
		if _, err := os.Lstat(filepath.Join(home, ".rox/sync.lock")); err != nil {
			t.Errorf("%s: removed a live lock", name)
		}
		if _, err := os.Stat(filepath.Join(home, ".rox/sync-work")); err == nil {
			t.Errorf("%s: used the staging folder", name)
		}
	}
	// A dead owner, a lock too old to be a running sync (a PID reused after a restart), and
	// an old lock that never got an owner all give way.
	for name, home := range map[string]string{
		"dead owner":        link(t, "999999", 0),
		"old lock":          link(t, me, time.Hour),
		"dead legacy owner": folder(t, "999999", 0),
		"old ownerless":     folder(t, "", time.Hour),
	} {
		if out, err := guestSh(t, home, tarball, "sync", "x", "--"); err != nil || readFile(t, home+"/x") != "1" {
			t.Errorf("%s: %v %s", name, err, out)
		}
		if _, err := os.Lstat(filepath.Join(home, ".rox/sync.lock")); err == nil {
			t.Errorf("%s: lock left behind", name)
		}
	}
}

func TestGuestSyncStaleLockKeepsTheJournalWhenRollbackFails(t *testing.T) {
	home := t.TempDir()
	seed(t, home, map[string]string{"d/x": "half", ".rox/sync-work/undo/data/d/x": "prior", ".rox/sync-work/undo/plan": "E d/x\n"})
	_ = os.Symlink("999999:1", filepath.Join(home, ".rox/sync.lock"))
	_ = os.Chmod(filepath.Join(home, "d"), 0o500)
	defer os.Chmod(filepath.Join(home, "d"), 0o700)
	if _, err := guestSh(t, home, syncTar(t, map[string]string{"y": "1"}, true), "sync", "y", "--"); err == nil {
		t.Fatal("sync went on")
	}
	if readFile(t, home+"/.rox/sync-work/undo/data/d/x") != "prior" || !sys.IsFile(home+"/.rox/sync-work/undo/plan") {
		t.Error("journal or backup lost")
	}
	if _, err := os.Lstat(filepath.Join(home, ".rox/sync.lock")); err == nil {
		t.Error("lock held forever")
	}
}

func TestGuestSyncCompetingRunsNeverOverlap(t *testing.T) {
	home := t.TempDir()
	seed(t, home, map[string]string{"x": "0"})
	tarball := syncTar(t, map[string]string{"x": "1"}, true)
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() { _, err := guestSh(t, home, tarball, "sync", "x", "--"); results <- err }()
	}
	ok := 0
	for i := 0; i < 6; i++ {
		if <-results == nil {
			ok++
		}
	}
	if ok == 0 || readFile(t, home+"/x") != "1" {
		t.Errorf("%d syncs succeeded, x=%q", ok, readFile(t, home+"/x"))
	}
	for _, left := range []string{".rox/sync.lock", ".rox/sync-work"} {
		if _, err := os.Stat(filepath.Join(home, left)); err == nil {
			t.Errorf("left %s", left)
		}
	}
}

func TestGuestSyncReplacesASymlinkWithoutTouchingItsTarget(t *testing.T) {
	home := t.TempDir()
	seed(t, home, map[string]string{"shared/SKILL.md": "someone else's"})
	_ = os.MkdirAll(filepath.Join(home, ".claude/skills"), 0o755)
	_ = os.Symlink(filepath.Join(home, "shared"), filepath.Join(home, ".claude/skills/a"))
	tarball := syncTar(t, map[string]string{".claude/skills/a/SKILL.md": "new"}, true)
	if out, err := guestSh(t, home, tarball, "sync", ".claude/skills/a", "--"); err != nil {
		t.Fatal(err, out)
	}
	if readFile(t, home+"/shared/SKILL.md") != "someone else's" || readFile(t, home+"/.claude/skills/a/SKILL.md") != "new" {
		t.Error("symlink handling wrong")
	}
}

func TestAuthCleanupRunsUnderTheConfigLock(t *testing.T) {
	isolate(t)
	path := ConfigLocation("")
	other := make(chan struct{})
	err := UpdateThen(path, func(map[string]any) error { return nil }, func() {
		go func() { _ = Update(path, func(map[string]any) error { return nil }); close(other) }()
		select {
		case <-other:
			t.Error("another command got the config before the cleanup finished")
		case <-time.After(200 * time.Millisecond):
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-other
}

func TestLegacyAgentOwnershipIsExact(t *testing.T) {
	isolate(t)
	name := "rv-web-0123456789ab"
	sock := agentSocket(name)
	var signaled int
	signalProcess = func(int, syscall.Signal) error { signaled++; return nil }
	defer func() { signalProcess = syscall.Kill }()
	for command, want := range map[string]bool{
		"ssh-agent -D -a " + sock:            true,
		"/usr/bin/ssh-agent -D -a " + sock:   true,
		"vim ssh-agent -D -a " + sock:        false,
		"ssh-agent -D -a " + sock + "2":      false,
		"ssh-agent -D -a " + sock + " extra": false,
		"sh -c echo ssh-agent -D -a " + sock: false,
		"not-ssh-agent -D -a " + sock:        false,
		"ssh-agent -D -a /other/agent.sock":  false,
		"ssh-agent -a " + sock:               false,
	} {
		signaled = 0
		restore := psFake("Mon Jan 1 10:00:00 2026", command).Install()
		stopAgent(name, &GuestState{AgentPid: 9})
		restore()
		if (signaled == 1) != want {
			t.Errorf("%q: signaled %d, want %v", command, signaled, want)
		}
	}
}
