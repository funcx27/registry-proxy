
DOCKER_BUILD = docker build ${DEBUG} --pull --push --builder=mybuilder --platform=linux/arm64,linux/amd64
amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-w -s' -gcflags="all=-trimpath=${PWD}" -asmflags="all=-trimpath=${PWD}" -tags "containers_image_openpgp exclude_graphdriver_btrfs containers_image_openpgp" -o bin/registry-amd64  main.go
	docker build --load -t registry.aliyuncs.com/funcx27/registry:3  -f Dockerfile bin
	#  gzip bin/registry-amd64
arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build  -tags "containers_image_openpgp exclude_graphdriver_btrfs containers_image_openpgp" -o bin/registry-arm64  cmd/registry/main.go
push: amd64 arm64
	$(DOCKER_BUILD) -t registry.aliyuncs.com/funcx27/registry:3  -f Dockerfile-new bin
	# skopeo copy docker://registry.aliyuncs.com/funcx27/registry:3 docker://registry.kubeease.cn/ops/registry:3 --all
test: amd64
	docker rm -f test; docker run --name test -e DOCKERHUB_MIRROR=registry.dockermirror.com -d -p80:80 registry.aliyuncs.com/funcx27/registry:3
	docker logs -f test