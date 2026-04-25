// Package auth builds and applies cluster authentication. Each
// supported method (basic, API key, bearer, mTLS, AWS SigV4) lives in
// its own file as a small Provider; auth.From turns a parsed
// config.Auth into the right one. Providers attach credentials at the
// HTTP request layer (Apply) or wrap the transport (TransportWrapper)
// — SigV4 needs the latter so each retry gets a fresh signature.
package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/config"
)

// Type identifiers used in config and adapter compatibility checks.
// The string values are what users write in `auth.type`.
const (
	TypeNone   = ""
	TypeBasic  = "basic"
	TypeAPIKey = "api_key"
	TypeBearer = "bearer"
	TypeMTLS   = "mtls"
	TypeSigV4  = "sigv4"
)

// Provider authenticates outbound requests to a cluster.
type Provider interface {
	// Apply attaches credentials to a single request. Used by the probe
	// path, which hits GET / with plain net/http before the typed client
	// exists. For providers that also implement TransportWrapper, Apply
	// is the building block Wrap composes from.
	Apply(req *http.Request) error

	// Type returns the canonical name of the auth method (one of the
	// Type* constants). Adapters use it to enforce dialect compatibility
	// at construction time, after the probe has identified the cluster
	// — api_key is ES-only, sigv4 is OS-only.
	Type() string
}

// TransportWrapper is implemented by providers that drive auth through
// a RoundTripper rather than a one-off Apply. SigV4 requires this
// because each signed request carries a timestamp and must be re-signed
// on retry; for header-only providers Wrap is a thin per-request
// wrapper around Apply, kept for symmetry between probe and adapter.
type TransportWrapper interface {
	Wrap(base http.RoundTripper) http.RoundTripper
}

// From builds a Provider from the parsed config.Auth.
//
// config.ValidateAuth is expected to have run at config.Load — From
// repeats only the bare-minimum required-field checks that catch a
// Provider being built from a hand-rolled (non-Loaded) config.Auth.
func From(a config.Auth) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(a.Type)) {
	case TypeNone, "none":
		return noneProvider{}, nil
	case TypeBasic:
		return newBasic(a)
	case TypeAPIKey:
		return newAPIKey(a)
	case TypeBearer:
		return newBearer(a)
	case TypeMTLS:
		return newMTLS(), nil
	case TypeSigV4:
		return newSigV4(a)
	default:
		return nil, fmt.Errorf("auth: unknown type %q", a.Type)
	}
}

// WrapTransport returns base wrapped with whatever transport-layer auth
// the provider needs. Providers that implement TransportWrapper get
// their Wrap; others fall through to a generic per-request applier
// that calls Provider.Apply on each round-trip. base may be nil — the
// helper substitutes http.DefaultTransport in that case so callers can
// pass a freshly-constructed transport without separately checking it.
func WrapTransport(p Provider, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if w, ok := p.(TransportWrapper); ok {
		return w.Wrap(base)
	}
	return &applyTransport{base: base, apply: p.Apply}
}

// noneProvider is the type=none/empty case. Apply does nothing; Type
// returns "" so adapters can branch on it explicitly.
type noneProvider struct{}

func (noneProvider) Type() string              { return TypeNone }
func (noneProvider) Apply(*http.Request) error { return nil }

// Wrap on noneProvider is a passthrough; we still expose it so the
// probe path can call WrapTransport unconditionally.
func (noneProvider) Wrap(base http.RoundTripper) http.RoundTripper { return base }
