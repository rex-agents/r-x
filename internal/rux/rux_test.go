package rux

import (
	"testing"
	"time"
)

func TestNextLink(t *testing.T) {
	link := `<https://api.github.com/user/repos?page=2>; rel="next", <https://api.github.com/user/repos?page=9>; rel="last"`
	if got := nextLink(link); got != "https://api.github.com/user/repos?page=2" {
		t.Errorf("nextLink = %q", got)
	}
	if got := nextLink(`<https://x>; rel="prev"`); got != "" {
		t.Errorf("nextLink without next = %q", got)
	}
}

func TestGuestArch(t *testing.T) {
	for in, want := range map[string]string{"aarch64": "arm64", "arm64": "arm64", "x86_64\n": "amd64", "amd64": "amd64", "": "arm64"} {
		if got := guestArch(in); got != want {
			t.Errorf("guestArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSleepy(t *testing.T) {
	s := &server{sleepAfter: 15 * time.Minute, backend: "rox", tools: newToolHost(t.TempDir())}
	s.tools.runs = map[string]*toolRun{}
	idle := func(ago time.Duration, status string) *thread {
		return &thread{meta: threadMeta{ID: "t1", Backend: "rox", Guest: "rv-x-0123456789ab", Status: status, Updated: time.Now().Add(-ago)}}
	}
	if !s.sleepy(idle(20*time.Minute, "idle")) {
		t.Error("an idle thread past --sleep-after should sleep")
	}
	if s.sleepy(idle(5*time.Minute, "idle")) {
		t.Error("a recently active thread shouldn't sleep")
	}
	if s.sleepy(idle(time.Hour, "running")) {
		t.Error("a working thread shouldn't sleep")
	}
	busy := idle(time.Hour, "idle")
	s.tools.runs["r1"] = &toolRun{Thread: "t1", notify: make(chan struct{})}
	if s.sleepy(busy) {
		t.Error("a thread with an action running shouldn't sleep")
	}
	s.tools.runs["r1"].ended = true
	if !s.sleepy(busy) {
		t.Error("once its action ends, the thread can sleep")
	}
	waiting := idle(time.Hour, "idle")
	waiting.outbox = []outItem{{ID: "w1", Msg: wireMsg{T: "user", Text: "[rux] worker done"}}}
	if s.sleepy(waiting) {
		t.Error("a manager with a wake-up waiting shouldn't sleep")
	}
}

func TestSSHRepo(t *testing.T) {
	if got := sshRepo("https://github.com/up360inc/inreachxr.git"); got != "git@github.com:up360inc/inreachxr.git" {
		t.Errorf("sshRepo = %q", got)
	}
	if got := sshRepo("https://gitlab.com/a/b.git"); got != "https://gitlab.com/a/b.git" {
		t.Errorf("non-GitHub URLs stay as they are, got %q", got)
	}
	if got := repoDisplayName("git@github.com:up360inc/inreachxr.git"); got != "up360inc/inreachxr" {
		t.Errorf("repoDisplayName = %q", got)
	}
}
