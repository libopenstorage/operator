package portworx

import (
	"context"
	"fmt"
	"strings"
	"testing"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBasicComponentsInstall(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	startPort := uint32(10001)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			StartPort: &startPort,
			Placement: &corev1alpha1.PlacementSpec{
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

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	// Portworx ServiceAccount
	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 1)

	sa := serviceAccountList.Items[0]
	require.Equal(t, pxutil.PortworxServiceAccountName, sa.Name)
	require.Equal(t, cluster.Namespace, sa.Namespace)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Portworx ClusterRole
	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 1)

	expectedCR := testutil.GetExpectedClusterRole(t, "portworxClusterRole.yaml")
	actualCR := clusterRoleList.Items[0]
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Portworx ClusterRoleBinding
	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 1)

	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "portworxClusterRoleBinding.yaml")
	actualCRB := crbList.Items[0]
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Portworx Role
	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Len(t, roleList.Items, 1)

	expectedRole := testutil.GetExpectedRole(t, "portworxRole.yaml")
	actualRole := roleList.Items[0]
	require.Equal(t, expectedRole.Name, actualRole.Name)
	require.Equal(t, expectedRole.Namespace, actualRole.Namespace)
	require.Len(t, actualRole.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualRole.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedRole.Rules, actualRole.Rules)

	// Portworx RoleBinding
	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Len(t, rbList.Items, 1)

	expectedRB := testutil.GetExpectedRoleBinding(t, "portworxRoleBinding.yaml")
	actualRB := rbList.Items[0]
	require.Equal(t, expectedRB.Name, actualRB.Name)
	require.Equal(t, expectedRB.Namespace, actualRB.Namespace)
	require.Len(t, actualRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedRB.Subjects, actualRB.Subjects)
	require.Equal(t, expectedRB.RoleRef, actualRB.RoleRef)

	// Portworx Services
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Len(t, serviceList.Items, 2)

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
	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Len(t, dsList.Items, 1)

	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "portworxAPIDaemonSet.yaml")
	ds := dsList.Items[0]
	require.Equal(t, expectedDaemonSet.Name, ds.Name)
	require.Equal(t, expectedDaemonSet.Namespace, ds.Namespace)
	require.Len(t, ds.OwnerReferences, 1)
	require.Equal(t, cluster.Name, ds.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, ds.Spec)
}

func TestBasicInstallWithPortworxDisabled(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				storagecluster.AnnotationDisableStorage: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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

func TestDisablePortworx(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxutil.PortworxServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, cr, component.PxClusterRoleName, "")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, component.PxClusterRoleBindingName, "")
	require.NoError(t, err)

	role := &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, component.PxRoleName, "")
	require.NoError(t, err)

	rb := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, rb, component.PxRoleBindingName, "")
	require.NoError(t, err)

	pxSvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxSvc, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)

	pxAPISvc := &v1.Service{}
	err = testutil.Get(k8sClient, pxAPISvc, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)

	ds := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)

	// Disable Portworx
	cluster.Annotations = map[string]string{
		storagecluster.AnnotationDisableStorage: "true",
	}
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, pxutil.PortworxServiceAccountName, cluster.Namespace)
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

	ds = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDefaultStorageClassesWithStork(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Len(t, storageClassList.Items, 8)

	expectedSC := testutil.GetExpectedStorageClass(t, "storageClassDb.yaml")
	actualSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbEncryptedStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassReplicated.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxReplicatedStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassReplicatedEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxReplicatedEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxReplicatedEncryptedStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbLocalSnapshot.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbLocalSnapshotStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbLocalSnapshotStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbLocalSnapshotEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbLocalSnapshotEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbLocalSnapshotEncryptedStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbCloudSnapshot.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbCloudSnapshotStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbCloudSnapshotStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)

	expectedSC = testutil.GetExpectedStorageClass(t, "storageClassDbCloudSnapshotEncrypted.yaml")
	actualSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualSC, component.PxDbCloudSnapshotEncryptedStorageClass, "")
	require.NoError(t, err)
	require.Equal(t, expectedSC.Name, component.PxDbCloudSnapshotEncryptedStorageClass)
	require.Len(t, actualSC.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualSC.OwnerReferences[0].Name)
	require.Equal(t, expectedSC.Annotations, actualSC.Annotations)
	require.Equal(t, expectedSC.Provisioner, actualSC.Provisioner)
	require.Equal(t, expectedSC.Parameters, actualSC.Parameters)
}

func TestDefaultStorageClassesWithoutStork(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: false,
			},
		},
	}

	// Stork is disabled
	err := driver.PreInstall(cluster)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				storagecluster.AnnotationDisableStorage: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)
}

func TestPortworxServiceTypeWithOverride(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsAKS:       "true",
				annotationIsGKE:       "true",
				annotationIsEKS:       "true",
				annotationServiceType: "ClusterIP",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	cluster.Annotations[annotationServiceType] = "LoadBalancer"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxService.Spec.Type)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, pxAPIService.Spec.Type)

	// Node port service type
	cluster.Annotations[annotationServiceType] = "NodePort"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxService.Spec.Type)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, pxAPIService.Spec.Type)

	// Invalid service type should use default service type
	cluster.Annotations[annotationServiceType] = "Invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	pxService = &v1.Service{}
	err = testutil.Get(k8sClient, pxService, pxutil.PortworxServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxService.Spec.Type)

	pxAPIService = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPIService, component.PxAPIServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, pxAPIService.Spec.Type)
}

func TestPVCControllerInstall(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerWithInvalidValue(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "invalid",
			},
		},
	}

	err := driver.PreInstall(cluster)
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

func TestPVCControllerInstallForOpenshift(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsOpenshift: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeploymentOpenshift.yaml")

	// Despite invalid pvc controller annotation, install for openshift
	cluster.Annotations[annotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeploymentOpenshift.yaml")
}

func TestPVCControllerInstallForOpenshiftInKubeSystem(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationIsOpenshift: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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

func TestPVCControllerInstallForPKS(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsPKS: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for PKS
	cluster.Annotations[annotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallForEKS(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsEKS: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for EKS
	cluster.Annotations[annotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallForGKE(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsGKE: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for GKE
	cluster.Annotations[annotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerInstallForAKS(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsAKS: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")

	// Despite invalid pvc controller annotation, install for AKS
	cluster.Annotations[annotationPVCController] = "invalid"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	verifyPVCControllerInstall(t, cluster, k8sClient)
	verifyPVCControllerDeployment(t, cluster, k8sClient, "pvcControllerDeployment.yaml")
}

func TestPVCControllerWhenPVCControllerDisabledExplicitly(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "false",
				annotationIsPKS:         "true",
				annotationIsEKS:         "true",
				annotationIsGKE:         "true",
				annotationIsAKS:         "true",
				annotationIsOpenshift:   "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsPKS:                         "true",
				annotationIsEKS:                         "true",
				annotationIsGKE:                         "true",
				annotationIsAKS:                         "true",
				annotationIsOpenshift:                   "true",
				storagecluster.AnnotationDisableStorage: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	cluster *corev1alpha1.StorageCluster,
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
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
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
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)
}

func verifyPVCControllerDeployment(
	t *testing.T,
	cluster *corev1alpha1.StorageCluster,
	k8sClient client.Client,
	specFileName string,
) {
	// PVC Controller Deployment
	deploymentList := &appsv1.DeploymentList{}
	err := testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Len(t, deploymentList.Items, 1)

	expectedDeployment := testutil.GetExpectedDeployment(t, specFileName)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	// Default PVC controller CPU
	expectedCPUQuantity := resource.MustParse("200m")
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Change the CPU resource required for PVC controller deployment
	expectedCPU := "300m"
	cluster.Annotations[annotationPVCControllerCPU] = expectedCPU

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, deployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestPVCControllerInvalidCPU(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), recorder)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController:    "true",
				annotationPVCControllerCPU: "invalid-cpu",
			},
		},
	}

	// Should not return error but raise an event instead
	err := driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup %v.", v1.EventTypeWarning,
			util.FailedComponentReason, component.PVCControllerComponentName))
}

func TestPVCControllerRollbackImageChanges(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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

func TestPVCControllerRollbackCommandChanges(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:2.1.1",
			},
		},
	}

	err := driver.PreInstall(cluster)

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
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
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
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsAKS: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err := driver.PreInstall(cluster)

	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.LhServiceName, lhService.Name)
	require.Equal(t, cluster.Namespace, lhService.Namespace)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseServiceTypeForGKE(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsGKE: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err := driver.PreInstall(cluster)

	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.LhServiceName, lhService.Name)
	require.Equal(t, cluster.Namespace, lhService.Namespace)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseServiceTypeForEKS(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsEKS: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err := driver.PreInstall(cluster)

	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, component.LhServiceName, lhService.Name)
	require.Equal(t, cluster.Namespace, lhService.Namespace)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseServiceTypeWithOverride(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsAKS:       "true",
				annotationIsGKE:       "true",
				annotationIsEKS:       "true",
				annotationServiceType: "ClusterIP",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService := &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeClusterIP, lhService.Spec.Type)

	// Load balancer service type
	cluster.Annotations[annotationServiceType] = "LoadBalancer"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)

	// Node port service type
	cluster.Annotations[annotationServiceType] = "NodePort"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, lhService.Spec.Type)

	// Invalid type should use the default service type
	cluster.Annotations[annotationServiceType] = "Invalid"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	lhService = &v1.Service{}
	err = testutil.Get(k8sClient, lhService, component.LhServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, lhService.Spec.Type)
}

func TestLighthouseWithoutImage(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), recorder)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
			},
		},
	}

	// Should not return an error, instead should raise an event
	err := driver.PreInstall(cluster)
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

func TestLighthouseImageChange(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:v1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "portworx/px-lighthouse:v1", image)

	// Change the lighthouse image
	cluster.Spec.UserInterface.Image = "portworx/px-lighthouse:v2"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "portworx/px-lighthouse:v2", image)
}

func TestLighthouseConfigInitImageChange(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:v1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName)
	require.Equal(t, "portworx/lh-config-sync:v1", image)

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
	require.Equal(t, "test/config-sync:v2", image)
}

func TestLighthouseStorkConnectorImageChange(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:v1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName)
	require.Equal(t, "portworx/lh-stork-connector:v1", image)

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
	require.Equal(t, "test/stork-connector:v2", image)
}

func TestLighthouseWithoutImageTag(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	// Lighthouse image should remain unchanged but sidecar container images
	// should use the default image tag
	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "portworx/px-lighthouse", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName)
	require.Equal(t, "portworx/lh-config-sync:2.0.4", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName)
	require.Equal(t, "portworx/lh-config-sync:2.0.4", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName)
	require.Equal(t, "portworx/lh-stork-connector:2.0.4", image)
}

func TestLighthouseSidecarsOverrideWithEnv(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse",
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

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	// Lighthouse image should remain unchanged but sidecar container images
	// should use the default image tag
	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName)
	require.Equal(t, "portworx/px-lighthouse", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName)
	require.Equal(t, "test/config-sync:t1", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName)
	require.Equal(t, "test/config-sync:t1", image)
	image = k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName)
	require.Equal(t, "test/stork-connector:t2", image)
}

func TestAutopilotInstall(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:1.1.1",
				Providers: []corev1alpha1.DataProviderSpec{
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
				Args: map[string]string{
					"min_poll_interval": "4",
					"log-level":         "debug",
				},
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Autopilot ClusterRoleBinding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "autopilotClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.AutopilotClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Autopilot Deployment
	expectedDeployment := testutil.GetExpectedDeployment(t, "autopilotDeployment.yaml")
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, autopilotDeployment.Name)
	require.Equal(t, expectedDeployment.Namespace, autopilotDeployment.Namespace)
	require.Len(t, autopilotDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, autopilotDeployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Labels, autopilotDeployment.Labels)
	require.Equal(t, expectedDeployment.Annotations, autopilotDeployment.Annotations)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	autopilotDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedDeployment.Spec, autopilotDeployment.Spec)
}

func TestAutopilotWithoutImage(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), recorder)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
			},
		},
	}

	// Should not return an error, instead should raise an event
	err := driver.PreInstall(cluster)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(0)
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), recorder)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
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

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, autopilotDeployment.Spec.Template.Spec.Containers[0].Env, 2)
	// Env vars are sorted on the key
	require.Equal(t, "BAR", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[0].Name)
	require.Equal(t, "bar", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[0].Value)
	require.Equal(t, "FOO", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[1].Name)
	require.Equal(t, "foo", autopilotDeployment.Spec.Template.Spec.Containers[0].Env[1].Value)
}

func TestAutopilotImageChange(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image := k8sutil.GetImageFromDeployment(autopilotDeployment, component.AutopilotContainerName)
	require.Equal(t, "portworx/autopilot:v1", image)

	// Change the autopilot image
	cluster.Spec.Autopilot.Image = "portworx/autopilot:v2"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	image = k8sutil.GetImageFromDeployment(autopilotDeployment, component.AutopilotContainerName)
	require.Equal(t, "portworx/autopilot:v2", image)
}

func TestAutopilotArgumentsChange(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
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

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	// Check env vars are passed to deployment
	expectedEnvs := append([]v1.EnvVar{}, cluster.Spec.Autopilot.Env...)
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, expectedEnvs, autopilotDeployment.Spec.Template.Spec.Containers[0].Env)

	// Overwrite existing env vars
	cluster.Spec.Autopilot.Env[0].Value = "bar"

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedEnvs[0].Value = "bar"
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

func TestAutopilotCPUChange(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	cluster.Annotations = map[string]string{annotationAutopilotCPU: expectedCPU}

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(
		autopilotDeployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestAutopilotInvalidCPU(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(10)
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), recorder)

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationAutopilotCPU: "invalid-cpu",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:v1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Autopilot.", v1.EventTypeWarning, util.FailedComponentReason))
}

func TestCSIInstall(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.11.4",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.1.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			Placement: &corev1alpha1.PlacementSpec{
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

	err := driver.PreInstall(cluster)
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
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
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
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// CSI Service
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Len(t, serviceList.Items, 3)

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

func TestCSIInstallWithOlderPortworxVersion(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.4",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.0.3",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			Placement: &corev1alpha1.PlacementSpec{
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

	err := driver.PreInstall(cluster)
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

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.13.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
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
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// CSI Service
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Len(t, serviceList.Items, 3)

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

func TestCSIInstallWithNewerCSIVersion(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.1.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			Placement: &corev1alpha1.PlacementSpec{
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

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.13.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
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
	require.Len(t, csiDriver.OwnerReferences, 1)
	require.Equal(t, cluster.Name, csiDriver.OwnerReferences[0].Name)
	require.False(t, *csiDriver.Spec.AttachRequired)
	require.False(t, *csiDriver.Spec.PodInfoOnMount)

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.DeprecatedCSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallShouldCreateNodeInfoCRD(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	extensionsClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	k8s.Instance().SetAPIExtensionsClient(extensionsClient)
	// CSINodeInfo CRD should be created for k8s version 1.12.*
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.1.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	expectedCRD := testutil.GetExpectedCRD(t, "csiNodeInfoCrd.yaml")
	go func() {
		err := testutil.ActivateCRDWhenCreated(extensionsClient, expectedCRD.Name)
		require.NoError(t, err)
	}()

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	actualCRD, err := extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(expectedCRD.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, expectedCRD.Name, actualCRD.Name)
	require.Equal(t, expectedCRD.Labels, actualCRD.Labels)
	require.Equal(t, expectedCRD.Spec, actualCRD.Spec)

	// Expect the CRD to be created even for k8s version 1.13.*
	err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Delete(expectedCRD.Name, nil)
	require.NoError(t, err)
	csiComponent, _ := component.Get(component.CSIComponentName)
	csiComponent.MarkDeleted()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.99",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	go func() {
		err := testutil.ActivateCRDWhenCreated(extensionsClient, expectedCRD.Name)
		require.NoError(t, err)
	}()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	actualCRD, err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(expectedCRD.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, expectedCRD.Name, actualCRD.Name)
	require.Equal(t, expectedCRD.Labels, actualCRD.Labels)
	require.Equal(t, expectedCRD.Spec, actualCRD.Spec)

	// CRD should not to be created for k8s version 1.11.* or below
	err = extensionsClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Delete(expectedCRD.Name, nil)
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
		Get(expectedCRD.Name, metav1.GetOptions{})
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
		Get(expectedCRD.Name, metav1.GetOptions{})
	require.True(t, errors.IsNotFound(err))
}

func TestCSIInstallWithDeprecatedCSIDriverName(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	// Install with Portworx version 2.2+ and deprecated CSI driver name
	// We should use add the resizer sidecar and use the old driver name.
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_USEDEPRECATED_CSIDRIVERNAME",
						Value: "true",
					},
				},
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	require.Len(t, csiDriver.OwnerReferences, 1)
	require.Equal(t, cluster.Name, csiDriver.OwnerReferences[0].Name)
	require.False(t, *csiDriver.Spec.AttachRequired)
	require.False(t, *csiDriver.Spec.PodInfoOnMount)

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, pxutil.CSIDriverName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestCSIClusterRoleK8sVersionGreaterThan_1_14(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	expectedCR := testutil.GetExpectedClusterRole(t, "csiClusterRole_k8s_1.14.yaml")
	actualCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, actualCR, component.CSIClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, actualCR.Name)
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)
}

func TestCSI_1_0_ChangeImageVersions(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t, "quay.io/openstorage/csi-provisioner:v1.4.0-1",
		deployment.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "quay.io/openstorage/csi-attacher:v1.2.1-1",
		deployment.Spec.Template.Spec.Containers[1].Image)
	require.Equal(t, "quay.io/openstorage/csi-snapshotter:v1.2.2-1",
		deployment.Spec.Template.Spec.Containers[2].Image)

	// Change provisioner image
	deployment.Spec.Template.Spec.Containers[0].Image = "my-csi-provisioner:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/openstorage/csi-provisioner:v1.4.0-1",
		deployment.Spec.Template.Spec.Containers[0].Image)

	// Change attacher image
	deployment.Spec.Template.Spec.Containers[1].Image = "my-csi-attacher:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/openstorage/csi-attacher:v1.2.1-1",
		deployment.Spec.Template.Spec.Containers[1].Image)

	// Change snapshotter image
	deployment.Spec.Template.Spec.Containers[2].Image = "my-csi-snapshotter:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/openstorage/csi-snapshotter:v1.2.2-1",
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
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v0.3.0",
		deployment.Spec.Template.Spec.Containers[2].Image)

	deployment.Spec.Template.Spec.Containers[2].Image = "my-csi-resizer:test"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v0.3.0",
		deployment.Spec.Template.Spec.Containers[2].Image)
}

func TestCSI_0_3_ChangeImageVersions(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.1.0",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	statefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, statefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v0.4.3",
		statefulSet.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v0.4.2",
		statefulSet.Spec.Template.Spec.Containers[1].Image)

	// Change provisioner image
	statefulSet.Spec.Template.Spec.Containers[0].Image = "my-csi-provisioner:test"
	err = k8sClient.Update(context.TODO(), statefulSet)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v0.4.3",
		statefulSet.Spec.Template.Spec.Containers[0].Image)

	// Change attacher image
	statefulSet.Spec.Template.Spec.Containers[1].Image = "my-csi-attacher:test"
	err = k8sClient.Update(context.TODO(), statefulSet)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v0.4.2",
		statefulSet.Spec.Template.Spec.Containers[1].Image)
}

func TestCSIChangeKubernetesVersions(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/image:2.2",
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	statefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, statefulSet.Spec.Template.Spec.Containers, 2)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v0.4.3",
		statefulSet.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v0.4.2",
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
	require.Equal(t, "quay.io/openstorage/csi-provisioner:v1.4.0-1",
		deployment.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "--leader-election-type=endpoints",
		deployment.Spec.Template.Spec.Containers[0].Args[4])
	require.Equal(t, "quay.io/openstorage/csi-attacher:v1.2.1-1",
		deployment.Spec.Template.Spec.Containers[1].Image)
	require.Equal(t, "--leader-election-type=configmaps",
		deployment.Spec.Template.Spec.Containers[1].Args[3])
	require.Equal(t, "quay.io/openstorage/csi-snapshotter:v1.2.2-1",
		deployment.Spec.Template.Spec.Containers[2].Image)
	require.Equal(t, "--leader-election-type=configmaps",
		deployment.Spec.Template.Spec.Containers[2].Args[4])

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
	require.Equal(t, "quay.io/openstorage/csi-provisioner:v1.4.0-1",
		deployment.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, "--leader-election-type=leases",
		deployment.Spec.Template.Spec.Containers[0].Args[4])
	require.Equal(t, "quay.io/openstorage/csi-snapshotter:v1.2.2-1",
		deployment.Spec.Template.Spec.Containers[1].Image)
	require.Equal(t, "--leader-election-type=leases",
		deployment.Spec.Template.Spec.Containers[1].Args[4])
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v0.3.0",
		deployment.Spec.Template.Spec.Containers[2].Image)
}

func TestPrometheusInstall(t *testing.T) {
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					Enabled: true,
				},
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Prometheus operator ClusterRoleBinding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "prometheusOperatorClusterRoleBinding.yaml")
	actualCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.PrometheusOperatorClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCRB.Subjects, actualCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, actualCRB.RoleRef)

	// Prometheus operator Deployment
	expectedDeployment := testutil.GetExpectedDeployment(t, "prometheusOperatorDeployment.yaml")
	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedDeployment.Name, autopilotDeployment.Name)
	require.Equal(t, expectedDeployment.Namespace, autopilotDeployment.Namespace)
	require.Len(t, autopilotDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, autopilotDeployment.OwnerReferences[0].Name)
	require.Equal(t, expectedDeployment.Labels, autopilotDeployment.Labels)
	require.Equal(t, expectedDeployment.Annotations, autopilotDeployment.Annotations)
	require.Equal(t, expectedDeployment.Spec, autopilotDeployment.Spec)

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
	require.Len(t, actualCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCR.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedCR.Rules, actualCR.Rules)

	// Prometheus ClusterRoleBinding
	expectedCRB = testutil.GetExpectedClusterRoleBinding(t, "prometheusClusterRoleBinding.yaml")
	actualCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, actualCRB, component.PrometheusClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, actualCRB.Name)
	require.Len(t, actualCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, actualCRB.OwnerReferences[0].Name)
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
}

func TestCompleteInstallWithCustomRepoRegistry(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image:               "portworx/image:2.2",
			CustomImageRegistry: customRepo,
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					Enabled: true,
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/pause:3.1", pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-controller-manager-amd64:v1.13.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/px-lighthouse:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-stork-connector:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRepo+"/lh-config-sync:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/autopilot:test",
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	parts := strings.Split(component.DefaultPrometheusOperatorImage, "/")
	expectedPrometheusImage := parts[len(parts)-1]
	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/"+expectedPrometheusImage,
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRepo+"/csi-provisioner:v1.4.0-1",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-attacher:v1.2.1-1",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRepo+"/csi-snapshotter:v1.2.2-1",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
	)

	// Change the k8s version to 1.14, so that resizer sidecar is deployed
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.6",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	driver.initializeComponents()

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/csi-resizer:v0.3.0",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
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
	require.Equal(t,
		customRepo+"/csi-provisioner:v0.4.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRepo+"/csi-attacher:v0.4.2",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)
}

func TestCompleteInstallWithCustomRegistry(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	customRegistry := "test-registry:1111"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image:               "portworx/image:2.2",
			CustomImageRegistry: customRegistry,
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					Enabled: true,
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/k8s.gcr.io/pause:3.1",
		pxAPIDaemonSet.Spec.Template.Spec.Containers[0].Image,
	)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-controller-manager-amd64:v1.13.0",
		pvcDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/px-lighthouse:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigSyncContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-stork-connector:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhStorkConnectorContainerName),
	)
	require.Equal(t,
		customRegistry+"/portworx/lh-config-sync:test",
		k8sutil.GetImageFromDeployment(lhDeployment, component.LhConfigInitContainerName),
	)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/portworx/autopilot:test",
		autopilotDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/"+component.DefaultPrometheusOperatorImage,
		prometheusOperatorDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t,
		customRegistry+"/quay.io/openstorage/csi-provisioner:v1.4.0-1",
		csiDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/quay.io/openstorage/csi-attacher:v1.2.1-1",
		csiDeployment.Spec.Template.Spec.Containers[1].Image,
	)
	require.Equal(t,
		customRegistry+"/quay.io/openstorage/csi-snapshotter:v1.2.2-1",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
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
	require.Equal(t,
		customRegistry+"/quay.io/k8scsi/csi-resizer:v0.3.0",
		csiDeployment.Spec.Template.Spec.Containers[2].Image,
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
	require.Equal(t,
		customRegistry+"/quay.io/k8scsi/csi-provisioner:v0.4.3",
		csiStatefulSet.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		customRegistry+"/quay.io/k8scsi/csi-attacher:v0.4.2",
		csiStatefulSet.Spec.Template.Spec.Containers[1].Image,
	)
}

func TestCompleteInstallWithImagePullPolicy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image:           "portworx/image:2.2",
			ImagePullPolicy: v1.PullIfNotPresent,
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					Enabled: true,
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "1",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	require.Len(t, csiDeployment.Spec.Template.Spec.Containers, 3)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[1].ImagePullPolicy,
	)
	require.Equal(t, v1.PullIfNotPresent,
		csiDeployment.Spec.Template.Spec.Containers[2].ImagePullPolicy,
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

func TestCompleteInstallWithImagePullSecret(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	k8s.Instance().SetAPIExtensionsClient(fakeExtClient)
	createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	imagePullSecret := "registry-secret"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image:           "portworx/image:2.2",
			ImagePullSecret: &imagePullSecret,
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					Enabled: true,
				},
			},
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "1",
			},
		},
	}

	err := driver.PreInstall(cluster)
	require.NoError(t, err)

	pxAPIDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		imagePullSecret,
		pxAPIDaemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)

	pvcDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, component.PVCDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		imagePullSecret,
		pvcDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)

	lhDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, lhDeployment, component.LhDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		imagePullSecret,
		lhDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)

	autopilotDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, component.AutopilotDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		imagePullSecret,
		autopilotDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)

	prometheusOperatorDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, prometheusOperatorDeployment, component.PrometheusOperatorDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		imagePullSecret,
		prometheusOperatorDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)

	csiDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, component.CSIApplicationName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		imagePullSecret,
		csiDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name,
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
	require.Equal(t,
		imagePullSecret,
		csiStatefulSet.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)
}

func TestRemovePVCController(t *testing.T) {
	// Set fake kubernetes client for k8s version
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)

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
	delete(cluster.Annotations, annotationPVCController)
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationPVCController: "true",
			},
		},
	}

	err := driver.PreInstall(cluster)

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
	cluster.Annotations[annotationPVCController] = "false"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PVCServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err := driver.PreInstall(cluster)

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

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.LhServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			UserInterface: &corev1alpha1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
		},
	}

	err := driver.PreInstall(cluster)

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

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.LhServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Autopilot: &corev1alpha1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
		},
	}

	err := driver.PreInstall(cluster)

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
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.11.4",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	cluster.Spec.FeatureGates[string(pxutil.FeatureCSI)] = "false"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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
	k8s.Instance().SetBaseClient(versionClient)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.14.0",
	}
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			FeatureGates: map[string]string{
				string(pxutil.FeatureCSI): "true",
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	cluster.Spec.FeatureGates[string(pxutil.FeatureCSI)] = "false"
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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

	// Keep the service account
	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.CSIServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					ExportMetrics: true,
				},
			},
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: false,
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					ExportMetrics: true,
				},
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					Enabled: true,
				},
			},
		},
	}

	err := driver.PreInstall(cluster)
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
	driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Monitoring: &corev1alpha1.MonitoringSpec{
				Prometheus: &corev1alpha1.PrometheusSpec{
					Enabled: true,
				},
			},
		},
	}

	err := driver.PreInstall(cluster)
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

func createFakeCRD(fakeClient *fakeextclient.Clientset, crdName string) error {
	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
	}
	_, err := fakeClient.ApiextensionsV1beta1().CustomResourceDefinitions().Create(crd)
	if err != nil {
		return err
	}
	return testutil.ActivateCRDWhenCreated(fakeClient, crdName)
}

func reregisterComponents() {
	// Not registering PortworxCRDs component to avoid creating CRD
	// for every test as we do not need to test it's creation every time.
	component.DeregisterAllComponents()
	component.RegisterPortworxBasicComponent()
	component.RegisterPortworxAPIComponent()
	component.RegisterPortworxStorageClassComponent()
	component.RegisterAutopilotComponent()
	component.RegisterCSIComponent()
	component.RegisterLighthouseComponent()
	component.RegisterPVCControllerComponent()
	component.RegisterMonitoringComponent()
	component.RegisterPrometheusComponent()
}
