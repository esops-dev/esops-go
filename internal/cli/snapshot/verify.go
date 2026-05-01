package snapshot

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/output"
	esopssnapshot "github.com/dj-wasabi/esops-go/internal/snapshot"
)

func verifyCommand() *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "Verify a snapshot repository can be reached from every node",
		Description: `Verify a registered repository via POST /_snapshot/<repo>/_verify.

The cluster asks every node to talk to the repository's backing store
and reports the set of nodes that succeeded. A node missing from the
returned list is the canonical signal of a misconfigured fs mount,
revoked S3 credentials, or DNS-level reachability problems.

--deep upgrades the connectivity check to the cluster's _analyze probe
(ES 7.4+, OS 2.x): writes a small set of test blobs, reads them back
from every node, then deletes them. Strictly more thorough than the
default; it does write scratch data, so a prod-protected context still
needs --i-know-this-is-prod despite the operation being recoverable.

Read-only from the operator's perspective in default mode: no snapshot
data is written. Safe to run against a prod-protected context without
extra flags.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Registered repository name to verify (literal, no patterns)",
			},
			&cli.BoolFlag{
				Name:  "deep",
				Usage: "Run the deeper repository _analyze probe (ES 7.4+, OS 2.x). Writes scratch blobs.",
			},
			&cli.IntFlag{
				Name:  "blob-count",
				Usage: "Number of test blobs --deep should write (cluster default: 100)",
			},
			&cli.IntFlag{
				Name:  "concurrency",
				Usage: "Concurrent reads/writes during --deep (cluster default: 10)",
			},
			&cli.IntFlag{
				Name:  "read-node-count",
				Usage: "Nodes that read each blob during --deep (cluster default: 10)",
			},
			&cli.StringFlag{
				Name:  "max-blob-size",
				Usage: "Per-blob size cap for --deep (e.g. 10MB, 1GB)",
			},
			&cli.StringFlag{
				Name:  "max-total-size",
				Usage: "Total data the --deep probe writes (e.g. 1GB)",
			},
			&cli.DurationFlag{
				Name:  "deep-timeout",
				Usage: "Cluster-side timeout for --deep (forwarded to ?timeout)",
			},
		},
		Action: runVerify,
	}
}

func runVerify(ctx context.Context, cmd *cli.Command) error {
	deep := cmd.Bool("deep")
	spec := cmdsetup.Spec{SupportedFormats: cmdsetup.FormatsStandard}
	if deep {
		// _analyze writes scratch blobs to the repository; treat it as
		// destructive so the prod-protection tier requires the explicit
		// acknowledgement, mirroring how every other write path behaves.
		spec.Destructive = true
	}
	s, err := cmdsetup.Prepare(ctx, cmd, spec)
	if err != nil {
		return err
	}

	opts := esopssnapshot.VerifyOptions{
		Repository: cmd.String("repository"),
		Deep:       deep,
		AnalyzeKnobs: esopssnapshot.AnalyzeKnobs{
			BlobCount:     cmd.Int("blob-count"),
			Concurrency:   cmd.Int("concurrency"),
			ReadNodeCount: cmd.Int("read-node-count"),
			MaxBlobSize:   cmd.String("max-blob-size"),
			MaxTotalSize:  cmd.String("max-total-size"),
			Timeout:       cmd.Duration("deep-timeout"),
		},
	}

	if deep {
		result, err := esopssnapshot.VerifyDeep(ctx, s.Client.SnapshotVerify, opts)
		if err != nil {
			return err
		}
		s.Log("snapshot.verify.deep",
			"repository", result.Repository,
			"blobs", result.BlobCount,
			"status", result.Status,
		)
		return output.SnapshotAnalyze(os.Stdout, result, s.Format)
	}

	result, err := esopssnapshot.Verify(ctx, s.Client.SnapshotVerify, opts)
	if err != nil {
		return err
	}
	s.Log("snapshot.verify",
		"repository", result.Repository,
		"verified_nodes", len(result.VerifiedNodes),
	)
	return output.SnapshotVerify(os.Stdout, result, s.Format)
}
