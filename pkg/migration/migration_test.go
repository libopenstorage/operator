package migration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/task"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestStorageClusterIsCreatedFromOnPremDaemonset(t *testing.T) {
	clusterName := "px-cluster"
	maxUnavailable := intstr.FromInt(3)
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					ImagePullSecrets: []v1.LocalObjectReference{
						{
							Name: "pull-secret",
						},
					},
					Containers: []v1.Container{
						{
							Name:            "portworx",
							Image:           "portworx/test:version",
							ImagePullPolicy: v1.PullIfNotPresent,
							Args: []string{
								"-c", clusterName,
								"-x", "k8s",
								"-a", "-A", "-f",
								"-s", "/dev/sda1", "-s", "/dev/sda2",
								"-j", "/dev/sdb",
								"-metadata", "/dev/sdc",
								"-kvdb_dev", "/dev/sdd",
								"-cache", "/dev/sde1", "-cache", "/dev/sde2",
								"-b",
								"-d", "eth1",
								"-m", "eth2",
								"-secret_type", "vault",
								"-r", "10001",
								"-rt_opts", "opt1=100,opt2=999",
								"-marketplace_name", "OperatorHub",
								"-csi_endpoint", "csi/endpoint",
								"-csiversion", "0.3",
								"-oidc_issuer", "OIDC_ISSUER",
								"-oidc_client_id", "OIDC_CLIENT_ID",
								"-oidc_custom_claim_namespace", "OIDC_CUSTOM_NS",
								"--log", "/tmp/log/location",
								"-extra_arg1",
								"-extra_arg2", "value2",
								"-extra_arg3", "value3",
							},
							Env: []v1.EnvVar{
								{
									Name:  "PX_TEMPLATE_VERSION",
									Value: "v3",
								},
								{
									Name:  "TEST_ENV_1",
									Value: "value1",
								},
							},
						},
					},
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{
									{
										MatchExpressions: []v1.NodeSelectorRequirement{
											{
												Key:      "px/enabled",
												Operator: v1.NodeSelectorOpNotIn,
												Values:   []string{"false"},
											},
										},
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      "taint_key",
							Operator: v1.TolerationOpExists,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	ctrl := &storagecluster.Controller{}
	ctrl.SetKubernetesClient(k8sClient)
	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
				pxutil.AnnotationMiscArgs:             "-extra_arg1 -extra_arg2 value2 -extra_arg3 value3",
				pxutil.AnnotationLogFile:              "/tmp/log/location",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/test:version",
			ImagePullPolicy: v1.PullIfNotPresent,
			ImagePullSecret: stringPtr("pull-secret"),
			SecretsProvider: stringPtr("vault"),
			StartPort:       uint32Ptr("10001"),
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll:               boolPtr(true),
					UseAllWithPartitions: boolPtr(true),
					ForceUseDisks:        boolPtr(true),
					Devices:              stringSlicePtr([]string{"/dev/sda1", "/dev/sda2"}),
					CacheDevices:         stringSlicePtr([]string{"/dev/sde1", "/dev/sde2"}),
					JournalDevice:        stringPtr("/dev/sdb"),
					SystemMdDevice:       stringPtr("/dev/sdc"),
					KvdbDevice:           stringPtr("/dev/sdd"),
				},
				Network: &corev1.NetworkSpec{
					DataInterface: stringPtr("eth1"),
					MgmtInterface: stringPtr("eth2"),
				},
				RuntimeOpts: map[string]string{
					"opt1": "100",
					"opt2": "999",
				},
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_AUTH_OIDC_ISSUER",
						Value: "OIDC_ISSUER",
					},
					{
						Name:  "PORTWORX_AUTH_OIDC_CLIENTID",
						Value: "OIDC_CLIENT_ID",
					},
					{
						Name:  "PORTWORX_AUTH_OIDC_CUSTOM_NAMESPACE",
						Value: "OIDC_CUSTOM_NS",
					},
					{
						Name:  "MARKETPLACE_NAME",
						Value: "OperatorHub",
					},
					{
						Name:  "CSI_ENDPOINT",
						Value: "csi/endpoint",
					},
					{
						Name:  "PORTWORX_CSIVERSION",
						Value: "0.3",
					},
					{
						Name:  "TEST_ENV_1",
						Value: "value1",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
			Placement: &corev1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
								},
							},
						},
					},
				},
				Tolerations: []v1.Toleration{
					{
						Key:      "taint_key",
						Operator: v1.TolerationOpExists,
						Effect:   v1.TaintEffectNoExecute,
					},
				},
			},
			UpdateStrategy: corev1.StorageClusterUpdateStrategy{
				Type: corev1.RollingUpdateStorageClusterStrategyType,
				RollingUpdate: &corev1.RollingUpdateStorageCluster{
					MaxUnavailable: &maxUnavailable,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status, cluster.Status)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterIsCreatedFromCloudDaemonset(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            "portworx",
							Image:           "portworx/test:version",
							ImagePullPolicy: v1.PullNever,
							Args: []string{
								"-c", clusterName,
								"-s", "type=disk", "-s", "type=disk",
								"-j", "type=journal",
								"-metadata", "type=md",
								"-kvdb_dev", "type=kvdb",
								"-max_drive_set_count", "10",
								"-max_storage_nodes_per_zone", "5",
								"-max_storage_nodes_per_zone_per_nodegroup", "1",
								"-node_pool_label", "px/storage",
								"-cloud_provider", "aws",
								"-k", "etcd:http://etcd-1.com:1111,etcd:http://etcd-2.com:1111",
								"-ca", "/etc/pwx/ca",
								"-cert", "/etc/pwx/cert",
								"-key", "/etc/pwx/key",
								"-acltoken", "acltoken",
								"-userpwd", "userpwd",
								"-jwt_issuer", "jwt_issuer",
								"-jwt_shared_secret", "shared_secret",
								"-jwt_rsa_pubkey_file", "rsa_file",
								"-jwt_ecds_pubkey_file", "ecds_file",
								"-username_claim", "username_claim",
								"-auth_system_key", "system_key",
							},
							Env: []v1.EnvVar{
								{
									Name:  "PX_TEMPLATE_VERSION",
									Value: "v3",
								},
								{
									Name:  "TEST_ENV_1",
									Value: "value1",
								},
							},
						},
					},
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.OnDeleteDaemonSetStrategyType,
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	ctrl := &storagecluster.Controller{}
	ctrl.SetKubernetesClient(k8sClient)
	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/test:version",
			ImagePullPolicy: v1.PullNever,
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd:http://etcd-1.com:1111",
					"etcd:http://etcd-2.com:1111",
				},
			},
			CloudStorage: &corev1.CloudStorageSpec{
				Provider: stringPtr("aws"),
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs:                        stringSlicePtr([]string{"type=disk", "type=disk"}),
					JournalDeviceSpec:                  stringPtr("type=journal"),
					SystemMdDeviceSpec:                 stringPtr("type=md"),
					KvdbDeviceSpec:                     stringPtr("type=kvdb"),
					MaxStorageNodesPerZonePerNodeGroup: uint32Ptr("1"),
				},
				MaxStorageNodes:        uint32Ptr("10"),
				MaxStorageNodesPerZone: uint32Ptr("5"),
				NodePoolLabel:          "px/storage",
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_AUTH_JWT_ISSUER",
						Value: "jwt_issuer",
					},
					{
						Name:  "PORTWORX_AUTH_JWT_SHAREDSECRET",
						Value: "shared_secret",
					},
					{
						Name:  "PORTWORX_AUTH_JWT_RSA_PUBKEY",
						Value: "rsa_file",
					},
					{
						Name:  "PORTWORX_AUTH_JWT_ECDS_PUBKEY",
						Value: "ecds_file",
					},
					{
						Name:  "PORTWORX_AUTH_USERNAME_CLAIM",
						Value: "username_claim",
					},
					{
						Name:  "PORTWORX_AUTH_SYSTEM_KEY",
						Value: "system_key",
					},
					{
						Name:  "TEST_ENV_1",
						Value: "value1",
					},
				},
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
			UpdateStrategy: corev1.StorageClusterUpdateStrategy{
				Type: corev1.OnDeleteStorageClusterStrategyType,
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status, cluster.Status)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestWhenStorageClusterIsAlreadyPresent(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{"-c", clusterName, "-a"},
						},
					},
				},
			},
		},
	}

	// Cluster with some missing params from the daemonset
	existingCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, existingCluster)
	ctrl := &storagecluster.Controller{}
	ctrl.SetKubernetesClient(k8sClient)
	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	stc := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, stc, clusterName, ds.Namespace)
	require.NoError(t, err)
	// The storage cluster object did not change even when the daemonset
	// is different now
	require.Nil(t, stc.Spec.Storage)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestWhenPortworxDaemonsetIsNotPresent(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	ctrl := &storagecluster.Controller{}
	ctrl.SetKubernetesClient(k8sClient)
	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	clusterList := &corev1.StorageClusterList{}
	err := testutil.List(k8sClient, clusterList)
	require.NoError(t, err)
	require.Empty(t, clusterList.Items)
}

func TestStorageClusterSpecWithComponents(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
						{
							Name: pxutil.CSIRegistrarContainerName,
						},
						{
							Name: pxutil.TelemetryContainerName,
						},
					},
				},
			},
		},
	}
	storkDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	autopilotDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.AutopilotDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	pvcControllerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.PVCDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusInstanceName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.PrometheusSpec{
			RuleSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"prometheus": "portworx",
				},
			},
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": serviceMonitorName,
				},
			},
		},
	}
	serviceMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceMonitorName,
			Namespace: ds.Namespace,
		},
	}
	alertManager := &monitoringv1.Alertmanager{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.AlertManagerInstanceName,
			Namespace: ds.Namespace,
		},
	}

	k8sClient := testutil.FakeK8sClient(
		ds, storkDeployment, autopilotDeployment,
		pvcControllerDeployment, prometheus,
		serviceMonitor, alertManager,
	)
	ctrl := &storagecluster.Controller{}
	ctrl.SetKubernetesClient(k8sClient)
	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CSI: &corev1.CSISpec{
				Enabled:                   true,
				InstallSnapshotController: boolPtr(true),
			},
			Stork: &corev1.StorkSpec{
				Enabled: true,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: true,
					Enabled:       true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status, cluster.Status)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterSpecWithPVCControllerInKubeSystem(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}
	pvcControllerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.PVCDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusInstanceName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.PrometheusSpec{
			RuleSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"prometheus": "portworx",
				},
			},
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": serviceMonitorName,
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, pvcControllerDeployment, prometheus)
	ctrl := &storagecluster.Controller{}
	ctrl.SetKubernetesClient(k8sClient)
	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
				pxutil.AnnotationPVCController:        "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					// Prometheus is not enabled although prometheus is present. This is because
					// the portworx metrics are not exported nor alert manager is running.
					Enabled: false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status, cluster.Status)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestSuccessfulMigration(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	migrationRetryIntervalFunc = func() time.Duration {
		return 2 * time.Second
	}
	defer func() {
		migrationRetryIntervalFunc = getMigrationRetryInterval
	}()

	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
			UID:       "100",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}

	numNodes := 2
	nodes := []*v1.Node{}
	dsPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		nodes = append(nodes, constructNode(i))
		dsPods = append(dsPods, constructDaemonSetPod(ds, i))
	}

	k8sClient := testutil.FakeK8sClient(ds)
	driver := testutil.MockDriver(mockCtrl)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetKubernetesClient(k8sClient)

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()

	for i := 0; i < numNodes; i++ {
		err := k8sClient.Create(context.TODO(), nodes[i])
		require.NoError(t, err)
		err = k8sClient.Create(context.TODO(), dsPods[i])
		require.NoError(t, err)
	}

	// Start the migration handler
	migrator := New(ctrl)
	go migrator.Start()
	time.Sleep(2 * time.Second)

	// Check cluster's initial status before user approval
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, constants.PhaseAwaitingApproval, cluster.Status.Phase)

	// Approve migration
	cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	err = testutil.Update(k8sClient, cluster)
	require.NoError(t, err)

	// Validate the migration has started
	err = validateMigrationIsInProgress(k8sClient, cluster)
	require.NoError(t, err)

	time.Sleep(time.Second)

	// Validate daemonset has been updated
	currDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.NoError(t, err)

	require.Equal(t, appsv1.OnDeleteDaemonSetStrategyType, currDaemonSet.Spec.UpdateStrategy.Type)
	require.Equal(t, expectedDaemonSetAffinity(), currDaemonSet.Spec.Template.Spec.Affinity)

	// Validate components have been paused
	cluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, "true", cluster.Annotations[constants.AnnotationPauseComponentMigration])

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	// Delete the daemonset pod so migration can proceed on first node
	err = testutil.Delete(k8sClient, dsPods[0])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	operatorPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		operatorPods = append(operatorPods, constructOperatorPod(cluster, i))
	}

	// Create an operator pod so migration can proceed on first node
	err = k8sClient.Create(context.TODO(), operatorPods[0])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)

	// Delete daemonset pod and create an operator pod on second node for migration to proceed
	err = testutil.Delete(k8sClient, dsPods[1])
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), operatorPods[1])
	require.NoError(t, err)

	// Validate the migration has completed
	err = validateNodeStatus(k8sClient, nodes[0].Name, "")
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, "")
	require.NoError(t, err)

	currDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	cluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	_, annotationExists := cluster.Annotations[constants.AnnotationPauseComponentMigration]
	require.False(t, annotationExists)
}

func TestFailedMigrationRecoveredWithSkip(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	migrationRetryIntervalFunc = func() time.Duration {
		return 2 * time.Second
	}
	daemonSetPodTerminationTimeoutFunc = func() time.Duration {
		return 2 * time.Second
	}
	operatorPodReadyTimeoutFunc = func() time.Duration {
		return 2 * time.Second
	}
	defer func() {
		migrationRetryIntervalFunc = getMigrationRetryInterval
		daemonSetPodTerminationTimeoutFunc = getDaemonSetPodTerminationTimeout
		operatorPodReadyTimeoutFunc = getOperatorPodReadyTimeout
	}()

	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
			UID:       "100",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}

	numNodes := 3
	nodes := []*v1.Node{}
	dsPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		nodes = append(nodes, constructNode(i))
		dsPods = append(dsPods, constructDaemonSetPod(ds, i))
	}

	k8sClient := testutil.FakeK8sClient(ds)
	driver := testutil.MockDriver(mockCtrl)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetKubernetesClient(k8sClient)

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()

	for i := 0; i < numNodes; i++ {
		err := k8sClient.Create(context.TODO(), nodes[i])
		require.NoError(t, err)
		err = k8sClient.Create(context.TODO(), dsPods[i])
		require.NoError(t, err)
	}

	// Start the migration handler
	migrator := New(ctrl)
	go migrator.Start()
	time.Sleep(2 * time.Second)

	// Check cluster's initial status before user approval
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, constants.PhaseAwaitingApproval, cluster.Status.Phase)

	// Approve migration
	cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	err = testutil.Update(k8sClient, cluster)
	require.NoError(t, err)

	// Validate the migration has started
	err = validateMigrationIsInProgress(k8sClient, cluster)
	require.NoError(t, err)

	operatorPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		operatorPods = append(operatorPods, constructOperatorPod(cluster, i))
	}

	// Delete daemonset pod and create an operator pod on first node for migration to proceed
	err = testutil.Delete(k8sClient, dsPods[0])
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), operatorPods[0])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	// We wait until the daemonset termination wait routine fails
	time.Sleep(3 * time.Second)

	// Validate status of the nodes
	// The status should not change as the daemonset pod is not yet terminated
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	// Mark the node as skipped
	node2 := &v1.Node{}
	err = testutil.Get(k8sClient, node2, nodes[1].Name, "")
	require.NoError(t, err)
	node2.Labels = map[string]string{
		constants.LabelPortworxDaemonsetMigration: constants.LabelValueMigrationSkip,
	}
	err = testutil.Update(k8sClient, node2)
	require.NoError(t, err)

	// Delete the third daemonset pod so third node can proceed
	err = testutil.Delete(k8sClient, dsPods[2])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)

	// We wait until the wait routine for pod to be ready fails
	time.Sleep(5 * time.Second)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)

	// Create the operator pod now, simulating it took much longer than usual,
	// but don't mark the pod as ready so it fails again
	operatorPods[2].Status.Conditions = nil
	err = k8sClient.Create(context.TODO(), operatorPods[2])
	require.NoError(t, err)

	// We wait until the wait routine for pod to be ready fails
	time.Sleep(5 * time.Second)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)

	// Mark the operator pod as ready now, simulating it took much longer for the pod
	// to get ready	and verify the migration still continued trying and eventually succeeded
	operatorPods[2].Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Update(context.TODO(), operatorPods[2])
	require.NoError(t, err)

	// Validate the migration has completed
	// Do not remove the labels from skipped nodes as user has added that label
	err = validateNodeStatus(k8sClient, nodes[0].Name, "")
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, "")
	require.NoError(t, err)

	currDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestOldComponentsAreDeleted(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	migrationRetryIntervalFunc = func() time.Duration {
		return 2 * time.Second
	}
	defer func() {
		migrationRetryIntervalFunc = getMigrationRetryInterval
	}()

	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-test",
			UID:       "100",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}
	pxClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: pxClusterRoleName,
		},
	}
	pxClusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: pxClusterRoleBindingName,
		},
	}
	pxRoleLocal := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleName,
			Namespace: ds.Namespace,
		},
	}
	pxRoleBindingLocal := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleBindingName,
			Namespace: ds.Namespace,
		},
	}
	pxRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleName,
			Namespace: secretsNamespace,
		},
	}
	pxRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleBindingName,
			Namespace: secretsNamespace,
		},
	}
	pxAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxAccountName,
			Namespace: ds.Namespace,
		},
	}
	pxSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: ds.Namespace,
		},
	}
	pxAPIDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxAPIDaemonSetName,
			Namespace: ds.Namespace,
		},
	}
	pxAPISvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxAPIServiceName,
			Namespace: ds.Namespace,
		},
	}
	storkDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	storkSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkServiceName,
			Namespace: ds.Namespace,
		},
	}
	storkConfig := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkConfigName,
			Namespace: ds.Namespace,
		},
	}
	storkRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: storkRoleName,
		},
	}
	storkRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: storkRoleBindingName,
		},
	}
	storkAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkAccountName,
			Namespace: ds.Namespace,
		},
	}
	schedDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      schedDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	schedRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: schedRoleName,
		},
	}
	schedRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: schedRoleBindingName,
		},
	}
	schedAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      schedAccountName,
			Namespace: ds.Namespace,
		},
	}
	autopilotDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	autopilotSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotServiceName,
			Namespace: ds.Namespace,
		},
	}
	autopilotConfig := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotConfigName,
			Namespace: ds.Namespace,
		},
	}
	autopilotRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: autopilotRoleName,
		},
	}
	autopilotRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: autopilotRoleBindingName,
		},
	}
	autopilotAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotAccountName,
			Namespace: ds.Namespace,
		},
	}
	pvcDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	pvcRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvcRoleName,
		},
	}
	pvcRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvcRoleBindingName,
		},
	}
	pvcAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcAccountName,
			Namespace: ds.Namespace,
		},
	}
	csiDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	csiSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiServiceName,
			Namespace: ds.Namespace,
		},
	}
	csiRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiRoleName,
		},
	}
	csiRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiRoleBindingName,
		},
	}
	csiAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiAccountName,
			Namespace: ds.Namespace,
		},
	}
	promOpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusOpDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	promOpRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusOpRoleName,
		},
	}
	promOpRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusOpRoleBindingName,
		},
	}
	promOpAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusOpAccountName,
			Namespace: ds.Namespace,
		},
	}
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusInstanceName,
			Namespace: ds.Namespace,
		},
	}
	prometheusSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prometheus",
			Namespace: ds.Namespace,
		},
	}
	prometheusRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusRoleName,
		},
	}
	prometheusRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusRoleBindingName,
		},
	}
	prometheusAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusAccountName,
			Namespace: ds.Namespace,
		},
	}
	serviceMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceMonitorName,
			Namespace: ds.Namespace,
		},
	}
	prometheusRule := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusRuleName,
			Namespace: ds.Namespace,
		},
	}
	alertManager := &monitoringv1.Alertmanager{
		ObjectMeta: metav1.ObjectMeta{
			Name:      alertManagerName,
			Namespace: ds.Namespace,
		},
	}
	alertManagerSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      alertManagerServiceName,
			Namespace: ds.Namespace,
		},
	}

	k8sClient := testutil.FakeK8sClient(
		ds, pxAPIDaemonSet, pxAPISvc, pxSvc,
		pxClusterRole, pxClusterRoleBinding, pxRole, pxRoleBinding, pxRoleLocal, pxRoleBindingLocal, pxAccount,
		storkDeployment, storkSvc, storkConfig, storkRole, storkRoleBinding, storkAccount,
		schedDeployment, schedRole, schedRoleBinding, schedAccount,
		autopilotDeployment, autopilotConfig, autopilotSvc, autopilotRole, autopilotRoleBinding, autopilotAccount,
		pvcDeployment, pvcRole, pvcRoleBinding, pvcAccount,
		csiDeployment, csiSvc, csiRole, csiRoleBinding, csiAccount,
		serviceMonitor, prometheusRule, alertManager, alertManagerSvc,
		prometheus, prometheusSvc, prometheusRole, prometheusRoleBinding, prometheusAccount,
		promOpDeployment, promOpRole, promOpRoleBinding, promOpAccount,
	)
	driver := testutil.MockDriver(mockCtrl)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetKubernetesClient(k8sClient)

	// Start the migration handler
	migrator := New(ctrl)
	go migrator.Start()
	time.Sleep(2 * time.Second)

	// Check cluster's initial status before user approval
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, constants.PhaseAwaitingApproval, cluster.Status.Phase)

	// Approve migration
	cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	err = testutil.Update(k8sClient, cluster)
	require.NoError(t, err)

	time.Sleep(1 * time.Second)

	// Validate all components have been deleted
	pxClusterRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, pxClusterRole, pxClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	pxClusterRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxClusterRoleBinding, pxClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pxRoleLocal = &rbacv1.Role{}
	err = testutil.Get(k8sClient, pxRoleLocal, pxRoleName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxRoleBindingLocal = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, pxRoleBindingLocal, pxRoleBindingName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxRole = &rbacv1.Role{}
	err = testutil.Get(k8sClient, pxRole, pxRoleName, secretsNamespace)
	require.True(t, errors.IsNotFound(err))

	pxRoleBinding = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, pxRoleBinding, pxRoleBindingName, secretsNamespace)
	require.True(t, errors.IsNotFound(err))

	pxAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxAccount, pxAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxSvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxSvc, pxServiceName, ds.Namespace)
	require.False(t, errors.IsNotFound(err))

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, pxAPIDaemonSetName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxAPISvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPISvc, pxAPIServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSvc = &v1.Service{}
	err = testutil.Get(k8sClient, storkSvc, storkServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkConfig = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkConfig, storkConfigName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkRole, storkRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkRoleBinding, storkRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkAccount, storkAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, schedDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedRole, schedRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedRoleBinding, schedRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, schedAccount, schedAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, autopilotDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotSvc = &v1.Service{}
	err = testutil.Get(k8sClient, autopilotSvc, autopilotServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotConfig = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, autopilotConfig, autopilotConfigName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, autopilotRole, autopilotRoleName, "")
	require.True(t, errors.IsNotFound(err))

	autopilotRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, autopilotRoleBinding, autopilotRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	autopilotAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, autopilotAccount, autopilotAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, pvcDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pvcRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, pvcRole, pvcRoleName, "")
	require.True(t, errors.IsNotFound(err))

	pvcRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pvcRoleBinding, pvcRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pvcAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pvcAccount, pvcAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, csiDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiSvc = &v1.Service{}
	err = testutil.Get(k8sClient, csiSvc, csiServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, csiRole, csiRoleName, "")
	require.True(t, errors.IsNotFound(err))

	csiRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, csiRoleBinding, csiRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	csiAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, csiAccount, csiAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	promOpDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, promOpDeployment, prometheusOpDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	promOpRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, promOpRole, prometheusOpRoleName, "")
	require.True(t, errors.IsNotFound(err))

	promOpRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, promOpRoleBinding, prometheusOpRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	promOpAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, promOpAccount, prometheusOpAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, prometheusInstanceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheusSvc = &v1.Service{}
	err = testutil.Get(k8sClient, prometheusSvc, prometheusServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheusRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, prometheusRole, prometheusRoleName, "")
	require.True(t, errors.IsNotFound(err))

	prometheusRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, prometheusRoleBinding, prometheusRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	prometheusAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, prometheusAccount, prometheusAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, serviceMonitorName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheusRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, prometheusRuleName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	alertManager = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManager, alertManagerName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	alertManagerSvc = &v1.Service{}
	err = testutil.Get(k8sClient, alertManagerSvc, alertManagerServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Validate the migration has completed
	currDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	cluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	_, annotationExists := cluster.Annotations[constants.AnnotationPauseComponentMigration]
	require.False(t, annotationExists)
}

func validateMigrationIsInProgress(
	k8sClient client.Client,
	cluster *corev1.StorageCluster,
) error {
	f := func() (interface{}, bool, error) {
		currCluster := &corev1.StorageCluster{}
		if err := testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace); err != nil {
			return nil, true, err
		}
		if currCluster.Status.Phase != constants.PhaseMigrationInProgress {
			return nil, true, fmt.Errorf("migration status expected to be in progress")
		}
		return nil, false, nil
	}
	_, err := task.DoRetryWithTimeout(f, 35*time.Second, 2*time.Second)
	return err
}

func validateNodeStatus(
	k8sClient client.Client,
	nodeName, expectedStatus string,
) error {
	f := func() (interface{}, bool, error) {
		node := &v1.Node{}
		if err := testutil.Get(k8sClient, node, nodeName, ""); err != nil {
			return nil, true, err
		}
		currStatus := node.Labels[constants.LabelPortworxDaemonsetMigration]
		if currStatus != expectedStatus {
			return nil, true, fmt.Errorf("status of node %s: expected: %s, actual: %s",
				node.Name, expectedStatus, currStatus)
		}
		return nil, false, nil
	}
	_, err := task.DoRetryWithTimeout(f, 30*time.Second, 2*time.Second)
	return err
}

func constructNode(id int) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("k8s-node-%d", id),
		},
	}
}

func constructDaemonSetPod(ds *appsv1.DaemonSet, id int) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("daemonset-pod-%d", id),
			Namespace: ds.Namespace,
			Labels: map[string]string{
				"name": "portworx",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(ds, appsv1.SchemeGroupVersion.WithKind("DaemonSet")),
			},
		},
		Spec: v1.PodSpec{
			NodeName: fmt.Sprintf("k8s-node-%d", id),
		},
	}
}

func constructOperatorPod(cluster *corev1.StorageCluster, id int) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("operator-pod-%d", id),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelKeyDriverName:  "mock-driver",
				constants.LabelKeyClusterName: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cluster, corev1.SchemeGroupVersion.WithKind("StorageCluster")),
			},
		},
		Spec: v1.PodSpec{
			NodeName: fmt.Sprintf("k8s-node-%d", id),
		},
		Status: v1.PodStatus{
			Conditions: []v1.PodCondition{
				{
					Type:   v1.PodReady,
					Status: v1.ConditionTrue,
				},
			},
		},
	}
}

func expectedDaemonSetAffinity() *v1.Affinity {
	return &v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      constants.LabelPortworxDaemonsetMigration,
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{constants.LabelValueMigrationPending},
							},
						},
					},
				},
			},
		},
	}
}
