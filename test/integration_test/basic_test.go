//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/libopenstorage/operator/drivers/storage/portworx"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/portworx/sched-ops/k8s/operator"
)

const (
	labelKeySkipPX = "skip/px"
)

var (
	pxVer2_9, _  = version.NewVersion("2.9")
	pxVer2_12, _ = version.NewVersion("2.12")
)

var testStorageClusterBasicCases = []types.TestCase{
	{
		TestName:        "InstallInCustomNamespaceWithShiftedPortAndAllComponents",
		TestrailCaseIDs: []string{"C52411", "C52430", "C53572"},
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("storagecluster/storagecluster-with-all-components.yaml")
			require.NoError(t, err)
			cluster, ok := objects[0].(*corev1.StorageCluster)
			require.True(t, ok)
			cluster.Name = "test-stc"
			cluster.Namespace = "custom-namespace"
			cluster.Spec.StartPort = func(val uint32) *uint32 { return &val }(17001)
			return cluster
		},
		TestFunc: BasicInstallInCustomNamespace,
	},
	{
		TestName:        "BasicUpgradeStorageCluster",
		TestrailCaseIDs: []string{"C50241"},
		TestSpec: func(t *testing.T) interface{} {
			return &corev1.StorageCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
			}
		},
		ShouldSkip: func(tc *types.TestCase) bool {
			if len(ci_utils.PxUpgradeHopsURLList) == 0 {
				logrus.Info("--px-upgrade-hops-url-list is empty, cannot run BasicUpgradeStorageCluster test")
				return true
			}
			k8sVersion, _ := k8sutil.GetVersion()
			pxVersion := ci_utils.GetPxVersionFromSpecGenURL(ci_utils.PxUpgradeHopsURLList[0])
			return k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) && pxVersion.LessThan(pxVer2_9)
		},
		TestFunc: BasicUpgradeStorageCluster,
	},
	{
		TestName:        "BasicUpgradeStorageClusterWithAllComponents",
		TestrailCaseIDs: []string{"C50241"},
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("storagecluster/storagecluster-with-all-components.yaml")
			require.NoError(t, err)
			cluster, ok := objects[0].(*corev1.StorageCluster)
			require.True(t, ok)
			cluster.Name = "test-stc"
			return cluster
		},
		ShouldSkip: func(tc *types.TestCase) bool {
			if len(ci_utils.PxUpgradeHopsURLList) == 0 {
				logrus.Info("--px-upgrade-hops-url-list is empty, cannot run BasicUpgradeStorageClusterWithAllComponents test")
				return true
			}
			k8sVersion, _ := k8sutil.GetVersion()
			pxVersion := ci_utils.GetPxVersionFromSpecGenURL(ci_utils.PxUpgradeHopsURLList[0])
			return k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) && pxVersion.LessThan(pxVer2_9)
		},
		TestFunc: BasicUpgradeStorageCluster,
	},
	{
		TestName:        "BasicUpgradeOperator",
		TestrailCaseIDs: []string{""},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		ShouldSkip: func(tc *types.TestCase) bool {
			if len(ci_utils.OperatorUpgradeHopsImageList) == 0 {
				logrus.Info("--operator-upgrade-hops-image-list is empty, cannot run BasicUpgradeOperator test")
				return true
			}
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_7)
		},
		TestFunc: BasicUpgradeOperator,
	},
	{
		TestName:        "BasicInstall",
		TestrailCaseIDs: []string{"C50962", "C51022", "C50236"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
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
		TestName:        "BasicTelemetryRegression",
		TestrailCaseIDs: []string{"C54888, C83063, C83064, C83160, C83161, C83076, C83077, C83078, C83082, C83162, C83163, C83164, C83165, C54892, C82916, C83083"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicTelemetryRegression,
		ShouldSkip: func(tc *types.TestCase) bool {
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_7)
		},
	},
	{
		TestName:        "BasicCsiRegression",
		TestrailCaseIDs: []string{"C55919", "C51020", "C51025", "C51026", "C54701", "C54706", "C58194", "C58195", "C60349", "C60350", "C79785", "C79788", "C79789"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicCsiRegression,
	},
	{
		TestName:        "BasicStorkRegression",
		TestrailCaseIDs: []string{"C57029", "C50244", "C50282", "C51243", "C54704", "C58260", "C54703", "C58259", "C53406", "C58256", "C58257", "C58258", "C53588", "C53628", "C53629", "C58261"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicStorkRegression,
	},
	{
		TestName:        "BasicAutopilotRegression",
		TestrailCaseIDs: []string{"C57036", "C51237", "C58434", "C58435", "C58433", "C51238", "C58432", "C51245"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicAutopilotRegression,
	},
	{
		TestName:        "BasicPvcControllerRegression",
		TestrailCaseIDs: []string{"C58438", "C54697", "C54698", "C54707", "C58437", "C54476", "C54477"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicPvcControllerRegression,
	},
	{
		TestName:        "BasicAlertManagerRegression",
		TestrailCaseIDs: []string{"C57120", "C57683", "C57121", "C57122", "C58871", "C57124"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicAlertManagerRegression,
	},
	{
		TestName:        "BasicKvdbRegression",
		TestrailCaseIDs: []string{"C52665", "C52667", "C52670", "C50237", "C53582", "C53583", "C53586", "C57011"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicKvdbRegression,
	},
	{
		TestName:        "BasicSecurityRegression",
		TestrailCaseIDs: []string{"C53416", "C53417", "C53423", "C60182", "C60183"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicSecurityRegression,
	},
	{
		TestName:        "InstallWithNodeTopologyLabels",
		TestrailCaseIDs: []string{"C59259", "C59260", "C59261"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-stc",
				Annotations: map[string]string{
					"portworx.io/pvc-controller": "true",
				},
			},
		}),
		TestFunc: InstallWithNodeTopologyLabels,
		ShouldSkip: func(tc *types.TestCase) bool {
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_8)
		},
	},
	{
		TestName:        "InstallWithCustomLabels",
		TestrailCaseIDs: []string{"C59042"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
			Spec: corev1.StorageClusterSpec{
				Metadata: &corev1.Metadata{
					Labels: map[string]map[string]string{
						"service/portworx-api": {
							"custom-portworx-api-label-key": "custom-portworx-api-label-val",
						},
					},
				},
			},
		}),

		TestFunc: InstallWithCustomLabels,
		ShouldSkip: func(tc *types.TestCase) bool {
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_8)
		},
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

		// Deploy PX and validate
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
	}
}

func BasicInstallInCustomNamespace(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Check if we need to create custom namespace
		if cluster.Namespace != ci_utils.PxNamespace {
			logrus.Debugf("Attempting to create custom namespace %s", cluster.Namespace)
			ns := &v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: cluster.Namespace,
				},
			}

			err := ci_utils.CreateObjects([]runtime.Object{ns})
			if err != nil {
				if errors.IsAlreadyExists(err) {
					logrus.Warnf("Namespace %s already exists", cluster.Namespace)
				} else {
					require.NoError(t, err)
				}
			}
		}

		// Create AlertManager secret, if AlertManager is enabled
		if cluster.Spec.Monitoring != nil {
			if cluster.Spec.Monitoring.Prometheus != nil {
				if cluster.Spec.Monitoring.Prometheus.AlertManager != nil {
					if cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled {
						objects, err := ci_utils.ParseSpecs("monitoring/alertmanager-secret.yaml")
						require.NoError(t, err)

						secret, ok := objects[0].(*v1.Secret)
						require.True(t, ok)

						secret.Namespace = cluster.Namespace
						logrus.Infof("Creating alertManager secret in %s namespace", secret.Namespace)
						_, err = coreops.Instance().CreateSecret(secret)
						require.NoError(t, err)
					}
				}
			}
		}

		// Construct StorageCluster
		err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
		require.NoError(t, err)

		// Deploy PX and validate
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Wipe PX and validate
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)

		// Delete namespace if custom
		if cluster.Namespace != ci_utils.PxNamespace {
			ns := &v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: cluster.Namespace,
				},
			}
			err := ci_utils.DeleteObjects([]runtime.Object{ns})
			require.NoError(t, err)
			err = ci_utils.ValidateObjectsAreTerminated([]runtime.Object{ns}, false)
			require.NoError(t, err)
		}
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

func BasicUpgradeStorageCluster(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		// Get the storage cluster to start with
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create AlertManager secret, if AlertManager is enabled
		if cluster.Spec.Monitoring != nil {
			if cluster.Spec.Monitoring.Prometheus != nil {
				if cluster.Spec.Monitoring.Prometheus.AlertManager != nil {
					if cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled {
						alertManagerSecret, err := ci_utils.ParseSpecs("monitoring/alertmanager-secret.yaml")
						require.NoError(t, err)

						logrus.Infof("Creating alert manager secret")
						err = ci_utils.CreateObjects(alertManagerSecret)
						require.NoError(t, err)
					}
				}
			}
		}

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
				k8sVersion, _ := version.NewVersion(ci_utils.K8sVersion)
				portworx.SetPortworxDefaults(cluster, k8sVersion)

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

func BasicUpgradeOperator(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		// Get the storage cluster to start with
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		var lastHopImage string
		for _, hopImage := range ci_utils.OperatorUpgradeHopsImageList {
			pxOperatorDeployment, err := ci_utils.GetPxOperatorDeployment()
			require.NoError(t, err)
			pxOperatorImage, err := ci_utils.GetPxOperatorImage()
			require.NoError(t, err)

			if pxOperatorImage == hopImage {
				logrus.Infof("Skipping upgrade of PX Operator from %s to %s", pxOperatorImage, hopImage)
				lastHopImage = hopImage
				continue
			}

			// Upgrade PX Operator image and validate deployment
			logrus.Infof("Upgrading PX Operator from %s to %s", lastHopImage, hopImage)
			updateParamFunc := func(pxOperator *appsv1.Deployment) *appsv1.Deployment {
				for ind, container := range pxOperator.Spec.Template.Spec.Containers {
					if container.Name == ci_utils.PortworxOperatorContainerName {
						container.Image = hopImage
						pxOperator.Spec.Template.Spec.Containers[ind] = container
						break
					}
				}
				return pxOperator
			}

			// Update and validate PX Operator deployment
			ci_utils.UpdateAndValidatePxOperator(pxOperatorDeployment, updateParamFunc, t)

			logrus.Infof("Upgraded PX Operator from %s to %s, letting it sleep for 15 secs to stabilize and let make changes to StorageCluster and/or existing objects", lastHopImage, hopImage)
			time.Sleep(15 * time.Second)

			// Validate PX Operator image
			pxOperatorImage, err = ci_utils.GetPxOperatorImage()
			require.NoError(t, err)
			require.Equal(t, pxOperatorImage, hopImage)

			// Validate StorageCluster
			err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
			require.NoError(t, err)

			lastHopImage = hopImage
		}

		// Delete and validate the deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

// BasicTelemetryRegression test includes the following steps:
// 1. Deploy PX and validate Telemetry is not enabled by default
// 2. Enable Telemetry and validate all its components got deployed
// 3. Disable Telemetry and validate its components got deleted
// 4. Delete StorageCluster and validate it got successfully removed (including px-telemetry-certs secret)
func BasicTelemetryRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		telemetryEnabled := cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.Telemetry != nil && cluster.Spec.Monitoring.Telemetry.Enabled

		// Validate Telemetry is enabled by default with PX 2.12+ and Operator 1.11+
		pxVersion := testutil.GetPortworxVersion(cluster)
		opVersion, _ := testutil.GetPxOperatorVersion()
		if pxVersion.GreaterThanOrEqual(pxVer2_12) && opVersion.GreaterThanOrEqual(ci_utils.PxOperatorVer1_11) {
			logrus.Infof("Validate Telemetry is enabled by default, PX version [%s], operator version [%s]", pxVersion, opVersion)
			require.True(t, telemetryEnabled, "failed to validate default Telemetry status: expected enabled [true], actual enabled [%v]", telemetryEnabled)

			err = testutil.ValidateMonitoring(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateComponentTimeout, ci_utils.DefaultValidateComponentRetryInterval)
			require.NoError(t, err)
		} else {
			// Validate Telemetry is not enabled by default
			logrus.Infof("Validate Telemetry is not enabled by default, PX version [%s], operator version [%s]", pxVersion, opVersion)
			require.False(t, telemetryEnabled, "failed to validate default Telemetry status: expected enabled [false], actual enabled [%v]", telemetryEnabled)

			// Enable Telemetry
			logrus.Info("Enable Telemetry and validate")
			updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
				cluster.Spec.Monitoring = &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
					},
				}
				return cluster
			}
			cluster = ci_utils.UpdateAndValidateMonitoring(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
			require.NoError(t, err)
		}

		// Disable Telemetry and validate
		logrus.Info("Disable Telemetry and validate")
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Monitoring.Telemetry.Enabled = false
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateMonitoring(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.NoError(t, err)
	}
}

// BasicCsiRegression test includes the following steps:
// 1. Deploy PX with CSI enabled by default and validate CSI components and images
// 2. Validate CSI is enabled by default and topology spec is empty
// 3. Delete "portworx" pods and validate they get re-deployed
// 4. Disable CSI and validate CSI components got successfully removed
// 5. Enabled CSI and topology and validate CSI components and images
// 6. Validate CSI snapshot controller is enabled by default on k8s 1.17+, disable and re-enabled it
// 7. Delete StorageCluster and validate it got successfully removed
func BasicCsiRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate CSI is enabled by default
		require.Equal(t, cluster.Spec.CSI.Enabled, true)
		require.Nil(t, cluster.Spec.CSI.Topology)

		logrus.Info("Delete portworx pods and validate they get re-deployed")
		err = coreops.Instance().DeletePodsByLabels(cluster.Namespace, map[string]string{"name": "portworx"}, 120*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		logrus.Info("Disable CSI and validate StorageCluster")
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.CSI.Enabled = false
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStorageCluster(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.Equal(t, cluster.Spec.CSI.Enabled, false)

		// Test CSI topology feature on Operator 1.8.1+
		if ci_utils.PxOperatorVersion.GreaterThanOrEqual(ci_utils.PxOperatorVer1_8_1) {
			logrus.Info("Enable CSI and topology and validate StorageCluster")
			updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
				cluster.Spec.CSI.Enabled = true
				cluster.Spec.CSI.Topology = &corev1.CSITopologySpec{
					Enabled: true,
				}
				return cluster
			}
			cluster = ci_utils.UpdateAndValidateStorageCluster(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
			require.True(t, cluster.Spec.CSI.Enabled)
			require.NotNil(t, cluster.Spec.CSI.Topology)
			require.True(t, cluster.Spec.CSI.Topology.Enabled)
		}

		k8sVersion, _ := k8sutil.GetVersion()
		if k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_17) {
			// Validate CSI snapshot controller is enabled by default on k8s 1.17+
			require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
			require.True(t, *cluster.Spec.CSI.InstallSnapshotController)

			logrus.Info("Disable csi snapshot controller and validate")
			updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
				cluster.Spec.CSI.InstallSnapshotController = testutil.BoolPtr(false)
				return cluster
			}
			cluster = ci_utils.UpdateAndValidateCSI(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
			require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
			require.False(t, *cluster.Spec.CSI.InstallSnapshotController)

			logrus.Info("Re-enable csi snapshot controller and validate")
			updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
				cluster.Spec.CSI.InstallSnapshotController = testutil.BoolPtr(true)
				return cluster
			}
			cluster = ci_utils.UpdateAndValidateCSI(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
			require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
			require.True(t, *cluster.Spec.CSI.InstallSnapshotController)
		}
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
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate Stork is enabled by default
		require.True(t, cluster.Spec.Stork.Enabled, "failed to validate Stork is enabled by default, it should be enabled, but it is set to %v", cluster.Spec.Stork.Enabled)

		// Validate Webhook controller arg doesn't exist by default
		require.Empty(t, cluster.Spec.Stork.Args["webhook-controller"], "failed to validate webhook-controller, it shouldn't exist by default, but it is set to %s", cluster.Spec.Stork.Args["webhook-controller"])

		// Validate HostNetwork is <nil> by default
		require.Nil(t, cluster.Spec.Stork.HostNetwork, "failed to validate HostNetwork, it should be nil by default, but it is set to %v", cluster.Spec.Stork.HostNetwork)

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
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

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
		// ci_utils.UninstallAndValidateStorageCluster(cluster, t)
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
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

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
	}
}

func InstallWithCustomLabels(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		logrus.Info("Install with custom labels and validate StorageCluster")
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Update service/portworx-api labels
		logrus.Info("Update custom labels and validate StorageCluster")
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Metadata.Labels["service/portworx-api"] = map[string]string{
				"custom-portworx-api-label-key":     "custom-portworx-api-label-val-updated",
				"custom-portworx-api-label-key-new": "custom-portworx-api-label-val-new",
			}
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStorageCluster(cluster, updateParamFunc, ci_utils.PxSpecImages, t)

		// Delete service/portworx-api labels
		logrus.Info("Delete custom labels and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Metadata.Labels["service/portworx-api"] = nil
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStorageCluster(cluster, updateParamFunc, ci_utils.PxSpecImages, t)

		// Delete service/portworx-api labels
		logrus.Info("Delete custom labels and validate StorageCluster")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Metadata.Labels["service/portworx-api"] = nil
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateStorageCluster(cluster, updateParamFunc, ci_utils.PxSpecImages, t)

		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

// BasicAlertManagerRegression test includes the following steps:
// 1. Deploy PX and validate AlertManager is not enabled by default
// 2. Create AlertManager secret
// 3. Enable AlertManager without Prometheus and validate it is not getting deployed
// 4. Enable AlertManager with Prometheus and validate it gets deployed
// 5. Delete AlertManager pods and validate they get re-deployed
// 6. Disable AlertManager in StorageCluster and validate components
// 7. Delete AlertManager secret
// 8. Delete StorageCluster and validate it got successfully removed
func BasicAlertManagerRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate AlertManager is not enabled by default
		logrus.Info("Validate ALertManager is not enabled by default")
		if cluster.Spec.Monitoring != nil {
			if cluster.Spec.Monitoring.Prometheus != nil {
				if cluster.Spec.Monitoring.Prometheus.AlertManager != nil {
					if cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled {
						require.False(t, cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled, "failed to validate AlertManager is enabled: expected: false, actual: %v", cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled)
					}
				}
			}
		}

		// Create AlertManager secret
		alertManagerSecret, err := ci_utils.ParseSpecs("monitoring/alertmanager-secret.yaml")
		require.NoError(t, err)

		logrus.Infof("Creating alert manager secret")
		err = ci_utils.CreateObjects(alertManagerSecret)
		require.NoError(t, err)

		// Enable AlertManager without Prometheus and validate it does't get deployed
		logrus.Info("Enable AlertManager without Prometheus and validate it doesn't get deployed")
		var monitoring *corev1.MonitoringSpec
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			if cluster.Spec.Monitoring == nil {
				monitoring = &corev1.MonitoringSpec{
					Prometheus: &corev1.PrometheusSpec{
						AlertManager: &corev1.AlertManagerSpec{
							Enabled: true,
						},
					},
				}
				cluster.Spec.Monitoring = monitoring
			}
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateMonitoring(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.NoError(t, err)

		// Enable AlertManager with Prometheus and validate it gets deployed
		logrus.Info("Enable AlertManager with Prometheus and validate it gets deployed")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Monitoring = &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
					Enabled: true,
				},
			}
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateMonitoring(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.NoError(t, err)

		// Disable AlertManager and validate
		logrus.Info("Disable AlertManager and validate")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled = false
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateMonitoring(cluster, updateParamFunc, ci_utils.PxSpecImages, t)
		require.NoError(t, err)

		// Delete AlertManager secret
		err = coreops.Instance().DeleteSecret("alertmanager-portworx", cluster.Namespace)
		require.NoError(t, err)
	}
}

// BasicSecurityRegression test includes the following steps:
// 1. Deploy PX and validate Security is not enabled by default
// 2. Enable Security and validate components got created
// 3. Disable Security and validate components got removed
// 4. Delete StorageCluster and validate it got successfully removed
func BasicSecurityRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate Security is not enabled by default
		logrus.Info("Validate Security is not enabled by default")
		if cluster.Spec.Security != nil {
			require.False(t, cluster.Spec.Security.Enabled, "failed to validate Security is enabled: expected: false, actual: %v", cluster.Spec.Security.Enabled)
		}

		// Enable Security and validate
		logrus.Info("Enable Security and validate")
		updateParamFunc := func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			if cluster.Spec.Security == nil {
				cluster.Spec.Security = &corev1.SecuritySpec{}
			}
			cluster.Spec.Security.Enabled = true
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateSecurity(cluster, false, updateParamFunc, t)
		require.NoError(t, err)

		// Disable Security and validate
		logrus.Info("Disable Security and validate")
		updateParamFunc = func(cluster *corev1.StorageCluster) *corev1.StorageCluster {
			cluster.Spec.Security.Enabled = false
			return cluster
		}
		cluster = ci_utils.UpdateAndValidateSecurity(cluster, true, updateParamFunc, t)
		require.NoError(t, err)

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

// BasicKvdbRegression test includes the following steps:
// 1. Deploy PX and validate internal KVDB
// 2. Validate KVDB pods and other components
// 3. Delete KVDB pods and validate they get redeployed
// 4. Delete StorageCluster and validate it got successfully removed
func BasicKvdbRegression(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		var err error
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Delete all KVDB pods and validate the get re-created
		logrus.Info("Delete portworx KVDB pods and validate they get re-deployed")
		err = coreops.Instance().DeletePodsByLabels(cluster.Namespace, map[string]string{"kvdb": "true"}, 120*time.Second)
		require.NoError(t, err)
		err = testutil.ValidateKvdb(cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval)
		require.NoError(t, err)
	}
}

// InstallWithNodeTopologyLabels includes the following steps:
// 1. backup all node topology labels
// 2. divide all k8s nodes with topology key 'topology.kubernetes.io/region' into 2 regions
// 3. install StorageCluster with stork, csi, pvc controller enabled
// 4. validate deployment.spec.template.spec.topologySpreadConstraints matches the expected constraints
// 5. add a new label 'topology.kubernetes.io/zone' to all nodes, then validate new constraint should be added to deployments
// 6. remove all topology labels, then validate all topology constraints should be removed
// 7. uninstall the cluster and recover node topology labels
func InstallWithNodeTopologyLabels(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		regionKey := "topology.kubernetes.io/region"
		zoneKey := "topology.kubernetes.io/zone"
		// Backup existing topology labels
		logrus.Infof("Backing up cluster topology labels")
		nodeList, err := coreops.Instance().GetNodes()
		require.NoError(t, err)
		for _, node := range nodeList.Items {
			region := ""
			zone := ""
			if val, ok := node.Labels[regionKey]; ok {
				region = val
			}
			if val, ok := node.Labels[zoneKey]; ok {
				zone = val
			}
			logrus.Infof("node %s: %s=%s, %s=%s", node.Name, regionKey, region, zoneKey, zone)
		}

		// Add/overwrite region topology label to all nodes, divide all nodes into 2 regions
		for i, node := range nodeList.Items {
			val := fmt.Sprintf("region%v", i%2)
			logrus.Infof("Label node %s with %s=%s", node.Name, regionKey, val)
			err := coreops.Instance().AddLabelOnNode(node.Name, regionKey, val)
			require.NoError(t, err)
		}

		// Install the cluster and do validations
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Add zone label to all nodes, now constraint changed so new pods should be created
		for i, node := range nodeList.Items {
			val := fmt.Sprintf("zone%v", i)
			logrus.Infof("Label node %s with %s=%s", node.Name, zoneKey, val)
			err := coreops.Instance().AddLabelOnNode(node.Name, zoneKey, val)
			require.NoError(t, err)
		}
		logrus.Infof("Validate StorageCluster %s", cluster.Name)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster,
			ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		// Remove topology labels from all nodes and validate pod topology spread constraints
		for _, node := range nodeList.Items {
			ci_utils.RemoveLabelFromNode(t, node.Name, regionKey)
			ci_utils.RemoveLabelFromNode(t, node.Name, zoneKey)
		}
		logrus.Infof("Validate StorageCluster %s", cluster.Name)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster,
			ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		// Uninstall storage cluster and recover labels
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
		logrus.Infof("Recovering cluster topology labels")
		for _, node := range nodeList.Items {
			if val, ok := node.Labels[regionKey]; ok {
				logrus.Infof("Label node %s with %s=%s", node.Name, regionKey, val)
				err := coreops.Instance().AddLabelOnNode(node.Name, regionKey, val)
				require.NoError(t, err)
			}
			if val, ok := node.Labels[zoneKey]; ok {
				logrus.Infof("Label node %s with %s=%s", node.Name, zoneKey, val)
				err := coreops.Instance().AddLabelOnNode(node.Name, zoneKey, val)
				require.NoError(t, err)
			}
		}
	}
}
