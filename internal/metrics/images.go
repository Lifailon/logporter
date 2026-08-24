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

	"logporter/internal/updates"
)

type imageMetric struct {
	imageFullName string
	imageName     string
	tag           string
	registry      string
	digest        string
	size          int
}

type imageUpdateMetrics struct {
	imageFullName string
	imageName     string
	tag           string
	registry      string
	currentDigest string
	remoteDigest  string
	updateStatus  int
}

func (m *Metrics) getImagesMetrics(dockerClient *client.Client) ([]imageMetric, error) {
	var imageMetrics []imageMetric
	imageOptions := image.ListOptions{SharedSize: true}
	images, err := dockerClient.ImageList(context.Background(), imageOptions)
	if err != nil {
		return nil, fmt.Errorf("error getting image list: %v", err)
	}
	for _, image := range images {
		imageFullName := "none"
		if len(image.RepoTags) > 0 {
			imageFullName = image.RepoTags[0]
		}
		imageName := imageFullName
		imageTag := "none"
		splitTag := strings.Split(imageFullName, ":")
		if len(splitTag) > 1 {
			imageName = strings.Join(splitTag[:len(splitTag)-1], ":")
			imageTag = splitTag[len(splitTag)-1]
		}
		registry := "docker.io"
		lastSlash := strings.LastIndex(imageName, "/")
		if lastSlash != -1 {
			secondLastSlash := strings.LastIndex(imageName[:lastSlash], "/")
			if secondLastSlash != -1 {
				registry = imageName[:secondLastSlash]
				imageName = imageName[secondLastSlash+1:]
			} else {
				checkRegistry := imageName[:lastSlash]
				if strings.Contains(checkRegistry, ".") || strings.Contains(checkRegistry, ":") {
					registry = checkRegistry
					imageName = imageName[lastSlash+1:]
				}
			}
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
			imageFullName: imageFullName,
			imageName:     imageName,
			tag:           imageTag,
			registry:      registry,
			digest:        repoDigest,
			size:          size,
		}
		imageMetrics = append(imageMetrics, data)
	}
	return imageMetrics, nil
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
				if image.imageFullName != "none" {
					updateStatus, remoteDigest, err := updates.CheckImageUpdateDigest(dockerClient, image.imageFullName, image.digest)
					if err != nil {
						logger.Error("failed to inspect image distribution", "image", image.tag, "error", err)
					} else {
						mu.Lock()
						updateMetrics := imageUpdateMetrics{
							imageFullName: image.imageFullName,
							imageName:     image.imageName,
							tag:           image.tag,
							registry:      image.registry,
							currentDigest: image.digest,
							remoteDigest:  remoteDigest,
							updateStatus:  updateStatus,
						}
						metrics = append(metrics, updateMetrics)
						mu.Unlock()
					}
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
	updateCount := 0
	for _, image := range m.imageUpdateMetrics {
		if image.updateStatus == 1 {
			updateCount++
		}
	}
	logger.Info(
		"collecting image update metrics",
		"source", "background worker",
		"images", imageCount,
		"updates", updateCount,
		"duration", time.Since(start).Round(time.Millisecond),
	)
}
