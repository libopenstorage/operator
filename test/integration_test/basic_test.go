// +build integrationtest

package integrationtest

import (
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"

	op_corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
)

func TestStorageClusterBasic(t *testing.T) {
	t.Run("InstallWithAllDefaults", testInstallWithAllDefaults)
	t.Run("NodeAffinityLabels", testNodeAffinityLabels)
	t.Run("Upgrade", testUpgrade)
}

func testInstallWithAllDefaults(t *testing.T) {
	// Get versions from URL
	logrus.Infof("Get component images from versions URL")
	imageListMap, err := testutil.GetImagesFromVersionURL(pxSpecGenURL)
	require.NoError(t, err)

	// Construct Portworx StorageCluster object
	cluster, err := constructStorageCluster(pxSpecGenURL, imageListMap)
	require.NoError(t, err)

	cluster.Name = "simple-install"

	// Deploy cluster
	logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	cluster, err = createStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deployment
	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateDeployTimeout, defaultValidateDeployRetryInterval, true, "")
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

func testNodeAffinityLabels(t *testing.T) {
	var err error
	labelKey := "skip/px"
	labelValue := "true"

	// Get versions from URL
	logrus.Infof("Get component images from versions URL")
	imageListMap, err := testutil.GetImagesFromVersionURL(pxSpecGenURL)
	require.NoError(t, err)

	// Construct Portworx StorageCluster object
	cluster, err := constructStorageCluster(pxSpecGenURL, imageListMap)
	require.NoError(t, err)

	cluster.Name = "node-affinity-labels"

	// Set Node Affinity
	cluster.Spec.Placement = &op_corev1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      labelKey,
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{labelValue},
							},
						},
					},
				},
			},
		},
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
		logrus.Infof("Label node %s with %s=%s", node.Name, labelKey, labelValue)
		if err := coreops.Instance().AddLabelOnNode(node.Name, labelKey, labelValue); err != nil {
			require.NoError(t, err)
		}
		nodeNameWithLabel = node.Name
		break
	}

	// Deploy cluster
	logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	cluster, err = createStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deployment
	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateDeployTimeout, defaultValidateDeployRetryInterval, true, "")
	require.NoError(t, err)

	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
	require.NoError(t, err)

	// Remove Node Affinity label from the node
	logrus.Infof("Remove label %s from node %s", nodeNameWithLabel, labelKey)
	if err := coreops.Instance().RemoveLabelOnNode(nodeNameWithLabel, labelKey); err != nil {
		require.NoError(t, err)
	}
}

func testUpgrade(t *testing.T) {
	var err error
	var lastHopURL string
	var cluster *op_corev1.StorageCluster
	clusterName := "upgrade-test"

	upgradeHopURLs := strings.Split(pxUpgradeHopsURLList, ",")

	for ind, hopURL := range upgradeHopURLs {
		if ind == 0 {
			logrus.Infof("Deploying starting cluster using %s", hopURL)
		} else {
			logrus.Infof("Upgrading from %s to %s", lastHopURL, hopURL)
		}
		lastHopURL = hopURL

		// Get versions from URL
		logrus.Infof("Get component images from versions URL")
		imageListMap, err := testutil.GetImagesFromVersionURL(hopURL)
		require.NoError(t, err)

		if ind == 0 {
			// Construct Portworx StorageCluster object
			cluster, err = constructStorageCluster(hopURL, imageListMap)
			require.NoError(t, err)

			cluster.Name = clusterName

			// Deploy cluster
			logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
			cluster, err = createStorageCluster(cluster)
			require.NoError(t, err)
		} else {
			// Update cluster
			logrus.Infof("Update StorageCluster %s in %s", cluster.Name, cluster.Namespace)
			cluster, err = updateStorageCluster(cluster, hopURL, imageListMap)
			require.NoError(t, err)
		}

		// Validate cluster deployment
		logrus.Infof("Validate StorageCluster %s", cluster.Name)
		err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateUpgradeTimeout, defaultValidateUpgradeRetryInterval, true, "")
		require.NoError(t, err)
	}

	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
	require.NoError(t, err)
}
