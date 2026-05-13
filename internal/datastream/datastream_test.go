package datastream_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeDataStreamInspector records calls and returns canned responses.
// Mirrors the fake shapes used in pipeline and snapshot tests so
// readers cross-checking behaviour see the same pattern.
type fakeDataStreamInspector struct {
	listFilter types.DataStreamFilter
	listOut    []types.DataStream
	listErr    error

	getName string
	getOut  types.DataStream
	getErr  error
}

func (f *fakeDataStreamInspector) DataStreams(_ context.Context, filter types.DataStreamFilter) ([]types.DataStream, error) {
	f.listFilter = filter
	return f.listOut, f.listErr
}

func (f *fakeDataStreamInspector) GetDataStream(_ context.Context, name string) (types.DataStream, error) {
	f.getName = name
	if f.getErr != nil {
		return types.DataStream{}, f.getErr
	}
	if f.getOut.Name == "" {
		f.getOut.Name = name
	}
	return f.getOut, nil
}

type fakeDataStreamManager struct {
	rollIn  types.DataStreamRolloverRequest
	rollOut types.DataStreamRolloverResult
	rollErr error

	modifyIn  types.DataStreamModifyRequest
	modifyOut types.DataStreamModifyResult
	modifyErr error

	deleteIn  types.DataStreamDeleteRequest
	deleteOut types.DataStreamDeleteResult
	deleteErr error
}

func (f *fakeDataStreamManager) RolloverDataStream(_ context.Context, req types.DataStreamRolloverRequest) (types.DataStreamRolloverResult, error) {
	f.rollIn = req
	return f.rollOut, f.rollErr
}

func (f *fakeDataStreamManager) ModifyDataStream(_ context.Context, req types.DataStreamModifyRequest) (types.DataStreamModifyResult, error) {
	f.modifyIn = req
	return f.modifyOut, f.modifyErr
}

func (f *fakeDataStreamManager) DeleteDataStream(_ context.Context, req types.DataStreamDeleteRequest) (types.DataStreamDeleteResult, error) {
	f.deleteIn = req
	if f.deleteErr != nil {
		return types.DataStreamDeleteResult{}, f.deleteErr
	}
	out := f.deleteOut
	if len(out.Names) == 0 {
		out.Names = req.Names
	}
	return out, nil
}

func sampleStreams() []types.DataStream {
	// Deliberately out of name order so the business layer's sort is
	// observable in TestList_sortsByName.
	return []types.DataStream{
		{Name: "metrics-stream", Generation: 1, IndexCount: 1, Status: "GREEN"},
		{Name: "logs-stream", Generation: 2, IndexCount: 2, Status: "GREEN"},
		{Name: "audit-stream", Generation: 1, IndexCount: 1, Status: "GREEN"},
	}
}

func TestList_sortsByName(t *testing.T) {
	fake := &fakeDataStreamInspector{listOut: sampleStreams()}
	got, err := datastream.List(context.Background(), fake, nil, datastream.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"audit-stream", "logs-stream", "metrics-stream"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, s := range got {
		if s.Name != want[i] {
			t.Errorf("got[%d].Name = %q, want %q", i, s.Name, want[i])
		}
	}
}

func TestList_passesNamesThrough(t *testing.T) {
	fake := &fakeDataStreamInspector{listOut: sampleStreams()}
	_, err := datastream.List(context.Background(), fake, nil, datastream.ListOptions{Names: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := fake.listFilter.Names; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Names = %v, want [a b]", got)
	}
}

// fakeIndexInspector returns canned /_cat/indices rows so the
// enrichment step can be exercised without spinning a real cluster.
// Only Indices() is implemented — the other IndexInspector methods
// aren't exercised by List/Get.
type fakeIndexInspector struct {
	got types.IndexFilter
	out []types.Index
	err error
}

func (f *fakeIndexInspector) Indices(_ context.Context, filter types.IndexFilter) ([]types.Index, error) {
	f.got = filter
	return f.out, f.err
}
func (f *fakeIndexInspector) IndicesNamesStatus(_ context.Context, _ types.IndexFilter) ([]types.IndexNameStatus, error) {
	return nil, nil
}
func (f *fakeIndexInspector) IndicesStream(_ context.Context, _ types.IndexFilter, _ func(types.Index) error) error {
	return nil
}

func TestList_enrichesBackingIndexStats(t *testing.T) {
	streams := []types.DataStream{
		{
			Name:       "logs-stream",
			Generation: 2,
			Indices: []types.DataStreamBackingIndex{
				{Name: ".ds-logs-stream-000001"},
				{Name: ".ds-logs-stream-000002"},
			},
		},
	}
	ds := &fakeDataStreamInspector{listOut: streams}
	idx := &fakeIndexInspector{out: []types.Index{
		{Name: ".ds-logs-stream-000001", Health: "green", DocsCount: 100, StoreBytes: 1024},
		{Name: ".ds-logs-stream-000002", Health: "yellow", DocsCount: 50, StoreBytes: 512},
	}}
	got, err := datastream.List(context.Background(), ds, idx, datastream.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].StoreBytes != 1536 {
		t.Errorf("StoreBytes = %d, want 1536", got[0].StoreBytes)
	}
	if got[0].DocsCount != 150 {
		t.Errorf("DocsCount = %d, want 150", got[0].DocsCount)
	}
	if got[0].Indices[0].Health != "green" || got[0].Indices[1].Health != "yellow" {
		t.Errorf("per-backing Health not merged: %+v", got[0].Indices)
	}
	if !strings.Contains(idx.got.Index, ".ds-logs-stream-000001") {
		t.Errorf("enrich call did not include backing names: filter=%+v", idx.got)
	}
}

func TestList_enrichFailureIsBestEffort(t *testing.T) {
	// /_cat/indices fails — the list must still come back, with
	// zero StoreBytes/DocsCount rather than a hard error.
	streams := []types.DataStream{
		{Name: "logs-stream", Indices: []types.DataStreamBackingIndex{{Name: ".ds-logs-stream-000001"}}},
	}
	ds := &fakeDataStreamInspector{listOut: streams}
	idx := &fakeIndexInspector{err: errors.New("cat indices down")}
	got, err := datastream.List(context.Background(), ds, idx, datastream.ListOptions{})
	if err != nil {
		t.Fatalf("List should not propagate enrich errors: %v", err)
	}
	if got[0].StoreBytes != 0 || got[0].DocsCount != 0 {
		t.Errorf("expected zero stats on enrich failure, got %+v", got[0])
	}
}

func TestList_nilIndexInspectorSkipsEnrichment(t *testing.T) {
	streams := []types.DataStream{
		{Name: "logs-stream", Indices: []types.DataStreamBackingIndex{{Name: ".ds-logs-stream-000001"}}},
	}
	ds := &fakeDataStreamInspector{listOut: streams}
	_, err := datastream.List(context.Background(), ds, nil, datastream.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
}

func TestList_surfacesCapabilityError(t *testing.T) {
	wantErr := errors.New("cluster on fire")
	fake := &fakeDataStreamInspector{listErr: wantErr}
	_, err := datastream.List(context.Background(), fake, nil, datastream.ListOptions{})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestGet_emptyNameRejected(t *testing.T) {
	fake := &fakeDataStreamInspector{}
	_, err := datastream.Get(context.Background(), fake, nil, "  ")
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("err = %v, want '--name is required'", err)
	}
	if fake.getName != "" {
		t.Errorf("GetDataStream was called for empty input (got %q)", fake.getName)
	}
}

func TestGet_patternRejected(t *testing.T) {
	fake := &fakeDataStreamInspector{}
	_, err := datastream.Get(context.Background(), fake, nil, "logs-*")
	if err == nil || !strings.Contains(err.Error(), "literal data stream name") {
		t.Errorf("err = %v, want pattern-rejection", err)
	}
	if fake.getName != "" {
		t.Errorf("GetDataStream was called for a pattern — should have been rejected pre-flight (got %q)", fake.getName)
	}
}

func TestGet_notFoundSurfaces(t *testing.T) {
	fake := &fakeDataStreamInspector{getErr: client.ErrNotFound}
	_, err := datastream.Get(context.Background(), fake, nil, "nope")
	if !errors.Is(err, client.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRollover_requiresConditionsOrForce(t *testing.T) {
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Rollover(context.Background(), mgr, datastream.RolloverOptions{Name: "logs-stream"})
	if err == nil || !strings.Contains(err.Error(), "unconditional") {
		t.Errorf("err = %v, want unconditional-rollover refusal", err)
	}
	if mgr.rollIn.Name != "" {
		t.Errorf("RolloverDataStream called despite refusal: %+v", mgr.rollIn)
	}
}

func TestRollover_forceAllowsUnconditional(t *testing.T) {
	mgr := &fakeDataStreamManager{rollOut: types.DataStreamRolloverResult{DataStream: "logs-stream", RolledOver: true, Acknowledged: true}}
	_, err := datastream.Rollover(context.Background(), mgr, datastream.RolloverOptions{Name: "logs-stream", Force: true})
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	if mgr.rollIn.Name != "logs-stream" {
		t.Errorf("RolloverDataStream got Name=%q, want logs-stream", mgr.rollIn.Name)
	}
}

func TestRollover_conditionsForwarded(t *testing.T) {
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Rollover(context.Background(), mgr, datastream.RolloverOptions{
		Name: "logs-stream",
		Conditions: types.IndexRolloverConditions{
			MaxAge:  "7d",
			MaxDocs: 1000,
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	if mgr.rollIn.Conditions.MaxAge != "7d" || mgr.rollIn.Conditions.MaxDocs != 1000 {
		t.Errorf("Conditions = %+v, want MaxAge=7d MaxDocs=1000", mgr.rollIn.Conditions)
	}
	if !mgr.rollIn.DryRun {
		t.Errorf("DryRun not forwarded")
	}
}

func TestRollover_patternRejected(t *testing.T) {
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Rollover(context.Background(), mgr, datastream.RolloverOptions{Name: "logs-*", Force: true})
	if err == nil || !strings.Contains(err.Error(), "literal data stream name") {
		t.Errorf("err = %v, want pattern-rejection", err)
	}
}

func TestModify_emptyActionsRejected(t *testing.T) {
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Modify(context.Background(), mgr, datastream.ModifyOptions{Name: "logs-stream"})
	if err == nil || !strings.Contains(err.Error(), "--add-index or --remove-index") {
		t.Errorf("err = %v, want empty-actions refusal", err)
	}
}

func TestModify_buildsAddRemoveActions(t *testing.T) {
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Modify(context.Background(), mgr, datastream.ModifyOptions{
		Name:   "logs-stream",
		Add:    []string{".ds-logs-stream-001"},
		Remove: []string{".ds-logs-stream-old"},
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}
	if len(mgr.modifyIn.Actions) != 2 {
		t.Fatalf("Actions len = %d, want 2", len(mgr.modifyIn.Actions))
	}
	got := mgr.modifyIn.Actions
	if got[0].Action != types.DataStreamModifyAdd || got[0].Index != ".ds-logs-stream-001" {
		t.Errorf("Actions[0] = %+v, want add .ds-logs-stream-001", got[0])
	}
	if got[1].Action != types.DataStreamModifyRemove || got[1].Index != ".ds-logs-stream-old" {
		t.Errorf("Actions[1] = %+v, want remove .ds-logs-stream-old", got[1])
	}
	if got[0].DataStream != "logs-stream" || got[1].DataStream != "logs-stream" {
		t.Errorf("DataStream not stamped onto entries: %+v", got)
	}
}

func TestDelete_requiresAllowDataLoss(t *testing.T) {
	insp := &fakeDataStreamInspector{listOut: sampleStreams()}
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Delete(context.Background(), insp, mgr, datastream.DeleteOptions{
		Pattern: "logs-stream",
	})
	if err == nil || !strings.Contains(err.Error(), "--allow-data-loss") {
		t.Errorf("err = %v, want --allow-data-loss requirement", err)
	}
	if mgr.deleteIn.Names != nil {
		t.Errorf("DeleteDataStream called without --allow-data-loss")
	}
}

func TestDelete_dryRunSkipsDataLossCheck(t *testing.T) {
	// Dry-run should be allowed to preview the resolved list even
	// without --allow-data-loss — the audit value of "what would I
	// delete" is exactly when the operator hasn't decided yet.
	insp := &fakeDataStreamInspector{listOut: sampleStreams()}
	mgr := &fakeDataStreamManager{}
	got, err := datastream.Delete(context.Background(), insp, mgr, datastream.DeleteOptions{
		Pattern: "logs-stream",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("got %+v, want DryRun=true Acknowledged=true", got)
	}
	if mgr.deleteIn.Names != nil {
		t.Errorf("DeleteDataStream was called on dry-run: %+v", mgr.deleteIn)
	}
}

func TestDelete_bareStarRefused(t *testing.T) {
	insp := &fakeDataStreamInspector{}
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Delete(context.Background(), insp, mgr, datastream.DeleteOptions{
		Pattern:       "*",
		AllowDataLoss: true,
	})
	if !errors.Is(err, client.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
}

func TestDelete_blastRadiusCap(t *testing.T) {
	// Six streams resolved against the default cap (5) → refuse.
	rows := make([]types.DataStream, 6)
	for i := range rows {
		rows[i] = types.DataStream{Name: string(rune('a' + i))}
	}
	insp := &fakeDataStreamInspector{listOut: rows}
	mgr := &fakeDataStreamManager{}
	_, err := datastream.Delete(context.Background(), insp, mgr, datastream.DeleteOptions{
		Pattern:       "stream-*",
		AllowDataLoss: true,
	})
	if !errors.Is(err, client.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
	if mgr.deleteIn.Names != nil {
		t.Errorf("DeleteDataStream called despite blast-radius cap")
	}
}

func TestDelete_maxStreamsOverridesDefaultCap(t *testing.T) {
	rows := make([]types.DataStream, 6)
	for i := range rows {
		rows[i] = types.DataStream{Name: string(rune('a' + i))}
	}
	insp := &fakeDataStreamInspector{listOut: rows}
	mgr := &fakeDataStreamManager{deleteOut: types.DataStreamDeleteResult{Acknowledged: true}}
	got, err := datastream.Delete(context.Background(), insp, mgr, datastream.DeleteOptions{
		Pattern:       "stream-*",
		AllowDataLoss: true,
		MaxStreams:    10,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(got.Names) != 6 {
		t.Errorf("Names len = %d, want 6", len(got.Names))
	}
	// Names must be passed through to the manager — the result echo
	// preserves them when the cluster's bare ack doesn't include them.
	if len(mgr.deleteIn.Names) != 6 {
		t.Errorf("manager got %d names, want 6", len(mgr.deleteIn.Names))
	}
}

func TestDelete_emptyResolveIsNoOp(t *testing.T) {
	// Pattern matched yesterday but matches nothing today → return
	// success rather than an exit-1 failure. Scripts re-run safely.
	insp := &fakeDataStreamInspector{listOut: nil}
	mgr := &fakeDataStreamManager{}
	got, err := datastream.Delete(context.Background(), insp, mgr, datastream.DeleteOptions{
		Pattern:       "ghost-*",
		AllowDataLoss: true,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !got.Acknowledged || len(got.Names) != 0 {
		t.Errorf("got %+v, want Acknowledged=true Names empty", got)
	}
	if mgr.deleteIn.Names != nil {
		t.Errorf("DeleteDataStream called for an empty resolve")
	}
}
