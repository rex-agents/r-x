package rux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// rux sandbox DIR [--read PATH]... -- CMD...  (macOS)
//
// Runs CMD in DIR under a macOS sandbox profile: no network, writes only in
// DIR (and the system's temp folders), and nothing readable in your home
// folder except DIR and the --read paths. For actions that build code from a
// guest, whose build scripts the agent could have changed.
func runSandbox(args []string) {
	const usage = "usage: rux sandbox DIR [--read PATH]... -- CMD [ARGS...]"
	var dir string
	var reads []string
	i := 0
	for ; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			i++
			goto cmd
		case a == "--read" && i+1 < len(args):
			i++
			reads = append(reads, args[i])
		case dir == "" && !strings.HasPrefix(a, "-"):
			dir = a
		default:
			usageExit(usage)
		}
	}
cmd:
	if dir == "" || i >= len(args) {
		usageExit(usage)
	}
	abs := func(p string) string {
		p, err := filepath.Abs(p)
		if err == nil {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				p = r
			}
		}
		return p
	}
	dir = abs(dir)
	home := abs(os.Getenv("HOME"))
	tmp := filepath.Join(dir, ".sandbox-tmp")
	os.MkdirAll(tmp, 0o700)
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("(deny network*)\n(allow network* (local unix))\n")
	// No asking other programs to do things outside the sandbox: Apple Events
	// (osascript), Launch Services (open), and other system services.
	b.WriteString("(deny appleevent-send)\n(deny mach-lookup)\n")
	b.WriteString("(allow mach-lookup (global-name \"com.apple.system.logger\") (global-name \"com.apple.system.notification_center\") (global-name \"com.apple.system.opendirectoryd.libinfo\") (global-name \"com.apple.system.opendirectoryd.membership\") (global-name \"com.apple.trustd\") (global-name \"com.apple.trustd.agent\") (global-name \"com.apple.SecurityServer\") (global-name \"com.apple.ocspd\") (global-name \"com.apple.diagnosticd\"))\n")
	b.WriteString("(deny file-write*)\n")
	fmt.Fprintf(&b, "(allow file-write* (subpath %s) (subpath \"/private/var/folders\") (literal \"/dev/null\") (literal \"/dev/tty\") (literal \"/dev/dtracehelper\") (regex #\"^/dev/fd/\"))\n", q(dir))
	fmt.Fprintf(&b, "(deny file-read* (subpath %s))\n", q(home))
	fmt.Fprintf(&b, "(allow file-read* (literal %s) (subpath %s)", q(home), q(dir))
	for _, r := range reads {
		fmt.Fprintf(&b, " (subpath %s)", q(abs(r)))
	}
	b.WriteString(")\n")

	sb, err := exec.LookPath("sandbox-exec")
	if err != nil {
		fail("rux sandbox: sandbox-exec not found (macOS only)")
	}
	env := []string{"TMPDIR=" + tmp, "HOME=" + dir}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "TMPDIR" && k != "HOME" {
			env = append(env, kv)
		}
	}
	if err := os.Chdir(dir); err != nil {
		fail("rux sandbox: %v", err)
	}
	argv := append([]string{"sandbox-exec", "-p", b.String()}, args[i:]...)
	if err := syscall.Exec(sb, argv, env); err != nil {
		fail("rux sandbox: %v", err)
	}
}
