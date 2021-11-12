package migration

import (
	"testing"
	"time"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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
