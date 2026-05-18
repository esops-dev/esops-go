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

// Transforms renders `esops transform list`. Table mode prints id,
// state, continuous flag, source pattern, dest index and docs
// processed — the operational columns an SRE expects from a list
// view. --output wide adds checkpoint age and the failure count so
// an operator scanning a fleet spots a stalled or misbehaving
// transform without paging into per-id stats. JSON, NDJSON and YAML
// emit the neutral Transform slice directly (state/docs/failures/
// checkpoint fields stay `omitempty` so structured consumers see the
// runtime view only when stats were joined).
func Transforms(w io.Writer, rows []types.Transform, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case FormatNDJSON:
		enc := json.NewEncoder(w)
		for _, r := range rows {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return nil
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(rows)
	case FormatWide:
		return transformsTable(w, rows, true)
	case FormatTable, "":
		return transformsTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func transformsTable(w io.Writer, rows []types.Transform, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tCONTINUOUS\tSOURCE\tDEST\tDOCS_PROCESSED\tCHECKPOINT_AGE\tFAILURES\tDESCRIPTION")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tCONTINUOUS\tSOURCE\tDEST\tDOCS_PROCESSED")
	}
	for _, r := range rows {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%d\t%s\n",
				r.Name,
				dashIfEmpty(r.State),
				boolCell(r.Continuous),
				dashIfEmpty(r.Source),
				dashIfEmpty(r.Dest),
				r.DocsProcessed,
				dashIfEmpty(formatDurationMillis(r.CheckpointAgeMillis)),
				r.Failures,
				dashIfEmpty(truncate(r.Description, 80)),
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
				r.Name,
				dashIfEmpty(r.State),
				boolCell(r.Continuous),
				dashIfEmpty(r.Source),
				dashIfEmpty(r.Dest),
				r.DocsProcessed,
			)
		}
	}
	return tw.Flush()
}

// Transform renders a single transform — `esops transform get`. Table
// mode is a header line plus an indented JSON dump of the cluster's
// raw definition body. JSON/YAML emit the neutral Transform shape
// directly.
func Transform(w io.Writer, t types.Transform, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(t)
	case FormatWide, FormatTable, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "NAME\tCONTINUOUS\tSOURCE\tDEST\tCREATED\tUPDATED\tVERSION\tDESCRIPTION")
		_, _ = fmt.Fprintf(hw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			t.Name,
			boolCell(t.Continuous),
			dashIfEmpty(t.Source),
			dashIfEmpty(t.Dest),
			dashIfEmpty(formatTimeMillis(t.CreatedAtMillis)),
			dashIfEmpty(formatTimeMillis(t.UpdatedAtMillis)),
			dashIfEmpty(t.Version),
			dashIfEmpty(t.Description),
		)
		if err := hw.Flush(); err != nil {
			return err
		}
		if len(t.Definition) > 0 {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "DEFINITION")
			buf, err := json.MarshalIndent(t.Definition, "  ", "  ")
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// TransformStats renders `esops transform stats`. Table mode prints
// the docs/processing counters, last-checkpoint age and the failure
// reason when the cluster reports one. JSON/YAML emit the neutral
// TransformStats shape directly.
func TransformStats(w io.Writer, s types.TransformStats, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(s)
	case FormatWide, FormatTable, "":
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tDOCS_PROCESSED\tDOCS_INDEXED\tPAGES\tINDEX_FAILS\tSEARCH_FAILS\tCHECKPOINT_AGE\tREASON")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
			s.Name,
			dashIfEmpty(s.State),
			s.DocsProcessed,
			s.DocsIndexed,
			s.PagesProcessed,
			s.IndexFailures,
			s.SearchFailures,
			dashIfEmpty(formatDurationMillis(s.CheckpointAgeMillis)),
			dashIfEmpty(truncate(s.Reason, 80)),
		)
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// TransformApply renders the outcome of `esops transform apply`. A
// one-line header reports Created/Updated/Unchanged plus the
// acknowledged flag. JSON/YAML emit the neutral shape directly.
func TransformApply(w io.Writer, r types.TransformApplyResult, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(r)
	case FormatWide, FormatTable, "":
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ACTION\tNAME\tCREATED\tUPDATED\tUNCHANGED\tDRY_RUN\tACK")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%t\t%t\t%t\n",
			dashIfEmpty(r.Action),
			r.Name,
			r.Created,
			r.Updated,
			r.Unchanged,
			r.DryRun,
			r.Acknowledged,
		)
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// TransformDeletes renders the outcome of `esops transform delete`.
// The CLI may pass a pattern that resolves to multiple ids, so the
// renderer accepts a slice. Table prints one row per delete; JSON/
// YAML emit the slice directly.
func TransformDeletes(w io.Writer, rows []types.TransformDeleteResult, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(rows)
	case FormatWide, FormatTable, "":
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ACTION\tNAME\tSTOPPED\tDRY_RUN\tACK")
		for _, r := range rows {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%t\n",
				dashIfEmpty(r.Action),
				r.Name,
				r.Stopped,
				r.DryRun,
				r.Acknowledged,
			)
		}
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// TransformStart renders the outcome of `esops transform start`.
func TransformStart(w io.Writer, r types.TransformStartResult, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(r)
	case FormatWide, FormatTable, "":
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ACTION\tNAME\tDRY_RUN\tACK")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%t\n",
			dashIfEmpty(r.Action),
			r.Name,
			r.DryRun,
			r.Acknowledged,
		)
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// TransformStop renders the outcome of `esops transform stop`.
func TransformStop(w io.Writer, r types.TransformStopResult, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(r)
	case FormatWide, FormatTable, "":
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ACTION\tNAME\tWAIT_FOR_CHECKPOINT\tDRY_RUN\tACK")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%t\n",
			dashIfEmpty(r.Action),
			r.Name,
			r.WaitForCheckpoint,
			r.DryRun,
			r.Acknowledged,
		)
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// TransformPreview renders the outcome of `esops transform preview`.
// Table mode prints the document count + an indented JSON dump of
// the sample so operators can scan it without re-piping to jq.
// JSON/YAML emit the neutral preview shape directly.
func TransformPreview(w io.Writer, r types.TransformPreviewResult, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(r)
	case FormatWide, FormatTable, "":
		_, _ = fmt.Fprintf(w, "DOCUMENTS: %d (sample, not exhaustive)\n", len(r.Documents))
		if len(r.Documents) > 0 {
			_, _ = fmt.Fprintln(w)
			buf, err := json.MarshalIndent(r.Documents, "  ", "  ")
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
		}
		if len(r.Mappings) > 0 {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "GENERATED MAPPINGS")
			buf, err := json.MarshalIndent(r.Mappings, "  ", "  ")
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// boolCell renders a typed boolean as "yes"/"no" — easier to scan in
// a wide column of true/false than the Go default formatting, and
// matches what `index list` does for the writeable column.
func boolCell(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// formatTimeMillis turns an epoch-millisecond into RFC3339 (UTC).
// Empty when ms is zero so the renderer can emit a dash.
func formatTimeMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// formatDurationMillis turns a duration in milliseconds into a
// short human readable string ("3m20s"). Empty when ms is zero so
// the renderer can emit a dash.
func formatDurationMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}
