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
			manifestDispatcherCustom(registryAddr, r)
		}
		next.ServeHTTP(w, r)
	})
}

func NewProxy(proxyAddr, registryAddr string) error {
	targetURL := &url.URL{
		Scheme: "http",
		Host:   registryAddr,
	}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	ln, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		return fmt.Errorf("listen proxy on %s: %w", proxyAddr, err)
	}

	handlerWithMiddleware := preprocessMiddleware(registryAddr, proxy)
	return http.Serve(ln, handlerWithMiddleware)
}
