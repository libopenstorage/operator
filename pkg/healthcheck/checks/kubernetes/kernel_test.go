package kubernetes

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKubernetesKernelLimits(t *testing.T) {
	numberOfNodes := rand.Intn(10) + 1
	tests := []struct {
		cat           *healthcheck.Category
		expectFailure bool
		expectWarning bool
	}{
		{
			cat: healthcheck.NewCategory(
				"kernel version is below limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with kernel version",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										NodeInfo: corev1.NodeSystemInfo{
											KernelVersion: "4.4.0",
										},
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sKernelMinVersionHardLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: true,
		},
		{
			cat: healthcheck.NewCategory(
				"kernel version is above limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with kernel version",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										NodeInfo: corev1.NodeSystemInfo{
											KernelVersion: "14.4.0",
										},
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sKernelMinVersionHardLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: false,
		},
		{
			cat: healthcheck.NewCategory(
				"kernel version is below soft limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with kernel version",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										NodeInfo: corev1.NodeSystemInfo{
											KernelVersion: "4.4.0",
										},
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sKernelMinPxStorageV2VersionLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: true,
		},
		{
			cat: healthcheck.NewCategory(
				"kernel version is above soft limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with kernel version",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										NodeInfo: corev1.NodeSystemInfo{
											KernelVersion: "14.4.0",
										},
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sKernelMinPxStorageV2VersionLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: false,
		},
		{
			cat: healthcheck.NewCategory(
				"kernel version is in between hard and soft limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with kernel version",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										NodeInfo: corev1.NodeSystemInfo{
											KernelVersion: "4.99.99",
										},
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sKernelMinVersionHardLimit(),
					K8sKernelMinPxStorageV2VersionLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: true,
		},
		{
			cat: healthcheck.NewCategory(
				"kernel version is above soft limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with kernel version",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										NodeInfo: corev1.NodeSystemInfo{
											KernelVersion: "14.99.99",
										},
									},
								})
							}
							state.Set(KubernetesNodesListKey, &corev1.NodeList{
								Items: nodes,
							})
							return nil
						},
					},
					K8sKernelMinVersionHardLimit(),
					K8sKernelMinPxStorageV2VersionLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: false,
		},
	}

	for _, test := range tests {
		hc := healthcheck.NewHealthChecker([]*healthcheck.Category{test.cat})
		results := hc.Run()
		if test.expectFailure {
			assert.False(t, results.Successful(), results)
		} else {
			assert.True(t, results.Successful(), results)
		}

		if test.expectWarning {
			assert.True(t, results.HasWarning(), results)
		} else {
			assert.False(t, results.HasWarning(), results)
		}
	}
}
