package datastream

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// MaxRecoveryProbeBackingIndices caps the number of backing-index
// names we forward to a single /_recovery probe. The cluster accepts
// a comma-joined index list at the path segment, so an unbounded
// fan-out (think a rollover-looped stream with thousands of frozen
// generations) would build a multi-megabyte URL the cluster or an
// intermediate proxy will refuse with a cryptic 414. Recovery is a
// transient-state probe — older write-blocked generations cannot
// be recovering, so trimming to the most recent N backing indices
// is the operator-useful slice. When the cap kicks in, the
// describe surfaces a section warning naming how many were skipped.
const MaxRecoveryProbeBackingIndices = 200

// DescribeOptions is the business-layer input to Describe. Name is the
// operator-supplied positional argument — a single literal data stream
// name. Patterns are refused up-front.
type DescribeOptions struct {
	Name string
}

// DescribeCapabilities is the read-only capability bundle the
// aggregator consumes. DataStream + Indices are mandatory (the cluster
// answers come from these); Templates / Recovery / Tasks are
// nil-tolerant.
type DescribeCapabilities struct {
	DataStream client.DataStreamInspector
	Indices    client.IndexInspector
	Templates  client.IndexTemplateInspector
	Recovery   client.RecoveryInspector
	Tasks      client.TasksInspector
	Dialect    string
}

// Describe is the business-layer entry point for
// `esops datastream describe`. Resolves the stream via the existing
// Get helper (which enriches backing-index health/size), then fans
// out matching template + recovery + tasks in parallel.
//
// Refusals: empty / patternish Name surface as ErrUsage; the
// downstream Get propagates ErrNotFound on a missing stream.
//
// Non-fatal collector failures (template lookup, recovery probe,
// tasks listing) land in SectionWarnings rather than aborting.
func Describe(ctx context.Context, caps DescribeCapabilities, opts DescribeOptions) (types.DataStreamDescription, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.DataStreamDescription{}, client.UsageError("NAME is required (a single literal data stream name)")
	}
	if client.IsPatternish(name) {
		return types.DataStreamDescription{}, client.UsageError("NAME must be a literal data stream name, not a pattern (got %q); use `esops datastream list` for fan-out", name)
	}

	stream, err := Get(ctx, caps.DataStream, caps.Indices, name)
	if err != nil {
		return types.DataStreamDescription{}, err
	}

	desc := types.DataStreamDescription{
		Name:            stream.Name,
		Dialect:         caps.Dialect,
		Stream:          stream,
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

	if caps.Templates != nil {
		run("template", func() error {
			tmpls, err := caps.Templates.GetTemplates(ctx, nil)
			if err != nil {
				return err
			}
			pred := func(t types.IndexTemplate) bool { return len(t.DataStream) > 0 }
			if t, ok := client.MatchHighestPriorityTemplate(stream.Name, tmpls, pred); ok {
				desc.MatchedTemplate = &t
			}
			return nil
		})
	}

	if caps.Recovery != nil && len(stream.Indices) > 0 {
		run("recoveries", func() error {
			// `stream.Indices` is reported oldest-first by ES/OS; trim
			// to the most recent generations when over the cap so the
			// probe URL doesn't blow past proxy limits on a stream with
			// thousands of rolled-over backings.
			backings := stream.Indices
			skipped := 0
			if len(backings) > MaxRecoveryProbeBackingIndices {
				skipped = len(backings) - MaxRecoveryProbeBackingIndices
				backings = backings[len(backings)-MaxRecoveryProbeBackingIndices:]
			}
			names := make([]string, 0, len(backings))
			for _, b := range backings {
				names = append(names, b.Name)
			}
			rep, err := caps.Recovery.Recovery(ctx, types.RecoveryRequest{
				Indices:    strings.Join(names, ","),
				ActiveOnly: true,
			})
			if err != nil {
				return err
			}
			active := make([]types.IndexRecovery, 0, len(rep.Indices))
			for _, r := range rep.Indices {
				if len(r.Shards) > 0 {
					active = append(active, r)
				}
			}
			if len(active) > 0 {
				sort.SliceStable(active, func(i, j int) bool {
					return active[i].Index < active[j].Index
				})
				desc.Recoveries = active
			}
			if skipped > 0 {
				warn("recoveries", fmt.Errorf("recovery probe limited to the %d most recent backing indices; %d older generations skipped to keep the cluster URL bounded", MaxRecoveryProbeBackingIndices, skipped))
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
			matched := filterTasksByDataStream(tasks, stream)
			if len(matched) > 0 {
				desc.Tasks = matched
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

// filterTasksByDataStream keeps tasks whose description mentions the
// stream name or any of its backing indices as a complete token. The
// cluster reports neither directly on /_tasks, and operators commonly
// trigger work via the stream alias (rollover) or against a specific
// backing index (forcemerge), so the matcher unions both name sets.
// Boundary rules live in client.DescriptionMentions.
func filterTasksByDataStream(tasks []types.Task, stream types.DataStream) []types.Task {
	tokens := make([]string, 0, 1+len(stream.Indices))
	if stream.Name != "" {
		tokens = append(tokens, stream.Name)
	}
	for _, b := range stream.Indices {
		if b.Name != "" {
			tokens = append(tokens, b.Name)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	out := make([]types.Task, 0, len(tasks))
	for _, t := range tasks {
		for _, tok := range tokens {
			if client.DescriptionMentions(t.Description, tok) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}
