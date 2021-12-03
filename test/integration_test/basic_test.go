// +build integrationtest

package integrationtest

import (
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
)

var (
	pxVer2_9, _ = version.NewVersion("2.9")
)

var testStorageClusterBasicCases = []types.TestCase{
	{
		TestName:        "InstallWithAllDefaults",
		TestrailCaseIDs: []string{"C51022", "C50236"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "simple-install"},
		}),
		TestFunc: BasicInstall,
	},
	{
		TestName:        "NodeAffinityLabels",
		TestrailCaseIDs: []string{"C50962"},
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
											Values:   []string{ci_utils.LabelValueTrue},
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
		TestrailCaseIDs: []string{"C50241"},
		TestSpec: func(t *testing.T) interface{} {
			return &corev1.StorageCluster{
				ObjectMeta: meta.ObjectMeta{Name: "upgrade-test"},
			}
		},
		ShouldSkip: func(tc *types.TestCase) bool {
			if len(ci_utils.PxUpgradeHopsURLList[0]) == 0 {
				return true
			}
			k8sVersion, _ := k8sutil.GetVersion()
			pxVersion := ci_utils.GetPxVersionFromSpecGenURL(ci_utils.PxUpgradeHopsURLList[0])
			return k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) && pxVersion.LessThan(pxVer2_9)
		},
		TestFunc: BasicUpgrade,
	},
	{
		TestName:        "InstallWithTelemetry",
		TestrailCaseIDs: []string{"C55909"},
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
		TestFunc: InstallWithTelemetry,
		ShouldSkip: func(tc *types.TestCase) bool {
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_7)
		},
	},
	{
		TestName:        "BasicCsiRegression",
		TestrailCaseIDs: []string{"C55919", "C51020", "C51025", "C51026", "C54701", "C54706", "C58194", "C58195"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "csi-regression-test"},
		}),
		TestFunc: BasicCsiRegression,
	},
	{
		TestName:        "BasicStorkRegression",
		TestrailCaseIDs: []string{"C57029", "C50244", "C50282", "C51243", "C54704", "C58260", "C54703", "C58259", "C53406", "C58256", "C58257", "C58258", "C53588", "C53628", "C53629", "C58261"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "stork-regression-test"},
		}),
		TestFunc: BasicStorkRegression,
	},
	{
		TestName: "BasicAutopilotRegression",
		TestrailCaseIDs: []string{"C57036", "C51237", "C58434", "	C58435", "C58433", "C51238", "C58432", "C51245"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "autopilot-regression-test"},
		}),
		TestFunc: BasicAutopilotRegression,
	},
	{
		TestName:        "BasicPvcControllerRegression",
		TestrailCaseIDs: []string{"C58438", "C54697", "C54698", "C54707", "C58437", "C54476", "C54477"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "pvccontroller-regression-test"},
		}),
		TestFunc: BasicPvcControllerRegression,
	},
}

func TestStorageClusterBasic(t *testing.T) {
	for _, testCase := range testStorageClusterBasicCases {
		testCase.RunTest(t)
	}
}

func BasicInstall(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func BasicInstallWithNodeAffinity(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		// Set Node Affinity label one of the K8S nodes
		nodeNameWithLabel := ci_utils.AddLabelToRandomNode(t, labelKeySkipPX, ci_utils.LabelValueTrue)

		// Run basic install test and validation
		BasicInstall(tc)(t)

		// Remove Node Affinity label from the node
		ci_utils.RemoveLabelFromNode(t, nodeNameWithLabel, labelKeySkipPX)
	}
}

func BasicUpgrade(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		// Get the storage cluster to start with
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		var lastHopURL string
		for i, hopURL := range ci_utils.PxUpgradeHopsURLList {
			// Get versions from URL
			specImages, err := testutil.GetImagesFromVersionURL(hopURL, ci_utils.K8sVersion)
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

				err = ci_utils.ConstructStorageCluster(cluster, hopURL, specImages)
				require.NoError(t, err)

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
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate CSI is enabled by default
		require.Equal(t, cluster.Spec.FeatureGates["CSI"], "true")

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
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.FeatureGates = map[string]string{"CSI": "false"}
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStorageCluster(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.Equal(t, cluster.Spec.FeatureGates["CSI"], "false")

		logrus.Info("Enable CSI and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.FeatureGates = map[string]string{"CSI": "true"}
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStorageCluster(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.Equal(t, cluster.Spec.FeatureGates["CSI"], "true")

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
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate Stork is enabled by default
		require.True(t, cluster.Spec.Stork.Enabled, "failed to validate Stork is enabled by default, it should be enabled, but it is set to %v", cluster.Spec.Stork.Enabled)

		// Validate Webhook controller arg doesn't exist by default
		require.Empty(t, cluster.Spec.Stork.Args["webhook-controller"], "failed to validate webhook-controller, it shouldn't exist by default, but it is set to %s", cluster.Spec.Stork.Args["webhook-controller"])

		// Validate HostNetwork is <nil> by default
		require.Nil(t, cluster.Spec.Stork.HostNetwork, "failed to validate HostNetwork, it should be nil by default, but it is set to %v", cluster.Spec.Stork.HostNetwork)

		logrus.Info("Delete stork pods and validate they get re-deployed")
		err = appsops.Instance().DeleteDeploymentPods("stork", cluster.Namespace, 120*time.Second)
		require.NoError(t, err)

		logrus.Info("Delete stork-scheduler pods and validate they get re-deployed")
		err = appsops.Instance().DeleteDeploymentPods("stork-scheduler", cluster.Namespace, 120*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		logrus.Info("Enable Stork webhook-controller and validate StorageCluster")
		// At this point this map should be <nil>
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			if cluster.Spec.Stork.Args == nil {
				cluster.Spec.Stork.Args = make(map[string]string)
			}
			cluster.Spec.Stork.Args["webhook-controller"] = "true"
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)

		logrus.Info("Disable Stork webhook-controller and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Stork.Args["webhook-controller"] = "false"
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)

		logrus.Info("Remove Stork webhook-controller and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			delete(cluster.Spec.Stork.Args, "webhook-controller")
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)

		logrus.Info("Enable Stork hostNetwork and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			hostNetworkValue := true
			cluster.Spec.Stork.HostNetwork = &hostNetworkValue
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)

		logrus.Info("Disable Stork hostNetwork and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			*cluster.Spec.Stork.HostNetwork = false
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)

		logrus.Info("Remove Stork hostNetwork and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Stork.HostNetwork = nil
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)

		logrus.Info("Disable Stork and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Stork.Enabled = false
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)
		require.False(t, cluster.Spec.Stork.Enabled, "failed to validate Stork is disabled: expected: false, actual: %v", cluster.Spec.Stork.Enabled)

		logrus.Info("Enable Stork and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Stork.Enabled = true
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStork(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)
		require.True(t, cluster.Spec.Stork.Enabled, "failed to validate Stork is enabled: expected: true, actual: %v", cluster.Spec.Stork.Enabled)

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

// BasicAutopilotRegression test includes the following steps:
// 1. Deploy PX and validate Autopilot components and images
// 2. Validate Autopilot is disabled by default
// 3. Enable Autopilot and validate Autopilot components and images
// 4. Delete "autopilot" pod and validate it gets re-deployed
// 5. Disable Autopilot and validate Autopilot components got successfully removed
// 6. Delete StorageCluster and validate it got successfully removed
func BasicAutopilotRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate Autopilot block is nil
		require.Nil(t, cluster.Spec.Autopilot, "failed to validate Autopilot block, it should be nil by default, but it seems there is something set in there %+v", cluster.Spec.Autopilot)

		logrus.Info("Enable Autopilot and validate StorageCluster")
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			// At this point this object should be <nil>
			cluster.Spec.Autopilot = &corev1.AutopilotSpec{
				Enabled: true,
			}
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateAutopilot(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.NotNil(t, cluster.Spec.Autopilot, "failed to validate Autopilot block, it should not be nil here, but it is: %+v", cluster.Spec.Autopilot)
		require.True(t, cluster.Spec.Autopilot.Enabled, "failed to validate Autopilot is enabled: expected: true, actual: %v", cluster.Spec.Autopilot.Enabled)

		logrus.Info("Delete autopilot pod and validate it gets re-deployed")
		err = appsops.Instance().DeleteDeploymentPods("autopilot", cluster.Namespace, 60*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateAutopilot(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateAutopilotTimeout, ci_utils.DefaultValidateAutopilotRetryInterval)
		require.NoError(t, err)

		logrus.Info("Disable Autopilot and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Autopilot.Enabled = false
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateAutopilot(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.NotNil(t, cluster.Spec.Autopilot, "failed to validate Autopilot block, it should not be nil here, but it is: %+v", cluster.Spec.Autopilot)
		require.False(t, cluster.Spec.Autopilot.Enabled, "failed to validate Autopilot is enabled: expected: false, actual: %v", cluster.Spec.Autopilot.Enabled)

		logrus.Info("Remove Autopilot block (set it to nil) and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Autopilot = nil
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateAutopilot(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.Nil(t, cluster.Spec.Autopilot, "failed to validate Autopilot block, it should be nil here, but it is not: %+v", cluster.Spec.Autopilot)

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

// BasicPvcControllerRegression test includes the following steps:
// 1. Deploy PX and validate PVC Controller components and images
// 2. Validate PVC Controller is not present in StorageCluster annotations by default
// 3. Enable PVC Controller in StorageCluster annotations and validate components
// 4. Delete PVC Controller pods and validate they get re-deployed
// 5. Set custom PVC Controller secure-port in StorageCluster annotations and validate components
// 6. Delete custom PVC Controller ports from StorageCluster annotations and validate components
// 7. Disable PVC Controller in StorageCluster annotations and validate components
// 8. Delete PVC Controller from StorageCluster annotations and validate components
// 9. Delete StorageCluster and validate it got successfully removed
func BasicPvcControllerRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		require.Empty(t, cluster.Annotations["portworx.io/pvc-controller"], "failed to validate portworx.io/pvc-controller annotation, it shouldn't exist by default, but it is and has value of %s", cluster.Annotations["portworx.io/pvc-controller"])

		logrus.Info("Enable PVC Controller annotation and validate StorageCluster")
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			// If annotations are nil, make it first
			if cluster.Annotations == nil {
				cluster.Annotations = make(map[string]string)
			}
			cluster.Annotations["portworx.io/pvc-controller"] = "true"
			return cluster
		}
		cluster = ci_utils.UpdateAndValidatePvcController(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)
		require.Equal(t, cluster.Annotations["portworx.io/pvc-controller"], "true")

		logrus.Info("Delete portworx-pvc-controller pods and validate it gets re-deployed")
		err = appsops.Instance().DeleteDeploymentPods("portworx-pvc-controller", cluster.Namespace, 60*time.Second)
		require.NoError(t, err)
		err = testutil.ValidatePvcController(ci_utils.PxSpecImages, cluster, ci_utils.K8sVersion, ci_utils.DefaultValidateAutopilotTimeout, ci_utils.DefaultValidateAutopilotRetryInterval)
		require.NoError(t, err)

		logrus.Info("Set PVC Controller custom secure-port in the annotations and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Annotations["portworx.io/pvc-controller-secure-port"] = "1111"
			return cluster
		}
		cluster = ci_utils.UpdateAndValidatePvcController(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)
		require.Equal(t, cluster.Annotations["portworx.io/pvc-controller-secure-port"], "1111")

		logrus.Info("Delete PVC Controller custom secure-port from the annotations and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			delete(cluster.Annotations, "portworx.io/pvc-controller-secure-port")
			return cluster
		}
		cluster = ci_utils.UpdateAndValidatePvcController(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)
		require.Empty(t, cluster.Annotations["portworx.io/pvc-controller-secure-port"], "failed to validate portworx.io/pvc-controller-secure-port annotation, it shouldn't be here, because it was deleted")

		logrus.Info("Disable PVC Controller annotation and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Annotations["portworx.io/pvc-controller"] = "false"
			return cluster
		}
		cluster = ci_utils.UpdateAndValidatePvcController(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)
		require.Equal(t, cluster.Annotations["portworx.io/pvc-controller"], "false")

		logrus.Info("Delete PVC Controller annotation and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			delete(cluster.Annotations, "portworx.io/pvc-controller")
			return cluster
		}
		cluster = ci_utils.UpdateAndValidatePvcController(cluster, updateParamFunc, ci_utils.PxSpecImages, ci_utils.K8sVersion, t)
		require.Empty(t, cluster.Annotations["portworx.io/pvc-controller"], "failed to validate portworx.io/pvc-controller annotation, it shouldn't be here, because it was deleted")

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}
