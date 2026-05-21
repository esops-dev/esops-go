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

	"github.com/esops-dev/esops-go/pkg/types"
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
// from the supplied callback-shaped source. Streams without buffering
// the full result so a 20k-index cluster's first row reaches stdout
// before the cluster has finished sending the last one. Use with
// index.ListStream.
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
// arrive from the supplied callback-shaped source. The streaming
// counterpart to Aliases, used by `aliases list -o ndjson`.
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
// records arrive from the supplied callback-shaped source. The
// streaming counterpart to IndexSettingsList, used by `settings get
// -o ndjson`.
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

	// Section 2b: anonymous-access posture, when the adapter probed
	// the cluster's settings. nil here means "not collected" (the
	// adapter skipped the probe — security disabled, endpoint missing,
	// or a permission note already in Status.Notes), which is distinct
	// from "collected and disabled" and worth not faking in either
	// direction.
	if a.Anonymous != nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "ANONYMOUS")
		_, _ = fmt.Fprintf(w, "  enabled:         %t\n", a.Anonymous.Enabled)
		if a.Anonymous.Username != "" {
			_, _ = fmt.Fprintf(w, "  username:        %s\n", a.Anonymous.Username)
		}
		if len(a.Anonymous.Roles) > 0 {
			_, _ = fmt.Fprintf(w, "  roles:           %s\n", joinOrDash(a.Anonymous.Roles))
		}
		// AuthzException is ES-only and pointer-typed so OS leaves it
		// nil. Only surface when set (the dialect has the concept) and
		// when the value diverges from the default of true — the
		// always-true line is noise that doesn't help an audit reader.
		if a.Anonymous.AuthzException != nil && !*a.Anonymous.AuthzException {
			_, _ = fmt.Fprintln(w, "  authz_exception: false")
		}
		if a.Anonymous.Source != "" {
			_, _ = fmt.Fprintf(w, "  source:          %s\n", a.Anonymous.Source)
		}
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

	// Section 8: realms. The configured authentication chain — what
	// an audit reading "how does this cluster identify a request?"
	// has to know. Empty when the realms read was forbidden (admin-
	// cert gating on OS) or when nothing is configured; the absence
	// is loud via Status.Notes in either case.
	if len(a.Realms) > 0 {
		if err := renderRealms(w, a.Realms, wide); err != nil {
			return err
		}
	}
	return nil
}

// renderRealms prints the configured-realm table. Default columns are
// the load-bearing tuple (order, type, name, enabled) plus a
// deprecation marker; -o wide adds nothing extra here because the type
// already carries everything operator-relevant — the realm-specific
// config (LDAP URLs, SAML metadata) is intentionally not surfaced by
// the capability.
func renderRealms(w io.Writer, realms []types.Realm, wide bool) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "REALMS")
	rw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(rw, "ORDER\tTYPE\tNAME\tENABLED\tDEPRECATED")
	for _, r := range realms {
		_, _ = fmt.Fprintf(rw, "%d\t%s\t%s\t%t\t%t\n",
			r.Order, dashIfEmpty(r.Type), dashIfEmpty(r.Name), r.Enabled, r.Deprecated)
	}
	_ = wide // wide adds no extra column for realms today
	return rw.Flush()
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
// records arrive from the supplied callback-shaped source. Streaming
// matters here: `ilm explain` against a multi-thousand-index fleet
// can run into tens of MB if buffered.
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

// SnapshotsStreamWriter is an incremental NDJSON writer for the snapshot
// list surface. The CLI hands one of these to internal/snapshot.Stream
// so each snapshot encodes to stdout the moment its repository's
// response decodes — large fleets stop blocking on the slowest repo.
//
// Close is a no-op (the underlying io.Writer is owned by the caller);
// the type exists so the streaming surface can grow per-format
// behaviour later (e.g. table-streaming with a header row) without
// rewriting every caller.
type SnapshotsStreamWriter struct {
	enc *json.Encoder
}

// NewSnapshotsStream constructs a stream writer for the requested
// format. NDJSON is the only format with meaningful streaming today;
// the constructor returns an error for any other format so the caller
// (cli/snapshot/list.go) gets a clear "this format requires the buffered
// path" rather than silently turning into a single-line dump.
func NewSnapshotsStream(w io.Writer, f Format) (*SnapshotsStreamWriter, error) {
	if f != FormatNDJSON {
		return nil, fmt.Errorf("streaming is only supported for --output ndjson (got %q)", f)
	}
	return &SnapshotsStreamWriter{enc: json.NewEncoder(w)}, nil
}

// Write emits one snapshot as a single line of JSON.
func (s *SnapshotsStreamWriter) Write(snap types.Snapshot) error {
	return s.enc.Encode(snap)
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

// SnapshotCreate renders the outcome of `esops snapshot create`. Default
// table is a one-line header (action, dry-run, repository, snapshot,
// accepted, wait-for-completion, state). Wide adds shard counts and
// duration when the cluster returned a populated snapshot block. JSON/
// YAML emit the neutral SnapshotCreateResult shape directly.
func SnapshotCreate(w io.Writer, r types.SnapshotCreateResult, f Format) error {
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
		return snapshotCreateTable(w, r, true)
	case FormatTable, "":
		return snapshotCreateTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotCreateTable(w io.Writer, r types.SnapshotCreateResult, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tSNAPSHOT\tACCEPTED\tWAIT\tSTATE\tSHARDS\tDURATION")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%t\t%t\t%s\t%s\t%s\n",
			r.Action, r.DryRun,
			dashIfEmpty(r.Repository), dashIfEmpty(r.Name),
			r.Accepted, r.WaitForCompletion,
			dashIfEmpty(r.Snapshot.State),
			snapshotShardsCell(r.Snapshot),
			humanDuration(r.Snapshot.DurationMillis),
		)
	} else {
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tSNAPSHOT\tACCEPTED\tWAIT\tSTATE")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%t\t%t\t%s\n",
			r.Action, r.DryRun,
			dashIfEmpty(r.Repository), dashIfEmpty(r.Name),
			r.Accepted, r.WaitForCompletion,
			dashIfEmpty(r.Snapshot.State),
		)
	}
	return tw.Flush()
}

// snapshotShardsCell formats the cluster's per-snapshot shard counters
// as "successful/total" with a trailing failure marker when non-zero.
// Returns "-" when the cluster did not echo a snapshot block (the async
// path with wait_for_completion=false).
func snapshotShardsCell(s types.Snapshot) string {
	if s.ShardsTotal == 0 && s.ShardsSuccessful == 0 && s.ShardsFailed == 0 {
		return "-"
	}
	out := fmt.Sprintf("%d/%d", s.ShardsSuccessful, s.ShardsTotal)
	if s.ShardsFailed > 0 {
		out += fmt.Sprintf(" (%d failed)", s.ShardsFailed)
	}
	return out
}

// SnapshotVerify renders the outcome of `esops snapshot verify`. Default
// table shows the repository and the count of nodes that connected;
// wide expands the per-node identifiers and names so an operator can see
// which node failed when the count is below the cluster size. JSON/YAML
// emit the neutral SnapshotVerifyResult shape directly.
func SnapshotVerify(w io.Writer, r types.SnapshotVerifyResult, f Format) error {
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
		return snapshotVerifyTable(w, r, true)
	case FormatTable, "":
		return snapshotVerifyTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotVerifyTable(w io.Writer, r types.SnapshotVerifyResult, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ACTION\tREPOSITORY\tNODES\tNODE_NAMES\tNODE_IDS")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n",
			r.Action, dashIfEmpty(r.Repository), len(r.VerifiedNodes),
			dashIfEmpty(strings.Join(r.Names, ",")),
			dashIfEmpty(strings.Join(r.VerifiedNodes, ",")),
		)
	} else {
		_, _ = fmt.Fprintln(tw, "ACTION\tREPOSITORY\tNODES")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\n",
			r.Action, dashIfEmpty(r.Repository), len(r.VerifiedNodes),
		)
	}
	return tw.Flush()
}

// SnapshotRestore renders the outcome of `esops snapshot restore`. Mirrors
// SnapshotCreate so JSON/YAML consumers can treat the two commands
// symmetrically. Wide adds the post-rename index list, shard counts and
// the rename pair so a captured record describes the side-by-side restore
// without needing to back-derive the rename intent.
func SnapshotRestore(w io.Writer, r types.SnapshotRestoreResult, f Format) error {
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
		return snapshotRestoreTable(w, r, true)
	case FormatTable, "":
		return snapshotRestoreTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotRestoreTable(w io.Writer, r types.SnapshotRestoreResult, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tSNAPSHOT\tACCEPTED\tWAIT\tINDICES\tSHARDS\tRENAME")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%t\t%t\t%d\t%s\t%s\n",
			r.Action, r.DryRun,
			dashIfEmpty(r.Repository), dashIfEmpty(r.Name),
			r.Accepted, r.WaitForCompletion,
			len(r.Snapshot.Indices),
			snapshotRestoreShardsCell(r.Snapshot),
			dashIfEmpty(snapshotRenameCell(r.RenamePattern, r.RenameReplacement)),
		)
	} else {
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tSNAPSHOT\tACCEPTED\tWAIT\tINDICES")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%t\t%t\t%d\n",
			r.Action, r.DryRun,
			dashIfEmpty(r.Repository), dashIfEmpty(r.Name),
			r.Accepted, r.WaitForCompletion,
			len(r.Snapshot.Indices),
		)
	}
	return tw.Flush()
}

func snapshotRestoreShardsCell(s types.SnapshotRestoreInfo) string {
	if s.ShardsTotal == 0 && s.ShardsSuccessful == 0 && s.ShardsFailed == 0 {
		return "-"
	}
	out := fmt.Sprintf("%d/%d", s.ShardsSuccessful, s.ShardsTotal)
	if s.ShardsFailed > 0 {
		out += fmt.Sprintf(" (%d failed)", s.ShardsFailed)
	}
	return out
}

func snapshotRenameCell(pattern, replacement string) string {
	if pattern == "" && replacement == "" {
		return ""
	}
	return fmt.Sprintf("%s→%s", pattern, replacement)
}

// SnapshotPrune renders the outcome of `esops snapshot prune`. Default table
// is a one-line header (action, dry-run, repo, considered/kept/deleted
// counts, acknowledged). Wide adds the comma-joined deleted list so the
// operator sees exactly which snapshots were targeted without piping into
// JSON; when the list is large the table cell will be long, but that's the
// signal the operator wants and `-o ndjson` is one flag away.
func SnapshotPrune(w io.Writer, r types.SnapshotPruneResult, f Format) error {
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
		return snapshotPruneTable(w, r, true)
	case FormatTable, "":
		return snapshotPruneTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// SnapshotRegister renders the outcome of `esops snapshot register`. Default
// table is a one-line header; JSON/YAML emit the neutral
// SnapshotRepositoryRegisterResult shape.
func SnapshotRegister(w io.Writer, r types.SnapshotRepositoryRegisterResult, f Format) error {
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
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tTYPE\tACK")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.Repository),
			dashIfEmpty(r.Type), r.Acknowledged,
		)
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// SnapshotDeregister mirrors SnapshotRegister for the deregister surface.
func SnapshotDeregister(w io.Writer, r types.SnapshotRepositoryDeregisterResult, f Format) error {
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
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tACK")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.Repository), r.Acknowledged,
		)
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// SnapshotPolicies renders the cluster's snapshot policy list for
// `esops snapshot policy list`. Default table shows name, repository,
// schedule and the operator-scannable last-success age; wide adds
// modified date and next-execution time. JSON/YAML emit the neutral
// []SnapshotPolicy shape directly. NDJSON streams one record per line.
func SnapshotPolicies(w io.Writer, rows []types.SnapshotPolicy, f Format) error {
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
		return snapshotPoliciesTable(w, rows, true)
	case FormatTable, "":
		return snapshotPoliciesTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotPoliciesTable(w io.Writer, rows []types.SnapshotPolicy, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tREPOSITORY\tSCHEDULE\tENABLED\tVERSION\tMODIFIED\tLAST_SUCCESS\tNEXT_RUN")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tREPOSITORY\tSCHEDULE\tENABLED\tLAST_SUCCESS")
	}
	for _, r := range rows {
		version := "-"
		if r.Version > 0 {
			version = fmt.Sprintf("%d", r.Version)
		}
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\t%s\t%s\t%s\n",
				r.Name,
				dashIfEmpty(r.Repository),
				dashIfEmpty(r.Schedule),
				r.Enabled,
				version,
				formatCreation(r.ModifiedDateMillis),
				dashIfEmpty(snapshotPolicyAge(r.LastSuccessMillis)),
				formatCreation(r.NextExecutionMillis),
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\n",
				r.Name,
				dashIfEmpty(r.Repository),
				dashIfEmpty(r.Schedule),
				r.Enabled,
				dashIfEmpty(snapshotPolicyAge(r.LastSuccessMillis)),
			)
		}
	}
	return tw.Flush()
}

// SnapshotPolicy renders a single snapshot policy — `esops snapshot
// policy get`. Table format is a header line; wide appends the full
// policy body as indented JSON so an operator scanning a policy
// definition doesn't have to reach for `--output json`. JSON/YAML
// emit the neutral SnapshotPolicy shape directly.
func SnapshotPolicy(w io.Writer, p types.SnapshotPolicy, f Format) error {
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
		return snapshotPolicyDetail(w, p, true)
	case FormatTable, "":
		return snapshotPolicyDetail(w, p, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotPolicyDetail(w io.Writer, p types.SnapshotPolicy, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "NAME\tREPOSITORY\tSCHEDULE\tENABLED\tVERSION\tMODIFIED\tLAST_SUCCESS\tNEXT_RUN")
	version := "-"
	if p.Version > 0 {
		version = fmt.Sprintf("%d", p.Version)
	}
	_, _ = fmt.Fprintf(hw, "%s\t%s\t%s\t%t\t%s\t%s\t%s\t%s\n",
		p.Name,
		dashIfEmpty(p.Repository),
		dashIfEmpty(p.Schedule),
		p.Enabled,
		version,
		formatCreation(p.ModifiedDateMillis),
		dashIfEmpty(snapshotPolicyAge(p.LastSuccessMillis)),
		formatCreation(p.NextExecutionMillis),
	)
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

// SnapshotPolicyPut renders the outcome of `esops snapshot policy put`.
// One-line header; JSON/YAML emit the neutral
// SnapshotPolicyPutResult shape directly.
func SnapshotPolicyPut(w io.Writer, r types.SnapshotPolicyPutResult, f Format) error {
	return snapshotPolicyAckResult(w, r.Action, r.DryRun, r.Name, r.Acknowledged, f)
}

// SnapshotPolicyDelete mirrors SnapshotPolicyPut for the delete path.
func SnapshotPolicyDelete(w io.Writer, r types.SnapshotPolicyDeleteResult, f Format) error {
	return snapshotPolicyAckResult(w, r.Action, r.DryRun, r.Name, r.Acknowledged, f)
}

// SnapshotPolicyExecute renders `snapshot policy execute`. The table
// adds a SNAPSHOT column for the synthesised snapshot name ES SLM
// returns; OS SM leaves it empty (the snapshot lands on the next cron
// tick). JSON/YAML emit the neutral SnapshotPolicyExecuteResult.
func SnapshotPolicyExecute(w io.Writer, r types.SnapshotPolicyExecuteResult, f Format) error {
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
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tNAME\tSNAPSHOT\tACKNOWLEDGED")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%s\t%t\n",
			r.Action, r.DryRun, dashIfEmpty(r.Name),
			dashIfEmpty(r.SnapshotName), r.Acknowledged,
		)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// snapshotPolicyAckResult is the shared one-row renderer for the put/
// delete result shapes. Both share the same five fields and table
// columns; consolidating saves two near-identical renderers and the
// drift their per-fix duplication invites.
func snapshotPolicyAckResult(w io.Writer, action string, dryRun bool, name string, ack bool, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"action": action, "dry_run": dryRun, "name": name, "acknowledged": ack,
		})
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{
			"action": action, "dry_run": dryRun, "name": name, "acknowledged": ack,
		})
	case FormatWide, FormatTable, "":
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tDRY_RUN\tNAME\tACKNOWLEDGED")
		_, _ = fmt.Fprintf(hw, "%s\t%t\t%s\t%t\n",
			action, dryRun, dashIfEmpty(name), ack)
		return hw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// snapshotPolicyAge renders an epoch-millis "last success" timestamp
// as a compact age — same idea as ageSince in the ES adapter, kept
// local so the renderer doesn't reach across the public API line.
// Zero/negative ms returns "" so the caller's dashIfEmpty helper
// renders "-".
func snapshotPolicyAge(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd%dh", days, hours)
	case d >= time.Hour:
		hours := int(d / time.Hour)
		minutes := int((d % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// SnapshotAnalyze renders the cluster's repository _analyze report. Default
// table shows the operator-facing counts (blob count, concurrency, read
// nodes); wide adds the coordinating node. JSON/YAML emit the neutral
// shape.
func SnapshotAnalyze(w io.Writer, r types.SnapshotAnalyzeResult, f Format) error {
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
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ACTION\tREPOSITORY\tBLOBS\tCONCURRENCY\tREAD_NODES\tEARLY\tSTATUS\tCOORDINATOR")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n",
			r.Action, dashIfEmpty(r.Repository),
			r.BlobCount, r.ConcurrencyLevels, r.ReadNodeCount, r.EarlyReadNodes,
			dashIfEmpty(r.Status), dashIfEmpty(r.CoordinatingNode),
		)
		return tw.Flush()
	case FormatTable, "":
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ACTION\tREPOSITORY\tBLOBS\tCONCURRENCY\tREAD_NODES\tSTATUS")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n",
			r.Action, dashIfEmpty(r.Repository),
			r.BlobCount, r.ConcurrencyLevels, r.ReadNodeCount,
			dashIfEmpty(r.Status),
		)
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func snapshotPruneTable(w io.Writer, r types.SnapshotPruneResult, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tCONSIDERED\tKEPT\tDELETED\tACK\tDELETED_LIST")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%d\t%d\t%d\t%t\t%s\n",
			r.Action, r.DryRun,
			dashIfEmpty(r.Repository),
			r.Considered, len(r.Kept), len(r.Deleted),
			r.Acknowledged,
			dashIfEmpty(strings.Join(r.Deleted, ",")),
		)
	} else {
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tREPOSITORY\tCONSIDERED\tKEPT\tDELETED\tACK")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%d\t%d\t%d\t%t\n",
			r.Action, r.DryRun,
			dashIfEmpty(r.Repository),
			r.Considered, len(r.Kept), len(r.Deleted),
			r.Acknowledged,
		)
	}
	return tw.Flush()
}

// Nodes renders a slice of Node rows. Default table is the operator-
// facing column set for `ops nodes`: name, role, version, heap, and
// disk usage. Wide adds the master flag, IP, CPU, load, and uptime.
// JSON/YAML emit the full Node struct.
func Nodes(w io.Writer, nodes []types.Node, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(nodes)
	case FormatNDJSON:
		enc := json.NewEncoder(w)
		for _, n := range nodes {
			if err := enc.Encode(n); err != nil {
				return err
			}
		}
		return nil
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(nodes)
	case FormatWide:
		return nodesTable(w, nodes, true)
	case FormatTable, "":
		return nodesTable(w, nodes, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func nodesTable(w io.Writer, nodes []types.Node, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tROLES\tVERSION\tIP\tMASTER\tHEAP%\tHEAP_USED\tHEAP_MAX\tDISK_USED\tDISK_AVAIL\tDISK%\tCPU\tLOAD_1M\tUPTIME")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tROLES\tVERSION\tHEAP%\tDISK_USED\tDISK_AVAIL\tDISK%")
	}
	for _, n := range nodes {
		roles := joinOrDash(n.Roles)
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				n.Name, roles,
				dashIfEmpty(n.Version),
				dashIfEmpty(n.IP),
				masterMark(n.Master),
				percentOrDash(n.HeapPercent),
				humanBytesOrDash(n.HeapUsedBytes),
				humanBytesOrDash(n.HeapMaxBytes),
				humanBytesOrDash(n.DiskUsedBytes),
				humanBytesOrDash(n.DiskAvailBytes),
				percentOrDash(n.DiskUsedPercent),
				percentOrDash(n.CPUPercent),
				dashIfEmpty(n.Load1m),
				dashIfEmpty(n.Uptime),
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				n.Name, roles,
				dashIfEmpty(n.Version),
				percentOrDash(n.HeapPercent),
				humanBytesOrDash(n.DiskUsedBytes),
				humanBytesOrDash(n.DiskAvailBytes),
				percentOrDash(n.DiskUsedPercent),
			)
		}
	}
	return tw.Flush()
}

// masterMark renders the elected-master flag as the same '*' the
// cluster prints in /_cat/nodes — kubectl-ish single character keeps
// the column narrow without losing meaning.
func masterMark(b bool) string {
	if b {
		return "*"
	}
	return "-"
}

// percentOrDash formats an integer 0–100 cell. Zero is ambiguous —
// could be "0% reported" or "no value reported" — so we render zero as
// "-" because /_cat/nodes leaves the column blank when the cluster has
// nothing to say (coordinating-only nodes have no heap/disk).
func percentOrDash(p int) string {
	if p <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", p)
}

// humanBytesOrDash is humanBytes with a "-" placeholder for zero bytes.
// Same rationale as percentOrDash: a coordinating-only node returning
// no disk total is information, not a 0b shard.
func humanBytesOrDash(n int64) string {
	if n <= 0 {
		return "-"
	}
	return humanBytes(n)
}

// NodeDrain renders the outcome of `esops ops drain`. Header carries
// the action/scope/dry-run flags plus the shard-count snapshot; the
// before/after exclude lists follow as two stacked sections. JSON/YAML
// emit the neutral NodeDrainResult shape directly.
func NodeDrain(w io.Writer, r types.NodeDrainResult, f Format) error {
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
		return nodeDrainTable(w, r, true)
	case FormatTable, "":
		return nodeDrainTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func nodeDrainTable(w io.Writer, r types.NodeDrainResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	scope := drainScope(r.Persistent)
	_, _ = fmt.Fprintln(hw, "ACTION\tNODES\tSCOPE\tDRY_RUN\tACK\tALREADY_EXCLUDED\tWAITED\tSHARDS_LEFT")
	_, _ = fmt.Fprintf(hw, "drain\t%s\t%s\t%t\t%t\t%s\t%t\t%d\n",
		joinOrDash(r.NodeNames), scope, r.DryRun, r.Acknowledged,
		dashIfEmpty(strings.Join(r.AlreadyExcluded, ",")),
		r.Waited, r.ShardsRemaining,
	)
	if err := hw.Flush(); err != nil {
		return err
	}

	// Per-node breakdown: only useful when there's more than one
	// node, or when wide is requested. Single-node drains have the
	// total in SHARDS_LEFT already.
	if (wide || len(r.NodeNames) > 1) && len(r.PerNodeShardsRemaining) > 0 {
		_, _ = fmt.Fprintln(w)
		pw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(pw, "NODE\tSHARDS_LEFT")
		// Iterate NodeNames (not the map) so the order is the
		// operator's input order — stable and grep-friendly.
		for _, name := range r.NodeNames {
			_, _ = fmt.Fprintf(pw, "%s\t%d\n", name, r.PerNodeShardsRemaining[name])
		}
		if err := pw.Flush(); err != nil {
			return err
		}
	}

	if r.VotingExclusionAcked || len(r.VotingExclusionsAdded) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "VOTING_CONFIG_EXCLUSIONS")
		_, _ = fmt.Fprintf(w, "  added:        %s\n", joinOrDash(r.VotingExclusionsAdded))
		_, _ = fmt.Fprintf(w, "  acknowledged: %t\n", r.VotingExclusionAcked)
	}

	if wide || len(r.AlreadyExcluded) != len(r.NodeNames) {
		writeExcludeBeforeAfter(w, r.Before, r.After)
	}
	return nil
}

// NodeUncordon renders the outcome of `esops ops uncordon`. Mirrors
// NodeDrain but omits the wait/shards columns since uncordon does not
// poll progress.
func NodeUncordon(w io.Writer, r types.NodeUncordonResult, f Format) error {
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
		return nodeUncordonTable(w, r, true)
	case FormatTable, "":
		return nodeUncordonTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func nodeUncordonTable(w io.Writer, r types.NodeUncordonResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	scope := drainScope(r.Persistent)
	_, _ = fmt.Fprintln(hw, "ACTION\tNODES\tSCOPE\tDRY_RUN\tACK\tNOT_IN_LIST\tVOTING_CLEARED")
	_, _ = fmt.Fprintf(hw, "uncordon\t%s\t%s\t%t\t%t\t%s\t%t\n",
		joinOrDash(r.NodeNames), scope, r.DryRun, r.Acknowledged,
		dashIfEmpty(strings.Join(r.NotInList, ",")),
		r.VotingExclusionsCleared,
	)
	if err := hw.Flush(); err != nil {
		return err
	}

	// The before/after exclude list is interesting whenever the
	// cluster's allocation state actually changed (some node was
	// removed). Skip it on a pure --clear-voting invocation, and on
	// the no-op case where every requested node was already absent.
	allAbsent := len(r.NodeNames) > 0 && len(r.NotInList) == len(r.NodeNames)
	allocationChanged := len(r.NodeNames) > 0 && !allAbsent
	if wide || allocationChanged {
		writeExcludeBeforeAfter(w, r.Before, r.After)
	}
	return nil
}

// NodeDrainList renders the read-only output of `ops drain --list`.
// Three small sections (persistent / transient / voting) — each empty
// list reads as "-" so the section is always present and grep-able.
func NodeDrainList(w io.Writer, r types.NodeDrainListResult, f Format) error {
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
		nodeDrainListTable(w, r)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func nodeDrainListTable(w io.Writer, r types.NodeDrainListResult) {
	_, _ = fmt.Fprintln(w, "ALLOCATION_EXCLUDE")
	_, _ = fmt.Fprintf(w, "  persistent: %s\n", joinOrDash(r.PersistentExcluded))
	_, _ = fmt.Fprintf(w, "  transient:  %s\n", joinOrDash(r.TransientExcluded))
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "VOTING_CONFIG_EXCLUSIONS")
	_, _ = fmt.Fprintf(w, "  nodes: %s\n", joinOrDash(r.VotingExcluded))
}

// drainScope picks the scope label for the table view — "transient" or
// "persistent". Used by both drain and uncordon renderers.
func drainScope(persistent bool) string {
	if persistent {
		return "persistent"
	}
	return "transient"
}

// writeExcludeBeforeAfter prints the BEFORE/AFTER exclude name lists.
// Empty list renders as a single "-" placeholder so the section is
// always present and grep-able in shell history.
func writeExcludeBeforeAfter(w io.Writer, before, after []string) {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "EXCLUDE._NAME")
	_, _ = fmt.Fprintf(w, "  before: %s\n", joinOrDash(before))
	_, _ = fmt.Fprintf(w, "  after:  %s\n", joinOrDash(after))
}

// TasksNDJSONStream writes one Task per line to w as records arrive
// from the supplied callback-shaped source. Honours the same
// streaming contract as IndicesNDJSONStream: a fleet running
// thousands of concurrent tasks emits the first row before the
// cluster has finished sending the last one. Use with
// ops.ListTasksStream.
func TasksNDJSONStream(w io.Writer, source func(yield func(types.Task) error) error) error {
	enc := json.NewEncoder(w)
	return source(func(t types.Task) error {
		return enc.Encode(t)
	})
}

// Tasks renders a slice of in-flight Task rows for `ops tasks list`.
// Default table is the columns operators reach for first when triaging
// "what's hot right now" (id, action, running, cancellable). Wide adds
// the description, parent task ID, and start time. JSON/YAML emit the
// neutral Task shape; NDJSON streams one record per line so a fleet
// with thousands of tasks doesn't have to parse a single blob.
func Tasks(w io.Writer, tasks []types.Task, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(tasks)
	case FormatNDJSON:
		enc := json.NewEncoder(w)
		for _, t := range tasks {
			if err := enc.Encode(t); err != nil {
				return err
			}
		}
		return nil
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(tasks)
	case FormatWide:
		return tasksTable(w, tasks, true)
	case FormatTable, "":
		return tasksTable(w, tasks, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func tasksTable(w io.Writer, tasks []types.Task, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ID\tNODE\tACTION\tRUNNING\tCANCELLABLE\tCANCELLED\tPARENT\tDESCRIPTION")
		for _, t := range tasks {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%t\t%s\t%s\n",
				t.ID, dashIfEmpty(t.Node), t.Action,
				humanRunningTime(t.RunningTimeNanos),
				t.Cancellable, t.Cancelled,
				dashIfEmpty(t.ParentTaskID),
				dashIfEmpty(t.Description),
			)
		}
	} else {
		_, _ = fmt.Fprintln(tw, "ID\tNODE\tACTION\tRUNNING\tCANCELLABLE")
		for _, t := range tasks {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\n",
				t.ID, dashIfEmpty(t.Node), t.Action,
				humanRunningTime(t.RunningTimeNanos),
				t.Cancellable,
			)
		}
	}
	return tw.Flush()
}

// TasksCancel renders the outcome of `ops tasks cancel`. Default table
// is a one-line header (action, dry-run, target, count, wait flag,
// node-failure count) followed by the per-task list of cancelled rows
// (the cluster's own echo of what got the signal). Wide adds the same
// extra columns the list renderer surfaces, plus the node_failures
// section verbatim. JSON/YAML emit the neutral TasksCancelResult shape.
func TasksCancel(w io.Writer, r types.TasksCancelResult, f Format) error {
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
		return tasksCancelTable(w, r, true)
	case FormatTable, "":
		return tasksCancelTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func tasksCancelTable(w io.Writer, r types.TasksCancelResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	target := tasksCancelTarget(r.TaskIDs)
	_, _ = fmt.Fprintln(hw, "ACTION\tTARGET\tDRY_RUN\tWAIT\tCANCELLED\tNODE_FAILURES")
	_, _ = fmt.Fprintf(hw, "cancel\t%s\t%t\t%t\t%d\t%d\n",
		target, r.DryRun, r.WaitForCompletion,
		len(r.Cancelled), len(r.NodeFailures),
	)
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(r.Cancelled) > 0 {
		_, _ = fmt.Fprintln(w)
		if err := tasksTable(w, r.Cancelled, wide); err != nil {
			return err
		}
	}

	if len(r.NodeFailures) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "NODE_FAILURES")
		for _, line := range r.NodeFailures {
			_, _ = fmt.Fprintf(w, "  %s\n", line)
		}
	}
	return nil
}

// tasksCancelTarget renders the TARGET cell for the cancel result
// header. One ID prints verbatim, two or three are joined for
// at-a-glance auditability, and four or more collapse to a count so
// the column doesn't blow out the table; the operator who ran the
// command already knows the IDs they passed, and `-o json` carries the
// full slice. "filter" marks the bulk-by-actions/nodes path.
func tasksCancelTarget(ids []string) string {
	switch len(ids) {
	case 0:
		return "filter"
	case 1, 2, 3:
		return strings.Join(ids, ",")
	default:
		return fmt.Sprintf("%d ids", len(ids))
	}
}

// humanRunningTime formats a nanosecond running-time value as a short
// scannable string. Mirrors humanDuration's contract (zero = "-") so
// the table cell stays aligned with how the snapshot/restore views
// render duration.
func humanRunningTime(ns int64) string {
	if ns <= 0 {
		return "-"
	}
	d := time.Duration(ns)
	if d >= time.Minute {
		d = d.Round(time.Second)
	} else if d >= time.Second {
		d = d.Round(time.Millisecond)
	}
	return d.String()
}

// PendingTasks renders the master's pending cluster-state queue for
// `ops pending-tasks`. Default table is the columns operators reach
// for first when triaging a slow master (order, priority, in-queue,
// executing, source). Wide adds the raw time-in-queue millis so
// scripted thresholds don't have to re-parse a humanised duration.
// JSON/YAML emit the neutral PendingTask shape unchanged.
func PendingTasks(w io.Writer, tasks []types.PendingTask, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(tasks)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(tasks)
	case FormatWide:
		return pendingTasksTable(w, tasks, true)
	case FormatTable, "":
		return pendingTasksTable(w, tasks, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func pendingTasksTable(w io.Writer, tasks []types.PendingTask, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "ORDER\tPRIORITY\tIN_QUEUE\tIN_QUEUE_MS\tEXECUTING\tSOURCE")
		for _, t := range tasks {
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%t\t%s\n",
				t.InsertOrder,
				dashIfEmpty(t.Priority),
				humanDuration(t.TimeInQueueMillis),
				t.TimeInQueueMillis,
				t.Executing,
				dashIfEmpty(t.Source),
			)
		}
	} else {
		_, _ = fmt.Fprintln(tw, "ORDER\tPRIORITY\tIN_QUEUE\tEXECUTING\tSOURCE")
		for _, t := range tasks {
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%t\t%s\n",
				t.InsertOrder,
				dashIfEmpty(t.Priority),
				humanDuration(t.TimeInQueueMillis),
				t.Executing,
				dashIfEmpty(t.Source),
			)
		}
	}
	return tw.Flush()
}

// Deprecations renders the cluster's pre-upgrade lint findings for
// `ops deprecations`. Default table is one row per issue with the
// columns operators reach for when triaging an upgrade (level,
// category, target, message). Wide adds the docs URL, the cluster's
// resolve-during-rolling-upgrade flag, and the per-issue details.
// JSON/YAML emit the neutral DeprecationIssue slice unchanged.
//
// The adapter sorts by category → target → message before returning,
// so a captured listing is stable across runs without re-sorting here.
func Deprecations(w io.Writer, issues []types.DeprecationIssue, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(issues)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(issues)
	case FormatWide:
		return deprecationsTable(w, issues, true)
	case FormatTable, "":
		return deprecationsTable(w, issues, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func deprecationsTable(w io.Writer, issues []types.DeprecationIssue, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "LEVEL\tCATEGORY\tTARGET\tRESOLVE_DURING_RU\tMESSAGE\tURL\tDETAILS")
		for _, i := range issues {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\t%s\t%s\n",
				dashIfEmpty(i.Level),
				dashIfEmpty(i.Category),
				dashIfEmpty(i.Target),
				i.ResolveDuringRollingUpgrade,
				dashIfEmpty(i.Message),
				dashIfEmpty(i.URL),
				dashIfEmpty(i.Details),
			)
		}
	} else {
		_, _ = fmt.Fprintln(tw, "LEVEL\tCATEGORY\tTARGET\tMESSAGE")
		for _, i := range issues {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				dashIfEmpty(i.Level),
				dashIfEmpty(i.Category),
				dashIfEmpty(i.Target),
				dashIfEmpty(i.Message),
			)
		}
	}
	return tw.Flush()
}

// ClusterSettings renders the read-side output of `ops settings get`.
// Table output is one row per (scope, key, value) triple — the scope
// label keeps the renderer interpretable when an operator pipes both
// scopes into one stream. JSON/YAML emit the neutral
// ClusterSettingsScopedView shape directly so a saved capture can be
// diffed across runs.
func ClusterSettings(w io.Writer, v types.ClusterSettingsScopedView, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(v)
	case FormatWide:
		return clusterSettingsTable(w, v, true)
	case FormatTable, "":
		return clusterSettingsTable(w, v, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func clusterSettingsTable(w io.Writer, v types.ClusterSettingsScopedView, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SCOPE\tKEY\tVALUE")
	writeScope := func(scope string, m map[string]any) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			leaves := flattenSettings(map[string]any{k: m[k]})
			leafKeys := make([]string, 0, len(leaves))
			for lk := range leaves {
				leafKeys = append(leafKeys, lk)
			}
			sort.Strings(leafKeys)
			for _, lk := range leafKeys {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", scope, lk, settingValueString(leaves[lk]))
			}
		}
	}
	writeScope("persistent", v.Persistent)
	writeScope("transient", v.Transient)
	if v.IncludeDefaults && wide {
		writeScope("default", v.Defaults)
	}
	return tw.Flush()
}

// ClusterSettingsDiff renders the output of `ops settings diff`. Table
// is one row per drifted key with both scope values side-by-side; an
// empty diff prints a friendly "no drift" line so the operator sees
// "the command ran" instead of a blank stdout.
func ClusterSettingsDiff(w io.Writer, d types.ClusterSettingsDiff, f Format) error {
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
	case FormatWide, FormatTable, "":
		if len(d.Rows) == 0 {
			_, _ = fmt.Fprintln(w, "no persistent/transient drift")
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "KEY\tPERSISTENT\tTRANSIENT")
		for _, r := range d.Rows {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n",
				r.Key,
				settingValueString(r.Persistent),
				settingValueString(r.Transient),
			)
		}
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// ClusterSettingsPut renders the post-update view of `ops settings
// set`. Mirrors IndexSettingsUpdate's shape — a one-line header
// (scope, ack, dry-run) followed by the resolved key list.
func ClusterSettingsPut(w io.Writer, r types.ClusterSettingsPutResult, scope string, dryRun bool, f Format) error {
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
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tSCOPE\tDRY_RUN\tACKNOWLEDGED")
		_, _ = fmt.Fprintf(hw, "settings-set\t%s\t%t\t%t\n", scope, dryRun, r.Acknowledged)
		if err := hw.Flush(); err != nil {
			return err
		}
		writeScopeKeys(w, "PERSISTENT", r.Persistent)
		writeScopeKeys(w, "TRANSIENT", r.Transient)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func writeScopeKeys(w io.Writer, header string, m map[string]any) {
	if len(m) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, header)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	leaves := flattenSettings(m)
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", k, settingValueString(leaves[k]))
	}
	_ = tw.Flush()
}

// IndexUnblock renders the outcome of `ops unblock`. Mirrors
// IndexSettingsUpdate's shape: a one-line header (action, dry-run,
// ack, indices) followed by the resolved index list.
func IndexUnblock(w io.Writer, r types.IndexUnblockResult, f Format) error {
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
		hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(hw, "ACTION\tBLOCK\tDRY_RUN\tACKNOWLEDGED\tINDICES")
		_, _ = fmt.Fprintf(hw, "%s\t%s\t%t\t%t\t%d\n",
			dashIfEmpty(r.Action), dashIfEmpty(r.Block),
			r.DryRun, r.Acknowledged, len(r.Indices),
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
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// IndexCacheClear renders the outcome of `ops cache-clear`. The shard
// totals from the cluster's _shards envelope are surfaced so a non-zero
// failed count is grep-able from the table view.
func IndexCacheClear(w io.Writer, r types.IndexCacheClearResult, f Format) error {
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
		return indexCacheClearTable(w, r, true)
	case FormatTable, "":
		return indexCacheClearTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Realms renders the cluster's configured authentication realm chain
// for `ops realms list`. Default table columns are the load-bearing
// tuple (order, type, name, enabled, deprecated). Wide is the same
// today — the capability deliberately doesn't surface realm-level
// config (LDAP URLs, SAML metadata, OIDC client IDs), so there is no
// extra column to widen into. JSON/YAML emit the neutral []Realm slice
// unchanged so a captured listing is diffable across runs.
//
// The business layer sorts by Order → Type → Name before returning,
// so the renderer does not re-sort.
func Realms(w io.Writer, realms []types.Realm, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(realms)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(realms)
	case FormatWide:
		return realmsTable(w, realms, true)
	case FormatTable, "":
		return realmsTable(w, realms, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func realmsTable(w io.Writer, realms []types.Realm, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ORDER\tTYPE\tNAME\tENABLED\tDEPRECATED")
	for _, r := range realms {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%t\t%t\n",
			r.Order, dashIfEmpty(r.Type), dashIfEmpty(r.Name), r.Enabled, r.Deprecated)
	}
	_ = wide
	return tw.Flush()
}

// RemoteSummary renders the remote-cluster surface in the requested
// format. The neutral shape is three slices (CCS remotes, CCR
// followers, CCR auto-follow patterns) plus a CCRStatus hint and an
// optional AutoFollowError surfaced alongside the table — JSON/YAML
// emit a wrapping object so scripts can keep the halves apart and
// read the status hint without a separate stderr log line.
func RemoteSummary(w io.Writer, remotes []types.RemoteCluster, followers []types.FollowerStats, autoFollows []types.AutoFollowPattern, ccrStatus, autoFollowError string, f Format) error {
	wrap := struct {
		Remotes         []types.RemoteCluster     `json:"remotes"                     yaml:"remotes"`
		Followers       []types.FollowerStats     `json:"followers"                   yaml:"followers"`
		AutoFollows     []types.AutoFollowPattern `json:"auto_follow_patterns"        yaml:"auto_follow_patterns"`
		CCRStatus       string                    `json:"ccr_status"                  yaml:"ccr_status"`
		AutoFollowError string                    `json:"auto_follow_error,omitempty" yaml:"auto_follow_error,omitempty"`
	}{Remotes: remotes, Followers: followers, AutoFollows: autoFollows, CCRStatus: ccrStatus, AutoFollowError: autoFollowError}
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(wrap)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(wrap)
	case FormatWide:
		return remoteSummaryTable(w, remotes, followers, autoFollows, ccrStatus, autoFollowError, true)
	case FormatTable, "":
		return remoteSummaryTable(w, remotes, followers, autoFollows, ccrStatus, autoFollowError, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func remoteSummaryTable(w io.Writer, remotes []types.RemoteCluster, followers []types.FollowerStats, autoFollows []types.AutoFollowPattern, ccrStatus, autoFollowError string, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tMODE\tCONNECTED\tNODES\tSKIP_UNAVAIL")
	for _, r := range remotes {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%d\t%t\n",
			dashIfEmpty(r.Name), dashIfEmpty(r.Mode), r.Connected, r.NumNodesConnected, r.SkipUnavailable)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if wide && len(remotes) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "SEEDS")
		for _, r := range remotes {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", r.Name, joinOrDash(r.SeedHosts))
		}
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "FOLLOWERS (%s)\n", dashIfEmpty(ccrStatus))
	if ccrStatus != "ok" || len(followers) == 0 {
		// Mirror the FOLLOWERS header above — operators reading the
		// table for "is replication healthy" want to see the empty-
		// state reason explicitly. "ok" + empty followers is a real
		// outcome (CCR licensed, no follower indices configured) and
		// surfaces the same way as "unsupported"/"forbidden".
		_, _ = fmt.Fprintln(w, "  (none)")
	} else {
		ftw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(ftw, "INDEX\tLEADER\tCLUSTER\tLAG_OPS\tPAUSED")
		for _, fl := range followers {
			_, _ = fmt.Fprintf(ftw, "%s\t%s\t%s\t%d\t%t\n",
				dashIfEmpty(fl.Index), dashIfEmpty(fl.LeaderIndex), dashIfEmpty(fl.LeaderCluster), fl.FollowerLagOps, fl.Paused)
		}
		if err := ftw.Flush(); err != nil {
			return err
		}
		if wide {
			for _, fl := range followers {
				if fl.LastReadFailure != "" {
					_, _ = fmt.Fprintf(w, "  %s: last_read_failure=%q\n", fl.Index, fl.LastReadFailure)
				}
				// Surface the retention-lease age when the cluster
				// reported one. Default lease retention is 12h; once
				// it expires the follower is unrecoverable, so an
				// operator scanning the wide view wants the duration
				// at a glance. Suppress when zero — older ES versions
				// or freshly-created followers do not surface a value.
				if fl.RetentionLeaseAgeMillis > 0 {
					_, _ = fmt.Fprintf(w, "  %s: lease_age=%s\n",
						fl.Index, (time.Duration(fl.RetentionLeaseAgeMillis) * time.Millisecond).Round(time.Second))
				}
			}
		}
	}

	// Auto-follow section. Heading mirrors the FOLLOWERS section so a
	// reader scanning the bottom of the table finds both views in
	// parallel form. Skip the section entirely on a non-CCR cluster
	// (CCRStatus != "ok" already explained the absence above) — only
	// surface the empty / error case when CCR is otherwise healthy.
	if ccrStatus != "ok" {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	if autoFollowError != "" {
		_, _ = fmt.Fprintln(w, "AUTO_FOLLOW (error)")
		_, _ = fmt.Fprintf(w, "  %s\n", autoFollowError)
		return nil
	}
	_, _ = fmt.Fprintln(w, "AUTO_FOLLOW (ok)")
	if len(autoFollows) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
		return nil
	}
	atw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(atw, "NAME\tREMOTE\tLEADER_PATTERNS\tFOLLOW_PATTERN\tACTIVE")
	for _, p := range autoFollows {
		_, _ = fmt.Fprintf(atw, "%s\t%s\t%s\t%s\t%t\n",
			dashIfEmpty(p.Name),
			dashIfEmpty(p.RemoteCluster),
			joinOrDash(p.LeaderIndexPatterns),
			dashIfEmpty(p.FollowIndexPattern),
			p.Active)
	}
	return atw.Flush()
}

// License renders a LicenseStatus in the requested format. Times are
// surfaced as RFC3339 in JSON/YAML (the time.Time tag is the standard
// stdlib marshaller — no override needed) and as either RFC3339 or
// the "(no expiry)" sentinel in the table form so a basic licence is
// readable at a glance.
func License(w io.Writer, l types.LicenseStatus, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(l)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(l)
	case FormatWide:
		return licenseTable(w, l, true)
	case FormatTable, "":
		return licenseTable(w, l, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func licenseTable(w io.Writer, l types.LicenseStatus, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	expiry := "(no expiry)"
	if l.ExpiresAt != nil {
		expiry = l.ExpiresAt.UTC().Format(time.RFC3339)
	}
	issued := "-"
	if !l.IssuedAt.IsZero() {
		issued = l.IssuedAt.UTC().Format(time.RFC3339)
	}
	_, _ = fmt.Fprintln(tw, "STATUS\tTYPE\tISSUED_TO\tEXPIRES\tMAX_NODES")
	maxNodes := "-"
	if l.MaxNodes > 0 {
		maxNodes = fmt.Sprintf("%d", l.MaxNodes)
	}
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
		dashIfEmpty(l.Status), dashIfEmpty(l.Type), dashIfEmpty(l.IssuedTo), expiry, maxNodes)
	if err := tw.Flush(); err != nil {
		return err
	}
	if wide {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "ISSUER:    %s\n", dashIfEmpty(l.Issuer))
		_, _ = fmt.Fprintf(w, "ISSUED_AT: %s\n", issued)
		_, _ = fmt.Fprintf(w, "UID:       %s\n", dashIfEmpty(l.UID))
	}
	return nil
}

// Recovery renders a RecoveryReport for `esops index recovery`. Table
// output is one row per (index, shard) recovering, with the columns an
// operator watching a restore reaches for first (stage, type, primary
// vs replica, recovered percent). Wide adds the source snapshot tuple
// and the raw bytes/files totals so an operator scripting against the
// numbers can avoid re-parsing the percent string. JSON/YAML emit the
// neutral RecoveryReport directly.
func Recovery(w io.Writer, r types.RecoveryReport, f Format) error {
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
		return recoveryTable(w, r, true)
	case FormatTable, "":
		return recoveryTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func recoveryTable(w io.Writer, r types.RecoveryReport, wide bool) error {
	if len(r.Indices) == 0 {
		_, _ = fmt.Fprintln(w, "No active recoveries.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tTYPE\tSTAGE\tBYTES%\tFILES%\tBYTES_REC\tBYTES_TOTAL\tFILES_REC\tFILES_TOTAL\tSOURCE")
	} else {
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tTYPE\tSTAGE\tBYTES%\tFILES%\tRECOVERED")
	}
	for _, ix := range r.Indices {
		for _, sh := range ix.Shards {
			role := "r"
			if sh.Primary {
				role = "p"
			}
			bp := dashIfEmpty(sh.BytesPercent)
			fp := dashIfEmpty(sh.FilesPercent)
			if wide {
				_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\n",
					ix.Index, sh.ID, role, dashIfEmpty(sh.Type), dashIfEmpty(sh.Stage),
					bp, fp,
					sh.BytesRecovered, sh.BytesTotal,
					sh.FilesRecovered, sh.FilesTotal,
					dashIfEmpty(sh.Source),
				)
			} else {
				_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					ix.Index, sh.ID, role, dashIfEmpty(sh.Type), dashIfEmpty(sh.Stage),
					bp, fp,
					humanBytes(sh.BytesRecovered),
				)
			}
		}
	}
	return tw.Flush()
}

// Segments renders a SegmentsReport for `esops index segments`. Table
// output is one row per index with the columns that drive a force-merge
// decision (segment count, deleted-doc ratio, on-disk size, and the
// highest per-shard segment count — the number that matters for
// --max-num-segments planning). Wide adds the cluster's
// num_committed_segments / num_search_segments rollups so an operator
// debugging a translog issue can see the committed-vs-search split.
// JSON/YAML emit the neutral SegmentsReport directly.
func Segments(w io.Writer, r types.SegmentsReport, f Format) error {
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
		return segmentsTable(w, r, true)
	case FormatTable, "":
		return segmentsTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func segmentsTable(w io.Writer, r types.SegmentsReport, wide bool) error {
	if len(r.Indices) == 0 {
		_, _ = fmt.Fprintln(w, "No matching indices.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARDS\tSEGMENTS\tTOTAL_SEGMENTS\tMAX_SHARD\tDOCS\tDELETED\tDELETED%\tSIZE\tCOMMITTED\tSEARCHABLE")
	} else {
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARDS\tSEGMENTS\tMAX_SHARD\tDOCS\tDELETED%\tSIZE")
	}
	for _, ix := range r.Indices {
		ratio := segmentsDeletedRatioCell(ix)
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%d\t%d\n",
				ix.Index, ix.Shards,
				ix.SegmentsPrimary, ix.SegmentsTotal, ix.MaxSegmentsShard,
				ix.DocsTotal, ix.DocsDeleted, ratio, humanBytes(ix.Bytes),
				ix.Committed, ix.Searchable,
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\t%s\n",
				ix.Index, ix.Shards,
				ix.SegmentsPrimary, ix.MaxSegmentsShard,
				ix.DocsTotal, ratio, humanBytes(ix.Bytes),
			)
		}
	}
	return tw.Flush()
}

// segmentsDeletedRatioCell formats the deleted-doc fraction as a one-
// decimal percent. Zero-doc indices render as "-" since the ratio is
// undefined; mirrors the behaviour of internal/index.FormatDeletedRatio
// without coupling the renderer to that package.
func segmentsDeletedRatioCell(ix types.IndexSegments) string {
	total := ix.DocsTotal + ix.DocsDeleted
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", float64(ix.DocsDeleted)/float64(total)*100)
}

func indexCacheClearTable(w io.Writer, r types.IndexCacheClearResult, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ACTION\tCACHES\tDRY_RUN\tINDICES\tSHARDS_OK\tSHARDS_FAILED")
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%d\t%d/%d\t%d\n",
		dashIfEmpty(r.Action),
		joinOrDash(r.Caches),
		r.DryRun,
		len(r.Indices),
		r.Successful, r.Total,
		r.Failed,
	)
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(r.Indices) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "INDICES")
		for _, name := range r.Indices {
			_, _ = fmt.Fprintf(w, "  %s\n", name)
		}
	}
	if wide && len(r.Fields) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "FIELDS")
		for _, f := range r.Fields {
			_, _ = fmt.Fprintf(w, "  %s\n", f)
		}
	}
	return nil
}

// IngestPipelines renders `esops pipeline list`. Table mode prints
// name + processor count + description; wide adds version and the
// comma-joined list of processor types so operators can spot e.g. a
// rogue `set` processor without dropping into --output json. JSON,
// NDJSON and YAML emit the neutral IngestPipeline slice directly.
func IngestPipelines(w io.Writer, rows []types.IngestPipeline, f Format) error {
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
		return ingestPipelinesTable(w, rows, true)
	case FormatTable, "":
		return ingestPipelinesTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ingestPipelinesTable(w io.Writer, rows []types.IngestPipeline, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tVERSION\tPROCESSORS\tON_FAILURE\tTYPES\tDESCRIPTION")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tPROCESSORS\tDESCRIPTION")
	}
	for _, r := range rows {
		version := "-"
		if r.Version > 0 {
			version = fmt.Sprintf("%d", r.Version)
		}
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n",
				r.Name,
				version,
				len(r.Processors),
				len(r.OnFailure),
				dashIfEmpty(strings.Join(processorTypes(r.Processors), ",")),
				dashIfEmpty(truncate(r.Description, 80)),
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n",
				r.Name,
				len(r.Processors),
				dashIfEmpty(truncate(r.Description, 80)),
			)
		}
	}
	return tw.Flush()
}

// IngestPipeline renders a single ingest pipeline — `esops pipeline
// get`. Table mode is a header line followed by an indented JSON
// dump of the processor chain (and on_failure handlers, when set);
// wide adds the _meta block. JSON/YAML emit the neutral
// IngestPipeline shape directly.
func IngestPipeline(w io.Writer, p types.IngestPipeline, f Format) error {
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
		return ingestPipelineDetail(w, p, true)
	case FormatTable, "":
		return ingestPipelineDetail(w, p, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ingestPipelineDetail(w io.Writer, p types.IngestPipeline, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "NAME\tVERSION\tPROCESSORS\tON_FAILURE\tDESCRIPTION")
	version := "-"
	if p.Version > 0 {
		version = fmt.Sprintf("%d", p.Version)
	}
	_, _ = fmt.Fprintf(hw, "%s\t%s\t%d\t%d\t%s\n",
		p.Name,
		version,
		len(p.Processors),
		len(p.OnFailure),
		dashIfEmpty(p.Description),
	)
	if err := hw.Flush(); err != nil {
		return err
	}
	if len(p.Processors) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "PROCESSORS")
		buf, err := json.MarshalIndent(p.Processors, "  ", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
	}
	if len(p.OnFailure) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "ON_FAILURE")
		buf, err := json.MarshalIndent(p.OnFailure, "  ", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
	}
	if wide && len(p.Meta) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "META")
		buf, err := json.MarshalIndent(p.Meta, "  ", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  %s\n", string(buf))
	}
	return nil
}

// processorTypes walks an []map[string]any processor chain and pulls
// the per-processor top-level key (the processor type — "set",
// "grok", "remove", …). Order is preserved so an operator scanning a
// wide-mode row sees the pipeline's actual order, not an alphabetical
// summary that hides "set runs after grok".
func processorTypes(procs []map[string]any) []string {
	out := make([]string, 0, len(procs))
	for _, p := range procs {
		for k := range p {
			out = append(out, k)
			break // each processor wraps exactly one type key
		}
	}
	return out
}

// DataStreams renders the data-stream list view for `esops datastream
// list`. Table columns track what an operator scanning a fleet
// reaches for: name, generation, backing-index count, oldest and
// newest backing index, and the bound lifecycle policy
// (ILM on ES, ISM template on OS — only one of the two is ever set
// for a given stream). Wide adds the bound template and
// hidden/system flags. JSON/YAML/NDJSON emit the neutral DataStream
// shape; NDJSON writes one record per line so a cluster with
// thousands of streams doesn't have to be parsed as one blob.
func DataStreams(w io.Writer, rows []types.DataStream, f Format) error {
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
		return dataStreamsTable(w, rows, true)
	case FormatTable, "":
		return dataStreamsTable(w, rows, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func dataStreamsTable(w io.Writer, rows []types.DataStream, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tGEN\tINDICES\tOLDEST\tNEWEST\tLIFECYCLE\tSIZE\tDOCS\tHEALTH\tTEMPLATE\tHIDDEN\tSYSTEM")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tGEN\tINDICES\tOLDEST\tNEWEST\tLIFECYCLE\tSIZE\tDOCS")
	}
	for _, r := range rows {
		gen := "-"
		if r.Generation > 0 {
			gen = fmt.Sprintf("%d", r.Generation)
		}
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Name,
				dashIfEmpty(r.Status),
				gen,
				r.IndexCount,
				dashIfEmpty(r.BackingIndexOldest),
				dashIfEmpty(r.BackingIndexNewest),
				dashIfEmpty(dataStreamLifecycle(r)),
				humanBytesOrDash(r.StoreBytes),
				docsCountOrDash(r.DocsCount),
				dataStreamHealthSummary(r),
				dashIfEmpty(r.Template),
				masterMark(r.Hidden),
				masterMark(r.System),
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				r.Name,
				dashIfEmpty(r.Status),
				gen,
				r.IndexCount,
				dashIfEmpty(r.BackingIndexOldest),
				dashIfEmpty(r.BackingIndexNewest),
				dashIfEmpty(dataStreamLifecycle(r)),
				humanBytesOrDash(r.StoreBytes),
				docsCountOrDash(r.DocsCount),
			)
		}
	}
	return tw.Flush()
}

// dataStreamHealthSummary collapses the per-backing-index health
// counts to a compact string like "2g/0y/0r". Rendered in wide mode
// so an operator scanning a fleet sees the spread of backing-index
// health without expanding into `datastream get`. "-" when no
// enrichment data is available (older clusters, /_cat/indices
// permission denial, etc.).
func dataStreamHealthSummary(d types.DataStream) string {
	if len(d.Indices) == 0 {
		return "-"
	}
	var green, yellow, red, unknown int
	for _, ix := range d.Indices {
		switch strings.ToLower(ix.Health) {
		case "green":
			green++
		case "yellow":
			yellow++
		case "red":
			red++
		default:
			unknown++
		}
	}
	// If we never saw a health value at all, the enrichment step
	// either ran into an error or was skipped — render "-" so
	// operators don't read "0g/0y/0r" as "everything is broken".
	if green+yellow+red == 0 {
		return "-"
	}
	return fmt.Sprintf("%dg/%dy/%dr", green, yellow, red)
}

// docsCountOrDash renders a doc-count cell. Zero is ambiguous —
// "the stream has no docs" vs "the cluster did not report a count"
// — so we render zero as "-" to match the convention store-bytes
// uses via humanBytesOrDash.
func docsCountOrDash(n int64) string {
	if n <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

// DataStream renders a single data stream — `esops datastream get`.
// Table mode is a header line followed by an indented list of the
// backing indices in generation order; wide adds the indices' uuid
// and managed-by columns when the cluster reports them. JSON/YAML
// emit the neutral DataStream shape directly.
func DataStream(w io.Writer, d types.DataStream, f Format) error {
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
		return dataStreamDetail(w, d, true)
	case FormatTable, "":
		return dataStreamDetail(w, d, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func dataStreamDetail(w io.Writer, d types.DataStream, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "NAME\tSTATUS\tGEN\tINDICES\tWRITE_INDEX\tLIFECYCLE\tTEMPLATE\tSIZE\tDOCS")
	gen := "-"
	if d.Generation > 0 {
		gen = fmt.Sprintf("%d", d.Generation)
	}
	_, _ = fmt.Fprintf(hw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
		d.Name,
		dashIfEmpty(d.Status),
		gen,
		d.IndexCount,
		dashIfEmpty(d.WriteIndex),
		dashIfEmpty(dataStreamLifecycle(d)),
		dashIfEmpty(d.Template),
		humanBytesOrDash(d.StoreBytes),
		docsCountOrDash(d.DocsCount),
	)
	if err := hw.Flush(); err != nil {
		return err
	}
	if len(d.Indices) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "BACKING INDICES")
	bw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(bw, "  INDEX\tHEALTH\tSIZE\tDOCS\tUUID\tMANAGED_BY")
		for _, ix := range d.Indices {
			_, _ = fmt.Fprintf(bw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				ix.Name,
				dashIfEmpty(ix.Health),
				humanBytesOrDash(ix.StoreBytes),
				docsCountOrDash(ix.DocsCount),
				dashIfEmpty(ix.UUID),
				dashIfEmpty(ix.ManagedBy),
			)
		}
	} else {
		_, _ = fmt.Fprintln(bw, "  INDEX\tHEALTH\tSIZE\tDOCS")
		for _, ix := range d.Indices {
			_, _ = fmt.Fprintf(bw, "  %s\t%s\t%s\t%s\n",
				ix.Name,
				dashIfEmpty(ix.Health),
				humanBytesOrDash(ix.StoreBytes),
				docsCountOrDash(ix.DocsCount),
			)
		}
	}
	return bw.Flush()
}

// dataStreamLifecycle picks the one lifecycle reference set on the
// stream — ILMPolicy on Elasticsearch, ISMTemplate on OpenSearch —
// for the LIFECYCLE table column. Both fields are zero on streams
// the operator hasn't bound to a policy, in which case the column
// renders as "-".
func dataStreamLifecycle(d types.DataStream) string {
	if d.ILMPolicy != "" {
		return d.ILMPolicy
	}
	return d.ISMTemplate
}

// DataStreamRollover renders the outcome of `esops datastream
// rollover`. Mirrors IndexRollover output: table is a one-line
// header (data_stream, dry-run, old/new index, rolled-over flag,
// ack flags) followed by the per-condition outcome block when
// present. JSON/YAML emit the neutral DataStreamRolloverResult
// shape directly.
func DataStreamRollover(w io.Writer, r types.DataStreamRolloverResult, f Format) error {
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
		_, _ = fmt.Fprintln(tw, "DATA_STREAM\tDRY_RUN\tOLD_INDEX\tNEW_INDEX\tROLLED_OVER\tACK\tSHARDS_ACK")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%t\t%t\t%t\n",
			r.DataStream, r.DryRun,
			dashIfEmpty(r.OldIndex), dashIfEmpty(r.NewIndex),
			r.RolledOver, r.Acknowledged, r.ShardsAcknowledged,
		)
		if err := tw.Flush(); err != nil {
			return err
		}
		if len(r.ConditionsMet) > 0 {
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "CONDITIONS")
			cw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(cw, "  KEY\tMET")
			for k, v := range r.ConditionsMet {
				_, _ = fmt.Fprintf(cw, "  %s\t%t\n", k, v)
			}
			if err := cw.Flush(); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// DataStreamModify renders the outcome of `esops datastream modify`.
// Table is a one-line header plus a list of the submitted actions.
// JSON/YAML emit the neutral DataStreamModifyResult shape directly.
func DataStreamModify(w io.Writer, r types.DataStreamModifyResult, f Format) error {
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
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tACK\tACTIONS")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%t\t%d\n",
			r.Action, r.DryRun, r.Acknowledged, len(r.Actions))
		if err := tw.Flush(); err != nil {
			return err
		}
		if len(r.Actions) == 0 {
			return nil
		}
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "ACTIONS")
		aw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(aw, "  ACTION\tDATA_STREAM\tINDEX")
		for _, a := range r.Actions {
			_, _ = fmt.Fprintf(aw, "  %s\t%s\t%s\n", a.Action, a.DataStream, a.Index)
		}
		return aw.Flush()
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// DataStreamDelete renders the outcome of `esops datastream delete`.
// Table is a one-line header plus the resolved stream names.
// JSON/YAML emit the neutral DataStreamDeleteResult shape directly.
// DataStreamStats renders the outcome of `esops datastream stats`.
// Table mode is a one-line header (aggregate counts + total size)
// followed by the per-stream rows; wide mode adds the
// maximum-timestamp column so an operator can spot a stale stream
// without paging into individual `datastream get` calls. JSON/YAML
// emit the neutral DataStreamStatsReport shape directly.
func DataStreamStats(w io.Writer, r types.DataStreamStatsReport, f Format) error {
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
		return dataStreamStatsTable(w, r, true)
	case FormatTable, "":
		return dataStreamStatsTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func dataStreamStatsTable(w io.Writer, r types.DataStreamStatsReport, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "STREAMS\tBACKING_INDICES\tTOTAL_SIZE")
	// humanBytes (not humanBytesOrDash) for the aggregate — a cluster
	// genuinely reporting zero bytes is unambiguous here, so "0b"
	// reads more honestly than "-" alongside the explicit counts.
	_, _ = fmt.Fprintf(hw, "%d\t%d\t%s\n",
		r.DataStreamCount,
		r.BackingIndicesCount,
		humanBytes(r.TotalStoreBytes),
	)
	if err := hw.Flush(); err != nil {
		return err
	}
	if len(r.DataStreams) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tINDICES\tSIZE\tMAX_TIMESTAMP")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tINDICES\tSIZE")
	}
	for _, s := range r.DataStreams {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n",
				s.DataStream,
				s.BackingIndicesCount,
				humanBytes(s.StoreBytes),
				maxTimestampOrDash(s.MaximumTimestampMillis),
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n",
				s.DataStream,
				s.BackingIndicesCount,
				humanBytes(s.StoreBytes),
			)
		}
	}
	return tw.Flush()
}

// maxTimestampOrDash renders an epoch-millis "maximum timestamp" as an
// RFC3339 UTC string. Zero (the cluster reported no value, or the
// stream has no docs yet) renders as "-".
func maxTimestampOrDash(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func DataStreamDelete(w io.Writer, r types.DataStreamDeleteResult, f Format) error {
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
		_, _ = fmt.Fprintln(tw, "ACTION\tDRY_RUN\tACK\tSTREAMS")
		_, _ = fmt.Fprintf(tw, "%s\t%t\t%t\t%d\n",
			r.Action, r.DryRun, r.Acknowledged, len(r.Names))
		if err := tw.Flush(); err != nil {
			return err
		}
		if len(r.Names) == 0 {
			return nil
		}
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "STREAMS")
		for _, n := range r.Names {
			_, _ = fmt.Fprintf(w, "  %s\n", n)
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}
