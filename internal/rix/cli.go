package rix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/selfupdate"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Main runs rix and returns its exit status.
func Main(version string, args []string) int {
	ui.SetProgram("rix")
	updater := &selfupdate.Updater{Program: "rix", Version: version, StateDir: StateRoot(), Mode: updateMode,
		After: func(string) {
			// rox-rex writes rix's Rex actions too, from the rix it finds on PATH.
			if p, err := sys.R.LookPath("rox-rex"); err == nil {
				sys.Try(sys.Cmd{Argv: []string{p, "install", "--refresh"}, Passthrough: true})
			}
		}}
	updater.Auto(args)
	root := RootCommand(version, updater)
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

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func updateMode() string {
	if mode := os.Getenv("RIX_UPDATES"); mode != "" {
		return mode
	}
	if c, err := LoadConfig(""); err == nil {
		if mode, ok := c.Data["updates"].(string); ok && mode != "" {
			return mode
		}
	}
	return "auto"
}

func show(out *Outcome) {
	for _, line := range out.Done {
		ui.Done("%s", line)
	}
	for _, line := range out.Warnings {
		ui.Warn("%s", line)
	}
}

// RootCommand is rix's command tree. rox reads it to make rix's Rex actions.
func RootCommand(version string, updater *selfupdate.Updater) *cobra.Command {
	var verbose bool
	root := &cobra.Command{
		Use:   "rix",
		Short: "The vault for the Git keys, agent credentials and agent setup rox guests use",
		Long: "rix keeps the secrets and settings that agents and Git use inside rox guests: Git SSH keys,\n" +
			"Claude Code and Codex credentials, and synced skills, instructions, MCP servers and settings.\n\n" +
			"rox calls rix at each step of a guest's life. rix reaches guests only through rox exec.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			if verbose {
				ui.SetVerbose(true)
			}
		},
	}
	root.SetVersionTemplate("rix {{.Version}}\n")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show the output of the tools rix runs")
	root.AddGroup(&cobra.Group{ID: "vault", Title: "Vault:"}, &cobra.Group{ID: "guests", Title: "Guests:"},
		&cobra.Group{ID: "setup", Title: "Setup:"}, &cobra.Group{ID: "tools", Title: "For rux and other tools:"})
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("vault", keyCommand(), knownHostsCommand(), authCommand(), secretCommand(), syncCommand())
	add("guests", applyCommand(), statusCommand(), gitAgentCommand())
	add("setup", configCommand(), migrateCommand(), selfUpdateCommand(updater))
	add("tools", credentialCommand())
	root.AddCommand(hookCommand())
	return root
}

func keyCommand() *cobra.Command {
	c := &cobra.Command{Use: "key", Short: "manage the Git SSH keys guests use"}
	edit := func(adding bool) func(*cobra.Command, []string) error {
		return func(_ *cobra.Command, args []string) error {
			path := sys.Expand(args[0])
			if adding {
				if _, err := sys.ConfiguredFile(path); err != nil {
					return err
				}
			}
			var changed bool
			err := Update(ConfigLocation(""), func(data map[string]any) error {
				c := &Config{Path: ConfigLocation(""), Data: data}
				keys, _, err := c.GitKeys()
				if err != nil {
					return err
				}
				var kept []any
				present := false
				for _, k := range keys {
					if sys.Expand(k) == path {
						present = true
					} else {
						kept = append(kept, k)
					}
				}
				switch {
				case adding && !present:
					data["git_keys"] = append(kept, path)
					changed = true
				case !adding && present:
					if len(kept) == 0 {
						// No keys left is the same as never having set any: ssh's defaults.
						delete(data, "git_keys")
					} else {
						data["git_keys"] = kept
					}
					changed = true
				}
				return nil
			})
			if err != nil {
				return err
			}
			switch {
			case adding && changed:
				ui.Done("Added Git SSH key %s", ui.Code(path))
			case adding:
				ui.Skip("%s is already a Git SSH key", path)
			case changed:
				ui.Done("Removed Git SSH key %s", ui.Code(path))
			default:
				ui.Skip("%s isn't a Git SSH key", path)
			}
			return nil
		}
	}
	c.AddCommand(
		&cobra.Command{Use: "add PATH", Short: "use a Git SSH key in guests", Args: cobra.ExactArgs(1), RunE: edit(true)},
		&cobra.Command{Use: "remove PATH", Short: "stop using a Git SSH key", Args: cobra.ExactArgs(1), RunE: edit(false)},
		&cobra.Command{Use: "ls", Short: "list the Git SSH keys", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			c, err := LoadConfig("")
			if err != nil {
				return err
			}
			keys, configured, err := c.GitKeys()
			if err != nil {
				return err
			}
			if !configured {
				defaults := defaultKeys()
				if len(defaults) == 0 {
					ui.Skip("No Git SSH keys, and ~/.ssh has none of ssh's default keys")
					ui.Hint("Add one with " + ui.Code("rix key add PATH"))
					return nil
				}
				ui.Info("No keys configured, so guests get ssh's default keys")
				keys = defaults
			}
			var rows [][]string
			for _, k := range keys {
				rows = append(rows, []string{filepath.Base(k), sys.Expand(k)})
			}
			ui.Table([]string{"key", "path"}, rows)
			return nil
		}},
	)
	return c
}

func knownHostsCommand() *cobra.Command {
	c := &cobra.Command{Use: "known-hosts", Short: "pin Git server host keys"}
	c.AddCommand(&cobra.Command{Use: "set PATH", Short: "pin the Git server keys in a known_hosts file", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := sys.ConfiguredFile(args[0])
			if err != nil {
				return err
			}
			if err := Update(ConfigLocation(""), func(data map[string]any) error { data["git_known_hosts"] = path; return nil }); err != nil {
				return err
			}
			ui.Done("Guests trust the Git servers pinned in %s", ui.Code(path))
			return nil
		}},
		&cobra.Command{Use: "clear", Short: "use this machine's ~/.ssh/known_hosts entries again", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				if err := Update(ConfigLocation(""), func(data map[string]any) error { delete(data, "git_known_hosts"); return nil }); err != nil {
					return err
				}
				ui.Done("Guests trust the Git servers in this machine's ~/.ssh/known_hosts")
				return nil
			}})
	return c
}

func codexAuthLocation() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(sys.Expand(dir), "auth.json")
	}
	return filepath.Join(sys.Home(), ".codex", "auth.json")
}

func authCommand() *cobra.Command {
	var apiKey, off, useGh bool
	var authFile string
	c := &cobra.Command{
		Use:   "auth claude|codex|github",
		Short: "store the credential an agent signs in with, or the GitHub token tools use",
		Long: "Store the credential an agent signs in with in guests (mode 0600). rix pushes it only into\n" +
			"guests that have the agent, which projects choose with \"agents\" in .rox.json.\n\n" +
			"rix auth github stores the GitHub token that rux and other tools on this machine get from\n" +
			"rix credential github. Without one, they use gh's login.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"claude", "codex", "github"},
		RunE: func(_ *cobra.Command, args []string) error {
			agent := args[0]
			if agent == "github" {
				if apiKey || authFile != "" {
					return errors.New("--api-key and --auth-file are for codex")
				}
				return AuthGitHub(useGh, off)
			}
			if useGh {
				return errors.New("--gh is for github")
			}
			if Credentials[agent] == nil {
				return errors.New("Choose claude, codex or github")
			}
			if agent != "codex" && authFile != "" {
				return errors.New("--auth-file is for codex")
			}
			return Auth(agent, apiKey, authFile, off)
		},
	}
	c.Flags().BoolVar(&apiKey, "api-key", false, "codex: sign in with an OpenAI API key")
	c.Flags().StringVar(&authFile, "auth-file", "", "codex: login file to copy into guests (default ~/.codex/auth.json)")
	c.Flags().BoolVar(&off, "off", false, "forget the agent's credential, or turn GitHub access off")
	c.Flags().BoolVar(&useGh, "gh", false, "github: use gh's login on this machine, forgetting any stored token")
	return c
}

// Auth stores an agent's credential and points the config at it.
func Auth(agent string, apiKey bool, authFile string, off bool) error {
	path := ConfigLocation("")
	ui.Header("auth "+agent, AgentNames[agent])
	update := map[string]string{}
	if !off {
		switch agent {
		case "claude":
			value, err := claudeCredential()
			if err != nil {
				return err
			}
			if strings.ContainsAny(value, " \t") {
				return errors.New("Expected a single token with no spaces")
			}
			if value != "" && !strings.HasPrefix(value, "sk-ant-") {
				return errors.New("That doesn't look like a Claude Code token (sk-ant-oat01-…) or an Anthropic API key (sk-ant-api03-…)")
			}
			if value != "" {
				setting := "token_file"
				if strings.HasPrefix(value, "sk-ant-api") {
					setting = "api_key_file"
				}
				update[setting] = value
			}
		case "codex":
			auth := codexAuthLocation()
			if authFile != "" {
				auth = sys.Expand(authFile)
			}
			useAuth := !apiKey && (authFile != "" || sys.IsFile(auth))
			if useAuth && authFile == "" && ui.IsTerminal() {
				useAuth = ui.Confirm(fmt.Sprintf("Use your Codex login in %s?", auth))
			}
			if useAuth {
				if _, err := sys.ConfiguredFile(auth); err != nil {
					return err
				}
				if info, err := os.Stat(auth); err == nil && info.Mode().Perm()&0o077 != 0 {
					return fmt.Errorf("%s holds your Codex login, so only you may read it. Run chmod 600 %s", auth, auth)
				}
				update["auth_file"] = "@" + auth
			} else {
				value, err := ui.Secret("OpenAI API key:", "Paste an OpenAI API key, or leave it blank to keep guests signed out.\nTo use your ChatGPT login instead, run codex login first.")
				if err != nil {
					return err
				}
				if value != "" {
					update["api_key_file"] = value
				}
			}
		}
	}
	var saved, obsolete []string
	err := UpdateThen(path, func(data map[string]any) error {
		agents, _ := data["agents"].(map[string]any)
		if agents == nil {
			agents = map[string]any{}
		}
		previous, _ := agents[agent].(map[string]any)
		settings := map[string]any{}
		if s, ok := previous["sync"]; ok {
			settings["sync"] = s
		}
		// Secrets rix saved for this agent that the new settings no longer use go once the
		// config no longer names them, not before.
		obsolete = obsolete[:0]
		for key, file := range SecretFiles {
			if key[0] == agent {
				if _, kept := update[key[1]]; !kept {
					obsolete = append(obsolete, filepath.Join(filepath.Dir(path), file))
				}
			}
		}
		for setting, value := range update {
			if strings.HasPrefix(value, "@") {
				settings[setting] = value[1:]
				continue
			}
			file := filepath.Join(filepath.Dir(path), SecretFiles[[2]string{agent, setting}])
			if err := sys.WritePrivate(file, value+"\n"); err != nil {
				return err
			}
			saved = append(saved, file)
			settings[setting] = file
		}
		agents[agent] = settings
		data["agents"] = agents
		return nil
	}, func() {
		// The config is committed and still locked: the files it dropped can go.
		for _, file := range obsolete {
			os.Remove(file)
		}
	})
	if err != nil {
		return err
	}
	for _, file := range saved {
		ui.Done("Saved the %s credential to %s %s", AgentNames[agent], ui.Code(file), ui.Muted("(0600)"))
	}
	switch {
	case off || len(update) == 0:
		ui.Done("Guests start %s signed out", AgentNames[agent])
	case update["auth_file"] != "":
		ui.Done("Guests sign %s in with %s", AgentNames[agent], ui.Code(strings.TrimPrefix(update["auth_file"], "@")))
		ui.Info("Each guest refreshes this login on its own. If guests sign each other out, use %s", ui.Code("rix auth codex --api-key"))
	default:
		ui.Done("Guests sign %s in with it", AgentNames[agent])
	}
	ui.Hint("Running guests get it with " + ui.Code("rix apply NAME") + " or " + ui.Code("rix apply --all"))
	return nil
}

func syncCommand() *cobra.Command {
	c := &cobra.Command{Use: "sync", Short: "choose what of this machine's agent setup guests get"}
	var mcp []string
	set := &cobra.Command{
		Use:   "set claude|codex ITEMS",
		Short: "choose what's copied: a comma list of skills, instructions, mcp and settings, or none",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			agent := args[0]
			if Credentials[agent] == nil {
				return errors.New("Choose claude, codex or github")
			}
			chosen := map[string]bool{}
			for _, item := range strings.Split(args[1], ",") {
				item = strings.TrimSpace(item)
				switch item {
				case "", "none":
				case "skills", "instructions", "mcp", "settings":
					chosen[item] = true
				default:
					return fmt.Errorf("Unknown sync item %s. Use skills, instructions, mcp, settings or none", item)
				}
			}
			s := Sync{Skills: chosen["skills"], Instructions: chosen["instructions"], Settings: chosen["settings"], MCPAll: chosen["mcp"]}
			if len(mcp) > 0 {
				s.MCPAll, s.MCPNames = false, mcp
			}
			return setSync(agent, s)
		},
	}
	set.Flags().StringSliceVar(&mcp, "mcp", nil, "copy only these remote MCP servers")
	c.AddCommand(set, &cobra.Command{Use: "show", Short: "show what guests get from this machine", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := LoadConfig("")
			if err != nil {
				return err
			}
			agents, err := c.Agents()
			if err != nil {
				return err
			}
			for _, name := range AgentOrder {
				out := &Outcome{}
				plan := MakePlan(agents, map[string]string{name: ""}, out)
				what := plan.Describe()
				if what == "" {
					what = ui.Muted("nothing")
				}
				ui.Card(AgentNames[name], "", []ui.Field{{Label: "Copies", Value: what}})
				show(&Outcome{Warnings: out.Warnings})
			}
			return nil
		}})
	return c
}

func setSync(agent string, s Sync) error {
	err := Update(ConfigLocation(""), func(data map[string]any) error {
		agents, _ := data["agents"].(map[string]any)
		if agents == nil {
			agents = map[string]any{}
		}
		settings, _ := agents[agent].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		settings["sync"] = s.JSON()
		agents[agent] = settings
		data["agents"] = agents
		return nil
	})
	if err != nil {
		return err
	}
	c, _ := LoadConfig("")
	agents, err := c.Agents()
	if err != nil {
		return err
	}
	out := &Outcome{}
	what := MakePlan(agents, map[string]string{agent: ""}, out).Describe()
	if what == "" {
		ui.Done("Guests get none of this machine's %s setup", AgentNames[agent])
	} else {
		ui.Done("Guests get %s", what)
	}
	show(&Outcome{Warnings: out.Warnings})
	ui.Hint("Running guests get it with " + ui.Code("rix apply NAME"))
	return nil
}

func applyCommand() *cobra.Command {
	var all, quiet bool
	c := &cobra.Command{
		Use:   "apply NAME|--all",
		Short: "push credentials and agent setup into running guests",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if (len(args) == 1) == all {
				return errors.New("Name a guest or pass --all")
			}
			if quiet {
				ui.SetOutput(os.Stdout, &strings.Builder{})
			}
			var names []string
			if all {
				guests, err := Guests()
				if err != nil {
					return err
				}
				for _, g := range guests {
					if g.Phase == "ready" && len(g.Agents) > 0 {
						names = append(names, g.Name)
					}
				}
				if len(names) == 0 {
					ui.Skip("No running guests with agent CLIs")
					return nil
				}
			} else {
				names = args
			}
			var failed []string
			for _, name := range names {
				if err := Apply(name); err != nil {
					failed = append(failed, name)
					ui.Warn("%s: %s", name, firstLine(err.Error()))
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("Could not apply the vault to %s", strings.Join(failed, ", "))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "every running guest on this machine")
	c.Flags().BoolVar(&quiet, "quiet", false, "")
	_ = c.Flags().MarkHidden("quiet")
	return c
}

// Apply pushes credentials and sync into a running guest, skipping agents it doesn't have.
func Apply(name string) error {
	s := ui.Start("Reading %s", name)
	g, err := Inspect(name)
	if err != nil {
		s.Fail(err)
		return err
	}
	s.End()
	ui.Header("apply", name)
	c, err := LoadConfig("")
	if err != nil {
		return err
	}
	out := &Outcome{}
	if err := ui.Run("Setting the Git identity", "Set the Git identity", func(*ui.Step) error { return PushGitIdentity(c, g, out) }); err != nil {
		return err
	}
	if len(g.Agents) == 0 {
		show(out)
		ui.Skip("%s has no agent CLIs, so there's nothing to sign in", name)
		return nil
	}
	if err := ui.Run("Signing in agent CLIs", "Pushed credentials", func(*ui.Step) error { return PushCredentials(c, g, out) }); err != nil {
		return err
	}
	if err := ui.Run("Syncing agent setup", "Synced agent setup", func(*ui.Step) error { return SyncSetup(c, g, out) }); err != nil {
		return err
	}
	show(out)
	ui.Info("New shells get the credentials now. Agents that rex run starts get them after %s and %s",
		ui.Code("rox stop "+name), ui.Code("rox start "+name))
	return nil
}

func statusCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status [NAME]",
		Short: "show each guest's credentials, Git agent and last sync",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			guests, err := Guests()
			if err != nil {
				return err
			}
			type row struct {
				Name        string            `json:"name"`
				Agents      map[string]string `json:"agents"`
				Credentials []string          `json:"credentials"`
				GitAgent    string            `json:"git_agent"`
				GitKeys     []string          `json:"git_keys"`
				Synced      string            `json:"synced"`
			}
			var rows []row
			for _, g := range guests {
				if len(args) == 1 && g.Name != args[0] {
					continue
				}
				state, _ := loadState(g.Name)
				if state == nil {
					state = &GuestState{}
				}
				agent := "none"
				if len(state.GitKeys) > 0 {
					agent = "down"
					if agentStatus(agentSocket(g.Name)) == 0 {
						agent = "up"
					}
				}
				rows = append(rows, row{g.Name, g.Agents, state.Credentials, agent, state.GitKeys, state.Synced})
			}
			if len(args) == 1 && len(rows) == 0 {
				return fmt.Errorf("No guest named %s on this machine", args[0])
			}
			if asJSON {
				data, _ := json.MarshalIndent(map[string]any{"schema": contract.Schema, "guests": rows}, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			if len(rows) == 0 {
				ui.Skip("No guests on this machine")
				return nil
			}
			var table [][]string
			for _, r := range rows {
				var agents []string
				for _, name := range AgentOrder {
					if _, ok := r.Agents[name]; ok {
						mark := ui.WarnText("signed out")
						if containsString(r.Credentials, name) {
							mark = ui.Good("signed in")
						}
						agents = append(agents, AgentNames[name]+" "+mark)
					}
				}
				synced := "-"
				if t, err := time.Parse(time.RFC3339, r.Synced); err == nil {
					synced = t.Local().Format("Jan 2 15:04")
				}
				table = append(table, []string{ui.Bold(r.Name), firstNonEmpty(strings.Join(agents, ", "), "-"), ui.Phase(r.GitAgent), synced})
			}
			ui.Table([]string{"guest", "agents", "git agent", "synced"}, table)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the status as JSON")
	return c
}

func gitAgentCommand() *cobra.Command {
	c := &cobra.Command{Use: "git-agent", Short: "manage a guest's Git SSH agent"}
	c.AddCommand(
		&cobra.Command{Use: "start NAME", Short: "start the guest's Git agent, asking for key passphrases here", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				name := args[0]
				state, err := loadState(name)
				if err != nil {
					return err
				}
				keys := state.GitKeys
				if len(keys) == 0 {
					c, err := LoadConfig("")
					if err != nil {
						return err
					}
					if keys, _, err = resolveKeys(c, nil); err != nil {
						return err
					}
					state.Forward = true
				}
				if len(keys) == 0 {
					return errors.New("No Git SSH keys to load. Add one with rix key add PATH")
				}
				var pid int
				err = ui.Suspend(func() error {
					var err error
					pid, err = startAgent(name, agentSocket(name), keys, true)
					return err
				})
				if err != nil {
					return err
				}
				if pid > 0 {
					recordAgent(state, pid)
				}
				state.GitKeys = keys
				if err := saveState(name, state); err != nil {
					discardAgent(name, pid)
					return err
				}
				ui.Done("Git agent for %s holds %s", name, keyNames(keys))
				ui.Hint("Run " + ui.Code("rox reconnect "+name) + " to forward it into the guest")
				return nil
			}},
		&cobra.Command{Use: "stop NAME", Short: "stop the guest's Git agent", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				state, err := loadState(args[0])
				if err != nil {
					return err
				}
				stopAgent(args[0], state)
				if err := saveState(args[0], state); err != nil {
					return err
				}
				ui.Done("Stopped the Git agent for %s", args[0])
				return nil
			}},
		&cobra.Command{Use: "sock NAME", Short: "print the guest's Git agent socket", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				fmt.Println(agentSocket(args[0]))
				return nil
			}},
	)
	return c
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

func configCommand() *cobra.Command {
	return &cobra.Command{Use: "config", Short: "open the rix config in VISUAL or EDITOR", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path := ConfigLocation("")
			if !sys.IsFile(path) {
				if err := sys.WriteAtomic(path, []byte("{}\n"), 0o600, false); err != nil {
					return err
				}
			}
			editor := strings.Fields(firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
			result, err := sys.Try(sys.Cmd{Argv: append(editor, path), Interactive: true})
			if err != nil {
				return err
			}
			if result.Code != 0 {
				return fmt.Errorf("%s exited with status %d", editor[0], result.Code)
			}
			if _, err := sys.ReadJSONObject(path); err != nil {
				return fmt.Errorf("%v. Run rix config to fix it", err)
			}
			ui.Done("Saved %s", ui.Code(path))
			return nil
		}}
}

func selfUpdateCommand(u *selfupdate.Updater) *cobra.Command {
	var check bool
	c := &cobra.Command{Use: "self-update", Short: "install the latest rix release", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return u.SelfUpdate(check) }}
	c.Flags().BoolVar(&check, "check", false, "only say whether a newer release is out")
	return c
}

func migrateCommand() *cobra.Command {
	var roxConfig string
	var dryRun bool
	c := &cobra.Command{
		Use:   "migrate",
		Short: "move Git keys and agent credentials from the Python rox config into rix",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return Migrate(roxConfig, dryRun) },
	}
	c.Flags().StringVar(&roxConfig, "rox-config", "", "the rox config to migrate (default ~/.config/rox/config.json)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "say what would move without changing anything")
	return c
}

var guestName = regexp.MustCompile(`^rv-[a-z0-9-]{1,24}-[a-f0-9]{12}$`)

// Migrate moves git_keys, git_known_hosts, git_agent, the agents' credential paths and sync,
// and their secret files, from rox's config to rix's, and takes over running guests' Git keys.
func Migrate(roxConfig string, dryRun bool) error {
	roxPath := roxConfig
	if roxPath == "" {
		roxPath = filepath.Join(sys.XDG("XDG_CONFIG_HOME", ".config"), "rox", "config.json")
	} else {
		roxPath = sys.Expand(roxPath)
	}
	rixPath := ConfigLocation("")
	ui.Header("migrate", roxPath+" → "+rixPath)
	old, err := sys.ReadJSONObject(roxPath)
	if err != nil {
		return err
	}
	moved := map[string]any{}
	for _, key := range []string{"git_keys", "git_known_hosts", "git_agent"} {
		if v, ok := old[key]; ok {
			moved[key] = v
		}
	}
	oldAgents, _ := old["agents"].(map[string]any)
	newAgents := map[string]any{}
	var files [][2]string
	for _, name := range AgentOrder {
		value, _ := oldAgents[name].(map[string]any)
		if value == nil {
			continue
		}
		settings := map[string]any{}
		if s, ok := value["sync"]; ok {
			settings["sync"] = s
		}
		for setting := range Credentials[name] {
			file, ok := value[setting].(string)
			if !ok || file == "" {
				continue
			}
			path := sys.Expand(file)
			// Secret files rox saved beside its config move beside rix's.
			if filepath.Dir(path) == filepath.Dir(roxPath) {
				if base, ok := SecretFiles[[2]string{name, setting}]; ok && filepath.Base(path) == base {
					target := filepath.Join(filepath.Dir(rixPath), base)
					files = append(files, [2]string{path, target})
					path = target
				}
			}
			settings[setting] = path
		}
		newAgents[name] = settings
	}
	if len(newAgents) > 0 {
		moved["agents"] = newAgents
	}
	// Running guests' forwarded keys were in rox's record; rix keeps them now.
	stateRoot := filepath.Join(sys.XDG("XDG_STATE_HOME", ".local/state"), "rox")
	records, _ := filepath.Glob(filepath.Join(stateRoot, "*", "guest.json"))
	sort.Strings(records)
	var guests [][2]any
	for _, record := range records {
		name := filepath.Base(filepath.Dir(record))
		if !guestName.MatchString(name) {
			continue
		}
		data, err := os.ReadFile(record)
		if err != nil {
			continue
		}
		var g struct {
			GitKeys []string `json:"git_keys"`
		}
		if json.Unmarshal(data, &g) == nil && len(g.GitKeys) > 0 {
			guests = append(guests, [2]any{name, g.GitKeys})
		}
	}
	if len(moved) == 0 && len(files) == 0 && len(guests) == 0 {
		ui.Skip("Nothing to migrate: %s has no Git keys or agent credentials", roxPath)
	}
	for _, key := range sortedKeys(moved) {
		ui.Done("%s %s", map[bool]string{true: "Would move", false: "Moved"}[dryRun], ui.Code(key))
	}
	for _, f := range files {
		ui.Done("%s %s %s %s", map[bool]string{true: "Would move", false: "Moved"}[dryRun], f[0], ui.Muted("→"), f[1])
	}
	for _, g := range guests {
		ui.Done("%s the Git keys of %s", map[bool]string{true: "Would take over", false: "Took over"}[dryRun], g[0])
	}
	// Agents now come from each project. Reproduce the old launcher-wide list.
	snippet := map[string]any{}
	for _, name := range AgentOrder {
		value, present := oldAgents[name]
		if value == false {
			continue
		}
		pin := any(true)
		if m, ok := value.(map[string]any); ok {
			if v, ok := m["version"].(string); ok && v != "" && v != "latest" {
				pin = v
			}
		} else if present && value != nil {
			continue
		}
		snippet[name] = pin
	}
	if !dryRun && (len(moved) > 0 || len(files) > 0 || len(guests) > 0) {
		if _, err := sys.PrivateDir(filepath.Dir(rixPath)); err != nil {
			return err
		}
		for _, f := range files {
			if err := os.Rename(f[0], f[1]); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := Update(rixPath, func(data map[string]any) error {
			for k, v := range moved {
				if _, exists := data[k]; !exists {
					data[k] = v
				}
			}
			return nil
		}); err != nil {
			return err
		}
		for _, g := range guests {
			name := g[0].(string)
			state, _ := loadState(name)
			if state == nil {
				state = &GuestState{}
			}
			if len(state.GitKeys) == 0 {
				state.GitKeys, state.Forward = g[1].([]string), true
				_ = saveState(name, state)
			}
		}
		lock, err := sys.WaitLock(roxPath + ".lock")
		if err != nil {
			return err
		}
		current, err := sys.ReadJSONObject(roxPath)
		if err == nil {
			for _, key := range []string{"git_keys", "git_known_hosts", "git_agent", "agents"} {
				delete(current, key)
			}
			err = sys.WriteJSON(roxPath, current)
		}
		lock.Unlock()
		if err != nil {
			return err
		}
		ui.Done("Removed the moved settings from %s", ui.Code(roxPath))
	}
	if len(snippet) > 0 {
		data, _ := json.Marshal(map[string]any{"agents": snippet})
		ui.Blank()
		ui.Info("rox used to install %s in every guest. Projects choose agents now.", agentList(snippet))
		ui.Info("To keep that for a checkout, put this in its %s (or a branch's .rox.json):", ui.Code(".rox.local.json"))
		ui.Say("%s", string(data))
	}
	return nil
}

func agentList(m map[string]any) string {
	var names []string
	for _, name := range AgentOrder {
		if _, ok := m[name]; ok {
			names = append(names, AgentNames[name])
		}
	}
	return strings.Join(names, " and ")
}

func detail(text string) {
	for _, line := range strings.Split(text, "\n") {
		ui.Detail("%s", line)
	}
}

// Where Anthropic API keys are made, and how to install Claude Code.
const (
	anthropicKeysURL  = "https://platform.claude.com/settings/keys"
	claudeCodeInstall = "https://code.claude.com/docs/en/setup"
)

// claudeCredential asks for the token or key guests sign Claude Code in with. With Claude
// Code on this machine, it offers to run claude setup-token first, which signs in with
// the browser and prints a token for the Claude subscription.
func claudeCredential() (string, error) {
	_, err := sys.R.LookPath("claude")
	haveClaude := err == nil
	if ui.IsTerminal() {
		ui.Info("Guests sign Claude Code in with either:")
		detail("a subscription token, for Claude Pro or Max (sk-ant-oat01-…)\n" +
			"  claude setup-token signs in with your browser and prints one that lasts a year\n" +
			"an Anthropic API key, billed per use (sk-ant-api03-…)\n" +
			"  create one at " + anthropicKeysURL)
		if haveClaude && ui.Confirm("Run claude setup-token now to create a subscription token?") {
			err := ui.Suspend(func() error {
				fmt.Fprintln(os.Stderr)
				cmd := exec.Command("claude", "setup-token")
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				return cmd.Run()
			})
			if err != nil {
				ui.Warn("claude setup-token didn't finish (%v). You can still paste a token or API key", err)
			}
			return ui.Secret("Paste the token it printed (sk-ant-oat01-…):", "Leave it blank to keep guests signed out.")
		}
		if !haveClaude {
			detail("Claude Code isn't installed here. To make a subscription token, install it\n  " + claudeCodeInstall + "\nand run claude setup-token, here or on any machine with it.")
		}
	}
	return ui.Secret("Claude Code token or Anthropic API key:", "Leave it blank to keep guests signed out.")
}
