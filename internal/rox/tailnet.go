package rox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// Funnels: Tailscale on the launcher serves a guest port on the internet, through an SSH tunnel.

var FunnelPorts = []int{443, 8443, 10000}

func tailscaleCLI() (string, error) {
	if p, err := sys.R.LookPath("tailscale"); err == nil {
		return p, nil
	}
	app := "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	if sys.IsFile(app) {
		return app, nil
	}
	return "", errors.New("Install Tailscale on this machine to funnel guest ports. Rex's own tailnet node can't funnel")
}

// The binary in Tailscale.app starts the GUI without a terminal, as under the service, unless this is set.
var tailscaleEnv = map[string]string{"TAILSCALE_BE_CLI": "1"}

func tailscale(timeout time.Duration, args ...string) (sys.Result, error) {
	cli, err := tailscaleCLI()
	if err != nil {
		return sys.Result{}, err
	}
	return sys.Try(sys.Cmd{Argv: append([]string{cli}, args...), Env: tailscaleEnv, Timeout: timeout})
}

func tailscaleJSON(args ...string) (map[string]any, error) {
	result, err := tailscale(20*time.Second, append(args, "--json")...)
	if err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("tailscale %s failed: %s", strings.Join(args, " "), firstLine(result.Output()))
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(firstNonEmpty(result.Stdout, "{}")), &v); err != nil {
		return nil, fmt.Errorf("tailscale %s didn't print JSON", strings.Join(args, " "))
	}
	return v, nil
}

func funnelTarget(f *Funnel) string { return fmt.Sprintf("http://127.0.0.1:%d", f.Local) }

func funnelPathArgs(f *Funnel) []string {
	if f.Path != "/" {
		return []string{"--set-path=" + f.Path}
	}
	return nil
}

type handlerKey struct {
	https int
	path  string
}

type handler struct {
	proxy  string
	public bool
}

func funnelHandlers() (map[handlerKey]handler, error) {
	status, err := tailscaleJSON("funnel", "status")
	if err != nil {
		return nil, err
	}
	public := map[string]bool{}
	if allow, ok := status["AllowFunnel"].(map[string]any); ok {
		for key, allowed := range allow {
			if allowed == true {
				public[key[strings.LastIndexByte(key, ':')+1:]] = true
			}
		}
	}
	handlers := map[handlerKey]handler{}
	web, _ := status["Web"].(map[string]any)
	for key, site := range web {
		port := key[strings.LastIndexByte(key, ':')+1:]
		https, _ := strconv.Atoi(port)
		siteMap, _ := site.(map[string]any)
		hs, _ := siteMap["Handlers"].(map[string]any)
		for p, h := range hs {
			hm, _ := h.(map[string]any)
			proxy, _ := hm["Proxy"].(string)
			handlers[handlerKey{https, p}] = handler{proxy, public[port]}
		}
	}
	return handlers, nil
}

func funnelSocket(g *Guest, f *Funnel) string {
	return filepath.Join(g.Runtime(), fmt.Sprintf("f%d", f.Port))
}

func funnelTunnelUp(g *Guest, f *Funnel) bool {
	sock := funnelSocket(g, f)
	return sys.IsSocket(sock) && sys.Status(sshCommand(g, "-S", sock, "-O", "check")...) == 0
}

// openFunnelTunnel tunnels a launcher port to the guest port, picking a new launcher port if
// it's taken. It reports whether the launcher port changed.
func openFunnelTunnel(g *Guest, f *Funnel) (bool, error) {
	if funnelTunnelUp(g, f) {
		return false, nil
	}
	if err := ensureRuntimeDirs(g); err != nil {
		return false, err
	}
	changed := false
	for attempt := 0; attempt < 2; attempt++ {
		result, err := sys.Try(sys.Cmd{Timeout: 20 * time.Second, Argv: sshCommand(g, "-fNM", "-S", funnelSocket(g, f),
			"-o", "ControlPersist=no", "-E", g.Log("ssh-tunnel.log"), "-o", "ForwardAgent=no", "-o", "ExitOnForwardFailure=yes",
			"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", f.Local, f.Port))})
		if err == nil && result.Code == 0 {
			return changed, nil
		}
		f.Local, changed = sys.FreePort(), true
	}
	return changed, fmt.Errorf("Could not tunnel to port %d in %s. See %s", f.Port, g.Name, g.Log("ssh-tunnel.log"))
}

func closeFunnelTunnel(g *Guest, f *Funnel) {
	if sock := funnelSocket(g, f); sys.IsSocket(sock) {
		sys.Status(sshCommand(g, "-S", sock, "-O", "exit")...)
	}
}

func funnelOn(f *Funnel) error {
	args := append([]string{"funnel", "--bg", "--yes", fmt.Sprintf("--https=%d", f.HTTPS)}, funnelPathArgs(f)...)
	result, err := tailscale(30*time.Second, append(args, funnelTarget(f))...)
	var timeout *sys.TimeoutError
	if errors.As(err, &timeout) {
		return fmt.Errorf("Tailscale is waiting for Funnel to be enabled. %s", strings.TrimSpace(timeout.Stdout))
	}
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("Tailscale couldn't funnel port %d: %s", f.HTTPS, firstLine(result.Output()))
	}
	return nil
}

func funnelOff(f *Funnel) error {
	args := append([]string{"funnel", "--yes", fmt.Sprintf("--https=%d", f.HTTPS)}, funnelPathArgs(f)...)
	_, err := tailscale(30*time.Second, append(args, "off")...)
	return err
}

// restoreFunnels brings funnels back after a restart. Problems are reported, not returned,
// so a funnel never keeps the guest itself from coming back.
func restoreFunnels(g *Guest) bool {
	if len(g.Funnels) == 0 {
		return false
	}
	restored := false
	err := func() error {
		handlers, err := funnelHandlers()
		if err != nil {
			return err
		}
		for _, f := range g.Funnels {
			changed, err := openFunnelTunnel(g, f)
			if err != nil {
				return err
			}
			h := handlers[handlerKey{f.HTTPS, f.Path}]
			if changed || h.proxy != funnelTarget(f) || !h.public {
				if err := funnelOn(f); err != nil {
					return err
				}
				restored = true
			}
		}
		if restored {
			g.MustSave()
		}
		return nil
	}()
	if err != nil {
		ui.Warn("Could not restore the funnels for %s: %s", g.Name, firstLine(err.Error()))
	}
	return restored
}

// stopFunnels takes funnels off the internet and closes their tunnels. The record keeps them.
func stopFunnels(g *Guest, funnels []*Funnel) {
	if funnels == nil {
		funnels = g.Funnels
	}
	for _, f := range funnels {
		if err := funnelOff(f); err != nil {
			ui.Warn("Could not turn off %s. Run tailscale funnel status to check (%v)", f.URL, err)
		}
		closeFunnelTunnel(g, f)
	}
}

// ListFunnels prints a guest's funnels, or every funnel of this machine's guests.
func ListFunnels(name string) error {
	var guests []*Guest
	if name != "" {
		g, err := LoadGuest(name)
		if err != nil {
			return err
		}
		guests = []*Guest{g}
	} else {
		guests = LocalGuests()
	}
	var rows [][]string
	for _, g := range guests {
		for _, f := range g.Funnels {
			rows = append(rows, []string{ui.Accent(f.URL), strconv.Itoa(f.Port), g.Name})
		}
	}
	if len(rows) == 0 {
		who := "No guest here has"
		if name != "" {
			who = name + " has no"
		}
		ui.Skip("%s funnels", who)
		ui.Hint("Start one with " + ui.Code("rox funnel add "+firstNonEmpty(name, "NAME")+" PORT"))
		return nil
	}
	ui.Table([]string{"url", "port", "guest"}, rows)
	return nil
}

// RemoveFunnels takes a guest's funnels for port, or all of them, off the internet for good.
func RemoveFunnels(name string, port int) error {
	var chosen []*Funnel
	err := withGuest(name, true, func(g *Guest) error {
		var kept []*Funnel
		for _, f := range g.Funnels {
			if port == 0 || f.Port == port {
				chosen = append(chosen, f)
			} else {
				kept = append(kept, f)
			}
		}
		if len(chosen) == 0 {
			return nil
		}
		stopFunnels(g, chosen)
		g.Funnels = kept
		if g.Funnels == nil {
			g.Funnels = []*Funnel{}
		}
		return g.Save()
	})
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		which := "s"
		if port != 0 {
			which = fmt.Sprintf(" for port %d", port)
		}
		ui.Skip("%s has no funnel%s", name, which)
	}
	for _, f := range chosen {
		ui.Done("Stopped funneling %s to port %d on %s", f.URL, f.Port, name)
	}
	return nil
}

// AddFunnel puts a guest port on the internet through this machine's Tailscale.
func AddFunnel(name string, port, https int, path string) error {
	ui.Header("funnel add", fmt.Sprintf("%s:%d", name, port))
	var entry *Funnel
	err := withGuest(name, true, func(g *Guest) error {
		if g.Stopped {
			return fmt.Errorf("%s is stopped. Start it with rox start %s", name, name)
		}
		if g.Phase != "ready" {
			return fmt.Errorf("%s is not ready", name)
		}
		if _, err := tailscaleCLI(); err != nil {
			return err
		}
		if _, err := resume(g, 120*time.Second, false); err != nil {
			return err
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// Each public address serves one target, so don't take one that's already in use.
		for _, other := range LocalGuests() {
			if other.Name == name {
				continue
			}
			for _, f := range other.Funnels {
				if f.HTTPS == https && f.Path == path {
					return fmt.Errorf("%s already funnels port %d at %s. Pick another with --https 8443 or 10000, or --path", other.Name, https, path)
				}
			}
		}
		var previous *Funnel
		for _, f := range g.Funnels {
			if f.HTTPS == https && f.Path == path {
				previous = f
			}
		}
		handlers, err := funnelHandlers()
		if err != nil {
			return err
		}
		if h, ok := handlers[handlerKey{https, path}]; ok && previous == nil {
			return fmt.Errorf("Tailscale already serves %s on port %d at %s. Pick another with --https or --path, or turn it off with tailscale funnel", h.proxy, https, path)
		}
		if previous != nil {
			closeFunnelTunnel(g, previous)
			var kept []*Funnel
			for _, f := range g.Funnels {
				if f != previous {
					kept = append(kept, f)
				}
			}
			g.Funnels = kept
		}
		status, err := tailscaleJSON("status")
		if err != nil {
			return err
		}
		self, _ := status["Self"].(map[string]any)
		dns, _ := self["DNSName"].(string)
		dns = strings.TrimRight(dns, ".")
		if dns == "" {
			return errors.New("tailscale status didn't report this machine's tailnet name. Is Tailscale signed in?")
		}
		url := "https://" + dns
		if https != 443 {
			url += fmt.Sprintf(":%d", https)
		}
		url += path
		entry = &Funnel{Port: port, HTTPS: https, Path: path, Local: sys.FreePort(), URL: url}
		s := ui.Start("Funneling %s", url)
		if _, err := openFunnelTunnel(g, entry); err != nil {
			s.Fail(err)
			return err
		}
		if err := funnelOn(entry); err != nil {
			closeFunnelTunnel(g, entry)
			s.Fail(err)
			return err
		}
		s.Done("Funneling %s to port %d", ui.Accent(url), port)
		g.Funnels = append(g.Funnels, entry)
		return g.Save()
	})
	if err != nil {
		return err
	}
	ui.Warn("Anyone on the internet can open it")
	ui.Hint("Stop it with " + ui.Code(fmt.Sprintf("rox funnel remove %s %d", name, port)))
	return nil
}

var _ = os.Getuid
