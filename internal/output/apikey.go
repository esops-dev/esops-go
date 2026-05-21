package output

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/esops-dev/esops-go/pkg/types"
)

// APIKeyList renders the cluster's active API keys for `ops apikey
// list`. Default columns are the load-bearing tuple (id, name, owner,
// created, expiration, status). Wide adds the realm and the
// last-authenticated timestamp. JSON/YAML emit the neutral []APIKey
// slice unchanged so a captured listing is diffable across runs.
func APIKeyList(w io.Writer, keys []types.APIKey, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(keys)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(keys)
	case FormatWide:
		return apiKeyTable(w, keys, true)
	case FormatTable, "":
		return apiKeyTable(w, keys, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// APIKeyDescribe renders a single key's metadata. Same columns as the
// list view, plus the wide-only realm/last-auth columns so a single
// describe call shows everything the inspector knows.
func APIKeyDescribe(w io.Writer, key types.APIKey, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(key)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(key)
	case FormatTable, FormatWide, "":
		return apiKeyTable(w, []types.APIKey{key}, true)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func apiKeyTable(w io.Writer, keys []types.APIKey, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tOWNER\tREALM\tCREATED\tEXPIRATION\tLAST_AUTH\tSTATUS")
	} else {
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tOWNER\tCREATED\tEXPIRATION\tSTATUS")
	}
	for _, k := range keys {
		status := "active"
		if k.Invalidated {
			status = "invalidated"
		}
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				dashIfEmpty(k.ID),
				dashIfEmpty(k.Name),
				dashIfEmpty(k.Username),
				dashIfEmpty(k.Realm),
				timeOrDash(k.Creation),
				timePtrOrDash(k.Expiration),
				timePtrOrDash(k.LastAuth),
				status,
			)
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			dashIfEmpty(k.ID),
			dashIfEmpty(k.Name),
			dashIfEmpty(k.Username),
			timeOrDash(k.Creation),
			timePtrOrDash(k.Expiration),
			status,
		)
	}
	return tw.Flush()
}

// APIKeyCreated renders the result of `ops apikey create`. The JSON
// shape is the cluster's response augmented with owner (filled in by
// the business layer via a follow-up GET) and dry_run (true when no
// cluster call happened). The table renderer prints label/value
// pairs so a `... > key.json`-like redirect captures everything the
// operator needs to use the key, including the wider api_key /
// encoded values that would wrap badly in a column layout.
//
// This is the secret-bearing renderer. Callers route it to stdout
// only; the CLI's TTY guard in `ops apikey create` keeps it from
// landing in scrollback by default. On a dry-run the api_key /
// encoded fields are empty (the cluster never issued a secret) and
// the renderer surfaces "(dry-run, no cluster call)" so the absence
// is explicit rather than read as a "I created a key but the cluster
// forgot to give me the value" bug.
func APIKeyCreated(w io.Writer, created types.APIKeyCreated, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		// #nosec G117 -- APIKeyCreated.Key is *meant* to be emitted to
		// stdout: this is the one renderer that prints the cluster's
		// one-shot secret so the operator can capture it via
		// `--output json > key.json`. TTY-guard + redaction-keyset
		// keep it out of every other sink.
		return enc.Encode(created)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		// #nosec G117 -- intentional secret emission; see the JSON arm above.
		return enc.Encode(created)
	case FormatTable, FormatWide, "":
		if created.DryRun {
			_, _ = fmt.Fprintln(w, "(dry-run, no cluster call)")
		}
		_, _ = fmt.Fprintf(w, "id:         %s\n", dashIfEmpty(created.ID))
		_, _ = fmt.Fprintf(w, "name:       %s\n", created.Name)
		_, _ = fmt.Fprintf(w, "owner:      %s\n", dashIfEmpty(created.Owner))
		_, _ = fmt.Fprintf(w, "expiration: %s\n", timePtrOrDash(created.Expiration))
		_, _ = fmt.Fprintf(w, "api_key:    %s\n", dashIfEmpty(created.Key))
		_, _ = fmt.Fprintf(w, "encoded:    %s\n", dashIfEmpty(created.Encoded))
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// APIKeyInvalidated renders the result of `ops apikey invalidate`. The
// JSON/YAML shape is the cluster's response (plus the resolved
// Requested list and a DryRun flag from the business layer); the
// table view summarises the counts and lists the affected ids so a
// captured run is self-describing.
//
// On dry-run the only populated field is Requested — the cluster
// was never called, so Invalidated/PreviouslyInvalidated/ErrorCount
// are zero by construction. The table renderer surfaces the dry-run
// banner up front and prints the would-be-revoked ids under a
// dedicated header.
func APIKeyInvalidated(w io.Writer, result types.APIKeyInvalidated, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(result)
	case FormatTable, FormatWide, "":
		if result.DryRun {
			_, _ = fmt.Fprintln(w, "(dry-run, no cluster call)")
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "REQUESTED\tINVALIDATED\tPREVIOUSLY_INVALIDATED\tERRORS")
		_, _ = fmt.Fprintf(tw, "%d\t%d\t%d\t%d\n",
			len(result.Requested), len(result.Invalidated), len(result.PreviouslyInvalidated), result.ErrorCount)
		if err := tw.Flush(); err != nil {
			return err
		}
		if result.DryRun && len(result.Requested) > 0 {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "WOULD INVALIDATE")
			for _, id := range result.Requested {
				_, _ = fmt.Fprintf(w, "  %s\n", id)
			}
			return nil
		}
		if len(result.Invalidated) > 0 {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "INVALIDATED IDS")
			for _, id := range result.Invalidated {
				_, _ = fmt.Fprintf(w, "  %s\n", id)
			}
		}
		if len(result.PreviouslyInvalidated) > 0 {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "PREVIOUSLY INVALIDATED IDS")
			for _, id := range result.PreviouslyInvalidated {
				_, _ = fmt.Fprintf(w, "  %s\n", id)
			}
		}
		if result.ErrorCount > 0 {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "ERROR DETAILS")
			enc := json.NewEncoder(w)
			enc.SetIndent("  ", "  ")
			for _, detail := range result.ErrorDetails {
				_ = enc.Encode(detail)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func timeOrDash(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func timePtrOrDash(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
