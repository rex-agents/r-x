package rox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/assets"
	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

const (
	ServiceLabel    = "io.rox.service"
	ServiceInterval = 15 * time.Second
)

func serviceLog(format string, args ...any) {
	fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

type retryState struct {
	next  time.Time
	delay time.Duration
}

// serviceTick restores each ready guest that should run but stopped or lost its tunnel. A
// guest that keeps failing is tried less often, up to every ten minutes.
func serviceTick(retry map[string]*retryState, now time.Time) {
	matches, _ := filepath.Glob(filepath.Join(StateRoot(), "*", "guest.json"))
	sort.Strings(matches)
	for _, m := range matches {
		name := filepath.Base(filepath.Dir(m))
		g, err := LoadGuest(name)
		if err != nil {
			continue
		}
		if g.Phase != "ready" || g.Stopped || g.GuestSocket == "" {
			delete(retry, name)
			continue
		}
		if r := retry[name]; r != nil && now.Before(r.next) {
			continue
		}
		err = withGuest(name, false, func(g *Guest) error {
			if g.Phase != "ready" || g.Stopped {
				return nil
			}
			restored, err := resume(g, 120*time.Second, false)
			if restored {
				serviceLog("Restored %s", name)
			}
			return err
		})
		switch {
		case errors.Is(err, ErrBusy):
			continue
		case err != nil:
			delay := 30 * time.Second
			if r := retry[name]; r != nil {
				delay = min(max(r.delay*2, 30*time.Second), 10*time.Minute)
			}
			retry[name] = &retryState{now.Add(delay), delay}
			serviceLog("Could not restore %s, trying again in %s: %v", name, delay, err)
		default:
			delete(retry, name)
		}
	}
}

// ServiceRun keeps guests running until rox is upgraded, then exits so the service manager restarts it.
func ServiceRun(interval time.Duration) error {
	// The service's log is a file, so it gets plain text.
	ui.SetOutput(os.Stdout, os.Stdout)
	self := selfPath()
	info, err := os.Stat(self)
	if err != nil {
		return err
	}
	installed := info.ModTime()
	retry := map[string]*retryState{}
	serviceLog("Watching rox guests every %s", interval)
	for {
		if info, err := os.Stat(self); err == nil && !info.ModTime().Equal(installed) {
			serviceLog("rox changed on disk, so restarting to load it")
			return nil
		}
		serviceTick(retry, time.Now())
		time.Sleep(interval)
	}
}

func serviceFile() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(sys.Home(), "Library", "LaunchAgents", ServiceLabel+".plist")
	}
	return filepath.Join(sys.XDG("XDG_CONFIG_HOME", ".config"), "systemd", "user", "rox.service")
}

// serviceEnvironment is the PATH and XDG folders to run the service with, since service managers set neither.
func serviceEnvironment() [][2]string {
	home := sys.Home()
	entries := append(filepath.SplitList(os.Getenv("PATH")), filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin",
		"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	seen := map[string]bool{}
	var path []string
	for _, e := range entries {
		if e != "" && !seen[e] {
			seen[e] = true
			path = append(path, e)
		}
	}
	env := [][2]string{{"PATH", strings.Join(path, ":")}}
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		if value := os.Getenv(v); value != "" {
			env = append(env, [2]string{v, value})
		}
	}
	return env
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// LaunchdPlist is the service's launchd agent.
func LaunchdPlist(command []string, env [][2]string, log string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>AbandonProcessGroup</key>
	<true/>
	<key>EnvironmentVariables</key>
	<dict>
`)
	sorted := append([][2]string{}, env...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })
	for _, kv := range sorted {
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEscape(kv[0]), xmlEscape(kv[1]))
	}
	b.WriteString("\t</dict>\n\t<key>KeepAlive</key>\n\t<true/>\n\t<key>Label</key>\n\t<string>" + ServiceLabel + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range command {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString("\t</array>\n\t<key>RunAtLoad</key>\n\t<true/>\n")
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", xmlEscape(log), xmlEscape(log))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func systemdQuote(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}

// SystemdUnit is the service's systemd user unit.
func SystemdUnit(command []string, env [][2]string, log string) string {
	var quoted []string
	for _, part := range command {
		quoted = append(quoted, systemdQuote(part))
	}
	lines := []string{"[Unit]", "Description=Keep rox guests running", "", "[Service]", "ExecStart=" + strings.Join(quoted, " ")}
	for _, kv := range env {
		lines = append(lines, "Environment="+systemdQuote(kv[0]+"="+kv[1]))
	}
	// Guests, tunnels and relays outlive the service, so restarting it mustn't stop them.
	lines = append(lines, "Restart=always", "RestartSec=10", "KillMode=process",
		"StandardOutput=append:"+log, "StandardError=append:"+log, "", "[Install]", "WantedBy=default.target", "")
	return strings.Join(lines, "\n")
}

// InstallService starts the service at login with launchd or systemd.
func InstallService() error {
	ui.Header("service install", "")
	file := serviceFile()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	if _, err := sys.PrivateDir(StateRoot()); err != nil {
		return err
	}
	log := filepath.Join(StateRoot(), "service.log")
	command := []string{selfPath(), "service", "run"}
	env := serviceEnvironment()
	if runtime.GOOS == "darwin" {
		if err := sys.Require("launchctl"); err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(LaunchdPlist(command, env, log)), 0o644); err != nil {
			return err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		sys.Status("launchctl", "bootout", domain+"/"+ServiceLabel)
		// launchd can take a moment to unload the previous copy.
		if _, err := sys.WaitFor(10*time.Second, "launchd to load the rox service", func() (bool, bool, error) {
			return true, sys.Status("launchctl", "bootstrap", domain, file) == 0, nil
		}); err != nil {
			return err
		}
	} else {
		if err := sys.Require("systemctl"); err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(SystemdUnit(command, env, log)), 0o644); err != nil {
			return err
		}
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "rox.service"}, {"restart", "rox.service"}} {
			if _, err := sys.Output(append([]string{"systemctl", "--user"}, args...)...); err != nil {
				return err
			}
		}
	}
	ui.Done("Installed the rox service %s", ui.Muted("· it starts at login and keeps guests running"))
	ui.Card("", "", []ui.Field{{Label: "File", Value: file}, {Label: "Log", Value: log}})
	if runtime.GOOS != "darwin" {
		ui.Hint("To keep it running while you're logged out, run " + ui.Code("loginctl enable-linger $USER"))
	}
	return nil
}

// UninstallService stops the service and removes it.
func UninstallService() error {
	file := serviceFile()
	if runtime.GOOS == "darwin" {
		sys.Status("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), ServiceLabel))
		os.Remove(file)
	} else {
		sys.Status("systemctl", "--user", "disable", "--now", "rox.service")
		os.Remove(file)
		sys.Status("systemctl", "--user", "daemon-reload")
	}
	ui.Done("Removed the rox service %s", ui.Muted("· guests keep running, but nothing restarts them"))
	return nil
}

// ServiceStatus shows whether the service is running.
func ServiceStatus() error {
	file := serviceFile()
	if !sys.IsFile(file) {
		ui.Skip("The rox service isn't installed")
		ui.Hint("Install it with " + ui.Code("rox service install"))
		return nil
	}
	state := "not loaded"
	if runtime.GOOS == "darwin" {
		result, _ := sys.Try(sys.Cmd{Argv: []string{"launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), ServiceLabel)}})
		if m := regexp.MustCompile(`(?m)^\s*state = (\S+)`).FindStringSubmatch(result.Stdout); m != nil {
			state = m[1]
		}
	} else {
		result, _ := sys.Try(sys.Cmd{Argv: []string{"systemctl", "--user", "is-active", "rox.service"}})
		state = firstNonEmpty(strings.TrimSpace(result.Stdout), state)
	}
	ui.Card("rox service", "", []ui.Field{{Label: "Service", Value: ui.Phase(state)}, {Label: "File", Value: file},
		{Label: "Log", Value: filepath.Join(StateRoot(), "service.log")}})
	return nil
}

// SkillAgents are where each agent keeps its skills: an override variable and a default folder.
var SkillAgents = map[string][2]string{"claude": {"CLAUDE_CONFIG_DIR", ".claude"}, "codex": {"CODEX_HOME", ".codex"}}

// AgentHome is an agent's config folder on this machine.
func AgentHome(agent string) string {
	v := SkillAgents[agent]
	if dir := os.Getenv(v[0]); dir != "" {
		return sys.Expand(dir)
	}
	return filepath.Join(sys.Home(), v[1])
}

func isRoxSkill(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(data), "---", 3)
	return len(parts) >= 2 && regexp.MustCompile(`(?m)^name:\s*rox\s*$`).MatchString(parts[1])
}

// InstallSkill copies the bundled rox skill into each agent's skills folder.
func InstallSkill(agents []string, dir string, force bool, version string) error {
	type target struct{ agent, skills string }
	var targets []target
	if dir != "" {
		targets = append(targets, target{"", sys.Expand(dir)})
	} else {
		if len(agents) == 0 {
			for _, agent := range AgentOrder {
				if sys.IsDir(AgentHome(agent)) {
					agents = append(agents, agent)
				}
			}
		}
		if len(agents) == 0 {
			return errors.New("Found no Claude Code or Codex folder. Choose one with --agent claude, --agent codex, or --dir DIR")
		}
		for _, agent := range agents {
			targets = append(targets, target{agent, filepath.Join(AgentHome(agent), "skills")})
		}
	}
	for _, t := range targets {
		dest := filepath.Join(t.skills, "rox")
		if info, err := os.Lstat(dest); err == nil && (info.Mode()&os.ModeSymlink != 0 || !isRoxSkill(dest)) && !force {
			return fmt.Errorf("%s exists and isn't a rox skill. Pass --force to replace it", dest)
		}
		if err := os.MkdirAll(t.skills, 0o755); err != nil {
			return err
		}
		// Copy beside the destination, then swap it in, so a failed copy leaves the old skill.
		staging, err := os.MkdirTemp(t.skills, ".rox-skill-")
		if err != nil {
			return err
		}
		err = func() error {
			defer os.RemoveAll(staging)
			if err := copyFS(assets.Skill(), filepath.Join(staging, "rox")); err != nil {
				return err
			}
			if err := os.RemoveAll(dest); err != nil {
				return err
			}
			return os.Rename(filepath.Join(staging, "rox"), dest)
		}()
		if err != nil {
			return err
		}
		label := map[string]string{"claude": " for Claude Code", "codex": " for Codex"}[t.agent]
		ui.Done("Installed the rox skill (%s)%s %s", version, label, ui.Muted("→ "+dest))
	}
	ui.Info("Start a new agent session to load it. %s keeps it up to date", ui.Code("rox self-update"))
	return nil
}

func copyFS(src fs.FS, dest string) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, p)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// RefreshSkills reinstalls the skill for each agent that has it, after an update.
func RefreshSkills(version string) {
	var agents []string
	for _, agent := range AgentOrder {
		dir := filepath.Join(AgentHome(agent), "skills", "rox")
		if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink == 0 && isRoxSkill(dir) {
			agents = append(agents, agent)
		}
	}
	if len(agents) == 0 {
		return
	}
	// The new binary installs its own skill.
	result, err := sys.Try(sys.Cmd{Argv: append([]string{selfPath(), "skill", "install"}, agentFlags(agents)...), Env: map[string]string{"ROX_UPDATED": "1"}})
	if err != nil || result.Code != 0 {
		ui.Warn("Could not update the rox skill. Run rox skill install")
		return
	}
	ui.Done("Updated the rox skill")
}

func agentFlags(agents []string) []string {
	var out []string
	for _, a := range agents {
		out = append(out, "--agent", a)
	}
	return out
}
