package rux

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skills/rux
var skillFS embed.FS

// rux skill install [--agent claude|codex]... [--dir DIR] [--force]
// rux skill show
//
// Installs the rux skill, which makes a Claude Code or Codex session the
// orchestrator for `rux agent` sub-agents.
func runSkill(args []string) {
	const usage = "usage: rux skill install [--agent claude|codex] [--dir SKILLS_DIR] [--force]\n       rux skill show"
	if len(args) == 0 {
		usageExit(usage)
	}
	switch args[0] {
	case "show":
		b, _ := skillFS.ReadFile("skills/rux/SKILL.md")
		os.Stdout.Write(b)
	case "install":
		skillInstall(args[1:])
	default:
		usageExit(usage)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func skillInstall(args []string) {
	fset := flag.NewFlagSet("rux skill install", flag.ExitOnError)
	var agents multiFlag
	fset.Var(&agents, "agent", "claude or codex (repeatable; default: each one set up on this machine)")
	dir := fset.String("dir", "", "install into this skills folder instead")
	force := fset.Bool("force", false, "replace an existing rux folder that isn't this skill")
	fset.Parse(args)
	home, _ := os.UserHomeDir()
	agentHome := map[string]string{"claude": filepath.Join(home, ".claude"), "codex": filepath.Join(home, ".codex")}
	label := map[string]string{"claude": "Claude Code", "codex": "Codex"}
	type target struct{ agent, skills string }
	var targets []target
	if *dir != "" {
		targets = append(targets, target{"", *dir})
	} else {
		if len(agents) == 0 {
			for _, a := range []string{"claude", "codex"} {
				if st, err := os.Stat(agentHome[a]); err == nil && st.IsDir() {
					agents = append(agents, a)
				}
			}
		}
		if len(agents) == 0 {
			fail("rux skill install: found no ~/.claude or ~/.codex; choose with --agent claude, --agent codex, or --dir DIR")
		}
		for _, a := range agents {
			if agentHome[a] == "" {
				fail("rux skill install: unknown agent %q (claude or codex)", a)
			}
			targets = append(targets, target{a, filepath.Join(agentHome[a], "skills")})
		}
	}
	for _, t := range targets {
		dest := filepath.Join(t.skills, "rux")
		if b, err := os.ReadFile(filepath.Join(dest, "SKILL.md")); err == nil || fileExists(dest) {
			if !strings.Contains(string(b), "name: rux") && !*force {
				fail("rux skill install: %s exists and isn't the rux skill; pass --force to replace it", dest)
			}
		}
		if err := os.MkdirAll(t.skills, 0o755); err != nil {
			fail("rux skill install: %v", err)
		}
		staging, err := os.MkdirTemp(t.skills, ".rux-skill-")
		if err != nil {
			fail("rux skill install: %v", err)
		}
		sub, _ := fs.Sub(skillFS, "skills/rux")
		if err := os.CopyFS(staging, sub); err != nil {
			os.RemoveAll(staging)
			fail("rux skill install: %v", err)
		}
		os.RemoveAll(dest)
		if err := os.Rename(staging, dest); err != nil {
			os.RemoveAll(staging)
			fail("rux skill install: %v", err)
		}
		os.Chmod(dest, 0o755)
		if t.agent != "" {
			fmt.Printf("Installed the rux skill for %s at %s\n", label[t.agent], dest)
		} else {
			fmt.Printf("Installed the rux skill at %s\n", dest)
		}
	}
	fmt.Println("Start a new agent session to load it.")
}
