package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/contract"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// UpOptions are rox up's and rox add's flags.
type UpOptions struct {
	ProjectArgs
	Backend       string
	Config        string
	Image         string
	CPUs          int
	CPUsSet       bool
	Memory        string
	Engine        string
	BootTimeout   time.Duration
	NoHook        bool
	NoPrebuild    bool
	Rebuild       bool
	NoTailscale   bool
	NoGitAgent    bool
	NoCredentials bool // rox up --credentials=false: hooks push no agent sign-in
	NoAgents      bool
	Agents        string
	GitKeys       []string
	GitKnownHosts string
	OpenShell     bool
	LocalSettings string
	// Vault chooses whose rix vault a launch on another host uses: "host" (the default) or "here".
	Vault   string
	Command []string
}

func (o *UpOptions) launch(url string) *contract.Launch {
	l := &contract.Launch{URL: url, GitKeys: o.GitKeys, GitKnownHosts: o.GitKnownHosts, NoGitAgent: o.NoGitAgent}
	if o.NoCredentials {
		no := false
		l.Credentials = &no
	}
	return l
}

var memoryPattern = regexp.MustCompile(`(?i)^[0-9]+[KMGT]$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]*$`)

// remoteOptions carries the launch's options to rox on another host.
func remoteOptions(o *UpOptions) []string {
	var out []string
	add := func(flag, value string) {
		if value != "" {
			out = append(out, "--"+flag+"="+value)
		}
	}
	add("config", o.Config)
	add("image", o.Image)
	add("engine", o.Engine)
	add("memory", o.Memory)
	add("git-known-hosts", o.GitKnownHosts)
	add("agents", o.Agents)
	if o.CPUsSet {
		out = append(out, fmt.Sprintf("--cpus=%d", o.CPUs))
	}
	for _, key := range o.GitKeys {
		out = append(out, "--git-key="+key)
	}
	for flag, on := range map[string]bool{"no-hook": o.NoHook, "no-prebuild": o.NoPrebuild, "rebuild": o.Rebuild,
		"no-tailscale": o.NoTailscale, "no-agents": o.NoAgents, "no-git-agent": o.NoGitAgent} {
		if on {
			out = append(out, "--"+flag)
		}
	}
	if o.NoCredentials {
		out = append(out, "--credentials=false")
	}
	if o.Vault == "here" {
		// The host's vault still gives the clone its Git keys; credentials and setup come from here.
		out = append(out, "--hooks=git")
	}
	sortStrings(out)
	return out
}

func projectOptions(src *Source) []string {
	if src == nil {
		return []string{"--no-project"}
	}
	args := []string{"--project=" + src.Name, "--repo=" + src.URL}
	if src.Branch != "" { // otherwise the remote rox finds the default branch with its own vault
		args = append(args, "--branch="+src.Branch)
	}
	return args
}

// Up starts and provisions a new guest.
func Up(o *UpOptions) error {
	if o.Host != "" && strings.TrimSpace(o.Host) == "" {
		return errors.New("--host needs a host to run on")
	}
	if o.Vault != "" && o.Vault != "host" && o.Vault != "here" {
		return errors.New("Expected host or here for --vault")
	}
	if o.Vault == "here" && o.Host == "" {
		return errors.New("--vault here is for launches on another host with --host")
	}
	src, err := ResolveProject(o.ProjectArgs, false)
	if err != nil {
		return err
	}
	checkout, err := localCheckout(o.ProjectArgs)
	if err != nil {
		return err
	}
	local := map[string]any{}
	if src != nil {
		if local, err = localProjectSettings(o.ProjectArgs, o.LocalSettings); err != nil {
			return err
		}
	}
	detail := "a guest without a project"
	if src != nil {
		detail = src.Name + " · " + firstNonEmpty(src.Branch, "default branch")
	}
	if o.Backend != "" {
		detail += " → " + o.Backend
	}
	if o.Host != "" {
		ui.Header("up", detail+" on "+o.Host)
		args := append([]string{"up"}, o.Backend)
		if o.Backend == "" {
			args = []string{"up"}
		}
		args = append(args, projectOptions(src)...)
		args = append(args, fmt.Sprintf("--boot-timeout=%d", int(o.BootTimeout.Seconds())))
		if len(local) > 0 {
			data, _ := json.Marshal(local)
			args = append(args, "--local-settings="+string(data))
		}
		args = append(args, remoteOptions(o)...)
		if len(o.Command) > 0 {
			args = append(append(args, "--"), o.Command...)
		}
		label := "host"
		if src != nil {
			label = src.Name
		}
		text, err := dispatch(o.Host, label, args)
		if err != nil {
			return err
		}
		if o.Vault == "here" {
			applyHere(text)
		}
		afterRemoteLaunch(text, o.Host, o.OpenShell)
		return nil
	}
	ui.Header("up", detail)
	if src == nil {
		ui.Info("Starting a guest without a project. Add one later with %s", ui.Code("rox add NAME"))
	}
	config, err := LoadConfig(o.Config)
	if err != nil {
		return err
	}
	config.warnMovedSettings()
	tailnet, err := config.TailnetSettings(o.NoTailscale)
	if err != nil {
		return err
	}
	flagAgents := AgentSpec{}
	if o.Agents != "" {
		if flagAgents, err = ParseAgentFlag(o.Agents); err != nil {
			return err
		}
	}

	var branch *Branch
	var lease *Lease
	defer func() { lease.Close() }()
	projectConfig := map[string]any{}
	if checkout != "" {
		// The guest gets this checkout as it is, so its own .rox.json applies, not the remote branch's.
		if projectConfig, err = ReadProjectFile(filepath.Join(checkout, ProjectConfig), ProjectConfig); err != nil {
			return err
		}
	} else if src != nil {
		if err := sys.Require("git"); err != nil {
			return err
		}
		if isSSHRemote(src.URL) {
			lease = fetchLease(o.launch(src.URL))
		}
		if err := DefaultBranch(src, lease); err != nil {
			return err
		}
		if branch, err = ReadBranch(src, lease); err != nil {
			return err
		}
		if branch != nil {
			projectConfig = branch.Settings
		}
	}
	backend := firstNonEmpty(o.Backend, str(local["backend"]), str(projectConfig["backend"]))
	if backend == "" {
		return fmt.Errorf("Choose a backend with rox up container, linux, macos, fly or railway, or set \"backend\" in %s", ProjectConfig)
	}
	if o.Backend == "" {
		where := ProjectConfig
		if str(local["backend"]) != "" {
			where = LocalConfig
		}
		ui.Info("Using the %s backend from %s", ui.Bold(backend), where)
	}
	agents := AgentSpec{}
	if !o.NoAgents {
		agents = MergeAgents(projectAgents(projectConfig, ProjectConfig), projectAgents(local, LocalConfig), flagAgents)
	}

	launcherSettings, err := config.Section(backend)
	if err != nil {
		return err
	}
	projectSection, _ := projectConfig[backend].(map[string]any)
	localSection, _ := local[backend].(map[string]any)
	// The branch's settings override the launcher's, the checkout's override the branch's, and flags override all.
	settings := map[string]any{}
	for _, layer := range []map[string]any{launcherSettings, projectSection, localSection} {
		for k, v := range layer {
			settings[k] = v
		}
	}
	origin := func(key string) string {
		if _, ok := localSection[key]; ok {
			return LocalConfig
		}
		if _, ok := projectSection[key]; ok {
			return ProjectConfig
		}
		return config.Path
	}
	if o.Engine != "" && backend != "container" {
		return errors.New("--engine applies to the container backend")
	}
	engine := ""
	if backend == "container" {
		engine = firstNonEmpty(o.Engine, str(settings["engine"]), DefaultEngine())
		if !contains(Engines, engine) {
			return errors.New("Choose container, docker or podman as the container engine")
		}
	}
	cpus := o.CPUs
	if !o.CPUsSet {
		if raw, ok := settings["cpus"]; ok {
			n, ok := intSetting(raw)
			if _, isBool := raw.(bool); !ok || isBool || n < 0 {
				return fmt.Errorf("Expected a whole number of CPUs for %s.cpus in %s", backend, origin("cpus"))
			}
			cpus = n
		}
	}
	memory := o.Memory
	if memory == "" {
		if raw, ok := settings["memory"]; ok {
			m, ok := raw.(string)
			if !ok || (m != "" && !memoryPattern.MatchString(m)) {
				return fmt.Errorf("Expected a size such as 4G or 512M for %s.memory in %s", backend, origin("memory"))
			}
			memory = m
		}
	} else if !memoryPattern.MatchString(memory) {
		return errors.New("Expected a size such as 4G or 512M for --memory")
	}
	var flyCfg *FlySettings
	var railwayCfg *RailwaySettings
	image := ""
	switch backend {
	case "railway":
		if railwayCfg, err = railwaySettings(settings, config.Path); err != nil {
			return err
		}
		// Railway sizes sandboxes by plan and runs only its own image, which rox's template builds on.
		for option, set := range map[string]bool{"image": o.Image != "" || str(settings["image"]) != "", "cpus": cpus != 0, "memory": memory != ""} {
			if set {
				return fmt.Errorf("The railway backend takes no %s. Railway sizes sandboxes by plan", option)
			}
		}
	case "fly":
		if flyCfg, err = flySettings(settings, config.Path); err != nil {
			return err
		}
		// No image means rox's own, which Fly builds and stores in the app's registry.
		image = firstNonEmpty(o.Image, str(settings["image"]))
		if strings.HasPrefix(image, "-") {
			return fmt.Errorf("Expected an image name for --image or fly.image in %s", origin("image"))
		}
	default:
		fallback := ""
		if engine != "" {
			fallback = DefaultContainerImage
		}
		image = firstNonEmpty(o.Image, str(settings["image"]), fallback)
		if image == "" || strings.HasPrefix(image, "-") {
			return fmt.Errorf("Choose a %s image with --image or in %s", backend, origin("image"))
		}
	}
	identity, hostKey, user := "", "", "workspace"
	if engine == "" && !isCloud(backend) {
		user = firstNonEmpty(str(settings["user"]), "admin")
		if identity, err = sys.ConfiguredFile(str(settings["identity"])); err != nil {
			return err
		}
		if hostKey, err = sys.ConfiguredFile(str(settings["host_key"])); err != nil {
			return err
		}
		if identity == "" || hostKey == "" || !userPattern.MatchString(user) {
			return fmt.Errorf("Configure user, identity and host_key for %s in %s, or run %s", backend, config.Path, ui.Code("rox template "+backend))
		}
	}
	prebuild := true
	if raw, ok := settings["prebuild"]; ok {
		b, ok := raw.(bool)
		if !ok {
			return fmt.Errorf("Expected true or false for %s.prebuild in %s", backend, origin("prebuild"))
		}
		prebuild = b
	}
	// A project image holds a remote branch, and a --local launch runs .rox.sh on the copied checkout.
	prebuild = prebuild && src != nil && !o.NoPrebuild && !o.NoHook && checkout == "" && !isCloud(backend)
	if o.Rebuild && !prebuild {
		return errors.New("--rebuild needs a project image, which --no-project, --no-hook, --no-prebuild, --local, prebuild: false and the fly and railway backends disable")
	}

	required := []string{"ssh", "ssh-keygen"}
	switch {
	case engine != "":
		required = append(required, engine)
	case !isCloud(backend):
		required = append(required, "tart")
	}
	if engine == "container" && !sys.Has("container") {
		return errors.New("Install Apple's container tool (https://github.com/apple/container), or choose --engine docker or --engine podman")
	}
	if err := sys.Require(required...); err != nil {
		return err
	}
	probe := NewGuest("", backend, "", "", engine)
	if err := backendFor(probe).Ensure(); err != nil {
		return err
	}
	if _, err := sys.PrivateDir(StateRoot()); err != nil {
		return err
	}
	if engine != "" {
		if err := PrepareBaseImage(engine, image); err != nil {
			return err
		}
	}
	if flyCfg != nil {
		account, err := flyAccount()
		if err != nil {
			return err
		}
		flyCfg.App = firstNonEmpty(flyCfg.App, flyAppName(account))
		if err := ensureFlyApp(flyCfg.App, flyCfg.Org); err != nil {
			return err
		}
		if image == "" {
			if image, err = flyImage(flyCfg.App); err != nil {
				return err
			}
		}
	}
	if railwayCfg != nil {
		if _, err := railwayCLI(); err != nil {
			return err
		}
	}
	versions := map[string]string{}
	if len(agents) > 0 {
		versions = AgentVersions(agents, false)
	}
	prebuilt := ""
	if prebuild {
		prebuilt, err = backendFor(probe).Prebuild(&PrebuildRequest{Backend: backend, Base: image, Project: src.Name, URL: src.URL,
			Branch: src.Branch, Branch_: branch, Rebuild: o.Rebuild, User: user, Identity: identity, HostKey: hostKey,
			Boot: o.BootTimeout, CPUs: cpus, Memory: memory, Versions: versions, Lease: lease})
		if err != nil {
			return err
		}
	}
	lease.Close()
	lease = nil
	launchImage := firstNonEmpty(prebuilt, image)
	if engine != "" && len(versions) > 0 {
		if launchImage, err = PrepareAgentImage(engine, launchImage, versions); err != nil {
			return err
		}
	}

	slug := "host"
	if src != nil {
		slug = src.Name
	}
	name := "rv-" + firstNonEmpty(Slugify(slug, 24), "project") + "-" + randomHex(6)
	g := NewGuest(name, backend, launchImage, user, engine)
	g.Prebuilt, g.CPUs, g.Memory = prebuilt != "", cpus, memory
	if flyCfg != nil {
		g.App, g.Region, g.Disk = flyCfg.App, flyCfg.Region, flyCfg.Disk
	}
	if railwayCfg != nil {
		g.RailwayProject, g.RailwayEnvironment, g.RailwayIdleTimeout = railwayCfg.Project, railwayCfg.Environment, railwayCfg.IdleTimeout
	}
	if err := os.Mkdir(g.Directory(), 0o700); err != nil {
		return err
	}
	g.Identity = firstNonEmpty(identity, filepath.Join(g.Directory(), "login"))
	l, err := lockGuest(g, false)
	if err != nil {
		return err
	}
	defer l.Unlock()
	if err := g.Save(); err != nil {
		return err
	}
	fail := func(err error) error {
		g.Phase = "failed"
		g.MustSave()
		ui.Hint("Workspace " + g.Name + " was kept. Remove it with " + ui.Code("rox down "+g.Name))
		return err
	}
	launch := o.launch("")
	launch.Config, launch.NoTailscale, launch.Command = config.Path, o.NoTailscale, o.Command
	if tailnet != nil && !hasProvider("rox-rex") {
		ui.Warn("The tailscale setting needs the rox-rex hook, which isn't installed, so %s won't join the tailnet", g.Name)
	}
	if err := bootGuest(g, hostKey, o.BootTimeout, versions, launch); err != nil {
		return fail(err)
	}
	g.Phase = "ready"
	g.MustSave()
	var project *Project
	if src != nil {
		if project, err = provisionProject(g, src, o, prebuilt != "", checkout); err != nil {
			return err
		}
	}
	postProvision(g, project, sortedAgents(versions), launch)
	describeGuest(g, project)
	l.Unlock()
	if o.OpenShell {
		return openShell(g, project)
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// bootGuest launches the guest and readies it: agents, then the post-boot hooks, which sign agents in
// and let providers such as rox-rex start their own servers.
func bootGuest(g *Guest, hostKey string, boot time.Duration, versions map[string]string, launch *contract.Launch) error {
	b := backendFor(g)
	s := ui.Start("Starting %s", ui.Bold(g.Name))
	if hostKey != "" {
		data, err := os.ReadFile(hostKey)
		if err == nil {
			err = pinHost(g, string(data))
		}
		if err != nil {
			s.Fail(err)
			return err
		}
	}
	if err := ensureRuntimeDirs(g); err != nil {
		s.Fail(err)
		return err
	}
	addr, port, err := b.Launch(g, boot, s)
	if err == nil {
		g.Address, g.Port = addr, port
		g.MustSave()
		s.Note("waiting for SSH")
		err = checkGuestOS(g, boot)
	}
	var out string
	if err == nil {
		out, err = guestStage(g, "prepare", stageOpts{}, g.Name)
	}
	if err == nil {
		paths := sys.Lines(out)
		if len(paths) != 2 || !strings.HasPrefix(paths[0], "/") || !strings.HasPrefix(paths[1], "/") || strings.ContainsAny(out, ":") {
			err = errors.New("Guest preparation did not return valid state and socket paths")
		} else {
			g.GuestSocket = paths[1]
			g.MustSave()
		}
	}
	if err != nil {
		s.Fail(err)
		return err
	}
	s.Done("Started %s %s", ui.Bold(g.Name), ui.Muted("· "+g.Backend+" "+b.Kind()))
	postBoot := *launch
	if len(versions) > 0 {
		fresh, err := installAgents(g, versions)
		if err != nil {
			return err
		}
		postBoot.NewAgents = fresh
	}
	// Credentials first, so a provider that starts a server reads them: rix is called before rox-rex.
	if reply, _ := runHook(contract.PostBoot, g, &postBoot); reply != nil && reply.Endpoint != "" {
		g.TailnetName = reply.Endpoint
		g.MustSave()
	}
	return nil
}

// postProvision tells the hooks the project (or, without one, the guest) is set up. A provider
// that keeps sessions answers with one, which rox records.
func postProvision(g *Guest, p *Project, fresh []string, base *contract.Launch) {
	launch := *base
	launch.NewAgents = fresh
	if p != nil {
		launch.Project = p.ID
	}
	reply, _ := runHook(contract.PostProvision, g, &launch)
	if reply != nil {
		switch {
		case reply.Session != "" && p != nil:
			p.SessionID = reply.Session
		case reply.Session != "":
			g.SessionID = reply.Session
		}
		if reply.Endpoint != "" {
			g.TailnetName = reply.Endpoint
		}
		g.MustSave()
	}
	if len(launch.Command) > 0 && (reply == nil || reply.Session == "") {
		ui.Warn("Didn't run the command after --: it runs in a session, which needs the rox-rex hook. Run it with rox exec %s -- COMMAND", g.Name)
	}
}

// provisionProject clones a project into a ready guest and runs its hook.
func provisionProject(g *Guest, src *Source, o *UpOptions, imageCheckout bool, local string) (*Project, error) {
	p := g.AddProject(src.Name, src.URL, src.Branch)
	// Checkouts go in the guest home. A project image prepared the first project's there already.
	p.Workspace = filepath.Join(g.Home(), p.ID)
	g.MustSave()
	err := func() error {
		// rix starts the guest's Git agent and lends it for the clone, with its pinned server keys.
		knownHosts := ""
		agent := ""
		forward := false
		if isSSHRemote(src.URL) {
			reply, err := runHook(contract.PreClone, g, o.launch(src.URL))
			if err != nil {
				return err
			}
			if reply != nil {
				agent, forward, knownHosts = reply.AgentSocket, reply.Forward, reply.KnownHosts
			} else if !hasProvider("rix") {
				ui.Warn("No rix on this machine, so the guest clones %s without Git keys", src.URL)
			}
			if knownHosts == "" {
				knownHosts = gitHostEntries(src.URL)
			}
		}
		pins := filepath.Join(g.Base(), "git_known_hosts")
		if knownHosts != "" {
			if _, err := guestRun(g, []string{"bash", "-c", `umask 077; mkdir -p "${1%/*}" && cat > "$1"`, "rox", pins}, knownHosts, 30*time.Second); err != nil {
				return err
			}
		}
		args := []string{p.Workspace, src.URL, src.Branch, yesNo(agent != ""), pins}
		if local != "" {
			args = append(args, "local")
		}
		verb := "Cloned"
		if imageCheckout {
			verb = "Updated the image's checkout of"
		}
		err := ui.Run(fmt.Sprintf("Cloning %s (%s)", src.Name, src.Branch), fmt.Sprintf("%s %s (%s) %s", verb, src.Name, src.Branch, ui.Muted("→ "+p.Workspace)), func(*ui.Step) error {
			_, err := guestStage(g, "clone", stageOpts{Agent: agent, Timeout: 30 * time.Minute}, args...)
			return err
		})
		if err != nil {
			return err
		}
		if local != "" {
			if err := ui.Run("Copying "+local, "Copied this checkout's work into the guest", func(*ui.Step) error {
				return sendLocal(g, p.Workspace, local, src.Branch)
			}); err != nil {
				return err
			}
		}
		mode := fmt.Sprint(o.NoHook)
		if imageCheckout {
			mode = "prebuilt"
		}
		hookStep := ui.Start("Setting up %s", p.ID)
		out, err := guestStage(g, "setup", stageOpts{Timeout: 2 * time.Hour}, p.Workspace, g.Name, g.Backend, g.GuestSocket, mode)
		if err != nil {
			hookStep.Fail(err)
			return err
		}
		_ = out
		switch {
		case imageCheckout:
			hookStep.Skip(".rox.sh already ran in the project image")
		case o.NoHook:
			hookStep.Skip("Skipped .rox.sh (--no-hook)")
		default:
			hookStep.Done("Set up %s", p.ID)
		}
		p.Phase = "ready"
		g.MustSave()
		// After .rox.sh, which runs without Git keys, so only Git and agents in the guest get them.
		if forward && agent != "" {
			if err := startGitForward(g, agent); err != nil {
				ui.Warn("Git in %s can't use your SSH keys: %v", g.Name, err)
			} else {
				ui.Done("Git keys forwarded into the guest %s", ui.Muted("· never copied"))
			}
		}
		return nil
	}()
	if err != nil {
		p.Phase = "failed"
		g.MustSave()
		ui.Warn("Setting up %s failed", p.ID)
		ui.Hint("Workspace " + g.Name + " was kept. Remove it with " + ui.Code("rox down "+g.Name))
		return nil, err
	}
	return p, nil
}

func describeGuest(g *Guest, p *Project) {
	fields := []ui.Field{{Label: "Workspace", Value: g.Name}, {Label: "Tailnet", Value: g.TailnetName}}
	if len(g.Agents) > 0 {
		fields = append(fields, ui.Field{Label: "Agents", Value: DescribeAgents(g.Agents)})
	}
	session := g.SessionID
	if p != nil {
		fields = append(fields, ui.Field{Label: "Project", Value: p.ID}, ui.Field{Label: "Checkout", Value: p.Workspace})
		session = p.SessionID
	}
	if session != "" {
		fields = append(fields, ui.Field{Label: "Session", Value: session})
	}
	fields = append(fields, ui.Field{Label: "Shell", Value: ui.Code("rox shell " + g.Name)})
	status := g.Phase
	if g.Stopped {
		status = "stopped"
	}
	ui.Card(g.Name, status, fields)
}

// applyHere pushes this machine's credentials and agent setup into a guest launched elsewhere.
func applyHere(text string) {
	workspace, _, _ := parseProvision(text)
	if workspace == "" {
		return
	}
	rix, ok := providerNamed("rix")
	if !ok {
		ui.Warn("--vault here needs rix on this machine")
		return
	}
	provider := rix.Path
	s := ui.Start("Applying this machine's vault to %s", workspace)
	result, err := sys.Try(sys.Cmd{Argv: []string{provider, "apply", workspace, "--quiet"}, Timeout: 10 * time.Minute, Env: map[string]string{"ROX_BIN": selfPath()}})
	if err == nil && result.Code != 0 {
		err = &sys.ExitError{Argv: []string{provider}, Result: result, Message: "rix apply failed for " + workspace}
	}
	if err != nil {
		s.Warn("Could not apply this machine's vault to %s: %s", workspace, firstLine(err.Error()))
		return
	}
	s.Done("Applied this machine's credentials and agent setup to %s", workspace)
}

// Add clones another project into a running guest.
func Add(name string, o *UpOptions) error {
	if o.Host != "" && strings.TrimSpace(o.Host) == "" {
		return errors.New("--host needs a host to run on")
	}
	if !NamePattern.MatchString(name) {
		return fmt.Errorf("Invalid workspace name %q", name)
	}
	src, err := ResolveProject(o.ProjectArgs, true)
	if err != nil {
		return err
	}
	checkout, err := localCheckout(o.ProjectArgs)
	if err != nil {
		return err
	}
	if o.Host == "" && !IsLocal(name) {
		host, _, err := findRemoteGuest(name, "")
		if err != nil {
			return err
		}
		o.Host = host
	}
	if o.Host != "" {
		ui.Header("add", src.Name+" · "+firstNonEmpty(src.Branch, "default branch")+" → "+name+" on "+o.Host)
		args := append([]string{"add", name}, projectOptions(src)...)
		args = append(args, remoteOptions(o)...)
		if len(o.Command) > 0 {
			args = append(append(args, "--"), o.Command...)
		}
		text, err := dispatch(o.Host, src.Name, args)
		if err != nil {
			return err
		}
		if o.Vault == "here" {
			applyHere(text)
		}
		afterRemoteLaunch(text, o.Host, o.OpenShell)
		return nil
	}
	if checkout == "" {
		var lease *Lease
		if isSSHRemote(src.URL) {
			lease = fetchLease(o.launch(src.URL))
		}
		err := DefaultBranch(src, lease)
		lease.Close()
		if err != nil {
			return err
		}
	}
	ui.Header("add", src.Name+" · "+src.Branch+" → "+name)
	if _, err := LoadConfig(o.Config); err != nil {
		return err
	}
	if err := sys.Require("ssh"); err != nil {
		return err
	}
	// The added project may list agents of its own.
	projectConfig := map[string]any{}
	local, err := localProjectSettings(o.ProjectArgs, o.LocalSettings)
	if err != nil {
		return err
	}
	if checkout != "" {
		if projectConfig, err = ReadProjectFile(filepath.Join(checkout, ProjectConfig), ProjectConfig); err != nil {
			return err
		}
	} else if !o.NoAgents {
		var lease *Lease
		if isSSHRemote(src.URL) {
			lease = fetchLease(o.launch(src.URL))
		}
		branch, err := ReadBranch(src, lease)
		lease.Close()
		if err != nil {
			return err
		}
		if branch != nil {
			projectConfig = branch.Settings
		}
	}
	flagAgents := AgentSpec{}
	if o.Agents != "" {
		if flagAgents, err = ParseAgentFlag(o.Agents); err != nil {
			return err
		}
	}
	wanted := AgentSpec{}
	if !o.NoAgents {
		wanted = MergeAgents(projectAgents(projectConfig, ProjectConfig), projectAgents(local, LocalConfig), flagAgents)
	}
	var project *Project
	var g *Guest
	err = withGuest(name, false, func(guest *Guest) error {
		g = guest
		if g.Stopped {
			return fmt.Errorf("%s is stopped. Start it with rox start %s before adding a project", name, name)
		}
		if g.Phase != "ready" || g.GuestSocket == "" {
			return fmt.Errorf("%s is %s. Run rox reconnect %s before adding a project", name, g.Phase, name)
		}
		if _, err := resume(g, 120*time.Second, true); err != nil {
			return err
		}
		missing := AgentSpec{}
		for agent, version := range wanted {
			if have, ok := g.Agents[agent]; !ok || (version != "latest" && have != version) {
				missing[agent] = version
			}
		}
		var fresh []string
		if len(missing) > 0 {
			var err error
			if fresh, err = installAgents(g, AgentVersions(missing, false)); err != nil {
				return err
			}
		}
		var err error
		if project, err = provisionProject(g, src, o, false, checkout); err != nil {
			return err
		}
		addLaunch := o.launch("")
		addLaunch.Config, addLaunch.Command = o.Config, o.Command
		postProvision(g, project, fresh, addLaunch)
		describeGuest(g, project)
		return nil
	})
	if err != nil {
		return err
	}
	if o.OpenShell {
		return openShell(g, project)
	}
	return nil
}

// resume brings a guest back after its runtime, the guest or its tunnel stopped. A guest
// that's running and reachable is left alone unless force is set. It reports whether
// anything had to be restored.
func resume(g *Guest, boot time.Duration, force bool) (bool, error) {
	b := backendFor(g)
	if err := b.Ensure(); err != nil {
		return false, err
	}
	state, err := b.State(g)
	if err != nil {
		return false, err
	}
	switch state {
	case "missing":
		return false, fmt.Errorf("The %s for %s no longer exists. Remove the workspace with rox down %s", b.Kind(), g.Name, g.Name)
	case "stopped":
		s := ui.Start("Starting %s", g.Name)
		err := b.Start(g, boot)
		if err == nil {
			var addr string
			var port int
			addr, port, err = waitAddress(b, g, boot, g.Name+" to get an address")
			if err == nil {
				g.Address, g.Port = addr, port
				g.MustSave()
				err = checkGuestOS(g, boot)
			}
		}
		if err != nil {
			s.Fail(err)
			return false, err
		}
		s.Done("Started %s", g.Name)
	default:
		if !force && guestReachable(g) {
			restored := restoreFunnels(g)
			return restoreGitAccess(g) || restored, nil
		}
	}
	// A tunnel to the old address can pass its check until SSH notices the guest went away.
	for _, f := range g.Funnels {
		closeFunnelTunnel(g, f)
	}
	closeGitForward(g)
	if err := refreshAddress(g); err != nil {
		return false, err
	}
	restoreFunnels(g)
	restoreGitAccess(g)
	return true, nil
}

// restoreGitAccess calls the resume hooks: rix brings back the guest's Git agent, which rox forwards
// again, and rox-rex restores its Rex server and connection.
func restoreGitAccess(g *Guest) bool {
	if sys.PidAlive(gitForwardPid(g)) {
		return false
	}
	reply, _ := runHook(contract.Resume, g, nil)
	if reply != nil && reply.Endpoint != "" && reply.Endpoint != g.TailnetName {
		g.TailnetName = reply.Endpoint
		g.MustSave()
	}
	if reply == nil || reply.AgentSocket == "" || !reply.Forward {
		return false
	}
	if err := startGitForward(g, reply.AgentSocket); err != nil {
		ui.Warn("Git in %s can't use your SSH keys until this works: %v", g.Name, err)
		return false
	}
	return true
}

// readyGuest loads a guest for a command that needs it running, restoring it if needed.
func readyGuest(name string) (*Guest, error) {
	g, err := LoadGuest(name)
	if err != nil {
		return nil, err
	}
	if g.Stopped {
		return nil, fmt.Errorf("%s is stopped. Start it with rox start %s", name, name)
	}
	if g.Phase != "ready" {
		return nil, fmt.Errorf("%s is not ready", name)
	}
	if state, err := backendFor(g).State(g); err == nil && state == "running" && guestReachable(g) {
		// A restart breaks the tunnel, so a reachable guest is still at its saved address.
		return g, nil
	}
	err = withGuest(name, true, func(guest *Guest) error {
		g = guest
		_, err := resume(g, 120*time.Second, false)
		return err
	})
	return g, err
}

// Reconnect starts the guest if it stopped, and restores its connections and, with the hooks installed, its Rex server and tailnet node.
func Reconnect(name string, force bool) error {
	verb := "reconnect"
	if !force {
		verb = "start"
	}
	ui.Header(verb, name)
	return withGuest(name, false, func(g *Guest) error {
		if g.GuestSocket == "" || g.Phase == "removing" {
			return fmt.Errorf("%s did not get far enough to reconnect", name)
		}
		// Only a guest whose first boot finished is ready. A failed or interrupted boot may be
		// missing agents, credentials or what the hook providers set up, so it is not promoted.
		if g.Phase != "ready" {
			return fmt.Errorf("%s did not finish setting up (phase %s). Remove it with rox down %s and launch it again", name, g.Phase, name)
		}
		g.Stopped = false
		g.MustSave()
		restored, err := resume(g, 120*time.Second, force)
		if err != nil {
			return err
		}
		if !restored {
			ui.Skip("%s is running and reachable", name)
		}
		g.Phase = "ready"
		g.MustSave()
		describeGuest(g, nil)
		if len(g.Projects) > 0 {
			var rows [][]string
			for _, p := range g.Projects {
				rows = append(rows, []string{p.ID, ui.Phase(p.Phase), p.Workspace})
			}
			ui.Say("")
			ui.Table([]string{"project", "phase", "checkout"}, rows)
		}
		return nil
	})
}

// Stop stops the guest and keeps it stopped until rox start, deleting nothing.
func Stop(name string) error {
	ui.Header("stop", name)
	return withGuest(name, false, func(g *Guest) error {
		if g.Phase == "removing" {
			return fmt.Errorf("%s is being removed", name)
		}
		if g.Backend == "railway" {
			return fmt.Errorf("Railway sandboxes can't be stopped and started again. Remove %s with rox down", name)
		}
		b := backendFor(g)
		// Saved state and live connections change only once the backend is really stopped.
		err := ui.Run("Stopping "+g.Name, "Stopped "+g.Name, func(*ui.Step) error {
			state, err := b.State(g)
			if err != nil {
				return err
			}
			if state != "running" {
				return nil
			}
			if err := b.Stop(g); err != nil {
				// A timeout can still have stopped it, so ask again before giving up.
				if after, serr := b.State(g); serr == nil && after != "running" {
					return nil
				}
				return err
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%w (%s keeps its saved state and connections)", err, name)
		}
		g.Stopped = true
		g.MustSave()
		stopFunnels(g, nil)
		closeGitForward(g)
		return nil
	})
}

// Down deletes a guest and its files. The down hooks run first, to forget what they set up for it.
func Down(name string) error {
	ui.Header("down", name)
	return withGuest(name, false, func(g *Guest) error {
		g.Phase = "removing"
		g.MustSave()
		stopFunnels(g, nil)
		runHook(contract.Down, g, nil)
		closeGitForward(g)
		b := backendFor(g)
		if err := ui.Run(fmt.Sprintf("Deleting the %s", b.Kind()), fmt.Sprintf("Deleted the %s", b.Kind()), func(*ui.Step) error {
			return b.Destroy(g)
		}); err != nil {
			return err
		}
		os.RemoveAll(g.Runtime())
		if err := os.RemoveAll(g.Directory()); err != nil {
			return err
		}
		ui.Done("Removed %s", ui.Bold(name))
		return nil
	})
}

// DownRemote removes a guest on another host, and tells this machine's down hooks, which forget
// what they set up for it, such as a Rex host entry.
func DownRemote(name, host string) error {
	if !NamePattern.MatchString(name) {
		return errors.New("Name the workspace to remove on another host: rox down NAME --host HOST")
	}
	ui.Header("down", name+" on "+host)
	if _, err := dispatch(host, name, []string{"down", name}); err != nil {
		return err
	}
	minimal := contract.Guest{Schema: contract.Schema, Name: name, Agents: map[string]string{}, Projects: []contract.Project{}}
	runHookPayload(contract.Down, name, payloadFor(minimal, nil), false)
	return nil
}
