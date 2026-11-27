package rix

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

func isSSH(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return !(err == nil && u.Scheme == "https")
}

// Hook answers one lifecycle event from rox. It reads the guest record from stdin and
// writes one JSON reply to stdout. For pre-fetch it then keeps its agent until stdin closes.
func Hook(event, name string, stdin io.Reader, stdout io.Writer) error {
	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("rix hook %s expects the guest record on stdin", event)
	}
	var g contract.Guest
	if err := json.Unmarshal([]byte(line), &g); err != nil {
		return fmt.Errorf("rix hook %s couldn't read the guest record: %v", event, err)
	}
	if g.Schema != contract.Schema {
		return fmt.Errorf("rox speaks guest record schema %d and this rix speaks %d. Update both", g.Schema, contract.Schema)
	}
	if name != "-" && name != g.Name {
		return fmt.Errorf("rix hook was asked about %s but given %s's record", name, g.Name)
	}
	launch := g.Launch
	if launch == nil {
		launch = &contract.Launch{}
	}
	c, err := LoadConfig("")
	if err != nil {
		return err
	}
	reply := contract.Reply{Schema: contract.Schema}
	out := &Outcome{}
	write := func() error {
		reply.Done, reply.Warnings = out.Done, out.Warnings
		data, _ := json.Marshal(reply)
		_, err := fmt.Fprintln(stdout, string(data))
		return err
	}
	switch event {
	case contract.PreFetch:
		return preFetch(c, launch, reader, stdout)
	case contract.PreClone:
		if !isSSH(launch.URL) {
			return write()
		}
		if reply.KnownHosts, err = knownHosts(c, launch.GitKnownHosts); err != nil {
			return err
		}
		keys, usedDefault, err := resolveKeys(c, launch.GitKeys)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			out.warn("No Git SSH key is configured and ~/.ssh has none of ssh's default keys, so the guest probably can't clone %s. Add one with rix key add PATH", launch.URL)
			return write()
		}
		forward, err := c.GitAgent()
		if err != nil {
			return err
		}
		forward = forward && !launch.NoGitAgent
		state, err := loadState(g.Name)
		if err != nil {
			return err
		}
		sock := agentSocket(g.Name)
		pid, err := startAgent(g.Name, sock, keys, false)
		if err != nil {
			return err
		}
		if pid > 0 {
			recordAgent(state, pid)
		}
		state.GitKeys, state.Forward = keys, forward
		if err := saveState(g.Name, state); err != nil {
			discardAgent(g.Name, pid)
			return err
		}
		reply.AgentSocket, reply.Forward = sock, forward
		source := "configured"
		if usedDefault {
			source = "default"
		}
		out.done("Git agent holds your %s %s %s", source, plural(len(keys), "key", "keys"), keyNames(keys))
		return write()
	case contract.PostBoot:
		if err := PushCredentials(c, &g, out); err != nil {
			return err
		}
		return write()
	case contract.PostProvision:
		if len(launch.NewAgents) > 0 {
			if err := PushCredentials(c, &g, out); err != nil {
				return err
			}
		}
		if err := SyncSetup(c, &g, out); err != nil {
			out.warn("Could not copy your agent setup into %s (%s)", g.Name, firstLine(err.Error()))
		}
		// A guest that keeps Git keys only for the clone gives them up now.
		if state, err := loadState(g.Name); err == nil && !state.Forward && state.AgentPid > 0 {
			stopAgent(g.Name, state)
			_ = saveState(g.Name, state)
		}
		return write()
	case contract.Resume:
		state, err := loadState(g.Name)
		if err != nil {
			return err
		}
		if len(state.GitKeys) == 0 || !state.Forward {
			return write()
		}
		sock := agentSocket(g.Name)
		restarted := agentStatus(sock) == 2
		pid, err := startAgent(g.Name, sock, state.GitKeys, false)
		if err != nil {
			return err
		}
		if pid > 0 {
			recordAgent(state, pid)
			if err := saveState(g.Name, state); err != nil {
				discardAgent(g.Name, pid)
				return err
			}
		}
		reply.AgentSocket, reply.Forward = sock, true
		if restarted {
			out.done("Restarted the Git agent with %s", keyNames(state.GitKeys))
		}
		return write()
	case contract.Down:
		state, err := loadState(g.Name)
		if err != nil {
			return err
		}
		had := state.AgentPid > 0 || len(state.GitKeys) > 0
		stopAgent(g.Name, state)
		os.RemoveAll(filepath.Join(StateRoot(), g.Name))
		os.RemoveAll(runtimeDir(g.Name))
		if had {
			out.done("Stopped the Git agent and forgot %s", g.Name)
		}
		return write()
	}
	return fmt.Errorf("Unknown hook event %s. rix knows %s", event, strings.Join(contract.Events, ", "))
}

// preFetch lends an agent for the launcher's own fetch of the branch, until rox closes stdin.
func preFetch(c *Config, launch *contract.Launch, stdin io.Reader, stdout io.Writer) error {
	reply := contract.Reply{Schema: contract.Schema}
	write := func() {
		data, _ := json.Marshal(reply)
		fmt.Fprintln(stdout, string(data))
		if f, ok := stdout.(*os.File); ok {
			f.Sync()
		}
	}
	if !isSSH(launch.URL) {
		write()
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	var err error
	if reply.KnownHosts, err = knownHosts(c, launch.GitKnownHosts); err != nil {
		return err
	}
	keys, _, err := resolveKeys(c, launch.GitKeys)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		write()
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	scratch, err := os.MkdirTemp("", "rix-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	sock := filepath.Join(scratch, "agent.sock")
	process, err := sys.R.Detach(sys.Cmd{Argv: []string{"ssh-agent", "-D", "-a", sock}}, "")
	if err != nil {
		return err
	}
	defer process.Kill()
	if _, err := sys.WaitFor(5e9, "the Git SSH agent", func() (bool, bool, error) {
		return true, sys.IsSocket(sock), nil
	}); err != nil {
		return err
	}
	result, err := sys.Try(sys.Cmd{Argv: append([]string{"ssh-add"}, keys...), Env: map[string]string{"SSH_AUTH_SOCK": sock, "SSH_ASKPASS_REQUIRE": "never"}})
	if err != nil {
		return err
	}
	if result.Code != 0 {
		reply.Warnings = append(reply.Warnings, "Could not unlock your Git keys without a terminal, so the launcher reads the branch without them")
	} else {
		reply.AgentSocket = sock
	}
	write()
	_, _ = io.Copy(io.Discard, stdin)
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
