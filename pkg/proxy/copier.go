package proxy

import (
	"context"
	"fmt"
	"sync"

	"github.com/regclient/regclient"
	"github.com/regclient/regclient/config"
	"github.com/regclient/regclient/types/ref"
)

// imageCopier copies the manifest referenced by one proxy request. It is
// deliberately lazy: a tag that points at a multi-platform index only copies
// the index itself. Child manifests and their blobs are copied when a client
// requests a specific child digest.
type imageCopier struct {
	authHosts []config.Host

	mu      sync.Mutex
	clients map[string]*regclient.RegClient
}

func newImageCopier(authFile string) (*imageCopier, error) {
	hosts, err := loadAuthHosts(authFile)
	if err != nil {
		return nil, err
	}
	return &imageCopier{
		authHosts: hosts,
		clients:   make(map[string]*regclient.RegClient),
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

	rc := regclient.New(regclient.WithConfigHost(hosts...))
	c.clients[registryAddr] = rc
	return rc
}

// Copy makes image available in the local registry. The image string may use
// either :tag or @digest. Index manifests are stored unchanged so the top-level
// digest remains identical to the upstream registry; the referenced children
// are intentionally not copied until requested.
func (c *imageCopier) Copy(ctx context.Context, registryAddr, image string) error {
	src, err := ref.New(image)
	if err != nil {
		return fmt.Errorf("parse source image %q: %w", image, err)
	}

	dstName := registryAddr + "/" + src.Repository
	if src.Digest != "" {
		dstName += "@" + src.Digest
	} else {
		dstName += ":" + src.Tag
	}
	dst, err := ref.New(dstName)
	if err != nil {
		return fmt.Errorf("parse destination image %q: %w", dstName, err)
	}

	rc := c.client(registryAddr)
	m, err := rc.ManifestGet(ctx, src)
	if err != nil {
		return fmt.Errorf("get manifest %s: %w", src.CommonName(), err)
	}

	if m.IsList() {
		if err := rc.ManifestPut(ctx, dst, m); err != nil {
			return fmt.Errorf("put manifest list %s: %w", dst.CommonName(), err)
		}
		return nil
	}

	if err := rc.ImageCopy(ctx, src, dst); err != nil {
		return fmt.Errorf("copy image %s to %s: %w", src.CommonName(), dst.CommonName(), err)
	}
	return nil
}
