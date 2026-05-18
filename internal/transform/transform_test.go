package transform_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/internal/transform"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeInspector records calls and returns canned responses. Covers
// both inspector and manager surfaces so the test file owns one
// stand-in for both halves of the transform API.
type fakeInspector struct {
	listNames []string
	listOut   []types.Transform
	listErr   error

	getName string
	getOut  types.Transform
	getErr  error

	statsName string
	statsOut  types.TransformStats
	statsErr  error

	listStatsNames []string
	listStatsOut   []types.TransformStats
	listStatsErr   error

	previewBody []byte
	previewOut  types.TransformPreviewResult
	previewErr  error
}

func (f *fakeInspector) ListTransforms(_ context.Context, names []string) ([]types.Transform, error) {
	f.listNames = names
	return f.listOut, f.listErr
}

func (f *fakeInspector) GetTransform(_ context.Context, name string) (types.Transform, error) {
	f.getName = name
	if f.getErr != nil {
		return types.Transform{}, f.getErr
	}
	if f.getOut.Name == "" {
		f.getOut.Name = name
	}
	return f.getOut, nil
}

func (f *fakeInspector) StatsTransform(_ context.Context, name string) (types.TransformStats, error) {
	f.statsName = name
	if f.statsErr != nil {
		return types.TransformStats{}, f.statsErr
	}
	if f.statsOut.Name == "" {
		f.statsOut.Name = name
	}
	return f.statsOut, nil
}

func (f *fakeInspector) ListTransformStats(_ context.Context, names []string) ([]types.TransformStats, error) {
	f.listStatsNames = names
	return f.listStatsOut, f.listStatsErr
}

func (f *fakeInspector) PreviewTransform(_ context.Context, req types.TransformPreviewRequest) (types.TransformPreviewResult, error) {
	f.previewBody = req.Body
	return f.previewOut, f.previewErr
}

type fakeManager struct {
	applyReq types.TransformApplyRequest
	applyOut types.TransformApplyResult
	applyErr error

	deleteCalls []types.TransformDeleteRequest
	deleteOut   types.TransformDeleteResult
	deleteErr   error

	startReq types.TransformStartRequest
	startOut types.TransformStartResult
	startErr error

	stopReq types.TransformStopRequest
	stopOut types.TransformStopResult
	stopErr error
}

func (f *fakeManager) ApplyTransform(_ context.Context, req types.TransformApplyRequest) (types.TransformApplyResult, error) {
	f.applyReq = req
	if f.applyErr != nil {
		return types.TransformApplyResult{}, f.applyErr
	}
	if f.applyOut.Name == "" {
		f.applyOut.Name = req.Name
	}
	if f.applyOut.Action == "" {
		f.applyOut.Action = "transform-apply"
	}
	return f.applyOut, nil
}

func (f *fakeManager) DeleteTransform(_ context.Context, req types.TransformDeleteRequest) (types.TransformDeleteResult, error) {
	f.deleteCalls = append(f.deleteCalls, req)
	if f.deleteErr != nil {
		return types.TransformDeleteResult{}, f.deleteErr
	}
	r := f.deleteOut
	if r.Name == "" {
		r.Name = req.Name
	}
	if r.Action == "" {
		r.Action = "transform-delete"
	}
	r.Stopped = req.ForceStop || r.Stopped
	return r, nil
}

func (f *fakeManager) StartTransform(_ context.Context, req types.TransformStartRequest) (types.TransformStartResult, error) {
	f.startReq = req
	if f.startErr != nil {
		return types.TransformStartResult{}, f.startErr
	}
	if f.startOut.Name == "" {
		f.startOut.Name = req.Name
	}
	return f.startOut, nil
}

func (f *fakeManager) StopTransform(_ context.Context, req types.TransformStopRequest) (types.TransformStopResult, error) {
	f.stopReq = req
	if f.stopErr != nil {
		return types.TransformStopResult{}, f.stopErr
	}
	if f.stopOut.Name == "" {
		f.stopOut.Name = req.Name
	}
	f.stopOut.WaitForCheckpoint = req.WaitForCheckpoint || f.stopOut.WaitForCheckpoint
	return f.stopOut, nil
}

func samples() []types.Transform {
	return []types.Transform{
		{Name: "users-pivot", Source: "users-*", Dest: "users-pivot", Continuous: true},
		{Name: "audit-rollup", Source: "audit-*", Dest: "audit-rollup", Continuous: false},
		{Name: "errors-pivot", Source: "errors-*", Dest: "errors-pivot", Continuous: true},
	}
}

func TestList_sortsByName(t *testing.T) {
	fake := &fakeInspector{listOut: samples()}
	got, err := transform.List(context.Background(), fake, transform.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"audit-rollup", "errors-pivot", "users-pivot"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, r := range got {
		if r.Name != want[i] {
			t.Errorf("got[%d].Name = %q, want %q", i, r.Name, want[i])
		}
	}
}

func TestList_passesNamesThrough(t *testing.T) {
	fake := &fakeInspector{listOut: samples()}
	_, err := transform.List(context.Background(), fake, transform.ListOptions{Names: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := fake.listNames; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("listNames = %v, want [a b]", got)
	}
}

func TestList_joinsStatsOntoRows(t *testing.T) {
	transform.SetNowFn(func() time.Time {
		return time.UnixMilli(2_000_000).UTC()
	})
	defer transform.ResetNowFn()

	fake := &fakeInspector{
		listOut: samples(),
		listStatsOut: []types.TransformStats{
			{Name: "users-pivot", State: "started", DocsProcessed: 1000, CheckpointLastTimeMillis: 1_500_000, IndexFailures: 2, SearchFailures: 1},
			{Name: "audit-rollup", State: "stopped", DocsProcessed: 50},
			// errors-pivot intentionally has no stats — should leave fields zero.
		},
	}
	got, err := transform.List(context.Background(), fake, transform.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// got[0] is audit-rollup (alphabetical)
	if got[0].Name != "audit-rollup" || got[0].State != "stopped" || got[0].DocsProcessed != 50 {
		t.Errorf("audit-rollup join wrong: %+v", got[0])
	}
	// errors-pivot has no stats entry — should stay zero.
	if got[1].Name != "errors-pivot" || got[1].State != "" || got[1].DocsProcessed != 0 {
		t.Errorf("errors-pivot should have empty stats, got %+v", got[1])
	}
	// users-pivot should have all fields including derived checkpoint age.
	u := got[2]
	if u.Name != "users-pivot" || u.State != "started" || u.DocsProcessed != 1000 {
		t.Errorf("users-pivot join wrong: %+v", u)
	}
	if u.Failures != 3 { // 2 index + 1 search
		t.Errorf("users-pivot Failures = %d, want 3", u.Failures)
	}
	if u.CheckpointAgeMillis != 500_000 {
		t.Errorf("users-pivot CheckpointAgeMillis = %d, want 500_000", u.CheckpointAgeMillis)
	}
}

func TestList_skipsStatsWhenAsked(t *testing.T) {
	fake := &fakeInspector{listOut: samples()}
	_, err := transform.List(context.Background(), fake, transform.ListOptions{SkipStats: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if fake.listStatsNames != nil {
		t.Errorf("ListTransformStats was called despite SkipStats=true (names=%v)", fake.listStatsNames)
	}
}

func TestList_statsErrorIsNonFatal(t *testing.T) {
	// Stats failure must not blow up the whole list — operators need
	// to see what exists even when /_stats is temporarily 5xx-ing.
	fake := &fakeInspector{
		listOut:      samples(),
		listStatsErr: errors.New("stats blew up"),
	}
	got, err := transform.List(context.Background(), fake, transform.ListOptions{})
	if err != nil {
		t.Fatalf("List returned err = %v; should be non-fatal", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3 (stats failure should not drop rows)", len(got))
	}
	for _, r := range got {
		if r.State != "" || r.DocsProcessed != 0 {
			t.Errorf("row %s carried stats despite failure: %+v", r.Name, r)
		}
	}
}

func TestGet_rejectsEmptyAndPattern(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "  ", "--name is required"},
		{"pattern", "users-*", "literal transform id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeInspector{}
			_, err := transform.Get(context.Background(), fake, c.in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want substring %q", err, c.want)
			}
			if fake.getName != "" {
				t.Errorf("GetTransform was called with %q despite invalid input", fake.getName)
			}
		})
	}
}

func TestStats_derivesCheckpointAge(t *testing.T) {
	// Freeze the clock at a known point so the derived age is exact.
	transform.SetNowFn(func() time.Time {
		return time.UnixMilli(2_000_000).UTC()
	})
	defer transform.ResetNowFn()

	fake := &fakeInspector{
		statsOut: types.TransformStats{
			Name:                     "audit-rollup",
			State:                    "started",
			CheckpointLastTimeMillis: 1_500_000,
		},
	}
	got, err := transform.Stats(context.Background(), fake, "audit-rollup")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got.CheckpointAgeMillis != 500_000 {
		t.Errorf("CheckpointAgeMillis = %d, want 500_000", got.CheckpointAgeMillis)
	}
}

func TestStats_noCheckpointLeavesAgeZero(t *testing.T) {
	fake := &fakeInspector{
		statsOut: types.TransformStats{Name: "fresh", State: "stopped"},
	}
	got, err := transform.Stats(context.Background(), fake, "fresh")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got.CheckpointAgeMillis != 0 {
		t.Errorf("CheckpointAgeMillis = %d, want 0 when no checkpoint reported", got.CheckpointAgeMillis)
	}
}

func TestPreview_rejectsEmptyBody(t *testing.T) {
	fake := &fakeInspector{}
	_, err := transform.Preview(context.Background(), fake, transform.PreviewOptions{Body: []byte("   \n")})
	if err == nil || !strings.Contains(err.Error(), "transform body is required") {
		t.Errorf("err = %v, want body-required usage error", err)
	}
	if fake.previewBody != nil {
		t.Errorf("PreviewTransform was called with %q despite empty input", fake.previewBody)
	}
}

func TestApply_createsWhenMissing(t *testing.T) {
	inspector := &fakeInspector{getErr: client.ErrNotFound}
	manager := &fakeManager{applyOut: types.TransformApplyResult{Acknowledged: true}}
	body := []byte(`{"description":"audit pivot"}`)

	got, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name: "audit-rollup",
		Body: body,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !got.Created || got.Updated || got.Unchanged {
		t.Errorf("flags = (created=%t,updated=%t,unchanged=%t), want created", got.Created, got.Updated, got.Unchanged)
	}
	if manager.applyReq.Update {
		t.Errorf("manager called with Update=true; should be false for create")
	}
}

func TestApply_updatesWhenChanged(t *testing.T) {
	existing := types.Transform{
		Name:       "audit-rollup",
		Definition: map[string]any{"description": "audit pivot"},
	}
	inspector := &fakeInspector{getOut: existing}
	manager := &fakeManager{applyOut: types.TransformApplyResult{Acknowledged: true}}

	body := []byte(`{"description":"audit pivot (updated)"}`)
	got, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name: "audit-rollup",
		Body: body,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !got.Updated || got.Created || got.Unchanged {
		t.Errorf("flags = (created=%t,updated=%t,unchanged=%t), want updated", got.Created, got.Updated, got.Unchanged)
	}
	if !manager.applyReq.Update {
		t.Errorf("manager called with Update=false; should be true for update path")
	}
}

func TestApply_unchangedShortCircuits(t *testing.T) {
	// Body and Definition contain the same logical map; the helper
	// re-marshals both and compares bytes, so key-order differences
	// in the submitted body must still resolve to "unchanged".
	def := map[string]any{
		"description":  "audit pivot",
		"source_index": "audit-*",
		"target_index": "audit-rollup",
	}
	body, _ := json.Marshal(def)

	inspector := &fakeInspector{getOut: types.Transform{Name: "audit-rollup", Definition: def}}
	manager := &fakeManager{}

	got, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name: "audit-rollup",
		Body: body,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !got.Unchanged {
		t.Errorf("Unchanged = false, want true")
	}
	if manager.applyReq.Name != "" {
		t.Errorf("manager was called despite unchanged body (req=%+v)", manager.applyReq)
	}
}

func TestApply_dryRunDoesNotCallManager(t *testing.T) {
	inspector := &fakeInspector{getErr: client.ErrNotFound}
	manager := &fakeManager{}
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name:   "audit-rollup",
		Body:   []byte(`{"description":"x"}`),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if manager.applyReq.Name != "" {
		t.Errorf("manager was called on dry-run (req=%+v)", manager.applyReq)
	}
}

func TestApply_rejectsInvalidJSON(t *testing.T) {
	inspector := &fakeInspector{}
	manager := &fakeManager{}
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name: "audit-rollup",
		Body: []byte(`not-json`),
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}

func TestApply_confirmOverwriteRunsOnUpdate(t *testing.T) {
	inspector := &fakeInspector{getOut: types.Transform{Name: "audit-rollup", Definition: map[string]any{"description": "old"}}}
	manager := &fakeManager{}
	called := false
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name: "audit-rollup",
		Body: []byte(`{"description":"new"}`),
		ConfirmOverwrite: func() error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !called {
		t.Errorf("ConfirmOverwrite was not called on update path")
	}
}

func TestApply_confirmOverwriteSkippedOnCreate(t *testing.T) {
	inspector := &fakeInspector{getErr: client.ErrNotFound}
	manager := &fakeManager{}
	called := false
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name: "audit-rollup",
		Body: []byte(`{"description":"new"}`),
		ConfirmOverwrite: func() error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if called {
		t.Errorf("ConfirmOverwrite was called on create path; should only fire for updates")
	}
}

func TestApply_confirmOverwriteErrorAborts(t *testing.T) {
	inspector := &fakeInspector{getOut: types.Transform{Name: "audit-rollup", Definition: map[string]any{"description": "old"}}}
	manager := &fakeManager{}
	wantErr := errors.New("protection tier refuses")
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name:             "audit-rollup",
		Body:             []byte(`{"description":"new"}`),
		ConfirmOverwrite: func() error { return wantErr },
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if manager.applyReq.Name != "" {
		t.Errorf("manager was called despite ConfirmOverwrite failure")
	}
}

func TestApply_rejectsOSBodyAgainstESCluster(t *testing.T) {
	inspector := &fakeInspector{}
	manager := &fakeManager{}
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name:    "audit-rollup",
		Body:    []byte(`{"transform":{"source_index":"x","target_index":"y","groups":[]}}`),
		Dialect: types.DialectElasticsearch,
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "OpenSearch-shaped") {
		t.Errorf("err = %q, want hint about OpenSearch-shaped body", err.Error())
	}
}

func TestApply_rejectsESBodyAgainstOSCluster(t *testing.T) {
	inspector := &fakeInspector{}
	manager := &fakeManager{}
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name:    "audit-rollup",
		Body:    []byte(`{"source":{"index":["x"]},"dest":{"index":"y"},"pivot":{}}`),
		Dialect: types.DialectOpenSearch,
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "Elasticsearch-shaped") {
		t.Errorf("err = %q, want hint about Elasticsearch-shaped body", err.Error())
	}
}

func TestApply_emptyDialectSkipsShapeCheck(t *testing.T) {
	// Tests pass Dialect="" so the body-shape check is opt-in via
	// the CLI layer. Apply should not reject an ambiguous body in
	// that case.
	inspector := &fakeInspector{getErr: client.ErrNotFound}
	manager := &fakeManager{}
	_, err := transform.Apply(context.Background(), inspector, manager, transform.ApplyOptions{
		Name: "audit-rollup",
		Body: []byte(`{"source":{"index":["x"]},"dest":{"index":"y"}}`),
	})
	if err != nil {
		t.Errorf("err = %v, want nil when Dialect is empty", err)
	}
}

func TestDelete_blastRadius(t *testing.T) {
	rows := make([]types.Transform, 0, 12)
	for i := 0; i < 12; i++ {
		rows = append(rows, types.Transform{Name: string(rune('a'+i)) + "-pivot"})
	}
	inspector := &fakeInspector{listOut: rows}
	manager := &fakeManager{}
	_, err := transform.Delete(context.Background(), inspector, manager, transform.DeleteOptions{
		Pattern: "*-pivot",
	})
	if err == nil || !errors.Is(err, client.ErrBlastRadius) {
		t.Errorf("err = %v, want ErrBlastRadius", err)
	}
	if len(manager.deleteCalls) != 0 {
		t.Errorf("manager was called %d times; want 0 over the cap", len(manager.deleteCalls))
	}
}

func TestDelete_rejectsBareStar(t *testing.T) {
	inspector := &fakeInspector{}
	manager := &fakeManager{}
	for _, p := range []string{"*", "_all"} {
		_, err := transform.Delete(context.Background(), inspector, manager, transform.DeleteOptions{Pattern: p})
		if !errors.Is(err, client.ErrBlastRadius) {
			t.Errorf("pattern %q: err = %v, want ErrBlastRadius", p, err)
		}
	}
}

func TestDelete_emptyResolveIsNoOp(t *testing.T) {
	inspector := &fakeInspector{listOut: nil}
	manager := &fakeManager{}
	got, err := transform.Delete(context.Background(), inspector, manager, transform.DeleteOptions{Pattern: "missing-*"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("rows = %d, want 0", len(got))
	}
	if len(manager.deleteCalls) != 0 {
		t.Errorf("manager called %d times; want 0 on empty resolve", len(manager.deleteCalls))
	}
}

func TestDelete_literalPathSkipsListRoundTrip(t *testing.T) {
	inspector := &fakeInspector{} // listOut is nil; would blow up if List was called.
	manager := &fakeManager{}
	got, err := transform.Delete(context.Background(), inspector, manager, transform.DeleteOptions{
		Pattern: "audit-rollup,users-pivot",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("rows = %d, want 2", len(got))
	}
	if len(manager.deleteCalls) != 2 {
		t.Errorf("manager called %d times, want 2", len(manager.deleteCalls))
	}
}

func TestDelete_patternResolveCallsList(t *testing.T) {
	inspector := &fakeInspector{listOut: samples()}
	manager := &fakeManager{}
	got, err := transform.Delete(context.Background(), inspector, manager, transform.DeleteOptions{
		Pattern: "*-pivot",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	wantCount := 2 // users-pivot and errors-pivot
	if len(got) != wantCount {
		t.Errorf("rows = %d, want %d", len(got), wantCount)
	}
}

func TestDelete_dryRunDoesNotCallManager(t *testing.T) {
	inspector := &fakeInspector{listOut: samples()}
	manager := &fakeManager{}
	got, err := transform.Delete(context.Background(), inspector, manager, transform.DeleteOptions{
		Pattern: "*-pivot",
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("rows = %d, want 2", len(got))
	}
	if len(manager.deleteCalls) != 0 {
		t.Errorf("manager called %d times on dry-run", len(manager.deleteCalls))
	}
	for _, r := range got {
		if !r.DryRun {
			t.Errorf("row %s had DryRun=false", r.Name)
		}
	}
}

func TestDelete_forceStopThreaded(t *testing.T) {
	inspector := &fakeInspector{}
	manager := &fakeManager{}
	_, err := transform.Delete(context.Background(), inspector, manager, transform.DeleteOptions{
		Pattern:   "audit-rollup",
		ForceStop: true,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(manager.deleteCalls) != 1 || !manager.deleteCalls[0].ForceStop {
		t.Errorf("ForceStop not threaded; calls = %+v", manager.deleteCalls)
	}
}

func TestStart_dryRun(t *testing.T) {
	manager := &fakeManager{}
	got, err := transform.Start(context.Background(), manager, transform.StartOptions{Name: "x", DryRun: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !got.DryRun {
		t.Errorf("DryRun=false on dry-run path")
	}
	if manager.startReq.Name != "" {
		t.Errorf("manager called on dry-run (req=%+v)", manager.startReq)
	}
}

func TestStop_passesWaitForCheckpoint(t *testing.T) {
	manager := &fakeManager{}
	_, err := transform.Stop(context.Background(), manager, transform.StopOptions{
		Name:              "x",
		WaitForCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !manager.stopReq.WaitForCheckpoint {
		t.Errorf("WaitForCheckpoint not threaded; req = %+v", manager.stopReq)
	}
}

func TestStop_rejectsPatterns(t *testing.T) {
	manager := &fakeManager{}
	_, err := transform.Stop(context.Background(), manager, transform.StopOptions{Name: "users-*"})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}
