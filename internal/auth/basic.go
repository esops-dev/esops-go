package auth

import (
	"errors"
	"net/http"

	"github.com/esops-dev/esops-go/internal/config"
)

type basicProvider struct {
	username, password string
}

func newBasic(a config.Auth) (*basicProvider, error) {
	if a.Username == "" {
		return nil, errors.New("auth.type=basic requires username")
	}
	return &basicProvider{username: a.Username, password: a.Password}, nil
}

func (p *basicProvider) Type() string { return TypeBasic }

func (p *basicProvider) Apply(req *http.Request) error {
	req.SetBasicAuth(p.username, p.password)
	return nil
}
