package portworxdiag

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	portworxv1 "github.com/libopenstorage/operator/pkg/apis/portworx/v1"
	"github.com/libopenstorage/operator/pkg/mock"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
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
	group := portworxv1.SchemeGroupVersion.Group
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
	require.Equal(t, portworxv1.SchemeGroupVersion.Group, pdCRD.Spec.Group)
	require.Len(t, pdCRD.Spec.Versions, 1)
	require.Equal(t, portworxv1.SchemeGroupVersion.Version, pdCRD.Spec.Versions[0].Name)
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
	require.Equal(t, reflect.TypeOf(portworxv1.PortworxDiag{}).Name(), pdCRD.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(portworxv1.PortworxDiagList{}).Name(), pdCRD.Spec.Names.ListKind)
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
