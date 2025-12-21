# logporter

A simple and lightweight alternative to [cAdvisor](https://github.com/google/cadvisor) for getting all basic metrics from Docker containers with support metrics by logs.

Comparative measurement of CPU and memory usage from `cAdvisor` and `logporter` metrics in 3 hours:

![](/img/cadvisor-cpu-usage.jpg)

![](/img/logporter-cpu-usage.jpg)

> [!NOTE]
> On average, CPU consumption is 15-20 times lower and memory consumption is 10 times lower in the basic metrics mode (including IOps and uptime) compared to `cAdvisor`.

## Why collect log counts?

- Monitor the frequency and number of errors in logs using a custom query.
- Compare the load increase with the number of logged messages. If the application does not consume many resources, the number of logged messages will reflect the load increase.
- Speed ​​up log analysis by displaying at what point in time the most messages were received from the standard and error stream.

> [!WARNING]
> Receiving metrics data directly affects performance and CPU load may become higher than `cAdvisor`, this directly depends on the number of running containers, so it is recommended to use it with a small number of running containers or for one in log parsing mode.

## Roadmap

- [x] Functions for extracting basic metrics
- [x] Functions for extracting custom metrics
- [x] Converting metrics to Prometheus format
- [x] HTTP server and logging
- [x] Error handling (check for missing data)
- [x] Getting data in a goroutine
- [x] Grafana dashboard
- [x] Docker image
- [ ] Testing

## Build

Build the Docker image yourself (optional):

```bash
git clone https://github.com/Lifailon/logporter
cd logporter
docker build -t lifailon/logporter .
# or build for different architectures
docker buildx build --platform linux/amd64,linux/arm64 -t lifailon/logporter .
```

## Install

- Run the exporter in a container using an image from [Docker Hub](https://hub.docker.com/r/lifailon/logporter):

```bash
docker run -d --name logporter \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -p 9333:9333 \
  --restart=unless-stopped \
  lifailon/logporter:latest
```

Or download the [docker-compose](https://github.com/Lifailon/logporter/blob/main/docker-compose.yml) file:

```bash
mkdir logporter && cd logporter
curl -sSL https://raw.githubusercontent.com/Lifailon/logporter/refs/heads/main/docker-compose.yml -o docker-compose.yml
docker-compose up -d
```

Use environment variables to configure the exporter:

| Label                       | Type      | Default                        | Description                                                                                           |
| --------------------------- | --------- | ------------------------------ | ----------------------------------------------------------------------------------------------------- |
| `DOCKER_LOG_METRICS`        | `boolean` | `false`                        | Getting the number of messages in logs from all streams                                               |
| `DOCKER_LOG_CUSTOM_METRICS` | `boolean` | `false`                        | Enable getting custom metrics                                                                         |
| `DOCKER_LOG_CUSTOM_QUERY`   | `string`  | `\"(err\|error\|ERR\|ERROR)\"` | Custom filter query in regex format (default example is equivalent to `error` level in `json` format) |
| `DOCKER_HOST`               | `string`  | `""`                           | Optional: Use a docker proxy instead of the docker socket mount for additional security.              |

- Connect the new target in the `prometheus.yml` configuration:

```yml
scrape_configs:
  - job_name: logporter
    scrape_interval: 5s
    scrape_timeout: 1s
    static_configs:
      - targets:
        - localhost:9333
```

> [!NOTE]
> If you are using custom metrics to get log counts, change the polling interval and response timeout settings in Prometheus based on the requests processing time in the exporter logs.

- Import the prepared public [Grafana dashboard](https://grafana.com/grafana/dashboards/23848-docker-exporter-logporter) using the id `23848` or from [json](https://raw.githubusercontent.com/Lifailon/logporter/refs/heads/main/grafana/dashboard.json) file.

![](/img/metrics-1.jpg)

![](/img/metrics-2.jpg)

- Set up alerts via [Alertmanager](https://github.com/prometheus/alertmanager), for example to receive notifications about high CPU load and reboot containers:

```yml
groups:
- name: processor
  rules:
  - alert: CONTAINER_CPU_WARN
    expr: avg(rate(docker_cpu_usage_total[1m])) by (containerName) * 100 > 50
    for: 1m
    labels:
      severity: warning
    annotations:
      description: "CPU load above 50% on container {{ $labels.containerName }}"

- name: reboot
  rules:
  - alert: CONTAINER_UPTIME_ERR
    expr: avg(changes(docker_started_time[1m])) by (containerName,hostname) > 0
    labels:
      severity: error
    annotations:
      description: "Reboot container {{ $labels.containerName }} on {{ $labels.hostname }}"
```