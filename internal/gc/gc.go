// Package gc wraps distribution's mark-and-sweep garbage collector.
package gc

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/distribution/distribution/v3/registry/storage/driver/factory"
)

// Run executes distribution's mark-and-sweep GC against the configured storage.
//
// Lazy multi-platform pulls can leave a repository with a tagged top-level index
// and no layer links until a client requests a child digest. Distribution's GC
// aborts with PathNotFound when a repository has manifests but no _layers
// directory, so ensure those directories exist before sweeping. This repairs a
// layout the filesystem driver would otherwise treat as invalid, and mirrors the
// filesystem-only cleanup path this app already relies on.
func Run(ctx context.Context, config configuration.Configuration) error {
	if config.Storage.Type() == "filesystem" {
		rootDir, _ := config.Storage.Parameters()["rootdirectory"].(string)
		if rootDir != "" {
			if err := ensureLayerDirs(rootDir); err != nil {
				return fmt.Errorf("ensure layer directories: %w", err)
			}
		}
	}

	driver, err := factory.Create(ctx, config.Storage.Type(), config.Storage.Parameters())
	if err != nil {
		return fmt.Errorf("construct %s driver: %w", config.Storage.Type(), err)
	}

	registry, err := storage.NewRegistry(ctx, driver)
	if err != nil {
		return fmt.Errorf("construct registry: %w", err)
	}

	if err := storage.MarkAndSweep(ctx, driver, registry, storage.GCOpts{
		DryRun:         false,
		RemoveUntagged: true,
	}); err != nil {
		return fmt.Errorf("garbage collect: %w", err)
	}
	return nil
}

// ensureLayerDirs creates an empty _layers directory next to every repository
// _manifests directory. Distribution's GC enumerates _layers unconditionally and
// fails the whole sweep when it is missing, which happens for repositories that
// currently hold only an index.
func ensureLayerDirs(rootDir string) error {
	reposPath := filepath.Join(rootDir, "docker", "registry", "v2", "repositories")
	err := filepath.WalkDir(reposPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() && entry.Name() == "_manifests" {
			layersPath := filepath.Join(filepath.Dir(path), "_layers")
			if err := os.MkdirAll(layersPath, 0o755); err != nil {
				return err
			}
			// No need to descend into _manifests; only the repo root matters.
			return fs.SkipDir
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
