package rox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

// noRex is a machine with ssh and docker but no rex, rix or rox-rex anywhere on PATH.
func noRex(t *testing.T, handle func(sys.Call) sys.Result) *sys.Fake {
	t.Helper()
	isolate(t)
	fake := sys.NewFake()
	fake.Tools = map[string]bool{"ssh": true, "ssh-keygen": true, "docker": true, "git": true}
	fake.Handle = func(c sys.Call) sys.Result {
		line := c.Line()
		switch {
		case strings.Contains(line, "inspect --format {{.State.Running}}"):
			return sys.Result{Stdout: "true\n"}
		case strings.Contains(line, "docker port"):
			return sys.Result{Stdout: "127.0.0.1:5022\n"}
		case handle != nil:
			return handle(c)
		}
		return sys.Result{}
	}
	t.Cleanup(fake.Install())
	return fake
}

func noRexCalls(t *testing.T, fake *sys.Fake) {
	t.Helper()
	for _, c := range fake.Calls {
		if sys.Base(c.Argv[0]) == "rex" || sys.Base(c.Argv[0]) == "rox-rex" || strings.Contains(c.Line(), "rex --autostart") {
			t.Errorf("ran rex: %s", c.Line())
		}
	}
}

func readyRecord(t *testing.T) {
	t.Helper()
	writeRecord(t, sampleName, map[string]any{"name": sampleName, "backend": "container", "user": "workspace", "engine": "docker",
		"identity": "/id", "address": "127.0.0.1", "port": 5022, "container_id": "cid", "phase": "ready",
		"guest_socket": "/home/workspace/.rox/" + sampleName + "/r.sock",
		"projects":     []any{map[string]any{"id": "web", "name": "web", "workspace": "/home/workspace/web", "phase": "ready"}}})
}

func TestShellExecStartStopDownNeedNoRex(t *testing.T) {
	fake := noRex(t, nil)
	readyRecord(t)
	if err := Shell(sampleName, ""); err != nil {
		t.Fatal(err)
	}
	shell := fake.Ran("-t")
	if len(shell) == 0 || strings.Contains(shell[len(shell)-1].Line(), "REX_") || !strings.Contains(shell[len(shell)-1].Line(), "SSH_AUTH_SOCK=") {
		t.Errorf("shell command %+v", shell)
	}
	if err := Exec(sampleName, false, "", []string{"true"}); err != nil {
		t.Fatal(err)
	}
	if err := Reconnect(sampleName, false); err != nil {
		t.Fatal(err)
	}
	if err := Stop(sampleName); err != nil {
		t.Fatal(err)
	}
	if err := Reconnect(sampleName, true); err != nil {
		t.Fatal(err)
	}
	if err := Down(sampleName); err != nil {
		t.Fatal(err)
	}
	noRexCalls(t, fake)
	if IsLocal(sampleName) {
		t.Error("down left the guest")
	}
}

func TestOpenShellFallsBackToRoxShell(t *testing.T) {
	fake := noRex(t, nil)
	readyRecord(t)
	if err := OpenGuest(sampleName, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.Ran("-t", "SSH_AUTH_SOCK=")) != 1 {
		t.Errorf("no shell ran: %+v", fake.Calls)
	}
	noRexCalls(t, fake)
}

func TestOpenShellHandledByProvider(t *testing.T) {
	fake := noRex(t, nil)
	fake.Tools["rox-rex"] = true
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "rox-rex hook open-shell") {
			return sys.Result{Stdout: `{"schema":1,"done":["Opened in the Rex app"]}` + "\n"}
		}
		if strings.Contains(c.Line(), "inspect --format") {
			return sys.Result{Stdout: "true\n"}
		}
		return sys.Result{}
	}
	readyRecord(t)
	if err := OpenGuest(sampleName, "web", ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.Ran("SSH_AUTH_SOCK=")) != 0 {
		t.Error("a handled open-shell still ran a shell here")
	}
	calls := fake.Ran("rox-rex hook open-shell")
	var record contract.Guest
	if len(calls) != 1 || json.Unmarshal([]byte(calls[0].Stdin), &record) != nil || record.Launch.Project != "web" {
		t.Errorf("payload %+v", calls)
	}
}

func TestPostProvisionRecordsSessionOnlyFromAProvider(t *testing.T) {
	fake := noRex(t, nil)
	readyRecord(t)
	g, _ := LoadGuest(sampleName)
	p := g.Projects[0]
	postProvision(g, p, nil, &contract.Launch{Command: []string{"make", "dev"}})
	if p.SessionID != "" {
		t.Errorf("session %q without a provider", p.SessionID)
	}
	fake.Tools["rox-rex"] = true
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "rox-rex hook post-provision") {
			return sys.Result{Stdout: `{"schema":1,"session":"session:7","endpoint":"g.ts.net"}` + "\n"}
		}
		return sys.Result{}
	}
	postProvision(g, p, nil, &contract.Launch{})
	if p.SessionID != "session:7" || g.TailnetName != "g.ts.net" {
		t.Errorf("session %q tailnet %q", p.SessionID, g.TailnetName)
	}
	g.Projects = nil
	postProvision(g, nil, nil, &contract.Launch{})
	if g.SessionID != "session:7" {
		t.Errorf("guest session %q", g.SessionID)
	}
}

func TestRemoteGuestsGoOverSSHToTheHost(t *testing.T) {
	remoteList := `{"schema":1,"guests":[{"name":"` + sampleName + `","backend":"container","phase":"ready","tailnet":"","checkouts":[],"sessions":{}}]}`
	fake := noRex(t, func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "ls --json") {
			return sys.Result{Stdout: remoteList + "\n"}
		}
		return sys.Result{}
	})
	t.Setenv("ROX_HOSTS", "mini.tail1.ts.net")
	if err := Exec(sampleName, true, "", []string{"echo", "hi"}); err != nil {
		t.Fatal(err)
	}
	exec := fake.Ran("exec "+sampleName, "--stdin", "echo hi")
	if len(exec) != 1 || exec[0].Argv[0] != "ssh" || !strings.Contains(exec[0].Line(), "-T -- mini.tail1.ts.net") {
		t.Fatalf("exec %+v", fake.Calls)
	}
	if err := RouteShell("mini.tail1.ts.net", contract.Entry{Name: sampleName}, "web"); err != nil {
		t.Fatal(err)
	}
	shell := fake.Ran("shell " + sampleName + " web")
	if len(shell) != 1 || !strings.Contains(shell[0].Line(), "-t -- mini.tail1.ts.net") {
		t.Fatalf("shell %+v", fake.Calls)
	}
	if err := RouteForward("mini.tail1.ts.net", contract.Entry{Name: sampleName}, [][2]int{{3000, 8080}}); err != nil {
		t.Fatal(err)
	}
	if f := fake.Ran("-L 127.0.0.1:3000:127.0.0.1:8080", "forward "+sampleName+" 8080:8080"); len(f) != 1 {
		t.Fatalf("forward %+v", fake.Calls)
	}
	if err := Exec(sampleName, false, "/tmp/agent", []string{"true"}); err == nil {
		t.Error("forwarded an agent socket to another host")
	}
	noRexCalls(t, fake)
}

func TestListAllAsksConfiguredHostsOverSSH(t *testing.T) {
	remoteList := `{"schema":1,"guests":[{"name":"` + sampleName + `","backend":"container","phase":"ready","checkouts":[],"sessions":{}}]}`
	fake := noRex(t, func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "ls --json") {
			return sys.Result{Stdout: remoteList + "\n"}
		}
		return sys.Result{}
	})
	t.Setenv("ROX_HOSTS", "mini,studio")
	results, order, problems, err := gatherRecords("all")
	if err != nil || len(problems) != 0 {
		t.Fatal(err, problems)
	}
	// Both hosts report one guest name, so it's kept once.
	if len(order) != 3 || len(results["mini"])+len(results["studio"]) != 1 {
		t.Errorf("order %v results %v", order, results)
	}
	if len(fake.Ran("ssh", "mini")) == 0 || len(fake.Ran("ssh", "studio")) == 0 {
		t.Errorf("calls %+v", fake.Calls)
	}
}

func TestHostTransportSelection(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Tools = map[string]bool{"rox-rex": true}
	defer fake.Install()()
	if tr, err := Transport(); err != nil || tr.Name() != "ssh" {
		t.Errorf("default %v %v", tr, err)
	}
	t.Setenv("ROX_HOST_TRANSPORT", "rox-rex")
	tr, err := Transport()
	if err != nil || tr.Name() != "rox-rex" {
		t.Fatalf("%v %v", tr, err)
	}
	argv, _ := tr.Command("studio", CommandOptions{TTY: true}, []string{"shell", "g"})
	if strings.Join(argv, " ") != "/usr/bin/rox-rex host run --tty studio -- shell g" {
		t.Errorf("command %v", argv)
	}
	if _, err := tr.Command("studio", CommandOptions{Forwards: [][2]int{{1, 2}}}, []string{"forward"}); err == nil {
		t.Error("a command transport accepted port forwards")
	}
	t.Setenv("ROX_HOST_TRANSPORT", "nonesuch")
	fake.Tools = map[string]bool{}
	if _, err := Transport(); err == nil {
		t.Error("accepted a missing transport")
	}
}
