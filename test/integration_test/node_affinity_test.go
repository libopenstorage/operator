// +build integrationtest

package integrationtest

import (
	"strings"
	"testing"
	"time"

	op_corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
)

const (
	defaultClusterName = "node-affinity-test"
	defaultPxNamespace = "kube-system"

	defaultValidateStorageClusterTimeout       = 900 * time.Second
	defaultValidateStorageClusterRetryInterval = 30 * time.Second

	defaultValidateUninstallTimeout       = 900 * time.Second
	defaultValidateUninstallRetryInterval = 30 * time.Second
)

func TestNodeAffinity(t *testing.T) {
	t.Run("testNodeAffinityLabels", testNodeAffinityLabels)
}

func testNodeAffinityLabels(t *testing.T) {
	var err error
	labelKey := "skip/px"
	labelValue := "true"

	// Construct Portworx StorageCluster object
	cluster := &op_corev1.StorageCluster{}
	cluster.Name = defaultClusterName
	cluster.Namespace = defaultPxNamespace

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

	// Set affinity Label one of the K8S nodes
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

	// Create cluster
	logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	err = createStorageCluster(cluster)
	require.NoError(t, err)

	// Validate deployment
	logrus.Infof("Get component images from versions URL")
	imageListMap, err := testutil.GetImagesFromVersionURL(defaultPxSpecGenURL, defaultPxSpecGenEndpoint)
	require.NoError(t, err)

	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateStorageClusterTimeout, defaultValidateStorageClusterRetryInterval, "")
	require.NoError(t, err)

	// Uninstall cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate Uninstall
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
	require.NoError(t, err)

	// Remove affinity Label from the node
	logrus.Infof("Remove label %s from node %s", nodeNameWithLabel, labelKey)
	if err := core.Instance().RemoveLabelOnNode(nodeNameWithLabel, labelKey); err != nil {
		require.NoError(t, err)
	}
}
