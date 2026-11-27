package rox

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rex-agents/r-x/internal/sys"
	"github.com/rex-agents/r-x/internal/ui"
)

// forwardTo makes a hidden command that runs another program's command, for what moved out of rox.
func forwardTo(use, short, program string, prefix []string, missing string) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			argv := append(append([]string{program}, prefix...), args...)
			ui.Warn("rox %s moved to %s", use, program)
			ui.Hint("Running " + ui.Code(strings.Join(argv, " ")))
			if !sys.Has(program) {
				return errors.New(missing)
			}
			result, err := sys.Try(sys.Cmd{Argv: argv, Interactive: true})
			if err != nil {
				return err
			}
			if result.Code != 0 {
				return exitCode(result.Code)
			}
			return nil
		},
	}
}

// rexForwarder keeps rox rex install working: Rex integration lives in rox-rex.
func rexForwarder() *cobra.Command {
	return forwardTo("rex", "moved: rox-rex install makes every rox and rix command a Rex action", "rox-rex", nil,
		"Install rox-rex for Rex actions and hosts")
}
