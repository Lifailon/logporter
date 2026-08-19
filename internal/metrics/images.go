package metrics

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/docker/docker/client"
)

type imageUpdateMetrics struct {
	tag           string
	currentDigest string
	remoteDigest  string
	update        int
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

func (m *Metrics) getImageUpdateMetrics(dockerClient *client.Client, hostname string, logger *slog.Logger) []imageUpdateMetrics {
	if len(m.imageMetrics) > 0 {
		// metrics := make([]imageUpdateMetrics, 0, len(m.imageMetrics))
		// var wg sync.WaitGroup
		// var mu sync.Mutex
		// wg.Add(len(m.imageMetrics))
		// for _, image := range m.imageMetrics {
		// 	go func(image imageMetric) {
		// 		defer wg.Done()
		// 		status, err := m.checkImageUpdate(dockerClient, image)
		// 		if err != nil {
		// 			logger.Error("failed to inspect image distribution", "image", image.tag, "error", err)
		// 		} else {
		// 			mu.Lock()
		// 			metrics = append(metrics, status)
		// 			mu.Unlock()
		// 		}
		// 	}(image)
		// }
		// wg.Wait()
		// return metrics

		// Создаем буферизированный канал для сбора результатов работы горутин
		ch := make(chan imageUpdateMetrics, len(m.imageMetrics))
		// Объявляем группу ожиданеия для отслеживания жизненного цикла всех запущенных горутин
		var wg sync.WaitGroup
		// Заполняем счетчик (выделяем память заранее)
		wg.Add(len(m.imageMetrics))
		// Создаем буферизированный канал (семафор) для ограничения одновременно выполняющихся задач
		// semaphore := make(chan struct{}, 10)
		for _, image := range m.imageMetrics {
			go func(image imageMetric) {
				// Гарантируем (даже в случае паники) уменьшение счетчика WaitGroup на 1 при выходе из горутины
				defer wg.Done()
				// Пытаемся отправить пустую структуру (0 байт) в семафор
				// Eсли в канале уже есть 10 элементов, горутина засыпает и ждет своей очереди
				// semaphore <- struct{}{}
				status, err := m.checkImageUpdate(dockerClient, image)
				// Извлекаем элемент из семафора и выбрасываем его
				// <-semaphore
				if err != nil {
					logger.Error("failed to inspect image distribution", "image", image.tag, "error", err)
				} else {
					ch <- status
				}
			}(image)
		}
		// Блокируем основной поток и ждем, пока все запущенные горутины вызовут wg.Done()
		wg.Wait()
		// Закрываем канал, чтобы цикл for-range понял, что все данные получены
		close(ch)
		metrics := make([]imageUpdateMetrics, 0, len(ch))
		for status := range ch {
			metrics = append(metrics, status)
		}
		return metrics
	} else {
		return nil
	}
}
