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

	"github.com/containerd/errdefs"
	"github.com/docker/docker/client"

	"logporter/internal/auth"
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

// Validation of container ID passed to API for logs
func checkContainerID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func main() {
	// Health check on /health endpoint using built-in probe for scratch image
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		host := os.Getenv("DOCKER_METRICS_HOST")
		if host == "" || host == "0.0.0.0" {
			host = "localhost"
		}
		port := os.Getenv("DOCKER_METRICS_PORT")
		if port == "" {
			port = "9333"
		}
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://" + host + ":" + port + "/health")
		if err != nil {
			os.Exit(1)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}

	// Wait for a termination signal, then run the exporter until it stops
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	os.Exit(run(stop))
}

// Starts the HTTP server and workers for metric processing, and blocks execution until a shutdown signal is received
func run(stop <-chan os.Signal) int {
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
		return 1
	}
	defer func() { _ = dockerClient.Close() }()

	var hostname string
	var port string
	var host string

	host = os.Getenv("DOCKER_METRICS_HOST")

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

	// Basic authorization for the Dashboard
	authEnabled := auth.Setup()
	if authEnabled {
		if t := auth.TTL(); t > 0 {
			logger.Info("dashboard auth enabled", "user", os.Getenv("DASHBOARD_USERNAME"), "ttl", t)
		} else {
			logger.Info("dashboard auth enabled", "user", os.Getenv("DASHBOARD_USERNAME"), "ttl", 0)
		}
	} else {
		logger.Info("dashboard auth disabled (set DASHBOARD_USERNAME and DASHBOARD_PASSWORD to enable)")
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

	// Endpoint: /dashboard
	httpServerMux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		refreshMetrics(r.Context())
		data := exporter.DashboardData()
		data.Auth = auth.Enabled()
		html, err := dashboard.Render(data)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintln(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintln(w, html)
	})

	// Endpoint: / (redirect to dashboard)
	httpServerMux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
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

	// Endpoint: /api/containers/{id}/logs
	httpServerMux.HandleFunc("/api/containers/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !checkContainerID(id) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintln(w, "invalid container id")
			return
		}
		q := r.URL.Query()
		tail := 200
		if v := q.Get("tail"); v != "" {
			parsed, err := strconv.Atoi(v)
			if err == nil && parsed > 0 {
				if parsed > 100000 {
					parsed = 100000
				}
				tail = parsed
			}
		}
		stream := strings.ToLower(q.Get("stream"))
		if stream != "stdout" && stream != "stderr" {
			stream = "all"
		}
		var since time.Time
		if v := q.Get("since"); v != "" {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				since = t
			}
		}

		// SSE (Server-Sent Events)
		if q.Get("follow") == "1" {
			logs.StreamSSE(r.Context(), w, dockerClient, id, logs.ReadLogsOptions{
				Tail:   tail,
				Since:  since,
				Stream: stream,
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		lines, truncated, err := logs.ReadContainerLogs(ctx, dockerClient, id, logs.ReadLogsOptions{
			Tail:   tail,
			Since:  since,
			Stream: stream,
		})
		if err != nil {
			if errdefs.IsNotFound(err) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprintln(w, "container not found")
				return
			}
			logger.Error("failed to read container logs", "container", id, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintln(w, "failed to read container logs")
			return
		}
		type apiLogLine struct {
			Timestamp string `json:"ts"`
			Stream    string `json:"stream"`
			Line      string `json:"line"`
		}
		out := make([]apiLogLine, 0, len(lines))
		for _, l := range lines {
			out = append(out, apiLogLine{
				Timestamp: l.Timestamp.UTC().Format(time.RFC3339Nano),
				Stream:    l.Stream,
				Line:      l.Line,
			})
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			Container string       `json:"container"`
			Logs      []apiLogLine `json:"logs"`
			Truncated bool         `json:"truncated"`
		}{Container: id, Logs: out, Truncated: truncated})
	})

	logSrv := loggingMiddleware(exporter, httpServerMux, logger)

	// Start HTTP server
	httpServer := &http.Server{
		Addr:    host + ":" + port,
		Handler: auth.Middleware(logSrv, logger),
	}
	logger.Info("exporter started", "host", host, "port", port)
	serverErr := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Graceful shutdown on signal or when the HTTP server fails to start
	select {
	case <-stop:
	case err := <-serverErr:
		logger.Error("failed to start HTTP server", "error", err)
		if lokiCancel != nil {
			lokiCancel()
		}
		return 1
	}
	if lokiCancel != nil {
		lokiCancel()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	logger.Info("exporter stopped")
	return 0
}
