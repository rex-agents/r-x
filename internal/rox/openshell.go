package rox

import (
	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/ui"
)

// openShellHandled reports whether a merged open-shell reply says a provider opened the shell.
func openShellHandled(reply *contract.Reply) bool { return reply != nil && len(reply.Done) > 0 }

// openShell opens a shell for the guest (and its project p, if any) where a hook provider
// shows one, such as a Guest tab or the Rex app. Without a provider that does, it runs
// rox shell in this terminal.
func openShell(g *Guest, p *Project) error {
	id := ""
	if p != nil {
		id = p.ID
	}
	reply, _ := runHook(contract.OpenShell, g, &contract.Launch{Project: id})
	if openShellHandled(reply) {
		return nil
	}
	ui.Info("No hook provider opened a shell, so opening one here")
	return Shell(g.Name, id)
}

// OpenGuest is rox open: it shows a guest in an app through the open-shell hook, and falls back
// to rox shell here.
func OpenGuest(name, key, host string) error {
	if host == "" && IsLocal(name) {
		g, err := readyGuest(name)
		if err != nil {
			return err
		}
		if key != "" {
			if _, err := g.FindProject(key); err != nil {
				return err
			}
		}
		id := key
		if p, err := g.FindProject(key); key != "" && err == nil {
			id = p.ID
		}
		reply, _ := runHook(contract.OpenShell, g, &contract.Launch{Project: id})
		if openShellHandled(reply) {
			return nil
		}
		ui.Info("No hook provider opened a shell, so opening one here")
		return Shell(name, key)
	}
	host, entry, err := findRemoteGuest(name, host)
	if err != nil {
		return err
	}
	record := recordFromEntry(host, entry)
	reply, _ := runHookPayload(contract.OpenShell, name, payloadFor(record, &contract.Launch{Project: key}), false)
	if openShellHandled(reply) {
		return nil
	}
	ui.Info("No hook provider opened a shell, so opening one here")
	return remoteShell(host, name, key)
}
