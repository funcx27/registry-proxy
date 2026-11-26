//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/distribution/distribution/v3/configuration"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/filesystem"

	"registry-proxy/internal/gc"
)

// runRegistryGC runs distribution's mark-and-sweep GC against the filesystem
// storage root through the same internal/gc path used by pkg/proxy. The proxy
// process is idle while this runs, so a second registry handle over the same
// directory is safe; in production the sweep runs in-process behind the
// maintenance gate.
func runRegistryGC(t *testing.T, storageRoot string) {
	t.Helper()

	config := configuration.Configuration{
		Storage: configuration.Storage{
			"filesystem": configuration.Parameters{"rootdirectory": storageRoot},
		},
	}
	if err := gc.Run(t.Context(), config); err != nil {
		t.Fatalf("run GC: %v", err)
	}
}

// TestLazyIndexSurvivesGarbageCollect is the GC cross-check for the lazy
// multi-platform path. It pins two behaviours that are easy to break:
//
//  1. A tag pull stores only the top-level index while every child manifest it
//     references is still absent. GC must accept that incomplete graph, keep
//     the tagged index, and not fail the sweep.
//  2. After a child digest is copied lazily, that child is an untagged manifest
//     revision. With RemoveUntagged set, GC must still retain it and its
//     config/layers because they are reachable from the tagged index.
func TestLazyIndexSurvivesGarbageCollect(t *testing.T) {
	p := startProxy(t, nil)

	const tag = "3.20"
	status, headers, body := doManifestRequest(t, p, http.MethodGet, tag)
	if status != http.StatusOK {
		t.Fatalf("GET tag status = %d, want %d; body=%s", status, http.StatusOK, body)
	}
	indexDigest := headers.Get("Docker-Content-Digest")
	if indexDigest == "" {
		t.Fatal("Docker-Content-Digest header is empty for index")
	}

	var index indexDoc
	if err := json.Unmarshal(body, &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	arm64 := findPlatform(t, index.Manifests, "arm64")
	amd64 := findPlatform(t, index.Manifests, "amd64")

	// The tag pull must have stored the index only; its children are missing.
	if got := countBlobs(t, p.storageRoot); got != 1 {
		t.Fatalf("blobs after tag pull = %d, want 1 (index only)", got)
	}
	if blobExists(t, p.storageRoot, arm64.Digest) || blobExists(t, p.storageRoot, amd64.Digest) {
		t.Fatal("child manifest present before GC; expected lazy copy")
	}

	// GC over an index whose children are all missing: keep the tagged index.
	runRegistryGC(t, p.storageRoot)
	if !blobExists(t, p.storageRoot, indexDigest) {
		t.Fatalf("GC removed index blob %s", indexDigest)
	}
	status, headersAfter, bodyAfter := doManifestRequest(t, p, http.MethodGet, tag)
	if status != http.StatusOK {
		t.Fatalf("GET tag after GC status = %d, want %d; body=%s", status, http.StatusOK, bodyAfter)
	}
	if got := headersAfter.Get("Docker-Content-Digest"); got != indexDigest {
		t.Fatalf("index digest after GC = %q, want %q", got, indexDigest)
	}
	if got := digestOf(bodyAfter); got != indexDigest {
		t.Fatalf("index body after GC digest = %s, want %s", got, indexDigest)
	}

	// Lazily copy one platform after the first GC.
	status, childHeaders, childBody := doManifestRequest(t, p, http.MethodGet, arm64.Digest)
	if status != http.StatusOK {
		t.Fatalf("GET child after GC status = %d, want %d; body=%s", status, http.StatusOK, childBody)
	}
	if got := childHeaders.Get("Docker-Content-Digest"); got != arm64.Digest {
		t.Fatalf("child digest header = %q, want %q", got, arm64.Digest)
	}
	var manifest manifestDoc
	if err := json.Unmarshal(childBody, &manifest); err != nil {
		t.Fatalf("decode child manifest: %v", err)
	}

	// Second GC: the child is untagged but reachable from the tagged index, so
	// it and its config/layers must survive. The amd64 sibling must stay absent.
	runRegistryGC(t, p.storageRoot)
	if !blobExists(t, p.storageRoot, indexDigest) {
		t.Fatalf("second GC removed index blob %s", indexDigest)
	}
	if !blobExists(t, p.storageRoot, arm64.Digest) {
		t.Fatalf("second GC removed arm64 manifest %s", arm64.Digest)
	}
	if !blobExists(t, p.storageRoot, manifest.Config.Digest) {
		t.Fatalf("second GC removed config %s", manifest.Config.Digest)
	}
	for _, layer := range manifest.Layers {
		if !blobExists(t, p.storageRoot, layer.Digest) {
			t.Fatalf("second GC removed layer %s", layer.Digest)
		}
	}
	if blobExists(t, p.storageRoot, amd64.Digest) {
		t.Fatalf("amd64 child %s present after arm64-only pull and GC", amd64.Digest)
	}

	// Both the tag and the lazily-copied child must still resolve after GC.
	if status, _, _ := doManifestRequest(t, p, http.MethodGet, tag); status != http.StatusOK {
		t.Fatalf("GET tag after second GC status = %d, want %d", status, http.StatusOK)
	}
	if status, _, _ := doManifestRequest(t, p, http.MethodGet, arm64.Digest); status != http.StatusOK {
		t.Fatalf("GET child after second GC status = %d, want %d", status, http.StatusOK)
	}
}
