package rux

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The runner keeps the user messages it has accepted in stateDir/inbox, one file each, until
// claude has been given them. A message the server sends again (it resends every message
// the runner hasn't acked on each connection) is recognised by its id and not run twice:
// ids of delivered messages are in the rux_user events, which are on disk too.

var msgIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type inboxMsg struct {
	msg  wireMsg
	file string // "" for a message from a server that sends no ids
}

func (r *runner) inboxDir() string { return filepath.Join(r.stateDir, "inbox") }

// loadInbox restores the messages accepted before a restart, and the ids already delivered.
func (r *runner) loadInbox() {
	r.inMu.Lock()
	defer r.inMu.Unlock()
	r.seen, r.queued = map[string]bool{}, map[string]bool{}
	for _, l := range r.lines {
		if !strings.Contains(string(l), `"rux_user"`) {
			continue
		}
		var w struct {
			Ev struct{ Type, ID string }
		}
		if json.Unmarshal(l, &w) == nil && w.Ev.Type == "rux_user" && w.Ev.ID != "" {
			r.seen[w.Ev.ID] = true
		}
	}
	ents, _ := os.ReadDir(r.inboxDir())
	var names []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(r.inboxDir(), n)
		b, err := os.ReadFile(p)
		var m wireMsg
		if err != nil || json.Unmarshal(b, &m) != nil || !msgIDRe.MatchString(m.ID) {
			log.Printf("runner: skipping unreadable inbox file %s", n)
			continue
		}
		if r.seen[m.ID] || r.queued[m.ID] {
			os.Remove(p) // already run, and the crash came before the cleanup
			continue
		}
		r.queued[m.ID] = true
		r.inQ = append(r.inQ, inboxMsg{msg: m, file: p})
	}
}

// accept stores a message from the server, and reports whether it's safe to ack: it's on
// disk, or it was taken before.
func (r *runner) accept(m wireMsg) bool {
	r.inMu.Lock()
	defer r.inMu.Unlock()
	im := inboxMsg{msg: m}
	if msgIDRe.MatchString(m.ID) {
		if r.seen[m.ID] || r.queued[m.ID] {
			r.nudgeRetry()
			return true
		}
		if err := os.MkdirAll(r.inboxDir(), 0o700); err != nil {
			log.Printf("runner: inbox: %v", err)
			return false
		}
		r.inSeq = max(r.inSeq+1, time.Now().UnixNano())
		im.file = filepath.Join(r.inboxDir(), fmt.Sprintf("%020d-%s.json", r.inSeq, m.ID))
		if err := writeFileSync(im.file, m); err != nil {
			log.Printf("runner: storing message %s: %v", m.ID, err)
			return false // not acked, so the server sends it again
		}
		r.queued[m.ID] = true
	}
	r.inQ = append(r.inQ, im)
	select {
	case r.inNotify <- struct{}{}:
	default:
	}
	r.nudgeRetry()
	return im.file != ""
}

// nudgeRetry makes a failed workspace setup run again; a message is a reason to try.
func (r *runner) nudgeRetry() {
	select {
	case r.retry <- struct{}{}:
	default:
	}
}

func writeFileSync(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir makes a new or renamed file's directory entry durable, where the filesystem allows.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// head blocks until a message is waiting, and returns it without removing it.
func (r *runner) head() inboxMsg {
	for {
		r.inMu.Lock()
		if len(r.inQ) > 0 {
			m := r.inQ[0]
			r.inMu.Unlock()
			return m
		}
		r.inMu.Unlock()
		<-r.inNotify
	}
}

// finish drops the message at the head once claude has it.
func (r *runner) finish(im inboxMsg) {
	r.inMu.Lock()
	defer r.inMu.Unlock()
	if len(r.inQ) > 0 {
		r.inQ = r.inQ[1:]
	}
	if im.msg.ID != "" {
		r.seen[im.msg.ID] = true
		delete(r.queued, im.msg.ID)
	}
	if im.file != "" {
		os.Remove(im.file)
	}
}

// deliverLoop hands accepted messages to claude in order, once the workspace is ready.
func (r *runner) deliverLoop() {
	backoff := r.retryBase()
	for {
		im := r.head()
		for !r.waitReady() {
		}
		if err := r.sendUser(im.msg.ID, im.msg.Text, im.msg.Files...); err != nil {
			// Keep it, at the head so order holds, and try again later without spinning.
			r.emit(ruxEv("rux_error", "text", "delivering a message: "+err.Error()+"; will retry"))
			time.Sleep(backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = r.retryBase()
		r.finish(im)
	}
}

func (r *runner) retryBase() time.Duration {
	if r.retryDelay > 0 {
		return r.retryDelay
	}
	return 2 * time.Second
}

// waitReady blocks until the workspace is ready, trying the setup again whenever a message
// arrives after a failure. It reports whether the workspace is ready.
func (r *runner) waitReady() bool {
	select {
	case <-r.ready:
		return true
	default:
	}
	select {
	case <-r.ready:
		return true
	case <-r.retry:
		return r.prepare()
	}
}
