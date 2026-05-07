package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// esLicenseWire mirrors the slice of GET /_license we read. The cluster
// reports both ISO strings (issue_date, expiry_date) and epoch
// milliseconds (issue_date_in_millis, expiry_date_in_millis) for the
// same fields; we prefer the millis form because the ISO form has
// drifted between RFC3339 and a "no-Z" variant across versions and
// epoch math is cheap. Pointer-typed ExpiryDateInMillis distinguishes
// "no expiry" (basic licence — the cluster omits the field) from a
// concrete expiry timestamp.
type esLicenseWire struct {
	License struct {
		Status             string `json:"status"`
		UID                string `json:"uid"`
		Type               string `json:"type"`
		IssuedTo           string `json:"issued_to"`
		Issuer             string `json:"issuer"`
		IssueDateInMillis  int64  `json:"issue_date_in_millis"`
		ExpiryDateInMillis *int64 `json:"expiry_date_in_millis"`
		MaxNodes           int    `json:"max_nodes"`
	} `json:"license"`
}

// License implements client.LicenseInspector. The cluster always has a
// licence — basic ships by default and never expires, surfaced as
// Status="active", Type="basic", ExpiresAt=nil.
//
// 404 on /_license is the "licensing module disabled" case (rare, but
// some custom builds strip it). Translate to ErrUnsupported so the
// rule consumer treats it as "not applicable on this cluster" — the
// same convention used for /_security/api_key on disabled-security
// clusters.
func (c *Client) License(ctx context.Context) (types.LicenseStatus, error) {
	body, status, err := c.doRawGET(ctx, "/_license")
	if err != nil {
		return types.LicenseStatus{}, err
	}
	switch status {
	case http.StatusUnauthorized:
		return types.LicenseStatus{}, esopsclient.ErrAuth
	case http.StatusForbidden:
		return types.LicenseStatus{}, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return types.LicenseStatus{}, errors.Join(esopsclient.ErrUnsupported,
			errors.New("/_license not exposed by this cluster build"))
	}
	if status >= 400 {
		return types.LicenseStatus{}, clientutil.ClassifyStatus("license", status, body)
	}

	var wire esLicenseWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return types.LicenseStatus{}, errors.Join(errors.New("decoding license response"), err)
	}
	out := types.LicenseStatus{
		Status:   wire.License.Status,
		Type:     wire.License.Type,
		IssuedTo: wire.License.IssuedTo,
		Issuer:   wire.License.Issuer,
		UID:      wire.License.UID,
		MaxNodes: wire.License.MaxNodes,
	}
	if wire.License.IssueDateInMillis > 0 {
		out.IssuedAt = time.UnixMilli(wire.License.IssueDateInMillis).UTC()
	}
	if wire.License.ExpiryDateInMillis != nil && *wire.License.ExpiryDateInMillis > 0 {
		t := time.UnixMilli(*wire.License.ExpiryDateInMillis).UTC()
		out.ExpiresAt = &t
	}
	return out, nil
}
