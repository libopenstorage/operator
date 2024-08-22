package kubernetes

import (
	"context"
	"fmt"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"

	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
)

func K8sMinMemoryHardLimit() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: fmt.Sprintf("node minimum memory must be greater than %s", MinMemoryHardLimit),
		HintAnchor:  "pxe/1005",
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			nodes, ok := GetKubernetesNodesList(state)
			if !ok {
				return fmt.Errorf("unable to get kubernetes node list")
			}

			badNodes, err := checkMinMemoryLimit(nodes, MinMemoryHardLimit)
			if err != nil {
				return err
			}

			if len(badNodes) > 0 {
				return fmt.Errorf(
					"the following nodes have less than the minimum memory of %s: %+v",
					MinMemoryHardLimit, badNodes)
			}

			return nil
		},
	}
}

func K8sMinMemorySoftLimit() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: fmt.Sprintf("node minimum memory should be greater than %s", MinMemorySoftLimit),
		HintAnchor:  "pxe/1006",
		Warning:     true,
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			nodes, ok := GetKubernetesNodesList(state)
			if !ok {
				return fmt.Errorf("unable to get kubernetes node list")
			}

			badNodes, err := checkMinMemoryLimit(nodes, MinMemorySoftLimit)
			if err != nil {
				return err
			}

			if len(badNodes) > 0 {
				return fmt.Errorf(
					"the following nodes have less than the minimum memory of %s: %+v",
					MinMemorySoftLimit, badNodes)
			}

			return nil
		},
	}
}

func checkMinMemoryLimit(nodes *corev1.NodeList, limit string) ([]string, error) {
	minMemory, err := k8sresource.ParseQuantity(limit)
	if err != nil {
		return nil, fmt.Errorf("bug: unable to parse minimum memory value")
	}

	var badNodes []string
	for _, node := range nodes.Items {
		if node.Status.Allocatable.Memory().Cmp(minMemory) < 0 {
			if len(badNodes) <= 0 {
				badNodes = []string{"\n"}
			}

			badNodes = append(badNodes, fmt.Sprintf(
				"Current memory in %s is %s\n",
				node.GetName(),
				node.Status.Allocatable.Memory().String(),
			))
		}
	}
	return badNodes, nil
}
