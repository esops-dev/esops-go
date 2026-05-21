// Package cli assembles the urfave/cli command tree and shared
// helpers. Command implementations live in subpackages; this file is
// the registration surface.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/completion"
	configcmd "github.com/esops-dev/esops-go/internal/cli/config"
	datastreamcmd "github.com/esops-dev/esops-go/internal/cli/datastream"
	indexcmd "github.com/esops-dev/esops-go/internal/cli/index"
	migratecmd "github.com/esops-dev/esops-go/internal/cli/migrate"
	"github.com/esops-dev/esops-go/internal/cli/ops"
	pipelinecmd "github.com/esops-dev/esops-go/internal/cli/pipeline"
	snapshotcmd "github.com/esops-dev/esops-go/internal/cli/snapshot"
	transformcmd "github.com/esops-dev/esops-go/internal/cli/transform"
	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/internal/version"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/config"
)

// Run builds and executes the root command. Errors bubble up to main,
// which maps them to stable exit codes.
func Run(ctx context.Context, args []string) error {
	app := &cli.Command{
		Name:                            "esops",
		Usage:                           "Operations CLI for self-managed Elasticsearch and OpenSearch clusters",
		Version:                         version.Version,
		Flags:                           globalFlags(),
		EnableShellCompletion:           true,
		ConfigureShellCompletionCommand: completion.Configure,
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"context": completion.SuggestContexts,
			"cluster": completion.SuggestContexts,
		}),
		Before: initLogger,
		Commands: []*cli.Command{
			ops.Command(),
			indexcmd.Command(),
			datastreamcmd.Command(),
			snapshotcmd.Command(),
			pipelinecmd.Command(),
			transformcmd.Command(),
			configcmd.Command(),
			migratecmd.Command(),
		},
		CustomRootCommandHelpTemplate: rootHelpTemplate,
	}
	return wrapUsageError(app.Run(ctx, args))
}

// rootHelpTemplate mirrors urfave/cli's RootCommandHelpTemplate but
// renders the COMMANDS section in three passes — user commands, then
// completion, then help — because the library auto-appends help before
// completion to the Commands slice and offers no knob to influence that
// ordering.
const rootHelpTemplate = `NAME:
   {{template "helpNameTemplate" .}}

USAGE:
   {{if .UsageText}}{{wrap .UsageText 3}}{{else}}{{.FullName}} {{if .VisibleFlags}}[global options]{{end}}{{if .VisibleCommands}} [command [command options]]{{end}}{{if .ArgsUsage}} {{.ArgsUsage}}{{else}}{{if .Arguments}} [arguments...]{{end}}{{end}}{{end}}{{if .Version}}{{if not .HideVersion}}

VERSION:
   {{.Version}}{{end}}{{end}}{{if .Description}}

DESCRIPTION:
   {{template "descriptionTemplate" .}}{{end}}
{{- if len .Authors}}

AUTHOR{{template "authorsTemplate" .}}{{end}}{{if .Commands}}

COMMANDS:{{ $cv := offsetCommands .Commands 5}}{{range .Commands}}{{if and (not .Hidden) (ne .Name "help") (ne .Name "completion")}}
   {{$s := join .Names ", "}}{{$s}}{{ $sp := subtract $cv (offset $s 3) }}{{ indent $sp ""}}{{wrap .Usage $cv}}{{end}}{{end}}{{range .Commands}}{{if and (not .Hidden) (eq .Name "completion")}}
   {{$s := join .Names ", "}}{{$s}}{{ $sp := subtract $cv (offset $s 3) }}{{ indent $sp ""}}{{wrap .Usage $cv}}{{end}}{{end}}{{range .Commands}}{{if eq .Name "help"}}
   {{$s := join .Names ", "}}{{$s}}{{ $sp := subtract $cv (offset $s 3) }}{{ indent $sp ""}}{{wrap .Usage $cv}}{{end}}{{end}}{{end}}{{if .VisibleFlagCategories}}

GLOBAL OPTIONS:{{template "visibleFlagCategoryTemplate" .}}{{else if .VisibleFlags}}

GLOBAL OPTIONS:{{template "visibleFlagTemplate" .}}{{end}}{{if .Copyright}}

COPYRIGHT:
   {{template "copyrightTemplate" .}}{{end}}
`

// wrapUsageError wraps urfave's flag-validation errors with
// client.ErrUsage so they map to exit code 2. urfave's typed errors
// (errRequiredFlags, mutuallyExclusiveGroup, …) are unexported, so we
// match on the well-known message prefixes the library emits. A
// best-effort match: a missed prefix degrades to exit code 1, which is
// the existing behaviour, not a regression.
func wrapUsageError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, prefix := range []string{
		"Required flag ",                          // single missing required flag
		"Required flags ",                         // multiple missing required flags
		"flag provided but not defined",           // unknown flag
		"flag needs an argument",                  // bare --foo with a string flag
		"option ",                                 // mutually-exclusive flag groups
		"one of these flags needs to be provided", // exclusive-required group
	} {
		if strings.Contains(msg, prefix) {
			return fmt.Errorf("%w: %s", client.ErrUsage, msg)
		}
	}
	return err
}

// initLogger configures slog from flag values, falling back to
// config.Defaults and finally to the flag's built-in default. Runs once
// at Before time so every subcommand observes the same logger. Config
// load is best-effort: if no config is found the command may still be
// useful (e.g. `esops --help`), so we don't fail here.
//
// A bad logger value (typo in defaults.log_level, --log-format=yamll,
// unwritable --log-file) must not block the command tree — notably
// `config validate`, whose entire job is to *surface* this exact class
// of bug. On failure we print a one-line warning to stderr, retry with
// safe builtins, and proceed.
func initLogger(ctx context.Context, cmd *cli.Command) (context.Context, error) {
	defaults := readDefaults(cmd.String("config"))
	level := resolveSetting(cmd, "log-level", defaults.LogLevel, "info")
	format := resolveSetting(cmd, "log-format", defaults.LogFormat, defaultLogFormat())
	logFile := resolveSetting(cmd, "log-file", defaults.LogFile, "")
	if err := logging.Init(level, format, logFile); err != nil {
		fmt.Fprintf(os.Stderr, "esops: warning: %v; falling back to default logger\n", err)
		// Builtin fallback is guaranteed valid; a second failure would
		// indicate a programming error, surface it.
		if err := logging.Init("info", "text", ""); err != nil {
			return ctx, err
		}
	}
	return ctx, nil
}

// readDefaults loads the config file quietly for the Before hook, so
// logger configuration can consult Defaults. Uses config.Parse (not
// Load) to skip the safety checks — those run once, visibly, when the
// command action calls cmdsetup.Prepare and the logger is ready to
// surface their warnings. Returns a zero Defaults on any failure.
func readDefaults(explicit string) config.Defaults {
	path, err := config.Resolve(explicit)
	if err != nil {
		return config.Defaults{}
	}
	cfg, err := config.Parse(path)
	if err != nil {
		return config.Defaults{}
	}
	return cfg.Defaults
}

// resolveSetting picks a string setting in priority order: explicit
// flag > config file > built-in default.
func resolveSetting(cmd *cli.Command, flag, fromConfig, builtin string) string {
	if cmd.IsSet(flag) {
		return cmd.String(flag)
	}
	if fromConfig != "" {
		return fromConfig
	}
	return builtin
}

// defaultLogFormat returns "json" under CI and "text" otherwise. Used
// when neither the flag nor the config file set a preference.
func defaultLogFormat() string {
	if inCI() {
		return "json"
	}
	return "text"
}

// inCI reports whether we're running under a CI system. CI=true is the
// de-facto standard (GitHub Actions, GitLab, CircleCI, Travis). When set,
// we default --log-format to json and --no-prompt to true.
func inCI() bool {
	v := os.Getenv("CI")
	return v == "true" || v == "1"
}

func globalFlags() []cli.Flag {
	noPromptDefault := inCI()
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Usage:   "Path to YAML config file (overrides ESOPS_CONFIG and the XDG search)",
		},
		&cli.StringFlag{
			Name:    "context",
			Aliases: []string{"cluster"},
			Usage:   "Named cluster from config (overrides current-context)",
		},
		&cli.StringFlag{
			Name:  "url",
			Usage: "Override: single cluster URL (bypasses context lookup)",
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Usage: "HTTP timeout",
			Value: 30 * time.Second,
		},
		&cli.BoolFlag{
			Name:  "insecure",
			Usage: "Skip TLS verification (last resort)",
		},
		&cli.StringFlag{
			Name:  "cacert",
			Usage: "Custom CA bundle path",
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "Output format: table | json | ndjson | yaml | wide (overrides defaults.output in config)",
		},
		&cli.StringFlag{
			Name:  "log-level",
			Usage: "Log level: debug | info | warn | error (overrides defaults.log_level)",
		},
		&cli.StringFlag{
			Name:  "log-format",
			Usage: "Log format: text | json (overrides defaults.log_format; auto-json under CI=true)",
		},
		&cli.StringFlag{
			Name:  "log-file",
			Usage: "Append log lines to PATH instead of stderr (file created with mode 0600; overrides defaults.log_file)",
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Report what would happen without making changes (where supported)",
		},
		&cli.BoolFlag{
			Name:  "no-prompt",
			Usage: "Fail instead of prompting (auto-enabled under CI=true)",
			Value: noPromptDefault,
		},
		&cli.BoolFlag{
			Name:  "i-know-this-is-prod",
			Usage: "Required for destructive operations against a prod-protected context",
		},
		&cli.DurationFlag{
			Name:  "complete-timeout",
			Usage: "Hard timeout for cluster-backed tab-completion lookups (e.g. --context, --index, --repository)",
			Value: completion.DefaultCompleteTimeout,
		},
		&cli.DurationFlag{
			Name:  "complete-cache",
			Usage: "TTL for cached tab-completion results under $XDG_CACHE_HOME/esops/complete; 0 disables caching",
			Value: completion.DefaultCompleteCacheTTL,
		},
	}
}
