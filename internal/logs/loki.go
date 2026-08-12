package logs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type lokiEntry struct {
	key       string
	labels    map[string]string
	timestamp int64
	line      string
}

type lokiPushRequest struct {
	Streams []lokiPushStream `json:"streams"`
}

type lokiPushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type LokiClient struct {
	URL         string
	username    string
	password    string
	tenant      string
	httpClient  *http.Client
	logger      *slog.Logger
	batchSize   int
	batchWindow time.Duration
	bufferSize  int
	entries     chan *lokiEntry
	done        chan struct{}
	dropped     atomic.Uint64
	wg          sync.WaitGroup
}

func NewClient(logger *slog.Logger) *LokiClient {
	if os.Getenv("LOKI_URL") == "" {
		return nil
	}
	l := &LokiClient{
		URL:         strings.TrimSuffix(os.Getenv("LOKI_URL"), "/"),
		username:    os.Getenv("LOKI_USERNAME"),
		password:    os.Getenv("LOKI_PASSWORD"),
		tenant:      os.Getenv("LOKI_TENANT_ID"),
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		logger:      logger,
		batchSize:   1000,
		batchWindow: 5 * time.Second,
		bufferSize:  10000,
	}
	if v, err := strconv.Atoi(os.Getenv("LOKI_PUSH_LINES")); err == nil && v > 0 {
		l.batchSize = v
	}
	if v, err := strconv.Atoi(os.Getenv("LOKI_PUSH_SECONDS")); err == nil && v > 0 {
		l.batchWindow = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(os.Getenv("LOKI_BUFFER_LINES")); err == nil && v > 0 {
		l.bufferSize = v
	}
	return l
}

// Start background dispatch thread
func (l *LokiClient) Start() {
	l.entries = make(chan *lokiEntry, l.bufferSize)
	l.done = make(chan struct{})
	l.wg.Add(1)
	go l.batchLoop()
}

// Send the remaining records and stop background thread
func (l *LokiClient) Stop() {
	close(l.done)
	l.wg.Wait()
}

// Adds an entry to the sending queue
// When a buffer overflow occurs, records are discarded to protect memory from OOM Killer
func (l *LokiClient) Send(key string, labels map[string]string, timestamp int64, line string) {
	select {
	case l.entries <- &lokiEntry{key, labels, timestamp, line}:
	case <-l.done:
	default:
		if l.dropped.Add(1)%1000 == 1 {
			l.logger.Warn("loki send buffer is full, records are being discarded",
				"dropped", l.dropped.Load(),
			)
		}
	}
}

// batchLoop - only stream for sending data to Loki.
func (l *LokiClient) batchLoop() {
	defer l.wg.Done()
	ticker := time.NewTicker(l.batchWindow)
	defer ticker.Stop()

	// Accumulated streams by label key
	streams := map[string]*lokiPushStream{}
	// The order of appearance of streams
	var order []string
	// Total number of lines in a batch
	size := 0

	flush := func() {
		if size == 0 {
			return
		}
		payload := make([]lokiPushStream, 0, len(order))
		for _, key := range order {
			payload = append(payload, *streams[key])
		}
		if err := l.push(payload); err != nil {
			l.logger.Error("failed to send logs to Loki", "entries", size, "error", err)
		} else {
			l.logger.Debug("logs successfully sent to Loki", "entries", size)
		}
		streams = map[string]*lokiPushStream{}
		order = nil
		size = 0
	}

	for {
		select {
		case e := <-l.entries:
			stream, ok := streams[e.key]
			if !ok {
				stream = &lokiPushStream{Stream: e.labels}
				streams[e.key] = stream
				order = append(order, e.key)
			}
			stream.Values = append(stream.Values, [2]string{strconv.FormatInt(e.timestamp, 10), e.line})
			// Increase the accumulated row counter
			size++
			// Checking the number of lines
			if size >= l.batchSize {
				flush()
			}
		// Checking the timer
		case <-ticker.C:
			flush()
		// We send the remainder upon stopping
		case <-l.done:
			flush()
			return
		}
	}
}

// Sends a batch of records to Loki
func (l *LokiClient) push(streams []lokiPushStream) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Gzip compression
	var body bytes.Buffer
	gzipWriter := gzip.NewWriter(&body)
	err := json.NewEncoder(gzipWriter).Encode(lokiPushRequest{Streams: streams})
	if err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL+"/loki/api/v1/push", &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	if l.tenant != "" {
		request.Header.Set("X-Scope-OrgID", l.tenant)
	}
	if l.username != "" {
		request.SetBasicAuth(l.username, l.password)
	}

	response, err := l.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("invalid status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
