package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/opensearch-project/opensearch-go/v4/signer"
	"github.com/opensearch-project/opensearch-go/v4/signer/awsv2"

	esopsconfig "github.com/esops-dev/esops-go/internal/config"
)

// sigV4Provider signs requests with AWS SigV4 using the standard AWS
// credential chain (env vars, shared config, IRSA, IMDS). Credentials
// are never read from the esops config file — accepting access keys in
// YAML is a footgun the tool deliberately doesn't enable. Currently
// OpenSearch-only; the ES adapter rejects sigv4 at construction time
// because go-elasticsearch has no first-class SigV4 helper and adding
// one means hand-rolling the canonical-request math.
type sigV4Provider struct {
	region  string
	service string
	signer  signer.Signer
}

func newSigV4(a esopsconfig.Auth) (*sigV4Provider, error) {
	if a.Region == "" {
		return nil, errors.New("auth.type=sigv4 requires region (e.g. eu-west-1)")
	}
	if a.Service == "" {
		return nil, errors.New("auth.type=sigv4 requires service (e.g. es, aoss)")
	}

	// LoadDefaultConfig builds the credential chain but doesn't execute
	// it — Retrieve fires lazily on the first signed request. So
	// constructing a SigV4 provider in a test or dry-run context that
	// never sends a request stays cheap, and a missing IRSA/IMDS
	// surface as an error at the request site rather than at startup.
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(a.Region))
	if err != nil {
		return nil, fmt.Errorf("auth.sigv4: loading AWS config: %w", err)
	}

	s, err := awsv2.NewSignerWithService(awsCfg, a.Service)
	if err != nil {
		return nil, fmt.Errorf("auth.sigv4: building signer: %w", err)
	}
	return &sigV4Provider{region: a.Region, service: a.Service, signer: s}, nil
}

func (p *sigV4Provider) Type() string { return TypeSigV4 }

// Apply signs the request in place. The OS signer reads, hashes, and
// re-buffers the body, so calling Apply on a request whose body has
// already been read once is safe — the previous SignRequest left the
// body re-readable.
func (p *sigV4Provider) Apply(req *http.Request) error {
	return p.signer.SignRequest(req)
}

// Wrap re-signs every outbound request. Implementing TransportWrapper
// (rather than relying on auth.WrapTransport's per-request applyTransport
// fallback) keeps the contract explicit: each retry inside the upstream
// client gets a fresh signature, and signing happens on a clone so the
// caller's *http.Request isn't mutated mid-flight.
func (p *sigV4Provider) Wrap(base http.RoundTripper) http.RoundTripper {
	return &applyTransport{base: base, apply: p.Apply}
}
