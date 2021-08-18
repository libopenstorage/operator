package portworx

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/cloudops"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
)

func TestBasicRuncPodSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/runc.yaml")
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll:        boolPtr(true),
					ForceUseDisks: boolPtr(true),
				},
				Env: []v1.EnvVar{
					{
						Name:  "TEST_KEY",
						Value: "TEST_VALUE",
					},
				},
				RuntimeOpts: map[string]string{
					"op1": "10",
				},
			},
		},
	}

	driver := portworx{}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWithImagePullSecrets(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/oci-monitor:2.0.3.4",
			ImagePullSecret: stringPtr("px-secret"),
		},
	}

	expectedPullSecret := v1.LocalObjectReference{
		Name: "px-secret",
	}
	expectedRegistryEnv := v1.EnvVar{
		Name: "REGISTRY_CONFIG",
		ValueFrom: &v1.EnvVarSource{
			SecretKeyRef: &v1.SecretKeySelector{
				Key: ".dockerconfigjson",
				LocalObjectReference: v1.LocalObjectReference{
					Name: "px-secret",
				},
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
	assert.Len(t, actual.Containers[0].Env, 7)
	var regConfigEnv *v1.EnvVar
	var regSecretEnv *v1.EnvVar
	for _, env := range actual.Containers[0].Env {
		if env.Name == "REGISTRY_CONFIG" {
			regConfigEnv = env.DeepCopy()
		} else if env.Name == "REGISTRY_SECRET" {
			regSecretEnv = env.DeepCopy()
		}
	}
	assert.Equal(t, expectedRegistryEnv, *regConfigEnv)
	assert.Nil(t, regSecretEnv)

	// TestCase: Portworx version is newer than 2.3.2
	cluster.Spec.Image = "portworx/oci-monitor:2.3.2"

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
	assert.Len(t, actual.Containers[0].Env, 8)
	regConfigEnv = nil
	for _, env := range actual.Containers[0].Env {
		if env.Name == "REGISTRY_CONFIG" {
			regConfigEnv = env.DeepCopy()
		} else if env.Name == "REGISTRY_SECRET" {
			regSecretEnv = env.DeepCopy()
		}
	}
	assert.Equal(t, "px-secret", regSecretEnv.Value)
	assert.Nil(t, regConfigEnv)

	// kvdb pod spec
	actual, err = driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
}

func TestPodSpecWithTolerations(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	tolerations := []v1.Toleration{
		{
			Key:      "must-exist",
			Operator: v1.TolerationOpExists,
			Effect:   v1.TaintEffectNoExecute,
		},
		{
			Key:      "foo",
			Operator: v1.TolerationOpEqual,
			Value:    "bar",
			Effect:   v1.TaintEffectNoSchedule,
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
				Tolerations: tolerations,
			},
		},
	}

	driver := portworx{}
	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.ElementsMatch(t, tolerations, podSpec.Tolerations)

	kvdbPodSpec, err := driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.NotNil(t, kvdbPodSpec)
	require.ElementsMatch(t, tolerations, kvdbPodSpec.Tolerations)
}

func TestPodSpecWithEnvOverrides(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxSecretsNamespace,
						Value: "custom",
					},
					{
						Name:  "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS",
						Value: "300",
					},
				},
			},
		},
	}

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/portworxPodEnvOverride.yaml")

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestGetKVDBPodSpec(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{},
	}

	expected := getExpectedPodSpec(t, "testspec/kvdbPodDefault.yaml")
	actual, err := driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	assertPodSpecEqual(t, expected, &actual)

	// custom port
	startPort := uint32(10001)
	cluster.Spec.StartPort = &startPort
	expected = getExpectedPodSpec(t, "testspec/kvdbPodCustomPort.yaml")
	actual, err = driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWithCustomServiceAccount(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.8",
	}

	nodeName := "testNode"
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxServiceAccount,
						Value: "custom-px-sa",
					},
				},
			},
		},
	}

	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)
	require.Equal(t, "custom-px-sa", podSpec.ServiceAccountName)

	kvdbPodSpec, err := driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.Equal(t, "custom-px-sa", kvdbPodSpec.ServiceAccountName)
}

func TestAutoNodeRecoveryTimeoutEnvForPxVersion2_6(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.5.1",
		},
	}
	recoveryEnv := v1.EnvVar{
		Name:  "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS",
		Value: "1500",
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.Contains(t, actual.Containers[0].Env, recoveryEnv)

	cluster.Spec.Image = "portworx/oci-monitor:2.6.0"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.NotContains(t, actual.Containers[0].Env, recoveryEnv)
}

func TestPodSpecWithKvdbSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"endpoint-1",
					"endpoint-2",
					"endpoint-3",
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-k", "endpoint-1,endpoint-2,endpoint-3",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should have both bootstrap and endpoints
	cluster.Spec.Kvdb.Internal = true
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-k", "endpoint-1,endpoint-2,endpoint-3",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should take bootstrap only if endpoints is nil
	cluster.Spec.Kvdb.Endpoints = nil
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should take bootstrap only if endpoints are empty
	cluster.Spec.Kvdb.Endpoints = []string{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithNetworkSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1.CommonConfig{
				Network: &corev1.NetworkSpec{
					DataInterface: stringPtr("data-intf"),
					MgmtInterface: stringPtr("mgmt-intf"),
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-d", "data-intf",
		"-m", "mgmt-intf",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Only data interface is given
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr("data-intf"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-d", "data-intf",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Mgmt interface given but empty
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr("data-intf"),
		MgmtInterface: stringPtr(""),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Only management interface is given
	cluster.Spec.Network = &corev1.NetworkSpec{
		MgmtInterface: stringPtr("mgmt-intf"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-m", "mgmt-intf",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Data interface given but empty
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr(""),
		MgmtInterface: stringPtr("mgmt-intf"),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Both data and mgmt interfaces are empty
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr(""),
		MgmtInterface: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Both data and mgmt interfaces are nil
	cluster.Spec.Network = &corev1.NetworkSpec{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Network spec is nil
	cluster.Spec.Network = &corev1.NetworkSpec{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithStorageSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-a",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// UseAllWithPartitions
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-A",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// UseAll with UseAllWithPartitions
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// ForceUseDisks
	cluster.Spec.Storage = &corev1.StorageSpec{
		ForceUseDisks: boolPtr(true),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-f",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Journal device
	cluster.Spec.Storage = &corev1.StorageSpec{
		JournalDevice: stringPtr("/dev/journal"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "/dev/journal",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No journal device if empty
	cluster.Spec.Storage = &corev1.StorageSpec{
		JournalDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Metadata device
	cluster.Spec.Storage = &corev1.StorageSpec{
		SystemMdDevice: stringPtr("/dev/metadata"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-metadata", "/dev/metadata",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No metadata device if empty
	cluster.Spec.Storage = &corev1.StorageSpec{
		SystemMdDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Kvdb device
	cluster.Spec.Storage = &corev1.StorageSpec{
		KvdbDevice: stringPtr("/dev/kvdb"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-kvdb_dev", "/dev/kvdb",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No kvdb device if empty
	cluster.Spec.Storage = &corev1.StorageSpec{
		KvdbDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices
	devices := []string{"/dev/one", "/dev/two", "/dev/three"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
		"-s", "/dev/two",
		"-s", "/dev/three",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Cache devices
	cacheDevices := []string{"/dev/one", "/dev/two", "/dev/three"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		CacheDevices: &cacheDevices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-cache", "/dev/one",
		"-cache", "/dev/two",
		"-cache", "/dev/three",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices empty
	devices = []string{}
	cluster.Spec.Storage = &corev1.StorageSpec{
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Explicit storage devices get priority over UseAll
	devices = []string{"/dev/one"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:  boolPtr(true),
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Explicit storage devices get priority over UseAllWithPartition
	devices = []string{"/dev/one"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
		Devices:              &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty storage config
	cluster.Spec.Storage = &corev1.StorageSpec{}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil storage config
	cluster.Spec.Storage = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithCloudStorageSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)

	_, yamlData := generateValidYamlData(t)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			UID:       "px-cluster-UID",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CloudStorage: &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					JournalDeviceSpec: stringPtr("type=journal")},
			},
		},
	}
	k8sClient := testutil.FakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: "kube-system",
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
		cluster,
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "testNode",
			},
			Status: v1.NodeStatus{
				NodeInfo: v1.NodeSystemInfo{
					OSImage: "Ubuntu 18.04.5 LTS",
				},
			},
		},
	)
	nodeName := "testNode"

	zoneToInstancesMap := map[string]uint64{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "type=journal",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty journal device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			JournalDeviceSpec: stringPtr(""),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Metadata device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			SystemMdDeviceSpec: stringPtr("type=metadata"),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-metadata", "type=metadata",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty metadata device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			SystemMdDeviceSpec: stringPtr(""),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Kvdb device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			KvdbDeviceSpec: stringPtr("type=kvdb"),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-kvdb_dev", "type=kvdb",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty kvdb device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			KvdbDeviceSpec: stringPtr(""),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage device specs
	devices := []string{"type=one", "type=two", "type=three"}
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: &devices,
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=one",
		"-s", "type=two",
		"-s", "type=three",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage device specs empty
	devices = []string{}
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: &devices,
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Node pool label
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		NodePoolLabel: "px-storage-type",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-node_pool_label", "px-storage-type",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Max storage nodes
	maxNodes := uint32(3)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		MaxStorageNodes: &maxNodes,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_drive_set_count", "3",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices and auto-compute max_storage_nodes_per_zone
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	expectedCloudStorageSpec := []corev1.StorageNodeCloudDriveConfig{
		{
			Type:      "foo",
			SizeInGiB: uint64(120),
			IOPS:      uint64(110),
			Options:   map[string]string{"foo1": "bar1"},
		},
		{
			Type:      "bar",
			SizeInGiB: uint64(220),
			IOPS:      uint64(210),
			Options: map[string]string{
				"foo3": "bar3",
			}},
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=foo,size=120,iops=110,foo1=bar1",
		"-s", "type=bar,size=220,iops=210,foo3=bar3",
		"-max_storage_nodes_per_zone", "2",
	}

	inputInstancesPerZone := uint64(2)
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        uint64(len(zoneToInstancesMap)),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint64(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint64(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(210),
				},
			},
		}, nil)

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err := driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Len(t, list, 1)
	assert.ElementsMatch(t, list[0].Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	assert.Equalf(t, nodeName, list[0].Name, "expected node name '%s'", nodeName)

	// Storage devices and use user provided max_storage_nodes_per_zone
	userProvidedInstancesPerZone := 3
	userProvidedInstancesPerZoneUint32 := uint32(userProvidedInstancesPerZone)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
		MaxStorageNodesPerZone: &userProvidedInstancesPerZoneUint32,
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=foo,size=120,iops=110,foo1=bar1",
		"-s", "type=bar,size=220,iops=210,foo3=bar3",
		"-max_storage_nodes_per_zone", "2",
	}

	// despite adding a new node, node spec should be pulled from the existing storage node for nodeName
	// Storage node will be created for "nodeName1"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")
	assert.ElementsMatch(t, list[0].Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	// Empty cloud config
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil cloud config
	cluster.Spec.CloudStorage = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")

	// TestCase: Nil cloud config but max storage nodes per zone is set
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_storage_nodes_per_zone", "2",
	}

	// Empty cloud config
	maxStorageNodesPerZone := uint32(2)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		MaxStorageNodesPerZone: &maxStorageNodesPerZone,
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")

	// TestCase: Nil cloud config but max storage nodes per zone per node group is set
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_storage_nodes_per_zone_per_nodegroup", "3",
	}

	// Empty cloud config
	maxStorageNodesPerZonePerNodeGroup := uint32(3)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			MaxStorageNodesPerZonePerNodeGroup: &maxStorageNodesPerZonePerNodeGroup,
		},
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")

	// Test cloud provider is provided, px version is less than 2.8.0, cloud provider parameter should not be specified.
	cluster.Spec.Image = "portworx/oci-monitor:2.7.0"
	cloudProvider := "AWS"
	cluster.Spec.CloudStorage.Provider = &cloudProvider
	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Test cloud provider is provided, px version is 2.8.0, cloud provider parameter should be specified.
	cluster.Spec.Image = "portworx/oci-monitor:2.8.0"
	expectedArgs = append(expectedArgs, "-cloud_provider", cloudProvider)
	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	cluster.Spec.CloudStorage.Provider = nil
}

func TestPodSpecWithCapacitySpecsAndDeviceSpecs(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)

	_, yamlData := generateValidYamlData(t)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
		},
	}

	k8sClient := testutil.FakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: "kube-system",
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
		cluster,
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "testNode",
			},
			Status: v1.NodeStatus{
				NodeInfo: v1.NodeSystemInfo{
					OSImage: "Ubuntu 18.04.5 LTS",
				},
			},
		},
	)

	// Provide both devices specs and cloud capacity specs
	deviceSpecs := []string{"type=one", "type=two"}

	nodeName := "testNode"

	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: &deviceSpecs,
		},
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	zoneToInstancesMap := map[string]uint64{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	inputInstancesPerZone := uint64(2)
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        uint64(len(zoneToInstancesMap)),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint64(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint64(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(210),
				},
			},
		}, nil)

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=foo,size=120,iops=110,foo1=bar1",
		"-s", "type=bar,size=220,iops=210,foo3=bar3",
		"-max_storage_nodes_per_zone", "2",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

}

func TestPodSpecWithStorageAndCloudStorageSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					JournalDevice: stringPtr("/dev/journal"),
				},
			},
			CloudStorage: &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					JournalDeviceSpec: stringPtr("type=journal"),
				},
			},
		},
	}
	driver := portworx{}

	// Use storage spec over cloud storage spec if not empty
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "/dev/journal",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithSecretsProvider(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			SecretsProvider: stringPtr("k8s"),
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-secret_type", "k8s",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the secrets provider if empty
	cluster.Spec.SecretsProvider = stringPtr("")
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the secrets provider if nil
	cluster.Spec.SecretsProvider = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithCustomStartPort(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	startPort := uint32(10001)

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image:     "portworx/oci-monitor:2.1.1",
			StartPort: &startPort,
		},
	}
	driver := portworx{}

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/portworxPodCustomPort.yaml")

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Don't set the start port if same as default start port
	startPort = uint32(pxutil.DefaultStartPort)
	cluster.Spec.StartPort = &startPort
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the start port if nil
	cluster.Spec.StartPort = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithLogAnnotation(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationLogFile: "/tmp/log",
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--log", "/tmp/log",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithRuntimeOptions(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				RuntimeOpts: map[string]string{
					"key": "10",
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-rt_opts", "key=10",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// For invalid value key should not be added. Only numeric values are supported.
	cluster.Spec.RuntimeOpts["invalid"] = "non-numeric"

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty runtime map should not add any options
	cluster.Spec.RuntimeOpts = make(map[string]string)
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil runtime map should not add any options
	cluster.Spec.RuntimeOpts = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithMiscArgs(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationMiscArgs: "-fruit apple -person john",
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-fruit", "apple",
		"-person", "john",
	}

	actual, _ := driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithInvalidMiscArgs(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationMiscArgs: "'-unescaped-quote",
			},
		},
	}
	driver := portworx{}

	// Don't add misc args if they are invalid
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithEssentials(t *testing.T) {
	defer func() {
		os.Unsetenv(pxutil.EnvKeyPortworxEssentials)
		os.Unsetenv(pxutil.EnvKeyMarketplaceName)
	}()
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationMiscArgs: "--oem custom",
			},
		},
	}
	driver := portworx{}

	// TestCase: Replace existing oem value to use essentials
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "true")
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ := driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Do not change anything if oem is already essentials
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "--oem esse"

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem flag if misc args are empty
	cluster.Annotations[pxutil.AnnotationMiscArgs] = ""

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem flag if misc arg annotation not present
	cluster.Annotations = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem flag if misc arg does not have oem flag
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-k1 v1",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-k1", "v1",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem value even if existing oem arg is invalid
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-k1 v1 --oem ",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Update oem value even if in the middle
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-k1  v1  --oem  custom  -k2  v2",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-k1", "v1",
		"--oem", "esse",
		"-k2", "v2",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name is present
	os.Setenv(pxutil.EnvKeyMarketplaceName, "operatorhub")
	cluster.Annotations = nil
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
		"-marketplace_name", "operatorhub",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name should not be passed for older portworx releases
	cluster.Spec.Image = "image/portworx:2.5.4"
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name should be passed for newer portworx releases
	cluster.Spec.Image = "image/portworx:2.5.5"
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
		"-marketplace_name", "operatorhub",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name is empty
	os.Setenv(pxutil.EnvKeyMarketplaceName, "")
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name is empty string on trimming
	os.Setenv(pxutil.EnvKeyMarketplaceName, "    ")
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Do not update oem value if essentials is disabled
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "false")
	os.Setenv(pxutil.EnvKeyMarketplaceName, "operatorhub")
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "--oem custom",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "custom",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Do not add oem value if essentials is disabled
	cluster.Annotations = nil
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithImagePullPolicy(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullPolicy: v1.PullIfNotPresent,
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "True",
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "csi/registrar",
			},
		},
	}
	driver := portworx{}

	// Pass the image pull policy in the pod args
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--pull", "IfNotPresent",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Len(t, actual.Containers, 2)
	assert.Equal(t, v1.PullIfNotPresent, actual.Containers[0].ImagePullPolicy)
	assert.Equal(t, v1.PullIfNotPresent, actual.Containers[1].ImagePullPolicy)
}

func TestPodSpecWithNilStorageCluster(t *testing.T) {
	var cluster *corev1.StorageCluster
	driver := portworx{}
	nodeName := "testNode"

	_, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.Error(t, err, "Expected an error on GetStoragePodSpec")
}

func TestPodSpecWithInvalidKubernetesVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid-version",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
	}
	driver := portworx{}

	_, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.Error(t, err, "Expected an error on GetStoragePodSpec")
}

func TestPKSPodSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/pks.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestOpenshiftRuncPodSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/openshift_runc.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsOpenshift: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
									{
										Key:      "node-role.kubernetes.io/infra",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForK3s(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.4+k3s1",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_k3s.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.6.0",
		},
	}
	driver := portworx{}
	driver.SetDefaultsOnStorageCluster(cluster)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForBottleRocketAMI(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.20.1",
	}

	nodeName := "testNode"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.7.1",
		},
	}
	driver := portworx{
		k8sClient: testutil.FakeK8sClient(
			&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeName,
					Namespace: "",
				},
				Status: v1.NodeStatus{
					NodeInfo: v1.NodeSystemInfo{
						OSImage: "BottleRocket OS 123",
					},
				},
			},
		),
	}

	// we'll be expecting BottleRocket-specific args
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-disable-log-proxy",
		"--install-uncompress",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.Len(t, actual.Containers, 1)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Contains(t, actual.Volumes, v1.Volume{
		Name: "containerd-br",
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: "/run/dockershim.sock",
			},
		},
	})
	// also double-check for BottleRocket-specific mount
	assert.Contains(t, actual.Containers[0].VolumeMounts, v1.VolumeMount{
		Name:      "containerd-br",
		MountPath: "/run/containerd/containerd.sock",
	})

	// double-check permissions
	expectedCapabilities := []v1.Capability{
		"SYS_ADMIN", "SYS_PTRACE", "SYS_RAWIO", "SYS_MODULE", "LINUX_IMMUTABLE",
	}
	require.NotNil(t, actual.Containers[0].SecurityContext.Privileged)
	assert.False(t, *actual.Containers[0].SecurityContext.Privileged)

	require.NotNil(t, actual.Containers[0].SecurityContext.Capabilities)
	assert.ElementsMatch(t, expectedCapabilities, actual.Containers[0].SecurityContext.Capabilities.Add)

	require.NotNil(t, actual.SecurityContext)
	require.NotNil(t, actual.SecurityContext.SELinuxOptions)
	assert.Equal(t, "super_t", actual.SecurityContext.SELinuxOptions.Type)
}

func TestPodWithTelemetry(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.4",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_telemetry-with-location.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				"portworx.io/arcus-location": "internal",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.8.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
					Image:   "portworx/px-telemetry:2.1.2",
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "false",
			},
		},
	}
	driver := portworx{}
	driver.SetDefaultsOnStorageCluster(cluster)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// don't specify arcus location
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/px_telemetry.yaml")
	delete(cluster.Annotations, "portworx.io/arcus-location")
	driver = portworx{}
	driver.SetDefaultsOnStorageCluster(cluster)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Now disable telemetry
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/px_disable_telemetry.yaml")
	cluster.Spec.Monitoring.Telemetry.Enabled = false
	driver = portworx{}
	driver.SetDefaultsOnStorageCluster(cluster)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWhenRunningOnMasterEnabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_master.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationRunOnMaster: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.6.0",
		},
	}
	driver := portworx{}
	driver.SetDefaultsOnStorageCluster(cluster)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForCSIWithOlderCSIVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	// Should use 0.3 csi version for k8s version less than 1.13
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_csi_0.3.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSIDriverRegistrar: "quay.io/k8scsi/driver-registrar:v0.4.2",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Update Portworx version, which should use new CSI driver name
	cluster.Spec.Image = "portworx/oci-monitor:2.2"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[3],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithNewerCSIVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.2",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_csi_1.0.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Update Portworx version, which should use new CSI driver name
	cluster.Spec.Image = "portworx/oci-monitor:2.2"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithCustomPortworxImage(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}

	// PX_IMAGE env var gets precedence over spec.image.
	// We verify that by checking that CSI registrar is using old driver name.
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_IMAGE",
						Value: "portworx/oci-monitor:2.1.1-rc1",
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}
	nodeName := "testNode"

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If version cannot be found from the Portworx image tag, then check the annotation
	// for version. This is useful in testing when your image tag does not have version.
	cluster.Spec.Image = "portworx/oci-monitor:custom_oci_tag"
	cluster.Spec.Env[0].Value = "portworx/oci-monitor:custom_px_tag"
	cluster.Annotations = map[string]string{
		pxutil.AnnotationPXVersion: "2.1",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If valid version is not found from the image or the annotation, then assume latest
	// Portworx version. Verify this by checking the new CSI driver name in registrar.
	cluster.Annotations = map[string]string{
		pxutil.AnnotationPXVersion: "portworx/oci-monitor:invalid",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForDeprecatedCSIDriverName(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is true, use the old CSI driver name.
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_USEDEPRECATED_CSIDRIVERNAME",
						Value: "true",
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}
	nodeName := "testNode"

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME has true value, use old CSI driver name.
	cluster.Spec.Env[0].Value = "1"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is false, use new CSI driver name.
	cluster.Spec.Env[0].Value = "false"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is invalid, use new CSI driver name.
	cluster.Spec.Env[0].Value = "invalid_boolean"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithIncorrectKubernetesVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid-version",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	driver := portworx{}
	_, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.Error(t, err, "Expected an error on GetStoragePodSpec")
}

func TestPodSpecForKvdbAuthCerts(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				secretKeyKvdbCA:       []byte("kvdb-ca-file"),
				secretKeyKvdbCert:     []byte("kvdb-cert-file"),
				secretKeyKvdbCertKey:  []byte("kvdb-key-file"),
				secretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForPKSWithCSI(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.15.2",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_pks_with_csi.yaml")

	nodeName := "testNode"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.5.5",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAuthCertsWithoutCert(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				secretKeyKvdbCA:       []byte("kvdb-ca-file"),
				secretKeyKvdbCertKey:  []byte("kvdb-key-file"),
				secretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs_without_cert.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAuthCertsWithoutCA(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				secretKeyKvdbCert:     []byte("kvdb-cert-file"),
				secretKeyKvdbCertKey:  []byte("kvdb-key-file"),
				secretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs_without_ca.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAuthCertsWithoutKey(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				secretKeyKvdbCA:       []byte("kvdb-ca-file"),
				secretKeyKvdbCert:     []byte("kvdb-cert-file"),
				secretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs_without_key.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAclToken(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				secretKeyKvdbACLToken: []byte("kvdb-acl-token"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_without_certs.yaml")
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-acltoken", "kvdb-acl-token",
	}

	assert.ElementsMatch(t, expected.Volumes, actual.Volumes)
	assert.ElementsMatch(t, expected.Containers[0].VolumeMounts, actual.Containers[0].VolumeMounts)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecForKvdbUsernamePassword(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_without_certs.yaml")
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-userpwd", "kvdb-username:kvdb-password",
	}

	assert.ElementsMatch(t, expected.Volumes, actual.Volumes)
	assert.ElementsMatch(t, expected.Containers[0].VolumeMounts, actual.Containers[0].VolumeMounts)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecForKvdbAuthErrorReadingSecret(t *testing.T) {
	// Create fake client without kvdb auth secret
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_without_certs.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestIfStorageNodeExists(t *testing.T) {
	testCases := []struct {
		in       []*corev1.StorageNode
		nodeName string
		result   bool
	}{
		{
			in: []*corev1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "1",
			result:   true,
		},
		{
			in: []*corev1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "5",
			result:   false,
		},
		{
			in: []*corev1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "",
			result:   false,
		},
	}
	for _, tc := range testCases {
		assert.Equal(t, storageNodeExists(tc.nodeName, tc.in), tc.result)
	}
}

func TestStorageNodeConfig(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)

	_, yamlData := generateValidYamlData(t)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			UID:       "px-cluster-UID",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CloudStorage: &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					JournalDeviceSpec: stringPtr("type=journal")},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: "kube-system",
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
		cluster,
		&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "testNode"}},
		&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "testNode2"}},
		&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "testNode3"}},
	)
	nodeName := "testNode"
	nodeName2 := "testNode2"
	nodeName3 := "testNode3"

	zoneToInstancesMap := map[string]uint64{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	// Max storage nodes
	maxNodes := uint32(3)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		MaxStorageNodes: &maxNodes,
	}
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_drive_set_count", "3",
	}

	// TC 1, no CloudStorage CapacitySpecs
	actual, _ := driver.GetStoragePodSpec(cluster, nodeName)
	list, err := driver.storageNodesList(cluster)
	assert.NoError(t, err, "Error getting StorageNodeList")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Equal(t, 0, len(list), "no storage nodes are expected at this point")

	// Storage devices and auto-compute max_storage_nodes_per_zone
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=foo,size=120,iops=110,foo1=bar1",
		"-s", "type=bar,size=220,iops=210,foo3=bar3",
		"-max_storage_nodes_per_zone", "2",
	}

	inputInstancesPerZone := uint64(2)
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        uint64(len(zoneToInstancesMap)),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint64(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint64(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(210),
				},
			},
		}, nil)

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, len(list), 1, "expected storage nodes in list")

	expectedCloudStorageSpec := []corev1.StorageNodeCloudDriveConfig{
		{
			Type:      "foo",
			SizeInGiB: uint64(120),
			IOPS:      uint64(110),
			Options:   map[string]string{"foo1": "bar1"},
		},
		{
			Type:      "bar",
			SizeInGiB: uint64(220),
			IOPS:      uint64(210),
			Options: map[string]string{
				"foo3": "bar3",
			}},
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=foo,size=120,iops=110,foo1=bar1",
		"-s", "type=bar,size=220,iops=210,foo3=bar3",
		"-max_storage_nodes_per_zone", "2",
	}

	// despite adding a new node, node spec should be pulled from the existing storage node for nodeName
	// Storage node will be created for "nodeName1"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName2)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 2, len(list), "expected storage nodes in list")
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	for _, node := range list {
		assert.ElementsMatch(t, node.Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName3)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 3, len(list), "expected storage nodes in list")
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	for _, node := range list {
		assert.ElementsMatch(t, node.Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	}
}

func TestSecuritySetEnv(t *testing.T) {
	k8sClient := coreops.New(fakek8sclient.NewSimpleClientset())
	coreops.SetInstance(k8sClient)
	nodeName := "testNode"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
	}
	setSecuritySpecDefaults(cluster)

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	expectedJwtIssuer := "operator.portworx.io"
	var jwtIssuer string
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthJwtIssuer {
			jwtIssuer = env.Value
			break
		}
	}
	assert.Equal(t, expectedJwtIssuer, jwtIssuer)

	expectedJwtIssuer = "test.io"
	cluster.Spec.Security.Auth.SelfSigned.Issuer = stringPtr(expectedJwtIssuer)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	var sharedSecretSet bool
	var systemSecretSet bool
	var appsSecretSet bool
	var storkSecretSet bool
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthJwtIssuer {
			jwtIssuer = env.Value
		}
		if env.Name == pxutil.EnvKeyPortworxAuthJwtSharedSecret {
			sharedSecretSet = true
		}
		if env.Name == pxutil.EnvKeyPortworxAuthSystemKey {
			systemSecretSet = true
		}
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.Equal(t, expectedJwtIssuer, jwtIssuer)
	assert.True(t, sharedSecretSet)
	assert.True(t, systemSecretSet)
	assert.True(t, appsSecretSet)
	assert.False(t, storkSecretSet)

	// for px < 2.6, use stork secret env var
	cluster.Spec.Image = "portworx/image:2.5.0"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	storkSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork < 2.5 and px 2.6+, use stork secret env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:2.3.2",
		Enabled: true,
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	storkSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork < 2.5 and px 2.6+ with annotation, use stork secret env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Annotations = make(map[string]string)
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "2.3.2"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:abcde2_12313a",
		Enabled: true,
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	storkSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork >= 2.5 and px 2.6+, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:2.5.0",
		Enabled: true,
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for desired image stork >= 2.5 and px 2.6+, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	cluster.Status.DesiredImages = &corev1.ComponentImages{
		Stork: "openstorage/image:2.5.0",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork >= 2.5 and px 2.6+ with annotation, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:abcde2_12313a",
		Enabled: true,
	}
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "2.5.0"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork >= 2.5 and px 2.6+ with annotation, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:abcde2_12313a",
		Enabled: true,
	}
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "openstorage/image:abcde2_465313a"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork >= 2.5 and px 2.6+ with annotation, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image",
		Enabled: true,
	}
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "openstorage/image"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

}

func TestIKSEnvVariables(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsIKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.6.0",
		},
	}

	expectedPodIPEnv := v1.EnvVar{
		Name: "PX_POD_IP",
		ValueFrom: &v1.EnvVarSource{
			FieldRef: &v1.ObjectFieldSelector{
				APIVersion: "v1",
				FieldPath:  "status.podIP",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.Containers[0].Env, 7)
	var podIPEnv *v1.EnvVar
	for _, env := range actual.Containers[0].Env {
		if env.Name == "PX_POD_IP" {
			podIPEnv = env.DeepCopy()
		}
	}
	assert.Equal(t, expectedPodIPEnv, *podIPEnv)

	// TestCase: When IKS is not enabled
	cluster.Annotations[pxutil.AnnotationIsIKS] = "false"

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.Containers[0].Env, 6)
	podIPEnv = nil
	for _, env := range actual.Containers[0].Env {
		if env.Name == "PX_POD_IP" {
			podIPEnv = env.DeepCopy()
		}
	}
	assert.Nil(t, podIPEnv)
}

func TestPruneVolumes(t *testing.T) {
	spec := v1.PodSpec{
		Volumes: []v1.Volume{
			{Name: "foo"},
			{Name: "bar"},
			{Name: "foo-override"},
			{Name: "orphaned"},
		},
		Containers: []v1.Container{
			{
				Name: "containerA",
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "foo",
						MountPath: "/mnt/foo",
					},
					{
						Name:      "bar",
						MountPath: "/mnt/bar",
					},
				},
			},
			{
				Name: "containerB",
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "foo",
						MountPath: "/mnt/foo",
					},
					{
						Name:      "bar",
						MountPath: "/mnt/bar",
					},
					{
						Name:      "foo-override",
						MountPath: "/mnt/foo",
					},
				},
			},
			{
				Name: "containerC",
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "foo",
						MountPath: "/mnt/foo/",
					},
					{
						Name:      "foo",
						MountPath: "/mnt/foo////",
					},
					{
						Name:      "foo",
						MountPath: "/mnt/foo/bar/..",
					},
					{
						Name:      "foo",
						MountPath: "/mnt/foo",
					},
				},
			},
		},
	}

	testObj := &portworx{}
	testObj.pruneVolumes(&spec)

	expectedVolumes := []v1.Volume{
		{Name: "foo"},
		{Name: "bar"},
		{Name: "foo-override"},
	}
	assert.Equal(t, expectedVolumes, spec.Volumes)

	expectedMounts := []v1.VolumeMount{
		{
			Name:      "foo",
			MountPath: "/mnt/foo",
		},
		{
			Name:      "bar",
			MountPath: "/mnt/bar",
		},
	}
	require.Equal(t, "containerA", spec.Containers[0].Name)
	assert.Equal(t, expectedMounts, spec.Containers[0].VolumeMounts)

	expectedMounts = []v1.VolumeMount{
		{
			Name:      "bar",
			MountPath: "/mnt/bar",
		},
		{
			Name:      "foo-override",
			MountPath: "/mnt/foo",
		},
	}
	require.Equal(t, "containerB", spec.Containers[1].Name)
	assert.Equal(t, expectedMounts, spec.Containers[1].VolumeMounts)

	expectedMounts = []v1.VolumeMount{
		{
			Name:      "foo",
			MountPath: "/mnt/foo",
		},
	}
	require.Equal(t, "containerC", spec.Containers[2].Name)
	assert.Equal(t, expectedMounts, spec.Containers[2].VolumeMounts)
}

func getExpectedPodSpecFromDaemonset(t *testing.T, fileName string) *v1.PodSpec {
	json, err := ioutil.ReadFile(fileName)
	assert.NoError(t, err)

	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)

	ds, ok := obj.(*v1beta1.DaemonSet)
	assert.True(t, ok, "Expected daemon set object")

	return &ds.Spec.Template.Spec
}

func getExpectedPodSpec(t *testing.T, podSpecFileName string) *v1.PodSpec {
	json, err := ioutil.ReadFile(podSpecFileName)
	assert.NoError(t, err)

	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)

	p, ok := obj.(*v1.Pod)
	assert.True(t, ok, "Expected pod object")

	return &p.Spec
}

func assertPodSpecEqual(t *testing.T, expected, actual *v1.PodSpec) {
	assert.Equal(t, expected.Affinity, actual.Affinity)
	assert.Equal(t, expected.HostNetwork, actual.HostNetwork)
	assert.Equal(t, expected.RestartPolicy, actual.RestartPolicy)
	assert.Equal(t, expected.ServiceAccountName, actual.ServiceAccountName)
	assert.Equal(t, expected.ImagePullSecrets, actual.ImagePullSecrets)
	assert.Equal(t, expected.Tolerations, actual.Tolerations)
	assert.ElementsMatch(t, expected.Volumes, actual.Volumes)

	assert.Equal(t, len(expected.Containers), len(actual.Containers))
	for i, e := range expected.Containers {
		assertContainerEqual(t, e, actual.Containers[i])
	}
}

func assertContainerEqual(t *testing.T, expected, actual v1.Container) {
	assert.Equal(t, expected.Name, actual.Name)
	assert.Equal(t, expected.Image, actual.Image)
	assert.Equal(t, expected.ImagePullPolicy, actual.ImagePullPolicy)
	assert.Equal(t, expected.TerminationMessagePath, actual.TerminationMessagePath)
	assert.Equal(t, expected.LivenessProbe, actual.LivenessProbe)
	assert.Equal(t, expected.ReadinessProbe, actual.ReadinessProbe)
	assert.Equal(t, expected.SecurityContext, actual.SecurityContext)
	assert.ElementsMatch(t, expected.Args, actual.Args)
	assert.ElementsMatch(t, expected.Env, actual.Env)
	assert.ElementsMatch(t, expected.VolumeMounts, actual.VolumeMounts)
}
