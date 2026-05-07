package ops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeIndexCacheClearer records each ClearCache call and returns a
// canned response.
type fakeIndexCacheClearer struct {
	calls []types.IndexCacheClearRequest
	resp  types.IndexCacheClearResult
	err   error
}

func (f *fakeIndexCacheClearer) ClearCache(_ context.Context, req types.IndexCacheClearRequest) (types.IndexCacheClearResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.IndexCacheClearResult{}, f.err
	}
	return f.resp, nil
}

func TestCacheClear_defaultClearsAllCaches(t *testing.T) {
	cc := &fakeIndexCacheClearer{
		resp: types.IndexCacheClearResult{Total: 4, Successful: 4, Failed: 0},
	}
	got, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{Index: "logs-*"})
	if err != nil {
		t.Fatalf("CacheClear: %v", err)
	}
	if !reflect.DeepEqual(got.Caches, []string{"all"}) {
		t.Errorf("default caches = %v, want [all]", got.Caches)
	}
	// None of the per-cache flags should be forwarded as true.
	req := cc.calls[0]
	if req.Fielddata || req.Query || req.Request {
		t.Errorf("per-cache flags should be false by default, got %+v", req)
	}
}

func TestCacheClear_perCacheFlagsForwarded(t *testing.T) {
	cc := &fakeIndexCacheClearer{}
	got, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{
		Index:   "logs-*",
		Query:   true,
		Request: true,
	})
	if err != nil {
		t.Fatalf("CacheClear: %v", err)
	}
	if !reflect.DeepEqual(got.Caches, []string{"query", "request"}) {
		t.Errorf("caches = %v, want [query request]", got.Caches)
	}
	req := cc.calls[0]
	if req.Fielddata || !req.Query || !req.Request {
		t.Errorf("per-cache flags wrong: %+v", req)
	}
}

func TestCacheClear_emptyIndexRejected(t *testing.T) {
	cc := &fakeIndexCacheClearer{}
	_, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if len(cc.calls) != 0 {
		t.Errorf("validation failure must not contact adapter")
	}
}

func TestCacheClear_starRejected(t *testing.T) {
	cc := &fakeIndexCacheClearer{}
	for _, idx := range []string{"*", "_all", "  *  "} {
		_, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{Index: idx})
		if !errors.Is(err, client.ErrUsage) {
			t.Errorf("idx=%q: err = %v, want ErrUsage", idx, err)
		}
	}
}

func TestCacheClear_fieldsRequireFielddata(t *testing.T) {
	cc := &fakeIndexCacheClearer{}
	_, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{
		Index:  "logs-*",
		Fields: []string{"title"},
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}

func TestCacheClear_emptyFieldEntryRejected(t *testing.T) {
	cc := &fakeIndexCacheClearer{}
	_, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{
		Index:     "logs-*",
		Fielddata: true,
		Fields:    []string{"a", ""},
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}

func TestCacheClear_dryRunDoesNotMutate(t *testing.T) {
	cc := &fakeIndexCacheClearer{}
	got, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{
		Index:  "logs-*",
		Query:  true,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("CacheClear: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun flag not echoed in result: %+v", got)
	}
	if got.Total != 0 || got.Successful != 0 || got.Failed != 0 {
		t.Errorf("dry-run should leave shard counts zero: %+v", got)
	}
	if len(cc.calls) != 0 {
		t.Errorf("dry-run must not contact adapter, got %d calls", len(cc.calls))
	}
	if !reflect.DeepEqual(got.Caches, []string{"query"}) {
		t.Errorf("dry-run caches = %v, want [query]", got.Caches)
	}
}

func TestCacheClear_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	cc := &fakeIndexCacheClearer{err: sentinel}
	_, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{Index: "x"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestCacheClear_resultEchoesIndicesAndFields(t *testing.T) {
	cc := &fakeIndexCacheClearer{
		resp: types.IndexCacheClearResult{Total: 2, Successful: 2},
	}
	got, err := ops.CacheClear(context.Background(), cc, ops.CacheClearOptions{
		Index:     "logs-*",
		Fielddata: true,
		Fields:    []string{"title", "name"},
	})
	if err != nil {
		t.Fatalf("CacheClear: %v", err)
	}
	if !reflect.DeepEqual(got.Indices, []string{"logs-*"}) {
		t.Errorf("Indices = %v, want [logs-*]", got.Indices)
	}
	if !reflect.DeepEqual(got.Fields, []string{"title", "name"}) {
		t.Errorf("Fields = %v, want [title name]", got.Fields)
	}
	// Adapter received the same fields in the request shape.
	if !reflect.DeepEqual(cc.calls[0].Fields, []string{"title", "name"}) {
		t.Errorf("forwarded fields = %v", cc.calls[0].Fields)
	}
}
