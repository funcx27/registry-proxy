package main

import (
	_ "net/http/pprof"
	"registry-proxy/cmd"

	_ "github.com/distribution/distribution/v3/registry/auth/htpasswd"
	_ "github.com/distribution/distribution/v3/registry/auth/silly"
	_ "github.com/distribution/distribution/v3/registry/auth/token"
	_ "github.com/distribution/distribution/v3/registry/proxy"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/azure"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/filesystem"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/gcs"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/middleware/cloudfront"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/middleware/redirect"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/middleware/rewrite"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/s3-aws"
)

func main() {
	cmd.RootCmd.Execute()
}
