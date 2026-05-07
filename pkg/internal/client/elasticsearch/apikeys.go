package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
)

// APIKeyInspector and ServiceTokenInspector wire the ES side of the
// service-token-hygiene and stale-API-key rules. The companion file
// realms.go documents the version-aware deprecation pattern this code
// follows for keeping rule logic out of consumer bookkeeping.
//
// The two capabilities live together because both surface
// /_security-rooted credential metadata: API keys (cluster-wide) and
// service-account tokens (per-account, fanned out). Sharing a file
// keeps the wire shapes and the time-decoding helpers next to each
// other.

// esAPIKeyWire mirrors one entry from /_security/api_key. Times are
// epoch-millis on the wire; we render them as time.Time on the way out
// so consumers don't re-parse RFC3339 strings every call. *int64 lets
// us tell "expiration absent" (key never expires) from "expiration set
// to zero" (which the cluster never produces but which a non-pointer
// int64 would conflate). LastAuth lands here as a pointer too — older
// ES omits the field entirely; we want the absence to survive into the
// type as nil, not a zeroed time the consumer mistakes for the epoch.
type esAPIKeyWire struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Username      string   `json:"username"`
	Realm         string   `json:"realm"`
	Invalidated   bool     `json:"invalidated"`
	CreationMS    int64    `json:"creation"`
	ExpirationMS  *int64   `json:"expiration"`
	LastAuthMS    *int64   `json:"last_authenticated"`
	RoleTemplates []string `json:"role_templates"`
}

// APIKeys implements client.APIKeyInspector. Sorted by Creation desc,
// then ID, so a captured slice is stable across runs.
func (c *Client) APIKeys(ctx context.Context) ([]types.APIKey, error) {
	body, status, err := c.doRawGET(ctx, "/_security/api_key?owner=false")
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		// Older ES (pre-7.x) didn't expose the listing endpoint — the
		// rule that relies on this capability needs to fall through to
		// "not applicable", same translation the realms reader uses.
		return nil, fmt.Errorf("%w: /_security/api_key not exposed by this cluster build", esopsclient.ErrUnsupported)
	}
	if status >= 400 {
		// ES 9.x with xpack.security.enabled=false 400s with a
		// "no handler found" body for /_security/* paths. The audit
		// pipeline already classifies that shape; we mirror its
		// translation here so a downstream rule sees a single
		// "security disabled" outcome (ErrUnsupported) instead of
		// a generic 400.
		if isSecurityDisabledBody(body) {
			return nil, fmt.Errorf("%w: security is disabled on this cluster", esopsclient.ErrUnsupported)
		}
		return nil, clientutil.ClassifyStatus("api_keys", status, body)
	}

	var wrap struct {
		APIKeys []esAPIKeyWire `json:"api_keys"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, errors.Join(errors.New("decoding api_key response"), err)
	}
	out := make([]types.APIKey, 0, len(wrap.APIKeys))
	for _, k := range wrap.APIKeys {
		out = append(out, types.APIKey{
			ID:            k.ID,
			Name:          k.Name,
			Username:      k.Username,
			Realm:         k.Realm,
			Creation:      epochMillisTime(k.CreationMS),
			Expiration:    epochMillisTimePtr(k.ExpirationMS),
			LastAuth:      epochMillisTimePtr(k.LastAuthMS),
			Invalidated:   k.Invalidated,
			RoleTemplates: append([]string(nil), k.RoleTemplates...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Creation.Equal(out[j].Creation) {
			return out[i].Creation.After(out[j].Creation)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// esServiceAccountListWire mirrors GET /_security/service. The response
// is keyed by "<namespace>/<service>" with a small per-account block we
// don't surface — only the account names are needed to fan out.
type esServiceAccountListWire map[string]json.RawMessage

// esServiceCredentialsWire mirrors GET /_security/service/<ns>/<svc>/credential.
// The cluster splits the response into two blocks: tokens.* (index-stored,
// API-managed) and nodes_credentials.file_tokens (declared on each node's
// disk). Both shapes have shipped across ES 8.x and remain stable.
type esServiceCredentialsWire struct {
	ServiceAccount string                            `json:"service_account"`
	Count          int                               `json:"count"`
	Tokens         map[string]esServiceTokenMetaWire `json:"tokens"`
	NodesCreds     struct {
		FileTokens map[string]json.RawMessage `json:"file_tokens"`
	} `json:"nodes_credentials"`
}

// esServiceTokenMetaWire is the per-token metadata. ES 8.x reports
// creation as either an epoch-millis number or an RFC3339-shaped
// string depending on the index format; we accept both via a
// json.RawMessage and decode in the projector.
type esServiceTokenMetaWire struct {
	Creation *json.RawMessage `json:"creation"`
}

// ServiceTokens implements client.ServiceTokenInspector. Account
// enumeration is one cheap GET; per-account credentials run in a small
// fan-out (most fleets carry single-digit service accounts, so the
// common case is two or three concurrent reads). A blanket 403 on the
// listing aborts; per-account 403s are skipped so partial visibility
// beats a dropped run.
func (c *Client) ServiceTokens(ctx context.Context) ([]types.ServiceToken, error) {
	listBody, listStatus, err := c.doRawGET(ctx, "/_security/service")
	if err != nil {
		return nil, err
	}
	switch listStatus {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: /_security/service not exposed by this cluster build", esopsclient.ErrUnsupported)
	}
	if listStatus >= 400 {
		if isSecurityDisabledBody(listBody) {
			return nil, fmt.Errorf("%w: security is disabled on this cluster", esopsclient.ErrUnsupported)
		}
		return nil, clientutil.ClassifyStatus("service accounts", listStatus, listBody)
	}

	var accounts esServiceAccountListWire
	if err := json.Unmarshal(listBody, &accounts); err != nil {
		return nil, errors.Join(errors.New("decoding service accounts response"), err)
	}
	if len(accounts) == 0 {
		return []types.ServiceToken{}, nil
	}

	type accountKey struct{ namespace, service string }
	keys := make([]accountKey, 0, len(accounts))
	for k := range accounts {
		ns, svc, ok := splitServiceAccountKey(k)
		if !ok {
			continue
		}
		keys = append(keys, accountKey{namespace: ns, service: svc})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].namespace != keys[j].namespace {
			return keys[i].namespace < keys[j].namespace
		}
		return keys[i].service < keys[j].service
	})

	results := make([][]types.ServiceToken, len(keys))
	tasks := make([]func(context.Context) error, len(keys))
	for i, k := range keys {
		i, k := i, k
		tasks[i] = func(ctx context.Context) error {
			toks, err := c.fetchServiceCredentials(ctx, k.namespace, k.service)
			if err != nil {
				// Per-account forbidden / not-found are skipped: the
				// caller's read_security may not extend to every
				// service account, and an account that disappeared
				// between listing and credential read is not the
				// caller's problem.
				if errors.Is(err, esopsclient.ErrForbidden) || errors.Is(err, errServiceAccountGone) {
					return nil
				}
				return err
			}
			results[i] = toks
			return nil
		}
	}
	if err := clientutil.RunBounded(ctx, 4, tasks); err != nil {
		return nil, err
	}

	var out []types.ServiceToken
	for _, r := range results {
		out = append(out, r...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	if out == nil {
		out = []types.ServiceToken{}
	}
	return out, nil
}

// errServiceAccountGone is the sentinel the per-account fetcher uses to
// flag "this account vanished between the listing and the credential
// read" so the fan-out skips it without aborting the whole call.
var errServiceAccountGone = errors.New("service account no longer present")

// fetchServiceCredentials reads /_security/service/<ns>/<svc>/credential
// and folds the index-stored and file-realm tokens into a flat slice
// of types.ServiceToken with Source set accordingly.
func (c *Client) fetchServiceCredentials(ctx context.Context, namespace, service string) ([]types.ServiceToken, error) {
	path := fmt.Sprintf("/_security/service/%s/%s/credential", namespace, service)
	body, status, err := c.doRawGET(ctx, path)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusUnauthorized:
		return nil, esopsclient.ErrAuth
	case http.StatusForbidden:
		return nil, esopsclient.ErrForbidden
	case http.StatusNotFound:
		return nil, errServiceAccountGone
	}
	if status >= 400 {
		return nil, clientutil.ClassifyStatus("service credentials", status, body)
	}

	var wire esServiceCredentialsWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, errors.Join(errors.New("decoding service credentials response"), err)
	}

	out := make([]types.ServiceToken, 0, len(wire.Tokens)+len(wire.NodesCreds.FileTokens))
	for name, meta := range wire.Tokens {
		out = append(out, types.ServiceToken{
			Namespace: namespace,
			Service:   service,
			Name:      name,
			Source:    "index",
			Creation:  decodeServiceTokenCreation(meta.Creation),
		})
	}
	for name := range wire.NodesCreds.FileTokens {
		// File-realm tokens are not timestamped by the cluster — the
		// authoritative record lives in service_tokens on each node's
		// disk. Leave Creation as the zero value.
		out = append(out, types.ServiceToken{
			Namespace: namespace,
			Service:   service,
			Name:      name,
			Source:    "file",
		})
	}
	return out, nil
}

// splitServiceAccountKey parses ES's "<namespace>/<service>" key.
// Returns ok=false on a malformed entry so the caller can skip it
// rather than fan out a "/credential" request that will 404.
func splitServiceAccountKey(k string) (string, string, bool) {
	i := strings.IndexByte(k, '/')
	if i <= 0 || i >= len(k)-1 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

// decodeServiceTokenCreation accepts both shapes ES 8.x emits for the
// per-token creation field: an epoch-millis number on freshly-created
// tokens, an RFC3339-shaped string on tokens persisted through a
// reindex of the security index. Returns the zero time on a
// missing/garbage value — surfacing zero is the right answer for "the
// cluster did not tell us when".
func decodeServiceTokenCreation(raw *json.RawMessage) time.Time {
	if raw == nil || len(*raw) == 0 {
		return time.Time{}
	}
	var ms int64
	if err := json.Unmarshal(*raw, &ms); err == nil {
		return epochMillisTime(ms)
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err == nil {
		s = strings.TrimSpace(s)
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

// epochMillisTime turns an ES millisecond timestamp into time.Time, or
// the zero time when the cluster did not report the field
// (zero/negative). The matching pointer helper preserves "absent" as
// nil for the optional fields.
func epochMillisTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func epochMillisTimePtr(ms *int64) *time.Time {
	if ms == nil || *ms <= 0 {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}
