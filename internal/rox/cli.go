package rox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/selfupdate"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

const hostHelp = "Rex host that runs the guest; found automatically when omitted"

// Main runs rox with the process's arguments and returns its exit status.
func Main(version string, args []string) int {
	ui.SetProgram("rox")
	if len(args) > 0 && args[0] == "__relay" {
		// rox __relay RUNTIME -- COMMAND...: the SSH relay that replaced guest/relay.py.
		if len(args) < 4 || args[2] != "--" {
			return 2
		}
		if err := Relay(args[1], args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	updater := &selfupdate.Updater{Program: "rox", Version: version, StateDir: StateRoot(), Mode: updateMode,
		After: func(tag string) {
			RefreshSkills(tag)
			// The new binary knows the new commands, so it writes the actions.
			sys.Try(sys.Cmd{Argv: []string{selfPath(), "rex", "install", "--refresh"}, Env: map[string]string{"ROX_UPDATED": tag}, Passthrough: true})
		}}
	updater.Auto(args)
	root := rootCommand(version, updater)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		if code, ok := ExitStatus(err); ok {
			return code
		}
		ui.Failure(err)
		return 1
	}
	return 0
}

func updateMode() string {
	if mode := os.Getenv("ROX_UPDATES"); mode != "" {
		return mode
	}
	if c, err := LoadConfig(""); err == nil {
		if mode, ok := c.Data["updates"].(string); ok && mode != "" {
			return mode
		}
	}
	return "auto"
}

func rootCommand(version string, updater *selfupdate.Updater) *cobra.Command {
	var verbose bool
	root := &cobra.Command{
		Use:           "rox",
		Short:         "Launch Rex guests and the project workspaces inside them",
		Long:          "rox launches Rex guests and the project workspaces inside them.\n\nGit keys, agent credentials and agent setup come from rix, when it's installed.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			if verbose {
				ui.SetVerbose(true)
			}
		},
	}
	root.SetVersionTemplate("rox {{.Version}}\n")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show the output of the tools rox runs")
	root.AddGroup(&cobra.Group{ID: "guests", Title: "Guests:"}, &cobra.Group{ID: "access", Title: "Access:"},
		&cobra.Group{ID: "setup", Title: "Setup:"}, &cobra.Group{ID: "machine", Title: "For rix and other tools:"})
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("guests", upCommand(), addCommand(), lsCommand(), lifecycleCommand("reconnect", "restore access to a guest, starting it if it stopped"),
		lifecycleCommand("start", "start a stopped guest and restore access to it"), lifecycleCommand("stop", "stop a guest and keep it stopped until rox start"),
		downCommand())
	add("access", shellCommandCLI(), forwardCommand(), cpCommand(), funnelCommand(), openCommand(), addRemoteHostCommand())
	add("setup", templateCommand(), configCommand(), serviceCommand(), skillCommand(version), selfUpdateCommand(updater), rexCommand(version), keyForwarder())
	add("machine", execCommand(), inspectCommand(), hooksCommand())
	return root
}

func projectFlags(c *cobra.Command, o *UpOptions) {
	cwd, _ := os.Getwd()
	f := c.Flags()
	f.StringVarP(&o.Host, "host", "S", "", "run on this Rex remote host")
	f.StringVar(&o.Config, "config", "", "config file on the machine that runs the guest")
	f.StringVar(&o.Cwd, "cwd", cwd, "directory within the source project")
	f.StringVar(&o.Project, "project", "", "project name for a launch from --repo and --branch without a checkout")
	f.StringVar(&o.Remote, "remote", "", "Git remote name")
	f.StringVar(&o.Repo, "repo", "", "override the repository URL")
	f.StringVar(&o.Branch, "branch", "", "remote branch to clone")
	f.StringArrayVar(&o.GitKeys, "git-key", nil, "Git SSH identity for rix to use (repeatable)")
	f.StringVar(&o.GitKnownHosts, "git-known-hosts", "", "known_hosts file for Git SSH servers")
	f.BoolVar(&o.NoHook, "no-hook", false, "skip the project's .rox.sh")
	f.BoolVar(&o.Local, "local", false, "copy this checkout's unpushed commits and uncommitted files into the guest")
	f.BoolVar(&o.OpenShell, "open-shell", false, "open the guest's session in the Rex app here, or a Guest tab in this Rex session")
	f.StringVar(&o.Agents, "agents", "", "agent CLIs to install for this launch, such as claude,codex or claude=1.2.3")
	f.BoolVar(&o.NoAgents, "no-agents", false, "install no agent CLIs, whatever the project lists")
	f.StringVar(&o.Vault, "vault", "host", "with --host: whose rix vault signs the guest in, host or here")
	f.StringVar(&HookScope, "hooks", "all", "which rix hooks run: all or git")
	_ = f.MarkHidden("hooks")
	c.MarkFlagsMutuallyExclusive("remote", "repo")
}

func commandAfterDash(c *cobra.Command, args []string) ([]string, []string, error) {
	at := c.ArgsLenAtDash()
	if at < 0 {
		return args, nil, nil
	}
	if at == len(args) {
		return nil, nil, errors.New("-- requires a command")
	}
	return args[:at], args[at:], nil
}

func upCommand() *cobra.Command {
	o := &UpOptions{}
	var boot int
	credentials := true
	c := &cobra.Command{
		Use:   "up [container|linux|macos|fly|railway] [-- COMMAND...]",
		Short: "start and provision a new guest",
		Long: "Start and provision a new guest.\n\nThe backend defaults to \"backend\" in the branch's .rox.json. Agent CLIs are installed\n" +
			"when the project lists them in .rox.json or .rox.local.json, as {\"agents\": {\"claude\": true}}.",
		RunE: func(c *cobra.Command, args []string) error {
			args, command, err := commandAfterDash(c, args)
			if err != nil {
				return err
			}
			if len(args) > 1 {
				return errors.New("rox up takes one backend")
			}
			if len(args) == 1 {
				if !contains(Backends, args[0]) {
					return fmt.Errorf("Choose a backend: %s", strings.Join(Backends, ", "))
				}
				o.Backend = args[0]
			}
			o.Command, o.BootTimeout, o.CPUsSet = command, time.Duration(boot)*time.Second, c.Flags().Changed("cpus")
			o.NoCredentials = !credentials
			if o.CPUsSet && o.CPUs < 1 {
				return errors.New("Expected a positive whole number for --cpus")
			}
			return Up(o)
		},
	}
	projectFlags(c, o)
	f := c.Flags()
	f.BoolVar(&o.NoProject, "no-project", false, "start a guest without a project, even from inside a checkout")
	f.StringVar(&o.Image, "image", "", "container image or prepared Tart template")
	f.IntVar(&o.CPUs, "cpus", 0, "CPUs for the container or VM")
	f.StringVar(&o.Memory, "memory", "", "memory for the container or VM, such as 8G or 512M")
	f.StringVar(&o.LocalSettings, "local-settings", "", "")
	_ = f.MarkHidden("local-settings")
	f.StringVar(&o.Engine, "engine", "", "container engine: container, docker or podman")
	f.IntVar(&boot, "boot-timeout", 120, "seconds per guest readiness check")
	f.BoolVar(&o.NoPrebuild, "no-prebuild", false, "clone and run .rox.sh at boot instead of using a project image")
	f.BoolVar(&o.Rebuild, "rebuild", false, "rebuild the project image before launching")
	f.BoolVar(&o.NoTailscale, "no-tailscale", false, "keep this guest off the tailnet even when tailscale is configured")
	f.BoolVar(&o.NoGitAgent, "no-git-agent", false, "give the guest Git keys only while cloning, not for its whole life")
	f.BoolVar(&credentials, "credentials", true, "sign the guest's agents in; --credentials=false keeps Claude Code and Codex credentials out (Git agent and setup sync still apply)")
	var ignored bool
	f.BoolVar(&ignored, "no-app-host", false, "")
	_ = f.MarkHidden("no-app-host")
	return c
}

func addCommand() *cobra.Command {
	o := &UpOptions{BootTimeout: 120 * time.Second}
	c := &cobra.Command{
		Use:   "add NAME [-- COMMAND...]",
		Short: "clone a project into a running guest",
		RunE: func(c *cobra.Command, args []string) error {
			args, command, err := commandAfterDash(c, args)
			if err != nil {
				return err
			}
			if len(args) != 1 {
				return errors.New("Name the guest to add the project to: rox add NAME")
			}
			o.Command = command
			return Add(args[0], o)
		},
	}
	projectFlags(c, o)
	c.Flags().BoolVar(&o.NoGitAgent, "no-git-agent", false, "give the guest Git keys only while cloning")
	return c
}

func lsCommand() *cobra.Command {
	var all, allProjects, asJSON bool
	var host, cwd string
	cwd, _ = os.Getwd()
	c := &cobra.Command{
		Use:   "ls",
		Short: "list managed workspaces",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			scope := host
			if all {
				scope = "all"
			}
			return List(scope, asJSON, cwd, allProjects)
		},
	}
	f := c.Flags()
	f.BoolVarP(&all, "all", "a", false, "also list workspaces on every Rex host this machine can reach")
	f.StringVarP(&host, "host", "S", "", "list workspaces on this Rex remote host instead")
	f.BoolVar(&allProjects, "all-projects", false, "in a checkout, list every workspace, not only the ones with this project")
	f.StringVar(&cwd, "cwd", cwd, "directory within the project to list")
	f.BoolVar(&asJSON, "json", false, `print {"schema": 1, "guests": [...]} as JSON`)
	c.MarkFlagsMutuallyExclusive("all", "host")
	return c
}

func lifecycleCommand(verb, short string) *cobra.Command {
	var host string
	c := &cobra.Command{
		Use:   verb + " NAME",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if handled, err := Route(verb, name, host, func(h string, _ contract.Entry) error {
				return RunOnHost(h, []string{verb, name}, 10*time.Minute)
			}); handled {
				return err
			}
			switch verb {
			case "reconnect":
				return Reconnect(name, true)
			case "start":
				return Reconnect(name, false)
			default:
				return Stop(name)
			}
		},
	}
	c.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	var ignored bool
	c.Flags().BoolVar(&ignored, "no-app-host", false, "")
	_ = c.Flags().MarkHidden("no-app-host")
	return c
}

func downCommand() *cobra.Command {
	var host, cwd string
	cwd, _ = os.Getwd()
	c := &cobra.Command{
		Use:   "down [NAME]",
		Short: "delete a guest and its workspace",
		Long:  "Delete a guest, its files and its Rex host entry. Without NAME, pick from the current project's workspaces.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" && host == "" {
				var err error
				if name, host, err = ChooseProjectGuest(cwd); err != nil {
					return err
				}
			}
			if host == "" && !IsLocal(name) {
				found, _, err := findRemoteGuest(name, "")
				if err != nil {
					return err
				}
				host = found
			}
			if host != "" {
				return DownRemote(name, host)
			}
			return Down(name)
		},
	}
	c.Flags().StringVar(&cwd, "cwd", cwd, "directory within the project")
	c.Flags().StringVarP(&host, "host", "S", "", "remove a guest that runs on this Rex remote host")
	return c
}

func shellCommandCLI() *cobra.Command {
	var host string
	c := &cobra.Command{
		Use:   "shell NAME [PROJECT]",
		Short: "open an interactive shell in a guest",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			key := ""
			if len(args) == 2 {
				key = args[1]
			}
			if handled, err := Route("shell", args[0], host, func(h string, e contract.Entry) error { return RouteShell(h, e, key) }); handled {
				return err
			}
			return Shell(args[0], key)
		},
	}
	c.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	return c
}

func forwardCommand() *cobra.Command {
	var host string
	c := &cobra.Command{
		Use:   "forward NAME PORT|LOCAL:GUEST...",
		Short: "forward localhost ports to servers in a guest",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			var ports [][2]int
			for _, spec := range args[1:] {
				local, guest, err := ParseForward(spec)
				if err != nil {
					return err
				}
				ports = append(ports, [2]int{local, guest})
			}
			if handled, err := Route("forward", args[0], host, func(h string, e contract.Entry) error { return RouteForward(h, e, ports) }); handled {
				return err
			}
			return Forward(args[0], ports)
		},
	}
	c.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	return c
}

func cpCommand() *cobra.Command {
	var project, cwd string
	cwd, _ = os.Getwd()
	c := &cobra.Command{
		Use:   "cp NAME SOURCE[:DEST]...",
		Short: "copy files from this machine into a guest",
		Long:  "Copy files into a guest. DEST is relative to the project's checkout, or absolute, and defaults to SOURCE.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			var copies [][2]string
			for _, spec := range args[1:] {
				src, dest, err := ParseCopy(spec)
				if err != nil {
					return err
				}
				copies = append(copies, [2]string{src, dest})
			}
			return Copy(args[0], copies, project, cwd)
		},
	}
	c.Flags().StringVar(&project, "project", "", "project whose checkout relative destinations go in")
	c.Flags().StringVar(&cwd, "cwd", cwd, "directory SOURCE paths start from")
	return c
}

func funnelCommand() *cobra.Command {
	c := &cobra.Command{Use: "funnel", Short: "expose ports in guests to the internet with Tailscale Funnel"}
	var host string
	var https int
	var path string
	addCmd := &cobra.Command{
		Use: "add NAME PORT", Short: "put a port in a guest on the internet", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			port, err := portNumber(args[1])
			if err != nil {
				return err
			}
			if !containsInt(FunnelPorts, https) {
				return errors.New("Choose 443, 8443 or 10000 for --https")
			}
			if handled, err := Route("funnel", args[0], host, func(h string, _ contract.Entry) error {
				return RunOnHost(h, []string{"funnel", "add", args[0], args[1], fmt.Sprintf("--https=%d", https), "--path=" + path}, 5*time.Minute)
			}); handled {
				return err
			}
			return AddFunnel(args[0], port, https, path)
		},
	}
	addCmd.Flags().IntVar(&https, "https", 443, "public HTTPS port: 443, 8443 or 10000")
	addCmd.Flags().StringVar(&path, "path", "/", "public path to serve it at")
	addCmd.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	var all bool
	removeCmd := &cobra.Command{
		Use: "remove NAME [PORT|--all]", Short: "take a guest's funnels off the internet", Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			port := 0
			if len(args) == 2 {
				var err error
				if port, err = portNumber(args[1]); err != nil {
					return err
				}
			}
			if (port == 0) == !all {
				return errors.New("Name a port, or pass --all")
			}
			if handled, err := Route("funnel", args[0], host, func(h string, _ contract.Entry) error {
				return RunOnHost(h, append([]string{"funnel", "remove"}, args...), 5*time.Minute)
			}); handled {
				return err
			}
			return RemoveFunnels(args[0], port)
		},
	}
	removeCmd.Flags().BoolVar(&all, "all", false, "remove every funnel of the guest")
	removeCmd.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	lsCmd := &cobra.Command{
		Use: "ls [NAME]", Short: "list funnels", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" && host != "" {
				return RunOnHost(host, []string{"funnel", "ls"}, time.Minute)
			}
			if handled, err := Route("funnel", name, host, func(h string, _ contract.Entry) error {
				return RunOnHost(h, []string{"funnel", "ls", name}, time.Minute)
			}); handled {
				return err
			}
			return ListFunnels(name)
		},
	}
	lsCmd.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	c.AddCommand(addCmd, removeCmd, lsCmd)
	return c
}

func containsInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

func portNumber(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("expected a port from 1 to 65535, got %q", value)
	}
	return n, nil
}

func openCommand() *cobra.Command {
	var host string
	c := &cobra.Command{
		Use:   "open NAME [PROJECT]",
		Short: "show a guest's own Rex session in the Rex app, so splits run in the guest",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			key := ""
			if len(args) == 2 {
				key = args[1]
			}
			return OpenGuest(args[0], key, host)
		},
	}
	c.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	return c
}

func addRemoteHostCommand() *cobra.Command {
	var host string
	c := &cobra.Command{
		Use:   "add-remote-host NAME",
		Short: "add a guest to the Rex app on this machine, which keeps its own host list",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return AddRemoteHost(args[0], host) },
	}
	c.Flags().StringVarP(&host, "host", "S", "", hostHelp)
	return c
}

func templateCommand() *cobra.Command {
	o := &TemplateOptions{}
	var host string
	var boot int
	c := &cobra.Command{
		Use:   "template macos|linux",
		Short: "prepare a Tart template and point the config at it",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if args[0] != "macos" && args[0] != "linux" {
				return errors.New("Choose macos or linux")
			}
			if o.Distro != "" {
				if _, ok := LinuxTemplateBases[o.Distro]; !ok {
					return errors.New("Choose ubuntu, debian, fedora or arch for --distro")
				}
			}
			o.Backend, o.BootTimeout = args[0], time.Duration(boot)*time.Second
			if host != "" {
				argv := []string{"template", o.Backend}
				c.Flags().Visit(func(f *pflagFlag) {
					if f.Name != "host" {
						argv = append(argv, "--"+f.Name+"="+f.Value.String())
					}
				})
				// A first template pulls a large image, so give it hours rather than minutes.
				return RunOnHost(host, argv, 4*time.Hour)
			}
			return PrepareTemplate(o)
		},
	}
	f := c.Flags()
	f.StringVarP(&host, "host", "S", "", "prepare it on this Rex remote host")
	f.StringVar(&o.Config, "config", "", "config file on the machine that runs the guests")
	f.StringVar(&o.Name, "name", "", "template name; defaults to macos.image or rox-macos, or rox-DISTRO")
	f.StringVar(&o.Base, "base", "", "Tart image to clone the template from")
	f.StringVar(&o.Distro, "distro", "", "Linux distribution: ubuntu (default), debian, fedora or arch")
	f.BoolVar(&o.Default, "default", false, "make this the linux image even when another one is configured")
	f.StringVar(&o.RexChannel, "rex-channel", "", "Rex channel for a Linux template: stable, preview or snapshot")
	f.StringVar(&o.BasePassword, "base-password", DefaultBasePassword, "the base image's login password, which rox replaces")
	f.IntVar(&boot, "boot-timeout", 600, "seconds per readiness check")
	return c
}

func configCommand() *cobra.Command {
	var config string
	c := &cobra.Command{
		Use:   "config",
		Short: "open the rox config in VISUAL or EDITOR",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return EditConfig(config) },
	}
	c.PersistentFlags().StringVar(&config, "config", "", "alternate rox configuration file")
	var tags []string
	var controlURL string
	tailscaleCmd := &cobra.Command{
		Use:   "tailscale",
		Short: "save a Tailscale auth key and put new guests on the tailnet",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return ConfigureTailscale(config, tags, controlURL) },
	}
	tailscaleCmd.Flags().StringArrayVar(&tags, "tag", nil, "tag for guest devices (repeatable); defaults to tag:rox-guest")
	tailscaleCmd.Flags().StringVar(&controlURL, "control-url", "", "https:// URL of a self-hosted control server such as Headscale")
	agentsCmd := &cobra.Command{
		Use:                "agents",
		Short:              "moved: rix auth signs agents in, and .rox.json chooses them",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE:               func(_ *cobra.Command, args []string) error { return forwardConfigAgents(args) },
	}
	c.AddCommand(tailscaleCmd, agentsCmd)
	return c
}

// forwardConfigAgents keeps rox config agents working for one release by translating it to rix.
func forwardConfigAgents(args []string) error {
	agent := ""
	var rixArgs []string
	var sync []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() string {
			if _, v, ok := strings.Cut(a, "="); ok {
				return v
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "claude" || a == "codex":
			agent = a
		case a == "--api-key" || a == "--off":
			rixArgs = append(rixArgs, a)
		case strings.HasPrefix(a, "--auth-file"):
			rixArgs = append(rixArgs, "--auth-file="+value())
		case strings.HasPrefix(a, "--sync"):
			sync = append(sync, value())
		case strings.HasPrefix(a, "--mcp"):
			rixArgs = append(rixArgs, "--mcp="+value())
		case strings.HasPrefix(a, "--version"):
			v := value()
			ui.Warn("Agent versions come from each project now")
			ui.Hint(fmt.Sprintf(`Add {"agents": {"%s": "%s"}} to %s or %s`, firstNonEmpty(agent, "claude"), v, ProjectConfig, LocalConfig))
		case strings.HasPrefix(a, "--config"):
			value()
		}
	}
	ui.Warn("rox config agents moved to rix")
	if agent == "" {
		ui.Hint("Sign an agent in with " + ui.Code("rix auth claude") + " or " + ui.Code("rix auth codex") +
			", and choose agents per project with \"agents\" in " + ProjectConfig)
		return nil
	}
	var argv []string
	if len(sync) > 0 {
		argv = append([]string{"rix", "sync", "set", agent, sync[0]}, rixArgs...)
	} else {
		argv = append([]string{"rix", "auth", agent}, rixArgs...)
	}
	ui.Hint("Running " + ui.Code(strings.Join(argv, " ")))
	if !sys.Has("rix") {
		return errors.New("Install rix to manage agent credentials")
	}
	result, err := sys.Try(sys.Cmd{Argv: argv, Interactive: true})
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return exitCode(result.Code)
	}
	return nil
}

func keyForwarder() *cobra.Command {
	return &cobra.Command{
		Use:                "key",
		Short:              "moved: rix key manages Git SSH keys",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			ui.Warn("rox key moved to rix")
			argv := append([]string{"rix", "key"}, args...)
			ui.Hint("Running " + ui.Code(strings.Join(argv, " ")))
			if !sys.Has("rix") {
				return errors.New("Install rix to manage Git SSH keys")
			}
			result, err := sys.Try(sys.Cmd{Argv: argv, Interactive: true})
			if err != nil {
				return err
			}
			if result.Code != 0 {
				return exitCode(result.Code)
			}
			return nil
		},
	}
}

func serviceCommand() *cobra.Command {
	c := &cobra.Command{Use: "service", Short: "keep guests running across runtime restarts and reboots"}
	var interval int
	run := &cobra.Command{Use: "run", Short: "run the service in the foreground", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return ServiceRun(time.Duration(interval) * time.Second) }}
	run.Flags().IntVar(&interval, "interval", int(ServiceInterval.Seconds()), "seconds between checks")
	c.AddCommand(
		&cobra.Command{Use: "install", Short: "start the service at login with launchd or systemd", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return InstallService() }},
		&cobra.Command{Use: "uninstall", Short: "stop the service and remove it", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return UninstallService() }},
		&cobra.Command{Use: "status", Short: "show whether the service is running", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return ServiceStatus() }},
		run)
	return c
}

func skillCommand(version string) *cobra.Command {
	c := &cobra.Command{Use: "skill", Short: "install the rox agent skill"}
	var agents []string
	var dir string
	var force bool
	install := &cobra.Command{Use: "install", Short: "copy the bundled skill into Claude Code or Codex", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			for _, a := range agents {
				if _, ok := SkillAgents[a]; !ok {
					return errors.New("Choose claude or codex for --agent")
				}
			}
			if len(agents) > 0 && dir != "" {
				return errors.New("Choose --agent or --dir, not both")
			}
			return InstallSkill(agents, dir, force, version)
		}}
	install.Flags().StringArrayVar(&agents, "agent", nil, "agent to install for (repeatable); defaults to each one set up here")
	install.Flags().StringVar(&dir, "dir", "", "skills folder to install into instead, such as a project's .claude/skills")
	install.Flags().BoolVar(&force, "force", false, "replace a folder named rox that isn't a rox skill")
	c.AddCommand(install)
	return c
}

func selfUpdateCommand(u *selfupdate.Updater) *cobra.Command {
	var check bool
	c := &cobra.Command{Use: "self-update", Short: "install the latest rox release and update the rox skill", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return u.SelfUpdate(check) }}
	c.Flags().BoolVar(&check, "check", false, "only say whether a newer release is out")
	return c
}

func execCommand() *cobra.Command {
	var stdin bool
	var agentSock string
	c := &cobra.Command{
		Use:   "exec NAME [--stdin] [--agent-sock PATH] -- COMMAND...",
		Short: "run a command in a guest over rox's SSH, passing its output and status through",
		Long: "Run a command in a guest over rox's SSH. stdout, stderr and the exit status pass through,\n" +
			"and with --stdin so does stdin. This is rix's only way into guests.",
		RunE: func(c *cobra.Command, args []string) error {
			args, command, err := commandAfterDash(c, args)
			if err != nil {
				return err
			}
			if len(args) != 1 || len(command) == 0 {
				return errors.New("Usage: rox exec NAME [--stdin] -- COMMAND...")
			}
			return Exec(args[0], stdin, agentSock, command)
		},
	}
	c.Flags().BoolVar(&stdin, "stdin", false, "pass this process's stdin to the command")
	c.Flags().StringVar(&agentSock, "agent-sock", "", "forward this SSH agent socket for the command")
	return c
}

func inspectCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "inspect NAME --json",
		Short: "print one guest's record as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if !asJSON {
				return describeLocal(args[0])
			}
			return Inspect(args[0])
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the record as JSON (schema 1)")
	return c
}

func describeLocal(name string) error {
	g, err := LoadGuest(name)
	if err != nil {
		return err
	}
	describeGuest(g, nil)
	return nil
}

func hooksCommand() *cobra.Command {
	return &cobra.Command{Use: "hooks", Short: "show which hook provider is active", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return ShowHooks() }}
}

var _ = filepath.Join

type pflagFlag = pflag.Flag
