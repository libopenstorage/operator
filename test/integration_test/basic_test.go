// +build integrationtest

package integrationtest

import (
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	op_corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
)

const (
	labelKeySkipPX = "skip/px"
	labelValueTrue = "true"
)

var (
	pxVer2_9, _ = version.NewVersion("2.9")
)

var testStorageClusterBasicCases = []TestCase{
	{
		TestName:        "InstallWithAllDefaults",
		TestrailCaseIDs: []string{},
		TestSpec: createStorageClusterTestSpecFunc(&op_corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "simple-install"},
		}),
		TestFunc: BasicInstall,
	},
	{
		TestName:        "NodeAffinityLabels",
		TestrailCaseIDs: []string{},
		TestSpec: createStorageClusterTestSpecFunc(&op_corev1.StorageCluster{
			ObjectMeta: meta.ObjectMeta{Name: "node-affinity-labels"},
			Spec: op_corev1.StorageClusterSpec{
				Placement: &op_corev1.PlacementSpec{
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
			return &op_corev1.StorageCluster{
				ObjectMeta: meta.ObjectMeta{Name: "upgrade-test"},
			}
		},
		ShouldSkip: func() bool {
			k8sVersion, _ := k8sutil.GetVersion()
			pxVersion := getPxVersionFromSpecGenURL(pxUpgradeHopsURLList[0])
			return k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) && pxVersion.LessThan(pxVer2_9)
		},
		TestFunc: BasicUpgrade,
	},
	{
		TestName:        "InstallWithTelemetry",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &op_corev1.StorageCluster{}
			cluster.Name = "telemetry-test"
			err := constructStorageCluster(cluster, pxSpecGenURL, pxSpecImages)
			require.NoError(t, err)
			cluster.Spec.Monitoring = &op_corev1.MonitoringSpec{
				Telemetry: &op_corev1.TelemetrySpec{
					Enabled: true,
				},
			}
			return cluster
		},
		ShouldSkip: func() bool { return false },
		TestFunc:   InstallWithTelemetry,
	},
}

func TestStorageClusterBasic(t *testing.T) {
	for _, testCase := range testStorageClusterBasicCases {
		testCase.RunTest(t)
	}
}

func BasicInstall(tc *TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*op_corev1.StorageCluster)
		require.True(t, ok)

		// Deploy cluster
		cluster, err := createStorageCluster(cluster)
		require.NoError(t, err)

		// Validate cluster deployment
		logrus.Infof("Validate StorageCluster %s", cluster.Name)
		err = testutil.ValidateStorageCluster(pxSpecImages, cluster, defaultValidateDeployTimeout, defaultValidateDeployRetryInterval, true, "")
		require.NoError(t, err)

		// Delete cluster
		logrus.Infof("Delete StorageCluster %s", cluster.Name)
		err = testutil.UninstallStorageCluster(cluster)
		require.NoError(t, err)

		// Validate cluster deletion
		logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
		err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
		require.NoError(t, err)
	}
}

func BasicInstallWithNodeAffinity(tc *TestCase) func(*testing.T) {
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

func BasicUpgrade(tc *TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		// Get the storage cluster to start with
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*op_corev1.StorageCluster)
		require.True(t, ok)

		var lastHopURL string
		for i, hopURL := range pxUpgradeHopsURLList {
			// Get versions from URL
			logrus.Infof("Get component images from versions URL")
			specImages, err := testutil.GetImagesFromVersionURL(hopURL)
			require.NoError(t, err)
			lastHopURL = hopURL
			if i == 0 {
				// Deploy cluster
				logrus.Infof("Deploying starting cluster using %s", hopURL)
				err := constructStorageCluster(cluster, hopURL, specImages)
				require.NoError(t, err)
				_, err = createStorageCluster(cluster)
				require.NoError(t, err)
			} else {
				logrus.Infof("Upgrading from %s to %s", lastHopURL, hopURL)
				cluster, err = updateStorageCluster(cluster, hopURL, specImages)
				require.NoError(t, err)
			}

			// Validate cluster deployment
			logrus.Infof("Validate StorageCluster %s", cluster.Name)
			err = testutil.ValidateStorageCluster(specImages, cluster, defaultValidateUpgradeTimeout, defaultValidateUpgradeRetryInterval, true, "")
			require.NoError(t, err)
		}

		// Delete cluster
		logrus.Infof("Delete StorageCluster %s", cluster.Name)
		err := testutil.UninstallStorageCluster(cluster)
		require.NoError(t, err)

		// Validate cluster deletion
		logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
		err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
		require.NoError(t, err)
	}
}

func InstallWithTelemetry(tc *TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*op_corev1.StorageCluster)
		require.True(t, ok)

		testInstallWithTelemetry(t, cluster)
	}
}

func testInstallWithTelemetry(t *testing.T, cluster *op_corev1.StorageCluster) {
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
	cluster, err = createStorageCluster(cluster)
	require.NoError(t, err)

	err = testutil.ValidateTelemetry(
		pxSpecImages,
		cluster,
		defaultValidateDeployTimeout,
		defaultValidateDeployRetryInterval)
	require.NoError(t, err)

	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
	require.NoError(t, err)

	err = coreops.Instance().DeleteSecret(secret.Name, secret.Namespace)
	require.NoError(t, err)
}
