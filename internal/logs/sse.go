package logs

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type SSELine struct {
	Timestamp string `json:"ts"`
	Stream    string `json:"stream"`
	Line      string `json:"line"`
}

// Serves a live log stream for one container as Server-Sent Events
// https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events
func StreamSSE(ctx context.Context, w http.ResponseWriter, dockerClient *client.Client, id string, opts ReadLogsOptions) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, _ := w.(http.Flusher)

	// If the 'since' parameter is missing, start only from the current moment so the stream does not duplicate history
	lastTimestamp := opts.Since
	if lastTimestamp.IsZero() {
		lastTimestamp = time.Now()
	}

	// Keep the connection alive during silent periods (proxies drop idle connections after 30-60s)
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopPing:
				return
			case <-ticker.C:
				_, _ = w.Write([]byte(": ping\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}()

	for {
		options := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Timestamps: true,
			Follow:     true,
			Tail:       strconv.Itoa(opts.Tail),
		}
		if !lastTimestamp.IsZero() {
			options.Since = lastTimestamp.UTC().Format(time.RFC3339Nano)
		}

		stream, err := dockerClient.ContainerLogs(ctx, id, options)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !sleepCtx(ctx, 3*time.Second) {
				return
			}
			continue
		}

		lastLine := lastTimestamp
		_ = parseLogFrames(stream, func(streamName string, ts time.Time, line string) error {
			if opts.Stream != "all" && opts.Stream != streamName {
				return nil
			}
			lastLine = ts
			writeSSELine(w, flusher, streamName, ts, line)
			return nil
		})
		_ = stream.Close()

		if ctx.Err() != nil {
			return
		}

		if !lastLine.IsZero() {
			lastTimestamp = lastLine
		}

		_, _ = w.Write([]byte(": ping\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		if !containerRunning(ctx, dockerClient, id) {
			return
		}

		if !sleepCtx(ctx, 2*time.Second) {
			return
		}
	}
}

func writeSSELine(w http.ResponseWriter, flusher http.Flusher, streamName string, ts time.Time, line string) {
	b, err := json.Marshal(SSELine{
		Timestamp: ts.UTC().Format(time.RFC3339Nano),
		Stream:    streamName,
		Line:      line,
	})
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}
