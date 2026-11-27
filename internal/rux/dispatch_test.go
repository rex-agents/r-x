package rux

import (
	"reflect"
	"testing"
)

// TestMainDispatch checks which entry point each command line reaches.
func TestMainDispatch(t *testing.T) {
	var got string
	var gotArgs []string
	save := [3]any{tuiFn, harnessFn, agentCmdFn}
	defer func() {
		tuiFn, harnessFn, agentCmdFn = save[0].(func()), save[1].(func([]string)), save[2].(func([]string))
	}()
	tuiFn = func() { got, gotArgs = "tui", nil }
	harnessFn = func(a []string) { got, gotArgs = "harness", a }
	agentCmdFn = func(a []string) { got, gotArgs = "agentcmd", a }

	cases := []struct {
		args []string
		want string
		rest []string
	}{
		{nil, "tui", nil},
		{[]string{"agent"}, "harness", []string{}},
		{[]string{"agent", "fix the bug"}, "harness", []string{"fix the bug"}},
		{[]string{"agent", "--model", "codex", "go"}, "harness", []string{"--model", "codex", "go"}},
		{[]string{"agent", "spawn", "--name", "a", "do it"}, "agentcmd", []string{"spawn", "--name", "a", "do it"}},
		{[]string{"agent", "ls", "--json"}, "agentcmd", []string{"ls", "--json"}},
		{[]string{"agent", "send", "x", "hi"}, "agentcmd", []string{"send", "x", "hi"}},
		{[]string{"fix", "the", "bug"}, "harness", []string{"fix", "the", "bug"}},           // deprecated
		{[]string{"--model", "codex", "go"}, "harness", []string{"--model", "codex", "go"}}, // deprecated
	}
	for _, c := range cases {
		got, gotArgs = "", nil
		Main("test", c.args)
		if got != c.want || (c.rest != nil && !reflect.DeepEqual(gotArgs, c.rest)) {
			t.Errorf("rux %q → %s %q, want %s %q", c.args, got, gotArgs, c.want, c.rest)
		}
	}
	if route([]string{"--version"}) != routeCommand || route([]string{"help"}) != routeCommand {
		t.Error("--version and help are commands")
	}
}
