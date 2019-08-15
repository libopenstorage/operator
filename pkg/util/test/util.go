package test

import (
	"context"
	"io/ioutil"
	"path"
	"testing"

	"github.com/golang/mock/gomock"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// MockDriver creates a mock storage driver
func MockDriver(mockCtrl *gomock.Controller) *mock.MockDriver {
	return mock.NewMockDriver(mockCtrl)
}

// FakeK8sClient creates a fake controller-runtime Kubernetes client. Also
// adds the CRDs defined in this repository to the scheme
func FakeK8sClient(initObjects ...runtime.Object) client.Client {
	s := scheme.Scheme
	corev1alpha1.AddToScheme(s)
	return fake.NewFakeClientWithScheme(s, initObjects...)
}

// List returns a list of objects using the given Kubernetes client
func List(k8sClient client.Client, obj runtime.Object) error {
	return k8sClient.List(context.TODO(), &client.ListOptions{}, obj)
}

// Get returns an object using the given Kubernetes client
func Get(k8sClient client.Client, obj runtime.Object, name, namespace string) error {
	return k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		obj,
	)
}

// Delete deletes an object using the given Kubernetes client
func Delete(k8sClient client.Client, obj runtime.Object) error {
	return k8sClient.Delete(context.TODO(), obj)
}

// GetExpectedClusterRole returns the ClusterRole object from given yaml spec file
func GetExpectedClusterRole(t *testing.T, fileName string) *rbacv1.ClusterRole {
	obj := getKubernetesObject(t, fileName)
	clusterRole, ok := obj.(*rbacv1.ClusterRole)
	assert.True(t, ok, "Expected ClusterRole object")
	return clusterRole
}

// GetExpectedClusterRoleBinding returns the ClusterRoleBinding object from given
// yaml spec file
func GetExpectedClusterRoleBinding(t *testing.T, fileName string) *rbacv1.ClusterRoleBinding {
	obj := getKubernetesObject(t, fileName)
	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	assert.True(t, ok, "Expected ClusterRoleBinding object")
	return crb
}

// GetExpectedRole returns the Role object from given yaml spec file
func GetExpectedRole(t *testing.T, fileName string) *rbacv1.Role {
	obj := getKubernetesObject(t, fileName)
	role, ok := obj.(*rbacv1.Role)
	assert.True(t, ok, "Expected Role object")
	return role
}

// GetExpectedRoleBinding returns the RoleBinding object from given yaml spec file
func GetExpectedRoleBinding(t *testing.T, fileName string) *rbacv1.RoleBinding {
	obj := getKubernetesObject(t, fileName)
	roleBinding, ok := obj.(*rbacv1.RoleBinding)
	assert.True(t, ok, "Expected RoleBinding object")
	return roleBinding
}

// GetExpectedService returns the Service object from given yaml spec file
func GetExpectedService(t *testing.T, fileName string) *v1.Service {
	obj := getKubernetesObject(t, fileName)
	service, ok := obj.(*v1.Service)
	assert.True(t, ok, "Expected Service object")
	return service
}

// GetExpectedDeployment returns the Deployment object from given yaml spec file
func GetExpectedDeployment(t *testing.T, fileName string) *appsv1.Deployment {
	obj := getKubernetesObject(t, fileName)
	deployment, ok := obj.(*appsv1.Deployment)
	assert.True(t, ok, "Expected Deployment object")
	return deployment
}

// GetExpectedStatefulSet returns the StatefulSet object from given yaml spec file
func GetExpectedStatefulSet(t *testing.T, fileName string) *appsv1.StatefulSet {
	obj := getKubernetesObject(t, fileName)
	statefulSet, ok := obj.(*appsv1.StatefulSet)
	assert.True(t, ok, "Expected StatefulSet object")
	return statefulSet
}

// GetExpectedDaemonSet returns the DaemonSet object from given yaml spec file
func GetExpectedDaemonSet(t *testing.T, fileName string) *appsv1.DaemonSet {
	obj := getKubernetesObject(t, fileName)
	daemonSet, ok := obj.(*appsv1.DaemonSet)
	assert.True(t, ok, "Expected DaemonSet object")
	return daemonSet
}

// getKubernetesObject returns a generic Kubernetes object from given yaml file
func getKubernetesObject(t *testing.T, fileName string) runtime.Object {
	json, err := ioutil.ReadFile(path.Join("testspec", fileName))
	assert.NoError(t, err)
	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)
	return obj
}

// GetPullPolicyForContainer returns the image pull policy for given deployment
// and container name
func GetPullPolicyForContainer(
	deployment *appsv1.Deployment,
	containerName string,
) v1.PullPolicy {
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.ImagePullPolicy
		}
	}
	return ""
}
