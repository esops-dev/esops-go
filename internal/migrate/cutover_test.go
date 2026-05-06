package migrate_test

import (
	"context"
	"errors"
	"testing"

	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeIndexNamesStatus is a slim IndexInspector that only honours
// IndicesNamesStatus. The full-row Indices method is not exercised by
// the cutover code path.
type fakeIndexNamesStatus struct {
	rows []types.IndexNameStatus
	err  error
}

func (f *fakeIndexNamesStatus) Indices(_ context.Context, _ types.IndexFilter) ([]types.Index, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeIndexNamesStatus) IndicesNamesStatus(_ context.Context, _ types.IndexFilter) ([]types.IndexNameStatus, error) {
	return f.rows, f.err
}
func (f *fakeIndexNamesStatus) IndicesStream(_ context.Context, _ types.IndexFilter, _ func(types.Index) error) error {
	return nil
}

type fakeAliasInspect struct {
	out []types.Alias
	err error
}

func (f *fakeAliasInspect) Aliases(_ context.Context, _ types.AliasFilter) ([]types.Alias, error) {
	return f.out, f.err
}
func (f *fakeAliasInspect) AliasesStream(_ context.Context, _ types.AliasFilter, _ func(types.Alias) error) error {
	return nil
}

type fakeAliasUpdate struct {
	gotActions   []types.AliasUpdateAction
	acknowledged bool
	err          error
}

func (f *fakeAliasUpdate) UpdateAliases(_ context.Context, req types.AliasUpdateRequest) (types.AliasUpdateResult, error) {
	f.gotActions = append([]types.AliasUpdateAction(nil), req.Actions...)
	if f.err != nil {
		return types.AliasUpdateResult{}, f.err
	}
	return types.AliasUpdateResult{Acknowledged: f.acknowledged, Actions: req.Actions}, nil
}

type fakeSettingsUpdater struct {
	gotBodies [][]byte
	err       error
}

func (f *fakeSettingsUpdater) PutSettings(_ context.Context, req types.IndexSettingsUpdateRequest) (types.IndexSettingsUpdateResult, error) {
	f.gotBodies = append(f.gotBodies, append([]byte(nil), req.Body...))
	if f.err != nil {
		return types.IndexSettingsUpdateResult{}, f.err
	}
	return types.IndexSettingsUpdateResult{Acknowledged: true, Indices: req.Indices, Body: string(req.Body)}, nil
}

func mkCutoverEndpoint(toExists bool, toStatus string, currentBindings []string) (esopsmigrate.Endpoint, *fakeAliasUpdate, *fakeSettingsUpdater) {
	rows := []types.IndexNameStatus{}
	if toExists {
		rows = append(rows, types.IndexNameStatus{Name: "logs-v2", Status: toStatus})
	}
	au := &fakeAliasUpdate{acknowledged: true}
	su := &fakeSettingsUpdater{}
	bindings := make([]types.Alias, 0, len(currentBindings))
	for _, b := range currentBindings {
		bindings = append(bindings, types.Alias{Alias: "logs", Index: b})
	}
	return esopsmigrate.Endpoint{
		Context:          "tgt",
		URL:              "http://es-tgt:9200",
		Info:             types.ClusterInfo{Dialect: types.DialectElasticsearch, Version: "9.3.3"},
		Indices:          &fakeIndexNamesStatus{rows: rows},
		AliasInspect:     &fakeAliasInspect{out: bindings},
		AliasUpdate:      au,
		IndexSettingsSet: su,
	}, au, su
}

func TestCutover_HappyPath(t *testing.T) {
	ep, au, su := mkCutoverEndpoint(true, "open", []string{"logs"})

	res, err := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs-v2",
		BlockWrites: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusOK {
		t.Errorf("expected ok, got %q (%+v)", res.Status, res.Steps)
	}
	if !res.Acknowledged {
		t.Errorf("expected acknowledged")
	}
	// Two PUT settings calls (block on, block off).
	if len(su.gotBodies) != 2 {
		t.Errorf("expected 2 settings calls, got %d", len(su.gotBodies))
	}
	// One alias-swap call with two actions.
	if len(au.gotActions) != 2 {
		t.Errorf("expected 2 alias actions, got %d", len(au.gotActions))
	}
	// Verify the swap actions are remove FROM + add TO.
	if au.gotActions[0].Action != types.AliasActionRemove || au.gotActions[0].Index != "logs" {
		t.Errorf("first action: got %+v, want remove logs", au.gotActions[0])
	}
	if au.gotActions[1].Action != types.AliasActionAdd || au.gotActions[1].Index != "logs-v2" {
		t.Errorf("second action: got %+v, want add logs-v2", au.gotActions[1])
	}
}

func TestCutover_DryRunNoMutations(t *testing.T) {
	ep, au, su := mkCutoverEndpoint(true, "open", []string{"logs"})

	res, err := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs-v2",
		BlockWrites: true,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.DryRun || res.Acknowledged {
		t.Errorf("dry-run should not have acknowledged")
	}
	if len(au.gotActions) != 0 {
		t.Errorf("dry-run should not call /_aliases; got %d actions", len(au.gotActions))
	}
	if len(su.gotBodies) != 0 {
		t.Errorf("dry-run should not call /_settings; got %d bodies", len(su.gotBodies))
	}
}

func TestCutover_TargetMissingBlocks(t *testing.T) {
	ep, _, _ := mkCutoverEndpoint(false, "", []string{"logs"})

	res, err := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs-v2",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusBlock {
		t.Errorf("expected block when target is missing, got %q", res.Status)
	}
}

func TestCutover_TargetClosedBlocks(t *testing.T) {
	ep, _, _ := mkCutoverEndpoint(true, "close", []string{"logs"})

	res, _ := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs-v2",
	})
	if res.Status != types.MigrationStatusBlock {
		t.Errorf("expected block when target is closed, got %q", res.Status)
	}
}

func TestCutover_AliasNotBoundWarns(t *testing.T) {
	// Alias currently fronts only "products", not "logs". The swap
	// still proceeds (the operator may have done a manual half-swap
	// already) but the inspect step warns.
	ep, _, _ := mkCutoverEndpoint(true, "open", []string{"products"})

	res, _ := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs-v2",
	})
	if res.Status != types.MigrationStatusWarn {
		t.Errorf("expected warn on alias-not-bound, got %q (%+v)", res.Status, res.Steps)
	}
}

func TestCutover_AliasSwapFailureRollsBackBlock(t *testing.T) {
	ep, au, su := mkCutoverEndpoint(true, "open", []string{"logs"})
	au.err = errors.New("simulated cluster failure")

	res, err := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs-v2",
		BlockWrites: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != types.MigrationStatusBlock {
		t.Fatalf("expected block on swap failure, got %q", res.Status)
	}
	// Two settings calls: one to set the block, one rollback.
	if len(su.gotBodies) != 2 {
		t.Errorf("expected 2 settings calls (block + rollback), got %d", len(su.gotBodies))
	}
}

func TestCutover_PatternRejected(t *testing.T) {
	ep, _, _ := mkCutoverEndpoint(true, "open", []string{"logs"})

	_, err := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs*", FromIndex: "logs", ToIndex: "logs-v2",
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected usage error on pattern, got %v", err)
	}
}

func TestCutover_FromAndToMustDiffer(t *testing.T) {
	ep, _, _ := mkCutoverEndpoint(true, "open", []string{"logs"})

	_, err := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs",
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected usage error on identical from/to, got %v", err)
	}
}

func TestCutover_KeepWriteBlockSkipsCleanup(t *testing.T) {
	ep, _, su := mkCutoverEndpoint(true, "open", []string{"logs"})

	res, _ := esopsmigrate.Cutover(context.Background(), ep, types.MigrationCutoverRequest{
		Alias: "logs", FromIndex: "logs", ToIndex: "logs-v2",
		BlockWrites:    true,
		KeepWriteBlock: true,
	})
	if res.Status != types.MigrationStatusOK {
		t.Errorf("expected ok, got %q", res.Status)
	}
	// Only one settings call (the block-on); no cleanup.
	if len(su.gotBodies) != 1 {
		t.Errorf("expected 1 settings call with --keep-write-block, got %d", len(su.gotBodies))
	}
}
