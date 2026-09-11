package metrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"logporter/internal/dashboard"
)

func (m *Metrics) DashboardData() dashboard.Data {
	running, stopped := 0, 0
	var memoryUsed, imagesSize, volumesSize int64
	for _, l := range m.Labels {
		if l != nil {
			if l.state == "running" {
				running++
			} else {
				stopped++
			}
		}
	}
	for _, bm := range m.baseMetrics {
		if bm != nil {
			memoryUsed += int64(bm.memUsageBytes)
		}
	}
	for _, img := range m.imageMetrics {
		imagesSize += int64(img.size)
	}
	for _, v := range m.volumeMetrics {
		volumesSize += v.size
	}

	summary := dashboard.Summary{
		Title:       "logporter - " + m.Info.Hostname,
		Hostname:    m.Info.Hostname,
		MemoryTotal: humanBytes(m.Info.totalMemory),
		MemoryUsed:  humanBytes(memoryUsed),
		ImagesSize:  humanBytes(imagesSize),
		VolumesSize: humanBytes(volumesSize),
		NumberCPU:   m.Info.numberCPU,
		Images:      len(m.imageMetrics),
		Running:     running,
		Stopped:     stopped,
		Volumes:     len(m.volumeMetrics),
		Updates:     updateCount(m.imageUpdateMetrics),
		ShowUpdates: m.GetImageUpdateMetrics,
		ShowVolumes: m.GetVolumeMetrics,
	}

	containers := make([]dashboard.Container, 0, len(m.Labels))
	for id, l := range m.Labels {
		if l == nil {
			continue
		}
		c := dashboard.Container{ID: id, Name: l.name, State: l.state, Status: cleanStatus(l.status), Compose: composeName(l), ComposeProject: l.composeProject, ComposeService: l.composeService}
		if bm := m.baseMetrics[id]; bm != nil {
			c.CPU = humanDuration(bm.cpuTotal)
			c.Memory = humanBytes(int64(bm.memUsageBytes))
			c.NetRx = humanBytes(int64(bm.netReceiveBytes))
			c.NetTx = humanBytes(int64(bm.netTransmitBytes))
			c.IORead = humanBytes(int64(bm.ioReadBytes))
			c.IOWrite = humanBytes(int64(bm.ioWriteBytes))
			c.PIDs = strconv.Itoa(bm.pids)
			c.HasStats = true
		}
		if in := m.inspectMetrics[id]; in != nil {
			switch in.healthy {
			case 1:
				c.Healthy = "healthy"
			case 0:
				c.Healthy = "unhealthy"
			}
			c.ExitCode = strconv.Itoa(in.exitCode)
			c.Mounts = strconv.Itoa(in.volumeMounts) + " vol / " + strconv.Itoa(in.bindMounts) + " bind"
		}
		containers = append(containers, c)
	}

	updates := make(map[string]imageUpdateMetrics, len(m.imageUpdateMetrics))
	for _, u := range m.imageUpdateMetrics {
		updates[u.id] = u
	}

	images := make([]dashboard.Image, 0, len(m.imageMetrics))
	for _, img := range m.imageMetrics {
		im := dashboard.Image{
			Name: img.name, Tag: img.tag, Registry: img.registry,
			Created: ts(img.createdTime), Size: humanBytes(int64(img.size)),
			Usage:      len(m.imageUsage[img.id]),
			Containers: strings.Join(m.imageUsage[img.id], ", "),
		}
		if u, ok := updates[img.id]; ok {
			if u.updateStatus == 1 {
				im.Update = "update"
			} else {
				im.Update = "ok"
			}
			im.RemoteVersion = shortDigest(u.remoteVersion)
			im.RemoteTime = ts(u.remoteTime)
		} else {
			im.Update = "unknown"
			im.RemoteVersion, im.RemoteTime = "-", "-"
		}
		images = append(images, im)
	}

	volumes := make([]dashboard.Volume, 0, len(m.volumeMetrics))
	for _, v := range m.volumeMetrics {
		volumes = append(volumes, dashboard.Volume{
			Name: v.name, Driver: v.driver, Size: humanBytes(v.size), Usage: v.usage,
			Containers: strings.Join(m.volumeUsage[v.name], ", "),
		})
	}

	return dashboard.Data{Summary: summary, Containers: containers, ContainerGroups: groupContainers(containers), Images: images, Volumes: volumes}
}

func groupContainers(containers []dashboard.Container) []dashboard.ContainerGroup {
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	byProject := make(map[string][]dashboard.Container)
	var projects []string
	for _, c := range containers {
		if _, ok := byProject[c.ComposeProject]; !ok {
			projects = append(projects, c.ComposeProject)
		}
		byProject[c.ComposeProject] = append(byProject[c.ComposeProject], c)
	}
	sort.Strings(projects)
	var out []dashboard.ContainerGroup
	for _, p := range projects {
		if p == "" {
			continue
		}
		out = append(out, dashboard.ContainerGroup{Name: p, Containers: byProject[p]})
	}
	if len(byProject[""]) > 0 {
		out = append(out, dashboard.ContainerGroup{Name: "", Containers: byProject[""]})
	}
	return out
}

func cleanStatus(status string) string {
	for _, s := range []string{" (healthy)", " (unhealthy)", " (health: starting)"} {
		if strings.HasSuffix(status, s) {
			return status[:len(status)-len(s)]
		}
	}
	return status
}

func composeName(l *Labels) string {
	switch {
	case l.composeService != "" && l.composeProject != "":
		return l.composeProject + "/" + l.composeService
	case l.composeService != "":
		return l.composeService
	default:
		return l.composeProject
	}
}

func updateCount(metrics []imageUpdateMetrics) int {
	count := 0
	for _, u := range metrics {
		if u.updateStatus == 1 {
			count++
		}
	}
	return count
}

func shortDigest(digest string) string {
	if digest == "" {
		return "-"
	}
	if len(digest) > 16 {
		return digest[:16] + "…"
	}
	return digest
}

func ts(secs int64) string {
	if secs == 0 {
		return "-"
	}
	return time.Unix(secs, 0).Format("2006-01-02 15:04")
}

func humanBytes(v int64) string {
	const unit = int64(1024)
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := unit, 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
}

func humanDuration(seconds float64) string {
	if seconds < 1 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	if seconds < 60 {
		return fmt.Sprintf("%.0fs", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%.1fm", seconds/60)
	}
	return fmt.Sprintf("%.2fh", seconds/3600)
}
