package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func preprocessMiddleware(proxyAddr string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 匹配 /v2/<name>/manifests/<reference>，用 LastIndex 避免镜像名含 "manifests" 段时误判
		p := r.URL.Path
		if idx := strings.LastIndex(p, "/manifests/"); idx != -1 && strings.HasPrefix(p, "/v2/") {
			manifestDispatcherCustom(proxyAddr, r)
		}
		next.ServeHTTP(w, r)
	})
}

func NewProxy(proxyAddr, registryAddr string) {
	targetURL, _ := url.Parse("http://localhost" + registryAddr)
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	handlerWithMiddleware := preprocessMiddleware(proxyAddr, proxy)
	http.ListenAndServe(proxyAddr, handlerWithMiddleware)
}
