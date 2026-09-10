package logs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Limits the memory allocated per log line in bytes.
const maxFrameSize = 4 * 1024 * 1024

var errStop = errors.New("stop frame parsing")

type LogLine struct {
	Stream    string
	Timestamp time.Time
	Line      string
}

type ReadLogsOptions struct {
	Tail     int
	Since    time.Time
	Stream   string
	MaxBytes int64
}

func parseLogFrames(r io.Reader, fn func(stream string, ts time.Time, line string) error) error {
	header := make([]byte, 8)
	buf := make([]byte, 0, 64*1024)

	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return err
		}
		size := int(binary.BigEndian.Uint32(header[4:8]))
		readLen := size
		if readLen > maxFrameSize {
			readLen = maxFrameSize
		}
		if cap(buf) < readLen {
			buf = make([]byte, readLen)
		}
		content := buf[:readLen]
		if _, err := io.ReadFull(r, content); err != nil {
			return err
		}
		if size > readLen {
			if _, err := io.CopyN(io.Discard, r, int64(size-readLen)); err != nil {
				return err
			}
		}

		stream := "stdout"
		if header[0] == 2 {
			stream = "stderr"
		}

		timestamp := time.Now()
		line := string(content)
		if i := bytes.IndexByte(content, ' '); i > 0 {
			if t, err := time.Parse(time.RFC3339Nano, string(content[:i])); err == nil {
				timestamp = t
				line = string(content[i+1:])
			}
		}

		if err := fn(stream, timestamp, line); err != nil {
			return err
		}
	}
}

func ReadContainerLogs(ctx context.Context, dockerClient *client.Client, id string, opts ReadLogsOptions) ([]LogLine, bool, error) {
	if opts.Tail <= 0 {
		opts.Tail = 200
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 4 * 1024 * 1024
	}
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       strconv.Itoa(opts.Tail),
	}
	if !opts.Since.IsZero() {
		options.Since = opts.Since.UTC().Format(time.RFC3339Nano)
	}
	reader, err := dockerClient.ContainerLogs(ctx, id, options)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = reader.Close() }()

	var lines []LogLine
	var size int64
	truncated := false
	err = parseLogFrames(reader, func(stream string, ts time.Time, line string) error {
		if opts.Stream != "all" && opts.Stream != stream {
			return nil
		}
		size += int64(len(line)) + 1
		if size > opts.MaxBytes {
			truncated = true
			return errStop
		}
		lines = append(lines, LogLine{Stream: stream, Timestamp: ts, Line: line})
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, errStop) {
		return nil, truncated, err
	}
	return lines, truncated, nil
}
