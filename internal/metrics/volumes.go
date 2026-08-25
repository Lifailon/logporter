package metrics

import (
	"context"
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

func (m *Metrics) getVolumesMetrics(dockerClient *client.Client) ([]volumeMetric, error) {
	diskOptions := types.DiskUsageOptions{}
	diskUsage, err := dockerClient.DiskUsage(context.Background(), diskOptions)
	if err != nil {
		return nil, err
	}
	volumeMetrics := make([]volumeMetric, 0, len(diskUsage.Volumes))
	for _, volume := range diskUsage.Volumes {
		var size int64
		var usage int64
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
		logger.Error("error getting volume list", "error", err)
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
