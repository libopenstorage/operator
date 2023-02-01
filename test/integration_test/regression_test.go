//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	appops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var regressionTestCases = []types.TestCase{
	{
		TestName:        "NodeOutOfCpu",
		TestrailCaseIDs: []string{""},
		TestSpec: func(t *testing.T) interface{} {
			// Construct StorageCluster
			cluster := &corev1.StorageCluster{}
			cluster.Name = clusterName
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)
			return cluster
		},
		TestFunc: NodeOutOfCpu,
	},
}

func shouldSkipRegressionTests(tc *types.TestCase) bool {
	return false
}

func TestRegression(t *testing.T) {
	for _, tc := range regressionTestCases {
		tc.ShouldSkip = shouldSkipRegressionTests
		tc.RunTest(t)
	}
}

func NodeOutOfCpu(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		// Get the StorageCluster to start with
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Pick a random node and label it with 'insufficient=cpu'
		nodeNameWithLabel := ci_utils.AddLabelToRandomNode(t, "insufficient", "cpu")

		// Create app deployment with node affinity to match above node
		appDeployment, err := deployAppWithNodeAffinity()
		require.NoError(t, err)

		// Scale app 1 pod at a time until new pod is Pending due to '1 Insufficient cpu' message in status
		err = scaleAppUntilInsufficientCpu(appDeployment)
		require.NoError(t, err)

		// Set resources for StorageCluster
		resources := &v1.ResourceRequirements{
			Requests: map[v1.ResourceName]resource.Quantity{
				v1.ResourceMemory: resource.MustParse("2Gi"),
				v1.ResourceCPU:    resource.MustParse("2000m"),
			},
		}
		cluster.Spec.Resources = resources

		// Deploy StorageCluster
		logrus.Infof("Deploying StorageCluster once [%s]", cluster.Name)
		cluster = ci_utils.DeployStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Validate StorageCluster is failing, using shorter timeout than usual (ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval)
		//if err := testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, 5*time.Minute, 30*time.Second, true); err == nil {
		//	require.NoError(t, fmt.Errorf("Was expecting to fail validation deployment, but it was successful"))
		//}
		//logrus.Infof("Validation for StorageCluster [%s] failed as expected", cluster.Name)

		// Validate PX pods are healthy on all nodes except the labeled node
		err = validatePxPodOnNodesExceptLabelenNode(cluster, nodeNameWithLabel, cluster.Namespace)
		require.NoError(t, err)

		// Get more info PX pod on a node we deploying apps on, PX pod shouldn't be provisioning due to 'Insufficient cpu'
		err = validatePxPodOnNodeIsOutOfCpu(nodeNameWithLabel, cluster.Namespace)
		require.NoError(t, err)

		// Scale down or delete the app, will need to check what is better
		err = deleteAndValidateAppDeployment(appDeployment)
		require.NoError(t, err)

		// Validate all PX pods are up and running
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true)
		require.NoError(t, err)

		// Remove 'insufficient=cpu' label from node
		ci_utils.RemoveLabelFromNode(t, nodeNameWithLabel, "insufficient")

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func validatePxPodOnNodesExceptLabelenNode(cluster *corev1.StorageCluster, nodeNameToExclude, namespace string) error {
	var expectedNodeList []v1.Node

	// Get nodes we expect PX to be deployed on
	nodeList, err := testutil.GetExpectedPxNodeList(cluster)
	if err != nil {
		return err
	}

	// Exclude labeled node from the list
	for _, node := range nodeList {
		if node.Name == nodeNameToExclude {
			continue
		}
		expectedNodeList = append(expectedNodeList, node)
	}

	// Validate PX pods are running on expected nodes
	if err := testutil.ValidatePxPodsAreReadyOnGivenNodes(cluster, expectedNodeList, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval); err != nil {
		return err
	}

	return nil
}

func validatePxPodOnNodeIsOutOfCpu(nodeName, namespace string) error {
	labelSelector := map[string]string{"name": "portworx"}

	logrus.Infof("Validate PX pod on node [%s] is in Out of CPU state", nodeName)

	t := func() (interface{}, bool, error) {
		podList, err := coreops.Instance().GetPodsByNodeAndLabels(nodeName, namespace, labelSelector)
		if err != nil {
			return nil, true, err
		}
		if len(podList.Items) == 0 {
			return nil, true, fmt.Errorf("Didn't find any PX pods on node [%s]", nodeName)
		}
		pod := podList.Items[0]
		logrus.Infof("Found PX pod [%s] on node [%s]", pod.Name, nodeName)
		if !coreops.Instance().IsPodReady(pod) {
			logrus.Infof("PX pod [%s] is in [%s] state due to [%s], with message [%s]", pod.Name, pod.Status.Phase, pod.Status.Reason, pod.Status.Message)
			if strings.Contains(pod.Status.Message, "Insufficient cpu") || strings.Contains(pod.Status.Message, "enough resource: cpu") || pod.Status.Reason == "OutOfcpu" {
				logrus.Infof("PX pod [%s] is failing to be created due to expected reason [%s], message [%s]", pod.Name, pod.Status.Reason, pod.Status.Message)
				return nil, false, nil
			}
			return nil, true, fmt.Errorf("Failed to match expected reason and message for failing in PX pod [%s], reason [%s], message [%s]", pod.Name, pod.Status.Reason, pod.Status.Message)
		}
		return nil, false, fmt.Errorf("PX Pod [%s] should not be in ready state, it should be failing", pod.Name)
	}

	if _, err := task.DoRetryWithTimeout(t, 5*time.Minute, 5*time.Second); err != nil {
		return err
	}

	return nil
}

func deployAppWithNodeAffinity() (*appsv1.Deployment, error) {
	var appDeploymentSpec *appsv1.Deployment

	logrus.Infof("Configuring and creating nginx deployment with custom resouces and node affinity")

	// Parse object specs
	appObjects, err := ci_utils.ParseApplicationSpecs("nginx-no-volumes")
	if err != nil {
		return nil, err
	}

	// Get app spec objects
	for _, appObject := range appObjects {
		if object, ok := appObject.(*appsv1.Deployment); ok {
			appDeploymentSpec = object
			break
		}
	}

	// Start with replica 1
	replicasCount := int32(1)
	appDeploymentSpec.Spec.Replicas = &replicasCount

	// Set resources
	resources := v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("1Gi"),
			v1.ResourceCPU:    resource.MustParse("2000m"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("1Gi"),
			v1.ResourceCPU:    resource.MustParse("2000m"),
		},
	}

	// Set node affinity
	nodeAffinity := &v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      "insufficient",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"cpu"},
							},
						},
					},
				},
			},
		},
	}

	appDeploymentSpec.Spec.Template.Spec.Affinity = nodeAffinity

	// Set resources
	for i, container := range appDeploymentSpec.Spec.Template.Spec.Containers {
		if container.Name == "nginx" {
			appDeploymentSpec.Spec.Template.Spec.Containers[i].Resources = resources
			break
		}
	}

	// Create app deployment
	appDeployment, err := appops.Instance().CreateDeployment(appDeploymentSpec, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	// Validate app deployment
	err = appops.Instance().ValidateDeployment(appDeploymentSpec, 2*time.Minute, 10*time.Second)
	if err != nil {
		return nil, err
	}

	logrus.Infof("Successfully created and validated nginx deployment")
	return appDeployment, nil
}

func deleteAndValidateAppDeployment(appDeployment *appsv1.Deployment) error {
	logrus.Infof("Delete nginx deployment")

	// Delete app deployment
	err := appops.Instance().DeleteDeployment(appDeployment.Name, appDeployment.Namespace)
	if err != nil {
		return err
	}

	// Va;idate app deployment is terminated
	err = appops.Instance().ValidateTerminatedDeployment(appDeployment, 5*time.Minute, 5*time.Second)
	if err != nil {
		return err
	}

	logrus.Infof("Successfully deleted and validated nginx deployment")
	return nil
}

func scaleAppUntilInsufficientCpu(appDeployment *appsv1.Deployment) error {
	logrus.Infof("Scaling up app [%s] until we cannot anymore due to CPU resource limits", appDeployment.Name)

	t := func() (interface{}, bool, error) {
		appDeployment, err := appops.Instance().GetDeployment(appDeployment.Name, appDeployment.Namespace)
		if err != nil {
			return nil, true, err
		}

		// Scale deployment replica by 1
		currentDeploymentReplicasCount := int(*appDeployment.Spec.Replicas)
		newDeploymentReplicaseCount := currentDeploymentReplicasCount + 1
		replicasCount := int32(newDeploymentReplicaseCount)
		appDeployment.Spec.Replicas = &replicasCount
		logrus.Infof("Scaling up app [%s] replicas from [%d] to [%d]", appDeployment.Name, currentDeploymentReplicasCount, newDeploymentReplicaseCount)
		appDeployment, err = appops.Instance().UpdateDeployment(appDeployment)
		if err != nil {
			return nil, true, err
		}

		err = appops.Instance().ValidateDeployment(appDeployment, 30*time.Second, 5*time.Second)
		if err != nil {
			pods, err := appops.Instance().GetDeploymentPods(appDeployment)
			if err != nil {
				return nil, true, err
			}
			for _, pod := range pods {
				if !coreops.Instance().IsPodReady(pod) {
					logrus.Infof("Found pod [%s] in not ready state", pod.Name)
					for _, podCondition := range pod.Status.Conditions {
						logrus.Infof("Pod [%s] message [%v]", pod.Name, podCondition.Message)
						if strings.Contains(podCondition.Message, "Insufficient cpu") {
							logrus.Infof("Successfully hit an issue with app scaling where pod [%s] is unable to get provisioned due to Insufficient CPU, message [%v]", pod.Name, podCondition.Message)
							return nil, false, nil
						}
					}
					return nil, true, fmt.Errorf("Failed to find expected message for pod [%s] about Insufficient CPU", pod.Name)
				}
			}
			return nil, true, fmt.Errorf("Failed to find not ready pods, need to create more pods")
		}
		return nil, true, fmt.Errorf("Deployment is healthy, need to create more pods..")
	}

	if _, err := task.DoRetryWithTimeout(t, 5*time.Minute, 10*time.Second); err != nil {
		return err
	}

	return nil
}
