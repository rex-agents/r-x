package rux

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// Shift+Enter and Enter both send "\r" in legacy terminal encoding, and
// bubbletea v1 can't tell them apart. Terminals that speak the kitty keyboard
// protocol can: we ask for its "disambiguate" flag (CSI > 1 u), and they then
// report Shift+Enter as "\x1b[13;2u". Bubbletea v1 doesn't parse CSI u, so it
// hands those over as an unexported unknownCSISequenceMsg, which kittyKeys.filter
// turns back into ordinary tea.KeyMsg values (or shiftEnterMsg).
// Terminals without the protocol ignore the request, and keep alt+enter and
// ctrl+j as the way to insert a newline.

const (
	kittyPush = "\x1b[>1u"
	kittyPop  = "\x1b[<u"
)

// shiftEnterMsg is sent when Shift+Enter is pressed: insert a newline.
type shiftEnterMsg struct{}

// kittyKeys turns kitty keyboard protocol on and off for one program.
type kittyKeys struct {
	mu     sync.Mutex
	active bool
}

func (k *kittyKeys) enable() {
	if t := os.Getenv("TERM"); t == "" || t == "dumb" || t == "linux" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.active {
		k.active = true
		os.Stdout.WriteString(kittyPush)
	}
}

// disable is safe to call more than once.
func (k *kittyKeys) disable() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.active {
		k.active = false
		os.Stdout.WriteString(kittyPop)
	}
}

// filter is a tea.WithFilter function. The first message arrives once the
// alt screen is up (it has its own flag stack, so enabling earlier is lost).
func (k *kittyKeys) filter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.QuitMsg, tea.InterruptMsg:
		k.disable()
		return msg
	case tea.SuspendMsg:
		k.disable()
		return msg
	case tea.ResumeMsg:
		k.enable()
		return msg
	}
	k.enable()
	if seq, ok := csiBytes(msg); ok {
		return parseKittyKey(seq)
	}
	return msg
}

// run builds a program with kitty key reporting. The returned run function
// restores the terminal when the program ends, including on a panic.
func (k *kittyKeys) run(m tea.Model, opts ...tea.ProgramOption) (*tea.Program, func() (tea.Model, error)) {
	p := tea.NewProgram(m, append(opts, tea.WithFilter(k.filter))...)
	return p, func() (tea.Model, error) {
		defer k.disable()
		return p.Run()
	}
}

// csiBytes returns the bytes of bubbletea's unknownCSISequenceMsg.
func csiBytes(msg tea.Msg) ([]byte, bool) {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Uint8 ||
		v.Type().Name() != "unknownCSISequenceMsg" {
		return nil, false
	}
	return v.Bytes(), true
}

// parseKittyKey translates a CSI u key report ("\x1b[" code[:alt] [;mods[:event]] "u")
// into a tea.KeyMsg, shiftEnterMsg, or nil when it isn't one we can use.
func parseKittyKey(seq []byte) tea.Msg {
	s := string(seq)
	if !strings.HasPrefix(s, "\x1b[") || !strings.HasSuffix(s, "u") {
		return nil
	}
	parts := strings.Split(s[2:len(s)-1], ";")
	if len(parts) > 3 {
		return nil
	}
	code, err := strconv.Atoi(strings.Split(parts[0], ":")[0])
	if err != nil {
		return nil
	}
	mods := 0
	if len(parts) > 1 && parts[1] != "" {
		n, err := strconv.Atoi(strings.Split(parts[1], ":")[0])
		if err != nil || n < 1 {
			return nil
		}
		mods = (n - 1) &^ (64 | 128) // ignore caps lock and num lock
	}
	const (
		shift = 1
		alt   = 2
		ctrl  = 4
	)
	if mods&^(shift|alt|ctrl) != 0 || code >= 57344 { // super, hyper, meta, keypad...
		return nil
	}
	isAlt := mods&alt != 0
	switch {
	case code == 13 && mods&ctrl != 0:
		return tea.KeyMsg{Type: tea.KeyEnter, Alt: isAlt} // what a legacy terminal sends
	case code == 13 && mods&shift != 0 && !isAlt:
		return shiftEnterMsg{}
	case code == 13:
		return tea.KeyMsg{Type: tea.KeyEnter, Alt: isAlt}
	case code == 27:
		return tea.KeyMsg{Type: tea.KeyEsc, Alt: isAlt}
	case code == 9:
		if mods&shift != 0 {
			return tea.KeyMsg{Type: tea.KeyShiftTab, Alt: isAlt}
		}
		return tea.KeyMsg{Type: tea.KeyTab, Alt: isAlt}
	case code == 127 || code == 8:
		return tea.KeyMsg{Type: tea.KeyBackspace, Alt: isAlt}
	}
	if mods&ctrl != 0 {
		var b rune
		switch {
		case code >= 'a' && code <= 'z':
			b = rune(code) - 'a' + 1
		case code >= 'A' && code <= 'Z':
			b = rune(code) - 'A' + 1
		case code == ' ' || code == '@':
			b = 0
		case code >= '[' && code <= '_':
			b = rune(code) - '@'
		default:
			return nil
		}
		return tea.KeyMsg{Type: tea.KeyType(b), Alt: isAlt}
	}
	r := rune(code)
	if code == 0 || r < ' ' {
		return nil
	}
	if mods&shift != 0 && r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: isAlt}
}
