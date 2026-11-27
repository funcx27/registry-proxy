package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/distribution/distribution/v3/configuration"

	"registry-proxy/internal/gc"
)

// CleanImage periodically deletes old tags and garbage-collects unreferenced
// blobs. The first pass runs immediately; subsequent passes use opts.Interval.
func CleanImage(ctx context.Context, config *configuration.Configuration, gate *MaintenanceGate, opts CleanupOptions) error {
	if !opts.Enabled {
		return nil
	}
	if gate == nil {
		return fmt.Errorf("maintenance gate is nil")
	}
	if opts.TagRetain < 0 {
		return fmt.Errorf("image tag retain nums must not be negative")
	}
	if opts.Interval <= 0 {
		return fmt.Errorf("image clean interval must be positive")
	}
	if len(opts.Projects) == 0 {
		return fmt.Errorf("image clean projects must not be empty")
	}

	cleanJob := func() error {
		return gate.RunExclusive(ctx, func() error {
			logger.Info("image clean maintenance window started")
			defer logger.Info("image clean maintenance window ended")
			logger.Info("image clean job started")
			var deletedPaths []string
			for _, project := range opts.Projects {
				deletes, err := deleteImageTagPath(config, project, opts.TagRetain, opts.DeleteBefore, false)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					return fmt.Errorf("clean project %q: %w", project, err)
				}
				deletedPaths = append(deletedPaths, deletes...)
			}
			if len(deletedPaths) == 0 {
				logger.Info("no images to clean")
				return nil
			}

			logger.Infof("image tags deleted: %d", len(deletedPaths))
			if err := gc.Run(ctx, *config); err != nil {
				return err
			}
			logger.Infof("image cleaned nums: %d", len(deletedPaths))
			return nil
		})
	}

	if err := cleanJob(); err != nil {
		logger.Errorf("image clean job failed: %v", err)
	}
	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := cleanJob(); err != nil {
				logger.Errorf("image clean job failed: %v", err)
			}
		}
	}
}

func (s *server) manifestDispatcherCustom(r *http.Request, route *registryRoute) http.Handler {
	image, ok := route.sourceImage()
	if !ok {
		return nil
	}

	switch s.options.CopyMode {
	case CopyModeSync:
		if err := s.imageHandler(r, route, image); err != nil {
			logger.Errorf("%s pull image failed: %v", r.RemoteAddr, err)
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
				status := http.StatusBadGateway
				if errors.Is(err, context.DeadlineExceeded) {
					status = http.StatusGatewayTimeout
				}
				http.Error(w, "image pull failed", status)
			})
		}
	case CopyModeAsync:
		// The request is proxied immediately below, so do not retain it after
		// ServeHTTP returns.
		asyncRequest := r.Clone(context.Background())
		go func() {
			if err := s.imageHandler(asyncRequest, route, image); err != nil {
				logger.Errorf("%s async pull image failed: %v", asyncRequest.RemoteAddr, err)
			}
		}()
	}
	return nil
}

func (s *server) imageHandler(r *http.Request, route *registryRoute, image string) error {
	ctx, cancel := context.WithTimeout(r.Context(), s.options.PullTimeout)
	defer cancel()
	localTarget := localTarget(route.localRepository, route.reference)
	if err := s.puller.ensure(ctx, s.registryAddr, image, localTarget); err != nil {
		return fmt.Errorf("ensure image %s: %w", image, err)
	}
	return nil
}

func deleteImageTagPath(config *configuration.Configuration, project string, retain int, deleteBefore time.Duration, dryRun bool) (preDeletePath []string, err error) {
	rootDir, ok := config.Storage.Parameters()["rootdirectory"].(string)
	if !ok || rootDir == "" {
		return nil, fmt.Errorf("filesystem storage rootdirectory is not configured")
	}
	reposPath := path.Join(rootDir, "docker/registry/v2/repositories")
	projectPath := path.Join(reposPath, project)
	type tagTime struct {
		tag  string
		time time.Time
	}
	var paths = map[string][]tagTime{}
	// 使用filepath.Walk遍历目录
	err = filepath.Walk(projectPath, func(fpath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == "current" {
			link := path.Join(fpath, "link")
			// f, _ := os.Stat(link)
			var stat syscall.Stat_t
			err := syscall.Stat(link, &stat)
			if err != nil {
				return nil
			}
			//nolint:unconvert // Atim.Nsec is int32 on some 32-bit platforms.
			accessTime := time.Unix(stat.Atim.Sec, int64(stat.Atim.Nsec))
			tagPath := path.Dir(fpath)
			paths[path.Dir(tagPath)] = append(paths[path.Dir(tagPath)], tagTime{tag: path.Base(tagPath), time: accessTime})
		}
		return nil
	})
	if err != nil {
		return
	}
	for p, v := range paths {
		if len(v) <= retain {
			continue
		}
		sort.Slice(v, func(i, j int) bool {
			return v[i].time.After(v[j].time)
		})
		for _, t := range v[retain:] {
			tagFilePath := path.Join(p, t.tag)
			if t.time.Add(deleteBefore).Before(time.Now()) {
				preDeletePath = append(preDeletePath, tagFilePath)
			}
		}
	}
	for _, tagFilePath := range preDeletePath {
		image := strings.TrimPrefix(tagFilePath, reposPath+"/")
		image = strings.Replace(image, "_manifests/tags/", "", 1)
		logger.Infof("deleting tag: %s", image)
	}
	if dryRun {
		return
	}
	for _, p := range preDeletePath {
		if err := os.RemoveAll(p); err != nil {
			return preDeletePath, fmt.Errorf("remove tag path %s: %w", p, err)
		}
	}
	return
}
