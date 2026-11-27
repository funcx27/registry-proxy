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
	instanceID   string
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
	instanceID, err := newProxyInstanceID()
	if err != nil {
		return nil, err
	}

	s := &server{
		registryAddr: registryAddr,
		instanceID:   instanceID,
		options:      opts,
		mappings:     mappings,
	}
	logger.Debugf("proxy instance ID: %s", instanceID)
	logger.Debugf("host mode: DNS Host headers are treated as upstream registries when no mapping matches")

	if opts.CopyMode == CopyModeOff {
		// Native registry mode: routing normalization stays active, but no
		// upstream credentials, copier, cache, or worker pool is created.
		logger.Info("image copy mode: off (native registry mode)")
		logRegistryMappings(mappings, logger.Debugf)
	} else {
		copier, err := newImageCopier(opts.RegistryAuthFile, instanceID)
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
		chain, tooDeep := parseLoopChain(r.Header)
		if tooDeep {
			s.rejectProxyLoop(w, r, len(chain), fmt.Sprintf("proxy chain reached the maximum of %d hops; check registry mappings and upstream addresses", maxProxyHops))
			return
		}
		if s.instanceID != "" && chainContains(chain, s.instanceID) {
			s.rejectProxyLoop(w, r, len(chain), "proxy loop detected: upstream request returned to the same registry-proxy instance; check registry mapping and upstream DNS")
			return
		}
		if len(chain) > 0 {
			r = r.WithContext(contextWithLoopChain(r.Context(), chain))
		}
		// The chain is propagated by loopGuardTransport from request context.
		// Do not forward the internal header to the local registry or clients.
		r.Header.Del(loopChainHeader)

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
