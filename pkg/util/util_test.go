package util

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util/k8s"
)

func TestImageURN(t *testing.T) {
	// TestCase: Empty image
	out := getImageURN("", "registry.io", "")
	require.Equal(t, "", out)

	// TestCase: Empty repo and registry
	out = getImageURN("", "", "test/image")
	require.Equal(t, "test/image", out)

	// TestCase: Registry without repo but image with repo
	out = getImageURN("", "registry.io", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = getImageURN("", "registry.io/", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = getImageURN("", "registry.io", "test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	// TestCase: Registry and image without repo
	out = getImageURN("", "registry.io", "image")
	require.Equal(t, "registry.io/image", out)

	// TestCase: Image with common docker registries
	out = getImageURN("", "registry.io", "docker.io/test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = getImageURN("", "registry.io", "quay.io/test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	out = getImageURN("", "registry.io/", "index.docker.io/test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	out = getImageURN("", "registry.io", "registry-1.docker.io/image")
	require.Equal(t, "registry.io/image", out)

	out = getImageURN("", "registry.io/", "registry.connect.redhat.com/image")
	require.Equal(t, "registry.io/image", out)

	// TestCase: Regsitry and image both with repo
	out = getImageURN("", "registry.io/repo", "test/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = getImageURN("", "registry.io/repo", "test/this/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = getImageURN("", "registry.io/repo/", "test/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = getImageURN("", "registry.io/repo//", "test/this/image")
	require.Equal(t, "registry.io/repo/image", out)

	// TestCase: Regsitry with repo but image without repo
	out = getImageURN("", "registry.io/repo", "image")
	require.Equal(t, "registry.io/repo/image", out)

	out = getImageURN("", "registry.io/repo/subdir", "image")
	require.Equal(t, "registry.io/repo/subdir/image", out)

	// TestCase: Registry with empty root repo
	out = getImageURN("", "registry.io//", "image")
	require.Equal(t, "registry.io/image", out)

	out = getImageURN("", "registry.io//", "test/image")
	require.Equal(t, "registry.io/image", out)

	out = getImageURN("", "registry.io//", "test/this/image")
	require.Equal(t, "registry.io/image", out)

	out = getImageURN("k8s.gcr.io", "registry.io//", "k8s.gcr.io/pause:3.1")
	require.Equal(t, "registry.io/pause:3.1", out)

	// Update it again, now k8s.gcr.io should be deleted from common registries.
	out = getImageURN("gcr.io", "registry.io", "k8s.gcr.io/pause:3.1")
	require.Equal(t, "registry.io/k8s.gcr.io/pause:3.1", out)

	out = getImageURN("", "registry.io//", "k8s.gcr.io/pause:3.1")
	require.Equal(t, "registry.io/pause:3.1", out)

	out = getImageURN("", "registry.io//", "gcr.io/pause:3.1")
	require.Equal(t, "registry.io/pause:3.1", out)

	out = getImageURN("gcr.io,k8s.gcr.io", "registry.io", "gcr.io/pause:3.1")
	require.Equal(t, "registry.io/pause:3.1", out)

	out = getImageURN("gcr.io,k8s.gcr.io", "registry.io", "k8s.gcr.io/pause:3.1")
	require.Equal(t, "registry.io/pause:3.1", out)

	out = getImageURN("gcr.io,k8s.gcr.io", "registry.io", "testrepo/pause:3.1")
	require.Equal(t, "registry.io/testrepo/pause:3.1", out)
}

func TestImageURNPreserved(t *testing.T) {
	// Ensure original behaviour remains
	out := getImageURNPreserved("", "registry.io", "")
	require.Equal(t, "", out)

	// Ensure original behaviour remains
	out = getImageURNPreserved("", "", "test/image")
	require.Equal(t, "test/image", out)

	// Ensure original behaviour remains
	out = getImageURNPreserved("", "registry.io", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	// Ensure original behaviour remains
	out = getImageURNPreserved("", "registry.io/", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	// TestCase: Image without registry, registry with / in it
	out = getImageURNPreserved("", "registry.io/public", "test/image")
	require.Equal(t, "registry.io/public/test/image", out)

	// TestCase: Image with registry, registry with / in it
	out = getImageURNPreserved("", "registry.io/public", "docker.io/test/image")
	require.Equal(t, "registry.io/public/test/image", out)
}

func setUpCluster(commonRegistries string, customImageRegistry string, image string) corev1.StorageCluster {
	cluster := corev1.StorageCluster{}
	cluster.Annotations = make(map[string]string)
	cluster.Annotations[constants.AnnotationCommonImageRegistries] = commonRegistries
	cluster.Spec.CustomImageRegistry = customImageRegistry
	return cluster
}

func getImageURN(commonRegistries string, customImageRegistry string, image string) string {
	cluster := setUpCluster(commonRegistries, customImageRegistry, image)
	return GetImageURN(&cluster, image)
}

func getImageURNPreserved(commonRegistries string, customImageRegistry string, image string) string {
	cluster := setUpCluster(commonRegistries, customImageRegistry, image)
	cluster.Spec.PreserveFullCustomRegistryPath = true
	return GetImageURN(&cluster, image)
}

func TestGetImageMajorVersion(t *testing.T) {
	ver := GetImageMajorVersion("docker.io/test/image:v0.1.0")
	require.Equal(t, 0, ver)

	ver = GetImageMajorVersion("quay.io/test/image:v5.1.0")
	require.Equal(t, 5, ver)

	ver = GetImageMajorVersion("quay.io/test/image:5.1.0")
	require.Equal(t, 5, ver)

	ver = GetImageMajorVersion("quay.io/test/image")
	require.Equal(t, -1, ver)

	ver = GetImageMajorVersion("")
	require.Equal(t, -1, ver)

	ver = GetImageMajorVersion("quay.io/a:v999.998.997")
	require.Equal(t, 999, ver)
}

func TestPartialSecretRef(t *testing.T) {
	// happy
	obj := &corev1.SecretRef{
		SecretName: "a",
		SecretKey:  "b",
	}
	require.False(t, IsPartialSecretRef(obj))

	// no valid secret key
	obj = &corev1.SecretRef{
		SecretName: "a",
		SecretKey:  "",
	}
	require.True(t, IsPartialSecretRef(obj))

	obj = &corev1.SecretRef{
		SecretName: "a",
	}
	require.True(t, IsPartialSecretRef(obj))

	// no valid secret name
	obj = &corev1.SecretRef{
		SecretName: "",
		SecretKey:  "b",
	}
	require.True(t, IsPartialSecretRef(obj))

	obj = &corev1.SecretRef{
		SecretKey: "b",
	}
	require.True(t, IsPartialSecretRef(obj))

	// no valid secret key or name. Return false because it's empty, not partial
	obj = &corev1.SecretRef{
		SecretName: "",
		SecretKey:  "",
	}
	require.False(t, IsPartialSecretRef(obj))

	obj = &corev1.SecretRef{}
	require.False(t, IsPartialSecretRef(obj))

	obj = nil
	require.False(t, IsPartialSecretRef(obj))
}

func TestGetCustomAnnotations(t *testing.T) {
	// To avoid loop import, define the component name directly
	componentName := "storage"
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{},
	}
	require.Nil(t, GetCustomAnnotations(cluster, k8s.Pod, componentName))

	cluster.Spec.Metadata = &corev1.Metadata{}
	require.Nil(t, GetCustomAnnotations(cluster, k8s.Pod, componentName))

	cluster.Spec.Metadata.Annotations = make(map[string]map[string]string)
	require.Nil(t, GetCustomAnnotations(cluster, k8s.Pod, componentName))

	podPortworxAnnotations := map[string]string{
		"portworx-pod-key": "portworx-pod-val",
	}
	cluster.Spec.Metadata.Annotations = map[string]map[string]string{
		"pod/storage": podPortworxAnnotations,
	}
	require.Nil(t, GetCustomAnnotations(cluster, k8s.Pod, "invalid-component"))
	require.Nil(t, GetCustomAnnotations(cluster, "invalid-kind", componentName))
	require.Equal(t, podPortworxAnnotations, GetCustomAnnotations(cluster, k8s.Pod, componentName))
}

func TestComponentsPausedForMigration(t *testing.T) {
	cluster := &corev1.StorageCluster{}
	require.False(t, ComponentsPausedForMigration(cluster))

	cluster.Annotations = map[string]string{
		constants.AnnotationMigrationApproved: "true",
	}
	require.False(t, ComponentsPausedForMigration(cluster))

	cluster.Annotations[constants.AnnotationPauseComponentMigration] = "false"
	require.False(t, ComponentsPausedForMigration(cluster))

	cluster.Annotations[constants.AnnotationPauseComponentMigration] = "invalid"
	require.False(t, ComponentsPausedForMigration(cluster))

	cluster.Annotations[constants.AnnotationPauseComponentMigration] = "true"
	require.True(t, ComponentsPausedForMigration(cluster))
}
