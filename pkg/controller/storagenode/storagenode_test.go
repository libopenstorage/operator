package storagenode

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/scheme"
	"github.com/libopenstorage/operator/pkg/mock"

	"github.com/golang/mock/gomock"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/constants"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestInit(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	fakeClient := fakek8sclient.NewSimpleClientset()
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakeClient))
	recorder := record.NewFakeRecorder(10)

	mgr := mock.NewMockManager(mockCtrl)
	mgr.EXPECT().GetClient().Return(k8sClient).AnyTimes()
	mgr.EXPECT().GetScheme().Return(scheme.Scheme).AnyTimes()
	mgr.EXPECT().GetEventRecorderFor(gomock.Any()).Return(recorder).AnyTimes()
	mgr.EXPECT().GetConfig().Return(&rest.Config{
		Host:    "127.0.0.1",
		APIPath: "fake",
	}).AnyTimes()
	mgr.EXPECT().SetFields(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().GetCache().Return(nil).AnyTimes()
	mgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()

	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
	}
	err := controller.Init(mgr)
	require.NoError(t, err)

	ctrl := mock.NewMockController(mockCtrl)
	controller.ctrl = ctrl
	ctrl.EXPECT().Watch(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	err = controller.StartWatch()
	require.NoError(t, err)
}

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

	snCRD, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(storageNodeCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, storageNodeCRDName, snCRD.Name)
	require.Equal(t, corev1alpha1.SchemeGroupVersion.Group, snCRD.Spec.Group)
	require.Len(t, snCRD.Spec.Versions, 1)
	require.Equal(t, corev1alpha1.SchemeGroupVersion.Version, snCRD.Spec.Versions[0].Name)
	require.True(t, snCRD.Spec.Versions[0].Served)
	require.True(t, snCRD.Spec.Versions[0].Storage)
	require.Equal(t, apiextensionsv1beta1.NamespaceScoped, snCRD.Spec.Scope)
	require.Equal(t, corev1alpha1.StorageNodeResourceName, snCRD.Spec.Names.Singular)
	require.Equal(t, corev1alpha1.StorageNodeResourcePlural, snCRD.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(corev1alpha1.StorageNode{}).Name(), snCRD.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(corev1alpha1.StorageNodeList{}).Name(), snCRD.Spec.Names.ListKind)
	require.Equal(t, []string{corev1alpha1.StorageNodeShortName}, snCRD.Spec.Names.ShortNames)
	require.Equal(t, subresource, snCRD.Spec.Subresources)
	require.NotEmpty(t, snCRD.Spec.Validation.OpenAPIV3Schema.Properties)

	snCRD.ResourceVersion = "1000"
	fakeExtClient.ApiextensionsV1beta1().CustomResourceDefinitions().Update(snCRD)

	go func() {
		err := keepCRDActivated(fakeExtClient, storageNodeCRDName)
		require.NoError(t, err)
	}()

	// If CRDs are already present, then should not fail
	err = controller.RegisterCRD()
	require.NoError(t, err)

	crds, err = fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		List(metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)
	require.Equal(t, storageNodeCRDName, crds.Items[0].Name)

	snCRD, err = fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(storageNodeCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "1000", snCRD.ResourceVersion)
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
	logrus.SetLevel(logrus.TraceLevel)
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
	driver.EXPECT().String().Return("ut-driver").AnyTimes()

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
	_, err := controller.Reconcile(request)
	require.NoError(t, err)

	checkStoragePod := &v1.Pod{}
	err = testutil.Get(controller.client, checkStoragePod, podNode1.Name, podNode1.Namespace)
	require.NoError(t, err)
	value, present := checkStoragePod.Labels[constants.LabelKeyStoragePod]
	require.Truef(t, present, "pod: %s/%s should have had the %s label",
		podNode1.Namespace, podNode1.Name, constants.LabelKeyStoragePod)
	require.Equal(t, "true", value)

	// now makes this node non-storage and ensure reconcile removes the label
	storageNode.Status.Conditions = nil
	err = controller.client.Update(context.TODO(), storageNode)
	require.NoError(t, err)

	_, err = controller.Reconcile(request)
	require.NoError(t, err)

	checkStoragePod = &v1.Pod{}
	err = testutil.Get(controller.client, checkStoragePod, podNode1.Name, podNode1.Namespace)
	require.NoError(t, err)
	_, present = checkStoragePod.Labels[constants.LabelKeyStoragePod]
	require.False(t, present)

	// reconcile storageless node
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testStoragelessNode,
			Namespace: testNS,
		},
	}
	_, err = controller.Reconcile(request)
	require.NoError(t, err)
	checkStorageLessPod := &v1.Pod{}
	err = testutil.Get(controller.client, checkStorageLessPod, podNode2.Name, podNode2.Namespace)
	require.NoError(t, err)
	_, present = checkStoragePod.Labels[constants.LabelKeyStoragePod]
	require.False(t, present)

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

// TestReconcileKVDB focuses on reconciling a StorageNode which is running KVDB
func TestReconcileKVDB(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	mockCtrl := gomock.NewController(t)
	testNS := "test-ns"
	clusterName := "test-cluster"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: testNS,
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
		},
	}

	controllerKind := corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	// node1 will not have any kvdb pods. So test will create
	testKVDBNode1 := "node1"
	kvdbNode1 := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testKVDBNode1,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1alpha1.NodeStatus{
			Phase: string(corev1alpha1.NodeInitStatus),
			Conditions: []corev1alpha1.NodeCondition{
				{
					Type:   corev1alpha1.NodeKVDBCondition,
					Status: corev1alpha1.NodeOnlineStatus,
				},
			},
		},
	}

	k8sNode1 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testKVDBNode1,
		},
		Status: v1.NodeStatus{
			Phase: v1.NodeRunning,
		},
	}

	// node2 will have kvdb pods but node is no longer kvdb. So test will check for deleted pods
	testKVDBNode2 := "node2"
	kvdbNode2 := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testKVDBNode2,
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

	k8sNode2 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testKVDBNode2,
		},
		Status: v1.NodeStatus{
			Phase: v1.NodeRunning,
		},
	}

	k8sClient := testutil.FakeK8sClient(kvdbNode1, kvdbNode2, k8sNode1, k8sNode2, cluster)
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("ut-driver").AnyTimes()
	driver.EXPECT().GetKVDBPodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
		Driver:   driver,
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testKVDBNode1,
			Namespace: testNS,
		},
	}
	_, err := controller.Reconcile(request)
	require.NoError(t, err)

	// check if reconcile created kvdb pods
	podList := &v1.PodList{}
	fieldSelector := fields.SelectorFromSet(map[string]string{"nodeName": kvdbNode1.Name})
	err = controller.client.List(context.TODO(), podList, &client.ListOptions{
		Namespace:     kvdbNode1.Namespace,
		LabelSelector: labels.SelectorFromSet(controller.kvdbPodLabels(cluster)),
		FieldSelector: fieldSelector,
	})
	require.NoError(t, err)
	require.Len(t, podList.Items, 1)

	// test reconcile when k8s node has been deleted
	err = controller.client.Delete(context.TODO(), k8sNode1)
	require.NoError(t, err)
	_, err = controller.Reconcile(request)
	require.NoError(t, err)

	podNode2 := createStoragePod(cluster, "kvdb-pod-node2", testKVDBNode2, controller.kvdbPodLabels(cluster), clusterRef)
	k8sClient.Create(context.TODO(), podNode2)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testKVDBNode2,
			Namespace: testNS,
		},
	}
	_, err = controller.Reconcile(request)
	require.NoError(t, err)

	checkPod := &v1.Pod{}
	err = k8sClient.Get(context.TODO(), client.ObjectKey{
		Name:      podNode2.Name,
		Namespace: podNode2.Namespace,
	}, checkPod)
	require.Error(t, err)
	require.Empty(t, checkPod.Name)

}

func TestSyncStorageNodeErrors(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	mockCtrl := gomock.NewController(t)
	testNS := "test-ns"
	clusterName := "test-cluster"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: testNS,
		},
	}

	testStorageNode := "node1"
	storageNode := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testStorageNode,
			Namespace: cluster.Namespace,
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

	k8sClient := testutil.FakeK8sClient(storageNode, cluster)
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
		Driver:   driver,
	}

	// no owner refs
	err := controller.syncStorageNode(storageNode)
	require.NoError(t, err)

	// invalid owner kind
	controllerKind := corev1alpha1.SchemeGroupVersion.WithKind("InvalidOwner")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	storageNode.OwnerReferences = []metav1.OwnerReference{*clusterRef}
	err = controller.syncStorageNode(storageNode)
	require.Error(t, err)

	// invalid owner name
	cluster.Name = "invalid-owner-name"
	controllerKind = corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef = metav1.NewControllerRef(cluster, controllerKind)
	storageNode.OwnerReferences = []metav1.OwnerReference{*clusterRef}
	err = controller.syncStorageNode(storageNode)
	require.Error(t, err)
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

func keepCRDActivated(fakeClient *fakeextclient.Clientset, crdName string) error {
	return wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
		crd, err := fakeClient.ApiextensionsV1beta1().
			CustomResourceDefinitions().
			Get(crdName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(crd.Status.Conditions) == 0 {
			crd.Status.Conditions = []apiextensionsv1beta1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1beta1.Established,
				Status: apiextensionsv1beta1.ConditionTrue,
			}}
			fakeClient.ApiextensionsV1beta1().CustomResourceDefinitions().UpdateStatus(crd)
			return true, nil
		}
		return false, nil
	})
}
