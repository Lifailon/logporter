package updates

import (
	"context"
	"strings"

	"github.com/docker/docker/client"
)

func CheckImageUpdateDigest(
	dockerClient *client.Client,
	imageFullName string,
	currentDigest string,
) (
	updateStatus int,
	remoteDigest string,
	err error,
) {
	imageInspect, err := dockerClient.DistributionInspect(
		context.Background(),
		imageFullName,
		"",
	)
	if err != nil {
		return 0, imageFullName, err
	}
	digest := imageInspect.Descriptor.Digest.String()
	shaIndex := strings.Index(digest, "sha256:")
	if shaIndex != -1 {
		digest = digest[shaIndex+7:]
	}
	status := 0
	if !strings.Contains(currentDigest, digest) {
		status = 1
	}
	return status, digest, nil
}
