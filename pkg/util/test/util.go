package test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/openstorage/api"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/libopenstorage/operator/pkg/util"
	appops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	k8serrors "github.com/portworx/sched-ops/k8s/errors"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	prometheusops "github.com/portworx/sched-ops/k8s/prometheus"
	"github.com/portworx/sched-ops/task"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
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
	"k8s.io/kubernetes/pkg/scheduler/algorithm/predicates"
	schedulernodeinfo "k8s.io/kubernetes/pkg/scheduler/nodeinfo"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
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
	corev1.AddToScheme(s)
	monitoringv1.AddToScheme(s)
	cluster_v1alpha1.AddToScheme(s)
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

// Update changes an object using the given Kubernetes client and updates the resource version
func Update(k8sClient client.Client, obj runtime.Object) error {
	return k8sClient.Update(
		context.TODO(),
		obj,
	)
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

// GetExpectedSecret returns the Secret object from given yaml spec file
func GetExpectedSecret(t *testing.T, fileName string) *v1.Secret {
	obj := getKubernetesObject(t, fileName)
	secret, ok := obj.(*v1.Secret)
	assert.True(t, ok, "Expected Secret object")
	return secret
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

// GetExpectedPrometheus returns the Prometheus object from given yaml spec file
func GetExpectedPrometheus(t *testing.T, fileName string) *monitoringv1.Prometheus {
	obj := getKubernetesObject(t, fileName)
	prometheus, ok := obj.(*monitoringv1.Prometheus)
	assert.True(t, ok, "Expected Prometheus object")
	return prometheus
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
func UninstallStorageCluster(cluster *corev1.StorageCluster, kubeconfig ...string) error {
	var err error
	if len(kubeconfig) != 0 && kubeconfig[0] != "" {
		os.Setenv("KUBECONFIG", kubeconfig[0])
	}
	cluster, err = operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if cluster.Spec.DeleteStrategy == nil ||
		(cluster.Spec.DeleteStrategy.Type != corev1.UninstallAndWipeStorageClusterStrategyType &&
			cluster.Spec.DeleteStrategy.Type != corev1.UninstallStorageClusterStrategyType) {
		cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
			Type: corev1.UninstallAndWipeStorageClusterStrategyType,
		}
		if _, err = operatorops.Instance().UpdateStorageCluster(cluster); err != nil {
			return err
		}
	}

	return operatorops.Instance().DeleteStorageCluster(cluster.Name, cluster.Namespace)
}

// ValidateStorageCluster validates a StorageCluster spec
func ValidateStorageCluster(
	pxImageList map[string]string,
	cluster *corev1.StorageCluster,
	timeout, interval time.Duration,
	kubeconfig ...string,
) error {
	var err error
	if len(kubeconfig) != 0 && kubeconfig[0] != "" {
		os.Setenv("KUBECONFIG", kubeconfig[0])
	}
	cluster, err = operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil {
		return err
	}

	if err = validateStorageClusterPods(cluster, timeout, interval); err != nil {
		return err
	}

	if err = validateComponents(pxImageList, cluster, timeout, interval); err != nil {
		return err
	}

	conn, err := getSdkConnection(cluster)
	if err != nil {
		return nil
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

// NewResourceVersion creates a random 16 character string
// to simulate a k8s resource version
func NewResourceVersion() string {
	var randBytes = make([]byte, 32)
	_, err := rand.Read(randBytes)
	if err != nil {
		return ""
	}

	ver := make([]byte, base64.StdEncoding.EncodedLen(len(randBytes)))
	base64.StdEncoding.Encode(ver, randBytes)

	return string(ver[:16])
}

func getSdkConnection(cluster *corev1.StorageCluster) (*grpc.ClientConn, error) {
	pxEndpoint, err := coreops.Instance().GetServiceEndpoint("portworx-service", cluster.Namespace)
	if err != nil {
		return nil, err
	}

	svc, err := coreops.Instance().GetService("portworx-service", cluster.Namespace)
	if err != nil {
		return nil, err
	}

	servicePort := int32(0)
	nodePort := ""
	for _, port := range svc.Spec.Ports {
		if port.Name == "px-sdk" {
			servicePort = port.Port
			nodePort = port.TargetPort.StrVal
			break
		}
	}

	if servicePort == 0 {
		return nil, fmt.Errorf("px-sdk port not found in service")
	}

	conn, err := grpc.Dial(fmt.Sprintf("%s:%d", pxEndpoint, servicePort), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	// try over the service endpoint
	cli := api.NewOpenStorageIdentityClient(conn)
	if _, err = cli.Version(context.Background(), &api.SdkIdentityVersionRequest{}); err == nil {
		return conn, nil
	}

	// if  service endpoint IP is not accessible, we pick one node IP
	if nodes, err := coreops.Instance().GetNodes(); err == nil {
		for _, node := range nodes.Items {
			for _, addr := range node.Status.Addresses {
				if addr.Type == v1.NodeInternalIP {
					conn, err := grpc.Dial(fmt.Sprintf("%s:%s", addr.Address, nodePort), grpc.WithInsecure())
					if err != nil {
						return nil, err
					}
					if _, err = cli.Version(context.Background(), &api.SdkIdentityVersionRequest{}); err == nil {
						return conn, nil
					}
				}
			}
		}
	}
	return nil, err
}

// ValidateUninstallStorageCluster validates if storagecluster and its related objects
// were properly uninstalled and cleaned
func ValidateUninstallStorageCluster(
	cluster *corev1.StorageCluster,
	timeout, interval time.Duration,
	kubeconfig ...string,
) error {
	if len(kubeconfig) != 0 && kubeconfig[0] != "" {
		os.Setenv("KUBECONFIG", kubeconfig[0])
	}
	t := func() (interface{}, bool, error) {
		cluster, err := operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			if errors.IsNotFound(err) {
				return "", false, nil
			}
			return "", true, err
		}

		pods, err := coreops.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil && err != k8serrors.ErrPodsNotFound {
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
	cluster *corev1.StorageCluster,
	timeout, interval time.Duration,
) error {
	expectedPodCount, err := expectedPods(cluster)
	if err != nil {
		return err
	}

	t := func() (interface{}, bool, error) {
		cluster, err := operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			return "", true, err
		}

		pods, err := coreops.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil || pods == nil {
			return "", true, fmt.Errorf("failed to get pods for StorageCluster %s/%s. Err: %v",
				cluster.Namespace, cluster.Name, err)
		}

		if len(pods) != expectedPodCount {
			return "", true, fmt.Errorf("expected pods: %v. actual pods: %v", expectedPodCount, len(pods))
		}

		for _, pod := range pods {
			if !coreops.Instance().IsPodReady(pod) {
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

func expectedPods(cluster *corev1.StorageCluster) (int, error) {
	nodeList, err := coreops.Instance().GetNodes()
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
		if coreops.Instance().IsNodeMaster(node) {
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

func validateComponents(pxImageList map[string]string, cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	k8sVersion, err := getK8SVersion()
	if err != nil {
		return err
	}

	if isPVCControllerEnabled(cluster) {
		pvcControllerDp := &appsv1.Deployment{}
		pvcControllerDp.Name = "portworx-pvc-controller"
		pvcControllerDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(pvcControllerDp, timeout, interval); err != nil {
			return err
		}

		if err = validateImageTag(k8sVersion, cluster.Namespace, map[string]string{"name": "portworx-pvc-controller"}); err != nil {
			return err
		}
	}

	if cluster.Spec.Stork != nil && cluster.Spec.Stork.Enabled {
		storkDp := &appsv1.Deployment{}
		storkDp.Name = "stork"
		storkDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(storkDp, timeout, interval); err != nil {
			return err
		}

		var storkImageName string
		if cluster.Spec.Stork.Image == "" {
			if value, ok := pxImageList["stork"]; ok {
				storkImageName = value
			} else {
				return fmt.Errorf("failed to find image for stork")
			}
		} else {
			storkImageName = cluster.Spec.Stork.Image
		}

		storkImage := util.GetImageURN(cluster.Spec.CustomImageRegistry, storkImageName)
		err := validateImageOnPods(storkImage, cluster.Namespace, map[string]string{"name": "stork"})
		if err != nil {
			return err
		}

		storkSchedulerDp := &appsv1.Deployment{}
		storkSchedulerDp.Name = "stork-scheduler"
		storkSchedulerDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(storkSchedulerDp, timeout, interval); err != nil {
			return err
		}

		if err = validateImageTag(k8sVersion, cluster.Namespace, map[string]string{"name": "stork-scheduler"}); err != nil {
			return err
		}
	}

	if cluster.Spec.Autopilot != nil && cluster.Spec.Autopilot.Enabled {
		autopilotDp := &appsv1.Deployment{}
		autopilotDp.Name = "autopilot"
		autopilotDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(autopilotDp, timeout, interval); err != nil {
			return err
		}

		var autopilotImageName string
		if cluster.Spec.Autopilot.Image == "" {
			if value, ok := pxImageList["autopilot"]; ok {
				autopilotImageName = value
			} else {
				return fmt.Errorf("failed to find image for autopilot")
			}
		} else {
			autopilotImageName = cluster.Spec.Autopilot.Image
		}

		autopilotImage := util.GetImageURN(cluster.Spec.CustomImageRegistry, autopilotImageName)
		if err = validateImageOnPods(autopilotImage, cluster.Namespace, map[string]string{"name": "autopilot"}); err != nil {
			return err
		}
	}

	if cluster.Spec.UserInterface != nil && cluster.Spec.UserInterface.Enabled {
		lighthouseDp := &appsv1.Deployment{}
		lighthouseDp.Name = "px-lighthouse"
		lighthouseDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(lighthouseDp, timeout, interval); err != nil {
			return err
		}

		var lighthouseImageName string
		if cluster.Spec.UserInterface.Image == "" {
			if value, ok := pxImageList["lighthouse"]; ok {
				lighthouseImageName = value
			} else {
				return fmt.Errorf("failed to find image for lighthouse")
			}
		} else {
			lighthouseImageName = cluster.Spec.UserInterface.Image
		}

		lhImage := util.GetImageURN(cluster.Spec.CustomImageRegistry, lighthouseImageName)
		if err = validateImageOnPods(lhImage, cluster.Namespace, map[string]string{"name": "lighthouse"}); err != nil {
			return err
		}
	}

	if csi, _ := strconv.ParseBool(cluster.Spec.FeatureGates["CSI"]); csi {
		// check if all px containers are up since csi has extra containers alongside oci monitor
		if err = validatePodsByName(cluster.Namespace, "portworx", timeout, interval); err != nil {
			return err
		}

		pxCsiDp := &appsv1.Deployment{}
		pxCsiDp.Name = "px-csi-ext"
		pxCsiDp.Namespace = cluster.Namespace
		if err = appops.Instance().ValidateDeployment(pxCsiDp, timeout, interval); err != nil {
			return err
		}
	}

	if err = validateMonitoring(cluster, timeout, interval); err != nil {
		return err
	}

	return nil
}

func validatePodsByName(namespace, name string, timeout, interval time.Duration) error {
	listOptions := map[string]string{"name": name}
	return validatePods(namespace, listOptions, timeout, interval)
}

func validatePods(namespace string, listOptions map[string]string, timeout, interval time.Duration) error {
	t := func() (interface{}, bool, error) {
		pods, err := coreops.Instance().GetPods(namespace, listOptions)
		if err != nil {
			return nil, true, err
		}
		podReady := 0
		for _, pod := range pods.Items {
			for _, c := range pod.Status.InitContainerStatuses {
				if !c.Ready {
					continue
				}
			}
			containerReady := 0
			for _, c := range pod.Status.ContainerStatuses {
				if c.Ready {
					containerReady++
					continue
				}
			}
			if len(pod.Spec.Containers) == containerReady {
				podReady++
			}
		}
		if len(pods.Items) == podReady {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("pods %+v not ready. Expected: %d Got: %d", listOptions, len(pods.Items), podReady)
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}

	return nil
}

func validateImageOnPods(image, namespace string, listOptions map[string]string) error {
	pods, err := coreops.Instance().GetPods(namespace, listOptions)
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			if container.Image != image {
				return fmt.Errorf("failed to validade image on pod %s container %s, Expected: %s Got: %s",
					pod.Name, container.Name, image, container.Image)
			}
		}
	}
	return nil
}

func validateImageTag(tag, namespace string, listOptions map[string]string) error {
	pods, err := coreops.Instance().GetPods(namespace, listOptions)
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			imageSplit := strings.Split(container.Image, ":")
			imageTag := ""
			if len(imageSplit) == 2 {
				imageTag = imageSplit[1]
			}
			if imageTag != tag {
				return fmt.Errorf("failed to validade image tag on pod %s container %s, Expected: %s Got: %s",
					pod.Name, container.Name, tag, imageTag)
			}
		}
	}
	return nil
}

func validateMonitoring(cluster *corev1.StorageCluster, timeout, interval time.Duration) error {
	if cluster.Spec.Monitoring != nil &&
		((cluster.Spec.Monitoring.EnableMetrics != nil && *cluster.Spec.Monitoring.EnableMetrics) ||
			(cluster.Spec.Monitoring.Prometheus != nil && cluster.Spec.Monitoring.Prometheus.ExportMetrics)) {
		if cluster.Spec.Monitoring.Prometheus != nil && cluster.Spec.Monitoring.Prometheus.Enabled {
			dep := appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "px-prometheus-operator",
					Namespace: cluster.Namespace,
				},
			}
			if err := appops.Instance().ValidateDeployment(&dep, timeout, interval); err != nil {
				return err
			}

			st := appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "px-prometheus-prometheus",
					Namespace: cluster.Namespace,
				},
			}
			if err := appops.Instance().ValidateStatefulSet(&st, timeout); err != nil {
				return err
			}
		}

		t := func() (interface{}, bool, error) {
			_, err := prometheusops.Instance().GetPrometheusRule("portworx", cluster.Namespace)
			if err != nil {
				return nil, true, err
			}
			return nil, false, nil
		}
		if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
			return err
		}

		t = func() (interface{}, bool, error) {
			_, err := prometheusops.Instance().GetServiceMonitor("portworx", cluster.Namespace)
			if err != nil {
				return nil, true, err
			}
			return nil, false, nil
		}
		if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
			return err
		}
	}
	return nil
}

func isPVCControllerEnabled(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/pvc-controller"])
	if err == nil {
		return enabled
	}

	// If portworx is disabled, then do not run pvc controller unless explicitly told to.
	if !isPortworxEnabled(cluster) {
		return false
	}

	// Enable PVC controller for managed kubernetes services. Also enable it for openshift,
	// only if Portworx service is not deployed in kube-system namespace.
	if isPKS(cluster) || isEKS(cluster) ||
		isGKE(cluster) || isAKS(cluster) ||
		(isOpenshift(cluster) && cluster.Namespace != "kube-system") {
		return true
	}
	return false
}

func isPortworxEnabled(cluster *corev1.StorageCluster) bool {
	disabled, err := strconv.ParseBool(cluster.Annotations["operator.libopenstorage.org/disable-storage"])
	return err != nil || !disabled
}

func isPKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-pks"])
	return err == nil && enabled
}

func isGKE(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-gke"])
	return err == nil && enabled
}

func isAKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-aks"])
	return err == nil && enabled
}

func isEKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-eks"])
	return err == nil && enabled
}

func isOpenshift(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations["portworx.io/is-openshift"])
	return err == nil && enabled
}

func getK8SVersion() (string, error) {
	kbVerRegex := regexp.MustCompile(`^(v\d+\.\d+\.\d+).*`)
	k8sVersion, err := coreops.Instance().GetVersion()
	if err != nil {
		return "", fmt.Errorf("unable to get kubernetes version: %v", err)
	}
	matches := kbVerRegex.FindStringSubmatch(k8sVersion.GitVersion)
	if len(matches) < 2 {
		return "", fmt.Errorf("invalid kubernetes version received: %v", k8sVersion.GitVersion)
	}
	return matches[1], nil
}
