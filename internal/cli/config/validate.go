package config

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
	"github.com/zalando/go-keyring"

	"github.com/esops-dev/esops-go/pkg/client"
	esopsconfig "github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/internal/output"
)

// severity classifies a finding. Errors fail validation; warnings only
// fail under --strict. Two levels keeps the CLI exit contract simple:
// the operator either has a CI-blocking problem or doesn't.
type severity string

const (
	severityWarning severity = "warning"
	severityError   severity = "error"
)

// finding is a single lint result. JSON tags drive --output json; the
// table renderer reads the same fields.
type finding struct {
	Severity severity `json:"severity" yaml:"severity"`
	File     string   `json:"file" yaml:"file"`
	Path     string   `json:"path" yaml:"path"`
	Message  string   `json:"message" yaml:"message"`
}

// validProtectionTiers mirrors cmdsetup.CheckProtection's accepted set.
// "" is allowed and behaves as "none".
var validProtectionTiers = map[string]bool{
	"":        true,
	"none":    true,
	"staging": true,
	"prod":    true,
}

func validateCommand() *cli.Command {
	return &cli.Command{
		Name:  "validate",
		Usage: "Lint the config file without contacting any cluster (CI-friendly)",
		Description: `Run the runtime config loader against the resolved file and report
every problem it finds, without touching the network. Intended for
pre-merge CI jobs where a single typo in a fleet-wide config would
otherwise only surface at the first command invocation.

Checks performed:
  - YAML parses and the document matches the expected structure.
  - Every named context has required auth fields per type, a
    well-formed URL, and any referenced TLS material (client cert /
    key / CA) exists on disk and parses.
  - Every secret indirection resolves at availability level —
    ${env:VAR} checks the variable is set, ${file:PATH} checks the
    file exists and is readable, ${keyring:SVC/ACCT} checks the
    entry is present. Secret values themselves are not logged.
  - Plaintext-secret rules: a world-readable config with inline
    secrets is an error (unless ESOPS_ALLOW_INSECURE_CONFIG=1, which
    downgrades to a warning); inline plaintext at any mode warns
    (unless ESOPS_ALLOW_PLAINTEXT_SECRETS=1).
  - Protection tier values are one of: none, staging, prod.
  - current-context (if set) names an existing context.
  - defaults.output / log_level / log_format match what the runtime
    parsers accept (validated via the same code paths).

Out of scope: connecting to clusters, probing dialect, validating
that role descriptors match a live security model. That's
'migrate check'. The distinction is "is my config sane" vs "does my
config match reality" — keep them separate so this lint can run
with no network.

Exit codes: 0 on success (errors absent, warnings allowed); 2 on
any error, or — with --strict — on any warning.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "strict",
				Usage: "Treat warnings as failures (warnings exit 2 instead of 0)",
			},
		},
		Action: runValidate,
	}
}

func runValidate(_ context.Context, cmd *cli.Command) error {
	format, err := parseValidateFormat(cmd.String("output"))
	if err != nil {
		return client.UsageError("%s", err.Error())
	}

	path, findings := collectFindings(cmd.String("config"))
	if err := renderFindings(os.Stdout, path, findings, format); err != nil {
		return err
	}

	return validateExitStatus(findings, cmd.Bool("strict"))
}

// parseValidateFormat accepts only table and json — yaml/wide/ndjson
// don't fit a homogeneous findings list, and excluding them up-front
// catches operator typos at flag-parse time instead of producing
// surprising output.
func parseValidateFormat(s string) (output.Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "table":
		return output.FormatTable, nil
	case "json":
		return output.FormatJSON, nil
	default:
		return "", fmt.Errorf("unsupported output format %q for config validate (accepted: table, json)", s)
	}
}

// collectFindings resolves the config path and runs every check,
// returning the path it inspected (for the file column) and the
// accumulated findings. A failure to resolve the path or parse the
// YAML short-circuits with a single error finding — without a parsed
// document the structural checks have nothing to inspect.
func collectFindings(explicit string) (string, []finding) {
	path, err := esopsconfig.Resolve(explicit)
	if err != nil {
		// Path resolution failure: nothing else to inspect. The empty
		// file column matches what other esops commands print when no
		// config is in play.
		return "", []finding{{
			Severity: severityError,
			File:     "",
			Path:     "",
			Message:  err.Error(),
		}}
	}

	cfg, parseErr := esopsconfig.Parse(path)
	if parseErr != nil {
		return path, []finding{{
			Severity: severityError,
			File:     path,
			Path:     "",
			Message:  parseErr.Error(),
		}}
	}

	var out []finding
	out = append(out, checkSafety(path, cfg)...)
	out = append(out, checkDefaults(path, cfg)...)
	out = append(out, checkCurrentContext(path, cfg)...)
	out = append(out, checkContexts(path, cfg)...)
	return path, out
}

// checkDefaults lints the top-level `defaults` block against the same
// parsers the runtime uses (output.Parse for output, logging.ValidateLevel
// / logging.ValidateFormat for the logger fields). Sharing the parsers
// is the drift-avoidance pattern: a clean lint here is a guarantee that
// Init and resolveFormat will accept the same values at process start.
// Empty strings are accepted — they mean "leave it to the runtime
// default", which is how the loader treats them too.
func checkDefaults(path string, cfg esopsconfig.Config) []finding {
	var out []finding
	if cfg.Defaults.Output != "" {
		if _, err := output.Parse(cfg.Defaults.Output); err != nil {
			out = append(out, finding{
				Severity: severityError,
				File:     path,
				Path:     "defaults.output",
				Message:  err.Error(),
			})
		}
	}
	if cfg.Defaults.LogLevel != "" {
		if err := logging.ValidateLevel(cfg.Defaults.LogLevel); err != nil {
			out = append(out, finding{
				Severity: severityError,
				File:     path,
				Path:     "defaults.log_level",
				Message:  err.Error(),
			})
		}
	}
	if cfg.Defaults.LogFormat != "" {
		if err := logging.ValidateFormat(cfg.Defaults.LogFormat); err != nil {
			out = append(out, finding{
				Severity: severityError,
				File:     path,
				Path:     "defaults.log_format",
				Message:  err.Error(),
			})
		}
	}
	return out
}

// checkSafety surfaces the loader's runtime safety rules as findings.
// World-readable + plaintext is an error unless ESOPS_ALLOW_INSECURE_CONFIG=1
// (which downgrades to a warning); plaintext at any mode warns unless
// ESOPS_ALLOW_PLAINTEXT_SECRETS=1. Drift between this and the loader
// is a bug — both code paths share esopsconfig.ContextsWithPlaintextSecret
// and esopsconfig.IsWorldReadable as their single source of truth.
func checkSafety(path string, cfg esopsconfig.Config) []finding {
	plaintextCtxs := esopsconfig.ContextsWithPlaintextSecret(cfg)
	if len(plaintextCtxs) == 0 {
		return nil
	}

	worldReadable, statErr := esopsconfig.IsWorldReadable(path)
	if statErr != nil {
		return []finding{{
			Severity: severityError,
			File:     path,
			Path:     "",
			Message:  fmt.Sprintf("checking config permissions: %v", statErr),
		}}
	}

	var out []finding
	if worldReadable {
		msg := fmt.Sprintf(
			"config is world-readable and contains inline plaintext secrets for context(s) [%s]; tighten permissions (chmod 600 %s) or set ESOPS_ALLOW_INSECURE_CONFIG=1",
			strings.Join(plaintextCtxs, ", "), path)
		sev := severityError
		if os.Getenv("ESOPS_ALLOW_INSECURE_CONFIG") == "1" {
			sev = severityWarning
		}
		out = append(out, finding{Severity: sev, File: path, Path: "", Message: msg})
		// When the world-readable error fires we don't double-up with
		// per-context plaintext warnings — the operator already has
		// the list of offending contexts in the message above.
		return out
	}

	if os.Getenv("ESOPS_ALLOW_PLAINTEXT_SECRETS") == "1" {
		return out
	}
	for _, name := range plaintextCtxs {
		out = append(out, finding{
			Severity: severityWarning,
			File:     path,
			Path:     "contexts." + name + ".auth",
			Message:  "inline plaintext secret; prefer ${env:...}, ${file:...}, or ${keyring:...} (silence with ESOPS_ALLOW_PLAINTEXT_SECRETS=1)",
		})
	}
	return out
}

// checkCurrentContext verifies the top-level current-context pointer
// names a context defined in the file. An empty pointer is fine — a
// command can always specify --context explicitly.
//
// current-context is the only cross-reference in the config schema as
// of this writing. The spec's "Cross-references resolve … etc." leaves
// room for future ones (e.g. inheritance, aliases); add cases here when
// they land rather than spreading the check across packages.
func checkCurrentContext(path string, cfg esopsconfig.Config) []finding {
	if cfg.CurrentContext == "" {
		return nil
	}
	if _, ok := cfg.Contexts[cfg.CurrentContext]; ok {
		return nil
	}
	return []finding{{
		Severity: severityError,
		File:     path,
		Path:     "current-context",
		Message: fmt.Sprintf("current-context %q does not match any defined context (defined: %s)",
			cfg.CurrentContext, strings.Join(sortedNames(cfg.Contexts), ", ")),
	}}
}

// checkContexts visits every context in alphabetical order and runs
// the per-context checks. Sorted iteration is load-bearing — operators
// paste these into tickets and we don't want two consecutive runs to
// reorder findings.
func checkContexts(path string, cfg esopsconfig.Config) []finding {
	var out []finding
	for _, name := range sortedNames(cfg.Contexts) {
		c := cfg.Contexts[name]
		base := "contexts." + name
		out = append(out, checkAddresses(path, base, c)...)
		out = append(out, checkAuth(path, base, c)...)
		out = append(out, checkTLS(path, base, c)...)
		out = append(out, checkSecretAvailability(path, base, c)...)
		out = append(out, checkProtection(path, base, c)...)
	}
	return out
}

// checkAddresses verifies the context has at least one URL and that
// every URL parses with an http or https scheme. Pre-resolution view:
// command-line --url overrides aren't visible here, so a context that
// relies on them will still show "no URL configured" — that's correct
// for a file-level lint.
func checkAddresses(path, base string, c esopsconfig.Context) []finding {
	addrs := c.Addresses()
	if len(addrs) == 0 {
		return []finding{{
			Severity: severityError,
			File:     path,
			Path:     base + ".url",
			Message:  "context has no url or urls configured",
		}}
	}
	var out []finding
	for i, addr := range addrs {
		field := base + ".url"
		if len(c.URLs) > 0 {
			field = fmt.Sprintf("%s.urls[%d]", base, i)
		}
		if msg := validateURL(addr); msg != "" {
			out = append(out, finding{
				Severity: severityError,
				File:     path,
				Path:     field,
				Message:  msg,
			})
		}
	}
	return out
}

// validateURL rejects empty strings, URLs without a scheme, anything
// that isn't http(s), and anything without a host. Matches what the
// upstream ES/OS transports require — we'd otherwise fail at first
// request with a less actionable error.
func validateURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "url is empty"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("url %q is malformed: %v", raw, err)
	}
	if u.Scheme == "" {
		return fmt.Sprintf("url %q is missing a scheme (expected http:// or https://)", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Sprintf("url %q has unsupported scheme %q (expected http or https)", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Sprintf("url %q is missing a host", raw)
	}
	return ""
}

// checkAuth runs the loader's own ValidateAuth so the structural rules
// (required + forbidden fields per auth.type) stay single-sourced. Any
// drift would be a bug — that's the whole point of importing it here
// rather than redefining the rule table.
func checkAuth(path, base string, c esopsconfig.Context) []finding {
	if err := esopsconfig.ValidateAuth(c.Auth); err != nil {
		return []finding{{
			Severity: severityError,
			File:     path,
			Path:     base + ".auth",
			Message:  err.Error(),
		}}
	}
	return nil
}

// checkTLS confirms that any referenced TLS material is loadable. We
// run the same primitives the runtime transport uses (tls.LoadX509KeyPair
// for the keypair, AppendCertsFromPEM for the CA bundle) so a config
// that lints cleanly is guaranteed to be loadable at probe time.
func checkTLS(path, base string, c esopsconfig.Context) []finding {
	var out []finding

	if c.TLS.CACert != "" {
		// Read + parse the CA bundle. ReadFile is the same call the
		// runtime makes, so a clean lint here means the runtime will
		// also succeed.
		pem, err := os.ReadFile(c.TLS.CACert) // #nosec G304 -- operator-supplied CA path being validated
		if err != nil {
			out = append(out, finding{
				Severity: severityError,
				File:     path,
				Path:     base + ".tls.cacert",
				Message:  fmt.Sprintf("reading cacert %q: %v", c.TLS.CACert, err),
			})
		} else if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			out = append(out, finding{
				Severity: severityError,
				File:     path,
				Path:     base + ".tls.cacert",
				Message:  fmt.Sprintf("cacert %q contains no valid PEM certificates", c.TLS.CACert),
			})
		}
	}

	// Client keypair: only meaningful when the operator has actually
	// pointed at one. The loader will reject mtls without both halves
	// (forbidden/required rules above), so we only need to verify the
	// material parses when both are present.
	if c.Auth.ClientCert != "" || c.Auth.ClientKey != "" {
		if c.Auth.ClientCert != "" && c.Auth.ClientKey != "" {
			if _, err := tls.LoadX509KeyPair(c.Auth.ClientCert, c.Auth.ClientKey); err != nil {
				out = append(out, finding{
					Severity: severityError,
					File:     path,
					Path:     base + ".auth.client_cert",
					Message: fmt.Sprintf("loading client keypair (%s, %s): %v",
						c.Auth.ClientCert, c.Auth.ClientKey, err),
				})
			}
		}
	}

	return out
}

// checkSecretAvailability probes every indirection on password,
// api_key, and token at *availability* level only — never reading the
// resolved secret into a logged path. The runtime resolver would fail
// at first command if any of these point at something missing, so
// surfacing it pre-merge is the whole reason this lint exists.
func checkSecretAvailability(path, base string, c esopsconfig.Context) []finding {
	var out []finding
	fields := []struct {
		name string
		val  string
	}{
		{"password", c.Auth.Password},
		{"api_key", c.Auth.APIKey},
		{"token", c.Auth.Token},
	}
	for _, f := range fields {
		if f.val == "" {
			continue
		}
		scheme, arg, ok := esopsconfig.ParseSecretRef(f.val)
		if !ok {
			// Literal value — plaintext warnings are emitted by
			// checkSafety. Nothing to probe here.
			continue
		}
		if msg := probeSecretRef(scheme, arg); msg != "" {
			out = append(out, finding{
				Severity: severityError,
				File:     path,
				Path:     base + ".auth." + f.name,
				Message:  msg,
			})
		}
	}
	return out
}

// probeSecretRef checks that the referenced secret would resolve at
// runtime, without preserving the secret material. env: env-var set;
// file: regular file exists and is openable (we stat to verify the
// kind, then open and immediately close to verify read permission,
// never ingesting the contents — the runtime's os.ReadFile would fail
// on a directory or unreadable file the same way); keyring: entry
// present (the value is fetched but discarded, never logged).
func probeSecretRef(scheme, arg string) string {
	switch scheme {
	case "env":
		if _, ok := os.LookupEnv(arg); !ok {
			return fmt.Sprintf("environment variable %q is not set", arg)
		}
		return ""
	case "file":
		// Stat first so a directory or other non-regular file is
		// rejected with a precise message; os.Open would silently
		// succeed on a directory but the runtime's os.ReadFile would
		// fail at first command. Then verify read permission without
		// ingesting the contents (open + immediate close).
		info, err := os.Stat(arg)
		if err != nil {
			return fmt.Sprintf("secret file %q is not readable: %v", arg, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Sprintf("secret file %q is not a regular file", arg)
		}
		f, err := os.Open(arg) // #nosec G304 -- operator-supplied secret file being validated
		if err != nil {
			return fmt.Sprintf("secret file %q is not readable: %v", arg, err)
		}
		_ = f.Close()
		return ""
	case "keyring":
		service, account, ok := strings.Cut(arg, "/")
		if !ok || service == "" || account == "" {
			return fmt.Sprintf("keyring reference %q must be in the form service/account", arg)
		}
		// keyring.Get returns the secret material; we drop it on the
		// floor and never let it touch a logger or finding message.
		if _, err := keyring.Get(service, account); err != nil {
			return fmt.Sprintf("keyring entry %q is not available: %v", arg, err)
		}
		return ""
	default:
		return fmt.Sprintf("unknown secret indirection scheme %q (expected env, file, or keyring)", scheme)
	}
}

// checkProtection enforces the documented protection-tier set. Empty
// string is allowed and behaves as "none" — matches cmdsetup.
func checkProtection(path, base string, c esopsconfig.Context) []finding {
	if validProtectionTiers[strings.ToLower(c.Protection)] {
		return nil
	}
	return []finding{{
		Severity: severityError,
		File:     path,
		Path:     base + ".protection",
		Message:  fmt.Sprintf("protection %q is not a valid tier (accepted: none, staging, prod)", c.Protection),
	}}
}

// validateExitStatus maps the finding set to the documented exit
// contract: any error fails; under --strict, any warning fails too.
// The returned error wraps client.ErrUsage so cmd/esops maps it to
// exit code 2 (the CI-friendly "operator input was wrong" code).
func validateExitStatus(findings []finding, strict bool) error {
	var errs, warns int
	for _, f := range findings {
		switch f.Severity {
		case severityError:
			errs++
		case severityWarning:
			warns++
		}
	}
	if errs == 0 && (!strict || warns == 0) {
		return nil
	}
	return client.UsageError("config validation failed (errors=%d, warnings=%d)", errs, warns)
}

func renderFindings(w io.Writer, path string, findings []finding, f output.Format) error {
	switch f {
	case output.FormatJSON:
		// json.Encode emits `null` for a nil slice; force an empty
		// array so downstream CI annotation tooling doesn't have to
		// special-case it.
		if findings == nil {
			findings = []finding{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(findings)
	case output.FormatTable, "":
		return renderFindingsTable(w, path, findings)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func renderFindingsTable(w io.Writer, path string, findings []finding) error {
	if len(findings) == 0 {
		dest := path
		if dest == "" {
			dest = "(none)"
		}
		_, err := fmt.Fprintf(w, "Config is valid: %s\n", dest)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SEVERITY\tFILE\tPATH\tMESSAGE")
	for _, f := range findings {
		p := f.Path
		if p == "" {
			p = "-"
		}
		file := f.File
		if file == "" {
			file = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Severity, file, p, f.Message)
	}
	return tw.Flush()
}
