// +build integrationtest

package integrationtest

import (
	"fmt"
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
)

var migrationTestCases = []types.TestCase{
	{
		TestName:   "SimpleDaemonSetMigration",
		ShouldSkip: func() bool { return false },
		TestFunc:   BasicMigration,
	},
}

func TestMigrationBasic(t *testing.T) {
	for _, tc := range migrationTestCases {
		tc.RunTest(t)
	}
}

func BasicMigration(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		if tc.ShouldSkip() {
			t.Skip()
		}

		logrus.Infof("Creating portworx daemonset spec")
		ds := testutil.GetExpectedDaemonSet(t, "migration/simple-daemonset.yaml")
		require.NotNil(t, ds)

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

		logrus.Infof("Delete StorageCluster %s", cluster.Name)
		err = testutil.UninstallStorageCluster(cluster)
		require.NoError(t, err)

		logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
		err = testutil.ValidateUninstallStorageCluster(cluster, ci_utils.DefaultValidateUninstallTimeout, ci_utils.DefaultValidateUninstallRetryInterval)
		require.NoError(t, err)
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
	return testutil.GetImagesFromVersionURL(versionURL, ci_utils.K8sVersion)
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
