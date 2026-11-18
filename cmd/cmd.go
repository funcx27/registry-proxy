package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"registry-proxy/pkg/proxy"
	"registry-proxy/pkg/version"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry"
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
		ctx := context.WithValue(context.Background(), versionKey{}, version.Version)
		config, err := resolveConfiguration(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
			// nolint:errcheck
			cmd.Usage()
			os.Exit(1)
		}

		// The proxy is the only externally reachable endpoint. Keep the
		// registry on a loopback-only, process-scoped port so multiple
		// instances cannot collide in the same network namespace.
		proxyAddr := config.HTTP.Addr
		registryAddr, err := pickLoopbackAddr()
		if err != nil {
			logrus.Fatalf("allocate internal registry address: %v", err)
		}
		config.HTTP.Net = "tcp"
		config.HTTP.Addr = registryAddr
		logrus.Infof("proxy listening on %s, internal registry on %s", proxyAddr, registryAddr)

		// Distribution's maintenance.readonly setting is read when the app is
		// constructed and cannot be toggled at runtime. Use a handler gate so
		// CleanImage can enter a read-only maintenance window without restarting.
		maintenanceGate := proxy.NewMaintenanceGate()
		registry.RegisterHandler(func(_ *configuration.Configuration, handler http.Handler) http.Handler {
			return maintenanceGate.Wrap(handler)
		})

		reg, err := registry.NewRegistry(ctx, config)
		if err != nil {
			logrus.Fatalln(err)
		}
		configureDebugServer(config)

		proxyErr := make(chan error, 1)
		go func() {
			proxyErr <- proxy.NewProxy(proxyAddr, registryAddr)
		}()

		registryErr := make(chan error, 1)
		go func() {
			registryErr <- reg.ListenAndServe()
		}()

		prePullDone := proxy.PrePullImages(registryAddr)
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-prePullDone:
			}
			if err := proxy.CleanImage(ctx, config, maintenanceGate); err != nil {
				logrus.Errorf("image cleanup stopped: %v", err)
			}
		}()

		select {
		case err := <-proxyErr:
			if err != nil {
				logrus.Fatalf("proxy server failed: %v", err)
			}
		case err := <-registryErr:
			if err != nil {
				logrus.Fatalf("registry server failed: %v", err)
			}
		}
	},
}

func pickLoopbackAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}

	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		return "", err
	}

	return addr, nil
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
