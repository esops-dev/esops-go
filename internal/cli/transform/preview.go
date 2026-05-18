package transform

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/output"
	esopstransform "github.com/esops-dev/esops-go/internal/transform"
)

func previewCommand() *cli.Command {
	return &cli.Command{
		Name:  "preview",
		Usage: "Preview the output of a transform body before applying it",
		Description: `Preview the materialised output of a transform body without
creating it. The cluster returns a bounded, non-exhaustive sample
of rows so an operator can sanity-check the pivot/aggregations
shape before committing.

Body input is exactly one of:
  --from-file PATH     Read the JSON body from a file
  -                    Read the JSON body from stdin

Read-only. Backs onto POST /_transform/_preview (Elasticsearch)
and POST /_plugins/_transform/_preview (OpenSearch).`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON transform body (use - for stdin)",
			},
		},
		Action: runPreview,
	}
}

func runPreview(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	body, err := readBody(cmd.String("from-file"))
	if err != nil {
		return err
	}

	result, err := esopstransform.Preview(ctx, s.Client.Transform, esopstransform.PreviewOptions{Body: body})
	if err != nil {
		return err
	}

	s.Log("transform.preview", "documents", len(result.Documents))
	return output.TransformPreview(os.Stdout, result, s.Format)
}

// readBody resolves the operator-supplied body input. "-" reads from
// stdin; any other value is treated as a file path. Empty input is
// allowed here — the business layer rejects with a UsageError so the
// CLI surface stays declarative.
func readBody(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	if path == "" {
		return nil, nil
	}
	buf, err := os.ReadFile(path) // #nosec G304 -- path is the operator-supplied --from-file flag
	if err != nil {
		return nil, fmt.Errorf("reading --from-file %q: %w", path, err)
	}
	return buf, nil
}
