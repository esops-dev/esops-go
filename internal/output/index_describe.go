package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultDescribeShardListing is the cap above which the per-shard
// listing collapses to a one-line summary in auto mode. 12 covers the
// vast majority of "normal" indices; larger indices benefit from
// `--show-shards` (or `--output wide`) being a deliberate choice
// rather than a default flood of rows.
const DefaultDescribeShardListing = 12

// IndexDescribeOptions are the renderer-side knobs `esops index
// describe` hands down: how many shard rows to show before auto-
// collapsing, whether to print the full mappings tree (instead of
// the per-type summary), and whether the settings block should print
// inherited defaults alongside configured values.
//
// AutoShardMax = 0 means "use DefaultDescribeShardListing"; passing 0
// from the CLI deliberately keeps the threshold a renderer-owned
// default rather than a CLI-passed constant.
type IndexDescribeOptions struct {
	ShowShards      *bool
	AutoShardMax    int
	FullMappings    bool
	IncludeDefaults bool
}

// IndexDescribe renders an IndexDescription in the requested format.
// Table (the default) produces a multi-section human view; Wide is
// identical to table except the per-shard listing is forced (the
// "give me everything" mode operators reach for during incidents);
// JSON/YAML emit the neutral IndexDescription shape unchanged so a
// script capturing `--output yaml` sees exactly what the aggregator
// assembled, including any SectionWarnings the fan-out collected.
func IndexDescribe(w io.Writer, d types.IndexDescription, opts IndexDescribeOptions, f Format) error {
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
		// Wide forces the per-shard listing — that's the one extra
		// signal it carries over the default table view. Honour an
		// explicit --no-shards by leaving the override in place;
		// only auto-mode (ShowShards == nil) gets the wide upgrade.
		if opts.ShowShards == nil {
			force := true
			opts.ShowShards = &force
		}
		indexDescribeText(w, d, opts)
		return nil
	case FormatTable, "":
		indexDescribeText(w, d, opts)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexDescribeText(w io.Writer, d types.IndexDescription, opts IndexDescribeOptions) {
	if opts.AutoShardMax == 0 {
		opts.AutoShardMax = DefaultDescribeShardListing
	}
	writeHeader(w, d)
	writeAliases(w, d)
	writeLifecycle(w, d)
	writeTemplate(w, d.MatchedTemplate)
	writeSettings(w, d.Settings, opts.IncludeDefaults)
	writeMappings(w, d.Mappings, opts.FullMappings)
	writeShards(w, d.Shards, opts)
	writeRecovery(w, d.Recovery)
	writeSegments(w, d.Segments)
	writeTasks(w, d.Tasks)
	writeWarnings(w, d.SectionWarnings)
}

func writeHeader(w io.Writer, d types.IndexDescription) {
	info := d.Info
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Name:\t%s\n", info.Name)
	if d.ResolvedFrom != "" && d.ResolvedFrom != info.Name {
		fprintfTW(tw, "Alias:\t%s (resolved to this index)\n", d.ResolvedFrom)
	}
	if info.UUID != "" {
		fprintfTW(tw, "UUID:\t%s\n", info.UUID)
	}
	if info.CreationDateMillis > 0 {
		t := time.UnixMilli(info.CreationDateMillis).UTC()
		fprintfTW(tw, "Created:\t%s  (%s)\n", t.Format(time.RFC3339), humanAge(t))
	}
	if info.Health != "" {
		fprintfTW(tw, "Health:\t%s\n", info.Health)
	}
	if info.Status != "" {
		fprintfTW(tw, "Status:\t%s\n", info.Status)
	}
	fprintfTW(tw, "Docs:\t%d  (deleted: %d)\n", info.DocsCount, info.DocsDeleted)
	fprintfTW(tw, "Size:\t%s primary  /  %s total\n",
		humanBytes(info.PrimaryStoreBytes), humanBytes(info.StoreBytes))
	fprintfTW(tw, "Shards:\t%d primary, %d replica\n", info.Primaries, info.Replicas)
	_ = tw.Flush()
}

func writeAliases(w io.Writer, d types.IndexDescription) {
	if len(d.Aliases) == 0 {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Aliases:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, a := range d.Aliases {
		extras := []string{}
		if a.IsWriteIndex {
			extras = append(extras, "write_index=true")
		}
		if a.Filter != "" && a.Filter != "-" {
			extras = append(extras, "filter="+a.Filter)
		}
		if a.RoutingIndex != "" {
			extras = append(extras, "routing_index="+a.RoutingIndex)
		}
		if a.RoutingSearch != "" {
			extras = append(extras, "routing_search="+a.RoutingSearch)
		}
		extra := ""
		if len(extras) > 0 {
			extra = "  " + strings.Join(extras, "  ")
		}
		fprintfTW(tw, "  %s\t%s\n", a.Alias, extra)
	}
	_ = tw.Flush()
}

func writeLifecycle(w io.Writer, d types.IndexDescription) {
	if d.ILM != nil && d.ILM.Managed {
		fprintlnW(w)
		fprintlnW(w, "Lifecycle (ILM):")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		ilm := d.ILM
		if ilm.Policy != "" {
			fprintfTW(tw, "  Policy:\t%s\n", ilm.Policy)
		}
		if ilm.Phase != "" {
			fprintfTW(tw, "  Phase:\t%s\n", ilm.Phase)
		}
		if ilm.Action != "" {
			fprintfTW(tw, "  Action:\t%s\n", ilm.Action)
		}
		if ilm.Step != "" {
			fprintfTW(tw, "  Step:\t%s\n", ilm.Step)
		}
		if ilm.Age != "" {
			fprintfTW(tw, "  Age:\t%s\n", ilm.Age)
		}
		if ilm.FailedStep != "" {
			fprintfTW(tw, "  Failed step:\t%s\n", ilm.FailedStep)
		}
		_ = tw.Flush()
	}
	if d.ISM != nil && d.ISM.Managed {
		fprintlnW(w)
		fprintlnW(w, "Lifecycle (ISM):")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		ism := d.ISM
		if ism.PolicyID != "" {
			fprintfTW(tw, "  Policy:\t%s\n", ism.PolicyID)
		}
		if ism.State != "" {
			fprintfTW(tw, "  State:\t%s\n", ism.State)
		}
		if ism.Action != "" {
			fprintfTW(tw, "  Action:\t%s\n", ism.Action)
		}
		if ism.Step != "" {
			fprintfTW(tw, "  Step:\t%s\n", ism.Step)
		}
		fprintfTW(tw, "  Enabled:\t%t\n", ism.Enabled)
		_ = tw.Flush()
	}
}

func writeTemplate(w io.Writer, t *types.IndexTemplate) {
	if t == nil {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Matched template:")
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

func writeSettings(w io.Writer, s types.IndexSettings, includeDefaults bool) {
	configured := flattenSettings(s.Settings)
	if len(configured) == 0 && !includeDefaults {
		return
	}
	fprintlnW(w)
	if includeDefaults {
		fprintlnW(w, "Settings (configured + defaults):")
	} else {
		fprintlnW(w, "Settings (configured):")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	keys := make([]string, 0, len(configured))
	for k := range configured {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fprintfTW(tw, "  %s:\t%s\n", k, settingValueString(configured[k]))
	}
	if includeDefaults {
		defaults := flattenSettings(s.Defaults)
		dkeys := make([]string, 0, len(defaults))
		for k := range defaults {
			if _, ok := configured[k]; ok {
				continue
			}
			dkeys = append(dkeys, k)
		}
		sort.Strings(dkeys)
		for _, k := range dkeys {
			fprintfTW(tw, "  %s:\t%s (default)\n", k, settingValueString(defaults[k]))
		}
	}
	_ = tw.Flush()
}

func writeMappings(w io.Writer, m *types.IndexMapping, full bool) {
	if m == nil {
		return
	}
	fprintlnW(w)
	if full {
		fprintlnW(w, "Mappings:")
		body, err := json.MarshalIndent(m.Properties, "  ", "  ")
		if err != nil {
			fprintfW(w, "  (failed to render mappings: %v)\n", err)
			return
		}
		fprintfW(w, "  %s\n", body)
		if m.Dynamic != "" {
			fprintfW(w, "  dynamic: %s\n", m.Dynamic)
		}
		return
	}
	summary := summariseMappings(m.Properties)
	fprintlnW(w, "Mappings:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  Fields:\t%d total\n", summary.total)
	if len(summary.byType) > 0 {
		parts := make([]string, 0, len(summary.byType))
		ks := make([]string, 0, len(summary.byType))
		for k := range summary.byType {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			parts = append(parts, fmt.Sprintf("%d %s", summary.byType[k], k))
		}
		fprintfTW(tw, "  By type:\t%s\n", strings.Join(parts, ", "))
	}
	if m.Dynamic != "" {
		fprintfTW(tw, "  Dynamic:\t%s\n", m.Dynamic)
	}
	_ = tw.Flush()
}

type mappingSummary struct {
	total  int
	byType map[string]int
}

// summariseMappings walks a properties tree and returns per-type field
// counts. Multi-fields under `fields` count as their own typed entries
// so a `url` with sub-fields `raw`/`domain` (both keyword) yields three
// entries — matches what operators mean by "how many indexed fields".
func summariseMappings(props map[string]any) mappingSummary {
	out := mappingSummary{byType: map[string]int{}}
	walkProperties(props, &out)
	return out
}

func walkProperties(props map[string]any, out *mappingSummary) {
	for _, raw := range props {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := field["type"].(string); ok {
			out.total++
			out.byType[t]++
		}
		if sub, ok := field["properties"].(map[string]any); ok {
			walkProperties(sub, out)
		}
		if sub, ok := field["fields"].(map[string]any); ok {
			walkProperties(sub, out)
		}
	}
}

func writeShards(w io.Writer, shards []types.Shard, opts IndexDescribeOptions) {
	if len(shards) == 0 {
		return
	}
	force := opts.ShowShards != nil && *opts.ShowShards
	hide := opts.ShowShards != nil && !*opts.ShowShards
	if hide {
		return
	}
	if !force && opts.AutoShardMax > 0 && len(shards) > opts.AutoShardMax {
		fprintlnW(w)
		fprintfW(w, "Shards:  %d total (pass --show-shards or --output wide for the per-shard listing)\n", len(shards))
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Shards:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range shards {
		role := "r"
		if s.Primary {
			role = "p"
		}
		node := s.Node
		if node == "" {
			node = "-"
		}
		fprintfTW(tw, "  [%s%d]\t%s\t%s\t%s\n",
			role, s.Shard, node, s.State, humanBytes(s.StoreBytes))
	}
	_ = tw.Flush()
}

// writeRecovery renders any in-flight recovery. Source is surfaced
// when populated — for snapshot-driven recoveries it carries the
// (repository, snapshot, index) tuple so an operator polling a
// multi-snapshot restore can tell which restore the shard belongs to.
func writeRecovery(w io.Writer, r *types.IndexRecovery) {
	if r == nil || len(r.Shards) == 0 {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Recovery (in progress):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range r.Shards {
		role := "r"
		if s.Primary {
			role = "p"
		}
		bytes := s.BytesPercent
		if bytes == "" {
			bytes = "-"
		}
		files := s.FilesPercent
		if files == "" {
			files = "-"
		}
		source := ""
		if s.Source != "" {
			source = "\tsource=" + s.Source
		}
		fprintfTW(tw, "  [%s%d]\t%s\tstage=%s\tbytes=%s\tfiles=%s%s\n",
			role, s.ID, s.Type, s.Stage, bytes, files, source)
	}
	_ = tw.Flush()
}

func writeSegments(w io.Writer, s *types.IndexSegments) {
	if s == nil {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Segments:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  Total:\t%d (primary: %d)\n", s.SegmentsTotal, s.SegmentsPrimary)
	fprintfTW(tw, "  Docs:\t%d total, %d deleted\n", s.DocsTotal, s.DocsDeleted)
	if s.MaxSegmentsShard > 0 {
		fprintfTW(tw, "  Max per shard:\t%d\n", s.MaxSegmentsShard)
	}
	_ = tw.Flush()
}

func writeTasks(w io.Writer, tasks []types.Task) {
	if len(tasks) == 0 {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Tasks (in flight, touching this index):")
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

func writeWarnings(w io.Writer, warns map[string]string) {
	if len(warns) == 0 {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Section warnings:")
	keys := make([]string, 0, len(warns))
	for k := range warns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, k := range keys {
		fprintfTW(tw, "  %s:\t%s\n", k, warns[k])
	}
	_ = tw.Flush()
}

func humanAge(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		return "just now"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

func fprintfTW(tw *tabwriter.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(tw, format, args...)
}

// fprintlnW and fprintfW are the bare-writer siblings of fprintfTW
// — same purpose, same intentional error-swallowing, but for the
// non-tabwriter sections of describe renderers (headers, blank-line
// separators, bodies). Stdout / stderr writes can't realistically
// fail; for everything else, the upstream Format dispatcher already
// returns error from the JSON/YAML branches where it matters.
// Centralising the swallow here keeps both errcheck and the
// codebase noise-free.
func fprintlnW(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

func fprintfW(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
