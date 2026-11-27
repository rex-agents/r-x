package roxrex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/rox"
	"github.com/rex-agents/r-x/internal/sys"
)

// Outcome collects what a hook did, for the reply.
type Outcome struct{ Done, Warnings []string }

func (o *Outcome) done(format string, a ...any) { o.Done = append(o.Done, fmt.Sprintf(format, a...)) }
func (o *Outcome) warn(format string, a ...any) {
	o.Warnings = append(o.Warnings, fmt.Sprintf(format, a...))
}

// Hook answers one lifecycle event from rox. It reads the guest record from stdin and writes
// one JSON reply to stdout. Events it has nothing to do for get an empty reply.
func Hook(event, name string, stdin io.Reader, stdout io.Writer) error {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("rox-rex hook %s expects the guest record on stdin", event)
	}
	var g contract.Guest
	if err := json.Unmarshal([]byte(line), &g); err != nil {
		return fmt.Errorf("rox-rex hook %s couldn't read the guest record: %v", event, err)
	}
	if g.Schema != contract.Schema {
		return fmt.Errorf("rox speaks guest record schema %d and this rox-rex speaks %d. Update both", g.Schema, contract.Schema)
	}
	if name != "-" && name != g.Name {
		return fmt.Errorf("rox-rex hook was asked about %s but given %s's record", name, g.Name)
	}
	launch := g.Launch
	if launch == nil {
		launch = &contract.Launch{}
	}
	reply := contract.Reply{Schema: contract.Schema}
	out := &Outcome{}
	switch event {
	case contract.PostBoot:
		err = postBoot(&g, launch, out, &reply)
	case contract.PostProvision:
		err = postProvision(&g, launch, out, &reply)
	case contract.Resume:
		err = resume(&g, out, &reply)
	case contract.Down:
		err = down(&g, out)
	case contract.OpenShell:
		err = openShell(&g, launch, out)
	}
	if err != nil {
		return err
	}
	reply.Done, reply.Warnings = out.Done, out.Warnings
	data, _ := json.Marshal(reply)
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

// rexChannel is the Rex release channel this machine runs, so guests match it.
func rexChannel() string {
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{"rex", "version"}, Timeout: 10 * time.Second})
	if err != nil {
		return "preview"
	}
	version := strings.Split(out, "\n")[0]
	switch {
	case regexp.MustCompile(`alpha|snapshot`).MatchString(version):
		return "snapshot"
	case strings.Contains(version, "preview") || !regexp.MustCompile(`\d+\.\d+\.\d+\s*$`).MatchString(version):
		return "preview"
	}
	return "stable"
}

// errNoRex marks a guest without Rex, which the later events then leave alone.
var errNoRex = errors.New("Rex isn't installed in the guest")

// startServer installs Rex in the guest if needed and starts its server.
func startServer(g *contract.Guest, install bool) error {
	if g.Socket == "" {
		return errors.New("the guest has no state folder yet")
	}
	if install {
		result, err := guestScript(g, 15*time.Minute, "install", rexChannel())
		if err != nil {
			return err
		}
		if result.Code == 3 {
			return fmt.Errorf("%w: %s", errNoRex, firstLine(result.Stderr))
		}
		if result.Code != 0 {
			return fmt.Errorf("Installing Rex in %s failed: %s", g.Name, firstLine(result.Output()))
		}
	}
	result, err := guestScript(g, 2*time.Minute, "server", g.Socket)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		if strings.Contains(result.Stderr, "isn't installed") {
			return errNoRex
		}
		return fmt.Errorf("Starting Rex in %s failed: %s", g.Name, firstLine(result.Output()))
	}
	return ensureRelay(g)
}

func postBoot(g *contract.Guest, launch *contract.Launch, out *Outcome, reply *contract.Reply) error {
	if err := startServer(g, true); err != nil {
		if errors.Is(err, errNoRex) {
			out.warn("%s. rox-rex skipped Rex there (%s)", err, "rox template macos --with-rex puts it in a macOS template")
			return nil
		}
		out.warn("%v", err)
		return nil
	}
	out.done("Rex is running in the guest")
	if name := enableTailnet(g, launch, out); name != "" {
		reply.Endpoint = name
	}
	return nil
}

func resume(g *contract.Guest, out *Outcome, reply *contract.Reply) error {
	if g.Socket == "" || g.Runtime == "" {
		return nil
	}
	was := reachable(g)
	if err := startServer(g, false); err != nil {
		if !errors.Is(err, errNoRex) {
			out.warn("%v", err)
		}
		return nil
	}
	if !was {
		out.done("Reconnected to Rex in %s", g.Name)
	}
	if name := resumeTailnet(g, out); name != "" {
		reply.Endpoint = name
	}
	if st := loadState(g); !st.Registered {
		registerHost(g, st, out)
	}
	return nil
}

// sessionFor creates the Rex session for a project, or for the guest, and checks it's visible.
func sessionFor(g *contract.Guest, launch *contract.Launch) (string, error) {
	label, cwd := g.Name, g.Home
	for _, p := range g.Projects {
		if p.ID == launch.Project {
			label, cwd = p.Name, p.Workspace
		}
	}
	argv := guestRex(g, "new", label, "-c", cwd, "--json")
	if len(launch.Command) > 0 {
		argv = append(append(argv, "--keep-open", "--"), launch.Command...)
	}
	out, err := guestRun(g, "", time.Minute, argv...)
	if err != nil {
		return "", err
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(out), &created) != nil || created.SessionID == "" {
		return "", errors.New("Rex did not return a session ID")
	}
	if _, err := sys.Run(sys.Cmd{Argv: rexOn(g, "session", "inspect", created.SessionID, "--json"), Timeout: 8 * time.Second}); err != nil {
		return created.SessionID, fmt.Errorf("Session %s was created but the launcher can't see it yet", created.SessionID)
	}
	return created.SessionID, nil
}

func postProvision(g *contract.Guest, launch *contract.Launch, out *Outcome, reply *contract.Reply) error {
	if g.Socket == "" || g.Runtime == "" {
		return nil
	}
	if !reachable(g) {
		if err := startServer(g, false); err != nil {
			if !errors.Is(err, errNoRex) {
				out.warn("No Rex session for %s: %v", g.Name, err)
			}
			return nil
		}
	}
	// A project gets a session when it was just provisioned; a guest without projects gets its own.
	wantSession, existing := launch.Project != "" || len(g.Projects) == 0, ""
	for _, p := range g.Projects {
		if p.ID == launch.Project {
			existing = p.Session
		}
	}
	if len(g.Projects) == 0 {
		existing = g.Session
	}
	if wantSession && existing == "" {
		session, err := sessionFor(g, launch)
		if session != "" {
			reply.Session = session
			out.done("Started Rex session %s", session)
		}
		if err != nil {
			out.warn("%v", err)
		}
	}
	st := loadState(g)
	if !st.Registered {
		registerHost(g, st, out)
	}
	return nil
}

func down(g *contract.Guest, out *Outcome) error {
	st := loadState(g)
	if g.Socket != "" && g.Tailnet != "" && g.Runtime != "" {
		leaveTailnet(g, out)
	}
	// A record from rox down --host has no folder here: its Rex host, if any, is by name.
	if g.Dir == "" || st.Registered {
		switch removeHost(g.Name) {
		case removed:
			out.done("Removed Rex host %s", g.Name)
		case failed:
			out.warn("Could not remove Rex host %s. Run rex hosts remove %s", g.Name, g.Name)
		}
	}
	closeWindows(st)
	closeRelay(g)
	return nil
}

var _ = rox.StateRoot
var _ = os.Getenv
