package ops

import (
	"context"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// GetLicense returns the cluster's installed licence. ES-only:
// LicenseInspector returns ErrUnsupported on OpenSearch, which the CLI
// surfaces as exit code 6.
//
// A basic licence (the default) lands as Status="active", Type="basic",
// ExpiresAt=nil — the rule consumer renders "no expiry concern" without
// a special-case branch.
func GetLicense(ctx context.Context, c esopsclient.LicenseInspector) (types.LicenseStatus, error) {
	return c.License(ctx)
}
