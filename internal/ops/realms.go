package ops

import (
	"context"
	"sort"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ListRealms returns the cluster's configured authentication realm
// chain in evaluation order — Order, then Type, then Name. The
// adapters already sort the slice deterministically; we re-apply the
// sort here so the function is the single source of truth for
// ordering and a future custom-comparator change lands in one place.
//
// On OpenSearch the underlying read
// (/_plugins/_security/api/securityconfig) is admin-cert-gated on most
// production deployments; a basic-auth caller hits 403 and the
// capability returns ErrForbidden. The CLI surfaces that as an auth
// error (exit code 5) — distinct from "no realms configured", which
// is an empty slice with no error.
func ListRealms(ctx context.Context, c client.RealmsInspector) ([]types.Realm, error) {
	realms, err := c.Realms(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(realms, func(i, j int) bool {
		if realms[i].Order != realms[j].Order {
			return realms[i].Order < realms[j].Order
		}
		if realms[i].Type != realms[j].Type {
			return realms[i].Type < realms[j].Type
		}
		return realms[i].Name < realms[j].Name
	})
	return realms, nil
}
