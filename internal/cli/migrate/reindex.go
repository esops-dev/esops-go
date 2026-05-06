package migrate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/logging"
	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func reindexCommand() *cli.Command {
	return &cli.Command{
		Name:  "reindex",
		Usage: "Submit a deferred /_reindex per source index and poll to completion",
		Description: `Task-based remote reindex. The target cluster fetches from
the source via 'source.remote', so this command submits to the *target*
and uses the *source*'s URL + auth as the remote pointer.

For every index resolved from --indices on the source, esops:

  1. Submits POST /_reindex?wait_for_completion=false on the target
     with a body referencing the source via source.remote.host.
  2. Records the cluster-side task ID.
  3. Polls /_tasks/<id> at --poll-interval (default 5s) until the
     task reports completed=true.

Multi-index requests run sequentially — one task at a time. Concurrency
knobs (slices, requests-per-second) belong on the cluster, not on
client-side fan-out.

The destination index is named by, in priority order:
  --to-index NAME    explicit literal (only with a single source index)
  --to-suffix SUFFIX appended to the source name (e.g. -v2)
  default:           same name as the source

A typical version-bump or cross-dialect migration:

  esops migrate check    --source es-src --target es-tgt
  esops migrate reindex  --source es-src --target es-tgt --to-suffix=-v2 \
                         --slices auto --requests-per-second 5000
  esops migrate verify   --source es-src --target es-tgt --to-suffix=-v2

Pre-flight: 'migrate check' surfaces the most common "the reindex
just won't start" condition (target's reindex.remote.whitelist).
Without that whitelist entry, the cluster refuses /_reindex
regardless of credentials.

Mutates cluster state on the *target*. Honours protection-tier checks
on both source and target — a prod-protected target requires
--i-know-this-is-prod, exit 9 otherwise.

--dry-run resolves the source-side index set, prints what would be
submitted, and exits without firing /_reindex.`,
		Flags: twinFlags(
			&cli.StringFlag{
				Name:  "to-suffix",
				Usage: "Append to each source index name to derive the target index (e.g. '-v2')",
			},
			&cli.StringFlag{
				Name:  "to-index",
				Usage: "Explicit target index name; only valid when --indices resolves to one source",
			},
			&cli.StringFlag{
				Name:  "conflicts",
				Value: "abort",
				Usage: "Version-conflict policy: 'abort' (default) stops on the first conflict; 'proceed' counts and continues",
			},
			&cli.FloatFlag{
				Name:  "requests-per-second",
				Usage: "Per-task throttle. -1 = unbounded; 0 = cluster default; positive = explicit cap",
			},
			&cli.StringFlag{
				Name:  "slices",
				Usage: "Source-side slicing: 'auto' (cluster picks) or a positive integer; '' = unsliced",
			},
			&cli.StringFlag{
				Name:  "wait-for-active-shards",
				Usage: "Forwarded as ?wait_for_active_shards: '' = cluster default, 'all', or a positive integer",
			},
			&cli.IntFlag{
				Name:  "size",
				Usage: "Cap total docs copied per source index. 0 = no cap",
			},
			&cli.DurationFlag{
				Name:  "poll-interval",
				Value: esopsmigrate.DefaultReindexPollInterval,
				Usage: "How often /_tasks/<id> is polled while a reindex runs",
			},
		),
		Action: runReindex,
	}
}

func runReindex(ctx context.Context, cmd *cli.Command) error {
	s, err := prepareTwin(ctx, cmd, cmdsetup.FormatsStandard)
	if err != nil {
		return err
	}
	if err := enforceTwinProtection(cmd, s); err != nil {
		return err
	}
	// Preflight the source's auth.type. mTLS / SigV4 cannot be
	// forwarded into the target's source.remote block; surface that
	// up-front rather than letting the cluster reject the source
	// fetch with an opaque 401 mid-reindex.
	srcAuthType, err := resolveSourceAuthType(cmd, s.Source.Context)
	if err != nil {
		return err
	}
	if ok, reason := sourceAuthIsForwardable(srcAuthType); !ok {
		return client.UsageError("source context %q: %s", s.Source.Context, reason)
	}

	req := types.MigrationReindexRequest{
		SourceContext:       s.Source.Context,
		TargetContext:       s.Target.Context,
		Indices:             cmd.String("indices"),
		TargetSuffix:        cmd.String("to-suffix"),
		TargetIndexLiteral:  cmd.String("to-index"),
		Conflicts:           cmd.String("conflicts"),
		RequestsPerSecond:   cmd.Float("requests-per-second"),
		Slices:              cmd.String("slices"),
		WaitForActiveShards: cmd.String("wait-for-active-shards"),
		SizeLimit:           int64(cmd.Int("size")),
		DryRun:              cmd.Bool("dry-run"),
		PollInterval:        cmd.Duration("poll-interval"),
	}

	res, err := esopsmigrate.Reindex(ctx, s.Source, s.Target, req)
	if err != nil {
		return err
	}

	logging.Logger().Info("migrate.reindex",
		"source", s.Source.Context,
		"target", s.Target.Context,
		"dry_run", res.DryRun,
		"indices", len(res.Indices),
		"status", res.Status,
		"blockers", res.Blockers,
		"warnings", res.Warnings,
	)

	if err := output.MigrationReindex(os.Stdout, res, s.Format); err != nil {
		return err
	}
	if res.Status == types.MigrationStatusBlock {
		return client.ErrDryRunBlocker
	}
	return nil
}
