package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"

	"github.com/esops-dev/esops-go/pkg/types"
)

// ISMDescribe renders an ISMPolicyDescription. Reuses
// PolicyDescribeOptions and the shared writePolicyDefinition helper
// from ilm_describe.go.
func ISMDescribe(w io.Writer, d types.ISMPolicyDescription, opts PolicyDescribeOptions, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(d)
	case FormatWide:
		if opts.ShowManaged == nil {
			force := true
			opts.ShowManaged = &force
		}
		ismDescribeText(w, d, opts)
		return nil
	case FormatTable, "":
		ismDescribeText(w, d, opts)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ismDescribeText(w io.Writer, d types.ISMPolicyDescription, opts PolicyDescribeOptions) {
	if opts.AutoMax == 0 {
		opts.AutoMax = DefaultPolicyDescribeIndexListing
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "ID:\t%s\n", d.Policy.ID)
	if d.Policy.SeqNo > 0 {
		fprintfTW(tw, "Seq no:\t%d\n", d.Policy.SeqNo)
	}
	if d.Policy.PrimaryTerm > 0 {
		fprintfTW(tw, "Primary term:\t%d\n", d.Policy.PrimaryTerm)
	}
	fprintfTW(tw, "Managed indices:\t%d\n", d.ManagedCount)
	if d.ErrorCount > 0 {
		fprintfTW(tw, "Indices in ERROR:\t%d\n", d.ErrorCount)
	}
	_ = tw.Flush()

	writeISMStates(w, d.Policy.Definition)
	writeISMManagedIndices(w, d.ManagedIndices, opts)
	writeWarnings(w, d.SectionWarnings)
}

// writeISMStates renders the policy's states block as a per-state
// section listing the named actions and outbound transitions. The
// cluster's shape is `{policy: {default_state: "...", states: [{name:
// "hot", actions: [{rollover: {...}}, ...], transitions: [{state_name:
// "warm", conditions: {...}}, ...]}, ...]}}`. When the body doesn't
// match (an exotic future shape or operator-supplied non-object root),
// the renderer falls back to a JSON dump so the section is still
// useful.
func writeISMStates(w io.Writer, def map[string]any) {
	if len(def) == 0 {
		return
	}
	policyBody, ok := def["policy"].(map[string]any)
	if !ok {
		policyBody = def
	}
	statesRaw, ok := policyBody["states"].([]any)
	if !ok || len(statesRaw) == 0 {
		writePolicyDefinition(w, def)
		return
	}
	defaultState := stringField(policyBody, "default_state")

	fprintlnW(w)
	if defaultState != "" {
		fprintfW(w, "States (default: %s):\n", defaultState)
	} else {
		fprintlnW(w, "States:")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, raw := range statesRaw {
		state, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := stringField(state, "name")
		if name == "" {
			name = "-"
		}
		fprintfTW(tw, "  %s:\t\n", name)
		if actions, ok := state["actions"].([]any); ok && len(actions) > 0 {
			fprintfTW(tw, "    actions:\t%s\n", strings.Join(actionNames(actions), ", "))
		}
		if transitions, ok := state["transitions"].([]any); ok && len(transitions) > 0 {
			targets := make([]string, 0, len(transitions))
			for _, tr := range transitions {
				if t, ok := tr.(map[string]any); ok {
					if to := stringField(t, "state_name"); to != "" {
						targets = append(targets, to)
					}
				}
			}
			if len(targets) > 0 {
				fprintfTW(tw, "    → %s\n", strings.Join(targets, ", "))
			}
		}
	}
	_ = tw.Flush()
}

// actionNames flattens the ISM `actions: [{rollover: {...}}, ...]`
// array into a per-step name list. Each action is a single-key object
// where the key is the action verb; non-conforming entries are
// reported as "?" so the count is still operator-useful.
func actionNames(actions []any) []string {
	out := make([]string, 0, len(actions))
	for _, raw := range actions {
		action, ok := raw.(map[string]any)
		if !ok || len(action) == 0 {
			out = append(out, "?")
			continue
		}
		// Single-key map — extract the action verb.
		for k := range action {
			out = append(out, k)
			break
		}
	}
	return out
}

func writeISMManagedIndices(w io.Writer, entries []types.ISMExplainEntry, opts PolicyDescribeOptions) {
	if len(entries) == 0 {
		return
	}
	force := opts.ShowManaged != nil && *opts.ShowManaged
	hide := opts.ShowManaged != nil && !*opts.ShowManaged
	if hide {
		return
	}
	if !force && opts.AutoMax > 0 && len(entries) > opts.AutoMax {
		fprintlnW(w)
		fprintfW(w, "Managed indices:  %d total (pass --show-indices or --output wide for the per-index listing)\n", len(entries))
		return
	}
	fprintlnW(w)
	fprintlnW(w, "Per-index state:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  INDEX\tSTATE\tACTION\tSTEP\tENABLED\n")
	for _, e := range entries {
		state := e.State
		if state == "" {
			state = "-"
		}
		action := e.Action
		if action == "" {
			action = "-"
		}
		step := e.Step
		if step == "" {
			step = "-"
		}
		fprintfTW(tw, "  %s\t%s\t%s\t%s\t%t\n", e.Index, state, action, step, e.Enabled)
	}
	_ = tw.Flush()
}
