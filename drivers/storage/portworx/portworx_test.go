package portworx

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/openstorage/pkg/dbg"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/mock"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/kvdb"
	"github.com/portworx/kvdb/consul"
	e2 "github.com/portworx/kvdb/etcd/v2"
	e3 "github.com/portworx/kvdb/etcd/v3"
	"github.com/portworx/kvdb/mem"
	"github.com/portworx/sched-ops/k8s"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestString(t *testing.T) {
	driver := portworx{}
	require.Equal(t, driverName, driver.String())
}

func TestInit(t *testing.T) {
	driver := portworx{}
	k8sClient := testutil.FakeK8sClient()
	scheme := runtime.NewScheme()
	recorder := record.NewFakeRecorder(0)

	// Nil k8s client
	err := driver.Init(nil, scheme, recorder)
	require.EqualError(t, err, "kubernetes client cannot be nil")

	// Nil k8s scheme
	err = driver.Init(k8sClient, nil, recorder)
	require.EqualError(t, err, "kubernetes scheme cannot be nil")

	// Nil k8s event recorder
	err = driver.Init(k8sClient, scheme, nil)
	require.EqualError(t, err, "event recorder cannot be nil")

	// Valid k8s client
	err = driver.Init(k8sClient, scheme, recorder)
	require.NoError(t, err)
	require.Equal(t, k8sClient, driver.k8sClient)
}

func TestGetSelectorLabels(t *testing.T) {
	driver := portworx{}
	expectedLabels := map[string]string{labelKeyName: driverName}
	require.Equal(t, expectedLabels, driver.GetSelectorLabels())
}

func TestGetStorkDriverName(t *testing.T) {
	driver := portworx{}
	actualStorkDriverName, err := driver.GetStorkDriverName()
	require.NoError(t, err)
	require.Equal(t, storkDriverName, actualStorkDriverName)
}

func TestGetStorkEnvList(t *testing.T) {
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	envVars := driver.GetStorkEnvList(cluster)

	require.Len(t, envVars, 1)
	require.Equal(t, envKeyPortworxNamespace, envVars[0].Name)
	require.Equal(t, cluster.Namespace, envVars[0].Value)
}

func TestSetDefaultsOnStorageCluster(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedPlacement := &corev1alpha1.PlacementSpec{
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
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	// Use default image if could not get it from release manifest
	require.Equal(t, "portworx/oci-monitor:2.1.5", cluster.Spec.Image)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(defaultStartPort), *cluster.Spec.StartPort)
	require.True(t, *cluster.Spec.Storage.UseAll)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// Use default image from release manifest when spec.image has empty value
	manifestSetup()
	defer manifestCleanup()
	cluster.Spec.Image = "  "
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/oci-monitor:2.1.5.1", cluster.Spec.Image)

	// Don't use default image when spec.image has a value
	cluster.Spec.Image = "foo/image:1.0.0"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "foo/image:1.0.0", cluster.Spec.Image)

	// Populate version from image
	cluster.Spec.Image = "test/image:1.2.3"
	cluster.Spec.Version = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "1.2.3", cluster.Spec.Version)

	// Don't populate version from image if tag not present
	cluster.Spec.Image = "test/image"
	cluster.Spec.Version = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Version)

	// Empty kvdb spec should still set internal kvdb as default
	cluster.Spec.Kvdb = &corev1alpha1.KvdbSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)

	// Should not overwrite complete kvdb spec if endpoints are empty
	cluster.Spec.Kvdb = &corev1alpha1.KvdbSpec{
		AuthSecret: "test-secret",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, "test-secret", cluster.Spec.Kvdb.AuthSecret)

	// If endpoints are set don't set internal kvdb
	cluster.Spec.Kvdb = &corev1alpha1.KvdbSpec{
		Endpoints: []string{"endpoint1"},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.Kvdb.Internal)

	// Don't overwrite secrets provider if already set
	cluster.Spec.SecretsProvider = stringPtr("aws-kms")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "aws-kms", *cluster.Spec.SecretsProvider)

	// Don't overwrite secrets provider if set to empty
	cluster.Spec.SecretsProvider = stringPtr("")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "", *cluster.Spec.SecretsProvider)

	// Don't overwrite start port if already set
	startPort := uint32(10001)
	cluster.Spec.StartPort = &startPort
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, uint32(10001), *cluster.Spec.StartPort)

	// Do not use default storage config if cloud storage config present
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{}
	cluster.Spec.Storage = nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage)

	// Add default storage config if cloud storage and storage config are both present
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{}
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Storage.UseAll)

	// Do no use default storage config if devices is not nil
	devices := make([]string, 0)
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		Devices: &devices,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	devices = append(devices, "/dev/sda")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	// Do not set useAll if useAllWithPartitions is true
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	// Should set useAll if useAllWithPartitions is false
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAllWithPartitions: boolPtr(false),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Storage.UseAll)

	// Do not change useAll if already has a value
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll: boolPtr(false),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, *cluster.Spec.Storage.UseAll)

	// Add default placement if node placement is nil
	cluster.Spec.Placement = &corev1alpha1.PlacementSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestStorageClusterDefaultsForLighthouse(t *testing.T) {
	manifestSetup()
	defer manifestCleanup()

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable lighthouse if nothing specified in the user interface spec
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface)

	// Don't use default Lighthouse image if disabled
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled: false,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)

	// Use default Lighthouse image from release manifest if no image present
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/px-lighthouse:2.3.4", cluster.Spec.UserInterface.Image)

	// Use default Lighthouse image from release manifest if empty image present
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled: true,
		Image:   "  ",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/px-lighthouse:2.3.4", cluster.Spec.UserInterface.Image)

	// Use default Lighthouse image from release manifest even if there is one present
	// in the spec, but if that image is not locked
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled: true,
		Image:   "custom/lighthouse-image:1.2.3",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/px-lighthouse:2.3.4", cluster.Spec.UserInterface.Image)

	// Don't use default Lighthouse image from release manifest even if there is one
	// present in the spec and it is locked by the user
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled:   true,
		Image:     "custom/lighthouse-image:1.2.3",
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "custom/lighthouse-image:1.2.3", cluster.Spec.UserInterface.Image)

	// Use hardcoded default Lighthouse image if not present in release manifest and
	// in the spec
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled: true,
		Image:   "",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultLighthouseImage, cluster.Spec.UserInterface.Image)

	// Use hardcoded default Lighthouse image if not present in release manifest and
	// an empty image in the spec
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled: true,
		Image:   "   ",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultLighthouseImage, cluster.Spec.UserInterface.Image)

	// Use hardcoded default Lighthouse image if not present in release manifest and
	// an empty image in the spec, even if it's locked
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{
		Enabled:   true,
		Image:     "  ",
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultLighthouseImage, cluster.Spec.UserInterface.Image)
}

func TestStorageClusterDefaultsForAutopilot(t *testing.T) {
	manifestSetup()
	defer manifestCleanup()

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable autopilot if nothing specified in the user interface spec
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot)

	// Don't use default Autopilot image if disabled
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled: false,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)

	// Use default Autopilot image from release manifest if no image present
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/autopilot:2.3.4", cluster.Spec.Autopilot.Image)

	// Use default Autopilot image from release manifest if empty image present
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled: true,
		Image:   "  ",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/autopilot:2.3.4", cluster.Spec.Autopilot.Image)

	// Use default Autopilot image from release manifest even if there is one present
	// in the spec, but if that image is not locked
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled: true,
		Image:   "custom/autopilot-image:1.2.3",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/autopilot:2.3.4", cluster.Spec.Autopilot.Image)

	// Don't use default Autopilot image from release manifest even if there is one
	// present in the spec and it is locked by the user
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled:   true,
		Image:     "custom/autopilot-image:1.2.3",
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "custom/autopilot-image:1.2.3", cluster.Spec.Autopilot.Image)

	// Use hardcoded default Autopilot image if not present in release manifest and
	// in the spec
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled: true,
		Image:   "",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultAutopilotImage, cluster.Spec.Autopilot.Image)

	// Use hardcoded default Autopilot image if not present in release manifest and
	// an empty image in the spec
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled: true,
		Image:   "   ",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultAutopilotImage, cluster.Spec.Autopilot.Image)

	// Use hardcoded default Autopilot image if not present in release manifest and
	// an empty image in the spec, even if it's locked
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.Autopilot = &corev1alpha1.AutopilotSpec{
		Enabled:   true,
		Image:     "  ",
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultAutopilotImage, cluster.Spec.Autopilot.Image)
}

func TestStorageClusterDefaultsForStork(t *testing.T) {
	manifestSetup()
	defer manifestCleanup()

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Stork should be enabled by default
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Stork.Enabled)

	// Don't use default Stork image if disabled
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: false,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)

	// Use default Stork image from release manifest if no image present
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "openstorage/stork:2.3.4", cluster.Spec.Stork.Image)

	// Use default Stork image from release manifest if empty image present
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
		Image:   "  ",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "openstorage/stork:2.3.4", cluster.Spec.Stork.Image)

	// Use default Stork image from release manifest even if there is one present
	// in the spec, but if that image is not locked
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
		Image:   "custom/stork-image:1.2.3",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "openstorage/stork:2.3.4", cluster.Spec.Stork.Image)

	// Don't use default Stork image from release manifest even if there is one
	// present in the spec and it is locked by the user
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled:   true,
		Image:     "custom/stork-image:1.2.3",
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "custom/stork-image:1.2.3", cluster.Spec.Stork.Image)

	// Use hardcoded default Stork image if not present in release manifest and
	// in the spec
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
		Image:   "",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultStorkImage, cluster.Spec.Stork.Image)

	// Use hardcoded default Stork image if not present in release manifest and
	// an empty image in the spec
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
		Image:   "   ",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultStorkImage, cluster.Spec.Stork.Image)

	// Use hardcoded default Stork image if not present in release manifest and
	// an empty image in the spec, even if it's locked
	cluster.Spec.Image = "px/image:2.1.4"
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled:   true,
		Image:     "  ",
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultStorkImage, cluster.Spec.Stork.Image)
}

func TestStorageClusterDefaultsForNodeSpecs(t *testing.T) {
	manifestSetup()
	defer manifestCleanup()

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Node specs should be nil if already nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Nodes)

	// Node specs should be empty if already empty
	cluster.Spec.Nodes = make([]corev1alpha1.NodeSpec, 0)
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Len(t, cluster.Spec.Nodes, 0)

	// Empty storage spec at node level should copy spec from cluster level
	// - If cluster level config is empty, we should use the default storage config
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{{}}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, &corev1alpha1.StorageSpec{UseAll: boolPtr(true)}, cluster.Spec.Nodes[0].Storage)

	// - If cluster level config is not empty, use it as is
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{{}}
	clusterStorageSpec := &corev1alpha1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	cluster.Spec.Storage = clusterStorageSpec.DeepCopy()
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, clusterStorageSpec, cluster.Spec.Nodes[0].Storage)

	// Do not set node spec storage fields if not set at the cluster level
	cluster.Spec.Storage = nil
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{
		{
			Storage: &corev1alpha1.StorageSpec{},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.SystemMdDevice)

	// Set node spec storage fields from cluster storage spec, if empty at node level
	clusterDevices := []string{"dev1", "dev2"}
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
		Devices:              &clusterDevices,
		ForceUseDisks:        boolPtr(true),
		JournalDevice:        stringPtr("journal"),
		SystemMdDevice:       stringPtr("metadata"),
	}
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{
		{
			Storage: &corev1alpha1.StorageSpec{},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, clusterDevices, *cluster.Spec.Nodes[0].Storage.Devices)
	require.Equal(t, "journal", *cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Equal(t, "metadata", *cluster.Spec.Nodes[0].Storage.SystemMdDevice)

	// Set node spec storage fields from cluster storage spec, if empty at node level
	nodeDevices := []string{"node-dev1", "node-dev2"}
	cluster.Spec.Nodes[0].Storage = &corev1alpha1.StorageSpec{
		UseAll:               boolPtr(false),
		UseAllWithPartitions: boolPtr(false),
		Devices:              &nodeDevices,
		ForceUseDisks:        boolPtr(false),
		JournalDevice:        stringPtr("node-journal"),
		SystemMdDevice:       stringPtr("node-metadata"),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.False(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, nodeDevices, *cluster.Spec.Nodes[0].Storage.Devices)
	require.Equal(t, "node-journal", *cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Equal(t, "node-metadata", *cluster.Spec.Nodes[0].Storage.SystemMdDevice)
}

func TestSetDefaultsOnStorageClusterForOpenshift(t *testing.T) {
	manifestSetup()
	defer manifestCleanup()

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsOpenshift: "true",
			},
		},
	}

	expectedPlacement := &corev1alpha1.PlacementSpec{
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
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(defaultStartPort), *cluster.Spec.StartPort)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestSetDefaultsOnStorageClusterOnError(t *testing.T) {
	manifestSetup()
	defer manifestCleanup()

	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid",
	}
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
			},
		},
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	// Should not set the defaults if there is an internal error
	require.Nil(t, cluster.Spec.Kvdb)
	require.Nil(t, cluster.Spec.SecretsProvider)
	require.Nil(t, cluster.Spec.StartPort)
	require.Nil(t, cluster.Spec.Placement)
	require.Nil(t, cluster.Spec.Stork)
	require.Empty(t, cluster.Spec.UserInterface.Image)

	// Should set default portworx image irrespective of the error
	require.Equal(t, "portworx/oci-monitor:2.1.5.1", cluster.Spec.Image)
}

func TestUpdateClusterStatusFirstTime(t *testing.T) {
	driver := portworx{}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	// Status should be set to initializing if not set
	require.Equal(t, cluster.Name, cluster.Status.ClusterName)
	require.Equal(t, "Initializing", cluster.Status.Phase)
}

func TestUpdateClusterStatus(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:   "cluster-id",
			Name: "cluster-name",
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "cluster-name", cluster.Status.ClusterName)
	require.Equal(t, "cluster-id", cluster.Status.ClusterUID)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Init
	expectedClusterResp.Cluster.Status = api.Status_STATUS_INIT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Offline
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OFFLINE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Error
	expectedClusterResp.Cluster.Status = api.Status_STATUS_ERROR
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Decommission
	expectedClusterResp.Cluster.Status = api.Status_STATUS_DECOMMISSION
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Unknown", cluster.Status.Phase)

	// Status Maintenance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_MAINTENANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status NeedsReboot
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NEEDS_REBOOT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status NotInQuorum
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status NotInQuorumNoStorage
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status Ok
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDown
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DOWN
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDegraded
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageRebalance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDriveReplace
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status Invalid
	expectedClusterResp.Cluster.Status = api.Status(9999)
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Unknown", cluster.Status.Phase)
}

func TestUpdateClusterStatusForNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "10.0.1.1",
		MgmtIp:            "10.0.1.2",
		Status:            api.Status_STATUS_NONE,
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
		DataIp:            "10.0.2.1",
		MgmtIp:            "10.0.2.2",
		Status:            api.Status_STATUS_OK,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Status None
	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-1", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.1.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.1.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeState, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-2", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.2.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.2.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeState, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Return only one node in enumerate for future tests
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne},
	}

	// Status Init
	expectedNodeOne.Status = api.Status_STATUS_INIT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Initializing", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeInit, nodeStatus.Status.Conditions[0].Status)

	// Status Offline
	expectedNodeOne.Status = api.Status_STATUS_OFFLINE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status Error
	expectedNodeOne.Status = api.Status_STATUS_ERROR
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorum
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeNotInQuorum, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorumNoStorage
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeNotInQuorum, nodeStatus.Status.Conditions[0].Status)

	// Status NeedsReboot
	expectedNodeOne.Status = api.Status_STATUS_NEEDS_REBOOT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status Decommission
	expectedNodeOne.Status = api.Status_STATUS_DECOMMISSION
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Decommissioned", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeDecommissioned, nodeStatus.Status.Conditions[0].Status)

	// Status Maintenance
	expectedNodeOne.Status = api.Status_STATUS_MAINTENANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Maintenance", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeMaintenance, nodeStatus.Status.Conditions[0].Status)

	// Status Ok
	expectedNodeOne.Status = api.Status_STATUS_OK
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDown
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DOWN
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDegraded
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status StorageRebalance
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDriveReplace
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status Invalid
	expectedNodeOne.Status = api.Status(9999)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Unknown", nodeStatus.Status.Phase)
	require.Equal(t, corev1alpha1.NodeUnknown, nodeStatus.Status.Conditions[0].Status)
}

func TestUpdateClusterStatusForNodeVersions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "test/image:1.2.3.4",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		NodeLabels: map[string]string{
			"PX Version": "5.6.7.8",
		},
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "5.6.7.8", nodeStatus.Spec.Version)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", nodeStatus.Spec.Version)

	// If the PX imgae does not have a tag then don't add version to status object
	cluster.Spec.Image = "test/image"

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, nodeStatus.Spec.Version)
}

func TestUpdateClusterStatusWithoutPortworxService(t *testing.T) {
	// Fake client without service
	k8sClient := testutil.FakeK8sClient()

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestUpdateClusterStatusServiceWithoutClusterIP(t *testing.T) {
	// Fake client with a service that does not have cluster ip
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to get endpoint")
}

func TestUpdateClusterStatusServiceGrpcServerError(t *testing.T) {
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "127.0.0.1",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error connecting to GRPC server")
}

func TestUpdateClusterStatusInspectClusterFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	// Error from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, fmt.Errorf("InspectCurrent error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "InspectCurrent error")

	// Nil response from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to inspect cluster")

	// Nil cluster object in the response of InspectCurrent API
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: nil,
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty ClusterInspect response")
}

func TestUpdateClusterStatusEnumerateNodesFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Error from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nil, fmt.Errorf("node Enumerate error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "node Enumerate error")

	// Nil response from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nil, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to enumerate nodes")

	// Empty list of nodes should not create any StorageNode objects
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Nil list of nodes should not create any StorageNode objects
	expectedNodeEnumerateResp.Nodes = nil
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)
}

func TestUpdateClusterStatusShouldUpdateStatusIfChanged(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_ERROR,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	expectedNode := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "1.1.1.1",
		Status:            api.Status_STATUS_MAINTENANCE,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNode},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Offline", cluster.Status.Phase)
	nodeStatusList := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha1.NodeMaintenance, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "1.1.1.1", nodeStatusList.Items[0].Status.Network.DataIP)

	// Update status based on the latest object
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	expectedNode.Status = api.Status_STATUS_OK
	expectedNode.DataIp = "2.2.2.2"
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Online", cluster.Status.Phase)
	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "2.2.2.2", nodeStatusList.Items[0].Status.Network.DataIP)
}

func TestUpdateClusterStatusWithoutSchedulerNodeName(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:       "node-uid",
				DataIp:   "1.1.1.1",
				MgmtIp:   "2.2.2.2",
				Hostname: "node-hostname",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Fake a node object without matching ip address or hostname to storage node
	k8s.Instance().SetBaseClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
		}),
	)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Fake a node object with matching data ip address
	k8s.Instance().SetBaseClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeInternalIP,
						Address: "1.1.1.1",
					},
				},
			},
		}),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching mgmt ip address
	testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetBaseClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeExternalIP,
						Address: "2.2.2.2",
					},
				},
			},
		}),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname
	testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetBaseClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeHostName,
						Address: "node-hostname",
					},
				},
			},
		}),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname from labels
	testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetBaseClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
				Labels: map[string]string{
					"kubernetes.io/hostname": "node-hostname",
				},
			},
		}),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldDeleteStatusForNonExistingNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Node got removed from storage driver
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldDeleteStatusIfSchedulerNodeNameNotPresent(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Scheduler node name missing for storage node
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id: "node-1",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)

	// Deleting already deleted StorageNode should not throw error
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestDeleteClusterWithoutDeleteStrategy(t *testing.T) {
	driver := portworx{
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiApplicationCreated:             true,
		csiNodeInfoCRDCreated:             true,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// If no delete strategy is provided, condition should be complete
	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Reason)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiApplicationCreated)
	require.False(t, driver.csiNodeInfoCRDCreated)
}

func TestDeleteClusterWithUninstallStrategy(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{
		k8sClient:                         k8sClient,
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiApplicationCreated:             true,
		csiNodeInfoCRDCreated:             true,
	}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiApplicationCreated)
	require.False(t, driver.csiNodeInfoCRDCreated)

	// Check condition
	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiper.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithCustomRepoRegistry(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			CustomImageRegistry: customRepo,
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/px-node-wiper:2.1.2-rc1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithCustomRegistry(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	customRegistry := "test-registry:1111"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/portworx/px-node-wiper:2.1.2-rc1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithCustomNodeWiperImage(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	customRegistry := "test-registry:1111"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallStorageClusterStrategyType,
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyNodeWiperImage,
						Value: "test/node-wiper:v1",
					},
				},
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/test/node-wiper:v1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithUninstallStrategyForPKS(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{
		k8sClient:                         k8sClient,
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiApplicationCreated:             true,
	}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsPKS: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiApplicationCreated)

	// Check condition
	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiperPKS.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithUninstallAndWipeStrategy(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{
		k8sClient:                         k8sClient,
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiApplicationCreated:             true,
	}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiApplicationCreated)

	// Check condition
	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiperWithWipe.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithNodeAffinity(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
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
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check wiper daemonset
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
	require.Equal(t, cluster.Spec.Placement.NodeAffinity, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
}

func TestDeleteClusterWithUninstallWhenNodeWiperCreated(t *testing.T) {
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS)
	driver := portworx{
		k8sClient: k8sClient,
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallMsg)
}

func TestDeleteClusterWithUninstallWipeStrategyWhenNodeWiperCreated(t *testing.T) {
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS)
	driver := portworx{
		k8sClient: k8sClient,
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveConfigMaps(t *testing.T) {
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	etcdConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      internalEtcdConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	cloudDriveConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cloudDriveConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS, wiperPod, etcdConfigMap, cloudDriveConfigMap)
	driver := portworx{
		k8sClient: k8sClient,
	}

	configMaps := &v1.ConfigMapList{}
	err := testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Len(t, configMaps.Items, 2)

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	// Check config maps are deleted
	configMaps = &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Empty(t, configMaps.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveKvdbData(t *testing.T) {
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Kvdb: &corev1alpha1.KvdbSpec{
				Endpoints: []string{
					"etcd://kvdb1.com:2001",
					"etcd://kvdb2.com:2001",
				},
			},
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Test etcd v3 without http/https
	kvdbMem, err := kvdb.New(mem.Name, pxKvdbPrefix, nil, nil, dbg.Panicf)
	require.NoError(t, err)
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err := kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit http
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:http://kvdb1.com:2001",
		"etcd:http://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit https
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd base version
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdBaseVersion, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e2.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test consul
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.ConsulVersion1, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"consul:http://kvdb1.com:2001",
		"consul:http://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, consul.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)
}

func TestDeleteClusterWithUninstallWipeStrategyFailedRemoveKvdbData(t *testing.T) {
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Kvdb: &corev1alpha1.KvdbSpec{
				Endpoints: []string{},
			},
			DeleteStrategy: &corev1alpha1.StorageClusterDeleteStrategy{
				Type: corev1alpha1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Fail if no kvdb endpoints given
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return kvdb.New(mem.Name, prefix, machines, opts, dbg.Panicf)
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if unknown kvdb type given in url
	cluster.Spec.Kvdb.Endpoints = []string{"zookeeper://kvdb.com:2001"}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if unknown kvdb version found
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "zookeeper1", nil
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if error getting kvdb version
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "", fmt.Errorf("kvdb version error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")
	require.Contains(t, condition.Reason, "kvdb version error")

	// Fail if error initializing kvdb
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return nil, fmt.Errorf("kvdb initialize error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")
	require.Contains(t, condition.Reason, "kvdb initialize error")
}

func fakeClientWithWiperPod(namespace string) client.Client {
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	return testutil.FakeK8sClient(wiperDS, wiperPod)
}

func manifestSetup() {
	os.Setenv(manifest.EnvKeyReleaseManifestURL, "foo")
	os.RemoveAll(manifest.ManifestDir)
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/testspec",
	)
	os.Symlink(linkPath, manifest.ManifestDir)
}

func manifestCleanup() {
	os.Unsetenv(manifest.EnvKeyReleaseManifestURL)
	os.RemoveAll(manifest.ManifestDir)
}
