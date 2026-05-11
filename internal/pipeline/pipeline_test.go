package pipeline_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/pipeline"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakePipelineInspector records calls and returns canned responses.
// Mirrors the fakePolicyManager pattern used in the snapshot tests so
// readers cross-checking behaviour see the same shape.
type fakePipelineInspector struct {
	listNames []string
	listOut   []types.IngestPipeline
	listErr   error

	getName string
	getOut  types.IngestPipeline
	getErr  error
}

func (f *fakePipelineInspector) ListPipelines(_ context.Context, names []string) ([]types.IngestPipeline, error) {
	f.listNames = names
	return f.listOut, f.listErr
}

func (f *fakePipelineInspector) GetPipeline(_ context.Context, name string) (types.IngestPipeline, error) {
	f.getName = name
	if f.getErr != nil {
		return types.IngestPipeline{}, f.getErr
	}
	if f.getOut.Name == "" {
		f.getOut.Name = name
	}
	return f.getOut, nil
}

func samplePipelines() []types.IngestPipeline {
	// Deliberately out of name order so the business layer's sort is
	// observable in TestList_sortsByName.
	return []types.IngestPipeline{
		{Name: "esops-products-rename", Description: "rename legacy fields", Version: 2,
			Processors: []map[string]any{{"rename": map[string]any{"field": "title"}}}},
		{Name: "esops-logs-enrich", Description: "normalise levels", Version: 1,
			Processors: []map[string]any{{"lowercase": map[string]any{"field": "level"}}}},
		{Name: "audit-tagger", Description: "tag audit events", Version: 1},
	}
}

func TestList_sortsByName(t *testing.T) {
	fake := &fakePipelineInspector{listOut: samplePipelines()}
	got, err := pipeline.List(context.Background(), fake, pipeline.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"audit-tagger", "esops-logs-enrich", "esops-products-rename"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.Name != want[i] {
			t.Errorf("got[%d].Name = %q, want %q", i, p.Name, want[i])
		}
	}
}

func TestList_passesNamesThrough(t *testing.T) {
	fake := &fakePipelineInspector{listOut: samplePipelines()}
	_, err := pipeline.List(context.Background(), fake, pipeline.ListOptions{Names: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := fake.listNames; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("listNames = %v, want [a b]", got)
	}
}

func TestList_surfacesCapabilityError(t *testing.T) {
	wantErr := errors.New("cluster on fire")
	fake := &fakePipelineInspector{listErr: wantErr}
	_, err := pipeline.List(context.Background(), fake, pipeline.ListOptions{})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestGet_emptyNameRejected(t *testing.T) {
	fake := &fakePipelineInspector{}
	_, err := pipeline.Get(context.Background(), fake, "  ")
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("err = %v, want '--name is required'", err)
	}
	if fake.getName != "" {
		t.Errorf("GetPipeline was called with %q despite empty input", fake.getName)
	}
}

func TestGet_patternRejected(t *testing.T) {
	fake := &fakePipelineInspector{}
	_, err := pipeline.Get(context.Background(), fake, "esops-*")
	if err == nil || !strings.Contains(err.Error(), "literal pipeline name") {
		t.Errorf("err = %v, want pattern-rejection", err)
	}
	if fake.getName != "" {
		t.Errorf("GetPipeline was called for a pattern — should have been rejected pre-flight (got %q)", fake.getName)
	}
}

func TestGet_passesLiteralNameThrough(t *testing.T) {
	fake := &fakePipelineInspector{
		getOut: types.IngestPipeline{Name: "esops-logs-enrich", Version: 1},
	}
	got, err := pipeline.Get(context.Background(), fake, "esops-logs-enrich")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fake.getName != "esops-logs-enrich" {
		t.Errorf("GetPipeline called with %q, want esops-logs-enrich", fake.getName)
	}
	if got.Name != "esops-logs-enrich" || got.Version != 1 {
		t.Errorf("got = %+v, want name=esops-logs-enrich version=1", got)
	}
}

func TestGet_notFoundFromCapabilitySurfaces(t *testing.T) {
	fake := &fakePipelineInspector{getErr: client.ErrNotFound}
	_, err := pipeline.Get(context.Background(), fake, "nope")
	if !errors.Is(err, client.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
