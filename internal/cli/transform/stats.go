package transform

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/output"
	esopstransform "github.com/esops-dev/esops-go/internal/transform"
)

func statsCommand() *cli.Command {
	return &cli.Command{
		Name:  "stats",
		Usage: "Show runtime stats for a transform",
		Description: `Show runtime stats for one transform: documents processed and
indexed, search/index times, recent failures and the age of the
last completed checkpoint.

Backs onto GET /_transform/<id>/_stats (Elasticsearch) and
GET /_plugins/_transform/<id>/_explain (OpenSearch). The two
products report different envelopes; the renderer surfaces the
overlap (state, counters, last-checkpoint time). Where the
products genuinely diverge (per-page indexing breakdown,
non-overlapping failure shapes) those fields stay zero rather
than being invented.

Read-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Transform id (literal)",
			},
		},
		Action:        runStats,
		ShellComplete: completion.ForFlags(nameSuggesters),
	}
}

func runStats(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	stats, err := esopstransform.Stats(ctx, s.Client.Transform, cmd.String("name"))
	if err != nil {
		return err
	}

	s.Log("transform.stats",
		"name", stats.Name,
		"state", stats.State,
		"docs_processed", stats.DocsProcessed,
		"checkpoint_age_ms", stats.CheckpointAgeMillis,
	)
	return output.TransformStats(os.Stdout, stats, s.Format)
}
