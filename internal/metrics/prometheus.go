package metrics

import "fmt"

// Converting metrics to Prometheus format
func (m *Metrics) prometheusFormat(
	metricName,
	helpText,
	typeData,
	containerId,
	containerName,
	containerState,
	composeProject,
	composeService,
	composeWorkDir string,
	customLabels []customLabelsKV,
	hostname string,
	value any,
) []string {
	var metricsText []string

	if helpText != "" && typeData != "" {
		metricsText = append(metricsText, "# HELP "+metricName+" "+helpText)
		metricsText = append(metricsText, "# TYPE "+metricName+" "+typeData)
	}

	// Add main labels
	labels := fmt.Sprintf("containerId=\"%s\",containerName=\"%s\",containerState=\"%s\"", containerId, containerName, containerState)

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

	// #18 Add custom labels (3)
	if len(customLabels) > 0 {
		for _, customLabel := range customLabels {
			labels += fmt.Sprintf(",\"%s\"=\"%s\"", customLabel.key, customLabel.value)
		}
	}

	labels += fmt.Sprintf(",hostname=\"%s\"", hostname)

	// Final metrics line
	metricsLine := fmt.Sprintf("%s{%s} %v", metricName, labels, value)
	metricsText = append(metricsText, metricsLine)

	return metricsText
}

func (m *Metrics) prometheusInspectMetrics(id string, hostname string) []string {
	// Main text slice
	var data []string

	// Get labels
	containerName := m.Labels[id].name
	containerState := m.Labels[id].state
	composeProject := m.Labels[id].composeProject
	composeService := m.Labels[id].composeService
	composeWorkDir := m.Labels[id].composeWorkDir
	customLabels := m.Labels[id].customLabelsKV

	// Status
	status := 0
	if containerState == "running" {
		status = 1
	}
	data = append(data, m.prometheusFormat(
		"docker_container_status",
		"Container status: running (1) or stopped (0)",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		status,
	)...)

	// Skip container if base inspect collection failed
	if m.inspectMetrics[id] == nil {
		return data
	}

	// Started time
	data = append(data, m.prometheusFormat(
		"docker_started_time",
		"Container started timestamp",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].startedTimestamp,
	)...)

	// Healthy
	if m.inspectMetrics[id].healthy != 2 {
		data = append(data, m.prometheusFormat(
			"docker_container_healthy",
			"Health check status",
			"gauge",
			id,
			containerName,
			containerState,
			composeProject,
			composeService,
			composeWorkDir,
			customLabels,
			hostname,
			m.inspectMetrics[id].healthy,
		)...)
	}

	// Exit code
	data = append(data, m.prometheusFormat(
		"docker_exit_code",
		"Container exit code",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].exitCode,
	)...)

	// OOM
	data = append(data, m.prometheusFormat(
		"docker_oom_killed",
		"Memory limit exceeded",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].oomKilled,
	)...)

	// Memory hard limit from inspect
	data = append(data, m.prometheusFormat(
		"docker_memory_inspect_limit",
		"Raw memory limit size in bytes (if the value is 0, then no limit is set)",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].memoryLimit,
	)...)

	// Layers size
	data = append(data, m.prometheusFormat(
		"docker_size_rw",
		"Size of the container writable layer in bytes",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].sizeRw,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_size_root_fs",
		"Total size of the container filesystem in bytes (shared image layers and writable layer)",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].sizeRootFs,
	)...)

	// Bind mounts
	data = append(data, m.prometheusFormat(
		"docker_bind_mounts",
		"Number of volumes mounted on host file system",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].bindMounts,
	)...)

	// Volume mounts
	data = append(data, m.prometheusFormat(
		"docker_volume_mounts",
		"Number of local volumes mounted",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.inspectMetrics[id].volumeMounts,
	)...)

	return data
}

// Getting all metrics in Prometheus format
func (m *Metrics) prometheusBaseMetrics(id string, hostname string) []string {
	// Skip container if base metrics collection failed
	if m.baseMetrics[id] == nil {
		return nil
	}

	// Main text slice
	var data []string

	// Get labels
	containerName := m.Labels[id].name
	containerState := m.Labels[id].state
	composeProject := m.Labels[id].composeProject
	composeService := m.Labels[id].composeService
	composeWorkDir := m.Labels[id].composeWorkDir
	customLabels := m.Labels[id].customLabelsKV

	// CPU
	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_total",
		"Total CPU usage (user and kernel) in seconds",
		"counter",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].cpuTotal,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_user",
		"User CPU usage in seconds",
		"counter",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].cpuUser,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_cpu_usage_kernel",
		"Kernel CPU usage in seconds",
		"counter",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].cpuKernel,
	)...)

	// Memory
	data = append(data, m.prometheusFormat(
		"docker_memory_current_limit",
		"Current memory limit size in bytes (if no value is specified, the total amount of available memory is displayed)",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].memoryLimit,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_memory_usage",
		"Usage memory size in bytes",
		"gauge",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
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
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].netReceiveBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_received_packages",
		"Number of packages received on the network",
		"counter",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].netReceivePackets,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_transmit_bytes",
		"Number of bytes transmitted on the network",
		"counter",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].netTransmitBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_network_transmit_packages",
		"Number of packages transmitted on the network",
		"counter",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
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
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].ioReadBytes,
	)...)

	data = append(data, m.prometheusFormat(
		"docker_io_write_bytes",
		"Number of bytes write by the block device",
		"counter",
		id,
		containerName,
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
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
		containerState,
		composeProject,
		composeService,
		composeWorkDir,
		customLabels,
		hostname,
		m.baseMetrics[id].pids,
	)...)

	return data
}
