package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v9/esapi"

	esopsclient "github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// ilmPolicyEntryWire mirrors one entry in the GET /_ilm/policy[/<name>]
// response. The cluster keys the response by policy name; we decode the
// value into this struct and surface Name from the map key.
type ilmPolicyEntryWire struct {
	Version      int            `json:"version"`
	ModifiedDate string         `json:"modified_date"`
	Policy       map[string]any `json:"policy"`
	InUseBy      struct {
		Indices             []string `json:"indices"`
		DataStreams         []string `json:"data_streams"`
		ComposableTemplates []string `json:"composable_templates"`
	} `json:"in_use_by"`
}

// ListPolicies implements client.ILMManager via GET /_ilm/policy[/<names>].
// Empty names returns every policy. The cluster honours comma-separated
// names in the path segment so we join names there rather than firing N
// requests.
func (c *Client) ListPolicies(ctx context.Context, names []string) ([]types.ILMPolicy, error) {
	gl := c.api.ILM.GetLifecycle
	opts := []func(*esapi.ILMGetLifecycleRequest){gl.WithContext(ctx)}
	if len(names) > 0 {
		// The typed ILMGetLifecycle.WithPolicy takes one string; the
		// cluster accepts a comma-separated list at that path segment.
		opts = append(opts, gl.WithPolicy(strings.Join(names, ",")))
	}

	resp, err := c.api.ILM.GetLifecycle(opts...)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("ilm get_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire map[string]ilmPolicyEntryWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding ilm get_lifecycle response"), err)
	}

	out := make([]types.ILMPolicy, 0, len(wire))
	for name, p := range wire {
		out = append(out, types.ILMPolicy{
			Name:         name,
			Version:      p.Version,
			ModifiedDate: p.ModifiedDate,
			InUseBy: types.ILMInUseBy{
				Indices:             p.InUseBy.Indices,
				DataStreams:         p.InUseBy.DataStreams,
				ComposableTemplates: p.InUseBy.ComposableTemplates,
			},
			Definition: p.Policy,
		})
	}
	return out, nil
}

// PutPolicy implements client.ILMManager via PUT /_ilm/policy/<name>.
// Body is forwarded verbatim — the cluster validates the inner shape
// (phases, actions, allowed keys per action). The wire response is
// {"acknowledged":true} on success.
func (c *Client) PutPolicy(ctx context.Context, req types.ILMPolicyPutRequest) (types.ILMPolicyPutResult, error) {
	pl := c.api.ILM.PutLifecycle
	opts := []func(*esapi.ILMPutLifecycleRequest){pl.WithContext(ctx)}

	resp, err := c.api.ILM.PutLifecycle(bytes.NewReader(req.Body), req.Name, opts...)
	if err != nil {
		return types.ILMPolicyPutResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("ilm put_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return types.ILMPolicyPutResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ILMPolicyPutResult{}, errors.Join(errors.New("decoding ilm put_lifecycle response"), err)
	}
	return types.ILMPolicyPutResult{
		Action:       "ilm-policy-put",
		Name:         req.Name,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// DeletePolicy implements client.ILMManager via DELETE /_ilm/policy/<name>.
// The cluster refuses with a 4xx (typically 400) when the policy is
// still attached to one or more indices. classifyResponse surfaces that
// body verbatim so the operator sees the cluster's own list of
// blockers.
func (c *Client) DeletePolicy(ctx context.Context, req types.ILMPolicyDeleteRequest) (types.ILMPolicyDeleteResult, error) {
	dl := c.api.ILM.DeleteLifecycle
	resp, err := c.api.ILM.DeleteLifecycle(req.Name, dl.WithContext(ctx))
	if err != nil {
		return types.ILMPolicyDeleteResult{}, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("ilm delete_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return types.ILMPolicyDeleteResult{}, err
	}

	var wire struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return types.ILMPolicyDeleteResult{}, errors.Join(errors.New("decoding ilm delete_lifecycle response"), err)
	}
	return types.ILMPolicyDeleteResult{
		Action:       "ilm-policy-delete",
		Name:         req.Name,
		Acknowledged: wire.Acknowledged,
	}, nil
}

// ilmExplainEntryWire mirrors one entry from GET /<index>/_ilm/explain.
// Time fields arrive as epoch millis (we don't ask for human=true);
// formatEpochMillis renders them to RFC3339 and ageSince to a friendly
// duration so the operator can scan for "how long has this been stuck?".
type ilmExplainEntryWire struct {
	Index             string         `json:"index"`
	Managed           bool           `json:"managed"`
	Policy            string         `json:"policy"`
	IndexCreationDate int64          `json:"index_creation_date_millis"`
	LifecycleDateMS   int64          `json:"lifecycle_date_millis"`
	Phase             string         `json:"phase"`
	Action            string         `json:"action"`
	Step              string         `json:"step"`
	FailedStep        string         `json:"failed_step"`
	StepInfo          map[string]any `json:"step_info"`
}

// ExplainPolicy implements client.ILMManager via GET /<index>/_ilm/explain.
// An empty Index falls back to "*" so the cluster reports every index it
// knows about — only_managed=true is the right knob for "only those with
// a policy", which the operator opts into via the request flag.
func (c *Client) ExplainPolicy(ctx context.Context, req types.ILMExplainRequest) ([]types.ILMExplainEntry, error) {
	index := req.Index
	if index == "" {
		index = "*"
	}
	el := c.api.ILM.ExplainLifecycle
	opts := []func(*esapi.ILMExplainLifecycleRequest){el.WithContext(ctx)}
	if req.OnlyErrors {
		opts = append(opts, el.WithOnlyErrors(true))
	}
	if req.OnlyManaged {
		opts = append(opts, el.WithOnlyManaged(true))
	}

	resp, err := c.api.ILM.ExplainLifecycle(index, opts...)
	if err != nil {
		return nil, esopsclient.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("ilm explain_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire struct {
		Indices map[string]ilmExplainEntryWire `json:"indices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, errors.Join(errors.New("decoding ilm explain_lifecycle response"), err)
	}

	out := make([]types.ILMExplainEntry, 0, len(wire.Indices))
	for name, e := range wire.Indices {
		entry := types.ILMExplainEntry{
			Index:         name,
			Managed:       e.Managed,
			Policy:        e.Policy,
			LifecycleDate: formatEpochMillis(e.LifecycleDateMS),
			Phase:         e.Phase,
			Action:        e.Action,
			Step:          e.Step,
			FailedStep:    e.FailedStep,
			StepInfo:      e.StepInfo,
		}
		if e.LifecycleDateMS > 0 {
			entry.Age = ageSince(e.LifecycleDateMS)
		}
		out = append(out, entry)
	}
	return out, nil
}

// ageSince renders an age in a compact "12d5h" / "3h" / "45m" form so a
// table column stays single-cell wide. Mirrors what `kubectl get` does
// for AGE on every line — operators can scan for "this has been stuck
// for days" without reading a full RFC3339 timestamp.
//
// 0/negative ms means "the cluster did not report" — return "" so the
// renderer prints "-" instead of a synthetic duration.
func ageSince(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	if d < 0 {
		// Skewed clock between client and cluster — clamp to 0s rather
		// than printing a negative duration.
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd%dh", days, hours)
	case d >= time.Hour:
		hours := int(d / time.Hour)
		minutes := int((d % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// Compile-time guarantee that this Client satisfies the ILM capability.
// ISM intentionally not implemented — see CLAUDE.md §3 and the factory.
var _ esopsclient.ILMManager = (*Client)(nil)
