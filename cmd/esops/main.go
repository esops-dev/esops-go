package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dj-wasabi/esops-go/internal/cli"
)

func main() {
	if err := cli.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
