package migrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeIndices is the IndexInspector subset migrate.Plan exercises.
// Only the Indices method matters here; the other two methods are
// stubbed for the interface contract.
type fakeIndices struct {
	indices   []types.Index
	err       error
	gotFilter types.IndexFilter
}

func (f *fakeIndices) Indices(_ context.Context, filter types.IndexFilter) ([]types.Index, error) {
	f.gotFilter = filter
	return f.indices, f.err
}

func (f *fakeIndices) IndicesNamesStatus(_ context.Context, _ types.IndexFilter) ([]types.IndexNameStatus, error) {
	return nil, nil
}

func (f *fakeIndices) IndicesStream(_ context.Context, _ types.IndexFilter, _ func(types.Index) error) error {
	return nil
}

type fakeMappings struct {
	out []types.IndexMapping
	err error
}

func (f *fakeMappings) GetMappings(_ context.Context, _ types.MappingFilter) ([]types.IndexMapping, error) {
	return f.out, f.err
}

type fakeWhitelist struct {
	out types.RemoteReindexWhitelist
	err error
}

func (f *fakeWhitelist) RemoteReindexWhitelist(_ context.Context) (types.RemoteReindexWhitelist, error) {
	return f.out, f.err
}

func mkSrc(t *testing.T, info types.ClusterInfo, url string, ix *fakeIndices, mp *fakeMappings) esopsmigrate.Endpoint {
	t.Helper()
	return esopsmigrate.Endpoint{
		Context:  "src",
		URL:      url,
		Info:     info,
		Indices:  ix,
		Mappings: mp,
	}
}

func mkTgt(info types.ClusterInfo, url string, wl *fakeWhitelist) esopsmigrate.Endpoint {
	return esopsmigrate.Endpoint{
		Context:   "tgt",
		URL:       url,
		Info:      info,
		Whitelist: wl,
	}
}

func TestCheck_AllOK(t *testing.T) {
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200", nil, nil)
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200",
		&fakeWhitelist{out: types.RemoteReindexWhitelist{Persistent: []string{"es-src:9200"}}})

	res, err := esopsmigrate.Check(context.Background(), src, tgt, types.MigrationCheckRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != types.MigrationStatusOK {
		t.Errorf("status: got %q, want ok; checks=%+v", res.Status, res.Checks)
	}
	if res.Blockers != 0 || res.Warnings != 0 {
		t.Errorf("expected no blockers/warnings, got blockers=%d warnings=%d", res.Blockers, res.Warnings)
	}
}

func TestCheck_MissingWhitelistBlocks(t *testing.T) {
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200", nil, nil)
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200",
		&fakeWhitelist{out: types.RemoteReindexWhitelist{}}) // empty whitelist

	res, err := esopsmigrate.Check(context.Background(), src, tgt, types.MigrationCheckRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != types.MigrationStatusBlock {
		t.Fatalf("expected block, got %q", res.Status)
	}
	// The blocker should mention the source host:port so an operator can
	// copy-paste it into the target's settings.
	var found bool
	for _, c := range res.Checks {
		if c.Name == "target.remote_whitelist" && c.Status == types.MigrationStatusBlock &&
			strings.Contains(c.Detail, "es-src:9200") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a remote_whitelist block mentioning es-src:9200; got %+v", res.Checks)
	}
}

func TestCheck_CrossDialectWarn(t *testing.T) {
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200", nil, nil)
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectOpenSearch, Version: "3.6.0"},
		"http://os-tgt:9200",
		&fakeWhitelist{out: types.RemoteReindexWhitelist{Persistent: []string{"es-src:9200"}}})

	res, _ := esopsmigrate.Check(context.Background(), src, tgt, types.MigrationCheckRequest{})
	if res.Status != types.MigrationStatusWarn {
		t.Fatalf("expected warn, got %q (checks=%+v)", res.Status, res.Checks)
	}
}

func TestCheck_AuthErrorPropagates(t *testing.T) {
	// ErrAuth on the whitelist read should bubble up so the CLI maps
	// to exit 4 — not collapsed into a "warn" row.
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200", nil, nil)
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200",
		&fakeWhitelist{err: client.ErrAuth})

	_, err := esopsmigrate.Check(context.Background(), src, tgt, types.MigrationCheckRequest{})
	if !errors.Is(err, client.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

func TestCheck_VersionBridgeBlocksOnSkippedMajor(t *testing.T) {
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "7.17.0"},
		"http://es-src:9200", nil, nil)
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200",
		&fakeWhitelist{out: types.RemoteReindexWhitelist{Persistent: []string{"es-src:9200"}}})

	res, _ := esopsmigrate.Check(context.Background(), src, tgt, types.MigrationCheckRequest{})
	if res.Status != types.MigrationStatusBlock {
		t.Fatalf("ES7→ES9 should block, got %q (checks=%+v)", res.Status, res.Checks)
	}
}

func TestCheck_WildcardWhitelistMatch(t *testing.T) {
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es01.internal:9200", nil, nil)
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200",
		&fakeWhitelist{out: types.RemoteReindexWhitelist{Persistent: []string{"*.internal:9200"}}})

	res, _ := esopsmigrate.Check(context.Background(), src, tgt, types.MigrationCheckRequest{})
	if res.Status != types.MigrationStatusOK {
		t.Errorf("wildcard whitelist match should be ok, got %q (checks=%+v)", res.Status, res.Checks)
	}
}

func TestPlan_DurationAndIssueAggregation(t *testing.T) {
	indices := []types.Index{
		{Name: "products", DocsCount: 50_000, PrimaryStoreBytes: 2 << 20},
		{Name: "logs", DocsCount: 1_000_000, PrimaryStoreBytes: 100 << 20},
	}
	mappings := []types.IndexMapping{
		{Name: "products", Properties: map[string]any{
			"category": map[string]any{"type": "constant_keyword"}, // warn (ES→OS)
		}},
		{Name: "logs", Properties: map[string]any{
			"score": map[string]any{"type": "rank_features"}, // block (ES→OS)
		}},
	}
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200",
		&fakeIndices{indices: indices},
		&fakeMappings{out: mappings})
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectOpenSearch, Version: "3.6.0"},
		"http://os-tgt:9200", nil)

	plan, err := esopsmigrate.Plan(context.Background(), src, tgt, types.MigrationPlanRequest{
		ThroughputDocsPerSec: 10_000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Status != types.MigrationStatusBlock {
		t.Errorf("expected status=block, got %q", plan.Status)
	}
	if plan.Blockers != 1 || plan.Warnings != 1 {
		t.Errorf("expected blockers=1 warnings=1, got blockers=%d warnings=%d", plan.Blockers, plan.Warnings)
	}
	if plan.Totals.Indices != 2 || plan.Totals.DocsCount != 1_050_000 {
		t.Errorf("totals wrong: %+v", plan.Totals)
	}
	// 1_050_000 docs at 10_000 docs/sec = 105 seconds.
	if plan.Totals.EstimatedSeconds != 105 {
		t.Errorf("estimated seconds: got %v, want 105", plan.Totals.EstimatedSeconds)
	}
	// The plan is sorted by index name — products before logs alphabetically.
	if plan.Indices[0].Index != "logs" || plan.Indices[1].Index != "products" {
		t.Errorf("plan order: got [%s, %s], want [logs, products]",
			plan.Indices[0].Index, plan.Indices[1].Index)
	}
}

func TestPlan_DefaultsThroughputWhenZero(t *testing.T) {
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200",
		&fakeIndices{},
		&fakeMappings{})
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200", nil)

	plan, err := esopsmigrate.Plan(context.Background(), src, tgt, types.MigrationPlanRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.ThroughputDPS != esopsmigrate.DefaultThroughputDocsPerSec {
		t.Errorf("throughput default: got %d, want %d", plan.ThroughputDPS, esopsmigrate.DefaultThroughputDocsPerSec)
	}
}

func TestPlan_NoSourceIndicesReturnsEmptyPlan(t *testing.T) {
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200",
		&fakeIndices{},
		&fakeMappings{})
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200", nil)

	plan, err := esopsmigrate.Plan(context.Background(), src, tgt, types.MigrationPlanRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Indices) != 0 || plan.Status != types.MigrationStatusOK {
		t.Errorf("expected empty plan with status=ok, got %+v", plan)
	}
}

func TestPlan_FilterForwardedToCluster(t *testing.T) {
	ix := &fakeIndices{indices: []types.Index{{Name: "users", DocsCount: 1}}}
	mp := &fakeMappings{out: []types.IndexMapping{{Name: "users", Properties: map[string]any{}}}}
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200", ix, mp)
	tgt := mkTgt(
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-tgt:9200", nil)

	_, err := esopsmigrate.Plan(context.Background(), src, tgt, types.MigrationPlanRequest{
		Indices: "users",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ix.gotFilter.Index != "users" {
		t.Errorf("expected filter Index=users to forward, got %q", ix.gotFilter.Index)
	}
	// Status filter should narrow to open indices.
	if ix.gotFilter.Status != "open" {
		t.Errorf("expected status=open, got %q", ix.gotFilter.Status)
	}
}
