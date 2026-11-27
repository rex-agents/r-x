package rux

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Runners keep running across `rux serve` upgrades, so a guest can be on an
// old runner (and claude on an old system prompt). When a runner connects with
// a different version than the binary the server serves, it's upgraded in
// place: it downloads the new binary and re-execs itself, and claude restarts
// with --resume, so the conversation carries over.

// fileVersion is a short hash of a binary.
func fileVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func selfVersion() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return fileVersion(exe)
}

// upgrade (runner side) waits for claude to finish its turn, replaces this
// binary with the server's, and re-execs with the same arguments.
func (r *runner) upgrade() {
	if !r.upgrading.CompareAndSwap(false, true) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		r.upgrading.Store(false)
		return
	}
	tmp := exe + ".new"
	if err := download(r.httpBase+"/dl/rux-runner?os="+runtime.GOOS+"&arch="+runtime.GOARCH, tmp); err != nil {
		r.emit(ruxEv("rux_error", "text", "runner upgrade: "+err.Error()))
		r.upgrading.Store(false)
		return
	}
	r.procMu.Lock()
	if r.cmd != nil {
		// Let claude finish its turn: with stdin closed it exits afterwards.
		r.stdin.Close()
		r.retiring = r.cmd
		done := r.done
		r.procMu.Unlock()
		<-done
		r.procMu.Lock()
	}
	if err := os.Rename(tmp, exe); err != nil {
		r.procMu.Unlock()
		r.emit(ruxEv("rux_error", "text", "runner upgrade: "+err.Error()))
		r.upgrading.Store(false)
		return
	}
	r.emit(ruxEv("rux_setup", "text", "runner upgraded; claude resumes with the new version on the next message"))
	r.logf.Sync()
	time.Sleep(300 * time.Millisecond) // let the event go out
	log.Printf("runner: upgraded, re-executing")
	err = syscall.Exec(exe, os.Args, os.Environ())
	r.procMu.Unlock()
	r.emit(ruxEv("rux_error", "text", "runner upgrade: exec: "+err.Error()))
	r.upgrading.Store(false)
}

func download(url, path string) error {
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// maybeUpgrade (server side) is called when a runner says hello.
func (s *server) maybeUpgrade(t *thread, version string) {
	if versions := s.runnerVersions(); len(versions) == 0 || versions[version] {
		return
	}
	t.mu.Lock()
	m := t.meta
	tried := t.upgradeTried
	t.upgradeTried = true
	t.mu.Unlock()
	if tried {
		return // once per server run, so a failing upgrade doesn't loop
	}
	if version != "" {
		t.mu.Lock()
		if t.send != nil {
			select {
			case t.send <- wireMsg{T: "upgrade"}:
			default:
			}
		}
		t.mu.Unlock()
		return
	}
	// Runners from before upgrades existed don't report a version or know the
	// upgrade message: replace them from outside, through the container.
	if m.Backend != "rox" || m.Guest == "" {
		return
	}
	go s.upgradeLegacy(t, m)
}

func (s *server) upgradeLegacy(t *thread, m threadMeta) {
	// Replacing the runner from outside kills claude, so wait for it to be idle.
	for deadline := time.Now().Add(6 * time.Hour); ; time.Sleep(10 * time.Second) {
		v := t.view()
		if v.Status == "destroyed" || v.Status == "destroying" || time.Now().After(deadline) {
			return
		}
		if v.Connected && v.Status == "idle" && time.Since(v.Updated) > 5*time.Second {
			break
		}
	}
	cmd := s.runnerCommand(&m, s.advertiseFor(m.Runtime))
	// runnerCommand downloads the runner and execs it; run it detached instead
	// of the old one. [r]ux keeps pkill from matching this shell itself.
	script := `set -e; pkill -f '[r]ux-runner runner --server' || true; sleep 1; ` +
		`set -a; . "$HOME/.rox/agents.env" 2>/dev/null || true; set +a; ` +
		`mkdir -p "$HOME/.rux"; nohup bash -lc ` + shQuote(cmd) + ` >>"$HOME/.rux/runner.log" 2>&1 </dev/null &`
	t.event(ruxEv("rux_setup", "text", "upgrading this guest's runner (claude resumes on the next message)"))
	out, err := exec.Command(s.roxBin, "exec", m.Guest, "--", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.event(ruxEv("rux_error", "text", fmt.Sprintf("runner upgrade failed: %v %s", err, strings.TrimSpace(string(out)))))
	}
}
