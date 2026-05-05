package config

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateAuth enforces the per-type required/forbidden field rules for
// an auth block. It runs at config load (against parsed, pre-resolution
// values) so misconfigured contexts fail before any HTTP call rather
// than surprising the operator mid-incident.
//
// Field presence is checked, not value validity: ${env:VAR} counts as
// present even if the env var isn't set yet — that's the point of
// indirection. The required/forbidden tables for each type are:
//
//	basic   : required=username                          forbidden=api_key,token,region,service
//	api_key : required=api_key                           forbidden=username,password,token,region,service
//	bearer  : required=token                             forbidden=username,password,api_key,region,service
//	mtls    : required=client_cert,client_key            forbidden=username,password,api_key,token,region,service
//	sigv4   : required=region,service                    forbidden=username,password,api_key,token
//	(empty) : everything allowed                         (auth block treated as absent)
//
// Dialect-specific rejections (api_key on OS, sigv4 on ES) are not
// enforced here — at config-load time the dialect is unknown. Adapter
// construction (after the probe) handles those.
func ValidateAuth(a Auth) error {
	t := strings.ToLower(strings.TrimSpace(a.Type))
	if t == "" || t == "none" {
		return nil
	}

	rule, ok := authRules[t]
	if !ok {
		return fmt.Errorf("auth.type=%q is not a known method (supported: basic, api_key, bearer, mtls, sigv4)", a.Type)
	}

	missing := rule.missingRequired(a)
	if len(missing) > 0 {
		return fmt.Errorf("auth.type=%s requires %s", t, joinAnd(missing))
	}

	forbidden := rule.setForbidden(a)
	if len(forbidden) > 0 {
		return fmt.Errorf("auth.type=%s does not accept %s; remove the field(s) or change auth.type", t, joinAnd(forbidden))
	}
	return nil
}

// validateAllAuth runs ValidateAuth against every context in cfg and
// returns the first failure prefixed with the offending context name.
// Contexts are visited in alphabetical order so the error message is
// stable across runs — operators paste these into tickets.
func validateAllAuth(cfg Config) error {
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ValidateAuth(cfg.Contexts[name].Auth); err != nil {
			return fmt.Errorf("context %q: %w", name, err)
		}
	}
	return nil
}

// authRule encodes the required/forbidden field policy for one auth
// type. fieldRefs are pointers into a fresh Auth pulled by the rule's
// extractor functions; we use closures rather than reflection to keep
// the validator boring and grep-able.
type authRule struct {
	required  []authField
	forbidden []authField
}

// authField pairs a field's user-facing name (as it appears in YAML)
// with an extractor that returns its raw string value from an Auth
// struct. The extractor lets validation read every named field without
// reflection.
type authField struct {
	name string
	get  func(Auth) string
}

var (
	fieldUsername   = authField{"username", func(a Auth) string { return a.Username }}
	fieldPassword   = authField{"password", func(a Auth) string { return a.Password }}
	fieldAPIKey     = authField{"api_key", func(a Auth) string { return a.APIKey }}
	fieldToken      = authField{"token", func(a Auth) string { return a.Token }}
	fieldClientCert = authField{"client_cert", func(a Auth) string { return a.ClientCert }}
	fieldClientKey  = authField{"client_key", func(a Auth) string { return a.ClientKey }}
	fieldRegion     = authField{"region", func(a Auth) string { return a.Region }}
	fieldService    = authField{"service", func(a Auth) string { return a.Service }}
)

// authRules is the source of truth for per-type field policy. New auth
// methods land as a single map entry plus a provider in pkg/internal/auth.
var authRules = map[string]authRule{
	"basic": {
		required:  []authField{fieldUsername},
		forbidden: []authField{fieldAPIKey, fieldToken, fieldClientCert, fieldClientKey, fieldRegion, fieldService},
	},
	"api_key": {
		required:  []authField{fieldAPIKey},
		forbidden: []authField{fieldUsername, fieldPassword, fieldToken, fieldClientCert, fieldClientKey, fieldRegion, fieldService},
	},
	"bearer": {
		required:  []authField{fieldToken},
		forbidden: []authField{fieldUsername, fieldPassword, fieldAPIKey, fieldClientCert, fieldClientKey, fieldRegion, fieldService},
	},
	"mtls": {
		required:  []authField{fieldClientCert, fieldClientKey},
		forbidden: []authField{fieldUsername, fieldPassword, fieldAPIKey, fieldToken, fieldRegion, fieldService},
	},
	"sigv4": {
		required:  []authField{fieldRegion, fieldService},
		forbidden: []authField{fieldUsername, fieldPassword, fieldAPIKey, fieldToken, fieldClientCert, fieldClientKey},
	},
}

func (r authRule) missingRequired(a Auth) []string {
	var missing []string
	for _, f := range r.required {
		if strings.TrimSpace(f.get(a)) == "" {
			missing = append(missing, f.name)
		}
	}
	return missing
}

func (r authRule) setForbidden(a Auth) []string {
	var set []string
	for _, f := range r.forbidden {
		if strings.TrimSpace(f.get(a)) != "" {
			set = append(set, f.name)
		}
	}
	return set
}

// joinAnd renders a list of field names as "a", "a and b", "a, b, and
// c" — operator-readable grammar for an error message that's already
// going to be quoted in a chat thread or ticket.
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}
