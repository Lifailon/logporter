package updates

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func getRemoteTagList(image string) []string {
	repositoryName, err := name.NewRepository(image)
	if err != nil {
		return nil
	}
	remoteTags, err := remote.List(repositoryName)
	if err != nil {
		return nil
	}
	return remoteTags
}

func CheckImageUpdateSemantic(
	imageFullName,
	imageTag string,
	logger *slog.Logger,
) (
	updateStatus int,
	latestTag string,
	err error,
) {
	// Remove tag from image name
	imageName := strings.TrimSuffix(imageFullName, ":"+imageTag)
	// Check input tag on semantic version
	currentVer, err := semver.NewVersion(imageTag)
	if err != nil {
		return 0, "", fmt.Errorf("current tag is not semantic")
	}
	// Filtering non-semantic tags in remote registry
	tagList := getRemoteTagList(imageName)
	var versions []*semver.Version
	for _, tag := range tagList {
		v, err := semver.NewVersion(tag)
		if err != nil {
			continue
		}
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		return 0, "", fmt.Errorf("no semantic tags found in the remote registry")
	}
	// Sort by version and get status
	sort.Sort(semver.Collection(versions))
	latestRemoteVer := versions[len(versions)-1]
	status := currentVer.Compare(latestRemoteVer)
	// Parsing update status
	switch status {
	case 1:
		status = -1
	case -1:
		status = 1
	default:
		status = 0
	}
	logger.Debug(
		"image semantic version detected",
		"image", imageName,
		"status", status,
		"currentTag", imageTag,
		"latestTag", latestRemoteVer,
		"remoteTags", versions,
	)
	if status == -1 {
		return 0, latestRemoteVer.String(), fmt.Errorf("current tag version is higher than the one in the remote registry")
	} else {
		return status, latestRemoteVer.String(), nil
	}
}
