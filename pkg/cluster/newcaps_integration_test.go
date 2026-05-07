//go:build integration

package cluster_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// TestNewCaps_integration_devContexts exercises FieldCapsInspector,
// FieldCardinalityInspector, and SnapshotRestoreHistoryInspector
// against the local dev compose stack (`make dev-up`) — the same
// three contexts the http-tls integration test already covers.
//
// Per CLAUDE.md §11 the integration tier is what catches wire-format
// regressions a contract test against an httptest.Server cannot —
// `_field_caps`, `cardinality` aggregations, and the task-history
// shape have all evolved across cluster versions and we want a
// future re-shaped key to fail loudly here, not silently in
// production.
//
// The test skips with a clear "make dev-up" hint when a cluster is
// unreachable rather than failing.
func TestNewCaps_integration_devContexts(t *testing.T) {
	repoRoot := repoRootFromCaller(t)
	t.Chdir(repoRoot)
	t.Setenv("ESOPS_ALLOW_INSECURE_CONFIG", "1")
	t.Setenv("ESOPS_ALLOW_PLAINTEXT_SECRETS", "1")
	if os.Getenv("ESOPS_LOCAL_OS_PASSWORD") == "" {
		t.Setenv("ESOPS_LOCAL_OS_PASSWORD", "EsopsDev123!")
	}

	cfg, err := config.Load(filepath.Join(repoRoot, "config", "esops.example.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	for _, name := range []string{"local-es", "local-os", "local-es-mtls"} {
		t.Run(name, func(t *testing.T) {
			_, cc, err := cfg.ResolveContext(name)
			if err != nil {
				t.Fatalf("resolve %q: %v", name, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			c, err := cluster.New(ctx, cc)
			if err != nil {
				t.Skipf("cluster %q unreachable (run `make dev-up`): %v", name, err)
			}

			caps, err := c.FieldCaps.FieldCaps(ctx, types.FieldCapsRequest{Index: "*"})
			if err != nil {
				t.Errorf("FieldCaps: %v", err)
			} else {
				t.Logf("FieldCaps: %d fields visible (dialect=%s)", len(caps), c.Info.Dialect)
			}

			// Cardinality: pick the first user-defined aggregatable
			// field FieldCaps reports, on the first visible index.
			// Metadata fields (`_id`, `_routing`, …) are not
			// aggregatable on recent ES, so a "use _id" shortcut would
			// fail spuriously on local-es.
			indices, err := c.Indices.Indices(ctx, types.IndexFilter{})
			if err != nil {
				t.Errorf("Indices: %v", err)
			} else if len(indices) > 0 {
				idx := indices[0].Name
				idxCaps, err := c.FieldCaps.FieldCaps(ctx, types.FieldCapsRequest{Index: idx})
				if err != nil {
					t.Errorf("FieldCaps(%s): %v", idx, err)
				}
				field := pickAggregatableField(idxCaps)
				if field == "" {
					t.Logf("no aggregatable user-defined field on %s — skipping Cardinality", idx)
				} else {
					card, err := c.FieldCardinality.Cardinality(ctx, types.FieldCardinalityRequest{
						Indices:         idx,
						Fields:          []string{field},
						MaxFieldsToScan: 1,
					})
					if err != nil {
						t.Errorf("Cardinality(%s, %s): %v", idx, field, err)
					} else if len(card) != 1 {
						t.Errorf("Cardinality returned %d rows, want 1", len(card))
					} else {
						t.Logf("Cardinality(%s, %s) = %d (precision=%s)",
							card[0].Index, card[0].Field, card[0].Cardinality, card[0].Precision)
					}
				}
			} else {
				t.Logf("no indices visible — skipping Cardinality")
			}

			// RestoreHistory: a fresh cluster has no recorded restores
			// — empty slice is the expected outcome and a load-bearing
			// signal that the convention-index 404 path translates to
			// an empty slice rather than an error.
			events, err := c.RestoreHistory.RestoreHistory(ctx, types.SnapshotRestoreHistoryRequest{
				IncludeTaskHistory:     true,
				IncludeConventionIndex: true,
				Lookback:               24 * time.Hour,
			})
			if err != nil {
				t.Errorf("RestoreHistory: %v", err)
			} else {
				t.Logf("RestoreHistory: %d events", len(events))
			}

			// RemoteClusters: a single-cluster dev compose has no
			// configured remotes — empty slice is the expected
			// outcome and proves the empty-object decode path on
			// both dialects.
			remotes, err := c.RemoteClusters.RemoteClusters(ctx)
			if err != nil {
				t.Errorf("RemoteClusters: %v", err)
			} else {
				t.Logf("RemoteClusters: %d configured (dialect=%s)", len(remotes), c.Info.Dialect)
			}

			// FollowerStats / AutoFollowPatterns / License: ES-only.
			// On OS we expect ErrUnsupported (the factory stub on
			// the OS branch); on ES the basic-licence dev cluster
			// reports ErrUnsupported (404), the trial-licence cluster
			// returns empty slices.
			_, ferr := c.RemoteClusters.FollowerStats(ctx)
			_, aerr := c.RemoteClusters.AutoFollowPatterns(ctx)
			lic, lerr := c.License.License(ctx)
			if c.Info.Dialect == types.DialectOpenSearch {
				if !errors.Is(ferr, esopsclient.ErrUnsupported) {
					t.Errorf("FollowerStats on OS: err = %v, want ErrUnsupported", ferr)
				}
				if !errors.Is(aerr, esopsclient.ErrUnsupported) {
					t.Errorf("AutoFollowPatterns on OS: err = %v, want ErrUnsupported", aerr)
				}
				if !errors.Is(lerr, esopsclient.ErrUnsupported) {
					t.Errorf("License on OS: err = %v, want ErrUnsupported", lerr)
				}
			} else {
				if !errors.Is(ferr, esopsclient.ErrUnsupported) {
					t.Logf("FollowerStats on ES (dev cluster): err = %v (basic licence is the expected case)", ferr)
				}
				if !errors.Is(aerr, esopsclient.ErrUnsupported) {
					t.Logf("AutoFollowPatterns on ES (dev cluster): err = %v (basic licence is the expected case)", aerr)
				}
				if lerr != nil {
					t.Errorf("License on ES: %v", lerr)
				} else {
					t.Logf("License: status=%s type=%s", lic.Status, lic.Type)
				}
			}
		})
	}
}

// pickAggregatableField picks the first user-defined aggregatable
// field — used by the integration test to dodge the metadata-field
// "not aggregatable" trap on recent ES.
func pickAggregatableField(caps []types.FieldCaps) string {
	for _, fc := range caps {
		if fc.MetadataField || !fc.Aggregatable || fc.Field == "" {
			continue
		}
		return fc.Field
	}
	return ""
}
