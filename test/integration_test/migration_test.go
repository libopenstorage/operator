// +build integrationtest

package integrationtest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	appops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiscore "k8s.io/kubernetes/pkg/apis/core"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
)

const (
	operatorRestartTimeout       = 5 * time.Minute
	operatorRestartRetryInterval = 10 * time.Second
	clusterCreationTimeout       = 2 * time.Minute
	clusterCreationRetryInterval = 5 * time.Second
	labelKeyPXEnabled            = "px/enabled"
)

var migrationTestCases = []types.TestCase{
	{
		TestName: "SimpleMigration",
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("migration/simple-daemonset.yaml")
			require.NoError(t, err)
			return objects
		},
		TestFunc: BasicMigration,
	},
	{
		TestName: "SimpleMigrationWithoutNodeAffinity",
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("migration/simple-daemonset.yaml")
			require.NoError(t, err)
			return objects
		},
		TestFunc: MigrationWithoutNodeAffinity,
	},
	{
		TestName: "AllComponentsEnabled",
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("migration/daemonset-with-all-components.yaml")
			require.NoError(t, err)
			return objects
		},
		TestFunc: MigrationWithAllComponents,
	},
}

func TestDaemonSetMigration(t *testing.T) {
	for _, tc := range migrationTestCases {
		tc.RunTest(t)
	}
}

func BasicMigration(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		objects, ok := testSpec.([]runtime.Object)
		require.True(t, ok)

		pxDaemonSet, objects := extractPortworxDaemonSetFromObjects(objects)
		require.NotNil(t, pxDaemonSet)

		nodeNameWithLabel := ci_utils.AddLabelToRandomNode(t, labelKeyPXEnabled, ci_utils.LabelValueFalse)

		testMigration(t, objects, pxDaemonSet)

		ci_utils.RemoveLabelFromNode(t, nodeNameWithLabel, labelKeyPXEnabled)
	}
}

func MigrationWithoutNodeAffinity(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		objects, ok := testSpec.([]runtime.Object)
		require.True(t, ok)

		pxDaemonSet, objects := extractPortworxDaemonSetFromObjects(objects)
		require.NotNil(t, pxDaemonSet)

		// Remove affinity from portworx daemonset
		pxDaemonSet.Spec.Template.Spec.Affinity = nil

		testMigration(t, objects, pxDaemonSet)
	}
}

func MigrationWithAllComponents(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		objects, err := ci_utils.ParseSpecs("migration/prometheus-operator.yaml")
		require.NoError(t, err)

		logrus.Infof("Installing prometheus operator")
		err = ci_utils.CreateObjects(objects)
		require.NoError(t, err)

		logrus.Infof("Waiting for prometheus operator to be ready")
		dep := &appsv1.Deployment{}
		dep.Name = "prometheus-operator"
		dep.Namespace = apiscore.NamespaceSystem
		err = appops.Instance().ValidateDeployment(dep, ci_utils.DefaultValidateDeployTimeout, 5*time.Second)
		require.NoError(t, err)

		MigrationWithoutNodeAffinity(tc)(t)

		logrus.Infof("Validate old prometheus operator is removed as well")
		err = ci_utils.ValidateObjectsAreTerminated(objects, true)
		require.NoError(t, err)
	}
}

func testMigration(t *testing.T, objects []runtime.Object, ds *appsv1.DaemonSet) {
	err := ci_utils.AddPortworxImageToDaemonSet(ds)
	require.NoError(t, err)

	err = updateImagesWithKubernetesVersion(objects)
	require.NoError(t, err)

	logrus.Infof("Creating portworx objects")
	err = ci_utils.CreateObjects(objects)
	require.NoError(t, err)

	logrus.Infof("Creating portworx daemonset")
	_, err = appops.Instance().CreateDaemonSet(ds, metav1.CreateOptions{})
	require.NoError(t, err)

	logrus.Infof("Waiting for portworx daemonset to be ready")
	err = appops.Instance().ValidateDaemonSet(ds.Name, ds.Namespace, ci_utils.DefaultValidateDeployTimeout)
	require.NoError(t, err)

	logrus.Infof("Restarting portworx operator to trigger migration")
	restartPortworxOperator(t)

	logrus.Infof("Validate equivalient StorageCluster has been created")
	cluster, err := validateStorageClusterIsCreatedForDaemonSet(ds)
	require.NoError(t, err)

	logrus.Infof("Approving migration for StorageCluster %s", cluster.Name)
	cluster, err = approveMigration(cluster)
	require.NoError(t, err)

	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster,
		ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true)
	require.NoError(t, err)

	logrus.Infof("Validate original spec is deleted")
	err = appops.Instance().ValidateDaemonSetIsTerminated(ds.Name, ds.Namespace, 5*time.Minute)
	require.NoError(t, err)

	ci_utils.UninstallAndValidateStorageCluster(cluster, t)

	logrus.Infof("Validate migration labels have been removed from nodes")
	nodeList, err := coreops.Instance().GetNodes()
	for _, node := range nodeList.Items {
		_, exists := node.Labels[constants.LabelPortworxDaemonsetMigration]
		require.False(t, exists, "%s label should not have been present on node %s",
			constants.LabelPortworxDaemonsetMigration, node.Name)
	}

	logrus.Infof("Validate old portworx objects are removed")
	err = ci_utils.ValidateObjectsAreTerminated(objects, true)
	require.NoError(t, err)

	logrus.Infof("Remove remaining portworx objects")
	err = ci_utils.DeleteObjects(objects)
	require.NoError(t, err)
	err = ci_utils.ValidateObjectsAreTerminated(objects, false)
	require.NoError(t, err)
}

func approveMigration(cluster *corev1.StorageCluster) (*corev1.StorageCluster, error) {
	cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	return operatorops.Instance().UpdateStorageCluster(cluster)
}

func validateStorageClusterIsCreatedForDaemonSet(ds *appsv1.DaemonSet) (*corev1.StorageCluster, error) {
	clusterName := getClusterNameFromDaemonSet(ds)
	t := func() (interface{}, bool, error) {
		cluster, err := operatorops.Instance().GetStorageCluster(clusterName, ds.Namespace)
		if err != nil {
			return nil, true, err
		}

		value, ok := cluster.Annotations[constants.AnnotationMigrationApproved]
		if !ok {
			return nil, true, fmt.Errorf("%s annotation is not present on StorageCluster", constants.AnnotationMigrationApproved)
		}
		approved, err := strconv.ParseBool(value)
		if err != nil {
			return nil, true, fmt.Errorf("invalid value %s assigned to annotation %s. %v",
				value, constants.AnnotationMigrationApproved, err)
		} else if approved {
			return nil, true, fmt.Errorf("migration should not be auto-approved")
		}

		return cluster, false, nil
	}
	cluster, err := task.DoRetryWithTimeout(t, clusterCreationTimeout, clusterCreationRetryInterval)
	if err != nil {
		return nil, err
	}
	return cluster.(*corev1.StorageCluster), nil
}

func getClusterNameFromDaemonSet(ds *appsv1.DaemonSet) string {
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == "portworx" {
			for i, arg := range c.Args {
				if arg == "-c" {
					return c.Args[i+1]
				}
			}
		}
	}
	return ""
}

func extractPortworxDaemonSetFromObjects(objects []runtime.Object) (*appsv1.DaemonSet, []runtime.Object) {
	var pxDaemonSet *appsv1.DaemonSet
	remainingObjects := []runtime.Object{}
	for _, obj := range objects {
		if daemonSet, ok := obj.(*appsv1.DaemonSet); ok && daemonSet.Name == "portworx" {
			pxDaemonSet = daemonSet.DeepCopy()
		} else {
			remainingObjects = append(remainingObjects, obj.DeepCopyObject())
		}
	}
	return pxDaemonSet, remainingObjects
}

func updateImagesWithKubernetesVersion(objects []runtime.Object) error {
	k8sVersion, err := k8sutil.GetVersion()
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if dep, ok := obj.(*appsv1.Deployment); ok &&
			(dep.Name == "stork-scheduler" || dep.Name == "portworx-pvc-controller") {
			image := strings.Split(dep.Spec.Template.Spec.Containers[0].Image, ":")[0]
			dep.Spec.Template.Spec.Containers[0].Image = image + ":v" + k8sVersion.String()
		}
	}
	return nil
}

func restartPortworxOperator(t *testing.T) {
	dep, err := appops.Instance().GetDeployment(ci_utils.PortworxOperatorDeploymentName, apiscore.NamespaceSystem)
	require.NoError(t, err)

	pods, err := appops.Instance().GetDeploymentPods(dep)
	require.NoError(t, err)

	err = coreops.Instance().DeletePods(pods, false)
	require.NoError(t, err)

	err = appops.Instance().ValidateDeployment(dep, operatorRestartTimeout, operatorRestartRetryInterval)
	require.NoError(t, err)
}
