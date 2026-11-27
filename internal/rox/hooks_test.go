package rox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
)

func names(ps []Provider) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return strings.Join(out, ",")
}

func TestParseProviderNames(t *testing.T) {
	cases := []struct {
		in   any
		want string
		ok   bool
	}{
		{"rix,rox-rex", "rix,rox-rex", true},
		{" rox-rex , rix ", "rox-rex,rix", true},
		{[]any{"rix", "rox-rex"}, "rix,rox-rex", true},
		{"off", "", true},
		{"none", "", true},
		{[]any{"rix", 3.0}, "", false},
		{nil, "", false},
		{4.0, "", false},
	}
	for _, c := range cases {
		got, ok := ParseProviderNames(c.in)
		if ok != c.ok || strings.Join(got, ",") != c.want {
			t.Errorf("%v: got %v %v", c.in, got, ok)
		}
	}
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	path := ConfigLocation("")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHookProvidersDefaultConfigAndEnv(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	defer fake.Install()()

	if got, _ := HookProviders(); names(got) != "rix,rox-rex" {
		t.Errorf("default %s", names(got))
	}
	fake.Tools = map[string]bool{"rox-rex": true}
	if got, _ := HookProviders(); names(got) != "rox-rex" {
		t.Errorf("only rox-rex on PATH: %s", names(got))
	}
	fake.Tools = map[string]bool{}
	if got, _ := HookProviders(); len(got) != 0 {
		t.Errorf("none on PATH: %s", names(got))
	}
	fake.Tools = nil

	writeConfig(t, `{"hooks": ["rox-rex", "rix"]}`)
	if got, where := HookProviders(); names(got) != "rox-rex,rix" || !strings.HasPrefix(where, "hooks in ") {
		t.Errorf("config list %s (%s)", names(got), where)
	}
	writeConfig(t, `{"hooks": "rix"}`)
	if got, _ := HookProviders(); names(got) != "rix" {
		t.Errorf("config string %s", names(got))
	}
	writeConfig(t, `{"hooks": "off"}`)
	if got, _ := HookProviders(); len(got) != 0 {
		t.Errorf("config off %s", names(got))
	}
	t.Setenv("ROX_HOOKS", "rox-rex,rix")
	if got, where := HookProviders(); names(got) != "rox-rex,rix" || where != "ROX_HOOKS" {
		t.Errorf("env %s (%s)", names(got), where)
	}
	t.Setenv("ROX_HOOKS", "rix")
	fake.Tools = map[string]bool{"rox-rex": true}
	if got, _ := HookProviders(); len(got) != 0 {
		t.Errorf("a named provider that isn't installed was kept: %s", names(got))
	}
}

func TestMergeReplies(t *testing.T) {
	got := MergeReplies([]*contract.Reply{
		{Done: []string{"a"}, Warnings: []string{"w1"}, AgentSocket: "/one", Forward: true, KnownHosts: "k1"},
		nil,
		{Done: []string{"b"}, AgentSocket: "/two", Forward: false, KnownHosts: "k2", Session: "session:1", Endpoint: "x.ts.net"},
		{Done: []string{"c"}, Warnings: []string{"w2"}, Session: "session:2", Endpoint: "y.ts.net"},
	})
	if strings.Join(got.Done, "") != "abc" || strings.Join(got.Warnings, "") != "w1w2" {
		t.Errorf("lists %+v", got)
	}
	if got.AgentSocket != "/one" || !got.Forward || got.KnownHosts != "k1" || got.Session != "session:1" || got.Endpoint != "x.ts.net" {
		t.Errorf("first non-empty wins: %+v", got)
	}
	// forward comes with the socket that won, not a later provider's.
	got = MergeReplies([]*contract.Reply{{Done: []string{"a"}}, {AgentSocket: "/two", Forward: true}})
	if got.AgentSocket != "/two" || !got.Forward {
		t.Errorf("%+v", got)
	}
}

func TestProvidersCalledInOrderAndMerged(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		switch {
		case strings.Contains(c.Line(), "rix hook"):
			return sys.Result{Stdout: `{"schema":1,"done":["rix did"],"agent_socket":"/a.sock","forward":true}` + "\n"}
		case strings.Contains(c.Line(), "rox-rex hook"):
			return sys.Result{Stdout: `{"schema":1,"done":["rex did"],"session":"session:9","endpoint":"g.ts.net"}` + "\n"}
		}
		return sys.Result{}
	}
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	g.Agents = map[string]string{"claude": "1"}
	reply, err := runHook(contract.PostProvision, g, &contract.Launch{Project: "web"})
	if err != nil || reply == nil || reply.Session != "session:9" || reply.AgentSocket != "/a.sock" || reply.Endpoint != "g.ts.net" || len(reply.Done) != 2 {
		t.Fatalf("reply %+v %v", reply, err)
	}
	if fake.Calls[0].Argv[0] != "/usr/bin/rix" || fake.Calls[1].Argv[0] != "/usr/bin/rox-rex" {
		t.Errorf("order %v / %v", fake.Calls[0].Argv, fake.Calls[1].Argv)
	}
	var record contract.Guest
	if err := json.Unmarshal([]byte(fake.Calls[1].Stdin), &record); err != nil || record.Launch.Project != "web" || record.Runtime == "" || record.Dir == "" {
		t.Errorf("payload %+v %v", record, err)
	}
}

func TestProvidersOnlyGetTheirEvents(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	// rix knows no open-shell, and has nothing to sign in without agents; rox-rex knows no pre-clone.
	runHook(contract.OpenShell, g, nil)
	runHook(contract.PreClone, g, nil)
	runHook(contract.PostBoot, g, nil)
	var lines []string
	for _, c := range fake.Calls {
		lines = append(lines, strings.TrimPrefix(c.Line(), "/usr/bin/"))
	}
	want := "rox-rex hook open-shell " + sampleName + "|rix hook pre-clone " + sampleName + "|rox-rex hook post-boot " + sampleName
	if strings.Join(lines, "|") != want {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(lines, "|"), want)
	}
	g.Agents = map[string]string{"claude": "1"}
	fake.Calls = nil
	runHook(contract.PostBoot, g, nil)
	if len(fake.Calls) != 2 || !strings.Contains(fake.Calls[0].Line(), "rix hook post-boot") {
		t.Errorf("post-boot with agents: %+v", fake.Calls)
	}
}

func TestOneProviderFailingLeavesTheOthers(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		if strings.Contains(c.Line(), "rix hook") {
			return sys.Result{Code: 1, Stderr: "boom"}
		}
		return sys.Result{Stdout: `{"schema":1,"session":"session:3"}` + "\n"}
	}
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	reply, err := runHook(contract.PostProvision, g, nil)
	if err != nil || reply == nil || reply.Session != "session:3" {
		t.Errorf("reply %+v %v", reply, err)
	}
}

func TestShowHooksListsEveryProvider(t *testing.T) {
	out, _ := isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(sys.Call) sys.Result { return sys.Result{Stdout: "1.0.0\n"} }
	defer fake.Install()()
	if err := ShowHooks(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rix", "rox-rex", "open-shell", "rix,rox-rex"} {
		if !strings.Contains(strings.ReplaceAll(out.String(), ", ", ","), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}

func TestCredentialsFlagReachesEveryProviderAndRemoteLaunches(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	o := &UpOptions{NoCredentials: true, Command: []string{"make"}}
	launch := o.launch("")
	launch.Command = o.Command
	postProvision(g, nil, nil, launch)
	if len(fake.Calls) != 2 {
		t.Fatalf("calls %v", fake.Calls)
	}
	for _, c := range fake.Calls {
		var rec contract.Guest
		if json.Unmarshal([]byte(c.Stdin), &rec) != nil || rec.Launch == nil || rec.Launch.WantCredentials() {
			t.Errorf("%s got %s", c.Argv[0], c.Stdin)
		}
	}
	if !strings.Contains(strings.Join(remoteOptions(o), " "), "--credentials=false") {
		t.Errorf("remote options %v", remoteOptions(o))
	}
}
