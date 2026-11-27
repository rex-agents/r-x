// Command rix keeps the Git keys, agent credentials and agent setup that rox guests use.
package main

import (
	"os"

	"github.com/rex-agents/r-x/internal/rix"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = "development"

func main() { os.Exit(rix.Main(version, os.Args[1:])) }
