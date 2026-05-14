package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
)

// dsModifySuggesters extends dsNameSuggesters with index suggestions
// for --add-index / --remove-index. The backing-index splice flags
// always take literal index names, so the regular index completer
// applies.
var dsModifySuggesters = map[string]completion.Suggester{
	"name":         completion.SuggestDataStreams,
	"add-index":    completion.SuggestIndices,
	"remove-index": completion.SuggestIndices,
}

func modifyCommand() *cli.Command {
	return &cli.Command{
		Name:  "modify",
		Usage: "Splice backing indices into or out of a data stream",
		Description: `Add or remove existing indices from a data stream's backing-index
chain via POST /_data_stream/_modify. Primarily used during
migrations — for example, to attach a reindexed copy of an old index
to a new data stream without re-indexing into it. Both Elasticsearch
and OpenSearch (2.x+) support the endpoint with the same
{actions:[...]} envelope.

Mutates cluster state. At least one --add-index or --remove-index is
required. Use --dry-run to validate the request without contacting
the cluster on the destructive path. Against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Data stream name (literal)",
			},
			&cli.StringSliceFlag{
				Name:  "add-index",
				Usage: "Backing index to add to the data stream; repeatable",
			},
			&cli.StringSliceFlag{
				Name:  "remove-index",
				Usage: "Backing index to remove from the data stream; repeatable",
			},
		},
		Action:        runModify,
		ShellComplete: completion.ForFlags(dsModifySuggesters),
	}
}

func runModify(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsdatastream.ModifyOptions{
		Name:   cmd.String("name"),
		Add:    cmd.StringSlice("add-index"),
		Remove: cmd.StringSlice("remove-index"),
		DryRun: cmd.Bool("dry-run"),
	}

	result, err := esopsdatastream.Modify(ctx, s.Client.DataStreamManage, opts)
	if err != nil {
		return err
	}

	s.Log("datastream.modify",
		"data_stream", cmd.String("name"),
		"actions", len(result.Actions),
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.DataStreamModify(os.Stdout, result, s.Format)
}
