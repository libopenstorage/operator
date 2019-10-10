package util

import (
	"path"
	"strings"
)

// Reasons for controller events
const (
	// FailedPlacementReason is added to an event when operator can't schedule a Pod to a specified node.
	FailedPlacementReason = "FailedPlacement"
	// FailedStoragePodReason is added to an event when the status of a Pod of a cluster is 'Failed'.
	FailedStoragePodReason = "FailedStoragePod"
	// FailedSyncReason is added to an event when the status the cluster could not be synced.
	FailedSyncReason = "FailedSync"
	// FailedComponentReason is added to an event when setting up or removing a component fails.
	FailedComponentReason = "FailedComponent"
)

var (
	// commonDockerRegistries is a map of commonly used Docker registries
	commonDockerRegistries = map[string]bool{
		"docker.io":                   true,
		"index.docker.io":             true,
		"registry-1.docker.io":        true,
		"registry.connect.redhat.com": true,
	}
)

// GetImageURN returns the complete image name based on the registry and repo
func GetImageURN(registryAndRepo, image string) string {
	if image == "" {
		return ""
	}

	registryAndRepo = strings.TrimRight(registryAndRepo, "/")
	if registryAndRepo == "" {
		// no registry/repository specifed, return image
		return image
	}

	imgParts := strings.Split(image, "/")
	if len(imgParts) > 1 {
		// advance imgParts to swallow the common registry
		if _, present := commonDockerRegistries[imgParts[0]]; present {
			imgParts = imgParts[1:]
		}
	}

	// if we have '/' in the registryAndRepo, return <registry/repository/><only-image>
	// else (registry only) -- return <registry/><image-with-repository>
	if strings.Contains(registryAndRepo, "/") && len(imgParts) > 1 {
		// advance to the last element, skipping image's repository
		imgParts = imgParts[len(imgParts)-1:]
	}
	return registryAndRepo + "/" + path.Join(imgParts...)
}
