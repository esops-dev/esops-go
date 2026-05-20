package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ---------- NodeDescribe ----------

func sampleNodeDescription() types.NodeDescription {
	return types.NodeDescription{
		Name:    "data-1",
		Dialect: "elasticsearch",
		Info: types.Node{
			Name:           "data-1",
			IP:             "10.0.0.5",
			Version:        "9.0.2",
			Roles:          []string{"master", "data", "ingest"},
			Master:         true,
			HeapUsedBytes:  4 * 1 << 30,
			HeapMaxBytes:   8 * 1 << 30,
			HeapPercent:    50,
			DiskTotalBytes: 500 * 1 << 30,
			DiskAvailBytes: 200 * 1 << 30,
			DiskUsedBytes:  300 * 1 << 30,
			DiskUsedPercent: 60,
			Uptime:         "12d",
		},
		Stats: &types.NodeStats{
			Name: "data-1",
			JVM:  types.NodeJVMStats{Heap: types.NodeJVMHeap{InitBytes: 8 * 1 << 30, MaxBytes: 8 * 1 << 30}},
			OS:   types.NodeOSStats{TotalPhysicalMemoryBytes: 16 * 1 << 30},
		},
		Bootstrap: &types.NodeBootstrap{
			Name:               "data-1",
			MlockallEnabled:    true,
			MaxFileDescriptors: 65535,
			MaxMapCount:        262144,
		},
		Drain: types.NodeDrainStatus{
			AllocationExcluded: true,
			Scopes:             []string{"transient"},
		},
		Shards: []types.Shard{
			{Index: "logs", Shard: 0, Primary: true, State: "STARTED", StoreBytes: 1 << 30},
		},
		ShardSummary: types.NodeShardSummary{Primaries: 1, Replicas: 0, StoreBytes: 1 << 30},
	}
}

func TestNodeDescribe_textHasHeaderAndSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.NodeDescribe(&buf, sampleNodeDescription(), output.NodeDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("NodeDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "data-1",
		"Roles:", "master, data, ingest",
		"Master:", "true",
		"Heap:",
		"Disk:",
		"JVM & host:",
		"Bootstrap & limits:",
		"mlockall:",
		"Drain state:",
		"Allocation exclude:", "transient",
		"Shards on node:", "[p0]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("node text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestNodeDescribe_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.NodeDescribe(&buf, sampleNodeDescription(), output.NodeDescribeOptions{}, output.FormatJSON); err != nil {
		t.Fatalf("NodeDescribe: %v", err)
	}
	var got types.NodeDescription
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Info.Name != "data-1" || !got.Drain.AllocationExcluded {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

func TestNodeDescribe_wideForcesShards(t *testing.T) {
	d := sampleNodeDescription()
	d.Shards = make([]types.Shard, 50)
	for i := range d.Shards {
		d.Shards[i] = types.Shard{Index: "logs", Shard: i, Primary: i%2 == 0, State: "STARTED"}
	}
	var buf bytes.Buffer
	if err := output.NodeDescribe(&buf, d, output.NodeDescribeOptions{}, output.FormatWide); err != nil {
		t.Fatalf("NodeDescribe: %v", err)
	}
	if !strings.Contains(buf.String(), "[p0]") {
		t.Errorf("wide should force the per-shard listing; got:\n%s", buf.String())
	}
}

// ---------- DataStreamDescribe ----------

func sampleDataStreamDescription() types.DataStreamDescription {
	return types.DataStreamDescription{
		Name:    "logs",
		Dialect: "elasticsearch",
		Stream: types.DataStream{
			Name:       "logs",
			Status:     "GREEN",
			Generation: 5,
			Template:   "logs-template",
			WriteIndex: ".ds-logs-2025.05.19-000005",
			IndexCount: 5,
			ILMPolicy:  "logs-30d",
			Indices: []types.DataStreamBackingIndex{
				{Name: ".ds-logs-2025.05.13-000001", Health: "green", DocsCount: 1000, StoreBytes: 1 << 30},
				{Name: ".ds-logs-2025.05.19-000005", Health: "green", DocsCount: 500, StoreBytes: 1 << 29},
			},
			DocsCount:  1500,
			StoreBytes: 1<<30 + 1<<29,
		},
		MatchedTemplate: &types.IndexTemplate{
			Name:          "logs-template",
			IndexPatterns: []string{"logs*"},
			Priority:      100,
		},
	}
}

func TestDataStreamDescribe_textHasSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.DataStreamDescribe(&buf, sampleDataStreamDescription(), output.DataStreamDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("DataStreamDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "logs",
		"Generation:", "5",
		"Write index:",
		"Lifecycle:", "ILM policy:", "logs-30d",
		"Matched template:", "logs-template",
		"Backing indices",
		".ds-logs-2025.05.13-000001",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("datastream text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestDataStreamDescribe_yamlRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.DataStreamDescribe(&buf, sampleDataStreamDescription(), output.DataStreamDescribeOptions{}, output.FormatYAML); err != nil {
		t.Fatalf("DataStreamDescribe(yaml): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"name: logs", "generation: 5", "matched_template:"} {
		if !strings.Contains(out, want) {
			t.Errorf("yaml missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestDataStreamDescribe_noIndicesHidesListing(t *testing.T) {
	d := sampleDataStreamDescription()
	hide := false
	var buf bytes.Buffer
	if err := output.DataStreamDescribe(&buf, d, output.DataStreamDescribeOptions{ShowIndices: &hide}, output.FormatTable); err != nil {
		t.Fatalf("DataStreamDescribe: %v", err)
	}
	if strings.Contains(buf.String(), ".ds-logs-2025.05.13-000001") {
		t.Errorf("--no-indices should hide the backing-index list; got:\n%s", buf.String())
	}
}

// ---------- ILMDescribe ----------

func sampleILMDescription() types.ILMPolicyDescription {
	return types.ILMPolicyDescription{
		Name:    "logs-30d",
		Dialect: "elasticsearch",
		Policy: types.ILMPolicy{
			Name:    "logs-30d",
			Version: 3,
			InUseBy: types.ILMInUseBy{Indices: []string{"logs-2025-05", "logs-2025-04"}},
			Definition: map[string]any{
				"phases": map[string]any{"hot": map[string]any{"min_age": "0ms"}},
			},
		},
		ManagedIndices: []types.ILMExplainEntry{
			{Index: "logs-2025-05", Managed: true, Policy: "logs-30d", Phase: "warm", Action: "complete"},
			{Index: "logs-2025-04", Managed: true, Policy: "logs-30d", Phase: "warm", Step: "ERROR", FailedStep: "forcemerge"},
		},
		ManagedCount: 2,
		ErrorCount:   1,
	}
}

func TestILMDescribe_textHasSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.ILMDescribe(&buf, sampleILMDescription(), output.PolicyDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("ILMDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "logs-30d",
		"Version:", "3",
		"Managed indices:", "2",
		"Indices in ERROR:", "1",
		"In use by:",
		"Phases:", "hot:", "min_age:",
		"Per-index state:",
		"forcemerge (FAILED)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ilm text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestILMDescribe_phasesFallsBackToJSONOnExoticShape(t *testing.T) {
	d := sampleILMDescription()
	// No "phases" key — the renderer should fall back to dumping the
	// full Definition body so the section is still useful.
	d.Policy.Definition = map[string]any{"unrecognized": map[string]any{"foo": "bar"}}
	var buf bytes.Buffer
	if err := output.ILMDescribe(&buf, d, output.PolicyDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("ILMDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Policy body:") || !strings.Contains(out, "unrecognized") {
		t.Errorf("fallback JSON dump missing; got:\n%s", out)
	}
}

func TestISMDescribe_textRendersStructuredStates(t *testing.T) {
	d := types.ISMPolicyDescription{
		ID:      "logs-policy",
		Dialect: "opensearch",
		Policy: types.ISMPolicy{
			ID: "logs-policy",
			Definition: map[string]any{
				"policy": map[string]any{
					"default_state": "hot",
					"states": []any{
						map[string]any{
							"name": "hot",
							"actions": []any{
								map[string]any{"rollover": map[string]any{"min_size": "10gb"}},
								map[string]any{"set_priority": map[string]any{"priority": 100}},
							},
							"transitions": []any{
								map[string]any{"state_name": "warm", "conditions": map[string]any{"min_index_age": "7d"}},
							},
						},
					},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := output.ISMDescribe(&buf, d, output.PolicyDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("ISMDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"States (default: hot):",
		"hot:",
		"actions:", "rollover", "set_priority",
		"→ warm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ism structured states missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestILMDescribe_collapsesLargeManagedList(t *testing.T) {
	d := sampleILMDescription()
	d.ManagedIndices = make([]types.ILMExplainEntry, 100)
	for i := range d.ManagedIndices {
		d.ManagedIndices[i] = types.ILMExplainEntry{Index: "logs-x", Managed: true, Policy: "logs-30d"}
	}
	var buf bytes.Buffer
	if err := output.ILMDescribe(&buf, d, output.PolicyDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("ILMDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "100 total") {
		t.Errorf("auto-collapse should fire on >25 indices; got:\n%s", out)
	}
	if strings.Contains(out, "Per-index state:") {
		t.Errorf("per-index listing should be hidden when collapsed; got:\n%s", out)
	}
}

// ---------- ISMDescribe ----------

func TestISMDescribe_textHasSections(t *testing.T) {
	// An exotic Definition shape (no `states` array) — the renderer
	// should fall back to dumping the body as JSON so the section
	// still surfaces something.
	d := types.ISMPolicyDescription{
		ID:      "logs-policy",
		Dialect: "opensearch",
		Policy: types.ISMPolicy{
			ID:          "logs-policy",
			SeqNo:       5,
			PrimaryTerm: 1,
			Definition:  map[string]any{"unrecognized": map[string]any{"foo": "bar"}},
		},
		ManagedIndices: []types.ISMExplainEntry{
			{Index: "logs-2025-05", PolicyID: "logs-policy", State: "warm", Enabled: true},
		},
		ManagedCount: 1,
	}
	var buf bytes.Buffer
	if err := output.ISMDescribe(&buf, d, output.PolicyDescribeOptions{}, output.FormatTable); err != nil {
		t.Fatalf("ISMDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"ID:", "logs-policy",
		"Seq no:", "5",
		"Managed indices:", "1",
		"Policy body:",
		"Per-index state:", "logs-2025-05", "warm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ism text missing %q\n--- output ---\n%s", want, out)
		}
	}
}
