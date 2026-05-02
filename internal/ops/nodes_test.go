package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestNodes_sortsByName(t *testing.T) {
	// /_cat/nodes ordering shifts after a master election; the business
	// layer normalises so a saved JSON capture compares equal across
	// runs and dialects.
	ni := &fakeNodeInspector{nodes: []types.Node{
		{Name: "es03", Roles: []string{"data"}, IsDataNode: true},
		{Name: "es01", Roles: []string{"master"}},
		{Name: "es02", Roles: []string{"data"}, IsDataNode: true},
	}}

	got, err := ops.Nodes(context.Background(), ni)
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	want := []string{"es01", "es02", "es03"}
	if len(got) != len(want) {
		t.Fatalf("got %d nodes, want %d", len(got), len(want))
	}
	for i, n := range got {
		if n.Name != want[i] {
			t.Errorf("nodes[%d].Name = %q, want %q", i, n.Name, want[i])
		}
	}
}

func TestNodes_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	ni := &fakeNodeInspector{err: sentinel}
	_, err := ops.Nodes(context.Background(), ni)
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestNodes_passesEnrichmentThrough(t *testing.T) {
	// Adapter-supplied enrichment (heap, disk, version) must round-trip
	// through Nodes() unchanged — the business layer's only job is the
	// sort.
	ni := &fakeNodeInspector{nodes: []types.Node{{
		Name:           "es01",
		Roles:          []string{"data", "master"},
		IsDataNode:     true,
		Version:        "9.0.0",
		IP:             "10.0.0.1",
		Master:         true,
		HeapPercent:    42,
		HeapUsedBytes:  gib(2),
		HeapMaxBytes:   gib(8),
		DiskUsedBytes:  gib(100),
		DiskAvailBytes: gib(900),
	}}}
	got, err := ops.Nodes(context.Background(), ni)
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Version != "9.0.0" || got[0].HeapPercent != 42 || !got[0].Master {
		t.Errorf("enrichment dropped: %+v", got[0])
	}
}
