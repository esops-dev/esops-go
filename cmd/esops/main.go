package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dj-wasabi/esops-go/internal/cli"
	"github.com/dj-wasabi/esops-go/internal/client"
)

func main() {
	err := cli.Run(context.Background(), os.Args)
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(exitCode(err))
}

// exitCode maps known sentinels to the stable codes documented in
// CLAUDE.md §6. Unknown errors collapse to 1.
func exitCode(err error) int {
	switch {
	case errors.Is(err, client.ErrUnreachable):
		return 3
	case errors.Is(err, client.ErrAuth):
		return 4
	case errors.Is(err, client.ErrForbidden):
		return 5
	case errors.Is(err, client.ErrUnsupported):
		return 6
	default:
		return 1
	}
}
