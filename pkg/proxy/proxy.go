package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func preprocessMiddleware(proxyAddr string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlArrary := strings.Split(r.URL.Path, "/")
		if len(urlArrary) > 2 && urlArrary[len(urlArrary)-2] == "manifests" {
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
