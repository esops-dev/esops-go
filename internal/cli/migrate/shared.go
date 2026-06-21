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
// checks are not run during prepareTwin because most commands here are
// read-only. `migrate reindex` calls enforceTwinProtection itself.
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

// prepareTwin loads the config once and builds two connected clients —
// one each for --source and --target. Errors from either probe are
// surfaced verbatim because the operator's first reaction is "which
// cluster failed?" and an aggregated "couldn't migrate" loses that.
//
// The same config.Config drives every downstream lookup the twin
// commands need (protection tier, auth type, format default), so a
// single LoadDefault per invocation is enough — earlier revisions
// re-read the file for each side and each gate, which on slow
// filesystems showed up in the trace.
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
// esopsmigrate.Endpoint. The role argument ("source" / "target") only
// shapes the error message — the resolved capabilities are the same on
// both sides.
//
// All capability fields are wired up regardless of which role this is —
// the migrate package only calls what it needs from each side, and
// keeping the wiring symmetric makes the command actions identical.
// Source-side auth is forwarded as EndpointAuth so the target's
// /_reindex body can fetch from the source. mTLS / SigV4 source-side
// auth is rejected at the reindex preflight: remote reindex's
// source.remote block does not honour either form.
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
		Context:          resolved,
		URL:              cc.URL,
		Info:             c.Info,
		Protection:       cc.Protection,
		AuthType:         cc.Auth.Type,
		Auth:             buildEndpointAuth(cc),
		Indices:          c.Indices,
		Mappings:         c.Mappings,
		Whitelist:        c.RemoteReindex,
		DocCount:         c.DocCount,
		Search:           c.Search,
		Reindex:          c.Reindex,
		TasksCancel:      c.TasksCancel,
		Health:           c.Health,
		IndexSettingsSet: c.IndexSettingsSet,
		AliasUpdate:      c.AliasUpdate,
		AliasInspect:     c.AliasInspect,
	}, nil
}

// buildEndpointAuth derives the migrate-layer EndpointAuth from a
// resolved context. The source.remote block in /_reindex honours
// username/password (basic), bearer (Authorization: Bearer …), and the
// ES api_key (Authorization: ApiKey …). mTLS keypairs and SigV4
// signing are not forwarded — the cluster's own connector cannot use
// either against the source. They produce an empty EndpointAuth here,
// equivalent to "no source-side credentials"; the reindex command's
// preflight separately surfaces the unsupported-source-auth case via
// sourceAuthIsForwardable so check / plan / verify (which don't need
// source credentials at all) keep working unchanged.
func buildEndpointAuth(cc config.Context) esopsmigrate.EndpointAuth {
	switch cc.Auth.Type {
	case "basic":
		return esopsmigrate.EndpointAuth{
			Username: cc.Auth.Username,
			Password: cc.Auth.Password,
		}
	case "bearer":
		return esopsmigrate.EndpointAuth{Token: cc.Auth.Token}
	case "api_key":
		return esopsmigrate.EndpointAuth{APIKey: cc.Auth.APIKey}
	default:
		return esopsmigrate.EndpointAuth{}
	}
}

// sourceAuthIsForwardable reports whether a context's auth.type can be
// forwarded into the target's /_reindex source.remote block. Used by
// `migrate reindex` to surface a precise error up-front when the
// operator points it at an mTLS or SigV4 source — those auth schemes
// can't be carried through the cluster's source-fetch connector, so the
// reindex would otherwise 401 mid-run with an opaque cluster message.
// check / plan / verify don't call this; they don't use the source-side
// credentials at all.
func sourceAuthIsForwardable(authType string) (ok bool, reason string) {
	switch authType {
	case "", "none", "basic", "bearer", "api_key":
		return true, ""
	case "mtls":
		return false, "auth.type=mtls is not supported by /_reindex source.remote — terminate mTLS at a proxy that issues bearer/basic credentials"
	case "sigv4":
		return false, "auth.type=sigv4 is not supported by /_reindex source.remote — the cluster's source-fetch connector cannot SigV4-sign requests"
	default:
		return false, fmt.Sprintf("auth.type=%q is not recognised by esops migrate reindex", authType)
	}
}

// enforceTwinProtection runs the protection-tier check on both
// endpoints when a migrate command is destructive (reindex). The
// migrate.Endpoint carries the resolved Protection tier so we don't
// have to re-load config or thread a config.Context through the
// migrate business package; cmdsetup.CheckProtection enforces the same
// rules cmdsetup.Prepare does for single-context commands.
//
// Reading state on the source is allowed against any tier; *writing*
// to the target is what triggers the prod / staging rule. The source
// side carries protection too because operators sometimes restrict
// reads — surface that here rather than letting the cluster reject the
// fetch mid-reindex.
func enforceTwinProtection(cmd *cli.Command, s twinSetup) error {
	for _, ep := range []esopsmigrate.Endpoint{s.Source, s.Target} {
		cc := config.Context{Protection: ep.Protection}
		if err := cmdsetup.CheckProtection(cc, cmd, true); err != nil {
			return fmt.Errorf("%w: %s", err, ep.Context)
		}
	}
	return nil
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
