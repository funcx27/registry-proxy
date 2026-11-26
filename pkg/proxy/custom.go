package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/distribution/distribution/v3/registry/storage/driver/factory"
	"github.com/patrickmn/go-cache"
	"gopkg.in/yaml.v3"
)

var (
	imagePullMinIntervalKey    = "IMAGE_REPULL_MIN_INTERVAL"
	imageCopyModekey           = "IMAGE_COPY_MODE"
	imagePullTimeoutKey        = "IMAGE_PULL_TIMEOUT"
	imagePullWorkersKey        = "IMAGE_PULL_WORKERS"
	imagePullQueueSizeKey      = "IMAGE_PULL_QUEUE_SIZE"
	imageCleanJobIntervalKey   = "IMAGE_CLEAN_INTERVAL"
	imageCleanBeforeDaysKey    = "IMAGE_CLEAN_BEFORE_DAYS"
	imageCleanTagRetainNumsKey = "IMAGE_CLEAN_TAG_RETAIN_NUMS"
	imageCleanProjectKey       = "IMAGE_CLEAN_PROJECT"
	registryMirrorKey          = "DOCKERHUB_MIRROR"
	imageCache                 *cache.Cache
	imagePuller                *pullPool
	imagePullTimeout           time.Duration
	projects                   []string
	imageCleanBefore           time.Duration
	registryProxyMap           = map[string]string{}
)

func init() {
	rePullInterval := envStrToDuration(imagePullMinIntervalKey)
	imagePullTimeout = envStrToDuration(imagePullTimeoutKey)
	imageCache = cache.New(rePullInterval, time.Hour)
	pullWorkers := envInt(imagePullWorkersKey, 2)
	if pullWorkers < 1 {
		pullWorkers = 1
	}
	pullQueueSize := envInt(imagePullQueueSizeKey, 0)
	if pullQueueSize < 0 {
		pullQueueSize = 0
	}
	copier, err := newImageCopier()
	if err != nil {
		log.Fatalf("initialize image copier: %v", err)
	}
	// Request waits may time out after a job is queued. Bound the copy itself
	// as well, otherwise a stuck upstream can occupy a worker indefinitely.
	copyWithTimeout := func(ctx context.Context, registryAddr, image string) error {
		ctx, cancel := context.WithTimeout(ctx, imagePullTimeout)
		defer cancel()
		return copier.Copy(ctx, registryAddr, image)
	}
	imagePuller = newPullPool(context.Background(), pullWorkers, pullQueueSize, imageCache, copyWithTimeout)
	trimmedProjects := strings.TrimFunc(getEnv(imageCleanProjectKey), unicode.IsSpace)
	projects = strings.Split(trimmedProjects, ",")
	log.Printf("image re-pull min interval: %s\n", rePullInterval)
	log.Printf("image pull timeout: %s\n", imagePullTimeout)
	log.Printf("image pull workers: %d, queue size: %d\n", pullWorkers, pullQueueSize)
	log.Printf("image copy mode: %s\n", getEnv(imageCopyModekey))
	log.Printf("dockerhub mirror: %s\n", getEnv(registryMirrorKey))
	const proxyMappingPath = "/var/lib/registry/proxy-mapping.yaml"
	if b, err := os.ReadFile(proxyMappingPath); err == nil {
		if err := yaml.Unmarshal(b, &registryProxyMap); err != nil {
			log.Printf("parse %s: %v", proxyMappingPath, err)
		}
	}
	if len(registryProxyMap) > 0 {
		s := "registry proxy host mapping: "
		for k, v := range registryProxyMap {
			s += fmt.Sprintf("%s -> %s\n", k, v)
		}
		log.Println(s)
	}
	if getEnv(imageCleanTagRetainNumsKey) != "" {
		days, err := strconv.ParseInt(getEnv(imageCleanBeforeDaysKey), 10, 64)
		if err != nil {
			log.Fatal(err)
		}
		imageCleanBefore = 24 * time.Hour * time.Duration(days)
		log.Printf("image clean interval: %s\n", getEnv(imageCleanJobIntervalKey))
		log.Printf("image clean project: %s\n", projects)
		log.Printf("image clean before: %s\n", imageCleanBefore)
		log.Printf("image clean retain tag nums: %s\n", getEnv(imageCleanTagRetainNumsKey))
	}
}

func CleanImage(ctx context.Context, config *configuration.Configuration, gate *MaintenanceGate) error {
	imageTagRetainStr := getEnv(imageCleanTagRetainNumsKey)
	if imageTagRetainStr == "" {
		return nil
	}
	if gate == nil {
		return fmt.Errorf("maintenance gate is nil")
	}
	imageTagRetainNums, err := strconv.Atoi(imageTagRetainStr)
	if err != nil {
		return err
	}
	if imageTagRetainNums < 0 {
		return fmt.Errorf("image tag retain nums must not be negative")
	}
	interval, err := time.ParseDuration(getEnv(imageCleanJobIntervalKey))
	if err != nil {
		return fmt.Errorf("parse image clean interval: %w", err)
	}
	if interval <= 0 {
		return fmt.Errorf("image clean interval must be positive")
	}

	cleanJob := func() error {
		return gate.RunExclusive(ctx, func() error {
			log.Println("image clean maintenance window started")
			defer log.Println("image clean maintenance window ended")
			log.Println("image clean job started")
			var deletedPaths []string
			for _, project := range projects {
				deletes, err := deleteImageTagPath(config, project, imageTagRetainNums, imageCleanBefore, false)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					return fmt.Errorf("clean project %q: %w", project, err)
				}
				deletedPaths = append(deletedPaths, deletes...)
			}
			if len(deletedPaths) == 0 {
				log.Println("no images to clean")
				return nil
			}

			log.Printf("image tags deleted: %d", len(deletedPaths))
			if err := garbageCollect(ctx, *config); err != nil {
				return err
			}
			log.Printf("image cleaned nums: %d", len(deletedPaths))
			return nil
		})
	}

	if err := cleanJob(); err != nil {
		log.Printf("image clean job failed: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := cleanJob(); err != nil {
				log.Printf("image clean job failed: %v", err)
			}
		}
	}
}

func manifestDispatcherCustom(registryAddr string, r *http.Request) http.Handler {
	switch getEnv(imageCopyModekey) {
	case "sync":
		if err := imageHandler(r, registryAddr); err != nil {
			log.Printf("%s pull image failed: %v", r.RemoteAddr, err)
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
				status := http.StatusBadGateway
				if errors.Is(err, context.DeadlineExceeded) {
					status = http.StatusGatewayTimeout
				}
				http.Error(w, "image pull failed", status)
			})
		}
	case "async":
		// The request is proxied immediately below, so do not retain it after
		// ServeHTTP returns.
		asyncRequest := r.Clone(context.Background())
		go func() {
			if err := imageHandler(asyncRequest, registryAddr); err != nil {
				log.Printf("%s async pull image failed: %v", asyncRequest.RemoteAddr, err)
			}
		}()
	}
	return nil
}

func imageHandler(r *http.Request, serverAddr string) error {
	image, ok := manifestImage(r)
	if !ok {
		return nil
	}

	ctx, cancel := context.WithTimeout(r.Context(), imagePullTimeout)
	defer cancel()
	if err := imagePuller.ensure(ctx, serverAddr, image); err != nil {
		return fmt.Errorf("ensure image %s: %w", image, err)
	}
	return nil
}

// manifestImage converts a manifest request into the upstream image reference.
// A tag request pulls only the top-level manifest; a digest request pulls that
// exact child manifest and its blobs.
func manifestImage(r *http.Request) (string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return "", false
	}

	imagePath := strings.TrimPrefix(r.URL.Path, "/v2")
	lastIdx := strings.LastIndex(imagePath, "/manifests/")
	if lastIdx == -1 {
		return "", false
	}

	repository := strings.TrimPrefix(imagePath[:lastIdx], "/")
	reference := imagePath[lastIdx+len("/manifests/"):]
	if repository == "" || reference == "" {
		return "", false
	}

	host := proxyHostMapping(r.Host)
	mirror := getEnv(registryMirrorKey)
	if isIPHost(host) && (mirror == "" || !strings.HasPrefix(imagePath, "/library/")) {
		return "", false
	}
	if mirror != "" && strings.HasPrefix(repository, "library/") {
		host = mirror
	}

	separator := ":"
	if strings.Contains(reference, ":") {
		separator = "@"
	}
	return host + "/" + repository + separator + reference, true
}

func isIPHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return net.ParseIP(strings.Trim(host, "[]")) != nil
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
		log.Printf("deleting tag: %s \n", image)
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

func getEnv(key string) string {
	var v = map[string]string{
		imagePullMinIntervalKey:  "5m",
		imageCopyModekey:         "sync",
		imagePullTimeoutKey:      "5m",
		imagePullWorkersKey:      "2",
		imagePullQueueSizeKey:    "0",
		imageCleanJobIntervalKey: "1m",
		imageCleanBeforeDaysKey:  "1",
	}
	if os.Getenv(key) == "" {
		return v[key]
	}
	return os.Getenv(key)
}

func envInt(envKey string, fallback int) int {
	value, err := strconv.Atoi(getEnv(envKey))
	if err != nil {
		log.Printf("%s value error: %v, using %d", envKey, err, fallback)
		return fallback
	}
	return value
}

func envStrToDuration(envKey string) time.Duration {
	duration, err := time.ParseDuration(getEnv(envKey))
	if err != nil {
		log.Println(envKey + "value error")
		panic(err)
	}
	return duration
}

func proxyHostMapping(host string) string {
	if registryProxyMap[host] != "" {
		return registryProxyMap[host]
	}
	return host
}

func garbageCollect(ctx context.Context, config configuration.Configuration) error {
	driver, err := factory.Create(ctx, config.Storage.Type(), config.Storage.Parameters())
	if err != nil {
		return fmt.Errorf("construct %s driver: %w", config.Storage.Type(), err)
	}

	registry, err := storage.NewRegistry(ctx, driver)
	if err != nil {
		return fmt.Errorf("construct registry: %w", err)
	}
	err = storage.MarkAndSweep(ctx, driver, registry, storage.GCOpts{
		DryRun:         false,
		RemoveUntagged: true,
	})
	if err != nil {
		return fmt.Errorf("garbage collect: %w", err)
	}
	return nil
}
