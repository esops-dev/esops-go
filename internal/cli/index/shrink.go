package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	esopsindex "github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/output"
)

func shrinkCommand() *cli.Command {
	return &cli.Command{
		Name:  "shrink",
		Usage: "Shrink an index into a new index with fewer primary shards",
		Description: `Shrink a source index into a new target index via /<source>/_shrink/<target>.
The cluster hard-links segments rather than copying — much cheaper than
a reindex when the operator just needs to reduce primary shard count
(e.g. an over-sharded daily index sealed for archive).

The cluster's shrink endpoint requires the source to be:
  - read-only (set 'index.blocks.write: true')
  - allocated with all primaries on a single node
  - have a primary count that is a factor of --shards
    (4→2, 4→1, 8→4, …)

esops does NOT flip those settings automatically — they are policy
decisions an operator should make before invoking shrink. The cluster
will surface its own error if the source isn't ready, which is the
right place for that contract.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "source",
				Required: true,
				Usage:    "Existing index to shrink (literal name, no patterns)",
			},
			&cli.StringFlag{
				Name:     "target",
				Required: true,
				Usage:    "New index name to create from the shrink (literal name, no patterns)",
			},
			&cli.IntFlag{
				Name:  "shards",
				Usage: "Number of primary shards on the target (must be a factor of source shards; 0 = cluster default)",
			},
			&cli.IntFlag{
				Name:  "replicas",
				Usage: "Number of replicas on the target (omit to leave the cluster default; 0 keeps shrink cheap)",
			},
			&cli.BoolFlag{
				Name:  "copy-settings",
				Usage: "Copy compatible source settings to the target (?copy_settings=true; OpenSearch only on this build)",
			},
			&cli.StringFlag{
				Name:  "wait-for-active-shards",
				Usage: "Forward to the cluster as ?wait_for_active_shards=<value> on the target (numeric or 'all')",
			},
		},
		Action: runShrink,
	}
}

func runShrink(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.ShrinkOptions{
		Source:              cmd.String("source"),
		Target:              cmd.String("target"),
		TargetShards:        cmd.Int("shards"),
		DryRun:              cmd.Bool("dry-run"),
		WaitForActiveShards: cmd.String("wait-for-active-shards"),
	}
	if cmd.IsSet("replicas") {
		v := cmd.Int("replicas")
		opts.TargetReplicas = &v
	}
	if cmd.IsSet("copy-settings") {
		v := cmd.Bool("copy-settings")
		opts.CopySettings = &v
	}

	result, err := esopsindex.Shrink(ctx, s.Client.IndexShrink, opts)
	if err != nil {
		return err
	}

	s.Log("index.shrink",
		"source", result.Source,
		"target", result.Target,
		"target_shards", result.TargetShards,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.IndexShrink(os.Stdout, result, s.Format)
}
