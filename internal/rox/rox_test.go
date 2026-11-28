package rox

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

var update = flag.Bool("update", false, "rewrite golden files")

// isolate points rox's state and config at a temporary home and captures ui output.
func isolate(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", dir)
	t.Setenv("ROX_HOOKS", "")
	var out, errs bytes.Buffer
	ui.SetOutput(&out, &errs)
	return &out, &errs
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s; run go test ./... -update", path)
	}
	if string(want) != got {
		t.Errorf("%s differs from its golden file.\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func writeRecord(t *testing.T, name string, record map[string]any) {
	t.Helper()
	dir := filepath.Join(StateRoot(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, "guest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

const sampleName = "rv-web-0123456789ab"

func TestLoadsPythonRecordWithProjects(t *testing.T) {
	isolate(t)
	writeRecord(t, sampleName, map[string]any{
		"name": sampleName, "backend": "container", "image": "rox:linux", "user": "workspace", "engine": "docker",
		"identity": "/x/login", "address": "127.0.0.1", "port": 50022, "guest_socket": "/home/workspace/.rox/" + sampleName + "/r.sock",
		"session_id": "", "registered": true, "tailnet_name": "", "phase": "ready", "container_id": "abc", "cpus": 0, "memory": "",
		"prebuilt": false, "stopped": false, "register_host": true,
		"projects": []any{map[string]any{"id": "web", "name": "web", "url": "git@github.com:a/web.git", "branch": "main", "workspace": "/home/workspace/web", "session_id": "session:1", "phase": "ready"}},
		"funnels":  []any{}, "shell_windows": []any{}, "agents": map[string]any{"claude": "2.1.0"}, "git_keys": []any{"/k"},
		"app": "", "region": "", "disk": 0, "volume": "", "railway_project": "", "railway_environment": "", "railway_idle_timeout": nil,
	})
	g, err := LoadGuest(sampleName)
	if err != nil {
		t.Fatal(err)
	}
	if g.Home() != "/home/workspace" || g.Base() != "/home/workspace/.rox/"+sampleName {
		t.Errorf("home %s base %s", g.Home(), g.Base())
	}
	if len(g.Projects) != 1 || g.Projects[0].SessionID != "session:1" || g.Agents["claude"] != "2.1.0" || g.GitKeys[0] != "/k" {
		t.Errorf("unexpected record %+v", g)
	}
	if !strings.HasSuffix(g.Runtime(), "/0123456789ab") {
		t.Errorf("runtime %s", g.Runtime())
	}
}

func TestMigratesSingleProjectRecord(t *testing.T) {
	isolate(t)
	writeRecord(t, sampleName, map[string]any{"name": sampleName, "backend": "linux", "image": "rox-ubuntu", "user": "admin",
		"project": "My Web", "branch": "dev", "workspace": "/home/admin/web", "session_id": "session:9", "phase": "ready"})
	g, err := LoadGuest(sampleName)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Projects) != 1 {
		t.Fatalf("projects %+v", g.Projects)
	}
	p := g.Projects[0]
	if p.ID != "my-web" || p.Branch != "dev" || p.Workspace != "/home/admin/web" || p.SessionID != "session:9" || g.SessionID != "" {
		t.Errorf("migrated project %+v, guest session %q", p, g.SessionID)
	}
	if g.Port != 22 {
		t.Errorf("default port %d", g.Port)
	}
}

func TestRejectsBadRecords(t *testing.T) {
	isolate(t)
	if _, err := LoadGuest("not-a-guest"); err == nil {
		t.Error("accepted an invalid name")
	}
	if _, err := LoadGuest(sampleName); err == nil || !strings.Contains(err.Error(), "No managed workspace") {
		t.Errorf("missing record: %v", err)
	}
	writeRecord(t, sampleName, map[string]any{"name": "rv-other-0123456789ab", "backend": "container"})
	if _, err := LoadGuest(sampleName); err == nil {
		t.Error("accepted a record for another guest")
	}
}

func TestAddProjectIDs(t *testing.T) {
	g := NewGuest(sampleName, "container", "", "", "")
	a, b, c := g.AddProject("Web App", "u", "main"), g.AddProject("web app", "u", "dev"), g.AddProject("!!!", "u", "x")
	if a.ID != "web-app" || b.ID != "web-app-2" || c.ID != "project" {
		t.Errorf("ids %s %s %s", a.ID, b.ID, c.ID)
	}
	if _, err := g.FindProject("nope"); err == nil || !strings.Contains(err.Error(), "web-app, web-app-2, project") {
		t.Errorf("FindProject error %v", err)
	}
}

func TestProjectSettings(t *testing.T) {
	cases := []struct {
		settings string
		problem  string
	}{
		{`{"backend": "container", "container": {"image": "x", "cpus": 2}}`, ""},
		{`{"agents": {"claude": true, "codex": "1.2.3"}}`, ""},
		{`{"agents": {"claude": "latest"}}`, "Expected true or a version"},
		{`{"agents": {"claude": false}}`, "Leave claude out"},
		{`{"agents": {"claude": {"version": "1.0.0"}}}`, "Expected true or a version"},
		{`{"agents": {"gemini": true}}`, "Unknown agent gemini"},
		{`{"agents": ["claude"]}`, "Expected an object"},
		{`{"backend": "cloud"}`, "Expected backend"},
		{`{"container": {"engine": "docker"}}`, "can only be set in the rox config"},
		{`{"fly": {"app": "mine"}}`, "can only be set in the rox config"},
		{`{"fly": {"image": "x"}}`, "can only be set in the rox config"},
		{`{"git_keys": []}`, "can only be set in the rox config"},
		{`{"linux": {"disk": 3}}`, "Unknown setting linux.disk"},
		{`{"mystery": 1}`, "Unknown setting mystery"},
		{`[]`, "Expected a JSON object"},
	}
	for _, c := range cases {
		var v any
		if err := json.Unmarshal([]byte(c.settings), &v); err != nil {
			t.Fatal(err)
		}
		_, err := CheckProjectSettings(v, ".rox.json")
		switch {
		case c.problem == "" && err != nil:
			t.Errorf("%s: %v", c.settings, err)
		case c.problem != "" && (err == nil || !strings.Contains(err.Error(), c.problem)):
			t.Errorf("%s: want %q, got %v", c.settings, c.problem, err)
		}
	}
}

func TestAgentSpecs(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"claude": true, "codex": "0.9.1"}`), &v)
	spec, err := ParseAgentSpec(v, "x")
	if err != nil || spec["claude"] != "latest" || spec["codex"] != "0.9.1" {
		t.Fatalf("spec %v %v", spec, err)
	}
	flagSpec, err := ParseAgentFlag("codex=1.0.0, claude")
	if err != nil || flagSpec["codex"] != "1.0.0" || flagSpec["claude"] != "latest" {
		t.Fatalf("flag %v %v", flagSpec, err)
	}
	if _, err := ParseAgentFlag("claude=new"); err == nil {
		t.Error("accepted a bad version")
	}
	merged := MergeAgents(AgentSpec{"claude": "1.0.0"}, AgentSpec{"claude": "latest", "codex": "latest"})
	if merged["claude"] != "latest" || len(merged) != 2 {
		t.Errorf("merged %v", merged)
	}
	if got := strings.Join(AgentSpecs(map[string]string{"codex": "1", "claude": "2"}), " "); got != "claude=2 codex=1" {
		t.Errorf("specs %s", got)
	}
}

func TestAgentVersionsUsesPinsAndCache(t *testing.T) {
	isolate(t)
	calls := 0
	LatestAgent = func(agent string) (string, error) { calls++; return "9.9.9", nil }
	defer func() { LatestAgent = latestAgentVersion }()
	v := AgentVersions(AgentSpec{"claude": "latest", "codex": "1.2.3"}, false)
	if v["claude"] != "9.9.9" || v["codex"] != "1.2.3" || calls != 1 {
		t.Fatalf("versions %v after %d checks", v, calls)
	}
	AgentVersions(AgentSpec{"claude": "latest"}, false)
	if calls != 1 {
		t.Errorf("checked again within the cache's lifetime")
	}
	AgentVersions(AgentSpec{"claude": "latest"}, true)
	if calls != 2 {
		t.Errorf("refresh didn't check")
	}
}

func TestLegacyAgentSnippet(t *testing.T) {
	cases := map[string]string{
		`{}`:               `{"agents":{"claude":true,"codex":true}}`,
		`{"codex": false}`: `{"agents":{"claude":true}}`,
		`{"claude": {"version": "1.2.3", "token_file": "x"}, "codex": false}`: `{"agents":{"claude":"1.2.3"}}`,
		`{"claude": false, "codex": false}`:                                   ``,
	}
	for in, want := range cases {
		var m map[string]any
		_ = json.Unmarshal([]byte(in), &m)
		if got := LegacyAgentSnippet(m); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestValidateClone(t *testing.T) {
	restore := sys.NewFake().Install()
	defer restore()
	good := []string{"git@github.com:a/b.git", "ssh://git@host:2222/a/b", "https://github.com/a/b.git", "host:path/repo"}
	bad := []string{"https://user@github.com/a/b", "https://u:p@host/x", "file:///etc", "-oProxyCommand=x:y", "https://host/a?b=1", "ext::sh"}
	for _, u := range good {
		if err := ValidateClone(u, "main"); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := ValidateClone(u, "main"); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
}

func TestRemoteKeysMatchAcrossForms(t *testing.T) {
	if remoteKey("git@GitHub.com:a/b.git") != remoteKey("https://github.com/a/b/") {
		t.Errorf("%s vs %s", remoteKey("git@GitHub.com:a/b.git"), remoteKey("https://github.com/a/b/"))
	}
	if RepoName("git@github.com:a/web.git") != "web" || RepoName("https://h/x/") != "x" {
		t.Error("RepoName")
	}
	refs := []checkoutRef{{"api", "api", ""}, {"web", "web", "git@github.com:a/web.git"}}
	id := &checkoutIdentity{Names: map[string]bool{"site": true}, Remotes: map[string]bool{"github.com/a/web": true}}
	if got := matchingCheckout(refs, "", id); got != "web" {
		t.Errorf("by remote: %q", got)
	}
	if got := matchingCheckout(refs, "api", nil); got != "api" {
		t.Errorf("by name: %q", got)
	}
}

func TestImageNames(t *testing.T) {
	cases := [][4]string{
		{"container", "My App", "rox:linux", "rox:my-app"},
		{"container", "linux", "rox:linux", "rox:linux-project"},
		{"linux", "My App", "rox-ubuntu", "rox-my-app-linux"},
		{"macos", "...", "rox-macos", "rox-project-macos"},
	}
	for _, c := range cases {
		if got := ProjectImageName(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("%v: got %s", c, got)
		}
	}
	if AgentImageName("rox:web") != "rox:web-agents" || AgentImageName("ghcr.io/Me/img:1") != "rox:ghcr.io-me-img-1-agents" {
		t.Errorf("agent images %s %s", AgentImageName("rox:web"), AgentImageName("ghcr.io/Me/img:1"))
	}
}

func TestGeneratedContainerfiles(t *testing.T) {
	project := render(projectContainerfile, map[string]string{"base": "rox:linux", "project": "/home/workspace/web",
		"bake": jsonArgv("bash", "/usr/local/share/rox/bootstrap.sh", "bake", "/home/workspace/web", "container", "rox:web")})
	golden(t, "project.Containerfile", project)
	agent := render(agentContainerfile, map[string]string{"base": "rox:web",
		"install": jsonArgv("env", "HOME=/home/workspace", "bash", "/usr/local/share/rox/bootstrap.sh", "agents", "claude=2.1.0")})
	golden(t, "agents.Containerfile", agent)
}

func TestToolkitLabel(t *testing.T) {
	if assets.Toolkit() != "9" {
		t.Errorf("toolkit %q: bump it whenever the Containerfile or entrypoint changes", assets.Toolkit())
	}
}

func TestServiceUnits(t *testing.T) {
	env := [][2]string{{"PATH", "/usr/bin:/bin"}, {"XDG_STATE_HOME", "/s & t"}}
	golden(t, "rox.plist", LaunchdPlist([]string{"/usr/local/bin/rox", "service", "run"}, env, "/log/service.log"))
	golden(t, "rox.service", SystemdUnit([]string{"/usr/local/bin/rox", "service", "run"}, env, "/log/service.log"))
}

func TestSSHCommand(t *testing.T) {
	isolate(t)
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	g.Identity, g.Address, g.Port = "/id", "127.0.0.1", 5022
	got := strings.Join(remoteCommand(g, []string{"echo", "a b"}, "/tmp/agent.sock", false), " ")
	for _, want := range []string{"-F /dev/null", "HostKeyAlias=" + sampleName, "-i /id -l workspace -p 5022", "ForwardAgent=/tmp/agent.sock", "-- 127.0.0.1 echo 'a b'"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if err := pinHost(g, "not a key"); err == nil {
		t.Error("pinned junk")
	}
}

func TestGuestStagePipesBootstrap(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result { return sys.Result{Stdout: "ok\n"} }
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	out, err := guestStage(g, "server", stageOpts{}, "/sock")
	if err != nil || out != "ok" {
		t.Fatalf("%q %v", out, err)
	}
	calls := fake.Ran("bash -s -- server /sock")
	if len(calls) != 1 || calls[0].Stdin != assets.Bootstrap() {
		t.Errorf("stage call %+v", calls)
	}
}

func TestStageFailureSummarizes(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(sys.Call) sys.Result { return sys.Result{Code: 1, Stderr: "npm ERR! boom\nmore\n"} }
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	_, err := guestStage(g, "setup", stageOpts{})
	if err == nil || err.Error() != ".rox.sh failed in "+sampleName {
		t.Fatalf("error %v", err)
	}
	var out interface{ Output() string }
	if !asOutput(err, &out) || !strings.Contains(out.Output(), "npm ERR! boom") {
		t.Error("the captured output isn't kept for the failure display")
	}
}

func asOutput(err error, target *interface{ Output() string }) bool {
	o, ok := err.(interface{ Output() string })
	*target = o
	return ok
}

func TestHookCallsRixWithRecord(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(c sys.Call) sys.Result {
		return sys.Result{Stdout: `{"schema":1,"agent_socket":"/tmp/a.sock","forward":true,"done":["Git agent holds your key"]}` + "\n"}
	}
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	g.GuestSocket = "/home/workspace/.rox/" + sampleName + "/r.sock"
	g.Agents = map[string]string{"claude": "1.0.0"}
	reply, err := runHook(contract.PreClone, g, &contract.Launch{URL: "git@h:a/b", NoGitAgent: true})
	if err != nil || reply == nil || reply.AgentSocket != "/tmp/a.sock" || !reply.Forward {
		t.Fatalf("reply %+v %v", reply, err)
	}
	calls := fake.Ran("rix hook pre-clone " + sampleName)
	if len(calls) != 1 {
		t.Fatalf("calls %+v", fake.Calls)
	}
	var record contract.Guest
	if err := json.Unmarshal([]byte(calls[0].Stdin), &record); err != nil {
		t.Fatal(err)
	}
	if record.Schema != 1 || record.Home != "/home/workspace" || record.Agents["claude"] != "1.0.0" || !record.Launch.NoGitAgent {
		t.Errorf("record %+v", record)
	}
}

func TestHooksOffAndScoped(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	t.Setenv("ROX_HOOKS", "off")
	if reply, err := runHook(contract.PostBoot, g, nil); reply != nil || err != nil || len(fake.Calls) != 0 {
		t.Errorf("ran a hook with hooks off")
	}
	t.Setenv("ROX_HOOKS", "")
	HookScope = "git"
	defer func() { HookScope = "all" }()
	runHook(contract.PostBoot, g, nil)
	if len(fake.Calls) != 0 {
		t.Errorf("ran post-boot with --hooks=git")
	}
}

func TestFailedPreCloneStopsButOthersWarn(t *testing.T) {
	isolate(t)
	fake := sys.NewFake()
	fake.Handle = func(sys.Call) sys.Result { return sys.Result{Code: 1, Stderr: "no keys"} }
	defer fake.Install()()
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	if _, err := runHook(contract.PreClone, g, nil); err == nil {
		t.Error("a failed pre-clone hook didn't fail")
	}
	if _, err := runHook(contract.PostProvision, g, nil); err != nil {
		t.Errorf("a failed post-provision hook failed: %v", err)
	}
}

func TestListJSONSchema(t *testing.T) {
	out, _ := isolate(t)
	writeRecord(t, sampleName, map[string]any{"name": sampleName, "backend": "container", "image": "rox:linux", "user": "workspace",
		"engine": "container", "address": "192.168.64.4", "guest_socket": "/home/workspace/.rox/" + sampleName + "/r.sock",
		"phase": "ready", "stopped": true, "session_id": "session:g", "agents": map[string]any{"codex": "0.1.0"},
		"projects": []any{map[string]any{"id": "web", "name": "web", "url": "u", "branch": "main", "workspace": "/home/workspace/web", "session_id": "session:w", "phase": "ready"}},
		"funnels":  []any{map[string]any{"port": 3000, "https": 443, "path": "/", "local": 1, "url": "https://x/"}}})
	if err := List("", true, "", true); err != nil {
		t.Fatal(err)
	}
	golden(t, "ls.json", out.String())
	entries, err := parseRecords("garbage before\n" + out.String())
	if err != nil || len(entries) != 1 || entries[0].Phase != "stopped" || entries[0].Sessions[""] != "session:g" {
		t.Errorf("parse back %+v %v", entries, err)
	}
}

func TestParsesOlderListings(t *testing.T) {
	entries, err := parseRecords(`[{"name": "` + sampleName + `", "backend": "linux", "phase": "ready"}]`)
	if err != nil || len(entries) != 1 || entries[0].Backend != "linux" {
		t.Errorf("python list: %+v %v", entries, err)
	}
	entries, err = parseRecords("#rox-plain\n" + sampleName + "\tcontainer\tready\t-\tweb:main\n")
	if err != nil || len(entries) != 1 || entries[0].Projects != "web:main" {
		t.Errorf("plain: %+v %v", entries, err)
	}
}

func TestCardParsesBack(t *testing.T) {
	out, _ := isolate(t)
	g := NewGuest(sampleName, "container", "", "workspace", "docker")
	g.TailnetName, g.SessionID, g.Registered, g.Phase = "rv-web.tail1.ts.net", "session:abc", true, "ready"
	describeGuest(g, nil)
	golden(t, "card.txt", out.String())
	workspace, tailnet, session := parseProvision("\x1b[2m" + out.String())
	if workspace != sampleName || tailnet != "rv-web.tail1.ts.net" || session != "session:abc" {
		t.Errorf("parsed %q %q %q", workspace, tailnet, session)
	}
}

func TestShellDirectory(t *testing.T) {
	isolate(t)
	checkouts := []checkoutDir{{"web", "web", "/h/web"}, {"api", "API", "/h/api"}}
	if d, _ := shellDirectory("g", checkouts, "/h", "API"); d != "/h/api" {
		t.Errorf("by name %s", d)
	}
	if d, _ := shellDirectory("g", checkouts, "/h", ""); d != "/h" {
		t.Errorf("several %s", d)
	}
	if d, _ := shellDirectory("g", checkouts[:1], "/h", ""); d != "/h/web" {
		t.Errorf("only %s", d)
	}
	if _, err := shellDirectory("g", checkouts, "/h", "nope"); err == nil {
		t.Error("unknown project")
	}
}

func TestParseSpecs(t *testing.T) {
	if l, g, err := ParseForward("3000:8080"); err != nil || l != 3000 || g != 8080 {
		t.Error("forward pair")
	}
	if l, g, err := ParseForward("8080"); err != nil || l != 8080 || g != 8080 {
		t.Error("forward single")
	}
	for _, bad := range []string{"0", "1:2:3", "x", "70000"} {
		if _, _, err := ParseForward(bad); err == nil {
			t.Errorf("forward accepted %s", bad)
		}
	}
	if s, d, _ := ParseCopy("/etc/x.env"); s != "/etc/x.env" || d != "x.env" {
		t.Error("absolute copy")
	}
	if _, _, err := ParseCopy("a:"); err == nil {
		t.Error("copy accepted an empty destination")
	}
}

func TestExecPassesThroughStatus(t *testing.T) {
	isolate(t)
	writeRecord(t, sampleName, map[string]any{"name": sampleName, "backend": "container", "user": "workspace", "engine": "docker",
		"identity": "/id", "address": "127.0.0.1", "port": 5022, "guest_socket": "/home/workspace/.rox/" + sampleName + "/r.sock", "phase": "ready"})
	fake := sys.NewFake()
	fake.Handle = func(sys.Call) sys.Result { return sys.Result{Code: 7} }
	defer fake.Install()()
	err := Exec(sampleName, true, "", []string{"sh", "-c", "exit 7"})
	if code, ok := ExitStatus(err); !ok || code != 7 {
		t.Fatalf("status %v", err)
	}
	if len(fake.Ran("ForwardAgent=no", "sh -c 'exit 7'")) != 1 {
		t.Errorf("calls %+v", fake.Calls)
	}
}

func TestKcpassword(t *testing.T) {
	got := Kcpassword("admin")
	if len(got) != 12 || got[0] != 'a'^0x7D || got[5] != 0^0xBC {
		t.Errorf("kcpassword % x", got)
	}
}

func TestMemory(t *testing.T) {
	if memoryMegabytes("4G") != 4096 || memoryMegabytes("512m") != 512 || memoryMegabytes("1K") != 1 {
		t.Error("memory sizes")
	}
}

func TestFlyAppNameHidesEmail(t *testing.T) {
	name := flyAppName("Me@Example.com")
	if name != flyAppName("me@example.com") || strings.Contains(name, "example") || len(name) != 14 {
		t.Errorf("app name %s", name)
	}
}

func TestConfiguredAgentVersionPinsLatest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "rix", "config.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"agents":{"claude":{"version":"2.0.1"},"codex":{"version":"0.5.0"}}}`)
	got, err := WithConfiguredVersions(AgentSpec{"claude": "latest", "codex": "0.9.0"})
	if err != nil || got["claude"] != "2.0.1" || got["codex"] != "0.9.0" {
		t.Errorf("got %v err=%v", got, err)
	}
	// The installer command carries the pinned version.
	if s := strings.Join(AgentSpecs(AgentVersions(got, false)), " "); s != "claude=2.0.1 codex=0.9.0" {
		t.Errorf("specs %s", s)
	}
	write(`{}`)
	if got, _ := WithConfiguredVersions(AgentSpec{"claude": "latest"}); got["claude"] != "latest" {
		t.Errorf("unset should stay latest: %v", got)
	}
	write(`{"agents":{"claude":{"version":"1;id"}}}`)
	if _, err := WithConfiguredVersions(AgentSpec{"claude": "latest"}); err == nil {
		t.Error("invalid version accepted")
	}
}
