package roxrex

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

func relaySocket(g *contract.Guest) string { return filepath.Join(g.Runtime, "relay.sock") }

// relayCommand is what the relay runs for each connection: rex dial-stdio in the guest, over rox exec.
func relayCommand(g *contract.Guest) []string {
	return execArgv(g.Name, true, "sh", "-c", guestPath, "rox", "rex", "--autostart=false", "-S", "unix://"+g.Socket, "__internal", "dial-stdio")
}

// openRelay starts a local socket whose every connection runs rex dial-stdio in the guest
// through rox exec, so the launcher reaches the guest's Rex server without knowing how.
func openRelay(g *contract.Guest) error {
	if _, err := sys.PrivateDir(filepath.Dir(g.Runtime)); err != nil {
		return err
	}
	if _, err := sys.PrivateDir(g.Runtime); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	argv := append([]string{self, "__relay", g.Runtime, "--"}, relayCommand(g)...)
	log := filepath.Join(g.Runtime, "relay.log")
	process, err := sys.R.Detach(sys.Cmd{Argv: argv}, log)
	if err != nil {
		return err
	}
	_, err = sys.WaitFor(5*time.Second, "the Rex relay", func() (bool, bool, error) {
		exited, _ := process.Exited()
		return true, sys.IsSocket(filepath.Join(g.Runtime, "rex.sock")) && !exited, nil
	})
	if err == nil {
		_, err = sys.Run(sys.Cmd{Argv: rexOn(g, "ls", "--json"), Timeout: 8 * time.Second})
	}
	if err != nil {
		if exited, _ := process.Exited(); !exited {
			closeRelay(g)
			process.Kill()
		}
		return err
	}
	return nil
}

func closeRelay(g *contract.Guest) {
	if g.Runtime == "" || !sys.IsSocket(relaySocket(g)) {
		return
	}
	conn, err := net.DialTimeout("unix", relaySocket(g), 5*time.Second)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	_, _ = conn.Read(buf)
}

// ensureRelay makes sure the launcher reaches the guest's Rex server, replacing a dead relay.
func ensureRelay(g *contract.Guest) error {
	if reachable(g) {
		return nil
	}
	closeRelay(g)
	return openRelay(g)
}

// Relay runs rox-rex __relay: it listens at RUNTIME/rex.sock and starts command for each
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
