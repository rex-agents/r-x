package rox

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

const (
	DefaultTemplateBase = "ghcr.io/cirruslabs/macos-tahoe-base:latest"
	DefaultTemplateName = "rox-macos"
	// DefaultBasePassword is the password Cirrus Labs' macOS images ship with. rox template replaces it.
	DefaultBasePassword = "admin"
	rexApp              = "/Applications/Rex.app"
	rexAppCLI           = "Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
	rexInstaller        = "https://pkgs.superlogical.com/rex/install.sh"
	linuxHostKeyFile    = "rox-linux-host-key"
	DefaultLinuxDistro  = "ubuntu"
)

// LinuxTemplateBases are Cirrus Labs' Tart images per distribution. "" means none is published.
var LinuxTemplateBases = map[string]string{
	"ubuntu": "ghcr.io/cirruslabs/ubuntu:24.04",
	"debian": "ghcr.io/cirruslabs/debian:trixie",
	"fedora": "ghcr.io/cirruslabs/fedora:42",
	"arch":   "",
}

// macOS auto-login reads the password from /etc/kcpassword, XORed with this key and padded with NULs.
var kcpasswordKey = []byte{0x7D, 0x89, 0x52, 0x23, 0xD2, 0xBC, 0xDD, 0xEA, 0xA3, 0xB9, 0x1F}

// Kcpassword encodes a password as macOS auto-login stores it.
func Kcpassword(password string) []byte {
	data := []byte(password)
	data = append(data, make([]byte, 12-len(data)%12)...)
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ kcpasswordKey[i%len(kcpasswordKey)]
	}
	return out
}

// Read the old password, the new one and the encoded kcpassword from stdin, so none reach a
// command line on the host. Change the password if the old one still works, then point
// auto-login at the new one, or the template boots to the login window with no desktop.
const templatePasswordScript = `set -eu
IFS= read -r old
IFS= read -r new
IFS= read -r kc
user=$(id -un)
if [ "$old" != "$new" ] && dscl . -authonly "$user" "$old" >/dev/null 2>&1; then
  dscl . -passwd "/Users/$user" "$old" "$new"
  keychain="$HOME/Library/Keychains/login.keychain-db"
  if [ -f "$keychain" ]; then
    security set-keychain-password -o "$old" -p "$new" "$keychain" >/dev/null 2>&1 || true
  fi
fi
if ! dscl . -authonly "$user" "$new" >/dev/null 2>&1; then
  echo "The template's password is neither the base image's nor the one rox saved." >&2
  exit 1
fi
if ! sudo -n true 2>/dev/null; then
  echo "$user needs passwordless sudo in the template." >&2
  exit 1
fi
printf '%s' "$kc" | base64 -D | sudo -n tee /etc/kcpassword >/dev/null
sudo -n chown root:wheel /etc/kcpassword
sudo -n chmod 600 /etc/kcpassword
sudo -n defaults write /Library/Preferences/com.apple.loginwindow autoLoginUser "$user"
`

// Unpack the Rex app from stdin and put its CLI on SSH's PATH, through ~/.zshenv.
const templateRexScript = `set -eu
archive=$(mktemp /tmp/rex.XXXXXX)
trap 'rm -f "$archive"' EXIT
cat > "$archive"
sudo -n rm -rf /Applications/Rex.app
sudo -n ditto -x -k "$archive" /Applications
mkdir -p "$HOME/.local/bin"
ln -sfn "/Applications/Rex.app/$1" "$HOME/.local/bin/rex"
line='export PATH="$HOME/.local/bin:$PATH"'
touch "$HOME/.zshenv"
grep -qxF "$line" "$HOME/.zshenv" || printf '%s\n' "$line" >> "$HOME/.zshenv"
`

const templateCheckScript = `set -eu
if [ "${1:-}" = rex ]; then
  if ! command -v rex >/dev/null; then
    echo "rex isn't on SSH's PATH in the template." >&2
    exit 1
  fi
  rex version | head -n 1
fi
if ! git --version; then
  echo "Git doesn't run in the template. Install the Xcode Command Line Tools there." >&2
  exit 1
fi
bash --version | head -n 1
`

// Read the new password from stdin. Install what rox needs, keep Rex running after SSH logs
// out, keep cloud-init from replacing host keys in clones, take only key logins, install Rex.
const linuxSetupScript = `set -eu
IFS= read -r new
user=$(id -un)
if ! sudo -n true 2>/dev/null; then
  echo "$user needs passwordless sudo in the template." >&2
  exit 1
fi
packages="git bash curl ca-certificates tar gzip"
quiet() {
  log=$("$@" 2>&1) || { printf '%s\n' "$log" >&2; exit 1; }
}
if command -v apt-get >/dev/null; then
  quiet sudo -n env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get update -q
  quiet sudo -n env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y -q $packages
elif command -v dnf >/dev/null; then
  quiet sudo -n dnf install -y $packages
elif command -v pacman >/dev/null; then
  quiet sudo -n pacman -Syu --noconfirm --needed $packages
elif command -v zypper >/dev/null; then
  quiet sudo -n zypper -n install $packages
elif command -v apk >/dev/null; then
  quiet sudo -n apk add $packages
else
  echo "rox template doesn't know this distribution's package manager. Install $packages in the template." >&2
  exit 1
fi
printf '%s:%s\n' "$user" "$new" | sudo -n chpasswd
if command -v loginctl >/dev/null; then
  sudo -n loginctl enable-linger "$user"
fi
if [ -d /etc/cloud/cloud.cfg.d ]; then
  printf 'ssh_deletekeys: false\nssh_genkeytypes: []\n' | sudo -n tee /etc/cloud/cloud.cfg.d/99-rox.cfg >/dev/null
fi
sudo -n mkdir -p /etc/ssh/sshd_config.d
if ! sudo -n grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/' /etc/ssh/sshd_config; then
  sudo -n sed -i '1i Include /etc/ssh/sshd_config.d/*.conf' /etc/ssh/sshd_config
fi
settings='PasswordAuthentication no\nAllowAgentForwarding yes\nAllowStreamLocalForwarding yes\n'
if sudo -n sshd -T 2>/dev/null | grep -qi '^kbdinteractiveauthentication '; then
  settings="${settings}KbdInteractiveAuthentication no\n"
fi
printf "$settings" | sudo -n tee /etc/ssh/sshd_config.d/05-rox.conf >/dev/null
if [ -n "$2" ]; then
  installer=$(mktemp)
  trap 'rm -f "$installer"' EXIT
  curl -fsSL "$2" -o "$installer"
  quiet sudo -n sh "$installer" --tarball --channel "$1"
fi
`

// Replace the ed25519 host key with the shared one from stdin, then restart sshd.
const linuxHostKeyScript = `set -eu
key=$(mktemp)
trap 'rm -f "$key"' EXIT
cat > "$key"
sudo -n install -m 600 -o root -g root "$key" /etc/ssh/ssh_host_ed25519_key
printf '%s\n' "$1" | sudo -n tee /etc/ssh/ssh_host_ed25519_key.pub >/dev/null
sudo -n chmod 644 /etc/ssh/ssh_host_ed25519_key.pub
if command -v restorecon >/dev/null; then
  sudo -n restorecon /etc/ssh/ssh_host_ed25519_key /etc/ssh/ssh_host_ed25519_key.pub
fi
sudo -n sshd -t
for unit in ssh.service sshd.service; do
  sudo -n systemctl try-restart "$unit" 2>/dev/null || true
done
`

const linuxCheckScript = `set -eu
if [ "${1:-}" = rex ]; then
  if ! command -v rex >/dev/null; then
    echo "rex isn't on SSH's PATH in the template." >&2
    exit 1
  fi
  rex version | head -n 1
fi
git --version
bash --version | head -n 1
if command -v loginctl >/dev/null; then
  echo "linger: $(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || echo unknown)"
fi
`

// TemplateOptions are rox template's flags.
type TemplateOptions struct {
	Backend, Config, Name, Base, Distro, RexChannel, BasePassword string
	// WithRex puts Rex in the template, for guests that use the rox-rex hook. Without it rox-rex
	// installs Rex in each Linux guest on first boot.
	WithRex     bool
	Default     bool
	BootTimeout time.Duration
}

func templateGuest(name, backend, user, identity string) *Guest {
	g := NewGuest(name, backend, name, user, "")
	g.Identity = identity
	g.dir = filepath.Join(StateRoot(), "templates", name)
	return g
}

func templatePassword(path string) (string, error) {
	if sys.IsFile(path) {
		return sys.ReadTrim(path)
	}
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	password := base64.RawURLEncoding.EncodeToString(b)
	return password, sys.WritePrivate(path, password+"\n")
}

var templateNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// templateLogin checks the template's name and user, and returns the VM login key, made if missing.
func templateLogin(name string, settings map[string]any, configPath string) (user, identitySetting, identity, public string, err error) {
	user = firstNonEmpty(str(settings["user"]), "admin")
	if !templateNamePattern.MatchString(name) || NamePattern.MatchString(name) {
		return "", "", "", "", fmt.Errorf("Choose a template name other than %q", name)
	}
	if !userPattern.MatchString(user) {
		return "", "", "", "", fmt.Errorf("Expected a user name in %s", configPath)
	}
	identitySetting = firstNonEmpty(str(settings["identity"]), "~/.ssh/rox_vm")
	identity = sys.Expand(identitySetting)
	if !sys.IsFile(identity) {
		_ = os.MkdirAll(filepath.Dir(identity), 0o700)
		if _, err = sys.Run(sys.Cmd{Argv: []string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "rox vm login", "-f", identity}}); err != nil {
			return
		}
		ui.Done("Created VM login key %s", ui.Code(identity))
	}
	public, err = sys.ReadTrim(identity + ".pub")
	return
}

func cloneTemplate(name, base, size string) error {
	images, err := tartImages()
	if err != nil {
		return err
	}
	if images[name] {
		if state, _ := (tartBackend{}).State(templateGuest(name, "linux", "admin", "")); state == "running" {
			return fmt.Errorf("%s is running. Shut it down and run rox template again", name)
		}
		ui.Info("Updating template %s", ui.Bold(name))
		return nil
	}
	return ui.Run(fmt.Sprintf("Cloning %s into %s", base, name), "Cloned "+base+" into "+name, func(s *ui.Step) error {
		s.Note("a first pull downloads " + size)
		_, err := sys.Run(sys.Cmd{Argv: []string{"tart", "clone", base, name}})
		return err
	})
}

func bootTemplate(g *Guest, boot time.Duration) (sys.Process, error) {
	vm, err := runVM(g)
	if err != nil {
		return nil, err
	}
	if g.Address, g.Port, err = bootAddress(g, vm, boot); err != nil {
		return vm, err
	}
	return vm, checkGuestOS(g, boot)
}

func shutDownTemplate(g *Guest, vm sys.Process) {
	sys.Try(sys.Cmd{Argv: remoteCommand(g, []string{"sync"}, "", false), Timeout: 60 * time.Second})
	sys.Try(sys.Cmd{Argv: remoteCommand(g, []string{"sudo", "-n", "shutdown", "-h", "now"}, "", false), Timeout: 60 * time.Second})
	_, err := sys.WaitFor(180*time.Second, "the template to shut down", func() (bool, bool, error) {
		exited, _ := vm.Exited()
		return true, exited, nil
	})
	if err != nil {
		sys.Try(sys.Cmd{Argv: []string{"tart", "stop", g.Name}, Timeout: 120 * time.Second})
	}
}

func stopIfRunning(g *Guest, vm sys.Process) {
	if vm == nil {
		return
	}
	if exited, _ := vm.Exited(); !exited {
		sys.Try(sys.Cmd{Argv: []string{"tart", "stop", g.Name}, Timeout: 120 * time.Second})
	}
}

// PrepareTemplate prepares a Tart template for the macos or linux backend.
func PrepareTemplate(o *TemplateOptions) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("Tart templates need an Apple Silicon Mac")
	}
	if err := sys.Require("tart", "ssh", "ssh-keygen"); err != nil {
		return err
	}
	config, err := LoadConfig(o.Config)
	if err != nil {
		return err
	}
	settings, err := config.Section(o.Backend)
	if err != nil {
		return err
	}
	ui.Header("template "+o.Backend, firstNonEmpty(o.Name, str(settings["image"])))
	if o.Backend == "linux" {
		return prepareLinuxTemplate(o, config.Path, settings)
	}
	if o.Distro != "" || o.Default || o.RexChannel != "" {
		return errors.New("--distro, --default and --rex-channel are for rox template linux")
	}
	return prepareMacosTemplate(o, config.Path, settings)
}

func authorizeTemplateKey(g *Guest, public, hostKey string, boot time.Duration) (string, error) {
	pinned := ""
	if data, err := os.ReadFile(hostKey); err == nil {
		pinned = string(data)
	}
	how, err := sys.WaitFor(boot, "SSH or the Tart guest agent in "+g.Name, func() (string, bool, error) {
		if pinned != "" {
			_ = pinHost(g, pinned)
			if addr, port, err := vmAddress(g); err == nil {
				g.Address, g.Port = addr, port
				if r, err := sys.Try(sys.Cmd{Argv: remoteCommand(g, []string{"true"}, "", false), Timeout: 10 * time.Second}); err == nil && r.Code == 0 {
					return "ssh", true, nil
				}
			}
		}
		if r, err := sys.Try(sys.Cmd{Argv: []string{"tart", "exec", g.Name, "true"}, Timeout: 15 * time.Second}); err == nil && r.Code == 0 {
			return "agent", true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return "", err
	}
	if how == "ssh" {
		if _, err := guestRun(g, []string{"bash", "-c", authorizeScript, "rox", public}, "", 30*time.Second); err != nil {
			return "", err
		}
		return guestRun(g, []string{"cat", "/etc/ssh/ssh_host_ed25519_key.pub"}, "", 30*time.Second)
	}
	if _, err := sys.OutputWith(sys.Cmd{Argv: []string{"tart", "exec", g.Name, "/bin/bash", "-c", authorizeScript, "rox", public}, Timeout: 30 * time.Second}); err != nil {
		return "", err
	}
	return sys.OutputWith(sys.Cmd{Argv: []string{"tart", "exec", g.Name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub"}, Timeout: 30 * time.Second})
}

func prepareMacosTemplate(o *TemplateOptions, configPath string, settings map[string]any) error {
	if o.WithRex {
		if err := sys.Require("ditto"); err != nil {
			return err
		}
		if !sys.IsFile(filepath.Join(rexApp, rexAppCLI)) {
			return fmt.Errorf("Install the Rex app at %s first. rox template --with-rex copies it into the template", rexApp)
		}
	}
	name := firstNonEmpty(o.Name, str(settings["image"]), DefaultTemplateName)
	base := firstNonEmpty(o.Base, DefaultTemplateBase)
	user, identitySetting, identity, public, err := templateLogin(name, settings, configPath)
	if err != nil {
		return err
	}
	hostKeySetting := firstNonEmpty(str(settings["host_key"]), "~/.ssh/"+strings.ReplaceAll(name, "-", "_")+"_host.pub")
	hostKey := sys.Expand(hostKeySetting)
	passwordPath := filepath.Join(filepath.Dir(configPath), name+"-"+user+"-password")
	if err := cloneTemplate(name, base, "about 30 GB"); err != nil {
		return err
	}
	g := templateGuest(name, "macos", user, identity)
	os.RemoveAll(g.Directory())
	if err := os.MkdirAll(g.Directory(), 0o700); err != nil {
		return err
	}
	var vm sys.Process
	defer func() { stopIfRunning(g, vm) }()
	err = func() error {
		s := ui.Start("Booting %s", name)
		var err error
		if vm, err = runVM(g); err != nil {
			s.Fail(err)
			return err
		}
		key, err := authorizeTemplateKey(g, public, hostKey, o.BootTimeout)
		if err == nil {
			err = pinHost(g, key)
		}
		if err == nil {
			_ = os.MkdirAll(filepath.Dir(hostKey), 0o700)
			err = os.WriteFile(hostKey, []byte(strings.TrimSpace(key)+"\n"), 0o644)
		}
		if err == nil {
			g.Address, g.Port, err = waitAddress(tartBackend{}, g, o.BootTimeout, name+" to get an address")
		}
		if err == nil {
			err = checkGuestOS(g, o.BootTimeout)
		}
		if err != nil {
			s.Fail(err)
			return err
		}
		s.Done("Booted %s and authorized the login key", name)
		if err := ui.Run("Replacing the base image's password", "Replaced the password and kept auto-login working", func(*ui.Step) error {
			password, err := templatePassword(passwordPath)
			if err != nil {
				return err
			}
			secret := strings.Join([]string{o.BasePassword, password, base64.StdEncoding.EncodeToString(Kcpassword(password))}, "\n") + "\n"
			_, err = guestRun(g, []string{"bash", "-c", templatePasswordScript}, secret, 60*time.Second)
			return err
		}); err != nil {
			return err
		}
		if o.WithRex {
			if err := ui.Run("Installing Rex from "+rexApp, "Installed Rex from "+rexApp, func(*ui.Step) error {
				scratch, err := os.MkdirTemp("", "rox-rex-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(scratch)
				archive := filepath.Join(scratch, "Rex.zip")
				if _, err := sys.Run(sys.Cmd{Argv: []string{"ditto", "-c", "-k", "--keepParent", rexApp, archive}}); err != nil {
					return err
				}
				f, err := os.Open(archive)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = sys.Run(sys.Cmd{Argv: remoteCommand(g, []string{"bash", "-c", templateRexScript, "rox", rexAppCLI}, "", false), Stdin: f})
				return err
			}); err != nil {
				return err
			}
		}
		out, err := guestRun(g, []string{"bash", "-c", templateCheckScript, "rox", rexCheck(o.WithRex)}, "", 30*time.Second)
		if err != nil {
			return err
		}
		for _, line := range sys.Lines(out) {
			ui.Detail("%s", line)
		}
		shutDownTemplate(g, vm)
		return ui.Run("Rebooting "+name+" to check that "+user+" signs in to the desktop", user+" signs in to the desktop", func(*ui.Step) error {
			var err error
			if vm, err = bootTemplate(g, o.BootTimeout); err != nil {
				return err
			}
			_, err = sys.WaitFor(o.BootTimeout, user+" to sign in to the desktop of "+name, func() (bool, bool, error) {
				out, err := guestRun(g, []string{"stat", "-f", "%Su", "/dev/console"}, "", 8*time.Second)
				return true, out == user, err
			})
			if err == nil {
				shutDownTemplate(g, vm)
			}
			return err
		})
	}()
	if err != nil {
		return err
	}
	if err := UpdateConfig(configPath, func(data map[string]any) error {
		section, _ := data["macos"].(map[string]any)
		if section == nil {
			section = map[string]any{}
		}
		section["image"], section["user"], section["identity"], section["host_key"] = name, user, identitySetting, hostKeySetting
		data["macos"] = section
		return nil
	}); err != nil {
		return err
	}
	os.RemoveAll(g.Directory())
	ui.Card(name, "ready", []ui.Field{{Label: "Template", Value: name}, {Label: "Password", Value: passwordPath},
		{Label: "Config", Value: configPath, Note: "(macos)"}, {Label: "Launch", Value: ui.Code("rox up macos")}})
	if o.WithRex {
		ui.Hint("Run " + ui.Code("rox template macos --with-rex") + " again after upgrading Rex")
	}
	return nil
}

// passwordSSH runs argv in the guest with the base image's password, before it takes the login key.
func passwordSSH(g *Guest, argv []string, password string) (sys.Result, error) {
	scratch, err := os.MkdirTemp("", "rox-askpass-")
	if err != nil {
		return sys.Result{}, err
	}
	defer os.RemoveAll(scratch)
	askpass := filepath.Join(scratch, "askpass")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\nprintf \"%s\\n\" \"$ROX_TEMPLATE_PASSWORD\"\n"), 0o700); err != nil {
		return sys.Result{}, err
	}
	known := g.KnownHosts()
	command := []string{"ssh", "-F", "/dev/null", "-T", "-o", "BatchMode=no", "-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=yes", "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostKeyAlias=" + g.Name,
		"-o", "UserKnownHostsFile=" + known, "-o", "PubkeyAuthentication=no",
		"-o", "PreferredAuthentications=keyboard-interactive,password", "-o", "NumberOfPasswordPrompts=1",
		"-l", g.User, "-p", fmt.Sprint(g.Port), "--", g.Address, sys.Quote(argv...)}
	return sys.Try(sys.Cmd{Argv: command, Timeout: 60 * time.Second, Env: map[string]string{"SSH_ASKPASS": askpass,
		"SSH_ASKPASS_REQUIRE": "force", "ROX_TEMPLATE_PASSWORD": password, "DISPLAY": firstNonEmpty(os.Getenv("DISPLAY"), ":0")}})
}

func scannedHostKey(g *Guest) (string, error) {
	out, err := sys.OutputWith(sys.Cmd{Argv: []string{"ssh-keyscan", "-T", "5", "-t", "ed25519", "-p", fmt.Sprint(g.Port), g.Address}, Timeout: 20 * time.Second})
	if err != nil {
		return "", err
	}
	for _, line := range sys.Lines(out) {
		if f := strings.Fields(line); len(f) >= 3 && !strings.HasPrefix(line, "#") {
			return f[1] + " " + f[2], nil
		}
	}
	return "", fmt.Errorf("%s didn't offer an ed25519 host key", g.Name)
}

func reachLinuxTemplate(g *Guest, public, shared, basePassword string, boot time.Duration) error {
	how, err := sys.WaitFor(boot, "SSH or the Tart guest agent in "+g.Name, func() (string, bool, error) {
		addr, port, err := vmAddress(g)
		if err != nil {
			return "", false, err
		}
		g.Address, g.Port = addr, port
		_ = pinHost(g, shared)
		if r, err := sys.Try(sys.Cmd{Argv: remoteCommand(g, []string{"true"}, "", false), Timeout: 15 * time.Second}); err == nil && r.Code == 0 {
			return "ssh", true, nil // rox prepared it before.
		}
		if r, err := sys.Try(sys.Cmd{Argv: []string{"tart", "exec", g.Name, "true"}, Timeout: 15 * time.Second}); err == nil && r.Code == 0 {
			return "agent", true, nil
		}
		key, err := scannedHostKey(g)
		if err != nil {
			return "", false, nil
		}
		_ = pinHost(g, key)
		if r, err := passwordSSH(g, []string{"true"}, basePassword); err == nil && r.Code == 0 {
			return "password", true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return err
	}
	switch how {
	case "agent":
		if _, err := sys.OutputWith(sys.Cmd{Argv: []string{"tart", "exec", g.Name, "/bin/sh", "-c", authorizeScript, "rox", public}, Timeout: 30 * time.Second}); err != nil {
			return err
		}
		key, err := sys.OutputWith(sys.Cmd{Argv: []string{"tart", "exec", g.Name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub"}, Timeout: 30 * time.Second})
		if err != nil {
			return err
		}
		if err := pinHost(g, key); err != nil {
			return err
		}
	case "password":
		r, err := passwordSSH(g, []string{"sh", "-c", authorizeScript, "rox", public}, basePassword)
		if err != nil {
			return err
		}
		if r.Code != 0 {
			return fmt.Errorf("Could not authorize the login key in %s: %s", g.Name, strings.TrimSpace(r.Stderr))
		}
	}
	return checkGuestOS(g, boot)
}

func sharedLinuxHostKey(configPath string) (string, string, error) {
	private := filepath.Join(filepath.Dir(configPath), linuxHostKeyFile)
	if !sys.IsFile(private) {
		_ = os.MkdirAll(filepath.Dir(private), 0o755)
		if _, err := sys.Run(sys.Cmd{Argv: []string{"ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "rox linux template", "-f", private}}); err != nil {
			return "", "", err
		}
	}
	public, err := sys.Output("ssh-keygen", "-y", "-f", private)
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(public)
	data, err := os.ReadFile(private)
	return string(data), strings.Join(fields[:min(2, len(fields))], " "), err
}

func checkTemplateClone(template, user, identity, hostKey string, boot time.Duration) error {
	clone := templateGuest(template+"-check-"+randomHex(3), "linux", user, identity)
	if err := os.MkdirAll(clone.Directory(), 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(clone.Directory())
	if _, err := sys.Run(sys.Cmd{Argv: []string{"tart", "clone", template, clone.Name}}); err != nil {
		return err
	}
	defer sys.Try(sys.Cmd{Argv: []string{"tart", "delete", clone.Name}, Timeout: 120 * time.Second})
	if err := pinHost(clone, hostKey); err != nil {
		return err
	}
	vm, err := bootTemplate(clone, boot)
	defer stopIfRunning(clone, vm)
	if err != nil {
		return err
	}
	if _, err := guestRun(clone, []string{"true"}, "", 30*time.Second); err != nil {
		return err
	}
	shutDownTemplate(clone, vm)
	return nil
}

func prepareLinuxTemplate(o *TemplateOptions, configPath string, settings map[string]any) error {
	if err := sys.Require("ssh-keyscan"); err != nil {
		return err
	}
	distro, base := o.Distro, o.Base
	if base == "" {
		distro = firstNonEmpty(distro, DefaultLinuxDistro)
		if base = LinuxTemplateBases[distro]; base == "" {
			return fmt.Errorf("No Tart image of %s is published. Pass --base with one", distro)
		}
	}
	name := o.Name
	if name == "" && distro != "" {
		name = "rox-" + distro
	}
	if name == "" {
		return errors.New("Name the template with --name, or say which distribution --base is with --distro")
	}
	user, identitySetting, identity, public, err := templateLogin(name, settings, configPath)
	if err != nil {
		return err
	}
	hostKeySetting := firstNonEmpty(str(settings["host_key"]), "~/.ssh/rox_linux_host.pub")
	hostKey := sys.Expand(hostKeySetting)
	privateHostKey, shared, err := sharedLinuxHostKey(configPath)
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(hostKey); err == nil && strings.Join(strings.Fields(string(data))[:min(2, len(strings.Fields(string(data))))], " ") != shared {
		// A template prepared by hand has its own key. Keep it, and pin the shared one elsewhere.
		if hostKeySetting != "~/.ssh/rox_linux_host.pub" {
			ui.Info("Linux templates now share one host key, so linux.host_key moves from %s to ~/.ssh/rox_linux_host.pub", hostKeySetting)
			hostKeySetting = "~/.ssh/rox_linux_host.pub"
			hostKey = sys.Expand(hostKeySetting)
		}
	}
	passwordPath := filepath.Join(filepath.Dir(configPath), name+"-"+user+"-password")
	if err := cloneTemplate(name, base, "a few GB"); err != nil {
		return err
	}
	channel, installer := "", ""
	installing := "Installing Git"
	if o.WithRex || o.RexChannel != "" {
		channel, installer = firstNonEmpty(o.RexChannel, "preview"), rexInstaller
		installing = "Installing Git and Rex (" + channel + ")"
	}
	g := templateGuest(name, "linux", user, identity)
	os.RemoveAll(g.Directory())
	if err := os.MkdirAll(g.Directory(), 0o700); err != nil {
		return err
	}
	var vm sys.Process
	defer func() { stopIfRunning(g, vm) }()
	err = func() error {
		if err := ui.Run("Booting "+name, "Booted "+name+" and authorized the login key", func(*ui.Step) error {
			var err error
			if vm, err = runVM(g); err != nil {
				return err
			}
			return reachLinuxTemplate(g, public, shared, o.BasePassword, o.BootTimeout)
		}); err != nil {
			return err
		}
		if err := ui.Run(installing, strings.Replace(installing, "Installing", "Installed", 1)+", replaced the password, turned on lingering", func(*ui.Step) error {
			password, err := templatePassword(passwordPath)
			if err != nil {
				return err
			}
			_, err = guestRun(g, []string{"bash", "-c", linuxSetupScript, "rox", channel, installer}, password+"\n", 30*time.Minute)
			return err
		}); err != nil {
			return err
		}
		if err := ui.Run("Installing the shared Linux host key", "Installed the shared Linux host key", func(*ui.Step) error {
			if _, err := guestRun(g, []string{"sh", "-c", linuxHostKeyScript, "rox", shared}, privateHostKey, 60*time.Second); err != nil {
				return err
			}
			return pinHost(g, shared)
		}); err != nil {
			return err
		}
		out, err := guestRun(g, []string{"bash", "-c", linuxCheckScript, "rox", rexCheck(installer != "")}, "", 30*time.Second)
		if err != nil {
			return err
		}
		for _, line := range sys.Lines(out) {
			ui.Detail("%s", line)
		}
		shutDownTemplate(g, vm)
		return nil
	}()
	if err != nil {
		return err
	}
	os.RemoveAll(g.Directory())
	_ = os.MkdirAll(filepath.Dir(hostKey), 0o700)
	if err := os.WriteFile(hostKey, []byte(shared+"\n"), 0o644); err != nil {
		return err
	}
	if err := ui.Run("Booting a clone of "+name+" to check its pinned host key", "A clone of "+name+" takes SSH with the pinned host key", func(*ui.Step) error {
		return checkTemplateClone(name, user, identity, shared, o.BootTimeout)
	}); err != nil {
		return err
	}
	isDefault := false
	if err := UpdateConfig(configPath, func(data map[string]any) error {
		section, _ := data["linux"].(map[string]any)
		if section == nil {
			section = map[string]any{}
		}
		current := str(section["image"])
		isDefault = current == "" || current == name || o.Default
		if isDefault {
			section["image"] = name
		}
		section["user"], section["identity"], section["host_key"] = user, identitySetting, hostKeySetting
		data["linux"] = section
		return nil
	}); err != nil {
		return err
	}
	launch := "rox up linux"
	if !isDefault {
		launch += " --image " + name
	}
	ui.Card(name, "ready", []ui.Field{{Label: "Template", Value: name, Note: "(" + base + ")"}, {Label: "Password", Value: passwordPath},
		{Label: "Config", Value: configPath, Note: "(linux)"}, {Label: "Launch", Value: ui.Code(launch)}})
	if installer != "" {
		ui.Hint("Run " + ui.Code("rox template linux --with-rex") + " again to update Rex")
	}
	return nil
}

// rexCheck is the argument that makes a template's check script look for Rex.
func rexCheck(withRex bool) string {
	if withRex {
		return "rex"
	}
	return ""
}

// authorizeScript adds the public key in $1 to the login's authorized_keys.
const authorizeScript = `set -eu
umask 077
mkdir -p "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
grep -qxF "$1" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$1" >> "$HOME/.ssh/authorized_keys"
`
