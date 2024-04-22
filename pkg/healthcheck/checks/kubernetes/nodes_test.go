package kubernetes

import (
	"context"
	"fmt"
	"testing"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKubernetesMinNumberNodes(t *testing.T) {
	tests := []struct {
		cat           *healthcheck.Category
		expectFailure bool
	}{
		{
			cat: healthcheck.NewCategory(
				"test more nodes than minimum",
				[]*healthcheck.Checker{
					{
						Description: "create fake nodes",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, MinNumberOfNodes+1)
							for i := 0; i < MinNumberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sMinNumNodes(),
				},
				true,
				"none",
			),
			expectFailure: false,
		},
		{
			cat: healthcheck.NewCategory(
				"test equal nodes to minimum",
				[]*healthcheck.Checker{
					{
						Description: "create fake nodes",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, MinNumberOfNodes)
							for i := 0; i < MinNumberOfNodes; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sMinNumNodes(),
				},
				true,
				"none",
			),
			expectFailure: false,
		},
		{
			cat: healthcheck.NewCategory(
				"test less nodes than minimum",
				[]*healthcheck.Checker{
					{
						Description: "create fake nodes",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, MinNumberOfNodes-1)
							for i := 0; i < MinNumberOfNodes-1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sMinNumNodes(),
				},
				true,
				"none",
			),
			expectFailure: true,
		},
	}

	for _, test := range tests {
		hc := healthcheck.NewHealthChecker([]*healthcheck.Category{test.cat})
		results := hc.Run()
		if test.expectFailure {
			assert.False(t, results.Successful())
		} else {
			assert.True(t, results.Successful())
		}
	}
}
