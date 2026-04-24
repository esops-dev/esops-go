package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/logging"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func hotThreadsCommand() *cli.Command {
	return &cli.Command{
		Name:  "hot-threads",
		Usage: "Sampled hot threads across cluster nodes",
		Description: `Wraps /_nodes/hot_threads — the JVM introspection endpoint both
Elasticsearch and OpenSearch expose in the same text format. The
response is the canonical "what is this node actually doing?" view
when a node is pinning CPU, blocking on a lock, or waiting on I/O.

No flags: samples all nodes with the cluster defaults (3 threads,
10 snapshots, 500ms interval, type=cpu). Narrow with --node to focus
on a specific node; change --type to sample a different dimension
(wait/block/mem) to find non-CPU contention.

Output is the raw text block the cluster produces — per-node "::: {node}"
headers followed by stack samples. Use -o json / -o yaml to wrap the
text in a single-object envelope for scripting; there is no per-node
structured parse because the text format is a cluster-version
implementation detail, not a stable schema.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "node",
				Usage: "Limit to one or more node names/IDs (repeatable, or comma-separated). Empty = all nodes.",
			},
			&cli.IntFlag{
				Name:  "threads",
				Usage: "Number of busy threads to report per node (cluster default: 3)",
			},
			&cli.DurationFlag{
				Name:  "interval",
				Usage: "Sampling interval between snapshots (cluster default: 500ms)",
			},
			&cli.IntFlag{
				Name:  "snapshots",
				Usage: "Number of stacktrace snapshots per thread (cluster default: 10)",
			},
			&cli.StringFlag{
				Name:  "type",
				Usage: "Sampling dimension: cpu | wait | block | mem (cluster default: cpu)",
			},
			&cli.BoolFlag{
				Name:  "show-idle",
				Usage: "Include threads parked on idle socket/queue selectors (default: hide them)",
			},
		},
		Action: runHotThreads,
	}
}

func runHotThreads(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	req := types.HotThreadsRequest{
		Nodes: cmd.StringSlice("node"),
		Type:  cmd.String("type"),
	}
	if cmd.IsSet("threads") {
		v := cmd.Int("threads")
		req.Threads = &v
	}
	if cmd.IsSet("interval") {
		req.Interval = cmd.Duration("interval")
	}
	if cmd.IsSet("snapshots") {
		v := cmd.Int("snapshots")
		req.Snapshots = &v
	}
	// The cluster already defaults ignore_idle_threads to true; only send
	// the parameter when the user explicitly asked to see idle threads,
	// so the default code path matches the cluster's own default exactly.
	if cmd.Bool("show-idle") {
		f := false
		req.IgnoreIdleThreads = &f
	}

	h, err := esopsops.HotThreads(ctx, s.Client.HotThreads, req)
	if err != nil {
		return err
	}

	logging.Logger().Info("ops.hot-threads",
		"context", s.ContextName,
		"dialect", string(s.Client.Info.Dialect),
		"version", s.Client.Info.Version,
		"nodes", req.Nodes,
		"type", req.Type,
		"bytes", len(h.Text),
	)
	return output.HotThreads(os.Stdout, h, s.Format)
}
