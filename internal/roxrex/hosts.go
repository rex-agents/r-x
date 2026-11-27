package roxrex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

var guestLabel = regexp.MustCompile(`^rv-[a-z0-9-]+-[0-9a-f]{12}$`)

// registerHost adds the guest to this machine's Rex hosts and shares this machine's hosts with it.
func registerHost(g *contract.Guest, st *state, out *Outcome) {
	if g.Runtime == "" {
		return
	}
	// The socket works only on this machine; devices that share this Rex home use the tailnet name.
	endpoints := []string{endpoint(g)}
	if g.Tailnet != "" {
		endpoints = append(endpoints, g.Tailnet)
	}
	if _, err := sys.Run(sys.Cmd{Argv: append([]string{"rex", "hosts", "add", g.Name}, endpoints...)}); err != nil {
		out.warn("Could not register %s as a Rex host: %s", g.Name, firstLine(err.Error()))
		return
	}
	st.Registered = true
	_ = st.save(g)
	out.done("Registered Rex host %s", g.Name)
	if g.Tailnet == "" {
		out.done("Only this machine can reach %s. Configure tailscale with rox config tailscale to reach guests from other devices", g.Name)
	}
	if !st.Shared {
		shareLauncherHosts(g, out)
		st.Shared = true
		_ = st.save(g)
	}
	if localApp() != nil {
		out.done("Add it to the Rex app's sidebar with rox-rex add-host %s", g.Name)
	}
}

type removal int

const (
	removed removal = iota
	absent
	failed
)

func removeHost(name string) removal {
	result, err := sys.Try(sys.Cmd{Argv: []string{"rex", "hosts", "remove", name}})
	switch {
	case err != nil:
		return failed
	case result.Code == 0:
		return removed
	case result.Code == 3 || strings.Contains(result.Stderr, "no host has that label"):
		return absent
	}
	return failed
}

type hostEntry struct {
	label     string
	endpoints []string
}

// launcherHosts are the Rex hosts a guest can reach: this machine's hosts and this machine.
func launcherHosts() ([]hostEntry, error) {
	listing, err := rexJSON(15*time.Second, "rex", "hosts", "list", "--json")
	if err != nil {
		return nil, err
	}
	var hosts []hostEntry
	seen := map[string]bool{}
	entries, _ := listing["hosts"].([]any)
	for _, e := range entries {
		host, _ := e.(map[string]any)["host"].(map[string]any)
		label, _ := host["label"].(string)
		var endpoints []string
		raw, _ := host["endpoints"].([]any)
		for _, ep := range raw {
			if s, ok := ep.(string); ok && !strings.HasPrefix(s, "unix:") {
				endpoints = append(endpoints, s)
			}
		}
		all := len(endpoints) > 0
		for _, ep := range endpoints {
			all = all && seen[ep]
		}
		if label == "" || len(endpoints) == 0 || all || guestLabel.MatchString(strings.SplitN(label, ".", 2)[0]) {
			continue
		}
		hosts = append(hosts, hostEntry{label, endpoints})
		for _, ep := range endpoints {
			seen[ep] = true
			seen[strings.TrimSuffix(strings.TrimPrefix(ep, "https://"), "/")] = true
		}
	}
	status, _ := rexJSON(10*time.Second, "rex", "--autostart=false", "--timeout", "5s", "server", "status", "--json")
	ts, _ := status["tailscale"].(map[string]any)
	own, _ := ts["dns_name"].(string)
	own = strings.TrimRight(own, ".")
	if ts["state"] == "running" && own != "" && !seen[own] {
		hostname, _ := os.Hostname()
		label := strings.ToLower(strings.SplitN(hostname, ".", 2)[0])
		for _, h := range hosts {
			if strings.EqualFold(h.label, label) {
				label = own
			}
		}
		hosts = append(hosts, hostEntry{firstNonEmpty(label, own), []string{own}})
	}
	return hosts, nil
}

// shareLauncherHosts adds this machine's Rex hosts to the guest's own server.
func shareLauncherHosts(g *contract.Guest, out *Outcome) {
	var added []string
	err := func() error {
		wanted, err := launcherHosts()
		if err != nil || len(wanted) == 0 {
			return err
		}
		// Rex in the guest reads hosts from its home, which defaults to a server the guest doesn't run.
		if _, err := guestRun(g, "", 20*time.Second, guestRex(g, "home", "set", "unix://"+g.Socket)...); err != nil {
			return err
		}
		listed, err := guestRun(g, "", 20*time.Second, guestRex(g, "hosts", "list", "--json")...)
		if err != nil {
			return err
		}
		var listing struct {
			Hosts []struct {
				Host struct {
					Label string `json:"label"`
				} `json:"host"`
			} `json:"hosts"`
		}
		_ = json.Unmarshal([]byte(listed), &listing)
		have := map[string]bool{}
		for _, h := range listing.Hosts {
			have[strings.ToLower(h.Host.Label)] = true
		}
		for _, h := range wanted {
			if have[strings.ToLower(h.label)] || strings.EqualFold(h.label, g.Name) {
				continue
			}
			if _, err := guestRun(g, "", 20*time.Second, guestRex(g, append([]string{"hosts", "add", h.label}, h.endpoints...)...)...); err != nil {
				return err
			}
			added = append(added, h.label)
		}
		return nil
	}()
	if err != nil {
		out.warn("Could not share this machine's Rex hosts with %s (%s)", g.Name, firstLine(err.Error()))
		return
	}
	if len(added) > 0 {
		out.done("Shared Rex hosts %s with the guest", strings.Join(added, ", "))
	}
}

// The Rex app on this machine.

type appClient struct {
	ID    string
	Label string
	Unix  bool
}

func appClients(server string) ([]appClient, error) {
	argv := []string{"rex", "--autostart=false", "--timeout", "5s"}
	if server != "" {
		argv = append(argv, "-S", server)
	}
	listing, err := rexJSON(10*time.Second, append(argv, "client", "ls", "--json")...)
	if err != nil {
		return nil, err
	}
	var apps []appClient
	clients, _ := listing["clients"].([]any)
	for _, c := range clients {
		m, _ := c.(map[string]any)
		info, _ := m["info"].(map[string]any)
		principal, _ := m["principal"].(map[string]any)
		if info["kind"] != "app" || m["connection_state"] != "connected" {
			continue
		}
		label, _ := info["label"].(string)
		if label == "" {
			label, _ = m["label"].(string)
		}
		id, _ := m["client_id"].(string)
		apps = append(apps, appClient{ID: id, Label: label, Unix: principal["transport"] == "unix"})
	}
	return apps, nil
}

func localApp() *appClient {
	apps, err := appClients("")
	if err != nil {
		return nil
	}
	for _, a := range apps {
		if a.Unix {
			return &a
		}
	}
	return nil
}

// openInApp shows a guest session in the Rex app here, so its splits and tabs run in the guest.
func openInApp(name, server, session, address string, out *Outcome) error {
	app := localApp()
	if app == nil {
		return errors.New("the Rex app isn't open on this machine")
	}
	guestApp := func() string {
		apps, err := appClients(server)
		if err != nil {
			return ""
		}
		for _, a := range apps {
			if app.Label == "" || a.Label == app.Label {
				return a.ID
			}
		}
		return ""
	}
	client := guestApp()
	if client == "" {
		if err := addAppHost(name, firstNonEmpty(address, server), out); err != nil {
			return err
		}
		var err error
		client, err = sys.WaitFor(180*time.Second, "the Rex app to connect to "+name, func() (string, bool, error) {
			c := guestApp()
			return c, c != "", nil
		})
		if err != nil {
			return err
		}
	}
	result, err := sys.Try(sys.Cmd{Argv: []string{"rex", "--autostart=false", "-S", server, "-C", client, "do", "session.select", "session_id=" + session}, Timeout: 15 * time.Second})
	if err == nil && result.Code != 0 {
		err = fmt.Errorf("The Rex app is on %s, but couldn't open session %s: %s", name, session, firstLine(result.Output()))
	}
	if err != nil {
		return err
	}
	out.done("Opened %s in the Rex app · splits and new tabs there run in the guest", name)
	return nil
}

// addAppHost copies the guest's address and opens Add Remote Host in the Rex app here, which
// keeps its own host list and ignores rex hosts add.
func addAppHost(name, address string, out *Outcome) error {
	app := localApp()
	if app == nil {
		return errors.New("Open the Rex app on this machine, then run this again")
	}
	if apps, err := appClients(address); err == nil {
		for _, a := range apps {
			if app.Label == "" || a.Label == app.Label {
				out.done("%s is already in the Rex app", name)
				return nil
			}
		}
	}
	copied := sys.Has("pbcopy") && sys.Status("sh", "-c", "printf %s "+sys.QuoteArg(address)+" | pbcopy") == 0
	how := "Enter " + address
	if copied {
		how = "Copied " + address + ". Paste it"
	}
	result, err := sys.Try(sys.Cmd{Argv: []string{"rex", "--autostart=false", "-C", app.ID, "do", "client.host.add"}, Timeout: 8 * time.Second})
	if err == nil && result.Code != 0 {
		err = fmt.Errorf("The Rex app didn't open Add Remote Host (%s).\nTurn on Remote Control in the app, or run Rox: Add a Guest to the Rex App from its palette.\n%s into Add Remote Host yourself", firstLine(result.Output()), how)
	}
	if err != nil {
		return err
	}
	if copied {
		out.done("Copied %s · paste it into Add Remote Host in the Rex app", address)
	} else {
		out.done("Enter %s in Add Remote Host in the Rex app to add %s", address, name)
	}
	return nil
}

// openAppShell opens a Guest tab running rox shell, and remembers it so down can close it.
func openAppShell(g *contract.Guest, session, project string, out *Outcome) error {
	argv := []string{"rex", "--autostart=false", "window", "new", "-s", session, "--keep-open", "--json", "Guest", "--", roxBin(), "shell", g.Name}
	if project != "" {
		argv = append(argv, project)
	}
	created, err := rexJSON(20*time.Second, argv...)
	if err != nil {
		return err
	}
	window, _ := created["window_id"].(string)
	if window == "" {
		return errors.New("Rex did not return the new tab")
	}
	out.done("Opened a Guest tab in this Rex session")
	st := loadState(g)
	st.Windows = append(st.Windows, Window{Session: session, Window: window})
	return st.save(g)
}

// closeWindows closes the guest's Guest tabs, and a session left empty.
func closeWindows(st *state) {
	rex := []string{"rex", "--autostart=false", "--timeout", "5s"}
	for _, w := range st.Windows {
		sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "window", "close", "-s", w.Session, w.Window), Timeout: 15 * time.Second})
		result, err := sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "window", "ls", "-s", w.Session, "--json"), Timeout: 15 * time.Second})
		if err != nil || result.Code != 0 {
			continue
		}
		var listing struct {
			Windows *[]any `json:"windows"`
		}
		if json.Unmarshal([]byte(result.Stdout), &listing) == nil && listing.Windows != nil && len(*listing.Windows) == 0 {
			sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "kill", w.Session), Timeout: 15 * time.Second})
		}
	}
}

// sessionOf finds the session for a project, the only project, or the guest itself.
func sessionOf(g *contract.Guest, project string) string {
	for _, p := range g.Projects {
		if p.ID == project {
			return p.Session
		}
	}
	if project == "" {
		if g.Session != "" {
			return g.Session
		}
		for _, p := range g.Projects {
			if p.Session != "" {
				return p.Session
			}
		}
	}
	return ""
}

// openShell is the open-shell event: show the guest in the Rex app here, or open a Guest tab in
// this Rex terminal's session. When neither is possible it replies without done, and rox runs
// rox shell here.
func openShell(g *contract.Guest, launch *contract.Launch, out *Outcome) error {
	session := sessionOf(g, launch.Project)
	server, address := g.Endpoint, g.Endpoint
	if g.Runtime != "" {
		if err := ensureRelay(g); err == nil {
			server, address = endpoint(g), firstNonEmpty(g.Tailnet, endpoint(g))
		}
	}
	if localApp() != nil && session != "" && server != "" {
		if err := openInApp(g.Name, server, session, address, out); err != nil {
			out.warn("%v", err)
		} else {
			return nil
		}
	}
	if app := os.Getenv("REX_SESSION"); app != "" && g.Runtime != "" {
		if err := openAppShell(g, app, launch.Project, out); err != nil {
			out.warn("%v", err)
		}
	}
	return nil
}
