package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/patrickmn/go-cache"
	"gopkg.in/yaml.v3"
)

const proxyMappingPath = "/var/lib/registry/proxy-mapping.yaml"

// server owns the state used by the HTTP proxy and by background image pulls.
type server struct {
	registryAddr string
	options      Options
	puller       *pullPool
	mapping      map[string]string
}

func newServer(registryAddr string, opts Options) (*server, error) {
	copier, err := newImageCopier(opts.RegistryAuthFile)
	if err != nil {
		return nil, fmt.Errorf("initialize image copier: %w", err)
	}

	cache := cache.New(opts.RepullMinInterval, time.Hour)
	// Request waits may time out after a job is queued. Bound the copy itself
	// as well, otherwise a stuck upstream can occupy a worker indefinitely.
	copyWithTimeout := func(ctx context.Context, address, image string) error {
		ctx, cancel := context.WithTimeout(ctx, opts.PullTimeout)
		defer cancel()
		return copier.Copy(ctx, address, image)
	}
	puller := newPullPool(context.Background(), opts.PullWorkers, opts.PullQueueSize, cache, copyWithTimeout)

	logger.Infof("image re-pull min interval: %s", opts.RepullMinInterval)
	logger.Infof("image pull timeout: %s", opts.PullTimeout)
	logger.Infof("image pull workers: %d, queue size: %d", opts.PullWorkers, opts.PullQueueSize)
	logger.Infof("image copy mode: %s", opts.CopyMode)
	logger.Infof("dockerhub mirror: %s", opts.DockerHubMirror)
	if opts.Cleanup.Enabled {
		logger.Infof("image clean interval: %s", opts.Cleanup.Interval)
		logger.Infof("image clean project: %s", opts.Cleanup.Projects)
		logger.Infof("image clean before: %s", opts.Cleanup.DeleteBefore)
		logger.Infof("image clean retain tag nums: %d", opts.Cleanup.TagRetain)
	}

	return &server{
		registryAddr: registryAddr,
		options:      opts,
		puller:       puller,
		mapping:      loadProxyMapping(),
	}, nil
}

// Close stops the pull workers. It is safe to call more than once.
func (s *server) Close() {
	if s.puller != nil {
		s.puller.Close()
	}
}

// handler builds the full proxy chain. A nil transport keeps the ReverseProxy
// default (http.DefaultTransport); tests inject the transport of an httptest
// in-memory server instead.
func (s *server) handler(transport http.RoundTripper) http.Handler {
	targetURL := &url.URL{
		Scheme: "http",
		Host:   s.registryAddr,
	}
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	if transport != nil {
		reverseProxy.Transport = transport
	}
	return s.preprocessMiddleware(reverseProxy)
}

func (s *server) preprocessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 匹配 /v2/<name>/manifests/<reference>，用 LastIndex 避免镜像名含 "manifests" 段时误判
		p := r.URL.Path
		if idx := strings.LastIndex(p, "/manifests/"); idx != -1 && strings.HasPrefix(p, "/v2/") {
			if handler := s.manifestDispatcherCustom(r); handler != nil {
				handler.ServeHTTP(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loadProxyMapping() map[string]string {
	mapping := make(map[string]string)
	b, err := os.ReadFile(proxyMappingPath)
	if err != nil {
		return mapping
	}
	if err := yaml.Unmarshal(b, &mapping); err != nil {
		logger.Warnf("parse %s: %v", proxyMappingPath, err)
		return make(map[string]string)
	}
	for k, v := range mapping {
		logger.Infof("registry proxy host mapping: %s -> %s", k, v)
	}
	return mapping
}
