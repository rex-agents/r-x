package roxrex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rex-agents/r-x/internal/rexbridge"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// isolate points state and config at a temporary home and captures ui output.
func isolate(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", dir)
	t.Setenv("ROX_HOOKS", "")
	t.Setenv("REX_CONFIG", "")
	var out, errs bytes.Buffer
	ui.SetOutput(&out, &errs)
	return &out, &errs
}

func TestEveryCommandHasARexSpec(t *testing.T) {
	isolate(t)
	programs := bridgePrograms("test")
	if missing := rexbridge.Missing(programs[0].Root, rexbridge.RoxSpecs); len(missing) > 0 {
		t.Errorf("rox commands without a Rex spec: %v", missing)
	}
	if missing := rexbridge.Missing(programs[1].Root, rexbridge.RixSpecs); len(missing) > 0 {
		t.Errorf("rix commands without a Rex spec: %v", missing)
	}
	lua := BridgeLua("test")
	for _, name := range []string{`"rox_up"`, `"rox_funnel_add"`, `"rix_auth"`, `"rix_git_agent_start"`} {
		if !strings.Contains(lua, name) {
			t.Errorf("no action %s", name)
		}
	}
	for _, gone := range []string{`"rox_agent"`, `"rox_update_agents"`, `"rox_service_run"`, `"rox_rex_install"`, `"rix_hook"`} {
		if strings.Contains(lua, gone) {
			t.Errorf("action %s shouldn't exist", gone)
		}
	}
}

func TestBridgeInstallEditsInitOnce(t *testing.T) {
	isolate(t)
	init := rexInit()
	_ = os.MkdirAll(filepath.Dir(init), 0o755)
	_ = os.WriteFile(init, []byte("rex.bind(\"cmd+k\", \"x\")"), 0o644)
	for i := 0; i < 2; i++ {
		if err := InstallBridge("test", false); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(init)
	if strings.Count(string(data), `require("rox")`) != 1 || !strings.HasPrefix(string(data), `rex.bind("cmd+k", "x")`) {
		t.Errorf("init.lua:\n%s", data)
	}
	if err := UninstallBridge(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(init)
	if strings.Contains(string(data), "require") || sys.IsFile(filepath.Join(filepath.Dir(init), "rox.lua")) {
		t.Errorf("uninstall left %s", data)
	}
}
