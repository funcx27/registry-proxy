package gc

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureLayerDirsCreatesMissingLayerLinks covers the layout that lazy
// multi-platform pulls produce: repositories with a _manifests directory but no
// _layers directory yet. Distribution GC aborts on that layout, so the helper
// must create the sibling _layers directory for every repository.
func TestEnsureLayerDirsCreatesMissingLayerLinks(t *testing.T) {
	root := t.TempDir()
	repos := filepath.Join(root, "docker", "registry", "v2", "repositories")

	for _, repo := range []string{"library/alpine", "team/nginx"} {
		manifestDir := filepath.Join(repos, repo, "_manifests")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			t.Fatalf("create %s: %v", manifestDir, err)
		}
	}

	if err := ensureLayerDirs(root); err != nil {
		t.Fatalf("ensureLayerDirs() error = %v", err)
	}

	for _, repo := range []string{"library/alpine", "team/nginx"} {
		layersDir := filepath.Join(repos, repo, "_layers")
		info, err := os.Stat(layersDir)
		if err != nil {
			t.Fatalf("stat %s: %v", layersDir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", layersDir)
		}
	}

	// Idempotent: an existing _layers directory must be left untouched.
	if err := ensureLayerDirs(root); err != nil {
		t.Fatalf("second ensureLayerDirs() error = %v", err)
	}
}

// TestEnsureLayerDirsMissingRootIsNoop guards first-run and empty-storage cases
// where the repositories directory does not exist yet.
func TestEnsureLayerDirsMissingRootIsNoop(t *testing.T) {
	if err := ensureLayerDirs(t.TempDir()); err != nil {
		t.Fatalf("ensureLayerDirs() on empty root error = %v", err)
	}
}
