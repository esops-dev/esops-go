// Package config holds the CLI wiring for the `esops config ...` command
// tree — inspection and mutation of the on-disk config file.
package config

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `config` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "Inspect and modify esops configuration",
		Commands: []*cli.Command{
			getContextsCommand(),
			useContextCommand(),
			viewCommand(),
			validateCommand(),
			cacheClearCommand(),
		},
	}
}
