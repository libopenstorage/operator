package k8s

import (
	"context"
	"testing"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	kversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))

	// Valid version
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "v1.2.3",
	}
	actualVersion, err := GetVersion()
	require.NoError(t, err)
	require.Equal(t, "1.2.3", actualVersion.String())

	// Invalid version
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "invalid",
	}
	actualVersion, err = GetVersion()
	require.EqualError(t, err, "invalid kubernetes version received: invalid")
	require.Nil(t, actualVersion)
}

func TestDeleteServiceAccount(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the service account is not present
	err := DeleteServiceAccount(k8sClient, "not-present-sa", namespace)
	require.NoError(t, err)

	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, sa)

	// Don't delete when there is no owner in the service account
	// but trying to delete for specific owners
	err = DeleteServiceAccount(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, sa)

	// Delete when there is no owner in the service account
	err = DeleteServiceAccount(k8sClient, name, namespace)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the service account is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteServiceAccount(k8sClient, name, namespace)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, sa)

	// Don't delete when the service account is owned by objects
	// more than what are passed on delete call
	err = DeleteServiceAccount(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, name, namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), sa.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), sa.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the service account
	err = DeleteServiceAccount(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteRole(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the role is not present
	err := DeleteRole(k8sClient, "not-present-role", namespace)
	require.NoError(t, err)

	role := &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, role)

	// Don't delete when there is no owner in the role
	// but trying to delete for specific owners
	err = DeleteRole(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, role)

	// Delete when there is no owner in the role
	err = DeleteRole(k8sClient, name, namespace)
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the role is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteRole(k8sClient, name, namespace)
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, role)

	// Don't delete when the role is owned by objects
	// more than what are passed on delete call
	err = DeleteRole(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, name, namespace)
	require.NoError(t, err)
	require.Len(t, role.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), role.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), role.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the role
	err = DeleteRole(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = testutil.Get(k8sClient, role, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteRoleBinding(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the role binding is not present
	err := DeleteRoleBinding(k8sClient, "not-present-role-binding", namespace)
	require.NoError(t, err)

	roleBinding := &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, roleBinding)

	// Don't delete when there is no owner in the role binding
	// but trying to delete for specific owners
	err = DeleteRoleBinding(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, roleBinding)

	// Delete when there is no owner in the role binding
	err = DeleteRoleBinding(k8sClient, name, namespace)
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the role binding is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteRoleBinding(k8sClient, name, namespace)
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, roleBinding)

	// Don't delete when the role binding is owned by objects
	// more than what are passed on delete call
	err = DeleteRoleBinding(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, name, namespace)
	require.NoError(t, err)
	require.Len(t, roleBinding.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), roleBinding.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), roleBinding.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the role binding
	err = DeleteRoleBinding(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, roleBinding, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteClusterRole(t *testing.T) {
	name := "test"
	expected := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the cluster role is not present
	err := DeleteClusterRole(k8sClient, "not-present-cluster-role")
	require.NoError(t, err)

	clusterRole := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, clusterRole, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, clusterRole)

	// Delete when there is no owner in the cluster role
	err = DeleteClusterRole(k8sClient, name)
	require.NoError(t, err)

	clusterRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, clusterRole, name, "")
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the cluster role is owned by an object
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteClusterRole(k8sClient, name)
	require.NoError(t, err)

	clusterRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, clusterRole, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, clusterRole)
}

func TestDeleteClusterRoleBinding(t *testing.T) {
	name := "test"
	expected := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the cluster role binding is not present
	err := DeleteClusterRoleBinding(k8sClient, "not-present-crb")
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, crb)

	// Delete when there is no owner in the cluster role binding
	err = DeleteClusterRoleBinding(k8sClient, name)
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, name, "")
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the cluster role binding is owned by an object
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteClusterRoleBinding(k8sClient, name)
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, crb, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, crb)
}

func TestCreateStorageClass(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	expectedStorageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Provisioner: "foo",
	}

	err := CreateStorageClass(k8sClient, expectedStorageClass)
	require.NoError(t, err)

	actualStorageClass := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualStorageClass, "test", "")
	require.NoError(t, err)
	require.Equal(t, "foo", actualStorageClass.Provisioner)

	// Trying to create again will not create again and not return an error
	expectedStorageClass.Provisioner = "bar"

	err = CreateStorageClass(k8sClient, expectedStorageClass)
	require.NoError(t, err)

	actualStorageClass = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, actualStorageClass, "test", "")
	require.NoError(t, err)
	require.Equal(t, "foo", actualStorageClass.Provisioner)
}

func TestDeleteStorageClass(t *testing.T) {
	name := "test"
	expected := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the storage class is not present
	err := DeleteStorageClass(k8sClient, "not-present-storage-class")
	require.NoError(t, err)

	storageClass := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storageClass, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, storageClass)

	// Delete when there is no owner in the storage class
	err = DeleteStorageClass(k8sClient, name)
	require.NoError(t, err)

	storageClass = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storageClass, name, "")
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the storage class is owned by an object
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteStorageClass(k8sClient, name)
	require.NoError(t, err)

	storageClass = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storageClass, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, storageClass)
}

func TestDeleteConfigMap(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the config map is not present
	err := DeleteConfigMap(k8sClient, "not-present-config-map", namespace)
	require.NoError(t, err)

	configMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, configMap)

	// Don't delete when there is no owner in the config map
	// but trying to delete for specific owners
	err = DeleteConfigMap(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, configMap)

	// Delete when there is no owner in the config map
	err = DeleteConfigMap(k8sClient, name, namespace)
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the config map is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteConfigMap(k8sClient, name, namespace)
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, configMap)

	// Don't delete when the config map is owned by objects
	// more than what are passed on delete call
	err = DeleteConfigMap(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, name, namespace)
	require.NoError(t, err)
	require.Len(t, configMap.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), configMap.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), configMap.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the config map
	err = DeleteConfigMap(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, configMap, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteCSIDriver(t *testing.T) {
	name := "test"
	expected := &storagev1beta1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the CSI driver is not present
	err := DeleteCSIDriver(k8sClient, "not-present-csi-driver")
	require.NoError(t, err)

	csiDriver := &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, csiDriver)

	// Delete when there is no owner in the CSI driver
	err = DeleteCSIDriver(k8sClient, name)
	require.NoError(t, err)

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, name, "")
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the CSI driver is owned by an object
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteCSIDriver(k8sClient, name)
	require.NoError(t, err)

	csiDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, csiDriver, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, csiDriver)
}

func TestDeleteService(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the service is not present
	err := DeleteService(k8sClient, "not-present-service", namespace)
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(k8sClient, service, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, service)

	// Don't delete when there is no owner in the service
	// but trying to delete for specific owners
	err = DeleteService(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, service)

	// Delete when there is no owner in the service
	err = DeleteService(k8sClient, name, namespace)
	require.NoError(t, err)

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the service is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteService(k8sClient, name, namespace)
	require.NoError(t, err)

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, service)

	// Don't delete when the service is owned by objects
	// more than what are passed on delete call
	err = DeleteService(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, name, namespace)
	require.NoError(t, err)
	require.Len(t, service.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), service.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), service.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the service
	err = DeleteService(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	service = &v1.Service{}
	err = testutil.Get(k8sClient, service, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteDeployment(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the deployment is not present
	err := DeleteDeployment(k8sClient, "not-present-deployment", namespace)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, deployment)

	// Don't delete when there is no owner in the deployment
	// but trying to delete for specific owners
	err = DeleteDeployment(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, deployment)

	// Delete when there is no owner in the deployment
	err = DeleteDeployment(k8sClient, name, namespace)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the deployment is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteDeployment(k8sClient, name, namespace)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, deployment)

	// Don't delete when the deployment is owned by objects
	// more than what are passed on delete call
	err = DeleteDeployment(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, name, namespace)
	require.NoError(t, err)
	require.Len(t, deployment.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), deployment.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), deployment.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the deployment
	err = DeleteDeployment(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteStatefulSet(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the stateful set is not present
	err := DeleteStatefulSet(k8sClient, "not-present-stateful-set", namespace)
	require.NoError(t, err)

	statefulSet := &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, statefulSet)

	// Don't delete when there is no owner in the stateful set
	// but trying to delete for specific owners
	err = DeleteStatefulSet(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, statefulSet)

	// Delete when there is no owner in the stateful set
	err = DeleteStatefulSet(k8sClient, name, namespace)
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the stateful set is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteStatefulSet(k8sClient, name, namespace)
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, statefulSet)

	// Don't delete when the stateful set is owned by objects
	// more than what are passed on delete call
	err = DeleteStatefulSet(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, name, namespace)
	require.NoError(t, err)
	require.Len(t, statefulSet.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), statefulSet.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), statefulSet.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the stateful set
	err = DeleteStatefulSet(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = testutil.Get(k8sClient, statefulSet, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteDaemonSet(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := fake.NewFakeClient(expected)

	// Don't delete or throw error if the daemonset is not present
	err := DeleteDaemonSet(k8sClient, "not-present-daemonset", namespace)
	require.NoError(t, err)

	daemonset := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, daemonset)

	// Don't delete when there is no owner in the daemonset
	// but trying to delete for specific owners
	err = DeleteDaemonSet(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	daemonset = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, daemonset)

	// Delete when there is no owner in the daemonset
	err = DeleteDaemonSet(k8sClient, name, namespace)
	require.NoError(t, err)

	daemonset = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the daemonset is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteDaemonSet(k8sClient, name, namespace)
	require.NoError(t, err)

	daemonset = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, daemonset)

	// Don't delete when the daemonset is owned by objects
	// more than what are passed on delete call
	err = DeleteDaemonSet(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	daemonset = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, name, namespace)
	require.NoError(t, err)
	require.Len(t, daemonset.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), daemonset.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), daemonset.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the daemonset
	err = DeleteDaemonSet(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	daemonset = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, daemonset, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestUpdateStorageClusterStatus(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			ResourceVersion: "200",
		},
		Status: corev1alpha1.StorageClusterStatus{
			Phase: "Install",
		},
	}

	// Fail if cluster is not present
	err := UpdateStorageClusterStatus(k8sClient, cluster)
	require.True(t, errors.IsNotFound(err))

	err = k8sClient.Create(context.TODO(), cluster)
	require.NoError(t, err)

	// Should keep the latest resource version on update even if input object is old
	cluster.Status.Phase = "Update"
	cluster.ResourceVersion = "100"
	err = UpdateStorageClusterStatus(k8sClient, cluster)
	require.NoError(t, err)

	actualCluster := &corev1alpha1.StorageCluster{}
	err = testutil.Get(k8sClient, actualCluster, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "Update", actualCluster.Status.Phase)
	require.Equal(t, "200", actualCluster.ResourceVersion)
}

func TestStorageNodeChangeSpec(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	expectedNode := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			ResourceVersion: "200",
		},
		Spec: corev1alpha1.StorageNodeSpec{
			Version: "1.0.0",
		},
		Status: corev1alpha1.NodeStatus{
			Phase: "Running",
		},
	}

	err := CreateOrUpdateStorageNode(k8sClient, expectedNode, nil)
	require.NoError(t, err)

	actualNode := &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, actualNode, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "1.0.0", actualNode.Spec.Version)

	// Change spec
	expectedNode.Spec.Version = "2.0.0"
	expectedNode.ResourceVersion = "100"

	err = CreateOrUpdateStorageNode(k8sClient, expectedNode, nil)
	require.NoError(t, err)

	actualNode = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, actualNode, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "2.0.0", actualNode.Spec.Version)
	require.Equal(t, "200", actualNode.ResourceVersion)

	// Change status
	expectedNode.Status.Phase = "Failed"

	err = CreateOrUpdateStorageNode(k8sClient, expectedNode, nil)
	require.NoError(t, err)

	actualNode = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, actualNode, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "Failed", actualNode.Status.Phase)
}

func TestStorageNodeWithOwnerReferences(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()

	firstOwner := metav1.OwnerReference{UID: "first-owner"}
	expectedNode := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			OwnerReferences: []metav1.OwnerReference{firstOwner},
		},
	}

	err := CreateOrUpdateStorageNode(k8sClient, expectedNode, nil)
	require.NoError(t, err)

	actualNode := &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, actualNode, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualNode.OwnerReferences)

	// Update with the same owner. Nothing should change as owner hasn't changed.
	err = CreateOrUpdateStorageNode(k8sClient, expectedNode, &firstOwner)
	require.NoError(t, err)

	actualNode = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, actualNode, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualNode.OwnerReferences)

	// Update with a new owner.
	secondOwner := metav1.OwnerReference{UID: "second-owner"}
	expectedNode.OwnerReferences = []metav1.OwnerReference{secondOwner}

	err = CreateOrUpdateStorageNode(k8sClient, expectedNode, &secondOwner)
	require.NoError(t, err)

	actualNode = &corev1alpha1.StorageNode{}
	err = testutil.Get(k8sClient, actualNode, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{secondOwner, firstOwner}, actualNode.OwnerReferences)
}

func TestServiceMonitorChangeSpec(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	expectedMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			NamespaceSelector: monitoringv1.NamespaceSelector{
				Any: true,
			},
		},
	}

	err := CreateOrUpdateServiceMonitor(k8sClient, expectedMonitor, nil)
	require.NoError(t, err)

	actualMonitor := &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, actualMonitor, "test", "test-ns")
	require.NoError(t, err)
	require.True(t, actualMonitor.Spec.NamespaceSelector.Any)

	// Change spec
	expectedMonitor.Spec.NamespaceSelector.Any = false

	err = CreateOrUpdateServiceMonitor(k8sClient, expectedMonitor, nil)
	require.NoError(t, err)

	actualMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, actualMonitor, "test", "test-ns")
	require.NoError(t, err)
	require.False(t, actualMonitor.Spec.NamespaceSelector.Any)
}

func TestServiceMonitorWithOwnerReferences(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()

	firstOwner := metav1.OwnerReference{UID: "first-owner"}
	expectedMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			OwnerReferences: []metav1.OwnerReference{firstOwner},
		},
	}

	err := CreateOrUpdateServiceMonitor(k8sClient, expectedMonitor, nil)
	require.NoError(t, err)

	actualMonitor := &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, actualMonitor, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualMonitor.OwnerReferences)

	// Update with the same owner. Nothing should change as owner hasn't changed.
	err = CreateOrUpdateServiceMonitor(k8sClient, expectedMonitor, &firstOwner)
	require.NoError(t, err)

	actualMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, actualMonitor, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualMonitor.OwnerReferences)

	// Update with a new owner.
	secondOwner := metav1.OwnerReference{UID: "second-owner"}
	expectedMonitor.OwnerReferences = []metav1.OwnerReference{secondOwner}

	err = CreateOrUpdateServiceMonitor(k8sClient, expectedMonitor, &secondOwner)
	require.NoError(t, err)

	actualMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, actualMonitor, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{secondOwner, firstOwner}, actualMonitor.OwnerReferences)
}

func TestDeleteServiceMonitor(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(expected)

	// Don't delete or throw error if the service monitor is not present
	err := DeleteServiceMonitor(k8sClient, "not-present-service-monitor", namespace)
	require.NoError(t, err)

	serviceMonitor := &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, serviceMonitor)

	// Don't delete when there is no owner in the service monitor
	// but trying to delete for specific owners
	err = DeleteServiceMonitor(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, serviceMonitor)

	// Delete when there is no owner in the service monitor
	err = DeleteServiceMonitor(k8sClient, name, namespace)
	require.NoError(t, err)

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the service monitor is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteServiceMonitor(k8sClient, name, namespace)
	require.NoError(t, err)

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, serviceMonitor)

	// Don't delete when the service monitor is owned by objects
	// more than what are passed on delete call
	err = DeleteServiceMonitor(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, name, namespace)
	require.NoError(t, err)
	require.Len(t, serviceMonitor.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), serviceMonitor.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), serviceMonitor.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the service monitor
	err = DeleteServiceMonitor(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestPrometheusChangeSpec(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	expectedPrometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: monitoringv1.PrometheusSpec{
			Tag: "foo",
		},
	}

	err := CreateOrUpdatePrometheus(k8sClient, expectedPrometheus, nil)
	require.NoError(t, err)

	actualPrometheus := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, actualPrometheus, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "foo", actualPrometheus.Spec.Tag)

	// Change spec
	expectedPrometheus.Spec.Tag = "bar"

	err = CreateOrUpdatePrometheus(k8sClient, expectedPrometheus, nil)
	require.NoError(t, err)

	actualPrometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, actualPrometheus, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "bar", actualPrometheus.Spec.Tag)
}

func TestPrometheusWithOwnerReferences(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()

	firstOwner := metav1.OwnerReference{UID: "first-owner"}
	expectedPrometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			OwnerReferences: []metav1.OwnerReference{firstOwner},
		},
	}

	err := CreateOrUpdatePrometheus(k8sClient, expectedPrometheus, nil)
	require.NoError(t, err)

	actualPrometheus := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, actualPrometheus, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualPrometheus.OwnerReferences)

	// Update with the same owner. Nothing should change as owner hasn't changed.
	err = CreateOrUpdatePrometheus(k8sClient, expectedPrometheus, &firstOwner)
	require.NoError(t, err)

	actualPrometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, actualPrometheus, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualPrometheus.OwnerReferences)

	// Update with a new owner.
	secondOwner := metav1.OwnerReference{UID: "second-owner"}
	expectedPrometheus.OwnerReferences = []metav1.OwnerReference{secondOwner}

	err = CreateOrUpdatePrometheus(k8sClient, expectedPrometheus, &secondOwner)
	require.NoError(t, err)

	actualPrometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, actualPrometheus, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{secondOwner, firstOwner}, actualPrometheus.OwnerReferences)
}

func TestDeletePrometheus(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(expected)

	// Don't delete or throw error if the prometheus is not present
	err := DeletePrometheus(k8sClient, "not-present-prometheus", namespace)
	require.NoError(t, err)

	prometheus := &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, prometheus)

	// Don't delete when there is no owner in the prometheus
	// but trying to delete for specific owners
	err = DeletePrometheus(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, prometheus)

	// Delete when there is no owner in the prometheus
	err = DeletePrometheus(k8sClient, name, namespace)
	require.NoError(t, err)

	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the prometheus is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeletePrometheus(k8sClient, name, namespace)
	require.NoError(t, err)

	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, prometheus)

	// Don't delete when the prometheus is owned by objects
	// more than what are passed on delete call
	err = DeletePrometheus(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, name, namespace)
	require.NoError(t, err)
	require.Len(t, prometheus.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), prometheus.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), prometheus.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the prometheus
	err = DeletePrometheus(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestPrometheusRuleChangeSpec(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	expectedRule := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: monitoringv1.PrometheusRuleSpec{
			Groups: []monitoringv1.RuleGroup{
				{
					Name: "group-1",
				},
			},
		},
	}

	err := CreateOrUpdatePrometheusRule(k8sClient, expectedRule, nil)
	require.NoError(t, err)

	actualRule := &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, actualRule, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "group-1", actualRule.Spec.Groups[0].Name)

	// Change spec
	expectedRule.Spec.Groups[0].Name = "group-2"

	err = CreateOrUpdatePrometheusRule(k8sClient, expectedRule, nil)
	require.NoError(t, err)

	actualRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, actualRule, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, "group-2", actualRule.Spec.Groups[0].Name)
}

func TestPrometheusRuleWithOwnerReferences(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()

	firstOwner := metav1.OwnerReference{UID: "first-owner"}
	expectedRule := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			OwnerReferences: []metav1.OwnerReference{firstOwner},
		},
	}

	err := CreateOrUpdatePrometheusRule(k8sClient, expectedRule, nil)
	require.NoError(t, err)

	actualRule := &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, actualRule, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualRule.OwnerReferences)

	// Update with the same owner. Nothing should change as owner hasn't changed.
	err = CreateOrUpdatePrometheusRule(k8sClient, expectedRule, &firstOwner)
	require.NoError(t, err)

	actualRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, actualRule, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualRule.OwnerReferences)

	// Update with a new owner.
	secondOwner := metav1.OwnerReference{UID: "second-owner"}
	expectedRule.OwnerReferences = []metav1.OwnerReference{secondOwner}

	err = CreateOrUpdatePrometheusRule(k8sClient, expectedRule, &secondOwner)
	require.NoError(t, err)

	actualRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, actualRule, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{secondOwner, firstOwner}, actualRule.OwnerReferences)
}

func TestDeletePrometheusRule(t *testing.T) {
	name := "test"
	namespace := "test-ns"
	expected := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(expected)

	// Don't delete or throw error if the prometheus rule is not present
	err := DeletePrometheusRule(k8sClient, "not-present-prometheus-rule", namespace)
	require.NoError(t, err)

	prometheusRule := &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, prometheusRule)

	// Don't delete when there is no owner in the prometheus rule
	// but trying to delete for specific owners
	err = DeletePrometheusRule(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	prometheusRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, prometheusRule)

	// Delete when there is no owner in the prometheus rule
	err = DeletePrometheusRule(k8sClient, name, namespace)
	require.NoError(t, err)

	prometheusRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the prometheus rule is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeletePrometheusRule(k8sClient, name, namespace)
	require.NoError(t, err)

	prometheusRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, prometheusRule)

	// Don't delete when the prometheus rule is owned by objects
	// more than what are passed on delete call
	err = DeletePrometheusRule(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	prometheusRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, name, namespace)
	require.NoError(t, err)
	require.Len(t, prometheusRule.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), prometheusRule.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), prometheusRule.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the prometheus rule
	err = DeletePrometheusRule(k8sClient, name, namespace,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	prometheusRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, name, namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestCSIDriverChangeSpec(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	attachRequired := true
	expectedDriver := &storagev1beta1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: storagev1beta1.CSIDriverSpec{
			AttachRequired: &attachRequired,
		},
	}

	err := CreateOrUpdateCSIDriver(k8sClient, expectedDriver)
	require.NoError(t, err)

	actualDriver := &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, actualDriver, "test", "")
	require.NoError(t, err)
	require.True(t, *actualDriver.Spec.AttachRequired)

	// Change spec
	attachRequired = false

	err = CreateOrUpdateCSIDriver(k8sClient, expectedDriver)
	require.NoError(t, err)

	actualDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, actualDriver, "test", "")
	require.NoError(t, err)
	require.False(t, *actualDriver.Spec.AttachRequired)

	// Do not add owner reference if the input object has it
	driver := actualDriver.DeepCopy()
	driver.OwnerReferences = []metav1.OwnerReference{{UID: "uid"}}

	err = CreateOrUpdateCSIDriver(k8sClient, driver)
	require.NoError(t, err)

	actualDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, actualDriver, "test", "")
	require.NoError(t, err)
	require.Empty(t, actualDriver.OwnerReferences)

	// Remove owner reference if already present
	driver = actualDriver.DeepCopy()
	driver.OwnerReferences = []metav1.OwnerReference{{UID: "uid"}}
	k8sClient.Update(context.TODO(), driver)

	err = CreateOrUpdateCSIDriver(k8sClient, expectedDriver)
	require.NoError(t, err)

	actualDriver = &storagev1beta1.CSIDriver{}
	err = testutil.Get(k8sClient, actualDriver, "test", "")
	require.NoError(t, err)
	require.Empty(t, actualDriver.OwnerReferences)
}

func TestServicePortAddition(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// New port added to the target service spec
	expectedService.Spec.Ports = append(
		expectedService.Spec.Ports,
		v1.ServicePort{Name: "p2", Port: int32(2000), Protocol: v1.ProtocolTCP},
	)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServicePortRemoval(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
				{
					Name:     "p2",
					Port:     int32(2000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Remove port from the target service spec
	expectedService.Spec.Ports = append([]v1.ServicePort{}, expectedService.Spec.Ports[1:]...)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServiceTargetPortChange(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:       "p1",
					Port:       int32(1000),
					TargetPort: intstr.FromInt(1000),
					Protocol:   v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the target port number of an existing port
	expectedService.Spec.Ports[0].TargetPort = intstr.FromInt(2000)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServicePortNumberChange(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the port number of an existing port
	expectedService.Spec.Ports[0].Port = int32(2000)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServiceRemoveNodePortsForClusterIP(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
					NodePort: int32(11111),
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Changing to ClusterIP type should remove the node ports
	expectedService.Spec.Type = v1.ServiceTypeClusterIP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Ports[0].NodePort)
}

func TestServiceRemoveNodePortsForExternalNameType(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeNodePort,
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
					NodePort: int32(11111),
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Changing to ClusterIP type should remove the node ports
	expectedService.Spec.Type = v1.ServiceTypeExternalName

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Ports[0].NodePort)
}

func TestServicePortProtocolChange(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the protocol of an existing port
	expectedService.Spec.Ports[0].Protocol = v1.ProtocolUDP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServicePortEmptyExistingProtocol(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Name: "p1",
					Port: int32(1000),
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Set the default TCP protocol and nothing should change
	expectedService.Spec.Ports[0].Protocol = v1.ProtocolTCP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Len(t, expectedService.Spec.Ports, 1)
	require.Empty(t, actualService.Spec.Ports[0].Protocol)
}

func TestServicePortEmptyNewProtocol(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Set the protocol to empty and nothing should change as default is TCP
	expectedService.Spec.Ports[0].Protocol = ""

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Len(t, expectedService.Spec.Ports, 1)
	require.Equal(t, v1.ProtocolTCP, actualService.Spec.Ports[0].Protocol)
}

func TestServiceChangeServiceType(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, actualService.Spec.Type)

	// Change service type
	expectedService.Spec.Type = v1.ServiceTypeNodePort

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, actualService.Spec.Type)
}

func TestServiceChangeLabels(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Labels)

	// Add new labels
	expectedService.Labels = map[string]string{"key": "value"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Labels, actualService.Labels)

	// Change labels
	expectedService.Labels = map[string]string{"key": "newvalue"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Labels, actualService.Labels)

	// Remove labels
	expectedService.Labels = nil

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Labels)
}

func TestServiceWithOwnerReferences(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	firstOwner := metav1.OwnerReference{UID: "first-owner"}
	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			OwnerReferences: []metav1.OwnerReference{firstOwner},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualService.OwnerReferences)

	// Update with the same owner. Nothing should change as owner hasn't changed.
	err = CreateOrUpdateService(k8sClient, expectedService, &firstOwner)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualService.OwnerReferences)

	// Update with a new owner.
	secondOwner := metav1.OwnerReference{UID: "second-owner"}

	err = CreateOrUpdateService(k8sClient, expectedService, &secondOwner)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{secondOwner, firstOwner}, actualService.OwnerReferences)
}

func TestServiceChangeSelector(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Selector)

	// Add new selectors
	expectedService.Spec.Selector = map[string]string{"key": "value"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Spec.Selector, actualService.Spec.Selector)

	// Change selectors
	expectedService.Spec.Selector = map[string]string{"key": "newvalue"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Spec.Selector, actualService.Spec.Selector)

	// Remove selectors
	expectedService.Spec.Selector = nil

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = testutil.Get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Selector)
}

func TestGetCRDFromFile(t *testing.T) {
	tests := []struct {
		dir         string
		file        string
		expectedErr string
	}{
		{
			dir:  "../../../deploy/crds",
			file: "core_v1alpha1_storagecluster_crd.yaml",
		},
		{
			dir:         "../../../deploy/crds",
			file:        "core_v1alpha1_storagecluster_crd-dont-exist.yaml",
			expectedErr: "no such file or directory",
		},
	}

	for _, test := range tests {
		crd, err := GetCRDFromFile(test.file, test.dir)
		if len(test.expectedErr) == 0 {
			require.NoError(t, err)
			require.NotNil(t, crd)
		} else {
			require.NotNil(t, err)
			require.Contains(t, err.Error(), test.expectedErr)
		}
	}
}
