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
	apiscore "k8s.io/kubernetes/pkg/apis/core"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
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
		TestName:   "SimpleMigration",
		ShouldSkip: func() bool { return false },
		TestFunc:   BasicMigration,
	},
	{
		TestName:   "SimpleMigrationWithoutNodeAffinity",
		ShouldSkip: func() bool { return false },
		TestFunc:   MigrationWithoutNodeAffinity,
	},
}

func TestDaemonSetMigration(t *testing.T) {
	for _, tc := range migrationTestCases {
		tc.RunTest(t)
	}
}

func BasicMigration(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		ds := testutil.GetExpectedDaemonSet(t, "migration/simple-daemonset.yaml")
		require.NotNil(t, ds)

		nodeNameWithLabel := ci_utils.AddLabelToRandomNode(t, labelKeyPXEnabled, ci_utils.LabelValueFalse)

		testMigration(t, ds)

		ci_utils.RemoveLabelFromNode(t, nodeNameWithLabel, labelKeyPXEnabled)
	}
}

func MigrationWithoutNodeAffinity(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		ds := testutil.GetExpectedDaemonSet(t, "migration/simple-daemonset.yaml")
		require.NotNil(t, ds)

		ds.Spec.Template.Spec.Affinity = nil

		testMigration(t, ds)
	}
}

func testMigration(t *testing.T, ds *appsv1.DaemonSet) {
	logrus.Infof("Creating portworx daemonset spec")
	_, err := appops.Instance().CreateDaemonSet(ds, metav1.CreateOptions{})
	require.NoError(t, err)

	err = appops.Instance().ValidateDaemonSet(ds.Name, ds.Namespace, ci_utils.DefaultValidateDeployTimeout)
	require.NoError(t, err)

	logrus.Infof("Restarting portworx operator to trigger migration")
	restartPortworxOperator(t)

	pxImageMap, err := getPortworxImagesFromDaemonSet(t, ds)
	require.NoError(t, err)

	logrus.Infof("Validate equivalient StorageCluster has been created")
	cluster, err := validateStorageClusterIsCreatedForDaemonSet(ds)
	require.NoError(t, err)

	logrus.Infof("Approving migration for StorageCluster %s", cluster.Name)
	cluster, err = approveMigration(cluster)
	require.NoError(t, err)

	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(pxImageMap, cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true)
	require.NoError(t, err)

	logrus.Infof("Validate original spec is deleted")
	err = appops.Instance().ValidateDaemonSetIsTerminated(ds.Name, ds.Namespace, 5*time.Minute)
	require.NoError(t, err)

	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, ci_utils.DefaultValidateUninstallTimeout, ci_utils.DefaultValidateUninstallRetryInterval)
	require.NoError(t, err)

	logrus.Infof("Validate migration labels have been removed from nodes")
	nodeList, err := coreops.Instance().GetNodes()
	for _, node := range nodeList.Items {
		_, exists := node.Labels[constants.LabelPortworxDaemonsetMigration]
		require.False(t, exists, "%s label should not have been present on node %s",
			constants.LabelPortworxDaemonsetMigration, node.Name)
	}
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

func getPortworxImagesFromDaemonSet(t *testing.T, ds *appsv1.DaemonSet) (map[string]string, error) {
	var pxImage string
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == "portworx" {
			pxImage = c.Image
			break
		}
	}
	require.NotEmpty(t, pxImage)

	parts := strings.Split(pxImage, ":")
	version := parts[len(parts)-1]
	require.NotEmpty(t, version)

	versionURL := fmt.Sprintf("https://install.portworx.com/%s", version)
	return testutil.GetImagesFromVersionURL(versionURL)
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
