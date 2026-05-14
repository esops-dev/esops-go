package migrate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/logging"
	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func planCommand() *cli.Command {
	return &cli.Command{
		Name:  "plan",
		Usage: "Estimate reindex size, duration, and translate source mappings against the target dialect",
		Description: `Read-only plan for a subsequent reindex.

Lists the source's open indices (filtered by --indices), runs the
mapping translator against the target dialect, and reports per-index
docs / primary store / mapping issues / a duration estimate. The
estimate uses --throughput-docs-per-sec when set; the package
default is conservative and meant as a "you have time to grab
coffee" hint, not a SLA.

Translator outcomes:
  ok     no known incompatibility for the (from, to) pair
  warn   downgrades and renamed-but-equivalent types (storage cost
         increases, query semantics change)
  block  removed/unsupported types (e.g. legacy 'string', ES
         'rank_features' on OpenSearch). The reindex will fail
         without a hand-translated mapping.

Exits non-zero (code 7) when any per-index status is a blocker — same
convention as 'migrate check'.`,
		Flags: twinFlags(
			&cli.IntFlag{
				Name:  "throughput-docs-per-sec",
				Usage: "Override the duration estimate's docs/sec rate (0 = package default)",
			},
		),
		Action:        runPlan,
		ShellComplete: completion.ForFlags(twinCompleteSuggesters),
	}
}

func runPlan(ctx context.Context, cmd *cli.Command) error {
	s, err := prepareTwin(ctx, cmd, cmdsetup.FormatsStandard)
	if err != nil {
		return err
	}

	plan, err := esopsmigrate.Plan(ctx, s.Source, s.Target, types.MigrationPlanRequest{
		SourceContext:        s.Source.Context,
		TargetContext:        s.Target.Context,
		Indices:              cmd.String("indices"),
		ThroughputDocsPerSec: cmd.Int("throughput-docs-per-sec"),
	})
	if err != nil {
		return err
	}

	logging.Logger().Info("migrate.plan",
		"source", s.Source.Context,
		"target", s.Target.Context,
		"indices", plan.Totals.Indices,
		"docs", plan.Totals.DocsCount,
		"status", plan.Status,
		"blockers", plan.Blockers,
		"warnings", plan.Warnings,
		"throughput_dps", plan.ThroughputDPS,
	)

	if err := output.MigrationPlan(os.Stdout, plan, s.Format); err != nil {
		return err
	}

	if plan.Status == types.MigrationStatusBlock {
		return client.ErrDryRunBlocker
	}
	return nil
}
