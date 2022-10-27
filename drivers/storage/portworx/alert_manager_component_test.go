package portworx

import (
	"context"
	"fmt"
	"testing"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAlertManagerInstall(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(1)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	cluster.Spec.Placement = nil

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Missing secret '%s/%s' containing alert manager config. "+
			"Create the secret with a valid config to use Alert Manager.",
			v1.EventTypeWarning, util.FailedComponentReason, cluster.Namespace, component.AlertManagerConfigSecretName))

	// Create an alert manager config, so alert manager install can proceed
	err = k8sClient.Create(
		context.TODO(),
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      component.AlertManagerConfigSecretName,
				Namespace: cluster.Namespace,
			},
		},
		&client.CreateOptions{},
	)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// AlertManager Service
	expectedService := testutil.GetExpectedService(t, "alertManagerService.yaml")
	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, component.AlertManagerServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedService.Name, actualService.Name)
	require.Equal(t, expectedService.Namespace, actualService.Namespace)
	require.Len(t, actualService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualService.OwnerReferences[0].Name)
	require.Equal(t, expectedService.Labels, actualService.Labels)
	require.Equal(t, expectedService.Spec, actualService.Spec)

	// AlertManager instance
	alertManagerList := &monitoringv1.AlertmanagerList{}
	err = testutil.List(k8sClient, alertManagerList)
	require.NoError(t, err)
	require.Len(t, alertManagerList.Items, 1)

	expectedAlertManager := testutil.GetExpectedAlertManager(t, "alertManagerInstance.yaml")
	alertManager := alertManagerList.Items[0]
	require.Equal(t, expectedAlertManager.Name, alertManager.Name)
	require.Equal(t, expectedAlertManager.Namespace, alertManager.Namespace)
	require.Len(t, alertManager.OwnerReferences, 1)
	require.Equal(t, cluster.Name, alertManager.OwnerReferences[0].Name)
	require.Equal(t, expectedAlertManager.Spec, alertManager.Spec)
}

func TestRemoveAlertManager(t *testing.T) {
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(
		context.TODO(),
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      component.AlertManagerConfigSecretName,
				Namespace: cluster.Namespace,
			},
		},
		&client.CreateOptions{},
	)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.AlertManagerServiceName, cluster.Namespace)
	require.NoError(t, err)

	alertManager := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManager, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)

	// Remove alert manager config
	cluster.Spec.Monitoring.Prometheus.AlertManager = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.AlertManagerServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	alertManager = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManager, component.AlertManagerInstanceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDisableAlertManager(t *testing.T) {
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(
		context.TODO(),
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      component.AlertManagerConfigSecretName,
				Namespace: cluster.Namespace,
			},
		},
		&client.CreateOptions{},
	)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.AlertManagerServiceName, cluster.Namespace)
	require.NoError(t, err)

	alertManager := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManager, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)

	// Disable alert manager
	cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.AlertManagerServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	alertManager = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManager, component.AlertManagerInstanceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}
