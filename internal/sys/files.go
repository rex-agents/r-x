package sys

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// XDG returns an XDG base directory, or fallback under the home directory.
func XDG(variable, fallback string) string {
	dir := os.Getenv(variable)
	if dir == "" {
		dir = filepath.Join(Home(), fallback)
	}
	return Resolve(dir)
}

// Home is the user's home directory.
func Home() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

// Expand replaces a leading ~ with the home directory and makes the path absolute.
func Expand(path string) string {
	if path == "~" {
		path = Home()
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(Home(), path[2:])
	}
	return Resolve(path)
}

// Resolve makes a path absolute and follows symlinks where it exists, as Python's resolve does.
func Resolve(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// Resolve the deepest parent that exists, so /tmp/x on macOS becomes /private/tmp/x.
	dir, base := filepath.Split(abs)
	if dir != "/" && dir != abs {
		if real, err := filepath.EvalSymlinks(strings.TrimRight(dir, "/")); err == nil {
			return filepath.Join(real, base)
		}
	}
	return abs
}

// PrivateDir makes dir, owned by this user and closed to others, and fails if it's anything else.
func PrivateDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return dir, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return dir, err
	}
	st, _ := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 || (st != nil && int(st.Uid) != os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return dir, fmt.Errorf("Expected a private directory owned by the current user at %s", dir)
	}
	return dir, nil
}

// WriteAtomic replaces path with data through a temporary file beside it.
// A new file gets mode; an existing one keeps its mode unless private is set.
func WriteAtomic(path string, data []byte, mode os.FileMode, keepMode bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	pending := f.Name()
	defer os.Remove(pending)
	if keepMode {
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(pending, path)
}

// WritePrivate replaces path with text only this user may read.
func WritePrivate(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return WriteAtomic(path, []byte(text), 0o600, false)
}

// WriteJSON writes v as indented JSON with a trailing newline, keeping an existing file's mode.
func WriteJSON(path string, v any) error {
	data, err := MarshalIndent(v)
	if err != nil {
		return err
	}
	return WriteAtomic(path, data, 0o644, true)
}

// MarshalIndent is JSON with two-space indents and no HTML escaping, as Python's json.dumps writes.
func MarshalIndent(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// ReadJSONObject reads a JSON object file. A missing file is an empty object.
func ReadJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s isn't valid JSON: %w", path, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Expected a JSON object in %s", path)
	}
	return obj, nil
}

// IsFile reports whether path is a regular file, following links.
func IsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// IsDir reports whether path is a directory, following links.
func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// IsSocket reports whether path is a Unix socket.
func IsSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

// ConfiguredFile expands a configured path and checks it's a file. "" stays "".
func ConfiguredFile(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	path := Expand(value)
	if !IsFile(path) {
		return "", fmt.Errorf("File does not exist at %s", path)
	}
	return path, nil
}

// ReadTrim reads a file and trims surrounding space.
func ReadTrim(path string) (string, error) {
	data, err := os.ReadFile(path)
	return strings.TrimSpace(string(data)), err
}

// Lock is an exclusive flock on a file.
type Lock struct{ f *os.File }

// ErrBusy means another process holds the lock.
var ErrBusy = errors.New("busy")

// TryLock takes the lock at path, or returns ErrBusy at once.
func TryLock(path string) (*Lock, error) { return lock(path, false) }

// WaitLock takes the lock at path, waiting for it.
func WaitLock(path string) (*Lock, error) { return lock(path, true) }

func lock(path string, wait bool) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Unlock releases the lock.
func (l *Lock) Unlock() {
	if l != nil && l.f != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
		l.f = nil
	}
}

// WaitFor calls probe every second until it reports done, an error that isn't retryable,
// or the timeout passes. Errors from probe are retried and the last one is reported.
func WaitFor[T any](timeout time.Duration, description string, probe func() (T, bool, error)) (T, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		value, done, err := probe()
		if err != nil {
			var fatal *Fatal
			if errors.As(err, &fatal) {
				return value, fatal.Err
			}
			last = err
		} else if done {
			return value, nil
		}
		if time.Now().After(deadline) {
			var zero T
			if last != nil {
				return zero, fmt.Errorf("Timed out waiting for %s.\n%s", description, last)
			}
			return zero, fmt.Errorf("Timed out waiting for %s.", description)
		}
		time.Sleep(time.Second)
	}
}

// Fatal wraps an error that WaitFor should not retry.
type Fatal struct{ Err error }

func (f *Fatal) Error() string { return f.Err.Error() }

// FreePort is a loopback TCP port nothing listens on right now.
func FreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// PortOpen reports whether something listens on a loopback port.
func PortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// PidAlive reports whether the process in a pid file still runs.
func PidAlive(path string) bool {
	pid, err := readPid(path)
	if err != nil {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// KillPid stops the process in a pid file and removes the file.
func KillPid(path string) {
	if pid, err := readPid(path); err == nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	_ = os.Remove(path)
}

// WritePid records a process in a pid file.
func WritePid(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func readPid(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}
