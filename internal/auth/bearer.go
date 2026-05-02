package auth

import (
	"errors"
	"net/http"

	"github.com/esops-dev/esops-go/internal/config"
)

// bearerProvider sends "Authorization: Bearer <token>". Works on any
// cluster fronted by bearer-issuing auth: ES service tokens, OIDC
// proxies, Keycloak/Okta in front of the cluster. We don't branch on
// dialect — the wire format is identical, and ES's typed ServiceToken
// field on its config is just a convenience that produces the same
// header, so collapsing both into one provider is lossless.
type bearerProvider struct {
	token string
}

func newBearer(a config.Auth) (*bearerProvider, error) {
	if a.Token == "" {
		return nil, errors.New("auth.type=bearer requires token")
	}
	return &bearerProvider{token: a.Token}, nil
}

func (p *bearerProvider) Type() string { return TypeBearer }

func (p *bearerProvider) Apply(req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+p.token)
	return nil
}
