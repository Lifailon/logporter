package updates

import (
	"log/slog"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func CheckImageUpdateDigest(
	imageFullName string,
	reference name.Reference,
	currentDigest string,
	logger *slog.Logger,
) (
	updateStatus int,
	remoteDigest string,
	err error,
) {
	descriptor, err := remote.Head(reference)
	if err != nil {
		return 0, currentDigest, err
	}
	digest := descriptor.Digest.Hex
	status := 0
	if !strings.Contains(currentDigest, digest) {
		status = 1
	}
	logger.Debug(
		"image digest detected",
		"image", imageFullName,
		"status", status,
		"currentDigest", currentDigest,
		"remoteDigest", digest,
	)
	return status, digest, nil
}
