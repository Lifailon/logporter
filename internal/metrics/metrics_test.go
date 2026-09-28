package metrics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"logporter/internal/dashboard"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func metricsClient(t *testing.T, handler http.Handler) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := client.NewClientWithOpts(
		client.WithHost(srv.URL),
		client.WithHTTPClient(srv.Client()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatalf("creating docker client: %v", err)
	}
	return c
}

func assertContains(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, want) {
			return
		}
	}
	t.Fatalf("expected a line containing %q, got:\n%s", want, strings.Join(lines, "\n"))
}

const validSha256Hex = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		v    int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 30, "1.0 GiB"},
		{3 << 40, "3.0 TiB"},
		{2 << 50, "2.0 PiB"},
		{4 << 60, "4.0 EiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.v); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestHumanBytesInt(t *testing.T) {
	if got := humanBytesInt(500); got != "500 B" {
		t.Errorf("humanBytesInt(500) = %q", got)
	}
	if got := humanBytesInt(1536); got != "2 KiB" {
		t.Errorf("humanBytesInt(1536) = %q, want 2 KiB", got)
	}
	if got := humanBytesInt(1 << 30); got != "1 GiB" {
		t.Errorf("humanBytesInt(1<<30) = %q, want 1 GiB", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{0.5, "0.5s"},
		{5, "5s"},
		{90, "1.5m"},
		{7200, "2.00h"},
	}
	for _, c := range cases {
		if got := humanDuration(c.v); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestHumanBytesPerSec(t *testing.T) {
	if got := humanBytesPerSec(2048); got != "2.0 KiB/s" {
		t.Errorf("humanBytesPerSec(2048) = %q, want 2.0 KiB/s", got)
	}
}

func TestCleanStatus(t *testing.T) {
	cases := map[string]string{
		"Up 2 hours (healthy)":          "Up 2 hours",
		"Exited (1) (unhealthy)":        "Exited (1)",
		"Restarting (health: starting)": "Restarting",
		"Up 1 minute":                   "Up 1 minute",
	}
	for in, want := range cases {
		if got := cleanStatus(in); got != want {
			t.Errorf("cleanStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComposeName(t *testing.T) {
	cases := []struct {
		l    *Labels
		want string
	}{
		{&Labels{composeProject: "proj", composeService: "web"}, "proj/web"},
		{&Labels{composeProject: "", composeService: "web"}, "web"},
		{&Labels{composeProject: "proj", composeService: ""}, "proj"},
	}
	for _, c := range cases {
		if got := composeName(c.l); got != c.want {
			t.Errorf("composeName(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}

func TestUpdateCount(t *testing.T) {
	if got := updateCount(nil); got != 0 {
		t.Errorf("updateCount(nil) = %d, want 0", got)
	}
	metrics := []imageUpdateMetrics{
		{updateStatus: 1},
		{updateStatus: 0},
		{updateStatus: 1},
	}
	if got := updateCount(metrics); got != 2 {
		t.Errorf("updateCount = %d, want 2", got)
	}
}

func TestShortDigest(t *testing.T) {
	if got := shortDigest(""); got != "-" {
		t.Errorf("shortDigest empty = %q", got)
	}
	if got := shortDigest("short"); got != "short" {
		t.Errorf("shortDigest short = %q", got)
	}
	want := validSha256Hex[:16] + "…"
	if got := shortDigest(validSha256Hex); got != want {
		t.Errorf("shortDigest long = %q, want %q", got, want)
	}
}

func TestTS(t *testing.T) {
	if got := ts(0); got != "-" {
		t.Errorf("ts(0) = %q, want -", got)
	}
	got := ts(1700000000)
	if !strings.HasPrefix(got, "2023-") {
		t.Errorf("ts(1700000000) = %q, want a 2023 date", got)
	}
}

func TestCPUPercString(t *testing.T) {
	if got := cpuPercentString(0, false); got != "-" {
		t.Errorf("cpuPercentString not-ok = %q", got)
	}
	if got := cpuPercentString(12.5, true); got != "12.5 %" {
		t.Errorf("cpuPercentString ok = %q, want 12.5 %%", got)
	}
}

func TestGroupContainers(t *testing.T) {
	containers := []dashboard.Container{
		{Name: "web", ComposeProject: "proj"},
		{Name: "db", ComposeProject: "proj"},
		{Name: "standalone", ComposeProject: ""},
		{Name: "api", ComposeProject: "api-proj"},
	}
	groups := groupContainers(containers)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	if groups[0].Name != "api-proj" || len(groups[0].Containers) != 1 {
		t.Errorf("group 0 = %+v", groups[0])
	}
	if groups[1].Name != "proj" || len(groups[1].Containers) != 2 {
		t.Errorf("group 1 = %+v", groups[1])
	}
	if groups[1].Containers[0].Name != "db" {
		t.Errorf("containers in a project must be sorted by name")
	}
	if groups[2].Name != "" || len(groups[2].Containers) != 1 {
		t.Errorf("ungrouped containers must come last: %+v", groups[2])
	}
	if got := groupContainers(nil); len(got) != 0 {
		t.Errorf("empty input must produce no groups")
	}
}

func TestCPUContainerPerc(t *testing.T) {
	m := &Metrics{
		baseMetrics:     map[string]*BaseMetrics{"a": {cpuTotal: 10}},
		previousMetrics: map[string]previousMetrics{"a": {cpu: 8}},
		cpuCurrentTime:  time.Now(),
		cpuPreviousTime: time.Now().Add(-time.Second),
	}
	if p, ok := m.cpuContainerPerc("a", 10); !ok || p != 200 {
		t.Fatalf("cpuContainerPerc ok = %v/%v, want 200/true", p, ok)
	}
	if _, ok := m.cpuContainerPerc("missing", 10); ok {
		t.Fatal("missing id must not be ok")
	}
	noInterval := &Metrics{cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now()}
	noInterval.previousMetrics = map[string]previousMetrics{"a": {cpu: 8}}
	if _, ok := noInterval.cpuContainerPerc("a", 10); ok {
		t.Fatal("zero interval must not be ok")
	}
	noInterval2 := &Metrics{previousMetrics: map[string]previousMetrics{"a": {cpu: 20}}, cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now().Add(-time.Second)}
	if _, ok := noInterval2.cpuContainerPerc("a", 10); ok {
		t.Fatal("negative delta must not be ok")
	}
}

func TestCPUHostPerc(t *testing.T) {
	m := &Metrics{
		Info:            &Info{numberCPU: 4},
		baseMetrics:     map[string]*BaseMetrics{"a": {cpuTotal: 10}},
		previousMetrics: map[string]previousMetrics{"a": {cpu: 8}},
		cpuCurrentTime:  time.Now(),
		cpuPreviousTime: time.Now().Add(-time.Second),
	}
	if p, ok := m.cpuHostPerc(); !ok || p != 50 {
		t.Fatalf("cpuHostPerc = %v/%v, want 50/true", p, ok)
	}

	noCPU := &Metrics{Info: &Info{numberCPU: 0}}
	if _, ok := noCPU.cpuHostPerc(); ok {
		t.Fatal("no CPUs must not be ok")
	}

	baseNil := &Metrics{Info: &Info{numberCPU: 4}, baseMetrics: map[string]*BaseMetrics{"a": nil}, previousMetrics: map[string]previousMetrics{"a": {cpu: 1}}, cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now().Add(-time.Second)}
	if _, ok := baseNil.cpuHostPerc(); ok {
		t.Fatal("nil base metrics must not be ok")
	}

	prevMissing := &Metrics{Info: &Info{numberCPU: 4}, baseMetrics: map[string]*BaseMetrics{"a": {cpuTotal: 10}}, cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now().Add(-time.Second)}
	if _, ok := prevMissing.cpuHostPerc(); ok {
		t.Fatal("missing previous metric must not be ok")
	}

	negDelta := &Metrics{Info: &Info{numberCPU: 4}, baseMetrics: map[string]*BaseMetrics{"a": {cpuTotal: 10}}, previousMetrics: map[string]previousMetrics{"a": {cpu: 99}}, cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now().Add(-time.Second)}
	if _, ok := negDelta.cpuHostPerc(); ok {
		t.Fatal("negative delta must not be ok")
	}

	zeroInterval := &Metrics{Info: &Info{numberCPU: 4}, baseMetrics: map[string]*BaseMetrics{"a": {cpuTotal: 10}}, previousMetrics: map[string]previousMetrics{"a": {cpu: 8}}, cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now()}
	if _, ok := zeroInterval.cpuHostPerc(); ok {
		t.Fatal("zero interval must not be ok")
	}
}

func TestContainerNSRate(t *testing.T) {
	m := &Metrics{
		baseMetrics:     map[string]*BaseMetrics{"a": {}},
		previousMetrics: map[string]previousMetrics{"a": {netRX: 1000, netTX: 1000, ioRead: 10, ioWrite: 10}},
		cpuCurrentTime:  time.Now(),
		cpuPreviousTime: time.Now().Add(-time.Second),
	}
	rx, tx, rd, wr, ok := m.containerNSRate("a", 2000, 2000, 20, 20)
	if !ok || rx != 1000 || tx != 1000 || rd != 10 || wr != 10 {
		t.Fatalf("containerNSRate = %v/%v/%v/%v/%v", rx, tx, rd, wr, ok)
	}
	if _, _, _, _, ok := m.containerNSRate("missing", 0, 0, 0, 0); ok {
		t.Fatal("missing id must not be ok")
	}
	noInterval := &Metrics{previousMetrics: map[string]previousMetrics{"a": {}}, cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now()}
	if _, _, _, _, ok := noInterval.containerNSRate("a", 1, 1, 1, 1); ok {
		t.Fatal("zero interval must not be ok")
	}
	neg := &Metrics{previousMetrics: map[string]previousMetrics{"a": {netRX: 9999}}, cpuCurrentTime: time.Now(), cpuPreviousTime: time.Now().Add(-time.Second)}
	if _, _, _, _, ok := neg.containerNSRate("a", 1, 1, 1, 1); ok {
		t.Fatal("negative delta must not be ok")
	}
}

func TestDashboardData(t *testing.T) {
	m := &Metrics{
		Info: &Info{Hostname: "host-01", totalMemory: 16 << 30, numberCPU: 4},
		Labels: map[string]*Labels{
			"abc":  {name: "web", state: "running", status: "Up 2 hours (healthy)", composeProject: "proj", composeService: "web", composeWorkDir: "/app"},
			"def":  {name: "db", state: "exited", status: "Exited (0) 2 hours ago", composeProject: "proj", composeService: "db"},
			"rate": {name: "no-rate", state: "running", status: "Up 1 minute", composeProject: "", composeService: ""},
			"nil":  nil,
		},
		baseMetrics: map[string]*BaseMetrics{
			"abc":  {cpuTotal: 10, memUsageBytes: 1 << 30, netReceiveBytes: 5000, netTransmitBytes: 6000, ioReadBytes: 100, ioWriteBytes: 200, pids: 7},
			"rate": {cpuTotal: 99, memUsageBytes: 20, netReceiveBytes: 1, netTransmitBytes: 1, ioReadBytes: 1, ioWriteBytes: 1, pids: 1},
		},
		previousMetrics: map[string]previousMetrics{
			"abc": {cpu: 8, netRX: 4000, netTX: 4000, ioRead: 0, ioWrite: 0},
		},
		cpuCurrentTime:  time.Now(),
		cpuPreviousTime: time.Now().Add(-time.Second),
		inspectMetrics: map[string]*InspectMetric{
			"abc": {startedTimestamp: 1700000000, healthy: 1, exitCode: 0, oomKilled: 0, memoryLimit: 4 << 30, sizeRw: 100, sizeRootFs: 500, volumeMounts: 1, bindMounts: 0},
			"def": {startedTimestamp: 0, healthy: 2, exitCode: 137, oomKilled: 1, memoryLimit: 0, sizeRw: 0, sizeRootFs: 0, volumeMounts: 0, bindMounts: 1},
		},
		imageMetrics: []imageMetric{
			{id: "img1", name: "nginx", tag: "latest", registry: "docker.io", createdTime: 1700000000, digest: "abc", size: 1024},
		},
		imageUpdateMetrics: []imageUpdateMetrics{
			{id: "img1", updateStatus: 0, remoteVersion: "sha256:" + validSha256Hex, remoteTime: 123},
		},
		volumeMetrics: []volumeMetric{
			{name: "pgdata", driver: "local", size: 2048, usage: 2},
		},
		volumeUsage:           map[string][]string{"pgdata": {"db", "db2"}},
		imageUsage:            map[string][]string{"img1": {"web"}},
		GetImageUpdateMetrics: true,
		GetVolumeMetrics:      true,
	}

	data := m.DashboardData()
	s := data.Summary
	if s.Running != 2 || s.Stopped != 1 || s.Images != 1 || s.Volumes != 1 || s.Updates != 0 {
		t.Fatalf("summary counters = %+v", s)
	}
	if s.Hostname != "host-01" || s.NumberCPU != 4 {
		t.Fatalf("summary host/cpu = %+v", s)
	}
	if s.MemoryUsed != "1.0 GiB" {
		t.Errorf("MemoryUsed = %q", s.MemoryUsed)
	}
	if s.CPU != "50.0 %" {
		t.Errorf("summary CPU = %q, want 50.0 %%", s.CPU)
	}
	if s.NetRX == "-" || s.NetTX == "-" || s.IORead == "-" || s.IOWrite == "-" {
		t.Errorf("network rates must be computed: %+v", s)
	}
	if !s.ShowUpdates || !s.ShowVolumes {
		t.Error("ShowUpdates/ShowVolumes must be true")
	}

	if len(data.Containers) != 3 {
		t.Fatalf("expected 3 containers, got %d (nil label must be skipped)", len(data.Containers))
	}
	var web, db, rate bool
	for _, c := range data.Containers {
		switch c.Name {
		case "web":
			web = true
			if c.CPU != "200.0 %" {
				t.Errorf("web CPU = %q, want 200.0 %%", c.CPU)
			}
			if c.HasStats == false || c.Healthy != "healthy" || c.Mounts != "1 vol / 0 bind" {
				t.Errorf("web container = %+v", c)
			}
			if c.NetRX == "-" {
				t.Errorf("web net rate must be computed")
			}
		case "db":
			db = true
			if c.HasStats {
				t.Errorf("db must have no stats: %+v", c)
			}
			if c.Healthy != "" || c.ExitCode != "137" || c.Mounts != "0 vol / 1 bind" {
				t.Errorf("db container = %+v", c)
			}
		case "no-rate":
			rate = true

			if !c.HasStats || c.CPU != "-" || c.NetRX != "-" || c.NetRxTotal != "1 B" {
				t.Errorf("no-rate container = %+v", c)
			}
		}
	}
	if !web || !db || !rate {
		t.Fatal("all three containers expected in the output")
	}

	if len(data.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(data.Images))
	}
	img := data.Images[0]
	if img.Name != "nginx" || img.Tag != "latest" || img.Usage != 1 || img.Containers != "web" || img.Update != "ok" {
		t.Errorf("image = %+v", img)
	}
	if img.RemoteVersion != ("sha256:" + validSha256Hex)[:16]+"…" {
		t.Errorf("image remote version = %q", img.RemoteVersion)
	}

	if len(data.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(data.Volumes))
	}
	if data.Volumes[0].Name != "pgdata" || data.Volumes[0].Size != "2.0 KiB" || data.Volumes[0].Containers != "db, db2" {
		t.Errorf("volume = %+v", data.Volumes[0])
	}
}

func TestDashboardDataEmpty(t *testing.T) {
	data := (&Metrics{Info: &Info{Hostname: "h", totalMemory: 0, numberCPU: 0}}).DashboardData()
	if data.Summary.CPU != "-" || data.Summary.NetRX != "-" || data.Summary.NetTX != "-" {
		t.Fatalf("empty summary must show dashes: %+v", data.Summary)
	}
	if len(data.Containers) != 0 || len(data.Images) != 0 || len(data.Volumes) != 0 {
		t.Fatal("empty metrics must produce no rows")
	}
}

func TestDashboardDataUpdateRequired(t *testing.T) {
	m := &Metrics{
		Info: &Info{Hostname: "h"},
		imageMetrics: []imageMetric{
			{id: "img1", name: "app", tag: "v2", registry: "reg", createdTime: 0, digest: "d", size: 10},
		},
		imageUpdateMetrics: []imageUpdateMetrics{
			{id: "img1", updateStatus: 1, remoteVersion: "", remoteTime: 0},
		},
		GetImageUpdateMetrics: true,
	}
	data := m.DashboardData()
	if len(data.Images) != 1 {
		t.Fatalf("expected 1 image")
	}
	if data.Images[0].Update != "update" || data.Images[0].RemoteVersion != "-" || data.Images[0].RemoteTime != "-" {
		t.Errorf("image update = %+v", data.Images[0])
	}
}

func TestDashboardDataNoUpdateInfo(t *testing.T) {
	m := &Metrics{
		Info: &Info{Hostname: "h"},
		imageMetrics: []imageMetric{
			{id: "img1", name: "app", tag: "v2", registry: "reg", size: 10},
		},
	}
	data := m.DashboardData()
	if len(data.Images) != 1 {
		t.Fatalf("expected 1 image")
	}
	if data.Images[0].Update != "unknown" || data.Images[0].RemoteVersion != "-" || data.Images[0].RemoteTime != "-" {
		t.Errorf("image without update info = %+v", data.Images[0])
	}
}

func TestPrometheusFormat(t *testing.T) {
	m := &Metrics{}
	out := m.prometheusFormat(
		"docker_test_metric", "Helps the test", "gauge",
		"id1", "name1", "running", "proj", "svc", "/wd",
		[]customLabelsKV{{"custom.k", "custom.v"}},
		"host-01", 42,
	)
	joined := strings.Join(out, "\n")
	for _, want := range []string{
		"# HELP docker_test_metric Helps the test",
		"# TYPE docker_test_metric gauge",
		`containerId="id1"`,
		`containerName="name1"`,
		`containerState="running"`,
		`composeProject="proj"`,
		`composeService="svc"`,
		`composeWorkDir="/wd"`,
		`"custom.k"="custom.v"`,
		`hostname="host-01"`,
		" 42",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("prometheusFormat output must contain %q", want)
		}
	}

	out2 := m.prometheusFormat("docker_test_metric", "", "", "id", "n", "s", "", "", "", nil, "host", 1)
	if len(out2) != 1 || strings.Contains(out2[0], "# HELP") {
		t.Fatalf("help/type lines must be omitted when empty: %v", out2)
	}
}

func TestPrometheusInspectMetrics(t *testing.T) {
	m := &Metrics{
		Labels: map[string]*Labels{
			"c1": {name: "web", state: "running", composeProject: "p", composeService: "s", composeWorkDir: "w", customLabelsKV: []customLabelsKV{{"a", "b"}}},
			"c2": {name: "db", state: "exited"},
			"c3": {name: "x", state: "exited"},
		},
		inspectMetrics: map[string]*InspectMetric{
			"c1": {startedTimestamp: 1, healthy: 1, exitCode: 2, oomKilled: 1, memoryLimit: 3, sizeRw: 4, sizeRootFs: 5, volumeMounts: 6, bindMounts: 7},
			"c2": {startedTimestamp: 0, healthy: 2, exitCode: 0, oomKilled: 0, memoryLimit: 0, sizeRw: 0, sizeRootFs: 0, volumeMounts: 0, bindMounts: 0},
		},
	}
	out := m.prometheusInspectMetrics("c1", "host-01")
	assertContains(t, out, `docker_container_status{`)
	assertContains(t, out, " 1")
	assertContains(t, out, "docker_container_healthy")
	assertContains(t, out, "docker_oom_killed")
	assertContains(t, out, "docker_volume_mounts")

	out2 := m.prometheusInspectMetrics("c2", "host-01")
	if hasLine(out2, "docker_container_healthy") {
		t.Fatal("healthy metric must be omitted when healthy==2 (no health check)")
	}
	if stateLineValue(out2) != "0" {
		t.Fatalf("stopped container status must be 0: %v", out2)
	}

	out3 := m.prometheusInspectMetrics("c3", "host-01")
	if len(out3) != 3 {
		t.Fatalf("without inspect data only the status lines are expected, got %v", out3)
	}
}

func TestPrometheusBaseMetrics(t *testing.T) {
	m := &Metrics{
		Labels: map[string]*Labels{
			"c1": {name: "web", state: "running"},
		},
		baseMetrics: map[string]*BaseMetrics{
			"c1": {id: "c1", cpuTotal: 1, cpuUser: 2, cpuKernel: 3, memoryLimit: 4, memUsageBytes: 5, netReceiveBytes: 6, netReceivePackets: 7, netTransmitBytes: 8, netTransmitPackets: 9, ioReadBytes: 10, ioWriteBytes: 11, pids: 12},
		},
	}
	out := m.prometheusBaseMetrics("c1", "host")
	for _, want := range []string{
		"docker_cpu_usage_total", "docker_cpu_usage_user", "docker_cpu_usage_kernel",
		"docker_memory_current_limit", "docker_memory_usage",
		"docker_network_received_bytes", "docker_network_received_packages",
		"docker_network_transmit_bytes", "docker_network_transmit_packages",
		"docker_io_read_bytes", "docker_io_write_bytes", "docker_process_pids_count",
	} {
		assertContains(t, out, want)
	}
	if got := m.prometheusBaseMetrics("missing", "host"); got != nil {
		t.Fatalf("nil base metric must yield nil, got %v", got)
	}
}

func hasLine(lines []string, name string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, name+"{") {
			return true
		}
	}
	return false
}

func stateLineValue(lines []string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, "docker_container_status{") {
			parts := strings.Split(l, "}")
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func TestGetDockerInfo(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/v1.41") != "/info" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"Name":"docker-host-1","IndexServerAddress":"https://registry.example.com","MemTotal":17179869184,"NCPU":8,"ContainersRunning":3,"ContainersStopped":2,"Images":7}`)
	}))
	m := &Metrics{}
	info := m.GetDockerInfo(context.Background(), dc)
	if info.Hostname != "docker-host-1" {
		t.Fatalf("hostname = %q", info.Hostname)
	}
	if info.defaultRegistry != "registry.example.com" {
		t.Fatalf("defaultRegistry = %q", info.defaultRegistry)
	}
	if info.numberCPU != 8 || info.totalMemory != 17179869184 || info.containersRunning != 3 || info.containersStopped != 2 || info.imageCount != 7 {
		t.Fatalf("info = %+v", info)
	}
}

func TestGetDockerInfoError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m := &Metrics{}
	info := m.GetDockerInfo(context.Background(), dc)
	oshn, _ := os.Hostname()
	if info.Hostname != oshn {
		t.Fatalf("on docker error the OS hostname is expected, got %q", info.Hostname)
	}
	if info.numberCPU != 0 {
		t.Fatalf("cpu must be zero on error, got %d", info.numberCPU)
	}
}

func getDockerMockRoutes(w http.ResponseWriter, r *http.Request, routes map[string]func(w http.ResponseWriter)) {
	path := strings.TrimPrefix(r.URL.Path, "/v1.41")
	if h, ok := routes[path]; ok {
		h(w)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func TestGetContainers(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getDockerMockRoutes(w, r, map[string]func(w http.ResponseWriter){
			"/containers/json": func(w http.ResponseWriter) {
				_, _ = io.WriteString(w, `[
					{"Id":"abc","Names":["/web"],"Image":"nginx","ImageID":"sha256:img1","Labels":{"com.docker.compose.project":"proj","com.docker.compose.service":"web","com.docker.compose.project.working_dir":"/app","custom.l1":"v1","custom.empty":""},"State":"running","Status":"Up 2 hours","Mounts":[{"Type":"volume","Name":"pgdata"}]},
					{"Id":"def","Names":["/db"],"Image":"postgres","ImageID":"sha256:img2","Labels":{},"State":"exited","Status":"Exited (0)","Mounts":[{"Type":"bind","Name":"","Source":"/s","Destination":"/d"}]}
				]`)
			},
		})
	}))
	m := &Metrics{CustomLabelsKeys: []string{"custom.l1", "custom.empty"}}
	labels, ids := m.getContainers(context.Background(), dc, true, testLogger())
	if len(labels) != 2 || len(ids) != 1 || ids[0] != "abc" {
		t.Fatalf("labels=%v ids=%v", labels, ids)
	}
	web := labels["abc"]
	if web.name != "web" || web.state != "running" || web.composeProject != "proj" || web.composeService != "web" || web.composeWorkDir != "/app" {
		t.Fatalf("web labels = %+v", web)
	}
	if len(web.customLabelsKV) != 1 || web.customLabelsKV[0].key != "custom.l1" {
		t.Fatalf("custom labels = %+v (empty values must be skipped)", web.customLabelsKV)
	}
	if got := m.imageUsage["sha256:img1"]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("imageUsage = %v", m.imageUsage)
	}
	if got := m.volumeUsage["pgdata"]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("volumeUsage = %v", m.volumeUsage)
	}
}

func TestGetContainersError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m := &Metrics{}
	labels, ids := m.getContainers(context.Background(), dc, true, testLogger())
	if labels != nil || ids != nil {
		t.Fatalf("on error both results must be nil, got %v / %v", labels, ids)
	}
}

func statsJSON(suffix int64) string {
	return fmt.Sprintf(`{
		"read":"2026-01-01T00:00:00Z",
		"cpu_stats":{"cpu_usage":{"total_usage":%d,"usage_in_usermode":%d,"usage_in_kernelmode":%d},"system_cpu_usage":0},
		"memory_stats":{"limit":8589934592,"usage":1073741824},
		"networks":{"eth0":{"rx_bytes":5000,"rx_packets":10,"tx_bytes":6000,"tx_packets":11},"bad-type":123,"bad-field":{"rx_bytes":"nope"}},
		"blkio_stats":{"io_service_bytes_recursive":[{"major":8,"minor":0,"op":"read","value":1024},{"major":8,"minor":0,"op":"write","value":2048},{"major":0,"minor":0,"op":"sync","value":0},{"major":0,"minor":0,"op":"meta"}]},
		"pids_stats":{"current":7}
	}`, 1200000000+suffix, 800000000+suffix, 400000000+suffix)
}

func TestGetBaseMetrics(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/stats") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, statsJSON(0))
	}))
	m := &Metrics{}
	bm := m.getBaseMetrics(context.Background(), dc, "abc", testLogger())
	if bm == nil {
		t.Fatal("expected base metrics")
	}
	if bm.id != "abc" || bm.cpuTotal != 1.2 || bm.cpuUser != 0.8 || bm.cpuKernel != 0.4 {
		t.Fatalf("cpu fields = %+v", bm)
	}
	if bm.memoryLimit != 8589934592 || bm.memUsageBytes != 1073741824 {
		t.Fatalf("memory fields = %+v", bm)
	}
	if bm.netReceiveBytes != 5000 || bm.netTransmitBytes != 6000 || bm.netReceivePackets != 10 || bm.netTransmitPackets != 11 {
		t.Fatalf("network fields = %+v", bm)
	}
	if bm.ioReadBytes != 1024 || bm.ioWriteBytes != 2048 || bm.pids != 7 {
		t.Fatalf("io/pids fields = %+v", bm)
	}
}

func TestGetBaseMetricsError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m := &Metrics{}
	if bm := m.getBaseMetrics(context.Background(), dc, "abc", testLogger()); bm != nil {
		t.Fatalf("on stats error nil is expected, got %+v", bm)
	}
}

func TestGetBaseMetricsMalformedJSON(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "this is not json {")
	}))
	m := &Metrics{}
	bm := m.getBaseMetrics(context.Background(), dc, "abc", testLogger())
	if bm == nil || bm.cpuTotal != 0 {
		t.Fatalf("malformed json must produce a zeroed structure, got %+v", bm)
	}
}

func TestGetBaseMetricsWrongTypes(t *testing.T) {
	roots := []string{
		`{"cpu_stats":5,"memory_stats":"x","networks":[],"blkio_stats":null,"pids_stats":"y"}`,
		`{"cpu_stats":{"cpu_usage":"bad"},"memory_stats":{},"networks":{"eth0":{"rx_bytes":"bad","tx_bytes":1,"rx_packets":true,"tx_packets":2},"other":3},"blkio_stats":{"io_service_bytes_recursive":[{"op":"read","value":"bad"},{"op":"noop"},5]},"pids_stats":{}}`,
	}
	for _, root := range roots {
		dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, root)
		}))
		m := &Metrics{}
		bm := m.getBaseMetrics(context.Background(), dc, "abc", testLogger())
		if bm == nil || bm.id != "abc" {
			t.Fatalf("wrong types must yield a zeroed structure, got %+v", bm)
		}
		if bm.cpuTotal != 0 || bm.memUsageBytes != 0 || bm.ioWriteBytes != 0 {
			t.Fatalf("values with wrong types must be skipped, got %+v", bm)
		}
	}
}

func inspectJSON(id, state, startedAt string, healthy *string, oomKilled bool, exitCode int) string {
	health := ""
	if healthy != nil {
		health = `,"Health":{"Status":"` + *healthy + `","FailingStreak":0}`
	}
	return `{
		"Id":"` + id + `",
		"State":{"Status":"` + state + `","Running":` + boolStr(state == "running") + `,"ExitCode":` + fmt.Sprint(exitCode) + `,"OOMKilled":` + boolStr(oomKilled) + `,"StartedAt":"` + startedAt + `","FinishedAt":"0001-01-01T00:00:00Z"` + health + `},
		"HostConfig":{"Memory":4294967296},
		"SizeRw":123,
		"SizeRootFs":456,
		"Mounts":[{"Type":"volume","Name":"vol1","Driver":"local","Source":"/v","Destination":"/data","RW":true},{"Type":"bind","Driver":"","Source":"/src","Destination":"/dst","RW":true}]
	}`
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func inspectDockerClient(t *testing.T, byID map[string]func(w http.ResponseWriter)) *client.Client {
	return metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.41")
		if h, ok := byID[path]; ok {
			h(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestGetInspectMetrics(t *testing.T) {
	healthy := "healthy"
	unhealthy := "unhealthy"
	dc := inspectDockerClient(t, map[string]func(w http.ResponseWriter){
		"/containers/c1/json": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, inspectJSON("c1", "running", "2026-01-01T00:00:00Z", &healthy, true, 0))
		},
		"/containers/c2/json": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, inspectJSON("c2", "exited", "2026-01-02T00:00:00Z", &unhealthy, false, 137))
		},
		"/containers/c3/json": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, inspectJSON("c3", "running", "2026-01-03T00:00:00Z", nil, false, 0))
		},
		"/containers/c4/json": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, inspectJSON("c4", "running", "not-a-time", nil, false, 0))
		},
		"/containers/c5/json": func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
	})

	m := &Metrics{}
	ctx := context.Background()
	logger := testLogger()

	collect := func(id string) *InspectMetric {
		var wg sync.WaitGroup
		results := make(chan *InspectMetric, 1)
		wg.Add(1)
		m.getInspectMetrics(ctx, dc, id, &wg, results, logger)
		wg.Wait()
		select {
		case r := <-results:
			return r
		default:
			return nil
		}
	}

	c1 := collect("c1")
	if c1 == nil || c1.healthy != 1 || c1.oomKilled != 1 || c1.status != "running" || c1.volumeMounts != 1 || c1.bindMounts != 1 || c1.sizeRw != 123 || c1.memoryLimit != 4294967296 {
		t.Fatalf("c1 inspect = %+v", c1)
	}

	c2 := collect("c2")
	if c2 == nil || c2.healthy != 0 || c2.oomKilled != 0 || c2.exitCode != 137 {
		t.Fatalf("c2 inspect = %+v (healthy=0 for unhealthy, no oom)", c2)
	}

	c3 := collect("c3")
	if c3 == nil || c3.healthy != 2 {
		t.Fatalf("c3 must have healthy==2 (no health check), got %+v", c3)
	}

	if c4 := collect("c4"); c4 != nil {
		t.Fatalf("unparseable StartedAt must produce no metric, got %+v", c4)
	}
	if c5 := collect("c5"); c5 != nil {
		t.Fatalf("inspect error must produce no metric, got %+v", c5)
	}
}

func TestGetImagesMetrics(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/images/json") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `[
			{"Id":"sha256:img1","RepoTags":["nginx:latest"],"RepoDigests":["docker.io/library/nginx@sha256:abc123"],"Created":1700000000,"Size":100,"SharedSize":30},
			{"Id":"sha256:img2","RepoTags":["127.0.0.1:1/foo@sha256:`+validSha256Hex+`"],"RepoDigests":[],"Created":0,"Size":50,"SharedSize":0},
			{"Id":"sha256:img3","RepoTags":["INVALID###"],"RepoDigests":[],"Created":0,"Size":5,"SharedSize":234},
			{"Id":"sha256:img4","RepoTags":[],"RepoDigests":[],"Created":0,"Size":7,"SharedSize":0}
		]`)
	}))
	m := &Metrics{Info: &Info{defaultRegistry: "docker.io"}}
	imgs, err := m.getImagesMetrics(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(imgs) != 4 {
		t.Fatalf("expected 4 images, got %d", len(imgs))
	}
	if imgs[0].name != "nginx" || imgs[0].tag != "latest" || imgs[0].registry != "docker.io" || imgs[0].digest != "abc123" || imgs[0].size != 70 {
		t.Fatalf("img1 = %+v", imgs[0])
	}
	if imgs[1].tag != "sha256:"+validSha256Hex || imgs[1].registry != "127.0.0.1:1" || imgs[1].name != "foo" || imgs[1].digest != "img2" {
		t.Fatalf("img2 = %+v", imgs[1])
	}
	if imgs[2].name != "INVALID###" || imgs[2].tag != "latest" || imgs[2].registry != "docker.io" || imgs[2].size != -229 {
		t.Fatalf("img3 = %+v", imgs[2])
	}
	if imgs[3].name != "none" || imgs[3].tag != "latest" {
		t.Fatalf("img4 = %+v", imgs[3])
	}
}

func TestGetImagesMetricsError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m := &Metrics{Info: &Info{}}
	if _, err := m.getImagesMetrics(context.Background(), dc); err == nil {
		t.Fatal("expected an error")
	}
}

func TestGetVolumesMetrics(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/system/df") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"Volumes":[{"Name":"pgdata","Driver":"local","UsageData":{"Size":2048,"RefCount":2}},{"Name":"tmp","Driver":"tmpfs","UsageData":null}]}`)
	}))
	m := &Metrics{}
	vols, err := m.getVolumesMetrics(context.Background(), dc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vols) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(vols))
	}
	if vols[0].name != "pgdata" || vols[0].size != 2048 || vols[0].usage != 2 || vols[0].driver != "local" {
		t.Fatalf("vol 0 = %+v", vols[0])
	}
	if vols[1].size != 0 || vols[1].usage != 0 {
		t.Fatalf("volume without UsageData must be zeroed: %+v", vols[1])
	}
}

func TestGetVolumesMetricsError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m := &Metrics{}
	if _, err := m.getVolumesMetrics(context.Background(), dc); err == nil {
		t.Fatal("expected an error")
	}
}

func TestVolumesMetricsWorker(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Volumes":[{"Name":"pgdata","Driver":"local","UsageData":{"Size":2048,"RefCount":2}}]}`)
	}))
	m := &Metrics{}
	m.VolumesMetricsWorker(dc, testLogger())
	if len(m.volumeMetrics) != 1 || m.volumeMetrics[0].name != "pgdata" {
		t.Fatalf("volume metrics = %v", m.volumeMetrics)
	}

	dcErr := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m2 := &Metrics{}
	m2.VolumesMetricsWorker(dcErr, testLogger())
	if m2.volumeMetrics != nil {
		t.Fatalf("on error volumeMetrics must be nil, got %v", m2.volumeMetrics)
	}
}

func TestImageMetricsWorkerWithExistingImageMetrics(t *testing.T) {
	m := &Metrics{
		imageMetrics: []imageMetric{{id: "i1", fullName: "none"}},
	}
	m.ImageMetricsWorker(nil, testLogger())
	if len(m.imageUpdateMetrics) != 0 {
		t.Fatalf("existing images must skip the ImageList fetch, got %v", m.imageUpdateMetrics)
	}
}

func TestImageMetricsWorkerWithUpdates(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(strings.TrimPrefix(r.URL.Path, "/v1.41"), "/distribution/") {
			_, _ = io.WriteString(w, `{"Descriptor":{"digest":"sha256:remotever"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	m := &Metrics{
		imageMetrics: []imageMetric{
			{id: "sha256:img1", fullName: "127.0.0.1:1/foo:latest", name: "127.0.0.1:1/foo", tag: "latest", registry: "127.0.0.1:1", digest: "cur", size: 1},
		},
	}
	m.ImageMetricsWorker(dc, testLogger())
	if len(m.imageUpdateMetrics) != 1 || m.imageUpdateMetrics[0].updateStatus != 1 {
		t.Fatalf("worker must count an available update, got %v", m.imageUpdateMetrics)
	}
}

func TestGetMetricsIntegration(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.41")
		switch path {
		case "/info":
			_, _ = io.WriteString(w, `{"Name":"host-01","IndexServerAddress":"https://registry.example.com","MemTotal":17179869184,"NCPU":8,"ContainersRunning":1,"ContainersStopped":1,"Images":1}`)
		case "/containers/json":
			_, _ = io.WriteString(w, `[
				{"Id":"abc","Names":["/web"],"Image":"nginx","ImageID":"sha256:img1","Labels":{"com.docker.compose.project":"proj","com.docker.compose.service":"web","custom.l":"v"},"State":"running","Status":"Up 2 hours"},
				{"Id":"def","Names":["/db"],"Image":"postgres","ImageID":"sha256:img2","Labels":{},"State":"exited","Status":"Exited (0)"}
			]`)
		case "/containers/abc/stats":
			_, _ = io.WriteString(w, statsJSON(0))
		case "/containers/abc/json":
			_, _ = io.WriteString(w, inspectJSON("abc", "running", "2026-01-01T00:00:00Z", &healthyStr, false, 0))
		case "/containers/def/json":
			_, _ = io.WriteString(w, inspectJSON("def", "exited", "2026-01-02T00:00:00Z", nil, true, 137))
		case "/images/json":
			_, _ = io.WriteString(w, `[{"Id":"sha256:img1","RepoTags":["nginx:latest"],"RepoDigests":["docker.io/library/nginx@sha256:abc123"],"Created":1700000000,"Size":100,"SharedSize":30}]`)
		case "/system/df":
			_, _ = io.WriteString(w, `{"Volumes":[{"Name":"pgdata","Driver":"local","UsageData":{"Size":2048,"RefCount":1}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	m := &Metrics{CustomLabelsKeys: []string{"custom.l"}}
	m.Info = m.GetDockerInfo(context.Background(), dc)
	ctx := context.Background()
	logger := testLogger()

	data := m.GetMetrics(ctx, dc, "host-01", logger)
	if len(m.baseMetrics) != 1 {
		t.Fatalf("expected 1 running container with base metrics, got %d", len(m.baseMetrics))
	}
	if len(m.inspectMetrics) != 2 {
		t.Fatalf("expected 2 inspect metrics, got %d", len(m.inspectMetrics))
	}
	if len(m.imageMetrics) != 1 || m.imageMetrics[0].name != "nginx" {
		t.Fatalf("image metrics = %v", m.imageMetrics)
	}
	assertContains(t, data, "docker_cpu_total_number")
	assertContains(t, data, "docker_image_size")
	assertContains(t, data, `hostname="host-01"`)
	assertContains(t, data, `containerName="web"`)

	m.cpuPreviousTime = m.cpuCurrentTime
	_ = m.GetMetrics(ctx, dc, "host-01", logger)
	if len(m.previousMetrics) != 1 {
		t.Fatalf("previous metrics must be derived from the first fetch, got %d", len(m.previousMetrics))
	}
}

func TestGetMetricsContainerListError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.41")
		switch path {
		case "/containers/json", "/images/json":
			w.WriteHeader(http.StatusInternalServerError)
		case "/info":
			_, _ = io.WriteString(w, `{"Name":"host"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	m := &Metrics{}
	m.Info = m.GetDockerInfo(context.Background(), dc)
	data := m.GetMetrics(context.Background(), dc, "host", testLogger())
	assertContains(t, data, "docker_cpu_total_number")
	if m.Labels != nil {
		t.Fatalf("labels must be nil on container list error, got %v", m.Labels)
	}
}

func TestGetMetricsUpdateAndVolumeBlocks(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.41")
		switch path {
		case "/containers/json":
			_, _ = io.WriteString(w, `[]`)
		case "/images/json":
			_, _ = io.WriteString(w, `[{"Id":"sha256:img1","RepoTags":["nginx:latest"],"RepoDigests":["docker.io/library/nginx@sha256:abc"],"Created":1700000000,"Size":100,"SharedSize":0}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	m := &Metrics{
		imageUpdateMetrics: []imageUpdateMetrics{
			{id: "sha256:img1", name: "nginx", tag: "latest", registry: "docker.io", createdTime: 0, remoteTime: 1700000000, digest: "abc", remoteVersion: "sha256:new", updateStatus: 1},
		},
		volumeMetrics: []volumeMetric{
			{name: "pgdata", driver: "local", size: 2048, usage: 2},
			{name: "empty", driver: "tmpfs", size: 0, usage: 0},
		},
	}
	m.Info = &Info{defaultRegistry: "docker.io", numberCPU: 4, totalMemory: 1 << 30}
	m.GetImageUpdateMetrics = true
	m.GetVolumeMetrics = true
	data := m.GetMetrics(context.Background(), dc, "host-01", testLogger())
	assertContains(t, data, "# HELP docker_image_update")
	assertContains(t, data, `remoteTime="1700000000"`)
	assertContains(t, data, " 1")
	assertContains(t, data, "# HELP docker_volume_size")
	assertContains(t, data, `volumeName="pgdata"`)
	assertContains(t, data, `volumeName="empty"`)
}

func TestImageMetricsWorkerError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m := &Metrics{}
	m.ImageMetricsWorker(dc, testLogger())
	if m.imageUpdateMetrics != nil {
		t.Fatalf("update metrics must be nil when no images are collected, got %v", m.imageUpdateMetrics)
	}
}

func TestImageMetricsWorkerNoUpdates(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/images/json") {
			_, _ = io.WriteString(w, `[{"Id":"sha256:img1","RepoTags":[],"Created":0,"Size":1,"SharedSize":0}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	m := &Metrics{Info: &Info{}}
	m.ImageMetricsWorker(dc, testLogger())
	if len(m.imageMetrics) != 1 || m.imageMetrics[0].fullName != "none" {
		t.Fatalf("image metrics = %v", m.imageMetrics)
	}
	if len(m.imageUpdateMetrics) != 0 {
		t.Fatalf("images named none must be skipped, got %v", m.imageUpdateMetrics)
	}
}

func TestGetImagesUpdateMetricsWithDigestCheck(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.41")
		switch {
		case strings.Contains(path, "/distribution/"):
			_, _ = io.WriteString(w, `{"Descriptor":{"digest":"sha256:remotever"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	m := &Metrics{
		imageMetrics: []imageMetric{
			{id: "sha256:img1", fullName: "127.0.0.1:1/foo:latest", name: "127.0.0.1:1/foo", tag: "latest", registry: "127.0.0.1:1", digest: "cur", size: 10},
		},
	}
	got := m.getImagesUpdateMetrics(dc, testLogger())
	if len(got) != 1 {
		t.Fatalf("expected 1 update metric, got %v", got)
	}
	if got[0].updateStatus != 1 {
		t.Fatalf("updateStatus = %d, want 1", got[0].updateStatus)
	}
	if got[0].remoteVersion != "remotever" {
		t.Fatalf("remoteVersion = %q", got[0].remoteVersion)
	}
}

func TestGetImagesUpdateMetricsEmpty(t *testing.T) {
	m := &Metrics{}
	if got := m.getImagesUpdateMetrics(nil, testLogger()); got != nil {
		t.Fatalf("empty image list must return nil, got %v", got)
	}
}

func TestGetImagesUpdateMetricsSkipsNone(t *testing.T) {
	m := &Metrics{
		imageMetrics: []imageMetric{
			{id: "i1", fullName: "none"},
			{id: "i2", fullName: "none"},
		},
	}
	got := m.getImagesUpdateMetrics(nil, testLogger())
	if len(got) != 0 {
		t.Fatalf("images named none must be skipped, got %v", got)
	}
}

func TestGetImagesUpdateMetricsDigestError(t *testing.T) {
	dc := metricsClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(strings.TrimPrefix(r.URL.Path, "/v1.41"), "/distribution/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	m := &Metrics{
		imageMetrics: []imageMetric{
			{id: "i1", fullName: "127.0.0.1:1/foo:latest", name: "127.0.0.1:1/foo", tag: "latest", registry: "127.0.0.1:1", digest: "cur", size: 1},
		},
	}
	got := m.getImagesUpdateMetrics(dc, testLogger())
	if len(got) != 0 {
		t.Fatalf("image with a failing distribution inspect must be dropped, got %v", got)
	}
}

func TestGetRemoteCreatedTimeParseError(t *testing.T) {
	if got := getRemoteCreatedTime(context.Background(), "!!!", "digest", false, testLogger()); got != 0 {
		t.Fatalf("unparseable reference must return 0, got %d", got)
	}
}

func TestGetRemoteCreatedTimeRemoteUnreachable(t *testing.T) {

	digest := strings.Repeat("ab", 32)
	got := getRemoteCreatedTime(context.Background(), "127.0.0.1:1/foo:latest", digest, false, testLogger())
	if got != 0 {
		t.Fatalf("unreachable registry must return 0, got %d", got)
	}
}

var healthyStr = "healthy"
