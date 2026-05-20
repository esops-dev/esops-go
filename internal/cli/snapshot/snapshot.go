// Package snapshot holds the CLI wiring for the `esops snapshot ...`
// command tree. Command actions stay thin: parse flags, load config,
// build a client, call internal/snapshot, render output.
package snapshot

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `snapshot` command group.
//
// Repository management lives under `snapshot repository <register|
// remove|list>`; the historical flat `register` / `deregister` verbs
// have been removed in favour of that nested form so the top-level
// `snapshot` tree stays focused on snapshots themselves.
// `snapshot policy` is the parallel home for SLM (Elasticsearch) and
// SM (OpenSearch) policy management — same operator question, two
// different cluster surfaces underneath, one CLI surface above.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "snapshot",
		Usage: "Inspect, create, verify, restore and prune snapshots, repositories, and policies",
		Commands: []*cli.Command{
			listCommand(),
			describeCommand(),
			createCommand(),
			verifyCommand(),
			restoreCommand(),
			pruneCommand(),
			repositoryCommand(),
			policyCommand(),
		},
	}
}
