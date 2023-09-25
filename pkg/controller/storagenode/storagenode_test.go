package storagenode

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/scheme"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
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
	mgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().GetCache().Return(nil).AnyTimes()
	mgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().GetLogger().Return(log.Log.WithName("test")).AnyTimes()

	controller := Controller{
		client:      k8sClient,
		recorder:    recorder,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
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
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "v1.16.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	group := corev1.SchemeGroupVersion.Group
	storageNodeCRDName := corev1.StorageNodeResourcePlural + "." + group

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

	crds, err := fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)

	subresource := &apiextensionsv1.CustomResourceSubresources{
		Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
	}
	snCRD, err := fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageNodeCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, storageNodeCRDName, snCRD.Name)
	require.Equal(t, corev1.SchemeGroupVersion.Group, snCRD.Spec.Group)
	require.Len(t, snCRD.Spec.Versions, 2)
	require.Equal(t, corev1.SchemeGroupVersion.Version, snCRD.Spec.Versions[0].Name)
	require.True(t, snCRD.Spec.Versions[0].Served)
	require.True(t, snCRD.Spec.Versions[0].Storage)
	require.Equal(t, subresource, snCRD.Spec.Versions[0].Subresources)
	require.NotEmpty(t, snCRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties)
	require.Equal(t, "v1alpha1", snCRD.Spec.Versions[1].Name)
	require.False(t, snCRD.Spec.Versions[1].Served)
	require.False(t, snCRD.Spec.Versions[1].Storage)
	require.Equal(t, subresource, snCRD.Spec.Versions[1].Subresources)
	require.NotEmpty(t, snCRD.Spec.Versions[1].Schema.OpenAPIV3Schema)
	require.Empty(t, snCRD.Spec.Versions[1].Schema.OpenAPIV3Schema.Properties)
	require.Equal(t, apiextensionsv1.NamespaceScoped, snCRD.Spec.Scope)
	require.Equal(t, corev1.StorageNodeResourceName, snCRD.Spec.Names.Singular)
	require.Equal(t, corev1.StorageNodeResourcePlural, snCRD.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(corev1.StorageNode{}).Name(), snCRD.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(corev1.StorageNodeList{}).Name(), snCRD.Spec.Names.ListKind)
	require.Equal(t, []string{corev1.StorageNodeShortName}, snCRD.Spec.Names.ShortNames)

	snCRD.ResourceVersion = "1000"
	_, err = fakeExtClient.ApiextensionsV1().CustomResourceDefinitions().Update(context.TODO(), snCRD, metav1.UpdateOptions{})
	require.NoError(t, err)

	go func() {
		err := keepCRDActivated(fakeExtClient, storageNodeCRDName)
		require.NoError(t, err)
	}()

	// If CRDs are already present, then should not fail
	err = controller.RegisterCRD()
	require.NoError(t, err)

	crds, err = fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)
	require.Equal(t, storageNodeCRDName, crds.Items[0].Name)

	snCRD, err = fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageNodeCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "1000", snCRD.ResourceVersion)
}

func TestRegisterDeprecatedCRD(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "v1.15.99",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	group := corev1.SchemeGroupVersion.Group
	storageNodeCRDName := corev1.StorageNodeResourcePlural + "." + group

	// When the CRDs are created, just updated their status so the validation
	// does not get stuck until timeout.

	go func() {
		err := testutil.ActivateV1beta1CRDWhenCreated(fakeExtClient, storageNodeCRDName)
		require.NoError(t, err)
	}()

	controller := Controller{}

	// Should fail if the CRD specs are not found
	err := controller.RegisterCRD()
	require.Error(t, err)

	// Set the correct crd path
	deprecatedCRDBaseDir = func() string {
		return "../../../deploy/crds/deprecated"
	}
	defer func() {
		deprecatedCRDBaseDir = getDeprecatedCRDBasePath
	}()

	err = controller.RegisterCRD()
	require.NoError(t, err)

	crds, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)

	subresource := &apiextensionsv1beta1.CustomResourceSubresources{
		Status: &apiextensionsv1beta1.CustomResourceSubresourceStatus{},
	}
	snCRD, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageNodeCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, storageNodeCRDName, snCRD.Name)
	require.Equal(t, corev1.SchemeGroupVersion.Group, snCRD.Spec.Group)
	require.Len(t, snCRD.Spec.Versions, 2)
	require.Equal(t, corev1.SchemeGroupVersion.Version, snCRD.Spec.Versions[0].Name)
	require.True(t, snCRD.Spec.Versions[0].Served)
	require.True(t, snCRD.Spec.Versions[0].Storage)
	require.Equal(t, "v1alpha1", snCRD.Spec.Versions[1].Name)
	require.False(t, snCRD.Spec.Versions[1].Served)
	require.False(t, snCRD.Spec.Versions[1].Storage)
	require.Equal(t, apiextensionsv1beta1.NamespaceScoped, snCRD.Spec.Scope)
	require.Equal(t, corev1.StorageNodeResourceName, snCRD.Spec.Names.Singular)
	require.Equal(t, corev1.StorageNodeResourcePlural, snCRD.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(corev1.StorageNode{}).Name(), snCRD.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(corev1.StorageNodeList{}).Name(), snCRD.Spec.Names.ListKind)
	require.Equal(t, []string{corev1.StorageNodeShortName}, snCRD.Spec.Names.ShortNames)
	require.Equal(t, subresource, snCRD.Spec.Subresources)
	require.NotEmpty(t, snCRD.Spec.Validation.OpenAPIV3Schema.Properties)

	snCRD.ResourceVersion = "1000"
	_, err = fakeExtClient.ApiextensionsV1beta1().CustomResourceDefinitions().Update(context.TODO(), snCRD, metav1.UpdateOptions{})
	require.NoError(t, err)

	go func() {
		err := keepV1beta1CRDActivated(fakeExtClient, storageNodeCRDName)
		require.NoError(t, err)
	}()

	// If CRDs are already present, then should not fail
	err = controller.RegisterCRD()
	require.NoError(t, err)

	crds, err = fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, crds.Items, 1)
	require.Equal(t, storageNodeCRDName, crds.Items[0].Name)

	snCRD, err = fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageNodeCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "1000", snCRD.ResourceVersion)
}

func TestReconcile(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	defaultQuantity, _ := resource.ParseQuantity("0")
	testNS := "test-ns"
	clusterName := "test-cluster"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: testNS,
		},
	}

	controllerKind := corev1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	testStorageNode := "node1"
	testStoragelessNode := "node2"
	trueValue := true
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testStorageNode,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
			Storage: &corev1.StorageStatus{
				TotalSize: *resource.NewQuantity(20971520, resource.BinarySI),
				UsedSize:  *resource.NewQuantity(10971520, resource.BinarySI),
			},
			NodeAttributes: &corev1.NodeAttributes{
				Storage: &trueValue,
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeStateCondition,
					Status: corev1.NodeOnlineStatus,
				},
			},
		},
	}

	storageLessNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testStoragelessNode,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
			Storage: &corev1.StorageStatus{
				TotalSize: defaultQuantity,
				UsedSize:  defaultQuantity,
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeStateCondition,
					Status: corev1.NodeOnlineStatus,
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
	err := k8sClient.Create(context.TODO(), podNode1)
	require.NoError(t, err)

	podNode2 := createStoragePod(cluster, "pod-node2", testStoragelessNode, nil, clusterRef)
	err = k8sClient.Create(context.TODO(), podNode2)
	require.NoError(t, err)

	controller := Controller{
		client:      k8sClient,
		recorder:    recorder,
		Driver:      driver,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testStorageNode,
			Namespace: testNS,
		},
	}
	_, err = controller.Reconcile(context.TODO(), request)
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

	_, err = controller.Reconcile(context.TODO(), request)
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
	_, err = controller.Reconcile(context.TODO(), request)
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
	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
}

func TestReconcileForSafeToEvictAnnotation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
	}
	controllerKind := corev1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	trueValue := true
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeStateCondition,
					Status: corev1.NodeOnlineStatus,
				},
			},
			NodeAttributes: &corev1.NodeAttributes{
				Storage: &trueValue,
			},
		},
	}

	falseValue := false
	storageLessNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeStateCondition,
					Status: corev1.NodeOnlineStatus,
				},
			},
			NodeAttributes: &corev1.NodeAttributes{
				Storage: &falseValue,
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(storageNode, storageLessNode, cluster)
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	controller := Controller{
		client:      k8sClient,
		recorder:    recorder,
		Driver:      driver,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("ut-driver").AnyTimes()

	podNode1 := createStoragePod(cluster, "pod-node1", storageNode.Name, nil, clusterRef)
	err := k8sClient.Create(context.TODO(), podNode1)
	require.NoError(t, err)

	podNode2 := createStoragePod(cluster, "pod-node2", storageLessNode.Name, nil, clusterRef)
	err = k8sClient.Create(context.TODO(), podNode2)
	require.NoError(t, err)

	// TestCase: Reconcile storage node to verify annotation is not added
	storageNodeRequest := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      storageNode.Name,
			Namespace: cluster.Namespace,
		},
	}
	_, err = controller.Reconcile(context.TODO(), storageNodeRequest)
	require.NoError(t, err)

	checkStoragePod := &v1.Pod{}
	err = testutil.Get(controller.client, checkStoragePod, podNode1.Name, podNode1.Namespace)
	require.NoError(t, err)
	_, present := checkStoragePod.Annotations[constants.AnnotationPodSafeToEvict]
	require.False(t, present)

	// TestCase: Reconcile storageless node to verify annotation is added
	storageLessNodeRequest := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      storageLessNode.Name,
			Namespace: cluster.Namespace,
		},
	}
	_, err = controller.Reconcile(context.TODO(), storageLessNodeRequest)
	require.NoError(t, err)

	checkStorageLessPod := &v1.Pod{}
	err = testutil.Get(controller.client, checkStorageLessPod, podNode2.Name, podNode2.Namespace)
	require.NoError(t, err)
	value, present := checkStorageLessPod.Annotations[constants.AnnotationPodSafeToEvict]
	require.True(t, present)
	require.Equal(t, value, constants.LabelValueTrue)

	// TestCase: Annotation value should be overwritten if not set to true
	checkStorageLessPod.Annotations[constants.AnnotationPodSafeToEvict] = "false"
	err = k8sClient.Update(context.TODO(), checkStorageLessPod)
	require.NoError(t, err)

	_, err = controller.Reconcile(context.TODO(), storageLessNodeRequest)
	require.NoError(t, err)

	checkStorageLessPod = &v1.Pod{}
	err = testutil.Get(controller.client, checkStorageLessPod, podNode2.Name, podNode2.Namespace)
	require.NoError(t, err)
	value, present = checkStorageLessPod.Annotations[constants.AnnotationPodSafeToEvict]
	require.True(t, present)
	require.Equal(t, value, constants.LabelValueTrue)

	// TestCase: Annotation should not be added for storageless kvdb nodes
	checkStorageLessPod.Annotations = nil
	err = k8sClient.Update(context.TODO(), checkStorageLessPod)
	require.NoError(t, err)
	hasKvdb := true
	storageLessNode.Status.NodeAttributes.KVDB = &hasKvdb
	err = k8sClient.Update(context.TODO(), storageLessNode)
	require.NoError(t, err)

	_, err = controller.Reconcile(context.TODO(), storageLessNodeRequest)
	require.NoError(t, err)

	checkStorageLessPod = &v1.Pod{}
	err = testutil.Get(controller.client, checkStorageLessPod, podNode2.Name, podNode2.Namespace)
	require.NoError(t, err)
	_, present = checkStorageLessPod.Annotations[constants.AnnotationPodSafeToEvict]
	require.False(t, present)
}

// TestReconcileKVDB focuses on reconciling a StorageNode which is running KVDB
func TestReconcileKVDB(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	testNS := "test-ns"
	clusterName := "test-cluster"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: testNS,
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
	}

	controllerKind := corev1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	// node1 will not have any kvdb pods. So test will create
	testKVDBNode1 := "node1"
	trueValue := true
	kvdbNode1 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testKVDBNode1,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
			NodeAttributes: &corev1.NodeAttributes{
				KVDB: &trueValue,
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
	falseValue := false
	kvdbNode2 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testKVDBNode2,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
			Storage: &corev1.StorageStatus{
				TotalSize: *resource.NewQuantity(20971520, resource.BinarySI),
				UsedSize:  *resource.NewQuantity(10971520, resource.BinarySI),
			},
			NodeAttributes: &corev1.NodeAttributes{
				KVDB:    &falseValue,
				Storage: &trueValue,
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
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("ut-driver").AnyTimes()
	podNode1 := v1.PodSpec{}
	podNode1.NodeName = "node1"
	driver.EXPECT().GetKVDBPodSpec(gomock.Any(), "node1").Return(podNode1, nil).AnyTimes()

	controller := Controller{
		client:      k8sClient,
		recorder:    recorder,
		Driver:      driver,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testKVDBNode1,
			Namespace: testNS,
		},
	}
	_, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	podList, err := coreops.Instance().GetPods(cluster.Namespace, controller.kvdbPodLabels(cluster))
	require.NoError(t, err)
	var kvdbPods []v1.Pod
	for _, p := range podList.Items {
		if p.Spec.NodeName == kvdbNode1.Name {
			kvdbPods = append(kvdbPods, p)
		}
	}
	require.NoError(t, err)
	require.Len(t, kvdbPods, 1)

	// test reconcile when k8s node has been deleted
	err = controller.client.Delete(context.TODO(), k8sNode1)
	require.NoError(t, err)
	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	podNode2 := createStoragePod(cluster, "kvdb-pod-node2", testKVDBNode2, controller.kvdbPodLabels(cluster), clusterRef)
	_, err = coreops.Instance().CreatePod(podNode2)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testKVDBNode2,
			Namespace: testNS,
		},
	}
	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	_, err = coreops.Instance().GetPodByName(podNode2.Name, podNode2.Namespace)
	require.Error(t, err)
}

func TestReconcileKVDBOddSatusPodCleanup(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	testNS := "test-ns"
	clusterName := "test-cluster"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: testNS,
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
	}

	clusterRef := metav1.NewControllerRef(cluster, corev1.SchemeGroupVersion.WithKind("StorageCluster"))

	// node2 will have kvdb pods but node is no longer kvdb. So test will check for deleted pods
	testKVDBNode2 := "node2"
	trueValue := true
	kvdbNode2 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testKVDBNode2,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
			Storage: &corev1.StorageStatus{
				TotalSize: *resource.NewQuantity(20971520, resource.BinarySI),
				UsedSize:  *resource.NewQuantity(10971520, resource.BinarySI),
			},
			NodeAttributes: &corev1.NodeAttributes{
				KVDB:    &trueValue,
				Storage: &trueValue,
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

	k8sClient := testutil.FakeK8sClient(kvdbNode2, k8sNode2, cluster)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("ut-driver").AnyTimes()

	controller := Controller{
		client:      k8sClient,
		recorder:    recorder,
		Driver:      driver,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
	}

	kvdbPodLabs := controller.kvdbPodLabels(cluster)

	// add 3 KVDB pods to the same node, all in weird non-running states
	testPodsData := []struct {
		name   string
		reason string
		msg    string
	}{
		{"kvdb-term1", "Terminated", "Pod was terminated in response to imminent node shutdown."},
		{"kvdb-evict2", "Evicted", "..."},
		{"kvdb-out3", "OutOfPods", "..."},
	}
	for i, td := range testPodsData {
		_, err := coreops.Instance().CreatePod(&v1.Pod{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Pod",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      td.name,
				Namespace: cluster.Namespace,
				Labels:    kvdbPodLabs,
			},
			Spec: v1.PodSpec{
				NodeName: testKVDBNode2,
			},
			Status: v1.PodStatus{
				Reason:  td.reason,
				Message: td.msg,
			},
		})
		require.NoError(t, err, "Unexpected error for test #d / %v", i+i, td)
	}
	// verify we got 3 quirky kvdb-pods
	podList, err := coreops.Instance().GetPods(cluster.Namespace, kvdbPodLabs)
	require.NoError(t, err)
	require.Len(t, podList.Items, 3)

	podNode2 := v1.PodSpec{
		NodeName: testKVDBNode2,
	}
	driver.EXPECT().GetKVDBPodSpec(gomock.Any(), "node2").Return(podNode2, nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testKVDBNode2,
			Namespace: testNS,
		},
	}
	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	// after reconcile, we expect odd-status kvdb pods gone, and new pod set up
	podList, err = coreops.Instance().GetPods(cluster.Namespace, kvdbPodLabs)
	require.NoError(t, err)
	require.Len(t, podList.Items, 1)
	require.Empty(t, podList.Items[0].Name)
	require.Empty(t, podList.Items[0].Status.Reason)
}

func TestReconcileKVDBWithNodeChanges(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
	}

	controllerKind := corev1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	trueValue := true
	kvdbNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "kvdb-node",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
			NodeAttributes: &corev1.NodeAttributes{
				KVDB: &trueValue,
			},
		},
	}

	machine := &cluster_v1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine",
			Namespace: "default",
		},
	}

	k8sNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: kvdbNode.Name,
			Annotations: map[string]string{
				constants.AnnotationClusterAPIMachine: "machine",
			},
		},
		Status: v1.NodeStatus{
			Phase: v1.NodeRunning,
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient(cluster, kvdbNode, k8sNode, machine)
	driver := testutil.MockDriver(mockCtrl)
	controller := Controller{
		client:      k8sClient,
		Driver:      driver,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("ut-driver").AnyTimes()
	podNode1 := v1.PodSpec{}
	podNode1.NodeName = "kvdb-node"
	driver.EXPECT().GetKVDBPodSpec(gomock.Any(), podNode1.NodeName).Return(podNode1, nil).AnyTimes()

	// TestCase: Do not create kvdb pod if associated machine is being deleted
	now := metav1.Now()
	machine.DeletionTimestamp = &now
	machine.Finalizers = []string{"do-not-delete-yet"}
	err := k8sClient.Update(context.TODO(), machine)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      kvdbNode.Name,
			Namespace: cluster.Namespace,
		},
	}
	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	podList := &v1.PodList{}
	err = testutil.List(k8sClient, podList)
	require.NoError(t, err)
	require.Empty(t, podList.Items)

	// TestCase: Create kvdb pod if associated machine is not being deleted
	machine.DeletionTimestamp = nil
	err = k8sClient.Update(context.TODO(), machine)
	require.NoError(t, err)

	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	podList, err = coreops.Instance().GetPods(cluster.Namespace, controller.kvdbPodLabels(cluster))
	require.NoError(t, err)
	require.NoError(t, err)
	require.Len(t, podList.Items, 1)
	require.Equal(t, controller.kvdbPodLabels(cluster), podList.Items[0].Labels)

	// TestCase: Create kvdb pod if associated machine is not found
	k8sNode.Annotations[constants.AnnotationClusterAPIMachine] = "not-present"
	err = k8sClient.Update(context.TODO(), k8sNode)
	require.NoError(t, err)
	err = coreops.Instance().DeletePod(podList.Items[0].Name, podList.Items[0].Namespace, false)
	require.NoError(t, err)

	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	podList, err = coreops.Instance().GetPods(cluster.Namespace, controller.kvdbPodLabels(cluster))
	require.NoError(t, err)
	require.Len(t, podList.Items, 1)
	require.Equal(t, controller.kvdbPodLabels(cluster), podList.Items[0].Labels)

	// TestCase: Do not create kvdb pod if node is recently cordoned
	k8sNode.Annotations = nil
	k8sNode.Spec.Unschedulable = true
	timeAdded := metav1.Now()
	k8sNode.Spec.Taints = []v1.Taint{
		{
			Key:       v1.TaintNodeUnschedulable,
			TimeAdded: &timeAdded,
		},
	}
	err = k8sClient.Update(context.TODO(), k8sNode)
	require.NoError(t, err)
	err = coreops.Instance().DeletePod(podList.Items[0].Name, podList.Items[0].Namespace, false)
	require.NoError(t, err)

	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	podList, err = coreops.Instance().GetPods(cluster.Namespace, controller.kvdbPodLabels(cluster))
	require.NoError(t, err)
	require.Empty(t, podList.Items)

	// TestCase: Create kvdb pod if node was cordoned long time ago, and pod was created long time ago.
	for k, v := range controller.nodeInfoMap {
		controller.nodeInfoMap[k].LastPodCreationTime = v.LastPodCreationTime.Add(-time.Hour)
	}
	timeAdded = metav1.NewTime(
		metav1.Now().
			Add(-constants.DefaultCordonedRestartDelay).
			Add(-time.Second),
	)
	err = k8sClient.Update(context.TODO(), k8sNode)
	require.NoError(t, err)

	_, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)

	podList, err = coreops.Instance().GetPods(cluster.Namespace, controller.kvdbPodLabels(cluster))
	require.NoError(t, err)
	require.Len(t, podList.Items, 1)
	require.Equal(t, controller.kvdbPodLabels(cluster), podList.Items[0].Labels)
}

func TestSyncStorageNodeErrors(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	testNS := "test-ns"
	clusterName := "test-cluster"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: testNS,
		},
	}

	testStorageNode := "node1"
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testStorageNode,
			Namespace: cluster.Namespace,
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
			Storage: &corev1.StorageStatus{
				TotalSize: *resource.NewQuantity(20971520, resource.BinarySI),
				UsedSize:  *resource.NewQuantity(10971520, resource.BinarySI),
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeStateCondition,
					Status: corev1.NodeOnlineStatus,
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(storageNode, cluster)
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	controller := Controller{
		client:      k8sClient,
		recorder:    recorder,
		Driver:      driver,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
	}

	// no owner refs
	err := controller.syncStorageNode(storageNode)
	require.NoError(t, err)

	// invalid owner kind
	controllerKind := corev1.SchemeGroupVersion.WithKind("InvalidOwner")
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	storageNode.OwnerReferences = []metav1.OwnerReference{*clusterRef}
	err = controller.syncStorageNode(storageNode)
	require.Error(t, err)

	// invalid owner name
	cluster.Name = "invalid-owner-name"
	controllerKind = corev1.SchemeGroupVersion.WithKind("StorageCluster")
	clusterRef = metav1.NewControllerRef(cluster, controllerKind)
	storageNode.OwnerReferences = []metav1.OwnerReference{*clusterRef}
	err = controller.syncStorageNode(storageNode)
	require.Error(t, err)
}

func createStoragePod(
	cluster *corev1.StorageCluster,
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
	return wait.PollUntilContextTimeout(context.TODO(), 1*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		crd, err := fakeClient.ApiextensionsV1().
			CustomResourceDefinitions().
			Get(context.TODO(), crdName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(crd.Status.Conditions) == 0 {
			crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			}}
			_, err = fakeClient.ApiextensionsV1().CustomResourceDefinitions().UpdateStatus(context.TODO(), crd, metav1.UpdateOptions{})
			if err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	})
}

func keepV1beta1CRDActivated(fakeClient *fakeextclient.Clientset, crdName string) error {
	return wait.PollUntilContextTimeout(context.TODO(), 1*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
		crd, err := fakeClient.ApiextensionsV1beta1().
			CustomResourceDefinitions().
			Get(context.TODO(), crdName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(crd.Status.Conditions) == 0 {
			crd.Status.Conditions = []apiextensionsv1beta1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1beta1.Established,
				Status: apiextensionsv1beta1.ConditionTrue,
			}}
			_, err = fakeClient.ApiextensionsV1beta1().CustomResourceDefinitions().UpdateStatus(context.TODO(), crd, metav1.UpdateOptions{})
			if err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	})
}
