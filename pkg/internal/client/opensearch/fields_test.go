package opensearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestFieldCaps_postsToFieldCapsAndDecodesUnion(t *testing.T) {
	tr := &docsTransport{body: `{
		"indices": ["idx-a","idx-b"],
		"fields": {
			"user": {
				"keyword": {"type":"keyword","searchable":true,"aggregatable":true,"indices":["idx-a"]},
				"text":    {"type":"text","searchable":true,"aggregatable":false,"indices":["idx-b"]}
			},
			"_id": {
				"_id": {"type":"_id","searchable":true,"aggregatable":false,"metadata_field":true}
			}
		}
	}`}
	c := newProbedClient(t, tr)

	got, err := c.FieldCaps(context.Background(), types.FieldCapsRequest{
		Index:  "idx-*",
		Fields: []string{"*"},
	})
	if err != nil {
		t.Fatalf("FieldCaps: %v", err)
	}
	if !strings.Contains(tr.gotPath, "/_field_caps") {
		t.Errorf("path = %q, want contains /_field_caps", tr.gotPath)
	}
	if got := tr.gotQuery.Get("fields"); got != "*" {
		t.Errorf("fields = %q, want *", got)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Field != "_id" {
		t.Errorf("got[0].Field = %q, want _id", got[0].Field)
	}
	if !got[0].MetadataField {
		t.Errorf("got[0].MetadataField = false, want true (cluster says metadata_field=true)")
	}
	if got[1].Field != "user" {
		t.Errorf("got[1].Field = %q, want user", got[1].Field)
	}
	if len(got[1].Types) != 2 || got[1].Types[0] != "keyword" || got[1].Types[1] != "text" {
		t.Errorf("got[1].Types = %v, want [keyword text] (sorted)", got[1].Types)
	}
	if !got[1].Searchable {
		t.Errorf("got[1].Searchable = false, want true (any-searchable wins)")
	}
	if !got[1].Aggregatable {
		t.Errorf("got[1].Aggregatable = false, want true (any-aggregatable wins)")
	}
	if len(got[1].Indices) != 2 || got[1].Indices[0] != "idx-a" || got[1].Indices[1] != "idx-b" {
		t.Errorf("got[1].Indices = %v, want [idx-a idx-b]", got[1].Indices)
	}
}

func TestFieldCaps_includeUnmappedSetsQueryParam(t *testing.T) {
	tr := &docsTransport{body: `{"indices":[],"fields":{}}`}
	c := newProbedClient(t, tr)
	if _, err := c.FieldCaps(context.Background(), types.FieldCapsRequest{IncludeUnmapped: true}); err != nil {
		t.Fatalf("FieldCaps: %v", err)
	}
	if got := tr.gotQuery.Get("include_unmapped"); got != "true" {
		t.Errorf("include_unmapped = %q, want true", got)
	}
}

func TestCardinality_emptyIndicesOrFieldsIsUsageError(t *testing.T) {
	tr := &docsTransport{body: `{}`}
	c := newProbedClient(t, tr)

	_, err := c.Cardinality(context.Background(), types.FieldCardinalityRequest{
		Indices: "",
		Fields:  []string{"f1"},
	})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected ErrUsage for empty indices, got %v", err)
	}

	_, err = c.Cardinality(context.Background(), types.FieldCardinalityRequest{
		Indices: "idx-a",
		Fields:  nil,
	})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected ErrUsage for empty fields, got %v", err)
	}
}

func TestCardinality_capsExceededIsUsageError(t *testing.T) {
	tr := &docsTransport{body: `{"aggregations":{"c":{"value":0}}}`}
	c := newProbedClient(t, tr)

	_, err := c.Cardinality(context.Background(), types.FieldCardinalityRequest{
		Indices:         "a,b",
		Fields:          []string{"f1", "f2"},
		MaxFieldsToScan: 3,
	})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected ErrUsage for cap exceeded, got %v", err)
	}

	_, err = c.Cardinality(context.Background(), types.FieldCardinalityRequest{
		Indices:    "a,b,c",
		Fields:     []string{"f1"},
		IndexLimit: 2,
	})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected ErrUsage for index limit exceeded, got %v", err)
	}
}

func TestFieldCapsSummary_carriesMatchedIndices(t *testing.T) {
	tr := &docsTransport{body: `{
		"indices": ["idx-a","idx-b"],
		"fields": {
			"user": {"keyword": {"type":"keyword","searchable":true,"aggregatable":true}}
		}
	}`}
	c := newProbedClient(t, tr)

	got, err := c.FieldCapsSummary(context.Background(), types.FieldCapsRequest{Index: "idx-*"})
	if err != nil {
		t.Fatalf("FieldCapsSummary: %v", err)
	}
	if len(got.MatchedIndices) != 2 || got.MatchedIndices[0] != "idx-a" {
		t.Errorf("MatchedIndices = %v, want [idx-a idx-b]", got.MatchedIndices)
	}
	if len(got.Fields) != 1 || got.Fields[0].Field != "user" {
		t.Errorf("Fields = %v, want one row for `user`", got.Fields)
	}
}

func TestCardinalityStream_yieldsEachPairAndStopsOnError(t *testing.T) {
	tr := &docsTransport{body: `{"aggregations":{"c":{"value":7}}}`}
	c := newProbedClient(t, tr)

	var seen []string
	err := c.CardinalityStream(context.Background(), types.FieldCardinalityRequest{
		Indices: "idx-a",
		Fields:  []string{"f1", "f2"},
	}, func(r types.FieldCardinality) error {
		seen = append(seen, r.Field)
		return nil
	})
	if err != nil {
		t.Fatalf("CardinalityStream: %v", err)
	}
	if len(seen) != 2 || seen[0] != "f1" || seen[1] != "f2" {
		t.Errorf("seen = %v, want [f1 f2] in request order", seen)
	}

	tr2 := &docsTransport{body: `{"aggregations":{"c":{"value":7}}}`}
	c2 := newProbedClient(t, tr2)
	stop := errors.New("stop")
	count := 0
	err = c2.CardinalityStream(context.Background(), types.FieldCardinalityRequest{
		Indices: "idx-a",
		Fields:  []string{"f1", "f2", "f3"},
	}, func(r types.FieldCardinality) error {
		count++
		if count == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("expected stop error, got %v", err)
	}
	if count != 2 {
		t.Errorf("yield invoked %d times, want 2 (early-exit on yield error)", count)
	}
}

func TestCardinalityStream_nilYieldIsUsageError(t *testing.T) {
	tr := &docsTransport{body: `{"aggregations":{"c":{"value":0}}}`}
	c := newProbedClient(t, tr)
	err := c.CardinalityStream(context.Background(), types.FieldCardinalityRequest{
		Indices: "idx-a",
		Fields:  []string{"f1"},
	}, nil)
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Errorf("expected ErrUsage for nil yield, got %v", err)
	}
}

func TestCardinality_sendsExpectedAggBodyAndDecodesValue(t *testing.T) {
	tr := &docsTransport{body: `{"aggregations":{"c":{"value":42}}}`}
	c := newProbedClient(t, tr)

	got, err := c.Cardinality(context.Background(), types.FieldCardinalityRequest{
		Indices:   "logs-2024",
		Fields:    []string{"user.id"},
		Precision: 5000,
	})
	if err != nil {
		t.Fatalf("Cardinality: %v", err)
	}
	if !strings.HasPrefix(tr.gotPath, "/logs-2024/_search") {
		t.Errorf("path = %q, want /logs-2024/_search…", tr.gotPath)
	}
	if got := tr.gotQuery.Get("size"); got != "0" {
		t.Errorf("size = %q, want 0", got)
	}
	if got := tr.gotQuery.Get("track_total_hits"); got != "false" {
		t.Errorf("track_total_hits = %q, want false", got)
	}

	var body map[string]any
	if err := json.Unmarshal(tr.gotBody, &body); err != nil {
		t.Fatalf("body not JSON: %v (raw=%s)", err, tr.gotBody)
	}
	aggs, _ := body["aggs"].(map[string]any)
	c2, _ := aggs["c"].(map[string]any)
	card, _ := c2["cardinality"].(map[string]any)
	if card["field"] != "user.id" {
		t.Errorf("aggs.c.cardinality.field = %v, want user.id", card["field"])
	}
	if v, _ := card["precision_threshold"].(float64); int(v) != 5000 {
		t.Errorf("aggs.c.cardinality.precision_threshold = %v, want 5000", card["precision_threshold"])
	}

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Cardinality != 42 {
		t.Errorf("got[0].Cardinality = %d, want 42", got[0].Cardinality)
	}
	if got[0].Field != "user.id" || got[0].Index != "logs-2024" {
		t.Errorf("got[0] = %+v, want field=user.id index=logs-2024", got[0])
	}
	if got[0].Precision != "approximate" {
		t.Errorf("got[0].Precision = %q, want approximate", got[0].Precision)
	}
}
