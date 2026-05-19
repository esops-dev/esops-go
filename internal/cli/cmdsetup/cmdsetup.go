// Package cmdsetup contains the shared opening of every `esops` command
// action: load config, resolve the context, layer CLI overrides, enforce
// the protection tier for destructive ops, parse --output (with a
// per-command allowlist), and build a connected client. Its own package
// (rather than internal/cli) so that command subpackages — which
// internal/cli registers — can import it without forming a cycle.
package cmdsetup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/internal/output"
)

// ErrProtected is returned when a destructive operation targets a
// context whose protection tier forbids it without an explicit flag.
// cmd/esops maps this to exit code 9.
var ErrProtected = errors.New("context is protected")

// Spec describes a command's requirements to Prepare. Zero value is
// safe: a read-only command that accepts every output format.
type Spec struct {
	// Destructive marks ops that mutate cluster state; triggers the
	// protection-tier check against the resolved context.
	Destructive bool

	// SupportedFormats is the allowlist of --output values the command
	// can render. Empty means no restriction. Prepare rejects an --output
	// value outside the list up-front, before the cluster is even probed.
	SupportedFormats []output.Format
}

// FormatsStandard is the common format set: table, json, yaml, wide.
// NDJSON is excluded because it's only useful where the renderer streams
// one record per line.
var FormatsStandard = []output.Format{
	output.FormatTable,
	output.FormatJSON,
	output.FormatYAML,
	output.FormatWide,
}

// FormatsStreaming is FormatsStandard plus NDJSON — for list commands
// whose renderer streams one record per line.
var FormatsStreaming = []output.Format{
	output.FormatTable,
	output.FormatJSON,
	output.FormatYAML,
	output.FormatWide,
	output.FormatNDJSON,
}

// Setup is the outcome of Prepare: everything a command action needs
// after flag parsing and config resolution.
type Setup struct {
	ContextName string
	Context     config.Context
	Format      output.Format
	Client      *client.Client
}

// Log emits an info-level structured log with the four boilerplate
// fields every command action carries (event name, context name,
// dialect, cluster version) plus any per-event attrs the caller adds.
// Replaces the per-action repetition of:
//
//	logging.Logger().Info("index.X",
//	    "context", s.ContextName,
//	    "dialect", string(s.Client.Info.Dialect),
//	    "version", s.Client.Info.Version,
//	    "k", v, ...)
//
// with:
//
//	s.Log("index.X", "k", v, ...)
//
// Eleven copies in the index command tree alone, all drift-prone.
func (s Setup) Log(event string, attrs ...any) {
	base := []any{
		"context", s.ContextName,
		"dialect", string(s.Client.Info.Dialect),
		"version", s.Client.Info.Version,
	}
	logging.Logger().Info(event, append(base, attrs...)...)
}

// Prepare is the shared opening of every command action.
func Prepare(ctx context.Context, cmd *cli.Command, spec Spec) (Setup, error) {
	cfg, _, err := config.LoadDefault(cmd.String("config"))
	if err != nil {
		return Setup{}, err
	}
	name, cc, err := cfg.ResolveContext(cmd.String("context"))
	if err != nil {
		return Setup{}, err
	}
	ApplyContextOverrides(&cc, cmd)

	if err := CheckProtection(cc, cmd, spec.Destructive); err != nil {
		return Setup{}, err
	}

	format, err := resolveFormat(cmd, cfg.Defaults.Output, spec.SupportedFormats)
	if err != nil {
		return Setup{}, err
	}

	c, err := cluster.New(ctx, cc)
	if err != nil {
		return Setup{}, err
	}

	logging.Logger().Debug("command.prepare",
		"context", name,
		"dialect", string(c.Info.Dialect),
		"version", c.Info.Version,
	)

	return Setup{
		ContextName: name,
		Context:     cc,
		Format:      format,
		Client:      c,
	}, nil
}

// ApplyContextOverrides layers command-line flags on top of the resolved
// context. URL/timeout/TLS flags override the config; everything else
// is left to the context.
func ApplyContextOverrides(cc *config.Context, cmd *cli.Command) {
	if u := cmd.String("url"); u != "" {
		cc.URL = u
		cc.URLs = nil
	}
	if t := cmd.Duration("timeout"); cmd.IsSet("timeout") || cc.Timeout == 0 {
		cc.Timeout = t
	}
	if cmd.Bool("insecure") {
		cc.TLS.Insecure = true
	}
	if ca := cmd.String("cacert"); ca != "" {
		cc.TLS.CACert = ca
	}
}

// resolveFormat picks the --output value in priority order: explicit
// flag > config Defaults.Output > the flag's built-in default. It then
// parses and validates against the command's format allowlist.
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
	return "", fmt.Errorf("output format %q is not supported for this command (accepted: %s)", f, strings.Join(names, ", "))
}

// ResolveHiddenFlags reconciles --hidden and --exclude-hidden into the
// single "include hidden?" bool the business layer consumes. The two
// flags are mutually exclusive; passing both is a usage error so a
// scripted invocation never quietly resolves one way or the other.
// --exclude-hidden alone is the default behaviour spelt out — useful
// in audited shell history where "this command is meant to skip
// hidden rows" is the kind of intent operators want grep-able.
//
// Shared across the `index` and `datastream` command trees so both
// surfaces stay in step on flag semantics.
func ResolveHiddenFlags(hidden, excludeHidden bool) (bool, error) {
	if hidden && excludeHidden {
		return false, client.UsageError("--hidden and --exclude-hidden are mutually exclusive")
	}
	return hidden, nil
}

// CheckProtection enforces the per-context protection tier. Dry-runs
// and read-only operations are always allowed; destructive operations
// against a `prod` context require an explicit acknowledgement flag,
// and `staging` requires `--no-prompt` (CI-safe) since we don't yet
// have an interactive prompter.
//
// Public so multi-context commands (e.g. `esops migrate reindex`, which
// gates against both source and target tiers) can apply the same rule
// without re-implementing it. Single-context commands get the same
// check via Prepare.
func CheckProtection(cc config.Context, cmd *cli.Command, destructive bool) error {
	if !destructive {
		return nil
	}
	if cmd.Bool("dry-run") {
		return nil
	}
	switch cc.Protection {
	case "", "none":
		return nil
	case "staging":
		if !cmd.Bool("no-prompt") {
			return fmt.Errorf("%w: staging requires --no-prompt (interactive prompt not yet implemented)", ErrProtected)
		}
		return nil
	case "prod":
		if !cmd.Bool("i-know-this-is-prod") {
			return fmt.Errorf("%w: prod context refuses destructive ops without --i-know-this-is-prod", ErrProtected)
		}
		return nil
	default:
		return fmt.Errorf("unknown protection tier %q (expected: none, staging, prod)", cc.Protection)
	}
}
