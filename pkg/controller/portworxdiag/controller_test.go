package portworxdiag

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	diagv1 "github.com/libopenstorage/operator/pkg/apis/portworx/v1"
	"github.com/libopenstorage/operator/pkg/mock"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/scheme"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
)

func TestInit(t *testing.T) {
	mockCtrl := gomock.NewController(t)

	fakeClient := fakek8sclient.NewSimpleClientset()
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakeClient))
	recorder := record.NewFakeRecorder(10)

	mgr := mock.NewMockManager(mockCtrl)
	mgr.EXPECT().GetClient().Return(k8sClient).AnyTimes()
	mgr.EXPECT().GetScheme().Return(scheme.Scheme).AnyTimes()
	mgr.EXPECT().GetEventRecorderFor(gomock.Any()).Return(recorder).AnyTimes()
	mgr.EXPECT().SetFields(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().GetLogger().Return(log.Log.WithName("test")).AnyTimes()
	mgr.EXPECT().GetConfig().Return(&rest.Config{
		Host:    "127.0.0.1",
		APIPath: "fake",
	}).AnyTimes()

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
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "v1.23.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	group := diagv1.SchemeGroupVersion.Group
	portworxDiagCRDName := "portworxdiags" + "." + group

	// When the CRDs are created, just updated their status so the validation
	// does not get stuck until timeout.
	go func() {
		err := testutil.ActivateCRDWhenCreated(fakeExtClient, portworxDiagCRDName)
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

	pdCRD, err := fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Get(context.TODO(), portworxDiagCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, portworxDiagCRDName, pdCRD.Name)
	require.Equal(t, diagv1.SchemeGroupVersion.Group, pdCRD.Spec.Group)
	require.Len(t, pdCRD.Spec.Versions, 1)
	require.Equal(t, diagv1.SchemeGroupVersion.Version, pdCRD.Spec.Versions[0].Name)
	require.True(t, pdCRD.Spec.Versions[0].Served)
	require.True(t, pdCRD.Spec.Versions[0].Storage)
	subresource := &apiextensionsv1.CustomResourceSubresources{
		Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
	}
	require.Equal(t, subresource, pdCRD.Spec.Versions[0].Subresources)
	require.NotEmpty(t, pdCRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties)
	require.Equal(t, apiextensionsv1.NamespaceScoped, pdCRD.Spec.Scope)
	require.Equal(t, "portworxdiag", pdCRD.Spec.Names.Singular)
	require.Equal(t, "portworxdiags", pdCRD.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(diagv1.PortworxDiag{}).Name(), pdCRD.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(diagv1.PortworxDiagList{}).Name(), pdCRD.Spec.Names.ListKind)
	require.Equal(t, []string{"pxdiag"}, pdCRD.Spec.Names.ShortNames)

	// If CRDs are already present, then should update it
	pdCRD.ResourceVersion = "1000"
	_, err = fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Update(context.TODO(), pdCRD, metav1.UpdateOptions{})
	require.NoError(t, err)

	// The fake client overwrites the status in Update call which real client
	// does not. This will keep the CRD activated so validation does not get stuck.
	go func() {
		err := keepCRDActivated(fakeExtClient, portworxDiagCRDName)
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
	require.Equal(t, portworxDiagCRDName, crds.Items[0].Name)

	pdCRD, err = fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Get(context.TODO(), portworxDiagCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "1000", pdCRD.ResourceVersion)
}

func TestReconcileOfDeletedDiag(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	recorder := record.NewFakeRecorder(1)
	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "does-not-exist",
			Namespace: "test-ns",
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, recorder.Events, 0)
}

func keepCRDActivated(fakeClient *fakeextclient.Clientset, crdName string) error {
	return wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
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
			_, err = fakeClient.ApiextensionsV1().
				CustomResourceDefinitions().
				UpdateStatus(context.TODO(), crd, metav1.UpdateOptions{})
			if err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	})
}

func TestShouldPodBeOnNode(t *testing.T) {
	// Diag is nil: should return false
	should := shouldPodBeOnNode("some-uuid", "node1", []v1.Node{}, nil)
	require.False(t, should)

	// Diag.Spec.Portworx is nil: should return false
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: nil,
		},
	})
	require.False(t, should)

	// NodeSelector.All is true: should return true
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					All: true,
				},
			},
		},
	})
	require.True(t, should)

	// NodeSelector.All is false, NodeSelector.IDs is nil and NodeSelector.Labels is nil: should return true
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					IDs:    nil,
					Labels: nil,
				},
			},
		},
	})
	require.False(t, should)

	// NodeSelector.All is false, NodeSelector.IDs is empty and NodeSelector.Labels is empty: should return false
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					IDs:    []string{},
					Labels: map[string]string{},
				},
			},
		},
	})
	require.False(t, should)

	// NodeSelector.IDs contains the given node ID: should return true
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					IDs: []string{"some-uuid"},
				},
			},
		},
	})
	require.True(t, should)

	// NodeSelector.IDs contains some node IDs but not the given node ID: should return false
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					IDs: []string{"another-uuid"},
				},
			},
		},
	})
	require.False(t, should)

	// NodeSelector.Labels is populated, no matching node: should return false
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
			},
		},
	}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					Labels: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	})
	require.False(t, should)

	// NodeSelector.Labels is populated, label missing: should return false
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{
					"baz": "nah",
				},
			},
		},
	}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					Labels: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	})
	require.False(t, should)

	// NodeSelector.Labels is populated, label present but wrong value: should return false
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{
					"foo": "nah",
				},
			},
		},
	}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					Labels: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	})
	require.False(t, should)

	// NodeSelector.Labels is populated, label present and correct value: should return true
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{
					"foo": "bar",
				},
			},
		},
	}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					Labels: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	})
	require.True(t, should)

	// NodeSelector.Labels is populated, one label present, one not: should return false
	should = shouldPodBeOnNode("some-uuid", "node1", []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{
					"foo": "bar",
					"baz": "nah",
				},
			},
		},
	}, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					Labels: map[string]string{
						"foo": "bar",
						"baz": "bar",
					},
				},
			},
		},
	})
	require.False(t, should)

	// NodeSelector.IDs and Labels is populated, disjoint set: should return true for both nodes
	nodes := []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
				Labels: map[string]string{
					"foo": "bar",
				},
			},
		},
	}
	// Both node1 and node2 should be valid
	// node1 because it has the correct ID
	// node2 because it has the correct label
	should = shouldPodBeOnNode("some-uuid", "node1", nodes, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					IDs: []string{"some-uuid"},
					Labels: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	})
	require.True(t, should)
	should = shouldPodBeOnNode("some-uuid", "node2", nodes, &diagv1.PortworxDiag{
		Spec: diagv1.PortworxDiagSpec{
			Portworx: &diagv1.PortworxComponent{
				NodeSelector: diagv1.NodeSelector{
					IDs: []string{"some-uuid"},
					Labels: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	})
	require.True(t, should)
}

func TestGetNodeToPodMap(t *testing.T) {
	res := getNodeToPodMap(nil)
	require.Empty(t, res)

	res = getNodeToPodMap(&v1.PodList{})
	require.Empty(t, res)

	pod1 := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod1",
		},
		Spec: v1.PodSpec{
			NodeName: "node1",
		},
	}
	pod2 := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod2",
		},
		Spec: v1.PodSpec{
			NodeName: "node2",
		},
	}

	res = getNodeToPodMap(&v1.PodList{
		Items: []v1.Pod{pod1, pod2},
	})
	require.Len(t, res, 2)
	require.Contains(t, res, "node1")
	require.Contains(t, res, "node2")
	require.Equal(t, *res["node1"], pod1)
	require.Equal(t, *res["node2"], pod2)
}

func TestGetNodeIDToStatusMap(t *testing.T) {
	// Test cases:
	// * a nil node list should return an empty map
	// * an empty node list should return an empty map
	// * a node list with two nodes should return a map with two entries
	// * a node list with different statuses should return a map with two entries with the correct statuses
	// * a node list with a node with an empty node ID should not include the node in the map

	res := getNodeIDToStatusMap(nil)
	require.Empty(t, res)

	res = getNodeIDToStatusMap([]diagv1.NodeStatus{})
	require.Empty(t, res)

	res = getNodeIDToStatusMap([]diagv1.NodeStatus{
		{
			NodeID: "node1",
			Status: diagv1.NodeStatusInProgress,
		},
	})
	require.Len(t, res, 1)
	require.Contains(t, res, "node1")
	require.Equal(t, res["node1"], diagv1.NodeStatusInProgress)

	res = getNodeIDToStatusMap([]diagv1.NodeStatus{
		{
			NodeID: "node1",
			Status: diagv1.NodeStatusInProgress,
		},
		{
			NodeID: "node2",
			Status: diagv1.NodeStatusCompleted,
		},
	})
	require.Len(t, res, 2)
	require.Contains(t, res, "node1")
	require.Contains(t, res, "node2")
	require.Equal(t, res["node1"], diagv1.NodeStatusInProgress)
	require.Equal(t, res["node2"], diagv1.NodeStatusCompleted)

	res = getNodeIDToStatusMap([]diagv1.NodeStatus{
		{
			NodeID: "",
			Status: diagv1.NodeStatusInProgress,
		},
	})
	require.Empty(t, res)
}

func TestGetPodsDiff(t *testing.T) {
	// Node 0 will be missing a status entirely (should add a status and create a pod)
	// Node 1 will have a status but be missing a pod (should create a pod)
	// Node 2 will be completed successfully (should delete a pod)
	// Node 3 will be completed and failed (should delete a pod)
	// Node 4 will be in progress (should not change anything)
	// Node 5 will not match the selector (should delete the existing pod)
	// Node 6 will not match the selector (should not create a new pod)

	// Test object setup
	n := 7
	pods := make([]v1.Pod, n)
	nodes := make([]v1.Node, n)
	nodeIDToName := make(map[string]string)
	for i := 0; i < n; i++ {
		pods[i] = v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod%d", i)},
			Spec:       v1.PodSpec{NodeName: fmt.Sprintf("node%d", i)},
		}
		nodes[i] = v1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node%d", i)}}
		nodeIDToName[fmt.Sprintf("id%d", i)] = fmt.Sprintf("node%d", i)
	}

	prs, err := getPodsDiff(
		&v1.PodList{Items: pods[2:6]}, // Only pass in a subset of the nodes to simulate some not existing
		&v1.NodeList{Items: nodes},
		&diagv1.PortworxDiag{
			Spec: diagv1.PortworxDiagSpec{
				Portworx: &diagv1.PortworxComponent{
					NodeSelector: diagv1.NodeSelector{
						IDs: []string{"id0", "id1", "id2", "id3", "id4"},
					},
				},
			},
			Status: diagv1.PortworxDiagStatus{
				NodeStatuses: []diagv1.NodeStatus{
					{NodeID: "id1", Status: diagv1.NodeStatusPending},
					{NodeID: "id2", Status: diagv1.NodeStatusCompleted},
					{NodeID: "id3", Status: diagv1.NodeStatusFailed},
					{NodeID: "id4", Status: diagv1.NodeStatusInProgress},
					{NodeID: "id5", Status: diagv1.NodeStatusInProgress},
					{NodeID: "id6", Status: diagv1.NodeStatusInProgress},
				},
			},
		}, nodeIDToName)
	require.ElementsMatch(t, prs.nodeStatusesToAdd, []*diagv1.NodeStatus{
		{
			NodeID: "id0",
			Status: diagv1.NodeStatusPending,
		},
	})
	require.ElementsMatch(t, prs.nodesToCreatePodsFor, []string{"node0", "node1"})
	require.ElementsMatch(t, prs.podsToDelete, []*v1.Pod{&pods[2], &pods[3], &pods[5]})
	require.NoError(t, err)
}

func TestGetOverallPhase(t *testing.T) {
	phase, msg := getOverallPhase([]diagv1.NodeStatus{})
	require.Equal(t, diagv1.DiagStatusPending, phase)
	require.Empty(t, msg)

	// If all phases are empty or pending, the overall phase should be pending
	phase, msg = getOverallPhase([]diagv1.NodeStatus{
		{Status: ""},
		{Status: ""},
		{Status: diagv1.NodeStatusPending},
	})
	require.Equal(t, diagv1.DiagStatusPending, phase)
	require.Empty(t, msg)

	// If all phases are completed, the overall phase should be completed
	phase, msg = getOverallPhase([]diagv1.NodeStatus{
		{Status: diagv1.NodeStatusCompleted},
		{Status: diagv1.NodeStatusCompleted},
		{Status: diagv1.NodeStatusCompleted},
	})
	require.Equal(t, diagv1.DiagStatusCompleted, phase)
	require.Equal(t, "All diags collected successfully", msg)

	// If all phases are failed, the overall phase should be failed
	phase, msg = getOverallPhase([]diagv1.NodeStatus{
		{Status: diagv1.NodeStatusFailed},
		{Status: diagv1.NodeStatusFailed},
		{Status: diagv1.NodeStatusFailed},
	})
	require.Equal(t, diagv1.DiagStatusFailed, phase)
	require.Equal(t, "All diags failed to collect", msg)

	// If some phases are pending and some are completed, the overall phase should be partial failure
	phase, msg = getOverallPhase([]diagv1.NodeStatus{
		{Status: diagv1.NodeStatusPending},
		{Status: diagv1.NodeStatusCompleted},
		{Status: diagv1.NodeStatusCompleted},
	})
	require.Equal(t, diagv1.DiagStatusPartialFailure, phase)
	require.Equal(t, "Some diags failed to collect", msg)

	// If some phases are failed and some are completed, the overall phase should be partial failure
	phase, msg = getOverallPhase([]diagv1.NodeStatus{
		{Status: diagv1.NodeStatusFailed},
		{Status: diagv1.NodeStatusCompleted},
		{Status: diagv1.NodeStatusCompleted},
	})
	require.Equal(t, diagv1.DiagStatusPartialFailure, phase)
	require.Equal(t, "Some diags failed to collect", msg)

	// If some phases are in progress, no matter what the phase should be in progress
	phase, msg = getOverallPhase([]diagv1.NodeStatus{
		{Status: diagv1.NodeStatusInProgress},
		{Status: diagv1.NodeStatusCompleted},
		{Status: diagv1.NodeStatusFailed},
		{Status: diagv1.NodeStatusPending},
	})
	require.Equal(t, diagv1.DiagStatusInProgress, phase)
	require.Equal(t, "Diag collection is in progress", msg)

	// If an unknown status slips in, the overall phase should be unknown unless another node is in progress
	phase, msg = getOverallPhase([]diagv1.NodeStatus{
		{Status: diagv1.NodeStatusPending},
		{Status: "InvalidStatus"},
	})
	require.Equal(t, diagv1.DiagStatusUnknown, phase)
	require.Empty(t, msg)
}

func TestGetDiagObject(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(&diagv1.PortworxDiag{})
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	// First find with no objects created
	diag, otherRunning, err := controller.getDiagObject(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test",
			Namespace: "portworx",
		},
	})
	require.Nil(t, diag)
	require.False(t, otherRunning)
	require.NoError(t, err)

	// Create another diag object
	diagInProgress := &diagv1.PortworxDiag{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "otherdiag",
			Namespace: "portworx",
		},
		Status: diagv1.PortworxDiagStatus{
			Phase: diagv1.DiagStatusInProgress,
		},
	}
	err = k8sClient.Create(context.Background(), diagInProgress)
	require.NoError(t, err)

	diag, otherRunning, err = controller.getDiagObject(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test",
			Namespace: "portworx",
		},
	})
	require.Nil(t, diag)
	require.True(t, otherRunning)
	require.NoError(t, err)

	// Create our diag object
	diagOurs := &diagv1.PortworxDiag{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "portworx",
		},
		Status: diagv1.PortworxDiagStatus{
			Phase: diagv1.DiagStatusInProgress,
		},
	}
	err = k8sClient.Create(context.Background(), diagOurs)
	require.NoError(t, err)

	diag, otherRunning, err = controller.getDiagObject(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test",
			Namespace: "portworx",
		},
	})
	require.Equal(t, diagOurs, diag)
	require.True(t, otherRunning)
	require.NoError(t, err)

	// Delete the other diag object, now we should be able to run
	err = k8sClient.Delete(context.Background(), diagInProgress)
	require.NoError(t, err)

	diag, otherRunning, err = controller.getDiagObject(context.TODO(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test",
			Namespace: "portworx",
		},
	})
	require.Equal(t, diagOurs, diag)
	require.False(t, otherRunning)
	require.NoError(t, err)
}
