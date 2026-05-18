package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"go.yaml.in/yaml/v3"

	esopsconfig "github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/internal/output"
)

// redactionPlaceholder replaces inline plaintext secrets when
// --show-secrets is not set. Chosen to be visually obvious in both
// yaml and json output, and short enough not to disrupt alignment
// when an operator eyeballs the file.
const redactionPlaceholder = "REDACTED"

// viewDoc is the render-friendly projection of a Config. Field names
// and casing intentionally mirror the on-disk YAML so that the output
// of `config view --show-secrets` is paste-compatible back into the
// file. Nested structs are pointers so omitempty elides empty blocks
// (yaml.v3 does not treat zero-value structs as empty).
type viewDoc struct {
	CurrentContext string                 `json:"current-context,omitempty" yaml:"current-context,omitempty"`
	Defaults       *viewDefaults          `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	Contexts       map[string]viewContext `json:"contexts,omitempty" yaml:"contexts,omitempty"`
}

type viewDefaults struct {
	Output     string `json:"output,omitempty" yaml:"output,omitempty"`
	LogLevel   string `json:"log_level,omitempty" yaml:"log_level,omitempty"`
	LogFormat  string `json:"log_format,omitempty" yaml:"log_format,omitempty"`
	LogFile    string `json:"log_file,omitempty" yaml:"log_file,omitempty"`
	Timeout    string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	MaxRetries int    `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
	Compress   bool   `json:"compress,omitempty" yaml:"compress,omitempty"`
}

type viewContext struct {
	URL         string    `json:"url,omitempty" yaml:"url,omitempty"`
	URLs        []string  `json:"urls,omitempty" yaml:"urls,omitempty"`
	Auth        *viewAuth `json:"auth,omitempty" yaml:"auth,omitempty"`
	TLS         *viewTLS  `json:"tls,omitempty" yaml:"tls,omitempty"`
	Protection  string    `json:"protection,omitempty" yaml:"protection,omitempty"`
	MaxInFlight int       `json:"max_in_flight,omitempty" yaml:"max_in_flight,omitempty"`
	Timeout     string    `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Compress    bool      `json:"compress,omitempty" yaml:"compress,omitempty"`
}

type viewAuth struct {
	Type       string `json:"type,omitempty" yaml:"type,omitempty"`
	Username   string `json:"username,omitempty" yaml:"username,omitempty"`
	Password   string `json:"password,omitempty" yaml:"password,omitempty"`
	APIKey     string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	Token      string `json:"token,omitempty" yaml:"token,omitempty"`
	ClientCert string `json:"client_cert,omitempty" yaml:"client_cert,omitempty"`
	ClientKey  string `json:"client_key,omitempty" yaml:"client_key,omitempty"`
	Region     string `json:"region,omitempty" yaml:"region,omitempty"`
	Service    string `json:"service,omitempty" yaml:"service,omitempty"`
}

type viewTLS struct {
	CACert   string `json:"cacert,omitempty" yaml:"cacert,omitempty"`
	Insecure bool   `json:"insecure,omitempty" yaml:"insecure,omitempty"`
}

func viewCommand() *cli.Command {
	return &cli.Command{
		Name:  "view",
		Usage: "Print the effective config (secrets redacted by default)",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "show-secrets",
				Usage: "Reveal inline auth secrets in the output (opt-in)",
			},
			&cli.BoolFlag{
				Name:  "all",
				Usage: "Include every context (default: only the current context)",
			},
		},
		Action: runView,
	}
}

func runView(_ context.Context, cmd *cli.Command) error {
	cfg, _, err := esopsconfig.LoadDefault(cmd.String("config"))
	if err != nil {
		return err
	}
	format, err := parseViewFormat(cmd.String("output"))
	if err != nil {
		return err
	}
	doc, err := buildViewDoc(cfg, viewOptions{
		showSecrets: cmd.Bool("show-secrets"),
		all:         cmd.Bool("all"),
		selected:    cmd.String("context"),
	})
	if err != nil {
		return err
	}
	return renderView(os.Stdout, doc, format)
}

// viewOptions controls which slice of the config view renders.
// Defaulting to the single current context (rather than the whole
// file) matches the "show me what I'm about to operate against" read
// of `config view`; --all restores the full-file dump for auditing.
type viewOptions struct {
	showSecrets bool
	all         bool
	// selected names the context to show. Empty falls back to the
	// file's current-context. Ignored when all is true.
	selected string
}

// parseViewFormat is a tighter parser than output.Parse: config view
// targets yaml by default (matches the on-disk shape) and only json
// as the scriptable alternative. table/wide/ndjson don't fit a nested
// heterogeneous document.
func parseViewFormat(s string) (output.Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "yaml", "yml":
		return output.FormatYAML, nil
	case "json":
		return output.FormatJSON, nil
	default:
		return "", fmt.Errorf("unsupported output format %q for config view (accepted: yaml, json)", s)
	}
}

// buildViewDoc projects a Config into viewDoc, masking password/
// api_key/token fields when opts.showSecrets is false. Indirection
// references (${env:...}, ${file:...}, ${keyring:...}) are preserved
// as-is — they're pointers, not secrets themselves, and hiding them
// removes information an operator needs to verify config shape.
// When opts.showSecrets is true, indirection references are resolved
// before rendering so the operator sees the actual credential a
// command would send. A failed resolution (unset env var, missing
// file, locked keyring) leaves the literal reference in place so a
// single broken context never blanks the whole listing.
//
// Context filtering: by default only the selected context (opts.selected,
// falling back to cfg.CurrentContext) is included. opts.all restores
// the full-file dump. A selected name that's absent from the file is
// a hard error — either the user's --context flag or current-context
// points somewhere broken, and silently dropping contexts would
// mask that.
func buildViewDoc(cfg esopsconfig.Config, opts viewOptions) (viewDoc, error) {
	doc := viewDoc{CurrentContext: cfg.CurrentContext}
	if d := projectDefaults(cfg.Defaults); d != nil {
		doc.Defaults = d
	}

	if opts.all {
		if len(cfg.Contexts) > 0 {
			doc.Contexts = make(map[string]viewContext, len(cfg.Contexts))
			for name, c := range cfg.Contexts {
				doc.Contexts[name] = projectContext(c, opts.showSecrets)
			}
		}
		return doc, nil
	}

	target := opts.selected
	if target == "" {
		target = cfg.CurrentContext
	}
	if target == "" {
		// No current-context and no --context: the "current slice" is
		// empty. Render defaults only; pass --all to see everything.
		return doc, nil
	}
	c, ok := cfg.Contexts[target]
	if !ok {
		return viewDoc{}, fmt.Errorf("context %q not found in config", target)
	}
	doc.Contexts = map[string]viewContext{target: projectContext(c, opts.showSecrets)}
	return doc, nil
}

func projectDefaults(d esopsconfig.Defaults) *viewDefaults {
	if d == (esopsconfig.Defaults{}) {
		return nil
	}
	return &viewDefaults{
		Output:     d.Output,
		LogLevel:   d.LogLevel,
		LogFormat:  d.LogFormat,
		LogFile:    d.LogFile,
		Timeout:    durationString(d.Timeout),
		MaxRetries: d.MaxRetries,
		Compress:   d.Compress,
	}
}

func projectContext(c esopsconfig.Context, showSecrets bool) viewContext {
	vc := viewContext{
		URL:         c.URL,
		URLs:        append([]string(nil), c.URLs...),
		Protection:  c.Protection,
		MaxInFlight: c.MaxInFlight,
		Timeout:     durationString(c.Timeout),
		Compress:    c.Compress,
	}
	if a := projectAuth(c.Auth, showSecrets); a != nil {
		vc.Auth = a
	}
	if c.TLS != (esopsconfig.TLS{}) {
		vc.TLS = &viewTLS{CACert: c.TLS.CACert, Insecure: c.TLS.Insecure}
	}
	return vc
}

func projectAuth(a esopsconfig.Auth, showSecrets bool) *viewAuth {
	if a == (esopsconfig.Auth{}) {
		return nil
	}
	if showSecrets {
		if resolved, err := esopsconfig.ResolveAuth(a); err == nil {
			a = resolved
		}
	}
	return &viewAuth{
		Type:       a.Type,
		Username:   a.Username,
		Password:   maybeRedact(a.Password, showSecrets),
		APIKey:     maybeRedact(a.APIKey, showSecrets),
		Token:      maybeRedact(a.Token, showSecrets),
		ClientCert: a.ClientCert,
		ClientKey:  a.ClientKey,
		Region:     a.Region,
		Service:    a.Service,
	}
}

func maybeRedact(s string, showSecrets bool) string {
	if showSecrets || s == "" || esopsconfig.IsSecretRef(s) {
		return s
	}
	return redactionPlaceholder
}

// durationString formats a Duration as "30s"-style text, matching the
// on-disk shape. Zero returns "" so omitempty hides the field.
func durationString(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

func renderView(w io.Writer, doc viewDoc, f output.Format) error {
	switch f {
	case output.FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	case output.FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(doc)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}
