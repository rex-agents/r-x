package roxrex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/rox"
	"github.com/rex-agents/r-x/internal/selfupdate"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

type exitCode int

func (e exitCode) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// Main runs rox-rex and returns its exit status.
func Main(version string, args []string) int {
	ui.SetProgram("rox-rex")
	if len(args) > 0 && args[0] == "__relay" {
		// rox-rex __relay RUNTIME -- COMMAND...: the socket relay to a guest's Rex server.
		if len(args) < 4 || args[2] != "--" {
			return 2
		}
		if err := Relay(args[1], args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	root := RootCommand(version)
	updater := &selfupdate.Updater{Program: "rox-rex", Version: version, StateDir: rox.StateRoot(), Mode: func() string { return "notify" }}
	var check bool
	update := &cobra.Command{Use: "self-update", Short: "install the latest rox-rex release", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return updater.SelfUpdate(check) }}
	update.Flags().BoolVar(&check, "check", false, "only say whether a newer release is out")
	root.AddCommand(update)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var code exitCode
		if errors.As(err, &code) {
			return int(code)
		}
		ui.Failure(err)
		return 1
	}
	return 0
}

// RootCommand is rox-rex's command tree.
func RootCommand(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "rox-rex",
		Short:         "Rex for rox guests: sessions, hosts, the Rex app and Rex actions",
		Long:          "rox-rex is a rox hook provider. rox calls it to install and start Rex in guests, create their sessions,\nregister them as Rex hosts and open them in the Rex app. rox works without it.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(hookCommand(), installCommand(version), addHostCommand(), hostCommand(),
		&cobra.Command{Use: "uninstall", Short: "remove the actions and their line in init.lua", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return UninstallBridge() }},
		&cobra.Command{Use: "lua", Short: "print the Lua module that defines the actions", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { fmt.Fprint(ui.Stdout(), BridgeLua(version)); return nil }})
	return root
}

func hookCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "hook EVENT NAME",
		Short:  "the entry point rox calls, with the guest record on stdin",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			// Hooks answer rox in JSON on stdout. Anything for people goes in the reply.
			ui.SetOutput(&strings.Builder{}, os.Stderr)
			return Hook(args[0], args[1], os.Stdin, os.Stdout)
		},
	}
}

func installCommand(version string) *cobra.Command {
	var noReload, refresh bool
	run := &cobra.Command{
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
	run.Flags().BoolVar(&noReload, "no-reload", false, "don't run rex config reload")
	run.Flags().BoolVar(&refresh, "refresh", false, "only rewrite the actions if they're installed, as updates do")
	_ = run.Flags().MarkHidden("refresh")
	return run
}

// addHostCommand adds a guest, here or on another host, to the Rex app on this machine.
func addHostCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "add-host NAME",
		Short: "add a guest to this machine's Rex hosts and to the Rex app, which keeps its own host list",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			out, err := sys.OutputWith(sys.Cmd{Argv: []string{roxBin(), "inspect", args[0], "--json"}, Timeout: 60 * time.Second})
			if err != nil {
				return err
			}
			var g contract.Guest
			if err := json.Unmarshal([]byte(out), &g); err != nil {
				return fmt.Errorf("rox inspect printed something other than a guest record")
			}
			o := &Outcome{}
			address := g.Tailnet
			if g.Runtime != "" {
				if st := loadState(&g); !st.Registered {
					return fmt.Errorf("%s isn't a Rex host yet. Run rox reconnect %s first", g.Name, g.Name)
				}
				address = firstNonEmpty(g.Tailnet, endpoint(&g))
			} else {
				if address == "" {
					return fmt.Errorf("%s runs on another host and isn't on the tailnet, so only that host can reach it", g.Name)
				}
				if _, err := sys.Run(sys.Cmd{Argv: []string{"rex", "hosts", "add", g.Name, address}}); err != nil {
					return err
				}
				o.done("Registered Rex host %s here at %s", g.Name, address)
			}
			err = addAppHost(g.Name, address, o)
			for _, line := range o.Done {
				ui.Done("%s", line)
			}
			return err
		},
	}
}

// remoteRox runs rox on another Rex host with the release install's PATH, which Rex may lack.
var remoteRox = []string{"sh", "-c", `PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"; exec rox "$@"`, "rox"}

// hostCommand is the host transport rox can use (rox's "host_transport": "rox-rex"): Rex hosts
// as the places guests run on.
func hostCommand() *cobra.Command {
	c := &cobra.Command{Use: "host", Short: "rox's host transport over Rex hosts", Hidden: true}
	c.AddCommand(&cobra.Command{Use: "list", Short: "print the Rex hosts rox ls --all asks, one per line", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			labels, err := rexHostLabels()
			for _, l := range labels {
				fmt.Fprintln(ui.Stdout(), l)
			}
			return err
		}})
	var tty bool
	run := &cobra.Command{
		Use:   "run [--tty] HOST -- ARGV...",
		Short: "run rox ARGV on a Rex host, passing its output and status through",
		RunE: func(c *cobra.Command, args []string) error {
			at := c.ArgsLenAtDash()
			if at != 1 || len(args) < 2 {
				return errors.New("Usage: rox-rex host run [--tty] HOST -- ARGV...")
			}
			host, argv := args[0], args[at:]
			if tty {
				return hostShell(host, argv)
			}
			code, text, err := rexOutput(host, "rox "+argv[0], append(append([]string{}, remoteRox...), argv...), 6*time.Hour)
			fmt.Fprint(ui.Stdout(), text)
			if err != nil {
				return err
			}
			if code != 0 {
				return exitCode(code)
			}
			return nil
		},
	}
	run.Flags().BoolVar(&tty, "tty", false, "the command is interactive: open it as a session on the host and show it in the Rex app")
	c.AddCommand(run)
	return c
}

// hostShell opens rox ARGV as a session on the host, shows it in the Rex app here when it's
// open, and says where it is. Rex sessions are the terminal on a Rex host.
func hostShell(host string, argv []string) error {
	created, err := rexJSON(20*time.Second, append([]string{"rex", "--autostart=false", "-S", host, "new", "--json", "rox " + strings.Join(argv, " "), "--"}, append(append([]string{}, remoteRox...), argv...)...)...)
	if err != nil {
		return err
	}
	session, _ := sessionBlock(created)
	ui.Info("Opened rox %s on %s in Rex session %s", argv[0], host, session)
	if app := localApp(); app != nil {
		if sys.Status("rex", "--autostart=false", "-C", app.ID, "do", "session.select", "session_id="+session) == 0 {
			ui.Done("It's showing in the Rex app")
			return nil
		}
	}
	ui.Hint("Open it from " + host + " in the Rex app, or run " + ui.Code("rex -S "+host+" session inspect "+session))
	return nil
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
