package util

import (
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
	cluster.Spec.PreserveFullCustomImageRegistry = true
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

	ver = GetImageMajorVersion("quay.io/test/image:")
	require.Equal(t, -1, ver)

	ver = GetImageMajorVersion(":5.1.0")
	require.Equal(t, 5, ver)

	ver = GetImageMajorVersion(":")
	require.Equal(t, -1, ver)

	ver = GetImageMajorVersion("")
	require.Equal(t, -1, ver)

	ver = GetImageMajorVersion("quay.io/a:v999.998.997")
	require.Equal(t, 999, ver)

	ver = GetImageMajorVersion("custom.registry:18443/repo:v1.2.3-beta1")
	require.Equal(t, 1, ver)

	ver = GetImageMajorVersion("custom.registry:18443/repo")
	require.Equal(t, -1, ver)
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

	componentName = "portworx-service"
	serviceAnnotations := map[string]string{
		"annotation-key": "annotation-val",
	}
	cluster.Spec.Metadata.Annotations = map[string]map[string]string{
		"service/portworx-service": serviceAnnotations,
	}
	require.Equal(t, serviceAnnotations, GetCustomAnnotations(cluster, k8s.Service, componentName))
}

func TestGetCustomLabels(t *testing.T) {
	// To avoid loop import, define the component name directly
	componentName := "portworx-api"
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{},
	}
	require.Nil(t, GetCustomLabels(cluster, k8s.Service, componentName))

	cluster.Spec.Metadata = &corev1.Metadata{}
	require.Nil(t, GetCustomLabels(cluster, k8s.Service, componentName))

	cluster.Spec.Metadata.Labels = make(map[string]map[string]string)
	require.Nil(t, GetCustomLabels(cluster, k8s.Pod, componentName))

	portworxAPIServiceLabels := map[string]string{
		"portworx-api-service-key": "portworx-api-service-val",
	}
	cluster.Spec.Metadata.Labels = map[string]map[string]string{
		"service/portworx-api": portworxAPIServiceLabels,
	}
	require.Nil(t, GetCustomLabels(cluster, k8s.Service, "invalid-component"))
	require.Nil(t, GetCustomLabels(cluster, "invalid-kind", componentName))
	require.Equal(t, portworxAPIServiceLabels, GetCustomLabels(cluster, k8s.Service, componentName))
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

func TestHaveTopologySpreadConstraintsChanged(t *testing.T) {
	regionConstraint := v1.TopologySpreadConstraint{
		MaxSkew:           1,
		TopologyKey:       "topology.kubernetes.io/region",
		WhenUnsatisfiable: v1.ScheduleAnyway,
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"key": "value",
			},
		},
	}
	zoneConstraint := v1.TopologySpreadConstraint{
		MaxSkew:           1,
		TopologyKey:       "topology.kubernetes.io/zone",
		WhenUnsatisfiable: v1.ScheduleAnyway,
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"key": "value",
			},
		},
	}
	var updatedConstraints, existingConstraints []v1.TopologySpreadConstraint
	require.False(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	// Add region constraint
	updatedConstraints = append(updatedConstraints, *regionConstraint.DeepCopy())
	require.True(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	existingConstraints = append(existingConstraints, *regionConstraint.DeepCopy())
	require.False(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	// Change labels
	updatedConstraints[0].LabelSelector.MatchLabels["key"] = "new-val"
	require.True(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	existingConstraints[0].LabelSelector.MatchLabels["key"] = "new-val"
	require.False(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	// Add zone constraint
	updatedConstraints = append(updatedConstraints, *zoneConstraint.DeepCopy())
	require.True(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	existingConstraints = append(existingConstraints, *zoneConstraint.DeepCopy())
	require.False(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	// Remove constraints
	updatedConstraints = nil
	require.True(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
	existingConstraints = nil
	require.False(t, HaveTopologySpreadConstraintsChanged(updatedConstraints, existingConstraints))
}

func TestGetTopologySpreadConstraints(t *testing.T) {
	fakeNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node0",
			Labels: map[string]string{
				"topology.kubernetes.io/region": "region0",
				"topology.kubernetes.io/zone":   "zone0",
				"other.label.key":               "value",
			},
		},
	}
	k8sClient := fakeK8sClient(fakeNode)
	templateLabels := map[string]string{
		"key": "value",
	}
	expectedConstraints := []v1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/region",
			WhenUnsatisfiable: v1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: templateLabels,
			},
		},
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: v1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: templateLabels,
			},
		},
	}
	constraints, err := GetTopologySpreadConstraints(k8sClient, templateLabels)
	require.NoError(t, err)
	require.Equal(t, expectedConstraints, constraints)
}

func fakeK8sClient(initObjects ...runtime.Object) client.Client {
	s := scheme.Scheme
	corev1.AddToScheme(s)
	monitoringv1.AddToScheme(s)
	cluster_v1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(initObjects...).Build()
}
