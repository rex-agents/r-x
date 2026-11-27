package rux

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Where a thread runs: a new guest from one of rox's runtimes, or an existing rox guest.
//
// Local runtimes (Apple container, Tart VMs) reach the runner listener on the host's own
// network. Fly.io and Railway guests reach it over the tailnet, so rux starts them with
// Tailscale and tells their runner the host's tailnet name.

var runtimes = map[string]bool{"container": true, "linux": true, "macos": true, "fly": true, "railway": true}

var guestNameOK = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func remoteRuntime(r string) bool { return r == "fly" || r == "railway" }

// Tailnet ranges, which guests on Fly or Railway connect from.
var tailnetCIDRs = []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}

var (
	tailnetOnce sync.Once
	tailnetName string
)

// hostTailnetName is this Mac's MagicDNS name, or "" without Tailscale.
func hostTailnetName() string {
	tailnetOnce.Do(func() {
		var st struct {
			Self struct {
				DNSName      string
				TailscaleIPs []string
			}
		}
		if runJSON(5*time.Second, &st, tailscaleBin(), "status", "--json") != nil {
			return
		}
		tailnetName = strings.TrimSuffix(st.Self.DNSName, ".")
		if tailnetName == "" && len(st.Self.TailscaleIPs) > 0 {
			tailnetName = st.Self.TailscaleIPs[0]
		}
	})
	return tailnetName
}

// advertiseFor is the host:port a guest of runtime uses to reach the runner listener.
func (s *server) advertiseFor(runtime string) string {
	if remoteRuntime(runtime) {
		if name := hostTailnetName(); name != "" {
			_, port, _ := net.SplitHostPort(s.advertise)
			return net.JoinHostPort(name, port)
		}
	}
	return s.advertise
}

// allowTailnet lets guests on the tailnet reach the runner listener. Every request
// there still needs its thread's token, except the runner binary download.
func (s *server) allowTailnet() {
	if hostTailnetName() == "" {
		return
	}
	for _, c := range tailnetCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			s.guestNets = append(s.guestNets, n)
		}
	}
}

// provisionShared runs a thread's runner in an existing guest, in a folder of its own.
func (s *server) provisionShared(t *thread, m threadMeta) {
	fail := func(text string) {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.appendLocked("rux", 0, ruxEv("rux_error", "text", text))
		t.setStatusLocked("failed")
	}
	t.event(ruxEv("rux_guest", "name", m.Guest))
	var record struct {
		Phase   string `json:"phase"`
		Backend string `json:"backend"`
	}
	if err := runJSON(30*time.Second, &record, s.roxBin, "inspect", m.Guest, "--json"); err != nil {
		fail(fmt.Sprintf("couldn't find guest %s: %v", m.Guest, err))
		return
	}
	if record.Phase != "ready" {
		if err := s.runLogged(t, s.roxBin, []string{"start", m.Guest}, nil); err != nil {
			fail(fmt.Sprintf("couldn't start guest %s: %v", m.Guest, err))
			return
		}
	}
	agent, agentName := s.agentOf(m.Model), "Claude Code"
	if agent == "codex" {
		agentName = "Codex"
	}
	agentBin := "command -v " + agent
	if out, err := exec.Command(s.roxBin, "exec", m.Guest, "--", "sh", "-lc", agentBin).CombinedOutput(); err != nil {
		fail(fmt.Sprintf("%s has no %s (%s). Pick a guest started with agents %s, or start a new one.",
			m.Guest, agentName, outputLine(string(out), err), agent))
		return
	}
	script := `set -a; . "$HOME/.rox/agents.env" 2>/dev/null || true; set +a; ` +
		`mkdir -p "$HOME/.rux"; nohup bash -lc ` + shQuote(s.runnerCommand(&m, s.advertiseFor(record.Backend))) +
		` >>"$HOME/.rux/runner-` + m.ID + `.log" 2>&1 </dev/null &`
	t.event(ruxEv("rux_setup", "text", "starting the runner in "+m.Guest))
	if out, err := exec.Command(s.roxBin, "exec", m.Guest, "--", "sh", "-c", script).CombinedOutput(); err != nil {
		fail("couldn't start the runner: " + outputLine(string(out), err))
		return
	}
	t.mu.Lock()
	if !t.meta.Connected {
		t.setStatusLocked("starting")
	}
	t.mu.Unlock()
	s.watchConnect(t)
}

// stopSharedRunner ends a thread's runner, and so its claude, leaving the guest running.
func (s *server) stopSharedRunner(t *thread) {
	m := t.view()
	if m.Guest == "" {
		return
	}
	pattern := "[r]ux-runner runner .*--thread " + m.ID + "( |$)"
	out, err := exec.Command(s.roxBin, "exec", m.Guest, "--", "pkill", "-f", pattern).CombinedOutput()
	if err != nil && !strings.Contains(err.Error(), "exit status 1") { // 1: nothing matched
		t.event(ruxEv("rux_error", "text", "couldn't stop the runner in "+m.Guest+": "+outputLine(string(out), err)))
		return
	}
	t.event(ruxEv("rux_setup", "text", "stopped this thread's runner; "+m.Guest+" keeps running"))
}
