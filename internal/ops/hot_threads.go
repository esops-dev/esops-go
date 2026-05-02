package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// validHotThreadsTypes is the set the cluster will accept. Validated in
// the business layer so a typo fails fast locally (exit code 2) instead
// of round-tripping to the cluster for a 400.
var validHotThreadsTypes = map[string]struct{}{
	"cpu":   {},
	"wait":  {},
	"block": {},
	"mem":   {},
}

// HotThreads returns the sampled-thread view from /_nodes/hot_threads.
// Parameter validation (Type, Snapshots/Threads positivity) happens here
// rather than in the CLI layer so the rules travel with the business
// logic — any future caller that wires a different flag shape can
// depend on the same guarantees.
func HotThreads(ctx context.Context, c client.HotThreadsInspector, req types.HotThreadsRequest) (types.HotThreads, error) {
	if req.Type != "" {
		lc := strings.ToLower(req.Type)
		if _, ok := validHotThreadsTypes[lc]; !ok {
			return types.HotThreads{}, fmt.Errorf("--type %q is not one of cpu, wait, block, mem", req.Type)
		}
		req.Type = lc
	}
	if req.Threads != nil && *req.Threads <= 0 {
		return types.HotThreads{}, fmt.Errorf("--threads must be > 0 (got %d)", *req.Threads)
	}
	if req.Snapshots != nil && *req.Snapshots <= 0 {
		return types.HotThreads{}, fmt.Errorf("--snapshots must be > 0 (got %d)", *req.Snapshots)
	}
	if req.Interval < 0 {
		return types.HotThreads{}, fmt.Errorf("--interval must be >= 0 (got %s)", req.Interval)
	}
	return c.HotThreads(ctx, req)
}
