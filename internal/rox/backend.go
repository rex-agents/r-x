package rox

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Backend runs guests of one kind. Each of the five backends implements it.
type Backend interface {
	// Kind names what a guest is on this backend: container, VM, machine or sandbox.
	Kind() string
	// Ensure starts the backend's runtime if it stopped, or explains how to.
	Ensure() error
	// Launch creates and boots the guest, pins its host key, and returns its SSH address.
	Launch(g *Guest, boot time.Duration, s *ui.Step) (string, int, error)
	// State is running, stopped or missing.
	State(g *Guest) (string, error)
	// Start boots a stopped guest.
	Start(g *Guest, boot time.Duration) error
	Stop(g *Guest) error
	// Destroy deletes the guest and whatever it stored. What is already gone is skipped.
	Destroy(g *Guest) error
	// Address is where the launcher reaches the guest's SSH server now.
	Address(g *Guest) (string, int, error)
	// Prebuild returns a project image that already ran .rox.sh, or "" when this backend has none.
	Prebuild(p *PrebuildRequest) (string, error)
}

func backendFor(g *Guest) Backend {
	switch {
	case g.Backend == "fly":
		return flyBackend{}
	case g.Backend == "railway":
		return railwayBackend{}
	case g.Engine == "container":
		return appleBackend{}
	case g.Engine != "":
		return engineBackend{engine: g.Engine}
	default:
		return tartBackend{}
	}
}

// DefaultEngine is Apple's container on Apple Silicon Macs, and Docker elsewhere.
func DefaultEngine() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "container"
	}
	return "docker"
}

func newLoginKey(g *Guest) (string, error) {
	if _, err := sys.Run(sys.Cmd{Argv: []string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", g.Identity}}); err != nil {
		return "", err
	}
	return sys.ReadTrim(g.Identity + ".pub")
}

func containerResources(g *Guest) []string {
	var out []string
	if g.CPUs > 0 {
		out = append(out, fmt.Sprintf("--cpus=%d", g.CPUs))
	}
	if g.Memory != "" {
		out = append(out, "--memory="+g.Memory)
	}
	return out
}

// memoryMegabytes converts a size such as 4G or 512M to megabytes.
func memoryMegabytes(size string) int {
	n, _ := strconv.Atoi(size[:len(size)-1])
	unit := map[byte]int{'K': 1, 'M': 1024, 'G': 1024 * 1024, 'T': 1024 * 1024 * 1024}[strings.ToUpper(size)[len(size)-1]]
	return max(1, n*unit/1024)
}

// Docker and Podman containers share the host kernel, so they get no sudo, and SSH reaches
// them through a published loopback port.
type engineBackend struct{ engine string }

func (b engineBackend) Kind() string { return "container" }

func (b engineBackend) Ensure() error {
	if _, err := sys.Run(sys.Cmd{Argv: []string{b.engine, "info"}, Timeout: 20 * time.Second}); err != nil {
		hint := ""
		if b.engine == "podman" && runtime.GOOS == "darwin" {
			hint = " with podman machine start"
		}
		return fmt.Errorf("%s isn't running. Start it%s and try again", b.engine, hint)
	}
	return nil
}

func (b engineBackend) Launch(g *Guest, boot time.Duration, s *ui.Step) (string, int, error) {
	key, err := newLoginKey(g)
	if err != nil {
		return "", 0, err
	}
	argv := []string{b.engine, "run", "-d"}
	if g.Prebuilt || strings.HasPrefix(g.Image, "rox:") {
		argv = append(argv, "--pull=never")
	}
	argv = append(argv, "--name", g.Name, "--hostname", g.Name, "--label", "io.rox.workspace="+g.Name,
		"--restart", "unless-stopped", "--publish", "127.0.0.1::22", "--env", "ROX_SSH_PUBLIC_KEY="+key)
	argv = append(append(argv, containerResources(g)...), g.Image)
	s.Note("starting the container")
	id, err := sys.Output(argv...)
	if err != nil {
		return "", 0, err
	}
	g.ContainerID = id
	g.MustSave()
	s.Note("waiting for its SSH host key")
	hostKey, err := sys.WaitFor(boot, "the container SSH host key", func() (string, bool, error) {
		out, err := sys.OutputWith(sys.Cmd{Argv: []string{b.engine, "exec", id, "cat", "/etc/ssh/ssh_host_ed25519_key.pub"}, Timeout: 8 * time.Second})
		return out, err == nil && out != "", err
	})
	if err != nil {
		return "", 0, err
	}
	if err := pinHost(g, hostKey); err != nil {
		return "", 0, err
	}
	return waitAddress(b, g, boot, "the container SSH port")
}

func waitAddress(b Backend, g *Guest, boot time.Duration, what string) (string, int, error) {
	type addr struct {
		host string
		port int
	}
	a, err := sys.WaitFor(boot, what, func() (addr, bool, error) {
		host, port, err := b.Address(g)
		return addr{host, port}, err == nil, err
	})
	return a.host, a.port, err
}

var loopbackPort = regexp.MustCompile(`(?m)^127\.0\.0\.1:(\d+)$`)

func (b engineBackend) Address(g *Guest) (string, int, error) {
	mapping, err := sys.OutputWith(sys.Cmd{Argv: []string{b.engine, "port", g.ContainerID, "22/tcp"}, Timeout: 8 * time.Second})
	if err != nil {
		return "", 0, err
	}
	m := loopbackPort.FindStringSubmatch(mapping)
	if m == nil {
		return "", 0, fmt.Errorf("No loopback SSH port found in %q", mapping)
	}
	port, _ := strconv.Atoi(m[1])
	return "127.0.0.1", port, nil
}

func (b engineBackend) State(g *Guest) (string, error) {
	result, err := sys.Try(sys.Cmd{Argv: []string{b.engine, "inspect", "--format", "{{.State.Running}}", g.ContainerID}, Timeout: 20 * time.Second})
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		if strings.Contains(strings.ToLower(result.Output()), "no such") {
			return "missing", nil
		}
		return "", fmt.Errorf("Could not inspect container %s: %s", g.Name, strings.TrimSpace(result.Stderr))
	}
	if strings.TrimSpace(result.Stdout) == "true" {
		return "running", nil
	}
	return "stopped", nil
}

func (b engineBackend) Start(g *Guest, boot time.Duration) error {
	_, err := sys.Run(sys.Cmd{Argv: []string{b.engine, "start", g.ContainerID}, Timeout: 120 * time.Second})
	return err
}

func (b engineBackend) Stop(g *Guest) error {
	_, err := sys.Run(sys.Cmd{Argv: []string{b.engine, "stop", g.ContainerID}, Timeout: 120 * time.Second})
	return err
}

func (b engineBackend) Destroy(g *Guest) error {
	ids, err := sys.Output(b.engine, "ps", "-aq", "--no-trunc", "--filter", "label=io.rox.workspace="+g.Name)
	if err != nil {
		return err
	}
	for _, id := range sys.Lines(ids) {
		if _, err := sys.Run(sys.Cmd{Argv: []string{b.engine, "rm", "-f", "-v", id}}); err != nil {
			return err
		}
	}
	return nil
}

func (b engineBackend) Prebuild(p *PrebuildRequest) (string, error) {
	return prebuildContainer(b.engine, p)
}

// Apple's containers each run in their own VM with their own address, so the workspace
// user gets sudo there and the container gets every capability, so Docker runs inside it.
type appleBackend struct{}

func (appleBackend) Kind() string { return "container" }

func (appleBackend) Ensure() error {
	if sys.Status("container", "system", "status") == 0 {
		return nil
	}
	return ui.Run("Starting Apple's container services", "Started Apple's container services", func(*ui.Step) error {
		// Guests already exist, so the kernel is installed and there's nothing to prompt for.
		_, err := sys.Run(sys.Cmd{Argv: []string{"container", "system", "start", "--disable-kernel-install"}, Timeout: 120 * time.Second})
		if err != nil {
			return fmt.Errorf("Could not start Apple's container services. Run container system start: %w", err)
		}
		return nil
	})
}

func (b appleBackend) Launch(g *Guest, boot time.Duration, s *ui.Step) (string, int, error) {
	key, err := newLoginKey(g)
	if err != nil {
		return "", 0, err
	}
	argv := append([]string{"container", "run", "--detach", "--name", g.Name, "--cap-add", "ALL"}, containerResources(g)...)
	argv = append(argv, "--env", "ROX_SSH_PUBLIC_KEY="+key, "--env", "ROX_SUDO=1", g.Image)
	s.Note("starting the container")
	if _, err := sys.Output(argv...); err != nil {
		return "", 0, err
	}
	g.ContainerID = g.Name
	g.MustSave()
	s.Note("waiting for its SSH host key")
	hostKey, err := sys.WaitFor(boot, "the container SSH host key", func() (string, bool, error) {
		out, err := sys.OutputWith(sys.Cmd{Argv: []string{"container", "exec", g.Name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub"}, Timeout: 8 * time.Second})
		return out, err == nil && out != "", err
	})
	if err != nil {
		return "", 0, err
	}
	if err := pinHost(g, hostKey); err != nil {
		return "", 0, err
	}
	return waitAddress(b, g, boot, "the container IP address")
}

func appleRecords(name string) ([]map[string]any, sys.Result, error) {
	result, err := sys.Try(sys.Cmd{Argv: []string{"container", "inspect", name}, Timeout: 20 * time.Second})
	if err != nil || result.Code != 0 {
		return nil, result, err
	}
	var raw any
	if err := json.Unmarshal([]byte(firstNonEmpty(strings.TrimSpace(result.Stdout), "[]")), &raw); err != nil {
		return nil, result, err
	}
	var records []map[string]any
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				records = append(records, m)
			}
		}
	case map[string]any:
		records = append(records, v)
	}
	return records, result, nil
}

func (appleBackend) Address(g *Guest) (string, int, error) {
	records, result, err := appleRecords(g.Name)
	if err != nil {
		return "", 0, err
	}
	if result.Code != 0 {
		return "", 0, &sys.ExitError{Argv: []string{"container", "inspect"}, Result: result}
	}
	for _, record := range records {
		// container 1.x reports the address under status; earlier releases put it at the top level.
		networks, _ := record["networks"].([]any)
		if status, ok := record["status"].(map[string]any); ok {
			if n, ok := status["networks"].([]any); ok && len(n) > 0 {
				networks = n
			}
		}
		for _, network := range networks {
			n, _ := network.(map[string]any)
			for _, key := range []string{"ipv4Address", "address"} {
				value, _ := n[key].(string)
				if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Addr().Is4() {
					return prefix.Addr().String(), 22, nil
				}
				if addr, err := netip.ParseAddr(value); err == nil && addr.Is4() {
					return addr.String(), 22, nil
				}
			}
		}
	}
	return "", 0, fmt.Errorf("container did not report an IPv4 address for %s", g.Name)
}

func (appleBackend) State(g *Guest) (string, error) {
	records, result, err := appleRecords(g.Name)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		if strings.Contains(strings.ToLower(result.Output()), "not found") {
			return "missing", nil
		}
		return "", fmt.Errorf("Could not inspect container %s: %s", g.Name, strings.TrimSpace(result.Stderr))
	}
	if len(records) == 0 {
		return "missing", nil
	}
	state := records[0]["status"]
	if m, ok := state.(map[string]any); ok {
		state = m["state"]
	}
	if state == "running" {
		return "running", nil
	}
	return "stopped", nil
}

func (appleBackend) Start(g *Guest, boot time.Duration) error {
	_, err := sys.Run(sys.Cmd{Argv: []string{"container", "start", g.Name}, Timeout: 120 * time.Second})
	return err
}

func (appleBackend) Stop(g *Guest) error {
	_, err := sys.Run(sys.Cmd{Argv: []string{"container", "stop", g.Name}, Timeout: 120 * time.Second})
	return err
}

func (appleBackend) Destroy(g *Guest) error {
	result, err := sys.Try(sys.Cmd{Argv: []string{"container", "delete", "--force", g.Name}})
	if err != nil {
		return err
	}
	if result.Code != 0 && !strings.Contains(strings.ToLower(result.Output()), "not found") {
		return &sys.ExitError{Argv: []string{"container", "delete"}, Result: result, Message: "Could not delete container " + g.Name}
	}
	return nil
}

func (appleBackend) Prebuild(p *PrebuildRequest) (string, error) {
	return prebuildContainer("container", p)
}

// Tart VMs back the linux and macos backends.
type tartBackend struct{}

func (tartBackend) Kind() string  { return "VM" }
func (tartBackend) Ensure() error { return nil }

// runVM runs the guest's Tart VM in the background, detached from rox.
func runVM(g *Guest) (sys.Process, error) {
	return sys.R.Detach(sys.Cmd{Argv: []string{"tart", "run", "--no-graphics", "--no-clipboard", g.Name}}, g.Log("tart.log"))
}

func (b tartBackend) Launch(g *Guest, boot time.Duration, s *ui.Step) (string, int, error) {
	s.Note("cloning " + g.Image)
	if _, err := sys.Run(sys.Cmd{Argv: []string{"tart", "clone", g.Image, g.Name}}); err != nil {
		return "", 0, err
	}
	var resources []string
	if g.CPUs > 0 {
		resources = append(resources, fmt.Sprintf("--cpu=%d", g.CPUs))
	}
	if g.Memory != "" {
		resources = append(resources, fmt.Sprintf("--memory=%d", memoryMegabytes(g.Memory)))
	}
	if len(resources) > 0 {
		if _, err := sys.Run(sys.Cmd{Argv: append([]string{"tart", "set", g.Name}, resources...)}); err != nil {
			return "", 0, err
		}
	}
	s.Note("booting")
	vm, err := runVM(g)
	if err != nil {
		return "", 0, err
	}
	return bootAddress(g, vm, boot)
}

func bootAddress(g *Guest, vm sys.Process, boot time.Duration) (string, int, error) {
	host, err := sys.WaitFor(boot, g.Name+" to get an address", func() (string, bool, error) {
		if exited, code := vm.Exited(); exited {
			return "", false, &sys.Fatal{Err: fmt.Errorf("Tart exited with status %d. See %s", code, g.Log("tart.log"))}
		}
		host, _, err := vmAddress(g)
		return host, err == nil, nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("No Tart IP address found for %s. See %s", g.Name, g.Log("tart.log"))
	}
	return host, 22, nil
}

func vmAddress(g *Guest) (string, int, error) {
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{"tart", "ip", g.Name}, Timeout: 5 * time.Second})
	if err != nil {
		return "", 0, err
	}
	addr, err := netip.ParseAddr(out)
	if err != nil || !addr.Is4() {
		return "", 0, fmt.Errorf("Tart did not return an IPv4 address for %s", g.Name)
	}
	return addr.String(), 22, nil
}

func (tartBackend) Address(g *Guest) (string, int, error) { return vmAddress(g) }

func tartList() ([]map[string]any, error) {
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{"tart", "list", "--source", "local", "--format", "json"}, Timeout: 20 * time.Second})
	if err != nil {
		return nil, err
	}
	var vms []map[string]any
	if err := json.Unmarshal([]byte(firstNonEmpty(out, "[]")), &vms); err != nil {
		return nil, err
	}
	return vms, nil
}

func tartImages() (map[string]bool, error) {
	vms, err := tartList()
	names := map[string]bool{}
	for _, vm := range vms {
		if n, ok := vm["Name"].(string); ok {
			names[n] = true
		}
	}
	return names, err
}

func (tartBackend) State(g *Guest) (string, error) {
	vms, err := tartList()
	if err != nil {
		return "", err
	}
	for _, vm := range vms {
		if vm["Name"] == g.Name {
			if vm["Running"] == true {
				return "running", nil
			}
			return "stopped", nil
		}
	}
	return "missing", nil
}

func (tartBackend) Start(g *Guest, boot time.Duration) error {
	_, err := runVM(g)
	return err
}

func (tartBackend) Stop(g *Guest) error {
	_, err := sys.Run(sys.Cmd{Argv: []string{"tart", "stop", g.Name}, Timeout: 120 * time.Second})
	return err
}

func (b tartBackend) Destroy(g *Guest) error {
	state, err := b.State(g)
	if err != nil || state == "missing" {
		return err
	}
	if state == "running" {
		if _, err := sys.Run(sys.Cmd{Argv: []string{"tart", "stop", g.Name}}); err != nil {
			return err
		}
	}
	_, err = sys.Run(sys.Cmd{Argv: []string{"tart", "delete", g.Name}})
	return err
}

func (tartBackend) Prebuild(p *PrebuildRequest) (string, error) { return prebuildVM(p) }

// forwardedAddress keeps a CLI forwarding a loopback port to a cloud guest's SSH port,
// since cloud guests have no address the launcher can reach directly.
func forwardedAddress(g *Guest, argvFor func(port int) []string, pidPath, logName, description string) (string, int, error) {
	if err := ensureRuntimeDirs(g); err != nil {
		return "", 0, err
	}
	if g.Port != 22 && sys.PidAlive(pidPath) && sys.PortOpen(g.Port) {
		return "127.0.0.1", g.Port, nil
	}
	sys.KillPid(pidPath)
	port := sys.FreePort()
	proxy, err := sys.R.Detach(sys.Cmd{Argv: argvFor(port)}, g.Log(logName))
	if err != nil {
		return "", 0, err
	}
	if err := sys.WritePid(pidPath, proxy.Pid()); err != nil {
		return "", 0, err
	}
	_, err = sys.WaitFor(30*time.Second, description, func() (bool, bool, error) {
		if exited, code := proxy.Exited(); exited {
			data, _ := os.ReadFile(g.Log(logName))
			lines := sys.Lines(string(data))
			if len(lines) > 5 {
				lines = lines[len(lines)-5:]
			}
			said := ""
			if len(lines) > 0 {
				said = "\n" + strings.Join(lines, "\n")
			}
			return false, false, &sys.Fatal{Err: fmt.Errorf("%s exited with status %d. See %s%s", description, code, g.Log(logName), said)}
		}
		return true, sys.PortOpen(port), nil
	})
	return "127.0.0.1", port, err
}

// Fly.io guests are Firecracker microVMs, one machine each in a shared app, with a volume for
// /home/workspace, since a machine's root filesystem is rebuilt from its image at each start.
const (
	flyDefaultCPUs   = 2
	flyDefaultMemory = "4G"
	flyDefaultDisk   = 10
	flyDefaultRegion = "iad"
	flySSHPort       = 2222 // Fly's own SSH server holds port 22 on the machine's private address.
	flyDefaultOrg    = "personal"
)

var flyStates = map[string]string{"started": "running", "starting": "running", "created": "running", "replacing": "running",
	"stopped": "stopped", "stopping": "stopped", "suspended": "stopped", "suspending": "stopped"}

func flyCLI() (string, error) {
	for _, name := range []string{"fly", "flyctl"} {
		if p, err := sys.R.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Install the Fly.io CLI (https://fly.io/docs/flyctl/install/) and run fly auth login")
}

func fly(app string, timeout time.Duration, args ...string) (string, error) {
	cli, err := flyCLI()
	if err != nil {
		return "", err
	}
	argv := append([]string{cli}, args...)
	if app != "" {
		argv = append(argv, "-a", app)
	}
	return sys.OutputWith(sys.Cmd{Argv: argv, Timeout: timeout})
}

func flyJSON(app string, timeout time.Duration, args ...string) ([]map[string]any, error) {
	out, err := fly(app, timeout, append(args, "--json")...)
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	if strings.TrimSpace(out) == "" || out == "null" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("fly %s returned something other than JSON", args[0])
	}
	return list, nil
}

// FlySettings is the config's fly section.
type FlySettings struct {
	App, Org, Region string
	Disk             int
}

var lowerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func flySettings(settings map[string]any, configPath string) (*FlySettings, error) {
	values := map[string]string{}
	for _, key := range []string{"app", "org", "region"} {
		raw, present := settings[key]
		value, ok := raw.(string)
		if present && (!ok || (value != "" && !lowerName.MatchString(value))) {
			return nil, fmt.Errorf("Expected a lowercase name for fly.%s in %s", key, configPath)
		}
		values[key] = value
	}
	disk := flyDefaultDisk
	if raw, ok := settings["disk"]; ok {
		n, ok := intSetting(raw)
		if !ok || n < 1 {
			return nil, fmt.Errorf("Expected a whole number of GB for fly.disk in %s", configPath)
		}
		disk = n
	}
	return &FlySettings{App: values["app"], Org: firstNonEmpty(values["org"], flyDefaultOrg),
		Region: firstNonEmpty(values["region"], flyDefaultRegion), Disk: disk}, nil
}

// flyAppName is the default shared app, named from a hash so the account's email stays out of it.
func flyAppName(account string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(account)))
	return "rox-" + hex.EncodeToString(sum[:])[:10]
}

func flyAccount() (string, error) {
	out, err := fly("", 30*time.Second, "auth", "whoami")
	if err != nil {
		return "", fmt.Errorf("The fly CLI isn't logged in. Run fly auth login (%v)", err)
	}
	return out, nil
}

func ensureFlyApp(app, org string) error {
	apps, err := flyJSON("", 60*time.Second, "apps", "list")
	if err != nil {
		return err
	}
	for _, entry := range apps {
		if entry["Name"] == app {
			return nil
		}
	}
	return ui.Run("Creating the Fly app "+app, "Created the Fly app "+app, func(*ui.Step) error {
		_, err := fly("", 60*time.Second, "apps", "create", app, "--org", org)
		return err
	})
}

// flyImage is rox's Containerfile as an image in the app's registry, built on Fly's remote builder when it changed.
func flyImage(app string) (string, error) {
	label := "rox-" + assets.Toolkit()
	image := "registry.fly.io/" + app + ":" + label
	marker := filepath.Join(StateRoot(), "fly-image-"+app)
	if current, _ := sys.ReadTrim(marker); current == label {
		ui.Skip("%s is current", image)
		return image, nil
	}
	err := ui.Run("Building "+image+" on Fly.io", "Built "+image+" on Fly.io", func(*ui.Step) error {
		scratch, err := os.MkdirTemp("", "rox-fly-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(scratch)
		if err := assets.BuildContext(scratch); err != nil {
			return err
		}
		config := filepath.Join(scratch, "fly.toml")
		if err := os.WriteFile(config, []byte(fmt.Sprintf("app = %q\n", app)), 0o644); err != nil {
			return err
		}
		cli, err := flyCLI()
		if err != nil {
			return err
		}
		// fly reads relative paths in fly.toml from the config's folder, so name the build folder and file outright.
		_, err = sys.Run(sys.Cmd{Dir: scratch, Argv: []string{cli, "deploy", scratch, "--build-only", "--push", "--image-label", label,
			"--dockerfile", filepath.Join(scratch, "Containerfile"), "--config", config, "-a", app}})
		if err != nil {
			return fmt.Errorf("Building the guest image on Fly.io failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return image, sys.WritePrivate(marker, label+"\n")
}

type flyBackend struct{}

func (flyBackend) Kind() string  { return "machine" }
func (flyBackend) Ensure() error { return nil }

func flyMachine(g *Guest) (map[string]any, error) {
	machines, err := flyJSON(g.App, 60*time.Second, "machines", "list")
	if err != nil {
		return nil, err
	}
	for _, m := range machines {
		if (g.ContainerID != "" && m["id"] == g.ContainerID) || m["name"] == g.Name {
			return m, nil
		}
	}
	return nil, nil
}

func flyProxyPid(g *Guest) string { return filepath.Join(g.Runtime(), "fly-proxy.pid") }

func (b flyBackend) Address(g *Guest) (string, int, error) {
	cli, err := flyCLI()
	if err != nil {
		return "", 0, err
	}
	return forwardedAddress(g, func(port int) []string {
		return []string{cli, "proxy", fmt.Sprintf("%d:%d", port, flySSHPort), g.ContainerID + ".vm." + g.App + ".internal", "-a", g.App}
	}, flyProxyPid(g), "fly-proxy.log", "fly proxy")
}

func (b flyBackend) Launch(g *Guest, boot time.Duration, s *ui.Step) (string, int, error) {
	key, err := newLoginKey(g)
	if err != nil {
		return "", 0, err
	}
	g.Volume = "rox_" + g.Name[strings.LastIndexByte(g.Name, '-')+1:]
	s.Note("creating its volume")
	if _, err := fly(g.App, 120*time.Second, "volumes", "create", g.Volume, "--region", g.Region, "--size", strconv.Itoa(g.Disk), "--yes"); err != nil {
		return "", 0, err
	}
	g.MustSave()
	// --restart no keeps a machine that exits from starting again behind rox stop's back. Each
	// machine is its own VM, so like Apple's containers it gets sudo and can run Docker.
	cpus := g.CPUs
	if cpus == 0 {
		cpus = flyDefaultCPUs
	}
	s.Note("starting the machine")
	if _, err := fly(g.App, boot+120*time.Second, "machine", "run", g.Image, "--name", g.Name, "--region", g.Region,
		"--vm-cpus", strconv.Itoa(cpus), "--vm-memory", strconv.Itoa(memoryMegabytes(firstNonEmpty(g.Memory, flyDefaultMemory))),
		"--volume", g.Volume+":/home/workspace", "--restart", "no",
		"--env", "ROX_SSH_PUBLIC_KEY="+key, "--env", "ROX_SUDO=1", "--env", fmt.Sprintf("ROX_SSH_PORT=%d", flySSHPort),
		"--metadata", "rox_workspace="+g.Name); err != nil {
		return "", 0, err
	}
	machine, err := sys.WaitFor(60*time.Second, "the Fly machine to appear", func() (map[string]any, bool, error) {
		m, err := flyMachine(g)
		return m, m != nil, err
	})
	if err != nil {
		return "", 0, err
	}
	g.ContainerID, _ = machine["id"].(string)
	g.MustSave()
	if _, err := sys.WaitFor(boot, "the Fly machine to start", func() (bool, bool, error) {
		m, err := flyMachine(g)
		return true, m != nil && m["state"] == "started", err
	}); err != nil {
		return "", 0, err
	}
	// fly machine exec goes through Fly's API, so the host key comes from the machine, not the network.
	s.Note("reading its SSH host key")
	hostKey, err := sys.WaitFor(boot, "the machine's SSH host key", func() (string, bool, error) {
		out, err := fly(g.App, 30*time.Second, "machine", "exec", g.ContainerID, "cat /etc/ssh/ssh_host_ed25519_key.pub")
		return out, err == nil, err
	})
	if err != nil {
		return "", 0, err
	}
	if err := pinHost(g, hostKey); err != nil {
		return "", 0, err
	}
	return waitAddress(b, g, boot, "the SSH proxy")
}

func (flyBackend) State(g *Guest) (string, error) {
	if g.ContainerID == "" {
		return "missing", nil
	}
	m, err := flyMachine(g)
	if err != nil {
		return "", err
	}
	if m == nil || m["state"] == "destroyed" {
		return "missing", nil
	}
	if state, ok := flyStates[fmt.Sprint(m["state"])]; ok {
		return state, nil
	}
	return "stopped", nil
}

func (flyBackend) Start(g *Guest, boot time.Duration) error {
	_, err := fly(g.App, boot+30*time.Second, "machine", "start", g.ContainerID)
	return err
}

func (flyBackend) Stop(g *Guest) error {
	sys.KillPid(flyProxyPid(g))
	_, err := fly(g.App, 120*time.Second, "machine", "stop", g.ContainerID)
	return err
}

func (flyBackend) Destroy(g *Guest) error {
	sys.KillPid(flyProxyPid(g))
	if g.App == "" {
		return nil
	}
	machines, err := flyJSON(g.App, 60*time.Second, "machines", "list")
	if err != nil {
		return err
	}
	for _, m := range machines {
		if m["name"] == g.Name || (g.ContainerID != "" && m["id"] == g.ContainerID) {
			if _, err := fly(g.App, 120*time.Second, "machine", "destroy", fmt.Sprint(m["id"]), "--force"); err != nil {
				return err
			}
		}
	}
	if g.Volume == "" {
		return nil
	}
	volumes, err := flyJSON(g.App, 60*time.Second, "volumes", "list")
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if v["name"] == g.Volume {
			if _, err := fly(g.App, 120*time.Second, "volumes", "destroy", fmt.Sprint(v["id"]), "--yes"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (flyBackend) Prebuild(*PrebuildRequest) (string, error) { return "", nil }

// Railway guests are sandboxes from a rox template built on Railway's Ubuntu image. Their own
// sshd listens on railwaySSHPort, which railway sandbox forward brings to a loopback port.
// Sandboxes can't be stopped, and Railway destroys one that idles past its timeout.
const (
	railwaySSHPort            = 2222
	railwayDefaultEnvironment = "production"
)

func railwayCLI() (string, error) {
	p, err := sys.R.LookPath("railway")
	if err != nil {
		return "", errors.New("Install the Railway CLI (https://docs.railway.com/cli) and run railway login")
	}
	return p, nil
}

func railwayBase(g *Guest) ([]string, error) {
	cli, err := railwayCLI()
	if err != nil {
		return nil, err
	}
	return []string{cli, "sandbox", "-p", g.RailwayProject, "-e", g.RailwayEnvironment}, nil
}

func railway(g *Guest, timeout time.Duration, jsonOut bool, args ...string) (string, error) {
	base, err := railwayBase(g)
	if err != nil {
		return "", err
	}
	// --json goes right after the subcommand, as exec takes its command after --.
	argv := append(base, args[0])
	if jsonOut {
		argv = append(argv, "--json")
	}
	return sys.OutputWith(sys.Cmd{Argv: append(argv, args[1:]...), Timeout: timeout})
}

// RailwaySettings is the config's railway section.
type RailwaySettings struct {
	Project, Environment string
	IdleTimeout          *int
}

func railwaySettings(settings map[string]any, configPath string) (*RailwaySettings, error) {
	for _, key := range []string{"project", "environment"} {
		if raw, ok := settings[key]; ok {
			value, ok := raw.(string)
			if !ok || strings.HasPrefix(value, "-") {
				return nil, fmt.Errorf("Expected a name or id for railway.%s in %s", key, configPath)
			}
		}
	}
	project, _ := settings["project"].(string)
	if project == "" {
		return nil, fmt.Errorf("Set railway.project to the Railway project that runs guests, in %s", configPath)
	}
	environment, _ := settings["environment"].(string)
	r := &RailwaySettings{Project: project, Environment: firstNonEmpty(environment, railwayDefaultEnvironment)}
	if raw, ok := settings["idle_timeout"]; ok && raw != nil {
		n, ok := intSetting(raw)
		if _, isBool := raw.(bool); !ok || isBool || n < 0 || n > 120 {
			return nil, fmt.Errorf("Expected minutes from 0 to 120 for railway.idle_timeout in %s", configPath)
		}
		r.IdleTimeout = &n
	}
	return r, nil
}

// railwayTemplateSteps turn Railway's sandbox image into a rox guest, as the Containerfile does for rox:linux.
func railwayTemplateSteps() []string {
	entrypoint := base64.StdEncoding.EncodeToString(assets.Read("guest/container-entrypoint.sh"))
	root := `S=$(test "$(id -u)" = 0 || echo sudo); `
	return []string{
		root + "$S apt-get update && $S env DEBIAN_FRONTEND=noninteractive apt-get install -y " +
			"--no-install-recommends bash ca-certificates curl git libpam0g openssh-client openssh-server sudo " +
			"xz-utils && $S rm -rf /var/lib/apt/lists/*",
		root + "curl -fsSL https://pkgs.superlogical.com/rex/install.sh -o /tmp/rex-install.sh && " +
			"$S sh /tmp/rex-install.sh --tarball --channel " + rexChannel() + " && rm /tmp/rex-install.sh && " +
			"if [ ! -x /usr/local/bin/rex ]; then $S install -m 0755 \"$($S sh -lc 'command -v rex')\" " +
			"/usr/local/bin/rex; fi && /usr/local/bin/rex version",
		root + "id workspace >/dev/null 2>&1 || $S useradd --create-home --shell /bin/bash workspace; $S passwd -d workspace",
		root + "printf %s " + entrypoint + " | base64 -d | $S tee /usr/local/bin/rox-container >/dev/null && " +
			"$S chmod 0755 /usr/local/bin/rox-container",
		// Every sandbox from the template shares its disk, so each one makes its own host key at boot.
		root + "$S rm -f /etc/ssh/ssh_host_*",
	}
}

func railwayTemplate(g *Guest) (string, error) {
	steps := railwayTemplateSteps()
	data, _ := json.Marshal(steps)
	sum := sha256.Sum256(data)
	name := "rox-" + hex.EncodeToString(sum[:])[:12]
	err := ui.Run("Preparing the Railway sandbox template "+name, "Railway sandbox template "+name+" is ready", func(*ui.Step) error {
		base, err := railwayBase(g)
		if err != nil {
			return err
		}
		argv := append(base, "template", "build", "--name", name, "--wait")
		for _, step := range steps {
			argv = append(argv, "-c", step)
		}
		if _, err := sys.Run(sys.Cmd{Argv: argv, Timeout: 50 * time.Minute}); err != nil {
			return fmt.Errorf("Building the Railway sandbox template failed: %w", err)
		}
		return nil
	})
	return name, err
}

type railwayBackend struct{}

func (railwayBackend) Kind() string  { return "sandbox" }
func (railwayBackend) Ensure() error { return nil }

func railwayForwardPid(g *Guest) string { return filepath.Join(g.Runtime(), "railway-forward.pid") }

func (railwayBackend) Address(g *Guest) (string, int, error) {
	base, err := railwayBase(g)
	if err != nil {
		return "", 0, err
	}
	// The forward's heartbeat also keeps the sandbox from idling out while rox is connected.
	return forwardedAddress(g, func(port int) []string {
		return append(append([]string{}, base...), "forward", "--id", g.ContainerID, "--strict", fmt.Sprintf("%d:%d", port, railwaySSHPort))
	}, railwayForwardPid(g), "railway-forward.log", "railway sandbox forward")
}

func (b railwayBackend) Launch(g *Guest, boot time.Duration, s *ui.Step) (string, int, error) {
	key, err := newLoginKey(g)
	if err != nil {
		return "", 0, err
	}
	s.Note("preparing the sandbox template")
	template, err := railwayTemplate(g)
	if err != nil {
		return "", 0, err
	}
	s.Note("creating the sandbox")
	args := []string{"create", "--template", template}
	if g.RailwayIdleTimeout != nil {
		args = append(args, "--idle-timeout-minutes", strconv.Itoa(*g.RailwayIdleTimeout))
	}
	out, err := railway(g, boot+360*time.Second, true, args...)
	if err != nil {
		return "", 0, err
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(out), &created)
	id, _ := created["id"].(string)
	if id == "" {
		return "", 0, errors.New("railway sandbox create did not return a sandbox id")
	}
	g.ContainerID = id
	g.MustSave()
	// Each sandbox is its own VM, so it gets sudo. The entrypoint ends in sshd, which the detached session keeps running.
	env := sys.Quote("ROX_SSH_PUBLIC_KEY="+key, "ROX_SUDO=1", fmt.Sprintf("ROX_SSH_PORT=%d", railwaySSHPort))
	if _, err := railway(g, 120*time.Second, false, "exec", "--id", id, "--detach", "--",
		`S=$(test "$(id -u)" = 0 || echo sudo); exec $S env `+env+` /usr/local/bin/rox-container`); err != nil {
		return "", 0, err
	}
	hostKey, err := sys.WaitFor(boot, "the sandbox's SSH host key", func() (string, bool, error) {
		out, err := railway(g, 60*time.Second, false, "exec", "--id", id, "--timeout", "20", "--", "cat /etc/ssh/ssh_host_ed25519_key.pub")
		return out, err == nil, err
	})
	if err != nil {
		return "", 0, err
	}
	if err := pinHost(g, hostKey); err != nil {
		return "", 0, err
	}
	// A forward that exits, such as for want of an SSH key on the Railway account, won't do better on a retry.
	return b.Address(g)
}

func (railwayBackend) State(g *Guest) (string, error) {
	if g.ContainerID == "" {
		return "missing", nil
	}
	out, err := railway(g, 60*time.Second, true, "list")
	if err != nil {
		return "", err
	}
	var sandboxes []map[string]any
	_ = json.Unmarshal([]byte(firstNonEmpty(out, "[]")), &sandboxes)
	for _, sb := range sandboxes {
		if sb["id"] == g.ContainerID {
			// CREATING counts as running, so rox waits on it rather than calling the sandbox gone.
			if sb["status"] == "RUNNING" || sb["status"] == "CREATING" {
				return "running", nil
			}
		}
	}
	return "missing", nil
}

func (railwayBackend) Start(g *Guest, _ time.Duration) error {
	return fmt.Errorf("Railway sandboxes can't be stopped and started again. Remove %s with rox down", g.Name)
}

func (railwayBackend) Stop(g *Guest) error {
	return fmt.Errorf("Railway sandboxes can't be stopped and started again. Remove %s with rox down", g.Name)
}

func (b railwayBackend) Destroy(g *Guest) error {
	sys.KillPid(railwayForwardPid(g))
	if g.ContainerID == "" || g.RailwayProject == "" {
		return nil
	}
	if state, err := b.State(g); err != nil || state == "missing" {
		return err
	}
	_, err := railway(g, 120*time.Second, false, "destroy", g.ContainerID)
	return err
}

func (railwayBackend) Prebuild(*PrebuildRequest) (string, error) { return "", nil }

// rexChannel is the Rex release channel this machine runs, so guests match it.
func rexChannel() string {
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{"rex", "version"}, Timeout: 10 * time.Second})
	if err != nil {
		return "preview"
	}
	version := strings.Split(out, "\n")[0]
	switch {
	case regexp.MustCompile(`alpha|snapshot`).MatchString(version):
		return "snapshot"
	case strings.Contains(version, "preview") || !regexp.MustCompile(`\d+\.\d+\.\d+\s*$`).MatchString(version):
		return "preview"
	}
	return "stable"
}
