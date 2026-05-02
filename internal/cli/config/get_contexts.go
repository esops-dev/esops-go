package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
	"go.yaml.in/yaml/v3"

	esopsconfig "github.com/esops-dev/esops-go/internal/config"
	"github.com/esops-dev/esops-go/internal/output"
)

// contextEntry is the render-friendly projection of a context. Field
// order matches the table column order so json/yaml output stays
// visually aligned with the table view.
type contextEntry struct {
	Name        string   `json:"name" yaml:"name"`
	Current     bool     `json:"current" yaml:"current"`
	URL         string   `json:"url,omitempty" yaml:"url,omitempty"`
	URLs        []string `json:"urls,omitempty" yaml:"urls,omitempty"`
	AuthType    string   `json:"auth_type,omitempty" yaml:"auth_type,omitempty"`
	Protection  string   `json:"protection,omitempty" yaml:"protection,omitempty"`
	MaxInFlight int      `json:"max_in_flight,omitempty" yaml:"max_in_flight,omitempty"`
	TLSInsecure bool     `json:"tls_insecure,omitempty" yaml:"tls_insecure,omitempty"`
	CACert      string   `json:"cacert,omitempty" yaml:"cacert,omitempty"`
}

func getContextsCommand() *cli.Command {
	return &cli.Command{
		Name:   "get-contexts",
		Usage:  "List configured clusters",
		Action: runGetContexts,
	}
}

func runGetContexts(_ context.Context, cmd *cli.Command) error {
	cfg, _, err := esopsconfig.LoadDefault(cmd.String("config"))
	if err != nil {
		return err
	}

	format, err := output.Parse(cmd.String("output"))
	if err != nil {
		return err
	}

	return renderContexts(os.Stdout, buildEntries(cfg), format)
}

// buildEntries projects a Config into the sorted, render-friendly slice
// consumed by every output format.
func buildEntries(cfg esopsconfig.Config) []contextEntry {
	names := make([]string, 0, len(cfg.Contexts))
	for n := range cfg.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)

	entries := make([]contextEntry, 0, len(names))
	for _, n := range names {
		c := cfg.Contexts[n]
		entries = append(entries, contextEntry{
			Name:        n,
			Current:     n == cfg.CurrentContext,
			URL:         c.URL,
			URLs:        c.URLs,
			AuthType:    c.Auth.Type,
			Protection:  c.Protection,
			MaxInFlight: c.MaxInFlight,
			TLSInsecure: c.TLS.Insecure,
			CACert:      c.TLS.CACert,
		})
	}
	return entries
}

// renderContexts dispatches to the per-format renderer. Taking an
// io.Writer (rather than wiring os.Stdout in each branch) keeps the
// action a one-liner and lets tests assert shape without subprocess
// juggling.
func renderContexts(w io.Writer, entries []contextEntry, format output.Format) error {
	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case output.FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(entries)
	case output.FormatWide:
		return renderTable(w, entries, true)
	case output.FormatTable, "":
		return renderTable(w, entries, false)
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func renderTable(w io.Writer, entries []contextEntry, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "CURRENT\tNAME\tURL\tAUTH\tPROTECTION\tMAX_IN_FLIGHT\tTLS_INSECURE\tCACERT")
	} else {
		_, _ = fmt.Fprintln(tw, "CURRENT\tNAME\tURL\tAUTH\tPROTECTION")
	}
	for _, e := range entries {
		marker := ""
		if e.Current {
			marker = "*"
		}
		url := e.URL
		if url == "" && len(e.URLs) > 0 {
			url = e.URLs[0]
		}
		if wide && len(e.URLs) > 1 {
			url = strings.Join(e.URLs, ",")
		}
		authType := e.AuthType
		if authType == "" {
			authType = "none"
		}
		prot := e.Protection
		if prot == "" {
			prot = "none"
		}
		if wide {
			cacert := e.CACert
			if cacert == "" {
				cacert = "-"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%t\t%s\n",
				marker, e.Name, url, authType, prot, e.MaxInFlight, e.TLSInsecure, cacert)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", marker, e.Name, url, authType, prot)
		}
	}
	return tw.Flush()
}
