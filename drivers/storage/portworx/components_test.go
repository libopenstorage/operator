package portworx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	ocpconfig "github.com/openshift/api/config/v1"

	"github.com/golang-jwt/jwt/v4"
	"github.com/golang/mock/gomock"
	osdapi "github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	ocp_secv1 "github.com/openshift/api/security/v1"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	api "k8s.io/kubernetes/pkg/apis/core"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	etcHostsFile       = "/etc/hosts"
	tempEtcHostsMarker = "### px-operator unit-test"
)

func TestOrderOfComponents(t *testing.T) {
	reregisterComponents()
	component.RegisterDisruptionBudgetComponent()
	component.RegisterPortworxCRDComponent()

	components := component.GetAll()

	componentNames := make([]string, len(components))
	for i, comp := range components {
		componentNames[i] = comp.Name()
	}
	require.Len(t, components, 20)
	// Higher priority components come first
	require.ElementsMatch(t,
		[]string{
			component.PSPComponentName,
			component.AuthComponentName,
			component.TLSComponentName,
			component.SCCComponentName,
		},
		componentNames[:4],
	)
	require.ElementsMatch(t,
		[]string{
			component.PxRepoComponentName,
			component.AutopilotComponentName,
			component.CSIComponentName,
			component.DisruptionBudgetComponentName,
			component.LighthouseComponentName,
			component.MonitoringComponentName,
			component.PortworxAPIComponentName,
			component.PortworxBasicComponentName,
			component.PortworxCRDComponentName,
			component.PortworxProxyComponentName,
			component.PortworxStorageClassComponentName,
			component.TelemetryComponentName,
			component.PrometheusComponentName,
			component.PVCControllerComponentName,
			component.AlertManagerComponentName,
			component.PluginComponentName,
		},
		componentNames[4:],
	)

	require.Equal(t, int32(0), components[0].Priority())
	require.Equal(t, int32(0), components[1].Priority())
	require.Equal(t, int32(0), components[2].Priority())
	require.Equal(t, int32(0), components[3].Priority())
	for _, comp := range components[4:] {
		require.Equal(t, component.DefaultComponentPriority, comp.Priority())
	}
}

// TestBasicComponentsInstallWithPreTLSPx validates that for px versions before 2.9,
// the TLS secured port 9023 is not exposed
func TestBasicComponentsInstallWithPreTLSPx(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			StartPort:  &startPort,
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
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	legacyImage := "portworx/oci-monitor:2.8.0"
	cluster.Spec.Image = legacyImage

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx Service with legacy px (pre 2.9) should not expose 9023 TSL secured port
	// s, _ := json.MarshalIndent(cluster.Spec, "", "\t")
	// t.Logf("cluster spec under validation for legacy portworxService.yaml: %+v", string(s))
	expectedPXService := testutil.GetExpectedService(t, "portworxService_pre_29.yaml")
	pxService := &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPXService.Name, pxService.Name)
	require.Equal(t, expectedPXService.Namespace, pxService.Namespace)
	require.Len(t, pxService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxService.OwnerReferences[0].Name)
	require.Equal(t, expectedPXService.Labels, pxService.Labels)
	require.Equal(t, expectedPXService.Spec, pxService.Spec)

	// Portworx API Service with legacy px (pre 2.9) should not expose 9023 TSL secured port
	expectedPxAPIService := testutil.GetExpectedService(t, "portworxAPIService_pre_29.yaml")
	pxAPIService := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Name, pxAPIService.Name)
	require.Equal(t, expectedPxAPIService.Namespace, pxAPIService.Namespace)
	require.Len(t, pxAPIService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxAPIService.OwnerReferences[0].Name)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)

	// Portworx Proxy Service
	expectedPxProxyService := testutil.GetExpectedService(t, "pxProxyService_pre_29.yaml")
	pxProxyService := &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, expectedPxProxyService.Name, pxProxyService.Name)
	require.Equal(t, expectedPxProxyService.Namespace, pxProxyService.Namespace)
	require.Empty(t, pxProxyService.OwnerReferences)
	require.Equal(t, expectedPxProxyService.Labels, pxProxyService.Labels)
	require.Equal(t, expectedPxProxyService.Spec, pxProxyService.Spec)
}

func TestBasicComponentsInstall(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			StartPort: &startPort,
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
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx ServiceAccount
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxutil.DefaultPortworxServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Portworx ClusterRole
	expectedCR := testutil.GetExpectedClusterRole(t, "portworxClusterRole.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.PxClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Portworx ClusterRoleBinding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "portworxClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.PxClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Portworx Role
	expectedRole := testutil.GetExpectedRole(t, "portworxRole.yaml")
	actualRole := &rbacv1.Role{}
	err = testutil.Get(k8sClient, actualRole, component.PxRoleName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedRole.Name, actualRole.Name)
	require.Equal(t, expectedRole.Namespace, actualRole.Namespace)
	require.Len(t, actualRole.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualRole.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedRole.Rules, actualRole.Rules)

	// Portworx RoleBinding
	expectedRB := testutil.GetExpectedRoleBinding(t, "portworxRoleBinding.yaml")
	actualRB := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, actualRB, component.PxRoleBindingName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedRB.Name, actualRB.Name)
	require.Equal(t, expectedRB.Namespace, actualRB.Namespace)
	require.Len(t, actualRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedRB.Subjects, actualRB.Subjects)
	require.Equal(t, expectedRB.RoleRef, actualRB.RoleRef)

	s, _ := json.MarshalIndent(cluster.Spec, "", "\t")
	t.Logf("cluster spec under validation for portworxService.yaml: %+v", string(s))

	// Portworx Service
	expectedPXService := testutil.GetExpectedService(t, "portworxService.yaml")
	pxService := &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPXService.Name, pxService.Name)
	require.Equal(t, expectedPXService.Namespace, pxService.Namespace)
	require.Len(t, pxService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxService.OwnerReferences[0].Name)
	require.Equal(t, expectedPXService.Labels, pxService.Labels)
	require.Equal(t, expectedPXService.Spec, pxService.Spec)

	// Portworx KVDB Service
	expectedPXKVDBService := testutil.GetExpectedService(t, "portworxKVDBService.yaml")
	pxKVDBService := &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPXKVDBService.Name, pxKVDBService.Name)
	require.Equal(t, expectedPXKVDBService.Namespace, pxKVDBService.Namespace)
	require.Len(t, pxKVDBService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxKVDBService.OwnerReferences[0].Name)
	require.Equal(t, expectedPXKVDBService.Labels, pxKVDBService.Labels)
	require.Equal(t, expectedPXKVDBService.Spec, pxKVDBService.Spec)

	// Portworx API Service
	expectedPxAPIService := testutil.GetExpectedService(t, "portworxAPIService.yaml")
	pxAPIService := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Name, pxAPIService.Name)
	require.Equal(t, expectedPxAPIService.Namespace, pxAPIService.Namespace)
	require.Len(t, pxAPIService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxAPIService.OwnerReferences[0].Name)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)

	// Portworx API DaemonSet
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "portworxAPIDaemonSet.yaml")
	ds := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDaemonSet.Name, ds.Name)
	require.Equal(t, expectedDaemonSet.Namespace, ds.Namespace)
	require.Len(t, ds.OwnerReferences, 1)
	require.Equal(t, cluster.Name, ds.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, ds.Spec)

	// Portworx Proxy ServiceAccount
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Empty(t, sa.OwnerReferences)

	// Portworx Proxy ClusterRoleBinding
	expectedCRB = testutil.GetExpectedClusterRoleBinding(t, "pxProxyClusterRoleBinding.yaml")
	actualCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.PxProxyClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Portworx Proxy Service
	expectedPxProxyService := testutil.GetExpectedService(t, "pxProxyService.yaml")
	pxProxyService := &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, expectedPxProxyService.Name, pxProxyService.Name)
	require.Equal(t, expectedPxProxyService.Namespace, pxProxyService.Namespace)
	require.Empty(t, pxProxyService.OwnerReferences)
	require.Equal(t, expectedPxProxyService.Labels, pxProxyService.Labels)
	require.Equal(t, expectedPxProxyService.Spec, pxProxyService.Spec)

	// Portworx Proxy DaemonSet
	expectedDaemonSet = testutil.GetExpectedDaemonSet(t, "pxProxyDaemonSet.yaml")
	ds = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, expectedDaemonSet.Name, ds.Name)
	require.Equal(t, expectedDaemonSet.Namespace, ds.Namespace)
	require.Empty(t, ds.OwnerReferences)
	require.Equal(t, expectedDaemonSet.Spec, ds.Spec)

	// Portworx CSI enabled by default
	statefulset := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulset, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, 2, len(statefulset.Spec.Template.Spec.Containers))
}

func TestPxRepoInstallUninstall(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			PxRepo: &corev1.PxRepoSpec{
				Enabled: false,
				Image:   "testImage",
			},
		},
	}

	// Disabled first
	cluster.Spec.PxRepo.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(driver.k8sClient, service, component.PxRepoServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment := &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Enable the service
	cluster.Spec.PxRepo.Enabled = true
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(driver.k8sClient, service, component.PxRepoServiceName, cluster.Namespace)
	require.NoError(t, err)

	err = testutil.Get(driver.k8sClient, deployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Change the deployment
	cluster.Spec.ImagePullPolicy = v1.PullNever
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(driver.k8sClient, deployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Disable the service
	cluster.Spec.PxRepo.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(driver.k8sClient, service, component.PxRepoServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(driver.k8sClient, deployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestBasicInstallWithPortworxDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx RBAC objects should not be created
	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Empty(t, serviceAccountList.Items)

	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Empty(t, clusterRoleList.Items)

	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Empty(t, crbList.Items)

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)

	// Portworx Services should not be created
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	// Portworx API DaemonSet should not be created
	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)
}

func TestBasicInstallWithInternalKVDBDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: false,
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx KVDB Services should not be created when internal KVDB is disabled
	pxKVDBService := &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestPortworxWithCustomSecretsNamespace(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	secretsNamespace := "secrets-namespace"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxSecretsNamespace,
						Value: secretsNamespace,
					},
				},
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx secrets namespace
	ns := &v1.Namespace{}
	err = testutil.Get(k8sClient, ns, secretsNamespace, "")
	require.NoError(t, err)
	require.Empty(t, ns.OwnerReferences, 0)

	// Portworx Secrets Role
	expectedRole := testutil.GetExpectedRole(t, "portworxRole.yaml")
	actualRole := &rbacv1.Role{}
	err = testutil.Get(k8sClient, actualRole, component.PxRoleName, secretsNamespace)
	require.NoError(t, err)
	require.Equal(t, expectedRole.Name, actualRole.Name)
	require.Empty(t, actualRole.OwnerReferences)
	require.ElementsMatch(t, expectedRole.Rules, actualRole.Rules)

	// Portworx Secrets RoleBinding
	expectedRB := testutil.GetExpectedRoleBinding(t, "portworxRoleBinding.yaml")
	actualRB := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, actualRB, component.PxRoleBindingName, secretsNamespace)
	require.NoError(t, err)
	require.Equal(t, expectedRB.Name, actualRB.Name)
	require.Empty(t, actualRB.OwnerReferences)
	require.ElementsMatch(t, expectedRB.Subjects, actualRB.Subjects)
	require.Equal(t, expectedRB.RoleRef, actualRB.RoleRef)

	// Disable storage should result in deleting objects even from secrets namespace
	cluster.Annotations = map[string]string{
		constants.AnnotationDisableStorage: "true",
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)
}

func TestPortworxAPIDaemonSetAlwaysDeploys(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	ds := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)

	// Case: Delete the daemon set and ensure it gets recreated
	err = k8sClient.Delete(context.TODO(), ds)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	ds = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)

	// Case: Change the image of daemon set, it should be recreated
	// it is already deployed
	ds.Spec.Template.Spec.Containers[0].Image = "new/image"
	err = k8sClient.Update(context.TODO(), ds)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	ds = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "registry.k8s.io/pause:3.1", ds.Spec.Template.Spec.Containers[0].Image)

	cluster.Status.DesiredImages.Pause = "private.repo.org/foo/bar:1.2.3"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "private.repo.org/foo/bar:1.2.3", ds.Spec.Template.Spec.Containers[0].Image)

	// Case: If the daemon set was marked as deleted, the it should be recreated,
	// even if it is already present
	pxAPIComponent, _ := component.Get(component.PortworxAPIComponentName)
	pxAPIComponent.MarkDeleted()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	ds = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.NotEqual(t, "new/image", ds.Spec.Template.Spec.Containers[0].Image)
}

func TestPortworxProxyIsNotDeployedWhenClusterInKubeSystem(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			StartPort: &startPort,
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxProxySA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxProxySA, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	pxProxyCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxProxyCRB, component.PxProxyClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pxProxyDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDS, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	// Portworx service should be deployed as part of basic deployment
	pxSvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxSvc, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
}

func TestPortworxProxyIsNotDeployedWhenUsingDefaultPort(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxProxySA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxProxySA, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	pxProxyCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxProxyCRB, component.PxProxyClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pxProxySvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxProxySvc, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	pxProxyDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDS, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))
}

func TestPortworxProxyIsNotDeployedWhenDisabledExplicitly(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPortworxProxy: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			StartPort: &startPort,
		},
	}

	// TestCase: Portworx proxy disabled explicitly
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxProxySA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxProxySA, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	pxProxyCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxProxyCRB, component.PxProxyClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pxProxySvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxProxySvc, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	pxProxyDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDS, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	// TestCase: Portworx proxy annotation has true value
	cluster.Annotations[pxutil.AnnotationPortworxProxy] = "true"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxProxySA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxProxySA, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.NoError(t, err)

	pxProxyCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxProxyCRB, component.PxProxyClusterRoleBindingName, "")
	require.NoError(t, err)

	pxProxySvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxySvc, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)

	pxProxyDS = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDS, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)

	// TestCase: Portworx proxy annotation has invalid boolean value
	cluster.Annotations[pxutil.AnnotationPortworxProxy] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxProxySA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxProxySA, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.NoError(t, err)

	pxProxyCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxProxyCRB, component.PxProxyClusterRoleBindingName, "")
	require.NoError(t, err)

	pxProxySvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxySvc, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)

	pxProxyDS = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDS, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
}

func TestPortworxWithCustomServiceAccount(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxServiceAccount,
						Value: "custom-px-sa",
					},
				},
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Default portworx ServiceAccount should not be created
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxutil.DefaultPortworxServiceAccountName, "")
	require.True(t, errors.IsNotFound(err))

	// Portworx ClusterRoleBinding with custom ServiceAccount
	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PxClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, "custom-px-sa", crb.Subjects[0].Name)

	// Portworx RoleBinding with custom ServiceAccount
	rb := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, rb, component.PxRoleBindingName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "custom-px-sa", rb.Subjects[0].Name)

	// Portworx API DaemonSet with custom ServiceAccount
	ds := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "custom-px-sa", ds.Spec.Template.Spec.ServiceAccountName)

	// Case: Change the custom service account
	cluster.Spec.Env[0].Value = "new-custom-px-sa"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxutil.DefaultPortworxServiceAccountName, "")
	require.True(t, errors.IsNotFound(err))

	// Portworx ClusterRoleBinding with custom ServiceAccount
	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PxClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, "new-custom-px-sa", crb.Subjects[0].Name)

	// Portworx RoleBinding with custom ServiceAccount
	rb = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, rb, component.PxRoleBindingName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "new-custom-px-sa", rb.Subjects[0].Name)

	// Portworx API DaemonSet with custom ServiceAccount
	ds = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "new-custom-px-sa", ds.Spec.Template.Spec.ServiceAccountName)

	// Case: Custom service account should not be deleted
	customSA := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-custom-px-sa",
			Namespace: cluster.Namespace,
		},
	}
	err = k8sClient.Create(context.TODO(), customSA)
	require.NoError(t, err)
	cluster.Annotations = map[string]string{
		constants.AnnotationDisableStorage: "true",
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, "new-custom-px-sa", cluster.Namespace)
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PxClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestDisablePortworx(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			StartPort: &startPort,
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxutil.DefaultPortworxServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PxClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PxClusterRoleBindingName, "")
	require.NoError(t, err)

	role := &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, component.PxRoleName, cluster.Namespace)
	require.NoError(t, err)

	rb := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, rb, component.PxRoleBindingName, cluster.Namespace)
	require.NoError(t, err)

	pxSvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxSvc, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)

	pxAPISvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPISvc, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)

	pxAPIDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDS, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)

	pxProxySA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxProxySA, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.NoError(t, err)

	pxProxyCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxProxyCRB, component.PxProxyClusterRoleBindingName, "")
	require.NoError(t, err)

	pxProxySvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxProxySvc, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)

	pxProxyDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDS, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)

	// Disable Portworx
	cluster.Annotations = map[string]string{
		constants.AnnotationDisableStorage: "true",
	}
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxutil.DefaultPortworxServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PxClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PxClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	role = &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, component.PxRoleName, "")
	require.True(t, errors.IsNotFound(err))

	rb = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, rb, component.PxRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pxSvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxSvc, pxutil.PortworxServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxAPISvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPISvc, component.PxAPIServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxAPIDS = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDS, component.PxAPIDaemonSetName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxProxySA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxProxySA, component.PxProxyServiceAccountName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))

	pxProxyCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxProxyCRB, component.PxProxyClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pxProxySvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxySvc, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.False(t, errors.IsNotFound(err))

	pxProxyDS = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDS, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.True(t, errors.IsNotFound(err))
}

func TestDefaultStorageClassesWithStork(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.22.0",
	}
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
			Stork: &corev1.StorkSpec{
				Enabled: true,
			},
			Image: "portworx/image:2.10.1",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Len(t, storageClassList.Items, 16)

	expectedSC := testutil.GetExpectedStorageClass(t, "storageClassDb.yaml")
	actualSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbEncryptedStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassReplicated.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxReplicatedStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassReplicatedEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxReplicatedEncryptedStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbLocalSnapshot.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbLocalSnapshotStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbLocalSnapshotStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbLocalSnapshotEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbLocalSnapshotEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbLocalSnapshotEncryptedStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbCloudSnapshot.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbCloudSnapshotStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbCloudSnapshotStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbCloudSnapshotEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbCloudSnapshotEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbCloudSnapshotEncryptedStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIDb.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIDbEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbEncryptedCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbEncryptedCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIReplicated.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxReplicatedCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIReplicatedEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedEncryptedCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxReplicatedEncryptedCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIDbLocalSnapshot.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbLocalSnapshotCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbLocalSnapshotCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIDbLocalSnapshotEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbLocalSnapshotEncryptedCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbLocalSnapshotEncryptedCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIDbCloudSnapshot.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbCloudSnapshotCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbCloudSnapshotCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassCSIDbCloudSnapshotEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbCloudSnapshotEncryptedCSIStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbCloudSnapshotEncryptedCSIStorageClass)
	require.Empty(t, actualSC.OwnerReferences)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)
}

func TestDefaultStorageClassesWithoutStork(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
		},
	}

	// Stork is disabled
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Len(t, storageClassList.Items, 4)

	actualSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbStorageClass, "")
	require.NoError(t, err)
	err = testutil.Get(k8sClient, actualSC, component.PxDbEncryptedStorageClass, "")
	require.NoError(t, err)
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedStorageClass, "")
	require.NoError(t, err)
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedEncryptedStorageClass, "")
	require.NoError(t, err)

	// Stork config is empty
	cluster.Spec.Stork = nil

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList = &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Len(t, storageClassList.Items, 4)

	err = testutil.Get(k8sClient, actualSC, component.PxDbStorageClass, "")
	require.NoError(t, err)
	err = testutil.Get(k8sClient, actualSC, component.PxDbEncryptedStorageClass, "")
	require.NoError(t, err)
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedStorageClass, "")
	require.NoError(t, err)
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedEncryptedStorageClass, "")
	require.NoError(t, err)
}

func TestDefaultStorageClassesWithPortworxDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)

	// Invalid annotation value should result in creating storage classes
	cluster.Annotations[constants.AnnotationDisableStorage] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList = &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)
}

func TestDefaultStorageClassesWhenDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationDisableStorageClass: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)

	// Invalid annotation value should result in creating storage classes
	cluster.Annotations[pxutil.AnnotationDisableStorageClass] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList = &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	// Remove storage classes even if already deployed
	cluster.Annotations[pxutil.AnnotationDisableStorageClass] = "1"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList = &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)
}

func TestPortworxServiceTypeWithOverride(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsAKS:       "true",
				pxutil.AnnotationIsGKE:       "true",
				pxutil.AnnotationIsOKE:       "true",
				pxutil.AnnotationIsEKS:       "true",
				pxutil.AnnotationServiceType: "ClusterIP",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxService := &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxService.Spec.Type)

	pxAPIService := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxAPIService.Spec.Type)

	// Load balancer service type
	cluster.Annotations[pxutil.AnnotationServiceType] = "LoadBalancer"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxService.Spec.Type)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxService.Spec.Type)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxAPIService.Spec.Type)

	// Node port service type
	cluster.Annotations[pxutil.AnnotationServiceType] = "NodePort"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxService.Spec.Type)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxService.Spec.Type)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxAPIService.Spec.Type)

	// Invalid service type should use default service type
	cluster.Annotations[pxutil.AnnotationServiceType] = "Invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxService.Spec.Type)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxService.Spec.Type)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxAPIService.Spec.Type)
}

func TestPVCControllerInstall(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallWithK8s1_24(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.24.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	expectedDeployment := testutil.GetExpectedDeployment(t, "pvcControllerDeployment.yaml")
	expectedContainer := expectedDeployment.Spec.Template.Spec.Containers[0]
	expectedContainer.Command = expectedContainer.Command[0:6]
	expectedContainer.Image = "registry.k8s.io/kube-controller-manager-amd64:v1.24.0"
	expectedContainer.LivenessProbe.HTTPGet.Port = intstr.FromInt(10257)
	expectedContainer.LivenessProbe.HTTPGet.Scheme = v1.URISchemeHTTPS
	expectedDeployment.Spec.Template.Spec.Containers[0] = expectedContainer
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeploymentObject(t, cluster, k8sClient, expectedDeployment)
}

func TestPVCControllerInstallWithK8s1_22(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.22.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	// TestCase: Simple install on k8s 1.22
	expectedDeployment := testutil.GetExpectedDeployment(t, "pvcControllerDeployment.yaml")
	expectedContainer := expectedDeployment.Spec.Template.Spec.Containers[0]
	expectedContainer.Command = expectedContainer.Command[0:6]
	expectedContainer.Image = "registry.k8s.io/kube-controller-manager-amd64:v1.22.0"
	expectedContainer.LivenessProbe.HTTPGet.Port = intstr.FromInt(10257)
	expectedContainer.LivenessProbe.HTTPGet.Scheme = v1.URISchemeHTTPS
	expectedDeployment.Spec.Template.Spec.Containers[0] = expectedContainer
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeploymentObject(t, cluster, k8sClient, expectedDeployment)

	// TestCase: Add both port and secure port annotations
	cluster.Annotations[pxutil.AnnotationPVCControllerPort] = "12345"
	cluster.Annotations[pxutil.AnnotationPVCControllerSecurePort] = "12346"
	expectedContainer.Command = append(expectedContainer.Command, "--secure-port=12346")
	expectedContainer.LivenessProbe.HTTPGet.Port = intstr.FromInt(12346)
	expectedDeployment.Spec.Template.Spec.Containers[0] = expectedContainer
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeploymentObject(t, cluster, k8sClient, expectedDeployment)

	// TestCase: Simple install on AKS
	cluster.Annotations = map[string]string{
		pxutil.AnnotationIsAKS: "true",
	}
	expectedContainer = expectedDeployment.Spec.Template.Spec.Containers[0]
	expectedContainer.Command[6] = "--secure-port=10261"
	expectedContainer.LivenessProbe.HTTPGet.Port = intstr.FromInt(10261)
	expectedDeployment.Spec.Template.Spec.Containers[0] = expectedContainer
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeploymentObject(t, cluster, k8sClient, expectedDeployment)
}

func TestPVCControllerWithInvalidValue(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "invalid",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestPVCControllerInstallInKubeSystemNamespace(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsOpenshift: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// PVC controller should not get installed if running in kube-system
	// namespace in openshift
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestPVCControllerInstallInNonKubeSystemNamespace(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Despite invalid pvc controller annotation, install for non kube-system namespace
	cluster.Annotations = map[string]string{
		pxutil.AnnotationPVCController: "invalid",
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
}

func TestPVCControllerInstallForPKS(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for PKS
	cluster.Annotations[pxutil.AnnotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallForEKS(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsEKS: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for EKS
	cluster.Annotations[pxutil.AnnotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallForGKE(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsGKE: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for GKE
	cluster.Annotations[pxutil.AnnotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallForOKE(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsOKE: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for OKE
	cluster.Annotations[pxutil.AnnotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallForAKS(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsAKS: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)

	specFileName := "pvcControllerDeployment.yaml"
	pvcControllerDeployment := testutil.GetExpectedDeployment(t, specFileName)
	command := pvcControllerDeployment.Spec.Template.Spec.Containers[0].Command
	command = append(command, "--port="+component.AksPVCControllerInsecurePort)
	command = append(command, "--secure-port="+component.AksPVCControllerSecurePort)
	pvcControllerDeployment.Spec.Template.Spec.Containers[0].Command = command
	pvcControllerDeployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Port = intstr.Parse(component.AksPVCControllerInsecurePort)

	verifyPVCControllerDeploymentObject(t, cluster, k8sClient, pvcControllerDeployment)

	// Despite invalid pvc controller annotation, install for AKS
	cluster.Annotations[pxutil.AnnotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeploymentObject(t, cluster, k8sClient, pvcControllerDeployment)
}

func TestPVCControllerWhenPVCControllerDisabledExplicitly(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
				pxutil.AnnotationIsPKS:         "true",
				pxutil.AnnotationIsEKS:         "true",
				pxutil.AnnotationIsGKE:         "true",
				pxutil.AnnotationIsOKE:         "true",
				pxutil.AnnotationIsAKS:         "true",
				pxutil.AnnotationIsOpenshift:   "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestPVCControllerInstallWithPortworxDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS:             "true",
				pxutil.AnnotationIsEKS:             "true",
				pxutil.AnnotationIsGKE:             "true",
				pxutil.AnnotationIsOKE:             "true",
				pxutil.AnnotationIsAKS:             "true",
				pxutil.AnnotationIsOpenshift:       "true",
				constants.AnnotationDisableStorage: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func verifyPVCControllerInstall(
	t *testing.T,
	cluster *corev1.StorageCluster,
	k8sClient client.Client,
) {
	// PVC Controller ServiceAccount
	serviceAccountList := &v1.ServiceAccountList{}
	err := testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 2)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.PVCServiceAccountName, sa.Name)
	require.Equal(t, cluster.Namespace, sa.Namespace)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// PVC Controller ClusterRole
	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 2)

	expectedCR := testutil.GetExpectedClusterRole(t, "pvcControllerClusterRole.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.PVCClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// PVC Controller ClusterRoleBinding
	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 2)

	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "pvcControllerClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.PVCClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)
}

func verifyPVCControllerDeployment(
	t *testing.T,
	cluster *corev1.StorageCluster,
	k8sClient client.Client,
	specFileName string,
) {
	verifyPVCControllerDeploymentObject(
		t, cluster,
		k8sClient,
		testutil.GetExpectedDeployment(t, specFileName))

}

func verifyPVCControllerDeploymentObject(
	t *testing.T,
	cluster *corev1.StorageCluster,
	k8sClient client.Client,
	expectedDeployment *appsv1.Deployment,
) {
	// PVC Controller Deployment
	deploymentList := &appsv1.DeploymentList{}
	err := testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Len(t, deploymentList.Items, 1)

	actualDeployment := deploymentList.Items[0]
	require.Equal(t, expectedDeployment.Name, actualDeployment.Name)
	require.Equal(t, expectedDeployment.Namespace, actualDeployment.Namespace)
	require.Len(t, actualDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualDeployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Labels, actualDeployment.Labels)
	require.Equal(t, expectedDeployment.Annotations, actualDeployment.Annotations)
	require.Equal(t, expectedDeployment.Spec, actualDeployment.Spec)
}

func TestPVCControllerCustomCPU(t *testing.T) {
	// Set fake kubernetes client for k8s version
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Default PVC controller CPU
	expectedCPUQuantity := resource.MustParse("200m")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Change the CPU resource required for PVC controller deployment
	expectedCPU := "300m"
	cluster.Annotations[pxutil.AnnotationPVCControllerCPU] = expectedCPU

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestPVCControllerInvalidCPU(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController:    "true",
				pxutil.AnnotationPVCControllerCPU: "invalid-cpu",
			},
		},
	}

	// Should not return error but raise an event instead
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup %v.", v1.EventTypeWarning,
			util.FailedComponentReason, component.PVCControllerComponentName))
}

func TestPVCControllerCustomPorts(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Default PVC controller ports
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.NotContains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--port")
	require.NotContains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--secure-port")
	require.Equal(t, "10252",
		deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Port.String())

	// Change the insecure port
	expectedPort := "10000"
	cluster.Annotations[pxutil.AnnotationPVCControllerPort] = expectedPort

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Contains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--port="+expectedPort)
	require.NotContains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--secure-port")
	require.Equal(t, expectedPort,
		deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Port.String())

	// Change the secure port
	expectedSecurePort := "10001"
	cluster.Annotations[pxutil.AnnotationPVCControllerSecurePort] = expectedSecurePort

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Contains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--secure-port="+expectedSecurePort)
	require.Contains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--port="+expectedPort)
	require.Equal(t, expectedPort,
		deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Port.String())

	// Reset the ports to default
	cluster.Annotations[pxutil.AnnotationPVCControllerPort] = ""
	cluster.Annotations[pxutil.AnnotationPVCControllerSecurePort] = ""

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.NotContains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--secure-port")
	require.NotContains(t, deployment.Spec.Template.Spec.Containers[0].Command, "--port")
	require.Equal(t, "10252",
		deployment.Spec.Template.Spec.Containers[0].LivenessProbe.ProbeHandler.HTTPGet.Port.String())
}

func TestPVCControllerRollbackImageChanges(t *testing.T) {
	// Set fake kubernetes client for k8s version
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v0.0.0",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Change the image of pvc controller deployment
	deployment.Spec.Template.Spec.Containers[0].Image = "foo/bar:v1"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v0.0.0",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestPVCControllerImageWithNewerK8sVersion(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.7",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"registry.k8s.io/kube-controller-manager-amd64:v1.18.7",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Older patches in Kubernetes 1.18 release train
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.6",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.18.6",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Newer patches in Kubernetes 1.17 release train
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.17.10",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"registry.k8s.io/kube-controller-manager-amd64:v1.17.10",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Older patches in Kubernetes 1.17 release train
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.17.9",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.17.9",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Newer patches in Kubernetes 1.16 release train
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.16.14",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"registry.k8s.io/kube-controller-manager-amd64:v1.16.14",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Older patches in Kubernetes 1.16 release train
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.16.13",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.16.13",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestPVCControllerRollbackCommandChanges(t *testing.T) {
	// Set fake kubernetes client for k8s version
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	expectedCommand := deployment.Spec.Template.Spec.Containers[0].Command

	// Change the command of pvc controller deployment
	deployment.Spec.Template.Spec.Containers[0].Command = append(
		deployment.Spec.Template.Spec.Containers[0].Command,
		"--new-arg=test",
	)
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, expectedCommand, deployment.Spec.Template.Spec.Containers[0].Command)
}

func TestLighthouseInstall(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "docker.io/portworx/px-lighthouse:2.1.1",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	// Lighthouse ServiceAccount
	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 3)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.LhServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.LhServiceAccountName, sa.Name)
	require.Equal(t, cluster.Namespace, sa.Namespace)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Lighthouse ClusterRole
	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 3)

	expectedCR := testutil.GetExpectedClusterRole(t, "lighthouseClusterRole.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.LhClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Lighthouse ClusterRoleBinding
	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 3)

	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "lighthouseClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.LhClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Lighthouse Service
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Len(t, serviceList.Items, 3)

	expectedLhService := testutil.GetExpectedService(t, "lighthouseService.yaml")
	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedLhService.Name, lhService.Name)
	require.Equal(t, expectedLhService.Namespace, lhService.Namespace)
	require.Len(t, lhService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, lhService.OwnerReferences[0].Name)
	require.Equal(t, expectedLhService.Labels, lhService.Labels)
	require.Equal(t, expectedLhService.Spec, lhService.Spec)

	// Lighthouse Deployment
	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Len(t, deploymentList.Items, 2)

	expectedDeployment := testutil.GetExpectedDeployment(t, "lighthouseDeployment.yaml")
	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, lhDeployment.Name)
	require.Equal(t, expectedDeployment.Namespace, lhDeployment.Namespace)
	require.Len(t, lhDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, lhDeployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Labels, lhDeployment.Labels)
	require.Equal(t, expectedDeployment.Annotations, lhDeployment.Annotations)
	require.Equal(t, expectedDeployment.Spec, lhDeployment.Spec)
}

func TestLighthouseServiceTypeForAKS(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsAKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.LhServiceName, lhService.Name)
	require.Equal(t, cluster.Namespace, lhService.Namespace)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseServiceTypeForGKE(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsGKE: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.LhServiceName, lhService.Name)
	require.Equal(t, cluster.Namespace, lhService.Namespace)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseServiceTypeForEKS(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsEKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.LhServiceName, lhService.Name)
	require.Equal(t, cluster.Namespace, lhService.Namespace)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseServiceTypeWithOverride(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsAKS:       "true",
				pxutil.AnnotationIsGKE:       "true",
				pxutil.AnnotationIsOKE:       "true",
				pxutil.AnnotationIsEKS:       "true",
				pxutil.AnnotationServiceType: "ClusterIP",
			},
		},
		Spec: corev1.StorageClusterSpec{
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, lhService.Spec.Type)

	// Load balancer service type
	cluster.Annotations[pxutil.AnnotationServiceType] = "LoadBalancer"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)

	// Node port service type
	cluster.Annotations[pxutil.AnnotationServiceType] = "NodePort"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, lhService.Spec.Type)

	// Invalid type should use the default service type
	cluster.Annotations[pxutil.AnnotationServiceType] = "Invalid"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseWithoutImage(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
		},
	}

	// Should not return an error, instead should raise an event
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Lighthouse. lighthouse image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)

	cluster.Spec.UserInterface.Image = ""
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Lighthouse. lighthouse image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)
}

func TestLighthouseWithDesiredImage(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				UserInterface: "docker.io/portworx/px-lighthouse:status",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "docker.io/portworx/px-lighthouse:status", image)

	// If image is present in spec, then use that instead of the desired image
	cluster.Spec.UserInterface.Image = "portworx/px-lighthouse:spec"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "docker.io/portworx/px-lighthouse:spec", image)
}

func TestLighthouseImageChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "docker.io/portworx/px-lighthouse:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "docker.io/portworx/px-lighthouse:v1", image)

	// Change the lighthouse image
	cluster.Spec.UserInterface.Image = "portworx/px-lighthouse:v2"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "docker.io/portworx/px-lighthouse:v2", image)
}

func TestLighthouseConfigInitImageChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "docker.io/portworx/px-lighthouse:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName)
	require.Equal(t, "docker.io/portworx/lh-config-sync:v1", image)

	// Change the lighthouse config init container image
	cluster.Spec.UserInterface.Env = []v1.EnvVar{
		{
			Name:  component.EnvKeyLhConfigSyncImage,
			Value: "test/config-sync:v2",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName)
	require.Equal(t, "docker.io/test/config-sync:v2", image)
}

func TestLighthouseStorkConnectorImageChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "docker.io/portworx/px-lighthouse:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName)
	require.Equal(t, "docker.io/portworx/lh-stork-connector:v1", image)

	// Change the lighthouse config sync container image
	cluster.Spec.UserInterface.Env = []v1.EnvVar{
		{
			Name:  component.EnvKeyLhStorkConnectorImage,
			Value: "test/stork-connector:v2",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName)
	require.Equal(t, "docker.io/test/stork-connector:v2", image)
}

func TestLighthouseWithoutImageTag(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:latest",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Lighthouse image should remain unchanged but sidecar container images
	// should use the default image tag
	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "docker.io/portworx/px-lighthouse:latest", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName)
	require.Equal(t, "docker.io/portworx/lh-config-sync:latest", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName)
	require.Equal(t, "docker.io/portworx/lh-config-sync:latest", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName)
	require.Equal(t, "docker.io/portworx/lh-stork-connector:latest", image)
}

func TestLighthouseSidecarsOverrideWithEnv(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:latest",
				Env: []v1.EnvVar{
					{
						Name:  component.EnvKeyLhConfigSyncImage,
						Value: "test/config-sync:t1",
					},
					{
						Name:  component.EnvKeyLhStorkConnectorImage,
						Value: "test/stork-connector:t2",
					},
				},
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Lighthouse image should remain unchanged but sidecar container images
	// should use the default image tag
	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "docker.io/portworx/px-lighthouse:latest", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName)
	require.Equal(t, "docker.io/test/config-sync:t1", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName)
	require.Equal(t, "docker.io/test/config-sync:t1", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName)
	require.Equal(t, "docker.io/test/stork-connector:t2", image)
}

func TestAutopilotInstall(t *testing.T) {
	// Start test with newer version (1.25 beyond) of Kubernetes first, on which PodSecurityPolicy is no longer existing
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.25.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	stcSpec := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:1.1.1",
				Providers: []corev1.DataProviderSpec{
					{
						Name: "default",
						Type: "prometheus",
						Params: map[string]string{
							"url": "http://prometheus:9090",
						},
					},
					{
						Name: "second",
						Type: "datadog",
						Params: map[string]string{
							"url":  "http://datadog:9090",
							"auth": "foobar",
						},
					},
				},
				GitOps: &corev1.GitOpsSpec{
					Name: "test",
					Type: "bitbucket-scm",
					Params: map[string]string{
						"defaultReviewers": "user1, user2",
						"user":             "oksana",
						"repo":             "autopilot-bb",
						"folder":           "workloads",
						"baseUrl":          "http://10.13.108.10:7990",
						"projectKey":       "PXAUT",
						"branch":           "master",
					},
				},
				Args: map[string]string{
					"min_poll_interval": "4",
					"log-level":         "debug",
				},
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					SelfSigned: &corev1.SelfSignedSpec{
						Issuer:        stringPtr(defaultSelfSignedIssuer),
						TokenLifetime: stringPtr(defaultTokenLifetime),
						SharedSecret:  stringPtr(pxutil.SecurityPXSharedSecretSecretName),
					},
				},
			},
		},
	}

	cluster := stcSpec.DeepCopy()

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Autopilot ConfigMap
	expectedConfigMap := testutil.GetExpectedConfigMap(t, "autopilotConfigMap.yaml")
	autopilotConfigMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, autopilotConfigMap, component.AutopilotConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, autopilotConfigMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, autopilotConfigMap.Namespace)
	require.Len(t, autopilotConfigMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, autopilotConfigMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, autopilotConfigMap.Data)

	// Autopilot ServiceAccount
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.AutopilotServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.AutopilotServiceAccountName, sa.Name)
	require.Equal(t, cluster.Namespace, sa.Namespace)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Autopilot ClusterRole
	expectedCR := testutil.GetExpectedClusterRole(t, "autopilotClusterRole.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.AutopilotClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Autopilot ClusterRoleBinding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "autopilotClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.AutopilotClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Autopilot Deployment
	expectedDeployment := testutil.GetExpectedDeployment(t, "autopilotDeployment.yaml")
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	autopilotDeployment.ResourceVersion = ""
	require.Equal(t, expectedDeployment, autopilotDeployment)
	// Till here we finished the verification of all aspects of Autopilot

	// Now test with older version (1.24 less) of Kubernetes, on which PodSecurityPolicy is used and
	// injected into Autopilot's ClusterRole definition
	versionClient = fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.24.0",
	}
	fakeExtClient = fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient = testutil.FakeK8sClient()
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster = stcSpec.DeepCopy()

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Load Autopilot ClusterRole with PSP rules and compare
	expectedCR = testutil.GetExpectedClusterRole(t, "autopilotClusterRole_k8s_1.24.yaml")
	actualCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.AutopilotClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)
}

func TestAutopilotInstallIncorrectSpec(t *testing.T) {
	// testing the case, where the params field can not be empty
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:1.1.1",
				Providers: []corev1.DataProviderSpec{
					{
						Name: "default",
						Type: "prometheus",
						Params: map[string]string{
							"url": "http://prometheus:9090",
						},
					},
					{
						Name: "second",
						Type: "datadog",
						Params: map[string]string{
							"url":  "http://datadog:9090",
							"auth": "foobar",
						},
					},
				},
				GitOps: &corev1.GitOpsSpec{
					Name: "test",
					Type: "bitbucket-scm",
				},
				Args: map[string]string{
					"min_poll_interval": "4",
					"log-level":         "debug",
				},
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					SelfSigned: &corev1.SelfSignedSpec{
						Issuer:        stringPtr(defaultSelfSignedIssuer),
						TokenLifetime: stringPtr(defaultTokenLifetime),
						SharedSecret:  stringPtr(pxutil.SecurityPXSharedSecretSecretName),
					},
				},
			},
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	recorder := record.NewFakeRecorder(10)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Autopilot. gitops params cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)
}

func TestAutopilotWithoutImage(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
		},
	}

	// Should not return an error, instead should raise an event
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Autopilot. autopilot image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)

	cluster.Spec.Autopilot.Image = ""
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Autopilot. autopilot image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)
}

func TestAutopilotWithEnvironmentVariables(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(0)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
				Env: []v1.EnvVar{
					{
						Name:  "FOO",
						Value: "foo",
					},
					{
						Name:  "BAR",
						Value: "bar",
					},
				},
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Env, 3)
	// Env vars are sorted on the key
	require.Equal(t, "BAR", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[0].Name)
	require.Equal(t, "bar", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[0].Value)
	require.Equal(t, "FOO", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[1].Name)
	require.Equal(t, "foo", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[1].Value)
	require.Equal(t, pxutil.EnvKeyPortworxNamespace,
		autopilotDeployment.Spec.Template.Spec.Containers[0].Env[2].Name)
	require.Equal(t, cluster.Namespace,
		autopilotDeployment.Spec.Template.Spec.Containers[0].Env[2].Value)
}

func TestAutopilotWithTLSEnabled(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	// setup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(false),
				},
				TLS: &corev1.TLSSpec{
					Enabled: boolPtr(true),
					RootCA: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/ca.crt"),
					},
					ServerCert: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/server.crt"),
					},
					ServerKey: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/server.key"),
					},
				},
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
		},
	}

	// test
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)

	// validate
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0) // no warnings
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Env, 4)
	expectedEnv := []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyCASecretName,
			Value: pxutil.DefaultCASecretName,
		},
		{
			Name:  pxutil.EnvKeyCASecretKey,
			Value: pxutil.DefaultCASecretKey,
		},
		{
			Name:  pxutil.EnvKeyPortworxEnableTLS,
			Value: "true",
		},
		{
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
	require.ElementsMatch(t, expectedEnv, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// TestCase: user supplies name of the secret to use for the CA cert. Validate that it is not overwritten by defaults
	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(false),
				},
				TLS: &corev1.TLSSpec{
					Enabled: boolPtr(true),
					RootCA: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/ca.crt"),
					},
					ServerCert: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/server.crt"),
					},
					ServerKey: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/server.key"),
					},
				},
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
				Env: []v1.EnvVar{
					{
						Name:  "PX_CA_CERT_SECRET",
						Value: "non_default_ca_secret",
					},
					{
						Name:  "PX_CA_CERT_SECRET_KEY",
						Value: "non_default_secret_key",
					},
				},
			},
		},
	}
	// test
	err = driver.PreInstall(cluster)

	// validate
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0) // no warnings
	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Env, 4)

	expectedEnv = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyCASecretName,
			Value: "non_default_ca_secret",
		},
		{
			Name:  pxutil.EnvKeyCASecretKey,
			Value: "non_default_secret_key",
		},
		{
			Name:  pxutil.EnvKeyPortworxEnableTLS,
			Value: "true",
		},
		{
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
	require.ElementsMatch(t, expectedEnv, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// TestCase: user supplies CA in k8s secret ... k8s secret name and key are supplied to autopilot
	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(false),
				},
				TLS: &corev1.TLSSpec{
					Enabled: boolPtr(true),
					RootCA: &corev1.CertLocation{
						SecretRef: &corev1.SecretRef{
							SecretName: "mycasecret",
							SecretKey:  "mysecretkey",
						},
					},
					ServerCert: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/server.crt"),
					},
					ServerKey: &corev1.CertLocation{
						FileName: stringPtr("/etc/pwx/server.key"),
					},
				},
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
		},
	}
	// test
	err = driver.PreInstall(cluster)

	// validate
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0) // no warnings
	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Env, 4)

	expectedEnv = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyCASecretName,
			Value: "mycasecret",
		},
		{
			Name:  pxutil.EnvKeyCASecretKey,
			Value: "mysecretkey",
		},
		{
			Name:  pxutil.EnvKeyPortworxEnableTLS,
			Value: "true",
		},
		{
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
	require.ElementsMatch(t, expectedEnv, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// TestCase: automatic TLS setup, when no CRT/KEY/CA files provided
	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(false),
				},
				TLS: &corev1.TLSSpec{
					Enabled: boolPtr(true),
				},
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Image:   "portworx/oci-monitor:" + pxutil.MinimumPxVersionAutoTLS.String(),
			Version: pxutil.MinimumPxVersionAutoTLS.String(),
		},
	}

	// test
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)

	// validate
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0) // no warnings
	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Env, 2)

	expectedEnv = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxEnableTLS,
			Value: "true",
		},
		{
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
	require.ElementsMatch(t, expectedEnv, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// TestCase: remove PX_ENABLE_TLS=true when TLS disabled
	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(false),
				},
				TLS: &corev1.TLSSpec{
					Enabled: boolPtr(false),
				},
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxEnableTLS,
						Value: "true",
					},
				},
			},
			Image:   "portworx/oci-monitor:" + pxutil.MinimumPxVersionAutoTLS.String(),
			Version: pxutil.MinimumPxVersionAutoTLS.String(),
		},
	}

	// test
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.Empty(t, cluster.Spec.Autopilot.Env)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)

	// validate
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0) // no warnings
	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Env, 1)

	expectedEnv = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
	require.ElementsMatch(t, expectedEnv, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// TestCase: remove PX_ENABLE_TLS=true when AutoPilot disabled.
	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(false),
				},
				TLS: &corev1.TLSSpec{
					Enabled: boolPtr(true),
				},
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
				Image:   "portworx/autopilot:test",
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxEnableTLS,
						Value: "true",
					},
				},
			},
			Image:   "portworx/oci-monitor:" + pxutil.MinimumPxVersionAutoTLS.String(),
			Version: pxutil.MinimumPxVersionAutoTLS.String(),
		},
	}

	// test
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Env)
	err = driver.PreInstall(cluster)

	// validate
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0) // no warnings

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.Error(t, err)
}

func TestAutopilotWithDesiredImage(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				Autopilot: "portworx/autopilot:status",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(autopilotDeployment, component.AutopilotContainerName)
	require.Equal(t, "docker.io/portworx/autopilot:status", image)

	// If image is present in spec, then use that instead of the desired image
	cluster.Spec.Autopilot.Image = "portworx/autopilot:spec"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(autopilotDeployment, component.AutopilotContainerName)
	require.Equal(t, "docker.io/portworx/autopilot:spec", image)
}

func TestAutopilotImageChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(autopilotDeployment, component.AutopilotContainerName)
	require.Equal(t, "docker.io/portworx/autopilot:v1", image)

	// Change the autopilot image
	cluster.Spec.Autopilot.Image = "portworx/autopilot:v2"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(autopilotDeployment, component.AutopilotContainerName)
	require.Equal(t, "docker.io/portworx/autopilot:v2", image)
}

func TestAutopilotArgumentsChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Check custom new arg is present
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Command, 4)
	require.Contains(t,
		autopilotDeployment.Spec.Template.Spec.Containers[0].Command,
		"--test-key=test-value",
	)
	require.Contains(t,
		autopilotDeployment.Spec.Template.Spec.Containers[0].Command,
		"--log-level=debug",
	)

	// Overwrite existing argument with new value
	cluster.Spec.Autopilot.Args["--log-level"] = "warn"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Command, 4)
	require.Contains(t,
		autopilotDeployment.Spec.Template.Spec.Containers[0].Command,
		"--log-level=warn",
	)
}

func TestAutopilotConfigArgumentsChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Use default config args if nothing specified in the cluster spec
	autopilotConfig := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, autopilotConfig, component.AutopilotConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.NotContains(t, autopilotConfig.Data["config.yaml"], "min_poll_interval")

	// Overwrite the config arguments
	// Check nothing has changed in deployment arguments and that the config map
	// has changed to reflect the new argument
	cluster.Spec.Autopilot.Args = map[string]string{"min_poll_interval": "10"}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotConfig = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, autopilotConfig, component.AutopilotConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Contains(t, autopilotConfig.Data["config.yaml"], "min_poll_interval: 10")

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Command, 3)
	require.NotContains(t,
		autopilotDeployment.Spec.Template.Spec.Containers[0].Command,
		"--min_poll_interval=10",
	)
}

func TestAutopilotEnvVarsChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
				Env: []v1.EnvVar{
					{
						Name:  "FOO",
						Value: "foo",
					},
				},
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Check env vars are passed to deployment
	defaultEnvVars := []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
	expectedEnvs := append(defaultEnvVars, cluster.Spec.Autopilot.Env...)
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, expectedEnvs, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// Overwrite existing env vars
	cluster.Spec.Autopilot.Env[0].Value = "bar"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedEnvs[1].Value = "bar"
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, expectedEnvs, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// Add new env vars
	newEnv := v1.EnvVar{Name: "BAZ", Value: "baz"}
	cluster.Spec.Autopilot.Env = append(cluster.Spec.Autopilot.Env, newEnv)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedEnvs = append(expectedEnvs, newEnv)
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, expectedEnvs, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)
}

func TestAutopilotResources(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Default Autopilot CPU
	expectedCPUQuantity := resource.MustParse("0.1")
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(
		autopilotDeployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Set custom resources.
	cluster.Spec.Autopilot.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("4Gi"),
			v1.ResourceCPU:    resource.MustParse("400m"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("8Gi"),
			v1.ResourceCPU:    resource.MustParse("800m"),
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	assert.Equal(t, *cluster.Spec.Autopilot.Resources, autopilotDeployment.Spec.Template.Spec.Containers[0].Resources)

	// Change custom resources.
	cluster.Spec.Autopilot.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("2Gi"),
			v1.ResourceCPU:    resource.MustParse("200m"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("6Gi"),
			v1.ResourceCPU:    resource.MustParse("600m"),
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	assert.Equal(t, *cluster.Spec.Autopilot.Resources, autopilotDeployment.Spec.Template.Spec.Containers[0].Resources)
}

func TestAutopilotCPUChange(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Default Autopilot CPU
	expectedCPUQuantity := resource.MustParse("0.1")
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(
		autopilotDeployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Change the CPU resource required for Autopilot deployment
	expectedCPU := "0.2"
	cluster.Annotations = map[string]string{pxutil.AnnotationAutopilotCPU: expectedCPU}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(
		autopilotDeployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestAutopilotInvalidCPU(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationAutopilotCPU: "invalid-cpu",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Autopilot.", v1.EventTypeWarning, util.FailedComponentReason))
}

func TestAutopilotSecurityContext(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.False(t, *autopilotDeployment.Spec.Template.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation)
	require.False(t, *autopilotDeployment.Spec.Template.Spec.Containers[0].SecurityContext.Privileged)
}

func TestAutopilotVolumesChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	volumeSpecs := []corev1.VolumeSpec{
		{
			Name:      "testvol",
			MountPath: "/var/testvol",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/host/test",
				},
			},
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
				Volumes: volumeSpecs,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
	require.NoError(t, err)

	// Case: Volumes should be applied to the deployment
	volumes, volumeMounts := expectedVolumesAndMounts(volumeSpecs)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	// Checking after first volume as autopilot deployment already has 1 volume
	require.ElementsMatch(t, volumes, autopilotDeployment.Spec.Template.Spec.Volumes[1:])
	require.ElementsMatch(t, volumeMounts,
		autopilotDeployment.Spec.Template.Spec.Containers[0].VolumeMounts[1:])

	// Case: Updated volumes should be applied to the deployment
	propagation := v1.MountPropagationBidirectional
	pathType := v1.HostPathDirectory
	volumeSpecs[0].MountPropagation = &propagation
	volumeSpecs[0].HostPath.Type = &pathType
	cluster.Spec.Autopilot.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, autopilotDeployment.Spec.Template.Spec.Volumes[1:])
	require.ElementsMatch(t, volumeMounts,
		autopilotDeployment.Spec.Template.Spec.Containers[0].VolumeMounts[1:])

	// Case: New volumes should be applied to the deployment
	volumeSpecs = append(volumeSpecs, corev1.VolumeSpec{
		Name:      "testvol2",
		MountPath: "/var/testvol2",
		VolumeSource: v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName: "volume-secret",
			},
		},
	})
	cluster.Spec.Autopilot.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, autopilotDeployment.Spec.Template.Spec.Volumes[1:])
	require.ElementsMatch(t, volumeMounts,
		autopilotDeployment.Spec.Template.Spec.Containers[0].VolumeMounts[1:])

	// Case: Removed volumes should be removed from the deployment
	volumeSpecs = []corev1.VolumeSpec{volumeSpecs[0]}
	cluster.Spec.Autopilot.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, autopilotDeployment.Spec.Template.Spec.Volumes[1:])
	require.ElementsMatch(t, volumeMounts,
		autopilotDeployment.Spec.Template.Spec.Containers[0].VolumeMounts[1:])

	// Case: If volumes are empty, should be removed from the deployment
	cluster.Spec.Autopilot.Volumes = []corev1.VolumeSpec{}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, autopilotDeployment.Spec.Template.Spec.Volumes[1:])
	require.Empty(t, autopilotDeployment.Spec.Template.Spec.Containers[0].VolumeMounts[1:])

	// Case: Volumes should be added back if not present in deployment
	cluster.Spec.Autopilot.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, autopilotDeployment.Spec.Template.Spec.Volumes[1:])
	require.ElementsMatch(t, volumeMounts,
		autopilotDeployment.Spec.Template.Spec.Containers[0].VolumeMounts[1:])

	// Case: If volumes is nil, deployment should not have volumes
	cluster.Spec.Autopilot.Volumes = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, autopilotDeployment.Spec.Template.Spec.Volumes[1:])
	require.Empty(t, autopilotDeployment.Spec.Template.Spec.Containers[0].VolumeMounts[1:])
}

func TestSecurityInstall(t *testing.T) {
	// auth enabled through security.enabled
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
					SelfSigned: &corev1.SelfSignedSpec{
						// Since we have a token expiration buffer of one minute,
						// a new token will constantly be fetched.
						TokenLifetime: stringPtr("1s"),
					},
				},
			},
		},
	}
	validateAuthSecurityInstall(t, cluster)

	// auth enabled explicitly
	cluster.Spec.Security.Auth.Enabled = boolPtr(true)
	validateSecurityTokenRefreshOnUpdate(t, cluster)
}

func validateAuthSecurityInstall(t *testing.T, cluster *corev1.StorageCluster) {
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	driver := portworx{}
	recorder := record.NewFakeRecorder(100)
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	// Initial run
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Default configuration should be loaded with autogenerated secret keys. ConfigMap
	sharedSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, sharedSecret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, sharedSecret.OwnerReferences)
	require.Len(t, sharedSecret.Annotations, 1)
	require.Equal(t, "true", sharedSecret.Annotations[component.AnnotationSkipResource])
	require.Equal(t, 64, len(sharedSecret.Data[pxutil.SecuritySharedSecretKey]))
	oldSharedSecret := sharedSecret.Data[pxutil.SecuritySharedSecretKey]

	systemSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, systemSecret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, systemSecret.OwnerReferences)
	require.Len(t, systemSecret.Annotations, 1)
	require.Equal(t, "true", systemSecret.Annotations[component.AnnotationSkipResource])
	require.Equal(t, 64, len(systemSecret.Data[pxutil.SecuritySystemSecretKey]))
	oldSystemSecret := systemSecret.Data[pxutil.SecuritySystemSecretKey]
	require.Equal(t, 64, len(systemSecret.Data[pxutil.SecurityAppsSecretKey]))
	oldAppsSecret := systemSecret.Data[pxutil.SecurityAppsSecretKey]

	require.NotEqual(t, systemSecret.Data[pxutil.SecuritySystemSecretKey], sharedSecret.Data[pxutil.SecuritySharedSecretKey])
	require.NotEqual(t, sharedSecret.Data[pxutil.SecuritySharedSecretKey], systemSecret.Data[pxutil.SecurityAppsSecretKey])
	require.NotEqual(t, systemSecret.Data[pxutil.SecurityAppsSecretKey], systemSecret.Data[pxutil.SecuritySystemSecretKey])

	// No changes should happen to the auto-generated secrets
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, systemSecret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, sharedSecret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, oldSystemSecret, systemSecret.Data[pxutil.SecuritySystemSecretKey])
	require.Equal(t, oldSharedSecret, sharedSecret.Data[pxutil.SecuritySharedSecretKey])
	require.Equal(t, oldAppsSecret, systemSecret.Data[pxutil.SecurityAppsSecretKey])

	// Token secrets should be created and be valid jwt tokens
	jwtClaims := jwt.MapClaims{}
	adminSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, adminSecret, pxutil.SecurityPXAdminTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, adminSecret.OwnerReferences, 1)
	require.Equal(t, cluster.Name, adminSecret.OwnerReferences[0].Name)
	require.Len(t, adminSecret.Annotations, 1)
	require.Equal(t, "true", adminSecret.Annotations[component.AnnotationSkipResource])
	_, _, err = new(jwt.Parser).ParseUnverified(string(adminSecret.Data[pxutil.SecurityAuthTokenKey]), &jwtClaims)
	require.NoError(t, err)
	groups := jwtClaims["groups"].([]interface{})
	require.Equal(t, "*", groups[0])
	roles := jwtClaims["roles"].([]interface{})
	require.Equal(t, "system.admin", roles[0])
	require.Equal(t, "px-admin-token", jwtClaims["name"])
	require.Equal(t, "px-admin-token@operator.portworx.io", jwtClaims["sub"])
	require.Equal(t, "px-admin-token@operator.portworx.io", jwtClaims["email"])
	require.Equal(t, "operator.portworx.io", jwtClaims["iss"])
	validateTokenLifetime(t, cluster, jwtClaims)

	userSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, userSecret.OwnerReferences, 1)
	require.Equal(t, cluster.Name, userSecret.OwnerReferences[0].Name)
	require.Len(t, userSecret.Annotations, 1)
	require.Equal(t, "true", userSecret.Annotations[component.AnnotationSkipResource])
	_, _, err = new(jwt.Parser).ParseUnverified(string(userSecret.Data[pxutil.SecurityAuthTokenKey]), &jwtClaims)
	require.NoError(t, err)
	groups = jwtClaims["groups"].([]interface{})
	require.Equal(t, 0, len(groups))
	roles = jwtClaims["roles"].([]interface{})
	require.Equal(t, "system.user", roles[0])
	require.Equal(t, "px-user-token", jwtClaims["name"])
	require.Equal(t, "px-user-token@operator.portworx.io", jwtClaims["sub"])
	require.Equal(t, "px-user-token@operator.portworx.io", jwtClaims["email"])
	require.Equal(t, "operator.portworx.io", jwtClaims["iss"])
	validateTokenLifetime(t, cluster, jwtClaims)

	// Token secrets should be refreshed
	oldUserToken := userSecret.Data[pxutil.SecurityAuthTokenKey]
	time.Sleep(2 * time.Second)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	newUserSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, newUserSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	_, _, err = new(jwt.Parser).ParseUnverified(string(newUserSecret.Data[pxutil.SecurityAuthTokenKey]), &jwtClaims)
	require.NoError(t, err)
	newUserToken := newUserSecret.Data[pxutil.SecurityAuthTokenKey]
	require.NotEqual(t, oldUserToken, newUserToken)

	// Invalid token lifetime should raise an event
	cluster.Spec.Security.Auth.SelfSigned.TokenLifetime = stringPtr("3sabc")
	err = driver.PreInstall(cluster)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup %v.", v1.EventTypeWarning,
			util.FailedComponentReason, component.AuthComponentName))
	require.NoError(t, err)
	cluster.Spec.Security.Auth.SelfSigned.TokenLifetime = stringPtr("1s")

	// User pre-configured values should not be overwritten
	err = testutil.Delete(k8sClient, sharedSecret)
	require.NoError(t, err)
	err = testutil.Delete(k8sClient, systemSecret)
	require.NoError(t, err)
	systemSecretKey := pxutil.EncodeBase64([]byte("systemSecretKey"))
	sharedSecretKey := pxutil.EncodeBase64([]byte("sharedSecretKey"))
	appsSecretKey := pxutil.EncodeBase64([]byte("appsSecretKey"))
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  pxutil.EnvKeyPortworxAuthSystemKey,
		Value: string(systemSecretKey),
	})
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  pxutil.EnvKeyPortworxAuthJwtSharedSecret,
		Value: string(sharedSecretKey),
	})
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  pxutil.EnvKeyPortworxAuthSystemAppsKey,
		Value: string(appsSecretKey),
	})
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sharedSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, sharedSecret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, sharedSecretKey, sharedSecret.Data[pxutil.SecuritySharedSecretKey])

	systemSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, systemSecret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, systemSecretKey, systemSecret.Data[pxutil.SecuritySystemSecretKey])
	require.Equal(t, appsSecretKey, systemSecret.Data[pxutil.SecurityAppsSecretKey])

	// User pre-configured partial px-system-secrets should not be overwritten
	cluster.Spec.Env = []v1.EnvVar{}
	err = testutil.Delete(k8sClient, sharedSecret)
	require.NoError(t, err)
	err = testutil.Delete(k8sClient, systemSecret)
	require.NoError(t, err)
	sharedSecretKey = pxutil.EncodeBase64([]byte("sharedSecretKey"))
	appsSecretKey = pxutil.EncodeBase64([]byte("appsSecretKey"))
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  pxutil.EnvKeyPortworxAuthJwtSharedSecret,
		Value: string(sharedSecretKey),
	})
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  pxutil.EnvKeyPortworxAuthSystemAppsKey,
		Value: string(appsSecretKey),
	})
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sharedSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, sharedSecret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, sharedSecretKey, sharedSecret.Data[pxutil.SecuritySharedSecretKey])

	systemSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, systemSecret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, 64, len(systemSecret.Data[pxutil.SecuritySystemSecretKey]))
	require.Equal(t, appsSecretKey, systemSecret.Data[pxutil.SecurityAppsSecretKey])

	// existing secret should not be erased if the operator starts using it as sharedSecret
	sharedSecret = &v1.Secret{}
	existingSharedSecretName := "existingSharedsecret"
	sharedSecret.Name = existingSharedSecretName
	sharedSecret.Namespace = cluster.Namespace
	sharedSecret.Data = make(map[string][]byte)
	sharedSecret.StringData = make(map[string]string)
	sharedSecret.Type = v1.SecretTypeOpaque
	sharedSecret.Data["key1"] = []byte("s1")
	sharedSecret.Data["key2"] = []byte("s2")
	err = k8sClient.Create(context.TODO(), sharedSecret)
	require.NoError(t, err)
	cluster.Spec.Security.Auth.SelfSigned.SharedSecret = &existingSharedSecretName

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, sharedSecret, existingSharedSecretName, cluster.Namespace)
	require.NoError(t, err)
	s1, ok := sharedSecret.Data["key1"]
	require.Equal(t, true, ok)
	require.Equal(t, s1, []byte("s1"))
	s2, ok := sharedSecret.Data["key2"]
	require.Equal(t, true, ok)
	require.Equal(t, s2, []byte("s2"))
	sharedSecretVal, ok := sharedSecret.Data["shared-secret"]
	require.Equal(t, true, ok)
	require.Equal(t, string(sharedSecretKey), string(sharedSecretVal))
}

func TestTLSSpecValidation(t *testing.T) {
	caCertFileName := stringPtr("/etc/pwx/testCA.crt")
	serverCertFileName := stringPtr("/etc/pwx/testServer.crt")
	serverKeyFileName := stringPtr("/etc/pwx/testServer.key")

	cluster := testutil.CreateClusterWithTLS(nil, serverCertFileName, serverKeyFileName)
	// missing ca cert
	// It is ok for ca cert to be missing if serverCert and serverKey specified
	s, _ := json.MarshalIndent(cluster.Spec.Security.TLS, "", "\t")
	t.Logf("TLS spec under validation (expect no error): %+v", string(s))

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	// Should not return error and raise no event
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0)

	// // invalid values of ca cert
	// empty value of ca cert ok if serverCert and serverKey specified
	cluster.Spec.Security.TLS.RootCA = &corev1.CertLocation{}
	s, _ = json.MarshalIndent(cluster.Spec.Security.TLS, "", "\t")
	t.Logf("TLS spec under validation (expect no error): %+v", string(s))

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	recorder = record.NewFakeRecorder(10)
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	// Should not return error and raise no event
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 0)

	// invalid value of rootCA
	cluster.Spec.Security.TLS.RootCA = &corev1.CertLocation{
		FileName: stringPtr("not_under_etc_pwx"),
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "rootCA: file path (not_under_etc_pwx) not under folder /etc/pwx")

	cluster.Spec.Security.TLS.RootCA = &corev1.CertLocation{
		FileName:  stringPtr("not_under_etc_pwx"),
		SecretRef: &corev1.SecretRef{},
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "rootCA: file path (not_under_etc_pwx) not under folder /etc/pwx")

	cluster.Spec.Security.TLS.RootCA = &corev1.CertLocation{
		FileName: stringPtr("not_under_etc_pwx"),
		SecretRef: &corev1.SecretRef{
			SecretName: "somesecret",
			SecretKey:  "somekey",
		},
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "rootCA: can not specify both filename and secretRef as source for tls certs")

	cluster.Spec.Security.TLS.RootCA = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "somesecret",
		},
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "rootCA: for kubernetes secret must specify both name and key")

	// missing serverCert
	cluster = testutil.CreateClusterWithTLS(caCertFileName, nil, serverKeyFileName)
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverCert: must specify location of tls certificate as either file or kubernetes secret")

	// // invalid values of serverCert
	cluster.Spec.Security.TLS.ServerCert = &corev1.CertLocation{}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverCert: must specify location of tls certificate as either file or kubernetes secret")

	cluster.Spec.Security.TLS.ServerCert = &corev1.CertLocation{
		FileName: stringPtr("not_under_etc_pwx"),
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverCert: file path (not_under_etc_pwx) not under folder /etc/pwx")

	cluster.Spec.Security.TLS.ServerCert = &corev1.CertLocation{
		FileName:  stringPtr("not_under_etc_pwx"),
		SecretRef: &corev1.SecretRef{},
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverCert: file path (not_under_etc_pwx) not under folder /etc/pwx")

	cluster.Spec.Security.TLS.ServerCert = &corev1.CertLocation{
		FileName: stringPtr("not_under_etc_pwx"),
		SecretRef: &corev1.SecretRef{
			SecretName: "somesecret",
			SecretKey:  "somekey",
		},
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverCert: can not specify both filename and secretRef as source for tls certs")

	// missing serverKey
	cluster = testutil.CreateClusterWithTLS(caCertFileName, serverCertFileName, nil)
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverKey: must specify location of tls certificate as either file or kubernetes secret")

	// // invalid values of serverKey
	cluster.Spec.Security.TLS.ServerKey = &corev1.CertLocation{}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverKey: must specify location of tls certificate as either file or kubernetes secret")

	cluster.Spec.Security.TLS.ServerKey = &corev1.CertLocation{
		FileName: stringPtr("not_under_etc_pwx"),
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverKey: file path (not_under_etc_pwx) not under folder /etc/pwx")

	cluster.Spec.Security.TLS.ServerKey = &corev1.CertLocation{
		FileName:  stringPtr("not_under_etc_pwx"),
		SecretRef: &corev1.SecretRef{},
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverKey: file path (not_under_etc_pwx) not under folder /etc/pwx")

	cluster.Spec.Security.TLS.ServerKey = &corev1.CertLocation{
		FileName: stringPtr("not_under_etc_pwx"),
		SecretRef: &corev1.SecretRef{
			SecretName: "somesecret",
			SecretKey:  "somekey",
		},
	}
	validateThatWarningEventIsRaisedOnPreinstall(t, cluster, "serverKey: can not specify both filename and secretRef as source for tls certs")
}

func validateThatWarningEventIsRaisedOnPreinstall(t *testing.T, cluster *corev1.StorageCluster, errStr string) {
	s, _ := json.MarshalIndent(cluster.Spec.Security.TLS, "", "\t")
	t.Logf("TLS spec under validation: %+v", string(s))

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	// Should return error. This error will eventually bubble up into an event
	err = driver.PreInstall(cluster)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), errStr))
}

func TestSecurityTokenRefreshOnUpdate(t *testing.T) {
	// auth enabled through security.enabled
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
					SelfSigned: &corev1.SelfSignedSpec{
						TokenLifetime: stringPtr("1h"),
					},
				},
			},
		},
	}
	validateSecurityTokenRefreshOnUpdate(t, cluster)

	// auth enabled explicitly
	cluster.Spec.Security.Auth.Enabled = boolPtr(true)
	validateSecurityTokenRefreshOnUpdate(t, cluster)
}

func validateSecurityTokenRefreshOnUpdate(t *testing.T, cluster *corev1.StorageCluster) {
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	// Initial run
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// token should be refreshed if the issuer changes
	err = driver.PreInstall(cluster) // regenerate token with long lifetime
	require.NoError(t, err)

	userSecret := &v1.Secret{}
	cluster.Spec.Security.Auth.SelfSigned.Issuer = stringPtr(fmt.Sprintf("newissuer_for_newtoken%d", rand.Intn(100000)))
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	oldUserToken := userSecret.Data[pxutil.SecurityAuthTokenKey]

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	newUserToken := userSecret.Data[pxutil.SecurityAuthTokenKey]
	require.NotEqual(t, oldUserToken, newUserToken)

	// token should be refreshed if the shared-secret content changes
	userSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	oldUserToken = userSecret.Data[pxutil.SecurityAuthTokenKey]

	sharedSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, sharedSecret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	sharedSecret.Data[pxutil.SecuritySharedSecretKey] = []byte(fmt.Sprintf("mynewsecret%d", rand.Intn(100000)))
	err = testutil.Update(k8sClient, sharedSecret)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	userSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	newUserToken = userSecret.Data[pxutil.SecurityAuthTokenKey]
	require.NotEqual(t, oldUserToken, newUserToken)

	// token should be refreshed if the shared-secret changes to a new secret
	userSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	oldUserToken = userSecret.Data[pxutil.SecurityAuthTokenKey]

	sharedSecret = &v1.Secret{}
	newSharedSecretName := fmt.Sprintf("newsharedsecret%d", rand.Intn(100000))
	sharedSecret.Name = newSharedSecretName
	sharedSecret.Namespace = cluster.Namespace
	sharedSecret.Annotations = map[string]string{component.AnnotationSkipResource: "true"}
	sharedSecret.Data = make(map[string][]byte)
	sharedSecret.Type = v1.SecretTypeOpaque
	sharedSecret.Data[pxutil.SecuritySharedSecretKey] = []byte(fmt.Sprintf("mynewsecret_in_different_location%d", rand.Intn(100000)))
	err = k8sClient.Create(context.TODO(), sharedSecret)
	require.NoError(t, err)
	cluster.Spec.Security.Auth.SelfSigned.SharedSecret = &newSharedSecretName

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	userSecret = &v1.Secret{}
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	newUserToken = userSecret.Data[pxutil.SecurityAuthTokenKey]
	require.NotEqual(t, string(oldUserToken), string(newUserToken))

	// no changes, token remains the same.
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	oldUserToken = userSecret.Data[pxutil.SecurityAuthTokenKey]

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	newUserToken = userSecret.Data[pxutil.SecurityAuthTokenKey]
	require.Equal(t, oldUserToken, newUserToken)
}

func TestSecuritySkipAnnotationIsAdded(t *testing.T) {
	// auth enabled through security.enabled
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
					SelfSigned: &corev1.SelfSignedSpec{
						TokenLifetime: stringPtr("1h"),
					},
				},
			},
		},
	}

	sharedSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-shared-secret",
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			pxutil.SecuritySharedSecretKey: []byte("shared-secret"),
		},
	}
	systemSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-system-secrets",
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			pxutil.SecuritySystemSecretKey: []byte("system-secret"),
			pxutil.SecurityAppsSecretKey:   []byte("apps-secret"),
		},
	}
	adminToken := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-admin-token",
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			pxutil.SecurityAuthTokenKey: []byte("admin-token"),
		},
	}
	userToken := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-user-token",
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			pxutil.SecurityAuthTokenKey: []byte("user-token"),
		},
	}

	k8sClient := testutil.FakeK8sClient(sharedSecret, systemSecret, adminToken, userToken)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	updatedSharedSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, updatedSharedSecret, pxutil.SecurityPXSharedSecretSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		sharedSecret.Data[pxutil.SecuritySharedSecretKey],
		updatedSharedSecret.Data[pxutil.SecuritySharedSecretKey],
	)
	require.Len(t, updatedSharedSecret.Annotations, 1)
	require.Equal(t, "true", updatedSharedSecret.Annotations[component.AnnotationSkipResource])

	updatedSystemSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, updatedSystemSecret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		systemSecret.Data[pxutil.SecuritySystemSecretKey],
		updatedSystemSecret.Data[pxutil.SecuritySystemSecretKey],
	)
	require.Len(t, updatedSystemSecret.Annotations, 1)
	require.Equal(t, "true", updatedSystemSecret.Annotations[component.AnnotationSkipResource])

	updatedAdminToken := &v1.Secret{}
	err = testutil.Get(k8sClient, updatedAdminToken, pxutil.SecurityPXAdminTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, updatedAdminToken.Annotations, 1)
	require.Equal(t, "true", updatedAdminToken.Annotations[component.AnnotationSkipResource])

	updatedUserToken := &v1.Secret{}
	err = testutil.Get(k8sClient, updatedUserToken, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, updatedUserToken.Annotations, 1)
	require.Equal(t, "true", updatedUserToken.Annotations[component.AnnotationSkipResource])
}

// setupEtcHosts sets up given ip/hosts in `/etc/hosts` file for "local" DNS resolution  (i.e. emulate K8s DNS)
// - you will need to be a root-user to run this
// - also, make sure your `/etc/nsswitch.conf` file contains `hosts: files ...` as a first entry
// - hostnames should be in `<service>.<namespace>` format
func setupEtcHosts(t *testing.T, ip string, hostnames ...string) {
	if len(hostnames) <= 0 {
		return
	}
	if err := unix.Access(etcHostsFile, unix.W_OK); err != nil {
		t.Skipf("This test requires ROOT user  (writeable /etc/hosts): %s", err)
	}

	// read original content
	content, err := os.ReadFile("/etc/hosts")
	require.NoError(t, err)

	if ip == "" {
		ip = "127.0.0.1"
	}

	// update content
	bb := bytes.NewBuffer(content)
	bb.WriteString(tempEtcHostsMarker)
	bb.WriteRune('\n')
	for _, hn := range hostnames {
		bb.WriteString(ip)
		bb.WriteRune('\t')
		bb.WriteString(hn)
		bb.WriteRune('\t')
		bb.WriteString(hn)
		bb.WriteString(".svc.cluster.local")
		bb.WriteRune('\n')
	}

	// overwrite /etc/hosts
	fd, err := os.OpenFile(etcHostsFile, os.O_WRONLY|os.O_TRUNC, 0666)
	require.NoError(t, err)

	n, err := fd.Write(bb.Bytes())
	require.NoError(t, err)
	assert.Equal(t, bb.Len(), n, "short write")
	fd.Close()

	// waiting for dns can be resolved
	for i := 0; i < 60; i++ {
		var ips []net.IP
		ips, err = net.LookupIP(hostnames[0])
		if err != nil || !strings.Contains(fmt.Sprintf("%v", ips), ip) {
			logrus.WithFields(logrus.Fields{
				"error": err,
				"ips":   ips,
				"ip":    ip,
				"hosts": hostnames,
			}).Warnf("failed to set /etc/hosts, retrying")
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	require.NoError(t, err)
}

func restoreEtcHosts(t *testing.T) {
	fd, err := os.Open(etcHostsFile)
	require.NoError(t, err)
	var bb bytes.Buffer
	scan := bufio.NewScanner(fd)
	for scan.Scan() {
		line := scan.Text()
		if line == tempEtcHostsMarker {
			// skip copying everything below `tempEtcHostsMarker`
			break
		}
		bb.WriteString(line)
		bb.WriteRune('\n')
	}
	fd.Close()

	// overwrite /etc/hosts
	require.True(t, bb.Len() > 0)
	fd, err = os.OpenFile(etcHostsFile, os.O_WRONLY|os.O_TRUNC, 0666)
	require.NoError(t, err)

	n, err := fd.Write(bb.Bytes())
	require.NoError(t, err)
	assert.Equal(t, bb.Len(), n, "short write")
	fd.Close()
}

func TestGuestAccessSecurity(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockRoleServer := mock.NewMockOpenStorageRoleServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 23888
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Role: mockRoleServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

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
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	component.DeregisterAllComponents()
	component.RegisterAuthComponent()

	driver := portworx{}
	recorder := record.NewFakeRecorder(10)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
					SelfSigned: &corev1.SelfSignedSpec{
						// Since we have a token expiration buffer of one minute,
						// a new token will constantly be fetched.
						TokenLifetime: stringPtr("1s"),
					},
				},
			},
		},
	}

	// Initial run
	setSecuritySpecDefaults(cluster)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// GuestAccess disabled but should not call update as cluster is not yet up.
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleDisabled)
	mockRoleServer.EXPECT().
		Inspect(gomock.Any(), gomock.Any()).
		Return(nil, nil).
		Times(0)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// GuestAccess disabled but should not call update as cluster is still initializing
	cluster.Status.Phase = string(corev1.ClusterStateInit)
	mockRoleServer.EXPECT().
		Inspect(gomock.Any(), gomock.Any()).
		Return(nil, nil).
		Times(0)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Disable GuestAccess. Should expect an update to be called.
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleDisabled)
	cluster.Status.Phase = string(corev1.ClusterStateRunning)
	inspectedRole := &component.GuestRoleEnabled
	inspectedRoleResp := &osdapi.SdkRoleInspectResponse{
		Role: inspectedRole,
	}
	mockRoleServer.EXPECT().
		Inspect(gomock.Any(), gomock.Any()).
		Return(inspectedRoleResp, nil).
		Times(1)
	mockRoleServer.EXPECT().
		Update(gomock.Any(), gomock.Any()).
		Return(&osdapi.SdkRoleUpdateResponse{
			Role: &component.GuestRoleDisabled,
		}, nil).
		Times(1)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Enable guest access, should be updated again
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleEnabled)
	inspectedRole = &component.GuestRoleDisabled
	inspectedRoleResp = &osdapi.SdkRoleInspectResponse{
		Role: inspectedRole,
	}
	mockRoleServer.EXPECT().
		Inspect(gomock.Any(), gomock.Any()).
		Return(inspectedRoleResp, nil).
		Times(1)
	mockRoleServer.EXPECT().
		Update(gomock.Any(), gomock.Any()).
		Return(&osdapi.SdkRoleUpdateResponse{
			Role: &component.GuestRoleEnabled,
		}, nil).
		Times(1)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// run without any change should result in only an inspect call
	inspectedRole = &component.GuestRoleEnabled
	inspectedRoleResp = &osdapi.SdkRoleInspectResponse{
		Role: inspectedRole,
	}
	mockRoleServer.EXPECT().
		Inspect(gomock.Any(), gomock.Any()).
		Return(inspectedRoleResp, nil).
		Times(1)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// GuestRole type changed, but PX is below 2.6, so no calls are needed.
	inspectedRole = &component.GuestRoleDisabled
	inspectedRoleResp = &osdapi.SdkRoleInspectResponse{
		Role: inspectedRole,
	}
	mockRoleServer.EXPECT().
		Inspect(gomock.Any(), gomock.Any()).
		Return(inspectedRoleResp, nil).
		Times(0)
	cluster.Spec.Image = "px/image:2.5.0"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Invalid guest access type, should be updated again
	cluster.Spec.Image = "px/image:2.6.0"
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestAccessType("invalid"))
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup %v.", v1.EventTypeWarning,
			util.FailedComponentReason, component.AuthComponentName))

	// set to managed to avoid more calls without a corresponding mock expect
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	// no expected mock calls
}

func TestDisableSecurity(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
	}
	setSecuritySpecDefaults(cluster)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sharedSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, sharedSecret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	systemSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, systemSecret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
	adminSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, adminSecret, pxutil.SecurityPXAdminTokenSecretName, cluster.Namespace)
	require.NoError(t, err)
	userSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.NoError(t, err)

	// Disable security, secrets should not be deleted, but tokens should be deleted
	cluster.Spec.Security.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, sharedSecret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, systemSecret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, adminSecret, pxutil.SecurityPXAdminTokenSecretName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, userSecret, pxutil.SecurityPXUserTokenSecretName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallDisable(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.11.4",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "false",
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
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.Error(t, err)
}

func TestCSIInstall(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.11.4",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.2",
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
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// CSI ServiceAccount
	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 2)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.CSIServiceAccountName, sa.Name)
	require.Equal(t, cluster.Namespace, sa.Namespace)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// CSI ClusterRole
	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 2)

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.11.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// CSI ClusterRoleBinding
	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 2)

	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "csiClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.CSIClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// CSI Service
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Len(t, serviceList.Items, 4)

	expectedService := testutil.GetExpectedService(t, "csiService.yaml")
	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.CSIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedService.Name, service.Name)
	require.Equal(t, expectedService.Namespace, service.Namespace)
	require.Len(t, service.OwnerReferences, 1)
	require.Equal(t, cluster.Name, service.OwnerReferences[0].Name)
	require.Equal(t, expectedService.Labels, service.Labels)
	require.Equal(t, expectedService.Spec, service.Spec)

	// CSI StatefulSet
	statefulSetList := &appsv1.StatefulSetList{}
	err = testutil.List(k8sClient, statefulSetList)
	require.NoError(t, err)
	require.Len(t, statefulSetList.Items, 1)

	expectedStatefulSet := testutil.GetExpectedStatefulSet(t, "csiStatefulSet_0.3.yaml")
	statefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedStatefulSet.Name, statefulSet.Name)
	require.Equal(t, expectedStatefulSet.Namespace, statefulSet.Namespace)
	require.Len(t, statefulSet.OwnerReferences, 1)
	require.Equal(t, cluster.Name, statefulSet.OwnerReferences[0].Name)
	require.Equal(t, expectedStatefulSet.Spec, statefulSet.Spec)

	// CSIDriver object should be created only for k8s version 1.14+
	csiDriver := &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, csiDriver, pxutil.DeprecatedCSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallWithk8s1_13(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
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
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.13.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeployment_1.0.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// CSIDriver object should be not be created for k8s 1.13
	csiDriver := &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, csiDriver, pxutil.DeprecatedCSIDriverName, "")
	require.True(t, errors.IsNotFound(err))

	// Install with Portworx version 2.2+ and k8s version 1.14
	// We should use add the resizer sidecar and use the new driver name.
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()
	cluster.Spec.Image = "portworx/image:2.2"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeploymentWithResizer_1.0.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// CSIDriver object should be created for k8s 1.14+
	// For Portworx 2.2+ we should create the driver object with new driver name
	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.NoError(t, err)
	require.Empty(t, csiDriver.OwnerReferences)
	require.False(t, *csiDriver.Spec.AttachRequired)
	require.True(t, *csiDriver.Spec.PodInfoOnMount)
	require.Nil(t, csiDriver.Spec.FSGroupPolicy)

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.DeprecatedCSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallWithk8s1_20(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.20.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
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
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
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
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpExists,
									},
									{
										Key:      "node-role.kubernetes.io/worker",
										Operator: v1.NodeSelectorOpExists,
									},
								},
							},
						},
					},
				},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.14.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, len(expectedDeployment.Spec.Template.Spec.Containers), len(deployment.Spec.Template.Spec.Containers))
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// CSIDriver object should be created for k8s 1.20
	csiDriver := &storagev1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.False(t, errors.IsNotFound(err))
	require.Equal(t, "storage.k8s.io/v1", csiDriver.TypeMeta.APIVersion)
	require.Equal(t, *csiDriver.Spec.FSGroupPolicy, storagev1.FileFSGroupPolicy)
	err = testutil.Get(k8sClient, csiDriver, pxutil.DeprecatedCSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallEphemeralWithK8s1_17VersionAndPX2_5(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.17.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
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
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Enable snapshot controller
	cluster.Spec.CSI.Enabled = true
	cluster.Spec.CSI.InstallSnapshotController = boolPtr(true)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Should have volumesnapshot CRDs
	_, err = apiextensionsops.Instance().GetCRD("volumesnapshots.snapshot.storage.k8s.io", metav1.GetOptions{})
	require.NoError(t, err)

	// Should have volumesnapshotclass CRDs
	_, err = apiextensionsops.Instance().GetCRD("volumesnapshotclasses.snapshot.storage.k8s.io", metav1.GetOptions{})
	require.NoError(t, err)

	// Should have volumesnapshotcontent CRDs
	_, err = apiextensionsops.Instance().GetCRD("volumesnapshotcontents.snapshot.storage.k8s.io", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestCSIInstallEphemeralWithK8s1_20VersionAndPX2_5(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.20.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
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
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiDriver := &storagev1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.NoError(t, err)
	require.Empty(t, csiDriver.OwnerReferences)
	require.False(t, *csiDriver.Spec.AttachRequired)
	require.True(t, *csiDriver.Spec.PodInfoOnMount)
	require.Equal(t, len(csiDriver.Spec.VolumeLifecycleModes), 2)
	require.Contains(t, csiDriver.Spec.VolumeLifecycleModes, storagev1.VolumeLifecyclePersistent)
	require.Contains(t, csiDriver.Spec.VolumeLifecycleModes, storagev1.VolumeLifecycleEphemeral)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, len(expectedDeployment.Spec.Template.Spec.Containers), len(deployment.Spec.Template.Spec.Containers))
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Should enable csi and snapshot controller by default
	require.NotNil(t, cluster.Spec.CSI)
	require.True(t, cluster.Spec.CSI.Enabled)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.True(t, *cluster.Spec.CSI.InstallSnapshotController)

	// Should have volumesnapshot CRDs
	_, err = apiextensionsops.Instance().GetCRD("volumesnapshots.snapshot.storage.k8s.io", metav1.GetOptions{})
	require.NoError(t, err)

	// Should have volumesnapshotclass CRDs
	_, err = apiextensionsops.Instance().GetCRD("volumesnapshotclasses.snapshot.storage.k8s.io", metav1.GetOptions{})
	require.NoError(t, err)

	// Should have volumesnapshotcontent CRDs
	_, err = apiextensionsops.Instance().GetCRD("volumesnapshotcontents.snapshot.storage.k8s.io", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestCSIInstallWithk8s1_21_px210(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.21.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
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
			Image: "portworx/image:2.10.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.14.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s123.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, len(expectedDeployment.Spec.Template.Spec.Containers), len(deployment.Spec.Template.Spec.Containers))
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)
}

func TestCSIInstallWithPKS(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.5.5",
			CSI: &corev1.CSISpec{
				Enabled: true,
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
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeploymentWithPKS.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)
}

func TestCSIInstallShouldCreateNodeInfoCRD(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	extensionsClient := fakeextclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	apiextensionsops.SetInstance(apiextensionsops.New(extensionsClient))
	// CSINodeInfo CRD should be created for k8s version 1.12.*
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
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
			Image: "portworx/image:2.1.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	expectedCRD := testutil.GetExpectedCRD(t, "csiNodeInfoCrd.yaml")
	go func() {
		err = testutil.ActivateV1beta1CRDWhenCreated(extensionsClient, expectedCRD.Name)
		require.NoError(t, err)
	}()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	actualCRD, err := extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), expectedCRD.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, expectedCRD.Name, actualCRD.Name)
	require.Equal(t, expectedCRD.Labels, actualCRD.Labels)
	require.Equal(t, expectedCRD.Spec, actualCRD.Spec)

	// Expect the CRD to be created even for k8s version 1.13.*
	err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Delete(context.TODO(), expectedCRD.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	csiComponent, _ := component.Get(component.CSIComponentName)
	csiComponent.MarkDeleted()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.99",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	go func() {
		err := testutil.ActivateV1beta1CRDWhenCreated(extensionsClient, expectedCRD.Name)
		require.NoError(t, err)
	}()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	actualCRD, err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), expectedCRD.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, expectedCRD.Name, actualCRD.Name)
	require.Equal(t, expectedCRD.Labels, actualCRD.Labels)
	require.Equal(t, expectedCRD.Spec, actualCRD.Spec)

	// CRD should not to be created for k8s version 1.11.* or below
	err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Delete(context.TODO(), expectedCRD.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	csiComponent.MarkDeleted()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.11.99",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	_, err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), expectedCRD.Name, metav1.GetOptions{})
	require.True(t, errors.IsNotFound(err))

	// CRD should not to be created for k8s version 1.14+
	csiComponent.MarkDeleted()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	_, err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), expectedCRD.Name, metav1.GetOptions{})
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallWithDeprecatedCSIDriverName(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	// Install with Portworx version 2.2+ and deprecated CSI driver name
	// We should use add the resizer sidecar and use the old driver name.
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_USEDEPRECATED_CSIDRIVERNAME",
						Value: "true",
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

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeploymentWithResizerAndOldDriver_1.0.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// CSIDriver should also be created with deprecated driver name
	csiDriver := &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.DeprecatedCSIDriverName, "")
	require.NoError(t, err)
	require.Empty(t, csiDriver.OwnerReferences)
	require.False(t, *csiDriver.Spec.AttachRequired)
	require.True(t, *csiDriver.Spec.PodInfoOnMount)

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallWithAlphaFeaturesDisabled(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_DISABLE_CSI_ALPHA",
						Value: "true",
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
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// Case: Should not deploy resizer and snapshotter as they are alpha in k8s 1.14
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeploymentAlphaDisabledK8s_1_14.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Case: Should not deploy snapshotter as it is alpha in k8s 1.16
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.16.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeploymentAlphaDisabledK8s_1_16.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Case: Should deploy all sidecars in k8s 1.17 as they are not in alpha
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.17.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeploymentWithResizer_1_17.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Case: Should deploy all sidecars even in k8s 1.14, as alpha features are not disabled
	err = k8sClient.Delete(context.TODO(), deployment)
	require.NoError(t, err)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()
	cluster.Spec.Env[0].Value = "false"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeploymentWithResizer_1.0.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Case: Should deploy all sidecars even in k8s 1.14, as invalid value is passed in env
	// and the default behavior is to allow alpha features
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()
	cluster.Spec.Env[0].Value = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeploymentWithResizer_1.0.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)
}

func TestCSIInstallWithTopology(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.20.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
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
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
				Topology: &corev1.CSITopologySpec{
					Enabled: true,
				},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120_with_topology.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, len(expectedDeployment.Spec.Template.Spec.Containers), len(deployment.Spec.Template.Spec.Containers))
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Disable CSI topology by setting the flag to false
	cluster.Spec.CSI.Topology.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	container := k8sutil.GetContainerFromDeployment(deployment, "csi-external-provisioner")
	require.NotContains(t, container.Args, "--feature-gates=Topology=true")

	// Enable again
	cluster.Spec.CSI.Topology.Enabled = true
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	container = k8sutil.GetContainerFromDeployment(deployment, "csi-external-provisioner")
	require.Contains(t, container.Args, "--feature-gates=Topology=true")

	// Disable CSI topology by removing the topology spec
	cluster.Spec.CSI.Topology = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	container = k8sutil.GetContainerFromDeployment(deployment, "csi-external-provisioner")
	require.NotContains(t, container.Args, "--feature-gates=Topology=true")
}

func TestCSIClusterRoleK8sVersionGreaterThan_1_14(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
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
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.14.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)
}

func TestCSI_1_0_ChangeImageVersions(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		deployment.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		deployment.Spec.Template.Spec.Containers[1].Image)
	require.Equal(t, "quay.io/k8scsi/csi-snapshotter:v1.2.3",
		deployment.Spec.Template.Spec.Containers[2].Image)

	// Change provisioner image
	deployment.Spec.Template.Spec.Containers[0].Image = "my-csi-provisioner:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		deployment.Spec.Template.Spec.Containers[0].Image)

	// Change attacher image
	deployment.Spec.Template.Spec.Containers[1].Image = "my-csi-attacher:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		deployment.Spec.Template.Spec.Containers[1].Image)

	// Change snapshotter image
	deployment.Spec.Template.Spec.Containers[2].Image = "my-csi-snapshotter:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-snapshotter:v1.2.3",
		deployment.Spec.Template.Spec.Containers[2].Image)

	// Enable resizer and the change it's image
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v1.2.3",
		deployment.Spec.Template.Spec.Containers[2].Image)

	deployment.Spec.Template.Spec.Containers[2].Image = "my-csi-resizer:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v1.2.3",
		deployment.Spec.Template.Spec.Containers[2].Image)
}

func TestCSI_0_3_ChangeImageVersions(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.0",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	statefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, statefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		statefulSet.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		statefulSet.Spec.Template.Spec.Containers[1].Image)

	// Change provisioner image
	statefulSet.Spec.Template.Spec.Containers[0].Image = "my-csi-provisioner:test"
	err = k8sClient.Update(context.TODO(), statefulSet)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		statefulSet.Spec.Template.Spec.Containers[0].Image)

	// Change attacher image
	statefulSet.Spec.Template.Spec.Containers[1].Image = "my-csi-attacher:test"
	err = k8sClient.Update(context.TODO(), statefulSet)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		statefulSet.Spec.Template.Spec.Containers[1].Image)
}

func TestCSIChangeKubernetesVersions(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	statefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, statefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		statefulSet.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		statefulSet.Spec.Template.Spec.Containers[1].Image)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Use kubernetes version 1.13. CSI sidecars should run as Deployment instead of StatefulSet
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// We should remove the old StatefulSet and replaced it with Deployment
	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		deployment.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "--leader-election-type=endpoints",
		deployment.Spec.Template.Spec.Containers[0].Args[4])
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		deployment.Spec.Template.Spec.Containers[1].Image)
	require.Equal(t, "--leader-election-type=configmaps",
		deployment.Spec.Template.Spec.Containers[1].Args[3])
	require.Equal(t, "quay.io/k8scsi/csi-snapshotter:v1.2.3",
		deployment.Spec.Template.Spec.Containers[2].Image)
	require.Equal(t, "--leader-election-type=configmaps",
		deployment.Spec.Template.Spec.Containers[2].Args[3])

	// Use kubernetes version 1.14. We should use CSIDriverInfo instead of attacher sidecar
	// and add the resizer sidecar
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		deployment.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "--leader-election-type=leases",
		deployment.Spec.Template.Spec.Containers[0].Args[4])
	require.Equal(t, "quay.io/k8scsi/csi-snapshotter:v1.2.3",
		deployment.Spec.Template.Spec.Containers[1].Image)
	require.Len(t, deployment.Spec.Template.Spec.Containers[1].Args, 3)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v1.2.3",
		deployment.Spec.Template.Spec.Containers[2].Image)
}

func TestCSI_0_3_ImagePullSecretChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	imagePullSecret := "pull-secret"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.0",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			ImagePullSecret: &imagePullSecret,
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
	require.NoError(t, err)

	// Case: Image pull secret should be applied to the deployment
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	// Case: Updated image pull secet should be applied to the deployment
	imagePullSecret = "new-secret"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	// Case: If empty, remove image pull secret from the deployment
	imagePullSecret = ""
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets)

	// Case: If nil, remove image pull secret from the deployment
	cluster.Spec.ImagePullSecret = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets)

	// Case: Image pull secret should be added back if not present in deployment
	imagePullSecret = "pull-secret"
	cluster.Spec.ImagePullSecret = &imagePullSecret
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, csiStatefulSet.Spec.Template.Spec.ImagePullSecrets[0].Name)
}

func TestCSI_0_3_TolerationsChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	tolerations := []v1.Toleration{
		{
			Key:      "foo",
			Value:    "bar",
			Operator: v1.TolerationOpEqual,
			Effect:   v1.TaintEffectNoSchedule,
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.0",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Placement: &corev1.PlacementSpec{
				Tolerations: tolerations,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
	require.NoError(t, err)

	// Case: Tolerations should be applied to the deployment
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiStatefulSet.Spec.Template.Spec.Tolerations)

	// Case: Updated tolerations should be applied to the deployment
	tolerations[0].Value = "baz"
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiStatefulSet.Spec.Template.Spec.Tolerations)

	// Case: New tolerations should be applied to the deployment
	tolerations = append(tolerations, v1.Toleration{
		Key:      "must-exist",
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoExecute,
	})
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiStatefulSet.Spec.Template.Spec.Tolerations)

	// Case: Removed tolerations should be removed from the deployment
	tolerations = []v1.Toleration{tolerations[0]}
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiStatefulSet.Spec.Template.Spec.Tolerations)

	// Case: If tolerations are empty, should be removed from the deployment
	cluster.Spec.Placement.Tolerations = []v1.Toleration{}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiStatefulSet.Spec.Template.Spec.Tolerations)

	// Case: Tolerations should be added back if not present in deployment
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiStatefulSet.Spec.Template.Spec.Tolerations)

	// Case: If placement is empty, deployment should not have tolerations
	cluster.Spec.Placement = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiStatefulSet.Spec.Template.Spec.Tolerations)
}

func TestCSI_0_3_NodeAffinityChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	nodeAffinity := &v1.NodeAffinity{
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
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.0",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Placement: &corev1.PlacementSpec{
				NodeAffinity: nodeAffinity,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
	require.NoError(t, err)

	// Case: Node affinity should be applied to the deployment
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, csiStatefulSet.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: Updated node affinity should be applied to the deployment
	nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
		NodeSelectorTerms[0].
		MatchExpressions[0].
		Key = "px/disabled"
	cluster.Spec.Placement.NodeAffinity = nodeAffinity
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, csiStatefulSet.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: If node affinity is removed, it should be removed from the deployment
	cluster.Spec.Placement.NodeAffinity = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiStatefulSet.Spec.Template.Spec.Affinity)

	// Case: Node affinity should be added back if not present in deployment
	cluster.Spec.Placement.NodeAffinity = nodeAffinity
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, csiStatefulSet.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: If placement is nil, node affinity should be removed from the deployment
	cluster.Spec.Placement = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiStatefulSet.Spec.Template.Spec.Affinity)
}

func TestCSIInstallWithCustomKubeletDir(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.11.4",
	}
	nodeName := "testNode"
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	customKubeletPath := "/data/kubelet"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.1.2",
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
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyKubeletDir,
						Value: customKubeletPath,
					},
				},
			},
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// CSI StatefulSet
	statefulSetList := &appsv1.StatefulSetList{}
	err = testutil.List(k8sClient, statefulSetList)
	require.NoError(t, err)
	require.Len(t, statefulSetList.Items, 1)

	var validStatefulSetSocketPath, validCSIDriverPath bool
	for _, v := range statefulSetList.Items[0].Spec.Template.Spec.Volumes {
		if v.Name == "socket-dir" && v.HostPath.Path == customKubeletPath+"/csi-plugins/com.openstorage.pxd" {
			validStatefulSetSocketPath = true
		}
	}
	require.True(t, validStatefulSetSocketPath)

	spec, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	logrus.Infof("Volumes %+v", spec.Volumes)

	// CSI driver path
	for _, v := range spec.Volumes {
		if v.Name == "csi-driver-path" && v.HostPath.Path == customKubeletPath+"/csi-plugins/com.openstorage.pxd" {
			validCSIDriverPath = true
		}
	}
	require.True(t, validCSIDriverPath)
}

func TestPrometheusUpgradeDefaultDesiredImages(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.22.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	pxutil.SpecsBaseDir = func() string {
		return pxutil.PortworxSpecsDir
	}

	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				PrometheusOperator: manifest.DefaultPrometheusOperatorImage,
			},
		},
	}

	// Setup manifest and config directory
	m := &fakeManifest{}
	m.Init(nil, nil, k8sutil.K8sVer1_22)
	manifest.SetInstance(m)
	defer manifestSetup()
	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	// TestCase: k8s 1.22 should upgrade Prometheus operator image
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	cluster.Spec.Placement = nil
	require.Equal(t,
		"quay.io/prometheus-operator/prometheus-operator:v0.50.0",
		cluster.Status.DesiredImages.PrometheusOperator)

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

	// Prometheus operator ClusterRole should be updated as well
	expectedCR := testutil.GetExpectedClusterRole(t, "prometheusOperatorClusterRole.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.PrometheusOperatorClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Prometheus operator Deployment
	expectedDeployment := testutil.GetExpectedDeployment(t, "prometheusOperatorDeployment_v0.50.0.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Labels, deployment.Labels)
	require.Equal(t, expectedDeployment.Annotations, deployment.Annotations)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Prometheus and alert manager instances should use new images
	prometheusInstance := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/prometheus/prometheus:v2.29.1",
		*prometheusInstance.Spec.Image,
	)
	alertManagerInstance := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/prometheus/alertmanager:v0.22.2",
		*alertManagerInstance.Spec.Image,
	)

	// Prometheus CRDs should be registered
	crdNameList := []string{
		"alertmanagerconfigs.monitoring.coreos.com",
		"alertmanagers.monitoring.coreos.com",
		"podmonitors.monitoring.coreos.com",
		"probes.monitoring.coreos.com",
		"prometheuses.monitoring.coreos.com",
		"prometheusrules.monitoring.coreos.com",
		"servicemonitors.monitoring.coreos.com",
		"thanosrulers.monitoring.coreos.com",
		"volumesnapshots.snapshot.storage.k8s.io",
		"volumesnapshotclasses.snapshot.storage.k8s.io",
		"volumesnapshotcontents.snapshot.storage.k8s.io",
	}
	crds, err := fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, len(crdNameList))
	for _, crdName := range crdNameList {
		_, err := fakeExtClient.ApiextensionsV1().
			CustomResourceDefinitions().
			Get(context.TODO(), crdName, metav1.GetOptions{})
		require.NoError(t, err)
	}
}

func TestPrometheusInstall(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	cluster.Spec.Placement = nil

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

	// Prometheus operator ServiceAccount
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusOperatorServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Prometheus operator ClusterRole
	expectedCR := testutil.GetExpectedClusterRole(t, "prometheusOperatorClusterRole.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.PrometheusOperatorClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Prometheus operator ClusterRoleBinding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "prometheusOperatorClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.PrometheusOperatorClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Prometheus operator Deployment
	expectedDeployment := testutil.GetExpectedDeployment(t, "prometheusOperatorDeployment.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Labels, deployment.Labels)
	require.Equal(t, expectedDeployment.Annotations, deployment.Annotations)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Prometheus ServiceAccount
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Prometheus ClusterRole
	expectedCR = testutil.GetExpectedClusterRole(t, "prometheusClusterRole.yaml")
	actualCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.PrometheusClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Empty(t, actualCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Prometheus ClusterRoleBinding
	expectedCRB = testutil.GetExpectedClusterRoleBinding(t, "prometheusClusterRoleBinding.yaml")
	actualCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.PrometheusClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Empty(t, actualCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Prometheus Service
	expectedService := testutil.GetExpectedService(t, "prometheusService.yaml")
	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, component.PrometheusServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedService.Name, actualService.Name)
	require.Equal(t, expectedService.Namespace, actualService.Namespace)
	require.Len(t, actualService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualService.OwnerReferences[0].Name)
	require.Equal(t, expectedService.Labels, actualService.Labels)
	require.Equal(t, expectedService.Spec, actualService.Spec)

	// Prometheus instance
	prometheusList := &monitoringv1.PrometheusList{}
	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.Len(t, prometheusList.Items, 1)

	expectedPrometheus := testutil.GetExpectedPrometheus(t, "prometheusInstance.yaml")
	prometheus := prometheusList.Items[0]
	require.Equal(t, expectedPrometheus.Name, prometheus.Name)
	require.Equal(t, expectedPrometheus.Namespace, prometheus.Namespace)
	require.Len(t, prometheus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, prometheus.OwnerReferences[0].Name)
	require.Equal(t, expectedPrometheus.Spec, prometheus.Spec)

	// Prometheus instance with remote write endpoint
	cluster.Spec.Monitoring.Prometheus.RemoteWriteEndpoint = "test.endpoint:1234"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedPrometheus = testutil.GetExpectedPrometheus(t, "prometheusInstanceWithRemoteWriteEndpoint.yaml")
	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPrometheus.Name, prometheus.Name)
	require.Equal(t, expectedPrometheus.Namespace, prometheus.Namespace)
	require.Len(t, prometheus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, prometheus.OwnerReferences[0].Name)
	require.Equal(t, expectedPrometheus.Spec, prometheus.Spec)

	// Prometheus instance with alert manager enabled
	cluster.Spec.Monitoring.Prometheus.RemoteWriteEndpoint = ""
	cluster.Spec.Monitoring.Prometheus.AlertManager = &corev1.AlertManagerSpec{Enabled: true}
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedPrometheus = testutil.GetExpectedPrometheus(t, "prometheusInstanceWithAlertManager.yaml")
	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPrometheus.Name, prometheus.Name)
	require.Equal(t, expectedPrometheus.Namespace, prometheus.Namespace)
	require.Len(t, prometheus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, prometheus.OwnerReferences[0].Name)
	require.Equal(t, expectedPrometheus.Spec, prometheus.Spec)

	// Modify resources and security context.
	cluster.Spec.Monitoring.Prometheus.Resources = v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("4Gi"),
			v1.ResourceCPU:    resource.MustParse("4"),
		},
	}
	runAsNonRoot := false
	cluster.Spec.Monitoring.Prometheus.SecurityContext = &v1.PodSecurityContext{
		RunAsNonRoot: &runAsNonRoot,
	}
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, prometheus.Spec.Resources.Requests, cluster.Spec.Monitoring.Prometheus.Resources.Requests)
	require.Equal(t, prometheus.Spec.SecurityContext, cluster.Spec.Monitoring.Prometheus.SecurityContext)

	// Modify replicas, retention, retentionSize, storage, volumes, volumeMounts
	replicas := int32(2)
	fakeSCName := "fake-test-sc"
	cluster.Spec.Monitoring.Prometheus.Replicas = &replicas
	cluster.Spec.Monitoring.Prometheus.Retention = "10h"
	cluster.Spec.Monitoring.Prometheus.RetentionSize = "1048MB"
	cluster.Spec.Monitoring.Prometheus.Storage = &monitoringv1.StorageSpec{
		VolumeClaimTemplate: monitoringv1.EmbeddedPersistentVolumeClaim{
			Spec: v1.PersistentVolumeClaimSpec{
				StorageClassName: &fakeSCName,
				Resources: v1.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceStorage: resource.MustParse("1G"),
					},
				},
			},
		},
	}
	cluster.Spec.Monitoring.Prometheus.VolumeMounts = []v1.VolumeMount{
		{
			Name:      "testVlm",
			MountPath: "/prometheus/",
		},
	}
	cluster.Spec.Monitoring.Prometheus.Volumes = []v1.Volume{
		{
			Name: "testVlm",
			VolumeSource: v1.VolumeSource{
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: "testPxPvc",
					ReadOnly:  false,
				},
			},
		},
	}
	cluster.Spec.Monitoring.Prometheus.SecurityContext = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, prometheus.Spec.Replicas, cluster.Spec.Monitoring.Prometheus.Replicas)
	require.Equal(t, prometheus.Spec.Retention, cluster.Spec.Monitoring.Prometheus.Retention)
	require.Equal(t, prometheus.Spec.RetentionSize, cluster.Spec.Monitoring.Prometheus.RetentionSize)
	require.Equal(t, prometheus.Spec.Storage, cluster.Spec.Monitoring.Prometheus.Storage)
	require.Equal(t, prometheus.Spec.VolumeMounts, cluster.Spec.Monitoring.Prometheus.VolumeMounts)
	require.Equal(t, prometheus.Spec.Volumes, cluster.Spec.Monitoring.Prometheus.Volumes)
}

func TestCompleteInstallDuringMigration(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved:       "true",
				constants.AnnotationPauseComponentMigration: "true",
				pxutil.AnnotationPVCController:              "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/image:2.2",
			ImagePullPolicy: v1.PullIfNotPresent,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "cluster-uid",
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

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	serviceMonitor := &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, component.PxServiceMonitor, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	alertManager := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManager, component.AlertManagerInstanceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	collectorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestCompleteInstallWithImagePullPolicy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/image:2.2",
			ImagePullPolicy: v1.PullIfNotPresent,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
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

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		pxAPIDaemonSet.Spec.Template.Spec.Containers[0].ImagePullPolicy)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		pvcDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		testutil.GetPullPolicyForContainer(lhDeployment, component.LhContainerName))
	require.Equal(t, v1.PullIfNotPresent,
		testutil.GetPullPolicyForContainer(lhDeployment, component.LhConfigSyncContainerName))
	require.Equal(t, v1.PullIfNotPresent,
		testutil.GetPullPolicyForContainer(lhDeployment, component.LhStorkConnectorContainerName))
	require.Equal(t, v1.PullIfNotPresent,
		lhDeployment.Spec.Template.Spec.InitContainers[0].ImagePullPolicy)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		autopilotDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[1].ImagePullPolicy,
	)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[2].ImagePullPolicy,
	)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[3].ImagePullPolicy,
	)

	// Change the k8s version to 1.14, so that resizer sidecar is deployed
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[2].ImagePullPolicy,
	)

	// Change the k8s version to 1.12, so that CSI stateful set is created instead of deployment
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiStatefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t, v1.PullIfNotPresent,
		csiStatefulSet.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)
	require.Equal(t, v1.PullIfNotPresent,
		csiStatefulSet.Spec.Template.Spec.Containers[1].ImagePullPolicy,
	)
}

func TestCompleteInstallWithCustomRegistryChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	customRegistry := "test-registry:1111"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:               "portworx/image:2.9.1",
			CustomImageRegistry: customRegistry,
			StartPort:           &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			PxRepo: &corev1.PxRepoSpec{
				Enabled: true,
				Image:   "testImage:latest",
			},
		},
		Status: corev1.StorageClusterStatus{ClusterUID: "test-uid"},
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

	// Case: Custom registry should be added to the images
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/pause:3.1",
		pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image,
	)

	pxProxyDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/pause:3.1",
		pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image,
	)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/coreos/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=" + customRegistry + "/coreos/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=" + customRegistry + "/coreos/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/prometheus/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/prometheus/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		customRegistry+"/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRegistry+"/purestorage/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/envoyproxy/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Update registry should be added to the images
	customRegistry = "test-registry:2222"
	cluster.Spec.CustomImageRegistry = customRegistry

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/pause:3.1",
		pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image,
	)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/pause:3.1",
		pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image,
	)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/coreos/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=" + customRegistry + "/coreos/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=" + customRegistry + "/coreos/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/prometheus/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/prometheus/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		customRegistry+"/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRegistry+"/purestorage/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/envoyproxy/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: If empty, remove custom registry from images
	cluster.Spec.CustomImageRegistry = ""

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "registry.k8s.io/pause:3.1", pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, "registry.k8s.io/pause:3.1", pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"docker.io/portworx/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		"docker.io/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		"docker.io/portworx/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		"docker.io/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"docker.io/portworx/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/coreos/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=quay.io/coreos/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=quay.io/coreos/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/prometheus/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/prometheus/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		"quay.io/k8scsi/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		"docker.io/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		"docker.io/purestorage/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		"docker.io/envoyproxy/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Custom registry should be added back in not present in images
	customRegistry = "test-registry:3333"
	cluster.Spec.CustomImageRegistry = customRegistry

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/pause:3.1",
		pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image,
	)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/pause:3.1",
		pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image,
	)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/coreos/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=" + customRegistry + "/coreos/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=" + customRegistry + "/coreos/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/prometheus/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/prometheus/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		cluster.Spec.CustomImageRegistry+"/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRegistry+"/purestorage/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/envoyproxy/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)
}

func TestCompleteInstallWithCustomRegistryChangeForK8s_1_14(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	customRegistry := "test-registry:1111"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:               "portworx/image:2.2",
			CustomImageRegistry: customRegistry,
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// Case: Custom registry should be added to the images
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Case: Update registry should be added to the images
	customRegistry = "test-registry:2222"
	cluster.Spec.CustomImageRegistry = customRegistry

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Case: If empty, remove custom registry from images
	cluster.Spec.CustomImageRegistry = ""

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		"quay.io/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Case: Custom registry should be added back in not present in images
	customRegistry = "test-registry:3333"
	cluster.Spec.CustomImageRegistry = customRegistry

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
}

func TestCompleteInstallWithCustomRegistryChangeForK8s_1_12(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	customRegistry := "test-registry:1111"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:               "portworx/image:2.2",
			CustomImageRegistry: customRegistry,
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// Case: Custom registry should be added to the images
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Update registry should be added to the images
	customRegistry = "test-registry:2222"
	cluster.Spec.CustomImageRegistry = customRegistry

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: If empty, remove custom registry from images
	cluster.Spec.CustomImageRegistry = ""

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		"quay.io/k8scsi/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Custom registry should be added back in not present in images
	customRegistry = "test-registry:3333"
	cluster.Spec.CustomImageRegistry = customRegistry

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/k8scsi/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)
}

func TestCompleteInstallWithCustomRepoRegistryChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:               "portworx/image:2.9.1",
			CustomImageRegistry: customRepo,
			StartPort:           &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{Enabled: true},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			PxRepo: &corev1.PxRepoSpec{
				Enabled: true,
				Image:   "pxRepoImage:latest",
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-uid",
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

	// Case: Custom repo-registry should be added to the images
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pxProxyDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=" + customRepo + "/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=" + customRepo + "/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		customRepo+"/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		customRepo+"/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Updated repo-registry should be added to the images
	customRepo = "test-registry:1111/new-repo"
	cluster.Spec.CustomImageRegistry = customRepo

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=" + customRepo + "/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=" + customRepo + "/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		customRepo+"/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		customRepo+"/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Flat registry should be used for images
	customRepo = "test-registry:1111"
	cluster.Spec.CustomImageRegistry = customRepo + "//"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=" + customRepo + "/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=" + customRepo + "/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		customRepo+"/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		customRepo+"/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: If empty, remove custom repo-registry from images
	cluster.Spec.CustomImageRegistry = ""

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "registry.k8s.io/pause:3.1", pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, "registry.k8s.io/pause:3.1", pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"docker.io/portworx/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		"docker.io/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		"docker.io/portworx/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		"docker.io/portworx/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"docker.io/portworx/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/coreos/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=quay.io/coreos/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=quay.io/coreos/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/prometheus/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"quay.io/prometheus/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		"quay.io/k8scsi/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		"docker.io/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		"docker.io/purestorage/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		"docker.io/envoyproxy/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Custom repo-registry should be added back in not present in images
	customRepo = "test-registry:1111/newest-repo"
	cluster.Spec.CustomImageRegistry = customRepo

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxProxyDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.18.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/px-lighthouse:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-stork-connector:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:"+newCompVersion(),
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/autopilot:"+newCompVersion(),
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment,
		component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus-operator:v1.2.3",
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Subset(t, prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Args, []string{
		"--config-reloader-image=" + customRepo + "/configmap-reload:v1.2.3",
		"--prometheus-config-reloader=" + customRepo + "/prometheus-config-reloader:v1.2.3",
	})

	prometheusInstance = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInstance, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/prometheus:v1.2.3",
		*prometheusInstance.Spec.Image,
	)

	alertManagerInstance = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInstance, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/alertmanager:v1.2.3",
		*alertManagerInstance.Spec.Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 4)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
	require.Equal(t,
		customRepo+"/snapshot-controller:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[3].Image,
	)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.Containers, 1)
	require.Equal(t,
		customRepo+"/"+cluster.Spec.PxRepo.Image,
		pxRepoDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)
}

func TestCompleteInstallWithCustomRepoRegistryChangeForK8s_1_14(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:               "portworx/image:2.2",
			CustomImageRegistry: customRepo,
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// Case: Custom repo-registry should be added to the images
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Case: Updated repo-registry should be added to the images
	customRepo = "test-registry:1111/new-repo"
	cluster.Spec.CustomImageRegistry = customRepo

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Case: Flat registry should be used for images
	customRepo = "test-registry:1111"
	cluster.Spec.CustomImageRegistry = customRepo + "//"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Case: If empty, remove custom repo-registry from images
	cluster.Spec.CustomImageRegistry = ""

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		"quay.io/k8scsi/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Case: Custom repo-registry should be added back in not present in images
	customRepo = "test-registry:1111/newest-repo"
	cluster.Spec.CustomImageRegistry = customRepo

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.14.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-resizer:v1.2.3",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)
}

func TestCompleteInstallWithCustomRepoRegistryChangeForK8s_1_12(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:               "portworx/image:2.9.1",
			CustomImageRegistry: customRepo,
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-uid",
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// Case: Custom repo-registry should be added to the images
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	collectorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Updated repo-registry should be added to the images
	customRepo = "test-registry:1111/new-repo"
	cluster.Spec.CustomImageRegistry = customRepo

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Flat registry should be used for images
	customRepo = "test-registry:1111"
	cluster.Spec.CustomImageRegistry = customRepo + "//"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: If empty, remove custom repo-registry from images
	cluster.Spec.CustomImageRegistry = ""

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		"quay.io/k8scsi/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		"quay.io/k8scsi/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		"docker.io/purestorage/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		"docker.io/envoyproxy/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)

	// Case: Custom repo-registry should be added back in not present in images
	customRepo = "test-registry:1111/newest-repo"
	cluster.Spec.CustomImageRegistry = customRepo

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.12.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiStatefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, csiStatefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiStatefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-attacher:v1.2.3",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.Containers, 2)
	require.Equal(t,
		customRepo+"/realtime-metrics:latest",
		collectorDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/envoy:v1.19.1",
		collectorDeployment.Spec.Template.Spec.Containers[1].Image,
	)
}

func TestCompleteInstallWithImagePullSecretChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	imagePullSecret := "pull-secret"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullSecret: &imagePullSecret,
			StartPort:       &startPort,
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
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			PxRepo: &corev1.PxRepoSpec{
				Enabled: true,
				Image:   "pxRepoImage",
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-uid",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
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

	// Case: Image pull secret should be applied to the deployment
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pxProxyDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Len(t, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pvcDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pvcDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, lhDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, lhDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	prometheusInst := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, prometheusInst.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, prometheusInst.Spec.ImagePullSecrets[0].Name)

	alertManagerInst := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, alertManagerInst.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, alertManagerInst.Spec.ImagePullSecrets[0].Name)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, csiDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pxRepoDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	collectorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, collectorDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	// Case: Updated image pull secet should be applied to the deployment
	imagePullSecret = "new-secret"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Len(t, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pvcDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pvcDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, lhDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, lhDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, prometheusInst.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, prometheusInst.Spec.ImagePullSecrets[0].Name)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, alertManagerInst.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, alertManagerInst.Spec.ImagePullSecrets[0].Name)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, csiDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, collectorDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	// Case: If empty, remove image pull secret from the deployment
	imagePullSecret = ""
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Empty(t, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pvcDeployment.Spec.Template.Spec.ImagePullSecrets)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, lhDeployment.Spec.Template.Spec.ImagePullSecrets)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, prometheusInst.Spec.ImagePullSecrets)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, alertManagerInst.Spec.ImagePullSecrets)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, csiDeployment.Spec.Template.Spec.ImagePullSecrets)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets)

	// Case: If nil, remove image pull secret from the deployment
	cluster.Spec.ImagePullSecret = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Empty(t, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pvcDeployment.Spec.Template.Spec.ImagePullSecrets)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, lhDeployment.Spec.Template.Spec.ImagePullSecrets)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, prometheusInst.Spec.ImagePullSecrets)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, alertManagerInst.Spec.ImagePullSecrets)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, csiDeployment.Spec.Template.Spec.ImagePullSecrets)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets)

	// Case: Image pull secret should be added back if not present in deployment
	imagePullSecret = "pull-secret"
	cluster.Spec.ImagePullSecret = &imagePullSecret
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Len(t, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxProxyDaemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pvcDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pvcDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, lhDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, lhDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, autopilotDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, prometheusInst.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, prometheusInst.Spec.ImagePullSecrets[0].Name)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, alertManagerInst.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, alertManagerInst.Spec.ImagePullSecrets[0].Name)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, csiDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, pxRepoDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, collectorDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, collectorDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)
}

func TestCompleteInstallWithTolerationsChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	tolerations := []v1.Toleration{
		{
			Key:      "foo",
			Value:    "bar",
			Operator: v1.TolerationOpEqual,
			Effect:   v1.TaintEffectNoSchedule,
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:     "portworx/image:2.9.1",
			StartPort: &startPort,
			Placement: &corev1.PlacementSpec{
				Tolerations: tolerations,
			},
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
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			PxRepo: &corev1.PxRepoSpec{
				Enabled: true,
				Image:   "pxRepoImage",
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-uid",
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
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

	// Case: Tolerations should be applied to the deployment
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxAPIDaemonSet.Spec.Template.Spec.Tolerations)

	pxProxyDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxProxyDaemonSet.Spec.Template.Spec.Tolerations)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pvcDeployment.Spec.Template.Spec.Tolerations)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, lhDeployment.Spec.Template.Spec.Tolerations)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, autopilotDeployment.Spec.Template.Spec.Tolerations)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusOperatorDeployment.Spec.Template.Spec.Tolerations)

	prometheusInst := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusInst.Spec.Tolerations)

	alertManagerInst := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, alertManagerInst.Spec.Tolerations)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiDeployment.Spec.Template.Spec.Tolerations)

	pxRepoDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxRepoDeployment.Spec.Template.Spec.Tolerations)

	// Case: Updated tolerations should be applied to the deployment
	tolerations[0].Value = "baz"
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxAPIDaemonSet.Spec.Template.Spec.Tolerations)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxProxyDaemonSet.Spec.Template.Spec.Tolerations)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pvcDeployment.Spec.Template.Spec.Tolerations)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, lhDeployment.Spec.Template.Spec.Tolerations)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, autopilotDeployment.Spec.Template.Spec.Tolerations)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusOperatorDeployment.Spec.Template.Spec.Tolerations)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusInst.Spec.Tolerations)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, alertManagerInst.Spec.Tolerations)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiDeployment.Spec.Template.Spec.Tolerations)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxRepoDeployment.Spec.Template.Spec.Tolerations)

	collectorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, collectorDeployment.Spec.Template.Spec.Tolerations)

	// Case: New tolerations should be applied to the deployment
	tolerations = append(tolerations, v1.Toleration{
		Key:      "must-exist",
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoExecute,
	})
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxAPIDaemonSet.Spec.Template.Spec.Tolerations)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxProxyDaemonSet.Spec.Template.Spec.Tolerations)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pvcDeployment.Spec.Template.Spec.Tolerations)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, lhDeployment.Spec.Template.Spec.Tolerations)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, autopilotDeployment.Spec.Template.Spec.Tolerations)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusOperatorDeployment.Spec.Template.Spec.Tolerations)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusInst.Spec.Tolerations)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, alertManagerInst.Spec.Tolerations)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiDeployment.Spec.Template.Spec.Tolerations)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxRepoDeployment.Spec.Template.Spec.Tolerations)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, collectorDeployment.Spec.Template.Spec.Tolerations)

	// Case: Removed tolerations should be removed from the deployment
	tolerations = []v1.Toleration{tolerations[0]}
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxAPIDaemonSet.Spec.Template.Spec.Tolerations)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxProxyDaemonSet.Spec.Template.Spec.Tolerations)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pvcDeployment.Spec.Template.Spec.Tolerations)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, lhDeployment.Spec.Template.Spec.Tolerations)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, autopilotDeployment.Spec.Template.Spec.Tolerations)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusOperatorDeployment.Spec.Template.Spec.Tolerations)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusInst.Spec.Tolerations)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, alertManagerInst.Spec.Tolerations)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiDeployment.Spec.Template.Spec.Tolerations)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxRepoDeployment.Spec.Template.Spec.Tolerations)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, collectorDeployment.Spec.Template.Spec.Tolerations)

	// Case: If tolerations are empty, should be removed from the deployment
	cluster.Spec.Placement.Tolerations = []v1.Toleration{}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxAPIDaemonSet.Spec.Template.Spec.Tolerations)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Nil(t, pxProxyDaemonSet.Spec.Template.Spec.Tolerations)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pvcDeployment.Spec.Template.Spec.Tolerations)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, lhDeployment.Spec.Template.Spec.Tolerations)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, autopilotDeployment.Spec.Template.Spec.Tolerations)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusOperatorDeployment.Spec.Template.Spec.Tolerations)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusInst.Spec.Tolerations)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, alertManagerInst.Spec.Tolerations)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiDeployment.Spec.Template.Spec.Tolerations)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxRepoDeployment.Spec.Template.Spec.Tolerations)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, collectorDeployment.Spec.Template.Spec.Tolerations)

	// Case: Tolerations should be added back if not present in deployment
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxAPIDaemonSet.Spec.Template.Spec.Tolerations)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxProxyDaemonSet.Spec.Template.Spec.Tolerations)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pvcDeployment.Spec.Template.Spec.Tolerations)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, lhDeployment.Spec.Template.Spec.Tolerations)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, autopilotDeployment.Spec.Template.Spec.Tolerations)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusOperatorDeployment.Spec.Template.Spec.Tolerations)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, prometheusInst.Spec.Tolerations)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, alertManagerInst.Spec.Tolerations)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, csiDeployment.Spec.Template.Spec.Tolerations)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, pxRepoDeployment.Spec.Template.Spec.Tolerations)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, collectorDeployment.Spec.Template.Spec.Tolerations)

	// Case: If placement is empty, deployment should not have tolerations
	cluster.Spec.Placement = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxAPIDaemonSet.Spec.Template.Spec.Tolerations)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Nil(t, pxProxyDaemonSet.Spec.Template.Spec.Tolerations)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pvcDeployment.Spec.Template.Spec.Tolerations)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, lhDeployment.Spec.Template.Spec.Tolerations)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, autopilotDeployment.Spec.Template.Spec.Tolerations)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusOperatorDeployment.Spec.Template.Spec.Tolerations)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusInst.Spec.Tolerations)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, alertManagerInst.Spec.Tolerations)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiDeployment.Spec.Template.Spec.Tolerations)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxRepoDeployment.Spec.Template.Spec.Tolerations)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, collectorDeployment.Spec.Template.Spec.Tolerations)
}

func TestCompleteInstallWithNodeAffinityChange(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	nodeAffinity := &v1.NodeAffinity{
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
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:     "portworx/image:2.9.1",
			StartPort: &startPort,
			Placement: &corev1.PlacementSpec{
				NodeAffinity: nodeAffinity,
			},
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
					Enabled: true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			PxRepo: &corev1.PxRepoSpec{
				Enabled: true,
				Image:   "pxRepoImage",
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-uid",
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
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

	// Case: Node affinity should be applied to the deployment
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxAPIDaemonSet.Spec.Template.Spec.Affinity.NodeAffinity)

	pxProxyDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxProxyDaemonSet.Spec.Template.Spec.Affinity.NodeAffinity)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pvcDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, lhDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, autopilotDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, prometheusOperatorDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusInst := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, prometheusInst.Spec.Affinity.NodeAffinity)

	alertManagerInst := &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, alertManagerInst.Spec.Affinity.NodeAffinity)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, csiDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	pxRepoDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxRepoDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	collectorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, collectorDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: Updated node affinity should be applied to the deployment
	nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
		NodeSelectorTerms[0].
		MatchExpressions[0].
		Key = "px/disabled"
	cluster.Spec.Placement.NodeAffinity = nodeAffinity
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxAPIDaemonSet.Spec.Template.Spec.Affinity.NodeAffinity)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxProxyDaemonSet.Spec.Template.Spec.Affinity.NodeAffinity)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pvcDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, lhDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, autopilotDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, prometheusOperatorDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, prometheusInst.Spec.Affinity.NodeAffinity)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, alertManagerInst.Spec.Affinity.NodeAffinity)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, csiDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxRepoDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, collectorDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: If node affinity is removed, it should be removed from the deployment
	cluster.Spec.Placement.NodeAffinity = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxAPIDaemonSet.Spec.Template.Spec.Affinity)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Nil(t, pxProxyDaemonSet.Spec.Template.Spec.Affinity)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pvcDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, lhDeployment.Spec.Template.Spec.Affinity)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, autopilotDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusOperatorDeployment.Spec.Template.Spec.Affinity)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusInst.Spec.Affinity)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, alertManagerInst.Spec.Affinity)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiDeployment.Spec.Template.Spec.Affinity)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxRepoDeployment.Spec.Template.Spec.Affinity)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, collectorDeployment.Spec.Template.Spec.Affinity)

	// Case: Node affinity should be added back if not present in deployment
	cluster.Spec.Placement.NodeAffinity = nodeAffinity
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxAPIDaemonSet.Spec.Template.Spec.Affinity.NodeAffinity)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxProxyDaemonSet.Spec.Template.Spec.Affinity.NodeAffinity)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pvcDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, lhDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, autopilotDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, prometheusOperatorDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, prometheusInst.Spec.Affinity.NodeAffinity)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, alertManagerInst.Spec.Affinity.NodeAffinity)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, csiDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, pxRepoDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: If placement is nil, node affinity should be removed from the deployment
	cluster.Spec.Placement = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxAPIDaemonSet.Spec.Template.Spec.Affinity)

	pxProxyDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxProxyDaemonSet, component.PxProxyDaemonSetName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Nil(t, pxProxyDaemonSet.Spec.Template.Spec.Affinity)

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pvcDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	lhDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, lhDeployment.Spec.Template.Spec.Affinity)

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, autopilotDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	prometheusOperatorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusOperatorDeployment.Spec.Template.Spec.Affinity)

	prometheusInst = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheusInst, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, prometheusInst.Spec.Affinity)

	alertManagerInst = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManagerInst, component.AlertManagerInstanceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, alertManagerInst.Spec.Affinity)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, csiDeployment.Spec.Template.Spec.Affinity)

	pxRepoDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pxRepoDeployment, component.PxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxRepoDeployment.Spec.Template.Spec.Affinity)

	collectorDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, collectorDeployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, collectorDeployment.Spec.Template.Spec.Affinity)
}

func TestRemovePVCController(t *testing.T) {
	// Set fake kubernetes client for k8s version
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Remove PVC Controller
	delete(cluster.Annotations, pxutil.AnnotationPVCController)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDisablePVCController(t *testing.T) {
	// Set fake kubernetes client for k8s version
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Disable PVC Controller
	cluster.Annotations[pxutil.AnnotationPVCController] = "false"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PVCClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PVCClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestRemoveLighthouse(t *testing.T) {
	// Set fake kubernetes client for k8s version
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.LhServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.LhClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.LhClusterRoleBindingName, "")
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Remove lighthouse config
	cluster.Spec.UserInterface = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.LhServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.LhClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.LhClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.LhServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.LhDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDisableLighthouse(t *testing.T) {
	// Set fake kubernetes client for k8s version
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
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.LhServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.LhClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.LhClusterRoleBindingName, "")
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Disable lighthouse
	cluster.Spec.UserInterface.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.LhServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.LhClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.LhClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.LhServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.LhDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestRemoveAutopilot(t *testing.T) {
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	cm := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, cm, component.AutopilotConfigMapName, cluster.Namespace)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.AutopilotServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.AutopilotClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.AutopilotClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Remove autopilot config
	cluster.Spec.Autopilot = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	cm = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, cm, component.AutopilotConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.AutopilotServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.AutopilotClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.AutopilotClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDisableAutopilot(t *testing.T) {
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
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
		},
	}

	err = driver.PreInstall(cluster)

	require.NoError(t, err)

	cm := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, cm, component.AutopilotConfigMapName, cluster.Namespace)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.AutopilotServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.AutopilotClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.AutopilotClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Disable Autopilot
	cluster.Spec.Autopilot.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	cm = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, cm, component.AutopilotConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.AutopilotServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.AutopilotClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.AutopilotClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDisableCSI_0_3(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.11.4",
	}
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
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.CSIClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.CSIClusterRoleBindingName, "")
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.CSIServiceName, cluster.Namespace)
	require.NoError(t, err)

	statefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)

	// Disable CSI
	cluster.Spec.CSI.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.CSIClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.CSIClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.CSIServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Remove CSI flag. Default should be disabled.
	delete(cluster.Spec.FeatureGates, string(pxutil.FeatureCSI))
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.CSIClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.CSIClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.CSIServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDisableCSI_1_0(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
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
			Image: "portworx/image:2.5.0",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.CSIClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.CSIClusterRoleBindingName, "")
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.CSIServiceName, cluster.Namespace)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)

	csiDriver := &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.NoError(t, err)

	// Disable CSI
	cluster.Spec.CSI.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.CSIClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.CSIClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.CSIServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.True(t, errors.IsNotFound(err))

	// Remove CSI flag. Default should be disabled.
	delete(cluster.Spec.FeatureGates, string(pxutil.FeatureCSI))
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.CSIClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.CSIClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.CSIServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestMonitoringMetricsEnabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

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
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: true,
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
			Kvdb: &corev1.KvdbSpec{
				Internal: false,
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// ServiceMonitor for Prometheus
	serviceMonitorList := &monitoringv1.ServiceMonitorList{}
	err = testutil.List(k8sClient, serviceMonitorList)
	require.NoError(t, err)
	require.Len(t, serviceMonitorList.Items, 1)

	expectedServiceMonitor := testutil.GetExpectedServiceMonitor(t, "prometheusServiceMonitor.yaml")
	serviceMonitor := serviceMonitorList.Items[0]
	require.Equal(t, expectedServiceMonitor.Name, serviceMonitor.Name)
	require.Equal(t, expectedServiceMonitor.Namespace, serviceMonitor.Namespace)
	require.Equal(t, expectedServiceMonitor.Labels, serviceMonitor.Labels)
	require.Len(t, serviceMonitor.OwnerReferences, 1)
	require.Equal(t, cluster.Name, serviceMonitor.OwnerReferences[0].Name)
	require.Equal(t, expectedServiceMonitor.Spec, serviceMonitor.Spec)

	// PrometheusRule for Prometheus
	ruleList := &monitoringv1.PrometheusRuleList{}
	err = testutil.List(k8sClient, ruleList)
	require.NoError(t, err)
	require.Len(t, ruleList.Items, 1)

	expectedPrometheusRule := testutil.GetExpectedPrometheusRule(t, "prometheusRule.yaml")
	prometheusRule := ruleList.Items[0]
	require.Equal(t, expectedPrometheusRule.Name, prometheusRule.Name)
	require.Equal(t, expectedPrometheusRule.Namespace, prometheusRule.Namespace)
	require.Len(t, prometheusRule.OwnerReferences, 1)
	require.Equal(t, cluster.Name, prometheusRule.OwnerReferences[0].Name)
	require.Equal(t, expectedPrometheusRule.Spec, prometheusRule.Spec)

	// ServiceMonitor with internal kvdb
	cluster.Spec.Kvdb.Internal = true

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedServiceMonitor = testutil.GetExpectedServiceMonitor(t, "prometheusServiceMonitorWithInternalKvdb.yaml")
	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, component.PxServiceMonitor, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceMonitor.Name, serviceMonitor.Name)
	require.Equal(t, expectedServiceMonitor.Namespace, serviceMonitor.Namespace)
	require.Equal(t, expectedServiceMonitor.Labels, serviceMonitor.Labels)
	require.Len(t, serviceMonitor.OwnerReferences, 1)
	require.Equal(t, cluster.Name, serviceMonitor.OwnerReferences[0].Name)
	require.Equal(t, expectedServiceMonitor.Spec, serviceMonitor.Spec)

	// ServiceMonitor when kvdb spec is empty
	cluster.Spec.Kvdb = nil

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, component.PxServiceMonitor, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceMonitor.Name, serviceMonitor.Name)
	require.Equal(t, expectedServiceMonitor.Namespace, serviceMonitor.Namespace)
	require.Len(t, serviceMonitor.OwnerReferences, 1)
	require.Equal(t, cluster.Name, serviceMonitor.OwnerReferences[0].Name)
	require.Equal(t, expectedServiceMonitor.Spec, serviceMonitor.Spec)
}

func TestDisableMonitoring(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

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
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: true,
				},
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sm := &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, sm, component.PxServiceMonitor, cluster.Namespace)
	require.NoError(t, err)

	pr := &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, pr, component.PxPrometheusRule, cluster.Namespace)
	require.NoError(t, err)

	// Disable metrics monitoring
	cluster.Spec.Monitoring.Prometheus.ExportMetrics = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sm = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, sm, component.PxServiceMonitor, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	pr = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, pr, component.PxPrometheusRule, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Remove monitoring spec. Default should be disabled.
	cluster.Spec.Monitoring = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sm = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, sm, component.PxServiceMonitor, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	pr = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, pr, component.PxPrometheusRule, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestRemovePrometheus(t *testing.T) {
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
					Enabled: true,
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusOperatorServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusOperatorClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusOperatorClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusClusterRoleName, "")
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusClusterRoleBindingName, "")
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.PrometheusServiceName, cluster.Namespace)
	require.NoError(t, err)

	prom := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prom, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)

	// Remove prometheus config
	cluster.Spec.Monitoring.Prometheus = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusOperatorServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusOperatorClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusOperatorClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.PrometheusServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	prom = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prom, component.PrometheusInstanceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDisablePrometheus(t *testing.T) {
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
					Enabled: true,
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusOperatorServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusOperatorClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusOperatorClusterRoleBindingName, "")
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusClusterRoleName, "")
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusClusterRoleBindingName, "")
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, component.PrometheusServiceName, cluster.Namespace)
	require.NoError(t, err)

	prom := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prom, component.PrometheusInstanceName, cluster.Namespace)
	require.NoError(t, err)

	// Disable prometheus
	cluster.Spec.Monitoring.Prometheus.Enabled = false
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusOperatorServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusOperatorClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusOperatorClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PrometheusServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	cr = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PrometheusClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PrometheusClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, component.PrometheusServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	prom = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prom, component.PrometheusInstanceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestPodDisruptionBudgetEnabled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node4"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node5"}},
	}}
	expectedNodeEnumerateResp := &osdapi.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*osdapi.StorageNode{
			{SchedulerNodeName: "node1"},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
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
	}, fakeK8sNodes)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	component.DeregisterAllComponents()
	component.RegisterDisruptionBudgetComponent()

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	// TestCase: Do not create KVDB PDB if not using internal KVDB
	cluster.Spec.Kvdb = &corev1.KvdbSpec{
		Internal: false,
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList := &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Empty(t, pdbList.Items)

	// TestCase: Create KVDB PDB when using internal KVDB
	cluster.Spec.Kvdb.Internal = true

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Len(t, pdbList.Items, 1)

	require.Equal(t, component.KVDBPodDisruptionBudgetName, pdbList.Items[0].Name)
	require.Equal(t, 2, pdbList.Items[0].Spec.MinAvailable.IntValue())
	require.Len(t, pdbList.Items[0].Spec.Selector.MatchLabels, 2)
	require.Equal(t, cluster.Name, pdbList.Items[0].Spec.Selector.MatchLabels[constants.LabelKeyClusterName])
	require.Equal(t, constants.LabelValueTrue, pdbList.Items[0].Spec.Selector.MatchLabels[constants.LabelKeyKVDBPod])

	// TestCase: Do not create storage PDB if total nodes with storage is less than 3
	expectedNodeEnumerateResp.Nodes = []*osdapi.StorageNode{
		{Pools: []*osdapi.StoragePool{{ID: 1}}, SchedulerNodeName: "node1"},
		{Pools: []*osdapi.StoragePool{{ID: 2}}, SchedulerNodeName: "node2"},
		{Pools: []*osdapi.StoragePool{}},
		{},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storagePDB := &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, storagePDB, component.StoragePodDisruptionBudgetName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// TestCase: Create storage PDB if total nodes with storage is at least 3
	expectedNodeEnumerateResp.Nodes = []*osdapi.StorageNode{
		{Pools: []*osdapi.StoragePool{{ID: 1}}, SchedulerNodeName: "node1"},
		{Pools: []*osdapi.StoragePool{{ID: 2}}, SchedulerNodeName: "node2"},
		{Pools: []*osdapi.StoragePool{}},
		{},
		{Pools: []*osdapi.StoragePool{{ID: 3}}, SchedulerNodeName: "node3"},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storagePDB = &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, storagePDB, component.StoragePodDisruptionBudgetName, cluster.Namespace)
	require.NoError(t, err)

	require.Equal(t, 2, storagePDB.Spec.MinAvailable.IntValue())
	require.Len(t, storagePDB.Spec.Selector.MatchLabels, 2)
	require.Equal(t, cluster.Name, storagePDB.Spec.Selector.MatchLabels[constants.LabelKeyClusterName])
	require.Equal(t, constants.LabelValueTrue, storagePDB.Spec.Selector.MatchLabels[constants.LabelKeyStoragePod])

	// TestCase: Update storage PDB if count of nodes with storage changes
	expectedNodeEnumerateResp.Nodes = []*osdapi.StorageNode{
		{Pools: []*osdapi.StoragePool{{ID: 1}}, SchedulerNodeName: "node1"},
		{Pools: []*osdapi.StoragePool{{ID: 2}}, SchedulerNodeName: "node2"},
		{Pools: []*osdapi.StoragePool{{ID: 3}}, SchedulerNodeName: "node3"},
		{Pools: []*osdapi.StoragePool{{ID: 4}}, SchedulerNodeName: "node4"},
		{Pools: []*osdapi.StoragePool{{ID: 5}}, SchedulerNodeName: "node5"},
		{Pools: []*osdapi.StoragePool{}},
		{},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storagePDB = &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, storagePDB, component.StoragePodDisruptionBudgetName, cluster.Namespace)
	require.NoError(t, err)

	require.Equal(t, 4, storagePDB.Spec.MinAvailable.IntValue())
}

func TestPodDisruptionBudgetWithMetroDR(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node3"},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{Type: v1.NodeHostName, Address: "node3"},
				},
			}},
		{ObjectMeta: metav1.ObjectMeta{
			Name: "node4",
			Labels: map[string]string{
				"px/enabled": "false",
			},
		}},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node5"},
			Spec: v1.NodeSpec{
				Taints: []v1.Taint{
					{Effect: v1.TaintEffectNoExecute},
				},
			},
		},
	}}
	// StorageNode with ID 1,2,3 will run on the cluster
	// k8s node4 doesn't meet affinity and node5 is tainted
	// StorageNode with ID 6 and 7 don't belong to this cluster
	expectedNodeEnumerateResp := &osdapi.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*osdapi.StorageNode{
			{Pools: []*osdapi.StoragePool{{ID: 1}}, SchedulerNodeName: "node1"},
			{Pools: []*osdapi.StoragePool{{ID: 2}}, SchedulerNodeName: "node2"},
			{Pools: []*osdapi.StoragePool{{ID: 3}}, SchedulerNodeName: "", Hostname: "node3"},
			{Pools: []*osdapi.StoragePool{{ID: 4}}, SchedulerNodeName: "node4"},
			{Pools: []*osdapi.StoragePool{{ID: 5}}, SchedulerNodeName: "node5"},
			{Pools: []*osdapi.StoragePool{{ID: 6}}, SchedulerNodeName: "metro-dr-node1"},
			{Pools: []*osdapi.StoragePool{{ID: 7}}, SchedulerNodeName: "metro-dr-node2"},
			{Pools: []*osdapi.StoragePool{}},
			{},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

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
								},
							},
						},
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
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
	}, fakeK8sNodes)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	component.DeregisterAllComponents()
	component.RegisterDisruptionBudgetComponent()

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storagePDB := &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, storagePDB, component.StoragePodDisruptionBudgetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, 2, storagePDB.Spec.MinAvailable.IntValue())
}

func TestPodDisruptionBudgetWithDifferentKvdbClusterSize(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

	expectedNodeEnumerateResp := &osdapi.SdkNodeEnumerateWithFiltersResponse{}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
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

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	component.DeregisterAllComponents()
	component.RegisterDisruptionBudgetComponent()

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	// TestCase: Create KVDB PDB without any misc args
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList := &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Len(t, pdbList.Items, 1)

	require.Equal(t, component.KVDBPodDisruptionBudgetName, pdbList.Items[0].Name)
	require.Equal(t, component.DefaultKVDBClusterSize-1, pdbList.Items[0].Spec.MinAvailable.IntValue())

	// TestCase: Create KVDB PDB with empty misc args
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "",
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Len(t, pdbList.Items, 1)

	require.Equal(t, component.KVDBPodDisruptionBudgetName, pdbList.Items[0].Name)
	require.Equal(t, component.DefaultKVDBClusterSize-1, pdbList.Items[0].Spec.MinAvailable.IntValue())

	// TestCase: Create KVDB PDB with misc args but without kvdb_cluster_size arg
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "-key value"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Len(t, pdbList.Items, 1)

	require.Equal(t, component.KVDBPodDisruptionBudgetName, pdbList.Items[0].Name)
	require.Equal(t, component.DefaultKVDBClusterSize-1, pdbList.Items[0].Spec.MinAvailable.IntValue())

	// TestCase: Create KVDB PDB with kvdb_cluster_size arg
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "-kvdb_cluster_size 5"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Len(t, pdbList.Items, 1)

	require.Equal(t, component.KVDBPodDisruptionBudgetName, pdbList.Items[0].Name)
	require.Equal(t, 4, pdbList.Items[0].Spec.MinAvailable.IntValue())

	// TestCase: Create KVDB PDB with invalid kvdb_cluster_size arg
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "-kvdb_cluster_size invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Len(t, pdbList.Items, 1)

	require.Equal(t, component.KVDBPodDisruptionBudgetName, pdbList.Items[0].Name)
	require.Equal(t, component.DefaultKVDBClusterSize-1, pdbList.Items[0].Spec.MinAvailable.IntValue())
}

func TestPodDisruptionBudgetDuringInitialization(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}},
	}}
	expectedNodeEnumerateResp := &osdapi.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*osdapi.StorageNode{
			{Pools: []*osdapi.StoragePool{{ID: 1}}, SchedulerNodeName: "node1"},
			{Pools: []*osdapi.StoragePool{{ID: 2}}, SchedulerNodeName: "node2"},
			{Pools: []*osdapi.StoragePool{{ID: 3}}, SchedulerNodeName: "node3"},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
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
	}, fakeK8sNodes)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	component.DeregisterAllComponents()
	component.RegisterDisruptionBudgetComponent()

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	// TestCase: Do not create PDBs if the cluster status is empty
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList := &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Empty(t, pdbList.Items)

	// TestCase: Do not create PDB if the cluster is initializing
	cluster.Status.Phase = string(corev1.ClusterStateInit)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Empty(t, pdbList.Items)

	// TestCase: Create PDBs when cluster is done initializing
	cluster.Status.Phase = string(corev1.ClusterStateRunning)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Len(t, pdbList.Items, 2)
}

func TestPodDisruptionBudgetWithErrors(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: false,
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}

	pxService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	component.DeregisterAllComponents()
	component.RegisterDisruptionBudgetComponent()

	driver := portworx{}
	k8sClient := testutil.FakeK8sClient(pxService)
	recorder := record.NewFakeRecorder(1)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	// TestCase: Error creating grpc connection
	// (due to missing endpoint)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup %v. failed to get endpoint", v1.EventTypeWarning,
			util.FailedComponentReason, component.DisruptionBudgetComponentName))

	pdbList := &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Empty(t, pdbList.Items)

	// TestCase: Error setting up context when using px-security
	// (system secret not present)
	pxService.Spec.ClusterIP = sdkServerIP
	err = k8sClient.Update(context.TODO(), pxService)
	require.NoError(t, err)

	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup %v. failed to get portworx apps secret",
			v1.EventTypeWarning, util.FailedComponentReason, component.DisruptionBudgetComponentName))

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Empty(t, pdbList.Items)

	// TestCase: Error enumerating nodes using SDK
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("NodeEnumerate error")).
		Times(1)
	cluster.Spec.Security.Enabled = false

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup %v. failed to enumerate nodes",
			v1.EventTypeWarning, util.FailedComponentReason, component.DisruptionBudgetComponentName))

	pdbList = &policyv1.PodDisruptionBudgetList{}
	err = testutil.List(k8sClient, pdbList)
	require.NoError(t, err)
	require.Empty(t, pdbList.Items)
}

func TestDisablePodDisruptionBudgets(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}},
	}}
	expectedNodeEnumerateResp := &osdapi.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*osdapi.StorageNode{
			{Pools: []*osdapi.StoragePool{{ID: 1}}, SchedulerNodeName: "node1"},
			{Pools: []*osdapi.StoragePool{{ID: 2}}, SchedulerNodeName: "node2"},
			{Pools: []*osdapi.StoragePool{{ID: 3}}, SchedulerNodeName: "node3"},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPodDisruptionBudget: "true",
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
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
	}, fakeK8sNodes)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	component.DeregisterAllComponents()
	component.RegisterDisruptionBudgetComponent()

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storagePDB := &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, storagePDB, component.StoragePodDisruptionBudgetName, cluster.Namespace)
	require.NoError(t, err)

	kvdbPDB := &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, kvdbPDB, component.KVDBPodDisruptionBudgetName, cluster.Namespace)
	require.NoError(t, err)

	// Removing the annotation will not disable pod disruption budgets,
	// as they are enabled by default
	cluster.Annotations = map[string]string{}
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storagePDB = &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, storagePDB, component.StoragePodDisruptionBudgetName, cluster.Namespace)
	require.NoError(t, err)

	kvdbPDB = &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, kvdbPDB, component.KVDBPodDisruptionBudgetName, cluster.Namespace)
	require.NoError(t, err)

	// Disable pod disruption budgets
	cluster.Annotations[pxutil.AnnotationPodDisruptionBudget] = "false"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	storagePDB = &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, storagePDB, component.StoragePodDisruptionBudgetName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	kvdbPDB = &policyv1.PodDisruptionBudget{}
	err = testutil.Get(k8sClient, kvdbPDB, component.KVDBPodDisruptionBudgetName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestSCC(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// Install with no SCC
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedSCC := testutil.GetExpectedSCC(t, "portworxSCC.yaml")
	scc := &ocp_secv1.SecurityContextConstraints{}
	err = testutil.Get(k8sClient, scc, expectedSCC.Name, "")
	require.NotNil(t, err)

	expectedPxRestrictedSCC := testutil.GetExpectedSCC(t, "portworxRestrictedSCC.yaml")
	pxRestrictedSCC := &ocp_secv1.SecurityContextConstraints{}
	err = testutil.Get(k8sClient, pxRestrictedSCC, expectedPxRestrictedSCC.Name, "")
	require.NotNil(t, err)

	// Install with SCC enabled
	crd := testutil.GetExpectedCRDV1(t, "sccCrd.yaml")
	err = k8sClient.Create(context.TODO(), crd)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, scc, expectedSCC.Name, "")
	require.NoError(t, err)
	require.Equal(t, expectedSCC, scc)

	err = testutil.Get(k8sClient, pxRestrictedSCC, expectedPxRestrictedSCC.Name, "")
	require.NoError(t, err)
	require.Equal(t, expectedSCC, scc)

	// Update SCC
	scc.AllowHostNetwork = false
	err = k8sClient.Update(context.TODO(), scc)
	require.NoError(t, err)

	pxRestrictedSCC.AllowHostNetwork = true
	err = k8sClient.Update(context.TODO(), pxRestrictedSCC)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, scc, expectedSCC.Name, "")
	require.NoError(t, err)

	err = testutil.Get(k8sClient, pxRestrictedSCC, expectedPxRestrictedSCC.Name, "")
	require.NoError(t, err)

	// Update SCC priority
	cluster.Annotations[pxutil.AnnotationSCCPriority] = "2"
	err = k8sClient.Update(context.TODO(), scc)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, scc, expectedSCC.Name, "")
	require.NoError(t, err)
	require.Equal(t, *scc.Priority, int32(2))
}

func TestPodSecurityPoliciesEnabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPodSecurityPolicy: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
			FeatureGates: map[string]string{
				"CSI": "T",
			},
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
		},
	}

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// check that podsecuritpolicies have been created
	expectedPSPs := []*policyv1beta1.PodSecurityPolicy{
		testutil.GetExpectedPSP(t, "privilegedPSP.yaml"),
		testutil.GetExpectedPSP(t, "restrictedPSP.yaml"),
	}
	expectedOwnerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	for _, expectedPolicy := range expectedPSPs {
		policy := &policyv1beta1.PodSecurityPolicy{}
		err = testutil.Get(k8sClient, policy, expectedPolicy.Name, "")
		require.NoError(t, err)

		require.Equalf(t, metav1.GetControllerOf(policy), expectedOwnerRef,
			"check owner reference for %s podsecuritypolicy", expectedPolicy.Name)
		require.Equalf(t, expectedPolicy.Spec, policy.Spec, "check psp spec for %s", expectedPolicy.Name)
	}

	// check each px role/clusterrole has a podsecuritypolicy assigned
	expectedClusterRoles := []struct {
		clusterRoleName string
		pspName         string
	}{
		{
			clusterRoleName: component.PxClusterRoleName,
			pspName:         constants.PrivilegedPSPName,
		},
		{
			clusterRoleName: component.CSIClusterRoleName,
			pspName:         constants.PrivilegedPSPName,
		},
		{
			clusterRoleName: component.PVCClusterRoleName,
			pspName:         constants.PrivilegedPSPName,
		},
		{
			clusterRoleName: component.LhClusterRoleName,
			pspName:         constants.PrivilegedPSPName,
		},
		{
			clusterRoleName: component.AutopilotClusterRoleName,
			pspName:         constants.RestrictedPSPName,
		},
		{
			clusterRoleName: component.PrometheusClusterRoleName,
			pspName:         constants.RestrictedPSPName,
		},
		{
			clusterRoleName: component.PrometheusOperatorClusterRoleName,
			pspName:         constants.RestrictedPSPName,
		},
		// stork
		// stork-scheduler
	}

	containsPolicyRule := func(rules []rbacv1.PolicyRule, verb, apiGroup, resource, resourceName string) bool {
		for _, rule := range rules {
			if contains(rule.Verbs, verb) &&
				contains(rule.APIGroups, apiGroup) &&
				contains(rule.Resources, resource) &&
				contains(rule.ResourceNames, resourceName) {
				return true
			}
		}
		return false
	}

	for _, expected := range expectedClusterRoles {
		clusterRole := &rbacv1.ClusterRole{}
		err = testutil.Get(k8sClient, clusterRole, expected.clusterRoleName, "")
		require.NoError(t, err)

		require.Truef(t, containsPolicyRule(clusterRole.Rules, "use", "policy", "podsecuritypolicies", expected.pspName),
			"check %s cluster role: podsecuritypolicy: expected=%s, got %v", expected.clusterRoleName, expected.pspName, clusterRole)
	}
}

func TestRemovePodSecurityPolicies(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPodSecurityPolicy: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
			FeatureGates: map[string]string{
				"CSI": "T",
			},
		},
	}

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// check that podsecuritpolicies have been created
	expectedPSPs := []*policyv1beta1.PodSecurityPolicy{
		testutil.GetExpectedPSP(t, "privilegedPSP.yaml"),
		testutil.GetExpectedPSP(t, "restrictedPSP.yaml"),
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	for _, expectedPolicy := range expectedPSPs {
		policy := &policyv1beta1.PodSecurityPolicy{}
		err = testutil.Get(k8sClient, policy, expectedPolicy.Name, "")
		require.NoError(t, err)
	}

	// Removing the annotation will disable pod security policies
	// as they are disabled by default
	cluster.Annotations = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	policies := &policyv1beta1.PodSecurityPolicyList{}
	err = testutil.List(k8sClient, policies)
	require.NoError(t, err)
	require.Empty(t, len(policies.Items))

	// Re-enable PSP
	cluster.Annotations = map[string]string{
		pxutil.AnnotationPodSecurityPolicy: "true",
	}
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	for _, expectedPolicy := range expectedPSPs {
		policy := &policyv1beta1.PodSecurityPolicy{}
		err = testutil.Get(k8sClient, policy, expectedPolicy.Name, "")
		require.NoError(t, err)
	}

	// Upgrade to k8s 1.25 PSP will be removed
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.20.0",
	}
	cluster.Annotations = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	policies = &policyv1beta1.PodSecurityPolicyList{}
	err = testutil.List(k8sClient, policies)
	require.NoError(t, err)
	require.Empty(t, len(policies.Items))
}

func TestDisablePodSecurityPolicies(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPodSecurityPolicy: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled: true,
				},
				Telemetry: &corev1.TelemetrySpec{},
			},
			FeatureGates: map[string]string{
				"CSI": "T",
			},
		},
	}

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// check that podsecuritpolicies have been created
	expectedPSPs := []*policyv1beta1.PodSecurityPolicy{
		testutil.GetExpectedPSP(t, "privilegedPSP.yaml"),
		testutil.GetExpectedPSP(t, "restrictedPSP.yaml"),
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	for _, expectedPolicy := range expectedPSPs {
		policy := &policyv1beta1.PodSecurityPolicy{}
		err = testutil.Get(k8sClient, policy, expectedPolicy.Name, "")
		require.NoError(t, err)
	}

	// Removing the annotation will disable pod security policies
	// as they are disabled by default
	cluster.Annotations[pxutil.AnnotationPodSecurityPolicy] = "false"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	policies := &policyv1beta1.PodSecurityPolicyList{}
	err = testutil.List(k8sClient, policies)
	require.NoError(t, err)
	require.Empty(t, len(policies.Items))
}

func TestTelemetryEnableAndDisable(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationTelemetryArcusLocation: "internal",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.9.1",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},

		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotEmpty(t, cluster.Status.DesiredImages.Telemetry)
	require.NotEmpty(t, cluster.Status.DesiredImages.MetricsCollector)
	require.NotEmpty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.Empty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.Empty(t, cluster.Status.DesiredImages.LogUploader)

	// CCM config map
	expectedConfigMap := testutil.GetExpectedConfigMap(t, "ccmconfig.yaml")
	telemetryConfigMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, telemetryConfigMap, component.TelemetryConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, telemetryConfigMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, telemetryConfigMap.Namespace)
	require.Len(t, telemetryConfigMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, telemetryConfigMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, telemetryConfigMap.Data)

	// This cert is created by ccm container outside of operator, let's simulate it.
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// without location
	delete(cluster.Annotations, "portworx.io/arcus-location")
	expectedConfigMap.Data["location"] = "external"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	telemetryConfigMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, telemetryConfigMap, component.TelemetryConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, telemetryConfigMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, telemetryConfigMap.Namespace)
	require.Len(t, telemetryConfigMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, telemetryConfigMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, telemetryConfigMap.Data)

	// Proxy config map
	configMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.CollectorProxyConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)

	// Collector config map
	expectedConfigMap = testutil.GetExpectedConfigMap(t, "metricsCollectorConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.CollectorConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// Collector role
	role := &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, component.CollectorRoleName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, role.OwnerReferences, 1)
	require.Equal(t, cluster.Name, role.OwnerReferences[0].Name)

	// Collector role binding
	roleBinding := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, component.CollectorRoleBindingName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, roleBinding.OwnerReferences, 1)
	require.Equal(t, cluster.Name, roleBinding.OwnerReferences[0].Name)

	// Collector cluster role
	expectedClusterRole := testutil.GetExpectedClusterRole(t, "metricsCollectorClusterRole.yaml")
	clusterRole := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, clusterRole, component.CollectorClusterRoleName, "")
	require.NoError(t, err)
	clusterRole.ResourceVersion = ""
	require.Equal(t, expectedClusterRole, clusterRole)

	// Collector cluster role binding
	expectedClusterRoleBinding := testutil.GetExpectedClusterRoleBinding(t, "metricsCollectorClusterRoleBinding.yaml")
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, clusterRoleBinding, component.CollectorClusterRoleBindingName, "")
	require.NoError(t, err)
	clusterRoleBinding.ResourceVersion = ""
	require.Equal(t, expectedClusterRoleBinding, clusterRoleBinding)

	// Collector deployment
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CollectorDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	expectedDeployment := testutil.GetExpectedDeployment(t, "metricsCollectorDeployment.yaml")
	// Revise unrelated fields so we can compare.
	expectedDeployment.Spec.Template.Spec.DeprecatedServiceAccount = ""
	deployment.ResourceVersion = ""
	require.Equal(t, expectedDeployment, deployment)

	secret := v1.Secret{}
	err = testutil.Get(k8sClient, &secret, component.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, secret.OwnerReferences[0].Name, cluster.Name)

	// Now disable telemetry
	cluster.Spec.Monitoring.Telemetry.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollector)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.Empty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.Empty(t, cluster.Status.DesiredImages.LogUploader)

	err = testutil.Get(k8sClient, telemetryConfigMap, component.TelemetryConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(k8sClient, configMap, component.CollectorProxyConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(k8sClient, configMap, component.CollectorConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(k8sClient, role, component.CollectorRoleName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(k8sClient, roleBinding, component.CollectorRoleBindingName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(k8sClient, clusterRole, component.CollectorClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(k8sClient, clusterRoleBinding, component.CollectorClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(k8sClient, deployment, component.CollectorDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Cert is not deleted after telemetry is disabled. it would be reused when it's re-enabled.
	err = testutil.Get(k8sClient, &secret, component.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)

	// Now enabled again with custom telemetry image.
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	cluster.Spec.Monitoring.Telemetry.Image = "myimage"
	cluster.Status = corev1.StorageClusterStatus{
		ClusterUID: "test-clusteruid",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
	require.NotEqual(t, cluster.Status.DesiredImages.MetricsCollector, "")
	require.NotEqual(t, cluster.Status.DesiredImages.MetricsCollectorProxy, "")
	require.Empty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.Empty(t, cluster.Status.DesiredImages.LogUploader)
}

func TestMetricsCollectorIsDisabledForOldPxVersions(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationTelemetryArcusLocation: "internal",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.9.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
		},

		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	configMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.CollectorConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	role := &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, component.CollectorRoleName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	roleBinding := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, component.CollectorRoleBindingName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	clusterRole := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, clusterRole, component.CollectorClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, clusterRoleBinding, component.CollectorClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CollectorDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestTelemetryCCMProxy(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	ccmProxyConfigMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, ccmProxyConfigMap, component.TelemetryCCMProxyConfigMapName, cluster.Namespace)
	require.Error(t, err)
	require.True(t, errors.IsNotFound(err))

	// Add PX_HTTP_PROXY environment variable and config map should be created
	proxy := "http://username:password@hotstname:port"
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxHTTPProxy,
			Value: proxy,
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, ccmProxyConfigMap, component.TelemetryCCMProxyConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.TelemetryCCMProxyConfigMapName, ccmProxyConfigMap.Name)
	require.Equal(t, "kube-test", ccmProxyConfigMap.Namespace)
	require.Equal(t, map[string]string{"http_proxy": proxy}, ccmProxyConfigMap.Data)

	// Update the environment variable and config map should be updated
	proxy = "http://username:password@hotstname2:port"
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxHTTPProxy,
			Value: proxy,
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, ccmProxyConfigMap, component.TelemetryCCMProxyConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.TelemetryCCMProxyConfigMapName, ccmProxyConfigMap.Name)
	require.Equal(t, "kube-test", ccmProxyConfigMap.Namespace)
	require.Equal(t, map[string]string{"http_proxy": proxy}, ccmProxyConfigMap.Data)

	// Remove the environment variable and config map should be gone
	cluster.Spec.Env = nil
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, ccmProxyConfigMap, component.TelemetryCCMProxyConfigMapName, cluster.Namespace)
	require.Error(t, err)
	require.True(t, errors.IsNotFound(err))
}

func TestTelemetryCCMGoEnableAndDisable(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.12.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}

	// This cert is created by ccm container outside of operator, let's simulate it.
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// Validate default ccm listening port
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "ccmGoPhonehomeDaemonSetDefaultPort.yaml")
	daemonset := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, component.DaemonSetNameTelemetryPhonehome, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, daemonset.OwnerReferences, 1)
	require.Equal(t, cluster.Name, daemonset.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Name, daemonset.Name)
	require.Equal(t, expectedDaemonSet.Namespace, daemonset.Namespace)
	require.Equal(t, expectedDaemonSet.Spec, daemonset.Spec)

	// Shift start port
	cluster.Spec.StartPort = &startPort
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotEmpty(t, cluster.Status.DesiredImages.Telemetry)
	require.NotEmpty(t, cluster.Status.DesiredImages.MetricsCollector)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.NotEmpty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.NotEmpty(t, cluster.Status.DesiredImages.LogUploader)

	// Validate ccm-go service account, cluster role and cluster role binding
	serviceAccount := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, serviceAccount, component.ServiceAccountNameTelemetry, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, serviceAccount.OwnerReferences, 1)
	require.Equal(t, cluster.Name, serviceAccount.OwnerReferences[0].Name)

	clusterRole := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, clusterRole, component.ClusterRoleNameTelemetry, "")
	require.NoError(t, err)
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, clusterRoleBinding, component.ClusterRoleBindingNameTelemetry, "")
	require.NoError(t, err)

	// Validate ccm-go role and role binding
	role := &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, component.RoleNameTelemetry, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, role.OwnerReferences, 1)
	require.Equal(t, cluster.Name, role.OwnerReferences[0].Name)

	roleBinding := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, component.RoleBindingNameTelemetry, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, roleBinding.OwnerReferences, 1)
	require.Equal(t, cluster.Name, roleBinding.OwnerReferences[0].Name)

	// Validate config maps
	expectedConfigMap := testutil.GetExpectedConfigMap(t, "ccmGoRegisterConfigMap.yaml")
	configMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegister, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoRegisterProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryTLSCertificate, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehome, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorV2, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// Validate deployments
	expectedDeployment := testutil.GetExpectedDeployment(t, "ccmGoRegisterDeployment.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryRegistration, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	expectedDaemonSet = testutil.GetExpectedDaemonSet(t, "ccmGoPhonehomeDaemonSet.yaml")
	daemonset = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, component.DaemonSetNameTelemetryPhonehome, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, daemonset.OwnerReferences, 1)
	require.Equal(t, cluster.Name, daemonset.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Name, daemonset.Name)
	require.Equal(t, expectedDaemonSet.Namespace, daemonset.Namespace)
	require.Equal(t, expectedDaemonSet.Spec, daemonset.Spec)

	expectedDeployment = testutil.GetExpectedDeployment(t, "ccmGoCollectorDeployment.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryCollectorV2, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, deployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, deployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Name, deployment.Name)
	require.Equal(t, expectedDeployment.Namespace, deployment.Namespace)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	secret := v1.Secret{}
	err = testutil.Get(k8sClient, &secret, component.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, secret.OwnerReferences[0].Name, cluster.Name)

	// Now disable telemetry
	cluster.Spec.Monitoring.Telemetry.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollector)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.Empty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.Empty(t, cluster.Status.DesiredImages.LogUploader)

	err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryRegistration, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryCollectorV2, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, daemonset, component.DaemonSetNameTelemetryPhonehome, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegister, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryTLSCertificate, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehome, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorV2, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, role, component.RoleNameTelemetry, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, roleBinding, component.RoleBindingNameTelemetry, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, clusterRole, component.ClusterRoleNameTelemetry, "")
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, clusterRoleBinding, component.ClusterRoleBindingNameTelemetry, "")
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, serviceAccount, component.ServiceAccountNameTelemetry, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Cert is not deleted after telemetry is disabled. it would be reused when it's re-enabled.
	err = testutil.Get(k8sClient, &secret, component.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)

	// Now enabled again with custom telemetry image.
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	cluster.Spec.Monitoring.Telemetry.Image = "purestorage/ccm-go:1.2.3"
	cluster.Spec.Monitoring.Telemetry.LogUploaderImage = "log-uploader-image"
	cluster.Status = corev1.StorageClusterStatus{
		ClusterUID: "test-clusteruid",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
	require.NotEmpty(t, cluster.Status.DesiredImages.MetricsCollector)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.NotEmpty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.Empty(t, cluster.Status.DesiredImages.LogUploader)
}

func TestValidateTelemetryEnabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	recorder := record.NewFakeRecorder(10)
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.7.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
					Image:   "purestorage/ccm-go:1.2.3",
				},
			},
		},
	}

	// TestCase: telemetry should be disabled if PX is lower than 2.8
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be disabled: telemetry is not supported on Portworx version",
			v1.EventTypeWarning, util.TelemetryDisabledReason))

	// TestCase: telemetry should not be enabled by default if CCM Go is not supported
	cluster.Spec.Image = "portworx/oci-monitor:2.11.0"
	cluster.Spec.Monitoring = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Monitoring)
	require.Len(t, recorder.Events, 0)

	// TestCase: telemetry should be disabled if using secure proxy on ccm-go
	cluster.Spec.Image = "portworx/oci-monitor:2.12.0"
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		Telemetry: &corev1.TelemetrySpec{
			Enabled: true,
		},
	}
	cluster.Spec.Env = []v1.EnvVar{{
		Name:  pxutil.EnvKeyPortworxHTTPSProxy,
		Value: "test-proxy",
	}}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be disabled: telemetry is not supported with secure proxy",
			v1.EventTypeWarning, util.TelemetryDisabledReason))

	// TestCase: telemetry should be disabled if using invalid http proxy format
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	cluster.Spec.Env = []v1.EnvVar{{
		Name:  pxutil.EnvKeyPortworxHTTPProxy,
		Value: "test-proxy",
	}}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be disabled: telemetry is not supported with proxy in a format of",
			v1.EventTypeWarning, util.TelemetryDisabledReason))

	// TestCase: telemetry should not be enabled by default if it's disabled
	cluster.Spec.Env = nil
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		Telemetry: &corev1.TelemetrySpec{
			Enabled: false,
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 0)
}

func TestSetTelemetryDefaultWithoutCertCreated(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	recorder := record.NewFakeRecorder(10)
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.12.0",
		},
	}

	// Handles both fresh install and upgrade, parameters:
	// registration endpoint reachable: yes/no
	// HTTP proxy provided: yes/no
	// telemetry spec: empty/enabled
	setupEtcHosts(t, "127.0.0.1", "register.cloud-support.purestorage.com")
	defer restoreEtcHosts(t)

	// TestCase: cannot ping, no proxy, telemetry empty
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be disabled: cannot reach to Pure1",
			v1.EventTypeWarning, util.TelemetryDisabledReason))

	// TestCase: cannot ping, proxy, telemetry empty
	cluster.Spec.Monitoring = nil
	cluster.Spec.Env = []v1.EnvVar{{
		Name:  pxutil.EnvKeyPortworxHTTPProxy,
		Value: "http://host:1234",
	}}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be disabled: cannot reach to Pure1",
			v1.EventTypeWarning, util.TelemetryDisabledReason))

	// TestCase: cannot ping, proxy, telemetry enabled
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be disabled: cannot reach to Pure1",
			v1.EventTypeWarning, util.TelemetryDisabledReason))

	// TestCase: cannot ping, no proxy, telemetry enabled
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	cluster.Spec.Env = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be disabled: cannot reach to Pure1",
			v1.EventTypeWarning, util.TelemetryDisabledReason))

	// TestCase: can ping, no proxy, telemetry empty
	cluster.Spec.Monitoring = nil
	cluster.Annotations = map[string]string{
		pxutil.AnnotationTelemetryArcusLocation: "internal",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be enabled by default",
			v1.EventTypeNormal, util.TelemetryEnabledReason))

	// TestCase: can ping, no proxy, telemetry enabled
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	cluster.Spec.Env = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 0)

	// Tests below are not enabled, to manually run the test remove this check and
	// set httpProxy below to a working proxy.
	if true {
		return
	}
	httpProxy := "http://hostname:1234"

	// TestCase: can ping, proxy, telemetry empty
	cluster.Spec.Monitoring = nil
	cluster.Spec.Env = []v1.EnvVar{{
		Name:  pxutil.EnvKeyPortworxHTTPProxy,
		Value: httpProxy,
	}}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be enabled by default",
			v1.EventTypeNormal, util.TelemetryEnabledReason))

	// TestCase: can ping, proxy, telemetry enabled
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 0)
}

func TestSetTelemetryDefaultWithCertCreated(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	recorder := record.NewFakeRecorder(10)
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.12.0",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// Parameters:
	// HTTP proxy provided: yes/no
	// telemetry spec: empty/enabled
	setupEtcHosts(t, "127.0.0.1", "register.cloud-support.purestorage.com")
	defer restoreEtcHosts(t)

	// TestCase: no proxy, telemetry empty
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be enabled by default",
			v1.EventTypeNormal, util.TelemetryEnabledReason))

	// TestCase: proxy, telemetry empty
	cluster.Spec.Monitoring = nil
	cluster.Spec.Env = []v1.EnvVar{{
		Name:  pxutil.EnvKeyPortworxHTTPProxy,
		Value: "http://host:port",
	}}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v telemetry will be enabled by default",
			v1.EventTypeNormal, util.TelemetryEnabledReason))

	// TestCase: proxy, telemetry enabled
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 0)

	// TestCase: no proxy, telemetry enabled
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	cluster.Spec.Env = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.Monitoring)
	require.NotNil(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Len(t, recorder.Events, 0)
}

func TestTelemetryCCMGoUpgrade(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	// Deploy px with CCM Java enabled and telemetry image specified
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.10.1",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
					Image:   "purestorage/telemetry:1.2.3",
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}
	// This cert is created by ccm container outside of operator, let's simulate it.
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
	require.NotEmpty(t, cluster.Status.DesiredImages.MetricsCollector)
	require.NotEmpty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.Empty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.Empty(t, cluster.Status.DesiredImages.LogUploader)

	validateCCMJavaComponents := func() {
		// validate ccm java components exist
		serviceAccount := &v1.ServiceAccount{}
		err = testutil.Get(k8sClient, serviceAccount, component.CollectorServiceAccountName, cluster.Namespace)
		require.NoError(t, err)
		clusterRole := &rbacv1.ClusterRole{}
		err = testutil.Get(k8sClient, clusterRole, component.CollectorClusterRoleName, "")
		require.NoError(t, err)
		clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
		err = testutil.Get(k8sClient, clusterRoleBinding, component.CollectorClusterRoleBindingName, "")
		require.NoError(t, err)
		role := &rbacv1.Role{}
		err = testutil.Get(k8sClient, role, component.CollectorRoleName, cluster.Namespace)
		require.NoError(t, err)
		roleBinding := &rbacv1.RoleBinding{}
		err = testutil.Get(k8sClient, roleBinding, component.CollectorRoleBindingName, cluster.Namespace)
		require.NoError(t, err)
		configMap := &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.TelemetryConfigMapName, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.CollectorProxyConfigMapName, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.CollectorConfigMapName, cluster.Namespace)
		require.NoError(t, err)
		deployment := &appsv1.Deployment{}
		err = testutil.Get(k8sClient, deployment, component.CollectorDeploymentName, cluster.Namespace)
		require.NoError(t, err)
		require.Len(t, deployment.Spec.Template.Spec.InitContainers, 0)
		require.Equal(t, component.CollectorServiceAccountName, deployment.Spec.Template.Spec.ServiceAccountName)
		// validate ccm go components don't exist
		err = testutil.Get(k8sClient, serviceAccount, component.ServiceAccountNameTelemetry, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, clusterRole, component.ClusterRoleNameTelemetry, "")
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, clusterRoleBinding, component.ClusterRoleBindingNameTelemetry, "")
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, role, component.RoleNameTelemetry, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, roleBinding, component.RoleBindingNameTelemetry, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegister, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehome, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorV2, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryTLSCertificate, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryRegistration, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryCollectorV2, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		daemonset := &appsv1.DaemonSet{}
		err = testutil.Get(k8sClient, daemonset, component.DaemonSetNameTelemetryPhonehome, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
	}
	validateCCMJavaComponents()

	// TestCase: upgrade PX version, ccm should force upgrade if image is specified and reset the image
	cluster.Spec.Image = "portworx/oci-monitor:2.12.1"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.NotEmpty(t, cluster.Status.DesiredImages.Telemetry)
	require.NotEmpty(t, cluster.Status.DesiredImages.MetricsCollector)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.NotEmpty(t, cluster.Status.DesiredImages.TelemetryProxy)
	require.NotEmpty(t, cluster.Status.DesiredImages.LogUploader)
	validateCCMGoComponents := func() {
		// validate ccm go components exist
		serviceAccount := &v1.ServiceAccount{}
		err = testutil.Get(k8sClient, serviceAccount, component.ServiceAccountNameTelemetry, cluster.Namespace)
		require.NoError(t, err)
		clusterRole := &rbacv1.ClusterRole{}
		err = testutil.Get(k8sClient, clusterRole, component.ClusterRoleNameTelemetry, "")
		require.NoError(t, err)
		clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
		err = testutil.Get(k8sClient, clusterRoleBinding, component.ClusterRoleBindingNameTelemetry, "")
		require.NoError(t, err)
		role := &rbacv1.Role{}
		err = testutil.Get(k8sClient, role, component.RoleNameTelemetry, cluster.Namespace)
		require.NoError(t, err)
		roleBinding := &rbacv1.RoleBinding{}
		err = testutil.Get(k8sClient, roleBinding, component.RoleBindingNameTelemetry, cluster.Namespace)
		require.NoError(t, err)
		configMap := &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegister, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryTLSCertificate, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehome, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorV2, cluster.Namespace)
		require.NoError(t, err)
		configMap = &v1.ConfigMap{}
		err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
		require.NoError(t, err)
		deployment := &appsv1.Deployment{}
		err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryRegistration, cluster.Namespace)
		require.NoError(t, err)
		daemonset := &appsv1.DaemonSet{}
		err = testutil.Get(k8sClient, daemonset, component.DaemonSetNameTelemetryPhonehome, cluster.Namespace)
		require.NoError(t, err)
		deployment = &appsv1.Deployment{}
		err = testutil.Get(k8sClient, deployment, component.DeploymentNameTelemetryCollectorV2, cluster.Namespace)
		require.NoError(t, err)
		require.Len(t, deployment.Spec.Template.Spec.InitContainers, 1)
		require.Equal(t, component.ServiceAccountNameTelemetry, deployment.Spec.Template.Spec.ServiceAccountName)
		// validate ccm java components don't exist
		err = testutil.Get(k8sClient, serviceAccount, component.CollectorServiceAccountName, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, clusterRole, component.CollectorClusterRoleName, "")
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, clusterRoleBinding, component.CollectorClusterRoleBindingName, "")
		require.True(t, errors.IsNotFound(err))
		role = &rbacv1.Role{}
		err = testutil.Get(k8sClient, role, component.CollectorRoleName, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, roleBinding, component.CollectorRoleBindingName, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.TelemetryConfigMapName, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.CollectorConfigMapName, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, configMap, component.CollectorProxyConfigMapName, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
		err = testutil.Get(k8sClient, deployment, component.CollectorDeploymentName, cluster.Namespace)
		require.True(t, errors.IsNotFound(err))
	}
	validateCCMGoComponents()
}

func TestTelemetryCCMGoHTTPProxy(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.12.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			StartPort: &startPort,
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// TestCase: no px proxy specified
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Validate component proxy config map content
	expectedConfigMap := testutil.GetExpectedConfigMap(t, "ccmGoRegisterProxyConfigMap.yaml")
	configMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// Add PX_HTTP_PROXY environment variable and config maps should be updated
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxHTTPProxy,
			Value: "http://hostname:1234",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoRegisterProxyConfigMap_httpproxy.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeProxyConfigMap_httpproxy.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorProxyConfigMap_httpproxy.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// Remove PX_HTTP_PROXY environment variable and config maps should be updated
	cluster.Spec.Env = []v1.EnvVar{}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoRegisterProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// When using invalid proxy format configmaps will be deleted
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxHTTPProxy,
			Value: "http://hostnameonly",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestTelemetryCCMGoHTTPSProxy(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.12.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			StartPort: &startPort,
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// TestCase: no px proxy specified
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Validate component proxy config map content
	expectedConfigMap := testutil.GetExpectedConfigMap(t, "ccmGoRegisterProxyConfigMap.yaml")
	configMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// Add PX_HTTPS_PROXY environment variable and config maps should be updated
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxHTTPSProxy,
			Value: "https://user:password@hostname:1234",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoRegisterProxyConfigMap_httpsproxy.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeProxyConfigMap_httpsproxy.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorProxyConfigMap_httpsproxy.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// Remove PX_HTTPS_PROXY environment variable and config maps should be updated
	cluster.Spec.Env = []v1.EnvVar{}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoRegisterProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	expectedConfigMap = testutil.GetExpectedConfigMap(t, "ccmGoCollectorProxyConfigMap.yaml")
	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	// When using invalid proxy format configmaps will be deleted
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxHTTPSProxy,
			Value: "https://user:password@hostnameonly",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryRegisterProxy, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestTelemetrySecretDeletion(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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
			Image: "portworx/oci-monitor:2.8.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
		},
	}

	// This cert is created by ccm container outside of operator, let's simulate it.
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// TestCase: Don't set secret owner reference when delete strategy is not set
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	secret := v1.Secret{}
	err = testutil.Get(k8sClient, &secret, component.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, secret.OwnerReferences)

	// TestCase: Set secret owner reference when delete strategy is UninstallAndWipe
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
		Type: corev1.UninstallAndWipeStorageClusterStrategyType,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	secret = v1.Secret{}
	err = testutil.Get(k8sClient, &secret, component.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)
	require.Contains(t, secret.OwnerReferences, *ownerRef)

	// TestCase: Update delete strategy and secret owner reference should be removed
	cluster.Spec.DeleteStrategy.Type = corev1.UninstallStorageClusterStrategyType
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	secret = v1.Secret{}
	err = testutil.Get(k8sClient, &secret, component.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, secret.OwnerReferences)
}

func TestTelemetryCCMGoRestartPhonehome(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.12.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			StartPort: &startPort,
		},
		Status: corev1.StorageClusterStatus{
			ClusterUID: "test-clusteruid",
		},
	}

	// This cert is created by ccm container outside of operator, let's simulate it.
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// Create phonehome configmap to update
	expectedConfigMap := testutil.GetExpectedConfigMap(t, "ccmGoPhonehomeConfigMap.yaml")
	oldConfigMap := expectedConfigMap.DeepCopy()
	oldConfigMap.Data = nil
	err = k8sClient.Create(
		context.TODO(),
		oldConfigMap,
		&client.CreateOptions{},
	)
	require.NoError(t, err)

	// Create existing daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "ccmGoPhonehomeDaemonSet.yaml")
	expectedDaemonSet.OwnerReferences = []metav1.OwnerReference{{UID: cluster.UID}}
	err = k8sClient.Create(
		context.TODO(),
		expectedDaemonSet,
		&client.CreateOptions{},
	)
	require.NoError(t, err)
	require.Equal(t, "1", expectedDaemonSet.ResourceVersion)

	// Verify config map is updated and daemonset is recreated
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	configMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, component.ConfigMapNameTelemetryPhonehome, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, configMap.OwnerReferences[0].Name)
	require.Equal(t, expectedConfigMap.Name, configMap.Name)
	require.Equal(t, expectedConfigMap.Namespace, configMap.Namespace)
	require.Equal(t, expectedConfigMap.Data, configMap.Data)

	daemonset := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, component.DaemonSetNameTelemetryPhonehome, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, daemonset.OwnerReferences, 1)
	require.Equal(t, cluster.Name, daemonset.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Name, daemonset.Name)
	require.Equal(t, expectedDaemonSet.Namespace, daemonset.Namespace)
	require.Equal(t, expectedDaemonSet.Spec, daemonset.Spec)
	require.Equal(t, "1", expectedDaemonSet.ResourceVersion)
}

func TestPortworxAPIServiceCustomLabels(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			StartPort:  &startPort,
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Validate Portworx API Service without custom labels
	expectedPxAPIService := testutil.GetExpectedService(t, "portworxAPIService.yaml")
	pxAPIService := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)

	// Custom labels of other components should not appear in service/portworx-api
	customLabels := map[string]string{
		"custom-label-key": "custom-label-val",
	}
	cluster.Spec.Metadata = &corev1.Metadata{
		Labels: map[string]map[string]string{
			"service/stork-service": customLabels,
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)

	// Add custom labels to service/portworx-api
	cluster.Spec.Metadata.Labels["service/portworx-api"] = customLabels
	expectedPxAPIService.Labels["custom-label-key"] = "custom-label-val"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)

	// Update custom label value
	cluster.Spec.Metadata.Labels["service/portworx-api"]["custom-label-key"] = "custom-label-new-val"
	expectedPxAPIService.Labels["custom-label-key"] = "custom-label-new-val"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)

	// Remove custom labels
	cluster.Spec.Metadata.Labels = nil
	expectedPxAPIService = testutil.GetExpectedService(t, "portworxAPIService.yaml")

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)
}

func TestCSIAndPVCControllerDeploymentWithPodTopologySpreadConstraints(t *testing.T) {
	fakeNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node0",
			Labels: map[string]string{
				"topology.kubernetes.io/region": "region0",
			},
		},
	}
	versionClient := fakek8sclient.NewSimpleClientset(fakeNode)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.17.0",
	}
	coreops.SetInstance(coreops.New(versionClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient(fakeNode)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// CSI deployment topology constraints
	deployment := &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	expectedConstraints := []v1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/region",
			WhenUnsatisfiable: v1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "px-csi-driver",
				},
			},
		},
	}
	require.Equal(t, expectedConstraints, deployment.Spec.Template.Spec.TopologySpreadConstraints)

	// PVC controller deployment topology constraints
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	expectedConstraints = []v1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/region",
			WhenUnsatisfiable: v1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": component.PVCDeploymentName,
					"tier": "control-plane",
				},
			},
		},
	}
	require.Equal(t, expectedConstraints, deployment.Spec.Template.Spec.TopologySpreadConstraints)
}

func TestCSIAndPVCControllerDeploymentWithoutPodTopologySpreadConstraints(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.17.0",
	}
	coreops.SetInstance(coreops.New(versionClient))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// CSI deployment topology constraints
	deployment := &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, deployment.Spec.Template.Spec.TopologySpreadConstraints)

	// PVC controller deployment topology constraints
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, deployment.Spec.Template.Spec.TopologySpreadConstraints)
}

func TestServiceCustomAnnotations(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			StartPort:  &startPort,
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
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
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx Service
	expectedPXService := testutil.GetExpectedService(t, "portworxService.yaml")
	pxService := &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPXService.Name, pxService.Name)
	require.Equal(t, expectedPXService.Namespace, pxService.Namespace)
	require.Len(t, pxService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxService.OwnerReferences[0].Name)
	require.Equal(t, expectedPXService.Labels, pxService.Labels)
	require.Equal(t, expectedPXService.Spec, pxService.Spec)
	require.Nil(t, pxService.Annotations)

	// Portworx KVDB Service
	expectedPXKVDBService := testutil.GetExpectedService(t, "portworxKVDBService.yaml")
	pxKVDBService := &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPXKVDBService.Name, pxKVDBService.Name)
	require.Equal(t, expectedPXKVDBService.Namespace, pxKVDBService.Namespace)
	require.Len(t, pxKVDBService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxKVDBService.OwnerReferences[0].Name)
	require.Equal(t, expectedPXKVDBService.Labels, pxKVDBService.Labels)
	require.Equal(t, expectedPXKVDBService.Spec, pxKVDBService.Spec)
	require.Nil(t, pxKVDBService.Annotations)

	// Portworx API Service
	expectedPxAPIService := testutil.GetExpectedService(t, "portworxAPIService.yaml")
	pxAPIService := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedPxAPIService.Name, pxAPIService.Name)
	require.Equal(t, expectedPxAPIService.Namespace, pxAPIService.Namespace)
	require.Len(t, pxAPIService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, pxAPIService.OwnerReferences[0].Name)
	require.Equal(t, expectedPxAPIService.Labels, pxAPIService.Labels)
	require.Equal(t, expectedPxAPIService.Spec, pxAPIService.Spec)
	require.Nil(t, pxAPIService.Annotations)

	// Portworx Proxy Service
	expectedPxProxyService := testutil.GetExpectedService(t, "pxProxyService.yaml")
	pxProxyService := &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, expectedPxProxyService.Name, pxProxyService.Name)
	require.Equal(t, expectedPxProxyService.Namespace, pxProxyService.Namespace)
	require.Empty(t, pxProxyService.OwnerReferences)
	require.Equal(t, expectedPxProxyService.Labels, pxProxyService.Labels)
	require.Equal(t, expectedPxProxyService.Spec, pxProxyService.Spec)
	require.Nil(t, pxProxyService.Annotations)

	// Add custom annotation to services
	pxServiceAnnotations := map[string]string{"px-service-annotation-key": "px-service-annotation-val"}
	pxKVDBServiceAnnotations := map[string]string{"px-kvdb-service-annotation-key": "px-kvdb-service-annotation-val"}
	pxAPIServiceAnnotations := map[string]string{"px-api-service-annotation-key": "px-api-service-annotation-val"}
	cluster.Spec.Metadata = &corev1.Metadata{
		Annotations: map[string]map[string]string{
			fmt.Sprintf("%s/%s", k8sutil.Service, pxutil.PortworxServiceName):     pxServiceAnnotations,
			fmt.Sprintf("%s/%s", k8sutil.Service, pxutil.PortworxKVDBServiceName): pxKVDBServiceAnnotations,
			fmt.Sprintf("%s/%s", k8sutil.Service, component.PxAPIServiceName):     pxAPIServiceAnnotations,
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx Service
	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxServiceAnnotations, pxService.Annotations)

	// Portworx KVDB Service
	pxKVDBService = &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxKVDBServiceAnnotations, pxKVDBService.Annotations)

	// Portworx API Service
	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxAPIServiceAnnotations, pxAPIService.Annotations)

	// Portworx Proxy Service
	pxProxyService = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, pxServiceAnnotations, pxService.Annotations)

	// Update and remove custom annotations
	pxServiceAnnotations["px-service-annotation-another-key"] = "px-service-annotation-another-val"
	pxKVDBServiceAnnotations["px-kvdb-service-annotation-key"] = "px-kvdb-service-annotation-new-val"
	cluster.Spec.Metadata = &corev1.Metadata{
		Annotations: map[string]map[string]string{
			fmt.Sprintf("%s/%s", k8sutil.Service, pxutil.PortworxServiceName):     pxServiceAnnotations,
			fmt.Sprintf("%s/%s", k8sutil.Service, pxutil.PortworxKVDBServiceName): pxKVDBServiceAnnotations,
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx Service
	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxServiceAnnotations, pxService.Annotations)

	// Portworx KVDB Service
	pxKVDBService = &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxKVDBServiceAnnotations, pxKVDBService.Annotations)

	// Portworx API Service
	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, pxAPIService.Annotations)

	// Portworx Proxy Service
	pxProxyService = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, pxServiceAnnotations, pxService.Annotations)
}

func TestServiceTypeAnnotation(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			StartPort:  &startPort,
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Test default service types
	// Portworx Service
	pxService := &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxService.Spec.Type)
	// Portworx KVDB Service
	pxKVDBService := &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxKVDBService.Spec.Type)
	// Portworx API Service
	pxAPIService := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxAPIService.Spec.Type)
	// Portworx Proxy Service
	pxProxyService := &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxProxyService.Spec.Type)
	// Lighthouse Service
	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, lhService.Spec.Type)

	// Test add service type annotation in backward compatible format
	expectedServiceType := v1.ServiceTypeLoadBalancer
	cluster.Annotations = map[string]string{
		pxutil.AnnotationServiceType: string(expectedServiceType),
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	// Portworx Service
	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxService.Spec.Type)
	// Portworx KVDB Service
	pxKVDBService = &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxKVDBService.Spec.Type)
	// Portworx API Service
	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxAPIService.Spec.Type)
	// Portworx Proxy Service
	pxProxyService = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxProxyService.Spec.Type)
	// Lighthouse Service
	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, lhService.Spec.Type)

	// Test update service type annotation to new format, services not specified should use their default values
	cluster.Annotations = map[string]string{
		pxutil.AnnotationServiceType: "portworx-service:LoadBalancer;portworx-api:NodePort;",
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	// Portworx Service
	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxService.Spec.Type)
	// Portworx KVDB Service
	pxKVDBService = &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxKVDBService.Spec.Type)
	// Portworx API Service
	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxAPIService.Spec.Type)
	// Portworx Proxy Service
	pxProxyService = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxProxyService.Spec.Type)
	// Lighthouse Service
	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, lhService.Spec.Type)

	// Test update service type annotation values, services not specified should use their default values
	cluster.Annotations = map[string]string{
		pxutil.AnnotationServiceType: "portworx-service:NodePort;portworx-kvdb-service:LoadBalancer",
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	// Portworx Service
	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxService.Spec.Type)
	// Portworx KVDB Service
	pxKVDBService = &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxKVDBService.Spec.Type)
	// Portworx API Service
	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxAPIService.Spec.Type)
	// Portworx Proxy Service
	pxProxyService = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxProxyService.Spec.Type)
	// Lighthouse Service
	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, lhService.Spec.Type)

	// Test update service type annotation back to backward compatible format
	expectedServiceType = v1.ServiceTypeExternalName
	cluster.Annotations = map[string]string{
		pxutil.AnnotationServiceType: string(expectedServiceType),
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	// Portworx Service
	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxService.Spec.Type)
	// Portworx KVDB Service
	pxKVDBService = &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxKVDBService.Spec.Type)
	// Portworx API Service
	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxAPIService.Spec.Type)
	// Portworx Proxy Service
	pxProxyService = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, pxProxyService.Spec.Type)
	// Lighthouse Service
	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedServiceType, lhService.Spec.Type)

	// Test remove service type annotation
	cluster.Annotations = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	// Portworx Service
	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxService.Spec.Type)
	// Portworx KVDB Service
	pxKVDBService = &v1.Service{}
	err = testutil.Get(k8sClient, pxKVDBService, pxutil.PortworxKVDBServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxKVDBService.Spec.Type)
	// Portworx API Service
	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxAPIService.Spec.Type)
	// Portworx Proxy Service
	pxProxyService = &v1.Service{}
	err = testutil.Get(k8sClient, pxProxyService, pxutil.PortworxServiceName, api.NamespaceSystem)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxProxyService.Spec.Type)
	// Lighthouse Service
	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, lhService.Spec.Type)
}

func TestCSISnapController(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))

	// TestCase: SnapshotController should not be enabled by default on k8s 1.16 and below
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.16.0",
	}
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
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.Empty(t, cluster.Status.DesiredImages.CSISnapshotController)

	expectedDeployment := testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s116.yaml")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// TestCase: SnapshotController should be enabled by default on fresh install
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.20.0",
	}
	reregisterComponents()
	k8sClient = testutil.FakeK8sClient()
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.True(t, *cluster.Spec.CSI.InstallSnapshotController)
	require.NotEmpty(t, cluster.Status.DesiredImages.CSISnapshotController)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// TestCase: SnapshotController set to false explicitly
	reregisterComponents()
	k8sClient = testutil.FakeK8sClient()
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled:                   true,
				InstallSnapshotController: boolPtr(false),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.False(t, *cluster.Spec.CSI.InstallSnapshotController)
	require.Empty(t, cluster.Status.DesiredImages.CSISnapshotController)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120_without_snapcontroller.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// TestCase: SnapshotController set to true explicitly
	reregisterComponents()
	k8sClient = testutil.FakeK8sClient()
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.6.1",
			CSI: &corev1.CSISpec{
				Enabled:                   true,
				InstallSnapshotController: boolPtr(true),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.True(t, *cluster.Spec.CSI.InstallSnapshotController)
	require.NotEmpty(t, cluster.Status.DesiredImages.CSISnapshotController)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// TestCase: If snapshot controller exists in the same ns before creating the StorageCluster, don't create the controller
	reregisterComponents()
	snapshotControllerPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "snapshot-controller-pod",
			Namespace: "kube-test",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "snapshot-controller",
					Image: "registry.k8s.io/sig-storage/snapshot-controller:v3.0.3",
				},
			},
		},
	}
	k8sClient = testutil.FakeK8sClient(snapshotControllerPod)
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.6.1",
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CSI)
	require.True(t, cluster.Spec.CSI.Enabled)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.True(t, *cluster.Spec.CSI.InstallSnapshotController)
	require.NotEmpty(t, cluster.Status.DesiredImages.CSISnapshotController)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120_without_snapcontroller.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// TestCase: If snapshot controller exists in another ns before creating the StorageCluster, don't create the controller
	reregisterComponents()
	snapshotControllerPod = &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "snapshot-controller-pod",
			Namespace: "another-ns",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "snapshot-controller",
					Image: "registry.k8s.io/sig-storage/snapshot-controller:v3.0.3",
				},
			},
		},
	}
	k8sClient = testutil.FakeK8sClient(snapshotControllerPod)
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/image:2.6.1",
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CSI)
	require.True(t, cluster.Spec.CSI.Enabled)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.True(t, *cluster.Spec.CSI.InstallSnapshotController)
	require.NotEmpty(t, cluster.Status.DesiredImages.CSISnapshotController)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120_without_snapcontroller.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Delete the snapshot controller and restart operator
	err = testutil.Delete(driver.k8sClient, snapshotControllerPod)
	require.NoError(t, err)
	reregisterComponents()
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CSI)
	require.True(t, cluster.Spec.CSI.Enabled)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.True(t, *cluster.Spec.CSI.InstallSnapshotController)
	require.NotEmpty(t, cluster.Status.DesiredImages.CSISnapshotController)

	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120.yaml")
	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)

	// Restart operator with snapshot controller enabled, the container should not be deleted
	expectedDeployment = testutil.GetExpectedDeployment(t, "csiDeployment_1.0_k8s120.yaml")
	csiPod := &v1.Pod{}
	csiPod.ObjectMeta = expectedDeployment.Spec.Template.ObjectMeta
	csiPod.Spec = expectedDeployment.Spec.Template.Spec
	dummyPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-other-random-pod",
			Namespace: "kube-system",
		},
	}
	k8sClient = testutil.FakeK8sClient(csiPod, dummyPod)
	reregisterComponents()

	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CSI)
	require.True(t, cluster.Spec.CSI.Enabled)
	require.NotNil(t, cluster.Spec.CSI.InstallSnapshotController)
	require.True(t, *cluster.Spec.CSI.InstallSnapshotController)
	require.NotEmpty(t, cluster.Status.DesiredImages.CSISnapshotController)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(driver.k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Spec, deployment.Spec)
}

func contains(slice []string, val string) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}

func createFakeCRD(fakeClient *fakeextclient.Clientset, crdName string) error {
	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
	}
	_, err := fakeClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Create(context.TODO(), crd, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	return testutil.ActivateV1beta1CRDWhenCreated(fakeClient, crdName)
}

func validateTokenLifetime(t *testing.T, cluster *corev1.StorageCluster, jwtClaims jwt.MapClaims) {
	iat, ok := jwtClaims["iat"]
	require.True(t, ok, "iat should present in token")
	iatFloat64, ok := iat.(float64)
	require.True(t, ok, "iat should be a number")
	exp, ok := jwtClaims["exp"]
	require.True(t, ok, "exp should present in token")
	expFloat64, ok := exp.(float64)
	require.True(t, ok, "exp should be a number")
	iatTime := time.Unix(int64(iatFloat64), 0)
	expTime := time.Unix(int64(expFloat64), 0)
	tokenLifetime := expTime.Sub(iatTime)
	duration, err := pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	require.NoError(t, err)
	require.Equal(t, tokenLifetime, duration)
}

func expectedVolumesAndMounts(volumeSpecs []corev1.VolumeSpec) ([]v1.Volume, []v1.VolumeMount) {
	expectedVolumeSpecs := make([]corev1.VolumeSpec, len(volumeSpecs))
	for i, v := range volumeSpecs {
		vCopy := v.DeepCopy()
		vCopy.Name = "user-" + v.Name
		expectedVolumeSpecs[i] = *vCopy
	}
	return util.ExtractVolumesAndMounts(expectedVolumeSpecs)
}

// createTelemetrySecret creates a telemetry secret, so when calling SetDefaultsOnStorageCluster
// in tests that are unrelated to telemetry it doesn't need to ping Arcus registration endpoint
func createTelemetrySecret(t *testing.T, k8sClient client.Client, namespace string) {
	err := k8sClient.Create(
		context.TODO(),
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      component.TelemetryCertName,
				Namespace: namespace,
			},
		},
		&client.CreateOptions{},
	)
	require.NoError(t, err)
}

// test if install can be enabled on non-openshift servers for Plugin
func TestInstallOnNonOpenshiftCluster(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "non-openshift-version",
			APIResources: []metav1.APIResource{
				{
					Kind: "non-openshift-kind",
				},
			},
		},
	}
	coreops.SetInstance(coreops.New(versionClient))

	reregisterComponents()

	k8sClient := testutil.FakeK8sClient()

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{}
	pluginComponent, _ := component.Get(component.PluginComponentName)

	enabled := pluginComponent.IsEnabled(cluster)
	require.Equal(t, false, enabled)
}

// test if install can be enabled on 4.13 version of openshift for Plugin
func TestIsVersionSupportedForSupportedVersionOpenshift(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: component.ClusterOperatorVersion,
			APIResources: []metav1.APIResource{
				{
					Kind: component.ClusterOperatorKind,
				},
			},
		},
	}
	coreops.SetInstance(coreops.New(versionClient))

	reregisterComponents()
	operator := &ocpconfig.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: component.OpenshiftAPIServer,
		},
		Status: ocpconfig.ClusterOperatorStatus{
			RelatedObjects: []ocpconfig.ObjectReference{
				{Name: component.OpenshiftAPIServer},
			},
			Versions: []ocpconfig.OperandVersion{
				{
					Name:    component.OpenshiftAPIServer,
					Version: "4.13",
				},
			},
		},
	}
	k8sClient := testutil.FakeK8sClient()
	err := k8sClient.Create(context.TODO(), operator)
	require.NoError(t, err)

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{}
	pluginComponent, _ := component.Get(component.PluginComponentName)

	enabled := pluginComponent.IsEnabled(cluster)
	require.Equal(t, true, enabled)

}

// test if install can be enabled on 4.11 version of openshift for Plugin
func TestIsVersionSupportedForUnsupportedVersionOpenshift(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: component.ClusterOperatorVersion,
			APIResources: []metav1.APIResource{
				{
					Kind: component.ClusterOperatorKind,
				},
			},
		},
	}
	coreops.SetInstance(coreops.New(versionClient))

	reregisterComponents()
	operator := &ocpconfig.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: component.OpenshiftAPIServer,
		},
		Status: ocpconfig.ClusterOperatorStatus{
			RelatedObjects: []ocpconfig.ObjectReference{
				{Name: component.OpenshiftAPIServer},
			},
			Versions: []ocpconfig.OperandVersion{
				{
					Name:    component.OpenshiftAPIServer,
					Version: "4.11",
				},
			},
		},
	}
	k8sClient := testutil.FakeK8sClient()
	err := k8sClient.Create(context.TODO(), operator)
	require.NoError(t, err)

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{}
	pluginComponent, _ := component.Get(component.PluginComponentName)

	enabled := pluginComponent.IsEnabled(cluster)
	require.Equal(t, false, enabled)

}

func TestPluginInstallAndUninstall(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: component.ClusterOperatorVersion,
			APIResources: []metav1.APIResource{
				{
					Kind: component.ClusterOperatorKind,
				},
			},
		},
	}
	coreops.SetInstance(coreops.New(versionClient))

	reregisterComponents()
	operator := &ocpconfig.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: component.OpenshiftAPIServer,
		},
		Status: ocpconfig.ClusterOperatorStatus{
			RelatedObjects: []ocpconfig.ObjectReference{
				{Name: component.OpenshiftAPIServer},
			},
			Versions: []ocpconfig.OperandVersion{
				{
					Name:    component.OpenshiftAPIServer,
					Version: "4.13",
				},
			},
		},
	}
	k8sClient := testutil.FakeK8sClient()
	err := k8sClient.Create(context.TODO(), operator)
	require.NoError(t, err)

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// test creation of nginx-configmap
	expectedNginxCm := testutil.GetExpectedConfigMap(t, "nginx-configmap.yaml")
	actualNginxCm := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, actualNginxCm, component.NginxConfigMapName, "kube-test")
	require.NoError(t, err)
	require.Equal(t, expectedNginxCm.Name, actualNginxCm.Name)

	// test creation of nginx-service
	expectedNginxSvc := testutil.GetExpectedService(t, "nginx-service.yaml")
	actualNginxSvc := &v1.Service{}
	err = testutil.Get(k8sClient, actualNginxSvc, component.NginxServiceName, "kube-test")
	require.NoError(t, err)
	require.Equal(t, expectedNginxSvc.Name, actualNginxSvc.Name)
	require.Equal(t, expectedNginxSvc.Spec, actualNginxSvc.Spec)

	// test creation of nginx deployment
	expectedNginxDeployment := testutil.GetExpectedDeployment(t, "nginx-deployment.yaml")
	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &expectedNginxDeployment.Spec.Template.Spec)
	actualNginxDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, actualNginxDeployment, component.NginxDeploymentName, "kube-test")
	require.NoError(t, err)
	require.Equal(t, expectedNginxDeployment.Name, actualNginxDeployment.Name)
	require.Equal(t, expectedNginxDeployment.Spec, actualNginxDeployment.Spec)

	// test creation of plugin-configmap
	expectedPluginCm := testutil.GetExpectedConfigMap(t, "plugin-configmap.yaml")
	actualPluginCm := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, actualPluginCm, component.PluginConfigMapName, "kube-test")
	require.NoError(t, err)
	require.Equal(t, expectedPluginCm.Name, actualPluginCm.Name)
	require.Equal(t, expectedPluginCm.Data, actualPluginCm.Data)

	// test creation of plugin-service
	expectedPluginxSvc := testutil.GetExpectedService(t, "plugin-service.yaml")
	actualPluginSvc := &v1.Service{}
	err = testutil.Get(k8sClient, actualPluginSvc, component.PluginServiceName, "kube-test")
	require.NoError(t, err)
	require.Equal(t, expectedPluginxSvc.Name, actualPluginSvc.Name)
	require.Equal(t, expectedPluginxSvc.Spec, actualPluginSvc.Spec)

	// test creation of plugin-deployment
	expectedPluginDeployment := testutil.GetExpectedDeployment(t, "plugin-deployment.yaml")
	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &expectedPluginDeployment.Spec.Template.Spec)
	actualPluginDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, actualPluginDeployment, component.PluginDeploymentName, "kube-test")
	require.NoError(t, err)
	require.Equal(t, expectedPluginDeployment.Name, actualPluginDeployment.Name)
	require.Equal(t, expectedPluginDeployment.Spec, actualPluginDeployment.Spec)

	pluginComponent, _ := component.Get(component.PluginComponentName)
	err = pluginComponent.Delete(cluster)
	pluginComponent.MarkDeleted()
	require.NoError(t, err)

}

func reregisterComponents() {
	// Skipping registering of some components to avoid blocking
	// other tests. Skipped components: PortworxCRD, DisruptionBudget
	component.DeregisterAllComponents()
	component.RegisterPortworxBasicComponent()
	component.RegisterPortworxAPIComponent()
	component.RegisterPortworxProxyComponent()
	component.RegisterPortworxStorageClassComponent()
	component.RegisterAutopilotComponent()
	component.RegisterCSIComponent()
	component.RegisterLighthouseComponent()
	component.RegisterPVCControllerComponent()
	component.RegisterMonitoringComponent()
	component.RegisterPrometheusComponent()
	component.RegisterAlertManagerComponent()
	component.RegisterAuthComponent()
	component.RegisterTLSComponent()
	component.RegisterPSPComponent()
	component.RegisterTelemetryComponent()
	component.RegisterPxRepoComponent()
	component.RegisterSCCComponent()
	component.RegisterPortworxPluginComponent()
	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
}
