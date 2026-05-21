package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// The unsupported stubs are the way the factory keeps the CLI surface
// dialect-agnostic: every Client always exposes both ILM and ISM, but
// the wrong-dialect side returns ErrUnsupported with a hint pointing at
// the right command. Verifying this contract here means a future
// refactor that nils-out a stub will break the build, not silently
// surface a nil-deref at runtime.

func TestUnsupportedILM_AllMethodsReturnUnsupported(t *testing.T) {
	stub := unsupportedILM{}
	ctx := context.Background()

	if _, err := stub.ListPolicies(ctx, nil); !errors.Is(err, client.ErrUnsupported) || !strings.Contains(err.Error(), "ism") {
		t.Errorf("ListPolicies err = %v, want ErrUnsupported with 'ism' hint", err)
	}
	if _, err := stub.PutPolicy(ctx, types.ILMPolicyPutRequest{}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("PutPolicy err = %v, want ErrUnsupported", err)
	}
	if _, err := stub.DeletePolicy(ctx, types.ILMPolicyDeleteRequest{}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("DeletePolicy err = %v, want ErrUnsupported", err)
	}
	if _, err := stub.ExplainPolicy(ctx, types.ILMExplainRequest{}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("ExplainPolicy err = %v, want ErrUnsupported", err)
	}
}

func TestUnsupportedISM_AllMethodsReturnUnsupported(t *testing.T) {
	stub := unsupportedISM{}
	ctx := context.Background()

	if _, err := stub.ListPolicies(ctx, nil); !errors.Is(err, client.ErrUnsupported) || !strings.Contains(err.Error(), "ilm") {
		t.Errorf("ListPolicies err = %v, want ErrUnsupported with 'ilm' hint", err)
	}
	if _, err := stub.PutPolicy(ctx, types.ISMPolicyPutRequest{}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("PutPolicy err = %v, want ErrUnsupported", err)
	}
	if _, err := stub.DeletePolicy(ctx, types.ISMPolicyDeleteRequest{}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("DeletePolicy err = %v, want ErrUnsupported", err)
	}
	if _, err := stub.ExplainPolicy(ctx, types.ISMExplainRequest{}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("ExplainPolicy err = %v, want ErrUnsupported", err)
	}
}

func TestUnsupportedDeprecations_ReturnsUnsupported(t *testing.T) {
	stub := unsupportedDeprecations{}
	ctx := context.Background()

	got, err := stub.Deprecations(ctx, types.DeprecationsRequest{})
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("Deprecations err = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "Elasticsearch-only") {
		t.Errorf("Deprecations err = %q, want hint mentioning 'Elasticsearch-only'", err.Error())
	}
	if got != nil {
		t.Errorf("Deprecations issues = %v, want nil on unsupported", got)
	}
}

func TestUnsupportedAPIKeys_ReturnsUnsupported(t *testing.T) {
	stub := unsupportedAPIKeys{}
	ctx := context.Background()

	got, err := stub.APIKeys(ctx)
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("APIKeys err = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "Elasticsearch-only") {
		t.Errorf("APIKeys err = %q, want hint mentioning 'Elasticsearch-only'", err.Error())
	}
	if got != nil {
		t.Errorf("APIKeys = %v, want nil on unsupported", got)
	}
}

func TestUnsupportedServiceTokens_ReturnsUnsupported(t *testing.T) {
	stub := unsupportedServiceTokens{}
	ctx := context.Background()

	got, err := stub.ServiceTokens(ctx)
	if !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("ServiceTokens err = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "Elasticsearch-only") {
		t.Errorf("ServiceTokens err = %q, want hint mentioning 'Elasticsearch-only'", err.Error())
	}
	if got != nil {
		t.Errorf("ServiceTokens = %v, want nil on unsupported", got)
	}
}

func TestUnsupportedAPIKeyManager_ReturnsUnsupported(t *testing.T) {
	stub := unsupportedAPIKeyManager{}
	ctx := context.Background()

	if _, err := stub.CreateAPIKey(ctx, types.APIKeyCreateRequest{Name: "k"}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("CreateAPIKey err = %v, want ErrUnsupported", err)
	} else if !strings.Contains(err.Error(), "Elasticsearch-only") {
		t.Errorf("CreateAPIKey err = %q, want hint mentioning 'Elasticsearch-only'", err.Error())
	}
	if _, err := stub.InvalidateAPIKey(ctx, types.APIKeyInvalidateRequest{IDs: []string{"id"}}); !errors.Is(err, client.ErrUnsupported) {
		t.Errorf("InvalidateAPIKey err = %v, want ErrUnsupported", err)
	}
}

// Compile-time guarantees that the stubs satisfy the capability
// interfaces. A future capability addition that breaks the contract
// will fail here, not deep in the factory call site.
var (
	_ client.ILMManager            = unsupportedILM{}
	_ client.ISMManager            = unsupportedISM{}
	_ client.DeprecationInspector  = unsupportedDeprecations{}
	_ client.APIKeyInspector       = unsupportedAPIKeys{}
	_ client.APIKeyManager         = unsupportedAPIKeyManager{}
	_ client.ServiceTokenInspector = unsupportedServiceTokens{}
)
