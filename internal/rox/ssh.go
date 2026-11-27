package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/sys"
)

// sshCommand is ssh to the guest with its pinned host key and login key, and nothing from ~/.ssh.
func sshCommand(g *Guest, options ...string) []string {
	known, _ := json.Marshal(g.KnownHosts())
	argv := []string{
		"ssh", "-F", "/dev/null",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=4",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "HostKeyAlias=" + g.Name,
		"-o", "UserKnownHostsFile=" + string(known),
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-i", g.Identity, "-l", g.User, "-p", strconv.Itoa(g.Port),
	}
	argv = append(argv, options...)
	return append(argv, "--", g.Address)
}

// remoteCommand runs argv in the guest. agent is a launcher SSH agent socket to forward, or "".
func remoteCommand(g *Guest, argv []string, agent string, tty bool) []string {
	forwarding := "ForwardAgent=no"
	if agent != "" {
		forwarding = "ForwardAgent=" + agent
	}
	mode := "-T"
	if tty {
		mode = "-t"
	}
	return append(sshCommand(g, mode, "-S", "none", "-o", forwarding), sys.Quote(argv...))
}

// guestRun runs argv in the guest and returns its stdout.
func guestRun(g *Guest, argv []string, input string, timeout time.Duration) (string, error) {
	return sys.OutputWith(sys.Cmd{Argv: remoteCommand(g, argv, "", false), Input: input, Timeout: timeout})
}

type stageOpts struct {
	Agent   string
	Timeout time.Duration
}

// guestStage pipes the bootstrap script into the guest and runs one of its stages.
func guestStage(g *Guest, stage string, opts stageOpts, args ...string) (string, error) {
	argv := remoteCommand(g, append([]string{"bash", "-s", "--", stage}, args...), opts.Agent, false)
	result, err := sys.Try(sys.Cmd{Argv: argv, Input: assets.Bootstrap(), Timeout: opts.Timeout})
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", &sys.ExitError{Argv: argv, Result: result, Message: stageFailure(g, stage, result)}
	}
	return strings.TrimSpace(result.Stdout), nil
}

func stageFailure(g *Guest, stage string, result sys.Result) string {
	if result.Code == 255 {
		return fmt.Sprintf("Could not reach %s over SSH for its %s stage", g.Name, stage)
	}
	what := map[string]string{
		"prepare": "Preparing the guest", "clone": "Cloning the project", "local": "Copying the local checkout",
		"setup": ".rox.sh", "bake": ".rox.sh", "server": "Starting Rex in the guest", "agents": "Installing the agent CLIs",
		"session": "Starting the Rex session", "bake-prepare": "Preparing the image build",
	}[stage]
	if what == "" {
		what = "The " + stage + " stage"
	}
	return fmt.Sprintf("%s failed in %s", what, g.Name)
}

var hostKeyType = regexp.MustCompile(`^(?:ssh-|ecdsa-|sk-)[\w@.-]+$`)
var hostKeyData = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// pinHost records the guest's SSH host key, which every later connection must match.
func pinHost(g *Guest, key string) error {
	fields := strings.Fields(key)
	if len(fields) < 2 || !hostKeyType.MatchString(fields[0]) || !hostKeyData.MatchString(fields[1]) {
		return errors.New("Expected an OpenSSH host public key")
	}
	return os.WriteFile(g.KnownHosts(), []byte(fmt.Sprintf("%s %s %s\n", g.Name, fields[0], fields[1])), 0o600)
}

// checkGuestOS waits for an SSH login and checks the guest runs the system its backend expects.
func checkGuestOS(g *Guest, timeout time.Duration) error {
	system, err := sys.WaitFor(timeout, "guest SSH login", func() (string, bool, error) {
		out, err := guestRun(g, []string{"uname", "-s"}, "", 8*time.Second)
		return out, err == nil && out != "", err
	})
	if err != nil {
		return err
	}
	expected := "Linux"
	if g.Backend == "macos" {
		expected = "Darwin"
	}
	if system != expected {
		return fmt.Errorf("%s expects %s, but the image runs %s", g.Backend, expected, system)
	}
	return nil
}

func ensureRuntimeDirs(g *Guest) error {
	if _, err := sys.PrivateDir(filepath.Dir(g.Runtime())); err != nil {
		return err
	}
	_, err := sys.PrivateDir(g.Runtime())
	return err
}

// openTunnel forwards the guest's Rex socket to the launcher over an SSH control master.
func openTunnel(g *Guest) error {
	if err := ensureRuntimeDirs(g); err != nil {
		return err
	}
	if sys.IsSocket(g.ControlSocket()) {
		if sys.Status(sshCommand(g, "-S", g.ControlSocket(), "-O", "check")...) == 0 {
			return nil
		}
	}
	_, err := sys.Run(sys.Cmd{Timeout: 15 * time.Second, Argv: sshCommand(g,
		"-fNM", "-S", g.ControlSocket(), "-o", "ControlPersist=no",
		"-E", g.Log("ssh-tunnel.log"),
		"-o", "ForwardAgent=no", "-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindUnlink=yes",
		"-L", g.RexSocket()+":"+g.GuestSocket)})
	return err
}

func closeRelay(g *Guest) {
	if !sys.IsSocket(g.RelaySocket()) {
		return
	}
	conn, err := net.DialTimeout("unix", g.RelaySocket(), 5*time.Second)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	_, _ = conn.Read(buf)
}

func closeTunnel(g *Guest) {
	closeRelay(g)
	if sys.IsSocket(g.ControlSocket()) {
		sys.Status(sshCommand(g, "-S", g.ControlSocket(), "-O", "exit")...)
	}
}

// rexOn is rex aimed at the guest's server through the launcher's socket.
func rexOn(g *Guest, args ...string) []string {
	return append([]string{"rex", "--autostart=false", "--timeout", "5s", "-S", g.Endpoint()}, args...)
}

// guestRex is rex for the guest's own server, run inside the guest with the bootstrap's PATH.
func guestRex(g *Guest, args ...string) []string {
	return append([]string{"sh", "-c", `export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; exec "$@"`,
		"rox", "rex", "--autostart=false", "-S", "unix://" + g.GuestSocket}, args...)
}

// reachable reports whether the launcher reaches the guest's Rex server through its tunnel.
func reachable(g *Guest) bool {
	result, err := sys.Try(sys.Cmd{Argv: rexOn(g, "ls", "--json"), Timeout: 8 * time.Second})
	return err == nil && result.Code == 0
}

// openRelay is the fallback when socket forwarding fails: a local socket whose every
// connection runs rex dial-stdio in the guest over its own SSH session.
func openRelay(g *Guest) error {
	if err := ensureRuntimeDirs(g); err != nil {
		return err
	}
	dial := []string{"sh", "-c", `export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; exec "$@"`,
		"rox", "rex", "--autostart=false", "-S", "unix://" + g.GuestSocket, "__internal", "dial-stdio"}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	argv := append([]string{self, "__relay", g.Runtime(), "--"}, remoteCommand(g, dial, "", false)...)
	process, err := sys.R.Detach(sys.Cmd{Argv: argv}, g.Log("ssh-tunnel.log"))
	if err != nil {
		return err
	}
	_, err = sys.WaitFor(5*time.Second, "the Rex SSH relay", func() (bool, bool, error) {
		exited, _ := process.Exited()
		return true, sys.IsSocket(g.RelaySocket()) && !exited, nil
	})
	if err == nil {
		_, err = sys.Run(sys.Cmd{Argv: rexOn(g, "ls", "--json"), Timeout: 8 * time.Second})
	}
	if err != nil {
		if exited, _ := process.Exited(); !exited {
			closeRelay(g)
			process.Kill()
		}
		return fmt.Errorf("Could not reach Rex through the SSH relay. See %s.\n%v", g.Log("ssh-tunnel.log"), err)
	}
	return nil
}

// attachGuest makes sure the launcher reaches the guest's Rex server, restarting it if needed.
func attachGuest(g *Guest, session string) error {
	ok := false
	if !sys.IsSocket(g.RelaySocket()) {
		ok = openTunnel(g) == nil
	}
	if ok || sys.IsSocket(g.RelaySocket()) {
		_, err := sys.Run(sys.Cmd{Argv: rexOn(g, "ls", "--json"), Timeout: 8 * time.Second})
		ok = err == nil
	}
	if !ok {
		if _, err := guestStage(g, "server", stageOpts{}, g.GuestSocket); err != nil {
			return err
		}
		closeTunnel(g)
		if err := openRelay(g); err != nil {
			return err
		}
	}
	if session != "" {
		_, err := sys.Run(sys.Cmd{Argv: rexOn(g, "session", "inspect", session, "--json"), Timeout: 8 * time.Second})
		return err
	}
	return nil
}

// connect finds the running guest's address, makes sure its Rex server runs, and attaches.
func connect(g *Guest, tailnet *Tailnet) error {
	addr, port, err := backendFor(g).Address(g)
	if err != nil {
		return err
	}
	g.Address, g.Port = addr, port
	g.MustSave()
	if _, err := guestStage(g, "server", stageOpts{}, g.GuestSocket); err != nil {
		return err
	}
	if err := attachGuest(g, ""); err != nil {
		return err
	}
	if g.TailnetName != "" {
		resumeTailnet(g, tailnet)
	}
	return nil
}

// Git forwarding: rix keeps the guest's Git keys in an agent on the launcher, and rox forwards
// that agent into the guest over SSH. The private keys never leave the launcher.
const gitAgentScript = `ln -sfn "$SSH_AUTH_SOCK" "$1" && exec tail -f /dev/null`

func guestGitSocket(base string) string { return path.Join(base, "git-agent.sock") }

func gitForwardPid(g *Guest) string { return filepath.Join(g.Runtime(), "git-forward.pid") }

// startGitForward keeps sock forwarded into the guest at a fixed path, for its Rex server and shells.
func startGitForward(g *Guest, sock string) error {
	if sock == "" || g.GuestSocket == "" {
		return nil
	}
	if err := ensureRuntimeDirs(g); err != nil {
		return err
	}
	if sys.PidAlive(gitForwardPid(g)) {
		return nil
	}
	script := sys.Quote("sh", "-c", gitAgentScript, "rox", guestGitSocket(g.Base()))
	argv := append(sshCommand(g, "-T", "-S", "none", "-o", "ForwardAgent="+sock), script)
	process, err := sys.R.Detach(sys.Cmd{Argv: argv}, g.Log("git-agent.log"))
	if err != nil {
		return err
	}
	return sys.WritePid(gitForwardPid(g), process.Pid())
}

func closeGitForward(g *Guest) { sys.KillPid(gitForwardPid(g)) }

// Relay runs rox __relay: it listens at RUNTIME/rex.sock and starts command for each
// connection, with the connection as its stdin and stdout. A connection to RUNTIME/relay.sock
// shuts it down. It replaces Python rox's guest/relay.py.
func Relay(runtime string, command []string) error {
	paths := []string{filepath.Join(runtime, "rex.sock"), filepath.Join(runtime, "relay.sock")}
	var listeners []*net.UnixListener
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
		for _, p := range paths {
			os.Remove(p)
		}
	}()
	for _, p := range paths {
		os.Remove(p)
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
		if err != nil {
			return err
		}
		l.SetUnlinkOnClose(false)
		listeners = append(listeners, l)
	}
	shutdown := make(chan net.Conn, 1)
	go func() {
		conn, err := listeners[1].Accept()
		if err == nil {
			shutdown <- conn
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	conns := make(chan net.Conn)
	go func() {
		for {
			conn, err := listeners[0].Accept()
			if err != nil {
				close(conns)
				return
			}
			conns <- conn
		}
	}()
	var children []*exec.Cmd
	defer func() {
		for _, c := range children {
			if c.Process != nil && c.ProcessState == nil {
				_ = c.Process.Signal(syscall.SIGTERM)
			}
		}
	}()
	for {
		select {
		case conn := <-shutdown:
			defer conn.Close()
			return nil
		case <-signals:
			return nil
		case conn, ok := <-conns:
			if !ok {
				return nil
			}
			child := exec.Command(command[0], command[1:]...)
			child.Stdin, child.Stdout = conn, conn
			if err := child.Start(); err != nil {
				conn.Close()
				continue
			}
			children = append(children, child)
			go func(c *exec.Cmd, conn net.Conn) {
				_ = c.Wait()
				conn.Close()
			}(child, conn)
		}
	}
}
