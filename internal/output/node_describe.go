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

// DefaultNodeDescribeShardListing is the cap above which the per-shard
// listing on `ops node describe` collapses to a one-line summary in
// auto mode. 25 is roomier than the index threshold because a single
// data node legitimately hosts more shards than a single index does
// (the node holds every shard of every index it serves).
const DefaultNodeDescribeShardListing = 25

// NodeDescribeOptions are the renderer-side knobs `ops node describe`
// passes down. ShowShards is the same tri-state ("nil"=auto / "&true"
// / "&false") used by IndexDescribeOptions. AutoShardMax=0 falls back
// to DefaultNodeDescribeShardListing.
type NodeDescribeOptions struct {
	ShowShards   *bool
	AutoShardMax int
}

// NodeDescribe renders a NodeDescription in the requested format. Wide
// implies force-show-shards (the operator-friendly "give me everything"
// mode); JSON/YAML emit the neutral shape unchanged.
func NodeDescribe(w io.Writer, d types.NodeDescription, opts NodeDescribeOptions, f Format) error {
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
		if opts.ShowShards == nil {
			force := true
			opts.ShowShards = &force
		}
		nodeDescribeText(w, d, opts)
		return nil
	case FormatTable, "":
		nodeDescribeText(w, d, opts)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func nodeDescribeText(w io.Writer, d types.NodeDescription, opts NodeDescribeOptions) {
	if opts.AutoShardMax == 0 {
		opts.AutoShardMax = DefaultNodeDescribeShardListing
	}
	writeNodeHeader(w, d)
	writeNodeJVMandOS(w, d)
	writeNodeBootstrap(w, d.Bootstrap)
	if d.Bootstrap != nil {
		writeNodePlugins(w, d.Bootstrap.Plugins)
	}
	writeNodeSettings(w, d.Settings)
	writeNodeDrain(w, d.Drain)
	writeNodeShards(w, d.Shards, opts)
	writeWarnings(w, d.SectionWarnings)
}

func writeNodeHeader(w io.Writer, d types.NodeDescription) {
	n := d.Info
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Name:\t%s\n", n.Name)
	if n.IP != "" {
		fprintfTW(tw, "IP:\t%s\n", n.IP)
	}
	if n.Version != "" {
		fprintfTW(tw, "Version:\t%s\n", n.Version)
	}
	if len(n.Roles) > 0 {
		fprintfTW(tw, "Roles:\t%s\n", strings.Join(n.Roles, ", "))
	}
	if n.Master {
		fprintfTW(tw, "Master:\ttrue (this is the elected master)\n")
	}
	if n.Uptime != "" {
		fprintfTW(tw, "Uptime:\t%s\n", n.Uptime)
	}
	if n.HeapMaxBytes > 0 {
		fprintfTW(tw, "Heap:\t%s used / %s max  (%d%%)\n",
			humanBytes(n.HeapUsedBytes), humanBytes(n.HeapMaxBytes), n.HeapPercent)
	}
	if n.DiskTotalBytes > 0 {
		fprintfTW(tw, "Disk:\t%s used / %s total  (%d%% used, %s avail)\n",
			humanBytes(n.DiskUsedBytes), humanBytes(n.DiskTotalBytes),
			n.DiskUsedPercent, humanBytes(n.DiskAvailBytes))
	}
	if n.CPUPercent > 0 {
		fprintfTW(tw, "CPU:\t%d%%\n", n.CPUPercent)
	}
	if n.Load1m != "" {
		fprintfTW(tw, "Load (1m):\t%s\n", n.Load1m)
	}
	fprintfTW(tw, "Shards on node:\t%d primary, %d replica  (%s on disk)\n",
		d.ShardSummary.Primaries, d.ShardSummary.Replicas, humanBytes(d.ShardSummary.StoreBytes))
	_ = tw.Flush()
}

func writeNodeJVMandOS(w io.Writer, d types.NodeDescription) {
	if d.Stats == nil {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "JVM & host:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if d.Stats.JVM.Heap.InitBytes > 0 || d.Stats.JVM.Heap.MaxBytes > 0 {
		fprintfTW(tw, "  Heap config:\t-Xms %s / -Xmx %s\n",
			humanBytes(d.Stats.JVM.Heap.InitBytes), humanBytes(d.Stats.JVM.Heap.MaxBytes))
	}
	if d.Stats.OS.TotalPhysicalMemoryBytes > 0 {
		fprintfTW(tw, "  Host memory:\t%s\n", humanBytes(d.Stats.OS.TotalPhysicalMemoryBytes))
	}
	_ = tw.Flush()
}

func writeNodeBootstrap(w io.Writer, b *types.NodeBootstrap) {
	if b == nil {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Bootstrap & limits:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  mlockall:\t%t\n", b.MlockallEnabled)
	if b.MaxFileDescriptors > 0 {
		fprintfTW(tw, "  max_file_descriptors:\t%d\n", b.MaxFileDescriptors)
	}
	if b.MaxMapCount > 0 {
		fprintfTW(tw, "  vm.max_map_count:\t%d\n", b.MaxMapCount)
	}
	if len(b.GCCollectors) > 0 {
		fprintfTW(tw, "  GC collectors:\t%s\n", strings.Join(b.GCCollectors, ", "))
	}
	if len(b.BootstrapWarnings) > 0 {
		fprintfTW(tw, "  Bootstrap warnings:\t%d\n", len(b.BootstrapWarnings))
		for _, msg := range b.BootstrapWarnings {
			fprintfTW(tw, "    -\t%s\n", msg)
		}
	}
	_ = tw.Flush()
}

// writeNodePlugins renders the installed-plugin list one per row,
// `name  version`. Sourced from /_nodes/<id>/plugins (folded into the
// existing NodeBootstrap call), so it costs nothing extra to display.
// Sorted alphabetically by the adapter so output is stable across runs.
func writeNodePlugins(w io.Writer, plugins []types.NodePlugin) {
	if len(plugins) == 0 {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Plugins:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range plugins {
		v := p.Version
		if v == "" {
			v = "-"
		}
		fprintfTW(tw, "  %s\t%s\n", p.Name, v)
	}
	_ = tw.Flush()
}

func writeNodeSettings(w io.Writer, s *types.NodeSettings) {
	if s == nil || len(s.Settings) == 0 {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Static node settings:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	keys := make([]string, 0, len(s.Settings))
	for k := range s.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fprintfTW(tw, "  %s:\t%s\n", k, settingValueString(s.Settings[k]))
	}
	_ = tw.Flush()
}

func writeNodeDrain(w io.Writer, d types.NodeDrainStatus) {
	if !d.AllocationExcluded && !d.VotingExcluded {
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Drain state:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if d.AllocationExcluded {
		fprintfTW(tw, "  Allocation exclude:\ttrue  (%s)\n", strings.Join(d.Scopes, ", "))
	}
	if d.VotingExcluded {
		fprintfTW(tw, "  Voting exclude:\ttrue\n")
	}
	_ = tw.Flush()
}

func writeNodeShards(w io.Writer, shards []types.Shard, opts NodeDescribeOptions) {
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
		fprintfW(w, "Shards on node:  %d total (pass --show-shards or --output wide for the per-shard listing)\n", len(shards))
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Shards on node:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range shards {
		role := "r"
		if s.Primary {
			role = "p"
		}
		fprintfTW(tw, "  %s\t[%s%d]\t%s\t%s\n",
			s.Index, role, s.Shard, s.State, humanBytes(s.StoreBytes))
	}
	_ = tw.Flush()
}
