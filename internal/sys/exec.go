// Package sys runs the external tools rox and rix drive, and holds shared file and process helpers.
//
// Every subprocess goes through a Runner, which captures its output instead of letting it
// reach the terminal. Tests swap in a Fake that records each command line and its stdin.
package sys

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rex-agents/r-x/internal/ui"
)

// Cmd describes one subprocess.
type Cmd struct {
	Argv []string
	// Stdin feeds the process. Nil means /dev/null, unless Input is set.
	Stdin io.Reader
	Input string
	// Env adds or replaces variables in this process's environment. Unset removes some.
	Env   map[string]string
	Unset []string
	Dir   string
	// Timeout kills the process after this long. Zero means no limit.
	Timeout time.Duration
	// Interactive hands the process this terminal: stdin, stdout and stderr.
	Interactive bool
	// Passthrough sends stdout and stderr straight to this process's, for transports such as rox exec.
	Passthrough bool
	// KeepStdin passes this process's stdin through rather than /dev/null.
	KeepStdin bool
}

// Result is what a finished subprocess left behind.
type Result struct {
	Code     int
	Stdout   string
	Stderr   string
	TimedOut bool
}

// Output is everything the process printed, for showing after a failure.
func (r Result) Output() string {
	if r.Stdout != "" && r.Stderr != "" {
		return strings.TrimRight(r.Stdout, "\n") + "\n" + r.Stderr
	}
	return r.Stdout + r.Stderr
}

// Process is a detached subprocess that outlives the command that started it.
type Process interface {
	Pid() int
	// Exited reports whether the process ended, and its status if so.
	Exited() (bool, int)
	Kill()
}

// Runner starts subprocesses.
type Runner interface {
	Run(Cmd) (Result, error)
	// Detach starts a process in its own session, logging to log, which may be "" for none.
	Detach(c Cmd, log string) (Process, error)
	// Replace runs argv in place of this process, for interactive shells.
	Replace(argv []string, env map[string]string) error
	LookPath(name string) (string, error)
}

// R is the runner every helper uses. Tests replace it.
var R Runner = Real{}

// Real runs actual subprocesses.
type Real struct{}

func environ(add map[string]string, unset []string) []string {
	env := os.Environ()
	drop := map[string]bool{}
	for _, name := range unset {
		drop[name] = true
	}
	for name := range add {
		drop[name] = true
	}
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			out = append(out, kv)
		}
	}
	for name, value := range add {
		out = append(out, name+"="+value)
	}
	return out
}

// lineTee streams each line written to it to the verbose log while keeping all of it.
type lineTee struct {
	buf     bytes.Buffer
	partial []byte
	mu      sync.Mutex
}

func (t *lineTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if ui.Verbose() {
		t.partial = append(t.partial, p...)
		for {
			i := bytes.IndexByte(t.partial, '\n')
			if i < 0 {
				break
			}
			ui.Stream(string(t.partial[:i]))
			t.partial = t.partial[i+1:]
		}
	}
	return len(p), nil
}

func (Real) Run(c Cmd) (Result, error) {
	if len(c.Argv) == 0 {
		return Result{}, errors.New("no command to run")
	}
	ctx := context.Background()
	var cancel context.CancelFunc = func() {}
	if c.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = environ(c.Env, c.Unset)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr lineTee
	switch {
	case c.Interactive:
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	case c.Passthrough:
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	default:
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	}
	if !c.Interactive {
		switch {
		case c.Stdin != nil:
			cmd.Stdin = c.Stdin
		case c.Input != "":
			cmd.Stdin = strings.NewReader(c.Input)
		case c.KeepStdin:
			cmd.Stdin = os.Stdin
		}
	}
	err := cmd.Run()
	result := Result{Stdout: stdout.buf.String(), Stderr: stderr.buf.String()}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.Code = -1
		return result, &TimeoutError{Argv: c.Argv, Timeout: c.Timeout, Result: result}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.Code = exit.ExitCode()
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("could not run %s: %w", c.Argv[0], err)
	}
	return result, nil
}

type realProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	code int
}

func (p *realProcess) Pid() int { return p.cmd.Process.Pid }
func (p *realProcess) Exited() (bool, int) {
	select {
	case <-p.done:
		return true, p.code
	default:
		return false, 0
	}
}
func (p *realProcess) Kill() {
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
	}
}

func (Real) Detach(c Cmd, log string) (Process, error) {
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = environ(c.Env, c.Unset)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Stdin != nil {
		cmd.Stdin = c.Stdin
	}
	if log != "" {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start %s: %w", c.Argv[0], err)
	}
	p := &realProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			p.code = exit.ExitCode()
		}
		close(p.done)
	}()
	return p, nil
}

func (Real) Replace(argv []string, env map[string]string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("could not find %s on PATH", argv[0])
	}
	return syscall.Exec(path, argv, environ(env, nil))
}

func (Real) LookPath(name string) (string, error) { return exec.LookPath(name) }

// ExitError is a command that exited with a nonzero status. Its output explains why.
type ExitError struct {
	Argv []string
	Result
	// Message replaces the default first line.
	Message string
}

func (e *ExitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	reason := firstLine(e.Stderr)
	if reason == "" {
		reason = firstLine(e.Stdout)
	}
	if reason != "" {
		return fmt.Sprintf("%s failed: %s", Base(e.Argv[0]), reason)
	}
	return fmt.Sprintf("%s exited with status %d", Base(e.Argv[0]), e.Code)
}

// Output is the captured output, which the ui shows under the error.
func (e *ExitError) Output() string {
	if e.Message == "" && strings.Count(strings.TrimSpace(e.Result.Output()), "\n") == 0 {
		return "" // A one-line reason is already in the message.
	}
	return e.Result.Output()
}

// TimeoutError is a command that ran out of time.
type TimeoutError struct {
	Argv    []string
	Timeout time.Duration
	Result
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s did not finish within %s", Base(e.Argv[0]), e.Timeout)
}

func (e *TimeoutError) Output() string { return e.Result.Output() }

func firstLine(s string) string {
	for _, line := range strings.Split(ui.StripANSI(s), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// Base is the last element of a program path.
func Base(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// Run runs c and fails when it exits nonzero.
func Run(c Cmd) (Result, error) {
	result, err := R.Run(c)
	if err != nil {
		return result, err
	}
	if result.Code != 0 {
		return result, &ExitError{Argv: c.Argv, Result: result}
	}
	return result, nil
}

// Try runs c and returns its result whatever its status. Only a failure to start is an error.
func Try(c Cmd) (Result, error) { return R.Run(c) }

// Output runs argv and returns its trimmed stdout, failing when it exits nonzero.
func Output(argv ...string) (string, error) {
	result, err := Run(Cmd{Argv: argv})
	return strings.TrimSpace(result.Stdout), err
}

// OutputWith runs c and returns its trimmed stdout, failing when it exits nonzero.
func OutputWith(c Cmd) (string, error) {
	result, err := Run(c)
	return strings.TrimSpace(result.Stdout), err
}

// Status runs argv and returns its exit status, ignoring output. -1 means it couldn't run.
func Status(argv ...string) int {
	result, err := R.Run(Cmd{Argv: argv})
	if err != nil {
		return -1
	}
	return result.Code
}

// Lines splits output into its non-empty lines.
func Lines(s string) []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		if line := strings.TrimRight(scanner.Text(), "\r"); strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// Has reports whether a program is on PATH.
func Has(name string) bool {
	_, err := R.LookPath(name)
	return err == nil
}

// Require fails unless every tool is on PATH.
func Require(tools ...string) error {
	for _, tool := range tools {
		if !Has(tool) {
			return fmt.Errorf("Install %s and make it available on PATH", tool)
		}
	}
	return nil
}

// Quote joins argv as one POSIX shell command line.
func Quote(argv ...string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = QuoteArg(arg)
	}
	return strings.Join(parts, " ")
}

// QuoteArg quotes one shell word, leaving safe words bare as Python's shlex does.
func QuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
