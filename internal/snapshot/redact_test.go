package snapshot_test

import (
	"testing"

	"github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestRedactRepositorySettings_redactsKnownSecretsAtAnyDepth(t *testing.T) {
	in := []types.SnapshotRepository{{
		Name: "s3-prod", Type: "s3",
		Settings: map[string]any{
			"bucket":     "backups",
			"region":     "eu-west-1",
			"access_key": "AKIAIOSFODNN7EXAMPLE",
			"secret_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
	}}
	got := snapshot.RedactRepositorySettings(in)
	if got[0].Settings["access_key"] != snapshot.RedactedPlaceholder {
		t.Errorf("access_key not redacted: %v", got[0].Settings["access_key"])
	}
	if got[0].Settings["secret_key"] != snapshot.RedactedPlaceholder {
		t.Errorf("secret_key not redacted: %v", got[0].Settings["secret_key"])
	}
	if got[0].Settings["bucket"] != "backups" {
		t.Errorf("non-secret key was clobbered: %v", got[0].Settings["bucket"])
	}
}

func TestRedactRepositorySettings_walksNestedAccessKeys(t *testing.T) {
	// The s3 plugin nests inline credentials under client.<n>.{access_key,secret_key}
	// (rare in practice — keystore is the recommended path). Verify the
	// universal credential names redact at any depth.
	in := []types.SnapshotRepository{{
		Name: "s3", Type: "s3",
		Settings: map[string]any{
			"client": map[string]any{
				"default": map[string]any{
					"endpoint":   "s3.eu-west-1.amazonaws.com",
					"access_key": "leak",
					"secret_key": "leak2",
				},
			},
		},
	}}
	got := snapshot.RedactRepositorySettings(in)
	def := got[0].Settings["client"].(map[string]any)["default"].(map[string]any)
	if def["access_key"] != snapshot.RedactedPlaceholder {
		t.Errorf("nested access_key not redacted: %v", def["access_key"])
	}
	if def["secret_key"] != snapshot.RedactedPlaceholder {
		t.Errorf("nested secret_key not redacted: %v", def["secret_key"])
	}
	if def["endpoint"] != "s3.eu-west-1.amazonaws.com" {
		t.Errorf("nested non-secret was clobbered: %v", def["endpoint"])
	}
}

func TestRedactRepositorySettings_topLevelBareKeyIsRedacted(t *testing.T) {
	// Azure plugin's bare `key` at the top of settings is the inline
	// account key — the case the redactor exists for.
	in := []types.SnapshotRepository{{
		Name: "az", Type: "azure",
		Settings: map[string]any{
			"account":   "publicname",
			"container": "backups",
			"key":       "shhh-this-is-the-account-key",
		},
	}}
	got := snapshot.RedactRepositorySettings(in)
	if got[0].Settings["key"] != snapshot.RedactedPlaceholder {
		t.Errorf("top-level azure 'key' not redacted: %v", got[0].Settings["key"])
	}
	if got[0].Settings["account"] != "publicname" {
		t.Errorf("non-secret 'account' was clobbered: %v", got[0].Settings["account"])
	}
}

func TestRedactRepositorySettings_nestedBareKeyIsNotRedacted(t *testing.T) {
	// Regression guard: a nested `key` field is much more likely to be a
	// public identifier than a credential. The earlier all-depths rule
	// over-redacted these. Use a realistic shape: an index_lookup-style
	// nested config with a `key` field.
	in := []types.SnapshotRepository{{
		Name: "custom", Type: "custom-plugin",
		Settings: map[string]any{
			"index_lookup": map[string]any{
				"key":   "user_id",
				"index": "users",
			},
		},
	}}
	got := snapshot.RedactRepositorySettings(in)
	nested := got[0].Settings["index_lookup"].(map[string]any)
	if nested["key"] != "user_id" {
		t.Errorf("nested 'key' was redacted (over-aggressive): got %v, want user_id", nested["key"])
	}
}

func TestRedactRepositorySettings_topLevelPasswordTokenCredentialAreRedacted(t *testing.T) {
	// password/token/credential at the top level cover the ambiguous
	// inline path for plugins that name credentials with these labels.
	in := []types.SnapshotRepository{{
		Name: "x", Type: "custom",
		Settings: map[string]any{
			"password":   "p",
			"token":      "t",
			"credential": "c",
		},
	}}
	got := snapshot.RedactRepositorySettings(in)
	for _, k := range []string{"password", "token", "credential"} {
		if got[0].Settings[k] != snapshot.RedactedPlaceholder {
			t.Errorf("top-level %q not redacted: %v", k, got[0].Settings[k])
		}
	}
}

func TestRedactRepositorySettings_nestedAmbiguousNamesAreNotRedacted(t *testing.T) {
	// A nested `token` could legitimately be a public identifier
	// (CSRF token name, OAuth issuer alias, etc.). Don't over-redact.
	in := []types.SnapshotRepository{{
		Name: "x", Type: "custom",
		Settings: map[string]any{
			"oauth": map[string]any{
				"token": "issuer-alias",  // public
				"scope": "snapshots:read",
			},
		},
	}}
	got := snapshot.RedactRepositorySettings(in)
	oauth := got[0].Settings["oauth"].(map[string]any)
	if oauth["token"] != "issuer-alias" {
		t.Errorf("nested 'token' was redacted (over-aggressive): got %v, want issuer-alias", oauth["token"])
	}
}

func TestRedactRepositorySettings_doesNotMutateInput(t *testing.T) {
	original := map[string]any{"access_key": "ORIGINAL"}
	in := []types.SnapshotRepository{{Name: "x", Type: "s3", Settings: original}}
	_ = snapshot.RedactRepositorySettings(in)
	if original["access_key"] != "ORIGINAL" {
		t.Errorf("redaction mutated caller's map: %v", original["access_key"])
	}
}

func TestRedactRepositorySettings_caseInsensitive(t *testing.T) {
	in := []types.SnapshotRepository{{
		Name: "x", Type: "s3",
		Settings: map[string]any{"Access_Key": "leak"},
	}}
	got := snapshot.RedactRepositorySettings(in)
	if got[0].Settings["Access_Key"] != snapshot.RedactedPlaceholder {
		t.Errorf("case-mismatched key not redacted: %v", got[0].Settings["Access_Key"])
	}
}
