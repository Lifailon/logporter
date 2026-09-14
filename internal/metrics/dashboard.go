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
		CPU:         cpuPercentString(m.cpuHostPerc()),
	}

	containers := make([]dashboard.Container, 0, len(m.Labels))
	var netRX, netTX, ioRead, ioWrite float64
	netStatsReady := false
	for id, l := range m.Labels {
		if l == nil {
			continue
		}
		c := dashboard.Container{ID: id, Name: l.name, State: l.state, Status: cleanStatus(l.status), Compose: composeName(l), ComposeProject: l.composeProject, ComposeService: l.composeService}
		if bm := m.baseMetrics[id]; bm != nil {
			c.CPU = cpuPercentString(m.cpuContainerPerc(id, bm.cpuTotal))
			c.CPUTotal = humanDuration(bm.cpuTotal)
			c.Memory = humanBytes(int64(bm.memUsageBytes))
			if rx, tx, rd, wr, ok := m.containerNSRate(id, int64(bm.netReceiveBytes), int64(bm.netTransmitBytes), int64(bm.ioReadBytes), int64(bm.ioWriteBytes)); ok {
				c.NetRX = humanBytesPerSec(rx)
				c.NetTX = humanBytesPerSec(tx)
				c.IORead = humanBytesPerSec(rd)
				c.IOWrite = humanBytesPerSec(wr)
				netRX += rx
				netTX += tx
				ioRead += rd
				ioWrite += wr
				netStatsReady = true
			} else {
				c.NetRX, c.NetTX, c.IORead, c.IOWrite = "-", "-", "-", "-"
			}
			c.NetRxTotal = humanBytes(int64(bm.netReceiveBytes))
			c.NetTxTotal = humanBytes(int64(bm.netTransmitBytes))
			c.IOReadTotal = humanBytes(int64(bm.ioReadBytes))
			c.IOWriteTotal = humanBytes(int64(bm.ioWriteBytes))
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

	if netStatsReady {
		summary.NetRX = humanBytesInt(int64(netRX))
		summary.NetTX = humanBytesInt(int64(netTX))
		summary.IORead = humanBytesInt(int64(ioRead))
		summary.IOWrite = humanBytesInt(int64(ioWrite))
	} else {
		summary.NetRX, summary.NetTX, summary.IORead, summary.IOWrite = "-", "-", "-", "-"
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

func humanBytesInt(v int64) string {
	const unit = int64(1024)
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := unit, 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
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

func (m *Metrics) cpuContainerPerc(id string, curCPU float64) (float64, bool) {
	prev, ok := m.previousMetrics[id]
	if !ok {
		return 0, false
	}
	interval := m.cpuCurrentTime.Sub(m.cpuPreviousTime)
	if interval <= 0 {
		return 0, false
	}
	delta := curCPU - prev.cpu
	if delta < 0 {
		return 0, false
	}
	return delta / interval.Seconds() * 100, true
}

func (m *Metrics) cpuHostPerc() (float64, bool) {
	if m.Info.numberCPU <= 0 {
		return 0, false
	}
	var delta float64
	for id, bm := range m.baseMetrics {
		if bm == nil {
			continue
		}
		prev, ok := m.previousMetrics[id]
		if !ok {
			continue
		}
		d := bm.cpuTotal - prev.cpu
		if d < 0 {
			continue
		}
		delta += d
	}
	if delta <= 0 {
		return 0, false
	}
	interval := m.cpuCurrentTime.Sub(m.cpuPreviousTime)
	if interval <= 0 {
		return 0, false
	}
	return delta / interval.Seconds() / float64(m.Info.numberCPU) * 100, true
}

func cpuPercentString(p float64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.1f %%", p)
}

func (m *Metrics) containerNSRate(id string, curRX, curTX, curRead, curWrite int64) (float64, float64, float64, float64, bool) {
	prev, ok := m.previousMetrics[id]
	if !ok {
		return 0, 0, 0, 0, false
	}
	interval := m.cpuCurrentTime.Sub(m.cpuPreviousTime).Seconds()
	if interval <= 0 {
		return 0, 0, 0, 0, false
	}
	rx := float64(curRX-prev.netRX) / interval
	tx := float64(curTX-prev.netTX) / interval
	rd := float64(curRead-prev.ioRead) / interval
	wr := float64(curWrite-prev.ioWrite) / interval
	if rx < 0 || tx < 0 || rd < 0 || wr < 0 {
		return 0, 0, 0, 0, false
	}
	return rx, tx, rd, wr, true
}

func humanBytesPerSec(bps float64) string {
	return humanBytes(int64(bps)) + "/s"
}
