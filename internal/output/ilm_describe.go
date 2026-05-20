package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"

	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultPolicyDescribeIndexListing is the cap above which the
// per-index listing in `ilm describe` / `ism describe` collapses to a
// one-line summary. A single policy can attach to thousands of
// indices on log-heavy fleets; 25 keeps the default readable while
// still listing every managed index on a typical workload.
const DefaultPolicyDescribeIndexListing = 25

// PolicyDescribeOptions are the renderer-side knobs both ILM and ISM
// describes hand down. ShowManaged toggles the per-managed-index
// listing; nil = auto (show iff ManagedCount ≤ AutoMax), &true forces,
// &false hides. AutoMax = 0 falls back to
// DefaultPolicyDescribeIndexListing.
type PolicyDescribeOptions struct {
	ShowManaged *bool
	AutoMax     int
}

// ILMDescribe renders an ILMPolicyDescription in the requested format.
// Wide forces the per-managed-index listing.
func ILMDescribe(w io.Writer, d types.ILMPolicyDescription, opts PolicyDescribeOptions, f Format) error {
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
		return ilmDescribeText(w, d, opts)
	case FormatTable, "":
		return ilmDescribeText(w, d, opts)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func ilmDescribeText(w io.Writer, d types.ILMPolicyDescription, opts PolicyDescribeOptions) error {
	if opts.AutoMax == 0 {
		opts.AutoMax = DefaultPolicyDescribeIndexListing
	}
	// Header
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "Name:\t%s\n", d.Policy.Name)
	if d.Policy.Version > 0 {
		fprintfTW(tw, "Version:\t%d\n", d.Policy.Version)
	}
	if d.Policy.ModifiedDate != "" {
		fprintfTW(tw, "Modified:\t%s\n", d.Policy.ModifiedDate)
	}
	fprintfTW(tw, "Managed indices:\t%d\n", d.ManagedCount)
	if d.ErrorCount > 0 {
		fprintfTW(tw, "Indices in ERROR:\t%d\n", d.ErrorCount)
	}
	_ = tw.Flush()

	writeILMInUseBy(w, d.Policy.InUseBy)
	writeILMPhases(w, d.Policy.Definition)
	writeILMManagedIndices(w, d.ManagedIndices, opts)
	writeWarnings(w, d.SectionWarnings)
	return nil
}

// writeILMPhases renders the policy's phases block as a per-phase
// section with min_age + named action list. The cluster's shape
// is `{policy: {phases: {hot: {min_age: "0ms", actions: {rollover:
// {...}, set_priority: {...}}}, warm: {...}, ...}}}`. When the body
// doesn't match (an exotic future shape or operator-supplied non-
// object root), the renderer falls back to a JSON dump so the
// section is still useful.
func writeILMPhases(w io.Writer, def map[string]any) {
	if len(def) == 0 {
		return
	}
	phases, ok := nestedMap(def, "policy", "phases")
	if !ok {
		phases, ok = nestedMap(def, "phases")
	}
	if !ok {
		writePolicyDefinition(w, "Policy body", def)
		return
	}
	order := []string{"hot", "warm", "cold", "frozen", "delete"}
	seen := map[string]struct{}{}
	for _, k := range order {
		seen[k] = struct{}{}
	}
	for k := range phases {
		if _, ok := seen[k]; !ok {
			order = append(order, k)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Phases:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rendered := false
	for _, name := range order {
		raw, ok := phases[name]
		if !ok {
			continue
		}
		rendered = true
		body, _ := raw.(map[string]any)
		minAge := stringField(body, "min_age")
		if minAge == "" {
			minAge = "-"
		}
		fprintfTW(tw, "  %s:\t\n", name)
		fprintfTW(tw, "    min_age:\t%s\n", minAge)
		if actions, ok := body["actions"].(map[string]any); ok && len(actions) > 0 {
			names := make([]string, 0, len(actions))
			for an := range actions {
				names = append(names, an)
			}
			sort.Strings(names)
			fprintfTW(tw, "    actions:\t%s\n", strings.Join(names, ", "))
		}
	}
	_ = tw.Flush()
	if !rendered {
		// Phases key present but empty — surface the raw body so
		// nothing operator-relevant is silently dropped.
		writePolicyDefinition(w, "Policy body", def)
	}
}

// nestedMap walks a series of map keys and returns the final
// map[string]any if every step resolves. Returns ok=false the moment
// a step is missing or the wrong type.
func nestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		raw, ok := cur[k]
		if !ok {
			return nil, false
		}
		cur, ok = raw.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func writeILMInUseBy(w io.Writer, u types.ILMInUseBy) {
	if len(u.Indices) == 0 && len(u.DataStreams) == 0 && len(u.ComposableTemplates) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "In use by:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(u.Indices) > 0 {
		fprintfTW(tw, "  Indices:\t%d  (%s)\n",
			len(u.Indices), truncatePolicyRefs(u.Indices))
	}
	if len(u.DataStreams) > 0 {
		fprintfTW(tw, "  Data streams:\t%d  (%s)\n",
			len(u.DataStreams), truncatePolicyRefs(u.DataStreams))
	}
	if len(u.ComposableTemplates) > 0 {
		fprintfTW(tw, "  Composable templates:\t%d  (%s)\n",
			len(u.ComposableTemplates), truncatePolicyRefs(u.ComposableTemplates))
	}
	_ = tw.Flush()
}

// truncatePolicyRefs renders the in_use_by reference list compactly:
// show up to 5 names, then `… N more` so a policy bound to 200 indices
// doesn't flood the header.
func truncatePolicyRefs(names []string) string {
	const max = 5
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(", … %d more", len(names)-max)
}

func writePolicyDefinition(w io.Writer, label string, def map[string]any) {
	if len(def) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s:\n", label)
	body, err := json.MarshalIndent(def, "  ", "  ")
	if err != nil {
		fmt.Fprintf(w, "  (failed to render: %v)\n", err)
		return
	}
	fmt.Fprintf(w, "  %s\n", body)
}

func writeILMManagedIndices(w io.Writer, entries []types.ILMExplainEntry, opts PolicyDescribeOptions) {
	if len(entries) == 0 {
		return
	}
	force := opts.ShowManaged != nil && *opts.ShowManaged
	hide := opts.ShowManaged != nil && !*opts.ShowManaged
	if hide {
		return
	}
	if !force && opts.AutoMax > 0 && len(entries) > opts.AutoMax {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Managed indices:  %d total (pass --show-indices or --output wide for the per-index listing)\n", len(entries))
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Per-index state:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fprintfTW(tw, "  INDEX\tPHASE\tACTION\tSTEP\tAGE\n")
	for _, e := range entries {
		step := e.Step
		if e.FailedStep != "" {
			step = e.FailedStep + " (FAILED)"
		}
		if step == "" {
			step = "-"
		}
		phase := e.Phase
		if phase == "" {
			phase = "-"
		}
		action := e.Action
		if action == "" {
			action = "-"
		}
		age := e.Age
		if age == "" {
			age = "-"
		}
		fprintfTW(tw, "  %s\t%s\t%s\t%s\t%s\n", e.Index, phase, action, step, age)
	}
	_ = tw.Flush()
}
