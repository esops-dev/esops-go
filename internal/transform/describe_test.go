package transform_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/transform"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

type descTransformInspector struct {
	def      types.Transform
	defErr   error
	stats    types.TransformStats
	statsErr error
}

func (f *descTransformInspector) ListTransforms(_ context.Context, _ []string) ([]types.Transform, error) {
	return []types.Transform{f.def}, nil
}
func (f *descTransformInspector) GetTransform(_ context.Context, _ string) (types.Transform, error) {
	return f.def, f.defErr
}
func (f *descTransformInspector) StatsTransform(_ context.Context, _ string) (types.TransformStats, error) {
	return f.stats, f.statsErr
}
func (f *descTransformInspector) ListTransformStats(_ context.Context, _ []string) ([]types.TransformStats, error) {
	return []types.TransformStats{f.stats}, nil
}
func (f *descTransformInspector) PreviewTransform(_ context.Context, _ types.TransformPreviewRequest) (types.TransformPreviewResult, error) {
	return types.TransformPreviewResult{}, client.ErrUnsupported
}

func TestTransformDescribe_rejectsEmpty(t *testing.T) {
	_, err := transform.Describe(context.Background(), transform.DescribeCapabilities{
		Transform: &descTransformInspector{},
	}, transform.DescribeOptions{Name: ""})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestTransformDescribe_rejectsPattern(t *testing.T) {
	_, err := transform.Describe(context.Background(), transform.DescribeCapabilities{
		Transform: &descTransformInspector{},
	}, transform.DescribeOptions{Name: "logs-*"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestTransformDescribe_aggregatesDefinitionAndStats(t *testing.T) {
	caps := transform.DescribeCapabilities{
		Transform: &descTransformInspector{
			def:   types.Transform{Name: "agg-1", Source: "logs", Dest: "logs-agg", Continuous: true},
			stats: types.TransformStats{Name: "agg-1", State: "started", DocsProcessed: 10000},
		},
		Dialect: "elasticsearch",
	}
	desc, err := transform.Describe(context.Background(), caps, transform.DescribeOptions{Name: "agg-1"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Info.Source != "logs" || desc.Info.Dest != "logs-agg" {
		t.Errorf("info wrong: %+v", desc.Info)
	}
	if desc.Stats == nil || desc.Stats.State != "started" || desc.Stats.DocsProcessed != 10000 {
		t.Errorf("stats wrong: %+v", desc.Stats)
	}
}

func TestTransformDescribe_statsNotFoundIsNotAWarning(t *testing.T) {
	// A transform that's never been started has no stats — the
	// cluster returns ErrNotFound on /_stats. Describe should swallow
	// it silently rather than surfacing a warning.
	caps := transform.DescribeCapabilities{
		Transform: &descTransformInspector{
			def:      types.Transform{Name: "new"},
			statsErr: client.ErrNotFound,
		},
	}
	desc, err := transform.Describe(context.Background(), caps, transform.DescribeOptions{Name: "new"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.SectionWarnings != nil {
		t.Errorf("stats 404 should be silent, got warnings %v", desc.SectionWarnings)
	}
	if desc.Stats != nil {
		t.Errorf("Stats should be nil when cluster reports not found, got %+v", desc.Stats)
	}
}

func TestTransformDescribe_statsErrorIsAWarning(t *testing.T) {
	caps := transform.DescribeCapabilities{
		Transform: &descTransformInspector{
			def:      types.Transform{Name: "agg-1"},
			statsErr: errors.New("boom"),
		},
	}
	desc, err := transform.Describe(context.Background(), caps, transform.DescribeOptions{Name: "agg-1"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.SectionWarnings["stats"] != "boom" {
		t.Errorf("SectionWarnings[stats] = %q, want boom", desc.SectionWarnings["stats"])
	}
}

// ErrUnsupported from GetTransform must propagate intact so the
// operator sees a dialect or capability gap with the right exit code.
func TestTransformDescribe_propagatesErrUnsupported(t *testing.T) {
	caps := transform.DescribeCapabilities{
		Transform: &descTransformInspector{defErr: client.ErrUnsupported},
	}
	_, err := transform.Describe(context.Background(), caps, transform.DescribeOptions{Name: "agg-1"})
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}
