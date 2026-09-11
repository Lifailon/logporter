package dashboard

import (
	"bytes"
	"embed"
	"html/template"
)

//go:embed dashboard.tmpl
var templateFS embed.FS

var tmpl = template.Must(template.New("dashboard.tmpl").ParseFS(templateFS, "dashboard.tmpl"))

type Data struct {
	Summary         Summary
	Containers      []Container
	ContainerGroups []ContainerGroup
	Images          []Image
	Volumes         []Volume
}

type ContainerGroup struct {
	Name       string
	Containers []Container
}

type Summary struct {
	Title       string
	Hostname    string
	MemoryTotal string
	MemoryUsed  string
	ImagesSize  string
	VolumesSize string
	NumberCPU   int
	Images      int
	Running     int
	Stopped     int
	Updates     int
	Volumes     int
	ShowUpdates bool
	ShowVolumes bool
}

type Container struct {
	ID             string
	Name           string
	State          string
	Status         string
	Compose        string
	ComposeProject string
	ComposeService string
	CPU            string
	Memory         string
	NetRx          string
	NetTx          string
	IORead         string
	IOWrite        string
	PIDs           string
	Healthy        string
	ExitCode       string
	Mounts         string
	HasStats       bool
}

func (d Container) StateClass() string {
	switch d.State {
	case "running":
		return "badge-running"
	case "exited":
		return "badge-exited"
	case "created":
		return "badge-created"
	case "restarting":
		return "badge-restarting"
	case "paused":
		return "badge-paused"
	case "dead":
		return "badge-dead"
	default:
		return "badge-other"
	}
}

func (d Container) ShortID() string {
	if len(d.ID) > 12 {
		return d.ID[:12] + "…"
	}
	return d.ID
}

func (d Container) HealthyClass() string {
	switch d.Healthy {
	case "healthy":
		return "badge-ok"
	case "unhealthy":
		return "badge-update"
	default:
		return ""
	}
}

type Image struct {
	Name          string
	Tag           string
	Registry      string
	Created       string
	Size          string
	Usage         int
	Containers    string
	Update        string
	RemoteVersion string
	RemoteTime    string
}

func (d Image) UpdateClass() string {
	switch d.Update {
	case "update":
		return "badge-update"
	case "ok":
		return "badge-ok"
	default:
		return "badge-other"
	}
}

func (d Image) UpdateLabel() string {
	switch d.Update {
	case "update":
		return "update available"
	case "ok":
		return "up to date"
	default:
		return "unknown"
	}
}

type Volume struct {
	Name       string
	Driver     string
	Size       string
	Usage      int64
	Containers string
}

func Render(data Data) (template.HTML, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func Refresh(data Data) (map[string]template.HTML, error) {
	fragments := map[string]string{
		"cards":      "cards",
		"containers": "rowsContainers",
		"images":     "rowsImages",
		"volumes":    "rowsVolumes",
	}
	out := make(map[string]template.HTML, len(fragments))
	for key, name := range fragments {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
			return nil, err
		}
		out[key] = template.HTML(buf.String())
	}
	return out, nil
}
