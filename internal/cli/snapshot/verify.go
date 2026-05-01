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

Read-only from the operator's perspective: no snapshot data is written.
Safe to run against a prod-protected context.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repository",
				Required: true,
				Usage:    "Registered repository name to verify (literal, no patterns)",
			},
		},
		Action: runVerify,
	}
}

func runVerify(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	result, err := esopssnapshot.Verify(ctx, s.Client.SnapshotVerify, esopssnapshot.VerifyOptions{
		Repository: cmd.String("repository"),
	})
	if err != nil {
		return err
	}

	s.Log("snapshot.verify",
		"repository", result.Repository,
		"verified_nodes", len(result.VerifiedNodes),
	)
	return output.SnapshotVerify(os.Stdout, result, s.Format)
}
