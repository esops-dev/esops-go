package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"

	"github.com/esops-dev/esops-go/pkg/types"
)

// SnapshotRepositoryDescribe renders a SnapshotRepositoryDescription.
// Wide is rejected by the CLI's FormatsNoWide allowlist — the
// renderer has no auto-collapsed section that wide could un-collapse,
// so silently aliasing wide to table would be a lie.
func SnapshotRepositoryDescribe(w io.Writer, d types.SnapshotRepositoryDescription, f Format) error {
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
		return snapshotRepositoryDescribeText(w, d)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotRepositoryDescribeText(w io.Writer, d types.SnapshotRepositoryDescription) error {
	r := d.Info
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Name:\t%s\n", r.Name)
	if r.Type != "" {
		fprintfTW(tw, "Type:\t%s\n", r.Type)
	}
	parts := []string{
		fmt.Sprintf("success: %d", d.Summary.SuccessCount),
		fmt.Sprintf("failed: %d", d.Summary.FailedCount),
		fmt.Sprintf("in_progress: %d", d.Summary.InProgressCount),
	}
	if d.Summary.OtherCount > 0 {
		parts = append(parts, fmt.Sprintf("other: %d", d.Summary.OtherCount))
	}
	fprintfTW(tw, "Snapshots:\t%d total  (%s)\n",
		d.Summary.SnapshotCount, strings.Join(parts, ", "))
	_ = tw.Flush()

	writeRepoSettings(w, r.Settings)
	writeRepoVerify(w, d.Verify)
	writeRepoPolicies(w, d.Policies)
	writeWarnings(w, d.SectionWarnings)
	return nil
}

func writeRepoSettings(w io.Writer, settings map[string]any) {
	if len(settings) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Settings:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fprintfTW(tw, "  %s:\t%s\n", k, settingValueString(settings[k]))
	}
	_ = tw.Flush()
}

func writeRepoVerify(w io.Writer, v *types.SnapshotVerifyResult) {
	if v == nil {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Verify:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  Verified nodes:\t%d\n", len(v.VerifiedNodes))
	if len(v.VerifiedNodes) > 0 {
		names := append([]string{}, v.VerifiedNodes...)
		sort.Strings(names)
		fprintfTW(tw, "  Nodes:\t%s\n", strings.Join(names, ", "))
	}
	_ = tw.Flush()
}

func writeRepoPolicies(w io.Writer, policies []types.SnapshotPolicy) {
	if len(policies) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Policies writing to this repository:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  NAME\tSCHEDULE\tENABLED\n")
	for _, p := range policies {
		sched := p.Schedule
		if sched == "" {
			sched = "-"
		}
		fprintfTW(tw, "  %s\t%s\t%t\n", p.Name, sched, p.Enabled)
	}
	_ = tw.Flush()
}
