// Package output renders domain types in the formats exposed by the CLI
// (table, json, yaml, wide). NDJSON is deferred until a streaming
// command needs it.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"

	"github.com/dj-wasabi/esops-go/internal/types"
)

// Format identifies a user-facing output format.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
	FormatWide  Format = "wide"
)

// Parse validates and normalises a --output value.
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
		return "", fmt.Errorf("ndjson output is not implemented yet")
	default:
		return "", fmt.Errorf("unknown output format %q (supported: table, json, yaml, wide)", s)
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
// unchanged — scripts depend on stable field names.
func Shards(w io.Writer, shards []types.Shard, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(shards)
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
