//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	upstreamHost   = "docker.io"
	defaultProxy   = "http://127.0.0.1:10808"
	manifestAccept = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"
)

var (
	binaryPath  string
	repoRoot    string
	dockerProxy string
)

func TestMain(m *testing.M) {
	var err error
	repoRoot, err = findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "find repository root: %v\n", err)
		os.Exit(1)
	}

	buildDir, err := os.MkdirTemp("", "registry-proxy-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create build directory: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(buildDir) }()

	binaryPath = filepath.Join(buildDir, "registry-proxy")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build registry-proxy: %v\n%s\n", err, out)
		os.Exit(1)
	}

	dockerProxy = os.Getenv("E2E_DOCKER_PROXY")
	if dockerProxy == "" {
		dockerProxy = defaultProxy
	}

	os.Exit(m.Run())
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type proxyProcess struct {
	addr        string
	storageRoot string
	cmd         *exec.Cmd
	logs        *syncBuffer
}

func startProxy(t *testing.T, extraEnv map[string]string) *proxyProcess {
	t.Helper()

	requireDockerProxy(t)

	root := t.TempDir()
	storageRoot := filepath.Join(root, "registry")
	addr := freeAddr(t)
	configPath := filepath.Join(root, "config.yml")
	config := fmt.Sprintf(`version: 0.1
log:
  level: error
  accesslog:
    disabled: true
storage:
  cache:
    blobdescriptor: inmemory
  filesystem:
    rootdirectory: %q
http:
  addr: %q
health:
  storagedriver:
    enabled: true
    interval: 10s
    threshold: 3
validation:
  manifests:
    indexes:
      platforms: none
`, storageRoot, addr)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := map[string]string{
		"IMAGE_COPY_MODE":           "sync",
		"DOCKERHUB_MIRROR":          upstreamHost,
		"IMAGE_PULL_TIMEOUT":        "2m",
		"IMAGE_REPULL_MIN_INTERVAL": "1h",
		"IMAGE_PULL_WORKERS":        "2",
		"IMAGE_PULL_QUEUE_SIZE":     "0",
		"HTTP_PROXY":                dockerProxy,
		"HTTPS_PROXY":               dockerProxy,
		"NO_PROXY":                  "127.0.0.1,localhost",
		"OTEL_TRACES_EXPORTER":      "none",
	}
	for key, value := range extraEnv {
		env[key] = value
	}

	logs := &syncBuffer{}
	cmd := exec.Command(binaryPath, "serve", configPath)
	cmd.Dir = root
	cmd.Env = append(cleanEnv(), envList(env)...)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start registry-proxy: %v", err)
	}

	p := &proxyProcess{addr: addr, storageRoot: storageRoot, cmd: cmd, logs: logs}
	t.Cleanup(func() {
		stopProxy(cmd)
		if t.Failed() {
			t.Logf("registry-proxy logs:\n%s", logs.String())
		}
	})
	waitReady(t, p)
	return p
}

func stopProxy(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func cleanEnv() []string {
	blocked := map[string]struct{}{
		"DOCKERHUB_MIRROR":          {},
		"HTTP_PROXY":                {},
		"HTTPS_PROXY":               {},
		"IMAGE_COPY_MODE":           {},
		"IMAGE_PULL_QUEUE_SIZE":     {},
		"IMAGE_PULL_TIMEOUT":        {},
		"IMAGE_PULL_WORKERS":        {},
		"IMAGE_REPULL_MIN_INTERVAL": {},
		"NO_PROXY":                  {},
		"OTEL_TRACES_EXPORTER":      {},
		"REGISTRY_AUTH_FILE":        {},
		"http_proxy":                {},
		"https_proxy":               {},
		"no_proxy":                  {},
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if _, ok := blocked[key]; ok {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func requireDockerProxy(t *testing.T) {
	t.Helper()
	u, err := url.Parse(dockerProxy)
	if err != nil {
		t.Fatalf("parse E2E_DOCKER_PROXY=%q: %v", dockerProxy, err)
	}
	host := u.Host
	if host == "" {
		host = u.Path
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Skipf("Docker Hub proxy %s is not reachable: %v", host, err)
	}
	_ = conn.Close()
}

func waitReady(t *testing.T, p *proxyProcess) {
	t.Helper()
	client := directClient()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + p.addr + "/v2/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("registry-proxy did not become ready; logs:\n%s", p.logs.String())
}

func directClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   3 * time.Second,
	}
}

func proxyClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   3 * time.Minute,
	}
}

func doManifestRequest(t *testing.T, p *proxyProcess, method, reference string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+p.addr+"/v2/library/alpine/manifests/"+reference, nil)
	if err != nil {
		t.Fatalf("new manifest request: %v", err)
	}
	req.Host = upstreamHost
	req.Header.Set("Accept", manifestAccept)

	resp, err := proxyClient().Do(req)
	if err != nil {
		t.Fatalf("%s manifest %s: %v", method, reference, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read manifest %s body: %v", reference, err)
	}
	return resp.StatusCode, resp.Header.Clone(), body
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}

type indexDoc struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []descriptor `json:"manifests"`
}

type manifestDoc struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

func TestLazyIndexPullCopiesOnlyRequestedPlatform(t *testing.T) {
	p := startProxy(t, nil)

	const tag = "3.20"
	status, headers, body := doManifestRequest(t, p, http.MethodGet, tag)
	if status != http.StatusOK {
		t.Fatalf("GET tag status = %d, want %d; body=%s", status, http.StatusOK, body)
	}

	indexDigest := headers.Get("Docker-Content-Digest")
	if indexDigest == "" {
		t.Fatal("Docker-Content-Digest header is empty for index")
	}
	if got := digestOf(body); got != indexDigest {
		t.Fatalf("index digest = %s, want %s", got, indexDigest)
	}
	if !strings.Contains(headers.Get("Content-Type"), "index") &&
		!strings.Contains(headers.Get("Content-Type"), "manifest.list") {
		t.Fatalf("Content-Type = %q, want an index media type", headers.Get("Content-Type"))
	}

	var index indexDoc
	if err := json.Unmarshal(body, &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if len(index.Manifests) < 2 {
		t.Fatalf("index has %d manifests, want at least 2", len(index.Manifests))
	}
	arm64 := findPlatform(t, index.Manifests, "arm64")
	amd64 := findPlatform(t, index.Manifests, "amd64")
	if arm64.Digest == amd64.Digest {
		t.Fatal("arm64 and amd64 descriptors have the same digest")
	}

	// The tag request must store the index and nothing else.
	if got := countBlobs(t, p.storageRoot); got != 1 {
		t.Fatalf("blobs after tag pull = %d, want 1 (index only)", got)
	}
	if !blobExists(t, p.storageRoot, indexDigest) {
		t.Fatalf("index blob %s is missing", indexDigest)
	}
	if blobExists(t, p.storageRoot, arm64.Digest) {
		t.Fatalf("arm64 child manifest %s was copied eagerly", arm64.Digest)
	}

	// Requesting the arm64 child digest lazily copies that platform only.
	status, childHeaders, childBody := doManifestRequest(t, p, http.MethodGet, arm64.Digest)
	if status != http.StatusOK {
		t.Fatalf("GET child status = %d, want %d; body=%s", status, http.StatusOK, childBody)
	}
	if got := childHeaders.Get("Docker-Content-Digest"); got != arm64.Digest {
		t.Fatalf("child digest header = %q, want %q", got, arm64.Digest)
	}
	if got := digestOf(childBody); got != arm64.Digest {
		t.Fatalf("child body digest = %s, want %s", got, arm64.Digest)
	}

	var manifest manifestDoc
	if err := json.Unmarshal(childBody, &manifest); err != nil {
		t.Fatalf("decode child manifest: %v", err)
	}
	if manifest.Config.Digest == "" {
		t.Fatal("child manifest has no config descriptor")
	}
	if len(manifest.Layers) == 0 {
		t.Fatal("child manifest has no layers")
	}

	if !blobExists(t, p.storageRoot, arm64.Digest) {
		t.Fatalf("child manifest blob %s is missing", arm64.Digest)
	}
	if !blobExists(t, p.storageRoot, manifest.Config.Digest) {
		t.Fatalf("config blob %s is missing", manifest.Config.Digest)
	}
	for _, layer := range manifest.Layers {
		if !blobExists(t, p.storageRoot, layer.Digest) {
			t.Fatalf("layer blob %s is missing", layer.Digest)
		}
	}

	// The other architecture must still be absent.
	if blobExists(t, p.storageRoot, amd64.Digest) {
		t.Fatalf("amd64 child manifest %s was copied with the arm64 request", amd64.Digest)
	}

	uniqueBlobs := 1 + 1 + 1 + len(manifest.Layers) // index, manifest, config, layers
	wantBlobs := uniqueBlobs
	seen := map[string]struct{}{}
	for _, layer := range manifest.Layers {
		if _, ok := seen[layer.Digest]; ok {
			wantBlobs--
			continue
		}
		seen[layer.Digest] = struct{}{}
	}
	if got := countBlobs(t, p.storageRoot); got != wantBlobs {
		t.Fatalf("blobs after child pull = %d, want %d", got, wantBlobs)
	}

	if got := strings.Count(p.logs.String(), "pulling image: "+upstreamHost+"/library/alpine:"+tag); got != 1 {
		t.Fatalf("tag copy log count = %d, want 1", got)
	}
	if got := strings.Count(p.logs.String(), "pulling image: "+upstreamHost+"/library/alpine@"+arm64.Digest); got != 1 {
		t.Fatalf("child copy log count = %d, want 1", got)
	}
}

func TestConcurrentTagRequestsPullOnce(t *testing.T) {
	p := startProxy(t, nil)

	const (
		tag      = "3.19"
		requests = 8
	)
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	wg.Add(requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodHead, "http://"+p.addr+"/v2/library/alpine/manifests/"+tag, nil)
			if err != nil {
				errs <- err
				return
			}
			req.Host = upstreamHost
			req.Header.Set("Accept", manifestAccept)
			resp, err := proxyClient().Do(req)
			if err != nil {
				errs <- err
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("status = %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent HEAD failed: %v", err)
	}

	needle := "pulling image: " + upstreamHost + "/library/alpine:" + tag
	if got := strings.Count(p.logs.String(), needle); got != 1 {
		t.Fatalf("copy log count = %d, want 1; logs:\n%s", got, p.logs.String())
	}
}

func TestMissingImageReturnsBadGateway(t *testing.T) {
	p := startProxy(t, nil)

	reference := fmt.Sprintf("missing-%d", time.Now().UnixNano())
	status, _, body := doManifestRequest(t, p, http.MethodHead, reference)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", status, http.StatusBadGateway, body)
	}
}

func TestPullTimeoutReturnsGatewayTimeout(t *testing.T) {
	p := startProxy(t, map[string]string{"IMAGE_PULL_TIMEOUT": "1ns"})

	status, _, body := doManifestRequest(t, p, http.MethodHead, "3.20")
	if status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d; body=%s", status, http.StatusGatewayTimeout, body)
	}
}

func findPlatform(t *testing.T, manifests []descriptor, arch string) descriptor {
	t.Helper()
	for _, m := range manifests {
		if m.Platform.OS == "linux" && m.Platform.Architecture == arch {
			return m
		}
	}
	t.Fatalf("index has no linux/%s manifest", arch)
	return descriptor{}
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func blobExists(t *testing.T, storageRoot, digest string) bool {
	t.Helper()
	path, err := blobPath(storageRoot, digest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(path)
	return err == nil
}

func blobPath(storageRoot, digest string) (string, error) {
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if len(hexDigest) < 2 || len(hexDigest) != 64 {
		return "", fmt.Errorf("invalid sha256 digest %q", digest)
	}
	return filepath.Join(storageRoot, "docker", "registry", "v2", "blobs", "sha256", hexDigest[:2], hexDigest, "data"), nil
}

func countBlobs(t *testing.T, storageRoot string) int {
	t.Helper()
	blobsDir := filepath.Join(storageRoot, "docker", "registry", "v2", "blobs")
	count := 0
	err := filepath.WalkDir(blobsDir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "data" {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk blobs: %v", err)
	}
	return count
}
