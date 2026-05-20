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

// DefaultSnapshotDescribeIndexListing is the cap above which the per-
// index listing collapses to a one-line summary in default-mode.
// 20 covers the typical small-app snapshot; on a fleet snapshot
// (hundreds of indices) the listing floods the screen, and wide is
// the explicit "give me everything" mode an operator reaches for.
const DefaultSnapshotDescribeIndexListing = 20

// SnapshotDescribe renders a SnapshotDescription. Wide is the
// "show every index name" mode — useful for fleet snapshots whose
// index list would otherwise auto-collapse to a one-line summary.
// JSON / YAML emit the neutral shape unchanged.
func SnapshotDescribe(w io.Writer, d types.SnapshotDescription, f Format) error {
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
	case FormatWide:
		return snapshotDescribeText(w, d, true)
	case FormatTable, "":
		return snapshotDescribeText(w, d, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotDescribeText(w io.Writer, d types.SnapshotDescription, wide bool) error {
	info := d.Info
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Repository:\t%s\n", d.Repository)
	fprintfTW(tw, "Name:\t%s\n", d.Name)
	if info.UUID != "" {
		fprintfTW(tw, "UUID:\t%s\n", info.UUID)
	}
	if info.State != "" {
		fprintfTW(tw, "State:\t%s\n", info.State)
	}
	if info.StartTimeMillis > 0 {
		t := time.UnixMilli(info.StartTimeMillis).UTC()
		fprintfTW(tw, "Started:\t%s  (%s)\n", t.Format(time.RFC3339), humanAge(t))
	}
	if info.DurationMillis > 0 {
		fprintfTW(tw, "Duration:\t%s\n", humanDuration(info.DurationMillis))
	}
	if info.ShardsTotal > 0 {
		fprintfTW(tw, "Shards:\t%d total, %d successful, %d failed\n",
			info.ShardsTotal, info.ShardsSuccessful, info.ShardsFailed)
	}
	if info.Version != "" {
		fprintfTW(tw, "Cluster version:\t%s\n", info.Version)
	}
	fprintfTW(tw, "Includes global state:\t%t\n", info.IncludeGlobalState)
	_ = tw.Flush()

	writeSnapshotIndices(w, info.Indices, wide)

	if d.Status != nil {
		writeSnapshotStatus(w, *d.Status)
	}
	if len(info.Failures) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Failures (%d):\n", len(info.Failures))
		for _, f := range info.Failures {
			fmt.Fprintf(w, "  %s\n", string(f))
		}
	}
	writeWarnings(w, d.SectionWarnings)
	return nil
}

// writeSnapshotIndices renders the per-index listing. Default-mode
// collapses to a one-line summary above DefaultSnapshotDescribeIndexListing;
// wide forces every name regardless. The fleet-snapshot case is
// real: a `cluster:all` snapshot legitimately covers hundreds of
// indices and the default text view should not flood the terminal.
func writeSnapshotIndices(w io.Writer, indices []string, wide bool) {
	if len(indices) == 0 {
		return
	}
	fmt.Fprintln(w)
	if !wide && len(indices) > DefaultSnapshotDescribeIndexListing {
		fmt.Fprintf(w, "Indices:  %d total (pass --output wide for the per-index listing)\n", len(indices))
		return
	}
	fmt.Fprintln(w, "Indices:")
	for _, idx := range indices {
		fmt.Fprintf(w, "  %s\n", idx)
	}
}

func writeSnapshotStatus(w io.Writer, s types.SnapshotStatus) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Status (live):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if s.State != "" {
		fprintfTW(tw, "  State:\t%s\n", s.State)
	}
	fprintfTW(tw, "  Shards init:\t%d\n", s.ShardsInit)
	fprintfTW(tw, "  Shards started:\t%d\n", s.ShardsStarted)
	fprintfTW(tw, "  Shards done:\t%d\n", s.ShardsDone)
	fprintfTW(tw, "  Shards failed:\t%d\n", s.ShardsFailed)
	if s.ShardsTotal > 0 {
		fprintfTW(tw, "  Shards total:\t%d\n", s.ShardsTotal)
	}
	_ = tw.Flush()
}
