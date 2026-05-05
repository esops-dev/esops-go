package auth

import "net/http"

// mtlsProvider is a no-op at the HTTP layer. mTLS identity travels on
// the TLS keypair, which is loaded into the *tls.Config in
// pkg/cluster.buildTLSConfig — by the time a request reaches this
// provider's Apply, the client cert has already been presented during
// the TLS handshake. The provider exists so adapters can call
// auth.From(cfg.Auth) uniformly without special-casing mtls.
type mtlsProvider struct{}

func newMTLS() mtlsProvider { return mtlsProvider{} }

func (mtlsProvider) Type() string              { return TypeMTLS }
func (mtlsProvider) Apply(*http.Request) error { return nil }
