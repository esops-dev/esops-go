package ops

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	"github.com/esops-dev/esops-go/internal/logging"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func shardsCommand() *cli.Command {
	return &cli.Command{
		Name:  "shards",
		Usage: "Show shard allocation across the cluster",
		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Limit to index pattern(s), comma-separated (e.g. 'logs-*,users')",
			},
			&cli.StringFlag{
				Name:  "state",
				Usage: "Filter by shard state (e.g. STARTED, UNASSIGNED, RELOCATING)",
			},
			&cli.StringFlag{
				Name:  "node",
				Usage: "Filter by node name (exact match)",
			},
			&cli.BoolFlag{
				Name:  "primary",
				Usage: "Show primaries only",
			},
			&cli.BoolFlag{
				Name:  "replica",
				Usage: "Show replicas only",
			},
			&cli.BoolFlag{
				Name:  "unassigned",
				Usage: "Shortcut for --state UNASSIGNED",
			},
		}, watch.Flags()...),
		// `analyze` is a subcommand of shards because it answers
		// distribution / skew / oversized questions over the same
		// /_cat/shards data the parent lists. urfave/cli/v3 routes a
		// matching positional to the subcommand and falls back to the
		// parent Action below for the bare `ops shards` invocation.
		Commands: []*cli.Command{
			shardsAnalyzeCommand(),
		},
		Action:        runShards,
		ShellComplete: completion.ForFlags(indexAndNodeSuggesters),
	}
}

func runShards(ctx context.Context, cmd *cli.Command) error {
	if cmd.Bool("primary") && cmd.Bool("replica") {
		return fmt.Errorf("--primary and --replica are mutually exclusive")
	}
	if cmd.Bool("unassigned") && cmd.String("state") != "" {
		return fmt.Errorf("--unassigned and --state are mutually exclusive")
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	filter := types.ShardFilter{
		Index: cmd.String("index"),
		State: cmd.String("state"),
		Node:  cmd.String("node"),
	}
	if cmd.Bool("unassigned") {
		filter.State = "UNASSIGNED"
	}
	switch {
	case cmd.Bool("primary"):
		filter.Role = types.ShardRolePrimary
	case cmd.Bool("replica"):
		filter.Role = types.ShardRoleReplica
	}

	return watch.Run(ctx, cmd, "esops ops shards", s.Format, func(ctx context.Context) error {
		shards, err := esopsops.Shards(ctx, s.Client.Shards, filter)
		if err != nil {
			return err
		}
		logging.Logger().Info("ops.shards",
			"context", s.ContextName,
			"dialect", string(s.Client.Info.Dialect),
			"version", s.Client.Info.Version,
			"shards", len(shards),
		)
		return output.Shards(os.Stdout, shards, s.Format)
	})
}

func shardsAnalyzeCommand() *cli.Command {
	return &cli.Command{
		Name:  "analyze",
		Usage: "Cluster-wide shard distribution, skew, and oversized-shard report",
		Description: `Read-only multi-section report over /_cat/shards and /_cat/nodes:
per-node distribution + median-based skew, top-N largest shards,
indices with shard-size issues (oversized / critical / undersized),
and an unassigned summary.

Thresholds default to community-recommended values (50 GiB oversized,
200 GiB critical, 1 GiB undersized, 20% skew) and are overridable
per-flag. Active values are echoed in the response so a saved JSON
capture is interpretable on its own.`,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:  "top",
				Usage: "Top-N largest shards (0 to hide the section)",
				Value: 10,
			},
			&cli.StringFlag{
				Name:  "max-shard-bytes",
				Usage: "Oversized threshold; accepts suffixes (50GiB, 1GB, 1024MiB) (default 50GiB)",
			},
			&cli.StringFlag{
				Name:  "critical-shard-bytes",
				Usage: "Critical threshold; accepts suffixes (200GiB) (default 200GiB)",
			},
			&cli.StringFlag{
				Name:  "min-shard-bytes",
				Usage: "Undersized threshold; accepts suffixes (1GiB) (default 1GiB)",
			},
			&cli.IntFlag{
				Name:  "skew-percent",
				Usage: "Per-node deviation flag threshold (default 20)",
			},
			&cli.BoolFlag{
				Name:  "include-hidden",
				Usage: "Include dot-prefixed / system indices",
			},
			&cli.StringSliceFlag{
				Name:  "by",
				Usage: "Limit output to one or more sections: nodes, indices, top, unassigned (default: all)",
			},
		},
		Action: runShardsAnalyze,
	}
}

func runShardsAnalyze(ctx context.Context, cmd *cli.Command) error {
	// Parse byte-suffix flags before opening the cluster connection —
	// a typo in --max-shard-bytes shouldn't cost a TLS handshake.
	maxBytes, err := parseBytesFlag(cmd.String("max-shard-bytes"))
	if err != nil {
		return fmt.Errorf("--max-shard-bytes: %w", err)
	}
	critBytes, err := parseBytesFlag(cmd.String("critical-shard-bytes"))
	if err != nil {
		return fmt.Errorf("--critical-shard-bytes: %w", err)
	}
	minBytes, err := parseBytesFlag(cmd.String("min-shard-bytes"))
	if err != nil {
		return fmt.Errorf("--min-shard-bytes: %w", err)
	}

	if err := validateAnalyzeSections(cmd.StringSlice("by")); err != nil {
		return err
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		// FormatsStandard, not streaming: analyze is a fixed-shape
		// multi-section report, not a stream of records, so NDJSON
		// has no meaning here.
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	req := types.ShardAnalysisRequest{
		TopN:               cmd.Int("top"),
		MaxShardBytes:      maxBytes,
		CriticalShardBytes: critBytes,
		MinShardBytes:      minBytes,
		SkewPercent:        cmd.Int("skew-percent"),
		IncludeHidden:      cmd.Bool("include-hidden"),
		Sections:           cmd.StringSlice("by"),
	}

	analysis, err := esopsops.AnalyzeShards(ctx, s.Client.Shards, s.Client.Nodes, req)
	if err != nil {
		return err
	}

	logging.Logger().Info("ops.shards.analyze",
		"context", s.ContextName,
		"dialect", string(s.Client.Info.Dialect),
		"version", s.Client.Info.Version,
		"data_nodes", analysis.Summary.DataNodes,
		"oversized", analysis.Summary.OversizedShards,
		"critical", analysis.Summary.CriticalShards,
		"skewed_nodes", analysis.Summary.SkewedNodes,
		"unassigned", analysis.Summary.UnassignedShards,
	)
	return output.ShardAnalysis(os.Stdout, analysis, s.Format)
}

// validateAnalyzeSections rejects unknown --by values up front. The
// business layer treats unknown sections as a no-op (defensive: the
// CLI is not the only path), but a typo here is an operator error
// that deserves a loud failure rather than a silently empty report.
func validateAnalyzeSections(sections []string) error {
	known := map[string]bool{"nodes": true, "indices": true, "top": true, "unassigned": true}
	for _, s := range sections {
		if !known[s] {
			return fmt.Errorf("--by: unknown section %q (expected: nodes, indices, top, unassigned)", s)
		}
	}
	return nil
}
