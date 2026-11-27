package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

type checkoutDir struct{ ID, Name, Workspace string }

// shellDirectory picks the checkout a shell opens in, or the guest home.
func shellDirectory(name string, checkouts []checkoutDir, home, key string) (string, error) {
	ids := func() string {
		var out []string
		for _, c := range checkouts {
			out = append(out, c.ID)
		}
		return firstNonEmpty(strings.Join(out, ", "), "none")
	}
	if key != "" {
		var matches []checkoutDir
		for _, c := range checkouts {
			if c.ID == key {
				matches = append(matches, c)
			}
		}
		if len(matches) == 0 {
			for _, c := range checkouts {
				if c.Name == key {
					matches = append(matches, c)
				}
			}
		}
		if len(matches) == 1 {
			return matches[0].Workspace, nil
		}
		problem := "is not a project"
		if len(matches) > 1 {
			problem = "matches several projects"
		}
		return "", fmt.Errorf("%q %s on %s. Projects: %s", key, problem, name, ids())
	}
	if len(checkouts) == 1 {
		return checkouts[0].Workspace, nil
	}
	if len(checkouts) > 1 {
		ui.Info("%s has several projects (%s). Opening the guest home; run %s to open a checkout", name, ids(), ui.Code("rox shell "+name+" PROJECT"))
	}
	return home, nil
}

func shellCommand(socket, directory, session string) []string {
	argv := []string{"env", "SSH_AUTH_SOCK=" + guestGitSocket(path.Dir(socket))}
	if session != "" {
		// A session means a Rex provider set the guest up, and its server listens at socket.
		argv = append(argv, "REX_SERVER=unix://"+socket, "REX_SESSION="+session)
	}
	return append(argv, "sh", "-c", `cd "$1" && exec "${SHELL:-/bin/sh}" -l`, "rox", directory)
}

// Shell opens an interactive shell in a guest here.
func Shell(name, key string) error {
	g, err := readyGuest(name)
	if err != nil {
		return err
	}
	if key != "" {
		if _, err := g.FindProject(key); err != nil {
			return err
		}
	}
	var checkouts []checkoutDir
	for _, p := range g.Projects {
		checkouts = append(checkouts, checkoutDir{p.ID, p.Name, p.Workspace})
	}
	dir, err := shellDirectory(name, checkouts, g.Home(), key)
	if err != nil {
		return err
	}
	session := g.SessionID
	for _, p := range g.Projects {
		if p.Workspace == dir {
			session = p.SessionID
		}
	}
	argv := remoteCommand(g, shellCommand(g.GuestSocket, dir, session), "", true)
	return sys.R.Replace(argv, map[string]string{"TERM": "xterm-256color"})
}

// ExitCode is an error that only sets the process's exit status.
type exitCode int

func (e exitCode) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// ExitStatus is the status an error asks the process to exit with.
func ExitStatus(err error) (int, bool) {
	var code exitCode
	if errors.As(err, &code) {
		return int(code), true
	}
	return 0, false
}

// ParseForward reads PORT or LOCAL:GUEST.
func ParseForward(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	var ports []int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return 0, 0, fmt.Errorf("expected PORT or LOCAL:GUEST, got %q", value)
		}
		ports = append(ports, n)
	}
	if len(ports) != 1 && len(ports) != 2 {
		return 0, 0, fmt.Errorf("expected PORT or LOCAL:GUEST, got %q", value)
	}
	return ports[0], ports[len(ports)-1], nil
}

// Forward tunnels launcher ports to servers on the guest's own localhost until Ctrl-C.
func Forward(name string, ports [][2]int) error {
	if err := sys.Require("ssh"); err != nil {
		return err
	}
	g, err := readyGuest(name)
	if err != nil {
		return err
	}
	var tunnels []string
	for _, p := range ports {
		tunnels = append(tunnels, "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", p[0], p[1]))
	}
	argv := sshCommand(g, append([]string{"-N", "-T", "-S", "none", "-o", "ForwardAgent=no", "-o", "ExitOnForwardFailure=yes"}, tunnels...)...)
	ui.Header("forward", name)
	for _, p := range ports {
		ui.Done("%s %s port %d on %s", ui.Accent(fmt.Sprintf("http://localhost:%d", p[0])), ui.Muted("→"), p[1], name)
	}
	ui.Info("Press Ctrl-C to stop")
	return sys.R.Replace(argv, nil)
}

// Write stdin to $1 with mode $2, through a temporary file so a reader never sees half of it.
const copyScript = `set -eu
mkdir -p "$(dirname "$1")"
temporary="$1.rox-cp.$$"
trap 'rm -f "$temporary"' EXIT
(umask 077 && cat > "$temporary")
chmod "$2" "$temporary"
mv -f "$temporary" "$1"
trap - EXIT
`

// ParseCopy reads SOURCE[:DEST]. DEST defaults to SOURCE, or its name when SOURCE is absolute.
func ParseCopy(value string) (string, string, error) {
	source, dest, colon := strings.Cut(value, ":")
	if source == "" || (colon && dest == "") {
		return "", "", errors.New("expected SOURCE:DEST, such as .env:.env")
	}
	if !colon {
		dest = source
		if filepath.IsAbs(source) {
			dest = filepath.Base(source)
		}
	}
	return source, dest, nil
}

func copyDirectory(g *Guest, key string) (string, error) {
	var p *Project
	if key != "" {
		var err error
		if p, err = g.FindProject(key); err != nil {
			return "", err
		}
	} else {
		var ready []*Project
		for _, p := range g.Projects {
			if p.Phase == "ready" {
				ready = append(ready, p)
			}
		}
		if len(ready) > 1 {
			var ids []string
			for _, p := range ready {
				ids = append(ids, p.ID)
			}
			return "", fmt.Errorf("%s has several projects (%s). Choose one with --project", g.Name, strings.Join(ids, ", "))
		}
		if len(ready) == 0 {
			return g.Home(), nil
		}
		p = ready[0]
	}
	if p.Phase != "ready" || p.Workspace == "" {
		return "", fmt.Errorf("%s on %s is %s, so it has no checkout to copy into", p.ID, g.Name, p.Phase)
	}
	return p.Workspace, nil
}

// Copy copies files from this machine into a guest, relative ones into its project's checkout.
func Copy(name string, copies [][2]string, key, cwd string) error {
	if NamePattern.MatchString(name) && !IsLocal(name) {
		return fmt.Errorf("rox cp copies into guests on this machine, and %s isn't one. Run it on the guest's host", name)
	}
	type file struct{ path, dest string }
	var files []file
	for _, c := range copies {
		p := sys.Expand(filepath.Join(cwd, c[0]))
		if filepath.IsAbs(c[0]) {
			p = sys.Expand(c[0])
		}
		if !sys.IsFile(p) {
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s is not a file", c[0])
			}
			return fmt.Errorf("No file %s", c[0])
		}
		files = append(files, file{p, c[1]})
	}
	if err := sys.Require("ssh"); err != nil {
		return err
	}
	g, err := readyGuest(name)
	if err != nil {
		return err
	}
	dir, err := copyDirectory(g, key)
	if err != nil {
		return err
	}
	ui.Header("cp", name)
	for _, f := range files {
		target := f.dest
		if !path.IsAbs(target) {
			target = path.Join(dir, target)
		}
		info, err := os.Stat(f.path)
		if err != nil {
			return err
		}
		stream, err := os.Open(f.path)
		if err != nil {
			return err
		}
		_, err = sys.Run(sys.Cmd{Argv: remoteCommand(g, []string{"bash", "-c", copyScript, "rox", target, fmt.Sprintf("%o", info.Mode().Perm())}, "", false), Stdin: stream})
		stream.Close()
		if err != nil {
			return err
		}
		ui.Done("Copied %s %s %s", f.path, ui.Muted("→"), target)
	}
	return nil
}

// guestIP is the address the launcher reaches the guest's servers at. Docker and Podman
// containers are reached through a loopback SSH port instead, so they have none.
func guestIP(g *Guest) string {
	if g.Address != "" && (g.Engine == "" || g.Engine == "container") && !isCloud(g.Backend) {
		return g.Address
	}
	return "-"
}

// Entry is the guest's line in rox ls --json.
func (g *Guest) Entry() contract.Entry {
	phase := g.Phase
	if g.Stopped && g.Phase == "ready" {
		phase = "stopped"
	}
	e := contract.Entry{Name: g.Name, Backend: g.Backend, Engine: g.Engine, Phase: phase, IP: guestIP(g), Tailnet: g.TailnetName,
		Projects: g.ProjectSummary(), Socket: g.GuestSocket, User: g.User, Agents: g.Agents,
		Checkouts: []contract.Checkout{}, Funnels: []contract.FunnelEntry{}, Sessions: map[string]string{}}
	if g.GuestSocket != "" {
		e.Home = g.Home()
	}
	for _, p := range g.Projects {
		if p.Workspace != "" {
			e.Checkouts = append(e.Checkouts, contract.Checkout{ID: p.ID, Name: p.Name, URL: p.URL, Workspace: p.Workspace})
		}
		if p.SessionID != "" {
			e.Sessions[p.ID] = p.SessionID
		}
	}
	if g.SessionID != "" {
		e.Sessions[""] = g.SessionID
	}
	for _, f := range g.Funnels {
		e.Funnels = append(e.Funnels, contract.FunnelEntry{URL: f.URL, Port: f.Port})
	}
	return e
}

func localEntries() []contract.Entry {
	entries := []contract.Entry{}
	for _, g := range LocalGuests() {
		entries = append(entries, g.Entry())
	}
	return entries
}

func entryRefs(e contract.Entry) []checkoutRef {
	var refs []checkoutRef
	for _, c := range e.Checkouts {
		refs = append(refs, checkoutRef{c.ID, c.Name, c.URL})
	}
	return refs
}

// List prints workspaces here, on one host, or on every reachable one. Inside a checkout,
// only the workspaces with a checkout of that project, unless allProjects.
func List(scope string, asJSON bool, cwd string, allProjects bool) error {
	var current *checkoutIdentity
	if !allProjects && cwd != "" {
		current = currentCheckout(cwd)
	}
	keep := func(e contract.Entry) bool {
		return current == nil || matchingCheckout(entryRefs(e), "", current) != ""
	}
	var results map[string][]contract.Entry
	var order []string
	problems := map[string]string{}
	if scope == "" {
		results = map[string][]contract.Entry{thisMachine: localEntries()}
		order = []string{thisMachine}
	} else {
		var err error
		s := ui.Start("Asking other hosts for their workspaces")
		if !ui.Fancy() || asJSON {
			s.End()
		}
		results, order, problems, err = gatherRecords(scope)
		s.End()
		if err != nil {
			return err
		}
		if scope != "all" && problems[scope] != "" {
			return fmt.Errorf("Could not list workspaces on %s: %s", scope, problems[scope])
		}
	}
	list := contract.List{Schema: contract.Schema, Guests: []contract.Entry{}}
	for _, host := range order {
		for _, e := range results[host] {
			if !keep(e) {
				continue
			}
			if scope != "" {
				e.Host = host
			}
			list.Guests = append(list.Guests, e)
		}
	}
	if asJSON {
		data, _ := sys.MarshalIndent(list)
		fmt.Fprint(ui.Stdout(), string(data))
		return nil
	}
	if current != nil {
		ui.Info("Workspaces with %s %s", ui.Bold(filepath.Base(current.Root)), ui.Muted("· rox ls --all-projects lists every one"))
	}
	if len(list.Guests) == 0 {
		ui.Skip("No workspaces")
		ui.Hint("Start one with " + ui.Code("rox up"))
	} else {
		headers := []string{"name", "backend", "phase", "ip", "projects"}
		if scope != "" {
			headers = append([]string{"host"}, headers...)
		}
		var rows [][]string
		for _, e := range list.Guests {
			row := []string{ui.Bold(e.Name), e.Backend, ui.Phase(e.Phase), firstNonEmpty(e.IP, "-"), firstNonEmpty(e.Projects, "-")}
			if scope != "" {
				row = append([]string{e.Host}, row...)
			}
			rows = append(rows, row)
		}
		ui.Table(headers, rows)
	}
	for _, host := range sortedKeysString(problems) {
		ui.Warn("Skipped %s: %s", host, problems[host])
	}
	return nil
}

// Inspect prints one guest's record as the contract defines it. A guest on another host
// is described from its rox ls entry.
func Inspect(name string) error {
	var record contract.Guest
	if IsLocal(name) {
		g, err := LoadGuest(name)
		if err != nil {
			return err
		}
		record = g.Record()
	} else {
		host, e, err := findRemoteGuest(name, "")
		if err != nil {
			return err
		}
		record = recordFromEntry(host, e)
	}
	data, _ := sys.MarshalIndent(record)
	fmt.Fprint(ui.Stdout(), string(data))
	return nil
}

func recordFromEntry(host string, e contract.Entry) contract.Guest {
	r := contract.Guest{Schema: contract.Schema, Name: e.Name, Backend: e.Backend, Engine: e.Engine, Phase: e.Phase,
		Stopped: e.Phase == "stopped", User: e.User, Home: e.Home, Socket: e.Socket, Agents: e.Agents,
		Tailnet: e.Tailnet, Endpoint: e.Tailnet, Projects: []contract.Project{}, Session: e.Sessions[""]}
	if e.Socket != "" {
		r.Base = path.Dir(e.Socket)
	}
	if r.Agents == nil {
		r.Agents = map[string]string{}
	}
	for _, c := range e.Checkouts {
		r.Projects = append(r.Projects, contract.Project{ID: c.ID, Name: c.Name, URL: c.URL, Workspace: c.Workspace, Phase: "ready", Session: e.Sessions[c.ID]})
	}
	_ = host
	return r
}

// Exec runs argv in the guest over rox's SSH, passing stdin, stdout and the exit status
// through. It is rix's only way into guests. It never takes the guest's lock, so hooks can
// use it while rox up holds it. A guest on another host is reached by running rox exec there.
func Exec(name string, stdin bool, agentSock string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("Pass the command to run after --")
	}
	var command []string
	if IsLocal(name) {
		g, err := LoadGuest(name)
		if err != nil {
			return err
		}
		if g.GuestSocket == "" || g.Address == "" {
			return fmt.Errorf("%s isn't reachable yet", name)
		}
		command = remoteCommand(g, argv, agentSock, false)
	} else {
		host, _, err := findRemoteGuest(name, "")
		if err != nil {
			return err
		}
		return remoteExec(host, name, stdin, agentSock, argv)
	}
	result, err := sys.Try(sys.Cmd{Argv: command, Passthrough: true, KeepStdin: stdin})
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return exitCode(result.Code)
	}
	return nil
}

// Route runs a command that names a guest on the host that runs it, when that isn't this
// machine. It reports whether the command was handled there.
func Route(command, name, host string, run func(host string, entry contract.Entry) error) (bool, error) {
	if name == "" || (host == "" && IsLocal(name)) {
		return false, nil
	}
	found, entry, err := findRemoteGuest(name, host)
	if err != nil {
		return true, err
	}
	return true, run(found, entry)
}

// RouteShell opens a shell in a guest on another host.
func RouteShell(host string, entry contract.Entry, key string) error {
	return remoteShell(host, entry.Name, key)
}

// RouteForward forwards ports to a guest on another host.
func RouteForward(host string, entry contract.Entry, ports [][2]int) error {
	return remoteForward(host, entry.Name, ports)
}

// RunOnHost runs a rox command on another host.
func RunOnHost(host string, argv []string, wait time.Duration) error {
	return runOnHost(host, "rox "+strings.Join(argv[:min(2, len(argv))], " "), argv, wait)
}

type guestRow struct{ host, name, backend, phase, projects string }

// chooseProjectGuest picks the guest that has the current project, here or on another host.
func ChooseProjectGuest(cwd string) (string, string, error) {
	root, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", errors.New("Run rox down from a project checkout or name a workspace: rox down NAME")
	}
	project := filepath.Base(root)
	var rows []guestRow
	for _, g := range LocalGuests() {
		for _, p := range g.Projects {
			if p.Name == project {
				rows = append(rows, guestRow{"", g.Name, g.Backend, g.Phase, g.ProjectSummary()})
				break
			}
		}
	}
	results, order, problems, err := gatherRecords("all")
	if err != nil {
		ui.Warn("Could not check other hosts for workspaces (%v)", err)
	}
	for _, host := range order {
		if host == thisMachine {
			continue
		}
		for _, e := range results[host] {
			for _, c := range e.Checkouts {
				if c.Name == project {
					rows = append(rows, guestRow{host, e.Name, e.Backend, e.Phase, e.Projects})
					break
				}
			}
		}
	}
	for _, host := range sortedKeysString(problems) {
		ui.Warn("Could not check %s for workspaces (%s)", host, problems[host])
	}
	if len(rows) == 0 {
		return "", "", fmt.Errorf("No workspaces for %s. Run rox ls --all --all-projects to see all workspaces", project)
	}
	i, err := pickGuest(rows, fmt.Sprintf("%s has %d workspaces", project, len(rows)), "Remove which workspace?", "rox down NAME")
	if err != nil {
		return "", "", err
	}
	return rows[i].name, rows[i].host, nil
}

func pickGuest(rows []guestRow, title, question, usage string) (int, error) {
	if len(rows) == 1 {
		return 0, nil
	}
	var listing []string
	for i, r := range rows {
		where := ""
		if r.host != "" {
			where = "on " + r.host + "  "
		}
		listing = append(listing, fmt.Sprintf("  %s %s  %s%s  %s  %s", ui.Accent(strconv.Itoa(i+1)+")"), ui.Bold(r.name), where, r.backend, ui.Phase(r.phase), r.projects))
	}
	if !ui.IsTerminal() {
		return 0, fmt.Errorf("%s. Choose one with %s:\n%s", title, usage, ui.StripANSI(strings.Join(listing, "\n")))
	}
	ui.Info("%s", title)
	for _, line := range listing {
		fmt.Fprintln(ui.Stderr(), "  "+line)
	}
	answer := ui.Ask(fmt.Sprintf("%s [1-%d]", question, len(rows)))
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(rows) {
		return 0, errors.New("No workspace selected")
	}
	return n - 1, nil
}

// RecordJSON prints v as JSON to stdout.
func RecordJSON(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(ui.Stdout(), string(data))
}
