package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

type Metrics struct {
	id               []string
	info             map[string]*Info
	baseMetrics      map[string]*BaseMetrics
	getLogMetrics    bool
	logMetrics       map[string]*LogMetrics
	inspectMetrics   map[string]float64
	imageMetrics     []imageMetric
	volumeMetrics    []volumeMetric
	cacheData        []string
	cacheTime        time.Time
	cacheTTL         time.Duration
	cacheMutex       sync.RWMutex
	cacheValid       bool
	lastLogScrape    time.Time
	getVolumeMetrics bool
	logCache         time.Duration
	volumeCache      time.Duration
}

type Info struct {
	name           string
	state          string
	status         string
	composeProject string // com.docker.compose.project
	composeService string // com.docker.compose.service
	composeWorkDir string // com.docker.compose.project.working_dir

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

type LogMetrics struct {
	stdout int
	stderr int
}

type LogMetric struct {
	id     string
	stdout bool
	stderr bool
	value  int
}

type InspectMetric struct {
	id        string
	timestamp float64
}

type imageMetric struct {
	tag  string
	size int
}

type volumeMetric struct {
	name   string
	driver string
	size   int64
	usage  int64
}

// Get information about all containers (second param to get all or only started containers)
func (m *Metrics) getContainers(dockerClient *client.Client, All bool) (map[string]*Info, []string) {
	containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{All: All})
	if err != nil {
		log.Printf("Failed to get container list: %v", err)
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
func (m *Metrics) getBaseMetrics(dockerClient *client.Client, id string) *BaseMetrics {
	stats, err := dockerClient.ContainerStatsOneShot(context.Background(), id)
	if err != nil {
		log.Printf("Failed to get container stats: %v", err)
		return nil
	}
	defer stats.Body.Close()

	// Read statistics
	jsonStats, err := io.ReadAll(stats.Body)
	if err != nil {
		log.Printf("Failed to read container stats: %v", err)
		return nil
	}

	// Create a map to extract data from json
	var data map[string]any

	// Parsing json and fill in map
	err = json.Unmarshal(jsonStats, &data)
	if err != nil {
		log.Printf("Failed to unmarshal JSON stats: %v", err)

	}
	// Extract data and fill structure
	var bm BaseMetrics = BaseMetrics{}

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

// Get line count from logs for specified container by id
func (m *Metrics) getLogsCount(dockerClient *client.Client, id string, stdout bool, stderr bool, since time.Time, wg *sync.WaitGroup, results chan *LogMetric) {
	defer wg.Done()

	// Fill in options to read container logs
	logsOptions := container.LogsOptions{
		ShowStdout: stdout,
		ShowStderr: stderr,
	}

	// #10 Add the number of records for the scrape interval (since the last data collection)
	if !since.IsZero() {
		logsOptions.Since = fmt.Sprintf("%d", since.Unix())
	}

	// Get log content
	logs, err := dockerClient.ContainerLogs(context.Background(), id, logsOptions)
	if err != nil {
		log.Printf("Failed to get container logs: %v", err)
		return
	}
	defer logs.Close()

	// Read and parsing json
	// dataLogs, err := io.ReadAll(logs)
	// if err != nil {
	// 	log.Printf("Failed to read container logs: %v", err)
	// 	return
	// }
	// Convert bytes to text and get array from rows
	// lines := strings.Split(string(dataLogs), "\n")
	// Get line count
	// countLogs := len(lines) - 1
	// Counting the number of line breaks in bytes
	// countLogs := bytes.Count(dataLogs, []byte{'\n'})

	scanner := bufio.NewScanner(logs)
	// Maximum buffer size is 1 MB for one line.
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	countLogs := 0
	for scanner.Scan() {
		countLogs++
	}
	err = scanner.Err()
	if err != nil {
		log.Printf("Failed to read container logs: %v", err)
		return
	}

	logMetric := LogMetric{
		id:     id,
		stdout: stdout,
		stderr: stderr,
		value:  countLogs,
	}

	results <- &logMetric
}

func (m *Metrics) updateLogsMetrics(dockerClient *client.Client, logger *slog.Logger) {
	start := time.Now()

	var wg sync.WaitGroup

	// Create x2 groups for logs (stdout and stderr streams)
	logBufferSize := len(m.id) * 2
	wg.Add(logBufferSize)
	logResults := make(chan *LogMetric, len(m.id)*2)

	// Get a list of custom metrics from logs
	for _, id := range m.id {
		go m.getLogsCount(dockerClient, id, true, false, m.lastLogScrape, &wg, logResults)
		go m.getLogsCount(dockerClient, id, false, true, m.lastLogScrape, &wg, logResults)
	}

	wg.Wait()
	close(logResults)

	// Get metrics from logs
	for lr := range logResults {
		// Initialize the LogMetrics structure if it doesn't exist
		if m.logMetrics[lr.id] == nil {
			m.logMetrics[lr.id] = &LogMetrics{}
		}
		if lr.stdout {
			m.logMetrics[lr.id].stdout += lr.value
		} else if lr.stderr {
			m.logMetrics[lr.id].stderr += lr.value
		}
	}

	logger.Info("Collecting log metrics",
		"source", "background worker",
		"containers", len(m.id),
		"duration", time.Since(start).Round(time.Millisecond),
	)

	// Update new scrape date
	m.lastLogScrape = time.Now()
}

// Get metrics from inspect method
func (m *Metrics) getInspect(dockerClient *client.Client, id string, wg *sync.WaitGroup, results chan *InspectMetric) {
	defer wg.Done()
	inspect, err := dockerClient.ContainerInspect(context.Background(), id)
	if err != nil {
		log.Printf("Failed to inspect container: %v", err)
		return
	}
	// Get started time
	StartedAt := inspect.State.StartedAt
	// Converting string to time type
	startedTime, err := time.Parse(time.RFC3339Nano, StartedAt)
	if err != nil {
		log.Printf("Failed to parse started time: %v", err)
		return
	}
	// Converting to timestamp
	startedTimestamp := float64(startedTime.Unix())
	data := InspectMetric{
		id:        id,
		timestamp: startedTimestamp,
	}
	results <- &data
}

// Get list of images and their sizes
func (m *Metrics) getImagesMetrics(dockerClient *client.Client) ([]imageMetric, error) {
	var imageMetrics []imageMetric
	imageOptions := image.ListOptions{SharedSize: true}
	images, err := dockerClient.ImageList(context.Background(), imageOptions)
	if err != nil {
		return nil, fmt.Errorf("Error getting image list: %v", err)
	}
	for _, image := range images {
		tag := "none"
		if len(image.RepoTags) > 0 {
			tag = image.RepoTags[0]
		}
		size := int(image.Size)
		sharedSize := int(image.SharedSize)
		if sharedSize > 0 {
			size = size - sharedSize
		}
		data := imageMetric{
			tag:  tag,
			size: size,
		}
		imageMetrics = append(imageMetrics, data)
	}
	return imageMetrics, nil
}

// Get list of volumes and their sizes
func (m *Metrics) getVolumesMetrics(dockerClient *client.Client) ([]volumeMetric, error) {
	diskOptions := types.DiskUsageOptions{}
	diskUsage, err := dockerClient.DiskUsage(context.Background(), diskOptions)
	if err != nil {
		return nil, fmt.Errorf("Error getting volume list: %v", err)
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

func (m *Metrics) updateVolumesMetrics(dockerClient *client.Client, logger *slog.Logger) {
	start := time.Now()
	volumeMetrics, err := m.getVolumesMetrics(dockerClient)
	if err != nil {
		fmt.Println(err)
	}
	m.volumeMetrics = volumeMetrics
	volumeCount := len(m.volumeMetrics)
	logger.Info("collecting volume metrics",
		"source", "background worker",
		"volumes", strconv.Itoa(volumeCount),
		"duration", time.Since(start).Round(time.Millisecond),
	)
}

// Converting metrics to Prometheus format
func (m *Metrics) prometheusFormat(metricName, helpText, typeData, id, containerName, composeProject, composeService, composeWorkDir, hostname string, value any) []string {
	var metricsText []string

	if helpText != "" && typeData != "" {
		metricsText = append(metricsText, "# HELP "+metricName+" "+helpText)
		metricsText = append(metricsText, "# TYPE "+metricName+" "+typeData)
	}

	// Add main labels
	labels := fmt.Sprintf("containerId=\"%s\",containerName=\"%s\"", id, containerName)

	// Add compose labels
	if composeProject != "" {
		labels += fmt.Sprintf(",composeProject=\"%s\"", composeProject)
	}
	if composeService != "" {
		labels += fmt.Sprintf(",composeService=\"%s\"", composeService)
	}
	if composeWorkDir != "" {
		labels += fmt.Sprintf(",composeWorkDir=\"%s\"", composeWorkDir)
	}
	labels += fmt.Sprintf(",hostname=\"%s\"", hostname)

	// Final metrics line
	metricsLine := fmt.Sprintf("%s{%s} %v", metricName, labels, value)
	metricsText = append(metricsText, metricsLine)

	return metricsText
}

// Getting all metrics in Prometheus format
func (m *Metrics) prometheusMetrics(id string, hostname string) []string {
	// Skip container if base metrics collection failed
	if m.baseMetrics[id] == nil {
		return nil
	}

	// Main text slice
	var data []string

	// Get container name
	containerName := m.info[id].name

	// Get compose labels
	composeProject := m.info[id].composeProject
	composeService := m.info[id].composeService
	composeWorkDir := m.info[id].composeWorkDir

	// Processor
	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_total",
		"Total CPU usage (user and kernel) in seconds",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].cpuTotal,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_user",
		"User CPU usage in seconds",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].cpuUser,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_kernel",
		"Kernel CPU usage in seconds",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].cpuKernel,
	)...)

	// Memory
	data = append(data, m.prometheusFormat(
		"docker_memory_total",
		"Total memory size in bytes",
		"gauge",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].memTotalBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_memory_usage",
		"Usage memory size in bytes",
		"gauge",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].memUsageBytes,
	)...)

	// Network
	data = append(data, m.prometheusFormat(
		"docker_network_received_bytes",
		"Number of bytes received on the network",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].netReceiveBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_received_packages",
		"Number of packages received on the network",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].netReceivePackets,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_transmit_bytes",
		"Number of bytes transmitted on the network",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].netTransmitBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_transmit_packages",
		"Number of packages transmitted on the network",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].netTransmitPackets,
	)...)

	// IO
	data = append(data, m.prometheusFormat(
		"docker_io_read_bytes",
		"Number of bytes read by the block device",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].ioReadBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_io_write_bytes",
		"Number of bytes write by the block device",
		"counter",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].ioWriteBytes,
	)...)

	// PIDs
	data = append(data, m.prometheusFormat(
		"docker_process_pids_count",
		"Number of running processes and threads",
		"gauge",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.baseMetrics[id].pids,
	)...)

	// Logs
	if m.getLogMetrics {
		if m.logMetrics[id] != nil {
			data = append(data, m.prometheusFormat(
				"docker_logs_stdout_count",
				"Number of messages in logs from standard output",
				"counter",
				id,
				containerName,
				composeProject,
				composeService,
				composeWorkDir,
				hostname,
				m.logMetrics[id].stdout,
			)...)

			data = append(data, m.prometheusFormat(
				"docker_logs_stderr_count",
				"Number of messages in logs from error output",
				"counter",
				id,
				containerName,
				composeProject,
				composeService,
				composeWorkDir,
				hostname,
				m.logMetrics[id].stderr,
			)...)
		}
	}

	// Started time
	data = append(data, m.prometheusFormat(
		"docker_started_time",
		"Container started time",
		"gauge",
		id,
		containerName,
		composeProject,
		composeService,
		composeWorkDir,
		hostname,
		m.inspectMetrics[id],
	)...)

	data = append(data, "")

	return data
}

// Main function for getting metrics
func (m *Metrics) getMetrics(dockerClient *client.Client, hostname string) []string {
	// Get a list of containers with status information and all container ID array
	m.info, m.id = m.getContainers(dockerClient, true)

	// Create a waiting group and a buffered channel to store data from goroutines
	var wg sync.WaitGroup
	wg.Add(len(m.id))
	results := make(chan *BaseMetrics, len(m.id))

	// Get base metrics
	for _, id := range m.id {
		go func(containerID string) {
			defer wg.Done()
			res := m.getBaseMetrics(dockerClient, containerID)
			results <- res
		}(id)
	}

	wg.Wait()
	close(results)

	// Initialize the metrics structure
	m.baseMetrics = make(map[string]*BaseMetrics, len(results))

	// Fill the map with values
	for r := range results {
		if r != nil {
			m.baseMetrics[r.id] = r
		}
	}

	// Get start time containers from inspect
	wg.Add(len(m.id))
	inspectData := make(chan *InspectMetric, len(m.id))

	for _, id := range m.id {
		go m.getInspect(dockerClient, id, &wg, inspectData)
	}

	wg.Wait()
	close(inspectData)

	m.inspectMetrics = map[string]float64{}
	for data := range inspectData {
		m.inspectMetrics[data.id] = data.timestamp
	}

	// Get metrics in Prometheus format
	var data []string

	// #12 Get image metrics
	var err error
	m.imageMetrics, err = m.getImagesMetrics(dockerClient)
	if err != nil {
		fmt.Println(err)
	}

	// Fill in the image metrics
	data = append(data, "# HELP docker_image_size The size of the image minus the layer shared by other images")
	data = append(data, "# TYPE docker_image_size gauge")
	for _, img := range m.imageMetrics {
		metricText := fmt.Sprintf("docker_image_size{imageTag=\"%s\",hostname=\"%s\"} %v", img.tag, hostname, img.size)
		data = append(data, metricText)
	}
	data = append(data, "")

	// #12 Fill in the volume metrics
	if m.getVolumeMetrics {
		data = append(data, "# HELP docker_volume_size The size of the volumes and the number of containers associated with it in the volumeUsage tag")
		data = append(data, "# TYPE docker_volume_size gauge")
		for _, vol := range m.volumeMetrics {
			metricText := fmt.Sprintf("docker_volume_size{volumeName=\"%s\",volumeDriver=\"%s\",volumeUsage=\"%d\",hostname=\"%s\"} %v", vol.name, vol.driver, vol.usage, hostname, vol.size)
			data = append(data, metricText)
		}
		data = append(data, "")
	}

	// #9 Fill in the status for all containers
	data = append(data, "# HELP docker_container_status Container status: running (1) or stopped (0)")
	data = append(data, "# TYPE docker_container_status gauge")
	for id, info := range m.info {
		var upValue int
		if info.state == "running" {
			upValue = 1
		}
		data = append(data, m.prometheusFormat(
			"docker_container_status",
			"", // skip help
			"", // skip data type
			id,
			info.name,
			info.composeProject,
			info.composeService,
			info.composeWorkDir,
			hostname,
			upValue,
		)...)
	}

	data = append(data, "")

	// Fill in the base and logs metrics
	for _, id := range m.id {
		data = append(data, m.prometheusMetrics(id, hostname)...)
	}

	return data
}

// Logging http server requests
func (m *Metrics) loggingMiddleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logger.Info("request received",
			"source", r.RemoteAddr,
			"method", r.Method,
			"path", r.URL.Path,
		)
		next.ServeHTTP(w, r)
		containersCount := len(m.id)
		logger.Info("response sent",
			"source", r.RemoteAddr,
			"cache", m.cacheValid,
			"containers", containersCount,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

// Get hostname from Docker Info method
func (m *Metrics) getHostname(dockerClient *client.Client) string {
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

func main() {
	// Initialize the main structure
	var metrics *Metrics = &Metrics{}
	var err error

	// Create client with connection parameters from environment variables and approval of the API version with the Docker Daemon
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	defer dockerClient.Close()

	var hostname string
	var port string

	// Get environment variables
	port = "9333"
	envPort := os.Getenv("DOCKER_METRICS_PORT")
	if envPort != "" {
		parsed, err := strconv.Atoi(envPort)
		if err == nil && parsed > 0 && parsed < 65536 {
			port = envPort
		}
	}

	envHostname := os.Getenv("DOCKER_METRICS_HOSTNAME")
	if envHostname == "" {
		hostname = metrics.getHostname(dockerClient)
	} else {
		hostname = envHostname
	}

	metrics.cacheTTL = 15 * time.Second
	envCache := os.Getenv("DOCKER_METRICS_CACHE")
	if envCache != "" {
		parsed, err := strconv.Atoi(envCache)
		if err == nil && parsed > 0 {
			metrics.cacheTTL = time.Duration(parsed) * time.Second
		}
	}

	// Custom logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// #10 Background worker for get metrics from logs
	metrics.getLogMetrics = false
	getLogMetrics := os.Getenv("DOCKER_METRICS_LOG")
	if strings.ToLower(getLogMetrics) == "true" {
		metrics.getLogMetrics = true
	}
	if metrics.getLogMetrics {
		// Init log map for accumulate logs
		metrics.logMetrics = map[string]*LogMetrics{}
		// We do NOT record the date on the first run to collect all logs without filtering.
		// metrics.lastLogScrape = time.Now()
		// We collect a list of identifiers of all containers before starting log collection.
		metrics.info, metrics.id = metrics.getContainers(dockerClient, true)
		// Default cache value
		metrics.logCache = 15 * time.Second
		envCache := os.Getenv("DOCKER_METRICS_LOG_CACHE")
		if envCache != "" {
			parsed, err := strconv.Atoi(envCache)
			if err == nil && parsed > 0 {
				metrics.logCache = time.Duration(parsed) * time.Second
			}
		}
		go func() {
			metrics.updateLogsMetrics(dockerClient, logger)
			ticker := time.NewTicker(metrics.logCache)
			defer ticker.Stop()
			for range ticker.C {
				metrics.updateLogsMetrics(dockerClient, logger)
			}
		}()
	}

	// #12 Background worker for get metrics from volumes
	metrics.getVolumeMetrics = true
	getVolumeMetrics := os.Getenv("DOCKER_METRICS_VOLUME")
	if strings.ToLower(getVolumeMetrics) == "false" {
		metrics.getVolumeMetrics = false
	}
	if metrics.getVolumeMetrics {
		metrics.volumeCache = 30 * time.Minute
		envCache := os.Getenv("DOCKER_METRICS_VOLUME_CACHE")
		if envCache != "" {
			parsed, err := strconv.Atoi(envCache)
			if err == nil && parsed > 0 {
				metrics.volumeCache = time.Duration(parsed) * time.Minute
			}
		}
		go func() {
			metrics.updateVolumesMetrics(dockerClient, logger)
			ticker := time.NewTicker(metrics.volumeCache)
			defer ticker.Stop()
			for range ticker.C {
				metrics.updateVolumesMetrics(dockerClient, logger)
			}
		}()
	}

	// Create HTTP server
	httpServerMux := http.NewServeMux()

	// Endpoint: /metrics
	httpServerMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		// #10 Using cache
		metrics.cacheMutex.RLock()
		metrics.cacheValid = len(metrics.cacheData) > 0 && time.Since(metrics.cacheTime) < metrics.cacheTTL
		metrics.cacheMutex.RUnlock()

		var metricsData []string
		if metrics.cacheValid {
			metrics.cacheMutex.RLock()
			metricsData = metrics.cacheData
			metrics.cacheMutex.RUnlock()
		} else {
			metricsData = metrics.getMetrics(dockerClient, hostname)
			metrics.cacheMutex.Lock()
			metrics.cacheData = metricsData
			metrics.cacheTime = time.Now()
			metrics.cacheMutex.Unlock()
		}

		// Output metrics in Prometheus format
		for _, m := range metricsData {
			fmt.Fprintln(w, m)
		}
	})

	logSrv := metrics.loggingMiddleware(httpServerMux, logger)

	// Start HTTP server
	fmt.Println("Exporter started on " + port + " port")
	err = http.ListenAndServe(":"+port, logSrv)
	if err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}
}
