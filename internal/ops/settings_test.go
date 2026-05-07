package ops_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeClusterSettingsFull replays a canned ClusterSettingsFull and
// records the include_defaults flag so tests can assert it was
// forwarded.
type fakeClusterSettingsFull struct {
	full              types.ClusterSettingsFull
	err               error
	includeDefaults   bool
	includeDefaultsOK bool
}

func (f *fakeClusterSettingsFull) GetClusterSettingsFull(_ context.Context, includeDefaults bool) (types.ClusterSettingsFull, error) {
	f.includeDefaults = includeDefaults
	f.includeDefaultsOK = true
	return f.full, f.err
}

// fakeClusterSettingsPutter records every PutClusterSettings call and
// returns a canned response.
type fakeClusterSettingsPutter struct {
	calls []types.ClusterSettingsPutRequest
	resp  types.ClusterSettingsPutResult
	err   error
}

func (f *fakeClusterSettingsPutter) PutClusterSettings(_ context.Context, req types.ClusterSettingsPutRequest) (types.ClusterSettingsPutResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.ClusterSettingsPutResult{}, f.err
	}
	return f.resp, nil
}

func TestGetClusterSettings_passesIncludeDefaultsThrough(t *testing.T) {
	gi := &fakeClusterSettingsFull{
		full: types.ClusterSettingsFull{
			Persistent: map[string]any{"cluster.routing.allocation.enable": "all"},
			Transient:  map[string]any{},
			Defaults:   map[string]any{"cluster.routing.allocation.enable": "all"},
		},
	}
	view, err := ops.GetClusterSettings(context.Background(), gi, ops.SettingsGetOptions{IncludeDefaults: true})
	if err != nil {
		t.Fatalf("GetClusterSettings: %v", err)
	}
	if !gi.includeDefaultsOK || !gi.includeDefaults {
		t.Errorf("include_defaults not forwarded to adapter: ok=%v val=%v", gi.includeDefaultsOK, gi.includeDefaults)
	}
	if !view.IncludeDefaults {
		t.Errorf("view.IncludeDefaults = false, want true")
	}
	if view.Defaults == nil {
		t.Errorf("Defaults should populate when include_defaults set")
	}
}

func TestGetClusterSettings_filtersByPrefix(t *testing.T) {
	gi := &fakeClusterSettingsFull{
		full: types.ClusterSettingsFull{
			Persistent: map[string]any{
				"cluster.routing.allocation.enable":               "all",
				"cluster.routing.allocation.disk.watermark.high":  "85%",
				"action.destructive_requires_name":                "true",
			},
			Transient: map[string]any{
				"cluster.routing.allocation.enable":               "primaries",
				"action.destructive_requires_name":                "false",
			},
		},
	}
	view, err := ops.GetClusterSettings(context.Background(), gi, ops.SettingsGetOptions{
		Names: []string{"cluster.routing.allocation"},
	})
	if err != nil {
		t.Fatalf("GetClusterSettings: %v", err)
	}
	wantPersistent := map[string]any{
		"cluster.routing.allocation.enable":              "all",
		"cluster.routing.allocation.disk.watermark.high": "85%",
	}
	if !reflect.DeepEqual(view.Persistent, wantPersistent) {
		t.Errorf("Persistent = %v, want %v", view.Persistent, wantPersistent)
	}
	wantTransient := map[string]any{
		"cluster.routing.allocation.enable": "primaries",
	}
	if !reflect.DeepEqual(view.Transient, wantTransient) {
		t.Errorf("Transient = %v, want %v", view.Transient, wantTransient)
	}
}

func TestGetClusterSettings_emptyNamesPassesThrough(t *testing.T) {
	gi := &fakeClusterSettingsFull{
		full: types.ClusterSettingsFull{
			Persistent: map[string]any{"a": 1, "b": 2},
		},
	}
	view, err := ops.GetClusterSettings(context.Background(), gi, ops.SettingsGetOptions{})
	if err != nil {
		t.Fatalf("GetClusterSettings: %v", err)
	}
	if len(view.Persistent) != 2 {
		t.Errorf("empty Names should pass every key through, got %d", len(view.Persistent))
	}
}

func TestDiffClusterSettings_excludesEqualPairs(t *testing.T) {
	gi := &fakeClusterSettingsFull{
		full: types.ClusterSettingsFull{
			Persistent: map[string]any{
				"a": "same",
				"b": "diff-p",
				"c": "only-persistent",
			},
			Transient: map[string]any{
				"a": "same",
				"b": "diff-t",
				"d": "only-transient",
			},
		},
	}
	diff, err := ops.DiffClusterSettings(context.Background(), gi)
	if err != nil {
		t.Fatalf("DiffClusterSettings: %v", err)
	}
	// Expect b, c, d (not a — a is the same in both scopes).
	keys := make([]string, 0, len(diff.Rows))
	for _, r := range diff.Rows {
		keys = append(keys, r.Key)
	}
	if !reflect.DeepEqual(keys, []string{"b", "c", "d"}) {
		t.Errorf("diff keys = %v, want [b c d]", keys)
	}
	for _, r := range diff.Rows {
		switch r.Key {
		case "b":
			if r.Persistent != "diff-p" || r.Transient != "diff-t" {
				t.Errorf("row b = %+v", r)
			}
		case "c":
			if r.Persistent != "only-persistent" || r.Transient != nil {
				t.Errorf("row c = %+v (transient should be nil)", r)
			}
		case "d":
			if r.Persistent != nil || r.Transient != "only-transient" {
				t.Errorf("row d = %+v (persistent should be nil)", r)
			}
		}
	}
}

func TestUpdateClusterSettings_kvBuildsTransientByDefault(t *testing.T) {
	su := &fakeClusterSettingsPutter{resp: types.ClusterSettingsPutResult{Acknowledged: true}}
	got, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{
		KeyValues: map[string]string{"cluster.routing.allocation.enable": "all"},
	})
	if err != nil {
		t.Fatalf("UpdateClusterSettings: %v", err)
	}
	if !got.Acknowledged {
		t.Errorf("got.Acknowledged = false")
	}
	if len(su.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(su.calls))
	}
	if len(su.calls[0].Persistent) != 0 {
		t.Errorf("default scope should be transient, got persistent=%v", su.calls[0].Persistent)
	}
	if su.calls[0].Transient["cluster.routing.allocation.enable"] != "all" {
		t.Errorf("transient body wrong: %+v", su.calls[0].Transient)
	}
}

func TestUpdateClusterSettings_persistentScope(t *testing.T) {
	su := &fakeClusterSettingsPutter{resp: types.ClusterSettingsPutResult{Acknowledged: true}}
	_, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{
		Scope:     ops.ScopePersistent,
		KeyValues: map[string]string{"cluster.foo": "bar"},
	})
	if err != nil {
		t.Fatalf("UpdateClusterSettings: %v", err)
	}
	if len(su.calls[0].Transient) != 0 {
		t.Errorf("transient should be empty, got %v", su.calls[0].Transient)
	}
	if su.calls[0].Persistent["cluster.foo"] != "bar" {
		t.Errorf("persistent body wrong: %+v", su.calls[0].Persistent)
	}
}

func TestUpdateClusterSettings_dryRunDoesNotCallPutter(t *testing.T) {
	su := &fakeClusterSettingsPutter{}
	got, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{
		Scope:     ops.ScopePersistent,
		KeyValues: map[string]string{"cluster.foo": "bar"},
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("UpdateClusterSettings: %v", err)
	}
	if len(su.calls) != 0 {
		t.Errorf("dry-run must not call adapter, got %d calls", len(su.calls))
	}
	if !got.Acknowledged {
		t.Errorf("dry-run result should report Acknowledged=true, got %+v", got)
	}
	if got.Persistent["cluster.foo"] != "bar" {
		t.Errorf("dry-run should echo the body it would send: %+v", got)
	}
}

func TestUpdateClusterSettings_bodyVsKVAreMutuallyExclusive(t *testing.T) {
	su := &fakeClusterSettingsPutter{}
	_, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{
		KeyValues: map[string]string{"a": "b"},
		Body:      []byte(`{"a":"b"}`),
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if len(su.calls) != 0 {
		t.Errorf("validation failure must not contact adapter, got %d calls", len(su.calls))
	}
}

func TestUpdateClusterSettings_emptyInputRejected(t *testing.T) {
	su := &fakeClusterSettingsPutter{}
	_, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}

func TestUpdateClusterSettings_bodyWithScopeWrapperRejected(t *testing.T) {
	su := &fakeClusterSettingsPutter{}
	_, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{
		Body: []byte(`{"persistent":{"a":"b"}}`),
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
	if err == nil || !strings.Contains(err.Error(), "no surrounding") {
		t.Errorf("expected 'no surrounding' message, got: %v", err)
	}
}

func TestUpdateClusterSettings_bodyForwardsBareObject(t *testing.T) {
	su := &fakeClusterSettingsPutter{resp: types.ClusterSettingsPutResult{Acknowledged: true}}
	_, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{
		Body: []byte(`{"cluster.foo":"bar"}`),
	})
	if err != nil {
		t.Fatalf("UpdateClusterSettings: %v", err)
	}
	if su.calls[0].Transient["cluster.foo"] != "bar" {
		t.Errorf("body forwarded incorrectly: %+v", su.calls[0].Transient)
	}
}

func TestUpdateClusterSettings_unknownScopeRejected(t *testing.T) {
	su := &fakeClusterSettingsPutter{}
	_, err := ops.UpdateClusterSettings(context.Background(), su, ops.SettingsSetOptions{
		Scope:     ops.SettingsScope("default"),
		KeyValues: map[string]string{"a": "b"},
	})
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}
