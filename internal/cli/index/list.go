package index

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cluster"
	"github.com/dj-wasabi/esops-go/internal/config"
	esopsindex "github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
)

func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List indices with size, docs and health",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Limit to index pattern(s), comma-separated (e.g. 'logs-*,users')",
			},
			&cli.StringFlag{
				Name:  "health",
				Usage: "Filter by health: green | yellow | red",
			},
			&cli.StringFlag{
				Name:  "status",
				Usage: "Filter by status: open | close",
			},
			&cli.BoolFlag{
				Name:  "hidden",
				Usage: "Include hidden indices (dot-prefixed system indices)",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort by: name | size | docs",
				Value: "name",
			},
		},
		Action: runList,
	}
}

func runList(ctx context.Context, cmd *cli.Command) error {
	cfg, _, err := config.LoadDefault(cmd.String("config"))
	if err != nil {
		return err
	}
	name, cc, err := cfg.ResolveContext(cmd.String("context"))
	if err != nil {
		return err
	}
	applyOverrides(&cc, cmd)

	format, err := output.Parse(cmd.String("output"))
	if err != nil {
		return err
	}

	sortKey, err := parseSort(cmd.String("sort"))
	if err != nil {
		return err
	}

	if h := cmd.String("health"); h != "" {
		if err := validateHealth(h); err != nil {
			return err
		}
	}
	if s := cmd.String("status"); s != "" {
		if err := validateStatus(s); err != nil {
			return err
		}
	}

	c, err := cluster.New(ctx, cc)
	if err != nil {
		return err
	}

	filter := types.IndexFilter{
		Index:  cmd.String("index"),
		Health: cmd.String("health"),
		Status: cmd.String("status"),
		Hidden: cmd.Bool("hidden"),
		Sort:   sortKey,
	}

	indices, err := esopsindex.List(ctx, c, filter)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "context=%s dialect=%s version=%s indices=%d\n", name, c.Info.Dialect, c.Info.Version, len(indices))
	return output.Indices(os.Stdout, indices, format)
}

func parseSort(s string) (types.IndexSort, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "name":
		return types.IndexSortName, nil
	case "size":
		return types.IndexSortSize, nil
	case "docs":
		return types.IndexSortDocs, nil
	default:
		return 0, fmt.Errorf("unknown --sort value %q (supported: name, size, docs)", s)
	}
}

func validateHealth(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "green", "yellow", "red":
		return nil
	default:
		return fmt.Errorf("unknown --health value %q (supported: green, yellow, red)", s)
	}
}

func validateStatus(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "open", "close":
		return nil
	default:
		return fmt.Errorf("unknown --status value %q (supported: open, close)", s)
	}
}

// applyOverrides layers command-line flags on top of the resolved
// context. URL/timeout/TLS flags override the config; everything else
// is left to the context. Kept local to the package so index commands
// don't reach into `ops`.
func applyOverrides(cc *config.Context, cmd *cli.Command) {
	if u := cmd.String("url"); u != "" {
		cc.URL = u
		cc.URLs = nil
	}
	if t := cmd.Duration("timeout"); cmd.IsSet("timeout") || cc.Timeout == 0 {
		cc.Timeout = t
	}
	if cmd.Bool("insecure") {
		cc.TLS.Insecure = true
	}
	if ca := cmd.String("cacert"); ca != "" {
		cc.TLS.CACert = ca
	}
}
