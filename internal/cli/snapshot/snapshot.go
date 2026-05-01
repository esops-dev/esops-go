// Package snapshot holds the CLI wiring for the `esops snapshot ...`
// command tree. Command actions stay thin: parse flags, load config,
// build a client, call internal/snapshot, render output.
package snapshot

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `snapshot` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "snapshot",
		Usage: "Inspect, create, verify, restore and prune snapshots and repositories",
		Commands: []*cli.Command{
			listCommand(),
			createCommand(),
			verifyCommand(),
			restoreCommand(),
			pruneCommand(),
			registerCommand(),
			deregisterCommand(),
		},
	}
}
