package portworx

import (
	"context"
	"io/ioutil"
	"path"
	"testing"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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

func list(k8sClient client.Client, obj runtime.Object) error {
	return k8sClient.List(context.TODO(), &client.ListOptions{}, obj)
}

func deleteObj(k8sClient client.Client, obj runtime.Object) error {
	return k8sClient.Delete(context.TODO(), obj)
}

func getExpectedClusterRole(t *testing.T, fileName string) *rbacv1.ClusterRole {
	obj := getKubernetesObject(t, fileName)
	clusterRole, ok := obj.(*rbacv1.ClusterRole)
	assert.True(t, ok, "Expected ClusterRole object")
	return clusterRole
}

func getExpectedClusterRoleBinding(t *testing.T, fileName string) *rbacv1.ClusterRoleBinding {
	obj := getKubernetesObject(t, fileName)
	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	assert.True(t, ok, "Expected ClusterRoleBinding object")
	return crb
}

func getExpectedRole(t *testing.T, fileName string) *rbacv1.Role {
	obj := getKubernetesObject(t, fileName)
	role, ok := obj.(*rbacv1.Role)
	assert.True(t, ok, "Expected Role object")
	return role
}

func getExpectedRoleBinding(t *testing.T, fileName string) *rbacv1.RoleBinding {
	obj := getKubernetesObject(t, fileName)
	roleBinding, ok := obj.(*rbacv1.RoleBinding)
	assert.True(t, ok, "Expected RoleBinding object")
	return roleBinding
}

func getExpectedService(t *testing.T, fileName string) *v1.Service {
	obj := getKubernetesObject(t, fileName)
	service, ok := obj.(*v1.Service)
	assert.True(t, ok, "Expected Service object")
	return service
}

func getExpectedDeployment(t *testing.T, fileName string) *appsv1.Deployment {
	obj := getKubernetesObject(t, fileName)
	deployment, ok := obj.(*appsv1.Deployment)
	assert.True(t, ok, "Expected Deployment object")
	return deployment
}

func getExpectedStatefulSet(t *testing.T, fileName string) *appsv1.StatefulSet {
	obj := getKubernetesObject(t, fileName)
	statefulSet, ok := obj.(*appsv1.StatefulSet)
	assert.True(t, ok, "Expected StatefulSet object")
	return statefulSet
}

func getExpectedDaemonSet(t *testing.T, fileName string) *appsv1.DaemonSet {
	obj := getKubernetesObject(t, fileName)
	daemonSet, ok := obj.(*appsv1.DaemonSet)
	assert.True(t, ok, "Expected DaemonSet object")
	return daemonSet
}

func getKubernetesObject(t *testing.T, fileName string) runtime.Object {
	json, err := ioutil.ReadFile(path.Join("testspec", fileName))
	assert.NoError(t, err)
	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)
	return obj
}

func getImageForContainer(deployment *appsv1.Deployment, containerName string) string {
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return ""
}

func getPullPolicyForContainer(deployment *appsv1.Deployment, containerName string) v1.PullPolicy {
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.ImagePullPolicy
		}
	}
	return ""
}

func fakeK8sClient(initObjects ...runtime.Object) client.Client {
	s := scheme.Scheme
	corev1alpha1.AddToScheme(s)
	return fake.NewFakeClientWithScheme(s, initObjects...)
}
