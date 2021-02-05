// +build integrationtest

package integrationtest

import (
	"strings"
	"testing"

	op_corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
)

func TestNodeAffinity(t *testing.T) {
	t.Run("testNodeAffinityLabels", testNodeAffinityLabels)
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
	nodeList, err := core.Instance().GetNodes()
	require.NoError(t, err)

	// Set Node Affinity label one of the K8S nodes
	var nodeNameWithLabel string
	for _, node := range nodeList.Items {
		for key := range node.Labels {
			if strings.Contains(key, "node-role.kubernetes.io/master") { // Skip master node, we don't need to label it
				continue
			}
		}
		logrus.Infof("Label node %s with %s=%s", node.Name, labelKey, labelValue)
		if err := core.Instance().AddLabelOnNode(node.Name, labelKey, labelValue); err != nil {
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
	err = testutil.ValidateStorageCluster(imageListMap, cluster, testutil.DefaultValidateDeployTimeout, testutil.DefaultValidateDeployRetryInterval, "")
	require.NoError(t, err)

	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, testutil.DefaultValidateUninstallTimeout, testutil.DefaultValidateUninstallRetryInterval)
	require.NoError(t, err)

	// Remove Node Affinity label from the node
	logrus.Infof("Remove label %s from node %s", nodeNameWithLabel, labelKey)
	if err := core.Instance().RemoveLabelOnNode(nodeNameWithLabel, labelKey); err != nil {
		require.NoError(t, err)
	}
}
