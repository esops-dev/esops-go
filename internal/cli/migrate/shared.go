package migrate

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/logging"
	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
)

// twinSetup is the migrate equivalent of cmdsetup.Setup — but for two
// contexts at once. Format applies to both reports; protection-tier
// checks are not run because both migrate plan and migrate check are
// read-only against both clusters.
type twinSetup struct {
	Source esopsmigrate.Endpoint
	Target esopsmigrate.Endpoint
	Format output.Format
}

// twinFlags is the flag set every migrate subcommand carries:
// --source / --target name a context each, --indices narrows the
// per-index report, and --output is parsed via the same allowlist
// cmdsetup uses for read-only commands.
func twinFlags(extra ...cli.Flag) []cli.Flag {
	base := []cli.Flag{
		&cli.StringFlag{
			Name:     "source",
			Required: true,
			Usage:    "Source context name (the cluster reindex would read from)",
		},
		&cli.StringFlag{
			Name:     "target",
			Required: true,
			Usage:    "Target context name (the cluster reindex would write to)",
		},
		&cli.StringFlag{
			Name:  "indices",
			Usage: "Index pattern to scope the report (cluster-wildcard syntax). Empty = every non-hidden index.",
		},
	}
	return append(base, extra...)
}

// prepareTwin loads the config and builds two connected clients —
// one each for --source and --target. Errors from either probe are
// surfaced verbatim because the operator's first reaction is "which
// cluster failed?" and an aggregated "couldn't migrate" loses that.
//
// supported is the --output allowlist; we pass it through to
// output.Parse so the CLI rejects --output ndjson up-front rather
// than silently ignoring it later.
func prepareTwin(ctx context.Context, cmd *cli.Command, supported []output.Format) (twinSetup, error) {
	srcName := cmd.String("source")
	tgtName := cmd.String("target")
	if srcName == tgtName {
		return twinSetup{}, client.UsageError("--source and --target must name different contexts (got %q for both)", srcName)
	}

	cfg, _, err := config.LoadDefault(cmd.String("config"))
	if err != nil {
		return twinSetup{}, err
	}

	srcEndpoint, err := buildEndpoint(ctx, cmd, cfg, srcName, "source")
	if err != nil {
		return twinSetup{}, err
	}
	tgtEndpoint, err := buildEndpoint(ctx, cmd, cfg, tgtName, "target")
	if err != nil {
		return twinSetup{}, err
	}

	format, err := resolveFormat(cmd, cfg.Defaults.Output, supported)
	if err != nil {
		return twinSetup{}, err
	}

	logging.Logger().Debug("migrate.prepare",
		"source", srcEndpoint.Context,
		"source_dialect", string(srcEndpoint.Info.Dialect),
		"source_version", srcEndpoint.Info.Version,
		"target", tgtEndpoint.Context,
		"target_dialect", string(tgtEndpoint.Info.Dialect),
		"target_version", tgtEndpoint.Info.Version,
	)

	return twinSetup{Source: srcEndpoint, Target: tgtEndpoint, Format: format}, nil
}

// buildEndpoint resolves one context name into a connected
// esopsmigrate.Endpoint. The role argument ("source" / "target")
// only shapes the error message — the resolved capabilities are the
// same on both sides.
func buildEndpoint(ctx context.Context, cmd *cli.Command, cfg config.Config, name, role string) (esopsmigrate.Endpoint, error) {
	resolved, cc, err := cfg.ResolveContext(name)
	if err != nil {
		return esopsmigrate.Endpoint{}, fmt.Errorf("%s context %q: %w", role, name, err)
	}
	cmdsetup.ApplyContextOverrides(&cc, cmd)

	c, err := cluster.New(ctx, cc)
	if err != nil {
		return esopsmigrate.Endpoint{}, fmt.Errorf("connecting to %s context %q: %w", role, resolved, err)
	}
	return esopsmigrate.Endpoint{
		Context:   resolved,
		URL:       cc.URL,
		Info:      c.Info,
		Indices:   c.Indices,
		Mappings:  c.Mappings,
		Whitelist: c.RemoteReindex,
	}, nil
}

// resolveFormat is the migrate-side mirror of cmdsetup's private
// helper. We can't reach the cmdsetup one (it lives behind unexported
// names) and the surface here is small — flag > config default >
// flag built-in default, then validate against the allowlist.
func resolveFormat(cmd *cli.Command, configDefault string, supported []output.Format) (output.Format, error) {
	raw := cmd.String("output")
	if !cmd.IsSet("output") && configDefault != "" {
		raw = configDefault
	}
	f, err := output.Parse(raw)
	if err != nil {
		return "", err
	}
	if len(supported) == 0 {
		return f, nil
	}
	for _, s := range supported {
		if s == f {
			return f, nil
		}
	}
	names := make([]string, len(supported))
	for i, s := range supported {
		names[i] = string(s)
	}
	return "", client.UsageError("output format %q is not supported for this command (accepted: %v)", f, names)
}
