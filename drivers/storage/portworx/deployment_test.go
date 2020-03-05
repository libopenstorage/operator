package portworx

import (
	"io/ioutil"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/cloudops"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
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
	expected := getExpectedPodSpec(t, "testspec/runc.yaml")
	nodeName := "testNode"

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
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWithImagePullSecrets(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

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
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
	assert.Len(t, actual.Containers[0].Env, 5)
	assert.Equal(t, expectedRegistryEnv, actual.Containers[0].Env[4])
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
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1alpha1.PlacementSpec{
				Tolerations: tolerations,
			},
		},
	}

	driver := portworx{}
	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.ElementsMatch(t, tolerations, podSpec.Tolerations)
}

func TestPodSpecWithKvdbSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

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

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Mgmt interface given but empty
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: stringPtr("data-intf"),
		MgmtInterface: stringPtr(""),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Data interface given but empty
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: stringPtr(""),
		MgmtInterface: stringPtr("mgmt-intf"),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Both data and mgmt interfaces are nil
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Network spec is nil
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithStorageSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

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

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// UseAll with UseAllWithPartitions
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No journal device if empty
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
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
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
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
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		SystemMdDevice: stringPtr(""),
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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty storage config
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{}
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

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			UID:       "px-cluster-UID",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CloudStorage: &corev1alpha1.CloudStorageSpec{
				JournalDeviceSpec: stringPtr("type=journal")},
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
	)
	nodeName := "testNode"

	zoneToInstancesMap := map[string]int{"a": 3, "b": 3, "c": 2}
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
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		JournalDeviceSpec: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

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

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty metadata device
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		SystemMdDeviceSpec: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage device specs
	devices := []string{"type=one", "type=two", "type=three"}
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		DeviceSpecs: &devices,
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
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		DeviceSpecs: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices and auto-compute max_storage_nodes_per_zone
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		CapacitySpecs: []corev1alpha1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint32(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint32(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	expectedCloudStorageSpec := []corev1alpha1.StorageNodeCloudDriveConfig{
		{
			Type:      "foo",
			SizeInGiB: uint64(120),
			IOPS:      uint32(110),
			Options:   map[string]string{"foo1": "bar1"},
		},
		{
			Type:      "bar",
			SizeInGiB: uint64(220),
			IOPS:      uint32(210),
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

	inputInstancesPerZone := 2
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        len(zoneToInstancesMap),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint32(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint32(200),
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
					IOPS:             uint32(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(210),
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
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		CapacitySpecs: []corev1alpha1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint32(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint32(200),
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
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil cloud config
	cluster.Spec.CloudStorage = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")

	// Nil cloud config but max storage nodes per zone is set
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_storage_nodes_per_zone", "2",
	}

	// Empty cloud config
	maxStorageNodesPerZone := uint32(2)
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		MaxStorageNodesPerZone: &maxStorageNodesPerZone,
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")
}

func TestPodSpecWithCapacitySpecsAndDeviceSpecs(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)

	_, yamlData := generateValidYamlData(t)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
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
	)

	// Provide both devices specs and cloud capacity specs
	deviceSpecs := []string{"type=one", "type=two"}

	nodeName := "testNode"

	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		DeviceSpecs: &deviceSpecs,
		CapacitySpecs: []corev1alpha1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint32(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint32(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	zoneToInstancesMap := map[string]int{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	inputInstancesPerZone := 2
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        len(zoneToInstancesMap),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint32(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint32(200),
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
					IOPS:             uint32(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(210),
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

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithSecretsProvider(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

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

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithImagePullPolicy(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			ImagePullPolicy: v1.PullIfNotPresent,
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "True",
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
	var cluster *corev1alpha1.StorageCluster
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

	cluster := &corev1alpha1.StorageCluster{
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
	expected := getExpectedPodSpec(t, "testspec/pks.yaml")

	nodeName := "testNode"

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
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestOpenshiftRuncPodSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpec(t, "testspec/openshift_runc.yaml")

	nodeName := "testNode"

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
	expected := getExpectedPodSpec(t, "testspec/px_csi_0.3.yaml")

	nodeName := "testNode"

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
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
		"--kubelet-registration-path=/var/lib/kubelet/plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithNewerCSIVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	// Should use 0.3 csi version for k8s version less than 1.13
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.2",
	}
	expected := getExpectedPodSpec(t, "testspec/px_csi_1.0.yaml")

	nodeName := "testNode"

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
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
		"--kubelet-registration-path=/var/lib/kubelet/plugins/pxd.portworx.com/csi.sock",
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
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_IMAGE",
						Value: "portworx/oci-monitor:2.1.1-rc1",
					},
				},
			},
		},
	}
	nodeName := "testNode"

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/plugins/com.openstorage.pxd/csi.sock",
	)

	// If version cannot be found from the Portworx image tag, then check the annotation
	// for version. This is useful in testing when your image tag does not have version.
	cluster.Spec.Image = "portworx/oci-monitor:custom_oci_tag"
	cluster.Spec.Env[0].Value = "portworx/oci-monitor:custom_px_tag"
	cluster.Annotations = map[string]string{
		annotationPXVersion: "2.1",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/plugins/com.openstorage.pxd/csi.sock",
	)

	// If valid version is not found from the image or the annotation, then assume latest
	// Portworx version. Verify this by checking the new CSI driver name in registrar.
	cluster.Annotations = map[string]string{
		annotationPXVersion: "portworx/oci-monitor:invalid",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForDeprecatedCSIDriverName(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is true, use the old CSI driver name.
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_USEDEPRECATED_CSIDRIVERNAME",
						Value: "true",
					},
				},
			},
		},
	}
	nodeName := "testNode"

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/plugins/com.openstorage.pxd/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME has true value, use old CSI driver name.
	cluster.Spec.Env[0].Value = "1"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/plugins/com.openstorage.pxd/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is false, use new CSI driver name.
	cluster.Spec.Env[0].Value = "false"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/plugins/pxd.portworx.com/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is invalid, use new CSI driver name.
	cluster.Spec.Env[0].Value = "invalid_boolean"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithIncorrectKubernetesVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid-version",
	}

	nodeName := "testNode"

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
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

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_certs.yaml")

	nodeName := "testNode"

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

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_certs_without_ca.yaml")

	nodeName := "testNode"

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
				secretKeyKvdbCert:     []byte("kvdb-cert-file"),
				secretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_certs_without_key.yaml")

	nodeName := "testNode"

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
				secretKeyKvdbUsername: []byte("kvdb-username"),
				secretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	nodeName := "testNode"

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
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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
	coreops.SetInstance(coreops.New(fakeClient))

	nodeName := "testNode"

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
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

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
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpec(t, "testspec/px_kvdb_without_certs.yaml")

	nodeName := "testNode"

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
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestIfStorageNodeExists(t *testing.T) {
	testCases := []struct {
		in       []*corev1alpha1.StorageNode
		nodeName string
		result   bool
	}{
		{
			in: []*corev1alpha1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "1",
			result:   true,
		},
		{
			in: []*corev1alpha1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "5",
			result:   false,
		},
		{
			in: []*corev1alpha1.StorageNode{
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

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			UID:       "px-cluster-UID",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CloudStorage: &corev1alpha1.CloudStorageSpec{
				JournalDeviceSpec: stringPtr("type=journal")},
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
	)
	nodeName := "testNode"
	nodeName2 := "testNode2"
	nodeName3 := "testNode3"

	zoneToInstancesMap := map[string]int{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	// Max storage nodes
	maxNodes := uint32(3)
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
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
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		CapacitySpecs: []corev1alpha1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint32(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint32(200),
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

	inputInstancesPerZone := 2
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        len(zoneToInstancesMap),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint32(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint32(200),
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
					IOPS:             uint32(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(210),
				},
			},
		}, nil)

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Equal(t, int32(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, len(list), 1, "expected storage nodes in list")

	expectedCloudStorageSpec := []corev1alpha1.StorageNodeCloudDriveConfig{
		{
			Type:      "foo",
			SizeInGiB: uint64(120),
			IOPS:      uint32(110),
			Options:   map[string]string{"foo1": "bar1"},
		},
		{
			Type:      "bar",
			SizeInGiB: uint64(220),
			IOPS:      uint32(210),
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
	assert.Equal(t, int32(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 2, len(list), "expected storage nodes in list")
	assert.Equal(t, int32(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	for _, node := range list {
		assert.ElementsMatch(t, node.Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName3)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 3, len(list), "expected storage nodes in list")
	assert.Equal(t, int32(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	for _, node := range list {
		assert.ElementsMatch(t, node.Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	}
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
