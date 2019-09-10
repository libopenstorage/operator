package portworx

import (
	"io/ioutil"
	"testing"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/portworx/sched-ops/k8s"
	"github.com/stretchr/testify/assert"
	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestBasicRuncPodSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	expected := getExpectedPodSpec(t, "testspec/runc.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1alpha1.PlacementSpec{
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
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
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
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWithImagePullSecrets(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
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
	actual := driver.GetStoragePodSpec(cluster)

	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
	assert.Len(t, actual.Containers[0].Env, 4)
	assert.Equal(t, expectedRegistryEnv, actual.Containers[0].Env[3])
}

func TestPodSpecWithKvdbSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Kvdb: &corev1alpha1.KvdbSpec{
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

	actual := driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should have both bootstrap and endpoints
	cluster.Spec.Kvdb.Internal = true
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-k", "endpoint-1,endpoint-2,endpoint-3",
	}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should take bootstrap only if endpoints is nil
	cluster.Spec.Kvdb.Endpoints = nil
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
	}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should take bootstrap only if endpoints are empty
	cluster.Spec.Kvdb.Endpoints = []string{}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithNetworkSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1alpha1.CommonConfig{
				Network: &corev1alpha1.NetworkSpec{
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

	actual := driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Only data interface is given
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: stringPtr("data-intf"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-d", "data-intf",
	}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Mgmt interface given but empty
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: stringPtr("data-intf"),
		MgmtInterface: stringPtr(""),
	}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Only management interface is given
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		MgmtInterface: stringPtr("mgmt-intf"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-m", "mgmt-intf",
	}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Data interface given but empty
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: stringPtr(""),
		MgmtInterface: stringPtr("mgmt-intf"),
	}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Both data and mgmt interfaces are empty
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: stringPtr(""),
		MgmtInterface: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Both data and mgmt interfaces are nil
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Network spec is nil
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{}

	actual = driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithStorageSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
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

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// UseAllWithPartitions
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-A",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// UseAll with UseAllWithPartitions
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// ForceUseDisks
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		ForceUseDisks: boolPtr(true),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-f",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Journal device
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		JournalDevice: stringPtr("/dev/journal"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "/dev/journal",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No journal device if empty
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		JournalDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Metadata device
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		SystemMdDevice: stringPtr("/dev/metadata"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-metadata", "/dev/metadata",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No metadata device if empty
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		SystemMdDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices
	devices := []string{"/dev/one", "/dev/two", "/dev/three"}
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
		"-s", "/dev/two",
		"-s", "/dev/three",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices empty
	devices = []string{}
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Explicit storage devices get priority over UseAll
	devices = []string{"/dev/one"}
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll:  boolPtr(true),
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Explicit storage devices get priority over UseAllWithPartition
	devices = []string{"/dev/one"}
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
		Devices:              &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty storage config
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil storage config
	cluster.Spec.Storage = nil

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithCloudStorageSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CloudStorage: &corev1alpha1.CloudStorageSpec{
				JournalDeviceSpec: stringPtr("type=journal"),
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "type=journal",
	}

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty journal device
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		JournalDeviceSpec: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Metadata device
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		SystemMdDeviceSpec: stringPtr("type=metadata"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-metadata", "type=metadata",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty metadata device
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		SystemMdDeviceSpec: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Max storage nodes
	maxNodes := uint32(3)
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		MaxStorageNodes: &maxNodes,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_drive_set_count", "3",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Max storage nodes per zone
	maxNodesPerZone := uint32(1)
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		MaxStorageNodesPerZone: &maxNodesPerZone,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_storage_nodes_per_zone", "1",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices
	devices := []string{"type=one", "type=two"}
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		DeviceSpecs: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=one",
		"-s", "type=two",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices empty
	devices = []string{}
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		DeviceSpecs: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty storage config
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil storage config
	cluster.Spec.CloudStorage = nil

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithStorageAndCloudStorageSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					JournalDevice: stringPtr("/dev/journal"),
				},
			},
			CloudStorage: &corev1alpha1.CloudStorageSpec{
				JournalDeviceSpec: stringPtr("type=journal"),
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

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithSecretsProvider(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			SecretsProvider: stringPtr("k8s"),
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-secret_type", "k8s",
	}

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the secrets provider if empty
	cluster.Spec.SecretsProvider = stringPtr("")
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the secrets provider if nil
	cluster.Spec.SecretsProvider = nil

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithCustomStartPort(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	startPort := uint32(10001)
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image:     "portworx/oci-monitor:2.1.1",
			StartPort: &startPort,
		},
	}
	driver := portworx{}

	expected := getExpectedPodSpec(t, "testspec/portworxPodCustomPort.yaml")

	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)

	// Don't set the start port if same as default start port
	startPort = uint32(defaultStartPort)
	cluster.Spec.StartPort = &startPort
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the start port if nil
	cluster.Spec.StartPort = nil

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithLogAnnotation(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationLogFile: "/tmp/log",
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--log", "/tmp/log",
	}

	actual := driver.GetStoragePodSpec(cluster)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithRuntimeOptions(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			CommonConfig: corev1alpha1.CommonConfig{
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

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// For invalid value key should not be added. Only numeric values are supported.
	cluster.Spec.RuntimeOpts["invalid"] = "non-numeric"

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty runtime map should not add any options
	cluster.Spec.RuntimeOpts = make(map[string]string)
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil runtime map should not add any options
	cluster.Spec.RuntimeOpts = nil

	actual = driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithMiscArgs(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationMiscArgs: "-fruit apple -person john",
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

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithInvalidMiscArgs(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationMiscArgs: "'-unescaped-quote",
			},
		},
	}
	driver := portworx{}

	// Don't add misc args if they are invalid
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithImagePullPolicy(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			ImagePullPolicy: v1.PullIfNotPresent,
			FeatureGates: map[string]string{
				string(FeatureCSI): "True",
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

	actual := driver.GetStoragePodSpec(cluster)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Len(t, actual.Containers, 2)
	assert.Equal(t, v1.PullIfNotPresent, actual.Containers[0].ImagePullPolicy)
	assert.Equal(t, v1.PullIfNotPresent, actual.Containers[1].ImagePullPolicy)
}

func TestPodSpecWithNilStorageCluster(t *testing.T) {
	var cluster *corev1alpha1.StorageCluster
	driver := portworx{}

	actual := driver.GetStoragePodSpec(cluster)
	assert.Equal(t, v1.PodSpec{}, actual)
}

func TestPodSpecWithInvalidKubernetesVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid-version",
	}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
	}
	driver := portworx{}

	actual := driver.GetStoragePodSpec(cluster)
	assert.Equal(t, v1.PodSpec{}, actual)
}

func TestPKSPodSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	expected := getExpectedPodSpec(t, "testspec/pks.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationIsPKS: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1alpha1.PlacementSpec{
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
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func TestOpenshiftRuncPodSpec(t *testing.T) {
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(),
		nil, nil, nil, nil, nil, nil,
	)
	expected := getExpectedPodSpec(t, "testspec/openshift_runc.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationIsOpenshift: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1alpha1.PlacementSpec{
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
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForCSIWithKubernetesVersionLessThan_1_11(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.10.9",
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(FeatureCSI): "true",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	// Spec should be empty
	assertPodSpecEqual(t, &v1.PodSpec{}, &actual)
}

func TestPodSpecForCSIWithOlderCSIVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	// Should use 0.3 csi version for k8s version less than 1.13
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}
	expected := getExpectedPodSpec(t, "testspec/px_csi_0.3.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(FeatureCSI): "true",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForCSIWithNewerCSIVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	// Should use 0.3 csi version for k8s version less than 1.13
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.2",
	}
	expected := getExpectedPodSpec(t, "testspec/px_csi_1.0.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(FeatureCSI): "true",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForCSIWithIncorrectKubernetesVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid-version",
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(FeatureCSI): "true",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	// Spec should be empty
	assertPodSpecEqual(t, &v1.PodSpec{}, &actual)
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
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_certs.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1alpha1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

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
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_certs_without_ca.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1alpha1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

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
				secretKeyKvdbCert:     []byte("kvdb-cert-file"),
				secretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_certs_without_key.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1alpha1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

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
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1alpha1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_without_certs.yaml")
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
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1alpha1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_without_certs.yaml")
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
	k8s.Instance().SetClient(fakeClient, nil, nil, nil, nil, nil, nil)

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_without_certs.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1alpha1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func getExpectedPodSpec(t *testing.T, fileName string) *v1.PodSpec {
	json, err := ioutil.ReadFile(fileName)
	assert.NoError(t, err)

	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)

	ds, ok := obj.(*v1beta1.DaemonSet)
	assert.True(t, ok, "Expected daemon set object")

	return &ds.Spec.Template.Spec
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
