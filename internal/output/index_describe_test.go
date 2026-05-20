package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func sampleIndexDescription() types.IndexDescription {
	return types.IndexDescription{
		Name:    "logs-2025-05",
		Dialect: "elasticsearch",
		Info: types.Index{
			Name:               "logs-2025-05",
			UUID:               "abc123",
			Health:             "green",
			Status:             "open",
			Primaries:          3,
			Replicas:           1,
			DocsCount:          1_204_331,
			DocsDeleted:        25,
			StoreBytes:         2_400_000_000,
			PrimaryStoreBytes:  1_200_000_000,
			CreationDateMillis: 1_700_000_000_000, // late 2023, deterministic
		},
		Settings: types.IndexSettings{
			Index: "logs-2025-05",
			Settings: map[string]any{
				"index": map[string]any{
					"number_of_shards":   "3",
					"number_of_replicas": "1",
					"refresh_interval":   "1s",
				},
			},
		},
		Aliases: []types.Alias{
			{Alias: "logs", Index: "logs-2025-05", IsWriteIndex: true},
		},
		ILM: &types.ILMExplainEntry{
			Index:   "logs-2025-05",
			Managed: true,
			Policy:  "logs-30d",
			Phase:   "warm",
			Action:  "complete",
		},
		Mappings: &types.IndexMapping{
			Name: "logs-2025-05",
			Properties: map[string]any{
				"timestamp": map[string]any{"type": "date"},
				"message":   map[string]any{"type": "text"},
				"level":     map[string]any{"type": "keyword"},
				"user": map[string]any{
					"properties": map[string]any{
						"name": map[string]any{"type": "keyword"},
						"id":   map[string]any{"type": "long"},
					},
				},
			},
			Dynamic: "strict",
		},
		Segments: &types.IndexSegments{
			Index:           "logs-2025-05",
			Shards:          3,
			SegmentsTotal:   12,
			SegmentsPrimary: 6,
			DocsTotal:       1_204_331,
			DocsDeleted:     25,
		},
		Shards: []types.Shard{
			{Index: "logs-2025-05", Shard: 0, Primary: true, State: "STARTED", Node: "node-1", StoreBytes: 400_000_000},
			{Index: "logs-2025-05", Shard: 0, Primary: false, State: "STARTED", Node: "node-2", StoreBytes: 400_000_000},
		},
		Tasks: []types.Task{
			{ID: "node:1", Action: "indices:admin/forcemerge", Description: "Force-merge [logs-2025-05]", RunningTimeNanos: 5_000_000_000},
		},
	}
}

func TestIndexDescribe_textHasHeaderAndSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, sampleIndexDescription(), output.IndexDescribeOptions{
		AutoShardMax: 12,
	}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "logs-2025-05",
		"UUID:", "abc123",
		"Health:", "green",
		"Status:", "open",
		"Docs:", "1204331",
		"Aliases:", "logs",
		"Lifecycle (ILM):", "logs-30d", "warm",
		"Settings (configured):", "index.refresh_interval",
		"Mappings:", "Fields:", "By type:",
		"Shards:", "[p0]",
		"Segments:",
		"Tasks (in flight, touching this index):", "forcemerge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestIndexDescribe_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, sampleIndexDescription(), output.IndexDescribeOptions{}, output.FormatJSON); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	var got types.IndexDescription
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Name != "logs-2025-05" {
		t.Errorf("Name = %q, want logs-2025-05", got.Name)
	}
	if got.ILM == nil || got.ILM.Policy != "logs-30d" {
		t.Errorf("ILM block did not survive round-trip: %+v", got.ILM)
	}
}

func TestIndexDescribe_textOmitsEmptySections(t *testing.T) {
	d := types.IndexDescription{
		Name: "minimal",
		Info: types.Index{Name: "minimal", Health: "green", Status: "open"},
	}
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{AutoShardMax: 12}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	// "Shards:" appears in the header summary (`Shards: N primary, M replica`)
	// regardless of the per-shard section; test for the section-heading
	// form ("Shards:\n") which only fires when Shards is populated.
	for _, banned := range []string{
		"Aliases:",
		"Lifecycle",
		"Mappings:",
		"Shards:\n",
		"Recovery",
		"Segments:",
		"Tasks (in flight",
		"Section warnings:",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("empty-section view should not contain %q\n--- output ---\n%s", banned, out)
		}
	}
}

func TestIndexDescribe_shardListingAutoCollapses(t *testing.T) {
	d := sampleIndexDescription()
	// Stage 20 shards — above the AutoShardMax of 12.
	d.Shards = make([]types.Shard, 20)
	for i := range d.Shards {
		d.Shards[i] = types.Shard{Index: "logs-2025-05", Shard: i, Primary: i%2 == 0, State: "STARTED", Node: "node-x"}
	}
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{AutoShardMax: 12}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "20 total") {
		t.Errorf("auto-collapse summary missing; got:\n%s", out)
	}
	if strings.Contains(out, "[p0]") {
		t.Errorf("per-shard listing leaked into auto-collapsed output:\n%s", out)
	}
}

func TestIndexDescribe_showShardsForcesListing(t *testing.T) {
	d := sampleIndexDescription()
	d.Shards = make([]types.Shard, 20)
	for i := range d.Shards {
		d.Shards[i] = types.Shard{Index: "logs-2025-05", Shard: i, Primary: i%2 == 0, State: "STARTED", Node: "node-x"}
	}
	force := true
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{
		AutoShardMax: 12,
		ShowShards:   &force,
	}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[p0]") {
		t.Errorf("--show-shards should force per-shard listing; got:\n%s", out)
	}
}

func TestIndexDescribe_noShardsHidesListing(t *testing.T) {
	d := sampleIndexDescription()
	hide := false
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{
		AutoShardMax: 12,
		ShowShards:   &hide,
	}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "Shards:\n") {
		t.Errorf("--no-shards should hide the per-shard listing; got:\n%s", out)
	}
}

func TestIndexDescribe_fullMappingsEmitsTree(t *testing.T) {
	d := sampleIndexDescription()
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{
		FullMappings: true,
		AutoShardMax: 12,
	}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\"timestamp\"") || !strings.Contains(out, "\"date\"") {
		t.Errorf("full mappings should dump the raw properties tree; got:\n%s", out)
	}
	if strings.Contains(out, "By type:") {
		t.Errorf("full mappings should suppress the per-type summary; got:\n%s", out)
	}
}

func TestIndexDescribe_yamlRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, sampleIndexDescription(), output.IndexDescribeOptions{}, output.FormatYAML); err != nil {
		t.Fatalf("IndexDescribe(yaml): %v", err)
	}
	// YAML round-trip via JSON would change tag handling; just confirm
	// the marshalled bytes contain a few load-bearing fields. YAML
	// decoding of map[string]any is fiddly across libraries, so we
	// don't unmarshal — that's covered by the JSON test.
	out := buf.String()
	for _, want := range []string{
		"name: logs-2025-05",
		"dialect: elasticsearch",
		"ilm:",
		"policy: logs-30d",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("yaml output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestIndexDescribe_widePrintsShardsEvenAboveThreshold(t *testing.T) {
	d := sampleIndexDescription()
	d.Shards = make([]types.Shard, 20)
	for i := range d.Shards {
		d.Shards[i] = types.Shard{Index: "logs-2025-05", Shard: i, Primary: i%2 == 0, State: "STARTED", Node: "node-x"}
	}
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{}, output.FormatWide); err != nil {
		t.Fatalf("IndexDescribe(wide): %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[p0]") {
		t.Errorf("--output wide should force per-shard listing; got:\n%s", out)
	}
}

func TestIndexDescribe_wideHonoursExplicitNoShards(t *testing.T) {
	d := sampleIndexDescription()
	d.Shards = make([]types.Shard, 20)
	for i := range d.Shards {
		d.Shards[i] = types.Shard{Index: "logs-2025-05", Shard: i, Primary: i%2 == 0, State: "STARTED", Node: "node-x"}
	}
	hide := false
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{ShowShards: &hide}, output.FormatWide); err != nil {
		t.Fatalf("IndexDescribe(wide+no-shards): %v", err)
	}
	if strings.Contains(buf.String(), "[p0]") {
		t.Errorf("--no-shards should override wide's force-show; got:\n%s", buf.String())
	}
}

func TestIndexDescribe_autoShardThresholdDefaults(t *testing.T) {
	d := sampleIndexDescription()
	d.Shards = make([]types.Shard, 20)
	for i := range d.Shards {
		d.Shards[i] = types.Shard{Index: "logs-2025-05", Shard: i, State: "STARTED"}
	}
	// AutoShardMax zero → renderer falls back to DefaultDescribeShardListing.
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	if !strings.Contains(buf.String(), "20 total") {
		t.Errorf("auto-collapse should fire with zero AutoShardMax (defaults to %d); got:\n%s",
			output.DefaultDescribeShardListing, buf.String())
	}
}

func TestIndexDescribe_matchedTemplateRendered(t *testing.T) {
	d := sampleIndexDescription()
	d.MatchedTemplate = &types.IndexTemplate{
		Name:          "logs-template",
		IndexPatterns: []string{"logs-*"},
		Priority:      100,
		ComposedOf:    []string{"logs-mappings", "logs-settings"},
	}
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Matched template:", "logs-template", "logs-*", "Priority:", "100", "logs-mappings"} {
		if !strings.Contains(out, want) {
			t.Errorf("template section missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestIndexDescribe_recoverySurfacesSource(t *testing.T) {
	d := sampleIndexDescription()
	d.Recovery = &types.IndexRecovery{
		Index: "logs-2025-05",
		Shards: []types.ShardRecovery{
			{ID: 0, Type: "SNAPSHOT", Stage: "INDEX", Primary: true, Source: "backup-repo/snap-2025-05", BytesPercent: "42.0%"},
		},
	}
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "source=backup-repo/snap-2025-05") {
		t.Errorf("recovery section should surface the snapshot source; got:\n%s", out)
	}
}

func TestIndexDescribe_warningsRendered(t *testing.T) {
	d := sampleIndexDescription()
	d.SectionWarnings = map[string]string{"segments": "boom"}
	var buf bytes.Buffer
	if err := output.IndexDescribe(&buf, d, output.IndexDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("IndexDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Section warnings:") || !strings.Contains(out, "segments") || !strings.Contains(out, "boom") {
		t.Errorf("warnings section not rendered correctly:\n%s", out)
	}
}
