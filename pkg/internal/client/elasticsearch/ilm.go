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

	esopsclient "github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/internal/clientutil"
	"github.com/esops-dev/esops-go/pkg/types"
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
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("ilm get_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire map[string]ilmPolicyEntryWire
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "ilm get_lifecycle")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
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

// GetPolicy implements client.ILMManager for the single-name lookup.
// Returns ErrNotFound (wrapped) when the cluster 404s, so the business
// layer can produce a precise message.
func (c *Client) GetPolicy(ctx context.Context, name string) (types.ILMPolicy, error) {
	rows, err := c.ListPolicies(ctx, []string{name})
	if err != nil {
		return types.ILMPolicy{}, err
	}
	for _, p := range rows {
		if p.Name == name {
			return p, nil
		}
	}
	return types.ILMPolicy{}, fmt.Errorf("%w: ilm policy %q", esopsclient.ErrNotFound, name)
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
		return types.ILMPolicyPutResult{}, clientutil.ClassifyTransport(err)
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
		return types.ILMPolicyDeleteResult{}, clientutil.ClassifyTransport(err)
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
		return nil, clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := classifyResponse("ilm explain_lifecycle", resp.StatusCode, resp.Body); err != nil {
		return nil, err
	}

	var wire struct {
		Indices map[string]ilmExplainEntryWire `json:"indices"`
	}
	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "ilm explain_lifecycle")
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
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

// ExplainPolicyStream implements client.ILMManager. Streams the
// /<index>/_ilm/explain envelope: {"indices": {"name1": {...}, ...}}.
// Reads the outer object, positions inside `indices`, then decodes
// each per-index entry one at a time. Cluster-wide explain on a busy
// cluster is the canonical case — buffering the full response into
// memory before printing the first line is wasted heap on a 20k-index
// fleet.
func (c *Client) ExplainPolicyStream(ctx context.Context, req types.ILMExplainRequest, yield func(types.ILMExplainEntry) error) error {
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
		return clientutil.ClassifyTransport(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := classifyResponse("ilm explain_lifecycle (stream)", resp.StatusCode, resp.Body); err != nil {
		return err
	}

	body := clientutil.NewBoundedReader(resp.Body, clientutil.DefaultMaxResponseBytes, "ilm explain_lifecycle (stream)")
	dec := json.NewDecoder(body)
	if err := streamObjectKey(dec, "indices", "ilm explain_lifecycle (stream)"); err != nil {
		return err
	}
	// Consume the inner '{'.
	if tok, err := dec.Token(); err != nil {
		return errors.Join(errors.New("decoding ilm explain_lifecycle (stream) indices"), err)
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("ilm explain_lifecycle (stream): expected indices object, got %v", tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return errors.Join(errors.New("decoding ilm explain_lifecycle (stream) key"), err)
		}
		name, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("ilm explain_lifecycle (stream): expected string key, got %v", keyTok)
		}
		var e ilmExplainEntryWire
		if err := dec.Decode(&e); err != nil {
			return errors.Join(errors.New("decoding ilm explain_lifecycle (stream) value"), err)
		}
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
		if err := yield(entry); err != nil {
			return err
		}
	}
	return nil
}

// streamObjectKey advances dec past the opening '{' of an object and
// then through tokens until the named key is consumed — leaving the
// decoder ready to read the value of that key. Used by both ILM and
// ISM streaming explain to position inside `{"indices": {...}}` /
// similar wrappers without buffering the whole response. Returns an
// error if the structure is unexpected or the key is absent.
func streamObjectKey(dec *json.Decoder, key, op string) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.Join(fmt.Errorf("decoding %s response", op), err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%s: expected JSON object, got %v", op, tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return errors.Join(fmt.Errorf("decoding %s key", op), err)
		}
		k, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("%s: expected string key, got %v", op, keyTok)
		}
		if k == key {
			return nil
		}
		// Skip past this value to reach the next key.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return errors.Join(fmt.Errorf("skipping %s value", op), err)
		}
	}
	return fmt.Errorf("%s: expected key %q in response, not found", op, key)
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
// ISM intentionally not implemented — ILM and ISM are deliberately
// kept as separate, dialect-specific capabilities; the factory wires
// the unsupportedISM stub on the ES branch.
var _ esopsclient.ILMManager = (*Client)(nil)
