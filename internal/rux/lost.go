package rux

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A thread's guest can vanish under rux (a reset container host, rox down by hand). Such
// a thread is "lost": rox stop/start/exec say the workspace doesn't exist. The next
// message provisions a fresh guest on the same repo and branch. The old agent session
// lived in the old guest, so the new one starts clean, and its first message carries a
// recap built from the event log.

var errGuestGone = errors.New("this thread's guest is gone, and it runs in a guest rux didn't start, so rux can't replace it")

// guestGone reports whether rox output says the named workspace doesn't exist.
func guestGone(out string) bool {
	return strings.Contains(ansiRe.ReplaceAllString(out, ""), "No managed workspace")
}

const (
	recapTurns = 10
	recapBytes = 8 << 10
)

// markLostLocked records that t's guest has vanished. Hold t.mu.
func (t *thread) markLostLocked() {
	t.appendLocked("rux", 0, ruxEv("rux_guest_lost", "guest", t.meta.Guest, "text",
		"guest "+t.meta.Guest+" no longer exists; the next message starts a new guest (a fresh agent session, with a recap of this thread)"))
	t.setStatusLocked("lost")
}

// recapLocked summarises the thread so far for a fresh agent: the original prompt and
// the last few user and assistant text turns. Hold t.mu.
func (t *thread) recapLocked() string {
	var turns []string
	for _, l := range t.events {
		var se storedEvent
		if json.Unmarshal(l, &se) != nil {
			continue
		}
		var e struct {
			Type, Text string
			Message    struct{ Content json.RawMessage }
		}
		if json.Unmarshal(se.Ev, &e) != nil {
			continue
		}
		switch e.Type {
		case "rux_user":
			if strings.TrimSpace(e.Text) != "" {
				turns = append(turns, "USER: "+clipText(strings.TrimSpace(e.Text), 2000))
			}
		case "assistant":
			var parts []struct{ Type, Text string }
			json.Unmarshal(e.Message.Content, &parts)
			for _, p := range parts {
				if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
					turns = append(turns, "ASSISTANT: "+clipText(strings.TrimSpace(p.Text), 2000))
				}
			}
		}
	}
	if len(turns) > recapTurns {
		turns = turns[len(turns)-recapTurns:]
	}
	var b strings.Builder
	b.WriteString("[rux: the guest this thread ran in was lost, so this is a new session in a fresh checkout. Work that wasn't pushed is gone. Earlier in this thread:\n\nOriginal prompt:\n")
	b.WriteString(clipText(t.meta.Prompt, 2000))
	if len(turns) > 0 {
		b.WriteString("\n\nLast turns:\n\n" + strings.Join(turns, "\n\n"))
	}
	r := b.String()
	if len(r) > recapBytes {
		r = strings.ToValidUTF8(r[:recapBytes], "") + "…"
	}
	return r + "]\n\n"
}

// prefixOutboxLocked puts recap before the text of the first queued user message.
func (t *thread) prefixOutboxLocked(recap string) {
	items := append([]outItem(nil), t.outbox...)
	done := false
	for _, it := range items {
		t.ackLocked(it.ID)
	}
	for _, it := range items {
		if !done && it.Msg.T == "user" {
			it.Msg.Text = recap + it.Msg.Text
			done = true
		}
		if err := t.queueLocked(it.Msg); err != nil {
			break
		}
	}
}

// reprovision starts a new guest for a lost thread, if rux started the old one.
func (s *server) reprovision(t *thread) {
	t.mu.Lock()
	if t.meta.Status != "lost" || t.meta.SharedGuest || t.meta.Backend != "rox" {
		t.mu.Unlock()
		return
	}
	old := t.meta.Guest
	recap := t.recapLocked()
	t.meta.Guest, t.meta.Connected = "", false
	t.rseq = 0 // the new runner counts from 1; rux_reprovision marks this when the log is reloaded
	t.appendLocked("rux", 0, ruxEv("rux_reprovision", "text", fmt.Sprintf("replacing lost guest %s with a new one", old)))
	t.prefixOutboxLocked(recap)
	t.setStatusLocked("provisioning")
	t.mu.Unlock()
	go s.provisionRox(t)
}
