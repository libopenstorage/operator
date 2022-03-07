package utils

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	coreops "github.com/portworx/sched-ops/k8s/core"
)

// AddLabelToRandomNode adds the given label to a random worker node
func AddLabelToRandomNode(t *testing.T, key, value string) string {
	nodeList, err := coreops.Instance().GetNodes()
	require.NoError(t, err)

	for _, node := range nodeList.Items {
		if coreops.Instance().IsNodeMaster(node) {
			continue // Skip master node, we don't need to label it
		}
		logrus.Infof("Label node %s with %s=%s", node.Name, key, value)
		if err := coreops.Instance().AddLabelOnNode(node.Name, key, value); err != nil {
			require.NoError(t, err)
		}
		return node.Name
	}
	return ""
}

// RemoveLabelFromNode removes the label from the given node
func RemoveLabelFromNode(t *testing.T, nodeName, key string) {
	logrus.Infof("Remove label %s from node %s", nodeName, key)
	if err := coreops.Instance().RemoveLabelOnNode(nodeName, key); err != nil {
		require.NoError(t, err)
	}
}
