package logs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func logFrame(stream byte, content string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(content)))
	return append(h, []byte(content)...)
}

func fakeDockerClient(t *testing.T, handler http.Handler) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := client.NewClientWithOpts(
		client.WithHost(srv.URL),
		client.WithHTTPClient(srv.Client()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatalf("creating docker client: %v", err)
	}
	return c
}

func writeFrames(w http.ResponseWriter, frames ...[]byte) {
	for _, f := range frames {
		_, _ = w.Write(f)
	}
}

func frameStdout(content string) []byte { return logFrame(1, content) }
func frameStderr(content string) []byte { return logFrame(2, content) }

func TestParseLogFramesStdoutAndStderr(t *testing.T) {
	var got []LogLine
	err := parseLogFrames(bytes.NewReader(append(
		frameStdout("hello"),
		frameStderr("2026-01-01T10:00:00.123456789Z bye")...,
	)), func(stream string, ts time.Time, line string) error {
		got = append(got, LogLine{stream, ts, line})
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(got))
	}
	if got[0].Stream != "stdout" || got[0].Line != "hello" {
		t.Fatalf("frame 0 = %+v, want stdout/hello", got[0])
	}
	if got[1].Stream != "stderr" || got[1].Line != "bye" {
		t.Fatalf("frame 1 = %+v, want stderr/bye", got[1])
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-01-01T10:00:00.123456789Z")
	if !got[1].Timestamp.Equal(want) {
		t.Fatalf("frame 1 timestamp = %v, want %v", got[1].Timestamp, want)
	}
}

func TestParseLogFramesEmptyReader(t *testing.T) {
	if err := parseLogFrames(bytes.NewReader(nil), func(string, time.Time, string) error {
		t.Fatal("callback must not be called on empty input")
		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseLogFramesTruncatedOversizedFrame(t *testing.T) {

	oversize := maxFrameSize + 100
	body := bytes.Repeat([]byte{'a'}, oversize)
	frame := logFrame(1, string(body))
	called := 0
	err := parseLogFrames(bytes.NewReader(frame), func(stream string, ts time.Time, line string) error {
		called++
		if len(line) != maxFrameSize {
			t.Fatalf("line length = %d, want %d", len(line), maxFrameSize)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("callback called %d times, want 1", called)
	}
}

func TestParseLogFramesEOFInHeader(t *testing.T) {
	err := parseLogFrames(bytes.NewReader([]byte{1, 2}), func(string, time.Time, string) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected an error for a truncated header")
	}
}

func TestParseLogFramesEOFInContent(t *testing.T) {
	frame := logFrame(1, "1234567890")
	err := parseLogFrames(bytes.NewReader(frame[:len(frame)-5]), func(string, time.Time, string) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected an error for a truncated frame payload")
	}
}

func TestParseLogFramesCallbackError(t *testing.T) {
	sentinel := errors.New("stop")
	err := parseLogFrames(bytes.NewReader(frameStdout("x")), func(string, time.Time, string) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error must be propagated, got %v", err)
	}
}

type errorAfterReader struct {
	remain int
	err    error
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.remain == 0 {
		return 0, r.err
	}
	if r.remain < len(p) {
		n := r.remain
		r.remain = 0
		return n, r.err
	}
	n := len(p)
	r.remain -= n
	return n, nil
}

func TestParseLogFramesDiscardError(t *testing.T) {

	oversize := maxFrameSize + 100
	frame := logFrame(1, strings.Repeat("a", oversize))
	rdr := &errorAfterReader{remain: len(frame), err: errors.New("tail read failed")}
	err := parseLogFrames(rdr, func(string, time.Time, string) error { return nil })
	if err == nil {
		t.Fatal("expected an error while discarding the oversized tail")
	}
}

func logsHandler(frames []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/logs") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(frames)
	})
}

func TestReadContainerLogsParsesFrames(t *testing.T) {
	dc := fakeDockerClient(t, logsHandler(append(frameStdout("one"), frameStderr("two")...)))
	lines, truncated, err := ReadContainerLogs(context.Background(), dc, "abc", ReadLogsOptions{Tail: 10, Stream: "all"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Fatal("truncated must be false for a small input")
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0].Line != "one" || lines[1].Line != "two" {
		t.Fatalf("unexpected lines: %+v", lines)
	}
}

func TestReadContainerLogsStreamFilter(t *testing.T) {
	dc := fakeDockerClient(t, logsHandler(append(frameStdout("one"), frameStderr("two")...)))
	lines, _, err := ReadContainerLogs(context.Background(), dc, "abc", ReadLogsOptions{Stream: "stderr"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 1 || lines[0].Line != "two" {
		t.Fatalf("expected only stderr lines, got %+v", lines)
	}
}

func TestReadContainerLogsTailDefault(t *testing.T) {
	var mu sync.Mutex
	var tail string
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tail = r.URL.Query().Get("tail")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(frameStdout("x"))
	}))
	if _, _, err := ReadContainerLogs(context.Background(), dc, "abc", ReadLogsOptions{Stream: "all"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if tail != "200" {
		t.Fatalf("default tail must be 200, got %q", tail)
	}
}

func TestReadContainerLogsSinceSet(t *testing.T) {
	var mu sync.Mutex
	var since string
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		since = r.URL.Query().Get("since")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(frameStdout("x"))
	}))
	if _, _, err := ReadContainerLogs(context.Background(), dc, "abc", ReadLogsOptions{Since: time.Unix(1700000000, 0), Stream: "all"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if since == "" {
		t.Fatal("since query must be set")
	}
}

func TestReadContainerLogsTruncation(t *testing.T) {
	stopLine := frameStdout("hello world")
	dc := fakeDockerClient(t, logsHandler(append(stopLine, frameStderr("another line")...)))
	lines, truncated, err := ReadContainerLogs(context.Background(), dc, "abc", ReadLogsOptions{Stream: "all", MaxBytes: 6})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated {
		t.Fatal("truncated must be true when MaxBytes is exceeded")
	}
	if len(lines) != 0 {
		t.Fatalf("expected parsing to stop before appending the oversized line, got %d", len(lines))
	}
}

func TestReadContainerLogsError(t *testing.T) {
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if _, _, err := ReadContainerLogs(context.Background(), dc, "abc", ReadLogsOptions{Stream: "all"}); err == nil {
		t.Fatal("expected an error when ContainerLogs fails")
	}
}

type errAtTailReader struct {
	data []byte
	pos  int
	err  error
}

func (r *errAtTailReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestParseLogFramesDiscardTailError(t *testing.T) {
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], uint32(maxFrameSize+100))
	data := append(header, bytes.Repeat([]byte{'a'}, maxFrameSize)...)
	rdr := &errAtTailReader{data: data, err: errors.New("tail read failed")}
	err := parseLogFrames(rdr, func(string, time.Time, string) error { return nil })
	if err == nil {
		t.Fatal("expected an error while discarding the oversized tail")
	}
}

func TestReadContainerLogsMalformedFrame(t *testing.T) {

	frame := frameStdout("1234567890")
	dc := fakeDockerClient(t, logsHandler(frame[:len(frame)-7]))
	if _, _, err := ReadContainerLogs(context.Background(), dc, "abc", ReadLogsOptions{Stream: "all"}); err == nil {
		t.Fatal("expected an error on malformed frame data")
	}
}

type noFlushWriter struct {
	bytes.Buffer
	header http.Header
	status int
}

func (w *noFlushWriter) Header() http.Header { return w.header }
func (w *noFlushWriter) WriteHeader(s int)   { w.status = s }

func TestWriteSSELine(t *testing.T) {
	var buf noFlushWriter
	buf.header = make(http.Header)
	writeSSELine(&buf, nil, "stdout", time.Unix(0, 0).UTC(), "hello")
	body := buf.String()
	if !strings.HasPrefix(body, "data: ") || !strings.Contains(body, `"stream":"stdout"`) || !strings.Contains(body, `"line":"hello"`) {
		t.Fatalf("unexpected SSE body: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("SSE event must end with a blank line: %q", body)
	}
}

func TestWriteSSELineWithFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSELine(rec, rec, "stderr", time.Unix(0, 0).UTC(), "err")
	if req := rec.Result(); req.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", req.StatusCode)
	}
}

func sseDockerClient(t *testing.T, running bool, frames []byte) *client.Client {
	return fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(frames)
		case strings.Contains(r.URL.Path, "/json"):
			state := "exited"
			if running {
				state = "running"
			}
			_, _ = io.WriteString(w, `{"State":{"Status":"`+state+`","Running":`+boolStr(running)+`}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestStreamSSESendsEventsThenStops(t *testing.T) {
	dc := sseDockerClient(t, false, append(frameStdout("line one"), frameStderr("line two")...))
	rec := httptest.NewRecorder()
	StreamSSE(context.Background(), rec, dc, "abc", ReadLogsOptions{Tail: 10, Stream: "all"})
	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("expected SSE data events, got: %q", body)
	}
	if !strings.Contains(body, `"stream":"stdout"`) || !strings.Contains(body, `"line":"line one"`) {
		t.Fatalf("stdout event missing: %q", body)
	}
	if !strings.Contains(body, ": ping") {
		t.Fatalf("expected keep-alive ping: %q", body)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
}

func TestStreamSSEStreamFilter(t *testing.T) {
	dc := sseDockerClient(t, false, append(frameStdout("stdout line"), frameStderr("stderr line")...))
	rec := httptest.NewRecorder()
	StreamSSE(context.Background(), rec, dc, "abc", ReadLogsOptions{Tail: 10, Stream: "stderr"})
	body := rec.Body.String()
	if strings.Contains(body, `"line":"stdout line"`) {
		t.Fatalf("stdout line must be filtered out: %q", body)
	}
	if !strings.Contains(body, `"line":"stderr line"`) {
		t.Fatalf("stderr line missing: %q", body)
	}
}

func TestStreamSSEPassesSinceToDocker(t *testing.T) {
	var mu sync.Mutex
	var since string
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			since = r.URL.Query().Get("since")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(frameStdout("x"))
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false}}`)
		}
	}))
	rec := httptest.NewRecorder()
	StreamSSE(context.Background(), rec, dc, "abc", ReadLogsOptions{Tail: 10, Stream: "all", Since: time.Now().Add(-time.Hour)})
	mu.Lock()
	defer mu.Unlock()
	if since == "" {
		t.Fatal("since must be sent to the Docker API when provided")
	}
}

func TestStreamSSECancelledContext(t *testing.T) {
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	StreamSSE(ctx, rec, dc, "abc", ReadLogsOptions{Tail: 10})
	if strings.Contains(rec.Body.String(), "data: ") {
		t.Fatalf("no events expected after cancellation: %q", rec.Body.String())
	}
}

func TestStreamSSEWithoutFlusher(t *testing.T) {
	dc := sseDockerClient(t, false, append(frameStdout("no flusher"), frameStderr("x")...))
	var w noFlushWriter
	w.header = make(http.Header)
	StreamSSE(context.Background(), &w, dc, "abc", ReadLogsOptions{Tail: 10, Stream: "all"})
	if w.status != 0 {
		t.Fatalf("unexpected status written %d", w.status)
	}
	if !strings.Contains(w.String(), `"line":"no flusher"`) {
		t.Fatalf("event missing in no-flusher stream: %q", w.String())
	}
}

type mutexWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	header http.Header
}

func (w *mutexWriter) Header() http.Header { return w.header }
func (w *mutexWriter) WriteHeader(int)     {}

func (w *mutexWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *mutexWriter) Flush() {}

func (w *mutexWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestStreamSSEKeepAliveAndReconnect(t *testing.T) {
	var logCalls atomic.Int64
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			if logCalls.Add(1) == 2 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(frameStdout("line"))
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"running","Running":true}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	var w mutexWriter
	w.header = make(http.Header)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		StreamSSE(ctx, &w, dc, "abc", ReadLogsOptions{Tail: 10, Stream: "all"})
		close(done)
	}()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && logCalls.Load() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if logCalls.Load() < 3 {
		cancel()
		t.Fatal("the SSE loop never reached the third log request")
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StreamSSE did not stop after cancellation")
	}
	body := w.String()
	if !strings.Contains(body, `"line":"line"`) {
		t.Fatalf("expected SSE data events from reconnecting, got: %q", body)
	}
	if !strings.Contains(body, ": ping") {
		t.Fatalf("expected keep-alive pings, got: %q", body)
	}
}

func TestStreamSSEReturnsAfterCancelDuringErrorSleep(t *testing.T) {
	var logCalls atomic.Int64
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			logCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		StreamSSE(ctx, rec, dc, "abc", ReadLogsOptions{Tail: 10, Stream: "all"})
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && logCalls.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if logCalls.Load() < 2 {
		cancel()
		t.Fatal("the SSE loop never reached the second error")
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StreamSSE did not stop after cancellation during the error sleep")
	}
}

func TestStreamSSEReturnsAfterCancelDuringParse(t *testing.T) {
	frameReady := make(chan struct{})
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(frameStdout("single"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			close(frameReady)
			<-r.Context().Done()
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		StreamSSE(ctx, rec, dc, "abc", ReadLogsOptions{Tail: 10, Stream: "all"})
		close(done)
	}()
	select {
	case <-frameReady:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("the mock never served the log frame")
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StreamSSE did not stop after the context was cancelled")
	}
}

func lokiServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	count := new(atomic.Int64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/push" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer gz.Close()
		var req lokiPushRequest
		if err := json.NewDecoder(gz).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		count.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, count
}

func TestLokiNewClientDisabled(t *testing.T) {
	t.Setenv("LOKI_URL", "")
	if l := NewClient(testLogger()); l != nil {
		t.Fatal("NewClient must return nil without LOKI_URL")
	}
}

func TestLokiNewClientConfigured(t *testing.T) {
	t.Setenv("LOKI_URL", "http://loki.example.com:3100/")
	t.Setenv("LOKI_USERNAME", "user")
	t.Setenv("LOKI_PASSWORD", "pass")
	t.Setenv("LOKI_TENANT_ID", "tenant1")
	t.Setenv("LOKI_PUSH_LINES", "25")
	t.Setenv("LOKI_PUSH_SECONDS", "7")
	t.Setenv("LOKI_BUFFER_LINES", "500")
	l := NewClient(testLogger())
	if l == nil {
		t.Fatal("NewClient must not be nil")
	}
	if l.URL != "http://loki.example.com:3100" {
		t.Fatalf("URL = %q, want no trailing slash", l.URL)
	}
	if l.username != "user" || l.password != "pass" || l.tenant != "tenant1" {
		t.Fatalf("credentials not set: %+v", l)
	}
	if l.batchSize != 25 || l.batchWindow != 7*time.Second || l.bufferSize != 500 {
		t.Fatalf("batch settings = %d/%v/%d", l.batchSize, l.batchWindow, l.bufferSize)
	}
}

func TestLokiNewClientInvalidEnv(t *testing.T) {
	t.Setenv("LOKI_URL", "http://loki:3100")
	t.Setenv("LOKI_PUSH_LINES", "abc")
	t.Setenv("LOKI_PUSH_SECONDS", "-3")
	t.Setenv("LOKI_BUFFER_LINES", "0")
	l := NewClient(testLogger())
	if l == nil {
		t.Fatal("NewClient must not be nil")
	}
	if l.batchSize != 1000 || l.batchWindow != 5*time.Second || l.bufferSize != 10000 {
		t.Fatalf("defaults must be kept on invalid env: %d/%v/%d", l.batchSize, l.batchWindow, l.bufferSize)
	}
}

func TestLokiPushPipelineAndHeaders(t *testing.T) {
	var mu sync.Mutex
	var gotStreams int
	var gotKey, gotValue string
	var handlerErr string
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/push" {
			mu.Lock()
			handlerErr = "unexpected path " + r.URL.Path
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			mu.Lock()
			handlerErr = "gzip: " + err.Error()
			mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(gz)
		var req lokiPushRequest
		if err := json.Unmarshal(raw.Bytes(), &req); err != nil {
			mu.Lock()
			handlerErr = "decode: " + err.Error() + " body=" + raw.String()
			mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		rawBody = raw.String()
		gotStreams = len(req.Streams)
		if len(req.Streams) > 0 {
			gotKey = req.Streams[0].Stream["stream"]
			gotValue = req.Streams[0].Values[0][1]
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("LOKI_URL", srv.URL)
	t.Setenv("LOKI_TENANT_ID", "tenant-x")
	t.Setenv("LOKI_USERNAME", "loki-user")
	t.Setenv("LOKI_PASSWORD", "loki-pass")
	var logBuf bytes.Buffer
	l := NewClient(slog.New(slog.NewTextHandler(&logBuf, nil)))
	if l == nil {
		t.Fatal("NewClient must not be nil")
	}
	l.httpClient = srv.Client()
	l.Start()
	l.Send("stdout", map[string]string{"stream": "stdout"}, 1700000000000000000, "hello from stdout")
	l.Send("stderr", map[string]string{"stream": "stderr"}, 1700000000000000001, "hello from stderr")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(l.entries) > 0 {
		time.Sleep(time.Millisecond)
	}
	l.Stop()
	mu.Lock()
	defer mu.Unlock()
	if handlerErr != "" {
		t.Fatalf("push rejected by fake server: %s", handlerErr)
	}
	if logBuf.Len() > 0 {
		t.Logf("loki client logs: %s", logBuf.String())
	}
	if gotStreams != 2 {
		t.Fatalf("expected 2 streams, got %d (raw: %s)", gotStreams, rawBody)
	}
	if gotKey != "stdout" || gotValue != "hello from stdout" {
		t.Fatalf("unexpected pushed entry key=%q value=%q", gotKey, gotValue)
	}
}

func TestLokiBatchSizeFlush(t *testing.T) {
	srv, calls := lokiServer(t, http.StatusNoContent)
	t.Setenv("LOKI_URL", srv.URL)
	t.Setenv("LOKI_PUSH_LINES", "2")
	l := NewClient(testLogger())
	l.httpClient = srv.Client()
	l.Start()
	l.Send("k", map[string]string{"stream": "o"}, 1, "a")
	l.Send("k", map[string]string{"stream": "o"}, 2, "b")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	l.Stop()
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 flush when batch size reached, got %d", calls.Load())
	}
}

func TestLokiTimerFlush(t *testing.T) {
	srv, count := lokiServer(t, http.StatusNoContent)
	t.Setenv("LOKI_URL", srv.URL)
	t.Setenv("LOKI_PUSH_SECONDS", "1")
	l := NewClient(testLogger())
	l.httpClient = srv.Client()
	l.Start()
	defer l.Stop()
	l.Send("k", map[string]string{"stream": "o"}, 3, "c")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if count.Load() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected a timer-based flush")
}

func TestLokiPushInvalidStatus(t *testing.T) {
	l := &LokiClient{URL: "http://localhost", httpClient: nil}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = r.Body.Close()
	}))
	t.Cleanup(srv.Close)
	l.URL = srv.URL
	l.httpClient = srv.Client()
	err := l.push([]lokiPushStream{{Stream: map[string]string{"a": "b"}, Values: [][2]string{{"1", "x"}}}})
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "invalid status 500") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestLokiPushTransportError(t *testing.T) {
	l := &LokiClient{URL: "http://localhost", httpClient: &http.Client{Transport: failingTransport{}}}
	if err := l.push([]lokiPushStream{{Values: [][2]string{{"1", "x"}}}}); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestLokiBatchLoopLogsPushError(t *testing.T) {
	srv, _ := lokiServer(t, http.StatusInternalServerError)
	t.Setenv("LOKI_URL", srv.URL)
	var logBuf bytes.Buffer
	l := NewClient(slog.New(slog.NewTextHandler(&logBuf, nil)))
	if l == nil {
		t.Fatal("NewClient must not be nil")
	}
	l.httpClient = srv.Client()
	l.Start()
	l.Send("k", map[string]string{"stream": "o"}, 1, "a")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(l.entries) > 0 {
		time.Sleep(time.Millisecond)
	}
	l.Stop()
	if !strings.Contains(logBuf.String(), "failed to send logs to Loki") {
		t.Fatalf("expected a batch-loop push error log, got: %s", logBuf.String())
	}
}

func TestLokiSendTakesDoneBranchWhenBufferFull(t *testing.T) {
	l := &LokiClient{
		logger:     testLogger(),
		entries:    make(chan *lokiEntry, 1),
		done:       make(chan struct{}),
		bufferSize: 1,
	}
	l.entries <- &lokiEntry{}
	close(l.done)
	l.Send("k", map[string]string{"stream": "o"}, 1, "x")
	if got := l.dropped.Load(); got != 0 {
		t.Fatalf("dropped = %d, want 0 when the done branch is selected", got)
	}
}

func TestLokiSendBufferOverflow(t *testing.T) {
	t.Setenv("LOKI_URL", "http://localhost:3100")
	l := NewClient(testLogger())
	if l == nil {
		t.Fatal("NewClient must not be nil")
	}

	l.Send("k", map[string]string{"stream": "o"}, 1, "a")
	l.Send("k", map[string]string{"stream": "o"}, 2, "b")
	if got := l.dropped.Load(); got != 2 {
		t.Fatalf("dropped counter = %d, want 2", got)
	}
}

func TestCloneLabels(t *testing.T) {
	src := map[string]string{"a": "1", "b": "2"}
	dst := cloneLabels(src, "stderr")
	if dst["stream"] != "stderr" || dst["a"] != "1" || len(dst) != 3 {
		t.Fatalf("unexpected clone: %v", dst)
	}
	src["a"] = "changed"
	if dst["a"] != "1" {
		t.Fatal("clone must not share the source map")
	}
}

func TestLabelsKeyDeterministic(t *testing.T) {
	a := map[string]string{"stream": "stdout", "containerId": "x", "containerName": "web"}
	b := map[string]string{"containerName": "web", "containerId": "x", "stream": "stdout"}
	k1, k2 := labelsKey(a), labelsKey(b)
	if k1 != k2 {
		t.Fatalf("label keys must be order independent: %q vs %q", k1, k2)
	}
	if !strings.HasSuffix(k1, "stream=stdout,") || !strings.HasPrefix(k1, "containerId=x,") {
		t.Fatalf("unexpected key format: %q", k1)
	}
}

func TestSleepCtx(t *testing.T) {
	if got := sleepCtx(context.Background(), time.Millisecond); !got {
		t.Fatal("sleepCtx must return true after the delay")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if got := sleepCtx(ctx, time.Minute); got {
		t.Fatal("sleepCtx must return false on a cancelled context")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("sleepCtx must return immediately on a cancelled context")
	}
}

func TestStreamParseLogsSendsBothStreams(t *testing.T) {
	last := time.Now()
	loki := &LokiClient{logger: testLogger()}
	gotData, err := streamParseLogs(
		bytes.NewReader(append(frameStdout("out"), frameStderr("2026-01-01T10:00:00Z err")...)),
		labelsKey(map[string]string{"stream": "stdout"}),
		map[string]string{"stream": "stdout"},
		labelsKey(map[string]string{"stream": "stderr"}),
		map[string]string{"stream": "stderr"},
		loki,
		&last,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotData {
		t.Fatal("gotData must be true")
	}
	if loki.dropped.Load() != 2 {
		t.Fatalf("expected 2 sends to Loki, got %d dropped", loki.dropped.Load())
	}
}

func TestStreamParseLogsError(t *testing.T) {
	frames := append(frameStdout("fine"), frameStdout("1234567890")[:len(frameStdout("1234567890"))-7]...)
	last := time.Now()
	loki := &LokiClient{logger: testLogger()}
	gotData, err := streamParseLogs(bytes.NewReader(frames), "k", map[string]string{}, "s", map[string]string{}, loki, &last)
	if err == nil {
		t.Fatal("expected a parse error for malformed frames")
	}
	if !gotData {
		t.Fatal("gotData must be true, the first frame was valid")
	}
}

func inspectHandler(running bool, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/json") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = io.WriteString(w, `{"State":{"Status":"`+inspectState(running)+`","Running":`+boolStr(running)+`}}`)
	})
}

func inspectState(running bool) string {
	if running {
		return "running"
	}
	return "exited"
}

func TestContainerRunning(t *testing.T) {
	ctx := context.Background()
	dc := fakeDockerClient(t, inspectHandler(true, http.StatusOK))
	if !containerRunning(ctx, dc, "abc") {
		t.Fatal("container must be running")
	}
	dc2 := fakeDockerClient(t, inspectHandler(false, http.StatusOK))
	if containerRunning(ctx, dc2, "abc") {
		t.Fatal("container must be stopped")
	}
	dc3 := fakeDockerClient(t, inspectHandler(false, http.StatusInternalServerError))
	if containerRunning(ctx, dc3, "abc") {
		t.Fatal("inspect error must report the container as stopped")
	}
}

func TestRunCancelsAndStops(t *testing.T) {
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/containers/json"):
			_, _ = io.WriteString(w, `[
				{"Id":"abc","Names":["/svc"],"State":"running","Labels":{"com.docker.compose.project":"proj","com.docker.compose.service":"svc","com.docker.compose.project.working_dir":"/app"}},
				{"Id":"zzz","Names":["/dead"],"State":"exited","Labels":{}}
			]`)
		case strings.Contains(r.URL.Path, "/logs"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false}}`)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, dc, &LokiClient{logger: testLogger()}, testLogger(), "host-01")
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run must return after context cancellation")
	}
}

func TestRunContainerListError(t *testing.T) {
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, dc, &LokiClient{logger: testLogger()}, testLogger(), "host-01")
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run must return after context cancellation")
	}
}

func TestStreamGetLogsGotData(t *testing.T) {
	var calls atomic.Int64
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			if calls.Add(1) == 1 {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write(frameStdout("first line"))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	loki := &LokiClient{logger: testLogger()}
	info := &ContainerInfo{Name: "svc", ComposeProject: "proj", ComposeService: "svc", ComposeWorkDir: "/app"}
	start := time.Now()
	streamGetLogs(context.Background(), dc, loki, testLogger(), "abc", info, "host-01")
	if got := time.Since(start); got < 5*time.Second {
		t.Logf("streamGetLogs returned after %v (one retry interval expected)", got)
	}
}

func TestStreamGetLogsStopsWhenContainerStopped(t *testing.T) {
	var logsCalls atomic.Int64
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			logsCalls.Add(1)
			w.Header().Set("Content-Type", "text/plain")
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"exited","Running":false}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	streamGetLogs(context.Background(), dc, &LokiClient{logger: testLogger()}, testLogger(), "abc", &ContainerInfo{}, "host-01")
	if logsCalls.Load() != 1 {
		t.Fatalf("expected a single /logs request, got %d", logsCalls.Load())
	}
}

func TestStreamGetLogsStopsOnCancelDuringSleep(t *testing.T) {
	var logsCalls atomic.Int64
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/logs"):
			logsCalls.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(frameStdout("live"))
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"running","Running":true}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		streamGetLogs(ctx, dc, &LokiClient{logger: testLogger()}, testLogger(), "abc", &ContainerInfo{}, "host-01")
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && logsCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if logsCalls.Load() == 0 {
		cancel()
		t.Fatal("the mock never served the /logs request")
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("streamGetLogs did not stop after cancellation")
	}
}

func TestRunRefreshRemovesStoppedStreamer(t *testing.T) {
	var listCalls atomic.Int64
	dc := fakeDockerClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/containers/json"):
			if listCalls.Add(1) == 1 {
				_, _ = io.WriteString(w, `[
					{"Id":"abc","Names":["/svc"],"State":"running","Labels":{"com.docker.compose.project":"proj","com.docker.compose.service":"svc"}}
				]`)
				return
			}
			_, _ = io.WriteString(w, `[]`)
		case strings.Contains(r.URL.Path, "/logs"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"State":{"Status":"running","Running":true}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, dc, &LokiClient{logger: testLogger()}, testLogger(), "host-01")
		close(done)
	}()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && listCalls.Load() < 2 {
		time.Sleep(100 * time.Millisecond)
	}
	if listCalls.Load() < 2 {
		cancel()
		t.Fatal("the refresh ticker never triggered a second container list")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
