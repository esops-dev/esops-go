// Package migrate holds the CLI wiring for the `esops migrate ...`
// command tree. Command actions stay thin: parse flags, build the
// source and target endpoints, call internal/migrate, render output.
//
// Most migrate commands operate on TWO contexts at once. The shared
// helper buildEndpoints (in shared.go) does the config-load and
// twin-cluster.New plumbing that cmdsetup.Prepare does for a single
// context. `migrate cutover` is the exception: alias swaps live on a
// single cluster and that command goes through cmdsetup.Prepare like
// every other single-context destructive op.
package migrate

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `migrate` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "migrate",
		Usage: "Plan, check, reindex, verify, and cut over data between two clusters",
		Description: `Migration toolkit covering the full reindex flow:

  migrate check    Read-only pre-flight: connectivity, version, target whitelist
  migrate plan     Read-only: per-index size, mapping translation, duration est.
  migrate reindex  Submit a deferred /_reindex per source index and poll to done
  migrate verify   Compare per-index doc counts source vs target
  migrate cutover  Atomic alias swap on a single cluster (FromIndex → ToIndex)

The first four take --source CTX and --target CTX (named contexts from
the config file). The destination cluster's reindex.remote.whitelist is
what gates remote /_reindex; that is the most common "the reindex just
won't start" foot-gun and ` + "`migrate check`" + ` reports it explicitly.

` + "`migrate cutover`" + ` is single-cluster: --context CTX, --alias, --from,
--to. It composes existing capabilities (set settings + bulk alias
update) into one atomic swap.

A typical reindex flow:

  esops migrate check    --source es-src --target es-tgt
  esops migrate plan     --source es-src --target es-tgt
  esops migrate reindex  --source es-src --target es-tgt --to-suffix=-v2
  esops migrate verify   --source es-src --target es-tgt --to-suffix=-v2
  esops migrate cutover  --context es-tgt --alias logs --from logs --to logs-v2 --block-writes`,
		Commands: []*cli.Command{
			checkCommand(),
			planCommand(),
			reindexCommand(),
			verifyCommand(),
			cutoverCommand(),
		},
	}
}
