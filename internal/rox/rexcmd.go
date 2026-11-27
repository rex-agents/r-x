package rox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rex-agents/r-x/internal/rexbridge"
	"github.com/rex-agents/r-x/internal/rix"
	"github.com/rex-agents/r-x/internal/selfupdate"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

const rexModule = "rox"

// requireLine is what init.lua needs to load the bridge.
const requireLine = `require("rox") -- rox and rix as Rex actions; managed by rox rex install`

func rexConfigDir() string {
	if p := os.Getenv("REX_CONFIG"); p != "" {
		return filepath.Dir(sys.Expand(p))
	}
	return filepath.Join(sys.XDG("XDG_CONFIG_HOME", ".config"), "rex")
}

func rexInit() string {
	if p := os.Getenv("REX_CONFIG"); p != "" {
		return sys.Expand(p)
	}
	return filepath.Join(rexConfigDir(), "init.lua")
}

// bridgePrograms are rox and rix with the paths the Rex server should run, since its PATH may lack them.
func bridgePrograms(version string) []rexbridge.Program {
	roxRoot := rootCommand(version, &selfupdate.Updater{Program: "rox", Version: version})
	rexbridge.AnnotateTree(roxRoot, rexbridge.RoxSpecs)
	rixRoot := rix.RootCommand(version, &selfupdate.Updater{Program: "rix", Version: version})
	rexbridge.AnnotateTree(rixRoot, rexbridge.RixSpecs)
	if p, err := sys.R.LookPath("rex"); err == nil {
		rexbridge.RexBin = sys.Resolve(p)
	}
	rixBin := ""
	if p, err := sys.R.LookPath("rix"); err == nil {
		rixBin = sys.Resolve(p)
	} else if sibling := filepath.Join(filepath.Dir(sys.Resolve(selfPath())), "rix"); sys.IsFile(sibling) {
		rixBin = sibling
	}
	return []rexbridge.Program{
		{Name: "rox", Bin: sys.Resolve(selfPath()), Category: "Rox", Root: roxRoot},
		{Name: "rix", Bin: rixBin, Category: "Rix", Root: rixRoot},
	}
}

// BridgeLua is the generated Lua module.
func BridgeLua(version string) string {
	programs := bridgePrograms(version)
	return rexbridge.Lua(programs, rexbridge.Actions(programs))
}

func rexCommand(version string) *cobra.Command {
	c := &cobra.Command{Use: "rex", Short: "make every rox and rix command a Rex action"}
	c.AddCommand(&cobra.Command{
		Use:   "lua",
		Short: "print the Lua module that defines the actions",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprint(ui.Stdout(), BridgeLua(version))
			return nil
		},
	})
	var noReload, refresh bool
	install := &cobra.Command{
		Use:   "install",
		Short: "write the actions beside Rex's init.lua, load them from it, and reload Rex",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if refresh {
				RefreshBridge(version)
				return nil
			}
			return InstallBridge(version, !noReload)
		},
	}
	install.Flags().BoolVar(&noReload, "no-reload", false, "don't run rex config reload")
	install.Flags().BoolVar(&refresh, "refresh", false, "only rewrite the actions if they're installed, as updates do")
	_ = install.Flags().MarkHidden("refresh")
	c.AddCommand(install, &cobra.Command{
		Use:   "uninstall",
		Short: "remove the actions and their line in init.lua",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return UninstallBridge() },
	})
	return c
}

// InstallBridge writes rox.lua beside init.lua, requires it there, and reloads Rex.
func InstallBridge(version string, reload bool) error {
	ui.Header("rex install", "")
	programs := bridgePrograms(version)
	actions := rexbridge.Actions(programs)
	module := filepath.Join(rexConfigDir(), rexModule+".lua")
	if err := sys.WriteAtomic(module, []byte(rexbridge.Lua(programs, actions)), 0o644, false); err != nil {
		return err
	}
	counts := map[string]int{}
	for _, a := range actions {
		counts[a.Program]++
	}
	ui.Done("Wrote %d rox and %d rix actions to %s", counts["rox"], counts["rix"], ui.Code(module))
	if programs[1].Bin == "" {
		ui.Warn("rix isn't on PATH, so its actions fail until it is. Rerun rox rex install after installing it")
	}
	init := rexInit()
	data, err := os.ReadFile(init)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !strings.Contains(string(data), `require("rox")`) {
		text := string(data)
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n" + requireLine + "\n"
		if err := sys.WriteAtomic(init, []byte(text), 0o644, true); err != nil {
			return err
		}
		ui.Done("Loaded it from %s", ui.Code(init))
	} else {
		ui.Skip("%s already loads it", init)
	}
	if !reload {
		ui.Hint("Run " + ui.Code("rex config reload") + " to load the actions")
		return nil
	}
	return ui.Run("Reloading Rex's config", "Reloaded Rex's config", func(*ui.Step) error {
		if result, err := sys.Try(sys.Cmd{Argv: []string{"rex", "config", "check"}, Timeout: 30 * time.Second}); err == nil && result.Code != 0 {
			return &sys.ExitError{Argv: []string{"rex"}, Result: result, Message: "rex config check found a problem, so Rex wasn't reloaded"}
		}
		_, err := sys.Run(sys.Cmd{Argv: []string{"rex", "config", "reload"}, Timeout: 30 * time.Second})
		return err
	})
}

// UninstallBridge removes rox.lua and its require line.
func UninstallBridge() error {
	module := filepath.Join(rexConfigDir(), rexModule+".lua")
	if err := os.Remove(module); err == nil {
		ui.Done("Removed %s", ui.Code(module))
	}
	init := rexInit()
	data, err := os.ReadFile(init)
	if err == nil && strings.Contains(string(data), `require("rox")`) {
		var kept []string
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), `require("rox")`) {
				kept = append(kept, line)
			}
		}
		if err := sys.WriteAtomic(init, []byte(strings.Join(kept, "\n")), 0o644, true); err != nil {
			return err
		}
		ui.Done("Stopped loading it from %s", ui.Code(init))
	}
	ui.Hint("Run " + ui.Code("rex config reload") + " to unload the actions")
	return nil
}

// BridgeInstalled reports whether rox rex install put the actions in place.
func BridgeInstalled() bool { return sys.IsFile(filepath.Join(rexConfigDir(), rexModule+".lua")) }

// RefreshBridge rewrites installed actions after an update, so they match the new commands
// and binary paths, and reloads Rex. It never installs them where they weren't.
func RefreshBridge(version string) {
	if !BridgeInstalled() {
		return
	}
	programs := bridgePrograms(version)
	module := filepath.Join(rexConfigDir(), rexModule+".lua")
	lua := rexbridge.Lua(programs, rexbridge.Actions(programs))
	if current, err := os.ReadFile(module); err == nil && string(current) == lua {
		return
	}
	if err := sys.WriteAtomic(module, []byte(lua), 0o644, false); err != nil {
		ui.Warn("Could not update the Rex actions in %s: %v", module, err)
		return
	}
	if _, err := sys.Run(sys.Cmd{Argv: []string{"rex", "config", "reload"}, Timeout: 30 * time.Second}); err != nil {
		ui.Done("Updated the Rex actions %s", ui.Muted("· run rex config reload to load them"))
		return
	}
	ui.Done("Updated the Rex actions and reloaded Rex")
}
