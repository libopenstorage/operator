// +build integrationtest

package integrationtest

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/libopenstorage/operator/drivers/storage/portworx"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	appsops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/portworx/sched-ops/k8s/operator"
)

const (
	labelKeySkipPX = "skip/px"
	labelValueTrue = "true"
)

var (
	pxVer2_9, _ = version.NewVersion("2.9")
)

var testStorageClusterBasicCases = []types.TestCase{
	{
		TestName:        "InstallWithAllDefaults",
		TestrailCaseIDs: []string{},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "simple-install"},
		}),
		TestFunc: BasicInstall,
	},
	{
		TestName:        "NodeAffinityLabels",
		TestrailCaseIDs: []string{},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "node-affinity-labels"},
			Spec: corev1.StorageClusterSpec{
				Placement: &corev1.PlacementSpec{
					NodeAffinity: &v1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
							NodeSelectorTerms: []v1.NodeSelectorTerm{
								{
									MatchExpressions: []v1.NodeSelectorRequirement{
										{
											Key:      labelKeySkipPX,
											Operator: v1.NodeSelectorOpNotIn,
											Values:   []string{labelValueTrue},
										},
									},
								},
							},
						},
					},
				},
			},
		}),
		TestFunc: BasicInstallWithNodeAffinity,
	},
	{
		TestName:        "Upgrade",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &corev1.StorageCluster{
				ObjectMeta: meta.ObjectMeta{Name: "upgrade-test"},
			}
		},
		ShouldSkip: func() bool {
			k8sVersion, _ := k8sutil.GetVersion()
			pxVersion := ci_utils.GetPxVersionFromSpecGenURL(ci_utils.PxUpgradeHopsURLList[0])
			return k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) && pxVersion.LessThan(pxVer2_9)
		},
		TestFunc: BasicUpgrade,
	},
	{
		TestName:        "InstallWithTelemetry",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "telemetry-test"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)
			cluster.Spec.Monitoring = &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			}
			return cluster
		},
		ShouldSkip: func() bool { return false },
		TestFunc:   InstallWithTelemetry,
	},
	{
		TestName:        "BasicCsiRegression",
		TestrailCaseIDs: []string{},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "csi-regression-test"},
		}),
		ShouldSkip: func() bool { return false },
		TestFunc:   BasicCsiRegression,
	},
	{
		TestName:        "BasicStorkRegression",
		TestrailCaseIDs: []string{},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "stork-regression-test"},
		}),
		ShouldSkip: func() bool { return false },
		TestFunc:   BasicStorkRegression,
	},
}

func TestStorageClusterBasic(t *testing.T) {
	for _, testCase := range testStorageClusterBasicCases {
		testCase.RunTest(t)
	}
}

func BasicInstall(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func BasicInstallWithNodeAffinity(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		// Get K8S nodes
		nodeList, err := coreops.Instance().GetNodes()
		require.NoError(t, err)

		// Set Node Affinity label one of the K8S nodes
		var nodeNameWithLabel string
		for _, node := range nodeList.Items {
			if coreops.Instance().IsNodeMaster(node) {
				continue // Skip master node, we don't need to label it
			}
			logrus.Infof("Label node %s with %s=%s", node.Name, labelKeySkipPX, labelValueTrue)
			if err := coreops.Instance().AddLabelOnNode(node.Name, labelKeySkipPX, labelValueTrue); err != nil {
				require.NoError(t, err)
			}
			nodeNameWithLabel = node.Name
			break
		}

		// Run basic install test and validation
		BasicInstall(tc)(t)

		// Remove Node Affinity label from the node
		logrus.Infof("Remove label %s from node %s", nodeNameWithLabel, labelKeySkipPX)
		if err := coreops.Instance().RemoveLabelOnNode(nodeNameWithLabel, labelKeySkipPX); err != nil {
			require.NoError(t, err)
		}
	}
}

func BasicUpgrade(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		// Get the storage cluster to start with
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		var lastHopURL string
		for i, hopURL := range ci_utils.PxUpgradeHopsURLList {
			// Get versions from URL
			logrus.Infof("Get component images from version URL")
			specImages, err := testutil.GetImagesFromVersionURL(hopURL)
			require.NoError(t, err)
			if i == 0 {
				// Deploy cluster
				logrus.Infof("Deploying starting cluster using %s", hopURL)
				err := ci_utils.ConstructStorageCluster(cluster, hopURL, specImages)
				require.NoError(t, err)
				cluster = ci_utils.DeployAndValidateStorageCluster(cluster, specImages, t)
			} else {
				logrus.Infof("Upgrading from %s to %s", lastHopURL, hopURL)
				// Get live StorageCluster
				cluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
				require.NoError(t, err)

				// Set Portworx Image
				cluster.Spec.Image = specImages["version"]

				// Set defaults
				portworx.SetPortworxDefaults(cluster)

				// Update live StorageCluster
				cluster, err = ci_utils.UpdateStorageCluster(cluster)
				require.NoError(t, err)
				logrus.Infof("Validate upgraded StorageCluster %s", cluster.Name)
				err = testutil.ValidateStorageCluster(specImages, cluster, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval, true, "")
				require.NoError(t, err)
			}
			lastHopURL = hopURL
		}

		// Delete and validate the deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func InstallWithTelemetry(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		testInstallWithTelemetry(t, cluster)
	}
}

func testInstallWithTelemetry(t *testing.T, cluster *corev1.StorageCluster) {
	secret := testutil.GetExpectedSecret(t, "pure-telemetry-cert.yaml")
	require.NotNil(t, secret)

	_, err := coreops.Instance().GetSecret(component.TelemetryCertName, "kube-system")
	require.True(t, err == nil || errors.IsNotFound(err))

	if errors.IsNotFound(err) {
		secret, err = coreops.Instance().CreateSecret(secret)
		require.NoError(t, err)
	} else {
		secret, err = coreops.Instance().UpdateSecret(secret)
		require.NoError(t, err)
	}

	// Deploy portworx with telemetry set to true
	cluster, err = ci_utils.CreateStorageCluster(cluster)
	require.NoError(t, err)

	err = testutil.ValidateTelemetry(
		ci_utils.PxSpecImages,
		cluster,
		ci_utils.DefaultValidateDeployTimeout,
		ci_utils.DefaultValidateDeployRetryInterval)
	require.NoError(t, err)

	// Delete and validate the deletion
	ci_utils.UninstallAndValidateStorageCluster(cluster, t)

	err = coreops.Instance().DeleteSecret(secret.Name, secret.Namespace)
	require.NoError(t, err)
}

// BasicCsiRegression test includes the following steps:
// 1. Deploy PX with CSI enabled by default and validate CSI components and images
// 2. Validate CSI is enabled by default
// 3. Delete "portworx" pods and validate they get re-deployed
// 4. Delete "px-csi-ext" pods and validate they get re-deployed
// 5. Disable CSI and validate CSI components got successfully removed
// 6. Enabled CSI and validate CSI components and images
// 7. Delete StorageCluster and validate it got successfully removed
func BasicCsiRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate CSI is enabled by default
		if cluster.Spec.FeatureGates["CSI"] != "true" {
			require.NoError(t, fmt.Errorf("failed to validate CSI is enabled by default, it shouldn't be enabled by default, but it is set to %s", cluster.Spec.FeatureGates["CSI"]))
		}

		logrus.Info("Delete portworx pods and validate they get re-deployed")
		err = coreops.Instance().DeletePodsByLabels(cluster.Namespace, map[string]string{"name": "portworx"}, 120*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		logrus.Info("Delete px-csi-ext pods and validate they get re-deployed")
		err = appsops.Instance().DeleteDeploymentPods("px-csi-ext", cluster.Namespace, 120*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		logrus.Info("Disable CSI and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		cluster.Spec.FeatureGates = map[string]string{"CSI": "false"}
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Enable CSI and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		cluster.Spec.FeatureGates = map[string]string{"CSI": "true"}
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

// BasicStorkRegression test includes the following steps:
// 1. Deploy PX with Stork enabled by default and validate Stork components and images
// 2. Validate Stork is enabled by default
// 3. Validate Stork webhook-controller is empty by default
// 4. Validate Stork hostName is <nil> by default
// 5. Delete "stork" pods and validate they get re-deployed
// 6. Delete "stork-scheduler" pods and validate they get re-deployed
// 7. Enable Stork webhook-controller and validate
// 8. Disable Stork webhook-controller and validate
// 9. Remove Stork webhook-controller and validate
// 10. Enable hotNetwork and validate
// 11. Disable hostNetwork and validate
// 12. Remove hostNetwork and valiate
// 13. Disable Stork and validate Stork components got successfully removed
// 14. Enabled Stork and validate Stork components and images
// 15. Delete StorageCluster and validate it got successfully removed
func BasicStorkRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate Stork is enabled by default
		if !cluster.Spec.Stork.Enabled {
			require.NoError(t, fmt.Errorf("failed to validate Stork is enabled by default, it should be enabled, but it is set to %v", cluster.Spec.Stork.Enabled))
		}

		// Validate Webhook controller arg doesn't by default
		if len(cluster.Spec.Stork.Args["webhook-controller"]) != 0 {
			require.NoError(t, fmt.Errorf("failed to validate webhook-controller, it shouldn't exist by default, but it is set to %s", cluster.Spec.Stork.Args["webhook-controller"]))
		}

		// Validate HostNetwork is <nil> by default
		if cluster.Spec.Stork.HostNetwork != nil {
			require.NoError(t, fmt.Errorf("failed to validate HostNetwork, it shouldn't exist by default, but it is set to %v", *cluster.Spec.Stork.HostNetwork))
		}

		logrus.Info("Delete stork pods and validate they get re-deployed")
		err = appsops.Instance().DeleteDeploymentPods("stork", cluster.Namespace, 120*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		logrus.Info("Delete stork-scheduler pods and validate they get re-deployed")
		err = appsops.Instance().DeleteDeploymentPods("stork-scheduler", cluster.Namespace, 120*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		logrus.Info("Enable Stork webhook-controller and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		// At this point this map should be <nil>
		if len(cluster.Spec.Stork.Args) == 0 {
			cluster.Spec.Stork.Args = make(map[string]string)
		}
		cluster.Spec.Stork.Args["webhook-controller"] = "true"
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Disable Stork webhook-controller and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		cluster.Spec.Stork.Args["webhook-controller"] = "false"
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Remove Stork webhook-controller and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		delete(cluster.Spec.Stork.Args, "webhook-controller")
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Enable Stork hostNetwork and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		hostNetworkValue := true
		cluster.Spec.Stork.HostNetwork = &hostNetworkValue
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Disable Stork hostNetwork and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		*cluster.Spec.Stork.HostNetwork = false
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Remove Stork hostNetwork and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		cluster.Spec.Stork.HostNetwork = nil
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Disable Stork and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		cluster.Spec.Stork.Enabled = false
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Info("Enable Stork and validate StorageCluster")
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		cluster.Spec.Stork.Enabled = true
		ci_utils.UpdateAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}
