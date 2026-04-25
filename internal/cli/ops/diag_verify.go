package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/logging"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/output"
)

// diagVerifyCommand is the `ops diag verify <bundle>` subcommand. It is
// purely offline — no cluster connectivity, no config lookup — so it
// deliberately bypasses cmdsetup.Prepare. An operator on a plane
// verifying a bundle a colleague sent should not need a cluster
// reachable.
func diagVerifyCommand() *cli.Command {
	return &cli.Command{
		Name:      "verify",
		Usage:     "Verify a diag bundle's manifest hashes against its contents",
		ArgsUsage: "<bundle.tar.gz> | -",
		Description: `Streams the bundle, recomputes the SHA-256 of every artifact, and
compares each one against the value the manifest recorded when the
bundle was created. Holds only the running hash state in memory — a
2 GB --include-mappings bundle verifies in the same memory budget as
a 50 MB one.

Exit code 0 if every artifact's hash matches its manifest entry and
no extra files were found in the tarball; non-zero otherwise. Use
--expect-sha256 to additionally verify the manifest itself against
the hash the operator recorded when running 'ops diag' (the line
'manifest sha256: ...' on stderr) — that catches a manifest that was
modified to point at modified artifacts.

Reads from a path or - for stdin so the command pipes from anywhere
(curl, scp -O -, gh release download -O -).

Examples:

    esops ops diag verify ./bundle.tar.gz
    esops ops diag verify ./bundle.tar.gz --expect-sha256 a3f5...d12
    cat bundle.tar.gz | esops ops diag verify -`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "expect-sha256",
				Usage: "Hex SHA-256 the manifest is expected to match (the value 'ops diag' printed when the bundle was created)",
			},
		},
		Action: runDiagVerify,
	}
}

func runDiagVerify(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args()
	if args.Len() != 1 {
		return fmt.Errorf("usage: esops ops diag verify <bundle.tar.gz> | -")
	}

	// --output is a global flag; resolve it directly without going
	// through cmdsetup since verify has no cluster to probe.
	rawFmt := cmd.String("output")
	if rawFmt == "" {
		rawFmt = string(output.FormatTable)
	}
	format, err := output.Parse(rawFmt)
	if err != nil {
		return err
	}
	switch format {
	case output.FormatTable, output.FormatWide, output.FormatJSON, output.FormatYAML:
		// supported
	default:
		return fmt.Errorf("output format %q is not supported for verify (accepted: table, wide, json, yaml)", format)
	}

	reader, label, closer, err := openBundleReader(args.First())
	if err != nil {
		return err
	}
	defer closer()

	result, err := esopsops.VerifyDiagBundle(ctx, esopsops.VerifyRequest{
		Reader:       reader,
		ExpectSHA256: cmd.String("expect-sha256"),
	})
	if err != nil {
		return err
	}

	logging.Logger().Info("ops.diag.verify",
		"bundle", label,
		"manifest_sha256", result.ManifestSHA256,
		"cluster", result.Cluster.Name,
		"dialect", result.Cluster.Dialect,
		"total", result.Summary.Total,
		"ok", result.Summary.OK,
		"mismatched", result.Summary.Mismatched,
		"missing", result.Summary.Missing,
		"no_hash", result.Summary.NoHash,
		"unknown", result.Summary.Unknown,
		"pass", result.Pass,
	)

	if renderErr := output.DiagVerify(os.Stdout, label, result, format); renderErr != nil {
		return renderErr
	}
	if !result.Pass {
		// Stable signal for scripts; the structured renderer above
		// already explained what's wrong on stdout.
		return errors.New("bundle integrity check failed")
	}
	return nil
}

// openBundleReader resolves the positional argument to a reader and a
// human-friendly label for logs / table headers. "-" reads from stdin;
// any other value is treated as a file path.
func openBundleReader(path string) (r io.Reader, label string, closer func(), err error) {
	if path == "-" {
		return os.Stdin, "<stdin>", func() {}, nil
	}
	// #nosec G304 -- path is the user's chosen input file via the
	// positional argument.
	f, err := os.Open(path)
	if err != nil {
		return nil, "", nil, fmt.Errorf("opening bundle %q: %w", path, err)
	}
	return f, path, func() { _ = f.Close() }, nil
}
