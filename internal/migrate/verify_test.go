package migrate_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeCounter serves canned per-index counts. Calls record what was
// asked for so we can assert IgnoreUnavailable / index pairing.
//
// Verify's per-pair fan-out invokes Count concurrently on the same
// fake (one goroutine per source index plus one per target index), so
// gotReqs needs a mutex — without it the race detector trips on the
// slice append.
type fakeCounter struct {
	counts   map[string]int64
	failedOn string
	err      error

	mu      sync.Mutex
	gotReqs []types.CountRequest
}

func (f *fakeCounter) Count(_ context.Context, req types.CountRequest) (types.CountResult, error) {
	f.mu.Lock()
	f.gotReqs = append(f.gotReqs, req)
	f.mu.Unlock()
	if f.err != nil {
		return types.CountResult{}, f.err
	}
	if len(req.Indices) == 0 {
		return types.CountResult{}, errors.New("no indices")
	}
	name := req.Indices[0]
	if name == f.failedOn {
		return types.CountResult{Count: f.counts[name], Shards: types.CountShardsInfo{Total: 1, Successful: 0, Failed: 1}}, nil
	}
	return types.CountResult{
		Count:  f.counts[name],
		Shards: types.CountShardsInfo{Total: 1, Successful: 1},
	}, nil
}

func mkVerifyEndpoints(t *testing.T, ix []types.Index, srcCounts, tgtCounts map[string]int64) (esopsmigrate.Endpoint, esopsmigrate.Endpoint, *fakeCounter, *fakeCounter) {
	t.Helper()
	srcDC := &fakeCounter{counts: srcCounts}
	tgtDC := &fakeCounter{counts: tgtCounts}
	src := mkSrc(t,
		types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		"http://es-src:9200",
		&fakeIndices{indices: ix}, &fakeMappings{})
	src.DocCount = srcDC
	tgt := esopsmigrate.Endpoint{
		Context:  "tgt",
		URL:      "http://es-tgt:9200",
		Info:     types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		DocCount: tgtDC,
	}
	return src, tgt, srcDC, tgtDC
}

func TestVerify_AllMatchingOK(t *testing.T) {
	indices := []types.Index{{Name: "logs"}, {Name: "products"}}
	src, tgt, _, _ := mkVerifyEndpoints(t, indices,
		map[string]int64{"logs": 1000, "products": 50},
		map[string]int64{"logs": 1000, "products": 50})

	res, err := esopsmigrate.Verify(context.Background(), src, tgt, types.MigrationVerifyRequest{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusOK {
		t.Errorf("expected ok, got %q", res.Status)
	}
	for _, e := range res.Indices {
		if e.Diff != 0 {
			t.Errorf("index %q diff %d, want 0", e.SourceIndex, e.Diff)
		}
	}
}

func TestVerify_TargetSuffixDerivesPair(t *testing.T) {
	indices := []types.Index{{Name: "logs"}}
	src, tgt, _, tgtDC := mkVerifyEndpoints(t, indices,
		map[string]int64{"logs": 100},
		map[string]int64{"logs-v2": 100})

	res, err := esopsmigrate.Verify(context.Background(), src, tgt, types.MigrationVerifyRequest{
		TargetSuffix: "-v2",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusOK {
		t.Errorf("expected ok, got %q", res.Status)
	}
	if res.Indices[0].TargetIndex != "logs-v2" {
		t.Errorf("expected target=logs-v2, got %q", res.Indices[0].TargetIndex)
	}
	if len(tgtDC.gotReqs) != 1 || tgtDC.gotReqs[0].Indices[0] != "logs-v2" {
		t.Errorf("target counter not asked for the suffixed name; got %+v", tgtDC.gotReqs)
	}
}

func TestVerify_DiffBlocks(t *testing.T) {
	indices := []types.Index{{Name: "logs"}}
	src, tgt, _, _ := mkVerifyEndpoints(t, indices,
		map[string]int64{"logs": 100},
		map[string]int64{"logs": 80})

	res, err := esopsmigrate.Verify(context.Background(), src, tgt, types.MigrationVerifyRequest{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusBlock {
		t.Errorf("expected block on diff, got %q", res.Status)
	}
	if res.Indices[0].Diff != 20 {
		t.Errorf("expected diff=20, got %d", res.Indices[0].Diff)
	}
}

func TestVerify_PartialShardsWarn(t *testing.T) {
	indices := []types.Index{{Name: "logs"}}
	src, tgt, _, tgtDC := mkVerifyEndpoints(t, indices,
		map[string]int64{"logs": 100},
		map[string]int64{"logs": 100})
	tgtDC.failedOn = "logs"

	res, err := esopsmigrate.Verify(context.Background(), src, tgt, types.MigrationVerifyRequest{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusWarn {
		t.Errorf("expected warn on partial shard, got %q", res.Status)
	}
}

func TestVerify_AuthErrorPropagates(t *testing.T) {
	indices := []types.Index{{Name: "logs"}}
	src, tgt, srcDC, _ := mkVerifyEndpoints(t, indices,
		map[string]int64{"logs": 100},
		map[string]int64{"logs": 100})
	srcDC.err = client.ErrAuth

	_, err := esopsmigrate.Verify(context.Background(), src, tgt, types.MigrationVerifyRequest{})
	if !errors.Is(err, client.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

func TestVerify_TargetIgnoreUnavailable(t *testing.T) {
	indices := []types.Index{{Name: "logs"}}
	src, tgt, _, tgtDC := mkVerifyEndpoints(t, indices,
		map[string]int64{"logs": 100},
		map[string]int64{"logs": 100})

	_, err := esopsmigrate.Verify(context.Background(), src, tgt, types.MigrationVerifyRequest{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// The target call should pass IgnoreUnavailable=true so a missing
	// target on a partially-reindexed dataset doesn't 404 the verify.
	if !tgtDC.gotReqs[0].IgnoreUnavailable {
		t.Errorf("expected target Count to be called with IgnoreUnavailable=true")
	}
}
