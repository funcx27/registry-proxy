package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"registry-proxy/pkg/proxy"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry"
	"github.com/distribution/distribution/v3/version"
	"github.com/docker/go-metrics"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var ServeCmd = &cobra.Command{
	Use:   "serve <config>",
	Short: "`serve` stores and distributes Docker images",
	Long:  "`serve` stores and distributes Docker images.",
	Run: func(cmd *cobra.Command, args []string) {
		// setup context
		type versionKey struct{}
		ctx := context.WithValue(context.Background(), versionKey{}, version.Version())
		config, err := resolveConfiguration(args)
		var registryAddr = ":50031"
		go proxy.NewProxy(config.HTTP.Addr, registryAddr)
		go proxy.CleanImage(ctx, config)
		config.HTTP.Addr = registryAddr
		if err != nil {
			fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
			// nolint:errcheck
			cmd.Usage()
			os.Exit(1)
		}
		reg, err := registry.NewRegistry(ctx, config)
		if err != nil {
			logrus.Fatalln(err)
		}
		configureDebugServer(config)
		if err = reg.ListenAndServe(); err != nil {
			logrus.Fatalln(err)
		}
	},
}

func resolveConfiguration(args []string) (*configuration.Configuration, error) {
	var configurationPath string

	if len(args) > 0 {
		configurationPath = args[0]
	} else if os.Getenv("REGISTRY_CONFIGURATION_PATH") != "" {
		configurationPath = os.Getenv("REGISTRY_CONFIGURATION_PATH")
	}

	if configurationPath == "" {
		return nil, fmt.Errorf("configuration path unspecified")
	}

	fp, err := os.Open(configurationPath)
	if err != nil {
		return nil, err
	}

	defer fp.Close()

	config, err := configuration.Parse(fp)
	if err != nil {
		return nil, fmt.Errorf("error parsing %s: %v", configurationPath, err)
	}

	return config, nil
}

func configureDebugServer(config *configuration.Configuration) {
	if config.HTTP.Debug.Addr != "" {
		go func(addr string) {
			logrus.Infof("debug server listening %v", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				logrus.Fatalf("error listening on debug interface: %v", err)
			}
		}(config.HTTP.Debug.Addr)
		configurePrometheus(config)
	}
}

func configurePrometheus(config *configuration.Configuration) {
	if config.HTTP.Debug.Prometheus.Enabled {
		path := config.HTTP.Debug.Prometheus.Path
		if path == "" {
			path = "/metrics"
		}
		logrus.Info("providing prometheus metrics on ", path)
		http.Handle(path, metrics.Handler())
	}
}
