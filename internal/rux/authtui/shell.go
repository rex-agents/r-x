package authtui

import (
	"net/url"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rex-agents/r-x/internal/rux/authapi"
)

// Enter on a MONITOR node opens a shell on it: `rox shell` for a guest, the machine's own shell
// for the local host, ssh for another host of the pack. Inside Rex the shell opens in a new pane
// beside the TUI; elsewhere the TUI suspends until it exits.

type shellDoneMsg struct{ err error }

// Stubs for the tests.
var (
	lookEnv  = os.Getenv
	runRex   = func(args []string) error { return exec.Command("rex", args...).Run() }
	execProc = func(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd { return tea.ExecProcess(c, done) }
)

func isLoopback(host string) bool {
	return host == "" || host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
}

// shellArgv is the command that opens a shell on the monitor node n. why explains an empty argv.
func (m *Model) shellArgv(n *node) (argv []string, why string) {
	switch {
	case n == nil:
		return nil, ""
	case n.guest != nil:
		auth, ok := strings.CutSuffix(n.key, "/"+n.guest.Name)
		if !ok {
			return nil, ""
		}
		argv = []string{"rox", "shell"}
		if a := m.authority(auth); a != nil && !m.onThisMachine(a.Name) {
			argv = append(argv, "-S", a.Name)
		}
		// rux starts its guests without a project checkout, so there is no directory to add.
		return append(argv, n.guest.Name), ""
	case n.host != nil:
		if m.onThisMachine(n.host.Name) {
			return []string{cmpOr(lookEnv("SHELL"), "sh")}, ""
		}
		u, err := url.Parse(n.host.URL)
		if err != nil || isLoopback(u.Hostname()) {
			return nil, "no address for " + n.host.Name + " to open a shell on"
		}
		return []string{"ssh", u.Hostname()}, ""
	}
	return nil, ""
}

func (m *Model) authority(name string) *authapi.AuthorityInfo {
	for i := range m.pack.Authorities {
		if a := &m.pack.Authorities[i]; a.Name == name {
			return a
		}
	}
	return nil
}

// onThisMachine reports whether the authority runs on the machine the TUI does: it's the one the
// TUI is attached to, over a loopback address.
func (m *Model) onThisMachine(name string) bool {
	a := m.authority(name)
	if a == nil || !a.Local {
		return false
	}
	u, err := url.Parse(m.c.Base)
	return err == nil && isLoopback(u.Hostname())
}

// shellCmd runs argv: in a new Rex pane beside the TUI when it's inside Rex, else in this terminal.
func (m *Model) shellCmd(argv []string, label string) tea.Cmd {
	if sess, blk := lookEnv("REX_SESSION"), lookEnv("REX_BLOCK"); sess != "" && blk != "" {
		args := append([]string{"split", "-s", sess, "-b", blk, "--split=right", "--label", label, "--"}, argv...)
		return func() tea.Msg { return shellDoneMsg{runRex(args)} }
	}
	return execProc(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { return shellDoneMsg{err} })
}

// shellKey is enter on a MONITOR node.
func (m *Model) shellKey() tea.Cmd {
	n := m.mselected(m.mfocus)
	argv, why := m.shellArgv(n)
	if why != "" {
		m.flash = why
	}
	if len(argv) == 0 {
		return nil
	}
	m.flash = "opening " + strings.Join(argv, " ")
	return m.shellCmd(argv, "shell "+n.name)
}
