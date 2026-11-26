# L1 end-to-end tests

These tests build and run the real `registry-proxy` binary. The binary starts
the internal distribution registry and pulls images from Docker Hub with
regclient, so the tests exercise the same path as production without containers,
Kubernetes, or Helm.

Run them with:

```bash
make e2e
```

Docker Hub is usually unreachable from this environment without an HTTP proxy.
The default is `http://127.0.0.1:10808`; override it with:

```bash
make e2e E2E_DOCKER_PROXY=http://127.0.0.1:10808
```

If the proxy is not reachable, the tests are skipped. Use `go test -v` directly
for the same behavior:

```bash
E2E_DOCKER_PROXY=http://127.0.0.1:10808 \
  go test -tags=e2e -v -count=1 -timeout=10m ./test/e2e
```

Covered scenarios:

- a cold tag request pulls the top-level index only;
- a later child-digest request lazily pulls that platform's manifest, config,
  and layers, leaving other architectures absent;
- concurrent tag requests perform a single copy;
- a missing upstream image returns `502`;
- `IMAGE_PULL_TIMEOUT` expiry returns `504`;
- GC keeps a tagged index whose child manifests are not yet copied, and after a
  lazy child pull it keeps that untagged child and its blobs because they are
  reachable from the index (it also creates missing `_layers` directories so
  distribution's sweep does not abort on index-only repositories).
