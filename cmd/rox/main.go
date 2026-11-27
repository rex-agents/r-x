// Command rox launches Rex guests and the project workspaces inside them.
package main

import (
	"os"

	"github.com/rex-agents/r-x/internal/rox"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = "development"

func main() { os.Exit(rox.Main(version, os.Args[1:])) }
