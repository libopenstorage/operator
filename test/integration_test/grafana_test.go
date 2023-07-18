//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"testing"
	"time"

	testutil "github.com/libopenstorage/operator/pkg/util/test"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	"github.com/portworx/sched-ops/k8s/operator"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	grafanaTestSpecUninstalled = ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "px-test-grafana"},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{Enabled: false},
				Grafana:    &corev1.GrafanaSpec{Enabled: false},
				Telemetry:  &corev1.TelemetrySpec{Enabled: false},
			},
		},
	})
)

var grafanaTestCases = []types.TestCase{
	{
		TestName:        "GrafanaInstall",
		TestrailCaseIDs: []string{"XXX", "XXX"},
		TestSpec:        grafanaTestSpecUninstalled,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				testSpec := tc.TestSpec(t)
				cluster, ok := testSpec.(*corev1.StorageCluster)
				require.True(t, ok)

				// Test Case: Grafana disabled by default
				logrus.Infof("Validate Grafana not installed by default")
				cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
				cluster, err := updateGrafanaInstallation(t, cluster, false, false)
				err = testutil.ValidateGrafana(cluster)
				require.NoError(t, err)

				// Test Case: Grafana disabled by default even with prometheus enabled
				logrus.Infof("Validate Grafana not installed with only prometheus enabled")
				cluster, err = updateGrafanaInstallation(t, cluster, true, false)
				require.NoError(t, err)
				err = testutil.ValidateGrafana(cluster)
				require.NoError(t, err)

				// Test Case: Grafana not installed when prometheus isn't enabled
				logrus.Infof("Validate Grafana not installed when prometheus isn't enabled")
				cluster, err = updateGrafanaInstallation(t, cluster, false, true)
				require.NoError(t, err)
				err = testutil.ValidateGrafana(cluster)
				require.NoError(t, err)

				// Test Case: Grafana installed once enabled with prometheus
				logrus.Infof("Validate Grafana installed once prometheus and grafana enabled")
				cluster, err = updateGrafanaInstallation(t, cluster, true, true)
				require.NoError(t, err)
				err = testutil.ValidateGrafana(cluster)
				require.NoError(t, err)
			}
		},
	},
	{
		TestName:        "GrafanaInstallVersionManifest",
		TestrailCaseIDs: []string{"XXX", "XXX"},
		TestSpec:        grafanaTestSpecUninstalled,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				testSpec := tc.TestSpec(t)
				cluster, ok := testSpec.(*corev1.StorageCluster)
				require.True(t, ok)
				err := createVersionsManifest(t)
				require.NoError(t, err)

				// Test Case: Grafana disabled by default
				logrus.Infof("Validate Grafana not installed by default")
				cluster, err = ci_utils.DeployStorageCluster(cluster, ci_utils.PxSpecImages)
				require.NoError(t, err)
				cluster, err = updateGrafanaInstallation(t, cluster, false, false)
				err = testutil.ValidateGrafanaDeploymentImage("docker.io/grafana/grafana:8.5.27")
				require.NoError(t, err)

				pxVersionManifest, err := ci_utils.ParseSpecs("monitoring/px-versions.yaml")
				require.NoError(t, err)
				err = ci_utils.DeleteObjects(pxVersionManifest)
				require.NoError(t, err)
			}
		},
	},
}

func createVersionsManifest(t *testing.T) error {
	pxVersionManifest, err := ci_utils.ParseSpecs("monitoring/px-versions.yaml")
	if err != nil {
		return err
	}

	err = ci_utils.DeleteObjects(pxVersionManifest)
	if err != nil {
		return err
	}
	check := func() (interface{}, bool, error) {
		err = ci_utils.CreateObjects(pxVersionManifest)
		if err != nil {
			return nil, true, err
		}

		return nil, false, nil
	}
	if _, err := task.DoRetryWithTimeout(check, 30*time.Second, 5*time.Second); err != nil {
		return err
	}

	return nil
}

func updateGrafanaInstallation(t *testing.T, cluster *corev1.StorageCluster, installPrometheus, installGrafana bool) (*corev1.StorageCluster, error) {
	check := func() (interface{}, bool, error) {
		cluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			return cluster, true, err
		}
		cluster.Spec.Monitoring = &corev1.MonitoringSpec{
			Prometheus: &corev1.PrometheusSpec{
				Enabled: installPrometheus,
			},
			Grafana: &corev1.GrafanaSpec{
				Enabled: installGrafana,
			},
		}
		cluster, err = ci_utils.UpdateStorageCluster(cluster)
		if err != nil {
			return cluster, true, err
		}

		return cluster, false, nil
	}
	if _, err := task.DoRetryWithTimeout(check, 30*time.Second, 5*time.Second); err != nil {
		return cluster, err
	}

	return cluster, nil
}

func TestGrafana(t *testing.T) {
	for _, testCase := range grafanaTestCases {
		testCase.RunTest(t)
	}
}
