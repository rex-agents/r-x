package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

func rexJSON(timeout time.Duration, argv ...string) (map[string]any, error) {
	out, err := sys.OutputWith(sys.Cmd{Argv: argv, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("%s printed something other than JSON", strings.Join(argv[:min(3, len(argv))], " "))
	}
	return v, nil
}

func registerGuest(g *Guest) error {
	if g.Registered {
		return nil
	}
	// The socket works only on this machine; devices that share this Rex home use the tailnet name.
	endpoints := []string{g.Endpoint()}
	if g.TailnetName != "" {
		endpoints = append(endpoints, g.TailnetName)
	}
	if _, err := sys.Run(sys.Cmd{Argv: append([]string{"rex", "hosts", "add", g.Name}, endpoints...)}); err != nil {
		return err
	}
	g.Registered = true
	g.MustSave()
	ui.Done("Registered Rex host %s", ui.Bold(g.Name))
	if g.TailnetName == "" {
		ui.Info("Only this machine can reach %s. Configure tailscale with %s to reach guests from other devices", g.Name, ui.Code("rox config tailscale"))
	}
	return nil
}

type hostEntry struct {
	label     string
	endpoints []string
}

var guestLabel = regexp.MustCompile(`^` + nameInText + `$`)

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
func shareLauncherHosts(g *Guest) {
	var added []string
	err := func() error {
		wanted, err := launcherHosts()
		if err != nil || len(wanted) == 0 {
			return err
		}
		// Rex in the guest reads hosts from its home, which defaults to a server the guest doesn't run.
		if _, err := guestRun(g, guestRex(g, "home", "set", "unix://"+g.GuestSocket), "", 20*time.Second); err != nil {
			return err
		}
		out, err := guestRun(g, guestRex(g, "hosts", "list", "--json"), "", 20*time.Second)
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
		_ = json.Unmarshal([]byte(out), &listing)
		have := map[string]bool{}
		for _, h := range listing.Hosts {
			have[strings.ToLower(h.Host.Label)] = true
		}
		for _, h := range wanted {
			if have[strings.ToLower(h.label)] || strings.EqualFold(h.label, g.Name) {
				continue
			}
			if _, err := guestRun(g, guestRex(g, append([]string{"hosts", "add", h.label}, h.endpoints...)...), "", 20*time.Second); err != nil {
				return err
			}
			added = append(added, h.label)
		}
		return nil
	}()
	if err != nil {
		ui.Warn("Could not share this machine's Rex hosts with %s (%s)", g.Name, firstLine(err.Error()))
		return
	}
	if len(added) > 0 {
		ui.Done("Shared Rex hosts %s with the guest", strings.Join(added, ", "))
	}
}

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
func openInApp(name, server, session, address string) error {
	app := localApp()
	if app == nil {
		return fmt.Errorf("Open the Rex app on this machine to open %s in it, or use rox shell %s", name, name)
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
		if err := addAppHost(name, firstNonEmpty(address, server)); err != nil {
			return err
		}
		var err error
		s := ui.Start("Waiting for the Rex app to connect to %s", name)
		client, err = sys.WaitFor(180*time.Second, "the Rex app to connect to "+name, func() (string, bool, error) {
			c := guestApp()
			return c, c != "", nil
		})
		if err != nil {
			s.Fail(err)
			return err
		}
		s.Done("The Rex app connected to %s", name)
	}
	result, err := sys.Try(sys.Cmd{Argv: []string{"rex", "--autostart=false", "-S", server, "-C", client, "do", "session.select", "session_id=" + session}, Timeout: 15 * time.Second})
	if err == nil && result.Code != 0 {
		err = fmt.Errorf("The Rex app is on %s, but couldn't open session %s: %s", name, session, firstLine(result.Output()))
	}
	if err != nil {
		return err
	}
	ui.Done("Opened %s in the Rex app %s", name, ui.Muted("· splits and new tabs there run in the guest"))
	return nil
}

// addAppHost copies the guest's address and opens Add Remote Host in the Rex app here, which
// keeps its own host list and ignores rex hosts add.
func addAppHost(name, address string) error {
	app := localApp()
	if app == nil {
		return errors.New("Open the Rex app on this machine, then run this again")
	}
	if apps, err := appClients(address); err == nil {
		for _, a := range apps {
			if app.Label == "" || a.Label == app.Label {
				ui.Skip("%s is already in the Rex app", name)
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
		ui.Done("Copied %s %s", ui.Accent(address), ui.Muted("· paste it into Add Remote Host in the Rex app"))
	} else {
		ui.Info("Enter %s in Add Remote Host in the Rex app to add %s", ui.Accent(address), name)
	}
	return nil
}

// OpenGuest shows a guest's own session in the Rex app here.
func OpenGuest(name, key, host string) error {
	if host == "" && IsLocal(name) {
		g, err := readyGuest(name)
		if err != nil {
			return err
		}
		session, err := g.Session(key)
		if err != nil {
			return err
		}
		if session == "" {
			return fmt.Errorf("%s has no Rex session for %s yet. Run rox reconnect %s", name, firstNonEmpty(key, "the guest"), name)
		}
		return openInApp(name, g.Endpoint(), session, firstNonEmpty(g.TailnetName, g.Endpoint()))
	}
	host, entry, err := findRemoteGuest(name, host)
	if err != nil {
		return err
	}
	if entry.Tailnet == "" {
		return fmt.Errorf("%s runs on %s and isn't on the tailnet, so the Rex app here can't reach it. Use rox shell %s", name, host, name)
	}
	session := entry.Sessions[key]
	if key == "" {
		session = entry.Sessions[""]
		if session == "" {
			for _, k := range sortedKeysString(entry.Sessions) {
				session = entry.Sessions[k]
				break
			}
		}
	}
	if session == "" {
		return fmt.Errorf("%s has no Rex session for %s yet. Run rox reconnect %s", name, firstNonEmpty(key, "the guest"), name)
	}
	return openInApp(name, entry.Tailnet, session, entry.Tailnet)
}

// AddRemoteHost adds a guest, here or on another Rex host, to the Rex app on this machine.
func AddRemoteHost(name, host string) error {
	if host == "" && IsLocal(name) {
		g, err := LoadGuest(name)
		if err != nil {
			return err
		}
		if !g.Registered {
			return fmt.Errorf("%s isn't a Rex host yet. Run rox reconnect %s first", name, name)
		}
		return addAppHost(name, firstNonEmpty(g.TailnetName, g.Endpoint()))
	}
	host, entry, err := findRemoteGuest(name, host)
	if err != nil {
		return err
	}
	if entry.Tailnet == "" {
		return fmt.Errorf("%s runs on %s and isn't on the tailnet, so only %s can reach it. Run rox add-remote-host %s there", name, host, host, name)
	}
	return addAppHost(name, entry.Tailnet)
}

func suggestAppHost(name string) {
	if localApp() != nil {
		ui.Hint("Add it to the Rex app's sidebar with " + ui.Code("rox add-remote-host "+name))
	}
}

// openAppShell opens a Guest tab running rox shell, and remembers it so rox down can close it.
func openAppShell(session, name, project string) error {
	argv := []string{"rex", "--autostart=false", "window", "new", "-s", session, "--keep-open", "--json", "Guest", "--", selfPath(), "shell", name}
	if project != "" {
		argv = append(argv, project)
	}
	created, err := rexJSON(20*time.Second, argv...)
	if err != nil {
		return err
	}
	window, _ := created["window_id"].(string)
	if window == "" {
		return nil
	}
	ui.Done("Opened a Guest tab in this Rex session")
	return withGuest(name, true, func(g *Guest) error {
		g.ShellWindows = append(g.ShellWindows, ShellWindow{Session: session, Window: window})
		return g.Save()
	})
}

// closeShellWindows closes the guest's Guest tabs, and a session left empty, such as a Provision one.
func closeShellWindows(g *Guest) {
	rex := []string{"rex", "--autostart=false", "--timeout", "5s"}
	for _, w := range g.ShellWindows {
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

func removeRexHost(name string) bool {
	result, err := sys.Try(sys.Cmd{Argv: []string{"rex", "hosts", "remove", name}})
	return err == nil && (result.Code == 0 || result.Code == 3 || strings.Contains(result.Stderr, "no host has that label"))
}

// remoteRox runs rox on another host with the release install's PATH, which Rex may lack.
var remoteRox = []string{"sh", "-c", `PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; exec rox "$@"`, "rox"}

// rexOutput runs argv in a short-lived session on a Rex server, and returns its status and output.
func rexOutput(server, label string, argv []string, wait time.Duration) (int, string, error) {
	rex := []string{"rex", "--autostart=false", "--timeout", "10s", "-S", server}
	created, err := rexJSON(20*time.Second, append(append([]string{}, rex...), append([]string{"new", "--json", "--keep-open", label, "--"}, argv...)...)...)
	if err != nil {
		return 0, "", err
	}
	session, block := sessionBlock(created)
	if session == "" {
		return 0, "", errors.New("Rex did not return the new session")
	}
	defer sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "kill", session), Timeout: 20 * time.Second})
	status, err := sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "wait", "-s", session, block, "--for", fmt.Sprintf("%ds", int(wait.Seconds()))), Timeout: wait + 10*time.Second})
	if err != nil {
		return 0, "", err
	}
	text, err := sys.OutputWith(sys.Cmd{Argv: append(append([]string{}, rex...), "capture", "-s", session, "-b", block, "--trim", "--unwrap"), Timeout: 20 * time.Second})
	return status.Code, text, err
}

func sessionBlock(created map[string]any) (string, string) {
	session, _ := created["session_id"].(string)
	windows, _ := created["initial_windows"].([]any)
	if len(windows) == 0 {
		return session, ""
	}
	blocks, _ := windows[0].(map[string]any)["block_ids"].([]any)
	if len(blocks) == 0 {
		return session, ""
	}
	block, _ := blocks[0].(string)
	return session, block
}

var listCommand = []string{"sh", "-c", `PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; ` +
	`rox ls --json --all-projects 2>/dev/null || rox ls --json 2>/dev/null || { echo "#rox-plain"; rox ls; }`}

// remoteRecords runs rox ls on a Rex host and reads its guests, from Go or Python rox.
func remoteRecords(host string) ([]contract.Entry, error) {
	code, text, err := rexOutput(host, "rox ls", listCommand, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if code == 127 || strings.Contains(text, "rox: not found") || strings.Contains(text, "rox: command not found") {
		return nil, errors.New("rox isn't installed there")
	}
	if code != 0 {
		return nil, fmt.Errorf("rox ls exited with status %d: %s", code, tail(text, 200))
	}
	return parseRecords(text)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func parseRecords(text string) ([]contract.Entry, error) {
	text = ui.StripANSI(text)
	if strings.HasPrefix(strings.TrimSpace(text), "#rox-plain") {
		// An old rox without --json prints one guest per line.
		var entries []contract.Entry
		for _, line := range sys.Lines(text)[1:] {
			fields := strings.Fields(line)
			if len(fields) == 0 || !NamePattern.MatchString(fields[0]) {
				continue
			}
			for len(fields) < 5 {
				fields = append(fields, "-")
			}
			entries = append(entries, contract.Entry{Name: fields[0], Backend: fields[1], Phase: fields[2], IP: fields[3], Projects: strings.Join(fields[4:], " ")})
		}
		return entries, nil
	}
	// Go rox prints {"schema":1,"guests":[...]}; Python rox prints the list itself.
	start := strings.IndexAny(text, "[{")
	if start < 0 {
		return nil, errors.New("rox ls printed no JSON")
	}
	text = text[start:]
	var list contract.List
	if strings.HasPrefix(text, "{") {
		end := strings.LastIndex(text, "}")
		if err := json.Unmarshal([]byte(text[:end+1]), &list); err != nil {
			return nil, fmt.Errorf("rox ls printed unreadable JSON: %v", err)
		}
		return list.Guests, nil
	}
	end := strings.LastIndex(text, "]")
	if err := json.Unmarshal([]byte(text[:end+1]), &list.Guests); err != nil {
		return nil, fmt.Errorf("rox ls printed unreadable JSON: %v", err)
	}
	return list.Guests, nil
}

func rexHostLabels() ([]string, error) {
	listing, err := rexJSON(15*time.Second, "rex", "hosts", "list", "--json")
	if err != nil {
		return nil, err
	}
	var labels []string
	entries, _ := listing["hosts"].([]any)
	for _, e := range entries {
		host, _ := e.(map[string]any)["host"].(map[string]any)
		label, _ := host["label"].(string)
		// The Rex app may label a guest by its tailnet name.
		if label != "" && !guestLabel.MatchString(strings.SplitN(label, ".", 2)[0]) {
			labels = append(labels, label)
		}
	}
	return labels, nil
}

func serverID(server string) string {
	argv := []string{"rex", "--autostart=false", "--timeout", "5s"}
	if server != "" {
		argv = append(argv, "-S", server)
	}
	status, err := rexJSON(10*time.Second, append(argv, "server", "status", "--json")...)
	if err != nil {
		return ""
	}
	id, _ := status["host_id"].(string)
	return id
}

const thisMachine = "this machine"

// gatherRecords returns each host's guests, and why any host couldn't be asked. scope is
// "all" for this machine and every Rex host, or one host label.
func gatherRecords(scope string) (map[string][]contract.Entry, []string, map[string]string, error) {
	var hosts []string
	if scope == "all" {
		labels, err := rexHostLabels()
		if err != nil {
			return nil, nil, nil, err
		}
		hosts = labels
	} else {
		hosts = []string{scope}
	}
	here := serverID("")
	results := map[string][]contract.Entry{}
	order := []string{}
	if scope == "all" {
		results[thisMachine] = localEntries()
		order = append(order, thisMachine)
	}
	problems := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	found := map[string][]contract.Entry{}
	for _, host := range hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if scope == "all" && here != "" && serverID(host) == here {
				return // Another label for this machine's own server.
			}
			entries, err := remoteRecords(host)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				problems[host] = err.Error()
			} else {
				found[host] = entries
			}
		}(host)
	}
	wg.Wait()
	for _, host := range hosts {
		if entries, ok := found[host]; ok {
			results[host] = entries
			order = append(order, host)
		}
	}
	// Two labels can name one server, so keep each launcher's guests once.
	names := map[string]bool{}
	for _, host := range order {
		var fresh []contract.Entry
		for _, e := range results[host] {
			if !names[e.Name] {
				fresh = append(fresh, e)
				names[e.Name] = true
			}
		}
		results[host] = fresh
	}
	return results, order, problems, nil
}

// findRemoteGuest finds the Rex host that runs a guest this machine has no record of.
func findRemoteGuest(name, host string) (string, contract.Entry, error) {
	scope := firstNonEmpty(host, "all")
	results, order, problems, err := gatherRecords(scope)
	if err != nil {
		return "", contract.Entry{}, err
	}
	for _, label := range order {
		for _, e := range results[label] {
			if e.Name == name && label != thisMachine {
				return label, e, nil
			}
		}
	}
	if host != "" && problems[host] != "" {
		return "", contract.Entry{}, fmt.Errorf("Could not list workspaces on %s: %s", host, problems[host])
	}
	skipped := ""
	if len(problems) > 0 {
		var hosts []string
		for h := range problems {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		skipped = " Couldn't ask " + strings.Join(hosts, ", ") + "."
	}
	where := "here or on any Rex host"
	if host != "" {
		where = "on " + host
	}
	return "", contract.Entry{}, fmt.Errorf("No managed workspace named %s %s.%s", name, where, skipped)
}

// runOnHost runs a rox command on another host and shows what it printed.
func runOnHost(host, label string, argv []string, wait time.Duration) error {
	s := ui.Start("Running %s on %s", label, host)
	code, text, err := rexOutput(host, label, append(append([]string{}, remoteRox...), argv...), wait)
	if err != nil {
		s.Fail(err)
		return err
	}
	if code != 0 {
		s.Fail(nil)
		showRemote(text)
		return fmt.Errorf("rox %s exited with status %d on %s", argv[0], code, host)
	}
	s.Done("Ran %s on %s", label, host)
	showRemote(text)
	return nil
}

// showRemote prints another host's rox output, which is already rox's own summary, under a gutter.
func showRemote(text string) {
	for _, line := range strings.Split(strings.TrimRight(ui.StripANSI(text), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			ui.Detail("%s", line)
		}
	}
}

// dispatch runs a rox command on a Rex remote host in a Provision tab and waits for it. The
// session closes when the command succeeds, and stays open to inspect when it fails.
func dispatch(host, label string, launch []string) (string, error) {
	rex := []string{"rex", "--autostart=false", "-S", host}
	created, err := rexJSON(30*time.Second, append(append([]string{}, rex...),
		append([]string{"new", "--window", "Provision", "--keep-open", "--json", "rox " + label, "--"}, append(append([]string{}, remoteRox...), launch...)...)...)...)
	if err != nil {
		return "", err
	}
	session, block := sessionBlock(created)
	s := ui.Start("Provisioning %s on %s", label, host)
	s.Note("Rex session " + session)
	status, err := sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "wait", "-s", session, block)})
	if err != nil {
		s.Fail(err)
		return "", err
	}
	captured, _ := sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "capture", "-s", session, "-b", block, "--trim", "--unwrap")})
	if status.Code != 0 {
		s.Fail(nil)
		showRemote(captured.Stdout)
		return "", fmt.Errorf("Provisioning exited with status %d. See the Provision tab in %s on %s", status.Code, session, host)
	}
	s.Done("Provisioned %s on %s %s", label, host, ui.Muted("· Rex session "+session))
	showRemote(captured.Stdout)
	if closed, _ := sys.Try(sys.Cmd{Argv: append(append([]string{}, rex...), "kill", session)}); closed.Code != 0 {
		ui.Warn("Could not close the Provision tab in %s on %s", session, host)
	}
	return captured.Stdout, nil
}

var (
	workspaceLine = regexp.MustCompile(`(?m)^[\s│●]*Workspace\s+(` + nameInText + `)\s*$`)
	tailnetLine   = regexp.MustCompile(`(?m)^[\s│●]*Tailnet\s+([a-z0-9-]+(?:\.[a-z0-9-]+)+)\s*$`)
	sessionLine   = regexp.MustCompile(`(?m)^[\s│●]*Session\s+(session:\S+)\s*$`)
)

func parseProvision(text string) (workspace, tailnet, session string) {
	text = ui.StripANSI(text)
	if m := workspaceLine.FindStringSubmatch(text); m != nil {
		workspace = m[1]
	}
	if m := tailnetLine.FindStringSubmatch(text); m != nil {
		tailnet = m[1]
	}
	if m := sessionLine.FindStringSubmatch(text); m != nil {
		session = m[1]
	}
	return
}

// registerRemoteGuest adds a guest launched on another host to this machine's Rex hosts.
func registerRemoteGuest(text, launcher string) {
	workspace, tailnet, _ := parseProvision(text)
	if workspace == "" {
		return
	}
	if tailnet == "" {
		ui.Info("%s is reachable only on %s, because it isn't on the tailnet. Configure tailscale there to reach guests from here", workspace, launcher)
		return
	}
	result, err := sys.Try(sys.Cmd{Argv: []string{"rex", "hosts", "add", workspace, tailnet}})
	if err != nil || result.Code != 0 {
		ui.Warn("Could not add %s to this machine's Rex hosts. Use rex -S %s instead", workspace, tailnet)
		return
	}
	ui.Done("Registered Rex host %s here at %s", ui.Bold(workspace), tailnet)
	suggestAppHost(workspace)
}

func openRemoteGuest(text string) {
	workspace, tailnet, session := parseProvision(text)
	if workspace == "" {
		return
	}
	if tailnet == "" || session == "" || localApp() == nil {
		ui.Hint("Open a shell in it with " + ui.Code("rox shell "+workspace))
		return
	}
	if err := openInApp(workspace, tailnet, session, tailnet); err != nil {
		ui.Warn("%v. Open it later with rox open %s", err, workspace)
	}
}

// Remote SSH through a guest's tailnet Rex server, for shells and rox exec from another machine.

const authorizeScript = `set -eu
umask 077
mkdir -p "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
grep -qxF "$1" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$1" >> "$HOME/.ssh/authorized_keys"
`

// remoteSSHKey is this machine's key for SSH into guests on other hosts, created on first use.
func remoteSSHKey() (string, error) {
	dir, err := sys.PrivateDir(filepath.Join(StateRoot(), "remote"))
	if err != nil {
		return "", err
	}
	key := filepath.Join(dir, "login")
	if !sys.IsFile(key) {
		if _, err := sys.Run(sys.Cmd{Argv: []string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "rox remote shell", "-f", key}}); err != nil {
			return "", err
		}
	}
	return key, nil
}

// tailnetSSH is a session that reaches a remote guest's SSH server through its tailnet Rex
// server. Close ends the forward.
type tailnetSSH struct {
	argv    []string // ssh options up to and including the destination
	forward sys.Process
}

func (t *tailnetSSH) Close() {
	if t.forward != nil {
		t.forward.Kill()
	}
}

func openTailnetSSH(entry contract.Entry) (*tailnetSSH, error) {
	name, server := entry.Name, entry.Tailnet
	if entry.Socket == "" || entry.User == "" {
		return nil, fmt.Errorf("Upgrade rox on the guest's host to reach its guests from here")
	}
	login, err := remoteSSHKey()
	if err != nil {
		return nil, err
	}
	public, err := sys.ReadTrim(login + ".pub")
	if err != nil {
		return nil, err
	}
	code, text, err := rexOutput(server, "rox authorize "+name, []string{"sh", "-c", authorizeScript, "rox", public}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("Could not authorize this machine's SSH key in %s: %s", name, tail(text, 200))
	}
	port := sys.FreePort()
	forward, err := sys.R.Detach(sys.Cmd{Argv: []string{"rex", "--autostart=false", "-S", server, "forward", "new", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:22", port)}}, "")
	if err != nil {
		return nil, err
	}
	t := &tailnetSSH{forward: forward}
	if _, err := sys.WaitFor(20*time.Second, "the tunnel through "+server, func() (bool, bool, error) {
		if exited, code := forward.Exited(); exited {
			return false, false, &sys.Fatal{Err: fmt.Errorf("rex forward exited with status %d", code)}
		}
		return true, sys.PortOpen(port), nil
	}); err != nil {
		t.Close()
		return nil, err
	}
	dir, _ := sys.PrivateDir(filepath.Join(StateRoot(), "remote"))
	knownHosts := filepath.Join(dir, name+".known_hosts")
	// The tunnel ends inside the guest, behind the Rex server's authenticated tailnet connection,
	// so the key it reports is the guest's own.
	scanned, err := sys.OutputWith(sys.Cmd{Argv: []string{"ssh-keyscan", "-q", "-t", "ed25519", "-p", fmt.Sprint(port), "127.0.0.1"}, Timeout: 20 * time.Second})
	var fields []string
	for _, line := range sys.Lines(scanned) {
		if f := strings.Fields(line); len(f) >= 3 {
			fields = f[1:3]
			break
		}
	}
	if err != nil || fields == nil {
		t.Close()
		return nil, fmt.Errorf("%s didn't report an SSH host key", name)
	}
	if err := os.WriteFile(knownHosts, []byte(name+" "+fields[0]+" "+fields[1]+"\n"), 0o600); err != nil {
		t.Close()
		return nil, err
	}
	t.argv = []string{"ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostKeyAlias=" + name,
		"-o", "UserKnownHostsFile=" + knownHosts, "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-i", login, "-l", entry.User, "-p", fmt.Sprint(port)}
	return t, nil
}

func (t *tailnetSSH) command(tty bool, agent string, argv []string) []string {
	mode := "-T"
	if tty {
		mode = "-t"
	}
	forwarding := "ForwardAgent=no"
	if agent != "" {
		forwarding = "ForwardAgent=" + agent
	}
	return append(append(append([]string{}, t.argv...), mode, "-o", forwarding, "--", "127.0.0.1"), sys.Quote(argv...))
}
