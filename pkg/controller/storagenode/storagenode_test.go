package storagenode

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/golang/mock/gomock"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestRegisterCRD(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	fakeExtClient := fakeextclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	group := corev1alpha1.SchemeGroupVersion.Group
	storageNodeCRDName := corev1alpha1.StorageNodeResourcePlural + "." + group

	// When the CRDs are created, just updated their status so the validation
	// does not get stuck until timeout.

	go func() {
		err := testutil.ActivateCRDWhenCreated(fakeExtClient, storageNodeCRDName)
		require.NoError(t, err)
	}()

	controller := Controller{}

	// Should fail if the CRD specs are not found
	err := controller.RegisterCRD()
	require.Error(t, err)

	// Set the correct crd path
	crdBaseDir = func() string {
		return "../../../deploy/crds"
	}
	defer func() {
		crdBaseDir = getCRDBasePath
	}()

	err = controller.RegisterCRD()
	require.NoError(t, err)

	crds, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		List(metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)

	subresource := &apiextensionsv1beta1.CustomResourceSubresources{
		Status: &apiextensionsv1beta1.CustomResourceSubresourceStatus{},
	}
	crd, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(storageNodeCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, storageNodeCRDName, crd.Name)
	require.Equal(t, corev1alpha1.SchemeGroupVersion.Group, crd.Spec.Group)
	require.Len(t, crd.Spec.Versions, 1)
	require.Equal(t, corev1alpha1.SchemeGroupVersion.Version, crd.Spec.Versions[0].Name)
	require.True(t, crd.Spec.Versions[0].Served)
	require.True(t, crd.Spec.Versions[0].Storage)
	require.Equal(t, apiextensionsv1beta1.NamespaceScoped, crd.Spec.Scope)
	require.Equal(t, corev1alpha1.StorageNodeResourceName, crd.Spec.Names.Singular)
	require.Equal(t, corev1alpha1.StorageNodeResourcePlural, crd.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(corev1alpha1.StorageNode{}).Name(), crd.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(corev1alpha1.StorageNodeList{}).Name(), crd.Spec.Names.ListKind)
	require.Equal(t, []string{corev1alpha1.StorageNodeShortName}, crd.Spec.Names.ShortNames)
	require.Equal(t, subresource, crd.Spec.Subresources)
	require.NotEmpty(t, crd.Spec.Validation.OpenAPIV3Schema.Properties)

	// If CRDs are already present, then should not fail
	err = controller.RegisterCRD()
	require.NoError(t, err)

	crds, err = fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		List(metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)
	require.Equal(t, storageNodeCRDName, crds.Items[0].Name)
}

func TestRegisterCRDShouldRemoveNodeStatusCRD(t *testing.T) {
	nodeStatusCRDName := fmt.Sprintf("%s.%s",
		storageNodeStatusPlural,
		corev1alpha1.SchemeGroupVersion.Group,
	)
	nodeStatusCRD := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeStatusCRDName,
		},
	}
	fakeClient := fakek8sclient.NewSimpleClientset()
	fakeExtClient := fakeextclient.NewSimpleClientset(nodeStatusCRD)
	coreops.SetInstance(coreops.New(fakeClient))
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	crdBaseDir = func() string {
		return "../../../deploy/crds"
	}
	defer func() {
		crdBaseDir = getCRDBasePath
	}()

	group := corev1alpha1.SchemeGroupVersion.Group
	storageClusterCRDName := corev1alpha1.StorageClusterResourcePlural + "." + group
	storageNodeCRDName := corev1alpha1.StorageNodeResourcePlural + "." + group

	// When the CRDs are created, just updated their status so the validation
	// does not get stuck until timeout.
	go func() {
		err := testutil.ActivateCRDWhenCreated(fakeExtClient, storageClusterCRDName)
		require.NoError(t, err)
	}()
	go func() {
		err := testutil.ActivateCRDWhenCreated(fakeExtClient, storageNodeCRDName)
		require.NoError(t, err)
	}()

	controller := Controller{}

	err := controller.RegisterCRD()
	require.NoError(t, err)

	crds, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		List(metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)
	for _, crd := range crds.Items {
		require.NotEqual(t, nodeStatusCRDName, crd.Name)
	}
}

func TestReconcile(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defaultQuantity, _ := resource.ParseQuantity("0")
	testNS := "test-ns"
	clusterName := "test-cluster"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: testNS,
		},
	}

	controllerKind := corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	testStorageNode := "node1"
	testStoragelessNode := "node2"
	storageNode := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testStorageNode,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1alpha1.NodeStatus{
			Phase: string(corev1alpha1.NodeInitStatus),
			Storage: corev1alpha1.StorageStatus{
				TotalSize: *resource.NewQuantity(20971520, resource.BinarySI),
				UsedSize:  *resource.NewQuantity(10971520, resource.BinarySI),
			},
			Conditions: []corev1alpha1.NodeCondition{
				{
					Type:   corev1alpha1.NodeStateCondition,
					Status: corev1alpha1.NodeOnlineStatus,
				},
			},
		},
	}

	storageLessNode := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testStoragelessNode,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1alpha1.NodeStatus{
			Phase: string(corev1alpha1.NodeInitStatus),
			Storage: corev1alpha1.StorageStatus{
				TotalSize: defaultQuantity,
				UsedSize:  defaultQuantity,
			},
			Conditions: []corev1alpha1.NodeCondition{
				{
					Type:   corev1alpha1.NodeStateCondition,
					Status: corev1alpha1.NodeOnlineStatus,
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(storageNode, storageLessNode, cluster)
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()

	// reconcile storage node
	podNode1 := createStoragePod(cluster, "pod-node1", testStorageNode, nil, clusterRef)
	k8sClient.Create(context.TODO(), podNode1)

	podNode2 := createStoragePod(cluster, "pod-node2", testStoragelessNode, nil, clusterRef)
	k8sClient.Create(context.TODO(), podNode2)

	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
		Driver:   driver,
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testStorageNode,
			Namespace: testNS,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.NotNil(t, result)

	// reconcile storageless node
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testStoragelessNode,
			Namespace: testNS,
		},
	}
	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.NotNil(t, result)

	// reconcile non-existing node
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "no-such-node",
			Namespace: testNS,
		},
	}
	_, err = controller.Reconcile(request)
	require.NoError(t, err)
}

func createStoragePod(
	cluster *corev1alpha1.StorageCluster,
	podName, nodeName string,
	labels map[string]string,
	clusterRef *metav1.OwnerReference,
) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            podName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: v1.PodSpec{
			NodeName: nodeName,
		},
	}
}
