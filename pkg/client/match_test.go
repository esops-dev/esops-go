package client_test

import (
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestDescriptionMentions(t *testing.T) {
	cases := []struct {
		name string
		desc string
		want bool
	}{
		{"bracketed match", "Force-merge [users]", true},
		{"space-bordered match", "search across users right now", true},
		{"end-of-string match", "scanning users", true},
		{"colon-bordered match", "indices:users", true},
		{"prefix-of-longer-name", "reindex users-v2 to users-v3", false},
		{"suffix-of-longer-name", "refresh cusers", false},
		{"no occurrence", "rollover logs", false},
		{"empty description", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := client.DescriptionMentions(tc.desc, "users"); got != tc.want {
				t.Errorf("DescriptionMentions(%q, %q) = %v, want %v", tc.desc, "users", got, tc.want)
			}
		})
	}
}

func TestDescriptionMentions_empty(t *testing.T) {
	if client.DescriptionMentions("nonempty", "") {
		t.Error("empty needle should not match")
	}
}

func TestMatchHighestPriorityTemplate(t *testing.T) {
	tmpls := []types.IndexTemplate{
		{Name: "fallback", IndexPatterns: []string{"*"}, Priority: 1},
		{Name: "logs", IndexPatterns: []string{"logs-*"}, Priority: 100},
		{Name: "users", IndexPatterns: []string{"users-*"}, Priority: 100},
		{Name: "logs-ds", IndexPatterns: []string{"logs*"}, Priority: 150, DataStream: map[string]any{"hidden": false}},
	}

	t.Run("no predicate picks highest priority match", func(t *testing.T) {
		got, ok := client.MatchHighestPriorityTemplate("logs-2025-05", tmpls, nil)
		if !ok {
			t.Fatal("want match, got none")
		}
		if got.Name != "logs-ds" {
			t.Errorf("got %q, want logs-ds (priority 150)", got.Name)
		}
	})
	t.Run("predicate filters to data-stream templates", func(t *testing.T) {
		pred := func(t types.IndexTemplate) bool { return len(t.DataStream) > 0 }
		got, ok := client.MatchHighestPriorityTemplate("logs-2025-05", tmpls, pred)
		if !ok {
			t.Fatal("want match, got none")
		}
		if got.Name != "logs-ds" {
			t.Errorf("got %q, want logs-ds", got.Name)
		}
	})
	t.Run("predicate filters out all candidates", func(t *testing.T) {
		pred := func(t types.IndexTemplate) bool { return false }
		_, ok := client.MatchHighestPriorityTemplate("logs-2025-05", tmpls, pred)
		if ok {
			t.Error("want no match when predicate excludes everything")
		}
	})
	t.Run("no pattern match", func(t *testing.T) {
		_, ok := client.MatchHighestPriorityTemplate("metrics-2025-05", []types.IndexTemplate{
			{Name: "users", IndexPatterns: []string{"users-*"}, Priority: 100},
		}, nil)
		if ok {
			t.Error("want no match when no template patterns the name")
		}
	})
}
