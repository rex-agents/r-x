package quorum

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/coder/websocket"
)

// The git SSH agent relay (phase 2 of the vault): the vault host's rix runs a per-guest
// ssh-agent; a member relays that socket over /quorum/ssh-agent/{guest} into its guest, so
// Git keys never leave the vault host. The vault host needs Config.SSHAgentSocket (the
// authority maps a guest to rix's agent socket); until it is set, members use their local rix.

func (n *Node) sshAgentStatus(cfg ClusterConfig) string {
	switch {
	case cfg.VaultHost == n.Name() || cfg.VaultHost == "":
		return "local rix"
	}
	return "relay via " + cfg.VaultHost + " (falls back to local rix if the vault host has no relay)"
}

func (n *Node) handleSSHAgent(w http.ResponseWriter, r *http.Request) {
	member, ok := n.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	guest := r.PathValue("guest")
	cfg := n.fsm.get()
	if cfg.VaultHost != n.Name() || n.cfg.SSHAgentSocket == nil {
		http.Error(w, "this authority doesn't relay an ssh-agent", http.StatusMisdirectedRequest)
		return
	}
	entry := AuditEntry{Member: member, Thread: guest, Secret: "ssh-agent:" + guest}
	if member != n.Name() && !cfg.Policy.Allows(member, "ssh-agent") && !cfg.Policy.Allows(member, "ssh-agent:"+guest) {
		entry.Result = "denied"
		n.vhost.audit(entry)
		http.Error(w, ErrVaultDenied.Error(), http.StatusForbidden)
		return
	}
	sock, err := n.cfg.SSHAgentSocket(guest)
	if err == nil {
		var c net.Conn
		if c, err = net.Dial("unix", sock); err == nil {
			defer c.Close()
			ws, aerr := websocket.Accept(w, r, nil)
			if aerr != nil {
				return
			}
			entry.Result = "ok"
			n.vhost.audit(entry)
			pump(websocket.NetConn(r.Context(), ws, websocket.MessageBinary), c)
			return
		}
	}
	entry.Result = "error"
	n.vhost.audit(entry)
	http.Error(w, "ssh-agent: "+err.Error(), http.StatusBadGateway)
}

func pump(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) { io.Copy(dst, src); dst.Close(); done <- struct{}{} }
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

// RelaySSHAgent connects conn (a guest's agent client) to the vault host's agent for guest.
func (n *Node) RelaySSHAgent(ctx context.Context, guest string, conn net.Conn) error {
	m, ok := n.memberByName(n.fsm.get().VaultHost)
	if !ok {
		return fmt.Errorf("%w: no vault host", ErrVaultUnreachable)
	}
	ws, _, err := websocket.Dial(ctx, wsURL(m.URL)+"/quorum/ssh-agent/"+guest, &websocket.DialOptions{HTTPHeader: n.dialHeader(), HTTPClient: n.client})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrVaultUnreachable, err)
	}
	pump(websocket.NetConn(context.Background(), ws, websocket.MessageBinary), conn)
	return nil
}

// ListenSSHAgent serves a unix socket at path that relays each connection to the vault
// host's agent for guest. Close the returned listener to stop.
func (n *Node) ListenSSHAgent(ctx context.Context, guest, path string) (net.Listener, error) {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				if err := n.RelaySSHAgent(ctx, guest, c); err != nil {
					n.logf("quorum: ssh-agent relay for %s: %v", guest, err)
					c.Close()
				}
			}()
		}
	}()
	return l, nil
}

var _ = errors.New
