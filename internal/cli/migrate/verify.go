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

func verifyCommand() *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "Compare per-index doc counts on the source and target clusters",
		Description: `Read-only post-reindex verification. For every source index
matching --indices, esops issues GET /<index>/_count on both clusters
and reports the diff (signed: source - target).

The pairing is name-for-name unless --to-suffix is given, in which
case the target name is "<source-name><suffix>" — same mapping as
'migrate reindex --to-suffix'.

/<index>/_count is the cluster's authoritative count (segment-flushed,
not /_cat/indices' refresh-cached column) so verify reflects the state
at the moment the call lands.

Exit codes:
  0  Every per-index count matches and the cluster reported a complete
     shard set on both sides.
  7  At least one per-index count differed — the report's per-row
     "DIFF" column shows by how much.

Verify-then-cutover is one runbook command:

  esops migrate verify  --source es-src --target es-tgt --to-suffix=-v2 \
    && esops migrate cutover --context es-tgt --alias logs --from logs --to logs-v2`,
		Flags: twinFlags(
			&cli.StringFlag{
				Name:  "to-suffix",
				Usage: "Append to each source index name to derive the target index (e.g. '-v2')",
			},
		),
		Action: runVerify,
	}
}

func runVerify(ctx context.Context, cmd *cli.Command) error {
	s, err := prepareTwin(ctx, cmd, cmdsetup.FormatsStandard)
	if err != nil {
		return err
	}

	res, err := esopsmigrate.Verify(ctx, s.Source, s.Target, types.MigrationVerifyRequest{
		SourceContext: s.Source.Context,
		TargetContext: s.Target.Context,
		Indices:       cmd.String("indices"),
		TargetSuffix:  cmd.String("to-suffix"),
	})
	if err != nil {
		return err
	}

	logging.Logger().Info("migrate.verify",
		"source", s.Source.Context,
		"target", s.Target.Context,
		"indices", len(res.Indices),
		"status", res.Status,
		"blockers", res.Blockers,
		"warnings", res.Warnings,
	)

	if err := output.MigrationVerify(os.Stdout, res, s.Format); err != nil {
		return err
	}
	if res.Status == types.MigrationStatusBlock {
		return client.ErrDryRunBlocker
	}
	return nil
}
