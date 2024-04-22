package kubernetes

import (
	"context"
	"fmt"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorcorev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	corev1 "k8s.io/api/core/v1"
)

const (
	KubernetesNodesListKey healthcheck.HealthStateDataKey = "k8s/nodes/list"
)

func GatherKubernesNodes(
	k8sclient client.Client,
	cluster *operatorcorev1.StorageCluster,
) *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: "get information on kubernetes nodes",
		HintAnchor:  "pxe/1001",
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {

			nodes := &corev1.NodeList{}
			err := k8sclient.List(ctx, nodes)
			if err != nil {
				return fmt.Errorf("unable to get node information from Kubernetes: %v", err)
			}

			operator_nodes := make([]corev1.Node, 0, len(nodes.Items))
			for _, node := range nodes.Items {

				shouldRun, _, err := k8sutil.CheckPredicatesForStoragePod(&node, cluster, nil)
				if err != nil {
					return fmt.Errorf("unable to check affinity for node %s: %v", node.Name, err)
				}
				if shouldRun {
					operator_nodes = append(operator_nodes, node)
				}
			}
			nodes.Items = operator_nodes
			state.Set(KubernetesNodesListKey, nodes)
			return nil
		},
	}
}

func GetKubernetesNodesList(state *healthcheck.HealthCheckState) (*corev1.NodeList, bool) {
	if v, ok := state.Get(KubernetesNodesListKey).(*corev1.NodeList); ok {
		return v, ok
	}
	return nil, false
}
