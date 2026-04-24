// Package ops holds the CLI wiring for the `esops ops ...` command
// tree. Command actions are thin: parse flags, load config, build a
// client, call internal/ops, render output.
package ops

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `ops` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "ops",
		Usage: "Day-to-day cluster operations",
		Commands: []*cli.Command{
			healthCommand(),
			shardsCommand(),
			allocationExplainCommand(),
			auditCommand(),
		},
	}
}
