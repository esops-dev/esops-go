package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/esops-dev/esops-go/pkg/types"
)

// DataStreamDescribeOptions are the renderer-side knobs the CLI
// passes down. ShowIndices toggles the per-backing-index listing;
// nil = auto (always show), &false hides them. Wide flips on the
// extra backing-index columns (UUID and managed_by) that the default
// view omits — that's the one signal --output wide carries here.
type DataStreamDescribeOptions struct {
	ShowIndices *bool
	Wide        bool
}

// DataStreamDescribe renders a DataStreamDescription in the requested
// format. Wide widens the per-backing-index listing with UUID and
// managed_by columns (the ES-only metadata that the cluster surfaces
// alongside each .ds-* index); table / default keeps to the operator-
// friendly four-column view (NAME / HEALTH / DOCS / SIZE).
func DataStreamDescribe(w io.Writer, d types.DataStreamDescription, opts DataStreamDescribeOptions, f Format) error {
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
		opts.Wide = true
		return dataStreamDescribeText(w, d, opts)
	case FormatTable, "":
		return dataStreamDescribeText(w, d, opts)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func dataStreamDescribeText(w io.Writer, d types.DataStreamDescription, opts DataStreamDescribeOptions) error {
	writeDataStreamHeader(w, d)
	writeDataStreamLifecycle(w, d)
	writeDataStreamTemplate(w, d.MatchedTemplate)
	writeDataStreamBackingIndices(w, d.Stream, opts)
	writeDataStreamRecoveries(w, d.Recoveries)
	writeDataStreamTasks(w, d.Tasks)
	writeWarnings(w, d.SectionWarnings)
	return nil
}

func writeDataStreamHeader(w io.Writer, d types.DataStreamDescription) {
	s := d.Stream
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Name:\t%s\n", s.Name)
	if s.Status != "" {
		fprintfTW(tw, "Status:\t%s\n", s.Status)
	}
	if s.Generation > 0 {
		fprintfTW(tw, "Generation:\t%d\n", s.Generation)
	}
	if s.WriteIndex != "" {
		fprintfTW(tw, "Write index:\t%s\n", s.WriteIndex)
	}
	fprintfTW(tw, "Backing indices:\t%d\n", s.IndexCount)
	if s.BackingIndexOldest != "" || s.BackingIndexNewest != "" {
		fprintfTW(tw, "Generation range:\t%s … %s\n", s.BackingIndexOldest, s.BackingIndexNewest)
	}
	if s.DocsCount > 0 || s.StoreBytes > 0 {
		fprintfTW(tw, "Docs / size:\t%d docs, %s on disk\n", s.DocsCount, humanBytes(s.StoreBytes))
	}
	if s.Template != "" {
		fprintfTW(tw, "Bound template:\t%s\n", s.Template)
	}
	flags := []string{}
	if s.Hidden {
		flags = append(flags, "hidden")
	}
	if s.System {
		flags = append(flags, "system")
	}
	if s.Replicated {
		flags = append(flags, "replicated")
	}
	if s.AllowCustomRouting {
		flags = append(flags, "allow_custom_routing")
	}
	if len(flags) > 0 {
		fprintfTW(tw, "Flags:\t%s\n", strings.Join(flags, ", "))
	}
	_ = tw.Flush()
}

func writeDataStreamLifecycle(w io.Writer, d types.DataStreamDescription) {
	s := d.Stream
	hasILM := s.ILMPolicy != ""
	hasISM := s.ISMTemplate != ""
	hasDSL := len(s.Lifecycle) > 0
	if !hasILM && !hasISM && !hasDSL {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Lifecycle:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if hasILM {
		fprintfTW(tw, "  ILM policy:\t%s\n", s.ILMPolicy)
	}
	if hasISM {
		fprintfTW(tw, "  ISM template:\t%s\n", s.ISMTemplate)
	}
	if hasDSL {
		body, err := json.MarshalIndent(s.Lifecycle, "  ", "  ")
		if err == nil {
			fprintfTW(tw, "  DSL:\t%s\n", body)
		}
	}
	_ = tw.Flush()
}

func writeDataStreamTemplate(w io.Writer, t *types.IndexTemplate) {
	if t == nil {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Matched template:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  Name:\t%s\n", t.Name)
	if t.Priority != 0 {
		fprintfTW(tw, "  Priority:\t%d\n", t.Priority)
	}
	if len(t.IndexPatterns) > 0 {
		fprintfTW(tw, "  Patterns:\t%s\n", strings.Join(t.IndexPatterns, ", "))
	}
	if len(t.ComposedOf) > 0 {
		fprintfTW(tw, "  Composed of:\t%s\n", strings.Join(t.ComposedOf, ", "))
	}
	_ = tw.Flush()
}

func writeDataStreamBackingIndices(w io.Writer, s types.DataStream, opts DataStreamDescribeOptions) {
	if len(s.Indices) == 0 {
		return
	}
	if opts.ShowIndices != nil && !*opts.ShowIndices {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Backing indices (oldest → newest):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if opts.Wide {
		fprintfTW(tw, "  NAME\tHEALTH\tDOCS\tSIZE\tUUID\tMANAGED_BY\n")
	} else {
		fprintfTW(tw, "  NAME\tHEALTH\tDOCS\tSIZE\n")
	}
	for _, b := range s.Indices {
		health := b.Health
		if health == "" {
			health = "-"
		}
		if opts.Wide {
			uuid := b.UUID
			if uuid == "" {
				uuid = "-"
			}
			managed := b.ManagedBy
			if managed == "" {
				managed = "-"
			}
			fprintfTW(tw, "  %s\t%s\t%d\t%s\t%s\t%s\n",
				b.Name, health, b.DocsCount, humanBytes(b.StoreBytes), uuid, managed)
		} else {
			fprintfTW(tw, "  %s\t%s\t%d\t%s\n", b.Name, health, b.DocsCount, humanBytes(b.StoreBytes))
		}
	}
	_ = tw.Flush()
}

func writeDataStreamRecoveries(w io.Writer, recs []types.IndexRecovery) {
	if len(recs) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Active recoveries (backing indices):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range recs {
		for _, s := range r.Shards {
			role := "r"
			if s.Primary {
				role = "p"
			}
			bytes := s.BytesPercent
			if bytes == "" {
				bytes = "-"
			}
			fprintfTW(tw, "  %s\t[%s%d]\t%s\tstage=%s\tbytes=%s\n",
				r.Index, role, s.ID, s.Type, s.Stage, bytes)
		}
	}
	_ = tw.Flush()
}

func writeDataStreamTasks(w io.Writer, tasks []types.Task) {
	if len(tasks) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Tasks (in flight, touching this stream or its backing indices):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range tasks {
		desc := t.Description
		if desc == "" {
			desc = "-"
		}
		running := humanDuration(t.RunningTimeNanos / int64(time.Millisecond))
		fprintfTW(tw, "  %s\t%s\trunning=%s\t%s\n", t.ID, t.Action, running, desc)
	}
	_ = tw.Flush()
}
