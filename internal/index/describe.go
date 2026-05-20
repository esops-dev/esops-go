package index

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DescribeOptions are the business-layer knobs for Describe. Name is
// the operator-supplied positional argument — a single literal index
// or alias name (patterns are refused up-front). IncludeDefaults asks
// the cluster for the full settings tree (configured + inherited
// defaults) instead of the default "what is actually set" view.
//
// Rendering concerns (which mappings depth, whether to show shards)
// live on the renderer's IndexDescribeOptions; they are not the
// aggregator's business and would otherwise sit here as dead fields.
type DescribeOptions struct {
	Name            string
	IncludeDefaults bool
}

// DescribeCapabilities is the read-only capability bundle Describe
// consumes. Pulling each interface in by name rather than depending on
// *client.Client keeps the business layer's contract small and lets
// fakes substitute one capability at a time in tests.
//
// ILM and ISM are always passed; the aggregator tolerates either or
// both returning client.ErrUnsupported (the ES adapter does so for
// ISM, the OS adapter for ILM — the divergence is expected, not an
// error). Tasks/Templates are nil-tolerant so a test or future caller
// can skip them; production callers always pass real inspectors.
type DescribeCapabilities struct {
	Info      client.IndexInspector
	Aliases   client.AliasInspector
	Settings  client.IndexSettingsInspector
	Mappings  client.MappingsInspector
	Shards    client.ShardInspector
	Recovery  client.RecoveryInspector
	Segments  client.SegmentsInspector
	ILM       client.ILMManager
	ISM       client.ISMManager
	Tasks     client.TasksInspector
	Templates client.IndexTemplateInspector
	Dialect   string
}

// Describe is the business-layer entry point for `esops index describe`.
// It validates the operator's input, identifies the concrete index the
// name resolves to (alias expansion is performed by the cluster on the
// /_cat/indices call — no upfront alias probe is needed in the common
// case), then fans out across the read-only inspector capabilities in
// parallel and assembles a neutral IndexDescription.
//
// Refusals:
//   - empty / patternish Name (ErrUsage)
//   - the resolved name does not match any index in the cluster
//     (ErrNotFound)
//   - the name resolves through a multi-target alias with no write
//     index (ErrUsage with a precise message)
//
// Non-fatal collector failures are captured in SectionWarnings rather
// than aborting the whole call — partial-but-loud beats one bad
// section taking the whole view down.
func Describe(ctx context.Context, caps DescribeCapabilities, opts DescribeOptions) (types.IndexDescription, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.IndexDescription{}, client.UsageError("NAME is required (a single literal index or alias name)")
	}
	if client.IsPatternish(name) {
		return types.IndexDescription{}, client.UsageError("NAME must be a literal index or alias name, not a pattern (got %q); use `esops index list` for fan-out", name)
	}

	// One RTT covers literal-name lookup and single-target-alias
	// expansion: /_cat/indices already resolves an alias to the
	// underlying index. Only multi-target aliases need a second call.
	rows, err := caps.Info.Indices(ctx, types.IndexFilter{
		Index:  name,
		Hidden: true, // operator named it explicitly; don't filter dot-prefixed
	})
	if err != nil {
		return types.IndexDescription{}, err
	}
	target, resolvedFrom, err := pickDescribeTarget(ctx, name, rows, caps.Aliases)
	if err != nil {
		return types.IndexDescription{}, err
	}
	closed := strings.EqualFold(target.Status, "close")

	desc := types.IndexDescription{
		Name:            target.Name,
		ResolvedFrom:    resolvedFrom,
		Dialect:         caps.Dialect,
		Info:            target,
		SectionWarnings: map[string]string{},
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	warn := func(section string, err error) {
		mu.Lock()
		desc.SectionWarnings[section] = err.Error()
		mu.Unlock()
	}
	run := func(section string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				warn(section, err)
			}
		}()
	}

	// Settings — always attempted; closed indices still expose settings.
	run("settings", func() error {
		rows, err := caps.Settings.GetSettings(ctx, types.IndexSettingsGetRequest{
			Indices:         []string{target.Name},
			IncludeDefaults: opts.IncludeDefaults,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Index == target.Name {
				desc.Settings = r
				return nil
			}
		}
		if len(rows) > 0 {
			desc.Settings = rows[0]
		}
		return nil
	})

	// Aliases pointing AT this index. AliasFilter.Index is honoured
	// client-side by the adapter (cat-aliases has no index path segment).
	run("aliases", func() error {
		rows, err := caps.Aliases.Aliases(ctx, types.AliasFilter{Index: target.Name})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			desc.Aliases = rows
		}
		return nil
	})

	run("shards", func() error {
		rows, err := caps.Shards.Shards(ctx, types.ShardFilter{Index: target.Name})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			desc.Shards = rows
		}
		return nil
	})

	// Mappings — skipped on closed indices (cluster returns 4xx).
	if !closed && caps.Mappings != nil {
		run("mappings", func() error {
			rows, err := caps.Mappings.GetMappings(ctx, types.MappingFilter{Index: target.Name})
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.Name == target.Name {
					m := r
					desc.Mappings = &m
					return nil
				}
			}
			if len(rows) == 1 {
				m := rows[0]
				desc.Mappings = &m
			}
			return nil
		})
	}

	if !closed && caps.Recovery != nil {
		run("recovery", func() error {
			rep, err := caps.Recovery.Recovery(ctx, types.RecoveryRequest{
				Indices:    target.Name,
				ActiveOnly: true,
			})
			if err != nil {
				return err
			}
			for _, r := range rep.Indices {
				if r.Index == target.Name && len(r.Shards) > 0 {
					rec := r
					desc.Recovery = &rec
					return nil
				}
			}
			return nil
		})
	}

	if !closed && caps.Segments != nil {
		run("segments", func() error {
			rep, err := caps.Segments.Segments(ctx, types.SegmentsRequest{Indices: target.Name})
			if err != nil {
				return err
			}
			for _, s := range rep.Indices {
				if s.Index == target.Name {
					seg := s
					desc.Segments = &seg
					return nil
				}
			}
			return nil
		})
	}

	if caps.ILM != nil {
		run("ilm", func() error {
			rows, err := caps.ILM.ExplainPolicy(ctx, types.ILMExplainRequest{Index: target.Name})
			if err != nil {
				if errors.Is(err, client.ErrUnsupported) {
					return nil
				}
				return err
			}
			for _, r := range rows {
				if r.Index == target.Name {
					e := r
					desc.ILM = &e
					return nil
				}
			}
			return nil
		})
	}

	if caps.ISM != nil {
		run("ism", func() error {
			rows, err := caps.ISM.ExplainPolicy(ctx, types.ISMExplainRequest{Index: target.Name})
			if err != nil {
				if errors.Is(err, client.ErrUnsupported) {
					return nil
				}
				return err
			}
			for _, r := range rows {
				if r.Index == target.Name {
					e := r
					desc.ISM = &e
					return nil
				}
			}
			return nil
		})
	}

	if caps.Tasks != nil {
		run("tasks", func() error {
			tasks, err := caps.Tasks.Tasks(ctx, types.TasksFilter{Detailed: true})
			if err != nil {
				return err
			}
			matched := filterTasksByIndex(tasks, target.Name)
			if len(matched) > 0 {
				desc.Tasks = matched
			}
			return nil
		})
	}

	if caps.Templates != nil {
		run("template", func() error {
			tmpls, err := caps.Templates.GetTemplates(ctx, nil)
			if err != nil {
				return err
			}
			if t, ok := client.MatchHighestPriorityTemplate(target.Name, tmpls, nil); ok {
				desc.MatchedTemplate = &t
			}
			return nil
		})
	}

	wg.Wait()

	if len(desc.SectionWarnings) == 0 {
		desc.SectionWarnings = nil
	}
	return desc, nil
}

// pickDescribeTarget turns the operator-supplied name and the /_cat/
// indices response into a concrete target index plus the alias the
// name came from (when applicable). The cluster has already expanded
// the alias for us in the common cases; we only consult the alias
// inspector when the name resolved to multiple indices (a multi-
// target alias) and we need to pick the write index.
//
// Returns (target, resolvedFrom, error). resolvedFrom is empty when
// name was a literal index.
func pickDescribeTarget(ctx context.Context, name string, rows []types.Index, ai client.AliasInspector) (types.Index, string, error) {
	switch len(rows) {
	case 0:
		return types.Index{}, "", fmt.Errorf("%w: index %q", client.ErrNotFound, name)
	case 1:
		row := rows[0]
		if row.Name == name {
			return row, "", nil
		}
		// Single-target alias: cluster expanded `name` to `row.Name`.
		return row, name, nil
	}

	// Multi-row response: name was a multi-target alias. Find the
	// write index via the alias inspector and match it back against
	// the rows we already have — no extra /_cat/indices call needed.
	if ai == nil {
		return types.Index{}, "", client.UsageError(
			"name %q resolves to %d indices; describe needs a single target", name, len(rows),
		)
	}
	aliases, err := ai.Aliases(ctx, types.AliasFilter{Alias: name})
	if err != nil {
		return types.Index{}, "", err
	}
	writeName, ambiguousErr := pickWriteIndex(name, aliases, rows)
	if ambiguousErr != nil {
		return types.Index{}, "", ambiguousErr
	}
	for _, r := range rows {
		if r.Name == writeName {
			return r, name, nil
		}
	}
	// The alias names a write index the cluster's /_cat/indices
	// response didn't include — race condition (index deleted between
	// calls), surface as not-found rather than guessing.
	return types.Index{}, "", fmt.Errorf("%w: write index %q (from alias %q)", client.ErrNotFound, writeName, name)
}

// pickWriteIndex picks the single concrete index a multi-target alias
// resolves to. Requires exactly one entry with IsWriteIndex=true;
// anything else (zero write indices, more than one) refuses so a typo
// doesn't silently describe an arbitrary backing index.
//
// allRows is passed for the error message — listing the candidates an
// operator could have meant is more useful than just the count.
func pickWriteIndex(name string, aliases []types.Alias, allRows []types.Index) (string, error) {
	writeTargets := make([]string, 0, 1)
	for _, a := range aliases {
		if a.IsWriteIndex {
			writeTargets = append(writeTargets, a.Index)
		}
	}
	if len(writeTargets) == 1 {
		return writeTargets[0], nil
	}
	candidates := make([]string, 0, len(allRows))
	for _, r := range allRows {
		candidates = append(candidates, r.Name)
	}
	return "", client.UsageError(
		"alias %q resolves to %d indices (%s); name the index directly or use `esops index list` to pick one",
		name, len(candidates), strings.Join(candidates, ", "),
	)
}

// filterTasksByIndex keeps tasks whose Description mentions the index
// name as a complete token — see client.DescriptionMentions for the
// boundary rules. Kept as a tiny shim so the call sites read
// uniformly with filterTasksByDataStream in the datastream package.
func filterTasksByIndex(tasks []types.Task, indexName string) []types.Task {
	if indexName == "" {
		return nil
	}
	out := make([]types.Task, 0, len(tasks))
	for _, t := range tasks {
		if client.DescriptionMentions(t.Description, indexName) {
			out = append(out, t)
		}
	}
	return out
}
