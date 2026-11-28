package rux

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
)

// A thread's outbox holds the user messages (the first prompt, what the user types, and
// wake-ups for managers) that the server has accepted but its runner hasn't acked yet.
// It's kept on disk in outbox.jsonl as a log of add and ack records, so a message survives a
// disconnect, a sleeping guest and a server restart. Every connection resends whatever is
// unacked; runners ignore an id they've already taken, so a resend never repeats a turn.

type outItem struct {
	ID  string  `json:"id"`
	Msg wireMsg `json:"msg"`
}

type outRec struct {
	Op  string   `json:"op"` // "add" or "ack"
	ID  string   `json:"id"`
	Msg *wireMsg `json:"msg,omitempty"`
}

func (t *thread) outboxPath() string { return filepath.Join(t.dir, "outbox.jsonl") }

func (t *thread) writeRecLocked(rec outRec) error {
	if t.dir == "" {
		return nil
	}
	if t.outLog == nil {
		f, err := os.OpenFile(t.outboxPath(), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		t.outLog = f
		// A crash can leave a torn last line with no newline; start this record on its own line.
		if st, err := f.Stat(); err == nil && st.Size() > 0 {
			last := make([]byte, 1)
			if _, err := f.ReadAt(last, st.Size()-1); err == nil && last[0] != '\n' {
				f.Write([]byte{'\n'})
			}
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := t.outLog.Write(append(b, '\n')); err != nil {
		return err
	}
	return t.outLog.Sync()
}

// queueLocked stores m until the runner acks it, and wakes the connection that sends it.
// A message with an id that's already queued is not added twice. Hold t.mu.
func (t *thread) queueLocked(m wireMsg) error {
	if m.ID == "" {
		m.ID = newUUID()
	}
	for _, it := range t.outbox {
		if it.ID == m.ID {
			return nil
		}
	}
	if err := t.writeRecLocked(outRec{Op: "add", ID: m.ID, Msg: &m}); err != nil {
		return err
	}
	t.outbox = append(t.outbox, outItem{ID: m.ID, Msg: m})
	t.kickLocked()
	return nil
}

func (t *thread) kickLocked() {
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

// ackLocked drops a delivered message. Hold t.mu.
func (t *thread) ackLocked(id string) {
	for i, it := range t.outbox {
		if it.ID != id {
			continue
		}
		t.outbox = append(t.outbox[:i:i], t.outbox[i+1:]...)
		if err := t.writeRecLocked(outRec{Op: "ack", ID: id}); err != nil {
			log.Printf("thread %s: saving ack: %v", t.meta.ID, err) // a resend is harmless
		}
		return
	}
}

// loadOutbox reads outbox.jsonl back, and rewrites it without finished messages.
func (t *thread) loadOutbox() error {
	f, err := os.Open(t.outboxPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), wsReadLimit)
	lines, finished := 0, false
	for sc.Scan() {
		var rec outRec
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue // a torn last line from a crash
		}
		lines++
		switch rec.Op {
		case "add":
			if rec.Msg != nil {
				t.outbox = append(t.outbox, outItem{ID: rec.ID, Msg: *rec.Msg})
			}
		case "ack":
			for i, it := range t.outbox {
				if it.ID == rec.ID {
					t.outbox = append(t.outbox[:i:i], t.outbox[i+1:]...)
					break
				}
			}
			finished = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if finished && lines > len(t.outbox) {
		tmp := t.outboxPath() + ".tmp"
		w, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil
		}
		for _, it := range t.outbox {
			b, _ := json.Marshal(outRec{Op: "add", ID: it.ID, Msg: &it.Msg})
			w.Write(append(b, '\n'))
		}
		if w.Sync() == nil && w.Close() == nil {
			os.Rename(tmp, t.outboxPath())
		} else {
			os.Remove(tmp)
		}
	}
	return nil
}

var errNoRunner = errors.New("the thread's runner isn't connected")

// enqueueUser accepts a user message for t's runner. It stays queued until the runner acks
// it, whether the runner is connected, reconnecting or asleep. A thread that's neither
// connected nor asleep (still starting, failed) refuses it with errNoRunner, unless
// anyState is set, as it is for manager wake-ups. waking reports that t's guest is being
// started for it.
func (s *server) enqueueUser(t *thread, m wireMsg, anyState bool) (waking bool, err error) {
	t.mu.Lock()
	lost := t.meta.Status == "lost"
	if lost && t.meta.SharedGuest {
		t.mu.Unlock()
		return false, errGuestGone
	}
	asleep := t.send == nil && asleepStatus(t.meta.Status)
	if t.send == nil && !asleep && !lost && !anyState {
		t.mu.Unlock()
		return false, errNoRunner
	}
	err = t.queueLocked(m)
	t.mu.Unlock()
	if err != nil {
		return false, err
	}
	if lost {
		s.reprovision(t)
		return true, nil
	}
	if asleep || anyState {
		waking = s.wakeIfAsleep(t)
	}
	return waking, nil
}

// queuePromptLocked queues the thread's first prompt, once. It has a fixed id, so a crash
// between queueing and saving the flag can't make it run twice. Hold t.mu.
func (t *thread) queuePromptLocked() {
	if t.meta.PromptSent {
		return
	}
	if err := t.queueLocked(wireMsg{T: "user", ID: "prompt", Text: t.meta.Prompt}); err != nil {
		log.Printf("thread %s: queueing the prompt: %v", t.meta.ID, err)
		return
	}
	t.meta.PromptSent = true
	t.saveMeta()
}
