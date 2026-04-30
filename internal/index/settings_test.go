package index_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeSettingsInspector records the request the business layer made
// and returns canned rows.
type fakeSettingsInspector struct {
	got  types.IndexSettingsGetRequest
	rows []types.IndexSettings
	err  error
}

func (f *fakeSettingsInspector) GetSettings(_ context.Context, req types.IndexSettingsGetRequest) ([]types.IndexSettings, error) {
	f.got = req
	return f.rows, f.err
}

// fakeSettingsUpdater records every PutSettings call and returns a
// canned response.
type fakeSettingsUpdater struct {
	calls []types.IndexSettingsUpdateRequest
	resp  types.IndexSettingsUpdateResult
	err   error
}

func (f *fakeSettingsUpdater) PutSettings(_ context.Context, req types.IndexSettingsUpdateRequest) (types.IndexSettingsUpdateResult, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return types.IndexSettingsUpdateResult{}, f.err
	}
	r := f.resp
	if r.Action == "" {
		r.Action = "settings-update"
	}
	return r, nil
}

func TestGetSettings_requiresIndex(t *testing.T) {
	gi := &fakeSettingsInspector{}
	_, err := index.GetSettings(context.Background(), gi, index.SettingsGetOptions{})
	if err == nil || !strings.Contains(err.Error(), "--index") {
		t.Errorf("expected error mentioning --index, got: %v", err)
	}
	if gi.got.Index != "" {
		t.Errorf("cluster contacted on validation failure: %+v", gi.got)
	}
}

func TestGetSettings_passesFilterThrough(t *testing.T) {
	gi := &fakeSettingsInspector{rows: []types.IndexSettings{
		{Index: "logs-2024-12"},
		{Index: "logs-2024-11"},
	}}
	out, err := index.GetSettings(context.Background(), gi, index.SettingsGetOptions{
		Index:           "logs-*",
		Names:           []string{"index.number_of_replicas"},
		IncludeDefaults: true,
		FlatSettings:    true,
	})
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if gi.got.Index != "logs-*" {
		t.Errorf("Index forwarded = %q, want logs-*", gi.got.Index)
	}
	if len(gi.got.Names) != 1 || gi.got.Names[0] != "index.number_of_replicas" {
		t.Errorf("Names forwarded = %#v", gi.got.Names)
	}
	if !gi.got.IncludeDefaults || !gi.got.FlatSettings {
		t.Errorf("flags forwarded incorrectly: %+v", gi.got)
	}
	// Sorted client-side regardless of cluster order.
	if len(out) != 2 || out[0].Index != "logs-2024-11" || out[1].Index != "logs-2024-12" {
		t.Errorf("sort order: %+v", out)
	}
}

func TestGetSettings_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	gi := &fakeSettingsInspector{err: sentinel}
	_, err := index.GetSettings(context.Background(), gi, index.SettingsGetOptions{Index: "logs-*"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestGetSettings_rejectsEmptyName(t *testing.T) {
	gi := &fakeSettingsInspector{}
	_, err := index.GetSettings(context.Background(), gi, index.SettingsGetOptions{
		Index: "logs",
		Names: []string{""},
	})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected error mentioning --name, got: %v", err)
	}
}

func TestUpdateSettings_requiresIndex(t *testing.T) {
	su := &fakeSettingsUpdater{}
	_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
		KeyValues: map[string]string{"number_of_replicas": "2"},
	})
	if err == nil || !strings.Contains(err.Error(), "--index") {
		t.Errorf("expected error mentioning --index, got: %v", err)
	}
	if len(su.calls) != 0 {
		t.Errorf("cluster contacted on validation failure: %+v", su.calls)
	}
}

func TestUpdateSettings_rejectsAllIndexShortcut(t *testing.T) {
	cases := []string{"*", "_all"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			su := &fakeSettingsUpdater{}
			_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
				Index:     name,
				KeyValues: map[string]string{"number_of_replicas": "2"},
			})
			if err == nil || !strings.Contains(err.Error(), "too broad") {
				t.Errorf("expected too-broad rejection for %q, got: %v", name, err)
			}
		})
	}
}

func TestUpdateSettings_requiresOneOfBodyOrKV(t *testing.T) {
	su := &fakeSettingsUpdater{}
	_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{Index: "logs"})
	if err == nil || !strings.Contains(err.Error(), "--set") {
		t.Errorf("expected error mentioning --set/--body, got: %v", err)
	}
}

func TestUpdateSettings_refusesBothBodyAndKV(t *testing.T) {
	su := &fakeSettingsUpdater{}
	_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
		Index:     "logs",
		Body:      []byte(`{"index":{"number_of_replicas":"2"}}`),
		KeyValues: map[string]string{"number_of_replicas": "2"},
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got: %v", err)
	}
}

func TestUpdateSettings_rejectsNonObjectBody(t *testing.T) {
	su := &fakeSettingsUpdater{}
	_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
		Index: "logs",
		Body:  []byte(`["nope"]`),
	})
	if err == nil || !strings.Contains(err.Error(), "object") {
		t.Errorf("expected object-shape rejection, got: %v", err)
	}
}

func TestUpdateSettings_buildsEnvelopeFromKVs(t *testing.T) {
	su := &fakeSettingsUpdater{resp: types.IndexSettingsUpdateResult{Acknowledged: true}}
	_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
		Index: "logs",
		KeyValues: map[string]string{
			"number_of_replicas": "2",
			"index.refresh_interval": "30s",
		},
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if len(su.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(su.calls))
	}
	var got map[string]any
	if err := json.Unmarshal(su.calls[0].Body, &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	idx, ok := got["index"].(map[string]any)
	if !ok {
		t.Fatalf("body missing 'index' object: %#v", got)
	}
	if idx["number_of_replicas"] != "2" {
		t.Errorf("number_of_replicas = %v, want \"2\"", idx["number_of_replicas"])
	}
	// "index." prefix is stripped before placement under the wrapper.
	if idx["refresh_interval"] != "30s" {
		t.Errorf("refresh_interval = %v, want \"30s\"", idx["refresh_interval"])
	}
}

func TestUpdateSettings_dryRun_doesNotCallCluster(t *testing.T) {
	su := &fakeSettingsUpdater{resp: types.IndexSettingsUpdateResult{Acknowledged: true}}
	got, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
		Index:     "logs-2024-*",
		KeyValues: map[string]string{"number_of_replicas": "2"},
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("expected synthetic dry-run ack, got %+v", got)
	}
	if len(su.calls) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", su.calls)
	}
	if got.Body == "" {
		t.Errorf("dry-run should echo the body it would have sent")
	}
}

func TestUpdateSettings_passesBodyThrough(t *testing.T) {
	su := &fakeSettingsUpdater{resp: types.IndexSettingsUpdateResult{Acknowledged: true}}
	body := []byte(`{"index":{"number_of_replicas":"2"}}`)
	_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
		Index:            "logs",
		Body:             body,
		PreserveExisting: true,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if len(su.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(su.calls))
	}
	if string(su.calls[0].Body) != string(body) {
		t.Errorf("body was rewritten: %s", su.calls[0].Body)
	}
	if !su.calls[0].PreserveExisting {
		t.Errorf("PreserveExisting not forwarded")
	}
}

func TestUpdateSettings_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	su := &fakeSettingsUpdater{err: sentinel}
	_, err := index.UpdateSettings(context.Background(), su, index.SettingsUpdateOptions{
		Index:     "logs",
		KeyValues: map[string]string{"number_of_replicas": "2"},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}
