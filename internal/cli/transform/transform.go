// Package transform holds the CLI wiring for the `esops transform ...`
// command tree. Command actions stay thin: parse flags, load config,
// build a client, call internal/transform, render output.
//
// Transforms are long-running pivot/aggregation jobs both products
// support; operators inherit them, lose track of them, and find them
// silently lagging or stopped weeks later. The subcommand set mirrors
// `pipeline` / `script` for inspect+manage (list/get/apply/delete)
// and adds the lifecycle verbs (start/stop/stats/preview) that the
// day-to-day operation of a continuous transform actually needs.
package transform

import (
	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/completion"
)

// Command returns the `transform` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "transform",
		Usage: "Inspect and manage transforms (pivot/aggregation jobs)",
		Commands: []*cli.Command{
			listCommand(),
			getCommand(),
			statsCommand(),
			previewCommand(),
			applyCommand(),
			deleteCommand(),
			startCommand(),
			stopCommand(),
		},
	}
}

// nameSuggesters drives tab-completion for the --name flag on every
// transform subcommand. Hoisted because the same map is wired on
// multiple call sites.
var nameSuggesters = map[string]completion.Suggester{
	"name": completion.SuggestTransforms,
}
