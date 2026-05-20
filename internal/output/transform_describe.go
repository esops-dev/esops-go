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

// TransformDescribe renders a TransformDescription. Wide is rejected
// by the CLI's FormatsNoWide allowlist — the definition body is
// already fully expanded in the default view and there is no
// collapsed section wide could surface, so aliasing it to table
// would be a no-op the operator could not distinguish from a bug.
func TransformDescribe(w io.Writer, d types.TransformDescription, f Format) error {
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
		transformDescribeText(w, d)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func transformDescribeText(w io.Writer, d types.TransformDescription) {
	t := d.Info
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Name:\t%s\n", t.Name)
	if t.Description != "" {
		fprintfTW(tw, "Description:\t%s\n", t.Description)
	}
	if t.Source != "" {
		fprintfTW(tw, "Source:\t%s\n", t.Source)
	}
	if t.Dest != "" {
		fprintfTW(tw, "Dest:\t%s\n", t.Dest)
	}
	fprintfTW(tw, "Continuous:\t%t\n", t.Continuous)
	if t.Version != "" {
		fprintfTW(tw, "Cluster version:\t%s\n", t.Version)
	}
	if t.CreatedAtMillis > 0 {
		fprintfTW(tw, "Created:\t%s  (%s)\n",
			formatMillisUTC(t.CreatedAtMillis), humanAge(time.UnixMilli(t.CreatedAtMillis)))
	}
	if t.UpdatedAtMillis > 0 {
		fprintfTW(tw, "Updated:\t%s  (%s)\n",
			formatMillisUTC(t.UpdatedAtMillis), humanAge(time.UnixMilli(t.UpdatedAtMillis)))
	}
	_ = tw.Flush()

	writeTransformStats(w, d.Stats)
	writeTransformDefinition(w, t.Definition)
	writeWarnings(w, d.SectionWarnings)
}

func writeTransformStats(w io.Writer, s *types.TransformStats) {
	if s == nil {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Runtime:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if s.State != "" {
		fprintfTW(tw, "  State:\t%s\n", s.State)
	}
	fprintfTW(tw, "  Docs processed:\t%d\n", s.DocsProcessed)
	fprintfTW(tw, "  Docs indexed:\t%d\n", s.DocsIndexed)
	if s.PagesProcessed > 0 {
		fprintfTW(tw, "  Pages processed:\t%d\n", s.PagesProcessed)
	}
	if s.TriggerCount > 0 {
		fprintfTW(tw, "  Trigger count:\t%d\n", s.TriggerCount)
	}
	if s.SearchTimeMillis > 0 || s.IndexTimeMillis > 0 {
		fprintfTW(tw, "  Search time:\t%s\n", humanDuration(s.SearchTimeMillis))
		fprintfTW(tw, "  Index time:\t%s\n", humanDuration(s.IndexTimeMillis))
	}
	if s.IndexFailures > 0 || s.SearchFailures > 0 {
		fprintfTW(tw, "  Index failures:\t%d\n", s.IndexFailures)
		fprintfTW(tw, "  Search failures:\t%d\n", s.SearchFailures)
	}
	if s.CheckpointLastTimeMillis > 0 {
		fprintfTW(tw, "  Last checkpoint:\t%s  (%s)\n",
			formatMillisUTC(s.CheckpointLastTimeMillis),
			humanDuration(s.CheckpointAgeMillis)+" ago")
	}
	if s.Reason != "" {
		fprintfTW(tw, "  Reason:\t%s\n", s.Reason)
	}
	_ = tw.Flush()
}

func writeTransformDefinition(w io.Writer, def map[string]any) {
	if len(def) == 0 {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Definition:")
	body, err := json.MarshalIndent(def, "  ", "  ")
	if err != nil {
		fprintfW(w, "  (failed to render: %v)\n", err)
		return
	}
	fprintfW(w, "  %s\n", body)
}
