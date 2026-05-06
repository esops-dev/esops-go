package mapping_test

import (
	"testing"

	"github.com/esops-dev/esops-go/internal/migrate/mapping"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestAnalyze_NoIssuesOnUnknownTypes(t *testing.T) {
	m := types.IndexMapping{
		Name: "esops-products",
		Properties: map[string]any{
			"sku":   map[string]any{"type": "keyword"},
			"price": map[string]any{"type": "scaled_float"},
		},
	}
	issues := mapping.Analyze(types.DialectElasticsearch, types.DialectOpenSearch, m)
	if len(issues) != 0 {
		t.Fatalf("expected zero issues for benign types, got %+v", issues)
	}
}

func TestAnalyze_FlattenedToFlatObjectWarns(t *testing.T) {
	m := types.IndexMapping{
		Name: "logs",
		Properties: map[string]any{
			"meta": map[string]any{"type": "flattened"},
		},
	}
	issues := mapping.Analyze(types.DialectElasticsearch, types.DialectOpenSearch, m)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %+v", issues)
	}
	got := issues[0]
	if got.Field != "meta" {
		t.Errorf("field: got %q, want %q", got.Field, "meta")
	}
	if got.FromType != "flattened" || got.ToType != "flat_object" {
		t.Errorf("type mapping: got %q→%q, want %q→%q", got.FromType, got.ToType, "flattened", "flat_object")
	}
	if got.Severity != types.MigrationStatusWarn {
		t.Errorf("severity: got %q, want %q", got.Severity, types.MigrationStatusWarn)
	}
}

func TestAnalyze_LegacyStringBlocks(t *testing.T) {
	m := types.IndexMapping{
		Name: "ancient",
		Properties: map[string]any{
			"name": map[string]any{"type": "string"},
		},
	}
	for _, pair := range []struct {
		from, to types.Dialect
	}{
		{types.DialectElasticsearch, types.DialectElasticsearch},
		{types.DialectElasticsearch, types.DialectOpenSearch},
		{types.DialectOpenSearch, types.DialectElasticsearch},
		{types.DialectOpenSearch, types.DialectOpenSearch},
	} {
		issues := mapping.Analyze(pair.from, pair.to, m)
		if len(issues) != 1 || issues[0].Severity != types.MigrationStatusBlock {
			t.Errorf("%s→%s: legacy 'string' should block, got %+v", pair.from, pair.to, issues)
		}
	}
}

func TestAnalyze_RankFeaturesBlocksOnOpenSearch(t *testing.T) {
	m := types.IndexMapping{
		Name: "search",
		Properties: map[string]any{
			"score": map[string]any{"type": "rank_features"},
		},
	}
	issues := mapping.Analyze(types.DialectElasticsearch, types.DialectOpenSearch, m)
	if len(issues) != 1 || issues[0].Severity != types.MigrationStatusBlock {
		t.Fatalf("rank_features → OS should block, got %+v", issues)
	}
	// Same-dialect ES → ES leaves the field untouched.
	issues = mapping.Analyze(types.DialectElasticsearch, types.DialectElasticsearch, m)
	if len(issues) != 0 {
		t.Fatalf("rank_features ES→ES should be ok, got %+v", issues)
	}
}

func TestAnalyze_RecursesIntoObjectsAndMultiFields(t *testing.T) {
	m := types.IndexMapping{
		Name: "users",
		Properties: map[string]any{
			"profile": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"category": map[string]any{"type": "constant_keyword"},
					"name": map[string]any{
						"type": "text",
						"fields": map[string]any{
							"raw": map[string]any{"type": "version"},
						},
					},
				},
			},
		},
	}
	issues := mapping.Analyze(types.DialectElasticsearch, types.DialectOpenSearch, m)
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues (constant_keyword + version), got %+v", issues)
	}
	// Sorted by field path.
	if issues[0].Field != "profile.category" {
		t.Errorf("first issue: got %q, want profile.category", issues[0].Field)
	}
	if issues[1].Field != "profile.name.raw" {
		t.Errorf("second issue (multi-field): got %q, want profile.name.raw", issues[1].Field)
	}
}

func TestWorstSeverity(t *testing.T) {
	cases := []struct {
		name   string
		issues []types.MappingIssue
		want   string
	}{
		{"empty", nil, types.MigrationStatusOK},
		{"warn-only", []types.MappingIssue{{Severity: types.MigrationStatusWarn}}, types.MigrationStatusWarn},
		{"warn-plus-ok", []types.MappingIssue{
			{Severity: types.MigrationStatusOK},
			{Severity: types.MigrationStatusWarn},
		}, types.MigrationStatusWarn},
		{"block-dominates", []types.MappingIssue{
			{Severity: types.MigrationStatusWarn},
			{Severity: types.MigrationStatusBlock},
		}, types.MigrationStatusBlock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapping.WorstSeverity(tc.issues); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
