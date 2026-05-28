package cmdsetup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// cmdWith runs a stub urfave/cli app just long enough to parse the
// given argv and hand back the resulting *cli.Command. Tests use it to
// construct realistic flag state without hand-rolling a fake, so the
// code exercised in tests is the same state the production harness
// exposes to its own helpers.
func cmdWith(t *testing.T, args ...string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	app := &cli.Command{
		Name: "test",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "dry-run"},
			&cli.BoolFlag{Name: "no-prompt"},
			&cli.BoolFlag{Name: "i-know-this-is-prod"},
			&cli.StringFlag{Name: "output"},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	if err := app.Run(context.Background(), append([]string{"test"}, args...)); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if captured == nil {
		t.Fatal("test stub did not capture cmd")
	}
	return captured
}

func TestResolveHiddenFlags_table(t *testing.T) {
	cases := []struct {
		name            string
		hidden, exclude bool
		want            bool
		wantErr         bool
	}{
		{name: "neither", hidden: false, exclude: false, want: false},
		{name: "hidden", hidden: true, exclude: false, want: true},
		{name: "exclude-hidden", hidden: false, exclude: true, want: false},
		{name: "both", hidden: true, exclude: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveHiddenFlags(tc.hidden, tc.exclude)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for hidden=%v exclude=%v", tc.hidden, tc.exclude)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveHiddenFlags: %v", err)
			}
			if got != tc.want {
				t.Errorf("got = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckProtection_nonDestructiveAlwaysAllowed(t *testing.T) {
	cases := []string{"", "none", "staging", "prod"}
	for _, tier := range cases {
		t.Run("tier="+tier, func(t *testing.T) {
			cmd := cmdWith(t)
			err := CheckProtection(config.Context{Protection: tier}, cmd, false)
			if err != nil {
				t.Errorf("non-destructive on %q returned error: %v", tier, err)
			}
		})
	}
}

func TestCheckProtection_dryRunAlwaysAllowed(t *testing.T) {
	for _, tier := range []string{"none", "staging", "prod"} {
		t.Run("tier="+tier, func(t *testing.T) {
			cmd := cmdWith(t, "--dry-run")
			err := CheckProtection(config.Context{Protection: tier}, cmd, true)
			if err != nil {
				t.Errorf("dry-run destructive on %q returned error: %v", tier, err)
			}
		})
	}
}

func TestCheckProtection_noneAllowsDestructive(t *testing.T) {
	cmd := cmdWith(t)
	if err := CheckProtection(config.Context{Protection: ""}, cmd, true); err != nil {
		t.Errorf("unprotected context rejected destructive: %v", err)
	}
	if err := CheckProtection(config.Context{Protection: "none"}, cmd, true); err != nil {
		t.Errorf("protection=none rejected destructive: %v", err)
	}
}

func TestCheckProtection_stagingRequiresNoPrompt(t *testing.T) {
	// No --no-prompt: must refuse.
	cmd := cmdWith(t)
	err := CheckProtection(config.Context{Protection: "staging"}, cmd, true)
	if !errors.Is(err, ErrProtected) {
		t.Errorf("staging without --no-prompt: err=%v, want ErrProtected", err)
	}

	// With --no-prompt: must allow.
	cmd = cmdWith(t, "--no-prompt")
	if err := CheckProtection(config.Context{Protection: "staging"}, cmd, true); err != nil {
		t.Errorf("staging with --no-prompt returned error: %v", err)
	}
}

func TestCheckProtection_prodRequiresAcknowledgement(t *testing.T) {
	// Without --i-know-this-is-prod: refuse, even with --no-prompt.
	cmd := cmdWith(t, "--no-prompt")
	err := CheckProtection(config.Context{Protection: "prod"}, cmd, true)
	if !errors.Is(err, ErrProtected) {
		t.Errorf("prod without --i-know-this-is-prod: err=%v, want ErrProtected", err)
	}

	// With --i-know-this-is-prod: allow.
	cmd = cmdWith(t, "--i-know-this-is-prod")
	if err := CheckProtection(config.Context{Protection: "prod"}, cmd, true); err != nil {
		t.Errorf("prod with --i-know-this-is-prod returned error: %v", err)
	}
}

func TestCheckProtection_unknownTier(t *testing.T) {
	cmd := cmdWith(t)
	err := CheckProtection(config.Context{Protection: "whatever"}, cmd, true)
	if err == nil || !strings.Contains(err.Error(), "unknown protection tier") {
		t.Errorf("unknown tier: err=%v, want 'unknown protection tier' message", err)
	}
}

func TestResolveFormat_flagBeatsConfig(t *testing.T) {
	cmd := cmdWith(t, "--output", "json")
	f, err := resolveFormat(cmd, "yaml", nil)
	if err != nil {
		t.Fatalf("resolveFormat: %v", err)
	}
	if f != output.FormatJSON {
		t.Errorf("format = %q, want json (flag wins over config default)", f)
	}
}

func TestResolveFormat_configBeatsBuiltin(t *testing.T) {
	cmd := cmdWith(t) // no --output
	f, err := resolveFormat(cmd, "yaml", nil)
	if err != nil {
		t.Fatalf("resolveFormat: %v", err)
	}
	if f != output.FormatYAML {
		t.Errorf("format = %q, want yaml (config when flag unset)", f)
	}
}

func TestResolveFormat_defaultsToTableWhenNothingSet(t *testing.T) {
	cmd := cmdWith(t)
	f, err := resolveFormat(cmd, "", nil)
	if err != nil {
		t.Fatalf("resolveFormat: %v", err)
	}
	if f != output.FormatTable {
		t.Errorf("format = %q, want table (built-in default)", f)
	}
}

func TestResolveFormat_rejectsOutsideAllowlist(t *testing.T) {
	cmd := cmdWith(t, "--output", "ndjson")
	_, err := resolveFormat(cmd, "", FormatsStandard)
	if err == nil {
		t.Fatal("expected ndjson to be rejected against FormatsStandard")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error should mention 'not supported', got: %v", err)
	}
}

func TestResolveFormat_acceptsWithinAllowlist(t *testing.T) {
	cmd := cmdWith(t, "--output", "ndjson")
	f, err := resolveFormat(cmd, "", FormatsStreaming)
	if err != nil {
		t.Fatalf("resolveFormat with ndjson against FormatsStreaming: %v", err)
	}
	if f != output.FormatNDJSON {
		t.Errorf("format = %q, want ndjson", f)
	}
}

func TestResolveFormat_unknownFormat(t *testing.T) {
	cmd := cmdWith(t, "--output", "xml")
	_, err := resolveFormat(cmd, "", nil)
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

// cmdWithOnError extends cmdWith with the --on-error / --verbose flags
// so OnErrorPolicy / Verbose can be exercised the same way the
// production code reads them.
func cmdWithOnError(t *testing.T, args ...string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	app := &cli.Command{
		Name: "test",
		Flags: append(
			[]cli.Flag{
				&cli.BoolFlag{Name: "dry-run"},
				&cli.BoolFlag{Name: "no-prompt"},
				&cli.BoolFlag{Name: "i-know-this-is-prod"},
				&cli.StringFlag{Name: "output"},
			},
			OnErrorFlags()...,
		),
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	if err := app.Run(context.Background(), append([]string{"test"}, args...)); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if captured == nil {
		t.Fatal("test stub did not capture cmd")
	}
	return captured
}

func TestOnErrorPolicy_defaultIsAbort(t *testing.T) {
	cmd := cmdWithOnError(t)
	got, err := OnErrorPolicy(cmd)
	if err != nil {
		t.Fatalf("OnErrorPolicy: %v", err)
	}
	if got != types.OnErrorAbort {
		t.Errorf("default policy = %q, want abort", got)
	}
}

func TestOnErrorPolicy_acceptsValidValues(t *testing.T) {
	cases := []struct {
		flag string
		want types.OnErrorPolicy
	}{
		{"abort", types.OnErrorAbort},
		{"skip", types.OnErrorSkip},
		{"rollback", types.OnErrorRollback},
	}
	for _, c := range cases {
		cmd := cmdWithOnError(t, "--on-error", c.flag)
		got, err := OnErrorPolicy(cmd)
		if err != nil {
			t.Errorf("OnErrorPolicy(%q): %v", c.flag, err)
			continue
		}
		if got != c.want {
			t.Errorf("OnErrorPolicy(%q) = %q, want %q", c.flag, got, c.want)
		}
	}
}

func TestOnErrorPolicy_rejectsUnknownAsUsageError(t *testing.T) {
	cmd := cmdWithOnError(t, "--on-error", "continue")
	_, err := OnErrorPolicy(cmd)
	if err == nil {
		t.Fatal("expected usage error for unknown --on-error value")
	}
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err should wrap ErrUsage (exit 2), got %v", err)
	}
}

func TestVerbose_defaultsFalse(t *testing.T) {
	if Verbose(cmdWithOnError(t)) {
		t.Error("--verbose default = true, want false")
	}
}

func TestVerbose_setExplicit(t *testing.T) {
	if !Verbose(cmdWithOnError(t, "--verbose")) {
		t.Error("--verbose set: Verbose() returned false")
	}
}

// cmdWithTree runs a stub esops-shaped app — a root "esops" command
// with a nested "ops health" subcommand — so commandPath can be
// tested against the same shape urfave/cli builds for the real CLI.
func cmdWithTree(t *testing.T, argv ...string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	root := &cli.Command{
		Name: "esops",
		Commands: []*cli.Command{
			{
				Name: "ops",
				Commands: []*cli.Command{
					{
						Name: "health",
						Action: func(_ context.Context, c *cli.Command) error {
							captured = c
							return nil
						},
					},
				},
			},
		},
	}
	if err := root.Run(context.Background(), argv); err != nil {
		t.Fatalf("root.Run: %v", err)
	}
	if captured == nil {
		t.Fatal("test stub did not capture cmd")
	}
	return captured
}

func TestCommandPath_stripsRootBinaryName(t *testing.T) {
	// "esops ops health" must record as "ops health" — the spec's
	// command-field example shows "migrate reindex", not "esops
	// migrate reindex".
	cmd := cmdWithTree(t, "esops", "ops", "health")
	got := commandPath(cmd)
	if got != "ops health" {
		t.Errorf("commandPath = %q, want %q", got, "ops health")
	}
}
