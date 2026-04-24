package opensearch

import (
	"testing"
	"time"

	"github.com/dj-wasabi/esops-go/internal/types"
)

// buildHotThreadsPath is exercised only on the OS --type=wait|block|mem
// code path (the typed SDK covers CPU). Fixing its output in a test
// guards against a future params rename silently changing the wire
// format on just the fallback path.
func TestBuildHotThreadsPath(t *testing.T) {
	true_ := true
	five := 5
	twenty := 20

	tests := []struct {
		name string
		req  types.HotThreadsRequest
		want string
	}{
		{
			name: "no flags",
			req:  types.HotThreadsRequest{},
			want: "/_nodes/hot_threads",
		},
		{
			name: "single node",
			req:  types.HotThreadsRequest{Nodes: []string{"es01"}},
			want: "/_nodes/es01/hot_threads",
		},
		{
			name: "multiple nodes comma-joined",
			req:  types.HotThreadsRequest{Nodes: []string{"es01", "es02"}},
			want: "/_nodes/es01,es02/hot_threads",
		},
		{
			name: "full knobs alphabetised",
			req: types.HotThreadsRequest{
				Nodes:             []string{"es01"},
				IgnoreIdleThreads: &true_,
				Interval:          500 * time.Millisecond,
				Snapshots:         &twenty,
				Threads:           &five,
				Type:              "wait",
			},
			want: "/_nodes/es01/hot_threads?ignore_idle_threads=true&interval=500ms&snapshots=20&threads=5&type=wait",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildHotThreadsPath(tc.req); got != tc.want {
				t.Errorf("buildHotThreadsPath = %q, want %q", got, tc.want)
			}
		})
	}
}
