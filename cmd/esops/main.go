package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/esops-dev/esops-go/internal/audit"
	"github.com/esops-dev/esops-go/internal/cli"
	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/onerror"
	"github.com/esops-dev/esops-go/pkg/client"
)

func main() {
	// SIGINT/SIGTERM cancel the root context. Long-running ops
	// (reindex polling, snapshot waits) observe the context and
	// cancel remote tasks rather than just exiting.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := cli.Run(ctx, os.Args)
	stop()
	code := exitCode(err)
	// audit.Finish is a no-op when auditing is disabled or Begin was
	// not reached (e.g. --help, flag-parse errors); calling it
	// unconditionally keeps the SIGINT-and-error paths uniform.
	audit.Finish(code, classifyError(err))
	if err == nil {
		return
	}
	// onerror.ExitError signals "command rendered its summary on stdout,
	// just exit non-zero" — printing the wrapper message to stderr on
	// top of the rendered summary would be noise.
	if !onerror.IsExitError(err) {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
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

// classifyError maps an error to the short class string recorded in
// the audit log's "error" field. Mirrors exitCode's branches so the
// recorded class lines up with the process exit code an operator can
// grep their shell history for. Returns "" on success.
func classifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "interrupted"
	case errors.Is(err, client.ErrUsage):
		return "usage"
	case errors.Is(err, client.ErrUnreachable):
		return "unreachable"
	case errors.Is(err, client.ErrAuth):
		return "auth"
	case errors.Is(err, client.ErrForbidden):
		return "forbidden"
	case errors.Is(err, client.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, client.ErrDryRunBlocker):
		return "dry-run-blocker"
	case errors.Is(err, client.ErrBlastRadius):
		return "blast-radius"
	case errors.Is(err, cmdsetup.ErrProtected):
		return "protected-context"
	case errors.Is(err, client.ErrUnknownProduct):
		return "unknown-product"
	case onerror.IsExitError(err):
		return "on-error-policy"
	default:
		return "error"
	}
}
