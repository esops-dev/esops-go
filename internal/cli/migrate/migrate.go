// Package migrate holds the CLI wiring for the `esops migrate ...`
// command tree. Command actions stay thin: parse flags, build the
// source and target endpoints, call internal/migrate, render output.
//
// Unlike most other esops command groups, every migrate command
// operates on TWO contexts at once. The shared helper buildEndpoints
// (in shared.go) does the config-load and twin-cluster.New plumbing
// that cmdsetup.Prepare does for a single context.
package migrate

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `migrate` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "migrate",
		Usage: "Plan, check, reindex, verify, and cut over data between two clusters",
		Description: `Migration toolkit. The MVP exposes the two read-only
pre-flights every reindex starts with:

  migrate check  Connectivity, version, and target reindex.remote.whitelist
  migrate plan   Per-index size, mapping translation issues, and a duration estimate

Each command takes --source CTX and --target CTX (named contexts from
the config file). The destination cluster's reindex.remote.whitelist
is what gates remote /_reindex; that is the most common "the reindex
just won't start" foot-gun and ` + "`migrate check`" + ` reports it explicitly.

The reindex / cutover / verify commands described in CLAUDE.md §9 are
the next layer on top of this base; migrate plan / check are safe to
run against a live prod cluster — both are read-only.`,
		Commands: []*cli.Command{
			checkCommand(),
			planCommand(),
		},
	}
}
