package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

type imageMetric struct {
	image string
	tag   string
	sha   string
	size  int
}

type imageUpdateMetrics struct {
	tag           string
	currentDigest string
	remoteDigest  string
	update        int
}

func (m *Metrics) getImagesMetrics(dockerClient *client.Client) ([]imageMetric, error) {
	var imageMetrics []imageMetric
	imageOptions := image.ListOptions{SharedSize: true}
	images, err := dockerClient.ImageList(context.Background(), imageOptions)
	if err != nil {
		return nil, fmt.Errorf("error getting image list: %v", err)
	}
	for _, image := range images {
		tag := "none"
		if len(image.RepoTags) > 0 {
			tag = image.RepoTags[0]
		}
		repoDigest := image.ID
		if len(image.RepoDigests) > 0 {
			repoDigest = image.RepoDigests[0]
		}
		shaIndex := strings.Index(repoDigest, "sha256:")
		if shaIndex != -1 {
			repoDigest = repoDigest[shaIndex+7:]
		}
		size := int(image.Size)
		sharedSize := int(image.SharedSize)
		if sharedSize > 0 {
			size = size - sharedSize
		}
		data := imageMetric{
			tag:  tag,
			sha:  repoDigest,
			size: size,
		}
		imageMetrics = append(imageMetrics, data)
	}
	return imageMetrics, nil
}

func (m *Metrics) checkImageUpdate(dockerClient *client.Client, image imageMetric) (imageUpdateMetrics, error) {
	imageStatus := imageUpdateMetrics{}
	imageInspect, err := dockerClient.DistributionInspect(
		context.Background(),
		image.tag,
		"",
	)
	if err != nil {
		return imageStatus, err
	}
	remoteDigest := imageInspect.Descriptor.Digest.String()
	shaIndex := strings.Index(remoteDigest, "sha256:")
	if shaIndex != -1 {
		remoteDigest = remoteDigest[shaIndex+7:]
	}
	imageStatus = imageUpdateMetrics{
		tag:           image.tag,
		currentDigest: image.sha,
		remoteDigest:  remoteDigest,
		update:        0,
	}
	if !strings.Contains(image.sha, remoteDigest) {
		imageStatus.update = 1
	}
	return imageStatus, nil
}

func (m *Metrics) getImagesUpdateMetrics(dockerClient *client.Client, logger *slog.Logger) []imageUpdateMetrics {
	if len(m.imageMetrics) > 0 {
		metrics := make([]imageUpdateMetrics, 0, len(m.imageMetrics))
		var wg sync.WaitGroup
		var mu sync.Mutex
		wg.Add(len(m.imageMetrics))
		for _, image := range m.imageMetrics {
			go func(image imageMetric) {
				defer wg.Done()
				status, err := m.checkImageUpdate(dockerClient, image)
				if err != nil {
					logger.Error("failed to inspect image distribution", "image", image.tag, "error", err)
				} else {
					mu.Lock()
					metrics = append(metrics, status)
					mu.Unlock()
				}
			}(image)
		}
		wg.Wait()
		return metrics
	} else {
		return nil
	}
}

func (m *Metrics) ImageMetricsWorker(dockerClient *client.Client, logger *slog.Logger) {
	start := time.Now()
	if len(m.imageMetrics) == 0 {
		var err error
		m.imageMetrics, err = m.getImagesMetrics(dockerClient)
		if err != nil {
			logger.Error("failed to get image metrics", "error", err)
		}
	}
	m.imageUpdateMetrics = m.getImagesUpdateMetrics(dockerClient, logger)
	imageCount := len(m.volumeMetrics)
	logger.Info(
		"collecting image update metrics",
		"source", "background worker",
		"images", imageCount,
		"duration", time.Since(start).Round(time.Millisecond),
	)
}
