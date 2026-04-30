// Package index holds the CLI wiring for the `esops index ...` command
// tree. Command actions are thin: parse flags, load config, build a
// client, call internal/index, render output.
package index

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `index` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "index",
		Usage: "Inspect and manage indices",
		Commands: []*cli.Command{
			listCommand(),
			openCommand(),
			closeCommand(),
		},
	}
}
