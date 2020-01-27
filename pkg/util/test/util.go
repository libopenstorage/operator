package test

import (
	"context"
	"fmt"
	"github.com/libopenstorage/openstorage/api"
	"github.com/portworx/sched-ops/k8s"
	"github.com/portworx/sched-ops/task"
	"google.golang.org/grpc"
	"io/ioutil"
	"k8s.io/kubernetes/pkg/scheduler/algorithm/predicates"
	"path"
	"testing"
	"time"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/golang/mock/gomock"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	schedulernodeinfo "k8s.io/kubernetes/pkg/scheduler/nodeinfo"
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
	monitoringv1.AddToScheme(s)
	return fake.NewFakeClientWithScheme(s, initObjects...)
}

// List returns a list of objects using the given Kubernetes client
func List(k8sClient client.Client, obj runtime.Object) error {
	return k8sClient.List(context.TODO(), obj, &client.ListOptions{})
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

// GetExpectedStorageClass returns the StorageClass object from given yaml spec file
func GetExpectedStorageClass(t *testing.T, fileName string) *storagev1.StorageClass {
	obj := getKubernetesObject(t, fileName)
	storageClass, ok := obj.(*storagev1.StorageClass)
	assert.True(t, ok, "Expected StorageClass object")
	return storageClass
}

// GetExpectedConfigMap returns the ConfigMap object from given yaml spec file
func GetExpectedConfigMap(t *testing.T, fileName string) *v1.ConfigMap {
	obj := getKubernetesObject(t, fileName)
	configMap, ok := obj.(*v1.ConfigMap)
	assert.True(t, ok, "Expected ConfigMap object")
	return configMap
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

// GetExpectedCRD returns the CustomResourceDefinition object from given yaml spec file
func GetExpectedCRD(t *testing.T, fileName string) *apiextensionsv1beta1.CustomResourceDefinition {
	obj := getKubernetesObject(t, fileName)
	crd, ok := obj.(*apiextensionsv1beta1.CustomResourceDefinition)
	assert.True(t, ok, "Expected CustomResourceDefinition object")
	return crd
}

// GetExpectedServiceMonitor returns the ServiceMonitor object from given yaml spec file
func GetExpectedServiceMonitor(t *testing.T, fileName string) *monitoringv1.ServiceMonitor {
	obj := getKubernetesObject(t, fileName)
	serviceMonitor, ok := obj.(*monitoringv1.ServiceMonitor)
	assert.True(t, ok, "Expected ServiceMonitor object")
	return serviceMonitor
}

// GetExpectedPrometheusRule returns the PrometheusRule object from given yaml spec file
func GetExpectedPrometheusRule(t *testing.T, fileName string) *monitoringv1.PrometheusRule {
	obj := getKubernetesObject(t, fileName)
	prometheusRule, ok := obj.(*monitoringv1.PrometheusRule)
	assert.True(t, ok, "Expected PrometheusRule object")
	return prometheusRule
}

// getKubernetesObject returns a generic Kubernetes object from given yaml file
func getKubernetesObject(t *testing.T, fileName string) runtime.Object {
	json, err := ioutil.ReadFile(path.Join("testspec", fileName))
	assert.NoError(t, err)
	s := scheme.Scheme
	apiextensionsv1beta1.AddToScheme(s)
	monitoringv1.AddToScheme(s)
	codecs := serializer.NewCodecFactory(s)
	obj, _, err := codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
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

// ActivateCRDWhenCreated activates the given CRD by updating it's status. It waits for
// CRD to be created for 1 minute before returning an error
func ActivateCRDWhenCreated(fakeClient *fakeextclient.Clientset, crdName string) error {
	return wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
		crd, err := fakeClient.ApiextensionsV1beta1().
			CustomResourceDefinitions().
			Get(crdName, metav1.GetOptions{})
		if err == nil {
			crd.Status.Conditions = []apiextensionsv1beta1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1beta1.Established,
				Status: apiextensionsv1beta1.ConditionTrue,
			}}
			fakeClient.ApiextensionsV1beta1().CustomResourceDefinitions().UpdateStatus(crd)
			return true, nil
		} else if !errors.IsNotFound(err) {
			return false, err
		}
		return false, nil
	})
}

// UninstallStorageCluster uninstalls and wipe storagecluster from k8s
func UninstallStorageCluster(cluster *corev1alpha1.StorageCluster) error {
	var err error
	cluster, err = k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if cluster.Spec.DeleteStrategy == nil ||
		(cluster.Spec.DeleteStrategy.Type != corev1alpha1.UninstallAndWipeStorageClusterStrategyType &&
			cluster.Spec.DeleteStrategy.Type != corev1alpha1.UninstallStorageClusterStrategyType) {
		cluster.Spec.DeleteStrategy = &corev1alpha1.StorageClusterDeleteStrategy{
			Type: corev1alpha1.UninstallAndWipeStorageClusterStrategyType,
		}
		if _, err = k8s.Instance().UpdateStorageCluster(cluster); err != nil {
			return err
		}
	}

	return k8s.Instance().DeleteStorageCluster(cluster.Name, cluster.Namespace)
}

// ValidateStorageCluster validates a StorageCluster spec
func ValidateStorageCluster(
	cluster *corev1alpha1.StorageCluster,
	timeout, interval time.Duration,
) error {
	var err error
	cluster, err = k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil {
		return err
	}

	if err = validateStorageClusterPods(cluster, timeout, interval); err != nil {
		return err
	}

	svc, err := k8s.Instance().GetService("portworx-service", cluster.Namespace)
	pxEndpoint := ""
	if err != nil {
		return err
	}

	servicePort := int32(0)
	for _, port := range svc.Spec.Ports {
		if port.Name == "px-sdk" {
			servicePort = port.Port
			break
		}
	}
	if servicePort == 0 {
		return fmt.Errorf("px-sdk port not found in service")
	}

	pxEndpoint = fmt.Sprintf("%s:%d", svc.Spec.ClusterIP, servicePort)

	conn, err := grpc.Dial(pxEndpoint, grpc.WithInsecure())
	if err != nil {
		return err
	}
	nodeClient := api.NewOpenStorageNodeClient(conn)
	nodeEnumerateResp, err := nodeClient.Enumerate(context.Background(), &api.SdkNodeEnumerateRequest{})
	if err != nil {
		return err
	}
	expectedNodes, err := expectedPods(cluster)
	if err != nil {
		return err
	}
	actualNodes := len(nodeEnumerateResp.GetNodeIds())
	if actualNodes != expectedNodes {
		return fmt.Errorf("expected nodes: %v. actual nodes: %v", expectedNodes, actualNodes)
	}

	// TODO: Validate portworx is started with correct params. Check individual options
	for _, n := range nodeEnumerateResp.GetNodeIds() {
		nodeResp, err := nodeClient.Inspect(context.Background(), &api.SdkNodeInspectRequest{NodeId: n})
		if err != nil {
			return err
		}
		if nodeResp.Node.Status != api.Status_STATUS_OK {
			return fmt.Errorf("node %s is not online. Current: %v", nodeResp.Node.SchedulerNodeName,
				nodeResp.Node.Status)
		}

	}
	return nil
}

// ValidateUninstallStorageCluster validates if storagecluster and its related objects
// were properly uninstalled and cleaned
func ValidateUninstallStorageCluster(
	cluster *corev1alpha1.StorageCluster,
	timeout, interval time.Duration,
) error {
	t := func() (interface{}, bool, error) {
		cluster, err := k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			if errors.IsNotFound(err) {
				return "", false, nil
			}
			return "", true, err
		}

		pods, err := k8s.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil && err != k8s.ErrPodsNotFound {
			return "", true, fmt.Errorf("failed to get pods for StorageCluster %s/%s. Err: %v",
				cluster.Namespace, cluster.Name, err)
		}

		if len(pods) > 0 {
			return "", true, fmt.Errorf("%v pods are still present", len(pods))
		}

		return "", true, fmt.Errorf("pods are deleted, but StorageCluster %v/%v still present",
			cluster.Namespace, cluster.Name)
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}
	return nil
}

func validateStorageClusterPods(
	cluster *corev1alpha1.StorageCluster,
	timeout, interval time.Duration,
) error {
	expectedPodCount, err := expectedPods(cluster)
	if err != nil {
		return err
	}

	t := func() (interface{}, bool, error) {
		cluster, err := k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			return "", true, err
		}

		pods, err := k8s.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil || pods == nil {
			return "", true, fmt.Errorf("failed to get pods for StorageCluster %s/%s. Err: %v",
				cluster.Namespace, cluster.Name, err)
		}

		if len(pods) != expectedPodCount {
			return "", true, fmt.Errorf("expected pods: %v. actual pods: %v", expectedPodCount, len(pods))
		}

		for _, pod := range pods {
			if !k8s.Instance().IsPodReady(pod) {
				return "", true, fmt.Errorf("pod %v/%v is not yet ready", pod.Namespace, pod.Name)
			}
		}

		return "", false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}
	return nil
}

func expectedPods(cluster *corev1alpha1.StorageCluster) (int, error) {
	nodeList, err := k8s.Instance().GetNodes()
	if err != nil {
		return 0, err
	}

	dummyPod := &v1.Pod{}
	if cluster.Spec.Placement != nil && cluster.Spec.Placement.NodeAffinity != nil {
		dummyPod.Spec.Affinity = &v1.Affinity{
			NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	podCount := 0
	for _, node := range nodeList.Items {
		if k8s.Instance().IsNodeMaster(node) {
			continue
		}
		nodeInfo := schedulernodeinfo.NewNodeInfo()
		nodeInfo.SetNode(&node)
		if ok, _, _ := predicates.PodMatchNodeSelector(dummyPod, nil, nodeInfo); ok {
			podCount++
		}
	}

	return podCount, nil
}
