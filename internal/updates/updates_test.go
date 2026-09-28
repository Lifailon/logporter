package updates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func dockerWithDistribution(t *testing.T, digest string, fail bool) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/distribution/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Descriptor": map[string]any{"digest": digest},
		})
	}))
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

func withRemoteTags(t *testing.T, tags []string, err error) {
	t.Helper()
	orig := remoteTagList
	remoteTagList = func(_ name.Repository, _ ...remote.Option) ([]string, error) {
		return tags, err
	}
	t.Cleanup(func() { remoteTagList = orig })
}

func TestCheckImageUpdateSemanticUpdateAvailable(t *testing.T) {
	withRemoteTags(t, []string{"1.0.0", "not-semantic", "2.5.0", "3.1.0"}, nil)
	status, latest, err := CheckImageUpdateSemantic(context.Background(), "registry.example.com/app:v2.5.0", "v2.5.0", testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 1 {
		t.Fatalf("status = %d, want 1 (update available)", status)
	}
	if latest != "3.1.0" {
		t.Fatalf("latest tag = %q, want %q", latest, "3.1.0")
	}
}

func TestCheckImageUpdateSemanticUpToDate(t *testing.T) {
	withRemoteTags(t, []string{"1.0.0", "2.0.0"}, nil)
	status, latest, err := CheckImageUpdateSemantic(context.Background(), "app:2.0.0", "2.0.0", testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 0 {
		t.Fatalf("status = %d, want 0 (up to date)", status)
	}
	if latest != "2.0.0" {
		t.Fatalf("latest tag = %q, want %q", latest, "2.0.0")
	}
}

func TestCheckImageUpdateSemanticCurrentHigher(t *testing.T) {
	withRemoteTags(t, []string{"1.0.0", "2.0.0"}, nil)
	status, latest, err := CheckImageUpdateSemantic(context.Background(), "app:3.0.0", "3.0.0", testLogger())
	if err == nil {
		t.Fatal("expected error when current tag is higher than the remote latest")
	}
	if !strings.Contains(err.Error(), "higher") {
		t.Fatalf("unexpected error text: %v", err)
	}
	if status != 0 || latest != "2.0.0" {
		t.Fatalf("status = %d, latest = %q, want status 0 and latest %q", status, latest, "2.0.0")
	}
}

func TestCheckImageUpdateSemanticInvalidTag(t *testing.T) {
	withRemoteTags(t, []string{"1.0.0"}, nil)
	_, _, err := CheckImageUpdateSemantic(context.Background(), "app:latest", "latest", testLogger())
	if err == nil {
		t.Fatal("expected error for a non-semantic current tag")
	}
	if !strings.Contains(err.Error(), "not semantic") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestCheckImageUpdateSemanticNoSemanticTags(t *testing.T) {
	withRemoteTags(t, []string{"unstable", "!!!"}, nil)
	_, _, err := CheckImageUpdateSemantic(context.Background(), "app:v1.0.0", "v1.0.0", testLogger())
	if err == nil {
		t.Fatal("expected error when no semantic tags found")
	}
	if !strings.Contains(err.Error(), "no semantic tags") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestCheckImageUpdateSemanticRemoteError(t *testing.T) {
	withRemoteTags(t, nil, errors.New("registry unreachable"))
	_, _, err := CheckImageUpdateSemantic(context.Background(), "app:v1.0.0", "v1.0.0", testLogger())
	if err == nil || !strings.Contains(err.Error(), "no semantic tags") {
		t.Fatalf("expected no semantic tags error, got %v", err)
	}
}

func TestCheckImageUpdateSemanticInvalidRepository(t *testing.T) {

	_, _, err := CheckImageUpdateSemantic(context.Background(), "INVALID###:v1.0.0", "v1.0.0", testLogger())
	if err == nil || !strings.Contains(err.Error(), "no semantic tags") {
		t.Fatalf("expected no semantic tags error, got %v", err)
	}
}

func TestGetRemoteTagList(t *testing.T) {
	ctx := context.Background()
	if got := getRemoteTagList(ctx, "INVALID###"); got != nil {
		t.Fatalf("invalid repository must return nil, got %v", got)
	}

	withRemoteTags(t, nil, errors.New("boom"))
	if got := getRemoteTagList(ctx, "app"); got != nil {
		t.Fatalf("repository error must return nil, got %v", got)
	}

	withRemoteTags(t, []string{"1.0.0", "2.0.0"}, nil)
	if got := getRemoteTagList(ctx, "app"); len(got) != 2 {
		t.Fatalf("expected 2 tags, got %v", got)
	}
}

func TestCheckImageUpdateDigestUpToDate(t *testing.T) {
	dc := dockerWithDistribution(t, "sha256:abc123", false)
	status, remoteDigest, err := CheckImageUpdateDigest(context.Background(), dc, "nginx:latest", "abc123", testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 0 {
		t.Fatalf("status = %d, want 0 (current digest found)", status)
	}
	if remoteDigest != "abc123" {
		t.Fatalf("remoteDigest = %q, want %q", remoteDigest, "abc123")
	}
}

func TestCheckImageUpdateDigestUpdateAvailable(t *testing.T) {
	dc := dockerWithDistribution(t, "sha256:abc123", false)
	status, remoteDigest, err := CheckImageUpdateDigest(context.Background(), dc, "nginx:latest", "def456", testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 1 {
		t.Fatalf("status = %d, want 1 (update available)", status)
	}
	if remoteDigest != "abc123" {
		t.Fatalf("remoteDigest = %q, want %q", remoteDigest, "abc123")
	}
}

func TestCheckImageUpdateDigestNoSha256Prefix(t *testing.T) {

	dc := dockerWithDistribution(t, "sha512:xyz789", false)
	_, remoteDigest, err := CheckImageUpdateDigest(context.Background(), dc, "nginx:latest", "abc", testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if remoteDigest != "sha512:xyz789" {
		t.Fatalf("remoteDigest = %q, want %q", remoteDigest, "sha512:xyz789")
	}
}

func TestCheckImageUpdateDigestError(t *testing.T) {
	dc := dockerWithDistribution(t, "", true)
	status, remoteDigest, err := CheckImageUpdateDigest(context.Background(), dc, "nginx:latest", "abc", testLogger())
	if err == nil {
		t.Fatal("expected error from DistributionInspect")
	}
	if status != 0 || remoteDigest != "nginx:latest" {
		t.Fatalf("status = %d, remoteDigest = %q, want 0 and the original image name", status, remoteDigest)
	}
}
