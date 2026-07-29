# logporter

![Docker Hub Pulls](https://img.shields.io/docker/pulls/lifailon/logporter?label=Docker+Hub+Pulls&logo=docker) \
![Docker Image Size](https://img.shields.io/docker/image-size/lifailon/logporter?label=Docker+Image+Size&logo=docker)

A simple and lightweight alternative to [cAdvisor](https://github.com/google/cadvisor) for extracting basic and custom metrics from Docker containers.

The exporter supports a metric displaying the number of logs over the scrape period. With proper logging configuration and stream separation on the application side, this metric can reflect the actual traffic volume and error rate.

Comparative measurement of CPU and memory usage from `cAdvisor` and `logporter` metrics in 3 hours:

![](/img/cadvisor-cpu-usage.jpg)

![](/img/logporter-cpu-usage.jpg)

> [!NOTE]
> On average, CPU consumption is 15-20 times lower and memory consumption is 10 times lower in the basic metrics mode (including IOps and uptime) compared to `cAdvisor`.

## Quick start

Clone the repository and launch a pre-configured monitoring stack including Prometheus and Grafana with a single command

```bash
git clone https://github.com/Lifailon/logporter
cd logporter
docker-compose up -d
```

The exporter is pre-connected to Prometheus, and a Prometheus data source is added to Grafana and a dashboard is connected.

Go to Grafana UI: `http://localhost:3000` and enter `admin`:`admin`.

## Manual configuration

- Build the Docker image yourself (optional):

```bash
docker build -t lifailon/logporter .
```

- Run the exporter in a container using a locally built image or one published on [Docker Hub](https://hub.docker.com/r/lifailon/logporter):

```bash
docker run -d --name logporter \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -p 9333:9333 \
  --restart=always \
  lifailon/logporter:latest
```

Or use [compose](https://github.com/docker/compose) to securely use the docker socket through a proxy:

```yaml
services:
  logporter:
    image: lifailon/logporter:latest
    container_name: logporter
    restart: always
    ports:
      - 9333:9333
    environment:
      - DOCKER_HOST=tcp://docker-proxy:2375

  docker-proxy:
    image: lscr.io/linuxserver/socket-proxy:latest
    container_name: docker-proxy
    restart: always
    expose:
      - 2375
    environment:
      - CONTAINERS=1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
```

Use environment variables to configure the exporter:

| Label                         | Type      | Default | Description                                                                               |
| -                             | -         | -       | -                                                                                         |
| `DOCKER_METRICS_PORT`         | `int`     | `9333`  | The port on which the exporter is listening.                                              |
| `DOCKER_METRICS_HOSTNAME`     | `string`  | `""`    | A custom hostname that appears in metric tags.                                            |
| `DOCKER_METRICS_CACHE`        | `int`     | `15`    | Cache time for all collected metrics in seconds.                                          |
| `DOCKER_METRICS_LOG`          | `boolean` | `true`  | Collecting the number of messages in logs from all threads.                               |
| `DOCKER_METRICS_LOG_CACHE`    | `int`     | `15`    | Cache time for collected logs metrics in seconds.                                         |
| `DOCKER_METRICS_VOLUME`       | `boolean` | `true`  | Collecting a list of all volumes and their sizes.                                         |
| `DOCKER_METRICS_VOLUME_CACHE` | `int`     | `15`    | Cache time for collected volumes metrics in minutes.                                      |
| `DOCKER_HOST`                 | `string`  | `""`    | Optional: use a docker proxy instead of the docker socket mount for additional security.  |

> [!WARNING]
> Using custom metrics may result in a slight increase in resource consumption (depending on the volume of logs in containers and volumes). The metrics themselves are transmitted instantly thanks to data collection by background workers and caching. Check the duration value in the exporter logs to ensure that it does not exceed the scrape interval.

- Connect the new target to the Prometheus configuration file:

```yml
scrape_configs:
  - job_name: logporter
    scrape_interval: 15s
    scrape_timeout: 2s
    static_configs:
      - targets:
        - localhost:9333
```

- Import the prepared public [Grafana dashboard](https://grafana.com/grafana/dashboards/23848-docker-exporter-logporter) using the id `23848` or from [json](https://raw.githubusercontent.com/Lifailon/logporter/refs/heads/main/grafana/dashboard.json) file.

![](/img/metrics-1.jpg)

![](/img/metrics-2.jpg)

- Configure rules in [Alertmanager](https://github.com/prometheus/alertmanager), e.g., to receive notifications about high CPU usage and reboot containers:

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

## List of metrics

| Name                               | Data type   | Help description                                                                                    |
| -                                  | -           | -                                                                                                   |
| docker_image_size                  | `gauge`     | The size of the image minus the layer shared by other images                                        |
| docker_volume_size                 | `gauge`     | The size of the volumes and the number of containers associated with it in the volumeUsage tag      |
| docker_container_status            | `gauge`     | Container status: running (1) or stopped (0)                                                        |
| docker_cpu_usage_total             | `counter`   | Total CPU usage (user and kernel) in seconds                                                        |
| docker_cpu_usage_user              | `counter`   | User CPU usage in seconds                                                                           |
| docker_cpu_usage_kernel            | `counter`   | Kernel CPU usage in seconds                                                                         |
| docker_memory_total                | `gauge`     | Total memory size in bytes                                                                          |
| docker_memory_usage                | `gauge`     | Usage memory size in bytes                                                                          |
| docker_network_received_bytes      | `counter`   | Number of bytes received on the network                                                             |
| docker_network_received_packages   | `counter`   | Number of packages received on the network                                                          |
| docker_network_transmit_bytes      | `counter`   | Number of bytes transmitted on the network                                                          |
| docker_network_transmit_packages   | `counter`   | Number of packages transmitted on the network                                                       |
| docker_io_read_bytes               | `counter`   | Number of bytes read by the block device                                                            |
| docker_io_write_bytes              | `counter`   | Number of bytes write by the block device                                                           |
| docker_process_pids_count          | `gauge`     | Number of running processes and threads                                                             |
| docker_logs_stdout_count           | `counter`   | Number of logs from stdout stream per scrape interval                                               |
| docker_logs_stderr_count           | `counter`   | Number of logs from stderr stream per scrape interval                                               |
| docker_started_time                | `gauge`     | Container started time                                                                              |
