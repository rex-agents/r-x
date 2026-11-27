package rux

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

func TestParseKittyKey(t *testing.T) {
	cases := []struct {
		seq  string
		want tea.Msg
		str  string
	}{
		{"\x1b[13;2u", shiftEnterMsg{}, ""},
		{"\x1b[13u", tea.KeyMsg{Type: tea.KeyEnter}, "enter"},
		{"\x1b[13;3u", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, "alt+enter"},
		{"\x1b[13;5u", tea.KeyMsg{Type: tea.KeyEnter}, "enter"},
		{"\x1b[27u", tea.KeyMsg{Type: tea.KeyEsc}, "esc"},
		{"\x1b[99;5u", tea.KeyMsg{Type: tea.KeyCtrlC}, "ctrl+c"},
		{"\x1b[106;5u", tea.KeyMsg{Type: tea.KeyCtrlJ}, "ctrl+j"},
		{"\x1b[100;5:1u", tea.KeyMsg{Type: tea.KeyCtrlD}, "ctrl+d"},
		{"\x1b[107;69u", tea.KeyMsg{Type: tea.KeyCtrlK}, "ctrl+k"}, // caps lock ignored
		{"\x1b[98;3u", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}, Alt: true}, "alt+b"},
		{"\x1b[98;4u", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}, Alt: true}, "alt+B"},
		{"\x1b[9;2u", tea.KeyMsg{Type: tea.KeyShiftTab}, "shift+tab"},
		{"\x1b[127;3u", tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}, "alt+backspace"},
		{"\x1b[99;9u", nil, ""}, // super+c
		{"\x1b[?u", nil, ""},
	}
	for _, c := range cases {
		got := parseKittyKey([]byte(c.seq))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %#v, want %#v", c.seq, got, c.want)
		}
		if k, ok := got.(tea.KeyMsg); ok && k.String() != c.str {
			t.Errorf("%q: String() = %q, want %q", c.seq, k.String(), c.str)
		}
	}
}

func TestKittyFilterPassesThrough(t *testing.T) {
	var k kittyKeys
	t.Setenv("TERM", "dumb")
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	if got := k.filter(nil, msg); !reflect.DeepEqual(got, msg) {
		t.Fatalf("got %#v", got)
	}
	if _, ok := csiBytes(msg); ok {
		t.Fatal("KeyMsg is not a CSI sequence")
	}
}

type keyLog struct{ got []string }

func (m *keyLog) Init() tea.Cmd { return nil }
func (m *keyLog) View() string  { return "" }
func (m *keyLog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case shiftEnterMsg:
		m.got = append(m.got, "shift+enter")
	case tea.KeyMsg:
		m.got = append(m.got, msg.String())
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// Real bytes through bubbletea's input reader and our filter.
func TestKittyThroughProgram(t *testing.T) {
	t.Setenv("TERM", "dumb") // don't write the enable sequence into test output
	var k kittyKeys
	m := &keyLog{}
	in := "\x1b[13;2u" + "\r" + "\x1b[13;3u" + "\x1b[27u" + "\x1b[99;5u"
	_, run := k.run(m, tea.WithInput(strings.NewReader(in)), tea.WithOutput(io.Discard))
	if _, err := run(); err != nil {
		t.Fatal(err)
	}
	want := []string{"shift+enter", "enter", "alt+enter", "esc", "ctrl+c"}
	if !reflect.DeepEqual(m.got, want) {
		t.Fatalf("got %v, want %v", m.got, want)
	}
}

func TestInsertStringNewline(t *testing.T) {
	ta := textarea.New()
	ta.Focus()
	ta.InsertString("a\nb")
	if ta.Value() != "a\nb" || ta.LineCount() != 2 {
		t.Fatalf("value %q lines %d", ta.Value(), ta.LineCount())
	}
}
