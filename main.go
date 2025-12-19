package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type Metrics struct {
	containersUp        int
	containersDown      int
	id                  []string
	info                map[string]*Info
	baseMetrics         map[string]*BaseMetrics
	getLogMetrics       bool
	getLogCustomMetrics bool
	logRegex            *regexp.Regexp
	logMetrics          map[string]*LogMetrics
	inspectMetrics      map[string]float64
}

type Info struct {
	name   string
	state  string
	status string
}

type BaseMetrics struct {
	id                 string
	cpuTotal           float64
	cpuUser            float64
	cpuKernel          float64
	memTotalBtyes      int
	memUsageBtyes      int
	netReceiveBytes    int
	netReceivePackets  int
	netTransmitBytes   int
	netTransmitPackets int
	ioReadBytes        int
	ioWriteBytes       int
	pids               int
}

type LogMetrics struct {
	stdout       int
	stderr       int
	stdall       int
	stderrCustom int
	stdoutCustom int
	stdCustom    int
}

type LogMetric struct {
	id          string
	stdout      bool
	stderr      bool
	value       int
	customValue int
}

type InspectMetric struct {
	id          string
	startedDate float64
}

// Get information about all containers (second param to get all or only started containers)
func (m *Metrics) getContainers(dockerClient *client.Client, All bool) (map[string]*Info, []string, int, int) {
	containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{All: All})
	if err != nil {
		log.Println("Failed to get container list: %w", err)
		return nil, nil, 0, 0
	}
	containersUp := 0
	containersDown := 0
	info := map[string]*Info{}
	var idArr []string
	for _, container := range containers {
		// Debug output container info
		// godump.Dump(container)
		// Counting the number of running containers
		if container.State == "running" {
			containersUp++
		} else {
			containersDown++
			continue
		}
		// Fills the info structure
		i := Info{}
		currentId := container.ID
		i.name = strings.Replace(container.Names[0], "/", "", 1)
		i.state = container.State
		i.status = container.Status
		info[currentId] = &i
		// Fills an array of container id
		idArr = append(idArr, currentId)
	}
	return info, idArr, containersUp, containersDown
}

// Get metric list for specified container by id
func (m *Metrics) getBaseMetrics(dockerClient *client.Client, id string) *BaseMetrics {
	stats, err := dockerClient.ContainerStatsOneShot(context.Background(), id)
	if err != nil {
		log.Println("Failed to get container stats: %w", err)
		return nil
	}
	defer stats.Body.Close()

	// Read statistics
	jsonStats, err := io.ReadAll(stats.Body)
	if err != nil {
		log.Println("Failed to read container stats: %w", err)
		return nil
	}

	// Create a map to extract data from json
	var data map[string]interface{}

	// Parsing json and fill in map
	err = json.Unmarshal(jsonStats, &data)
	if err != nil {
		log.Println("Failed to unmarshal JSON stats: %w", err)
	}

	// Debug output metrics from stats
	// godump.Dump(data)

	// Extract data and fill structure
	var bm BaseMetrics = BaseMetrics{}

	bm.id = id

	// Processor
	cpuStats, ok := data["cpu_stats"].(map[string]interface{})
	if ok {
		cpuUsage, ok := cpuStats["cpu_usage"].(map[string]interface{})
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
	memory_stats, ok := data["memory_stats"].(map[string]interface{})
	if ok {
		memory_limit, ok := memory_stats["limit"].(float64)
		if ok {
			memLimit := int(memory_limit)
			bm.memTotalBtyes = memLimit
		}
		memory_usage, ok := memory_stats["usage"].(float64)
		if ok {
			memUsage := int(memory_usage)
			bm.memUsageBtyes = memUsage
		}
	}

	// Network
	networks, ok := data["networks"].(map[string]interface{})
	if ok {
		// Aggregate stats from all network interfaces
		for _, v := range networks {
			network_interface, ok := v.(map[string]interface{})
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
	blkioStats, ok := data["blkio_stats"].(map[string]interface{})
	if ok {
		ioBytesRecursive, ok := blkioStats["io_service_bytes_recursive"].([]interface{})
		if ok {
			for i := range ioBytesRecursive {
				operation := ioBytesRecursive[i].(map[string]interface{})
				op, ok := operation["op"].(string)
				if !ok {
					continue
				}
				value, ok := operation["value"].(float64)
				if !ok {
					continue
				}
				
				if op == "read" {
					bm.ioReadBytes += int(value)
				} else if op == "write" {
					bm.ioWriteBytes += int(value)
				}
			}
		}
	}

	// PIDs count
	pidsStats, ok := data["pids_stats"].(map[string]interface{})
	if ok {
		bm.pids = int(pidsStats["current"].(float64))
	}

	// defer wg.Done()
	return &bm
}

// Get line count from logs for specified container by id
func (m *Metrics) getLogsCount(dockerClient *client.Client, id string, stdout bool, stderr bool, wg *sync.WaitGroup, results chan *LogMetric) {
	// Fill in options to read container logs
	logsOptions := container.LogsOptions{
		ShowStdout: stdout,
		ShowStderr: stderr,
	}

	// Get log content
	logs, err := dockerClient.ContainerLogs(context.Background(), id, logsOptions)
	if err != nil {
		log.Println("Failed to get container logs: %w", err)
		return
	}
	defer logs.Close()

	// Read and parsing json
	dataLogs, err := io.ReadAll(logs)
	if err != nil {
		log.Println("Failed to read container logs: %w", err)
		return
	}

	// Debug output logs
	// fmt.Println(string(dataLogs))

	// Convert bytes to text and get array from rows
	lines := strings.Split(string(dataLogs), "\n")
	// Get line count
	countLogs := len(lines) - 1

	// Parse errors/custom lines
	errConuter := 0
	if m.getLogCustomMetrics {
		if len(lines) > 1 {
			for _, line := range lines {
				if m.logRegex.MatchString(line) {
					errConuter++
				}
			}
		}
	}

	logMetric := LogMetric{
		id:          id,
		stdout:      stdout,
		stderr:      stderr,
		value:       countLogs,
		customValue: errConuter,
	}

	defer wg.Done()
	results <- &logMetric
}

// Get metrics from inspect method
func (m *Metrics) getInspect(dockerClient *client.Client, id string, wg *sync.WaitGroup, results chan *InspectMetric) {
	inspect, err := dockerClient.ContainerInspect(context.Background(), id)
	if err != nil {
		log.Println("Failed to inspect container: %w", err)
		return
	}
	// Debug output inspect data
	// godump.Dump(inspect)
	// Get started time
	startedDate := inspect.State.StartedAt
	// Converting string to time type
	startedTime, err := time.Parse(time.RFC3339Nano, startedDate)
	if err != nil {
		log.Println("Failed to parse started time: %w", err)
		return
	}
	// Converting to timestamp
	startedTimestamp := float64(startedTime.Unix())
	data := InspectMetric{
		id:          id,
		startedDate: startedTimestamp,
	}
	defer wg.Done()
	results <- &data
}

// Converting metrics to Prometheus format
func (m *Metrics) prometheusFormat(metricName, helpText, typeData, id, containerName, hostname string, value any) []string {
	var metricsText []string

	metricsText = append(metricsText, "# HELP "+metricName+" "+helpText)
	metricsText = append(metricsText, "# TYPE "+metricName+" "+typeData)
	metricsLine := fmt.Sprintf(
		"%s{containerId=\"%s\",containerName=\"%s\",hostname=\"%s\"} %v",
		metricName, id, containerName, hostname, value,
	)
	metricsText = append(metricsText, metricsLine)

	return metricsText
}

// Getting all metrics in Prometheus format
func (m *Metrics) prometheusMetrics(id string, hostname string) []string {
	// Main text slice
	var data []string

	// Get container name
	containerName := m.info[id].name

	// Processor
	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_total",
		"Total CPU usage (user and kernel) in seconds",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].cpuTotal,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_user",
		"User CPU usage in seconds",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].cpuUser,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_kernel",
		"Kernel CPU usage in seconds",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].cpuKernel,
	)...)

	// Memory
	data = append(data, m.prometheusFormat(
		"docker_memory_total",
		"Total memory size in bytes",
		"gauge", id, containerName, hostname,
		m.baseMetrics[id].memTotalBtyes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_memory_usage",
		"Usage memory size in bytes",
		"gauge", id, containerName, hostname,
		m.baseMetrics[id].memUsageBtyes,
	)...)

	// Network
	data = append(data, m.prometheusFormat(
		"docker_network_received_bytes",
		"Number of bytes received on the network",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].netReceiveBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_received_packages",
		"Number of packages received on the network",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].netReceivePackets,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_transmit_bytes",
		"Number of bytes transmitted on the network",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].netTransmitBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_transmit_packages",
		"Number of packages transmitted on the network",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].netTransmitPackets,
	)...)

	// IO
	data = append(data, m.prometheusFormat(
		"docker_io_read_bytes",
		"Number of bytes read by the block device",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].ioReadBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_io_write_bytes",
		"Number of bytes write by the block device",
		"counter", id, containerName, hostname,
		m.baseMetrics[id].ioWriteBytes,
	)...)

	// PIDs
	data = append(data, m.prometheusFormat(
		"docker_process_pids_count",
		"Number of running processes and threads",
		"gauge", id, containerName, hostname,
		m.baseMetrics[id].pids,
	)...)

	// Logs
	if m.getLogMetrics {
		data = append(data, m.prometheusFormat(
			"docker_logs_stdout_count",
			"Number of logs from stdout stream",
			"counter", id, containerName, hostname,
			m.logMetrics[id].stdout,
		)...)

		data = append(data, m.prometheusFormat(
			"docker_logs_stderr_count",
			"Number of logs from stderr stream",
			"counter", id, containerName, hostname,
			m.logMetrics[id].stderr,
		)...)

		data = append(data, m.prometheusFormat(
			"docker_logs_all_count",
			"Number of logs from all stream",
			"counter", id, containerName, hostname,
			m.logMetrics[id].stdall,
		)...)

		if m.getLogCustomMetrics {
			data = append(data, m.prometheusFormat(
				"docker_logs_custom_count",
				"Number of logs containing custom regular expression from all streams (by default, containing the error level)",
				"counter", id, containerName, hostname,
				m.logMetrics[id].stdCustom,
			)...)
		}
	}

	// Started time
	data = append(data, m.prometheusFormat(
		"docker_started_time",
		"Container started time",
		"gauge", id, containerName, hostname,
		m.inspectMetrics[id],
	)...)

	data = append(data, "")

	return data
}

// Main function for getting metrics
func (m *Metrics) getMetrics(dockerClient *client.Client, hostname string) []string {
	// Get a list of containers with status information and all container ID array
	m.info, m.id, m.containersUp, m.containersDown = m.getContainers(dockerClient, true)

	// Create a waiting group and a buffered channel to store data from goroutines
	var wg sync.WaitGroup
	wg.Add(len(m.id))
	results := make(chan *BaseMetrics, len(m.id))

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

	if m.getLogMetrics {
		// Create x2 groups for logs (stdout and stderr)
		wg.Add(len(m.id) * 2)
		logResults := make(chan *LogMetric, len(m.id)*2)

		// Get a list of custom metrics from logs
		for _, id := range m.id {
			go m.getLogsCount(dockerClient, id, true, false, &wg, logResults)
			go m.getLogsCount(dockerClient, id, false, true, &wg, logResults)
		}

		wg.Wait()
		close(logResults)

		// Get metrics from logs
		m.logMetrics = map[string]*LogMetrics{}
		for lr := range logResults {
			// Initialize the LogMetrics structure if it doesn't exist
			if m.logMetrics[lr.id] == nil {
				m.logMetrics[lr.id] = &LogMetrics{}
			}
			if lr.stdout {
				m.logMetrics[lr.id].stdout = lr.value
				if m.getLogCustomMetrics {
					m.logMetrics[lr.id].stderrCustom = lr.customValue
				}
			} else if lr.stderr {
				m.logMetrics[lr.id].stderr = lr.value
				if m.getLogCustomMetrics {
					m.logMetrics[lr.id].stdoutCustom = lr.customValue
				}
			}
		}

		// Filling the sum of the streams
		for _, id := range m.id {
			m.logMetrics[id].stdall = m.logMetrics[id].stdout + m.logMetrics[id].stderr
			if m.getLogCustomMetrics {
				m.logMetrics[id].stdCustom = m.logMetrics[id].stderrCustom + m.logMetrics[id].stdoutCustom
			}
		}
	}

	// Get start time containers
	wg.Add(len(m.id))
	inspectData := make(chan *InspectMetric, len(m.id))

	for _, id := range m.id {
		m.getInspect(dockerClient, id, &wg, inspectData)
	}

	wg.Wait()
	close(inspectData)

	m.inspectMetrics = map[string]float64{}
	for data := range inspectData {
		m.inspectMetrics[data.id] = data.startedDate
	}

	// Debug output main structure
	// godump.Dump(m)

	// Get metrics in Prometheus format
	var data []string

	data = append(data, "# HELP docker_containers_up_count Number of running containers")
	data = append(data, "# TYPE docker_containers_up_count gauge")
	metricText := fmt.Sprintf("docker_containers_up_count{hostname=\"%s\"} %v", hostname, m.containersUp)
	data = append(data, metricText)

	data = append(data, "# HELP docker_containers_down_count Number of stopped containers")
	data = append(data, "# TYPE docker_containers_down_count gauge")
	metricText = fmt.Sprintf("docker_containers_down_count{hostname=\"%s\"} %v", hostname, m.containersDown)
	data = append(data, metricText)

	data = append(data, "")

	for _, id := range m.id {
		data = append(data, m.prometheusMetrics(id, hostname)...)
	}

	return data
}

// Logging http server requests
func loggingMiddleware(next http.Handler) http.Handler {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	log := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logger.Printf("%s request on %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		next.ServeHTTP(w, r)
		logger.Printf("Response time %v from %s", time.Since(start)/1000000*1000000, r.RemoteAddr)
	})
	return log
}

// Get hostname from Docker Info method
func (m *Metrics) getHostname(dockerClient *client.Client) string {
	info, err := dockerClient.Info(context.Background())
	if err != nil {
		log.Println("Failed to get hostname: %w", err)
	}
	return info.Name
}

func main() {
	// Initialize the main structure
	var metrics *Metrics = &Metrics{}
	var err error

	// Create client with connection parameters from environment variables and approval of the API version with the Docker Daemon
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	client.NewClientWithOpts()
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	defer dockerClient.Close()

	// Get environment variables
	getLogMetrics := os.Getenv("DOCKER_LOG_METRICS")
	if getLogMetrics == "true" || getLogMetrics == "True" {
		metrics.getLogMetrics = true
	} else {
		metrics.getLogMetrics = false
	}
	getLogCustomMetrics := os.Getenv("DOCKER_LOG_CUSTOM_METRICS")
	if getLogCustomMetrics == "true" || getLogCustomMetrics == "True" {
		metrics.getLogCustomMetrics = true
		textRegex := os.Getenv("DOCKER_LOG_CUSTOM_QUERY")
		if textRegex == "" {
			textRegex = `\"(err|error|ERR|ERROR)\"`
		}
		metrics.logRegex, err = regexp.Compile(textRegex)
		if err != nil {
			log.Fatalf("Failed to compile custom query regular expression: %v", err)
		}
	} else {
		metrics.getLogCustomMetrics = false
	}

	// Get hostname
	// hostname, _ := os.Hostname()
	hostname := metrics.getHostname(dockerClient)

	// Create HTTP server
	httpServerMux := http.NewServeMux()

	// Endpoint: /metrics
	httpServerMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		metricsData := metrics.getMetrics(dockerClient, hostname)
		// Output metrics in Prometheus format
		for _, m := range metricsData {
			fmt.Fprintln(w, m)
		}
	})

	logSrv := loggingMiddleware(httpServerMux)

	// Start HTTP server
	port := "9333"
	fmt.Println("Exporter started on " + port + " port.")
	err = http.ListenAndServe(":"+port, logSrv)
	if err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}
}
