package rexbridge

import (
	"strings"

	"github.com/spf13/cobra"
)

var backends = []string{"container", "linux", "macos", "fly", "railway"}

// RoxSpecs say how each rox command becomes an action, by command path.
var RoxSpecs = map[string]Spec{
	"up":                {Args: "backend?,command*", Choices: map[string][]string{"backend": backends}, Title: "Rox: Start a New Guest"},
	"add":               {Args: "name,command*", Pick: "name", Title: "Rox: Add This Project to a Guest"},
	"ls":                {Mode: "json", Title: "Rox: List Guests"},
	"reconnect":         {Args: "name", Pick: "name", Title: "Rox: Reconnect to a Guest"},
	"start":             {Args: "name", Pick: "name", Title: "Rox: Start a Stopped Guest"},
	"stop":              {Args: "name", Pick: "name", Title: "Rox: Stop a Guest"},
	"down":              {Args: "name?", Pick: "name", Title: "Rox: Remove a Guest"},
	"shell":             {Args: "name,project?", Pick: "name", Title: "Rox: Open a Shell in a Guest"},
	"forward":           {Args: "name,ports+", Pick: "name", Title: "Rox: Forward Ports to a Guest"},
	"cp":                {Args: "name,copies+", Pick: "name", Title: "Rox: Copy Files into a Guest"},
	"funnel add":        {Args: "name,port", Pick: "name", Title: "Rox: Put a Guest Port on the Internet"},
	"funnel remove":     {Args: "name,port?", Pick: "name", Title: "Rox: Remove a Funnel"},
	"funnel ls":         {Args: "name?", Mode: "text", Title: "Rox: List Funnels"},
	"open":              {Args: "name,project?", Pick: "name", Title: "Rox: Open a Guest in the Rex App"},
	"add-remote-host":   {Args: "name", Pick: "name", Custom: "add_remote_host"},
	"template":          {Args: "backend", Choices: map[string][]string{"backend": {"macos", "linux"}}, Title: "Rox: Prepare a Tart Template"},
	"config":            {Title: "Rox: Edit the Config"},
	"config tailscale":  {Title: "Rox: Put Guests on the Tailnet"},
	"service install":   {Mode: "text", Title: "Rox: Install the Service"},
	"service uninstall": {Mode: "text", Title: "Rox: Uninstall the Service"},
	"service status":    {Mode: "text", Title: "Rox: Service Status"},
	"service run":       {Skip: true},
	"skill install":     {Mode: "text", Title: "Rox: Install the Agent Skill"},
	"self-update":       {Title: "Rox: Update"},
	"exec":              {Args: "name,command+", Pick: "name", Mode: "text", Title: "Rox: Run a Command in a Guest"},
	"inspect":           {Args: "name", Pick: "name", Mode: "json", Title: "Rox: Inspect a Guest"},
	"hooks":             {Mode: "text", Title: "Rox: Show the Hook Provider"},
	// The bridge's own commands would only rewrite the module from inside it.
	"rex":           {Skip: true},
	"rex lua":       {Skip: true},
	"rex install":   {Skip: true},
	"rex uninstall": {Skip: true},
}

// RixSpecs say how each rix command becomes an action.
var RixSpecs = map[string]Spec{
	"key add":           {Args: "path", Mode: "text", Title: "Rix: Add a Git SSH Key"},
	"key remove":        {Args: "path", Mode: "text", Title: "Rix: Remove a Git SSH Key"},
	"key ls":            {Mode: "text", Title: "Rix: List Git SSH Keys"},
	"known-hosts set":   {Args: "path", Mode: "text", Title: "Rix: Pin Git Server Keys"},
	"known-hosts clear": {Mode: "text", Title: "Rix: Unpin Git Server Keys"},
	// rix auth asks for the secret in its terminal, so it never passes through Lua.
	"auth":            {Args: "agent", Choices: map[string][]string{"agent": {"claude", "codex"}}, Title: "Rix: Sign an Agent In"},
	"sync set":        {Args: "agent,items", Choices: map[string][]string{"agent": {"claude", "codex"}}, Mode: "text", Title: "Rix: Choose What Guests Get"},
	"sync show":       {Mode: "text", Title: "Rix: Show What Guests Get"},
	"apply":           {Args: "name?", Pick: "name", Title: "Rix: Apply the Vault to a Guest"},
	"status":          {Args: "name?", Mode: "json", Title: "Rix: Vault Status"},
	"git-agent start": {Args: "name", Pick: "name", Title: "Rix: Start a Guest's Git Agent"},
	"git-agent stop":  {Args: "name", Pick: "name", Mode: "text", Title: "Rix: Stop a Guest's Git Agent"},
	"git-agent sock":  {Args: "name", Pick: "name", Mode: "text", Title: "Rix: Show a Guest's Git Agent Socket"},
	"config":          {Title: "Rix: Edit the Config"},
	"migrate":         {Title: "Rix: Migrate from Python rox"},
	"self-update":     {Title: "Rix: Update"},
}

// AnnotateTree applies specs to root's commands by their path below root.
func AnnotateTree(root *cobra.Command, specs map[string]Spec) {
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		for _, sub := range c.Commands() {
			p := append(append([]string{}, path...), sub.Name())
			if s, ok := specs[strings.Join(p, " ")]; ok {
				Annotate(sub, s)
			}
			walk(sub, p)
		}
	}
	walk(root, nil)
}

// Missing lists runnable, visible commands that have no spec, so a new command can't be
// bridged by accident with the wrong arguments.
func Missing(root *cobra.Command, specs map[string]Spec) []string {
	var out []string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			p := append(append([]string{}, path...), sub.Name())
			key := strings.Join(p, " ")
			if _, ok := specs[key]; !ok && sub.Runnable() {
				out = append(out, key)
			}
			walk(sub, p)
		}
	}
	walk(root, nil)
	return out
}
