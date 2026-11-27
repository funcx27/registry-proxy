# registry-proxy Helm chart

Deploys `registry-proxy`, an on-demand Docker/OCI registry proxy backed by a
persistent local registry.

## Install

```bash
helm install registry-proxy ./charts/registry-proxy \
  --namespace registry-proxy \
  --create-namespace
```

The default chart uses `ghcr.io/funcx27/registry-proxy:1.0.1` and stores registry
data on the node under `/var/lib/registry-proxy` through a `hostPath` volume.
The host path is intentionally explicit so operators can inspect and back it up
directly.

```bash
helm install registry-proxy ./charts/registry-proxy \
  --namespace registry-proxy \
  --create-namespace \
  --set persistence.hostPath.path=/data/registry-proxy \
  --set service.type=LoadBalancer
```

For a multi-node cluster, use a PVC instead:

```bash
helm install registry-proxy ./charts/registry-proxy \
  --namespace registry-proxy \
  --create-namespace \
  --set persistence.type=pvc \
  --set persistence.size=100Gi
```

## Addressing the proxy

The proxy normalizes three equivalent client forms to the same local
repository, so path-mode and host-mode clients share one cache:

| Mode | Client request | Local repository |
| --- | --- | --- |
| path | `/v2/ghcr.io/org/app/manifests/v1` | `ghcr.io/org/app` |
| host | `Host: ghcr.io` + `/v2/org/app/manifests/v1` | `ghcr.io/org/app` |
| ns | `/v2/org/app/manifests/v1?ns=ghcr.io` | `ghcr.io/org/app` |

Path mode needs no configuration for public domains: the first path segment is
treated as the upstream registry when it is a valid DNS name. Host mode suits
clients that resolve an upstream hostname (for example through `/etc/hosts`) to
the proxy. Namespace mode is a fallback for runtimes that can rewrite neither
the Host header nor the path. Host and ns modes require an entry in
`config.registryMapping`; restricting them to configured names keeps the proxy's
own Service hostname from being mistaken for an upstream registry.

`config.registryMapping` also selects the upstream address and Docker Hub's
conventional `library` namespace:

```yaml
config:
  registryMapping:
    docker.io:
      defaultNamespace: library
    ghcr.io: {}
    quay.io:
      # Optional: copy through another host or registry-proxy, with an optional
      # repository prefix.
      registry: proxy.example.com/quay.io
    dockerhub:
      # Namespace mapping: keep dockerhub/alpine/socat in the upstream path.
      registry: upstream-proxy.example.com
```

A mapping key that is a DNS registry name uses registry semantics: the first
path component identifies the upstream and is not repeated in the upstream
repository path.

| Client request | Mapping | Upstream image |
| --- | --- | --- |
| `/v2/docker.io/alpine/socat/manifests/latest` | `docker.io` | `docker.io/alpine/socat:latest` |
| `/v2/docker.io/alpine/manifests/latest` | `docker.io` with `defaultNamespace: library` | `docker.io/library/alpine:latest` |
| `/v2/dockerhub/alpine/socat/manifests/latest` | `dockerhub` with `registry: upstream-proxy.example.com` | `upstream-proxy.example.com/dockerhub/alpine/socat:latest` |

Namespace keys preserve the full upstream path by default. Set
`preservePath: false` on a namespace mapping to remove the first path component,
or `preservePath: true` on a domain mapping to keep it. This makes it possible to
chain through another proxy without conflating a namespace such as `dockerhub`
with the `docker.io` registry.

When both a Host mapping and a namespace mapping match, the Host mapping wins.
An explicit registry domain in the first path component still takes precedence
over the Host header, so `/v2/docker.io/...` is always treated as Docker Hub.

The chart writes this table to `/var/lib/registry/registry-mapping.yaml`, the
default location derived from the registry configuration's filesystem root. Set
`REGISTRY_MAPPING_PATH` to use a different path, or point
`config.existingRegistryMappingConfigMap` at an existing ConfigMap containing
the `registry-mapping.yaml` key.

### containerd path mode

The client sends `/v2/ghcr.io/...` to the proxy:

```toml
# /etc/containerd/certs.d/ghcr.io/hosts.toml
server = "http://registry.example.com:5000/v2/ghcr.io"
override_path = true

[host."http://registry.example.com:5000/v2/ghcr.io"]
  capabilities = ["pull", "resolve"]
  override_path = true
```

### containerd host mode

The client sends `Host: ghcr.io` and `/v2/org/app/...` to the proxy. It needs a
mapping entry for `ghcr.io` and the proxy must be reachable under that name:

```toml
# /etc/containerd/certs.d/ghcr.io/hosts.toml
server = "http://registry.example.com:5000"
```

### Fixed ClusterIP for `/etc/hosts`

To keep a hostname mapping stable, pin the Service ClusterIP and use port 80:

```yaml
service:
  type: ClusterIP
  # Replace with an unused address from your cluster's Service CIDR.
  clusterIP: 192.0.2.10
  port: 80
```

Then map the registry hostname on a client that can route to the cluster service
CIDR:

```text
192.0.2.10 registry.example.com
```

The address must be unused and within the cluster's service CIDR. `clusterIP` is
immutable after Service creation; to change it, delete and recreate the Service.
A ClusterIP is normally reachable only from inside the cluster or from networks
that route to the service CIDR. For external clients, use NodePort, LoadBalancer,
or Ingress instead.

## Plain HTTP and the 443 fallback

registry-proxy serves plain HTTP on the main Service port, which defaults to
80. The Service always also exposes port 443 to the same plain-HTTP backend.
This is not a TLS endpoint. It exists because containerd and nerdctl probe
HTTPS first when `--insecure-registry` is set:

- Against a ClusterIP Service without a 443 port, the SYN is dropped and the
  probe blocks for about 30 seconds.
- nerdctl only retries over HTTP for `ErrSchemeMismatch` or
  `connection refused`; a dial timeout does not trigger the fallback.
- Mapping 443 to the plain-HTTP backend returns a fast plaintext response,
  which the client maps to `ErrSchemeMismatch` and immediately retries on the
  main HTTP port.

Typical client usage:

```bash
nerdctl --insecure-registry pull registry.example.com/docker.io/library/alpine:latest
```

The `--insecure-registry` flag is still required: it skips TLS verification and
allows the HTTP fallback. This chart does not terminate TLS. To expose the
registry over HTTPS, terminate TLS at an Ingress or L7 load balancer and
forward plain HTTP to the Service's main port.

### Ingress

Ingress uses `service.port` (80 by default). If `ingress.tls` is configured, the
Ingress controller terminates HTTPS and forwards HTTP to the Service. The 443
fallback port is not involved in Ingress traffic.

### Kubernetes and kubelet pulls

The 443 fallback is a nerdctl compatibility behavior. containerd's CRI plugin
does not use nerdctl's fallback logic, so kubelet image pulls need node-side
configuration. Install a `hosts.toml` under `/etc/containerd/certs.d/<registry>/`
and enable the directory in containerd:

```toml
# /etc/containerd/config.toml
[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/etc/containerd/certs.d"
```

The `hosts.toml` examples above then select path or host mode for CRI pulls.
Node bootstrap is intentionally not managed by this chart.

Do not expose this plain-HTTP Service directly to an untrusted network.

## Copy modes

`env.imageCopyMode` controls on-demand copying:

- `sync` copies the manifest before answering the manifest request.
- `async` answers immediately and copies in the background.
- `off` is native registry mode. No upstream credentials, copier, cache, or
  worker pool is created and nothing is pulled. Path/host/ns normalization
  still runs, so every entry point resolves to the same local repository.

## Logging

All logs share one format, regardless of which component writes them:

```text
time="2026-11-27 09:45:06" level=info msg="pulled image: ..."
```

`env.logLevel` (default `error`) controls distribution's internal registry
logger. `env.appLogLevel` (default `info`) controls registry-proxy's own
logger, which records normal pull progress and cleanup events. Expected
existence checks performed by regclient during a copy (404 responses for
manifests and blobs that are not cached yet) are filtered, because they are a
normal part of the copy protocol, not failures. A pull by tag also copies the
referenced index and child manifests by digest; those internal copies are
logged at `debug` so one client pull produces one completion line. Set
`appLogLevel=debug` to see every copy, or `appLogLevel=error` to keep only
failures.

## Upstream registry credentials

The application uses regclient to copy images. When `upstreamAuth.enabled=true`,
the chart:

1. mounts a Secret containing Docker config JSON at
   `/etc/registry-proxy/auth`;
2. sets `REGISTRY_AUTH_FILE` to
   `/etc/registry-proxy/auth/.dockerconfigjson`.

regclient reads this file for every proxied pull, so entries for multiple
registries can live in the same Secret.

### Convenient: username/password in values

The simplest option is to put registry accounts directly in values:

```yaml
upstreamAuth:
  enabled: true
  registries:
    registry.example.com:
      username: robot
      password: secret
    "https://index.docker.io/v1/":
      username: dockerhub-user
      password: dockerhub-token
```

The chart generates the Docker config JSON automatically:

```json
{
  "auths": {
    "registry.example.com": {
      "auth": "cm9ib3Q6c2VjcmV0"
    }
  }
}
```

`auth` is the base64 encoding of `username:password`. The map key must match the
registry server used in Docker config; for Docker Hub use
`https://index.docker.io/v1/`.

Values are stored in Helm release data and are visible to users who can read the
release. This is convenient for development or non-sensitive environments. For
production, prefer the existing Secret option below.

### Recommended for production: reference an existing Secret

Create the Secret from an existing Docker config file:

```bash
kubectl create namespace registry-proxy

kubectl -n registry-proxy create secret generic registry-proxy-upstream-auth \
  --from-file=.dockerconfigjson="${HOME}/.docker/config.json" \
  --type=kubernetes.io/dockerconfigjson
```

Or create it for one registry with `kubectl`:

```bash
kubectl -n registry-proxy create secret docker-registry registry-proxy-upstream-auth \
  --docker-server=registry.example.com \
  --docker-username=robot \
  --docker-password='<password-or-token>'
```

Reference it in values:

```yaml
upstreamAuth:
  enabled: true
  existingSecret: registry-proxy-upstream-auth
  mountPath: /etc/registry-proxy/auth
```

The Secret must store the Docker config JSON under the standard
`.dockerconfigjson` key. Both `kubectl create secret docker-registry` and the
`--from-file=.dockerconfigjson=...` command above produce this layout.

If your existing Secret uses a different key such as `config.json`, recreate it
with the standard key rather than adding a chart-specific key setting.

For Docker Hub, use the server key `https://index.docker.io/v1/` in the Docker
config JSON. This is what `docker login` writes and what regclient expects.

### Optional: create the Secret from a Docker config file

If you already have a complete Docker config file and do not want to create the
Secret with `kubectl`, pass it to Helm with `--set-file`:

```bash
helm install registry-proxy ./charts/registry-proxy \
  --namespace registry-proxy \
  --create-namespace \
  --set upstreamAuth.enabled=true \
  --set-file upstreamAuth.dockerconfigjson="${HOME}/.docker/config.json"
```

The relevant part of the chart template is:

```yaml
data:
  .dockerconfigjson: {{ .Values.upstreamAuth.dockerconfigjson | b64enc | quote }}
```

The `b64enc` call is only the encoding required by Kubernetes `Secret.data`;
the decoded value is the Docker config file exactly as supplied.

Helm cannot read an arbitrary host path from inside a template. Use
`--set-file` for host files. `.Files.Get` only reads files packaged inside the
chart, so do not put credentials in the chart directory.

The file content becomes part of Helm release data. For production, prefer
`existingSecret` managed by an external secret store.

### Optional: use a file packaged inside the chart

Helm can also render a JSON file packaged under the chart directory with
`.Files.Get`. Put the file under `charts/registry-proxy/files/`, for example
`files/config.json`, then set:

```yaml
upstreamAuth:
  enabled: true
  dockerConfigFile: files/config.json
```

This is useful for CI fixtures or demo charts. The chart template reads that
file directly:

```yaml
{{- $dockerConfigJSON := .Files.Get .Values.upstreamAuth.dockerConfigFile -}}
data:
  .dockerconfigjson: {{ $dockerConfigJSON | b64enc | quote }}
```

Important: `.Files.Get` only reads files included in the chart package. It
cannot read arbitrary paths on the machine running `helm install`. A file placed
under the chart directory is embedded in the `.tgz` package, so never commit
real credentials or publish a chart containing them. For production, use
`existingSecret` or `--set-file` instead.

Configure only one of `upstreamAuth.registries`, `upstreamAuth.dockerconfigjson`,
and `upstreamAuth.dockerConfigFile`. The chart rejects mixed generated inputs to
avoid silently ignoring credentials.

### Pulling the chart image from a private registry

`upstreamAuth` is for upstream image pulls performed by regclient. To pull the
`registry-proxy` container image itself from a private registry, configure
`imagePullSecrets`:

```yaml
image:
  repository: private.example.com/platform/registry-proxy
  tag: 1.0.1

imagePullSecrets:
  - name: private-registry-credentials
```

The same Secret can be used for both purposes if it contains all required
registry entries.

## Data storage

The registry always writes to `/var/lib/registry` inside the container. Select
how that directory is backed:

### hostPath (default)

```yaml
persistence:
  type: hostPath
  hostPath:
    path: /var/lib/registry-proxy
    type: DirectoryOrCreate
```

This is easy to inspect and back up, but the data is node-local. Pin the pod to
the intended node with `nodeSelector` or `affinity`; use a unique path per
release when running multiple instances on one node; and make sure the directory
has permissions compatible with the pod's `securityContext`. Kubernetes does
not apply `fsGroup` ownership changes to `hostPath` volumes, so a non-root
container usually needs a pre-created directory owned by its UID/GID.

If the pod is rescheduled to another node, it will use that node's directory.
For production or multi-node clusters, prefer a PVC.

### PVC

```yaml
persistence:
  type: pvc
  size: 20Gi
  storageClass: ""
  accessModes:
    - ReadWriteOnce
```

Set `persistence.existingClaim` to use an existing PVC instead of creating one:

```yaml
persistence:
  type: pvc
  existingClaim: registry-data
```

### emptyDir

```yaml
persistence:
  type: emptyDir
```

Data is deleted when the pod is removed. This is useful for tests only.

The chart defaults to a `Recreate` deployment strategy because a
`ReadWriteOnce` volume cannot normally be attached to two pods at once. If you use an RWX volume and set `replicaCount`
above one, change `deploymentStrategy` accordingly.

## Registry mapping

The routing table is loaded at startup from
`<storage rootdirectory>/registry-mapping.yaml` by default. For the container
image that is `/var/lib/registry/registry-mapping.yaml`. `REGISTRY_MAPPING_PATH`
overrides the location, which is useful when the storage driver does not expose
a filesystem root.

The chart can generate the table from values:

```yaml
config:
  registryMapping:
    docker.io:
      defaultNamespace: library
    ghcr.io: {}
    quay.io:
      registry: proxy.example.com/quay.io
```

Or mount an existing ConfigMap containing the `registry-mapping.yaml` key:

```yaml
config:
  existingRegistryMappingConfigMap: my-registry-mapping
```

A missing file is allowed and simply disables the mappings. A malformed file
fails startup instead of silently changing routing.

## Image cleanup

The container image enables tag cleanup by default. The chart explicitly
disables it unless `imageClean.enabled=true`:

```yaml
imageClean:
  enabled: true
  interval: 12h
  retainTags: 2
  project: test
  beforeDays: 1
```

`project` is a comma-separated list of top-level repository namespaces.

## Other configuration

| Value | Default | Description |
| --- | --- | --- |
| `replicaCount` | `1` | Number of pods. Keep at 1 with RWO storage. |
| `image.repository` | `ghcr.io/funcx27/registry-proxy` | Chart container image. |
| `image.tag` | `Chart.appVersion` | Image tag. |
| `image.digest` | `""` | Image digest; takes precedence over tag. |
| `enableServiceLinks` | `false` | Inject Service environment variables into the registry pod. |
| `service.type` | `ClusterIP` | Service type. |
| `service.clusterIP` | `""` | Optional fixed ClusterIP; immutable after creation. |
| `persistence.type` | `hostPath` | `hostPath`, `pvc`, or `emptyDir`. |
| `persistence.hostPath.path` | `/var/lib/registry-proxy` | Host directory backing `/var/lib/registry`. |
| `service.port` | `80` | Main plain-HTTP Service port. Port 443 is always added as a fallback to the same backend and cannot be used here. |
| `ingress.enabled` | `false` | Enable an Ingress. |
| `upstreamAuth.existingSecret` | `""` | Existing Docker config JSON Secret. |
| `upstreamAuth.dockerconfigjson` | `""` | Raw JSON supplied with `--set-file`. |
| `env.logLevel` | `error` | `REGISTRY_LOG_LEVEL`, distribution's internal logger. |
| `env.appLogLevel` | `info` | `APP_LOG_LEVEL`, registry-proxy's own logger. |
| `env.imageCopyMode` | `sync` | `sync`, `async`, or `off`. |
| `env.imagePullTimeout` | `5m` | Pull timeout. |
| `env.imagePullWorkers` | `2` | Number of workers executing image copies. |
| `env.imagePullQueueSize` | `0` | In-memory pull queue size. `0` uses direct worker handoff with backpressure. |
| `env.imageRepullMinInterval` | `5m` | Minimum re-pull interval. |
| `env.extra` | `[]` | Additional Kubernetes env entries. |
| `extraVolumes` / `extraVolumeMounts` | `[]` | Additional pod volumes and mounts. |

In `sync` mode, concurrent manifest requests for the same image are coalesced
into one copy, and copies run on at most `env.imagePullWorkers` workers. If the
copy fails or exceeds `env.imagePullTimeout`, the request fails with `502` (or
`504` on timeout) instead of being forwarded to the local registry.

Multi-platform images use lazy, client-driven copying. A tag request copies
only the top-level OCI index/manifest list. The child manifest for a specific
platform, together with its config and layers, is copied when the client
requests that child digest. There is intentionally no all-platforms mode:
clients pull only the platform they actually run. The registry configuration
therefore sets `validation.manifests.indexes.platforms: none` so distribution
accepts the top-level index before its children are present.

## Validate the chart

```bash
make chart-lint
make chart-package
```

The default Makefile variables package the chart as
`dist/registry-proxy-<version>.tgz`. Override `CHART_VERSION` and
`CHART_APP_VERSION` when packaging a release:

```bash
make chart-package CHART_VERSION=1.0.1 CHART_APP_VERSION=1.0.1
```

On `v*` tag pushes, GitHub Actions lints and packages the chart, uploads it as a
workflow artifact, and pushes it to GHCR as an OCI artifact:

```bash
helm install registry-proxy oci://ghcr.io/funcx27/charts/registry-proxy \
  --version 0.1.0 \
  --namespace registry-proxy \
  --create-namespace
```

Branch and pull-request builds still produce the workflow artifact but do not
publish to GHCR.
