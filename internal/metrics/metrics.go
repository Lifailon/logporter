package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type Metrics struct {
	idRunning             []string
	Info                  map[string]*Info
	baseMetrics           map[string]*BaseMetrics
	inspectMetrics        map[string]*InspectMetric
	imageMetrics          []imageMetric
	imageUpdateMetrics    []imageUpdateMetrics
	volumeMetrics         []volumeMetric
	CustomLabelsKeys      []string
	CacheData             []string
	CacheTime             time.Time
	CacheTTL              time.Duration
	CacheMutex            sync.RWMutex
	CacheValid            bool
	GetImageUpdateMetrics bool
	GetVolumeMetrics      bool
	VolumeCache           time.Duration
	ImageInterval         time.Duration
}

type Info struct {
	name           string
	state          string
	status         string
	composeProject string // com.docker.compose.project
	composeService string // com.docker.compose.service
	composeWorkDir string // com.docker.compose.project.working_dir
	customLabelsKV []customLabelsKV
}

type BaseMetrics struct {
	id                 string
	cpuTotal           float64
	cpuUser            float64
	cpuKernel          float64
	memTotalBytes      int
	memUsageBytes      int
	netReceiveBytes    int
	netReceivePackets  int
	netTransmitBytes   int
	netTransmitPackets int
	ioReadBytes        int
	ioWriteBytes       int
	pids               int
}

type InspectMetric struct {
	id               string
	startedTimestamp float64
	status           string
	exitCode         int
	oomKilled        int
	healthy          int
}

type customLabelsKV struct {
	key   string
	value string
}

// Get information about all containers (second param to get all or only started containers)
func (m *Metrics) getContainers(dockerClient *client.Client, All bool, logger *slog.Logger) (map[string]*Info, []string) {
	containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{All: All})
	if err != nil {
		logger.Error("failed to get container list", "error", err)
		return nil, nil
	}
	info := map[string]*Info{}
	var idArr []string
	for _, container := range containers {
		// Fills the info structure
		i := Info{}
		i.name = strings.Replace(container.Names[0], "/", "", 1)
		i.state = container.State
		i.status = container.Status

		// #8 Add compose labels
		i.composeProject = container.Labels["com.docker.compose.project"]
		i.composeService = container.Labels["com.docker.compose.service"]
		i.composeWorkDir = container.Labels["com.docker.compose.project.working_dir"]

		// #18 Check and add custom labels to KV struct
		if m.CustomLabelsKeys != nil {
			for _, labelKey := range m.CustomLabelsKeys {
				labelValue := container.Labels[labelKey]
				if labelValue != "" {
					i.customLabelsKV = append(i.customLabelsKV, customLabelsKV{
						key:   labelKey,
						value: labelValue,
					})
				}
			}
		}

		currentId := container.ID
		info[currentId] = &i

		// Fills an array of container id for get metrics (skip for stopped)
		if container.State == "running" {
			idArr = append(idArr, currentId)
		}
	}
	return info, idArr
}

// Get metric list for specified container by id
func (m *Metrics) getBaseMetrics(dockerClient *client.Client, id string, logger *slog.Logger) *BaseMetrics {
	stats, err := dockerClient.ContainerStatsOneShot(context.Background(), id)
	if err != nil {
		logger.Error("failed to get container stats", "error", err)
		return nil
	}
	defer func() { _ = stats.Body.Close() }()

	// Read statistics
	jsonStats, err := io.ReadAll(stats.Body)
	if err != nil {
		logger.Error("failed to read container stats", "error", err)
		return nil
	}

	// Create a map to extract data from json
	var data map[string]any

	// Parsing json and fill in map
	err = json.Unmarshal(jsonStats, &data)
	if err != nil {
		logger.Error("failed to unmarshal JSON stats", "error", err)

	}
	// Extract data and fill structure
	bm := BaseMetrics{}

	bm.id = id

	// Processor
	cpuStats, ok := data["cpu_stats"].(map[string]any)
	if ok {
		cpuUsage, ok := cpuStats["cpu_usage"].(map[string]any)
		if ok {
			cpuTotal, ok := cpuUsage["total_usage"].(float64)
			if ok {
				// Convert nanoseconds to seconds (divided by 1 000 000 000 000)
				bm.cpuTotal = cpuTotal / 1e9
			}
			cpuUser, ok := cpuUsage["usage_in_usermode"].(float64)
			if ok {
				bm.cpuUser = cpuUser / 1e9
			}
			cpuKernel, ok := cpuUsage["usage_in_kernelmode"].(float64)
			if ok {
				bm.cpuKernel = cpuKernel / 1e9
			}
		}
	}

	// Memory
	memory_stats, ok := data["memory_stats"].(map[string]any)
	if ok {
		memory_limit, ok := memory_stats["limit"].(float64)
		if ok {
			memLimit := int(memory_limit)
			bm.memTotalBytes = memLimit
		}
		memory_usage, ok := memory_stats["usage"].(float64)
		if ok {
			memUsage := int(memory_usage)
			bm.memUsageBytes = memUsage
		}
	}

	// Network
	networks, ok := data["networks"].(map[string]any)
	if ok {
		// Aggregate stats from all network interfaces
		for _, v := range networks {
			network_interface, ok := v.(map[string]any)
			if !ok {
				continue
			}
			// Accumulate rx_bytes from all interfaces
			if rx_bytes, ok := network_interface["rx_bytes"].(float64); ok {
				bm.netReceiveBytes += int(rx_bytes)
			}
			// Accumulate rx_packets from all interfaces
			if rx_packets, ok := network_interface["rx_packets"].(float64); ok {
				bm.netReceivePackets += int(rx_packets)
			}
			// Accumulate tx_bytes from all interfaces
			if tx_bytes, ok := network_interface["tx_bytes"].(float64); ok {
				bm.netTransmitBytes += int(tx_bytes)
			}
			// Accumulate tx_packets from all interfaces
			if tx_packets, ok := network_interface["tx_packets"].(float64); ok {
				bm.netTransmitPackets += int(tx_packets)
			}
		}
	}

	// IO
	blkioStats, ok := data["blkio_stats"].(map[string]any)
	if ok {
		ioBytesRecursive, ok := blkioStats["io_service_bytes_recursive"].([]any)
		if ok {
			for i := range ioBytesRecursive {
				operation, ok := ioBytesRecursive[i].(map[string]any)
				if !ok {
					continue
				}
				op, ok := operation["op"].(string)
				if !ok {
					continue
				}
				value, ok := operation["value"].(float64)
				if !ok {
					continue
				}
				switch op {
				case "read":
					bm.ioReadBytes += int(value)
				case "write":
					bm.ioWriteBytes += int(value)
				}
			}
		}
	}

	// PIDs count
	pidsStats, ok := data["pids_stats"].(map[string]any)
	if ok {
		if pidsCurrent, ok := pidsStats["current"].(float64); ok {
			bm.pids = int(pidsCurrent)
		}
	}

	return &bm
}

// Get metrics from inspect method
func (m *Metrics) getInspectMetrics(dockerClient *client.Client, id string, wg *sync.WaitGroup, results chan *InspectMetric, logger *slog.Logger) {
	defer wg.Done()
	inspectData, err := dockerClient.ContainerInspect(context.Background(), id)
	if err != nil {
		logger.Error("failed to inspect container", "error", err)
		return
	}
	// Get started time
	StartedAt := inspectData.State.StartedAt
	// Converting string to time type
	startedTime, err := time.Parse(time.RFC3339Nano, StartedAt)
	if err != nil {
		logger.Error("failed to parse started time", "error", err)
		return
	}
	// Converting to timestamp
	startedTimestamp := float64(startedTime.Unix())
	// Get state metrics
	status := inspectData.ContainerJSONBase.State.Status
	exitCode := inspectData.ContainerJSONBase.State.ExitCode
	oomKilled := 0
	stateOOM := inspectData.ContainerJSONBase.State.OOMKilled
	if stateOOM {
		oomKilled = 1
	}
	healthy := 2
	stateHealth := inspectData.ContainerJSONBase.State.Health
	if stateHealth != nil {
		healthy = 0
		if inspectData.State.Health.Status == "healthy" {
			healthy = 1
		}
	}
	data := InspectMetric{
		id:               id,
		startedTimestamp: startedTimestamp,
		status:           status,
		exitCode:         exitCode,
		oomKilled:        oomKilled,
		healthy:          healthy,
	}
	results <- &data
}

// Main function for getting metrics
func (m *Metrics) GetMetrics(dockerClient *client.Client, hostname string, logger *slog.Logger) []string {
	// Get a list of containers with status information and all container ID array
	m.Info, m.idRunning = m.getContainers(dockerClient, true, logger)

	// Create a waiting group and a buffered channel to store data from goroutines
	var wg sync.WaitGroup
	wg.Add(len(m.idRunning))
	results := make(chan *BaseMetrics, len(m.idRunning))

	// Get base metrics for running containers
	for _, id := range m.idRunning {
		go func(containerID string) {
			defer wg.Done()
			res := m.getBaseMetrics(dockerClient, containerID, logger)
			results <- res
		}(id)
	}

	wg.Wait()
	close(results)

	// Initialize the metrics structure
	m.baseMetrics = make(map[string]*BaseMetrics, len(results))

	// Fill the map with values
	for res := range results {
		if res != nil {
			m.baseMetrics[res.id] = res
		}
	}

	// Get metrics from inspect for all containers
	allID := make([]string, 0, len(m.Info))
	for id := range m.Info {
		allID = append(allID, id)
	}

	wg.Add(len(allID))
	inspectData := make(chan *InspectMetric, len(allID))

	for _, id := range allID {
		go m.getInspectMetrics(dockerClient, id, &wg, inspectData, logger)
	}

	wg.Wait()
	close(inspectData)

	m.inspectMetrics = make(map[string]*InspectMetric, len(inspectData))

	for data := range inspectData {
		if data != nil {
			m.inspectMetrics[data.id] = data
		}
	}

	// Get metrics in Prometheus format
	var data []string

	// #12 Get image metrics
	var err error
	m.imageMetrics, err = m.getImagesMetrics(dockerClient)
	if err != nil {
		logger.Error("failed to get image metrics", "error", err)
	}

	// Fill in the image metrics
	data = append(data, "# HELP docker_image_size The size of the image minus the layer shared by other images")
	data = append(data, "# TYPE docker_image_size gauge")
	for _, image := range m.imageMetrics {
		metricText := fmt.Sprintf("docker_image_size{imageName=\"%s\",tag=\"%s\",registry=\"%s\",digest=\"%s\",hostname=\"%s\"} %v",
			image.name,
			image.tag,
			image.registry,
			image.digest,
			hostname,
			image.size,
		)
		data = append(data, metricText)
	}
	data = append(data, "")

	// Fill in the image update status
	if m.GetImageUpdateMetrics {
		data = append(data, "# HELP docker_image_update Image update status based on digests: update required (1) or latest version (0)")
		data = append(data, "# TYPE docker_image_update gauge")
		for _, image := range m.imageUpdateMetrics {
			metricText := fmt.Sprintf(
				"docker_image_update{imageName=\"%s\",tag=\"%s\",registry=\"%s\",digest=\"%s\",remoteVersion=\"%s\",hostname=\"%s\"} %v",
				image.name,
				image.tag,
				image.registry,
				image.digest,
				image.remoteVersion,
				hostname,
				image.updateStatus,
			)
			data = append(data, metricText)
		}
		data = append(data, "")
	}

	// #12 Fill in the volume metrics
	if m.GetVolumeMetrics {
		data = append(data, "# HELP docker_volume_size The size of the volumes and the number of containers associated with it in the volumeUsage tag")
		data = append(data, "# TYPE docker_volume_size gauge")
		for _, volume := range m.volumeMetrics {
			metricText := fmt.Sprintf("docker_volume_size{volumeName=\"%s\",volumeDriver=\"%s\",volumeUsage=\"%d\",hostname=\"%s\"} %v",
				volume.name,
				volume.driver,
				volume.usage,
				hostname,
				volume.size,
			)
			data = append(data, metricText)
		}
		data = append(data, "")
	}

	// #19 Fill in the inspect metrics for all containers
	for id := range m.Info {
		data = append(data, m.prometheusInspectMetrics(id, hostname)...)
		// Fill in the base metrics for running containers
		if m.Info[id].state == "running" {
			data = append(data, m.prometheusBaseMetrics(id, hostname)...)
		}
		data = append(data, "")
	}

	return data
}

// Get hostname from Docker Info or OS
func (m *Metrics) GetHostname(dockerClient *client.Client) string {
	info, err := dockerClient.Info(context.Background())
	if err == nil {
		return info.Name
	}
	hostname, err := os.Hostname()
	if err == nil {
		return hostname
	}
	return "nil"
}
