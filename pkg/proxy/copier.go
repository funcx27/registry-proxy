package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/regclient/regclient"
	"github.com/regclient/regclient/config"
	"github.com/regclient/regclient/scheme/reg"
	"github.com/regclient/regclient/types/manifest"
	"github.com/regclient/regclient/types/ref"
)

// imageCopier copies the manifest referenced by one proxy request. It is
// deliberately lazy: a tag that points at a multi-platform index only copies
// the index itself. Child manifests and their blobs are copied when a client
// requests a specific child digest.
type imageCopier struct {
	authHosts  []config.Host
	instanceID string

	mu      sync.Mutex
	clients map[string]*regclient.RegClient
}

func newImageCopier(authFile, instanceID string) (*imageCopier, error) {
	hosts, err := loadAuthHosts(authFile)
	if err != nil {
		return nil, err
	}
	return &imageCopier{
		authHosts:  hosts,
		instanceID: instanceID,
		clients:    make(map[string]*regclient.RegClient),
	}, nil
}

// loadAuthHosts keeps REGISTRY_AUTH_FILE compatibility with the existing Helm
// chart while still supporting the standard Docker config locations.
func loadAuthHosts(authFile string) ([]config.Host, error) {
	if authFile != "" {
		hosts, err := config.DockerLoadFile(authFile)
		if err != nil {
			return nil, fmt.Errorf("load REGISTRY_AUTH_FILE %q: %w", authFile, err)
		}
		return hosts, nil
	}

	hosts, err := config.DockerLoad()
	if err != nil {
		return nil, fmt.Errorf("load docker credentials: %w", err)
	}
	return hosts, nil
}

func (c *imageCopier) client(registryAddr string) *regclient.RegClient {
	c.mu.Lock()
	defer c.mu.Unlock()

	if rc := c.clients[registryAddr]; rc != nil {
		return rc
	}

	hosts := make([]config.Host, 0, len(c.authHosts)+1)
	hosts = append(hosts, c.authHosts...)
	hosts = append(hosts, config.Host{Name: registryAddr, TLS: config.TLSDisabled})

	transport := &loopGuardTransport{base: http.DefaultTransport, instanceID: c.instanceID}
	rc := regclient.New(
		regclient.WithConfigHost(hosts...),
		regclient.WithRegOpts(reg.WithHTTPClient(&http.Client{Transport: transport})),
	)
	c.clients[registryAddr] = rc
	return rc
}

// copyResult describes what one copy operation actually moved. A tag that
// points at a multi-platform index only copies the index itself, so reporting
// it as a full image copy would make the log look far cheaper than the pull the
// user observes.
type copyResult struct {
	// kind is "index" for a multi-platform manifest and "image" for a single
	// platform manifest with its config and layers.
	kind      string
	mediaType string
	// children is the number of manifests referenced by an index.
	children int
	// layers is the number of layers of a single platform image.
	layers int
	// size is the manifest size for an index and the total config+layer size
	// for a single platform image.
	size int64
}

// Copy makes image available in the local registry under localTarget, which
// is a repository plus :tag or @digest. The source and local repository may
// differ: Host/ns requests use a namespaced local path while the source keeps
// the upstream repository layout. Index manifests are stored unchanged so the
// top-level digest remains identical to the upstream registry; the referenced
// children are intentionally not copied until requested.
func (c *imageCopier) Copy(ctx context.Context, registryAddr, image, localTarget string) (copyResult, error) {
	src, err := ref.New(image)
	if err != nil {
		return copyResult{}, fmt.Errorf("parse source image %q: %w", image, err)
	}

	dstName := registryAddr + "/" + localTarget
	dst, err := ref.New(dstName)
	if err != nil {
		return copyResult{}, fmt.Errorf("parse destination image %q: %w", dstName, err)
	}

	rc := c.client(registryAddr)
	m, err := rc.ManifestGet(ctx, src)
	if err != nil {
		return copyResult{}, fmt.Errorf("get manifest %s: %w", src.CommonName(), err)
	}

	if m.IsList() {
		if err := rc.ManifestPut(ctx, dst, m); err != nil {
			return copyResult{}, fmt.Errorf("put manifest list %s: %w", dst.CommonName(), err)
		}
		result := copyResult{kind: "index", mediaType: m.GetDescriptor().MediaType}
		if indexer, ok := m.(manifest.Indexer); ok {
			if list, err := indexer.GetManifestList(); err == nil {
				result.children = len(list)
			}
		}
		if desc := m.GetDescriptor(); desc.Size > 0 {
			result.size = desc.Size
		}
		return result, nil
	}

	if err := rc.ImageCopy(ctx, src, dst); err != nil {
		return copyResult{}, fmt.Errorf("copy image %s to %s: %w", src.CommonName(), dst.CommonName(), err)
	}
	result := copyResult{kind: "image", mediaType: m.GetDescriptor().MediaType}
	if imager, ok := m.(manifest.Imager); ok {
		if layers, err := imager.GetLayers(); err == nil {
			result.layers = len(layers)
		}
		if size, err := imager.GetSize(); err == nil && size > 0 {
			result.size = size
		}
	}
	return result, nil
}
