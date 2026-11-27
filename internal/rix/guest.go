package rix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

// roxBin is the rox to drive: the one that called this hook, or rox on PATH.
func roxBin() (string, error) {
	if bin := os.Getenv("ROX_BIN"); bin != "" {
		return bin, nil
	}
	if p, err := sys.R.LookPath("rox"); err == nil {
		return p, nil
	}
	return "", errors.New("Install rox: rix reaches guests only through rox exec")
}

// Inspect reads a guest's record with rox inspect --json.
func Inspect(name string) (*contract.Guest, error) {
	rox, err := roxBin()
	if err != nil {
		return nil, err
	}
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{rox, "inspect", name, "--json"}, Timeout: 2 * time.Minute})
	if err != nil {
		return nil, err
	}
	var g contract.Guest
	if err := json.Unmarshal([]byte(out), &g); err != nil {
		return nil, fmt.Errorf("rox inspect printed something other than a guest record")
	}
	if g.Schema != contract.Schema {
		return nil, fmt.Errorf("rox speaks guest record schema %d and this rix speaks %d. Update both", g.Schema, contract.Schema)
	}
	return &g, nil
}

// Guests lists this machine's guests with rox ls --json.
func Guests() ([]contract.Entry, error) {
	rox, err := roxBin()
	if err != nil {
		return nil, err
	}
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{rox, "ls", "--json", "--all-projects"}, Timeout: 2 * time.Minute})
	if err != nil {
		return nil, err
	}
	var list contract.List
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, errors.New("rox ls --json printed something rix can't read. Update rox")
	}
	return list.Guests, nil
}

// guestScript runs one action of rix-guest.sh in the guest, with input on stdin.
func guestScript(name, input string, action string, args ...string) (string, error) {
	rox, err := roxBin()
	if err != nil {
		return "", err
	}
	argv := append([]string{rox, "exec", name, "--stdin", "--", "sh", "-c", assets.RixGuest(), "rix", action}, args...)
	out, err := sys.OutputWith(sys.Cmd{Argv: argv, Input: input, Timeout: 2 * time.Minute})
	if err != nil {
		var exit *sys.ExitError
		if errors.As(err, &exit) {
			exit.Argv = []string{"rox"}
			exit.Message = fmt.Sprintf("Writing to %s failed (%s)", name, firstLine(exit.Result.Output()))
		}
	}
	return out, err
}

// guestScriptBytes is guestScript with binary input, for the sync tar.
func guestScriptBytes(name string, input []byte, action string, args ...string) error {
	rox, err := roxBin()
	if err != nil {
		return err
	}
	argv := append([]string{rox, "exec", name, "--stdin", "--", "sh", "-c", assets.RixGuest(), "rix", action}, args...)
	_, err = sys.Run(sys.Cmd{Argv: argv, Stdin: strings.NewReader(string(input)), Timeout: 5 * time.Minute})
	return err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// Git agents. One ssh-agent per guest holds only its selected keys, and rox forwards it into
// the guest. Keys never leave the launcher.

// processIdentity reads when a process started and its command line, with ps, which
// Linux and macOS launchers both have. A PID that ps can't describe has no identity.
func processIdentity(pid int) (start, command string, ok bool) {
	if pid <= 0 {
		return "", "", false
	}
	env := map[string]string{"LC_ALL": "C"}
	p := fmt.Sprint(pid)
	started, err := sys.Try(sys.Cmd{Argv: []string{"ps", "-o", "lstart=", "-p", p}, Env: env})
	if err != nil || started.Code != 0 || strings.TrimSpace(started.Stdout) == "" {
		return "", "", false
	}
	cmd, err := sys.Try(sys.Cmd{Argv: []string{"ps", "-ww", "-o", "command=", "-p", p}, Env: env})
	if err != nil || cmd.Code != 0 || strings.TrimSpace(cmd.Stdout) == "" {
		return "", "", false
	}
	return strings.Join(strings.Fields(started.Stdout), " "), strings.TrimSpace(cmd.Stdout), true
}

// signalProcess is syscall.Kill, replaceable in tests.
var signalProcess = syscall.Kill

// isOurAgent reports whether pid is the ssh-agent rix started on sock. A record with a start
// time must match it, so a PID the system reused is never taken for the agent. An older
// record holds only the PID, and the process must then be an ssh-agent on sock.
func isOurAgent(pid int, start, sock string) bool {
	now, command, ok := processIdentity(pid)
	if !ok {
		return false
	}
	if start != "" && now != start {
		return false
	}
	// The command must be exactly what rix runs: ssh-agent, then -D -a and this socket.
	exe, rest, _ := strings.Cut(strings.TrimSpace(command), " ")
	return filepath.Base(exe) == "ssh-agent" && strings.TrimSpace(rest) == "-D -a "+sock
}

// recordAgent notes the agent rix started, with the identity stopAgent checks later.
func recordAgent(state *GuestState, pid int) {
	if pid <= 0 {
		return
	}
	state.AgentPid, state.AgentStart = pid, ""
	if start, _, ok := processIdentity(pid); ok {
		state.AgentStart = start
	}
}

// agentStatus is ssh-add -l's status: 0 with keys, 1 empty, 2 no agent.
func agentStatus(sock string) int {
	result, err := sys.Try(sys.Cmd{Argv: []string{"ssh-add", "-l"}, Env: map[string]string{"SSH_AUTH_SOCK": sock}})
	if err != nil {
		return 2
	}
	return result.Code
}

// startAgent makes sure the agent at sock runs and holds keys. Without a terminal, encrypted
// keys can't be unlocked, and the error says to run rix git-agent start in one. It returns the
// PID of an agent it started. When it fails, an agent this call started is stopped again, so
// no detached ssh-agent is left behind without a record.
func startAgent(name, sock string, keys []string, interactive bool) (pid int, err error) {
	if _, err := sys.PrivateDir(filepath.Dir(filepath.Dir(sock))); err != nil {
		return 0, err
	}
	if _, err := sys.PrivateDir(filepath.Dir(sock)); err != nil {
		return 0, err
	}
	status := agentStatus(sock)
	if status == 2 {
		os.Remove(sock)
		process, detachErr := sys.R.Detach(sys.Cmd{Argv: []string{"ssh-agent", "-D", "-a", sock}}, "")
		if detachErr != nil {
			return 0, detachErr
		}
		pid = process.Pid()
		defer func() {
			if err != nil {
				process.Kill()
				os.Remove(sock)
				pid = 0
			}
		}()
		if _, err = sys.WaitFor(5*time.Second, "the Git SSH agent", func() (bool, bool, error) {
			if exited, _ := process.Exited(); exited {
				return false, false, &sys.Fatal{Err: errors.New("Could not start the Git SSH agent")}
			}
			return true, sys.IsSocket(sock), nil
		}); err != nil {
			return 0, err
		}
		status = 1
	}
	if status == 1 && len(keys) > 0 {
		c := sys.Cmd{Argv: append([]string{"ssh-add"}, keys...), Env: map[string]string{"SSH_AUTH_SOCK": sock}}
		if interactive {
			c.Interactive = true
		} else {
			c.Env["SSH_ASKPASS_REQUIRE"] = "never"
			c.Unset = []string{"DISPLAY"}
		}
		result, runErr := sys.Try(c)
		if runErr == nil && result.Code != 0 {
			runErr = fmt.Errorf("Could not load the Git keys for %s: %s. Encrypted keys need their passphrase, so run rix git-agent start %s in a terminal",
				name, firstNonEmpty(firstLine(result.Stderr), "ssh-add failed"), name)
		}
		if runErr != nil {
			return pid, runErr
		}
	}
	return pid, nil
}

// stopAgent stops the guest's agent, but only a process that is still the one rix started.
func stopAgent(name string, state *GuestState) {
	sock := agentSocket(name)
	if state.AgentPid > 0 && isOurAgent(state.AgentPid, state.AgentStart, sock) {
		_ = signalProcess(state.AgentPid, syscall.SIGTERM)
	}
	state.AgentPid, state.AgentStart = 0, ""
	os.Remove(sock)
}

// discardAgent stops an agent this call started when its record couldn't be saved.
func discardAgent(name string, pid int) {
	if pid > 0 {
		stopAgent(name, &GuestState{AgentPid: pid})
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func keyNames(keys []string) string {
	var names []string
	for _, k := range keys {
		names = append(names, filepath.Base(k))
	}
	return strings.Join(names, ", ")
}
