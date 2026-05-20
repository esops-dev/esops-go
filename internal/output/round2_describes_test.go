package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ---------- SnapshotDescribe ----------

func sampleSnapshotDescription() types.SnapshotDescription {
	return types.SnapshotDescription{
		Repository: "backup",
		Name:       "daily-2025-05",
		Dialect:    "elasticsearch",
		Info: types.Snapshot{
			Repository:         "backup",
			Name:               "daily-2025-05",
			UUID:               "abc-123",
			State:              "SUCCESS",
			Indices:            []string{"logs-2025-05", "users"},
			IncludeGlobalState: true,
			StartTimeMillis:    1_700_000_000_000,
			DurationMillis:     180_000,
			ShardsTotal:        12,
			ShardsSuccessful:   12,
			ShardsFailed:       0,
			Version:            "9.0.2",
		},
		Status: &types.SnapshotStatus{
			Repository:    "backup",
			Snapshot:      "daily-2025-05",
			State:         "SUCCESS",
			ShardsTotal:   12,
			ShardsDone:    12,
			ShardsStarted: 0,
			ShardsFailed:  0,
		},
	}
}

func TestSnapshotDescribe_textHasSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotDescribe(&buf, sampleSnapshotDescription(), output.FormatTable); err != nil {
		t.Fatalf("SnapshotDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Repository:", "backup",
		"Name:", "daily-2025-05",
		"UUID:", "abc-123",
		"State:", "SUCCESS",
		"Shards:", "12 total",
		"Indices:", "logs-2025-05", "users",
		"Status (live):", "Shards done:", "12",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestSnapshotDescribe_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotDescribe(&buf, sampleSnapshotDescription(), output.FormatJSON); err != nil {
		t.Fatalf("SnapshotDescribe: %v", err)
	}
	var got types.SnapshotDescription
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Name != "daily-2025-05" || got.Status == nil {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

// Default-mode collapses the per-index listing when it exceeds the
// auto-threshold; wide forces the full list. Operators reach for
// --output wide on cluster-wide snapshots that legitimately cover
// hundreds of indices.
func TestSnapshotDescribe_indicesAutoCollapse(t *testing.T) {
	d := sampleSnapshotDescription()
	d.Info.Indices = make([]string, output.DefaultSnapshotDescribeIndexListing+5)
	for i := range d.Info.Indices {
		d.Info.Indices[i] = "logs-" + string(rune('a'+i%26))
	}
	var buf bytes.Buffer
	if err := output.SnapshotDescribe(&buf, d, output.FormatTable); err != nil {
		t.Fatalf("SnapshotDescribe: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "total (pass --output wide") {
		t.Errorf("default mode should collapse the index listing; got:\n%s", out)
	}
}

func TestSnapshotDescribe_wideForcesIndices(t *testing.T) {
	d := sampleSnapshotDescription()
	d.Info.Indices = make([]string, output.DefaultSnapshotDescribeIndexListing+5)
	for i := range d.Info.Indices {
		d.Info.Indices[i] = "logs-x"
	}
	var buf bytes.Buffer
	if err := output.SnapshotDescribe(&buf, d, output.FormatWide); err != nil {
		t.Fatalf("SnapshotDescribe(wide): %v", err)
	}
	if strings.Contains(buf.String(), "total (pass --output wide") {
		t.Errorf("wide should suppress the collapse summary; got:\n%s", buf.String())
	}
}

// ---------- SnapshotPolicyDescribe ----------

func samplePolicyDescription() types.SnapshotPolicyDescription {
	return types.SnapshotPolicyDescription{
		Name:    "daily",
		Dialect: "elasticsearch",
		Policy: types.SnapshotPolicy{
			Name:       "daily",
			Repository: "backup",
			Schedule:   "0 0 * * *",
			Enabled:    true,
			Definition: map[string]any{"name": "<daily-{now/d}>"},
		},
		RecentSnapshots: []types.Snapshot{
			{Name: "daily-2025-05-19", State: "SUCCESS", StartTimeMillis: 1_700_000_000_000, DurationMillis: 60_000},
		},
		RecentLimit: 5,
	}
}

func TestSnapshotPolicyDescribe_textHasSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotPolicyDescribe(&buf, samplePolicyDescription(), output.FormatTable); err != nil {
		t.Fatalf("SnapshotPolicyDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "daily",
		"Repository:", "backup",
		"Schedule:", "0 0 * * *",
		"Enabled:", "true",
		"Recent snapshots in repository", "(limit 5)",
		"daily-2025-05-19", "SUCCESS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("policy text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestSnapshotPolicyDescribe_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotPolicyDescribe(&buf, samplePolicyDescription(), output.FormatJSON); err != nil {
		t.Fatalf("SnapshotPolicyDescribe: %v", err)
	}
	var got types.SnapshotPolicyDescription
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Policy.Repository != "backup" || len(got.RecentSnapshots) != 1 {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

func TestSnapshotPolicyDescribe_wideRejected(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotPolicyDescribe(&buf, samplePolicyDescription(), output.FormatWide); err == nil {
		t.Errorf("wide should be rejected by the renderer; got no error")
	}
}

// ---------- SnapshotRepositoryDescribe ----------

func sampleRepoDescription() types.SnapshotRepositoryDescription {
	return types.SnapshotRepositoryDescription{
		Name:    "backup",
		Dialect: "elasticsearch",
		Info: types.SnapshotRepository{
			Name:     "backup",
			Type:     "s3",
			Settings: map[string]any{"bucket": "my-bucket", "base_path": "es-backups"},
		},
		Summary: types.SnapshotRepositorySummary{
			SnapshotCount: 4, SuccessCount: 2, FailedCount: 1, InProgressCount: 1,
		},
		Verify: &types.SnapshotVerifyResult{
			VerifiedNodes: []string{"data-1", "data-2"},
		},
		Policies: []types.SnapshotPolicy{
			{Name: "daily", Repository: "backup", Schedule: "0 0 * * *", Enabled: true},
		},
	}
}

func TestSnapshotRepositoryDescribe_textHasSections(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotRepositoryDescribe(&buf, sampleRepoDescription(), output.FormatTable); err != nil {
		t.Fatalf("SnapshotRepositoryDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "backup",
		"Type:", "s3",
		"Snapshots:", "4 total",
		"success: 2", "failed: 1", "in_progress: 1",
		"Settings:", "bucket:", "my-bucket",
		"Verify:", "Verified nodes:", "data-1, data-2",
		"Policies writing to this repository:", "daily",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("repo text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// "other:" only renders when OtherCount > 0 — verify it appears on a
// repo with ABORTED/MISSING snapshots and is absent otherwise (the
// happy-path test above already covers absence).
func TestSnapshotRepositoryDescribe_otherCountRendered(t *testing.T) {
	d := sampleRepoDescription()
	d.Summary.OtherCount = 2
	d.Summary.SnapshotCount = 6
	var buf bytes.Buffer
	if err := output.SnapshotRepositoryDescribe(&buf, d, output.FormatTable); err != nil {
		t.Fatalf("SnapshotRepositoryDescribe: %v", err)
	}
	if !strings.Contains(buf.String(), "other: 2") {
		t.Errorf("expected `other: 2` in header; got:\n%s", buf.String())
	}
}

func TestSnapshotRepositoryDescribe_jsonRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotRepositoryDescribe(&buf, sampleRepoDescription(), output.FormatJSON); err != nil {
		t.Fatalf("SnapshotRepositoryDescribe: %v", err)
	}
	var got types.SnapshotRepositoryDescription
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Info.Type != "s3" || got.Summary.SnapshotCount != 4 || got.Verify == nil {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

func TestSnapshotRepositoryDescribe_wideRejected(t *testing.T) {
	var buf bytes.Buffer
	if err := output.SnapshotRepositoryDescribe(&buf, sampleRepoDescription(), output.FormatWide); err == nil {
		t.Errorf("wide should be rejected by the renderer; got no error")
	}
}

// ---------- TransformDescribe ----------

func TestTransformDescribe_textHasSections(t *testing.T) {
	d := types.TransformDescription{
		Name:    "agg-1",
		Dialect: "elasticsearch",
		Info: types.Transform{
			Name:        "agg-1",
			Description: "rolls up logs into hourly aggregates",
			Source:      "logs",
			Dest:        "logs-agg",
			Continuous:  true,
			Definition:  map[string]any{"pivot": map[string]any{"group_by": map[string]any{"host": map[string]any{"terms": map[string]any{"field": "host.keyword"}}}}},
		},
		Stats: &types.TransformStats{
			Name:                     "agg-1",
			State:                    "started",
			DocsProcessed:            10000,
			DocsIndexed:              10000,
			SearchTimeMillis:         5000,
			IndexTimeMillis:          3000,
			CheckpointLastTimeMillis: 1_700_000_000_000,
			CheckpointAgeMillis:      60_000,
		},
	}
	var buf bytes.Buffer
	if err := output.TransformDescribe(&buf, d, output.FormatTable); err != nil {
		t.Fatalf("TransformDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "agg-1",
		"Description:", "rolls up logs",
		"Source:", "logs",
		"Dest:", "logs-agg",
		"Continuous:", "true",
		"Runtime:", "State:", "started",
		"Docs processed:", "10000",
		"Last checkpoint:",
		"Definition:", "pivot",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("transform text missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestTransformDescribe_jsonRoundTrips(t *testing.T) {
	d := types.TransformDescription{
		Name: "agg-1",
		Info: types.Transform{Name: "agg-1", Source: "logs"},
		Stats: &types.TransformStats{Name: "agg-1", State: "started"},
	}
	var buf bytes.Buffer
	if err := output.TransformDescribe(&buf, d, output.FormatJSON); err != nil {
		t.Fatalf("TransformDescribe: %v", err)
	}
	var got types.TransformDescription
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Info.Source != "logs" || got.Stats == nil || got.Stats.State != "started" {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}

func TestTransformDescribe_wideRejected(t *testing.T) {
	d := types.TransformDescription{Name: "agg-1", Info: types.Transform{Name: "agg-1"}}
	var buf bytes.Buffer
	if err := output.TransformDescribe(&buf, d, output.FormatWide); err == nil {
		t.Errorf("wide should be rejected by the renderer; got no error")
	}
}
