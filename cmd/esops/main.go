package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dj-wasabi/esops-go/internal/cli"
	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/client"
)

func main() {
	// SIGINT/SIGTERM cancel the root context. Long-running ops
	// (reindex polling, snapshot waits) observe the context and
	// cancel remote tasks rather than just exiting.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := cli.Run(ctx, os.Args)
	stop()
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	switch {
	case errors.Is(err, context.Canceled):
		return 130
	case errors.Is(err, client.ErrUsage):
		return 2
	case errors.Is(err, client.ErrUnreachable):
		return 3
	case errors.Is(err, client.ErrAuth):
		return 4
	case errors.Is(err, client.ErrForbidden):
		return 5
	case errors.Is(err, client.ErrUnsupported):
		return 6
	case errors.Is(err, client.ErrDryRunBlocker):
		return 7
	case errors.Is(err, client.ErrBlastRadius):
		return 8
	case errors.Is(err, cmdsetup.ErrProtected):
		return 9
	case errors.Is(err, client.ErrUnknownProduct):
		return 10
	default:
		return 1
	}
}
