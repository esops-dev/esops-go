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

// Parse validates and normalises a --output value.
//
// NDJSON is accepted only for commands that opt in via a dedicated
// renderer (currently `index list`). Commands whose renderer does not
// implement ndjson return a per-command "not supported" error — the
// parse step no longer rejects the format up-front.
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

// formatNotSupported returns a uniform error message for commands whose
// renderer does not (yet) implement a particular Format. Keeps the
// ndjson-not-yet-streaming story consistent without each renderer
// inventing its own wording.
func formatNotSupported(cmd string, f Format) error {
	return fmt.Errorf("output format %q is not supported for %s", f, cmd)
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
	case FormatNDJSON:
		return formatNotSupported("health", f)
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
	case FormatNDJSON:
		return formatNotSupported("shards", f)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Indices renders a slice of Index rows in the requested format. Table
// output shows the columns an operator reaches for when triaging an
// index-count or size question (name, health, status, primaries/replicas,
// docs, size). Wide adds deleted docs, UUID, creation date and
// primary-store bytes. JSON/YAML emit the neutral Index shape;
// NDJSON writes one Index per line so large lists don't have to be
// held in the downstream consumer's memory in one blob.
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
