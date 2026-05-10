package segmentswire_test

import (
	"encoding/json"
	"testing"

	"github.com/esops-dev/esops-go/pkg/internal/client/segmentswire"
)

// fixture mirrors a real /_segments response: two indices, the first
// with a single shard exposed as primary + replica, the second with
// two primary-only shards. The replica copy on logs-1/0 carries the
// same num_docs as the primary so the test can verify that
// ReportFromWire counts replica segments only into SegmentsTotal and
// not into the per-primary aggregates.
const fixture = `{
  "indices": {
    "logs-1": {
      "shards": {
        "0": [
          {
            "routing": {"state":"STARTED","primary":true,"node":"node1"},
            "num_committed_segments": 2,
            "num_search_segments":    3,
            "segments": {
              "_a": {"generation":1,"num_docs":100,"deleted_docs":10,"size_in_bytes":1024,"committed":true,"search":true},
              "_b": {"generation":2,"num_docs":50,"deleted_docs":0,"size_in_bytes":512,"committed":true,"search":true},
              "_c": {"generation":3,"num_docs":25,"deleted_docs":5,"size_in_bytes":256,"committed":false,"search":true}
            }
          },
          {
            "routing": {"state":"STARTED","primary":false,"node":"node2"},
            "num_committed_segments": 2,
            "num_search_segments":    2,
            "segments": {
              "_a": {"generation":1,"num_docs":100,"deleted_docs":10,"size_in_bytes":1024,"committed":true,"search":true},
              "_b": {"generation":2,"num_docs":50,"deleted_docs":0,"size_in_bytes":512,"committed":true,"search":true}
            }
          }
        ]
      }
    },
    "users": {
      "shards": {
        "0": [
          {
            "routing": {"state":"STARTED","primary":true,"node":"node1"},
            "num_committed_segments": 1,
            "num_search_segments":    1,
            "segments": {
              "_x": {"generation":1,"num_docs":7,"deleted_docs":0,"size_in_bytes":128,"committed":true,"search":true}
            }
          }
        ],
        "1": [
          {
            "routing": {"state":"STARTED","primary":true,"node":"node2"},
            "num_committed_segments": 4,
            "num_search_segments":    4,
            "segments": {
              "_p": {"generation":1,"num_docs":2,"deleted_docs":0,"size_in_bytes":64,"committed":true,"search":true},
              "_q": {"generation":2,"num_docs":2,"deleted_docs":0,"size_in_bytes":64,"committed":true,"search":true},
              "_r": {"generation":3,"num_docs":2,"deleted_docs":0,"size_in_bytes":64,"committed":true,"search":true},
              "_s": {"generation":4,"num_docs":1,"deleted_docs":0,"size_in_bytes":64,"committed":true,"search":true}
            }
          }
        ]
      }
    }
  }
}`

func TestReportFromWire_aggregatesPerIndex(t *testing.T) {
	var env segmentswire.Envelope
	if err := json.Unmarshal([]byte(fixture), &env); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	got := segmentswire.ReportFromWire(env)
	if len(got.Indices) != 2 {
		t.Fatalf("got %d indices, want 2", len(got.Indices))
	}

	// Indices come back sorted: logs-1 then users.
	if got.Indices[0].Index != "logs-1" || got.Indices[1].Index != "users" {
		t.Errorf("ordering = %v, want [logs-1 users]", []string{got.Indices[0].Index, got.Indices[1].Index})
	}

	// logs-1: single shard, primary has 3 segments, replica adds 2 to
	// SegmentsTotal but contributes nothing to the primary-side
	// counters.
	logs := got.Indices[0]
	if logs.Shards != 1 {
		t.Errorf("logs-1 Shards = %d, want 1", logs.Shards)
	}
	if logs.SegmentsPrimary != 3 {
		t.Errorf("logs-1 SegmentsPrimary = %d, want 3", logs.SegmentsPrimary)
	}
	if logs.SegmentsTotal != 5 {
		t.Errorf("logs-1 SegmentsTotal = %d, want 5 (primary 3 + replica 2)", logs.SegmentsTotal)
	}
	if logs.MaxSegmentsShard != 3 {
		t.Errorf("logs-1 MaxSegmentsShard = %d, want 3", logs.MaxSegmentsShard)
	}
	if logs.DocsTotal != 175 {
		t.Errorf("logs-1 DocsTotal = %d, want 175", logs.DocsTotal)
	}
	if logs.DocsDeleted != 15 {
		t.Errorf("logs-1 DocsDeleted = %d, want 15", logs.DocsDeleted)
	}
	if logs.Bytes != 1024+512+256 {
		t.Errorf("logs-1 Bytes = %d, want 1792", logs.Bytes)
	}
	if logs.Committed != 2 {
		t.Errorf("logs-1 Committed = %d, want 2 (primary copy only)", logs.Committed)
	}
	if logs.Searchable != 3 {
		t.Errorf("logs-1 Searchable = %d, want 3 (primary copy only)", logs.Searchable)
	}

	// users: two primary shards, max segments per shard is 4.
	users := got.Indices[1]
	if users.Shards != 2 {
		t.Errorf("users Shards = %d, want 2", users.Shards)
	}
	if users.SegmentsPrimary != 5 {
		t.Errorf("users SegmentsPrimary = %d, want 5", users.SegmentsPrimary)
	}
	if users.MaxSegmentsShard != 4 {
		t.Errorf("users MaxSegmentsShard = %d, want 4 (shard 1 has 4 segments)", users.MaxSegmentsShard)
	}
}

func TestReportFromWire_emptyEnvelope(t *testing.T) {
	got := segmentswire.ReportFromWire(segmentswire.Envelope{})
	if len(got.Indices) != 0 {
		t.Errorf("empty envelope produced %d rows, want 0", len(got.Indices))
	}
	// Caller depends on a non-nil slice for stable JSON encoding.
	if got.Indices == nil {
		t.Error("empty envelope produced nil slice; want non-nil empty slice")
	}
}
