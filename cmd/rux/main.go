// rux runs Claude Code agents in Rox guests, driven from a web app or from Rex panes.
package main

import (
	"os"

	"github.com/rex-agents/r-x/internal/rux"
)

var version = "development"

func main() { os.Exit(rux.Main(version, os.Args[1:])) }
