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

type versions struct {
	semVersion *semver.Version
	rawVersion string
}

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
	var tags []versions
	for _, tag := range tagList {
		semVer, err := semver.NewVersion(tag)
		if err != nil {
			continue
		}
		tags = append(tags, versions{
			semVersion: semVer,
			rawVersion: tag,
		})
	}
	if len(tags) == 0 {
		return 0, "", fmt.Errorf("no semantic tags found in the remote registry")
	}
	// Sort by version and get status
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].semVersion.LessThan(tags[j].semVersion)
	})
	latestTag = tags[len(tags)-1].rawVersion
	status := currentVer.Compare(tags[len(tags)-1].semVersion)
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
		"latestTag", latestTag,
		"remoteTags", tagList,
	)
	if status == -1 {
		return 0, latestTag, fmt.Errorf("current tag version is higher than the one in the remote registry")
	} else {
		return status, latestTag, nil
	}
}
