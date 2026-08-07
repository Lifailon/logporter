package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/client"

	"logporter/internal/logs"
	"logporter/internal/metrics"
)

// Logging http server requests
func loggingMiddleware(m *metrics.Metrics, next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logger.Info("request received",
			"source", r.RemoteAddr,
			"method", r.Method,
			"path", r.URL.Path,
		)
		next.ServeHTTP(w, r)
		containersCount := len(m.ID)
		logger.Info("response sent",
			"source", r.RemoteAddr,
			"cache", m.CacheValid,
			"containers", containersCount,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

func main() {
	// Initialize the main structure
	var exporter *metrics.Metrics = &metrics.Metrics{}
	var err error

	// Create client with connection parameters from environment variables and approval of the API version with the Docker Daemon
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	defer dockerClient.Close()

	var hostname string
	var port string

	// Get environment variables
	port = "9333"
	envPort := os.Getenv("DOCKER_METRICS_PORT")
	if envPort != "" {
		parsed, err := strconv.Atoi(envPort)
		if err == nil && parsed > 0 && parsed < 65536 {
			port = envPort
		}
	}

	envHostname := os.Getenv("DOCKER_METRICS_HOSTNAME")
	if envHostname == "" {
		hostname = exporter.GetHostname(dockerClient)
	} else {
		hostname = envHostname
	}

	exporter.CacheTTL = 15 * time.Second
	envCache := os.Getenv("DOCKER_METRICS_CACHE")
	if envCache != "" {
		parsed, err := strconv.Atoi(envCache)
		if err == nil && parsed > 0 {
			exporter.CacheTTL = time.Duration(parsed) * time.Second
		}
	}

	// Custom logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// #12 Background worker for get metrics from volumes
	exporter.GetVolumeMetrics = true
	getVolumeMetrics := os.Getenv("DOCKER_METRICS_VOLUME")
	if strings.ToLower(getVolumeMetrics) == "false" {
		exporter.GetVolumeMetrics = false
	}
	if exporter.GetVolumeMetrics {
		exporter.VolumeCache = 30 * time.Minute
		envCache := os.Getenv("DOCKER_METRICS_VOLUME_CACHE")
		if envCache != "" {
			parsed, err := strconv.Atoi(envCache)
			if err == nil && parsed > 0 {
				exporter.VolumeCache = time.Duration(parsed) * time.Minute
			}
		}
		go func() {
			exporter.UpdateVolumesMetrics(dockerClient, logger)
			ticker := time.NewTicker(exporter.VolumeCache)
			defer ticker.Stop()
			for range ticker.C {
				exporter.UpdateVolumesMetrics(dockerClient, logger)
			}
		}()
	}

	lokiClient := logs.NewClient(logger)
	if lokiClient != nil {
		lokiClient.Start()
		defer lokiClient.Stop()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go logs.Run(ctx, dockerClient, lokiClient, logger, hostname)
		logger.Info("log collection and sending to Loki is enabled", "url", lokiClient.URL)
	}

	// Create HTTP server
	httpServerMux := http.NewServeMux()

	// Endpoint: /metrics
	httpServerMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		// #10 Using cache
		exporter.CacheMutex.RLock()
		exporter.CacheValid = len(exporter.CacheData) > 0 && time.Since(exporter.CacheTime) < exporter.CacheTTL
		exporter.CacheMutex.RUnlock()

		var metricsData []string
		if exporter.CacheValid {
			exporter.CacheMutex.RLock()
			metricsData = exporter.CacheData
			exporter.CacheMutex.RUnlock()
		} else {
			metricsData = exporter.GetMetrics(dockerClient, hostname, logger)
			exporter.CacheMutex.Lock()
			exporter.CacheData = metricsData
			exporter.CacheTime = time.Now()
			exporter.CacheMutex.Unlock()
		}

		// Output metrics in Prometheus format
		for _, m := range metricsData {
			fmt.Fprintln(w, m)
		}
	})

	logSrv := loggingMiddleware(exporter, httpServerMux, logger)

	// Start HTTP server
	logger.Info("exporter started", "port", port)
	err = http.ListenAndServe(":"+port, logSrv)
	if err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}
}
