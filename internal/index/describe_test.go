package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// describeFakes wraps the per-inspector fakes the aggregator consumes.
// Each fake is defined inline so the tests can stage exactly the
// response a given scenario needs without leaking shared setup
// between tests.
type describeFakes struct {
	index     *descIndexInspector
	alias     *descAliasInspector
	settings  *descSettingsInspector
	mappings  *descMappingsInspector
	shards    *descShardInspector
	recovery  *descRecoveryInspector
	segments  *descSegmentsInspector
	ilm       *descILMManager
	ism       *descISMManager
	tasks     *descTasksInspector
	templates *descTemplatesInspector
}

func newDescribeFakes() *describeFakes {
	return &describeFakes{
		index:     &descIndexInspector{},
		alias:     &descAliasInspector{},
		settings:  &descSettingsInspector{},
		mappings:  &descMappingsInspector{},
		shards:    &descShardInspector{},
		recovery:  &descRecoveryInspector{},
		segments:  &descSegmentsInspector{},
		ilm:       &descILMManager{},
		ism:       &descISMManager{},
		tasks:     &descTasksInspector{},
		templates: &descTemplatesInspector{},
	}
}

func (f *describeFakes) caps(dialect string) index.DescribeCapabilities {
	return index.DescribeCapabilities{
		Info:      f.index,
		Aliases:   f.alias,
		Settings:  f.settings,
		Mappings:  f.mappings,
		Shards:    f.shards,
		Recovery:  f.recovery,
		Segments:  f.segments,
		ILM:       f.ilm,
		ISM:       f.ism,
		Tasks:     f.tasks,
		Templates: f.templates,
		Dialect:   dialect,
	}
}

// --- inspector fakes ---

type descIndexInspector struct {
	indices []types.Index
	err     error
	got     types.IndexFilter
}

func (f *descIndexInspector) Indices(_ context.Context, filter types.IndexFilter) ([]types.Index, error) {
	f.got = filter
	return f.indices, f.err
}
func (f *descIndexInspector) IndicesNamesStatus(_ context.Context, _ types.IndexFilter) ([]types.IndexNameStatus, error) {
	return nil, nil
}
func (f *descIndexInspector) IndicesStream(_ context.Context, _ types.IndexFilter, _ func(types.Index) error) error {
	return nil
}

// descAliasInspector returns different rows depending on the filter:
// the resolution call uses AliasFilter.Alias (empty Index); the
// collection call uses AliasFilter.Index (empty Alias).
type descAliasInspector struct {
	byAlias map[string][]types.Alias
	byIndex map[string][]types.Alias
	err     error
	calls   []types.AliasFilter
}

func (f *descAliasInspector) Aliases(_ context.Context, filter types.AliasFilter) ([]types.Alias, error) {
	f.calls = append(f.calls, filter)
	if f.err != nil {
		return nil, f.err
	}
	if filter.Alias != "" {
		return f.byAlias[filter.Alias], nil
	}
	if filter.Index != "" {
		return f.byIndex[filter.Index], nil
	}
	return nil, nil
}
func (f *descAliasInspector) AliasesStream(_ context.Context, _ types.AliasFilter, _ func(types.Alias) error) error {
	return nil
}

type descSettingsInspector struct {
	rows []types.IndexSettings
	err  error
}

func (f *descSettingsInspector) GetSettings(_ context.Context, _ types.IndexSettingsGetRequest) ([]types.IndexSettings, error) {
	return f.rows, f.err
}
func (f *descSettingsInspector) GetSettingsStream(_ context.Context, _ types.IndexSettingsGetRequest, _ func(types.IndexSettings) error) error {
	return nil
}

type descMappingsInspector struct {
	rows []types.IndexMapping
	err  error
}

func (f *descMappingsInspector) GetMappings(_ context.Context, _ types.MappingFilter) ([]types.IndexMapping, error) {
	return f.rows, f.err
}

type descShardInspector struct {
	shards []types.Shard
	err    error
}

func (f *descShardInspector) Shards(_ context.Context, _ types.ShardFilter) ([]types.Shard, error) {
	return f.shards, f.err
}

type descRecoveryInspector struct {
	report types.RecoveryReport
	err    error
}

func (f *descRecoveryInspector) Recovery(_ context.Context, _ types.RecoveryRequest) (types.RecoveryReport, error) {
	return f.report, f.err
}

type descSegmentsInspector struct {
	report types.SegmentsReport
	err    error
}

func (f *descSegmentsInspector) Segments(_ context.Context, _ types.SegmentsRequest) (types.SegmentsReport, error) {
	return f.report, f.err
}

type descILMManager struct {
	entries []types.ILMExplainEntry
	err     error
}

func (f *descILMManager) ListPolicies(_ context.Context, _ []string) ([]types.ILMPolicy, error) {
	return nil, client.ErrUnsupported
}
func (f *descILMManager) GetPolicy(_ context.Context, _ string) (types.ILMPolicy, error) {
	return types.ILMPolicy{}, client.ErrUnsupported
}
func (f *descILMManager) PutPolicy(_ context.Context, _ types.ILMPolicyPutRequest) (types.ILMPolicyPutResult, error) {
	return types.ILMPolicyPutResult{}, client.ErrUnsupported
}
func (f *descILMManager) DeletePolicy(_ context.Context, _ types.ILMPolicyDeleteRequest) (types.ILMPolicyDeleteResult, error) {
	return types.ILMPolicyDeleteResult{}, client.ErrUnsupported
}
func (f *descILMManager) ExplainPolicy(_ context.Context, _ types.ILMExplainRequest) ([]types.ILMExplainEntry, error) {
	return f.entries, f.err
}
func (f *descILMManager) ExplainPolicyStream(_ context.Context, _ types.ILMExplainRequest, _ func(types.ILMExplainEntry) error) error {
	return nil
}

type descISMManager struct {
	entries []types.ISMExplainEntry
	err     error
}

func (f *descISMManager) ListPolicies(_ context.Context, _ []string) ([]types.ISMPolicy, error) {
	return nil, client.ErrUnsupported
}
func (f *descISMManager) GetPolicy(_ context.Context, _ string) (types.ISMPolicy, error) {
	return types.ISMPolicy{}, client.ErrUnsupported
}
func (f *descISMManager) PutPolicy(_ context.Context, _ types.ISMPolicyPutRequest) (types.ISMPolicyPutResult, error) {
	return types.ISMPolicyPutResult{}, client.ErrUnsupported
}
func (f *descISMManager) DeletePolicy(_ context.Context, _ types.ISMPolicyDeleteRequest) (types.ISMPolicyDeleteResult, error) {
	return types.ISMPolicyDeleteResult{}, client.ErrUnsupported
}
func (f *descISMManager) ExplainPolicy(_ context.Context, _ types.ISMExplainRequest) ([]types.ISMExplainEntry, error) {
	return f.entries, f.err
}
func (f *descISMManager) ExplainPolicyStream(_ context.Context, _ types.ISMExplainRequest, _ func(types.ISMExplainEntry) error) error {
	return nil
}

type descTasksInspector struct {
	tasks []types.Task
	err   error
}

func (f *descTasksInspector) Tasks(_ context.Context, _ types.TasksFilter) ([]types.Task, error) {
	return f.tasks, f.err
}
func (f *descTasksInspector) TasksStream(_ context.Context, _ types.TasksFilter, _ func(types.Task) error) error {
	return nil
}

type descTemplatesInspector struct {
	templates []types.IndexTemplate
	err       error
}

func (f *descTemplatesInspector) GetTemplates(_ context.Context, _ []string) ([]types.IndexTemplate, error) {
	return f.templates, f.err
}
func (f *descTemplatesInspector) GetTemplate(_ context.Context, name string) (types.IndexTemplate, error) {
	if f.err != nil {
		return types.IndexTemplate{}, f.err
	}
	for _, t := range f.templates {
		if t.Name == name {
			return t, nil
		}
	}
	return types.IndexTemplate{}, nil
}

// --- tests ---

func TestDescribe_rejectsEmptyName(t *testing.T) {
	f := newDescribeFakes()
	_, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "  "})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage on empty name, got %v", err)
	}
}

func TestDescribe_rejectsPattern(t *testing.T) {
	f := newDescribeFakes()
	_, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs-*"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage on pattern, got %v", err)
	}
}

func TestDescribe_resolvesSingleTargetAlias(t *testing.T) {
	f := newDescribeFakes()
	f.alias.byAlias = map[string][]types.Alias{
		"logs": {{Alias: "logs", Index: "logs-2025-05"}},
	}
	f.index.indices = []types.Index{{Name: "logs-2025-05", Health: "green", Status: "open"}}

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Info.Name != "logs-2025-05" {
		t.Errorf("Info.Name = %q, want logs-2025-05", desc.Info.Name)
	}
	if desc.ResolvedFrom != "logs" {
		t.Errorf("ResolvedFrom = %q, want logs", desc.ResolvedFrom)
	}
}

func TestDescribe_resolvesWriteIndexOnMultiTargetAlias(t *testing.T) {
	f := newDescribeFakes()
	f.alias.byAlias = map[string][]types.Alias{
		"logs": {
			{Alias: "logs", Index: "logs-2025-04"},
			{Alias: "logs", Index: "logs-2025-05", IsWriteIndex: true},
		},
	}
	// The cluster expands the alias on /_cat/indices; both underlying
	// indices show up in phase 1.
	f.index.indices = []types.Index{
		{Name: "logs-2025-04", Health: "green", Status: "open"},
		{Name: "logs-2025-05", Health: "green", Status: "open"},
	}

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Info.Name != "logs-2025-05" {
		t.Errorf("Info.Name = %q, want logs-2025-05 (the write index)", desc.Info.Name)
	}
	if desc.ResolvedFrom != "logs" {
		t.Errorf("ResolvedFrom = %q, want logs", desc.ResolvedFrom)
	}
}

func TestDescribe_refusesAmbiguousAlias(t *testing.T) {
	f := newDescribeFakes()
	f.alias.byAlias = map[string][]types.Alias{
		"logs": {
			{Alias: "logs", Index: "logs-2025-04"},
			{Alias: "logs", Index: "logs-2025-05"},
		},
	}
	// Pre-stage both backing indices so phase 1 returns multi-row and
	// triggers the alias-write-index lookup.
	f.index.indices = []types.Index{
		{Name: "logs-2025-04", Health: "green", Status: "open"},
		{Name: "logs-2025-05", Health: "green", Status: "open"},
	}
	_, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage on ambiguous alias, got %v", err)
	}
	if !strings.Contains(err.Error(), "logs-2025-04") || !strings.Contains(err.Error(), "logs-2025-05") {
		t.Errorf("error should list both targets, got %v", err)
	}
}

func TestDescribe_notFound(t *testing.T) {
	f := newDescribeFakes()
	_, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "missing"})
	if err == nil || !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDescribe_closedIndexSkipsClosedHostileSections(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "archive-2023", Health: "green", Status: "close"}}
	// Wire collectors to fail loudly so a leaked call would show up.
	f.recovery.err = errors.New("should not be called on closed index")
	f.segments.err = errors.New("should not be called on closed index")
	f.mappings.err = errors.New("should not be called on closed index")

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "archive-2023"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.SectionWarnings) != 0 {
		t.Errorf("closed-index section warnings should be empty, got %v", desc.SectionWarnings)
	}
	if desc.Mappings != nil || desc.Recovery != nil || desc.Segments != nil {
		t.Errorf("closed-index sections should be nil; got mappings=%v recovery=%v segments=%v",
			desc.Mappings != nil, desc.Recovery != nil, desc.Segments != nil)
	}
}

func TestDescribe_ignoresUnsupportedOnILMandISM(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "logs-2025-05", Health: "green", Status: "open"}}
	// Simulate cross-dialect: ES adapter returns ErrUnsupported for ISM,
	// OS adapter for ILM. Both should be silent in SectionWarnings.
	f.ilm.err = client.ErrUnsupported
	f.ism.err = client.ErrUnsupported

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs-2025-05"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if _, ok := desc.SectionWarnings["ilm"]; ok {
		t.Errorf("ilm ErrUnsupported should not surface as a warning")
	}
	if _, ok := desc.SectionWarnings["ism"]; ok {
		t.Errorf("ism ErrUnsupported should not surface as a warning")
	}
}

func TestDescribe_capturesNonFatalErrorsAsWarnings(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "logs-2025-05", Health: "green", Status: "open"}}
	f.segments.err = errors.New("boom")

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs-2025-05"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got := desc.SectionWarnings["segments"]; got != "boom" {
		t.Errorf("SectionWarnings[segments] = %q, want boom", got)
	}
}

func TestDescribe_filtersTasksByIndexName(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "logs-2025-05", Health: "green", Status: "open"}}
	f.tasks.tasks = []types.Task{
		{ID: "node:1", Action: "indices:data/write/reindex", Description: "reindex from logs-2025-04 to logs-2025-05"},
		{ID: "node:2", Action: "indices:data/write/reindex", Description: "reindex from users to users-v2"},
		{ID: "node:3", Action: "indices:admin/forcemerge", Description: "Force-merge indices [logs-2025-05]"},
	}

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs-2025-05"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.Tasks) != 2 {
		t.Fatalf("want 2 tasks matching index name, got %d: %+v", len(desc.Tasks), desc.Tasks)
	}
	for _, tk := range desc.Tasks {
		if !strings.Contains(tk.Description, "logs-2025-05") {
			t.Errorf("task %s description %q does not mention the index name", tk.ID, tk.Description)
		}
	}
}

func TestDescribe_collectsAliasesPointingAtIndex(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "logs-2025-05", Health: "green", Status: "open"}}
	f.alias.byIndex = map[string][]types.Alias{
		"logs-2025-05": {
			{Alias: "logs", Index: "logs-2025-05", IsWriteIndex: true},
			{Alias: "logs-recent", Index: "logs-2025-05"},
		},
	}

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs-2025-05"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.Aliases) != 2 {
		t.Fatalf("want 2 aliases, got %d", len(desc.Aliases))
	}
}

func TestDescribe_dialectIsEchoed(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "x", Health: "green", Status: "open"}}
	desc, err := index.Describe(context.Background(), f.caps("opensearch"), index.DescribeOptions{Name: "x"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Dialect != "opensearch" {
		t.Errorf("Dialect = %q, want opensearch", desc.Dialect)
	}
}

func TestDescribe_matchesTemplate(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "logs-2025-05", Health: "green", Status: "open"}}
	f.templates.templates = []types.IndexTemplate{
		{Name: "fallback", IndexPatterns: []string{"*"}, Priority: 1},
		{Name: "logs-template", IndexPatterns: []string{"logs-*"}, Priority: 100},
		{Name: "users-template", IndexPatterns: []string{"users-*"}, Priority: 100},
	}

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs-2025-05"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.MatchedTemplate == nil {
		t.Fatalf("expected MatchedTemplate, got nil")
	}
	if desc.MatchedTemplate.Name != "logs-template" {
		t.Errorf("MatchedTemplate.Name = %q, want logs-template (highest priority match)", desc.MatchedTemplate.Name)
	}
}

func TestDescribe_noTemplateMatch(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "logs-2025-05", Health: "green", Status: "open"}}
	f.templates.templates = []types.IndexTemplate{
		{Name: "users-template", IndexPatterns: []string{"users-*"}, Priority: 100},
	}

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "logs-2025-05"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.MatchedTemplate != nil {
		t.Errorf("MatchedTemplate should be nil when no patterns match, got %+v", desc.MatchedTemplate)
	}
}

// Task description matching is word-boundary aware: "users" must not
// match a reindex of "users-v2", but should match when surrounded by
// non-index-name bytes (brackets, spaces, line ends).
func TestDescribe_taskMatchingRespectsBoundaries(t *testing.T) {
	f := newDescribeFakes()
	f.index.indices = []types.Index{{Name: "users", Health: "green", Status: "open"}}
	f.tasks.tasks = []types.Task{
		// Should match — bracketed.
		{ID: "n:1", Action: "indices:admin/forcemerge", Description: "Force-merge [users]"},
		// Should match — space-bordered.
		{ID: "n:2", Action: "indices:data/read/search", Description: "search across users right now"},
		// Should NOT match — `users` is a prefix of `users-v2`.
		{ID: "n:3", Action: "indices:data/write/reindex", Description: "reindex users-v2 to users-v3"},
		// Should NOT match — `users` is part of `cusers`.
		{ID: "n:4", Action: "indices:admin/refresh", Description: "refresh cusers"},
	}

	desc, err := index.Describe(context.Background(), f.caps("elasticsearch"), index.DescribeOptions{Name: "users"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.Tasks) != 2 {
		t.Fatalf("want 2 matched tasks, got %d: %+v", len(desc.Tasks), idsOf(desc.Tasks))
	}
	gotIDs := idsOf(desc.Tasks)
	for _, want := range []string{"n:1", "n:2"} {
		if !containsStr(gotIDs, want) {
			t.Errorf("expected task %s, got %v", want, gotIDs)
		}
	}
}

func idsOf(tasks []types.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

