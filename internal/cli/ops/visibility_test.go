package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/pkg/client"
)

func primedNodeCmd(t *testing.T, argv []string) *cli.Command {
	t.Helper()
	cmd := &cli.Command{
		Name: "describe",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "show-shards"},
			&cli.BoolFlag{Name: "no-shards"},
		},
		Action: func(_ context.Context, _ *cli.Command) error { return nil },
	}
	if err := cmd.Run(context.Background(), argv); err != nil {
		t.Fatalf("priming Run(%v): %v", argv, err)
	}
	return cmd
}

func TestResolveNodeShardVisibility(t *testing.T) {
	t.Run("both set → ErrUsage", func(t *testing.T) {
		_, err := resolveNodeShardVisibility(primedNodeCmd(t, []string{"describe", "--show-shards", "--no-shards"}))
		if err == nil || !errors.Is(err, client.ErrUsage) {
			t.Errorf("got err=%v, want ErrUsage", err)
		}
	})
	t.Run("--show-shards alone → &true", func(t *testing.T) {
		got, err := resolveNodeShardVisibility(primedNodeCmd(t, []string{"describe", "--show-shards"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || *got != true {
			t.Errorf("got %v, want &true", got)
		}
	})
	t.Run("--no-shards alone → &false", func(t *testing.T) {
		got, err := resolveNodeShardVisibility(primedNodeCmd(t, []string{"describe", "--no-shards"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || *got != false {
			t.Errorf("got %v, want &false", got)
		}
	})
	t.Run("neither set → nil (auto)", func(t *testing.T) {
		got, err := resolveNodeShardVisibility(primedNodeCmd(t, []string{"describe"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", *got)
		}
	})
}
