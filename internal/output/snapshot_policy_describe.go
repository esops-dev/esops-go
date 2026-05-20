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

// SnapshotPolicyDescribe renders a SnapshotPolicyDescription. Only
// table / json / yaml are supported — wide carries no extra signal
// here, so it's rejected up-front via cmdsetup's FormatsNoWide
// allowlist rather than treated as a silent alias for table.
func SnapshotPolicyDescribe(w io.Writer, d types.SnapshotPolicyDescription, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(d)
	case FormatTable, "":
		return snapshotPolicyDescribeText(w, d)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotPolicyDescribeText(w io.Writer, d types.SnapshotPolicyDescription) error {
	p := d.Policy
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Name:\t%s\n", p.Name)
	if p.Repository != "" {
		fprintfTW(tw, "Repository:\t%s\n", p.Repository)
	}
	if p.Schedule != "" {
		fprintfTW(tw, "Schedule:\t%s\n", p.Schedule)
	}
	fprintfTW(tw, "Enabled:\t%t\n", p.Enabled)
	if p.Version > 0 {
		fprintfTW(tw, "Version:\t%d\n", p.Version)
	}
	if p.ModifiedDateMillis > 0 {
		fprintfTW(tw, "Modified:\t%s\n", formatMillisUTC(p.ModifiedDateMillis))
	}
	if p.LastSuccessMillis > 0 {
		fprintfTW(tw, "Last success:\t%s  (%s)\n",
			formatMillisUTC(p.LastSuccessMillis),
			humanAge(time.UnixMilli(p.LastSuccessMillis)))
	}
	if p.LastFailureMillis > 0 {
		fprintfTW(tw, "Last failure:\t%s  (%s)\n",
			formatMillisUTC(p.LastFailureMillis),
			humanAge(time.UnixMilli(p.LastFailureMillis)))
	}
	if p.NextExecutionMillis > 0 {
		fprintfTW(tw, "Next execution:\t%s  (in %s)\n",
			formatMillisUTC(p.NextExecutionMillis),
			humanUntil(time.UnixMilli(p.NextExecutionMillis)))
	}
	_ = tw.Flush()

	writePolicyDefinition(w, "Policy body", p.Definition)

	if len(d.RecentSnapshots) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Recent snapshots in repository (limit %d):\n", d.RecentLimit)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fprintfTW(tw, "  NAME\tSTATE\tSTARTED\tDURATION\n")
		for _, s := range d.RecentSnapshots {
			started := "-"
			if s.StartTimeMillis > 0 {
				started = humanAge(time.UnixMilli(s.StartTimeMillis))
			}
			dur := "-"
			if s.DurationMillis > 0 {
				dur = humanDuration(s.DurationMillis)
			}
			fprintfTW(tw, "  %s\t%s\t%s\t%s\n", s.Name, s.State, started, dur)
		}
		_ = tw.Flush()
	}

	writeWarnings(w, d.SectionWarnings)
	return nil
}

// formatMillisUTC renders an epoch-millis as an RFC3339 UTC string.
// Zero collapses to "-" so callers don't need to guard.
func formatMillisUTC(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// humanUntil is humanAge's twin for future times: "5m" instead of
// "5m ago". Used for next-execution display where the policy's next
// run is by definition in the future.
func humanUntil(t time.Time) string {
	d := time.Until(t)
	if d < 0 {
		return "overdue"
	}
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
