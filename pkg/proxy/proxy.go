package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func preprocessMiddleware(registryAddr string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 匹配 /v2/<name>/manifests/<reference>，用 LastIndex 避免镜像名含 "manifests" 段时误判
		p := r.URL.Path
		if idx := strings.LastIndex(p, "/manifests/"); idx != -1 && strings.HasPrefix(p, "/v2/") {
			if handler := manifestDispatcherCustom(registryAddr, r); handler != nil {
				handler.ServeHTTP(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// newProxyHandler builds the full proxy chain. A nil transport keeps the
// ReverseProxy default (http.DefaultTransport); tests inject the transport of
// an httptest in-memory server instead.
func newProxyHandler(registryAddr string, transport http.RoundTripper) http.Handler {
	targetURL := &url.URL{
		Scheme: "http",
		Host:   registryAddr,
	}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	if transport != nil {
		proxy.Transport = transport
	}
	return preprocessMiddleware(registryAddr, proxy)
}

func NewProxy(proxyAddr, registryAddr string) error {
	ln, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		return fmt.Errorf("listen proxy on %s: %w", proxyAddr, err)
	}
	return http.Serve(ln, newProxyHandler(registryAddr, nil))
}
