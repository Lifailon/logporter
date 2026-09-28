package dashboard

import (
	"html/template"
	"strings"
	"testing"
)

func TestRenderInjectsEmbeddedJS(t *testing.T) {
	h, err := Render(Data{Summary: Summary{Title: "logPorter test", Hostname: "host-01"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(h)
	if !strings.Contains(s, "<title>logPorter test</title>") {
		t.Error("page must render the <title> from Summary.Title")
	}
	if strings.Contains(s, "{{.Script}}") {
		t.Error("template action {{.Script}} must be executed")
	}
	if !strings.Contains(s, `var uiKey = "docker-exporter-ui"`) {
		t.Error("embedded dashboard.js must be injected into the page")
	}
	if !strings.Contains(s, `document.addEventListener("mouseout"`) {
		t.Error("dashboard.js tail (tooltip code) must be injected into the page")
	}
}

func TestRenderAuthAndLogo(t *testing.T) {
	h, err := Render(Data{Summary: Summary{Title: "t"}, Auth: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(h)
	if !strings.Contains(s, `<h1><span class="logo-w">log</span><span class="logo-p">Porter</span></h1>`) {
		t.Error("header logo spans expected")
	}
	if !strings.Contains(s, `href="/logout"`) {
		t.Error("logout link expected when Auth is true")
	}
	h2, err := Render(Data{Summary: Summary{Title: "t"}, Auth: false})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(h2), `href="/logout"`) {
		t.Error("logout link must be absent when Auth is false")
	}
}

func TestRefreshFragments(t *testing.T) {
	data := Data{
		Summary: Summary{
			Title:       "logPorter test",
			Hostname:    "host-01",
			MemoryTotal: "16GiB",
			MemoryUsed:  "8.2GiB",
			ImagesSize:  "1.5GB",
			VolumesSize: "2.3GB",
			NumberCPU:   8,
			Images:      3,
			Running:     2,
			Stopped:     1,
			Updates:     5,
			Volumes:     2,
			ShowUpdates: true,
			ShowVolumes: true,
			CPU:         "12.5%",
			NetRX:       "1.1MB/s",
			NetTX:       "2.2MB/s",
			IORead:      "3.3MB",
			IOWrite:     "4.4MB",
		},
		ContainerGroups: []ContainerGroup{
			{
				Name: "proj",
				Containers: []Container{
					{
						ID:             "abcdef1234567890",
						Name:           "web",
						State:          "running",
						Status:         "Up 2 hours",
						ComposeProject: "proj",
						ComposeService: "web",
						HasStats:       true,
						CPUTotal:       "10.0%",
						Memory:         "256MiB",
						NetRX:          "1MB",
						NetTX:          "2MB",
						IORead:         "3MB",
						IOWrite:        "4MB",
						PIDs:           "12",
						Healthy:        "healthy",
						Mounts:         "/data",
					},
				},
			},
		},
		Images: []Image{
			{Name: "nginx:latest", Tag: "latest", Registry: "docker.io", Created: "2026-01-01", Size: "150MB", Usage: 1, Containers: "1", Update: "update"},
		},
		Volumes: []Volume{
			{Name: "pgdata", Driver: "local", Size: "2.3GB", Usage: 1, Containers: "1"},
		},
	}

	frags, err := Refresh(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cards", "containers", "images", "volumes"} {
		if frags[k] == "" {
			t.Errorf("fragment %q must be rendered", k)
		}
	}

	cards := string(frags["cards"])
	for _, want := range []string{"host-01", "CPU load of 8 cores", "12.5%", "8.2GiB / 16GiB", "1.1MB/s / 2.2MB/s", "2.3GB"} {
		if !strings.Contains(cards, want) {
			t.Errorf("cards fragment must contain %q", want)
		}
	}

	rows := string(frags["containers"])
	for _, want := range []string{"proj", "web", "badge-running", "Up 2 hours", "abcdef123456…", "badge-ok"} {
		if !strings.Contains(rows, want) {
			t.Errorf("containers fragment must contain %q", want)
		}
	}

	images := string(frags["images"])
	for _, want := range []string{"nginx:latest", "latest", "docker.io", "150MB", "update available", "badge-update"} {
		if !strings.Contains(images, want) {
			t.Errorf("images fragment must contain %q", want)
		}
	}

	volumes := string(frags["volumes"])
	for _, want := range []string{"pgdata", "local", "2.3GB"} {
		if !strings.Contains(volumes, want) {
			t.Errorf("volumes fragment must contain %q", want)
		}
	}
}

func TestRefreshEmpty(t *testing.T) {
	frags, err := Refresh(Data{})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cards", "containers", "images", "volumes"} {
		if _, ok := frags[k]; !ok {
			t.Errorf("fragment %q must be present even with empty data", k)
		}
	}
}

func TestRefreshTemplateError(t *testing.T) {
	orig := tmpl
	defer func() { tmpl = orig }()

	tmpl = template.Must(template.New("broken").Parse(`{{define "cards"}}ok{{end}}`))
	if _, err := Refresh(Data{}); err == nil {
		t.Fatal("Refresh must return an error when a fragment is undefined")
	}
}

func TestRenderTemplateError(t *testing.T) {
	orig := tmpl
	defer func() { tmpl = orig }()
	tmpl = template.Must(template.New("report").Parse(`{{.Summary.Nope}}`))
	if _, err := Render(Data{}); err == nil {
		t.Fatal("Render must return an error for a broken template")
	}
}

func TestStateClass(t *testing.T) {
	cases := map[string]string{
		"running":    "badge-running",
		"exited":     "badge-exited",
		"created":    "badge-created",
		"restarting": "badge-restarting",
		"paused":     "badge-paused",
		"dead":       "badge-dead",
		"something":  "badge-other",
	}
	for state, want := range cases {
		if got := (Container{State: state}).StateClass(); got != want {
			t.Errorf("StateClass(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestShortID(t *testing.T) {
	long := Container{ID: "abcdef1234567890"}
	if got := long.ShortID(); got != "abcdef123456…" {
		t.Errorf("ShortID truncation = %q, want %q", got, "abcdef123456…")
	}
	short := Container{ID: "abc"}
	if got := short.ShortID(); got != "abc" {
		t.Errorf("ShortID short = %q, want %q", got, "abc")
	}
}

func TestHealthyClass(t *testing.T) {
	if got := (Container{Healthy: "healthy"}).HealthyClass(); got != "badge-ok" {
		t.Errorf("healthy class = %q, want badge-ok", got)
	}
	if got := (Container{Healthy: "unhealthy"}).HealthyClass(); got != "badge-update" {
		t.Errorf("unhealthy class = %q, want badge-update", got)
	}
	if got := (Container{}).HealthyClass(); got != "" {
		t.Errorf("unknown class = %q, want empty", got)
	}
}

func TestImageUpdateLabels(t *testing.T) {
	cases := map[string][2]string{
		"update": {"badge-update", "update available"},
		"ok":     {"badge-ok", "up to date"},
		"":       {"badge-other", "unknown"},
	}
	for update, want := range cases {
		img := Image{Update: update}
		if got := img.UpdateClass(); got != want[0] {
			t.Errorf("UpdateClass(%q) = %q, want %q", update, got, want[0])
		}
		if got := img.UpdateLabel(); got != want[1] {
			t.Errorf("UpdateLabel(%q) = %q, want %q", update, got, want[1])
		}
	}
}
