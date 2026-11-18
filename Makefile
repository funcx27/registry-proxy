
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
DOCKER_BUILD = docker build ${DEBUG} --pull --push --builder=mybuilder --platform=linux/arm64,linux/amd64
amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -gcflags="all=-trimpath=${PWD}" -asmflags="all=-trimpath=${PWD}" -tags "containers_image_openpgp exclude_graphdriver_btrfs" -o bin/registry-amd64 main.go
	docker build --load -t $(IMAGE_REPO):$(VERSION) -f Dockerfile bin
	#  gzip bin/registry-amd64
arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -tags "containers_image_openpgp exclude_graphdriver_btrfs" -o bin/registry-arm64 main.go
push: amd64 arm64
	$(DOCKER_BUILD) -t $(IMAGE_REPO):$(VERSION) -f Dockerfile bin
test: amd64
	docker rm -f test; docker run --name test -e DOCKERHUB_MIRROR=registry.dockermirror.com -d -p80:80 $(IMAGE_REPO):$(VERSION)
	docker logs -f test
