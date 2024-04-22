package kubernetes

import (
	"context"
	"fmt"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
)

func K8sMinNumNodes() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: fmt.Sprintf("number of worker nodes must be greater than %d", MinNumberOfNodes),
		HintAnchor:  "pxe/1004",
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			nodes, ok := GetKubernetesNodesList(state)
			if !ok {
				return fmt.Errorf("unable to get kubernetes node list")
			}

			if len(nodes.Items) < MinNumberOfNodes {
				return fmt.Errorf(
					"must have at least %d nodes. detected only %d nodes",
					MinNumberOfNodes,
					len(nodes.Items))
			}

			return nil
		},
	}
}
