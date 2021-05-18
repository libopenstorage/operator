package util

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util/k8s"
)

func TestGetImageURN(t *testing.T) {
	// TestCase: Empty image
	out := GetImageURN("registry.io", "")
	require.Equal(t, "", out)

	// TestCase: Empty repo and registry
	out = GetImageURN("", "test/image")
	require.Equal(t, "test/image", out)

	// TestCase: Registry without repo but image with repo
	out = GetImageURN("registry.io", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = GetImageURN("registry.io/", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = GetImageURN("registry.io", "test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	// TestCase: Registry and image without repo
	out = GetImageURN("registry.io", "image")
	require.Equal(t, "registry.io/image", out)

	// TestCase: Image with common docker registries
	out = GetImageURN("registry.io", "docker.io/test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = GetImageURN("registry.io", "quay.io/test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	out = GetImageURN("registry.io/", "index.docker.io/test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	out = GetImageURN("registry.io", "registry-1.docker.io/image")
	require.Equal(t, "registry.io/image", out)

	out = GetImageURN("registry.io/", "registry.connect.redhat.com/image")
	require.Equal(t, "registry.io/image", out)

	// TestCase: Regsitry and image both with repo
	out = GetImageURN("registry.io/repo", "test/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo", "test/this/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo/", "test/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo//", "test/this/image")
	require.Equal(t, "registry.io/repo/image", out)

	// TestCase: Regsitry with repo but image without repo
	out = GetImageURN("registry.io/repo", "image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo/subdir", "image")
	require.Equal(t, "registry.io/repo/subdir/image", out)

	// TestCase: Registry with empty root repo
	out = GetImageURN("registry.io//", "image")
	require.Equal(t, "registry.io/image", out)

	out = GetImageURN("registry.io//", "test/image")
	require.Equal(t, "registry.io/image", out)

	out = GetImageURN("registry.io//", "test/this/image")
	require.Equal(t, "registry.io/image", out)
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
