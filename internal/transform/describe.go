package transform

import (
	"context"
	"errors"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DescribeOptions is the business-layer input to Describe. Name is
// the operator-supplied transform id; patterns refused — describe
// is the single-transform verb.
type DescribeOptions struct {
	Name string
}

// DescribeCapabilities is the read-only capability bundle the
// aggregator consumes. Only TransformInspector is required; both
// the definition (GetTransform) and the stats (StatsTransform) live
// on the same interface.
type DescribeCapabilities struct {
	Transform client.TransformInspector
	Dialect   string
}

// Describe is the business-layer entry point for
// `esops transform describe`. Fetches the definition then the
// runtime stats sequentially — the cluster has no parallel
// dependency between the two and the stats call needs the name we
// already have. Stats failures (a transform never started, the
// cluster's explain endpoint 4xx-ing on a stopped continuous
// transform) land in SectionWarnings; ErrNotFound is silently
// swallowed because a transform that has never ticked has no
// stats and reporting it as a warning would be noise.
//
// Refusals:
//   - empty / patternish Name surfaces as ErrUsage via Get.
//   - a missing transform surfaces as ErrNotFound from the cluster.
func Describe(ctx context.Context, caps DescribeCapabilities, opts DescribeOptions) (types.TransformDescription, error) {
	info, err := Get(ctx, caps.Transform, opts.Name)
	if err != nil {
		return types.TransformDescription{}, err
	}

	desc := types.TransformDescription{
		Name:    info.Name,
		Dialect: caps.Dialect,
		Info:    info,
	}

	stats, err := Stats(ctx, caps.Transform, info.Name)
	switch {
	case errors.Is(err, client.ErrNotFound):
		// Transform exists but has never been started — the cluster
		// stores no explain state for it. Treat as "no stats yet".
	case err != nil:
		desc.SectionWarnings = map[string]string{"stats": err.Error()}
	default:
		desc.Stats = &stats
	}

	return desc, nil
}
