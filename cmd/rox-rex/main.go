// Command rox-rex is rox's Rex hook provider: it installs and starts Rex in guests, creates
// their sessions, registers them as Rex hosts and opens them in the Rex app.
package main

import (
	"os"

	"github.com/rex-agents/r-x/internal/roxrex"
)

// version is set at release time with -ldflags "-X main.version=v1.2.3".
var version = "development"

func main() { os.Exit(roxrex.Main(version, os.Args[1:])) }
