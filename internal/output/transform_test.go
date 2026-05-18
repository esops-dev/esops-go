package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func sampleTransforms() []types.Transform {
	return []types.Transform{
		{
			Name:        "users-pivot",
			Description: "users pivot",
			Source:      "users-*",
			Dest:        "users-pivot",
			Continuous:  true,
			Version:     "9.0.0",
		},
		{
			Name:       "audit-rollup",
			Source:     "audit-*",
			Dest:       "audit-rollup",
			Continuous: false,
		},
	}
}

func TestTransforms_tableHasExpectedColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Transforms(&buf, sampleTransforms(), output.FormatTable); err != nil {
		t.Fatalf("Transforms: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "STATE", "CONTINUOUS", "SOURCE", "DEST", "DOCS_PROCESSED", "users-pivot", "audit-rollup"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestTransforms_wideIncludesCheckpointAndFailures(t *testing.T) {
	rows := []types.Transform{
		{
			Name: "users-pivot", Source: "users-*", Dest: "users-pivot", Continuous: true,
			State: "started", DocsProcessed: 1000, Failures: 3, CheckpointAgeMillis: 500_000,
		},
	}
	var buf bytes.Buffer
	if err := output.Transforms(&buf, rows, output.FormatWide); err != nil {
		t.Fatalf("Transforms wide: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"CHECKPOINT_AGE", "FAILURES", "started"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide output missing %q:\n%s", want, out)
		}
	}
}

func TestTransforms_ndjsonOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Transforms(&buf, sampleTransforms(), output.FormatNDJSON); err != nil {
		t.Fatalf("Transforms ndjson: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
}

func TestTransform_detailIncludesDefinition(t *testing.T) {
	tr := types.Transform{
		Name:       "users-pivot",
		Source:     "users-*",
		Dest:       "users-pivot",
		Continuous: true,
		Definition: map[string]any{
			"source": map[string]any{"index": []string{"users-*"}},
			"dest":   map[string]any{"index": "users-pivot"},
		},
	}
	var buf bytes.Buffer
	if err := output.Transform(&buf, tr, output.FormatTable); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "users-pivot", "DEFINITION"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestTransformStats_table(t *testing.T) {
	s := types.TransformStats{
		Name:          "users-pivot",
		State:         "started",
		DocsProcessed: 1000,
		DocsIndexed:   950,
	}
	var buf bytes.Buffer
	if err := output.TransformStats(&buf, s, output.FormatTable); err != nil {
		t.Fatalf("TransformStats: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "STATE", "started", "DOCS_PROCESSED", "1000"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestTransformApply_jsonShape(t *testing.T) {
	r := types.TransformApplyResult{
		Action:       "transform-apply",
		Name:         "users-pivot",
		Created:      true,
		Acknowledged: true,
	}
	var buf bytes.Buffer
	if err := output.TransformApply(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("TransformApply: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if got["name"] != "users-pivot" {
		t.Errorf("name = %v, want users-pivot", got["name"])
	}
	if got["created"] != true {
		t.Errorf("created = %v, want true", got["created"])
	}
}

func TestTransformDeletes_tableHasOneRowPerDelete(t *testing.T) {
	rows := []types.TransformDeleteResult{
		{Action: "transform-delete", Name: "a", Acknowledged: true},
		{Action: "transform-delete", Name: "b", Stopped: true, Acknowledged: true},
	}
	var buf bytes.Buffer
	if err := output.TransformDeletes(&buf, rows, output.FormatTable); err != nil {
		t.Fatalf("TransformDeletes: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"a", "b", "STOPPED"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestTransformPreview_tableShowsSampleCount(t *testing.T) {
	r := types.TransformPreviewResult{
		Documents: []map[string]any{
			{"k": "v1"},
			{"k": "v2"},
		},
	}
	var buf bytes.Buffer
	if err := output.TransformPreview(&buf, r, output.FormatTable); err != nil {
		t.Fatalf("TransformPreview: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "DOCUMENTS: 2") {
		t.Errorf("table missing 'DOCUMENTS: 2':\n%s", out)
	}
	if !strings.Contains(out, "sample, not exhaustive") {
		t.Errorf("preview should advertise non-exhaustive sample:\n%s", out)
	}
}
