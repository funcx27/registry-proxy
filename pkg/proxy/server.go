package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/patrickmn/go-cache"
)

// server owns the state used by the HTTP proxy and by background image pulls.
type server struct {
	registryAddr string
	options      Options
	puller       *pullPool
	mappings     map[string]RegistryMapping
}

func newServer(registryAddr string, opts Options) (*server, error) {
	switch opts.CopyMode {
	case CopyModeSync, CopyModeAsync, CopyModeOff:
	default:
		return nil, fmt.Errorf("invalid image copy mode %q", opts.CopyMode)
	}

	mappings, err := loadRegistryMappings(opts.RegistryMappingPath)
	if err != nil {
		return nil, fmt.Errorf("load registry mappings: %w", err)
	}

	s := &server{
		registryAddr: registryAddr,
		options:      opts,
		mappings:     mappings,
	}

	if opts.CopyMode == CopyModeOff {
		// Native registry mode: routing normalization stays active, but no
		// upstream credentials, copier, cache, or worker pool is created.
		logger.Info("image copy mode: off (native registry mode)")
		logRegistryMappings(mappings, logger.Debugf)
	} else {
		copier, err := newImageCopier(opts.RegistryAuthFile)
		if err != nil {
			return nil, fmt.Errorf("initialize image copier: %w", err)
		}

		cache := cache.New(opts.RepullMinInterval, time.Hour)
		// Request waits may time out after a job is queued. Bound the copy
		// itself as well, otherwise a stuck upstream can occupy a worker
		// indefinitely.
		copyWithTimeout := func(ctx context.Context, address, image, localTarget string) error {
			ctx, cancel := context.WithTimeout(ctx, opts.PullTimeout)
			defer cancel()
			return copier.Copy(ctx, address, image, localTarget)
		}
		s.puller = newPullPool(context.Background(), opts.PullWorkers, opts.PullQueueSize, cache, copyWithTimeout)

		logger.Infof("image re-pull min interval: %s", opts.RepullMinInterval)
		logger.Infof("image pull timeout: %s", opts.PullTimeout)
		logger.Infof("image pull workers: %d, queue size: %d", opts.PullWorkers, opts.PullQueueSize)
		logger.Infof("image copy mode: %s", opts.CopyMode)
		logRegistryMappings(mappings, logger.Infof)
	}

	if opts.Cleanup.Enabled {
		logger.Infof("image clean interval: %s", opts.Cleanup.Interval)
		logger.Infof("image clean project: %s", opts.Cleanup.Projects)
		logger.Infof("image clean before: %s", opts.Cleanup.DeleteBefore)
		logger.Infof("image clean retain tag nums: %d", opts.Cleanup.TagRetain)
	}

	return s, nil
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
		route, err := s.resolveRoute(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if route != nil {
			route.apply(r)
			if s.options.CopyMode != CopyModeOff && route.endpoint == "manifests" &&
				(r.Method == http.MethodGet || r.Method == http.MethodHead) {
				if handler := s.manifestDispatcherCustom(r, route); handler != nil {
					handler.ServeHTTP(w, r)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
