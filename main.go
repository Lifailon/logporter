package main

import (
	"context"
	"fmt"
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
		logger.Info(
			"request received",
			"source", r.RemoteAddr,
			"method", r.Method,
			"path", r.URL.Path,
		)
		next.ServeHTTP(w, r)
		containersCount := len(m.Info)
		logger.Info(
			"response sent",
			"source", r.RemoteAddr,
			"cache", m.CacheValid,
			"containers", containersCount,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

// Determining logging level
func logLevelParse(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "err", "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	// Get environment variables
	envLogLevel := os.Getenv("LOG_LEVEL")
	logLevel := logLevelParse(strings.ToLower(envLogLevel))

	// Custom logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	// Initialize the main structure
	exporter := &metrics.Metrics{}

	// Create client with connection parameters from environment variables and approval of the API version with the Docker Daemon
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer func() { _ = dockerClient.Close() }()

	var hostname string
	var port string

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

	// #18 Add custom labels to array (1)
	envLabels := os.Getenv("DOCKER_METRICS_CUSTOM_LABELS")
	if envLabels != "" {
		exporter.CustomLabelsKeys = strings.Split(envLabels, ",")
	}

	exporter.CacheTTL = 15 * time.Second
	envCache := os.Getenv("DOCKER_METRICS_CACHE")
	if envCache != "" {
		parsed, err := strconv.Atoi(envCache)
		if err == nil && parsed > 0 {
			exporter.CacheTTL = time.Duration(parsed) * time.Second
		}
	}

	// Background worker for check image update
	exporter.GetImageUpdateMetrics = true
	getImageUpdateMetrics := os.Getenv("DOCKER_METRICS_IMAGE_UPDATE")
	envImageUpdateMetrics := strings.ToLower(getImageUpdateMetrics)
	if envImageUpdateMetrics == "false" {
		exporter.GetImageUpdateMetrics = false
	}
	if exporter.GetImageUpdateMetrics {
		exporter.ImageInterval = 30 * time.Minute
		envInterval := os.Getenv("DOCKER_METRICS_IMAGE_INTERVAL")
		if envInterval != "" {
			parsed, err := strconv.Atoi(envInterval)
			if err == nil && parsed > 0 {
				exporter.ImageInterval = time.Duration(parsed) * time.Minute
			}
		}
		go func() {
			logger.Info(
				"image update check started",
				"source", "background worker",
				"interval", exporter.ImageInterval,
			)
			exporter.ImageMetricsWorker(dockerClient, logger)
			ticker := time.NewTicker(exporter.ImageInterval)
			defer ticker.Stop()
			for range ticker.C {
				exporter.ImageMetricsWorker(dockerClient, logger)
			}
		}()
	}

	// #12 Background worker for get metrics from volumes
	exporter.GetVolumeMetrics = true
	getVolumeMetrics := os.Getenv("DOCKER_METRICS_VOLUME")
	envVolumeMetrics := strings.ToLower(getVolumeMetrics)
	if envVolumeMetrics == "false" {
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
			logger.Info(
				"volume metrics collection started",
				"source", "background worker",
				"cache", exporter.VolumeCache,
			)
			exporter.VolumesMetricsWorker(dockerClient, logger)
			ticker := time.NewTicker(exporter.VolumeCache)
			defer ticker.Stop()
			for range ticker.C {
				exporter.VolumesMetricsWorker(dockerClient, logger)
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
			_, _ = fmt.Fprintln(w, m)
		}
	})

	logSrv := loggingMiddleware(exporter, httpServerMux, logger)

	// Start HTTP server
	logger.Info("exporter started", "port", port)
	err = http.ListenAndServe(":"+port, logSrv)
	if err != nil {
		logger.Error("failed to start HTTP server", "error", err)
		os.Exit(1)
	}
}
