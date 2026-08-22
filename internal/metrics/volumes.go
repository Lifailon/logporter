package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

type volumeMetric struct {
	name   string
	driver string
	size   int64
	usage  int64
}

// Get list of volumes and their sizes
func (m *Metrics) getVolumesMetrics(dockerClient *client.Client) ([]volumeMetric, error) {
	diskOptions := types.DiskUsageOptions{}
	diskUsage, err := dockerClient.DiskUsage(context.Background(), diskOptions)
	if err != nil {
		return nil, fmt.Errorf("error getting volume list: %v", err)
	}
	// Allocating memory for a slice
	volumeMetrics := make([]volumeMetric, 0, len(diskUsage.Volumes))
	for _, volume := range diskUsage.Volumes {
		var size int64
		var usage int64
		// Protection from nil
		if volume.UsageData != nil {
			size = volume.UsageData.Size
			usage = volume.UsageData.RefCount
		}
		volumeMetrics = append(volumeMetrics, volumeMetric{
			name:   volume.Name,
			driver: volume.Driver,
			size:   size,
			usage:  usage,
		})
	}
	return volumeMetrics, nil
}

func (m *Metrics) VolumesMetricsWorker(dockerClient *client.Client, logger *slog.Logger) {
	start := time.Now()
	volumeMetrics, err := m.getVolumesMetrics(dockerClient)
	if err != nil {
		logger.Error("failed to get volume list", "error", err)
	}
	m.volumeMetrics = volumeMetrics
	volumeCount := len(m.volumeMetrics)
	logger.Info(
		"collecting volume metrics",
		"source", "background worker",
		"volumes", volumeCount,
		"duration", time.Since(start).Round(time.Millisecond),
	)
}
