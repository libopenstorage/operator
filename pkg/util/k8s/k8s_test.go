package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
	err = get(k8sClient, sa, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, sa)

	// Don't delete when there is no owner in the service account
	// but trying to delete for specific owners
	err = DeleteServiceAccount(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = get(k8sClient, sa, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, sa)

	// Delete when there is no owner in the service account
	err = DeleteServiceAccount(k8sClient, name, namespace)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = get(k8sClient, sa, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the service account is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteServiceAccount(k8sClient, name, namespace)
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = get(k8sClient, sa, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, sa)

	// Don't delete when the service account is owned by objects
	// more than what are passed on delete call
	err = DeleteServiceAccount(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	sa = &v1.ServiceAccount{}
	err = get(k8sClient, sa, name, namespace)
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
	err = get(k8sClient, sa, name, namespace)
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
	err = get(k8sClient, role, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, role)

	// Don't delete when there is no owner in the role
	// but trying to delete for specific owners
	err = DeleteRole(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = get(k8sClient, role, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, role)

	// Delete when there is no owner in the role
	err = DeleteRole(k8sClient, name, namespace)
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = get(k8sClient, role, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the role is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteRole(k8sClient, name, namespace)
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = get(k8sClient, role, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, role)

	// Don't delete when the role is owned by objects
	// more than what are passed on delete call
	err = DeleteRole(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	role = &rbacv1.Role{}
	err = get(k8sClient, role, name, namespace)
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
	err = get(k8sClient, role, name, namespace)
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
	err = get(k8sClient, roleBinding, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, roleBinding)

	// Don't delete when there is no owner in the role binding
	// but trying to delete for specific owners
	err = DeleteRoleBinding(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = get(k8sClient, roleBinding, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, roleBinding)

	// Delete when there is no owner in the role binding
	err = DeleteRoleBinding(k8sClient, name, namespace)
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = get(k8sClient, roleBinding, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the role binding is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteRoleBinding(k8sClient, name, namespace)
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = get(k8sClient, roleBinding, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, roleBinding)

	// Don't delete when the role binding is owned by objects
	// more than what are passed on delete call
	err = DeleteRoleBinding(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	roleBinding = &rbacv1.RoleBinding{}
	err = get(k8sClient, roleBinding, name, namespace)
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
	err = get(k8sClient, roleBinding, name, namespace)
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
	err = get(k8sClient, clusterRole, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, clusterRole)

	// Don't delete when there is no owner in the cluster role
	// but trying to delete for specific owners
	err = DeleteClusterRole(k8sClient, name, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	clusterRole = &rbacv1.ClusterRole{}
	err = get(k8sClient, clusterRole, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, clusterRole)

	// Delete when there is no owner in the cluster role
	err = DeleteClusterRole(k8sClient, name)
	require.NoError(t, err)

	clusterRole = &rbacv1.ClusterRole{}
	err = get(k8sClient, clusterRole, name, "")
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the cluster role is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteClusterRole(k8sClient, name)
	require.NoError(t, err)

	clusterRole = &rbacv1.ClusterRole{}
	err = get(k8sClient, clusterRole, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, clusterRole)

	// Don't delete when the cluster role is owned by objects
	// more than what are passed on delete call
	err = DeleteClusterRole(k8sClient, name, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	clusterRole = &rbacv1.ClusterRole{}
	err = get(k8sClient, clusterRole, name, "")
	require.NoError(t, err)
	require.Len(t, clusterRole.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), clusterRole.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), clusterRole.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the cluster role
	err = DeleteClusterRole(k8sClient, name,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	clusterRole = &rbacv1.ClusterRole{}
	err = get(k8sClient, clusterRole, name, "")
	require.True(t, errors.IsNotFound(err))
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
	err = get(k8sClient, crb, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, crb)

	// Don't delete when there is no owner in the cluster role binding
	// but trying to delete for specific owners
	err = DeleteClusterRoleBinding(k8sClient, name, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = get(k8sClient, crb, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, crb)

	// Delete when there is no owner in the cluster role binding
	err = DeleteClusterRoleBinding(k8sClient, name)
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = get(k8sClient, crb, name, "")
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the cluster role binding is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteClusterRoleBinding(k8sClient, name)
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = get(k8sClient, crb, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, crb)

	// Don't delete when the cluster role binding is owned by objects
	// more than what are passed on delete call
	err = DeleteClusterRoleBinding(k8sClient, name, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = get(k8sClient, crb, name, "")
	require.NoError(t, err)
	require.Len(t, crb.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), crb.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), crb.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the cluster role binding
	err = DeleteClusterRoleBinding(k8sClient, name,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	crb = &rbacv1.ClusterRoleBinding{}
	err = get(k8sClient, crb, name, "")
	require.True(t, errors.IsNotFound(err))
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
	err = get(k8sClient, storageClass, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, storageClass)

	// Don't delete when there is no owner in the storage class
	// but trying to delete for specific owners
	err = DeleteStorageClass(k8sClient, name, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	storageClass = &storagev1.StorageClass{}
	err = get(k8sClient, storageClass, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, storageClass)

	// Delete when there is no owner in the storage class
	err = DeleteStorageClass(k8sClient, name)
	require.NoError(t, err)

	storageClass = &storagev1.StorageClass{}
	err = get(k8sClient, storageClass, name, "")
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the storage class is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteStorageClass(k8sClient, name)
	require.NoError(t, err)

	storageClass = &storagev1.StorageClass{}
	err = get(k8sClient, storageClass, name, "")
	require.NoError(t, err)
	require.Equal(t, expected, storageClass)

	// Don't delete when the storage class is owned by objects
	// more than what are passed on delete call
	err = DeleteStorageClass(k8sClient, name, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	storageClass = &storagev1.StorageClass{}
	err = get(k8sClient, storageClass, name, "")
	require.NoError(t, err)
	require.Len(t, storageClass.OwnerReferences, 2)
	require.Equal(t, types.UID("alpha"), storageClass.OwnerReferences[0].UID)
	require.Equal(t, types.UID("gamma"), storageClass.OwnerReferences[1].UID)

	// Delete when delete call passes all owners (or more) of the storage class
	err = DeleteStorageClass(k8sClient, name,
		metav1.OwnerReference{UID: "theta"},
		metav1.OwnerReference{UID: "gamma"},
		metav1.OwnerReference{UID: "alpha"},
	)
	require.NoError(t, err)

	storageClass = &storagev1.StorageClass{}
	err = get(k8sClient, storageClass, name, "")
	require.True(t, errors.IsNotFound(err))
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
	err = get(k8sClient, configMap, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, configMap)

	// Don't delete when there is no owner in the config map
	// but trying to delete for specific owners
	err = DeleteConfigMap(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = get(k8sClient, configMap, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, configMap)

	// Delete when there is no owner in the config map
	err = DeleteConfigMap(k8sClient, name, namespace)
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = get(k8sClient, configMap, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the config map is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteConfigMap(k8sClient, name, namespace)
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = get(k8sClient, configMap, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, configMap)

	// Don't delete when the config map is owned by objects
	// more than what are passed on delete call
	err = DeleteConfigMap(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	configMap = &v1.ConfigMap{}
	err = get(k8sClient, configMap, name, namespace)
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
	err = get(k8sClient, configMap, name, namespace)
	require.True(t, errors.IsNotFound(err))
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
	err = get(k8sClient, service, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, service)

	// Don't delete when there is no owner in the service
	// but trying to delete for specific owners
	err = DeleteService(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	service = &v1.Service{}
	err = get(k8sClient, service, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, service)

	// Delete when there is no owner in the service
	err = DeleteService(k8sClient, name, namespace)
	require.NoError(t, err)

	service = &v1.Service{}
	err = get(k8sClient, service, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the service is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteService(k8sClient, name, namespace)
	require.NoError(t, err)

	service = &v1.Service{}
	err = get(k8sClient, service, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, service)

	// Don't delete when the service is owned by objects
	// more than what are passed on delete call
	err = DeleteService(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	service = &v1.Service{}
	err = get(k8sClient, service, name, namespace)
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
	err = get(k8sClient, service, name, namespace)
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
	err = get(k8sClient, deployment, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, deployment)

	// Don't delete when there is no owner in the deployment
	// but trying to delete for specific owners
	err = DeleteDeployment(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = get(k8sClient, deployment, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, deployment)

	// Delete when there is no owner in the deployment
	err = DeleteDeployment(k8sClient, name, namespace)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = get(k8sClient, deployment, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the deployment is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteDeployment(k8sClient, name, namespace)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = get(k8sClient, deployment, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, deployment)

	// Don't delete when the deployment is owned by objects
	// more than what are passed on delete call
	err = DeleteDeployment(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = get(k8sClient, deployment, name, namespace)
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
	err = get(k8sClient, deployment, name, namespace)
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
	err = get(k8sClient, statefulSet, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, statefulSet)

	// Don't delete when there is no owner in the stateful set
	// but trying to delete for specific owners
	err = DeleteStatefulSet(k8sClient, name, namespace, metav1.OwnerReference{UID: "foo"})
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = get(k8sClient, statefulSet, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, statefulSet)

	// Delete when there is no owner in the stateful set
	err = DeleteStatefulSet(k8sClient, name, namespace)
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = get(k8sClient, statefulSet, name, namespace)
	require.True(t, errors.IsNotFound(err))

	// Don't delete when the stateful set is owned by an object
	// and no owner reference passed in delete call
	expected.OwnerReferences = []metav1.OwnerReference{{UID: "alpha"}, {UID: "beta"}, {UID: "gamma"}}
	k8sClient.Create(context.TODO(), expected)

	err = DeleteStatefulSet(k8sClient, name, namespace)
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = get(k8sClient, statefulSet, name, namespace)
	require.NoError(t, err)
	require.Equal(t, expected, statefulSet)

	// Don't delete when the stateful set is owned by objects
	// more than what are passed on delete call
	err = DeleteStatefulSet(k8sClient, name, namespace, metav1.OwnerReference{UID: "beta"})
	require.NoError(t, err)

	statefulSet = &appsv1.StatefulSet{}
	err = get(k8sClient, statefulSet, name, namespace)
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
	err = get(k8sClient, statefulSet, name, namespace)
	require.True(t, errors.IsNotFound(err))
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
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Remove port from the target service spec
	expectedService.Spec.Ports = append([]v1.ServicePort{}, expectedService.Spec.Ports[1:]...)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the target port number of an existing port
	expectedService.Spec.Ports[0].TargetPort = intstr.FromInt(2000)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the port number of an existing port
	expectedService.Spec.Ports[0].Port = int32(2000)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Changing to ClusterIP type should remove the node ports
	expectedService.Spec.Type = v1.ServiceTypeClusterIP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Changing to ClusterIP type should remove the node ports
	expectedService.Spec.Type = v1.ServiceTypeExternalName

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the protocol of an existing port
	expectedService.Spec.Ports[0].Protocol = v1.ProtocolUDP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Set the default TCP protocol and nothing should change
	expectedService.Spec.Ports[0].Protocol = v1.ProtocolTCP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Set the protocol to empty and nothing should change as default is TCP
	expectedService.Spec.Ports[0].Protocol = ""

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, actualService.Spec.Type)

	// Change service type
	expectedService.Spec.Type = v1.ServiceTypeNodePort

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Labels)

	// Add new labels
	expectedService.Labels = map[string]string{"key": "value"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Labels, actualService.Labels)

	// Change labels
	expectedService.Labels = map[string]string{"key": "newvalue"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Labels, actualService.Labels)

	// Remove labels
	expectedService.Labels = nil

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualService.OwnerReferences)

	// Update with the same owner. Nothing should change as owner hasn't changed.
	err = CreateOrUpdateService(k8sClient, expectedService, &firstOwner)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualService.OwnerReferences)

	// Update with a new owner.
	secondOwner := metav1.OwnerReference{UID: "second-owner"}

	err = CreateOrUpdateService(k8sClient, expectedService, &secondOwner)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
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
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Selector)

	// Add new selectors
	expectedService.Spec.Selector = map[string]string{"key": "value"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Spec.Selector, actualService.Spec.Selector)

	// Change selectors
	expectedService.Spec.Selector = map[string]string{"key": "newvalue"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Spec.Selector, actualService.Spec.Selector)

	// Remove selectors
	expectedService.Spec.Selector = nil

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Selector)
}

func get(k8sClient client.Client, obj runtime.Object, name, namespace string) error {
	return k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		obj,
	)
}
