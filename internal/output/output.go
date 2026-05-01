// Package output renders domain types in the formats exposed by the CLI
// (table, json, yaml, wide). NDJSON is deferred until a streaming
// command needs it.
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

	"github.com/dj-wasabi/esops-go/internal/types"
)

// Format identifies a user-facing output format.
type Format string

const (
	FormatTable  Format = "table"
	FormatJSON   Format = "json"
	FormatYAML   Format = "yaml"
	FormatWide   Format = "wide"
	FormatNDJSON Format = "ndjson"
)

// Parse validates and normalises a --output value. Per-command
// restrictions (e.g. "health doesn't support ndjson") are enforced by
// cmdsetup.Prepare using the command's SupportedFormats allowlist — the
// renderers here assume the format has already been cleared for use.
func Parse(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "table":
		return FormatTable, nil
	case "json":
		return FormatJSON, nil
	case "yaml", "yml":
		return FormatYAML, nil
	case "wide":
		return FormatWide, nil
	case "ndjson":
		return FormatNDJSON, nil
	default:
		return "", fmt.Errorf("unknown output format %q (supported: table, json, ndjson, yaml, wide)", s)
	}
}

// Health renders a ClusterHealth in the requested format to w.
// For JSON/YAML, only the health record is written (dialect/context
// metadata goes to stderr via the caller); scripts get a stable shape.
func Health(w io.Writer, h types.ClusterHealth, info types.ClusterInfo, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(h)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(h)
	case FormatWide:
		return healthTable(w, h, info, true)
	case FormatTable, "":
		return healthTable(w, h, info, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Shards renders a slice of Shard rows in the requested format. Table
// output shows the columns an operator reaches for first during an
// incident (index, shard, role, state, node, size). Wide adds docs,
// IP, and the raw STORE_BYTES. JSON/YAML emit the neutral Shard shape
// unchanged — scripts depend on stable field names. NDJSON writes one
// Shard per line so a 200k-shard pipe doesn't have to be parsed as a
// single blob downstream.
func Shards(w io.Writer, shards []types.Shard, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(shards)
	case FormatNDJSON:
		enc := json.NewEncoder(w)
		for _, s := range shards {
			if err := enc.Encode(s); err != nil {
				return err
			}
		}
		return nil
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(shards)
	case FormatWide:
		return shardsTable(w, shards, true)
	case FormatTable, "":
		return shardsTable(w, shards, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// IndicesNDJSONStream writes one Index per line to w as records arrive
// from the supplied callback-shaped source. Honours CLAUDE.md §6: a
// 20k-index cluster's first row reaches stdout before the cluster has
// finished sending the last one. Use with index.ListStream.
func IndicesNDJSONStream(w io.Writer, source func(yield func(types.Index) error) error) error {
	enc := json.NewEncoder(w)
	return source(func(ix types.Index) error {
		return enc.Encode(ix)
	})
}

// Indices renders a slice of Index rows in the requested format. Table
// output shows the columns an operator reaches for when triaging an
// index-count or size question (name, health, status, primaries/replicas,
// docs, size). Wide adds deleted docs, UUID, creation date and
// primary-store bytes. JSON/YAML emit the neutral Index shape;
// NDJSON writes one Index per line — but if the caller has a row count
// it cares about, prefer IndicesNDJSONStream so memory doesn't scale
// with cluster size.
func Indices(w io.Writer, indices []types.Index, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(indices)
	case FormatNDJSON:
		enc := json.NewEncoder(w)
		for _, ix := range indices {
			if err := enc.Encode(ix); err != nil {
				return err
			}
		}
		return nil
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(indices)
	case FormatWide:
		return indicesTable(w, indices, true)
	case FormatTable, "":
		return indicesTable(w, indices, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// IndexStateChange renders the outcome of `esops index open` or
// `esops index close`. Table format is a two-section layout: a one-line
// header (action, dry-run, ack flags, counts) followed by a list of the
// transitioned index names. Skipped (already-in-state) indices are
// shown only in wide mode — the default focuses on what changed. JSON/
// YAML emit the neutral IndexStateChange shape directly.
func IndexStateChange(w io.Writer, r types.IndexStateChange, f Format) error {
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
		return indexStateChangeTable(w, r, true)
	case FormatTable, "":
		return indexStateChangeTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// IndexOptimize renders the outcome of `esops index optimize`. Table
// format is a header line (action, dry-run, indices, shard counts)
// followed by a list of the targeted index names. Any failures (closed-
// index skips or per-shard merge errors) are printed in a FAILURES
// block. JSON/YAML emit the neutral IndexOptimizeResult shape directly.
func IndexOptimize(w io.Writer, r types.IndexOptimizeResult, f Format) error {
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
		return indexOptimizeTable(w, r, true)
	case FormatTable, "":
		return indexOptimizeTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexOptimizeTable(w io.Writer, r types.IndexOptimizeResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tINDICES\tMAX_SEGMENTS\tSHARDS_OK\tSHARDS_FAILED")
	maxSeg := "-"
	if r.MaxNumSegments > 0 {
		maxSeg = fmt.Sprintf("%d", r.MaxNumSegments)
	}
	_, _ = fmt.Fprintf(hw, "%s\t%t\t%d\t%s\t%d\t%d\n",
		r.Action, r.DryRun, len(r.Indices), maxSeg,
		r.ShardsSuccess, r.ShardsFailed,
	)
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(r.Indices) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "INDICES")
		for _, name := range r.Indices {
			_, _ = fmt.Fprintf(w, "  %s\n", name)
		}
	}

	if len(r.Failures) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "FAILURES")
		for _, msg := range r.Failures {
			_, _ = fmt.Fprintf(w, "  %s\n", msg)
		}
	}
	_ = wide
	return nil
}

// IndexShrink renders the outcome of `esops index shrink`. Table
// format is a one-line header (action, dry-run, source, target, ack
// flags, target shards). JSON/YAML emit the neutral
// IndexShrinkResult shape directly.
func IndexShrink(w io.Writer, r types.IndexShrinkResult, f Format) error {
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
		return indexShrinkTable(w, r, true)
	case FormatTable, "":
		return indexShrinkTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexShrinkTable(w io.Writer, r types.IndexShrinkResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tSOURCE\tTARGET\tSHARDS\tACKNOWLEDGED\tSHARDS_ACK")
	shards := "-"
	if r.TargetShards > 0 {
		shards = fmt.Sprintf("%d", r.TargetShards)
	}
	_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%s\t%s\t%t\t%t\n",
		r.Action, r.DryRun,
		dashIfEmpty(r.Source), dashIfEmpty(r.Target),
		shards,
		r.Acknowledged, r.ShardsAcknowledged,
	)
	_ = wide
	return hw.Flush()
}

// Aliases renders the cat-aliases view for `esops index aliases list`.
// Table output shows the columns operators reach for first (alias,
// index, write-index flag, filter presence). Wide adds the routing
// columns and the raw filter JSON. JSON/YAML emit the neutral Alias
// slice; NDJSON writes one record per line so a cluster with tens of
// thousands of aliases doesn't have to be parsed as one blob.
// AliasesNDJSONStream writes one Alias per line to w as records
// arrive from the supplied callback-shaped source. Honours CLAUDE.md
// §6 for `aliases list`.
func AliasesNDJSONStream(w io.Writer, source func(yield func(types.Alias) error) error) error {
	enc := json.NewEncoder(w)
	return source(func(a types.Alias) error {
		return enc.Encode(a)
	})
}

func Aliases(w io.Writer, aliases []types.Alias, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(aliases)
	case FormatNDJSON:
		enc := json.NewEncoder(w)
		for _, a := range aliases {
			if err := enc.Encode(a); err != nil {
				return err
			}
		}
		return nil
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(aliases)
	case FormatWide:
		return aliasesTable(w, aliases, true)
	case FormatTable, "":
		return aliasesTable(w, aliases, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func aliasesTable(w io.Writer, aliases []types.Alias, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ALIAS\tINDEX\tWRITE_INDEX\tROUTING_INDEX\tROUTING_SEARCH\tFILTER")
	} else {
		_, _ = fmt.Fprintln(tw, "ALIAS\tINDEX\tWRITE_INDEX\tFILTERED")
	}
	for _, a := range aliases {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\t%s\n",
				a.Alias, a.Index, a.IsWriteIndex,
				dashIfEmpty(a.RoutingIndex), dashIfEmpty(a.RoutingSearch),
				dashIfEmpty(a.Filter),
			)
		} else {
			filtered := "no"
			if a.Filter != "" {
				filtered = "yes"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%s\n",
				a.Alias, a.Index, a.IsWriteIndex, filtered,
			)
		}
	}
	return tw.Flush()
}

// AliasUpdate renders the outcome of `esops index aliases add` /
// `esops index aliases remove`. Table format is a one-line header
// (action, dry-run, ack) followed by a per-action summary line. JSON/
// YAML emit the neutral AliasUpdateResult shape directly.
func AliasUpdate(w io.Writer, r types.AliasUpdateResult, f Format) error {
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
		return aliasUpdateTable(w, r, true)
	case FormatTable, "":
		return aliasUpdateTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func aliasUpdateTable(w io.Writer, r types.AliasUpdateResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tACKNOWLEDGED\tACTIONS")
	_, _ = fmt.Fprintf(hw, "%s\t%t\t%t\t%d\n",
		r.Action, r.DryRun, r.Acknowledged, len(r.Actions),
	)
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(r.Actions) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	aw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(aw, "ACTION\tALIAS\tINDEX\tFILTER\tROUTING\tWRITE_INDEX")
	} else {
		_, _ = fmt.Fprintln(aw, "ACTION\tALIAS\tINDEX")
	}
	for _, a := range r.Actions {
		if wide {
			writeIndex := "-"
			if a.IsWriteIndex != nil {
				writeIndex = fmt.Sprintf("%t", *a.IsWriteIndex)
			}
			_, _ = fmt.Fprintf(aw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				a.Action, a.Alias, a.Index,
				dashIfEmpty(a.Filter), dashIfEmpty(a.Routing), writeIndex,
			)
		} else {
			_, _ = fmt.Fprintf(aw, "%s\t%s\t%s\n",
				a.Action, a.Alias, a.Index,
			)
		}
	}
	return aw.Flush()
}

// IndexSettings renders the read-only settings view from `esops index
// settings get`. Table output flattens each index's nested settings
// map to dotted keys (so the columns line up regardless of how deep a
// setting nests) and emits one row per (index, key, value) triple.
// JSON/YAML emit the neutral []IndexSettings shape directly. NDJSON
// streams one IndexSettings record per line so a cluster with tens of
// thousands of indices doesn't have to be parsed as a single blob.
// IndexSettingsNDJSONStream writes one IndexSettings per line to w as
// records arrive from the supplied callback-shaped source. Honours
// CLAUDE.md §6 for `settings get`.
func IndexSettingsNDJSONStream(w io.Writer, source func(yield func(types.IndexSettings) error) error) error {
	enc := json.NewEncoder(w)
	return source(func(r types.IndexSettings) error {
		return enc.Encode(r)
	})
}

func IndexSettings(w io.Writer, rows []types.IndexSettings, f Format) error {
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
		return indexSettingsTable(w, rows, true)
	case FormatTable, "":
		return indexSettingsTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexSettingsTable(w io.Writer, rows []types.IndexSettings, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "INDEX\tKEY\tVALUE\tSOURCE")
	for _, r := range rows {
		flatSet := flattenSettings(r.Settings)
		flatDef := flattenSettings(r.Defaults)
		if len(flatSet) == 0 && len(flatDef) == 0 {
			_, _ = fmt.Fprintf(tw, "%s\t-\t-\t-\n", r.Index)
			continue
		}
		// Configured keys win over defaults — the cluster reports both
		// blocks separately and "configured" is the load-bearing answer
		// to "is this knob actually set". keyset is the merged sorted
		// view; per-row source comes from which map the key lives in.
		keyset := make(map[string]struct{}, len(flatSet)+len(flatDef))
		for k := range flatSet {
			keyset[k] = struct{}{}
		}
		for k := range flatDef {
			keyset[k] = struct{}{}
		}
		keys := make([]string, 0, len(keyset))
		for k := range keyset {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val, source := settingsLookup(flatSet, flatDef, k)
			v := settingValueString(val)
			if !wide {
				v = truncate(v, 60)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Index, k, v, source)
		}
	}
	return tw.Flush()
}

// settingsLookup picks the cluster's effective value for a flattened
// dotted key: a configured value wins over the default. Returns
// "configured" or "default" so the renderer can mark the source.
func settingsLookup(set, def map[string]any, key string) (any, string) {
	if v, ok := set[key]; ok {
		return v, "configured"
	}
	return def[key], "default"
}

// flattenSettings walks a nested settings map and produces a flat map
// of dotted keys. Cluster responses commonly nest "index" → "routing"
// → "allocation" → "require" → "_name"; the table flattening makes
// "index.routing.allocation.require._name" greppable in one line.
//
// Leaves are returned as their decoded JSON value (string / number /
// bool / nil / slice). Arrays are kept as slices so settingValueString
// can render them compactly without losing the round-trip shape in
// JSON output.
func flattenSettings(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any)
	flattenInto(out, "", m)
	return out
}

func flattenInto(out map[string]any, prefix string, v any) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			out[prefix] = "{}"
			return
		}
		for k, vv := range t {
			next := k
			if prefix != "" {
				next = prefix + "." + k
			}
			flattenInto(out, next, vv)
		}
	default:
		out[prefix] = v
	}
}

// settingValueString renders a setting value the way scripts and
// operators expect to see it: strings unquoted, numbers as their
// canonical decoded form, slices joined by commas. Anything we don't
// recognise falls through to fmt.Sprint.
func settingValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, settingValueString(e))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(v)
	}
}

// IndexSettingsUpdate renders the outcome of `esops index settings
// set`. Table format is a one-line header (action, dry-run, ack,
// indices count) followed by the resolved index list. JSON/YAML emit
// the neutral IndexSettingsUpdateResult shape directly.
func IndexSettingsUpdate(w io.Writer, r types.IndexSettingsUpdateResult, f Format) error {
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
		return indexSettingsUpdateTable(w, r, true)
	case FormatTable, "":
		return indexSettingsUpdateTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexSettingsUpdateTable(w io.Writer, r types.IndexSettingsUpdateResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tACKNOWLEDGED\tINDICES")
	_, _ = fmt.Fprintf(hw, "%s\t%t\t%t\t%d\n",
		r.Action, r.DryRun, r.Acknowledged, len(r.Indices),
	)
	if err := hw.Flush(); err != nil {
		return err
	}
	if len(r.Indices) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "INDICES")
		for _, name := range r.Indices {
			_, _ = fmt.Fprintf(w, "  %s\n", name)
		}
	}
	if wide && r.Body != "" {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "BODY")
		_, _ = fmt.Fprintf(w, "  %s\n", r.Body)
	}
	return nil
}

// IndexTemplates renders the cluster-side template list for
// `esops index template list`. Table format is one row per template,
// showing the columns operators reach for first (name, priority,
// version, the count and first three index patterns). Wide adds the
// composed_of list and the full pattern set. JSON/YAML emit the neutral
// []IndexTemplate shape directly. NDJSON streams one record per line.
func IndexTemplates(w io.Writer, rows []types.IndexTemplate, f Format) error {
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
		return indexTemplatesTable(w, rows, true)
	case FormatTable, "":
		return indexTemplatesTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexTemplatesTable(w io.Writer, rows []types.IndexTemplate, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tPRIORITY\tVERSION\tINDEX_PATTERNS\tCOMPOSED_OF\tDATA_STREAM")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tPRIORITY\tVERSION\tINDEX_PATTERNS")
	}
	for _, r := range rows {
		priority := "-"
		if r.Priority > 0 {
			priority = fmt.Sprintf("%d", r.Priority)
		}
		version := "-"
		if r.Version > 0 {
			version = fmt.Sprintf("%d", r.Version)
		}
		patterns := joinOrDash(r.IndexPatterns)
		if !wide {
			patterns = truncate(patterns, 50)
		}
		if wide {
			ds := "no"
			if len(r.DataStream) > 0 {
				ds = "yes"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Name, priority, version, patterns, joinOrDash(r.ComposedOf), ds)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				r.Name, priority, version, patterns)
		}
	}
	return tw.Flush()
}

// IndexTemplate renders a single template — `esops index template get`.
// JSON/YAML emit the neutral IndexTemplate shape directly. Table format
// is a multi-section human view: a one-line header (name, priority,
// version) followed by the index_patterns / composed_of lists and a
// pretty-printed template body block. The body is rendered as
// indented JSON so an operator pasting it into a ticket sees the same
// shape they would feed back into `template put --from-file`.
func IndexTemplate(w io.Writer, t types.IndexTemplate, f Format) error {
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
	case FormatWide:
		return indexTemplateDetail(w, t, true)
	case FormatTable, "":
		return indexTemplateDetail(w, t, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexTemplateDetail(w io.Writer, t types.IndexTemplate, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "NAME\tPRIORITY\tVERSION\tINDEX_PATTERNS\tCOMPOSED_OF")
	priority := "-"
	if t.Priority > 0 {
		priority = fmt.Sprintf("%d", t.Priority)
	}
	version := "-"
	if t.Version > 0 {
		version = fmt.Sprintf("%d", t.Version)
	}
	_, _ = fmt.Fprintf(hw, "%s\t%s\t%s\t%s\t%s\n",
		t.Name, priority, version, joinOrDash(t.IndexPatterns), joinOrDash(t.ComposedOf))
	if err := hw.Flush(); err != nil {
		return err
	}
	if !wide {
		return nil
	}
	if len(t.Template) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "TEMPLATE")
		buf, err := json.MarshalIndent(t.Template, "  ", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
	}
	if len(t.DataStream) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "DATA_STREAM")
		buf, err := json.MarshalIndent(t.DataStream, "  ", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
	}
	return nil
}

// IndexTemplatePut renders the outcome of `esops index template put`.
// One-line header (action, dry-run, name, ack). JSON/YAML emit the
// neutral IndexTemplatePutResult shape directly.
func IndexTemplatePut(w io.Writer, r types.IndexTemplatePutResult, f Format) error {
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
	case FormatTable, FormatWide, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tNAME\tACKNOWLEDGED")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.Name), r.Acknowledged)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// IndexTemplateDelete mirrors IndexTemplatePut for the delete path.
func IndexTemplateDelete(w io.Writer, r types.IndexTemplateDeleteResult, f Format) error {
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
	case FormatTable, FormatWide, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tNAME\tACKNOWLEDGED")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.Name), r.Acknowledged)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// IndexRollover renders the outcome of `esops index rollover`. Table
// format is a one-line header (alias, rolled_over, dry_run, old/new
// index) followed by a per-condition table when the cluster reported
// any. JSON/YAML emit the neutral IndexRolloverResult shape directly.
func IndexRollover(w io.Writer, r types.IndexRolloverResult, f Format) error {
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
		return indexRolloverTable(w, r, true)
	case FormatTable, "":
		return indexRolloverTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func indexRolloverTable(w io.Writer, r types.IndexRolloverResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ALIAS\tROLLED_OVER\tDRY_RUN\tACKNOWLEDGED\tOLD_INDEX\tNEW_INDEX")
	_, _ = fmt.Fprintf(hw, "%s\t%t\t%t\t%t\t%s\t%s\n",
		r.Alias, r.RolledOver, r.DryRun, r.Acknowledged,
		dashIfEmpty(r.OldIndex), dashIfEmpty(r.NewIndex),
	)
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(r.ConditionsMet) > 0 {
		// Stable order — map iteration is randomised, and a captured
		// stdout (saved to a ticket) is more useful when the rows line
		// up across runs.
		keys := make([]string, 0, len(r.ConditionsMet))
		for k := range r.ConditionsMet {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "CONDITIONS")
		cw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(cw, "CONDITION\tMET")
		for _, k := range keys {
			_, _ = fmt.Fprintf(cw, "%s\t%t\n", k, r.ConditionsMet[k])
		}
		if err := cw.Flush(); err != nil {
			return err
		}
	}
	_ = wide
	return nil
}

func indexStateChangeTable(w io.Writer, r types.IndexStateChange, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tACKNOWLEDGED\tSHARDS_ACK\tCHANGED\tSKIPPED")
	_, _ = fmt.Fprintf(hw, "%s\t%t\t%t\t%t\t%d\t%d\n",
		r.Action, r.DryRun, r.Acknowledged, r.ShardsAcknowledged,
		len(r.Indices), len(r.Skipped),
	)
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(r.Indices) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "INDICES")
		for _, name := range r.Indices {
			_, _ = fmt.Fprintf(w, "  %s\n", name)
		}
	}

	if wide && len(r.Skipped) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "SKIPPED (already in target state)")
		for _, name := range r.Skipped {
			_, _ = fmt.Fprintf(w, "  %s\n", name)
		}
	}
	return nil
}

// HotThreads renders a /_nodes/hot_threads response. The cluster returns
// a free-form text block (per-node "::: {node}..." headers followed by
// stack samples) that is the canonical diagnostic format — operators
// read it by eye and paste it into tickets. We preserve it verbatim for
// table/wide; JSON/YAML wrap it in {"text": "..."} so the scripted
// contract ("JSON output is a single object") holds without pretending
// to parse a schema that is really an implementation detail of the
// cluster's own hot-threads printer.
func HotThreads(w io.Writer, h types.HotThreads, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(h)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(h)
	case FormatTable, FormatWide, "":
		_, err := io.WriteString(w, h.Text)
		if err != nil {
			return err
		}
		// Guarantee the output ends on a newline so shell prompts don't
		// land in the middle of the last stack frame. The cluster already
		// usually adds one, but some older ES versions skip the trailing
		// newline on empty responses.
		if !strings.HasSuffix(h.Text, "\n") {
			_, err = io.WriteString(w, "\n")
		}
		return err
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// DiagVerify renders the result of `ops diag verify`. Table format
// shows a header (manifest hash + cluster + summary) and only the
// problem rows by default — when everything passes, no per-artifact
// table is rendered, just the summary line. Wide adds the full
// per-artifact list including OK rows. JSON/YAML emit the neutral
// DiagVerifyResult shape directly.
//
// The `label` is the operator-facing source name (file path or "<stdin>");
// surfaced in the header so the table self-documents which bundle was
// checked.
func DiagVerify(w io.Writer, label string, r types.DiagVerifyResult, f Format) error {
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
		return diagVerifyTable(w, label, r, true)
	case FormatTable, "":
		return diagVerifyTable(w, label, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func diagVerifyTable(w io.Writer, label string, r types.DiagVerifyResult, wide bool) error {
	_, _ = fmt.Fprintf(w, "BUNDLE          %s\n", label)
	_, _ = fmt.Fprintf(w, "MANIFEST_SHA256 %s\n", r.ManifestSHA256)
	if r.Cluster.Name != "" || r.Cluster.Dialect != "" {
		_, _ = fmt.Fprintf(w, "CLUSTER         %s (%s %s)\n",
			dashIfEmpty(r.Cluster.Name), r.Cluster.Dialect, r.Cluster.Version)
	}
	if r.CreatedAt != "" {
		_, _ = fmt.Fprintf(w, "CREATED         %s\n", r.CreatedAt)
	}
	_, _ = fmt.Fprintln(w)

	verdict := "PASS"
	if !r.Pass {
		verdict = "FAIL"
	}
	_, _ = fmt.Fprintf(w,
		"SUMMARY  %s  %d artifacts  ok=%d  mismatched=%d  missing=%d  no_hash=%d  unknown=%d\n",
		verdict, r.Summary.Total, r.Summary.OK,
		r.Summary.Mismatched, r.Summary.Missing,
		r.Summary.NoHash, r.Summary.Unknown,
	)

	// In default (non-wide) mode, only show problem rows. A clean run
	// shouldn't fill the screen with green checkmarks — the SUMMARY
	// line is the answer. Wide mode shows everything for the operator
	// who wants the full per-artifact view.
	rows := r.Artifacts
	if !wide {
		filtered := rows[:0:0]
		for _, a := range r.Artifacts {
			if a.Status != types.DiagVerifyOK {
				filtered = append(filtered, a)
			}
		}
		rows = filtered
	}

	if len(rows) > 0 {
		_, _ = fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "STATUS\tNAME\tFILE\tEXPECTED\tACTUAL")
		for _, a := range rows {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				strings.ToUpper(string(a.Status)),
				a.Name, a.File,
				shortHash(a.Expected), shortHash(a.Actual),
			)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(r.UnknownEntries) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "UNKNOWN ENTRIES (in tarball, not listed in manifest)")
		for _, name := range r.UnknownEntries {
			_, _ = fmt.Fprintf(w, "  %s\n", name)
		}
	}
	return nil
}

// shortHash clips a hex SHA-256 to its first 12 characters for table
// rendering — that's enough to spot mismatches by eye while keeping
// columns narrow. JSON/YAML output preserves the full hash.
func shortHash(h string) string {
	if h == "" {
		return "-"
	}
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

// Audit renders a SecurityAudit — the full `ops audit` view. JSON/YAML
// emit the whole struct as one object so scripts can read TLS, status,
// users, and roles in a single parse. The table format is a four-section
// layout (TLS, status, users, roles) read top-to-bottom; wide adds
// backend-role / email / description columns that are too wide for the
// default view.
//
// When the cluster reports security as disabled, the users and roles
// sections are suppressed — the status line carries the explanation.
func Audit(w io.Writer, a types.SecurityAudit, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(a)
	case FormatWide:
		return auditTable(w, a, true)
	case FormatTable, "":
		return auditTable(w, a, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Rebalance renders a /_cluster/reroute outcome. Table output is a
// two-section layout: a one-line "did the master ack? was it a dry
// run?" header, then a per-command summary listing the action and
// the {from,to} nodes the command targeted. Wide mode adds the cluster's
// per-decider explanations[] block when the request was made with
// --explain — operators reading "the cluster refused my move, why?" want
// the deciders inline, not behind a second command. JSON/YAML emit the
// neutral RerouteResult shape directly.
func Rebalance(w io.Writer, r types.RerouteResult, f Format) error {
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
		return rebalanceTable(w, r, true)
	case FormatTable, "":
		return rebalanceTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func rebalanceTable(w io.Writer, r types.RerouteResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ACKNOWLEDGED\tDRY_RUN\tCOMMANDS")
	_, _ = fmt.Fprintf(hw, "%t\t%t\t%d\n", r.Acknowledged, r.DryRun, len(r.Commands))
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(r.Commands) > 0 {
		_, _ = fmt.Fprintln(w)
		cw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(cw, "ACTION\tINDEX\tSHARD\tFROM\tTO\tFLAGS")
		for _, cmd := range r.Commands {
			_, _ = fmt.Fprintf(cw, "%s\t%s\t%d\t%s\t%s\t%s\n",
				cmd.Action,
				dashIfEmpty(cmd.Index),
				cmd.Shard,
				dashIfEmpty(cmd.FromNode),
				dashIfEmpty(cmd.ToNode),
				dashIfEmpty(rebalanceFlags(cmd)),
			)
		}
		if err := cw.Flush(); err != nil {
			return err
		}
	}

	if len(r.Explanations) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "EXPLANATIONS")
	ew := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(ew, "COMMAND\tDECIDER\tDECISION\tEXPLANATION")
		for _, ex := range r.Explanations {
			if len(ex.Decisions) == 0 {
				_, _ = fmt.Fprintf(ew, "%s\t-\t-\t-\n", ex.Command)
				continue
			}
			for _, d := range ex.Decisions {
				_, _ = fmt.Fprintf(ew, "%s\t%s\t%s\t%s\n",
					ex.Command, d.Decider, d.Decision, d.Explanation)
			}
		}
	} else {
		_, _ = fmt.Fprintln(ew, "COMMAND\tDECISIONS")
		for _, ex := range r.Explanations {
			_, _ = fmt.Fprintf(ew, "%s\t%s\n", ex.Command, summariseRerouteDecisions(ex.Decisions))
		}
	}
	return ew.Flush()
}

// rebalanceFlags renders the boolean knobs (allow_primary,
// accept_data_loss) as a compact "k1,k2" cell — only flags that are
// actually set make it into the cell, so the column reads "-" for the
// happy path of a routine move.
func rebalanceFlags(cmd types.RerouteCommand) string {
	parts := make([]string, 0, 2)
	if cmd.AllowPrimary {
		parts = append(parts, "allow_primary")
	}
	if cmd.AcceptDataLoss {
		parts = append(parts, "accept_data_loss")
	}
	return strings.Join(parts, ",")
}

// summariseRerouteDecisions compresses a deciders list into a single
// cell for the default table — same logic as the allocation-explain
// summary: NO/THROTTLE rulings carry the signal, YES rulings are noise
// unless the operator asked for them.
func summariseRerouteDecisions(ds []types.RerouteDecision) string {
	if len(ds) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		if strings.EqualFold(d.Decision, "YES") {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", d.Decider, strings.ToLower(d.Decision)))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func auditTable(w io.Writer, a types.SecurityAudit, wide bool) error {
	// Section 1: TLS posture. Four compact key/value lines; no tabwriter
	// needed — the short keys keep alignment without it.
	_, _ = fmt.Fprintln(w, "TLS")
	_, _ = fmt.Fprintf(w, "  scheme:     %s\n", a.TLS.Scheme)
	_, _ = fmt.Fprintf(w, "  insecure:   %t\n", a.TLS.InsecureSkipVerify)
	_, _ = fmt.Fprintf(w, "  cacert_set: %t\n", a.TLS.CACertSet)
	_, _ = fmt.Fprintf(w, "  auth:       %s\n", a.TLS.AuthType)
	if a.TLS.ClientCertSet {
		_, _ = fmt.Fprintln(w, "  client_cert: true")
	}

	// Section 2: cluster-side status. When security is disabled the note
	// is the load-bearing piece; elevate it so the operator can't miss it.
	// Per-collection notes (forbidden on api_keys, etc.) follow as a list
	// — they don't disable the audit but the operator should see what
	// went unfetched so the absence is explicit, not silent.
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "SECURITY")
	_, _ = fmt.Fprintf(w, "  dialect:  %s\n", a.Status.Dialect)
	_, _ = fmt.Fprintf(w, "  enabled:  %t\n", a.Status.Enabled)
	if a.Status.Note != "" {
		_, _ = fmt.Fprintf(w, "  note:     %s\n", a.Status.Note)
	}
	for _, n := range a.Status.Notes {
		_, _ = fmt.Fprintf(w, "  note:     %s\n", n)
	}

	if !a.Status.Enabled {
		return nil
	}

	// Section 3: users table. Reserved flag rendered as "res" — short
	// enough to keep the table narrow, obvious in meaning from context.
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "USERS")
	uw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(uw, "USERNAME\tENABLED\tRESERVED\tROLES\tBACKEND_ROLES\tFULL_NAME\tEMAIL")
	} else {
		_, _ = fmt.Fprintln(uw, "USERNAME\tENABLED\tRESERVED\tROLES")
	}
	for _, u := range a.Users {
		if wide {
			_, _ = fmt.Fprintf(uw, "%s\t%t\t%t\t%s\t%s\t%s\t%s\n",
				u.Username, u.Enabled, u.Reserved,
				joinOrDash(u.Roles), joinOrDash(u.BackendRoles),
				dashIfEmpty(u.FullName), dashIfEmpty(u.Email))
		} else {
			_, _ = fmt.Fprintf(uw, "%s\t%t\t%t\t%s\n",
				u.Username, u.Enabled, u.Reserved, joinOrDash(u.Roles))
		}
	}
	if err := uw.Flush(); err != nil {
		return err
	}

	// Section 4: roles table. The cluster privileges column is truncated
	// in the default view since "all" is often short but cluster lists
	// can be long; wide mode shows the full list.
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "ROLES")
	rw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(rw, "NAME\tRESERVED\tCLUSTER\tINDEX_PATTERNS\tDESCRIPTION")
	} else {
		_, _ = fmt.Fprintln(rw, "NAME\tRESERVED\tCLUSTER\tINDEX_PATTERNS")
	}
	for _, r := range a.Roles {
		if wide {
			_, _ = fmt.Fprintf(rw, "%s\t%t\t%s\t%s\t%s\n",
				r.Name, r.Reserved,
				joinOrDash(r.ClusterPrivileges), joinOrDash(r.IndexPatterns),
				dashIfEmpty(r.Description))
		} else {
			_, _ = fmt.Fprintf(rw, "%s\t%t\t%s\t%s\n",
				r.Name, r.Reserved,
				truncate(joinOrDash(r.ClusterPrivileges), 40),
				truncate(joinOrDash(r.IndexPatterns), 40))
		}
	}
	if err := rw.Flush(); err != nil {
		return err
	}

	// Section 5: role mappings (both dialects when present). Surfaces
	// the link between external identity (LDAP groups, OIDC claims)
	// and product roles — the bridge an audit reading "who can do X?"
	// has to cross.
	if len(a.RoleMappings) > 0 {
		if err := renderRoleMappings(w, a.RoleMappings, wide); err != nil {
			return err
		}
	}

	// Section 6: ES API keys. Surfaces every active key the auth user
	// can see, including invalidated ones — an audit specifically wants
	// "this key was revoked, when, by whom" in the trail.
	if len(a.APIKeys) > 0 {
		if err := renderAPIKeys(w, a.APIKeys, wide); err != nil {
			return err
		}
	}

	// Section 7: OS tenants. Multi-tenancy gates which Dashboards
	// saved-objects each user sees; missing it from an audit would
	// miss a real lever.
	if len(a.Tenants) > 0 {
		if err := renderTenants(w, a.Tenants); err != nil {
			return err
		}
	}
	return nil
}

func renderRoleMappings(w io.Writer, mappings []types.SecurityRoleMapping, wide bool) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "ROLE_MAPPINGS")
	mw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(mw, "NAME\tRESERVED\tENABLED\tROLES\tBACKEND_ROLES\tUSERS\tHOSTS")
	} else {
		_, _ = fmt.Fprintln(mw, "NAME\tRESERVED\tENABLED\tROLES\tBACKEND_ROLES\tUSERS")
	}
	for _, m := range mappings {
		if wide {
			_, _ = fmt.Fprintf(mw, "%s\t%t\t%t\t%s\t%s\t%s\t%s\n",
				m.Name, m.Reserved, m.Enabled,
				joinOrDash(m.Roles), joinOrDash(m.BackendRoles),
				joinOrDash(m.Users), joinOrDash(m.Hosts))
		} else {
			_, _ = fmt.Fprintf(mw, "%s\t%t\t%t\t%s\t%s\t%s\n",
				m.Name, m.Reserved, m.Enabled,
				truncate(joinOrDash(m.Roles), 30),
				truncate(joinOrDash(m.BackendRoles), 30),
				truncate(joinOrDash(m.Users), 30))
		}
	}
	return mw.Flush()
}

func renderAPIKeys(w io.Writer, keys []types.SecurityAPIKey, wide bool) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "API_KEYS")
	kw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(kw, "NAME\tID\tUSERNAME\tREALM\tINVALIDATED\tCREATED\tEXPIRES")
	} else {
		_, _ = fmt.Fprintln(kw, "NAME\tUSERNAME\tINVALIDATED\tEXPIRES")
	}
	for _, k := range keys {
		if wide {
			_, _ = fmt.Fprintf(kw, "%s\t%s\t%s\t%s\t%t\t%s\t%s\n",
				k.Name, k.ID, dashIfEmpty(k.Username),
				dashIfEmpty(k.Realm), k.Invalidated,
				dashIfEmpty(k.Creation), dashIfEmpty(k.Expiration))
		} else {
			_, _ = fmt.Fprintf(kw, "%s\t%s\t%t\t%s\n",
				k.Name, dashIfEmpty(k.Username),
				k.Invalidated, dashIfEmpty(k.Expiration))
		}
	}
	return kw.Flush()
}

// renderTenants has no wide variant — the OS tenant model is just
// {name, reserved, description}, so there's no extra column to surface.
// Kept consistent with the other render* helpers in shape so callers
// can swap them in without thinking about which dialect is in play.
func renderTenants(w io.Writer, tenants []types.SecurityTenant) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "TENANTS")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tRESERVED\tDESCRIPTION")
	for _, t := range tenants {
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\n",
			t.Name, t.Reserved, dashIfEmpty(t.Description))
	}
	return tw.Flush()
}

func joinOrDash(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncate clips a cell's content to n runes, appending an ellipsis when
// it had to cut — so the default table stays one row per entity but the
// operator still sees that there's more behind `-o wide`.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// AllocationExplain renders a single allocation-explain record. JSON/YAML
// emit the neutral AllocationExplanation shape directly; the table format
// is a two-section layout operators read top-to-bottom: a header block
// with the shard-level facts ("is it assigned? why not?") followed by a
// per-node decision table. Wide expands the decision table into a full
// deciders dump (one row per decider rule), which is the signal-dense
// view when debugging "why did the cluster not pick node X?".
func AllocationExplain(w io.Writer, ex types.AllocationExplanation, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(ex)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(ex)
	case FormatWide:
		return allocationExplainTable(w, ex, true)
	case FormatTable, "":
		return allocationExplainTable(w, ex, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func allocationExplainTable(w io.Writer, ex types.AllocationExplanation, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	role := "r"
	if ex.Primary {
		role = "p"
	}
	node := "-"
	if ex.CurrentNode != nil && ex.CurrentNode.Name != "" {
		node = ex.CurrentNode.Name
	}
	canAllocate := ex.CanAllocate
	if canAllocate == "" {
		canAllocate = "-"
	}
	_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tNODE\tCAN_ALLOCATE")
	_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
		ex.Index, ex.Shard, role, ex.CurrentState, node, canAllocate)
	if err := tw.Flush(); err != nil {
		return err
	}

	// Narrative block: reason for unassignment + human-language
	// explanation strings. Kept outside the tabwriter so free-form prose
	// wraps naturally in terminals.
	if ex.UnassignedInfo != nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "UNASSIGNED_REASON: %s\n", ex.UnassignedInfo.Reason)
		if ex.UnassignedInfo.At != "" {
			_, _ = fmt.Fprintf(w, "UNASSIGNED_AT:     %s\n", ex.UnassignedInfo.At)
		}
		if ex.UnassignedInfo.LastAllocationStatus != "" {
			_, _ = fmt.Fprintf(w, "LAST_STATUS:       %s\n", ex.UnassignedInfo.LastAllocationStatus)
		}
		if ex.UnassignedInfo.Details != "" {
			_, _ = fmt.Fprintf(w, "DETAILS:           %s\n", ex.UnassignedInfo.Details)
		}
	}
	if ex.AllocateExplanation != "" {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "EXPLANATION: %s\n", ex.AllocateExplanation)
	}
	if ex.MoveExplanation != "" {
		_, _ = fmt.Fprintf(w, "MOVE: %s\n", ex.MoveExplanation)
	}
	if ex.RebalanceExplanation != "" {
		_, _ = fmt.Fprintf(w, "REBALANCE: %s\n", ex.RebalanceExplanation)
	}
	if ex.Note != "" {
		_, _ = fmt.Fprintf(w, "NOTE: %s\n", ex.Note)
	}

	if len(ex.NodeDecisions) == 0 {
		return nil
	}

	_, _ = fmt.Fprintln(w)
	nw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(nw, "NODE\tDECISION\tWEIGHT\tDECIDER\tRULING\tEXPLANATION")
		for _, d := range ex.NodeDecisions {
			if len(d.Deciders) == 0 {
				_, _ = fmt.Fprintf(nw, "%s\t%s\t%d\t-\t-\t-\n",
					d.NodeName, d.NodeDecision, d.WeightRanking)
				continue
			}
			for _, dd := range d.Deciders {
				_, _ = fmt.Fprintf(nw, "%s\t%s\t%d\t%s\t%s\t%s\n",
					d.NodeName, d.NodeDecision, d.WeightRanking,
					dd.Decider, dd.Decision, dd.Explanation)
			}
		}
	} else {
		_, _ = fmt.Fprintln(nw, "NODE\tDECISION\tDECIDERS")
		for _, d := range ex.NodeDecisions {
			_, _ = fmt.Fprintf(nw, "%s\t%s\t%s\n",
				d.NodeName, d.NodeDecision, summariseDeciders(d.Deciders))
		}
	}
	return nw.Flush()
}

// summariseDeciders compresses a node's deciders list into a single
// cell for the default table. Only NO/THROTTLE rulings get surfaced —
// YES rulings are noise unless the operator passed --include-yes.
// Empty or all-yes lists render "-" so the column stays grep-friendly.
func summariseDeciders(ds []types.AllocationDecider) string {
	if len(ds) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		if strings.EqualFold(d.Decision, "YES") {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", d.Decider, strings.ToLower(d.Decision)))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func indicesTable(w io.Writer, indices []types.Index, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tHEALTH\tSTATUS\tPRI\tREP\tDOCS\tDELETED\tSTORE\tPRI_STORE\tUUID\tCREATED")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tHEALTH\tSTATUS\tPRI\tREP\tDOCS\tSTORE")
	}
	for _, ix := range indices {
		health := ix.Health
		if health == "" {
			health = "-"
		}
		if wide {
			uuid := ix.UUID
			if uuid == "" {
				uuid = "-"
			}
			created := formatCreation(ix.CreationDateMillis)
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\n",
				ix.Name, health, ix.Status,
				ix.Primaries, ix.Replicas,
				ix.DocsCount, ix.DocsDeleted,
				humanBytes(ix.StoreBytes), humanBytes(ix.PrimaryStoreBytes),
				uuid, created,
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
				ix.Name, health, ix.Status,
				ix.Primaries, ix.Replicas,
				ix.DocsCount, humanBytes(ix.StoreBytes),
			)
		}
	}
	return tw.Flush()
}

// formatCreation renders an epoch-millis creation timestamp as RFC3339.
// Zero (unknown) renders as "-" so the wide table stays grep-friendly.
func formatCreation(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func shardsTable(w io.Writer, shards []types.Shard, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// REASON is appended to the default table only when at least one row
	// actually has a reason — keeps the happy-path view tight, but makes
	// `ops shards --unassigned` self-explanatory without -o wide.
	showReason := wide
	if !showReason {
		for _, s := range shards {
			if s.UnassignedReason != "" {
				showReason = true
				break
			}
		}
	}

	switch {
	case wide:
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tDOCS\tSTORE\tSTORE_BYTES\tNODE\tIP\tREASON\tSINCE")
	case showReason:
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tSTORE\tNODE\tREASON")
	default:
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tSTORE\tNODE")
	}

	for _, s := range shards {
		role := "r"
		if s.Primary {
			role = "p"
		}
		node := s.Node
		if node == "" {
			node = "-"
		}
		reason := s.UnassignedReason
		if reason == "" {
			reason = "-"
		}
		switch {
		case wide:
			ip := s.IP
			if ip == "" {
				ip = "-"
			}
			since := s.UnassignedFor
			if since == "" {
				since = "-"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%d\t%s\t%s\t%s\t%s\n",
				s.Index, s.Shard, role, s.State,
				s.Docs, humanBytes(s.StoreBytes), s.StoreBytes, node, ip, reason, since)
		case showReason:
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				s.Index, s.Shard, role, s.State, humanBytes(s.StoreBytes), node, reason)
		default:
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
				s.Index, s.Shard, role, s.State, humanBytes(s.StoreBytes), node)
		}
	}
	return tw.Flush()
}

// humanBytes formats byte counts in the same kubectl-ish style used by
// the upstream _cat/shards default output: one decimal place, IEC units.
// A zero-byte shard (unassigned, empty, or system) renders as "0b".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%db", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cb", float64(n)/float64(div), "kmgtpe"[exp])
}

func healthTable(w io.Writer, h types.ClusterHealth, info types.ClusterInfo, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tDIALECT\tVERSION\tNODES\tDATA_NODES\tSHARDS\tPRIMARIES\tRELOC\tINIT\tUNASSIGNED\tPENDING\tACTIVE%")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f\n",
			h.ClusterName, h.Status, info.Dialect, info.Version,
			h.NumberOfNodes, h.NumberOfDataNodes,
			h.ActiveShards, h.ActivePrimaryShards,
			h.RelocatingShards, h.InitializingShards, h.UnassignedShards,
			h.NumberOfPendingTasks, h.ActiveShardsPercentAsNumber,
		)
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tNODES\tDATA_NODES\tSHARDS\tUNASSIGNED\tPENDING")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			h.ClusterName, h.Status,
			h.NumberOfNodes, h.NumberOfDataNodes,
			h.ActiveShards, h.UnassignedShards, h.NumberOfPendingTasks,
		)
	}
	return tw.Flush()
}

// ShardAnalysis renders a ShardAnalysis report. JSON/YAML emit the
// neutral shape unchanged so scripts get a stable schema. Table/wide
// renders the multi-section human view: THRESHOLDS header → PER-NODE
// → TOP → INDICES → UNASSIGNED → SUMMARY. Sections that the request
// excluded come through as nil slices and are skipped here — empty
// section outputs would mislead.
func ShardAnalysis(w io.Writer, a types.ShardAnalysis, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(a)
	case FormatWide:
		return shardAnalysisTable(w, a, true)
	case FormatTable, "":
		return shardAnalysisTable(w, a, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// shardAnalysisTable renders the report as a series of section-headed
// tables separated by blank lines. The header line names the active
// thresholds so a captured stdout (e.g. saved to a ticket) is
// interpretable without re-running the command.
//
// wide currently adds nothing — kept for symmetry with other renderers
// and to leave a hook open for future per-shard / per-node columns
// without an API change.
func shardAnalysisTable(w io.Writer, a types.ShardAnalysis, wide bool) error {
	_ = wide

	t := a.Thresholds
	_, _ = fmt.Fprintf(w, "THRESHOLDS  oversized=%s  critical=%s  undersized=%s  skew=%d%%\n",
		humanBytes(t.OversizedBytes), humanBytes(t.CriticalBytes),
		humanBytes(t.UndersizedBytes), t.SkewPercent,
	)

	if a.Nodes != nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "PER-NODE DISTRIBUTION")
		if a.Summary.SkewSkippedReason != "" {
			_, _ = fmt.Fprintf(w, "(skew analysis skipped: %s)\n", a.Summary.SkewSkippedReason)
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NODE\tSHARDS\tSTORE\tSHARDS_VS_MED\tSTORE_VS_MED\tSTATUS")
		for _, n := range a.Nodes {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
				n.Node, n.Shards, humanBytes(n.StoreBytes),
				signedPercent(n.ShardsDeviationPercent),
				signedPercent(n.StoreDeviationPercent),
				n.Status,
			)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if a.TopShards != nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "TOP HOT SHARDS  (%d largest)\n", len(a.TopShards))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tNODE\tSTORE\tSTATUS")
		for _, s := range a.TopShards {
			role := "r"
			if s.Primary {
				role = "p"
			}
			node := s.Node
			if node == "" {
				node = "-"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
				s.Index, s.Shard, role, node, humanBytes(s.StoreBytes), s.Status,
			)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if a.IndicesAtRisk != nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "INDICES WITH SHARD-SIZE ISSUES  (%d %s)\n",
			len(a.IndicesAtRisk), pluralize(len(a.IndicesAtRisk), "index", "indices"))
		if len(a.IndicesAtRisk) == 0 {
			_, _ = fmt.Fprintln(w, "(none)")
		} else {
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "INDEX\tPRIMARIES\tAVG_PRIMARY\tMAX_PRIMARY\tSTATUS")
			for _, ix := range a.IndicesAtRisk {
				status := ix.Status
				if ix.Note != "" {
					status = fmt.Sprintf("%s (%s)", ix.Status, ix.Note)
				}
				_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n",
					ix.Index, ix.Primaries,
					humanBytes(ix.AvgPrimaryBytes), humanBytes(ix.MaxPrimaryBytes),
					status,
				)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}

	// The unassigned section is rendered when the request asked for it.
	// We can tell the difference between "asked for it but the cluster
	// has none" (Total == 0, Indices nil) and "didn't ask" (whole
	// section skipped — Total stays in Summary): asked-for-it always
	// emits the heading. Sections != nil is the gate.
	if sectionRendered(a, "unassigned") {
		_, _ = fmt.Fprintln(w)
		switch a.Unassigned.Total {
		case 0:
			_, _ = fmt.Fprintln(w, "UNASSIGNED  none")
		default:
			_, _ = fmt.Fprintf(w, "UNASSIGNED  %d %s across %d %s (%s)\n",
				a.Unassigned.Total, pluralize(a.Unassigned.Total, "shard", "shards"),
				len(a.Unassigned.Indices), pluralize(len(a.Unassigned.Indices), "index", "indices"),
				strings.Join(a.Unassigned.Indices, ", "),
			)
			if len(a.Unassigned.Indices) > 0 {
				_, _ = fmt.Fprintf(w, "  next: esops ops allocation-explain --index %s\n", a.Unassigned.Indices[0])
			}
		}
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "SUMMARY  %d oversized · %d critical · %d undersized · %d node%s skewed · %d unassigned\n",
		a.Summary.OversizedShards, a.Summary.CriticalShards,
		a.Summary.UndersizedIndices,
		a.Summary.SkewedNodes, plural(a.Summary.SkewedNodes),
		a.Summary.UnassignedShards,
	)
	return nil
}

// sectionRendered tells whether the analysis carries a given section
// (i.e. the operator didn't gate it off via --by). Each section's
// presence has a distinct signal:
//   - nodes / top / indices: nil slice = section was excluded
//   - unassigned: there's no obvious nil-marker on the struct, so we
//     rely on the convention that AnalyzeShards leaves Indices==nil
//     and Total==0 when the section is gated, and populates one or
//     both otherwise.
func sectionRendered(a types.ShardAnalysis, name string) bool {
	switch name {
	case "nodes":
		return a.Nodes != nil
	case "top":
		return a.TopShards != nil
	case "indices":
		return a.IndicesAtRisk != nil
	case "unassigned":
		return a.Unassigned.Total > 0 || a.Unassigned.Indices != nil
	}
	return false
}

// signedPercent renders a deviation percent with an explicit sign,
// matching the "+12%" / "-6%" form in the spec output. Zero renders
// as "+0%" — the explicit sign keeps the column visually aligned.
func signedPercent(p int) string {
	if p >= 0 {
		return fmt.Sprintf("+%d%%", p)
	}
	return fmt.Sprintf("%d%%", p)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// ILMPolicies renders the cluster-side policy list for
// `esops index ilm list`. Table format is one row per policy, showing
// name, version, modified date, and the count of indices it currently
// manages. Wide adds the data-streams and composable-templates
// reference counts so an audit can spot heavily-shared policies. JSON/
// YAML emit the neutral []ILMPolicy shape directly. NDJSON streams one
// record per line.
func ILMPolicies(w io.Writer, rows []types.ILMPolicy, f Format) error {
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
		return ilmPoliciesTable(w, rows, true)
	case FormatTable, "":
		return ilmPoliciesTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ilmPoliciesTable(w io.Writer, rows []types.ILMPolicy, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tVERSION\tMODIFIED\tINDICES\tDATA_STREAMS\tTEMPLATES")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tVERSION\tMODIFIED\tINDICES")
	}
	for _, r := range rows {
		version := "-"
		if r.Version > 0 {
			version = fmt.Sprintf("%d", r.Version)
		}
		modified := dashIfEmpty(r.ModifiedDate)
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\n",
				r.Name, version, modified,
				len(r.InUseBy.Indices), len(r.InUseBy.DataStreams), len(r.InUseBy.ComposableTemplates))
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n",
				r.Name, version, modified, len(r.InUseBy.Indices))
		}
	}
	return tw.Flush()
}

// ILMPolicy renders a single ILM policy — `esops index ilm get`. Table
// format is a header line followed by an in-use-by section and a
// pretty-printed policy body block. JSON/YAML emit the neutral
// ILMPolicy shape directly.
func ILMPolicy(w io.Writer, p types.ILMPolicy, f Format) error {
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
		return ilmPolicyDetail(w, p, true)
	case FormatTable, "":
		return ilmPolicyDetail(w, p, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ilmPolicyDetail(w io.Writer, p types.ILMPolicy, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "NAME\tVERSION\tMODIFIED\tINDICES\tDATA_STREAMS\tTEMPLATES")
	version := "-"
	if p.Version > 0 {
		version = fmt.Sprintf("%d", p.Version)
	}
	_, _ = fmt.Fprintf(hw, "%s\t%s\t%s\t%d\t%d\t%d\n",
		p.Name, version, dashIfEmpty(p.ModifiedDate),
		len(p.InUseBy.Indices), len(p.InUseBy.DataStreams), len(p.InUseBy.ComposableTemplates))
	if err := hw.Flush(); err != nil {
		return err
	}
	if !wide {
		return nil
	}
	if len(p.InUseBy.Indices) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "INDICES")
		for _, name := range p.InUseBy.Indices {
			_, _ = fmt.Fprintf(w, "  %s\n", name)
		}
	}
	if len(p.Definition) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "POLICY")
		buf, err := json.MarshalIndent(p.Definition, "  ", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
	}
	return nil
}

// ILMPolicyPut renders the outcome of `esops index ilm put`. One-line
// header (action, dry-run, name, ack). JSON/YAML emit the neutral
// ILMPolicyPutResult shape directly.
func ILMPolicyPut(w io.Writer, r types.ILMPolicyPutResult, f Format) error {
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
	case FormatTable, FormatWide, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tNAME\tACKNOWLEDGED")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.Name), r.Acknowledged)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// ILMPolicyDelete mirrors ILMPolicyPut for the delete path.
func ILMPolicyDelete(w io.Writer, r types.ILMPolicyDeleteResult, f Format) error {
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
	case FormatTable, FormatWide, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tNAME\tACKNOWLEDGED")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.Name), r.Acknowledged)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// ILMExplain renders the cluster's per-index lifecycle status for
// `esops index ilm explain`. Table format shows the columns operators
// reach for first when triaging a stuck rollover (index, managed flag,
// policy, phase, action, step, age). Wide adds the failed-step name
// and a compact step_info JSON blob. JSON/YAML emit the neutral
// []ILMExplainEntry shape directly. NDJSON streams one record per line.
// ILMExplainNDJSONStream writes one ILMExplainEntry per line to w as
// records arrive from the supplied callback-shaped source. Honours
// CLAUDE.md §6 for `ilm explain` against a multi-thousand-index fleet.
func ILMExplainNDJSONStream(w io.Writer, source func(yield func(types.ILMExplainEntry) error) error) error {
	enc := json.NewEncoder(w)
	return source(func(e types.ILMExplainEntry) error {
		return enc.Encode(e)
	})
}

func ILMExplain(w io.Writer, rows []types.ILMExplainEntry, f Format) error {
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
		return ilmExplainTable(w, rows, true)
	case FormatTable, "":
		return ilmExplainTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ilmExplainTable(w io.Writer, rows []types.ILMExplainEntry, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "INDEX\tMANAGED\tPOLICY\tPHASE\tACTION\tSTEP\tAGE\tFAILED_STEP\tSTEP_INFO")
	} else {
		_, _ = fmt.Fprintln(tw, "INDEX\tMANAGED\tPOLICY\tPHASE\tACTION\tSTEP\tAGE")
	}
	for _, r := range rows {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Index, r.Managed, dashIfEmpty(r.Policy), dashIfEmpty(r.Phase),
				dashIfEmpty(r.Action), dashIfEmpty(r.Step), dashIfEmpty(r.Age),
				dashIfEmpty(r.FailedStep), dashIfEmpty(stepInfoString(r.StepInfo)))
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%s\t%s\t%s\n",
				r.Index, r.Managed, dashIfEmpty(r.Policy), dashIfEmpty(r.Phase),
				dashIfEmpty(r.Action), dashIfEmpty(r.Step), dashIfEmpty(r.Age))
		}
	}
	return tw.Flush()
}

// stepInfoString collapses the cluster's step_info object to a compact
// one-line preview for table cells. Long error blobs are truncated; the
// JSON output carries the full object for scripts that need it.
func stepInfoString(info map[string]any) string {
	if len(info) == 0 {
		return ""
	}
	buf, err := json.Marshal(info)
	if err != nil {
		return fmt.Sprintf("(unrenderable: %v)", err)
	}
	return truncate(string(buf), 60)
}

// ISMPolicies renders the cluster-side policy list for
// `esops index ism list`. Mirrors ILMPolicies in shape; the per-policy
// columns differ because ISM exposes seq_no/primary_term (the optimistic-
// concurrency tokens) where ILM exposes version/modified-date.
func ISMPolicies(w io.Writer, rows []types.ISMPolicy, f Format) error {
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
		return ismPoliciesTable(w, rows, true)
	case FormatTable, "":
		return ismPoliciesTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ismPoliciesTable(w io.Writer, rows []types.ISMPolicy, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ID\tSEQ_NO\tPRIMARY_TERM\tDESCRIPTION")
	} else {
		_, _ = fmt.Fprintln(tw, "ID\tSEQ_NO\tPRIMARY_TERM")
	}
	for _, r := range rows {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n",
				r.ID, r.SeqNo, r.PrimaryTerm,
				dashIfEmpty(truncate(ismPolicyDescription(r.Definition), 60)))
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\n",
				r.ID, r.SeqNo, r.PrimaryTerm)
		}
	}
	return tw.Flush()
}

// ismPolicyDescription extracts the description field from the plugin's
// policy body. Renderers use it to give the operator a one-line preview
// of what each policy is for; missing or non-string values render as ""
// so dashIfEmpty can take over.
//
// The plugin returns the policy fields directly under the entry's
// "policy" key — by the time we reach this renderer the adapter has
// already pulled that out into Definition, so description is one level
// down. We also check the wrapped form ({"policy":{"description":...}})
// for callers that pass a body that hasn't been unwrapped yet.
func ismPolicyDescription(def map[string]any) string {
	if len(def) == 0 {
		return ""
	}
	if d, ok := def["description"].(string); ok && d != "" {
		return d
	}
	if policy, ok := def["policy"].(map[string]any); ok {
		if d, ok := policy["description"].(string); ok {
			return d
		}
	}
	return ""
}

// ISMPolicy renders a single ISM policy — `esops index ism get`.
// JSON/YAML emit the neutral ISMPolicy shape directly. Table format is
// a header followed by the policy body block under -o wide.
func ISMPolicy(w io.Writer, p types.ISMPolicy, f Format) error {
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
		return ismPolicyDetail(w, p, true)
	case FormatTable, "":
		return ismPolicyDetail(w, p, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ismPolicyDetail(w io.Writer, p types.ISMPolicy, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ID\tSEQ_NO\tPRIMARY_TERM\tDESCRIPTION")
	_, _ = fmt.Fprintf(hw, "%s\t%d\t%d\t%s\n",
		p.ID, p.SeqNo, p.PrimaryTerm,
		dashIfEmpty(ismPolicyDescription(p.Definition)))
	if err := hw.Flush(); err != nil {
		return err
	}
	if !wide {
		return nil
	}
	if len(p.Definition) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "POLICY")
		buf, err := json.MarshalIndent(p.Definition, "  ", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
	}
	return nil
}

// ISMPolicyPut renders the outcome of `esops index ism put`. One-line
// header (action, dry-run, id, seq_no, primary_term). JSON/YAML emit
// the neutral ISMPolicyPutResult shape directly.
func ISMPolicyPut(w io.Writer, r types.ISMPolicyPutResult, f Format) error {
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
	case FormatTable, FormatWide, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tID\tSEQ_NO\tPRIMARY_TERM")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%d\t%d\n",
			r.Action, r.DryRun, dashIfEmpty(r.ID), r.SeqNo, r.PrimaryTerm)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// ISMPolicyDelete mirrors ISMPolicyPut for the delete path.
func ISMPolicyDelete(w io.Writer, r types.ISMPolicyDeleteResult, f Format) error {
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
	case FormatTable, FormatWide, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tID\tOK")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.ID), r.OK)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// ISMExplain renders the per-index ISM status for
// `esops index ism explain`. Table format mirrors the ILM explain
// renderer — same operator-facing question ("what is this index doing
// right now?") even though the underlying state machine differs. Wide
// adds a compact step_info JSON blob.
// ISMExplainNDJSONStream writes one ISMExplainEntry per line to w as
// records arrive from the supplied callback-shaped source.
func ISMExplainNDJSONStream(w io.Writer, source func(yield func(types.ISMExplainEntry) error) error) error {
	enc := json.NewEncoder(w)
	return source(func(e types.ISMExplainEntry) error {
		return enc.Encode(e)
	})
}

func ISMExplain(w io.Writer, rows []types.ISMExplainEntry, f Format) error {
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
		return ismExplainTable(w, rows, true)
	case FormatTable, "":
		return ismExplainTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ismExplainTable(w io.Writer, rows []types.ISMExplainEntry, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "INDEX\tMANAGED\tPOLICY_ID\tSTATE\tACTION\tSTEP\tENABLED\tSTEP_INFO")
	} else {
		_, _ = fmt.Fprintln(tw, "INDEX\tMANAGED\tPOLICY_ID\tSTATE\tACTION\tSTEP")
	}
	for _, r := range rows {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%s\t%s\t%t\t%s\n",
				r.Index, r.Managed, dashIfEmpty(r.PolicyID),
				dashIfEmpty(r.State), dashIfEmpty(r.Action), dashIfEmpty(r.Step),
				r.Enabled, dashIfEmpty(stepInfoString(r.StepInfo)))
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%s\t%s\n",
				r.Index, r.Managed, dashIfEmpty(r.PolicyID),
				dashIfEmpty(r.State), dashIfEmpty(r.Action), dashIfEmpty(r.Step))
		}
	}
	return tw.Flush()
}

// SnapshotRepositories renders a slice of SnapshotRepository entries.
// Default table shows name + type; wide adds a flattened settings preview
// so an operator scanning for misconfigured S3 bucket paths doesn't have
// to hop into JSON. JSON/YAML emit the neutral SnapshotRepository shape.
func SnapshotRepositories(w io.Writer, rows []types.SnapshotRepository, f Format) error {
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
		return snapshotRepositoriesTable(w, rows, true)
	case FormatTable, "":
		return snapshotRepositoriesTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotRepositoriesTable(w io.Writer, rows []types.SnapshotRepository, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tTYPE\tSETTINGS")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tTYPE")
	}
	for _, r := range rows {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, r.Type, dashIfEmpty(settingsPreview(r.Settings)))
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\n", r.Name, r.Type)
		}
	}
	return tw.Flush()
}

// settingsPreview renders the repository settings map as a flat
// key=value list, sorted by key for stable output. Nested values render
// via fmt.Sprintf("%v") rather than a JSON marshal — keeps the wide
// table column scannable; operators who want the full structure use
// `-o json` or `-o yaml`.
func settingsPreview(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, ",")
}

// Snapshots renders a slice of Snapshot rows. Default table shows the
// columns operators reach for during incident triage (repo, name, state,
// duration, shard counts); wide adds UUID, indices count and start time.
// JSON/YAML emit the neutral Snapshot shape; NDJSON streams one record
// per line.
func Snapshots(w io.Writer, rows []types.Snapshot, f Format) error {
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
		return snapshotsTable(w, rows, true)
	case FormatTable, "":
		return snapshotsTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotsTable(w io.Writer, rows []types.Snapshot, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "REPOSITORY\tNAME\tSTATE\tINDICES\tSHARDS\tFAILED\tDURATION\tSTART\tUUID")
	} else {
		_, _ = fmt.Fprintln(tw, "REPOSITORY\tNAME\tSTATE\tINDICES\tSHARDS\tDURATION")
	}
	for _, r := range rows {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d/%d\t%d\t%s\t%s\t%s\n",
				r.Repository, r.Name, r.State,
				len(r.Indices),
				r.ShardsSuccessful, r.ShardsTotal, r.ShardsFailed,
				humanDuration(r.DurationMillis),
				formatCreation(r.StartTimeMillis),
				dashIfEmpty(r.UUID),
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d/%d\t%s\n",
				r.Repository, r.Name, r.State,
				len(r.Indices),
				r.ShardsSuccessful, r.ShardsTotal,
				humanDuration(r.DurationMillis),
			)
		}
	}
	return tw.Flush()
}

// humanDuration renders a millisecond duration as a short, scannable
// string ("2m30s", "5h10m", "120ms"). Zero or negative renders as "-"
// — in-progress snapshots leave duration unset until completion and
// "0ms" would be misleading.
func humanDuration(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Duration(ms) * time.Millisecond
	// time.Duration.String() emits "2m30.000s" — truncate sub-second
	// noise for the common case where snapshots run for minutes.
	if d >= time.Minute {
		d = d.Round(time.Second)
	}
	return d.String()
}
