package util

import (
	"path"
	"reflect"
	"strings"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"k8s.io/api/core/v1"
)

// Reasons for controller events
const (
	// FailedPlacementReason is added to an event when operator can't schedule a Pod to a specified node.
	FailedPlacementReason = "FailedPlacement"
	// FailedStoragePodReason is added to an event when the status of a Pod of a cluster is 'Failed'.
	FailedStoragePodReason = "FailedStoragePod"
	// FailedSyncReason is added to an event when the status of the cluster could not be synced.
	FailedSyncReason = "FailedSync"
	// FailedValidationReason is added to an event when operator validations fail.
	FailedValidationReason = "FailedValidation"
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

// HasPullSecretChanged checks if the imagePullSecret in the cluster is the only one
// in the given list of pull secrets
func HasPullSecretChanged(
	cluster *corev1alpha1.StorageCluster,
	existingPullSecrets []v1.LocalObjectReference,
) bool {
	return len(existingPullSecrets) > 1 ||
		(len(existingPullSecrets) == 1 &&
			cluster.Spec.ImagePullSecret != nil && existingPullSecrets[0].Name != *cluster.Spec.ImagePullSecret) ||
		(len(existingPullSecrets) == 0 &&
			cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "")
}

// HaveTolerationsChanged checks if the tolerations in the cluster are same as the
// given list of tolerations
func HaveTolerationsChanged(
	cluster *corev1alpha1.StorageCluster,
	existingTolerations []v1.Toleration,
) bool {
	if cluster.Spec.Placement == nil {
		return len(existingTolerations) != 0
	}
	return !reflect.DeepEqual(cluster.Spec.Placement.Tolerations, existingTolerations)
}

// HasNodeAffinityChanged checks if the nodeAffinity in the cluster is same as the
// node affinity in the given affinity
func HasNodeAffinityChanged(
	cluster *corev1alpha1.StorageCluster,
	existingAffinity *v1.Affinity,
) bool {
	if cluster.Spec.Placement == nil {
		return existingAffinity != nil && existingAffinity.NodeAffinity != nil
	} else if existingAffinity == nil {
		return cluster.Spec.Placement.NodeAffinity != nil
	}
	return !reflect.DeepEqual(cluster.Spec.Placement.NodeAffinity, existingAffinity.NodeAffinity)
}
