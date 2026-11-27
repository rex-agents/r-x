package rox

import (
	"fmt"
	"os"
	"time"

	"github.com/rex-agents/r-x/internal/sys"
)

// HostTransport is how rox reaches rox on another host: to list its guests, run a rox command
// there, and open a shell, exec or port forward into one of its guests. rox ships one, SSH, and
// can run a program that speaks the same three commands instead (see commandTransport), so
// the rux authority can become a transport later without rox knowing about it.
//
// A host is whatever the transport understands: an SSH destination for SSH.
type HostTransport interface {
	// Name is "ssh" or the program's name.
	Name() string
	// Hosts lists the hosts that rox ls --all asks.
	Hosts() ([]string, error)
	// Capture runs `rox ARGV...` on host without a terminal and returns its exit status and
	// combined output. A wait of zero means no limit.
	Capture(host string, argv []string, wait time.Duration) (int, string, error)
	// Command returns the argv that runs `rox ARGV...` on host with this process's terminal and
	// stdio, so shells, exec and forwards pass through. The caller runs it.
	Command(host string, o CommandOptions, argv []string) ([]string, error)
}

// CommandOptions shape HostTransport.Command.
type CommandOptions struct {
	// TTY asks for a terminal on the host.
	TTY bool
	// Forwards are {launcher port, host port} pairs to forward from this machine to the host
	// while the command runs.
	Forwards [][2]int
}

// remoteRox runs rox on another host with the release install's PATH, which a non-login shell may lack.
var remoteRox = []string{"sh", "-c", `PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; exec rox "$@"`, "rox"}

// sshTransport runs rox on the host over SSH, with the user's own ssh config, keys and agent,
// so a tailnet name, a Tailscale SSH host or an ssh_config alias all work as the host.
type sshTransport struct{ hosts []string }

func (sshTransport) Name() string { return "ssh" }

func (t sshTransport) Hosts() ([]string, error) { return t.hosts, nil }

func (sshTransport) argv(host string, o CommandOptions, wait time.Duration, rox []string) []string {
	mode := "-T"
	if o.TTY {
		mode = "-t"
	}
	argv := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=8", mode}
	for _, f := range o.Forwards {
		argv = append(argv, "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", f[0], f[1]))
	}
	return append(argv, "--", host, sys.Quote(append(append([]string{}, remoteRox...), rox...)...))
}

func (t sshTransport) Capture(host string, argv []string, wait time.Duration) (int, string, error) {
	result, err := sys.Try(sys.Cmd{Argv: t.argv(host, CommandOptions{}, wait, argv), Timeout: wait})
	if err != nil {
		return 0, "", err
	}
	if result.Code == 255 {
		return 255, result.Output(), fmt.Errorf("Could not reach %s over SSH: %s", host, tail(result.Output(), 200))
	}
	return result.Code, result.Output(), nil
}

func (t sshTransport) Command(host string, o CommandOptions, argv []string) ([]string, error) {
	return t.argv(host, o, 0, argv), nil
}

// commandTransport delegates to a program (rox-rex for Rex hosts, later the rux authority):
//
//	PROGRAM host list                      one host per line
//	PROGRAM host run [--tty] HOST -- ARGV  run `rox ARGV` on HOST, passing stdio and status through
type commandTransport struct{ program, path string }

func (t commandTransport) Name() string { return t.program }

func (t commandTransport) Hosts() ([]string, error) {
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{t.path, "host", "list"}, Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	return sys.Lines(out), nil
}

func (t commandTransport) Capture(host string, argv []string, wait time.Duration) (int, string, error) {
	result, err := sys.Try(sys.Cmd{Argv: append([]string{t.path, "host", "run", host, "--"}, argv...), Timeout: wait})
	if err != nil {
		return 0, "", err
	}
	return result.Code, result.Output(), nil
}

func (t commandTransport) Command(host string, o CommandOptions, argv []string) ([]string, error) {
	if len(o.Forwards) > 0 {
		return nil, fmt.Errorf("%s can't forward ports to a guest on another host. Use the ssh host transport", t.program)
	}
	cmd := []string{t.path, "host", "run"}
	if o.TTY {
		cmd = append(cmd, "--tty")
	}
	return append(append(cmd, host, "--"), argv...), nil
}

// HostTransportNamed resolves a transport setting: "" or "ssh", or the name of a program on PATH.
func HostTransportNamed(name string, hosts []string) (HostTransport, error) {
	if name == "" || name == "ssh" {
		return sshTransport{hosts: hosts}, nil
	}
	path, err := sys.R.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("The host transport %s isn't on PATH. Install it, or use \"host_transport\": \"ssh\"", name)
	}
	return commandTransport{program: sys.Base(path), path: path}, nil
}

// Transport is the host transport in use: ROX_HOST_TRANSPORT, then "host_transport" in the
// config, then SSH. The SSH hosts rox ls --all asks are ROX_HOSTS (comma-separated), then
// "hosts" in the config.
func Transport() (HostTransport, error) {
	name := os.Getenv("ROX_HOST_TRANSPORT")
	var hosts []string
	if v := os.Getenv("ROX_HOSTS"); v != "" {
		hosts, _ = ParseProviderNames(v)
	}
	if c, err := LoadConfig(""); err == nil {
		if name == "" {
			name, _ = c.Data["host_transport"].(string)
		}
		if hosts == nil {
			hosts, _ = ParseProviderNames(c.Data["hosts"])
		}
	}
	return HostTransportNamed(name, hosts)
}

// runRemote runs a command from Transport.Command with this terminal and returns its status as rox's.
func runRemote(argv []string, err error) error {
	if err != nil {
		return err
	}
	result, err := sys.Try(sys.Cmd{Argv: argv, Interactive: true, Env: map[string]string{"TERM": "xterm-256color"}})
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return exitCode(result.Code)
	}
	return nil
}
