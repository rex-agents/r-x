package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Guests on other hosts are reached through the host transport: rox runs on the host that
// runs the guest, and this machine runs rox there. Nothing here needs Rex.

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// remoteRecords runs rox ls on a host and reads its guests, from Go or Python rox.
func remoteRecords(t HostTransport, host string) ([]contract.Entry, error) {
	var code int
	var text string
	var err error
	attempts := []struct {
		argv  []string
		plain bool
	}{{[]string{"ls", "--json", "--all-projects"}, false}, {[]string{"ls", "--json"}, false}, {[]string{"ls"}, true}}
	for _, a := range attempts {
		if code, text, err = t.Capture(host, a.argv, 30*time.Second); err != nil {
			return nil, err
		}
		if code == 127 || strings.Contains(text, "rox: not found") || strings.Contains(text, "rox: command not found") {
			return nil, errors.New("rox isn't installed there")
		}
		if code == 0 {
			if a.plain {
				text = "#rox-plain\n" + text
			}
			return parseRecords(text)
		}
	}
	return nil, fmt.Errorf("rox ls exited with status %d: %s", code, tail(text, 200))
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

const thisMachine = "this machine"

// gatherRecords returns each host's guests, and why any host couldn't be asked. scope is
// "all" for this machine and every host the transport lists, or one host.
func gatherRecords(scope string) (map[string][]contract.Entry, []string, map[string]string, error) {
	t, err := Transport()
	if err != nil {
		return nil, nil, nil, err
	}
	return gatherFrom(t, scope)
}

func gatherFrom(t HostTransport, scope string) (map[string][]contract.Entry, []string, map[string]string, error) {
	var hosts []string
	if scope == "all" {
		listed, err := t.Hosts()
		if err != nil {
			return nil, nil, nil, err
		}
		hosts = listed
	} else {
		hosts = []string{scope}
	}
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
			entries, err := remoteRecords(t, host)
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
	// Two names can reach one host, so keep each guest once.
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

// findRemoteGuest finds the host that runs a guest this machine has no record of.
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
	where := "here or on any known host"
	if host != "" {
		where = "on " + host
	}
	return "", contract.Entry{}, fmt.Errorf("No managed workspace named %s %s.%s", name, where, skipped)
}

// runOnHost runs a rox command on another host and shows what it printed.
func runOnHost(host, label string, argv []string, wait time.Duration) error {
	t, err := Transport()
	if err != nil {
		return err
	}
	s := ui.Start("Running %s on %s", label, host)
	code, text, err := t.Capture(host, argv, wait)
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

// dispatch runs a rox command on a remote host and waits for it, returning what it printed.
func dispatch(host, label string, launch []string) (string, error) {
	t, err := Transport()
	if err != nil {
		return "", err
	}
	s := ui.Start("Provisioning %s on %s", label, host)
	s.Note(t.Name())
	code, text, err := t.Capture(host, launch, 0)
	if err != nil {
		s.Fail(err)
		return "", err
	}
	if code != 0 {
		s.Fail(nil)
		showRemote(text)
		return "", fmt.Errorf("Provisioning exited with status %d on %s", code, host)
	}
	s.Done("Provisioned %s on %s", label, host)
	showRemote(text)
	return text, nil
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

// afterRemoteLaunch tells the user how to reach a guest launched on another host, and opens
// it when --open-shell asked.
func afterRemoteLaunch(text, host string, open bool) {
	workspace, _, _ := parseProvision(text)
	if workspace == "" {
		return
	}
	if open {
		if err := OpenGuest(workspace, "", host); err != nil {
			ui.Warn("%v", err)
		}
		return
	}
	ui.Hint("Open a shell in it with " + ui.Code("rox shell "+workspace+" --host "+host))
}

// remoteShell opens a shell in a guest on another host: rox shell there, over the transport.
func remoteShell(host string, name, key string) error {
	t, err := Transport()
	if err != nil {
		return err
	}
	argv := []string{"shell", name}
	if key != "" {
		argv = append(argv, key)
	}
	return runRemote(t.Command(host, CommandOptions{TTY: true}, argv))
}

// remoteExec runs argv in a guest on another host: rox exec there, over the transport.
func remoteExec(host, name string, stdin bool, agentSock string, argv []string) error {
	if agentSock != "" {
		return errors.New("--agent-sock can't be forwarded to a guest on another host")
	}
	t, err := Transport()
	if err != nil {
		return err
	}
	rox := []string{"exec", name}
	if stdin {
		rox = append(rox, "--stdin")
	}
	rox = append(append(rox, "--"), argv...)
	command, err := t.Command(host, CommandOptions{}, rox)
	if err != nil {
		return err
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

// remoteForward forwards launcher ports to a guest on another host: this machine's port
// reaches the host's, and rox forward there carries it into the guest.
func remoteForward(host string, name string, ports [][2]int) error {
	t, err := Transport()
	if err != nil {
		return err
	}
	ui.Header("forward", name+" on "+host)
	argv := []string{"forward", name}
	var forwards [][2]int
	for _, p := range ports {
		ui.Done("%s %s port %d on %s %s", ui.Accent(fmt.Sprintf("http://localhost:%d", p[0])), ui.Muted("→"), p[1], name, ui.Muted("· through "+host))
		argv = append(argv, fmt.Sprintf("%d:%d", p[1], p[1]))
		forwards = append(forwards, [2]int{p[0], p[1]})
	}
	ui.Info("Press Ctrl-C to stop")
	return runRemote(t.Command(host, CommandOptions{Forwards: forwards}, argv))
}
