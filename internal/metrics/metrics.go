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
	Info                  *Info
	Labels                map[string]*Labels
	baseMetrics           map[string]*BaseMetrics
	inspectMetrics        map[string]*InspectMetric
	imageMetrics          []imageMetric
	imageUpdateMetrics    []imageUpdateMetrics
	volumeMetrics         []volumeMetric
	volumeUsage           map[string][]string
	imageUsage            map[string][]string
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
	Hostname          string
	defaultRegistry   string
	totalMemory       int64
	numberCPU         int
	containersRunning int
	containersStopped int
	imageCount        int
}

type Labels struct {
	name           string
	state          string
	status         string
	composeProject string // com.docker.compose.project
	composeService string // com.docker.compose.service
	composeWorkDir string // com.docker.compose.project.working_dir
	customLabelsKV []customLabelsKV
}

type customLabelsKV struct {
	key   string
	value string
}

type BaseMetrics struct {
	id                 string
	cpuTotal           float64
	cpuUser            float64
	cpuKernel          float64
	memoryLimit        int
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
	status           string
	startedTimestamp float64
	healthy          int
	exitCode         int
	oomKilled        int
	memoryLimit      int64
	sizeRw           int64
	sizeRootFs       int64
	volumeMounts     int
	bindMounts       int
}

// Get general metrics and hostname from Docker Info or OS
func (m *Metrics) GetDockerInfo(dockerClient *client.Client) *Info {
	info := Info{}
	hostname := "none"
	dockerInfo, err := dockerClient.Info(context.Background())
	if err == nil {
		hostname = dockerInfo.Name
	} else {
		oshn, err := os.Hostname()
		if err == nil {
			hostname = oshn
		}
	}
	info.Hostname = hostname
	info.defaultRegistry = strings.TrimPrefix(strings.TrimPrefix(dockerInfo.IndexServerAddress, "http://"), "https://")
	info.totalMemory = dockerInfo.MemTotal
	info.numberCPU = dockerInfo.NCPU
	info.containersRunning = dockerInfo.ContainersRunning
	info.containersStopped = dockerInfo.ContainersStopped
	info.imageCount = dockerInfo.Images
	return &info
}

// Get information about all containers (second param to get all or only started containers)
func (m *Metrics) getContainers(dockerClient *client.Client, All bool, logger *slog.Logger) (map[string]*Labels, []string) {
	containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{All: All})
	if err != nil {
		logger.Error("failed to get container list", "error", err)
		return nil, nil
	}
	info := map[string]*Labels{}
	m.volumeUsage = make(map[string][]string)
	m.imageUsage = make(map[string][]string)
	var idArr []string
	for _, container := range containers {
		// Fills the info structure
		i := Labels{}
		i.name = strings.Replace(container.Names[0], "/", "", 1)
		i.state = container.State
		i.status = container.Status

		// for key, value := range container.Labels {
		// 	logger.Debug(
		// 		"Container labels list",
		// 		"id", container.ID,
		// 		"name", i.name,
		// 		"key", key,
		// 		"value", value,
		// 	)
		// }

		// #8 Add compose labels
		i.composeProject = container.Labels["com.docker.compose.project"]
		i.composeService = container.Labels["com.docker.compose.service"]
		i.composeWorkDir = container.Labels["com.docker.compose.project.working_dir"]

		// #18 Add custom labels to KV struct (2)
		if m.CustomLabelsKeys != nil {
			for _, labelKey := range m.CustomLabelsKeys {
				labelValue := container.Labels[labelKey]
				// Check label value
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

		// Mapping volume names and container names
		for _, mount := range container.Mounts {
			if mount.Type == "volume" && mount.Name != "" {
				m.volumeUsage[mount.Name] = append(m.volumeUsage[mount.Name], i.name)
			}
		}
		m.imageUsage[container.ImageID] = append(m.imageUsage[container.ImageID], i.name)

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

	// CPU
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
	memoryStats, ok := data["memory_stats"].(map[string]any)
	if ok {
		memoryLimit, ok := memoryStats["limit"].(float64)
		if ok {
			memLimit := int(memoryLimit)
			bm.memoryLimit = memLimit
		}
		memoryUsage, ok := memoryStats["usage"].(float64)
		if ok {
			memUsage := int(memoryUsage)
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
	inspectData, _, err := dockerClient.ContainerInspectWithRaw(context.Background(), id, true)
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
	status := inspectData.State.Status
	// Health check
	healthy := 2
	stateHealth := inspectData.State.Health
	if stateHealth != nil {
		healthy = 0
		if inspectData.State.Health.Status == "healthy" {
			healthy = 1
		}
	}
	// Exit code
	exitCode := inspectData.State.ExitCode
	// OOM
	oomKilled := 0
	stateOOM := inspectData.State.OOMKilled
	if stateOOM {
		oomKilled = 1
	}
	// Memory limit
	memoryLimit := inspectData.HostConfig.Memory
	// Layers size
	sizeRw := *inspectData.SizeRw
	sizeRootFs := *inspectData.SizeRootFs
	// Mounts count
	bindMounts := 0
	volumeMounts := 0
	for _, mnt := range inspectData.Mounts {
		if mnt.Driver == "" {
			bindMounts++
		} else {
			volumeMounts++
		}
	}
	data := InspectMetric{
		id:               id,
		startedTimestamp: startedTimestamp,
		status:           status,
		exitCode:         exitCode,
		oomKilled:        oomKilled,
		healthy:          healthy,
		memoryLimit:      memoryLimit,
		sizeRw:           sizeRw,
		sizeRootFs:       sizeRootFs,
		bindMounts:       bindMounts,
		volumeMounts:     volumeMounts,
	}
	results <- &data
}

// Main function for getting metrics
func (m *Metrics) GetMetrics(dockerClient *client.Client, hostname string, logger *slog.Logger) []string {
	// Get a list of containers with status information and all container ID array
	m.Labels, m.idRunning = m.getContainers(dockerClient, true, logger)

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
	allID := make([]string, 0, len(m.Labels))
	for id := range m.Labels {
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
		imageUsage := len(m.imageUsage[image.id])
		imageContainers := strings.Join(m.imageUsage[image.id], ",")
		metricText := fmt.Sprintf(
			"docker_image_size{"+
				"imageName=\"%s\","+
				"tag=\"%s\","+
				"registry=\"%s\","+
				"createdDate=\"%d\","+
				"digest=\"%s\","+
				"imageUsage=\"%d\","+
				"imageContainers=\"%s\","+
				"hostname=\"%s\""+
				"} %v",
			image.name,
			image.tag,
			image.registry,
			image.createdTime,
			image.digest,
			imageUsage,
			imageContainers,
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
			imageUsage := len(m.imageUsage[image.id])
			imageContainers := strings.Join(m.imageUsage[image.id], ",")
			metricText := fmt.Sprintf(
				"docker_image_update{"+
					"imageName=\"%s\","+
					"tag=\"%s\","+
					"registry=\"%s\","+
					"createdDate=\"%d\","+
					"remoteDate=\"%d\","+
					"digest=\"%s\","+
					"remoteVersion=\"%s\","+
					"imageUsage=\"%d\","+
					"imageContainers=\"%s\","+
					"hostname=\"%s\""+
					"} %v",
				image.name,
				image.tag,
				image.registry,
				image.createdTime,
				image.remoteTime,
				image.digest,
				image.remoteVersion,
				imageUsage,
				imageContainers,
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
			metricText := fmt.Sprintf(
				"docker_volume_size{"+
					"volumeName=\"%s\","+
					"volumeDriver=\"%s\","+
					"volumeUsage=\"%d\","+
					"volumeContainers=\"%s\","+
					"hostname=\"%s\""+
					"} %v",
				volume.name,
				volume.driver,
				volume.usage,
				strings.Join(m.volumeUsage[volume.name], ","),
				hostname,
				volume.size,
			)
			data = append(data, metricText)
		}
		data = append(data, "")
	}

	// General metrics
	data = append(data, "# HELP docker_cpu_total_number Total available vCPUs")
	data = append(data, "# TYPE docker_cpu_total_number gauge")
	data = append(data, fmt.Sprintf("docker_cpu_total_number{hostname=\"%s\"} %v", hostname, m.Info.numberCPU))
	data = append(data, "# HELP docker_memory_total Total memory size in bytes")
	data = append(data, "# TYPE docker_memory_total gauge")
	data = append(data, fmt.Sprintf("docker_memory_total{hostname=\"%s\"} %v", hostname, m.Info.totalMemory))
	data = append(data, "# HELP docker_container_running_count Number of running containers")
	data = append(data, "# TYPE docker_container_running_count gauge")
	data = append(data, fmt.Sprintf("docker_container_running_count{hostname=\"%s\"} %v", hostname, m.Info.containersRunning))
	data = append(data, "# HELP docker_container_stopped_count Number of stopped containers")
	data = append(data, "# TYPE docker_container_stopped_count gauge")
	data = append(data, fmt.Sprintf("docker_container_stopped_count{hostname=\"%s\"} %v", hostname, m.Info.containersStopped))
	data = append(data, "# HELP docker_image_count Number of images")
	data = append(data, "# TYPE docker_image_count gauge")
	data = append(data, fmt.Sprintf("docker_image_count{hostname=\"%s\"} %v", hostname, m.Info.imageCount))
	data = append(data, "")

	// #19 Fill in the inspect metrics for all containers
	for id := range m.Labels {
		data = append(data, m.prometheusInspectMetrics(id, hostname)...)
		// Fill in the base metrics for running containers
		if m.Labels[id].state == "running" {
			data = append(data, m.prometheusBaseMetrics(id, hostname)...)
		}
		data = append(data, "")
	}

	return data
}
