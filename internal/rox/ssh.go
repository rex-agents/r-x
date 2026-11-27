package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8",
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
		"setup": ".rox.sh", "bake": ".rox.sh", "agents": "Installing the agent CLIs",
		"bake-prepare": "Preparing the image build",
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

// guestReachable reports whether the launcher logs in to the guest over SSH right now.
func guestReachable(g *Guest) bool {
	if g.Address == "" {
		return false
	}
	result, err := sys.Try(sys.Cmd{Argv: remoteCommand(g, []string{"true"}, "", false), Timeout: 12 * time.Second})
	return err == nil && result.Code == 0
}

// refreshAddress asks the backend where the running guest is now and saves it.
func refreshAddress(g *Guest) error {
	addr, port, err := backendFor(g).Address(g)
	if err != nil {
		return err
	}
	g.Address, g.Port = addr, port
	g.MustSave()
	return nil
}

// Git forwarding: rix keeps the guest's Git keys in an agent on the launcher, and rox forwards
// that agent into the guest over SSH. The private keys never leave the launcher.
const gitAgentScript = `ln -sfn "$SSH_AUTH_SOCK" "$1" && exec tail -f /dev/null`

func guestGitSocket(base string) string { return path.Join(base, "git-agent.sock") }

func gitForwardPid(g *Guest) string { return filepath.Join(g.Runtime(), "git-forward.pid") }

// startGitForward keeps sock forwarded into the guest at a fixed path, for its shells and tools.
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
