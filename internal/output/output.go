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
		defer enc.Close()
		return enc.Encode(h)
	case FormatWide:
		return healthTable(w, h, info, true)
	case FormatTable, "":
		return healthTable(w, h, info, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func healthTable(w io.Writer, h types.ClusterHealth, info types.ClusterInfo, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		fmt.Fprintln(tw, "NAME\tSTATUS\tDIALECT\tVERSION\tNODES\tDATA_NODES\tSHARDS\tPRIMARIES\tRELOC\tINIT\tUNASSIGNED\tPENDING\tACTIVE%")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f\n",
			h.ClusterName, h.Status, info.Dialect, info.Version,
			h.NumberOfNodes, h.NumberOfDataNodes,
			h.ActiveShards, h.ActivePrimaryShards,
			h.RelocatingShards, h.InitializingShards, h.UnassignedShards,
			h.NumberOfPendingTasks, h.ActiveShardsPercentAsNumber,
		)
	} else {
		fmt.Fprintln(tw, "NAME\tSTATUS\tNODES\tDATA_NODES\tSHARDS\tUNASSIGNED\tPENDING")
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			h.ClusterName, h.Status,
			h.NumberOfNodes, h.NumberOfDataNodes,
			h.ActiveShards, h.UnassignedShards, h.NumberOfPendingTasks,
		)
	}
	return tw.Flush()
}
