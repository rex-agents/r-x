package authtui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// selectedGuest is the MONITOR tab's selected guest, with the authority it's on ("" when it's
// the attached one).
func (m *Model) selectedGuest() (name, authority, thread, phase string, ok bool) {
	if m.mfocus != mcGuest {
		return
	}
	n := m.mselected(mcGuest)
	if n == nil || n.guest == nil {
		return
	}
	for _, a := range m.pack.Authorities {
		if a.Name+"/"+n.guest.Name == n.key && !a.Local {
			authority = a.Name
		}
	}
	return n.guest.Name, authority, n.guest.Thread, n.guest.Phase, true
}

func guestAsleep(phase string) bool {
	return phase == "stopped" || phase == "exited" || phase == "asleep"
}

// guestKey takes s (sleep or wake) and d (down, after a y/n) on a selected guest.
func (m *Model) guestKey(s string) tea.Cmd {
	name, auth, thread, phase, ok := m.selectedGuest()
	if !ok {
		return nil
	}
	switch s {
	case "s":
		action, done := "sleep", "sleeping "+name
		if guestAsleep(phase) {
			action, done = "wake", "waking "+name
		}
		m.flash = done + "…"
		return m.guestAct(auth, name, action, false, done)
	case "d":
		ask := &guestAsk{authority: auth, name: name, thread: thread}
		if thread != "" {
			if t := m.threadByID(thread); t != nil {
				ask.working = isWorking(t.Status)
			}
		}
		m.downAsk = ask
	}
	return nil
}

// downKey answers the down prompt: y or enter downs the guest, anything else cancels.
func (m *Model) downKey(s string) tea.Cmd {
	ask := m.downAsk
	m.downAsk = nil
	if s != "y" && s != "Y" {
		m.flash = "cancelled"
		return nil
	}
	m.flash = "downing " + ask.name + "…"
	return m.guestAct(ask.authority, ask.name, "down", ask.working, "downed "+ask.name)
}

func (m *Model) guestAct(authority, name, action string, force bool, done string) tea.Cmd {
	return func() tea.Msg {
		return guestActMsg{done, m.c.GuestAction(authority, name, action, force)}
	}
}

// askText is the footer's question.
func (a *guestAsk) text() string {
	s := fmt.Sprintf("down %s", a.name)
	if a.thread != "" {
		s += " (thread " + a.thread + ")"
	}
	if a.working {
		s += ", which is working right now"
	}
	return s + "? y/n"
}
