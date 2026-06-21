package migrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/logging"
	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func shadowCommand() *cli.Command {
	return &cli.Command{
		Name:  "shadow",
		Usage: "Replay queries against source and target and diff the hits (mapping/analyzer drift)",
		Description: `Read-only shadow-read verification. Doc-count parity (` + "`migrate verify`" + `)
proves nothing about mapping drift, analyzer changes, or score-affecting
configuration: the same query can return a different set or order of
hits after a migration even when the document counts match. shadow
replays a representative set of queries against both clusters and diffs
the top-K hits.

Inherited from ` + "`migrate verify`" + ` (not repeated in OPTIONS below):
  --source CTX           Source context name (required).
  --target CTX           Target context name (required).
  --indices PATTERN      Index pattern resolved identically on both
                         clusters (empty = cluster-wide).

Provide exactly one query source:

  --queries FILE         NDJSON of {"name":"...","body":{...}} records,
                         one per line. body is a search request. Use '-'
                         to read NDJSON from stdin.
  --from-slowlog FILE    An Elasticsearch/OpenSearch search-slowlog
                         export (plain-text or JSON). Distinct query
                         bodies are extracted and the top --slowlog-top
                         by frequency are replayed.

The diff compares the ordered document IDs in the top --top-k hits. Set
membership and ordering drive the per-query status; score differences
are reported but do not fail the check (they're expected across versions
and analyzer changes) unless --strict-scores is set.

Query bodies may carry PII (user IDs in filters, etc.) — they are NOT
echoed into the output unless --include-query-body is set.

Exit codes:
  0  Every query matched (or only score differences, without --strict-scores).
  1  At least one query errored against a cluster.
  7  At least one query mismatched (different hit set/order) — suppress
     with --allow-mismatch.

  esops migrate verify shadow --source es-old --target es-new \
    --indices 'logs-*' --queries ./queries.ndjson`,
		Flags: append(shadowFlags(),
			&cli.StringFlag{
				Name:      "queries",
				Usage:     "NDJSON file of {name, body} search records ('-' for stdin)",
				TakesFile: true,
			},
			&cli.StringFlag{
				Name:      "from-slowlog",
				Usage:     "Search-slowlog export to extract distinct query bodies from",
				TakesFile: true,
			},
		),
		Action:        runShadow,
		ShellComplete: completion.ForFlags(twinCompleteSuggesters),
	}
}

// shadowFlags are the execution / diffing knobs, kept separate from the
// two input-source flags so the command body reads as
// "knobs + one-of(queries, slowlog)".
func shadowFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{
			Name:  "top-k",
			Value: 100,
			Usage: "Compare the top-K hits per query",
		},
		&cli.IntFlag{
			Name:  "concurrency",
			Value: 4,
			Usage: "Max queries replayed in parallel",
		},
		&cli.DurationFlag{
			Name:  "query-timeout",
			Value: 10 * time.Second,
			Usage: "Per-query timeout",
		},
		&cli.IntFlag{
			Name:  "slowlog-top",
			Value: 20,
			Usage: "With --from-slowlog: replay the top-N distinct queries by frequency",
		},
		&cli.BoolFlag{
			Name:  "strict-scores",
			Usage: "Treat a score divergence as a mismatch (only reasonable for same-version migrations)",
		},
		&cli.BoolFlag{
			Name:  "allow-mismatch",
			Usage: "Report mismatches but exit 0 instead of 7",
		},
		&cli.BoolFlag{
			Name:  "include-query-body",
			Usage: "Echo each query body into the output (may contain PII; off by default)",
		},
	}
}

func runShadow(ctx context.Context, cmd *cli.Command) error {
	s, err := prepareTwin(ctx, cmd, cmdsetup.FormatsStreaming)
	if err != nil {
		return err
	}

	queries, err := loadShadowQueries(cmd)
	if err != nil {
		return err
	}

	res, err := esopsmigrate.Shadow(ctx, s.Source, s.Target, types.MigrationShadowRequest{
		SourceContext: s.Source.Context,
		TargetContext: s.Target.Context,
		Indices:       cmd.String("indices"),
		Queries:       queries,
		TopK:          cmd.Int("top-k"),
		Concurrency:   cmd.Int("concurrency"),
		QueryTimeout:  cmd.Duration("query-timeout"),
		StrictScores:  cmd.Bool("strict-scores"),
		IncludeBody:   cmd.Bool("include-query-body"),
	})
	if err != nil {
		return err
	}

	logging.Logger().Info("migrate.verify.shadow",
		"source", s.Source.Context,
		"target", s.Target.Context,
		"queries", len(res.Queries),
		"status", res.Status,
		"mismatches", res.Mismatches,
		"errors", res.Errors,
	)

	if err := output.MigrationShadow(os.Stdout, res, s.Format); err != nil {
		return err
	}

	// An error row means a query could not be replayed at all — that's a
	// genuine failure regardless of --allow-mismatch, so it always exits
	// non-zero. A mismatch is the dry-run blocker (exit 7) the operator
	// can opt to tolerate.
	if res.Errors > 0 {
		return fmt.Errorf("%d shadow quer(y/ies) failed to execute against a cluster", res.Errors)
	}
	if res.Mismatches > 0 && !cmd.Bool("allow-mismatch") {
		return client.ErrDryRunBlocker
	}
	return nil
}

// loadShadowQueries resolves the one-of(--queries, --from-slowlog) input
// into the query set. Exactly one source is required.
func loadShadowQueries(cmd *cli.Command) ([]types.ShadowQuery, error) {
	queriesPath := cmd.String("queries")
	slowlogPath := cmd.String("from-slowlog")

	switch {
	case queriesPath != "" && slowlogPath != "":
		return nil, client.UsageError("--queries and --from-slowlog are mutually exclusive")
	case queriesPath == "" && slowlogPath == "":
		return nil, client.UsageError("one of --queries or --from-slowlog is required")
	case queriesPath != "":
		r, closeFn, err := openShadowInput(queriesPath)
		if err != nil {
			return nil, err
		}
		defer closeFn()
		return esopsmigrate.ParseShadowQueriesNDJSON(r)
	default:
		r, closeFn, err := openShadowInput(slowlogPath)
		if err != nil {
			return nil, err
		}
		defer closeFn()
		return esopsmigrate.ParseShadowSlowlog(r, cmd.Int("slowlog-top"))
	}
}

// openShadowInput opens a query/slowlog input, treating "-" as stdin.
// The returned closeFn is always safe to call (a no-op for stdin).
func openShadowInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path) // #nosec G304 -- operator-supplied query/slowlog file path
	if err != nil {
		return nil, nil, client.UsageError("opening %q: %v", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}
