// +build integrationtest

package integrationtest

import (
	"testing"
	"time"

	op_corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
)

const (
	defaultClusterName = "node-affinity-test"
	defaultPxNamespace = "kube-system"

	defaultValidateUninstallTimeout       = 15 * time.Minute
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
		if err := core.Instance().AddLabelOnNode(node.Name, labelKey, labelValue); err != nil {
			require.NoError(t, err)
		}
		nodeNameWithLabel = node.Name
		break
	}

	// Create cluster
	err = createStorageCluster(cluster)
	require.NoError(t, err)

	// TODO: Validate deployment

	// TODO: This will eventually be replaced by ValidateStorageCluster() when its fully implemented
	// Wait for Storagecluster to be ready
	err = waitForStorageClusterToBeReady(cluster)
	require.NoError(t, err)

	// Uninstall cluster
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate Uninstall
	err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
	require.NoError(t, err)

	// Remove affinity Label from the node
	if err := core.Instance().RemoveLabelOnNode(nodeNameWithLabel, labelKey); err != nil {
		require.NoError(t, err)
	}
}
