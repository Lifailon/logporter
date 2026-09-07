package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/client"

	"logporter/internal/dashboard"
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
		containersCount := len(m.Labels)
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
	// Health check on /health endpoint using built-in probe for scratch image
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		port := os.Getenv("DOCKER_METRICS_PORT")
		if port == "" {
			port = "9333"
		}
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://localhost:" + port + "/health")
		if err != nil {
			os.Exit(1)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	exporter.Info = exporter.GetDockerInfo(ctx, dockerClient)
	cancel()
	hostname = exporter.Info.Hostname
	envHostname := os.Getenv("DOCKER_METRICS_HOSTNAME")
	if envHostname != "" {
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

	var lokiCancel context.CancelFunc
	lokiClient := logs.NewClient(logger)
	if lokiClient != nil {
		lokiClient.Start()
		defer lokiClient.Stop()
		var lokiCtx context.Context
		lokiCtx, lokiCancel = context.WithCancel(context.Background())
		go logs.Run(lokiCtx, dockerClient, lokiClient, logger, hostname)
		logger.Info("log collection and sending to Loki is enabled", "url", lokiClient.URL)
	}

	// Create HTTP server
	httpServerMux := http.NewServeMux()

	// The local function returns the current metrics for the exporter and Dashboard
	refreshMetrics := func(ctx context.Context) []string {
		// #10 Using cache
		exporter.CacheMutex.RLock()
		exporter.CacheValid = len(exporter.CacheData) > 0 && time.Since(exporter.CacheTime) < exporter.CacheTTL
		exporter.CacheMutex.RUnlock()

		if exporter.CacheValid {
			exporter.CacheMutex.RLock()
			metricsData := exporter.CacheData
			exporter.CacheMutex.RUnlock()
			return metricsData
		}

		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		metricsData := exporter.GetMetrics(ctx, dockerClient, hostname, logger)
		exporter.CacheMutex.Lock()
		exporter.CacheData = metricsData
		exporter.CacheTime = time.Now()
		exporter.CacheMutex.Unlock()
		return metricsData
	}

	// Endpoint: /metrics
	httpServerMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		// Output metrics in Prometheus format
		for _, m := range refreshMetrics(r.Context()) {
			_, _ = fmt.Fprintln(w, m)
		}
	})

	// Endpoint: /dashboard
	httpServerMux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		refreshMetrics(r.Context())
		html, err := dashboard.Render(exporter.DashboardData())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintln(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintln(w, html)
	})

	// Endpoint: /api/dashboard/refresh
	httpServerMux.HandleFunc("/api/dashboard/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshMetrics(r.Context())
		Refresh, err := dashboard.Refresh(exporter.DashboardData())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintln(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(Refresh)
	})

	// Endpoint: /health
	httpServerMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, err := dockerClient.Ping(ctx)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintln(w, "docker daemon is unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})

	logSrv := loggingMiddleware(exporter, httpServerMux, logger)

	// Start HTTP server
	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: logSrv,
	}
	logger.Info("exporter started", "port", port)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("failed to start HTTP server", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown on signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	if lokiCancel != nil {
		lokiCancel()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	logger.Info("exporter stopped")
}
