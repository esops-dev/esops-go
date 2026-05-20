package index

import (
	"context"
	"errors"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/pkg/client"
)

// primedCmd builds a cli.Command with the supplied flag set and runs
// it once so cmd.Bool() / cmd.Args() return parsed state. urfave/cli's
// accessors nil-deref on an un-Run command, so this helper is the
// shortest path to a populated *cli.Command from a unit test.
func primedCmd(t *testing.T, flagNames []string, argv []string) *cli.Command {
	t.Helper()
	flags := make([]cli.Flag, 0, len(flagNames))
	for _, n := range flagNames {
		flags = append(flags, &cli.BoolFlag{Name: n})
	}
	cmd := &cli.Command{
		Name:   "describe",
		Flags:  flags,
		Action: func(_ context.Context, _ *cli.Command) error { return nil },
	}
	if err := cmd.Run(context.Background(), argv); err != nil {
		t.Fatalf("priming Run(%v): %v", argv, err)
	}
	return cmd
}

func TestResolveShardVisibility(t *testing.T) {
	t.Run("neither set → nil (auto)", func(t *testing.T) {
		got, err := resolveShardVisibility(primedCmd(t, []string{"show-shards", "no-shards"}, []string{"describe"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", *got)
		}
	})
	t.Run("--show-shards → &true", func(t *testing.T) {
		got, err := resolveShardVisibility(primedCmd(t, []string{"show-shards", "no-shards"}, []string{"describe", "--show-shards"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || *got != true {
			t.Errorf("got %v, want &true", got)
		}
	})
	t.Run("--no-shards → &false", func(t *testing.T) {
		got, err := resolveShardVisibility(primedCmd(t, []string{"show-shards", "no-shards"}, []string{"describe", "--no-shards"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || *got != false {
			t.Errorf("got %v, want &false", got)
		}
	})
	t.Run("both set → ErrUsage", func(t *testing.T) {
		_, err := resolveShardVisibility(primedCmd(t, []string{"show-shards", "no-shards"}, []string{"describe", "--show-shards", "--no-shards"}))
		if err == nil || !errors.Is(err, client.ErrUsage) {
			t.Errorf("got err=%v, want ErrUsage", err)
		}
	})
}

func TestResolveManagedVisibility(t *testing.T) {
	t.Run("neither set → nil (auto)", func(t *testing.T) {
		got, err := resolveManagedVisibility(primedCmd(t, []string{"show-indices", "no-indices"}, []string{"describe"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", *got)
		}
	})
	t.Run("both set → ErrUsage", func(t *testing.T) {
		_, err := resolveManagedVisibility(primedCmd(t, []string{"show-indices", "no-indices"}, []string{"describe", "--show-indices", "--no-indices"}))
		if err == nil || !errors.Is(err, client.ErrUsage) {
			t.Errorf("got err=%v, want ErrUsage", err)
		}
	})
}
