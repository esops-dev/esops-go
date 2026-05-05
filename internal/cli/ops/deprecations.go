package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func deprecationsCommand() *cli.Command {
	return &cli.Command{
		Name:  "deprecations",
		Usage: "List Elasticsearch deprecation findings (ES only)",
		Description: `Read-only listing over GET /_migration/deprecations — the cluster's
own pre-upgrade lint that reports cluster, node, index, ML,
data-stream, template, and ILM-policy settings that will not survive
the next major version.

Findings are flattened across the cluster's category groupings into
a single sorted list (LEVEL CATEGORY TARGET MESSAGE) so a triage
pass can scan top-down for criticals. -o wide adds the docs URL,
the cluster's resolve-during-rolling-upgrade flag, and any per-issue
details the cluster supplied.

Elasticsearch only. OpenSearch has no equivalent endpoint; the
command exits with code 6 (operation unsupported on this cluster
dialect) when targeted at an OS context.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Limit the index_settings category to indices matching this pattern (cluster-wildcard syntax)",
			},
		},
		Action: runDeprecations,
	}
}

func runDeprecations(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	req := types.DeprecationsRequest{Index: cmd.String("index")}
	issues, err := esopsops.ListDeprecations(ctx, s.Client.Deprecations, req)
	if err != nil {
		return err
	}

	critical := 0
	warning := 0
	for _, i := range issues {
		switch i.Level {
		case "critical":
			critical++
		case "warning":
			warning++
		}
	}
	s.Log("ops.deprecations",
		"issues", len(issues),
		"critical", critical,
		"warning", warning,
		"index", req.Index,
	)
	return output.Deprecations(os.Stdout, issues, s.Format)
}
