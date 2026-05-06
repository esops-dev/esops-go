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

// MigrationCheck renders a MigrationCheckResult in the requested format.
// JSON/YAML emit the neutral shape unchanged so a captured report is
// interpretable on its own. Table groups the source/target endpoints
// in a header and lists the per-check rows underneath.
func MigrationCheck(w io.Writer, r types.MigrationCheckResult, f Format) error {
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
	case FormatWide:
		return migrationCheckTable(w, r, true)
	case FormatTable, "":
		return migrationCheckTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func migrationCheckTable(w io.Writer, r types.MigrationCheckResult, wide bool) error {
	_, _ = fmt.Fprintf(w, "SOURCE  context=%s  dialect=%s  version=%s  url=%s\n",
		dashIfEmpty(r.Source.Context), dashIfEmpty(r.Source.Dialect),
		dashIfEmpty(r.Source.Version), dashIfEmpty(r.Source.URL))
	_, _ = fmt.Fprintf(w, "TARGET  context=%s  dialect=%s  version=%s  url=%s\n",
		dashIfEmpty(r.Target.Context), dashIfEmpty(r.Target.Dialect),
		dashIfEmpty(r.Target.Version), dashIfEmpty(r.Target.URL))
	_, _ = fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "STATUS\tCHECK\tDETAIL")
	for _, c := range r.Checks {
		detail := c.Detail
		if !wide {
			detail = truncate(detail, 100)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n",
			migrationStatusLabel(c.Status),
			dashIfEmpty(c.Name),
			dashIfEmpty(detail),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "\nSUMMARY  status=%s  blockers=%d  warnings=%d\n",
		migrationStatusLabel(r.Status), r.Blockers, r.Warnings)
	return nil
}

// MigrationPlan renders a MigrationPlan in the requested format.
// JSON/YAML mirror the neutral shape. Table prints a per-index row
// with status, docs, primary store, issue count, and the duration
// estimate; -o wide adds the per-issue field/from/to/note breakdown
// underneath each affected index.
func MigrationPlan(w io.Writer, p types.MigrationPlan, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(p)
	case FormatWide:
		return migrationPlanTable(w, p, true)
	case FormatTable, "":
		return migrationPlanTable(w, p, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func migrationPlanTable(w io.Writer, p types.MigrationPlan, wide bool) error {
	_, _ = fmt.Fprintf(w, "SOURCE  context=%s  dialect=%s  version=%s\n",
		dashIfEmpty(p.Source.Context), dashIfEmpty(p.Source.Dialect),
		dashIfEmpty(p.Source.Version))
	_, _ = fmt.Fprintf(w, "TARGET  context=%s  dialect=%s  version=%s\n",
		dashIfEmpty(p.Target.Context), dashIfEmpty(p.Target.Dialect),
		dashIfEmpty(p.Target.Version))
	_, _ = fmt.Fprintf(w, "THROUGHPUT  %d docs/sec\n\n", p.ThroughputDPS)

	if len(p.Indices) == 0 {
		_, _ = fmt.Fprintln(w, "(no source indices matched the filter)")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "STATUS\tINDEX\tDOCS\tPRIMARY_STORE\tISSUES\tEST_DURATION")
	for _, i := range p.Indices {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%d\t%s\n",
			migrationStatusLabel(i.Status),
			i.Index,
			i.DocsCount,
			humanBytes(i.PrimaryStoreBytes),
			len(i.Issues),
			formatSeconds(i.EstimatedSeconds),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if wide {
		for _, i := range p.Indices {
			if len(i.Issues) == 0 {
				continue
			}
			_, _ = fmt.Fprintf(w, "\nISSUES for %s\n", i.Index)
			itw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(itw, "  SEVERITY\tFIELD\tFROM\tTO\tNOTE")
			for _, issue := range i.Issues {
				_, _ = fmt.Fprintf(itw, "  %s\t%s\t%s\t%s\t%s\n",
					migrationStatusLabel(issue.Severity),
					dashIfEmpty(issue.Field),
					dashIfEmpty(issue.FromType),
					dashIfEmpty(issue.ToType),
					dashIfEmpty(issue.Note),
				)
			}
			if err := itw.Flush(); err != nil {
				return err
			}
		}
	}

	_, _ = fmt.Fprintf(w, "\nTOTALS  indices=%d  docs=%d  primary_store=%s  estimated_duration=%s\n",
		p.Totals.Indices, p.Totals.DocsCount,
		humanBytes(p.Totals.PrimaryStoreBytes),
		formatSeconds(p.Totals.EstimatedSeconds))
	_, _ = fmt.Fprintf(w, "SUMMARY  status=%s  blockers=%d  warnings=%d\n",
		migrationStatusLabel(p.Status), p.Blockers, p.Warnings)
	return nil
}

// migrationStatusLabel uppercases the lowercase enum so it lines up
// with the LEVEL/STATUS columns elsewhere in the renderer set
// (HEALTH, DEPRECATIONS). Empty maps to "-".
func migrationStatusLabel(s string) string {
	switch s {
	case types.MigrationStatusOK:
		return "OK"
	case types.MigrationStatusWarn:
		return "WARN"
	case types.MigrationStatusBlock:
		return "BLOCK"
	case "":
		return "-"
	default:
		return s
	}
}

// formatSeconds renders an estimated-duration value. <60s prints
// "Ns"; minutes / hours / days for larger values. Same shape an
// operator pastes into a Slack thread.
func formatSeconds(secs float64) string {
	if secs <= 0 {
		return "0s"
	}
	d := time.Duration(secs * float64(time.Second))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%dd%dh", days, hours)
	}
}
