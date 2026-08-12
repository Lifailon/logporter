![](/img/logo.png)

[![Docker Hub Pulls](https://img.shields.io/docker/pulls/lifailon/logporter?label=Docker+Hub+Pulls&logo=docker)](https://hub.docker.com/r/lifailon/logporter) \
[![Docker Image Size](https://img.shields.io/docker/image-size/lifailon/logporter?label=Docker+Image+Size&logo=docker)](https://hub.docker.com/r/lifailon/logporter/tags)

A lightweight alternative to [cAdvisor](https://github.com/google/cadvisor) for exporting metrics from Docker containers and a log collector for sending to Loki, with support for filtering by compose labels.

## Performance

Comparative CPU and memory usage measurements using `logporter` with log collection enabled (left) and `cAdvisor` (right) over a 24-hour period for 25 containers:

![](/img/logporter-vs-cadvisor.jpg)

## Quick start

Clone the repository and run the monitoring full-stack with one command:

```bash
git clone https://github.com/Lifailon/logporter
cd logporter
docker-compose up -d
```

The stack includes Prometheus with an exporter connected and alerts configured, a Loki server targeted by a log collector, and Grafana with pre-configured data sources and added dashboards.

Go to Grafana UI: `http://localhost:3000` and enter `admin`:`admin`.

## Manual setup

### Exporter

- Run the exporter in a container using the image published on [Docker Hub](https://hub.docker.com/r/lifailon/logporter):

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
    environment:
      - CONTAINERS=1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
```

List of environment variables to configure:

| Label                         | Type      | Default | Description                                                                                                                         |
| -                             | -         | -       | -                                                                                                                                   |
| `DOCKER_METRICS_PORT`         | `int`     | `9333`  | The port on which the exporter is listening.                                                                                        |
| `DOCKER_METRICS_HOSTNAME`     | `string`  | `""`    | Custom hostname displayed in Prometheus and Loki labels.                                                                            |
| `DOCKER_METRICS_CACHE`        | `int`     | `15`    | Cache time for all collected metrics in seconds. Until the cache expires, queried metrics will return the last value received.      |
| `DOCKER_METRICS_VOLUME`       | `boolean` | `true`  | Enable metrics collection for all volumes and their size. Data collection occurs in the background, without waiting for scrape.     |
| `DOCKER_METRICS_VOLUME_CACHE` | `int`     | `30`    | Cache time for collected volumes metrics in minutes.                                                                                |
| `DOCKER_HOST`                 | `string`  | `""`    | Change the socket address to monitor a remote host or proxy instead of mounting a Docker socket for increased security (optional).  |

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

- Import the prepared public [Grafana dashboard](https://grafana.com/grafana/dashboards/23848-docker-exporter-logporter) using the id `23848` or from [json](https://raw.githubusercontent.com/Lifailon/logporter/refs/heads/main/grafana/dashboards/metrics.json) file.

![](/img/basic-metrics.jpg)

- Mount the alert [rules](https://github.com/Lifailon/logporter/blob/main/prometheus/alert/rules.yml) file to your Prometheus container at `/etc/prometheus/alert.yml` (this is a basic example, you can customize it to suit your needs) to display the number and list of alerts in the Grafana dashboard.

![](/img/storage-metrics.jpg)

### Loki

- Configure a connection with Loki to send logs using environment variables:

| Label                         | Type      | Default | Description                                                                     |
| -                             | -         | -       | -                                                                               |
| `LOKI_URL`                    | `string`  | `""`    | Loki server address and port.                                                   |
| `LOKI_USERNAME`               | `string`  | `""`    | basic auth username for Loki (optional).                                        |
| `LOKI_PASSWORD`               | `string`  | `""`    | basic auth password for Loki (optional).                                        |
| `LOKI_TENANT_ID`              | `string`  | `""`    | Tenant id sent in the `X-Scope-OrgID` header (optional).                        |
| `LOKI_PUSH_SECONDS`           | `int`     | `5`     | Maximum wait time in seconds before pushing the batch to Loki.                  |
| `LOKI_PUSH_LINES`             | `int`     | `1000`  | Maximum number of log entries (lines) before pushing the batch in one request.  |
| `LOKI_BUFFER_LINES`           | `int`     | `10000` | Buffer size in entries before they are dropped (memory protection).             |

By default, metrics collection is disabled if the `LOKI_URL` variable is empty.

- Import the prepared Loki log dashboard into Grafana from [json](https://raw.githubusercontent.com/Lifailon/logporter/refs/heads/main/grafana/dashboards/logs.json) file.

![](/img/loki.jpg)

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
| docker_started_time                | `gauge`     | Container started time                                                                              |

List of available labels for filtering:

| Label             | Description                                                                                         |
| -                 | -                                                                                                   |
| `hostname`        | Hostname obtained from the Docker API or custom from the `DOCKER_METRICS_HOSTNAME` variable.        |
| `containerId`     | Unique container identifier.                                                                        |
| `containerName`   | Container name from `container_name` in compose file.                                               |
| `composeProject`  | Name of the compose project (usually the name of the directory where the compose file is located).  |
| `composeService`  | Name of the service in the compose project (may differ from `containerName`).                       |
| `composeWorkDir`  | Path to the directory with the compose stack on the host.                                           |
