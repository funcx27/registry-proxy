package proxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distribution/distribution/v3/configuration"
)

func TestDeleteImageTagPathAllRepositories(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	createTag(t, root, "example.com/team/app", "v1", now.Add(-3*time.Hour))
	createTag(t, root, "example.com/team/app", "v2", now.Add(-2*time.Hour))
	createTag(t, root, "example.com/team/app", "v3", now.Add(-time.Hour))
	createTag(t, root, "ghcr.io/org/other", "only", now.Add(-4*time.Hour))

	cfg := &configuration.Configuration{
		Storage: configuration.Storage{
			"filesystem": configuration.Parameters{"rootdirectory": root},
		},
	}

	deleted, err := deleteImageTagPath(cfg, "", 2, 0, false)
	if err != nil {
		t.Fatalf("deleteImageTagPath() error = %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted paths = %v, want exactly the oldest tag", deleted)
	}
	if !strings.HasSuffix(deleted[0], filepath.Join("example.com", "team", "app", "_manifests", "tags", "v1")) {
		t.Fatalf("deleted path = %q, want v1 of example.com/team/app", deleted[0])
	}

	if _, err := os.Stat(tagPath(root, "example.com/team/app", "v1")); !os.IsNotExist(err) {
		t.Fatalf("v1 still exists, stat error = %v", err)
	}
	for _, tag := range []string{"v2", "v3"} {
		if _, err := os.Stat(tagPath(root, "example.com/team/app", tag)); err != nil {
			t.Fatalf("%s was removed: %v", tag, err)
		}
	}
	if _, err := os.Stat(tagPath(root, "ghcr.io/org/other", "only")); err != nil {
		t.Fatalf("single tag repository was removed: %v", err)
	}
}

func TestCleanImageRejectsZeroRetain(t *testing.T) {
	err := CleanImage(context.Background(), &configuration.Configuration{}, NewMaintenanceGate(), CleanupOptions{
		Enabled:      true,
		TagRetain:    0,
		Interval:     time.Hour,
		DeleteBefore: time.Hour,
	})
	if err == nil {
		t.Fatal("CleanImage() error = nil, want zero retain rejection")
	}
	if !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("CleanImage() error = %q, want retain validation", err)
	}
}

func createTag(t *testing.T, root, repository, tag string, atime time.Time) {
	t.Helper()
	tagDir := tagPath(root, repository, tag)
	linkDir := filepath.Join(tagDir, "current")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatalf("create tag dir: %v", err)
	}
	link := filepath.Join(linkDir, "link")
	if err := os.WriteFile(link, []byte("sha256:0123456789abcdef"), 0o644); err != nil {
		t.Fatalf("write tag link: %v", err)
	}
	if err := os.Chtimes(link, atime, atime); err != nil {
		t.Fatalf("set tag atime: %v", err)
	}
}

func tagPath(root, repository, tag string) string {
	return filepath.Join(root, "docker", "registry", "v2", "repositories", repository, "_manifests", "tags", tag)
}
