package datastream_test

import (
	"context"
	"errors"
	"testing"

	"github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// describeStreamFake serves a single canned stream by name.
type describeStreamFake struct {
	stream types.DataStream
	err    error
}

func (f *describeStreamFake) DataStreams(_ context.Context, _ types.DataStreamFilter) ([]types.DataStream, error) {
	return []types.DataStream{f.stream}, f.err
}
func (f *describeStreamFake) GetDataStream(_ context.Context, name string) (types.DataStream, error) {
	if f.err != nil {
		return types.DataStream{}, f.err
	}
	if f.stream.Name != name {
		return types.DataStream{}, errors.New("not found")
	}
	return f.stream, nil
}
func (f *describeStreamFake) DataStreamStats(_ context.Context, _ types.DataStreamStatsRequest) (types.DataStreamStatsReport, error) {
	return types.DataStreamStatsReport{}, nil
}

type describeIndexFake struct {
	indices []types.Index
}

func (f *describeIndexFake) Indices(_ context.Context, _ types.IndexFilter) ([]types.Index, error) {
	return f.indices, nil
}
func (f *describeIndexFake) IndicesNamesStatus(_ context.Context, _ types.IndexFilter) ([]types.IndexNameStatus, error) {
	return nil, nil
}
func (f *describeIndexFake) IndicesStream(_ context.Context, _ types.IndexFilter, _ func(types.Index) error) error {
	return nil
}

type describeTemplatesFake struct {
	templates []types.IndexTemplate
}

func (f *describeTemplatesFake) GetTemplates(_ context.Context, _ []string) ([]types.IndexTemplate, error) {
	return f.templates, nil
}
func (f *describeTemplatesFake) GetTemplate(_ context.Context, name string) (types.IndexTemplate, error) {
	for _, t := range f.templates {
		if t.Name == name {
			return t, nil
		}
	}
	return types.IndexTemplate{}, nil
}

type describeRecoveryFake struct {
	report types.RecoveryReport
}

func (f *describeRecoveryFake) Recovery(_ context.Context, _ types.RecoveryRequest) (types.RecoveryReport, error) {
	return f.report, nil
}

type describeTasksFake struct {
	tasks []types.Task
}

func (f *describeTasksFake) Tasks(_ context.Context, _ types.TasksFilter) ([]types.Task, error) {
	return f.tasks, nil
}
func (f *describeTasksFake) TasksStream(_ context.Context, _ types.TasksFilter, _ func(types.Task) error) error {
	return nil
}

func sampleStream() types.DataStream {
	return types.DataStream{
		Name:       "logs",
		Status:     "GREEN",
		Generation: 7,
		Template:   "logs-template",
		WriteIndex: ".ds-logs-2025.05.19-000007",
		IndexCount: 7,
		Indices: []types.DataStreamBackingIndex{
			{Name: ".ds-logs-2025.05.13-000001"},
			{Name: ".ds-logs-2025.05.19-000007"},
		},
	}
}

func TestDescribe_rejectsEmpty(t *testing.T) {
	_, err := datastream.Describe(context.Background(), datastream.DescribeCapabilities{
		DataStream: &describeStreamFake{},
	}, datastream.DescribeOptions{Name: ""})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestDescribe_rejectsPattern(t *testing.T) {
	_, err := datastream.Describe(context.Background(), datastream.DescribeCapabilities{
		DataStream: &describeStreamFake{},
	}, datastream.DescribeOptions{Name: "logs-*"})
	if err == nil || !errors.Is(err, client.ErrUsage) {
		t.Fatalf("want ErrUsage, got %v", err)
	}
}

func TestDescribe_matchesDataStreamTemplate(t *testing.T) {
	caps := datastream.DescribeCapabilities{
		DataStream: &describeStreamFake{stream: sampleStream()},
		Indices:    &describeIndexFake{},
		Templates: &describeTemplatesFake{templates: []types.IndexTemplate{
			// Plain-index template — should NOT match (no data_stream block).
			{Name: "catchall", IndexPatterns: []string{"*"}, Priority: 1},
			// Data-stream template that matches — should win.
			{Name: "logs-template", IndexPatterns: []string{"logs*"}, Priority: 100, DataStream: map[string]any{"hidden": false}},
		}},
		Dialect: "elasticsearch",
	}
	desc, err := datastream.Describe(context.Background(), caps, datastream.DescribeOptions{Name: "logs"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.MatchedTemplate == nil || desc.MatchedTemplate.Name != "logs-template" {
		t.Errorf("matched template wrong: %+v", desc.MatchedTemplate)
	}
}

func TestDescribe_filtersTasksByBackingIndices(t *testing.T) {
	caps := datastream.DescribeCapabilities{
		DataStream: &describeStreamFake{stream: sampleStream()},
		Indices:    &describeIndexFake{},
		Tasks: &describeTasksFake{tasks: []types.Task{
			// Matches via backing-index name.
			{ID: "n:1", Action: "indices:admin/forcemerge", Description: "Force-merge [.ds-logs-2025.05.13-000001]"},
			// Matches via the stream name.
			{ID: "n:2", Action: "indices:admin/rollover", Description: "rollover stream logs"},
			// Should NOT match — different stream.
			{ID: "n:3", Action: "indices:data/write/reindex", Description: "reindex users to users-v2"},
		}},
		Dialect: "elasticsearch",
	}
	desc, err := datastream.Describe(context.Background(), caps, datastream.DescribeOptions{Name: "logs"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.Tasks) != 2 {
		t.Fatalf("want 2 matched tasks, got %d", len(desc.Tasks))
	}
}

func TestDescribe_collectsActiveRecoveries(t *testing.T) {
	caps := datastream.DescribeCapabilities{
		DataStream: &describeStreamFake{stream: sampleStream()},
		Indices:    &describeIndexFake{},
		Recovery: &describeRecoveryFake{report: types.RecoveryReport{
			Indices: []types.IndexRecovery{
				{Index: ".ds-logs-2025.05.13-000001", Shards: []types.ShardRecovery{{ID: 0, Stage: "INDEX"}}},
				// Empty-shards index is dropped by the aggregator (post-DONE).
				{Index: ".ds-logs-2025.05.19-000007", Shards: nil},
			},
		}},
	}
	desc, err := datastream.Describe(context.Background(), caps, datastream.DescribeOptions{Name: "logs"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.Recoveries) != 1 {
		t.Fatalf("want 1 active recovery, got %d", len(desc.Recoveries))
	}
	if desc.Recoveries[0].Index != ".ds-logs-2025.05.13-000001" {
		t.Errorf("unexpected recovery index: %s", desc.Recoveries[0].Index)
	}
}
