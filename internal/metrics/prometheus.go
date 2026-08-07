package metrics

import "fmt"

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
