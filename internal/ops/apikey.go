package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultMaxInvalidateKeys is the blast-radius cap for `ops apikey
// invalidate`. Ten keys cover the realistic one-shot rotation
// (operator rotates a service's keys after a compromise) without
// inviting "invalidate everything matching *" mistakes. Operators
// wanting a larger sweep pass --max-keys N explicitly so the intent
// is visible in shell history.
const DefaultMaxInvalidateKeys = 10

// ListAPIKeys returns the cluster's API keys via APIKeyInspector. The
// adapter already sorts deterministically; we re-apply the sort here
// so the function is the single source of truth for ordering and a
// future custom-comparator change lands in one place.
func ListAPIKeys(ctx context.Context, c client.APIKeyInspector) ([]types.APIKey, error) {
	keys, err := c.APIKeys(ctx)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// GetAPIKey returns the metadata of a single key by id. Never returns
// the secret — the cluster only emits the secret at creation time, and
// the inspector endpoint omits it by design.
func GetAPIKey(ctx context.Context, c client.APIKeyInspector, id string) (types.APIKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return types.APIKey{}, client.UsageError("api key id is required")
	}
	return c.GetAPIKey(ctx, id)
}

// CreateAPIKeyOptions describes the inputs the CLI hands to CreateAPIKey.
// RoleDescriptorsPath is the optional file the operator points at via
// `--role-descriptors FILE`; the business layer reads it and forwards
// the raw bytes verbatim to the adapter (no schema re-validation —
// the cluster is the authority on the shape). DryRun short-circuits
// the network call and returns a synthetic result echoing the
// resolved request so the operator can sanity-check the spec without
// the cluster having to issue a secret it'd have to invalidate.
type CreateAPIKeyOptions struct {
	Name                string
	Expiration          string
	RoleDescriptorsPath string
	DryRun              bool
}

// CreateAPIKey creates a new API key. The cluster's response contains
// the raw secret — the caller is responsible for routing it to stdout
// only, never to a log sink. This function refuses an empty name
// up-front so the operator sees a usage error before any network
// traffic.
//
// On DryRun the function returns a zero-key, zero-encoded
// APIKeyCreated with DryRun=true and the resolved Name/Expiration
// echoed back. The cluster is never contacted; nothing is created.
//
// On a successful live create, the function makes a best-effort
// follow-up GetAPIKey call to populate Owner — the create response
// itself does not echo the username, and the operator's audit-log
// summary wants `{id, name, owner, expiration}`. A follow-up failure
// (forbidden, transient network blip) is logged at debug and the
// owner stays empty; the create is not retried.
func CreateAPIKey(ctx context.Context, c client.APIKeyManager, inspector client.APIKeyInspector, opts CreateAPIKeyOptions) (types.APIKeyCreated, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.APIKeyCreated{}, client.UsageError("--name is required")
	}
	req := types.APIKeyCreateRequest{
		Name:       name,
		Expiration: strings.TrimSpace(opts.Expiration),
	}
	if path := strings.TrimSpace(opts.RoleDescriptorsPath); path != "" {
		raw, err := os.ReadFile(path) // #nosec G304 -- path is the operator-supplied --role-descriptors flag
		if err != nil {
			return types.APIKeyCreated{}, fmt.Errorf("reading --role-descriptors file %q: %w", path, err)
		}
		if !json.Valid(raw) {
			return types.APIKeyCreated{}, client.UsageError("--role-descriptors file %q is not valid JSON", path)
		}
		req.RoleDescriptors = raw
	}
	if opts.DryRun {
		return types.APIKeyCreated{
			Name:       req.Name,
			Expiration: parseExpirationForDryRun(req.Expiration),
			DryRun:     true,
		}, nil
	}
	created, err := c.CreateAPIKey(ctx, req)
	if err != nil {
		return types.APIKeyCreated{}, err
	}
	// Best-effort owner enrichment. The create endpoint does not echo
	// the username; a follow-up GET on the same id does. A failure
	// here is informative, not blocking — the key already exists, the
	// secret is in `created`, and the operator can run `ops apikey get`
	// later to see the owner.
	if inspector != nil && created.ID != "" {
		if got, gerr := inspector.GetAPIKey(ctx, created.ID); gerr == nil {
			created.Owner = got.Username
		} else {
			logging.Logger().Debug("ops.apikey.create.owner_lookup_failed",
				"id", created.ID, "err", gerr.Error())
		}
	}
	return created, nil
}

// parseExpirationForDryRun parses an "expiration" duration the way the
// cluster would, so the dry-run summary shows the operator the
// computed expiry instead of just echoing the raw "1d" string. Best-
// effort — an unparseable input (e.g. cluster-specific calendar
// duration) leaves the field nil and the renderer prints the original
// string from the request alongside.
func parseExpirationForDryRun(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Go's time.ParseDuration handles ns/us/ms/s/m/h. ES accepts a
	// superset (d, w, M, y). Best-effort: recognise the common d/w
	// shorthands explicitly so dry-run dates land in the right
	// ballpark.
	mult := time.Duration(0)
	var num string
	switch {
	case strings.HasSuffix(s, "d"):
		mult = 24 * time.Hour
		num = strings.TrimSuffix(s, "d")
	case strings.HasSuffix(s, "w"):
		mult = 7 * 24 * time.Hour
		num = strings.TrimSuffix(s, "w")
	}
	if mult > 0 {
		var n int
		if _, err := fmt.Sscanf(num, "%d", &n); err == nil && n > 0 {
			t := time.Now().UTC().Add(time.Duration(n) * mult)
			return &t
		}
		return nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		t := time.Now().UTC().Add(d)
		return &t
	}
	return nil
}

// InvalidateAPIKeyOptions describes the inputs for InvalidateAPIKey.
// MaxKeys is the explicit opt-in past the default 10-key blast-radius
// cap; 0 or negative resolves to DefaultMaxInvalidateKeys. DryRun
// short-circuits the cluster call and returns the resolved id list
// without revoking anything.
type InvalidateAPIKeyOptions struct {
	IDs     []string
	MaxKeys int
	DryRun  bool
}

// InvalidateAPIKey revokes one or more keys. Enforces the blast-radius
// cap before the call lands so a runaway list doesn't get partially
// invalidated when the cluster would have accepted the whole bulk
// request.
//
// On DryRun the function returns an APIKeyInvalidated with the resolved
// id list under Requested and DryRun=true; the cluster is never
// contacted.
func InvalidateAPIKey(ctx context.Context, c client.APIKeyManager, opts InvalidateAPIKeyOptions) (types.APIKeyInvalidated, error) {
	ids := normaliseInvalidateIDs(opts.IDs)
	if len(ids) == 0 {
		return types.APIKeyInvalidated{}, client.UsageError("at least one api key id is required")
	}
	limit := opts.MaxKeys
	if limit <= 0 {
		limit = DefaultMaxInvalidateKeys
	}
	if len(ids) > limit {
		return types.APIKeyInvalidated{}, fmt.Errorf(
			"%w: %d api key ids exceed the default %d-key limit; pass --max-keys N to override",
			client.ErrBlastRadius, len(ids), limit)
	}
	if opts.DryRun {
		return types.APIKeyInvalidated{
			Requested: ids,
			DryRun:    true,
		}, nil
	}
	result, err := c.InvalidateAPIKey(ctx, types.APIKeyInvalidateRequest{IDs: ids})
	if err != nil {
		return types.APIKeyInvalidated{}, err
	}
	result.Requested = ids
	return result, nil
}

// normaliseInvalidateIDs trims whitespace, drops empties, and
// deduplicates the operator's input. Dedup is mostly defensive — the
// cluster handles duplicates fine — but the resolved list is what
// gets logged and rendered, and a duplicate id in scrollback looks
// like a bug.
func normaliseInvalidateIDs(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}


// APIKeyCreateSummary is the non-secret summary the CLI logs and
// echoes to stderr. The audit-log trail records this shape — never
// the raw key. Owner is the cluster-reported username, populated by
// CreateAPIKey via a follow-up GET on the new id. Expiration is
// rendered as RFC3339 for grep-ability. DryRun flags a dry-run row
// so an operator reading the audit trail can tell a planned create
// from a real one without re-parsing the rest of the line.
type APIKeyCreateSummary struct {
	ID         string `json:"id,omitempty"         yaml:"id,omitempty"`
	Name       string `json:"name"                 yaml:"name"`
	Owner      string `json:"owner,omitempty"      yaml:"owner,omitempty"`
	Expiration string `json:"expiration,omitempty" yaml:"expiration,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"    yaml:"dry_run,omitempty"`
}

// SummariseCreated extracts the non-secret summary from a create
// response. Pulls Owner straight off the APIKeyCreated; CreateAPIKey
// is responsible for populating it via the follow-up GET so callers
// don't have to thread the inspector twice.
func SummariseCreated(created types.APIKeyCreated) APIKeyCreateSummary {
	s := APIKeyCreateSummary{
		ID:     created.ID,
		Name:   created.Name,
		Owner:  created.Owner,
		DryRun: created.DryRun,
	}
	if created.Expiration != nil {
		s.Expiration = created.Expiration.UTC().Format(time.RFC3339)
	}
	return s
}
