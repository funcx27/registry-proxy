package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseLoopChain(t *testing.T) {
	header := make(http.Header)
	header.Add(loopChainHeader, "instance-a, instance-b")
	header.Add(loopChainHeader, "instance-c")

	chain, tooDeep := parseLoopChain(header)
	if tooDeep {
		t.Fatal("parseLoopChain() reported tooDeep = true")
	}
	want := []string{"instance-a", "instance-b", "instance-c"}
	if strings.Join(chain, ",") != strings.Join(want, ",") {
		t.Fatalf("chain = %v, want %v", chain, want)
	}
}

func TestParseLoopChainRejectsDeepChain(t *testing.T) {
	header := make(http.Header)
	for i := 0; i < maxProxyHops; i++ {
		header.Add(loopChainHeader, "instance")
	}

	chain, tooDeep := parseLoopChain(header)
	if !tooDeep {
		t.Fatalf("parseLoopChain() tooDeep = false, want true; chain length = %d", len(chain))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestLoopGuardTransportAppendsInstanceID(t *testing.T) {
	var got []string
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Header.Values(loopChainHeader)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})

	transport := &loopGuardTransport{base: base, instanceID: "instance-a"}
	req := httptest.NewRequest(http.MethodGet, "http://registry.example.com/v2/", nil)
	req = req.WithContext(contextWithLoopChain(req.Context(), []string{"instance-b"}))
	req.Header.Set(loopChainHeader, "stale-value")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	_ = resp.Body.Close()

	want := []string{"instance-b", "instance-a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("outbound chain = %v, want %v", got, want)
	}
	if got := req.Header.Get(loopChainHeader); got != "stale-value" {
		t.Fatalf("original request header = %q, want stale-value", got)
	}
}

func TestPreprocessMiddlewareLoopGuard(t *testing.T) {
	manyHops := make(http.Header)
	for i := 0; i < maxProxyHops; i++ {
		manyHops.Add(loopChainHeader, "other-instance")
	}

	tests := []struct {
		name            string
		header          http.Header
		wantStatus      int
		wantNext        bool
		wantChain       []string
		wantHeaderEmpty bool
	}{
		{
			name:            "direct self loop is rejected",
			header:          http.Header{loopChainHeader: []string{"instance-a"}},
			wantStatus:      http.StatusLoopDetected,
			wantHeaderEmpty: true,
		},
		{
			name:            "returning chain is rejected",
			header:          http.Header{loopChainHeader: []string{"instance-b", "instance-a"}},
			wantStatus:      http.StatusLoopDetected,
			wantHeaderEmpty: true,
		},
		{
			name:            "another instance is allowed",
			header:          http.Header{loopChainHeader: []string{"instance-b"}},
			wantStatus:      http.StatusOK,
			wantNext:        true,
			wantChain:       []string{"instance-b"},
			wantHeaderEmpty: true,
		},
		{
			name:            "too many hops is rejected",
			header:          manyHops,
			wantStatus:      http.StatusLoopDetected,
			wantHeaderEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &server{instanceID: "instance-a", options: Options{CopyMode: CopyModeOff}}
			nextCalled := false
			var gotChain []string
			var gotHeader string
			handler := srv.preprocessMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				gotChain = loopChainFromContext(r.Context())
				gotHeader = r.Header.Get(loopChainHeader)
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodHead, "http://registry.example.com/v2/", nil)
			req.Header = tt.header.Clone()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if nextCalled != tt.wantNext {
				t.Fatalf("next called = %v, want %v", nextCalled, tt.wantNext)
			}
			if tt.wantNext {
				if strings.Join(gotChain, ",") != strings.Join(tt.wantChain, ",") {
					t.Fatalf("context chain = %v, want %v", gotChain, tt.wantChain)
				}
			}
			if tt.wantHeaderEmpty && gotHeader != "" {
				t.Fatalf("forwarded loop header = %q, want empty", gotHeader)
			}
		})
	}
}

func TestPullPoolCarriesLoopChain(t *testing.T) {
	gotChain := make(chan []string, 1)
	pool := newPullPool(t.Context(), 1, 0, nil, func(ctx context.Context, _, _, _ string) error {
		gotChain <- loopChainFromContext(ctx)
		return nil
	})
	t.Cleanup(pool.Close)

	ctx := contextWithLoopChain(t.Context(), []string{"instance-b"})
	if err := pool.ensure(ctx, "127.0.0.1:5000", "ghcr.io/org/app:latest", "ghcr.io/org/app:latest"); err != nil {
		t.Fatalf("ensure() error = %v", err)
	}

	select {
	case chain := <-gotChain:
		if strings.Join(chain, ",") != "instance-b" {
			t.Fatalf("copy chain = %v, want [instance-b]", chain)
		}
	case <-time.After(time.Second):
		t.Fatal("copy did not receive loop chain")
	}
}

func TestAsyncManifestPullPreservesLoopChain(t *testing.T) {
	gotChain := make(chan []string, 1)
	srv := newTestProxyServer(t, Options{CopyMode: CopyModeAsync}, map[string]RegistryMapping{
		"ghcr.io": {},
	}, func(ctx context.Context, _, _, _ string) error {
		gotChain <- loopChainFromContext(ctx)
		return nil
	})

	var upstreamCalls int32
	upstream := newUpstream(t, &upstreamCalls)
	proxyServer := httptest.NewTestServer(t, srv.handler(upstream.Client().Transport))
	client := proxyServer.Client()

	req, err := http.NewRequest(http.MethodHead, proxyServer.URL+manifestProxyPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "ghcr.io"
	req.Header.Set(loopChainHeader, "previous-instance")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	select {
	case chain := <-gotChain:
		if strings.Join(chain, ",") != "previous-instance" {
			t.Fatalf("async copy chain = %v, want [previous-instance]", chain)
		}
	case <-time.After(time.Second):
		t.Fatal("async copy did not receive loop chain")
	}
}

// loopForwardHandler exercises the same inbound loop check and outbound
// loopGuardTransport used by the production proxy, but replaces the local
// registry with another HTTP endpoint. It lets tests build proxy topologies
// without starting a distribution registry.
func loopForwardHandler(srv *server, targetURL func() string) http.Handler {
	transport := &loopGuardTransport{base: http.DefaultTransport, instanceID: srv.instanceID}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, err := url.Parse(targetURL())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		outbound := r.Clone(r.Context())
		outbound.URL.Scheme = target.Scheme
		outbound.URL.Host = target.Host
		outbound.RequestURI = ""

		resp, err := (&http.Client{Transport: transport, Timeout: 2 * time.Second}).Do(outbound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
	return srv.preprocessMiddleware(next)
}

func loopTestServer(t *testing.T, srv *server) (*httptest.Server, func(string)) {
	t.Helper()
	var target string
	server := httptest.NewServer(loopForwardHandler(srv, func() string { return target }))
	t.Cleanup(server.Close)
	return server, func(value string) { target = value }
}

func TestProxyLoopIntegration(t *testing.T) {
	mappings := map[string]RegistryMapping{"ghcr.io": {}}
	const requestPath = "/v2/ghcr.io/org/app/manifests/latest"

	t.Run("single instance detects its own request", func(t *testing.T) {
		srvA := &server{instanceID: "instance-a", options: Options{CopyMode: CopyModeOff}, mappings: mappings}
		proxyA, setTargetA := loopTestServer(t, srvA)
		setTargetA(proxyA.URL)

		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Head(proxyA.URL + requestPath)
		if err != nil {
			t.Fatalf("HEAD: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusLoopDetected {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusLoopDetected)
		}
		if got := resp.Header.Get("Docker-Distribution-API-Version"); got != "registry/2.0" {
			t.Fatalf("Docker-Distribution-API-Version = %q, want registry/2.0", got)
		}
	})

	t.Run("two proxies detect A to B to A", func(t *testing.T) {
		srvA := &server{instanceID: "instance-a", options: Options{CopyMode: CopyModeOff}, mappings: mappings}
		srvB := &server{instanceID: "instance-b", options: Options{CopyMode: CopyModeOff}, mappings: mappings}
		proxyA, setTargetA := loopTestServer(t, srvA)
		proxyB, setTargetB := loopTestServer(t, srvB)
		setTargetA(proxyB.URL)
		setTargetB(proxyA.URL)

		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Head(proxyA.URL + requestPath)
		if err != nil {
			t.Fatalf("HEAD: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusLoopDetected {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusLoopDetected)
		}
	})

	t.Run("two proxies forward to an upstream successfully", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(upstream.Close)

		srvA := &server{instanceID: "instance-a", options: Options{CopyMode: CopyModeOff}, mappings: mappings}
		srvB := &server{instanceID: "instance-b", options: Options{CopyMode: CopyModeOff}, mappings: mappings}
		proxyA, setTargetA := loopTestServer(t, srvA)
		proxyB, setTargetB := loopTestServer(t, srvB)
		setTargetA(proxyB.URL)
		setTargetB(upstream.URL)

		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Head(proxyA.URL + requestPath)
		if err != nil {
			t.Fatalf("HEAD: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
	})
}
