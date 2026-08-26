package logs

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Limits the memory allocated per log line in bytes.
const maxFrameSize = 4 * 1024 * 1024

type ContainerInfo struct {
	Name           string
	ComposeProject string
	ComposeService string
	ComposeWorkDir string
}

type activeStream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func Run(ctx context.Context, dockerClient *client.Client, loki *LokiClient, logger *slog.Logger, hostname string) {
	// Map of working streams
	streamers := map[string]*activeStream{}

	// Function for updating the list of current streams
	refresh := func() {
		containers, err := dockerClient.ContainerList(ctx, container.ListOptions{})
		if err != nil {
			logger.Error("failed to get container list", "error", err)
			return
		}
		running := map[string]*ContainerInfo{}
		for _, c := range containers {
			if c.State != "running" {
				continue
			}
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			running[c.ID] = &ContainerInfo{
				Name:           name,
				ComposeProject: c.Labels["com.docker.compose.project"],
				ComposeService: c.Labels["com.docker.compose.service"],
				ComposeWorkDir: c.Labels["com.docker.compose.project.working_dir"],
			}
		}

		// Remove streamers for stopped containers
		for id, s := range streamers {
			select {
			// If the goroutine is already stopped on the Docker side, remove it from the map
			case <-s.done:
				delete(streamers, id)
			default:
				if _, ok := running[id]; !ok {
					// Stopping the goroutine
					s.cancel()
					// Delete from the map
					delete(streamers, id)
				}
			}
		}

		// Go run streamers for new containers
		for id, info := range running {
			// If container is missing in map
			if _, ok := streamers[id]; !ok {
				streamCtx, cancel := context.WithCancel(ctx)
				stream := &activeStream{cancel: cancel, done: make(chan struct{})}
				streamers[id] = stream
				logger.Info("log collection started", "container", info.Name)
				go func() {
					// Close channel
					defer close(stream.done)
					// Read logs
					streamGetLogs(streamCtx, dockerClient, loki, logger, id, info, hostname)
				}()
			}
		}
	}

	refresh()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		// Stops all streams when finished (e.g. Ctrl+C)
		case <-ctx.Done():
			for _, s := range streamers {
				s.cancel()
			}
			return
		// Updates the container list every 15 seconds
		case <-ticker.C:
			refresh()
		}
	}
}

// Read logs from one container and sends them to Loki
func streamGetLogs(ctx context.Context, dockerClient *client.Client, loki *LokiClient, logger *slog.Logger, id string, info *ContainerInfo, hostname string) {
	labels := map[string]string{
		"containerId":   id,
		"containerName": info.Name,
		"service_name":  info.Name,
		"hostname":      hostname,
	}
	if info.ComposeProject != "" {
		labels["composeProject"] = info.ComposeProject
	}
	if info.ComposeService != "" {
		labels["composeService"] = info.ComposeService
		// Label for Grafana Explorer
		labels["service_name"] = info.ComposeService
	}
	if info.ComposeWorkDir != "" {
		labels["composeWorkDir"] = info.ComposeWorkDir
	}

	stdoutLabels := cloneLabels(labels, "stdout")
	stderrLabels := cloneLabels(labels, "stderr")
	stdoutKey := labelsKey(stdoutLabels)
	stderrKey := labelsKey(stderrLabels)

	lastTimestamp := time.Now()

	for {
		logsOptions := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Timestamps: true,
			Follow:     true, // stream mode
		}

		// When reconnecting to the Docker API, we continue from the last timestamp
		if !lastTimestamp.IsZero() {
			logsOptions.Since = lastTimestamp.UTC().Format(time.RFC3339Nano)
		}

		// Open a socket for receiving logs
		logs, err := dockerClient.ContainerLogs(ctx, id, logsOptions)
		if err != nil {
			logger.Error("failed to open log stream", "container", info.Name, "error", err)
			if !containerRunning(ctx, dockerClient, id) || !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}

		gotData, readErr := streamParseLogs(logs, stdoutKey, stdoutLabels, stderrKey, stderrLabels, loki, &lastTimestamp)
		_ = logs.Close()
		if ctx.Err() == nil {
			logger.Error("log stream has stopped", "container", info.Name, "error", readErr)
		}
		if ctx.Err() != nil || (!gotData && !containerRunning(ctx, dockerClient, id)) {
			return
		}
		if !sleepCtx(ctx, 5*time.Second) {
			return
		}
	}
}

// Parsing the response from the stream
func streamParseLogs(logs io.Reader, stdoutKey string, stdoutLabels map[string]string, stderrKey string, stderrLabels map[string]string, loki *LokiClient, lastTimestamp *time.Time) (bool, error) {
	gotData := false
	// Reading the packet header to determine the stream type
	header := make([]byte, 8)
	// Allocating memory for one line of log text
	buf := make([]byte, 0, 64*1024)

	for {
		// Read headers
		if _, err := io.ReadFull(logs, header); err != nil {
			return gotData, err
		}
		// Get message size
		size := int(binary.BigEndian.Uint32(header[4:8]))
		readLen := size
		// If the message size exceeds 4 MB, we truncate it
		if readLen > maxFrameSize {
			readLen = maxFrameSize
		}
		// The buffer is reused between frames to avoid memory allocation
		if cap(buf) < readLen {
			buf = make([]byte, readLen)
		}
		content := buf[:readLen]
		if _, err := io.ReadFull(logs, content); err != nil {
			return gotData, err
		}
		// If the log line size exceeds 4 MB, discard the tail
		// The next frame should start with a new header
		if size > readLen {
			if _, err := io.CopyN(io.Discard, logs, int64(size-readLen)); err != nil {
				return gotData, err
			}
		}

		// Get stdout (1) by default or stderr (2) from the first byte of the header
		key, labels := stdoutKey, stdoutLabels
		if header[0] == 2 {
			key, labels = stderrKey, stderrLabels
		}

		timestamp := time.Now()
		line := string(content)
		// Extract the embedded timestamp from the message body
		if i := bytes.IndexByte(content, ' '); i > 0 {
			if t, err := time.Parse(time.RFC3339Nano, string(content[:i])); err == nil {
				timestamp = t
				line = string(content[i+1:])
			}
		}

		gotData = true
		*lastTimestamp = timestamp
		loki.Send(key, labels, timestamp.UnixNano(), line)
	}
}

func containerRunning(ctx context.Context, dockerClient *client.Client, id string) bool {
	inspect, err := dockerClient.ContainerInspect(ctx, id)
	return err == nil && inspect.State.Running
}

func cloneLabels(src map[string]string, stream string) map[string]string {
	dst := make(map[string]string, len(src)+1)
	for k, v := range src {
		dst[k] = v
	}
	dst["stream"] = stream
	return dst
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Sorts label keys in a single format.
func labelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(labels[k])
		sb.WriteByte(',')
	}
	return sb.String()
}
