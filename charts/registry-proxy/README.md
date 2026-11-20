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

The application selects the upstream registry from the Docker request `Host`
header. Point the upstream registry hostname (or a hostname listed in
`config.proxyMapping`) at this Service through DNS or an Ingress. If the public
hostname is not the upstream registry hostname, add a mapping entry:

```yaml
config:
  proxyMapping:
    proxy.example.com: registry-1.docker.io
```

### Fixed ClusterIP for `/etc/hosts`

To keep a hostname mapping stable, pin the Service ClusterIP and use port 80:

```yaml
service:
  type: ClusterIP
  clusterIP: 10.96.0.50
  port: 80
```

Then map the registry hostname on a client that can route to the cluster service
CIDR:

```text
10.96.0.50 registry.example.com
```

The address must be unused and within the cluster's service CIDR. `clusterIP` is
immutable after Service creation; to change it, delete and recreate the Service.
A ClusterIP is normally reachable only from inside the cluster or from networks
that route to the service CIDR. For external clients, use NodePort, LoadBalancer,
or Ingress instead.

When the Service is reached by IP, `DOCKERHUB_MIRROR` enables the special
Docker Hub library-image path (`/v2/library/...`). For example:

```yaml
env:
  dockerHubMirror: registry.dockermirror.com
```

```bash
kubectl port-forward -n registry-proxy svc/registry-proxy 5000:80
docker pull 127.0.0.1:5000/library/nginx:latest
```

## Upstream registry credentials

The application uses skopeo to copy images. When `upstreamAuth.enabled=true`,
the chart:

1. mounts a Secret containing Docker config JSON at
   `/etc/registry-proxy/auth`;
2. sets `REGISTRY_AUTH_FILE` to
   `/etc/registry-proxy/auth/.dockerconfigjson`.

Skopeo reads this file for every proxied pull, so entries for multiple
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
config JSON. This is what `docker login` writes and what skopeo expects.

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

`upstreamAuth` is for upstream image pulls performed by skopeo. To pull the
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

The pre-pull `.done` marker is stored in the same volume. The chart defaults to
a `Recreate` deployment strategy because a `ReadWriteOnce` volume cannot normally
be attached to two pods at once. If you use an RWX volume and set `replicaCount`
above one, change `deploymentStrategy` accordingly.

## Proxy host mapping

`/var/lib/registry/proxy-mapping.yaml` is loaded by the application at startup.
The chart can generate it from values:

```yaml
config:
  proxyMapping:
    docker.io: registry.dockermirror.com
```

Or mount an existing ConfigMap containing the `proxy-mapping.yaml` key:

```yaml
config:
  existingProxyMappingConfigMap: my-proxy-mapping
```

## Pre-pull images

```yaml
prePull:
  enabled: true
  images:
    - docker.io/library/nginx:1.27
    - ghcr.io/example/app:v1
```

The image list is written to a ConfigMap and passed through `IMAGE_LIST_FILE`.
An existing ConfigMap can be used with `prePull.existingConfigMap`.

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
| `service.type` | `ClusterIP` | Service type. |
| `service.clusterIP` | `""` | Optional fixed ClusterIP; immutable after creation. |
| `persistence.type` | `hostPath` | `hostPath`, `pvc`, or `emptyDir`. |
| `persistence.hostPath.path` | `/var/lib/registry-proxy` | Host directory backing `/var/lib/registry`. |
| `service.port` | `80` | Service port. |
| `ingress.enabled` | `false` | Enable an Ingress. |
| `upstreamAuth.existingSecret` | `""` | Existing Docker config JSON Secret. |
| `upstreamAuth.dockerconfigjson` | `""` | Raw JSON supplied with `--set-file`. |
| `env.logLevel` | `error` | `REGISTRY_LOG_LEVEL`. |
| `env.dockerHubMirror` | `""` | `DOCKERHUB_MIRROR`. |
| `env.imageCopyMode` | `sync` | `sync` or `async`. |
| `env.imagePullTimeout` | `5m` | Pull timeout. |
| `env.imageRepullMinInterval` | `5m` | Minimum re-pull interval. |
| `env.extra` | `[]` | Additional Kubernetes env entries. |
| `extraVolumes` / `extraVolumeMounts` | `[]` | Additional pod volumes and mounts. |

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
