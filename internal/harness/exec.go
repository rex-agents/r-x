package harness

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Executor runs shell scripts where an agent works: this machine, or a rox guest.
type Executor interface {
	// Run runs script with sh in the working directory, with stdin if it's not nil, and
	// returns its combined output and exit code.
	Run(ctx context.Context, script string, stdin []byte, timeout time.Duration) (string, int, error)
	// Where describes the place, as "this machine" or "guest NAME".
	Where() string
	// Dir is the working directory.
	Dir() string
}

// Local runs on this machine.
type Local struct{ WorkDir string }

func (l *Local) Where() string { return "this machine" }
func (l *Local) Dir() string   { return l.WorkDir }

func (l *Local) Run(ctx context.Context, script string, stdin []byte, timeout time.Duration) (string, int, error) {
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = l.WorkDir
	return run(ctx, cmd, stdin, timeout)
}

// Guest runs in a rox guest over rox exec, in the guest's checkout. AgentSock, when set,
// is rix's Git agent for the guest, forwarded so git can fetch and push with the user's key.
type Guest struct {
	Rox       string
	Name      string
	Host      string
	WorkDir   string
	AgentSock string
}

func (g *Guest) Where() string { return "guest " + g.Name }
func (g *Guest) Dir() string   { return g.WorkDir }

func (g *Guest) Run(ctx context.Context, script string, stdin []byte, timeout time.Duration) (string, int, error) {
	args := []string{"exec", g.Name}
	if stdin != nil {
		args = append(args, "--stdin")
	}
	if g.AgentSock != "" {
		args = append(args, "--agent-sock", g.AgentSock)
	}
	full := script
	if g.WorkDir != "" {
		full = "cd " + ShellQuote(g.WorkDir) + " && " + script
	}
	// A login shell picks up the PATH the guest's tools were installed with.
	args = append(args, "--", "bash", "-lc", full)
	return run(ctx, exec.Command(firstNonEmpty(g.Rox, "rox"), args...), stdin, timeout)
}

func run(ctx context.Context, cmd *exec.Cmd, stdin []byte, timeout time.Duration) (string, int, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// Its own process group, so stopping it stops what it started (rox exec's ssh too),
	// and a grandchild holding the output open can't keep Wait from returning.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Start(); err != nil {
		return "", -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			return out.String(), -1, err
		}
		return out.String(), code, nil
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
			done <- nil
		case <-time.After(time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out.String(), -1, errors.New("timed out after " + timeout.String())
		}
		return out.String(), -1, errors.New("interrupted")
	}
}

// ShellQuote quotes s for sh.
func ShellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:@%+,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
