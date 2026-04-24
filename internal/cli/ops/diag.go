package ops

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/logging"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func diagCommand() *cli.Command {
	return &cli.Command{
		Name:  "diag",
		Usage: "Collect a redacted diagnostic bundle for support / post-incident review",
		Description: `Captures the endpoints that together answer "why is this cluster
behaving this way?": cluster health, nodes stats, hot threads, pending
and running tasks, shard allocation, snapshot repositories, and the
dialect-specific lifecycle policies (ILM on Elasticsearch, ISM on
OpenSearch). Each response is written verbatim into a single gzipped
tarball, alongside a manifest.json index that a reviewer can read
without unpacking the archive.

Redaction rule: the bundle never contains the cluster URL or any
credential. The tool writes only the cluster's own response bodies plus
the manifest — and the manifest deliberately omits connection details.

The two opt-in flags cover captures that are either large (mappings on
big clusters run to hundreds of MB) or policy-sensitive (slow-log
settings expose which queries the cluster would log). Both default off.

Default output path: ./esops-diag-<cluster>-<timestamp>.tar.gz. Override
with -o/--out to write to a different file or to - for stdout (useful
when uploading straight to an incident ticket).`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "out",
				Aliases: []string{"O"},
				Usage:   "Output path for the tarball (use '-' for stdout). Default: ./esops-diag-<cluster>-<timestamp>.tar.gz",
			},
			&cli.BoolFlag{
				Name:  "include-slowlog",
				Usage: "Also capture per-index slow-log settings (small, but exposes logged query shapes)",
			},
			&cli.BoolFlag{
				Name:  "include-mappings",
				Usage: "Also capture /_all/_mapping (can be very large on clusters with many indices)",
			},
			&cli.BoolFlag{
				Name:  "include-idle-threads",
				Usage: "Also capture hot_threads with ignore_idle_threads=false (reveals parked-lock contention the default hides)",
			},
		},
		// Despite producing a file, `ops diag` is read-only against the
		// cluster and therefore not destructive — it never hits a
		// protection tier, and the standard --output formats don't apply
		// (we emit a tarball, not a table). cmdsetup still opens the
		// client, logger, and context for us.
		Action: runDiag,
	}
}

func runDiag(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{})
	if err != nil {
		return err
	}

	req := types.DiagRequest{
		IncludeSlowlog:     cmd.Bool("include-slowlog"),
		IncludeMappings:    cmd.Bool("include-mappings"),
		IncludeIdleThreads: cmd.Bool("include-idle-threads"),
	}

	out, outPath, closer, err := openBundleWriter(cmd.String("out"), s.Client.Info.ClusterName, time.Now())
	if err != nil {
		return err
	}
	defer closer()

	manifest, err := esopsops.Diag(ctx, s.Client, req, out)
	if err != nil {
		return err
	}

	ok, failed := countArtifacts(manifest.Artifacts)
	logging.Logger().Info("ops.diag",
		"context", s.ContextName,
		"dialect", manifest.Cluster.Dialect,
		"version", manifest.Cluster.Version,
		"cluster", manifest.Cluster.Name,
		"artifacts_ok", ok,
		"artifacts_failed", failed,
		"out", outPath,
	)

	// Progress / summary goes to stderr — stdout may be carrying the
	// tarball when the user passed `--out -`, and we must not corrupt it.
	if outPath != "-" {
		fmt.Fprintf(os.Stderr, "wrote %s (%d artifacts, %d failed)\n", outPath, ok, failed)
	}
	return nil
}

// openBundleWriter resolves --out to a writer, an effective path label
// (for logs / progress lines), and a closer to run on defer. "-" writes
// to stdout; empty defaults to ./esops-diag-<cluster>-<ts>.tar.gz.
func openBundleWriter(explicit, clusterName string, now time.Time) (w *os.File, displayPath string, closer func(), err error) {
	if explicit == "-" {
		return os.Stdout, "-", func() {}, nil
	}
	path := explicit
	if path == "" {
		path = esopsops.BundleFilename(clusterName, now)
	}
	// #nosec G304 -- path is the user's chosen output file via --out.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", nil, fmt.Errorf("opening bundle output %q: %w", path, err)
	}
	return f, path, func() { _ = f.Close() }, nil
}

func countArtifacts(arts []types.DiagArtifact) (ok, failed int) {
	for _, a := range arts {
		switch {
		case a.Status >= 200 && a.Status < 300:
			ok++
		default:
			failed++
		}
	}
	return ok, failed
}
