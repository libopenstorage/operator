package portworx

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	version "github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/mock"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/kvdb"
	"github.com/portworx/kvdb/api/bootstrap/k8s"
	"github.com/portworx/kvdb/consul"
	e2 "github.com/portworx/kvdb/etcd/v2"
	e3 "github.com/portworx/kvdb/etcd/v3"
	"github.com/portworx/kvdb/mem"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestString(t *testing.T) {
	driver := portworx{}
	require.Equal(t, pxutil.DriverName, driver.String())
}

func TestInit(t *testing.T) {
	driver := portworx{}
	k8sClient := testutil.FakeK8sClient()
	scheme := runtime.NewScheme()
	recorder := record.NewFakeRecorder(0)

	// Nil k8s client
	err := driver.Init(nil, scheme, recorder)
	require.EqualError(t, err, "kubernetes client cannot be nil")

	// Nil k8s scheme
	err = driver.Init(k8sClient, nil, recorder)
	require.EqualError(t, err, "kubernetes scheme cannot be nil")

	// Nil k8s event recorder
	err = driver.Init(k8sClient, scheme, nil)
	require.EqualError(t, err, "event recorder cannot be nil")

	// Valid k8s client
	err = driver.Init(k8sClient, scheme, recorder)
	require.NoError(t, err)
	require.Equal(t, k8sClient, driver.k8sClient)
}

func TestGetSelectorLabels(t *testing.T) {
	driver := portworx{}
	expectedLabels := map[string]string{"name": pxutil.DriverName}
	require.Equal(t, expectedLabels, driver.GetSelectorLabels())
}

func TestGetStorkDriverName(t *testing.T) {
	driver := portworx{}
	actualStorkDriverName, err := driver.GetStorkDriverName()
	require.NoError(t, err)
	require.Equal(t, storkDriverName, actualStorkDriverName)
}

func TestGetStorkEnvMap(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
	}

	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	cluster.Status.DesiredImages = &corev1.ComponentImages{
		Stork: "stork/image:2.5.0",
	}
	envVars := driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 4)
	require.Equal(t, cluster.Namespace, envVars[pxutil.EnvKeyPortworxNamespace].Value)
	require.Equal(t, component.PxAPIServiceName, envVars[pxutil.EnvKeyPortworxServiceName].Value)
	require.Equal(t, pxutil.SecurityPXSystemSecretsSecretName,
		envVars[pxutil.EnvKeyStorkPXSharedSecret].ValueFrom.SecretKeyRef.Name)
	require.Equal(t, pxutil.SecurityAppsSecretKey,
		envVars[pxutil.EnvKeyStorkPXSharedSecret].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, "apps.portworx.io", envVars[pxutil.EnvKeyStorkPXJwtIssuer].Value)

	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
		Image:   "stork/image:2.5.0",
	}
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 4)
	require.Len(t, envVars, 4)
	require.Equal(t, cluster.Namespace, envVars[pxutil.EnvKeyPortworxNamespace].Value)
	require.Equal(t, component.PxAPIServiceName, envVars[pxutil.EnvKeyPortworxServiceName].Value)
	require.Equal(t, pxutil.SecurityPXSystemSecretsSecretName,
		envVars[pxutil.EnvKeyStorkPXSharedSecret].ValueFrom.SecretKeyRef.Name)
	require.Equal(t, pxutil.SecurityAppsSecretKey,
		envVars[pxutil.EnvKeyStorkPXSharedSecret].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, "apps.portworx.io", envVars[pxutil.EnvKeyStorkPXJwtIssuer].Value)

	cluster.Spec.Image = "portworx/image:2.5.0"
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 4)
	require.Equal(t, pxutil.SecurityPXSystemSecretsSecretName,
		envVars[pxutil.EnvKeyStorkPXSharedSecret].ValueFrom.SecretKeyRef.Name)
	require.Equal(t, pxutil.SecurityAppsSecretKey,
		envVars[pxutil.EnvKeyStorkPXSharedSecret].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, "stork.openstorage.io", envVars[pxutil.EnvKeyStorkPXJwtIssuer].Value)

	cluster.Spec.Security.Enabled = false
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 2)
}

func TestSetDefaultsOnStorageCluster(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedPlacement := &corev1.PlacementSpec{
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
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
						},
					},
				},
			},
		},
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	// Use default image from release manifest when spec.image is not set
	require.Equal(t, defaultPortworxImage+":3.0.0", cluster.Spec.Image)
	require.Equal(t, "3.0.0", cluster.Spec.Version)
	require.Equal(t, "3.0.0", cluster.Status.Version)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(pxutil.DefaultStartPort), *cluster.Spec.StartPort)
	require.True(t, *cluster.Spec.Storage.UseAll)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// Use default image from release manifest when spec.image has empty string
	cluster.Spec.Image = "  "
	cluster.Spec.Version = "  "
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, defaultPortworxImage+":3.0.0", cluster.Spec.Image)
	require.Equal(t, "3.0.0", cluster.Spec.Version)
	require.Equal(t, "3.0.0", cluster.Status.Version)

	// Don't use default image when spec.image has a value
	cluster.Spec.Image = "foo/image:1.0.0"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "foo/image:1.0.0", cluster.Spec.Image)
	require.Equal(t, "1.0.0", cluster.Spec.Version)
	require.Equal(t, "1.0.0", cluster.Status.Version)

	// Populate version and image tag even if not present
	cluster.Spec.Image = "test/image"
	cluster.Spec.Version = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "test/image:3.0.0", cluster.Spec.Image)
	require.Equal(t, "3.0.0", cluster.Spec.Version)
	require.Equal(t, "3.0.0", cluster.Status.Version)

	// Empty kvdb spec should still set internal kvdb as default
	cluster.Spec.Kvdb = &corev1.KvdbSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)

	// Should not overwrite complete kvdb spec if endpoints are empty
	cluster.Spec.Kvdb = &corev1.KvdbSpec{
		AuthSecret: "test-secret",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, "test-secret", cluster.Spec.Kvdb.AuthSecret)

	// If endpoints are set don't set internal kvdb
	cluster.Spec.Kvdb = &corev1.KvdbSpec{
		Endpoints: []string{"endpoint1"},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.Kvdb.Internal)

	// Don't overwrite secrets provider if already set
	cluster.Spec.SecretsProvider = stringPtr("aws-kms")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "aws-kms", *cluster.Spec.SecretsProvider)

	// Don't overwrite secrets provider if set to empty
	cluster.Spec.SecretsProvider = stringPtr("")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "", *cluster.Spec.SecretsProvider)

	// Don't overwrite start port if already set
	startPort := uint32(10001)
	cluster.Spec.StartPort = &startPort
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, uint32(10001), *cluster.Spec.StartPort)

	// Do not use default storage config if cloud storage config present
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.Storage = nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage)

	// Add default storage config if cloud storage and storage config are both present
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.Storage = &corev1.StorageSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Storage.UseAll)

	// Do no use default storage config if devices is not nil
	devices := make([]string, 0)
	cluster.Spec.Storage = &corev1.StorageSpec{
		Devices: &devices,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	devices = append(devices, "/dev/sda")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	// Do not set useAll if useAllWithPartitions is true
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	// Should set useAll if useAllWithPartitions is false
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(false),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Storage.UseAll)

	// Do not change useAll if already has a value
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll: boolPtr(false),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, *cluster.Spec.Storage.UseAll)

	// Add default placement if node placement is nil
	cluster.Spec.Placement = &corev1.PlacementSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// By default monitoring is not enabled
	require.Nil(t, cluster.Spec.Monitoring)

	// If metrics was enabled previosly, enable it in prometheus spec
	// and remove the enableMetrics config
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		EnableMetrics: boolPtr(true),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Monitoring.Prometheus.ExportMetrics)
	require.Nil(t, cluster.Spec.Monitoring.EnableMetrics)

	// If prometheus is enabled but metrics is explicitly disabled,
	// then do no enable it and remove it from config
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		EnableMetrics: boolPtr(false),
		Prometheus: &corev1.PrometheusSpec{
			Enabled: true,
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.Monitoring.Prometheus.ExportMetrics)
	require.Nil(t, cluster.Spec.Monitoring.EnableMetrics)
}

func TestSetDefaultsOnStorageClusterWithPortworxDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "true",
			},
		},
	}

	// No defaults should be set
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec)

	// Use default component versions if components are enabled
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)
}

func TestStorageClusterDefaultsForLighthouse(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable lighthouse if nothing specified in the user interface spec
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)

	// Don't use default Lighthouse image if disabled
	// Also reset lockImage flag if Lighthouse is disabled
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled:   false,
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.False(t, cluster.Spec.UserInterface.LockImage)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)

	// Use image from release manifest if no image present
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Use given spec image if specified and reset desired image in status
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
		Image:   "custom/lighthouse-image:1.2.3",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "custom/lighthouse-image:1.2.3", cluster.Spec.UserInterface.Image)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)
	require.False(t, cluster.Spec.UserInterface.LockImage)

	// Reset lockImage flag even when spec image is set as it is deprecated
	cluster.Spec.UserInterface.LockImage = true
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.UserInterface.LockImage)
	require.Equal(t, "custom/lighthouse-image:1.2.3", cluster.Spec.UserInterface.Image)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)

	// Use image from release manifest if spec image is reset
	cluster.Spec.UserInterface.Image = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Use image from release manifest if desired was reset
	cluster.Status.DesiredImages.UserInterface = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)

	// Do not overwrite desired lighthouse image even if
	// some other component has changed
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)

	// Don't change desired image if auto update of components is not set
	cluster.Spec.AutoUpdateComponents = nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)

	// For upgrades from existing cluster, if image is not locked
	// we need to reset it the first time as previously it was
	// overwritten by the operator.
	// Resetting status.version to simulate first run by the operator
	cluster.Status.Version = ""
	cluster.Spec.UserInterface.Image = "portworx/px-lighthouse:existing"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Reset desired image if lighthouse has been disabled
	cluster.Spec.UserInterface.Enabled = false
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)
}

func TestStorageClusterDefaultsForAutopilot(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable autopilot if nothing specified in the autopilot spec
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)

	// Don't use default Autopilot image if disabled
	// Also reset lockImage flag if Autopilot is disabled
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled:   false,
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.False(t, cluster.Spec.Autopilot.LockImage)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)

	// Use image from release manifest if no image present
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Use given spec image if specified and reset desired image in status
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
		Image:   "custom/autopilot-image:1.2.3",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "custom/autopilot-image:1.2.3", cluster.Spec.Autopilot.Image)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)
	require.False(t, cluster.Spec.Autopilot.LockImage)

	// Reset lockImage flag even when spec image is set as it is deprecated
	cluster.Spec.Autopilot.LockImage = true
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.Autopilot.LockImage)
	require.Equal(t, "custom/autopilot-image:1.2.3", cluster.Spec.Autopilot.Image)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)

	// Use image from release manifest if spec image is reset
	cluster.Spec.Autopilot.Image = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Use image from release manifest if desired was reset
	cluster.Status.DesiredImages.Autopilot = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)

	// Do not overwrite desired autopilot image even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)

	// Don't change desired image if auto update of components is not set
	cluster.Spec.AutoUpdateComponents = nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)

	// For upgrades from existing cluster, if image is not locked
	// we need to reset it the first time as previously it was
	// overwritten by the operator.
	// Resetting status.version to simulate first run by the operator
	cluster.Status.Version = ""
	cluster.Spec.Autopilot.Image = "portworx/autopilot:existing"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Reset desired image if autopilot has been disabled
	cluster.Spec.Autopilot.Enabled = false
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)
}

func TestStorageClusterDefaultsForStork(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Stork should be enabled by default
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Stork.Enabled)

	// Don't use default Stork image if disabled
	// Also reset lockImage flag if Stork is disabled
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled:   false,
		LockImage: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.False(t, cluster.Spec.Stork.LockImage)
	require.Empty(t, cluster.Status.DesiredImages.Stork)

	// Use image from release manifest if no image present
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Use given spec image if specified and reset desired image in status
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
		Image:   "custom/stork-image:1.2.3",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "custom/stork-image:1.2.3", cluster.Spec.Stork.Image)
	require.Empty(t, cluster.Status.DesiredImages.Stork)
	require.False(t, cluster.Spec.Stork.LockImage)

	// Reset lockImage flag even when spec image is set as it is deprecated
	cluster.Spec.Stork.LockImage = true
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.Stork.LockImage)
	require.Equal(t, "custom/stork-image:1.2.3", cluster.Spec.Stork.Image)
	require.Empty(t, cluster.Status.DesiredImages.Stork)

	// Use image from release manifest if spec image is reset
	cluster.Spec.Stork.Image = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Use image from release manifest if desired was reset
	cluster.Status.DesiredImages.Stork = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)

	// Do not overwrite desired stork image even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)

	// Don't change desired image if auto update of components is not set
	cluster.Spec.AutoUpdateComponents = nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)

	// For upgrades from existing cluster, if image is not locked
	// we need to reset it the first time as previously it was
	// overwritten by the operator.
	// Resetting status.version to simulate first run by the operator
	cluster.Status.Version = ""
	cluster.Spec.Stork.Image = "openstorage/stork:existing"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Reset desired image if stork has been disabled
	cluster.Spec.Stork.Enabled = false
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Status.DesiredImages.Stork)
}

func TestStorageClusterDefaultsForCSI(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable CSI if not enabled
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Status.DesiredImages.CSIProvisioner)

	// Enable CSI if running in k3s cluster
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.18.4+k3s",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, pxutil.FeatureCSI.IsEnabled(cluster.Spec.FeatureGates))
	require.NotEmpty(t, cluster.Status.DesiredImages.CSIProvisioner)

	// Use images from release manifest if enabled
	cluster.Spec.FeatureGates = map[string]string{
		string(pxutil.FeatureCSI): "true",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)
	require.Equal(t, "quay.io/k8scsi/csi-node-driver-registrar:v1.2.3",
		cluster.Status.DesiredImages.CSINodeDriverRegistrar)
	require.Equal(t, "quay.io/k8scsi/driver-registrar:v1.2.3",
		cluster.Status.DesiredImages.CSIDriverRegistrar)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		cluster.Status.DesiredImages.CSIAttacher)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v1.2.3",
		cluster.Status.DesiredImages.CSIResizer)
	require.Equal(t, "quay.io/k8scsi/csi-snapshotter:v1.2.3",
		cluster.Status.DesiredImages.CSISnapshotter)

	// Use images from release manifest if desired was reset
	cluster.Status.DesiredImages.CSIProvisioner = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Do not overwrite desired images if nothing has changed
	cluster.Status.DesiredImages.CSIProvisioner = "k8scsi/csi-provisioner:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "k8scsi/csi-provisioner:old",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Do not overwrite desired images even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "k8scsi/csi-provisioner:old",
		cluster.Status.DesiredImages.CSIProvisioner)
	require.Equal(t, "portworx/px-lighthouse:2.3.4",
		cluster.Status.DesiredImages.UserInterface)

	// Change desired images if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.CSIProvisioner = "k8scsi/csi-provisioner:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Change desired images if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.CSIProvisioner = "k8scsi/csi-provisioner:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Reset desired images if CSI has been disabled
	cluster.Spec.FeatureGates[string(pxutil.FeatureCSI)] = "false"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Status.DesiredImages.CSIProvisioner)
	require.Empty(t, cluster.Status.DesiredImages.CSIAttacher)
	require.Empty(t, cluster.Status.DesiredImages.CSIDriverRegistrar)
	require.Empty(t, cluster.Status.DesiredImages.CSINodeDriverRegistrar)
	require.Empty(t, cluster.Status.DesiredImages.CSIResizer)
	require.Empty(t, cluster.Status.DesiredImages.CSISnapshotter)
}

func TestStorageClusterDefaultsForPrometheus(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable prometheus if monitoring spec is nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Monitoring)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)

	// Don't enable prometheus if prometheus spec is nil
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)

	// Don't enable prometheus if nothing specified in prometheus spec
	cluster.Spec.Monitoring.Prometheus = &corev1.PrometheusSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)

	// Use images from release manifest if enabled
	cluster.Spec.Monitoring.Prometheus.Enabled = true
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/prometheus/prometheus:v1.2.3",
		cluster.Status.DesiredImages.Prometheus)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)
	require.Equal(t, "quay.io/coreos/prometheus-config-reloader:v1.2.3",
		cluster.Status.DesiredImages.PrometheusConfigReloader)
	require.Equal(t, "quay.io/coreos/configmap-reload:v1.2.3",
		cluster.Status.DesiredImages.PrometheusConfigMapReload)

	// Use images from release manifest if desired was reset
	cluster.Status.DesiredImages.PrometheusOperator = ""
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Do not overwrite desired images if nothing has changed
	cluster.Status.DesiredImages.PrometheusOperator = "coreos/prometheus-operator:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "coreos/prometheus-operator:old",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Do not overwrite desired images even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "coreos/prometheus-operator:old",
		cluster.Status.DesiredImages.PrometheusOperator)
	require.Equal(t, "portworx/px-lighthouse:2.3.4",
		cluster.Status.DesiredImages.UserInterface)

	// Change desired images if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.PrometheusOperator = "coreos/prometheus-operator:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Change desired images if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.PrometheusOperator = "coreos/prometheus-operator:old"
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Reset desired images if prometheus has been disabled
	cluster.Spec.Monitoring.Prometheus.Enabled = false
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.PrometheusOperator)
	require.Empty(t, cluster.Status.DesiredImages.PrometheusConfigReloader)
	require.Empty(t, cluster.Status.DesiredImages.PrometheusConfigMapReload)
}

func TestStorageClusterDefaultsForNodeSpecs(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Node specs should be nil if already nil
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Nodes)

	// Node specs should be empty if already empty
	cluster.Spec.Nodes = make([]corev1.NodeSpec, 0)
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Len(t, cluster.Spec.Nodes, 0)

	// Empty storage spec at node level should copy spec from cluster level
	// - If cluster level config is empty, we should use the default storage config
	cluster.Spec.Nodes = []corev1.NodeSpec{{}}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, &corev1.StorageSpec{UseAll: boolPtr(true)}, cluster.Spec.Nodes[0].Storage)

	// - If cluster level config is not empty, use it as is
	cluster.Spec.Nodes = []corev1.NodeSpec{{}}
	clusterStorageSpec := &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	cluster.Spec.Storage = clusterStorageSpec.DeepCopy()
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, clusterStorageSpec, cluster.Spec.Nodes[0].Storage)

	// Do not set node spec storage fields if not set at the cluster level
	cluster.Spec.Storage = nil
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.SystemMdDevice)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.KvdbDevice)

	// Set node spec storage fields from cluster storage spec, if empty at node level
	// If devices is set, then no need to set UseAll and UseAllWithPartitions as it
	// does not matter.
	clusterDevices := []string{"dev1", "dev2"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(false),
		UseAllWithPartitions: boolPtr(false),
		Devices:              &clusterDevices,
		ForceUseDisks:        boolPtr(true),
		JournalDevice:        stringPtr("journal"),
		SystemMdDevice:       stringPtr("metadata"),
		KvdbDevice:           stringPtr("kvdb"),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, clusterDevices, *cluster.Spec.Nodes[0].Storage.Devices)
	require.Equal(t, "journal", *cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Equal(t, "metadata", *cluster.Spec.Nodes[0].Storage.SystemMdDevice)
	require.Equal(t, "kvdb", *cluster.Spec.Nodes[0].Storage.KvdbDevice)

	// If devices is set and empty, even then no need to set UseAll and UseAllWithPartitions,
	// as devices take precedence over them.
	clusterDevices = make([]string, 0)
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
		Devices:              &clusterDevices,
		ForceUseDisks:        boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, clusterDevices, *cluster.Spec.Nodes[0].Storage.Devices)

	// If cluster devices is nil, then set UseAllWithPartitions at node level
	// if set at cluster level. Do not set UseAll as UseAllWithPartitions takes
	// precedence over it.
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
		ForceUseDisks:        boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAll)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)

	// If cluster devices is nil and UseAllWithPartitions is false, then set UseAll
	// at node level if set at cluster level.
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(false),
		ForceUseDisks:        boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)

	// If cluster devices is nil and UseAllWithPartitions is nil, then set UseAll
	// at node level if set at cluster level.
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:        boolPtr(true),
		ForceUseDisks: boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)

	// Should not overwrite storage spec from cluster level, if present at node level
	nodeDevices := []string{"node-dev1", "node-dev2"}
	cluster.Spec.Nodes[0].Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(false),
		UseAllWithPartitions: boolPtr(false),
		Devices:              &nodeDevices,
		ForceUseDisks:        boolPtr(false),
		JournalDevice:        stringPtr("node-journal"),
		SystemMdDevice:       stringPtr("node-metadata"),
		KvdbDevice:           stringPtr("node-kvdb"),
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.False(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, nodeDevices, *cluster.Spec.Nodes[0].Storage.Devices)
	require.Equal(t, "node-journal", *cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Equal(t, "node-metadata", *cluster.Spec.Nodes[0].Storage.SystemMdDevice)
	require.Equal(t, "node-kvdb", *cluster.Spec.Nodes[0].Storage.KvdbDevice)
}

func assertDefaultSecuritySpec(t *testing.T, cluster *corev1.StorageCluster) {
	require.NotNil(t, cluster.Spec.Security)
	require.Equal(t, true, cluster.Spec.Security.Enabled)
	require.NotNil(t, true, cluster.Spec.Security.Auth.SelfSigned.Issuer)
	require.Equal(t, "operator.portworx.io", *cluster.Spec.Security.Auth.SelfSigned.Issuer)
	require.NotNil(t, true, cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	duration, err := pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, duration)
}

func TestStorageClusterDefaultsForSecurity(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Security spec should be nil, as it's disabled by default
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Security)

	// when security.enabled is false, no security fields should be populated.
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: false,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Nil(t, cluster.Spec.Security.Auth)

	// Check for default values when only enabled=true
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	assertDefaultSecuritySpec(t, cluster)

	// Check for default values when only enabled=true and fields non-nil but empty
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth:    &corev1.AuthSpec{},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	assertDefaultSecuritySpec(t, cluster)

	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth:    &corev1.AuthSpec{},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	assertDefaultSecuritySpec(t, cluster)

	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			SelfSigned: &corev1.SelfSignedSpec{},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	assertDefaultSecuritySpec(t, cluster)

	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			SelfSigned: &corev1.SelfSignedSpec{
				Issuer: stringPtr(""),
			},
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	assertDefaultSecuritySpec(t, cluster)

	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			GuestAccess: guestAccessTypePtr(corev1.GuestAccessType("")),
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			GuestAccess: nil,
		},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	assertDefaultSecuritySpec(t, cluster)

	// issuer, when manually set, is not overwritten.
	cluster.Spec.Security.Auth.SelfSigned.Issuer = stringPtr("myissuer.io")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "myissuer.io", *cluster.Spec.Security.Auth.SelfSigned.Issuer)

	// token lifetime, when manually set, is not overwritten.
	cluster.Spec.Security.Auth.SelfSigned.TokenLifetime = stringPtr("1h")
	driver.SetDefaultsOnStorageCluster(cluster)
	duration, err := pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	require.NoError(t, err)
	require.Equal(t, 1*time.Hour, duration)

	// support for extended token durations
	cluster.Spec.Security.Auth.SelfSigned.TokenLifetime = stringPtr("1y")
	driver.SetDefaultsOnStorageCluster(cluster)
	duration, err = pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	require.NoError(t, err)
	require.Equal(t, time.Hour*24*365, duration)
}

func TestSetDefaultsOnStorageClusterForOpenshift(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsOpenshift: "true",
			},
		},
	}

	expectedPlacement := &corev1.PlacementSpec{
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
							{
								Key:      "node-role.kubernetes.io/infra",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
						},
					},
				},
			},
		},
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(pxutil.DefaultOpenshiftStartPort), *cluster.Spec.StartPort)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestValidationsForEssentials(t *testing.T) {
	component.DeregisterAllComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	cluster := &corev1.StorageCluster{}
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "True")

	// TestCase: Should fail if px-essential secret not present
	err := driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"should be present to deploy a Portworx Essentials cluster")

	// TestCase: Should fail if essentials user id not present
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.EssentialsSecretName,
			Namespace: "kube-system",
		},
		Data: map[string][]byte{},
	}
	k8sClient.Create(context.TODO(), secret)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Essentials Entitlement ID (px-essen-user-id)")

	// TestCase: Should fail if essentials user id is empty
	secret.Data[pxutil.EssentialsUserIDKey] = []byte("")
	k8sClient.Update(context.TODO(), secret)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Essentials Entitlement ID (px-essen-user-id)")

	// TestCase: Should fail if OSB endpoint is not present
	secret.Data[pxutil.EssentialsUserIDKey] = []byte("user-id")
	k8sClient.Update(context.TODO(), secret)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Portworx OSB endpoint (px-osb-endpoint)")

	// TestCase: Should fail if OSB endpoint is empty
	secret.Data[pxutil.EssentialsOSBEndpointKey] = []byte("")
	k8sClient.Update(context.TODO(), secret)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Portworx OSB endpoint (px-osb-endpoint)")

	// TestCase: Should not fail if both user id and osb endpoint present
	secret.Data[pxutil.EssentialsOSBEndpointKey] = []byte("osb-endpoint")
	k8sClient.Update(context.TODO(), secret)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// TestCase: Should not fail if essentials is disabled
	k8sClient.Delete(context.TODO(), secret)
	os.Unsetenv(pxutil.EnvKeyPortworxEssentials)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
}

func TestUpdateClusterStatusFirstTime(t *testing.T) {
	driver := portworx{}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	// Status should be set to initializing if not set
	require.Equal(t, cluster.Name, cluster.Status.ClusterName)
	require.Equal(t, "Initializing", cluster.Status.Phase)
}

func TestUpdateClusterStatusWithPortworxDisabled(t *testing.T) {
	driver := portworx{}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "True",
			},
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, cluster.Name, cluster.Status.ClusterName)
	require.Equal(t, "Initializing", cluster.Status.Phase)

	// If portworx is disabled, change status as online
	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, cluster.Name, cluster.Status.ClusterName)
	require.Equal(t, "Online", cluster.Status.Phase)
}

func TestUpdateClusterStatus(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:   "cluster-id",
			Name: "cluster-name",
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "cluster-name", cluster.Status.ClusterName)
	require.Equal(t, "cluster-id", cluster.Status.ClusterUID)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Init
	expectedClusterResp.Cluster.Status = api.Status_STATUS_INIT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Offline
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OFFLINE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Error
	expectedClusterResp.Cluster.Status = api.Status_STATUS_ERROR
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Decommission
	expectedClusterResp.Cluster.Status = api.Status_STATUS_DECOMMISSION
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Unknown", cluster.Status.Phase)

	// Status Maintenance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_MAINTENANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status NeedsReboot
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NEEDS_REBOOT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status NotInQuorum
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status NotInQuorumNoStorage
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status Ok
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDown
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DOWN
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDegraded
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageRebalance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDriveReplace
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status Invalid
	expectedClusterResp.Cluster.Status = api.Status(9999)
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Unknown", cluster.Status.Phase)
}

func TestUpdateClusterStatusForNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "10.0.1.1",
		MgmtIp:            "10.0.1.2",
		Status:            api.Status_STATUS_NONE,
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
		DataIp:            "10.0.2.1",
		MgmtIp:            "10.0.2.2",
		Status:            api.Status_STATUS_OK,
		Pools: []*api.StoragePool{
			{
				ID:        0,
				TotalSize: 21474836480,
				Used:      10737418240,
			},
			{
				ID:        1,
				TotalSize: 21474836480,
				Used:      2147483648,
			},
		},
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Status None
	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-1", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.1.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.1.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeStateCondition, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)
	require.Equal(t, int64(0), nodeStatus.Status.Storage.TotalSize.Value())
	require.Equal(t, int64(0), nodeStatus.Status.Storage.UsedSize.Value())

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-2", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.2.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.2.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeStateCondition, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1.NodeOnlineStatus, nodeStatus.Status.Conditions[0].Status)
	require.Equal(t, int64(42949672960), nodeStatus.Status.Storage.TotalSize.Value())
	require.Equal(t, int64(12884901888), nodeStatus.Status.Storage.UsedSize.Value())

	// Return only one node in enumerate for future tests
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne},
	}

	// Status Init
	expectedNodeOne.Status = api.Status_STATUS_INIT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Initializing", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeInitStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Offline
	expectedNodeOne.Status = api.Status_STATUS_OFFLINE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Error
	expectedNodeOne.Status = api.Status_STATUS_ERROR
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorum
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeNotInQuorumStatus, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorumNoStorage
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeNotInQuorumStatus, nodeStatus.Status.Conditions[0].Status)

	// Status NeedsReboot
	expectedNodeOne.Status = api.Status_STATUS_NEEDS_REBOOT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Decommission
	expectedNodeOne.Status = api.Status_STATUS_DECOMMISSION
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Decommissioned", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDecommissionedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Maintenance
	expectedNodeOne.Status = api.Status_STATUS_MAINTENANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Maintenance", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeMaintenanceStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Ok
	expectedNodeOne.Status = api.Status_STATUS_OK
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOnlineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDown
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DOWN
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOnlineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDegraded
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDegradedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageRebalance
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDegradedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDriveReplace
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDegradedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Invalid
	expectedNodeOne.Status = api.Status(9999)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Unknown", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeUnknownStatus, nodeStatus.Status.Conditions[0].Status)
}

func TestUpdateClusterStatusForNodeVersions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "test/image:1.2.3.4",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		NodeLabels: map[string]string{
			"PX Version": "5.6.7.8",
		},
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "5.6.7.8", nodeStatus.Spec.Version)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", nodeStatus.Spec.Version)

	// If the PX image does not have a tag then don't update the version
	cluster.Spec.Image = "test/image"

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", nodeStatus.Spec.Version)

	// If the PX image does not have a tag then create without version
	k8sClient.Delete(context.TODO(), nodeStatus)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, nodeStatus.Spec.Version)
}

func TestUpdateClusterStatusWithoutPortworxService(t *testing.T) {
	// Fake client without service
	k8sClient := testutil.FakeK8sClient()

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	// TestCase: No storage nodes and portworx pods exist
	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Empty(t, storageNodes.Items)

	// TestCase: Portworx pods exist, but no storage nodes present
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	k8sClient.Create(context.TODO(), pod1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Contains(t, err.Error(), "not found")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Empty(t, storageNodes.Items)

	// TestCase: Portworx pods and storage nodes both exist
	storageNode1 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	storageNode2 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-2",
			Namespace: cluster.Namespace,
		},
	}
	k8sClient.Create(context.TODO(), storageNode1)
	k8sClient.Create(context.TODO(), storageNode2)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Contains(t, err.Error(), "not found")

	// Delete extra nodes that do not have corresponding pods
	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusServiceWithoutClusterIP(t *testing.T) {
	// Fake client with a service that does not have cluster ip
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	// TestCase: No storage nodes and portworx pods exist
	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to get endpoint")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Empty(t, storageNodes.Items)

	// TestCase: Portworx pod exist and storage node exist
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	k8sClient.Create(context.TODO(), pod)
	k8sClient.Create(context.TODO(), storageNode)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Contains(t, err.Error(), "failed to get endpoint")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusServiceGrpcServerError(t *testing.T) {
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "127.0.0.1",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Maintenance",
		},
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	k8sClient.Create(context.TODO(), pod)
	k8sClient.Create(context.TODO(), storageNode)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error connecting to GRPC server")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If the cluster is initializing then do not return an error on
	// grpc connection timeout
	cluster.Status.Phase = string(corev1.ClusterInit)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusInspectClusterFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	k8sClient.Create(context.TODO(), pod)
	k8sClient.Create(context.TODO(), storageNode)

	// Error from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, fmt.Errorf("InspectCurrent error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "InspectCurrent error")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// Nil response from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, nil).
		Times(1)

	// Reset the storage node status
	storageNode.Status = corev1.NodeStatus{}
	k8sClient.Status().Update(context.TODO(), storageNode)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to inspect cluster")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// Nil cluster object in the response of InspectCurrent API
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: nil,
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	// Reset the storage node status
	storageNode.Status = corev1.NodeStatus{}
	k8sClient.Status().Update(context.TODO(), storageNode)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty ClusterInspect response")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusEnumerateNodesFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	k8sClient.Create(context.TODO(), pod)
	k8sClient.Create(context.TODO(), storageNode)

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// TestCase: Error from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nil, fmt.Errorf("node Enumerate error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "node Enumerate error")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Nil response from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nil, nil).
		Times(1)

	// Reset the storage node status
	storageNode.Status = corev1.NodeStatus{}
	k8sClient.Status().Update(context.TODO(), storageNode)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to enumerate nodes")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Empty list of nodes should create StorageNode objects only for matching pods
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Reset the storage node status
	storageNode.Status = corev1.NodeStatus{}
	k8sClient.Status().Update(context.TODO(), storageNode)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Nil list of nodes should create StorageNode objects only for matching pods
	expectedNodeEnumerateResp.Nodes = nil
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Reset the storage node status
	storageNode.Status = corev1.NodeStatus{}
	k8sClient.Status().Update(context.TODO(), storageNode)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Empty list of nodes should not create any StorageNode objects
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	k8sClient.Delete(context.TODO(), pod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// TestCase: Nil list of nodes should not create any StorageNode objects
	expectedNodeEnumerateResp.Nodes = nil
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)
}

func TestUpdateClusterStatusShouldUpdateStatusIfChanged(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_ERROR,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	expectedNode := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "1.1.1.1",
		Status:            api.Status_STATUS_MAINTENANCE,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNode},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Offline", cluster.Status.Phase)
	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1.NodeMaintenanceStatus, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "1.1.1.1", nodeStatusList.Items[0].Status.Network.DataIP)

	// Update status based on the latest object
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	expectedNode.Status = api.Status_STATUS_OK
	expectedNode.DataIp = "2.2.2.2"
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Online", cluster.Status.Phase)
	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1.NodeOnlineStatus, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "2.2.2.2", nodeStatusList.Items[0].Status.Network.DataIP)
}

func TestUpdateClusterStatusShouldUpdateNodePhaseBasedOnConditions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// TestCases: Portworx node present in SDK output
	// TestCase: Node does not have any condition, node state condition will be
	// used to populate the node phase
	expectedNode := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "1.1.1.1",
		Status:            api.Status_STATUS_MAINTENANCE,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNode},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeMaintenanceStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Node has another condition which is newer than when the node state
	// was last transitioned
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Sleep is needed because fake k8s client stores the timestamp at sec granularity
	time.Sleep(time.Second)
	operatorops.Instance().UpdateStorageNodeCondition(
		&storageNodes.Items[0].Status,
		&corev1.NodeCondition{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeFailedStatus,
		},
	)
	k8sClient.Status().Update(context.TODO(), &storageNodes.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: NodeInit condition happened at the same time as NodeState, then
	// phase should have use NodeState as that is the latest response from SDK
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	commonTime := metav1.Now()
	conditions := make([]corev1.NodeCondition, len(storageNodes.Items[0].Status.Conditions))
	for i, c := range storageNodes.Items[0].Status.Conditions {
		conditions[i] = *c.DeepCopy()
		conditions[i].LastTransitionTime = commonTime
	}
	storageNodes.Items[0].Status.Conditions = conditions
	k8sClient.Status().Update(context.TODO(), &storageNodes.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeMaintenanceStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Node state has transitioned
	expectedNode.Status = api.Status_STATUS_DECOMMISSION
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	time.Sleep(time.Second)
	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeDecommissionedStatus), storageNodes.Items[0].Status.Phase)

	// TestCases: Portworx node not present in SDK output, but matching pod present
	// TestCase: If latest node condition is not Init or Failed (here it is Decommissioned)
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-one",
		},
	}
	k8sClient.Create(context.TODO(), pod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeUnknownStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If latest node condition is Failed then phase should be Failed
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeStateCondition,
			Status: corev1.NodeFailedStatus,
		},
	}
	k8sClient.Update(context.TODO(), &storageNodes.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If latest node condition is NodeInit with succeeded status
	// then phase should be Init
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeSucceededStatus,
		},
	}
	k8sClient.Update(context.TODO(), &storageNodes.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If latest node condition does not have status, phase should be Init
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeSucceededStatus,
		},
	}
	k8sClient.Update(context.TODO(), &storageNodes.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If no condition present, phase should be Init
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{}
	k8sClient.Update(context.TODO(), &storageNodes.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If conditions are nil, phase should be Init
	storageNodes.Items[0].Status.Conditions = nil
	k8sClient.Update(context.TODO(), &storageNodes.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusWithoutSchedulerNodeName(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(0),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:       "node-uid",
				DataIp:   "1.1.1.1",
				MgmtIp:   "2.2.2.2",
				Hostname: "node-hostname",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Fake a node object without matching ip address or hostname to storage node
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
			}),
		),
	)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Fake a node object with matching data ip address
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{
							Type:    v1.NodeInternalIP,
							Address: "1.1.1.1",
						},
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching mgmt ip address
	testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{
							Type:    v1.NodeExternalIP,
							Address: "2.2.2.2",
						},
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname
	testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{
							Type:    v1.NodeHostName,
							Address: "node-hostname",
						},
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname from labels
	testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
					Labels: map[string]string{
						"kubernetes.io/hostname": "node-hostname",
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldDeleteStorageNodeForNonExistingNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Node got removed from storage driver
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldNotDeleteStorageNodeIfPodExists(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	nodeEnumerateRespWithAllNodes := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
				Status:            api.Status_STATUS_OK,
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node got removed from portworx sdk response, but corresponding pod exists
	nodeEnumerateRespWithOneNode := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	nodeOnePod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-one",
		},
	}
	k8sClient.Create(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeUnknownStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Initializing state; should remain in Initializing state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	// No conditions means the node is initializing state
	storageNodeList.Items[0].Status.Conditions = nil
	k8sClient.Update(context.TODO(), &storageNodeList.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Failed state; should remain in Failed state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	// Latest condition is in failed state
	storageNodeList.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeFailedStatus,
		},
	}
	k8sClient.Update(context.TODO(), &storageNodeList.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, but pod labels do not match
	// that of portworx pods
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	nodeOnePod.Labels = nil
	k8sClient.Update(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct owner references
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.Labels = driver.GetSelectorLabels()
	k8sClient.Update(context.TODO(), nodeOnePod)

	driver.UpdateStorageClusterStatus(cluster)

	storageNodeList = &corev1.StorageNodeList{}
	testutil.List(k8sClient, storageNodeList)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)
	nodeOnePod.OwnerReferences[0].UID = types.UID("dummy")
	k8sClient.Update(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct node name
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	k8sClient.Update(context.TODO(), nodeOnePod)

	driver.UpdateStorageClusterStatus(cluster)

	storageNodeList = &corev1.StorageNodeList{}
	testutil.List(k8sClient, storageNodeList)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)
	nodeOnePod.Spec.NodeName = "dummy"
	k8sClient.Update(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// exist in cluster's namespace
	k8sClient.Delete(context.TODO(), nodeOnePod)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	driver.UpdateStorageClusterStatus(cluster)

	storageNodeList = &corev1.StorageNodeList{}
	testutil.List(k8sClient, storageNodeList)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)
	nodeOnePod.Namespace = "dummy"
	k8sClient.Create(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)
}

func TestUpdateClusterStatusShouldDeleteStorageNodeIfSchedulerNodeNameNotPresent(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Scheduler node name missing for storage node
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id: "node-1",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)

	// Deleting already deleted StorageNode should not throw error
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldNotDeleteStorageNodeIfPodExistsAndScheduleNameAbsent(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	nodeEnumerateRespWithAllNodes := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
				Status:            api.Status_STATUS_OK,
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Scheduler node name missing for storage node, but corresponding pod exists
	nodeEnumerateRespWithNoSchedName := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id: "node-1",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	nodeOnePod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-one",
		},
	}
	k8sClient.Create(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeUnknownStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Initializing state; should remain in Initializing state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	// No conditions means the node is initializing state
	storageNodeList.Items[0].Status.Conditions = nil
	k8sClient.Update(context.TODO(), &storageNodeList.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Failed state; should remain in Failed state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	// Latest condition is in failed state
	storageNodeList.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeFailedStatus,
		},
	}
	k8sClient.Update(context.TODO(), &storageNodeList.Items[0])

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, but pod labels do not match
	// that of portworx pods
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	nodeOnePod.Labels = nil
	k8sClient.Update(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct owner references
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.Labels = driver.GetSelectorLabels()
	k8sClient.Update(context.TODO(), nodeOnePod)

	driver.UpdateStorageClusterStatus(cluster)

	storageNodeList = &corev1.StorageNodeList{}
	testutil.List(k8sClient, storageNodeList)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)
	nodeOnePod.OwnerReferences[0].UID = types.UID("dummy")
	k8sClient.Update(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct node name
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	k8sClient.Update(context.TODO(), nodeOnePod)

	driver.UpdateStorageClusterStatus(cluster)

	storageNodeList = &corev1.StorageNodeList{}
	testutil.List(k8sClient, storageNodeList)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)
	nodeOnePod.Spec.NodeName = "dummy"
	k8sClient.Update(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// exist in cluster's namespace
	k8sClient.Delete(context.TODO(), nodeOnePod)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	driver.UpdateStorageClusterStatus(cluster)

	storageNodeList = &corev1.StorageNodeList{}
	testutil.List(k8sClient, storageNodeList)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)
	nodeOnePod.Namespace = "dummy"
	k8sClient.Create(context.TODO(), nodeOnePod)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)
}

func TestDeleteClusterWithoutDeleteStrategy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.15.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	startPort := uint32(10001)

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:     "portworx/image:2.2",
			StartPort: &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled:       true,
					ExportMetrics: true,
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "1",
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	setSecuritySpecDefaults(cluster)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

	// Install all components
	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceAccountList.Items)

	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.NotEmpty(t, clusterRoleList.Items)

	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.NotEmpty(t, crbList.Items)

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.NotEmpty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.NotEmpty(t, rbList.Items)

	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceList.Items)

	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.NotEmpty(t, dsList.Items)

	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.NotEmpty(t, deploymentList.Items)

	prometheusList := &monitoringv1.PrometheusList{}
	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.NotEmpty(t, prometheusList.Items)

	smList := &monitoringv1.ServiceMonitorList{}
	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.NotEmpty(t, smList.Items)

	prList := &monitoringv1.PrometheusRuleList{}
	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.NotEmpty(t, prList.Items)

	csiDriverList := &storagev1beta1.CSIDriverList{}
	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.NotEmpty(t, csiDriverList.Items)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	secretList := &v1.SecretList{}
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 4)

	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// If no delete strategy is provided, condition should be complete
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Reason)

	// Verify that all components have been removed
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Empty(t, serviceAccountList.Items)

	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Empty(t, clusterRoleList.Items)

	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Empty(t, crbList.Items)

	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)

	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)

	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Empty(t, deploymentList.Items)

	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.Empty(t, prometheusList.Items)

	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.Empty(t, smList.Items)

	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.Empty(t, prList.Items)

	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.Empty(t, csiDriverList.Items)

	// Storage classes should not get deleted if there is no
	// delete strategy
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	// Security secret keys should not be deleted,
	// but tokens should be deleted
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 2)
	secret := &v1.Secret{}
	err = testutil.Get(k8sClient, secret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, secret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
}

func TestDeleteClusterWithUninstallStrategy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.15.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	startPort := uint32(10001)

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},

			Image:     "portworx/image:2.2",
			StartPort: &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled:       true,
					ExportMetrics: true,
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "1",
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	setSecuritySpecDefaults(cluster)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

	// Install all components
	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceAccountList.Items)

	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.NotEmpty(t, clusterRoleList.Items)

	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.NotEmpty(t, crbList.Items)

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.NotEmpty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.NotEmpty(t, rbList.Items)

	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceList.Items)

	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.NotEmpty(t, dsList.Items)

	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.NotEmpty(t, deploymentList.Items)

	prometheusList := &monitoringv1.PrometheusList{}
	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.NotEmpty(t, prometheusList.Items)

	smList := &monitoringv1.ServiceMonitorList{}
	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.NotEmpty(t, smList.Items)

	prList := &monitoringv1.PrometheusRuleList{}
	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.NotEmpty(t, prList.Items)

	csiDriverList := &storagev1beta1.CSIDriverList{}
	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.NotEmpty(t, csiDriverList.Items)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	secretList := &v1.SecretList{}
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 4)

	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper service account
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxNodeWiperServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Check wiper cluster role
	expectedCR := testutil.GetExpectedClusterRole(t, "nodeWiperClusterRole.yaml")
	wiperCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, wiperCR, pxNodeWiperClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, wiperCR.Name)
	require.Empty(t, wiperCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, wiperCR.Rules)

	// Check wiper cluster role binding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "nodeWiperClusterRoleBinding.yaml")
	wiperCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, wiperCRB, pxNodeWiperClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, wiperCRB.Name)
	require.Empty(t, wiperCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, wiperCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, wiperCRB.RoleRef)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiper.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)

	// Verify that all components have been removed, except node wiper
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 1)
	require.Equal(t, pxNodeWiperServiceAccountName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleBindingName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)

	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Len(t, dsList.Items, 1)
	require.Equal(t, pxNodeWiperDaemonSetName, dsList.Items[0].Name)

	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Empty(t, deploymentList.Items)

	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.Empty(t, prometheusList.Items)

	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.Empty(t, smList.Items)

	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.Empty(t, prList.Items)

	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.Empty(t, csiDriverList.Items)

	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)

	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Empty(t, secretList.Items)
}

func TestDeleteClusterWithCustomRepoRegistry(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	customRepo := "test-registry:1111/test-repo"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRepo,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/px-node-wiper:"+newCompVersion(),
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)

	// Flat registry should be used for image
	customRepo = "test-registry:111"
	cluster.Spec.CustomImageRegistry = customRepo + "//"
	testutil.Delete(k8sClient, wiperDS)

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/px-node-wiper:"+newCompVersion(),
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithCustomRegistry(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	customRegistry := "test-registry:1111"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/portworx/px-node-wiper:"+newCompVersion(),
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithImagePullPolicy(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullPolicy: v1.PullIfNotPresent,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		wiperDS.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)
}

func TestDeleteClusterWithImagePullSecret(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	imagePullSecret := "registry-secret"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullSecret: &imagePullSecret,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, imagePullSecret,
		wiperDS.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)
}

func TestDeleteClusterWithTolerations(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	tolerations := []v1.Toleration{
		{
			Key:      "must-exist",
			Operator: v1.TolerationOpExists,
			Effect:   v1.TaintEffectNoExecute,
		},
		{
			Key:      "foo",
			Operator: v1.TolerationOpEqual,
			Value:    "bar",
			Effect:   v1.TaintEffectNoSchedule,
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Placement: &corev1.PlacementSpec{
				Tolerations: tolerations,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t,
		tolerations,
		wiperDS.Spec.Template.Spec.Tolerations,
	)
}

func TestDeleteClusterWithNodeAffinity(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
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
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check wiper daemonset
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
	require.Equal(t, cluster.Spec.Placement.NodeAffinity, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
}

func TestDeleteClusterWithCustomNodeWiperImage(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	customRegistry := "test-registry:1111"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyNodeWiperImage,
						Value: "test/node-wiper:v1",
					},
				},
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/test/node-wiper:v1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithUninstallStrategyForPKS(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper service account
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxNodeWiperServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Check wiper cluster role
	expectedCR := testutil.GetExpectedClusterRole(t, "nodeWiperClusterRole.yaml")
	wiperCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, wiperCR, pxNodeWiperClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, wiperCR.Name)
	require.Empty(t, wiperCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, wiperCR.Rules)

	// Check wiper cluster role binding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "nodeWiperClusterRoleBinding.yaml")
	wiperCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, wiperCRB, pxNodeWiperClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, wiperCRB.Name)
	require.Empty(t, wiperCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, wiperCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, wiperCRB.RoleRef)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiperPKS.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithUninstallAndWipeStrategy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.15.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	startPort := uint32(10001)

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},

			Image:     "portworx/image:2.2",
			StartPort: &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled:       true,
					ExportMetrics: true,
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "1",
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	setSecuritySpecDefaults(cluster)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

	// Install all components
	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceAccountList.Items)

	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.NotEmpty(t, clusterRoleList.Items)

	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.NotEmpty(t, crbList.Items)

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.NotEmpty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.NotEmpty(t, rbList.Items)

	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceList.Items)

	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.NotEmpty(t, dsList.Items)

	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.NotEmpty(t, deploymentList.Items)

	prometheusList := &monitoringv1.PrometheusList{}
	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.NotEmpty(t, prometheusList.Items)

	smList := &monitoringv1.ServiceMonitorList{}
	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.NotEmpty(t, smList.Items)

	prList := &monitoringv1.PrometheusRuleList{}
	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.NotEmpty(t, prList.Items)

	csiDriverList := &storagev1beta1.CSIDriverList{}
	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.NotEmpty(t, csiDriverList.Items)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	secretList := &v1.SecretList{}
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 4)

	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper service account
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxNodeWiperServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Check wiper cluster role
	expectedCR := testutil.GetExpectedClusterRole(t, "nodeWiperClusterRole.yaml")
	wiperCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, wiperCR, pxNodeWiperClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, wiperCR.Name)
	require.Empty(t, wiperCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, wiperCR.Rules)

	// Check wiper cluster role binding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "nodeWiperClusterRoleBinding.yaml")
	wiperCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, wiperCRB, pxNodeWiperClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, wiperCRB.Name)
	require.Empty(t, wiperCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, wiperCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, wiperCRB.RoleRef)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiperWithWipe.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)

	// Verify that all components have been removed, except node wiper
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 1)
	require.Equal(t, pxNodeWiperServiceAccountName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleBindingName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)

	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Len(t, dsList.Items, 1)
	require.Equal(t, pxNodeWiperDaemonSetName, dsList.Items[0].Name)

	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Empty(t, deploymentList.Items)

	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.Empty(t, prometheusList.Items)

	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.Empty(t, smList.Items)

	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.Empty(t, prList.Items)

	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.Empty(t, csiDriverList.Items)

	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)

	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Empty(t, secretList.Items)
}

func TestDeleteClusterWithUninstallWhenNodeWiperCreated(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	reregisterComponents()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxNodeWiperDaemonSetName,
			Namespace:       cluster.Namespace,
			UID:             types.UID("wiper-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS)
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallMsg)

	// Node wiper daemon set should be removed
	dsList := &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)

	// TestCase: Wiper daemonset should not be created again if already
	// completed and deleted
	cluster.Status.Phase = "DeleteCompleted"
	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallMsg)

	dsList = &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyWhenNodeWiperCreated(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	reregisterComponents()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxNodeWiperDaemonSetName,
			Namespace:       cluster.Namespace,
			UID:             types.UID("wiper-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS)
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	// Node wiper daemon set should be removed
	dsList := &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)

	// TestCase: Wiper daemonset should not be created again if already
	// completed and deleted
	cluster.Status.Phase = "DeleteCompleted"
	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	dsList = &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveConfigMaps(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	etcdConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      internalEtcdConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	cloudDriveConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cloudDriveConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS, wiperPod, etcdConfigMap, cloudDriveConfigMap)
	driver := portworx{
		k8sClient: k8sClient,
	}

	configMaps := &v1.ConfigMapList{}
	err := testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Len(t, configMaps.Items, 2)

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	// Check config maps are deleted
	configMaps = &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Empty(t, configMaps.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveKvdbData(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd://kvdb1.com:2001",
					"etcd://kvdb2.com:2001",
				},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Test etcd v3 without http/https
	kvdbMem, err := kvdb.New(mem.Name, pxKvdbPrefix, nil, nil, kvdb.LogFatalErrorCB)
	require.NoError(t, err)
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err := kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit http
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:http://kvdb1.com:2001",
		"etcd:http://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit https
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd base version
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdBaseVersion, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e2.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test consul
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.ConsulVersion1, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"consul:http://kvdb1.com:2001",
		"consul:http://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, consul.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)
}

func TestDeleteClusterWithUninstallWipeStrategyFailedRemoveKvdbData(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Fail if no kvdb endpoints given
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return kvdb.New(mem.Name, prefix, machines, opts, kvdb.LogFatalErrorCB)
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if unknown kvdb type given in url
	cluster.Spec.Kvdb.Endpoints = []string{"zookeeper://kvdb.com:2001"}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if unknown kvdb version found
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "zookeeper1", nil
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if error getting kvdb version
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "", fmt.Errorf("kvdb version error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")
	require.Contains(t, condition.Reason, "kvdb version error")

	// Fail if error initializing kvdb
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return nil, fmt.Errorf("kvdb initialize error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")
	require.Contains(t, condition.Reason, "kvdb initialize error")
}

func TestDeleteClusterWithPortworxDisabled(t *testing.T) {
	driver := portworx{}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "1",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Reason)

	// Uninstall delete strategy
	cluster.Spec.DeleteStrategy.Type = corev1.UninstallStorageClusterStrategyType
	cluster.Status = corev1.StorageClusterStatus{}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterOperationCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Reason)

}

func TestUpdateStorageNodeKVDB(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	// Create fake k8s client without any nodes to lookup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	clusterName := "px-cluster"
	clusterNS := "kube-system"
	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: clusterNS,
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: clusterNS,
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: "Initializing",
		},
	}

	cmName := k8s.GetBootstrapConfigMapName(cluster.GetName())

	// TEST 1: Add missing KVDB condition
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()
	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-one",
		SchedulerNodeName: "node-one",
		DataIp:            "10.0.1.1",
		MgmtIp:            "10.0.1.2",
		Status:            api.Status_STATUS_NONE,
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-two",
		SchedulerNodeName: "node-two",
		DataIp:            "10.0.2.1",
		MgmtIp:            "10.0.2.2",
		Status:            api.Status_STATUS_OK,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}

	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: clusterNS,
		},
		Data: map[string]string{
			pxEntriesKey: `[{"IP":"10.0.1.2","ID":"node-one","Index":0,"State":1,"Type":1,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-1.internal.kvdb","DataDirType":"KvdbDevice"},{"IP":"10.0.2.2","ID":"node-two","Index":2,"State":2,"Type":2,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-3.internal.kvdb","DataDirType":"KvdbDevice"}]`,
		},
	}

	driver.k8sClient.Create(context.TODO(), cm)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(3)
	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	// check if both storage nodes exist and have the KVDB condition
	for _, n := range []string{"node-one", "node-two"} {
		found := false
		checkStorageNode := &corev1.StorageNode{}
		err = driver.k8sClient.Get(context.TODO(), client.ObjectKey{
			Name:      n,
			Namespace: clusterNS,
		}, checkStorageNode)
		require.NoError(t, err)

		for _, c := range checkStorageNode.Status.Conditions {
			if c.Type == corev1.NodeKVDBCondition {
				found = true
				break
			}
		}
		require.True(t, found)
	}

	// TEST 2: Remove KVDB condition
	cm.Data[pxEntriesKey] = `[{"IP":"10.0.1.2","ID":"node-three","Index":0,"State":3,"Type":0,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-1.internal.kvdb","DataDirType":"KvdbDevice"},{"IP":"10.0.2.2","ID":"node-four","Index":2,"State":0,"Type":2,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-3.internal.kvdb","DataDirType":"KvdbDevice"}]`
	driver.k8sClient.Update(context.TODO(), cm)
	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	// check if both storage nodes exist and DONT have the KVDB condition
	for _, n := range []string{"node-one", "node-two"} {
		found := false
		checkStorageNode := &corev1.StorageNode{}
		err = driver.k8sClient.Get(context.TODO(), client.ObjectKey{
			Name:      n,
			Namespace: clusterNS,
		}, checkStorageNode)
		require.NoError(t, err)

		for _, c := range checkStorageNode.Status.Conditions {
			if c.Type == corev1.NodeKVDBCondition {
				found = true
				break
			}
		}
		require.False(t, found)
	}

	// TEST 4: Check kvdn node state translations
	kvdbNodeStateTests := []struct {
		state                   int
		nodeType                int
		expectedConditionStatus corev1.NodeConditionStatus
		expectedNodeType        string
	}{
		{
			state:                   0,
			nodeType:                0,
			expectedConditionStatus: corev1.NodeUnknownStatus,
			expectedNodeType:        "",
		},
		{
			state:                   1,
			nodeType:                1,
			expectedConditionStatus: corev1.NodeInitStatus,
			expectedNodeType:        "leader",
		},
		{
			state:                   2,
			nodeType:                1,
			expectedConditionStatus: corev1.NodeOnlineStatus,
			expectedNodeType:        "leader",
		},
		{
			state:                   3,
			nodeType:                2,
			expectedConditionStatus: corev1.NodeOfflineStatus,
			expectedNodeType:        "member",
		},
		{
			state:                   4,
			nodeType:                2,
			expectedConditionStatus: corev1.NodeUnknownStatus,
			expectedNodeType:        "member",
		},
	}

	for _, kvdbNodeStateTest := range kvdbNodeStateTests {
		mockNodeServer.EXPECT().
			EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
			Return(expectedNodeEnumerateResp, nil).
			Times(1)

		cm.Data[pxEntriesKey] = fmt.Sprintf(
			`[{"IP":"10.0.1.2","ID":"node-one","State":%d,"Type":%d,"Version":"v2","peerport":"9018","clientport":"9019"}]`,
			kvdbNodeStateTest.state, kvdbNodeStateTest.nodeType)
		driver.k8sClient.Update(context.TODO(), cm)
		err = driver.UpdateStorageClusterStatus(cluster)
		require.NoError(t, err)

		var (
			found        bool
			status       corev1.NodeConditionStatus
			conditionMsg string
		)
		checkStorageNode := &corev1.StorageNode{}
		err = driver.k8sClient.Get(context.TODO(), client.ObjectKey{
			Name:      "node-one",
			Namespace: clusterNS,
		}, checkStorageNode)
		require.NoError(t, err)
		for _, c := range checkStorageNode.Status.Conditions {
			if c.Type == corev1.NodeKVDBCondition {
				found = true
				status = c.Status
				conditionMsg = c.Message
				break
			}
		}
		require.True(t, found)
		require.Equal(t, kvdbNodeStateTest.expectedConditionStatus, status)
		require.NotEmpty(t, conditionMsg)
		require.True(t, strings.Contains(conditionMsg, kvdbNodeStateTest.expectedNodeType))
	}

	// TEST 5: config map not found
	driver.k8sClient.Delete(context.TODO(), cm)
	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
}

func fakeClientWithWiperPod(namespace string) client.Client {
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	return testutil.FakeK8sClient(wiperDS, wiperPod)
}

func TestIsPodUpdatedWithoutArgs(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pxcluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: For pod without containers, should return false
	pxPod := &v1.Pod{
		Spec: v1.PodSpec{},
	}
	isUpdated := driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: For pod with nil args, should return false
	pxPod.Spec.Containers = []v1.Container{
		{
			Name: pxContainerName,
		},
	}
	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: For pod with empty args, should return false
	pxPod.Spec.Containers = []v1.Container{
		{
			Name: pxContainerName,
			Args: make([]string, 0),
		},
	}
	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)
}

func TestIsPodUpdatedWithMiscArgs(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pxcluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: No misc args present in the cluster
	pxPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: pxContainerName,
					Args: []string{"-c", "pxcluster"},
				},
			},
		},
	}

	isUpdated := driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-foo bar --alpha beta",
	}
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"-foo", "bar",
		"--alpha", "beta",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod, with extra spaces
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "-foo   bar   --alpha   beta"

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod, with different order
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--alpha", "beta",
		"-foo", "bar",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod, with changed values
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "--alpha beta -foo bazz"

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)
}

func TestIsPodUpdatedWithEssentialsArgs(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pxcluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: If essentials is disabled and args don't have essentials arg
	pxPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: pxContainerName,
					Args: []string{"-c", "pxcluster"},
				},
			},
		},
	}

	isUpdated := driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: If essentials is disabled and args have essentials arg
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--oem", "esse",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: If essentials is disabled and args have essentials arg,
	// but present in misc args
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: " --oem    esse  ",
	}
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--oem", "esse",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: If essentials is enabled, but args don't have essentials arg
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "true")
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: If essentials is enabled, and args have essentials arg
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--oem", "esse",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	os.Unsetenv(pxutil.EnvKeyPortworxEssentials)
}

func manifestSetup() {
	manifest.SetInstance(&fakeManifest{})
}

type fakeManifest struct{}

func (m *fakeManifest) Init(_ client.Client, _ record.EventRecorder, _ *version.Version) {}

func (m *fakeManifest) GetVersions(
	_ *corev1.StorageCluster,
	force bool,
) *manifest.Version {
	compVersion := compVersion()
	if force {
		compVersion = newCompVersion()
	}
	return &manifest.Version{
		PortworxVersion: "3.0.0",
		Components: manifest.Release{
			Stork:                     "openstorage/stork:" + compVersion,
			Autopilot:                 "portworx/autopilot:" + compVersion,
			Lighthouse:                "portworx/px-lighthouse:" + compVersion,
			NodeWiper:                 "portworx/px-node-wiper:" + compVersion,
			CSIProvisioner:            "quay.io/k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:               "quay.io/k8scsi/csi-attacher:v1.2.3",
			CSIDriverRegistrar:        "quay.io/k8scsi/driver-registrar:v1.2.3",
			CSINodeDriverRegistrar:    "quay.io/k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:            "quay.io/k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                "quay.io/k8scsi/csi-resizer:v1.2.3",
			Prometheus:                "quay.io/prometheus/prometheus:v1.2.3",
			PrometheusOperator:        "quay.io/coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:  "quay.io/coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload: "quay.io/coreos/configmap-reload:v1.2.3",
		},
	}
}

func compVersion() string {
	return "2.3.4"
}

func newCompVersion() string {
	return "4.3.2"
}

func TestMain(m *testing.M) {
	manifestSetup()
	code := m.Run()
	os.Exit(code)
}
