package main

import (
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"logporter/internal/metrics"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLogLevelParse(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"err":     slog.LevelError,
		"error":   slog.LevelError,
		"trace":   slog.LevelInfo,
		"":        slog.LevelInfo,
	}
	for in, want := range cases {
		if got := logLevelParse(in); got != want {
			t.Errorf("logLevelParse(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCheckContainerID(t *testing.T) {
	valid := []string{
		"abcd1234",
		"ABCDEF1234567890",
		strings.Repeat("a", 64),
	}
	for _, id := range valid {
		if !checkContainerID(id) {
			t.Errorf("checkContainerID(%q) must be true", id)
		}
	}
	invalid := []string{
		"",
		strings.Repeat("a", 65),
		"abcd z",
		"xyz-",
		"0xdeadbeef!",
	}
	for _, id := range invalid {
		if checkContainerID(id) {
			t.Errorf("checkContainerID(%q) must be false", id)
		}
	}
}

func TestLoggingMiddleware(t *testing.T) {
	m := &metrics.Metrics{
		Labels:     map[string]*metrics.Labels{"abc": {}},
		CacheValid: true,
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	loggingMiddleware(m, next, discardLogger()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("middleware must pass the request through: %d %q", rec.Code, rec.Body.String())
	}
}

func TestHealthcheckChild(t *testing.T) {
	if os.Getenv("LP_HEALTHCHECK_CHILD") != "1" {
		return
	}
	os.Args = []string{"logporter", "--healthcheck"}
	main()

	os.Exit(0)
}

func runHealthcheckChild(t *testing.T) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHealthcheckChild$")
	cmd.Env = append(os.Environ(), "LP_HEALTHCHECK_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 0 {
		t.Logf("child output: %s", out)
	}
	return err
}

func healthServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprintln(w, "ok")
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.Listener = ln
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func healthPort(t *testing.T, s *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split port: %v", err)
	}
	return port
}

func TestHealthcheckSuccess(t *testing.T) {
	s := healthServer(t, http.StatusOK)
	t.Setenv("DOCKER_METRICS_PORT", healthPort(t, s))
	if err := runHealthcheckChild(t); err != nil {
		t.Fatalf("healthcheck must exit 0 on a 200 response: %v", err)
	}
}

func TestHealthcheckFailureStatus(t *testing.T) {
	s := healthServer(t, http.StatusServiceUnavailable)
	t.Setenv("DOCKER_METRICS_PORT", healthPort(t, s))
	if err := runHealthcheckChild(t); err == nil {
		t.Fatal("healthcheck must exit 1 on a non-200 response")
	}
}

func TestHealthcheckConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	t.Setenv("DOCKER_METRICS_PORT", fmt.Sprint(port))
	if err := runHealthcheckChild(t); err == nil {
		t.Fatal("healthcheck must exit 1 when the server is unreachable")
	}
}

func TestHealthcheckDefaultPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:9333")
	if err != nil {
		t.Skip("port 9333 is not available")
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintln(w, "no")
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	if err := runHealthcheckChild(t); err == nil {
		t.Fatal("healthcheck with the default port must exit 1 when the server is unhealthy")
	}
}

func daemonLogFrame(stream byte, content string) []byte {
	frame := make([]byte, 8+len(content))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(content)))
	copy(frame[8:], content)
	return frame
}

func fakeDockerDaemon(t *testing.T) (string, *atomic.Bool) {
	t.Helper()
	pingBroken := &atomic.Bool{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			if pingBroken.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Api-Version", "1.41")
			_, _ = io.WriteString(w, "OK")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1.41")
		switch path {
		case "/info":
			_, _ = io.WriteString(w, `{"Name":"fake-node","IndexServerAddress":"https://index.docker.io/v1/","MemTotal":16777216000,"NCPU":4,"ContainersRunning":1,"ContainersStopped":1,"Images":2}`)
		case "/containers/json":
			_, _ = io.WriteString(w, `[]`)
		case "/images/json":
			_, _ = io.WriteString(w, `[]`)
		case "/system/df":
			_, _ = io.WriteString(w, `{"Containers":[],"Volumes":[],"Images":[]}`)
		case "/containers/abc123/json":
			_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false},"Name":"/svc","Config":{}}`)
		default:
			if strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/logs") {
				id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/logs")
				switch id {
				case "abc123":
					_, _ = w.Write(append(
						daemonLogFrame(1, "2024-01-01T00:00:00.000000000Z hello world\n"),
						daemonLogFrame(2, "2024-01-01T00:00:01.000000000Z stderr line\n")...,
					))
				case "dead":
					w.WriteHeader(http.StatusNotFound)
				case "beef":
					w.WriteHeader(http.StatusInternalServerError)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("docker daemon listen: %v", err)
	}
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("docker daemon port: %v", err)
	}
	return "tcp://127.0.0.1:" + port, pingBroken
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return fmt.Sprint(port)
}

func runStart(t *testing.T) (chan os.Signal, <-chan int) {
	t.Helper()
	stop := make(chan os.Signal, 1)
	done := make(chan int, 1)
	go func() { done <- run(stop) }()
	return stop, done
}

func waitServerReady(t *testing.T, port string) {
	t.Helper()
	base := "http://127.0.0.1:" + port
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server on port %s did not start", port)
}

func TestRunEndpoints(t *testing.T) {
	daemon, pingBroken := fakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon)
	port := freePort(t)
	t.Setenv("DOCKER_METRICS_PORT", port)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("DOCKER_METRICS_HOSTNAME", "test-host")
	t.Setenv("DOCKER_METRICS_CUSTOM_LABELS", "team=ops,env=prod")
	t.Setenv("DOCKER_METRICS_CACHE", "2")
	t.Setenv("DOCKER_METRICS_IMAGE_INTERVAL", "1")
	t.Setenv("DOCKER_METRICS_VOLUME_CACHE", "1")
	stop, done := runStart(t)
	waitServerReady(t, port)
	base := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 10 * time.Second}
	get := func(path string) *http.Response {
		t.Helper()
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	resp := get("/metrics")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "docker_cpu_total_number") {
		t.Fatalf("metrics scrape failed: %d\n%s", resp.StatusCode, string(body))
	}

	resp = get("/metrics")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cached metrics scrape failed: %d", resp.StatusCode)
	}

	resp = get("/health")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health must be ok: %d", resp.StatusCode)
	}

	pingBroken.Store(true)
	resp = get("/health")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "unavailable") {
		t.Fatalf("health must fail when the daemon is down: %d %s", resp.StatusCode, string(body))
	}
	pingBroken.Store(false)

	resp = get("/dashboard")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html") {
		t.Fatalf("dashboard render failed: %d", resp.StatusCode)
	}

	redirectClient := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := redirectClient.Get(base + "/")
	if err != nil {
		t.Fatalf("root request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/dashboard" {
		t.Fatalf("root must redirect to /dashboard, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, err = client.Get(base + "/api/dashboard/refresh")
	if err != nil {
		t.Fatalf("refresh request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard refresh failed: %d", resp.StatusCode)
	}

	resp = get("/api/containers/abc123/logs")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"stream":"stdout"`) {
		t.Fatalf("container logs failed: %d\n%s", resp.StatusCode, string(body))
	}

	resp = get("/api/containers/missing/logs")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-hex id must be rejected: %d", resp.StatusCode)
	}

	resp = get("/api/containers/dead/logs")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing container must be 404: %d", resp.StatusCode)
	}

	resp = get("/api/containers/beef/logs")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("broken daemon must be 500: %d", resp.StatusCode)
	}

	resp, err = client.Get(base + "/api/containers/abc123/logs?tail=10&stream=stderr&since=2024-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("logs with options request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"stream":"stderr"`) {
		t.Fatalf("logs with options failed: %d\n%s", resp.StatusCode, string(body))
	}

	resp, err = client.Get(base + "/api/containers/abc123/logs?tail=0")
	if err != nil {
		t.Fatalf("tail=0 request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tail=0 must fall back to default: %d", resp.StatusCode)
	}

	resp, err = client.Get(base + "/api/containers/abc123/logs?tail=1000001&stream=junk&since=not-a-time")
	if err != nil {
		t.Fatalf("capped tail request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("capped tail and fallback stream failed: %d", resp.StatusCode)
	}

	resp, err = client.Get(base + "/api/containers/abc123/logs?follow=1")
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "data:") {
		t.Fatalf("sse stream failed: %d\n%s", resp.StatusCode, string(body))
	}

	time.Sleep(2100 * time.Millisecond)
	resp = get("/metrics")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "docker_cpu_total_number") {
		t.Fatalf("metrics refresh after cache expiry failed: %d", resp.StatusCode)
	}

	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("run must exit 0 on graceful shutdown, got %d", code)
	}
}

func TestRunAuthTTL(t *testing.T) {
	daemon, _ := fakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon)
	port := freePort(t)
	t.Setenv("DOCKER_METRICS_PORT", port)
	t.Setenv("DASHBOARD_USERNAME", "admin")
	t.Setenv("DASHBOARD_PASSWORD", "admin")
	t.Setenv("DASHBOARD_AUTH_TTL", "3600")
	t.Setenv("DOCKER_METRICS_IMAGE_UPDATE", "false")
	t.Setenv("DOCKER_METRICS_VOLUME", "false")
	t.Setenv("LOG_LEVEL", "error")
	stop, done := runStart(t)
	waitServerReady(t, port)
	resp, err := http.Get("http://127.0.0.1:" + port + "/dashboard")
	if err != nil {
		t.Fatalf("dashboard request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dashboard must be protected without a session: %d", resp.StatusCode)
	}
	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("run must exit 0 on graceful shutdown, got %d", code)
	}
}

func TestRunAuthNoTTL(t *testing.T) {
	daemon, _ := fakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon)
	port := freePort(t)
	t.Setenv("DOCKER_METRICS_PORT", port)
	t.Setenv("DASHBOARD_USERNAME", "admin")
	t.Setenv("DASHBOARD_PASSWORD", "admin")
	t.Setenv("DOCKER_METRICS_IMAGE_UPDATE", "false")
	t.Setenv("DOCKER_METRICS_VOLUME", "false")
	t.Setenv("LOG_LEVEL", "error")
	stop, done := runStart(t)
	waitServerReady(t, port)
	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("run must exit 0 on graceful shutdown, got %d", code)
	}
}

func TestRunLokiEnabled(t *testing.T) {
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer loki.Close()
	daemon, _ := fakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon)
	port := freePort(t)
	t.Setenv("DOCKER_METRICS_PORT", port)
	t.Setenv("LOKI_URL", loki.URL)
	t.Setenv("LOKI_PUSH_LINES", "1")
	t.Setenv("DOCKER_METRICS_IMAGE_UPDATE", "false")
	t.Setenv("DOCKER_METRICS_VOLUME", "false")
	t.Setenv("LOG_LEVEL", "error")
	stop, done := runStart(t)
	waitServerReady(t, port)
	time.Sleep(100 * time.Millisecond)
	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("run with loki must exit 0, got %d", code)
	}
}

func TestRunServerBindError(t *testing.T) {
	daemon, _ := fakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon)
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer loki.Close()
	t.Setenv("LOKI_URL", loki.URL)
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("DOCKER_METRICS_PORT", fmt.Sprint(port))
	t.Setenv("DOCKER_METRICS_IMAGE_UPDATE", "false")
	t.Setenv("DOCKER_METRICS_VOLUME", "false")
	t.Setenv("LOG_LEVEL", "error")
	stop := make(chan os.Signal, 1)
	done := make(chan int, 1)
	go func() { done <- run(stop) }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("run must return 1 on bind error, got %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit on bind error")
	}
}

func TestRunCustomHost(t *testing.T) {
	daemon, _ := fakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon)
	port := freePort(t)
	t.Setenv("DOCKER_METRICS_HOST", "127.0.0.1")
	t.Setenv("DOCKER_METRICS_PORT", port)
	t.Setenv("DOCKER_METRICS_IMAGE_UPDATE", "false")
	t.Setenv("DOCKER_METRICS_VOLUME", "false")
	t.Setenv("LOG_LEVEL", "error")
	stop, done := runStart(t)
	waitServerReady(t, port)
	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("run with custom host must exit 0, got %d", code)
	}
}

func TestRunDockerClientError(t *testing.T) {
	t.Setenv("DOCKER_HOST", "not-a-host")
	t.Setenv("LOG_LEVEL", "error")
	stop := make(chan os.Signal, 1)
	done := make(chan int, 1)
	go func() { done <- run(stop) }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("run must return 1 on client error, got %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit on client error")
	}
}

func TestGzipMiddleware(t *testing.T) {
	payload := strings.Repeat("docker log line with repetitive content\n", 500)
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, payload)
	}))

	t.Run("compresses when accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Error("Vary must advertise Accept-Encoding")
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("read gzip body: %v", err)
		}
		if string(got) != payload {
			t.Error("decompressed body does not match the original payload")
		}
		if rec.Body.Len() >= len(payload) {
			t.Errorf("compressed body is %d bytes, expected smaller than %d", rec.Body.Len(), len(payload))
		}
	})

	t.Run("stays plain without accept-encoding", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty", got)
		}
		if rec.Body.String() != payload {
			t.Error("body must be sent uncompressed")
		}
	})

	t.Run("never compresses the follow stream", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/containers/abc/logs?follow=1", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("SSE stream must not be compressed, got %q", got)
		}
		if rec.Body.String() != payload {
			t.Error("SSE body must be sent uncompressed and complete")
		}
	})
}

func TestMainRunChild(t *testing.T) {
	if os.Getenv("LP_MAIN_RUN_CHILD") != "1" {
		return
	}
	os.Args = []string{"logporter"}
	main()
	os.Exit(0)
}

func TestMainShutdownExitCode(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainRunChild$")
	cmd.Env = append(os.Environ(), "LP_MAIN_RUN_CHILD=1", "DOCKER_HOST=not-a-host")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 0 {
		t.Logf("child output: %s", out)
	}
	if err == nil {
		t.Fatal("main must exit non-zero when the docker client cannot be created")
	}
}
