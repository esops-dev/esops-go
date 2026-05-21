package snapshot

import (
	"strings"

	"github.com/esops-dev/esops-go/pkg/types"
)

// alwaysSecretKeys are setting key names that are credentials wherever
// they appear in a repository's settings tree. The list is the union
// of unambiguous credential names emitted by the s3, azure, gcs, and
// hdfs repository plugins on both Elasticsearch and OpenSearch when
// the secret was configured inline (rather than via the cluster's
// keystore — the recommended path which doesn't echo at all).
//
// Match is case-insensitive and operates on the leaf key name; nested
// objects are walked. These names are specific enough that
// false-positive redaction is not a real risk — `access_key` in a
// non-credential context would be unusual.
var alwaysSecretKeys = []string{
	"access_key",
	"secret_key",
	"session_token",
	"client_secret",
	"sas_token",
	"account_key",
	"shared_key",
	"client_email",     // gcs
	"private_key",      // gcs
	"private_key_id",   // gcs
	"credentials_file", // gcs
	"service_account",  // gcs
	"auth_token",
}

// topLevelSecretKeys are setting key names that ARE credentials only
// when they appear at the top of `settings` for a repository. The
// canonical case is the Azure plugin's bare `key` field — it is the
// account key when sat directly under settings (the inline,
// not-recommended configuration path) but `key` as a leaf elsewhere
// in the tree is much more likely to be a public identifier.
//
// `password`, `token`, and `credential` are bundled here for the same
// reason: ambiguous enough that nested matches over-redact, top-level
// matches almost certainly are credentials in the inline path.
//
// On a non-zero depth, these names fall through to the leave-alone
// path. An operator who needs to redact a hypothetical custom plugin's
// nested `password` field can extend this list, but the default leans
// toward not lying about non-secret data.
var topLevelSecretKeys = []string{
	"key", // azure plugin (inline)
	"password",
	"token",
	"credential",
}

// RedactedPlaceholder is what redacted values are replaced with. The
// `[REDACTED]` form is intentionally human-readable so the operator
// notices when scanning output and so a leaked log line reads as "we
// did the right thing", not as a freshly-leaked secret.
const RedactedPlaceholder = "[REDACTED]"

// RedactRepositorySettings walks each repository's Settings map and
// replaces any value whose key matches alwaysSecretKeys (any depth) or
// topLevelSecretKeys (depth 0 only) with RedactedPlaceholder. Returns
// a *deep-enough* copy so the caller's view of the cluster's response
// is unmodified — we don't want to silently change the data that other
// parts of the program may have retained references to.
//
// `--show-secrets` is the operator's escape hatch: callers that want
// the raw map skip this function entirely. Mirrors how `esops config
// view` handles plaintext config secrets.
func RedactRepositorySettings(rows []types.SnapshotRepository) []types.SnapshotRepository {
	if len(rows) == 0 {
		return rows
	}
	out := make([]types.SnapshotRepository, len(rows))
	for i, r := range rows {
		out[i] = types.SnapshotRepository{
			Name:     r.Name,
			Type:     r.Type,
			Settings: redactSettings(r.Settings, 0),
		}
	}
	return out
}

func redactSettings(in map[string]any, depth int) map[string]any {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if isSecretKey(k, depth) {
			out[k] = RedactedPlaceholder
			continue
		}
		switch nested := v.(type) {
		case map[string]any:
			out[k] = redactSettings(nested, depth+1)
		default:
			out[k] = v
		}
	}
	return out
}

// isSecretKey reports whether k should be redacted given its depth in
// the settings tree. Top-level (depth==0) keys are matched against
// both lists; nested keys only against alwaysSecretKeys.
func isSecretKey(k string, depth int) bool {
	lk := strings.ToLower(k)
	for _, candidate := range alwaysSecretKeys {
		if lk == candidate {
			return true
		}
	}
	if depth == 0 {
		for _, candidate := range topLevelSecretKeys {
			if lk == candidate {
				return true
			}
		}
	}
	return false
}
