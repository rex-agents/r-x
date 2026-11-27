package roxrex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

const guestName = "rv-web-0123456789ab"

func record(t *testing.T, launch *contract.Launch) (contract.Guest, string) {
	t.Helper()
	dir := t.TempDir()
	return contract.Guest{Schema: 1, Name: guestName, Phase: "ready", Socket: "/home/u/.rox/" + guestName + "/r.sock", Home: "/home/u",
		Dir: dir, Runtime: filepath.Join(dir, "run"), Agents: map[string]string{},
		Projects: []contract.Project{{ID: "web", Name: "web", Workspace: "/home/u/web", Phase: "ready"}}, Launch: launch}, dir
}

func call(t *testing.T, event string, g contract.Guest) (contract.Reply, error) {
	t.Helper()
	data, _ := json.Marshal(g)
	var out bytes.Buffer
	err := Hook(event, g.Name, strings.NewReader(string(data)+"\n"), &out)
	var reply contract.Reply
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &reply); jerr != nil {
			t.Fatalf("not JSON: %q", out.String())
		}
	}
	return reply, err
}

func TestHookRejectsBadInput(t *testing.T) {
	isolate(t)
	g, _ := record(t, nil)
	data, _ := json.Marshal(g)
	if err := Hook("resume", "other", strings.NewReader(string(data)+"\n"), &bytes.Buffer{}); err == nil {
		t.Error("accepted another guest's record")
	}
	g.Schema = 2
	if _, err := call(t, "resume", g); err == nil {
		t.Error("accepted schema 2")
	}
	if err := Hook("resume", "x", strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Error("accepted an empty stdin")
	}
}

func TestUnknownEventsGetAnEmptyReply(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	defer fake.Install()()
	g, _ := record(t, nil)
	for _, event := range []string{"pre-fetch", "pre-clone", "something-new"} {
		reply, err := call(t, event, g)
		if err != nil || reply.Schema != 1 || len(reply.Done)+len(reply.Warnings) != 0 || len(fake.Calls) != 0 {
			t.Errorf("%s: %+v %v %v", event, reply, err, fake.Calls)
		}
	}
}

func TestPostBootInstallsStartsAndReaches(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.HasPrefix(c.Line(), "rex version") {
			return sys.Result{Stdout: "rex 1.2.3\n"}
		}
		return sys.Result{}
	}
	defer fake.Install()()
	t.Setenv("ROX_BIN", "/bin/rox")
	g, _ := record(t, &contract.Launch{NoTailscale: true})
	reply, err := call(t, contract.PostBoot, g)
	if err != nil || len(reply.Warnings) != 0 || len(reply.Done) != 1 || !strings.Contains(reply.Done[0], "Rex is running") {
		t.Fatalf("reply %+v %v", reply, err)
	}
	install := fake.Ran("/bin/rox exec "+guestName+" -- bash -c", "rox-rex install stable")
	server := fake.Ran("rox-rex server " + g.Socket)
	if len(install) != 1 || len(server) != 1 {
		t.Fatalf("calls %v", fake.Calls)
	}
	if len(fake.Ran("rex --autostart=false --timeout 5s -S unix://"+g.Runtime+"/rex.sock ls --json")) == 0 {
		t.Errorf("never checked the launcher reaches Rex: %v", fake.Calls)
	}
	// Everything reaches the guest through rox exec; nothing runs ssh.
	for _, c := range fake.Calls {
		if c.Argv[0] == "ssh" {
			t.Errorf("ran ssh: %s", c.Line())
		}
	}
}

func TestPostBootWithoutRexInTheGuestIsAWarning(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "rox-rex install") {
			return sys.Result{Code: 3, Stderr: "Install the Rex app in the guest\n"}
		}
		return sys.Result{}
	}
	defer fake.Install()()
	g, _ := record(t, nil)
	reply, err := call(t, contract.PostBoot, g)
	if err != nil || len(reply.Warnings) != 1 || len(fake.Ran("rox-rex server")) != 0 {
		t.Errorf("reply %+v %v", reply, err)
	}
	// Later events leave a guest without Rex alone.
	fake.Calls = nil
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "rox-rex server") {
			return sys.Result{Code: 1, Stderr: "Rex isn't installed in the guest.\n"}
		}
		if strings.HasPrefix(c.Line(), "rex ") {
			return sys.Result{Code: 1}
		}
		return sys.Result{}
	}
	for _, event := range []string{contract.Resume, contract.PostProvision} {
		if reply, err := call(t, event, g); err != nil || len(reply.Warnings) != 0 || reply.Session != "" {
			t.Errorf("%s: %+v %v", event, reply, err)
		}
	}
}

func TestPostProvisionCreatesTheSessionAndRegisters(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		line := c.Line()
		switch {
		case strings.Contains(line, "rex --autostart=false -S unix://") && strings.Contains(line, " new "):
			return sys.Result{Stdout: `{"session_id":"session:42"}` + "\n"}
		case strings.HasPrefix(line, "rex hosts list"):
			return sys.Result{Stdout: `{"hosts":[]}`}
		case strings.Contains(line, "server status"):
			return sys.Result{Stdout: `{}`}
		}
		return sys.Result{Stdout: "{}"}
	}
	defer fake.Install()()
	g, dir := record(t, &contract.Launch{Project: "web", Command: []string{"make", "dev"}})
	reply, err := call(t, contract.PostProvision, g)
	if err != nil || reply.Session != "session:42" {
		t.Fatalf("reply %+v %v", reply, err)
	}
	created := fake.Ran(" new web -c /home/u/web --json --keep-open -- make dev")
	if len(created) != 1 || !strings.Contains(created[0].Line(), "exec "+guestName) {
		t.Errorf("session call %v", fake.Calls)
	}
	if len(fake.Ran("rex hosts add "+guestName+" unix://"+g.Runtime+"/rex.sock")) != 1 {
		t.Errorf("not registered: %v", fake.Calls)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "rex.json"))
	if !strings.Contains(string(data), `"registered": true`) {
		t.Errorf("state %s", data)
	}
	// A second call neither registers again nor creates a session for a project that has one.
	fake.Calls = nil
	g.Projects[0].Session = "session:42"
	reply, err = call(t, contract.PostProvision, g)
	if err != nil || reply.Session != "" || len(fake.Ran(" new ")) != 0 || len(fake.Ran("hosts add")) != 0 {
		t.Errorf("repeat: %+v %v %v", reply, err, fake.Calls)
	}
}

func TestGuestWithoutProjectsGetsItsOwnSession(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), " new ") {
			return sys.Result{Stdout: `{"session_id":"session:g"}`}
		}
		return sys.Result{Stdout: "{}"}
	}
	defer fake.Install()()
	g, _ := record(t, &contract.Launch{})
	g.Projects = nil
	reply, err := call(t, contract.PostProvision, g)
	if err != nil || reply.Session != "session:g" || len(fake.Ran(" new "+guestName+" -c /home/u --json")) != 1 {
		t.Errorf("%+v %v %v", reply, err, fake.Calls)
	}
}

func TestDownRemovesHostAndForgetsLegacyRecords(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	defer fake.Install()()
	g, dir := record(t, nil)
	// A guest registered before Rex moved into this hook has it in guest.json.
	_ = os.WriteFile(filepath.Join(dir, "guest.json"), []byte(`{"registered": true, "shell_windows": []}`), 0o600)
	reply, err := call(t, contract.Down, g)
	if err != nil || len(reply.Done) != 1 || len(fake.Ran("rex hosts remove "+guestName)) != 1 {
		t.Errorf("%+v %v %v", reply, err, fake.Calls)
	}
	// A record rox down --host sends has no folder: the host is removed by name.
	fake.Calls = nil
	reply, err = call(t, contract.Down, contract.Guest{Schema: 1, Name: guestName})
	if err != nil || len(fake.Ran("rex hosts remove "+guestName)) != 1 {
		t.Errorf("%+v %v %v", reply, err, fake.Calls)
	}
	// A guest that was never registered is left alone.
	fake.Calls = nil
	g2, _ := record(t, nil)
	_, _ = call(t, contract.Down, g2)
	if len(fake.Ran("hosts remove")) != 0 {
		t.Errorf("removed a host that was never added: %v", fake.Calls)
	}
}

func TestOpenShellNeedsAnAppOrARexTerminal(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		line := c.Line()
		switch {
		case strings.Contains(line, "client ls"):
			return sys.Result{Stdout: `{"clients":[]}`}
		case strings.Contains(line, "window new"):
			return sys.Result{Stdout: `{"window_id":"win:1"}`}
		}
		return sys.Result{Stdout: "{}"}
	}
	defer fake.Install()()
	t.Setenv("REX_SESSION", "")
	t.Setenv("ROX_BIN", "/bin/rox")
	g, _ := record(t, &contract.Launch{Project: "web"})
	reply, err := call(t, contract.OpenShell, g)
	if err != nil || len(reply.Done) != 0 {
		t.Errorf("no app and no Rex terminal still claimed a shell: %+v %v", reply, err)
	}
	t.Setenv("REX_SESSION", "session:here")
	reply, err = call(t, contract.OpenShell, g)
	if err != nil || len(reply.Done) != 1 || len(fake.Ran("window new -s session:here", "/bin/rox shell "+guestName+" web")) != 1 {
		t.Errorf("tab: %+v %v %v", reply, err, fake.Calls)
	}
	if st := loadState(&g); len(st.Windows) != 1 || st.Windows[0].Window != "win:1" {
		t.Errorf("state %+v", st)
	}
}

func TestOpenShellInTheAppShowsTheSession(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "client ls") {
			return sys.Result{Stdout: `{"clients":[{"client_id":"c1","connection_state":"connected","info":{"kind":"app","label":"Rex"},"principal":{"transport":"unix"}}]}`}
		}
		return sys.Result{Stdout: "{}"}
	}
	defer fake.Install()()
	g, _ := record(t, &contract.Launch{Project: "web"})
	g.Projects[0].Session = "session:5"
	reply, err := call(t, contract.OpenShell, g)
	if err != nil || len(reply.Done) != 1 || len(fake.Ran("-C c1 do session.select session_id=session:5")) != 1 {
		t.Errorf("%+v %v %v", reply, err, fake.Calls)
	}
}
