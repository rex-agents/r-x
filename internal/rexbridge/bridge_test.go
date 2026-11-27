package rexbridge

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func tree() *cobra.Command {
	root := &cobra.Command{Use: "rox"}
	up := &cobra.Command{Use: "up", Short: "start a guest", RunE: func(*cobra.Command, []string) error { return nil }}
	up.Flags().Bool("local", false, "copy the checkout")
	up.Flags().StringArray("git-key", nil, "key")
	up.Flags().Int("cpus", 0, "cpus")
	up.Flags().String("secret", "", "")
	_ = up.Flags().MarkHidden("secret")
	funnel := &cobra.Command{Use: "funnel"}
	add := &cobra.Command{Use: "add", RunE: func(*cobra.Command, []string) error { return nil }}
	funnel.AddCommand(add)
	root.AddCommand(up, funnel, &cobra.Command{Use: "hidden", Hidden: true, Run: func(*cobra.Command, []string) {}})
	return root
}

func TestActionsFollowTheCommands(t *testing.T) {
	root := tree()
	AnnotateTree(root, map[string]Spec{"up": {Args: "backend?,command*", Choices: map[string][]string{"backend": {"container"}}}, "funnel add": {Args: "name,port", Pick: "name", Mode: "text"}})
	actions := Actions([]Program{{Name: "rox", Category: "Rox", Root: root}})
	byName := map[string]Action{}
	for _, a := range actions {
		byName[a.Name] = a
	}
	up, funnel := byName["rox_up"], byName["rox_funnel_add"]
	if len(actions) != 2 || up.Name == "" || funnel.Name == "" {
		t.Fatalf("actions %+v", actions)
	}
	flags := map[string]string{}
	for _, f := range up.Flags {
		flags[f.Name] = f.Type
	}
	if flags["local"] != "boolean" || flags["git_key"] != "array" || flags["cpus"] != "integer" || flags["secret"] != "" {
		t.Errorf("flags %v", flags)
	}
	if up.Mode != "terminal" || len(up.Positional) != 2 || !up.Positional[1].Many || !up.Positional[1].Optional {
		t.Errorf("up %+v", up)
	}
	if got := paletteTitle(funnel); got != "Rox: Funnel Add…" {
		t.Errorf("title %q", got)
	}
}

func TestLuaQuotesKeywordsAndStrings(t *testing.T) {
	root := tree()
	AnnotateTree(root, map[string]Spec{"up": {Title: `Say "hi"`}})
	programs := []Program{{Name: "rox", Bin: "/opt/rox", Category: "Rox", Root: root}}
	lua := Lua(programs, Actions(programs))
	for _, want := range []string{`["local"] = "boolean?"`, `title       = "Say \"hi\"…"`, `rox = "/opt/rox"`, `local function bridge(`} {
		if !strings.Contains(lua, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(lua, "\n    local = ") {
		t.Error("a Lua keyword is a bare key")
	}
}

func TestCustomActionReplacesGenerated(t *testing.T) {
	root := &cobra.Command{Use: "rox"}
	root.AddCommand(&cobra.Command{Use: "add-remote-host", RunE: func(*cobra.Command, []string) error { return nil }})
	AnnotateTree(root, map[string]Spec{"add-remote-host": {Args: "name", Pick: "name", Custom: "add_remote_host"}})
	programs := []Program{{Name: "rox", Category: "Rox", Root: root}}
	lua := Lua(programs, Actions(programs))
	if strings.Count(lua, `name        = "rox_add_remote_host"`) != 1 || !strings.Contains(lua, `rex.client.queue, "client.host.add"`) {
		t.Errorf("custom action missing or doubled:\n%s", lua)
	}
}
