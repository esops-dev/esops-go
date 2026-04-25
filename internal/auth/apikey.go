package auth

import (
	"errors"
	"net/http"

	"github.com/dj-wasabi/esops-go/internal/config"
)

// apiKeyProvider sends "Authorization: ApiKey <encoded>" — Elasticsearch's
// native API-key authentication. The cluster expects the base64-encoded
// "<id>:<api_key>" form (the `encoded` field returned by
// POST /_security/api_key); we accept that verbatim rather than
// auto-detecting raw vs. encoded, since a string with one colon could
// be either and a wrong guess produces a 401 with no clue why.
type apiKeyProvider struct {
	encoded string
}

func newAPIKey(a config.Auth) (*apiKeyProvider, error) {
	if a.APIKey == "" {
		return nil, errors.New("auth.type=api_key requires api_key (use the base64-encoded form returned by ES as `encoded`)")
	}
	return &apiKeyProvider{encoded: a.APIKey}, nil
}

func (p *apiKeyProvider) Type() string { return TypeAPIKey }

func (p *apiKeyProvider) Apply(req *http.Request) error {
	req.Header.Set("Authorization", "ApiKey "+p.encoded)
	return nil
}
