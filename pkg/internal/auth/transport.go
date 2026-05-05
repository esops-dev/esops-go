package auth

import "net/http"

// applyTransport is the default RoundTripper used when a Provider does
// not implement TransportWrapper: it calls Provider.Apply on a clone of
// the outbound request, then delegates to base. The clone matters
// because the upstream clients may retry the same *http.Request and our
// header changes would otherwise stack across attempts.
type applyTransport struct {
	base  http.RoundTripper
	apply func(*http.Request) error
}

func (t *applyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if err := t.apply(clone); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(clone)
}
