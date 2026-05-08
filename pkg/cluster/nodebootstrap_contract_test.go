package cluster_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/cluster"
	"github.com/esops-dev/esops-go/pkg/config"
)

// TestNodeBootstrap_JVMErgonomicsContract proves both adapters lift
// `jvm.gc_collectors[]` and `jvm.input_arguments[]` off the same
// /_nodes/process,jvm body and surface them on NodeBootstrap. The
// jvm_gc_ergonomics rule reads these fields directly: the collector
// list to flag CMS, the argument list to flag manual G1 overrides.
// A future drift in either adapter's wire decode would mute the rule
// without breaking the build.
func TestNodeBootstrap_JVMErgonomicsContract(t *testing.T) {
	const procJVMBody = `{
		"nodes": {
			"n1": {
				"name": "node-a",
				"process": {"mlockall": true, "max_file_descriptors": 65535},
				"jvm": {
					"mem": {"using_memory_lock": true},
					"gc_collectors": ["G1 Young Generation","G1 Old Generation"],
					"input_arguments": ["-XX:+UseG1GC","-XX:MaxGCPauseMillis=200","-Xms2g"]
				}
			},
			"n2": {
				"name": "node-b",
				"process": {"mlockall": false, "max_file_descriptors": 4096},
				"jvm": {
					"mem": {"using_memory_lock": false},
					"gc_collectors": ["ParNew","ConcurrentMarkSweep"],
					"input_arguments": ["-XX:+UseConcMarkSweepGC"]
				}
			}
		}
	}`

	for _, dialect := range []string{dialectES, dialectOS} {
		t.Run(dialect, func(t *testing.T) {
			c := newNodeBootstrapClient(t, dialect, procJVMBody)
			got, err := c.NodeBootstrap.NodeBootstrap(context.Background())
			if err != nil {
				t.Fatalf("NodeBootstrap: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("len(got) = %d, want 2", len(got))
			}
			// Sort guarantee: alphabetical by node name.
			if got[0].Name != "node-a" || got[1].Name != "node-b" {
				t.Errorf("unsorted: got[0]=%q got[1]=%q", got[0].Name, got[1].Name)
			}
			wantG1 := []string{"G1 Young Generation", "G1 Old Generation"}
			if !reflect.DeepEqual(got[0].GCCollectors, wantG1) {
				t.Errorf("node-a GCCollectors = %#v, want %#v", got[0].GCCollectors, wantG1)
			}
			wantG1Args := []string{"-XX:+UseG1GC", "-XX:MaxGCPauseMillis=200", "-Xms2g"}
			if !reflect.DeepEqual(got[0].JVMInputArguments, wantG1Args) {
				t.Errorf("node-a JVMInputArguments = %#v, want %#v", got[0].JVMInputArguments, wantG1Args)
			}
			wantCMS := []string{"ParNew", "ConcurrentMarkSweep"}
			if !reflect.DeepEqual(got[1].GCCollectors, wantCMS) {
				t.Errorf("node-b GCCollectors = %#v, want %#v", got[1].GCCollectors, wantCMS)
			}
			wantCMSArgs := []string{"-XX:+UseConcMarkSweepGC"}
			if !reflect.DeepEqual(got[1].JVMInputArguments, wantCMSArgs) {
				t.Errorf("node-b JVMInputArguments = %#v, want %#v", got[1].JVMInputArguments, wantCMSArgs)
			}
			// Existing fields must continue to populate alongside the
			// new ones — the extension must not regress mlockall /
			// max_file_descriptors decoding.
			if !got[0].MlockallEnabled || got[0].MaxFileDescriptors != 65535 {
				t.Errorf("node-a bootstrap fields drifted: %+v", got[0])
			}
			if got[1].MlockallEnabled || got[1].MaxFileDescriptors != 4096 {
				t.Errorf("node-b bootstrap fields drifted: %+v", got[1])
			}
		})
	}
}

// TestNodeBootstrap_OmitsErgonomicsWhenAbsent proves a cluster that
// does not surface gc_collectors / input_arguments (defensive — every
// supported version does, but the omitempty contract on the type
// matters for downstream JSON consumers) lands the bootstrap fields
// without injecting empty arrays.
func TestNodeBootstrap_OmitsErgonomicsWhenAbsent(t *testing.T) {
	const procJVMBody = `{
		"nodes": {
			"n1": {
				"name": "node-a",
				"process": {"mlockall": true, "max_file_descriptors": 65535},
				"jvm": {"mem": {"using_memory_lock": true}}
			}
		}
	}`
	c := newNodeBootstrapClient(t, dialectES, procJVMBody)
	got, err := c.NodeBootstrap.NodeBootstrap(context.Background())
	if err != nil {
		t.Fatalf("NodeBootstrap: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].GCCollectors != nil {
		t.Errorf("GCCollectors = %#v, want nil when cluster omitted the field", got[0].GCCollectors)
	}
	if got[0].JVMInputArguments != nil {
		t.Errorf("JVMInputArguments = %#v, want nil when cluster omitted the field", got[0].JVMInputArguments)
	}
}

func newNodeBootstrapClient(t *testing.T, dialect, procJVMBody string) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeDialectHeaders(w, dialect)
		if strings.HasPrefix(r.URL.Path, "/_nodes/process,jvm") {
			_, _ = w.Write([]byte(procJVMBody))
			return
		}
		if dialect == dialectES {
			_, _ = w.Write([]byte(esRootBody))
		} else {
			_, _ = w.Write([]byte(osRootBody))
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	cc := config.Context{URL: s.URL}
	c, err := cluster.New(context.Background(), cc)
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	return c
}
