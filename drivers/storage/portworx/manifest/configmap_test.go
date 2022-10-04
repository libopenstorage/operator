package manifest

import (
	"context"
	"testing"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConfigMapManifestWithValidData(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-test",
		},
	}
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: `
version: 3.2.1
components:
  stork: stork/image:3.2.1
`,
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	m, err := newConfigMapManifest(k8sClient, cluster)
	require.NoError(t, err)

	r, err := m.Get()
	require.NoError(t, err)
	require.Equal(t, "3.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)
}

func TestConfigMapManifestWithValidDataButDifferentKey(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-test",
		},
	}
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			"invalid": `
version: 3.2.1
components:
  stork: stork/image:3.2.1
`,
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	// TestCase: Has invalid data key but it is the only key present
	m, err := newConfigMapManifest(k8sClient, cluster)
	require.NoError(t, err)

	r, err := m.Get()
	require.NoError(t, err)
	require.Equal(t, "3.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)

	// TestCase: Has both a valid and invalid key.
	// We should use the valid one, if present
	versionsConfigMap.Data[VersionConfigMapKey] = `
version: 4.2.1
components:
  stork: stork/image:4.2.1
`
	err = k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)

	m, err = newConfigMapManifest(k8sClient, cluster)
	require.NoError(t, err)

	r, err = m.Get()
	require.NoError(t, err)
	require.Equal(t, "4.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:4.2.1", r.Components.Stork)
}

func TestConfigMapManifestWhenConfigMapMissing(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-test",
		},
	}
	k8sClient := testutil.FakeK8sClient()

	m, err := newConfigMapManifest(k8sClient, cluster)
	require.Error(t, err)
	require.Nil(t, m)
}

func TestConfigMapManifestWithEmptyData(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-test",
		},
	}
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	// TestCase: No versions data present
	m, err := newConfigMapManifest(k8sClient, cluster)
	require.NoError(t, err)

	r, err := m.Get()
	require.Equal(t, err, ErrReleaseNotFound)
	require.Nil(t, r)

	// TestCase: Empty versions data present
	versionsConfigMap.Data = map[string]string{
		VersionConfigMapKey: "",
	}
	err = k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)

	m, err = newConfigMapManifest(k8sClient, cluster)
	require.NoError(t, err)

	r, err = m.Get()
	require.Equal(t, err, ErrReleaseNotFound)
	require.Nil(t, r)
}

func TestConfigMapManifestWithInvalidData(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-test",
		},
	}
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: "invalid_yaml",
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	// TestCase: No versions data present
	m, err := newConfigMapManifest(k8sClient, cluster)
	require.NoError(t, err)

	r, err := m.Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot unmarshal")
	require.Nil(t, r)
}
