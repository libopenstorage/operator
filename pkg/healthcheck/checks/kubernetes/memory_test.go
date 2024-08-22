package kubernetes

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKubernetesMemoryLimits(t *testing.T) {
	numberOfNodes := rand.Intn(10) + 1
	tests := []struct {
		cat           *healthcheck.Category
		expectFailure bool
		expectWarning bool
	}{
		{
			cat: healthcheck.NewCategory(
				"memory is below limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with memory limits",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										Allocatable: corev1.ResourceList{
											corev1.ResourceMemory: resource.MustParse("1Gi"),
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
					K8sMinMemoryHardLimit(),
				},
				true,
				"none",
			),
			expectFailure: true,
			expectWarning: false,
		},
		{
			cat: healthcheck.NewCategory(
				"memory greater than hard limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with memory limits",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										Allocatable: corev1.ResourceList{
											corev1.ResourceMemory: resource.MustParse(MinMemoryHardLimit),
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
					K8sMinMemoryHardLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: false,
		},
		{
			cat: healthcheck.NewCategory(
				"memory is less than limit",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with memory limits",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										Allocatable: corev1.ResourceList{
											corev1.ResourceMemory: resource.MustParse("7Gi"),
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
					K8sMinMemoryHardLimit(),
					K8sMinMemorySoftLimit(),
				},
				true,
				"none",
			),
			expectFailure: false,
			expectWarning: true,
		},
		{
			cat: healthcheck.NewCategory(
				"memory limit is ok",
				[]*healthcheck.Checker{
					{
						Description: "set fake nodes with memory limits",
						HintAnchor:  "none",
						Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
							nodes := make([]corev1.Node, 0, numberOfNodes+1)
							for i := 0; i < numberOfNodes+1; i++ {
								nodes = append(nodes, corev1.Node{
									ObjectMeta: metav1.ObjectMeta{
										Name: fmt.Sprintf("node%d", i),
									},
									Status: corev1.NodeStatus{
										Allocatable: corev1.ResourceList{
											corev1.ResourceMemory: resource.MustParse("128Gi"),
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
					K8sMinMemoryHardLimit(),
					K8sMinMemorySoftLimit(),
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
