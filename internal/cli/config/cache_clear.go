package config

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/completion"
)

func cacheClearCommand() *cli.Command {
	return &cli.Command{
		Name:  "cache-clear",
		Usage: "Remove tab-completion cache files written under $XDG_CACHE_HOME/esops/complete",
		Description: `Delete the on-disk tab-completion cache populated by cluster-backed
suggesters (--index, --repository, --node, …). Each completion call
re-fetches and re-caches as needed, so clearing only forces the next
TAB to do a live lookup — useful when the cluster's inventory has
changed inside the --complete-cache TTL window and the operator
wants the freshest suggestions immediately.

The cache lives at:

  $XDG_CACHE_HOME/esops/complete/        (Linux)
  ~/Library/Caches/esops/complete/       (macOS)
  %LOCALAPPDATA%\esops\complete\         (Windows)

Removes only the .tmp and cache files this package owns; never touches
sibling directories. --dry-run lists what would be deleted without
removing anything.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Print the files that would be removed, then exit without deleting",
			},
		},
		Action: runCacheClear,
	}
}

func runCacheClear(_ context.Context, cmd *cli.Command) error {
	dir := completion.CacheDir()
	if dir == "" {
		// Couldn't determine a cache location on this platform — same
		// failure mode as the writer: silently do nothing. Surfaces a
		// clear message rather than 'success' so an operator who
		// expects a cleanup doesn't walk away misled.
		_, _ = fmt.Fprintln(os.Stdout, "No tab-completion cache directory is configured on this platform.")
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintf(os.Stdout, "No cache to clear: %s does not exist.\n", dir)
			return nil
		}
		return fmt.Errorf("reading cache dir %q: %w", dir, err)
	}

	dryRun := cmd.Bool("dry-run")
	var (
		removed int
		bytes   int64
		errs    []string
	)
	for _, e := range entries {
		// Defence in depth: the cache only writes regular files at the
		// directory's top level. Refuse to descend into subdirectories
		// (none should exist) or follow links — keeps the blast radius
		// pinned to what this package created.
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := infoOf(e, path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		if dryRun {
			_, _ = fmt.Fprintf(os.Stdout, "would remove: %s (%d bytes)\n", path, info.Size())
			removed++
			bytes += info.Size()
			continue
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		removed++
		bytes += info.Size()
	}

	verb := "Removed"
	if dryRun {
		verb = "Would remove"
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s %d file(s), %d bytes from %s.\n", verb, removed, bytes, dir)
	if len(errs) > 0 {
		return fmt.Errorf("cache-clear: %d error(s): %v", len(errs), errs)
	}
	return nil
}

// infoOf prefers the directory-entry's cached Info() (one stat per
// entry from ReadDir) and falls back to an explicit os.Lstat only when
// the cached entry can't service it. Saves an stat-per-file on
// platforms where the DirEntry already carries size and mode.
func infoOf(e fs.DirEntry, path string) (fs.FileInfo, error) {
	if info, err := e.Info(); err == nil {
		return info, nil
	}
	return os.Lstat(path)
}
