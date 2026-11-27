
GIT_TAG_VERSION := $(shell git describe --tags --exact-match --match 'v[0-9]*' HEAD 2>/dev/null | sed 's/^v//')
BRANCH_VERSION := $(shell git symbolic-ref --short HEAD 2>/dev/null | sed -nE 's/^v([0-9]+\.[0-9]+)$$/\1.0/p; s/^v([0-9]+\.[0-9]+\.[0-9]+)$$/\1/p')
VERSION ?= $(if $(GIT_TAG_VERSION),$(GIT_TAG_VERSION),$(if $(BRANCH_VERSION),$(BRANCH_VERSION),dev))
GIT_REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DISTRIBUTION_VERSION ?= $(shell go list -m -f '{{.Version}}' github.com/distribution/distribution/v3 2>/dev/null | sed 's/^v//')
LDFLAGS = -w -s \
	-X registry-proxy/pkg/version.Version=$(VERSION) \
	-X registry-proxy/pkg/version.Revision=$(GIT_REVISION) \
	-X registry-proxy/pkg/version.DistributionVersion=$(DISTRIBUTION_VERSION)

IMAGE_REPO ?= registry.aliyuncs.com/funcx27/registry
CHART_DIR ?= charts/registry-proxy
CHART_PACKAGE_DIR ?= dist
CHART_OCI_REPO ?= oci://ghcr.io/funcx27/charts
CHART_VERSION ?= $(shell awk '$$1 == "version:" { print $$2; exit }' $(CHART_DIR)/Chart.yaml)
CHART_APP_VERSION ?= $(shell awk '$$1 == "appVersion:" { gsub(/"/, "", $$2); print $$2; exit }' $(CHART_DIR)/Chart.yaml)
CHART_AUTH_VALUES ?= $(CHART_DIR)/ci/auth-values.yaml
CHART_AUTH_FILE ?= $(CHART_DIR)/ci/dockerconfig.json
CHART_REGISTRIES_VALUES ?= $(CHART_DIR)/ci/registries-values.yaml
DOCKER_BUILD = docker build ${DEBUG} --pull --push --builder=mybuilder --platform=linux/arm64,linux/amd64
amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -gcflags="all=-trimpath=${PWD}" -asmflags="all=-trimpath=${PWD}" -o bin/registry-amd64 main.go
	docker build --load -t $(IMAGE_REPO):$(VERSION) -f Dockerfile bin
	#  gzip bin/registry-amd64
arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o bin/registry-arm64 main.go
push: amd64 arm64
	$(DOCKER_BUILD) -t $(IMAGE_REPO):$(VERSION) -f Dockerfile bin
test: amd64
	docker rm -f test; docker run --name test -e DOCKERHUB_MIRROR=registry.dockermirror.com -d -p80:80 $(IMAGE_REPO):$(VERSION)
	docker logs -f test

E2E_DOCKER_PROXY ?= http://127.0.0.1:10808
e2e:
	E2E_DOCKER_PROXY=$(E2E_DOCKER_PROXY) go test -tags=e2e -v -count=1 -timeout=10m ./test/e2e

.PHONY: chart-lint chart-package chart-push e2e

chart-lint:
	helm lint --strict $(CHART_DIR)
	helm lint --strict $(CHART_DIR) --values $(CHART_AUTH_VALUES) --set-file upstreamAuth.dockerconfigjson=$(CHART_AUTH_FILE)
	helm template registry-proxy $(CHART_DIR) > /dev/null
	helm template registry-proxy $(CHART_DIR) --values $(CHART_AUTH_VALUES) --set-file upstreamAuth.dockerconfigjson=$(CHART_AUTH_FILE) > /dev/null
	helm lint --strict $(CHART_DIR) --values $(CHART_REGISTRIES_VALUES)
	helm template registry-proxy $(CHART_DIR) --values $(CHART_REGISTRIES_VALUES) > /dev/null
	helm template registry-proxy $(CHART_DIR) --set service.clusterIP=10.96.0.50 > /dev/null
	helm template registry-proxy $(CHART_DIR) --set persistence.type=pvc > /dev/null
	helm template registry-proxy $(CHART_DIR) --set service.port=8080 > /dev/null
	! helm template registry-proxy $(CHART_DIR) --set service.port=443 > /dev/null

chart-package: chart-lint
	mkdir -p $(CHART_PACKAGE_DIR)
	helm package $(CHART_DIR) --destination $(CHART_PACKAGE_DIR) --version $(CHART_VERSION) --app-version $(CHART_APP_VERSION)

chart-push:
	helm push $(CHART_PACKAGE_DIR)/registry-proxy-$(CHART_VERSION).tgz $(CHART_OCI_REPO)
