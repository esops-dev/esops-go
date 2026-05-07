package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeIndexSettingsUpdater records the request the business layer
// passed in.
type fakeIndexSettingsUpdater struct {
	calls []types.IndexSettingsUpdateRequest
	resp  types.IndexSettingsUpdateResult
	err   error
}

func (f *fakeIndexSettingsUpdater) PutSettings(_ context.Context, req types.IndexSettingsUpdateRequest) (types.IndexSettingsUpdateResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.IndexSettingsUpdateResult{}, f.err
	}
	r := f.resp
	if r.Action == "" {
		r.Action = "settings-update"
	}
	if r.Indices == nil {
		r.Indices = req.Indices
	}
	return r, nil
}

func TestUnblockIndices_defaultBlock(t *testing.T) {
	su := &fakeIndexSettingsUpdater{resp: types.IndexSettingsUpdateResult{Acknowledged: true}}
	got, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{Index: "logs-*"})
	if err != nil {
		t.Fatalf("UnblockIndices: %v", err)
	}
	if got.Block != "read_only_allow_delete" {
		t.Errorf("default block should be read_only_allow_delete, got %q", got.Block)
	}
	if !got.Acknowledged {
		t.Errorf("got.Acknowledged = false")
	}
	if !reflect.DeepEqual(got.Indices, []string{"logs-*"}) {
		t.Errorf("Indices = %v, want [logs-*]", got.Indices)
	}
	// Verify the body shape sent to the adapter is the JSON-null
	// removal — not an empty string, which the cluster would honour
	// as "block disabled" but keep visible in /_settings forever.
	var body map[string]any
	if err := json.Unmarshal(su.calls[0].Body, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	idx, _ := body["index"].(map[string]any)
	blocks, _ := idx["blocks"].(map[string]any)
	if v, ok := blocks["read_only_allow_delete"]; !ok || v != nil {
		t.Errorf("expected JSON null at index.blocks.read_only_allow_delete, got: %v (present=%v)", v, ok)
	}
}

func TestUnblockIndices_alternateBlockName(t *testing.T) {
	su := &fakeIndexSettingsUpdater{resp: types.IndexSettingsUpdateResult{Acknowledged: true}}
	got, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{
		Index: "archive-2023",
		Block: "read_only",
	})
	if err != nil {
		t.Fatalf("UnblockIndices: %v", err)
	}
	if got.Block != "read_only" {
		t.Errorf("Block = %q, want read_only", got.Block)
	}
}

func TestUnblockIndices_unknownBlockRejected(t *testing.T) {
	su := &fakeIndexSettingsUpdater{}
	_, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{
		Index: "x",
		Block: "spurious",
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if len(su.calls) != 0 {
		t.Errorf("validation failure must not contact adapter, got %d calls", len(su.calls))
	}
}

func TestUnblockIndices_emptyIndexRejected(t *testing.T) {
	su := &fakeIndexSettingsUpdater{}
	_, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}

func TestUnblockIndices_starRejected(t *testing.T) {
	su := &fakeIndexSettingsUpdater{}
	for _, idx := range []string{"*", "_all", " * "} {
		_, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{Index: idx})
		if !errors.Is(err, client.ErrUsage) {
			t.Errorf("idx=%q: err = %v, want ErrUsage", idx, err)
		}
	}
}

func TestUnblockIndices_dryRunDoesNotMutate(t *testing.T) {
	su := &fakeIndexSettingsUpdater{}
	got, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{
		Index:  "logs-*",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("UnblockIndices: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun flag not echoed in result: %+v", got)
	}
	if !got.Acknowledged {
		t.Errorf("dry-run should report Acknowledged=true (no work to fail)")
	}
	if len(su.calls) != 0 {
		t.Errorf("dry-run must not contact adapter, got %d calls", len(su.calls))
	}
}

func TestUnblockIndices_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	su := &fakeIndexSettingsUpdater{err: sentinel}
	_, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{Index: "x"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestUnblockIndices_forwardsIgnoreUnavailable(t *testing.T) {
	su := &fakeIndexSettingsUpdater{resp: types.IndexSettingsUpdateResult{Acknowledged: true}}
	_, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{
		Index:             "x",
		IgnoreUnavailable: true,
	})
	if err != nil {
		t.Fatalf("UnblockIndices: %v", err)
	}
	if !su.calls[0].IgnoreUnavailable {
		t.Errorf("ignore_unavailable not forwarded to adapter")
	}
}

func TestUnblockIndices_splitsCommaSeparatedIndices(t *testing.T) {
	su := &fakeIndexSettingsUpdater{resp: types.IndexSettingsUpdateResult{Acknowledged: true}}
	_, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{
		Index: "a,b,c",
	})
	if err != nil {
		t.Fatalf("UnblockIndices: %v", err)
	}
	got := su.calls[0].Indices
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("CSV split wrong: %v", got)
	}
}

func TestUnblockIndices_validationErrorMessageMentionsBlockNames(t *testing.T) {
	su := &fakeIndexSettingsUpdater{}
	_, err := ops.UnblockIndices(context.Background(), su, ops.UnblockOptions{
		Index: "x",
		Block: "spurious",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{"read_only_allow_delete", "read_only", "write", "metadata", "read"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error message missing %q: %v", name, err)
		}
	}
}
