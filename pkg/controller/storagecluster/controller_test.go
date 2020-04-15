package storagecluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/fake"
	"github.com/libopenstorage/operator/pkg/util"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	kversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestRegisterCRD(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	fakeExtClient := fakeextclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
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
	require.Len(t, crds.Items, 2)

	crd, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(storageClusterCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, storageClusterCRDName, crd.Name)
	require.Equal(t, corev1alpha1.SchemeGroupVersion.Group, crd.Spec.Group)
	require.Len(t, crd.Spec.Versions, 1)
	require.Equal(t, corev1alpha1.SchemeGroupVersion.Version, crd.Spec.Versions[0].Name)
	require.True(t, crd.Spec.Versions[0].Served)
	require.True(t, crd.Spec.Versions[0].Storage)
	require.Equal(t, apiextensionsv1beta1.NamespaceScoped, crd.Spec.Scope)
	require.Equal(t, corev1alpha1.StorageClusterResourceName, crd.Spec.Names.Singular)
	require.Equal(t, corev1alpha1.StorageClusterResourcePlural, crd.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(corev1alpha1.StorageCluster{}).Name(), crd.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(corev1alpha1.StorageClusterList{}).Name(), crd.Spec.Names.ListKind)
	require.Equal(t, []string{corev1alpha1.StorageClusterShortName}, crd.Spec.Names.ShortNames)
	subresource := &apiextensionsv1beta1.CustomResourceSubresources{
		Status: &apiextensionsv1beta1.CustomResourceSubresourceStatus{},
	}
	require.Equal(t, subresource, crd.Spec.Subresources)
	require.NotEmpty(t, crd.Spec.Validation.OpenAPIV3Schema.Properties)

	crd, err = fakeExtClient.ApiextensionsV1beta1().
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
	require.Len(t, crds.Items, 2)
	require.Equal(t, storageClusterCRDName, crds.Items[0].Name)
	require.Equal(t, storageNodeCRDName, crds.Items[1].Name)
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
	require.Len(t, crds.Items, 2)
	for _, crd := range crds.Items {
		require.NotEqual(t, nodeStatusCRDName, crd.Name)
	}
}

func TestKubernetesVersionValidation(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "v1.11.99",
	}
	coreops.SetInstance(coreops.New(fakeClient))

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
	}

	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
	}
	result, err := controller.Reconcile(request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), "minimum supported kubernetes version")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v minimum supported kubernetes version",
			v1.EventTypeWarning, util.FailedValidationReason))

	// Invalid kubernetes version
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "invalid",
	}
	controller.kubernetesVersion = nil

	result, err = controller.Reconcile(request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), "invalid kubernetes version received")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v invalid kubernetes version received",
			v1.EventTypeWarning, util.FailedValidationReason))
}

func TestSingleClusterValidation(t *testing.T) {
	existingCluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "main-cluster",
			Namespace:  "main-ns",
			Finalizers: []string{deleteFinalizerName},
		},
	}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "extra-cluster",
			Namespace: "extra-ns",
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	k8sClient := testutil.FakeK8sClient(existingCluster, cluster)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), fmt.Sprintf("only one StorageCluster is allowed in a Kubernetes cluster. "+
		"StorageCluster %s/%s already exists", existingCluster.Namespace, existingCluster.Name))

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v only one StorageCluster is allowed in a Kubernetes cluster. "+
			"StorageCluster %s/%s already exists", v1.EventTypeWarning, util.FailedValidationReason,
			existingCluster.Namespace, existingCluster.Name))
}

func TestStorageClusterDefaults(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)

	controller := Controller{
		client: k8sClient,
		Driver: driver,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()

	// Use default revision history limit if not set
	err := controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, int32(defaultRevisionHistoryLimit), *cluster.Spec.RevisionHistoryLimit)

	// Don't use default revision history limit if already set
	revisionHistoryLimit := int32(20)
	cluster.Spec.RevisionHistoryLimit = &revisionHistoryLimit
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, revisionHistoryLimit, *cluster.Spec.RevisionHistoryLimit)

	// Use default image pull policy if not set
	cluster.Spec.ImagePullPolicy = ""
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, v1.PullAlways, cluster.Spec.ImagePullPolicy)

	// Don't use default image pull policy if already set
	cluster.Spec.ImagePullPolicy = v1.PullNever
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, v1.PullNever, cluster.Spec.ImagePullPolicy)
}

func TestStorageClusterDefaultsForUpdateStrategy(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)

	controller := Controller{
		client: k8sClient,
		Driver: driver,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()

	// Use rolling update as default update strategy if nothing specified
	err := controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, 1, cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntValue())

	// Use default max unavailable if rolling update is set but empty
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type: corev1alpha1.RollingUpdateStorageClusterStrategyType,
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, 1, cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntValue())

	// Use default max unavailable if rolling update is set but max unavailable not set
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type:          corev1alpha1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1alpha1.RollingUpdateStorageCluster{},
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, 1, cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntValue())

	// Don't use default max unavailable if rolling update and max unavailable is set
	maxUnavailable := intstr.FromString("20%")
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type: corev1alpha1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1alpha1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, "20%", cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.String())

	// Don't overwrite update strategy is specified
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type: corev1alpha1.OnDeleteStorageClusterStrategyType,
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.OnDeleteStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Nil(t, cluster.Spec.UpdateStrategy.RollingUpdate)
}

func TestStorageClusterDefaultsForFinalizer(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)

	controller := Controller{
		client: k8sClient,
		Driver: driver,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()

	// Add delete finalizer if no finalizers are present
	expectedFinalizers := []string{deleteFinalizerName}
	err := controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, expectedFinalizers, cluster.Finalizers)

	// Add delete finalizer if it is not present
	cluster.Finalizers = []string{"foo", "bar"}
	expectedFinalizers = []string{"foo", "bar", deleteFinalizerName}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, expectedFinalizers, cluster.Finalizers)

	// Do not add delete finalizer if already present
	expectedFinalizers = []string{"foo", deleteFinalizerName, "bar"}
	cluster.Finalizers = []string{"foo", deleteFinalizerName, "bar"}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, expectedFinalizers, cluster.Finalizers)
}

func TestStorageClusterDefaultsWithDriverOverrides(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)

	controller := Controller{
		client: k8sClient,
		Driver: driver,
	}

	driver.EXPECT().
		SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(cluster *corev1alpha1.StorageCluster) {
			cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
				Type: corev1alpha1.OnDeleteStorageClusterStrategyType,
			}
			cluster.Spec.Stork = &corev1alpha1.StorkSpec{
				Enabled: false,
			}
			revisionHistoryLimit := int32(5)
			cluster.Spec.RevisionHistoryLimit = &revisionHistoryLimit
			cluster.Spec.ImagePullPolicy = v1.PullIfNotPresent
			cluster.Spec.Image = "test/image:1.2.3"
		})

	// The default values from from the storage driver should take precendence
	err := controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)

	require.Equal(t, "test/image:1.2.3", cluster.Spec.Image)
	require.Equal(t, int32(5), *cluster.Spec.RevisionHistoryLimit)
	require.Equal(t, v1.PullIfNotPresent, cluster.Spec.ImagePullPolicy)
	require.False(t, cluster.Spec.Stork.Enabled)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, corev1alpha1.OnDeleteStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Empty(t, cluster.Spec.UpdateStrategy.RollingUpdate)
}

func TestReconcileForNonExistingCluster(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:   testutil.FakeK8sClient(),
		recorder: recorder,
	}

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existing-cluster",
			Namespace: "test-ns",
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)

	require.Empty(t, result)
	require.Empty(t, recorder.Events)
}

func TestFailureDuringStorkInstallation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Stork.Enabled = true
	cluster.Annotations = map[string]string{
		annotationStorkCPU: "invalid-cpu",
	}
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorkDriverName().Return("mock", nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	// Reconcile should not fail on stork install failure. Only event should be raised.
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedComponentReason))
}

func TestFailureDuringDriverPreInstall(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().PreInstall(gomock.Any()).Return(fmt.Errorf("preinstall error"))
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Contains(t, err.Error(), "preinstall error")
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	actualEvent := <-recorder.Events
	require.Contains(t, actualEvent,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
	require.Contains(t, actualEvent, "preinstall error")
}

func TestStoragePodsShouldNotBeScheduledIfDisabled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Annotations = map[string]string{
		AnnotationDisableStorage: "true",
	}

	// Kubernetes node with resources to create a pod
	k8sNode := createK8sNode("k8s-node-1", 10)

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify there is no event raised
	require.Empty(t, recorder.Events)

	// Verify there is one revision for the new StorageCluster object
	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)

	// Verify no storage pods are created
	require.Empty(t, podControl.Templates)
}

func TestStoragePodGetsScheduled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode2 := createK8sNode("k8s-node-2", 1)

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	expectedPodSpec := v1.PodSpec{
		Containers: []v1.Container{{Name: "test"}},
	}
	addOrUpdateStoragePodTolerations(&expectedPodSpec)
	expectedPodTemplate := &v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Labels: map[string]string{
				labelKeyName:       cluster.Name,
				labelKeyDriverName: driverName,
			},
		},
		Spec: expectedPodSpec,
	}

	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(c *corev1alpha1.StorageCluster) {
			hash := computeHash(&c.Spec, nil)
			expectedPodTemplate.Labels[defaultStorageClusterUniqueLabelKey] = hash
		})
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).
		Return(expectedPodSpec, nil).
		AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify there is no event raised
	require.Empty(t, recorder.Events)

	// Verify there is one revision for the new StorageCluster object
	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)

	// Verify a pod is created for the given node with correct owner ref
	require.Len(t, podControl.Templates, 2)
	require.Equal(t, expectedPodTemplate, &podControl.Templates[0])
	require.Equal(t, expectedPodTemplate, &podControl.Templates[1])
	require.Len(t, podControl.ControllerRefs, 2)
	require.Equal(t, *clusterRef, podControl.ControllerRefs[0])
	require.Equal(t, *clusterRef, podControl.ControllerRefs[1])
}

func TestStorageNodeGetsCreated(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode2 := createK8sNode("k8s-node-2", 1)

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	storageLabels := map[string]string{"foo": "bar"}

	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(storageLabels).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	expectedStorageNode1 := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            k8sNode1.Name,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
			Labels:          storageLabels,
		},
		Status: corev1alpha1.NodeStatus{
			Phase: string(corev1alpha1.NodeInitStatus),
		},
	}
	expectedStorageNode2 := expectedStorageNode1.DeepCopy()
	expectedStorageNode2.Name = k8sNode2.Name
	expectedStorageNodes := []corev1alpha1.StorageNode{*expectedStorageNode1, *expectedStorageNode2}

	storageNodes := &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.ElementsMatch(t,
		expectedStorageNodes,
		storageNodes.Items,
	)

	// TestCase: Recreating the pods should not affect the created storage nodes
	pods := &v1.PodList{}
	testutil.List(k8sClient, pods)
	require.Empty(t, pods.Items)

	storageNodes.Items[0].Status.Phase = string(corev1alpha1.NodeOnlineStatus)
	k8sClient.Update(context.TODO(), &storageNodes.Items[0])

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	storageNodes = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 2)
	require.Equal(t, string(corev1alpha1.NodeOnlineStatus), storageNodes.Items[0].Status.Phase)
	require.Equal(t, string(corev1alpha1.NodeInitStatus), storageNodes.Items[1].Status.Phase)

	// TestCase: Should recreate the storage nodes when re-creating pods
	pods = &v1.PodList{}
	testutil.List(k8sClient, pods)
	require.Empty(t, pods.Items)

	k8sClient.Delete(context.TODO(), &storageNodes.Items[0])
	k8sClient.Delete(context.TODO(), &storageNodes.Items[1])

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	storageNodes = &corev1alpha1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 2)
}

func TestStoragePodGetsScheduledWithCustomNodeSpecs(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	useAllDevices := true
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll: &useAllDevices,
	}
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: stringPtr("cluster_data_intf"),
		MgmtInterface: stringPtr("cluster_mgmt_intf"),
	}
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  "ENV_CLUSTER",
			Value: "cluster_value",
		},
		{
			Name:  "ENV_OVERRIDE",
			Value: "override_cluster_value",
		},
	}
	cluster.Spec.RuntimeOpts = map[string]string{
		"cluster_rt_one": "rt_val_1",
	}
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{
		{
			// Match using node name
			Selector: corev1alpha1.NodeSelector{
				NodeName: "k8s-node-1",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"dev1"}),
				},
				Network: &corev1alpha1.NetworkSpec{
					DataInterface: stringPtr("dface"),
					MgmtInterface: stringPtr("mface"),
				},
				Env: []v1.EnvVar{
					{
						Name:  "ENV_NODE",
						Value: "node_value",
					},
					{
						Name:  "ENV_OVERRIDE",
						Value: "override_node_value",
					},
				},
				RuntimeOpts: map[string]string{
					"rt_one": "rt_val_1",
					"rt_two": "rt_val_2",
				},
			},
		},
		{
			// Match using a label selector
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node2",
					},
				},
			},
		},
		{
			// Duplicate selector with same node name. If the node has already
			// matched a previous spec, then this spec will not be used by that node.
			Selector: corev1alpha1.NodeSelector{
				NodeName: "k8s-node-1",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Even though the labels match a valid node, if the node has already
			// matched a previous spec, then this spec will not be used by that node.
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test2": "node2",
					},
				},
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Even though the node name matches a valid node, if the node has already
			// matched a previous spec, then this spec will not be used by that node.
			Selector: corev1alpha1.NodeSelector{
				NodeName: "k8s-node-2",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Even though the labels match a valid node, if the node has already
			// matched a previous spec, then this spec will not be used by that node.
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node1",
					},
				},
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Selector with node name that does not exist. No pod should
			// be deployed with this configuration
			Selector: corev1alpha1.NodeSelector{
				NodeName: "non-existent-node",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Selector with requirements that do not match any node. No pod
			// should be deployed with this configuration
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "not-matching-label",
					},
				},
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Selector with invalid requirements. No pod should be
			// deployed with this configuration
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "test",
							Operator: "InvalidOperator",
						},
					},
				},
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode1.Labels = map[string]string{
		"test": "node1",
	}
	k8sNode2 := createK8sNode("k8s-node-2", 1)
	k8sNode2.Labels = map[string]string{
		"test":  "node2",
		"test2": "node2",
	}
	k8sNode3 := createK8sNode("k8s-node-3", 1)

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2, k8sNode3)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	expectedPodSpec := v1.PodSpec{
		Containers: []v1.Container{{Name: "test"}},
	}
	addOrUpdateStoragePodTolerations(&expectedPodSpec)
	expectedPodTemplate := &v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Labels: map[string]string{
				labelKeyName:       cluster.Name,
				labelKeyDriverName: driverName,
			},
		},
		Spec: expectedPodSpec,
	}
	expectedPodTemplates := []v1.PodTemplateSpec{
		*expectedPodTemplate,
		*expectedPodTemplate.DeepCopy(),
		*expectedPodTemplate.DeepCopy(),
	}

	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(c *corev1alpha1.StorageCluster) {
			hash := computeHash(&c.Spec, nil)
			expectedPodTemplates[0].Labels[defaultStorageClusterUniqueLabelKey] = hash
			expectedPodTemplates[1].Labels[defaultStorageClusterUniqueLabelKey] = hash
			expectedPodTemplates[2].Labels[defaultStorageClusterUniqueLabelKey] = hash
		})
	gomock.InOrder(
		driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-1").
			DoAndReturn(func(c *corev1alpha1.StorageCluster, _ string) (v1.PodSpec, error) {
				require.Equal(t, cluster.Spec.Nodes[0].Storage, c.Spec.Storage)
				require.Equal(t, cluster.Spec.Nodes[0].Network, c.Spec.Network)
				require.Equal(t, cluster.Spec.Nodes[0].RuntimeOpts, c.Spec.RuntimeOpts)
				expectedEnv := []v1.EnvVar{
					{
						Name:  "ENV_CLUSTER",
						Value: "cluster_value",
					},
					{
						Name:  "ENV_OVERRIDE",
						Value: "override_node_value",
					},
					{
						Name:  "ENV_NODE",
						Value: "node_value",
					},
				}
				require.ElementsMatch(t, expectedEnv, c.Spec.Env)
				nodeLabels, _ := json.Marshal(k8sNode1.Labels)
				expectedPodTemplates[0].Annotations = map[string]string{annotationNodeLabels: string(nodeLabels)}
				return expectedPodSpec, nil
			}).
			Times(1),
		driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-2").
			DoAndReturn(func(c *corev1alpha1.StorageCluster, _ string) (v1.PodSpec, error) {
				require.Empty(t, cluster.Spec.Nodes[1].CommonConfig, c.Spec.CommonConfig)
				nodeLabels, _ := json.Marshal(k8sNode2.Labels)
				expectedPodTemplates[1].Annotations = map[string]string{annotationNodeLabels: string(nodeLabels)}
				return expectedPodSpec, nil
			}).
			Times(1),
		driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-3").
			DoAndReturn(func(c *corev1alpha1.StorageCluster, _ string) (v1.PodSpec, error) {
				require.Equal(t, cluster.Spec.CommonConfig, c.Spec.CommonConfig)
				return expectedPodSpec, nil
			}).
			Times(1),
	)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify there is no event raised
	require.Empty(t, recorder.Events)

	// Verify there is one revision for the new StorageCluster object
	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)

	// Verify a pod is created for the given node with correct owner ref
	require.Len(t, podControl.Templates, 3)
	require.ElementsMatch(t, expectedPodTemplates, podControl.Templates)
	require.Len(t, podControl.ControllerRefs, 3)
	require.Equal(t, *clusterRef, podControl.ControllerRefs[0])
	require.Equal(t, *clusterRef, podControl.ControllerRefs[1])
	require.Equal(t, *clusterRef, podControl.ControllerRefs[2])
}

func TestFailedStoragePodsGetRemoved(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes nodes with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash

	// Running pod should not get deleted if nothing has changed
	runningPod := createStoragePod(cluster, "running-pod", k8sNode1.Name, storageLabels)

	// Failed pod should be deleted on reconcile
	failedPod := createStoragePod(cluster, "failed-pod", k8sNode2.Name, storageLabels)
	failedPod.Status = v1.PodStatus{
		Phase: v1.PodFailed,
	}

	// Deleted pod should not be deleted again
	deletedPod := createStoragePod(cluster, "deleted-pod", k8sNode3.Name, storageLabels)
	deletionTimestamp := metav1.Now()
	deletedPod.DeletionTimestamp = &deletionTimestamp

	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), k8sNode3)
	k8sClient.Create(context.TODO(), runningPod)
	k8sClient.Create(context.TODO(), failedPod)
	k8sClient.Create(context.TODO(), deletedPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify there is event raised for the failed pod
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedStoragePodReason))

	// Verify no pod is created on first node, failed pod is deleted,
	// and already deleted pod is not deleted again
	require.Empty(t, podControl.Templates)
	require.Len(t, podControl.DeletePodName, 1)
	require.Equal(t, failedPod.Name, podControl.DeletePodName[0])
}

func TestExtraStoragePodsGetRemoved(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	affinity := &v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchFields: []v1.NodeSelectorRequirement{
							{
								Key:      schedulerapi.NodeFieldSelectorKeyNodeName,
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{k8sNode.Name},
							},
						},
					},
				},
			},
		},
	}

	// If multiple pods are running on a single node, only one with earliest
	// timestamp and which is scheduled should be retained. If spec.NodeName
	// is set, it is assumed that the pod is scheduled.
	unscheduledPod1 := createStoragePod(cluster, "unscheduled-pod-1", "", storageLabels)
	unscheduledPod1.CreationTimestamp = metav1.Now()
	unscheduledPod1.Spec.Affinity = affinity

	creationTimestamp := metav1.NewTime(time.Now().Add(1 * time.Minute))
	runningPod1 := createStoragePod(cluster, "running-pod-1", k8sNode.Name, storageLabels)
	runningPod1.CreationTimestamp = creationTimestamp
	runningPod2 := createStoragePod(cluster, "running-pod-2", k8sNode.Name, storageLabels)
	runningPod2.CreationTimestamp = creationTimestamp

	unscheduledPod2 := createStoragePod(cluster, "unscheduled-pod-2", "", storageLabels)
	unscheduledPod2.CreationTimestamp = metav1.NewTime(time.Now().Add(2 * time.Minute))
	unscheduledPod2.Spec.Affinity = affinity

	extraRunningPod := createStoragePod(cluster, "extra-running-pod", k8sNode.Name, storageLabels)
	extraRunningPod.CreationTimestamp = metav1.NewTime(time.Now().Add(3 * time.Minute))

	k8sClient.Create(context.TODO(), k8sNode)
	k8sClient.Create(context.TODO(), unscheduledPod1)
	k8sClient.Create(context.TODO(), runningPod1)
	k8sClient.Create(context.TODO(), runningPod2)
	k8sClient.Create(context.TODO(), unscheduledPod2)
	k8sClient.Create(context.TODO(), extraRunningPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify there is no event raised for the extra pods
	require.Empty(t, recorder.Events)

	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t,
		[]string{runningPod2.Name, unscheduledPod1.Name, unscheduledPod2.Name, extraRunningPod.Name},
		podControl.DeletePodName,
	)
}

func TestStoragePodsAreRemovedIfDisabled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Annotations = map[string]string{
		AnnotationDisableStorage: "1",
	}
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod := createStoragePod(cluster, "running-pod", k8sNode.Name, storageLabels)

	k8sClient.Create(context.TODO(), k8sNode)
	k8sClient.Create(context.TODO(), runningPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify there is no event raised for the extra pods
	require.Empty(t, recorder.Events)

	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{runningPod.Name}, podControl.DeletePodName)
}

func TestStoragePodFailureDueToInsufficientResources(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes nodes without enough resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 0)
	k8sNode2 := createK8sNode("k8s-node-2", 0)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod := createStoragePod(cluster, "running-pod", k8sNode2.Name, storageLabels)

	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), runningPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify event is raised if failed to place pod
	// Two events per pod are raised as we simulate every pod on the node twice
	require.Len(t, recorder.Events, 4)
	expectedEvent := fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedPlacementReason)
	require.Contains(t, <-recorder.Events, expectedEvent)
	require.Contains(t, <-recorder.Events, expectedEvent)
	require.Contains(t, <-recorder.Events, expectedEvent)
	require.Contains(t, <-recorder.Events, expectedEvent)

	// Verify there is one revision for the new StorageCluster object
	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)

	// Verify no pod is created due to insufficient resources.
	// Existing pods are not removed from nodes without resources.
	require.Empty(t, podControl.Templates)
	require.Empty(t, podControl.DeletePodName)
}

func TestStoragePodFailureDueToNodeSelectorNotMatch(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Placement = &corev1alpha1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchFields: []v1.NodeSelectorRequirement{
							{
								Key:      schedulerapi.NodeFieldSelectorKeyNodeName,
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"k8s-node-1", "k8s-node-2"},
							},
						},
					},
				},
			},
		},
	}
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode1.Labels = map[string]string{
		"test": "node1",
	}
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode2.Labels = map[string]string{
		"test": "node2",
	}
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod := createStoragePod(cluster, "running-pod", k8sNode3.Name, storageLabels)

	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), k8sNode3)
	k8sClient.Create(context.TODO(), runningPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// No need to raise events if node selectors don't match for a node
	// Verify no pod is created due to node selector mismatch. Also remove any
	// running pod if the selectors don't match.
	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{runningPod.Name}, podControl.DeletePodName)
}

func TestStoragePodSchedulingWithTolerations(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Placement = &corev1alpha1.PlacementSpec{
		Tolerations: []v1.Toleration{
			{
				Key:      "must-exist",
				Operator: v1.TolerationOpExists,
				Effect:   v1.TaintEffectNoExecute,
			},
			{
				Key:      "foo",
				Operator: v1.TolerationOpEqual,
				Value:    "bar",
				Effect:   v1.TaintEffectNoSchedule,
			},
		},
	}
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).Times(3)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).Times(3)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).Times(3)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).Times(3)

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode1.Spec.Taints = []v1.Taint{
		{
			Key:    "must-exist",
			Value:  "anything",
			Effect: v1.TaintEffectNoExecute,
		},
		{
			Key:    "foo",
			Value:  "bar",
			Effect: v1.TaintEffectNoSchedule,
		},
	}
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode2.Spec.Taints = []v1.Taint{
		{
			Key:    "foo",
			Value:  "bar",
			Effect: v1.TaintEffectNoSchedule,
		},
	}
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod1 := createStoragePod(cluster, "running-pod-1", k8sNode1.Name, storageLabels)
	runningPod2 := createStoragePod(cluster, "running-pod-2", k8sNode2.Name, storageLabels)
	runningPod3 := createStoragePod(cluster, "running-pod-3", k8sNode3.Name, storageLabels)

	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), k8sNode3)
	k8sClient.Create(context.TODO(), runningPod1)
	k8sClient.Create(context.TODO(), runningPod2)
	k8sClient.Create(context.TODO(), runningPod3)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// No pods should be deleted as they have tolerations for the node taints
	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.Empty(t, podControl.DeletePodName)

	// Case: Remove tolerations and the pods on nodes with NoExecute should be removed.
	// Pods on nodes with NoSchedule should NOT be removed
	cluster.Spec.Placement = nil
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{runningPod1.Name}, podControl.DeletePodName)

	// Case: Delete a pod lacking NoSchedule toleration and it should not be started again
	k8sClient.Delete(context.TODO(), runningPod2)
	k8sClient.Delete(context.TODO(), runningPod1)
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.Empty(t, podControl.DeletePodName)
}

func TestFailureDuringPodTemplateCreation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	useAllDevices := true
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll: &useAllDevices,
	}
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{
		{
			Selector: corev1alpha1.NodeSelector{
				NodeName: "k8s-node-1",
			},
		},
	}

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode2 := createK8sNode("k8s-node-2", 1)

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-1").
		Return(v1.PodSpec{}, fmt.Errorf("pod template error for k8s-node-1")).
		Times(1)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pod template error for k8s-node-1")
	require.Empty(t, result)

	// Verify there is event raised for failure to create pod templates for node spec
	require.Len(t, recorder.Events, 1)
	eventMsg := <-recorder.Events
	require.Contains(t, eventMsg, fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
	require.Contains(t, eventMsg, "pod template error for k8s-node-1")

	// When pod template creation passes for some and fails for others
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-1").
		Return(v1.PodSpec{}, nil).
		Times(1)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-2").
		Return(v1.PodSpec{}, fmt.Errorf("pod template error for k8s-node-2")).
		Times(1)

	result, err = controller.Reconcile(request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pod template error for k8s-node-2")
	require.Empty(t, result)

	// Verify there is event raised for failure to create pod templates for node spec
	require.Len(t, recorder.Events, 1)
	eventMsg = <-recorder.Events
	require.Contains(t, eventMsg, fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
	require.Contains(t, eventMsg, "pod template error for k8s-node-2")
}

func TestFailureDuringCreateDeletePods(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{
		Err: fmt.Errorf("pod control error"),
	}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	failedPod := createStoragePod(cluster, "failed-pod", k8sNode1.Name, storageLabels)
	failedPod.Status = v1.PodStatus{
		Phase: v1.PodFailed,
	}

	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), k8sNode3)
	k8sClient.Create(context.TODO(), failedPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pod control error")
	require.Empty(t, result)

	// Verify there is event raised for failure to create/delete pods
	require.Len(t, recorder.Events, 2)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedStoragePodReason))
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
}

func TestTimeoutFailureDuringCreatePods(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	podControl := &k8scontroller.FakePodControl{
		Err: errors.NewTimeoutError("timeout error", 0),
	}
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Verify there is no error or event raised for timeout errors
	// during pod creation
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 0)
}

func TestUpdateClusterStatusFromDriver(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().
		UpdateStorageClusterStatus(gomock.Any()).
		Do(func(c *corev1alpha1.StorageCluster) {
			c.Status.Phase = "Online"
		}).
		Return(nil)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 0)

	newCluster := &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, newCluster, cluster.Name, cluster.Namespace)
	require.Equal(t, "Online", newCluster.Status.Phase)
}

func TestUpdateClusterStatusErrorFromDriver(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().
		UpdateStorageClusterStatus(gomock.Any()).
		Do(func(c *corev1alpha1.StorageCluster) {
			c.Status.Phase = "Offline"
		}).
		Return(fmt.Errorf("update status error"))

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Equal(t, <-recorder.Events,
		fmt.Sprintf("%v %v update status error", v1.EventTypeWarning, util.FailedSyncReason))

	newCluster := &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, newCluster, cluster.Name, cluster.Namespace)
	require.Equal(t, "Offline", newCluster.Status.Phase)
}

func TestFailedPreInstallFromDriver(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(fmt.Errorf("pre-install error"))

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pre-install error")
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
}

func TestUpdateDriverWithInstanceInformation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode1.Labels = map[string]string{failureDomainZoneKey: "z1"}
	k8sNode2 := createK8sNode("k8s-node-2", 1)
	k8sNode2.Labels = map[string]string{failureDomainZoneKey: "z1"}
	k8sNode3 := createK8sNode("k8s-node-3", 1)
	k8sNode3.Labels = map[string]string{failureDomainZoneKey: "z2"}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2, k8sNode3)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	expectedDriverInfo := &storage.UpdateDriverInfo{
		ZoneToInstancesMap: map[string]int{
			"z1": 2,
			"z2": 1,
		},
	}
	driver.EXPECT().UpdateDriver(expectedDriverInfo).Return(nil)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	// Should contain cloud provider information if any node has it
	k8sNode2.Spec.ProviderID = "invalid"
	k8sClient.Update(context.TODO(), k8sNode2)
	k8sNode3.Spec.ProviderID = "testcloud://test-instance-id"
	k8sClient.Update(context.TODO(), k8sNode3)

	expectedDriverInfo.CloudProvider = "testcloud"
	driver.EXPECT().UpdateDriver(expectedDriverInfo).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	// Should not fail reconcile even if there is an error on UpdateDriver
	driver.EXPECT().UpdateDriver(expectedDriverInfo).Return(fmt.Errorf("update error"))

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)
}

func TestDeleteStorageClusterWithoutFinalizers(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := createStorageCluster()
	cluster.Finalizers = []string{}
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp

	driverName := "mock-driver"
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()

	// Kubernetes node with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Only pods that are associated with this cluster should get deleted
	storagePod1 := createStoragePod(cluster, "pod-1", k8sNode1.Name, storageLabels)
	storagePod2 := createStoragePod(cluster, "pod-2", k8sNode2.Name, storageLabels)

	storagePod3 := createStoragePod(cluster, "pod-3", k8sNode3.Name, nil)
	storagePod3.OwnerReferences = nil

	otherCluster := createStorageCluster()
	otherCluster.UID = "other-uid"
	otherCluster.Name = "other-cluster"
	storagePod4 := createStoragePod(otherCluster, "pod-4", k8sNode3.Name, storageLabels)

	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), k8sNode3)
	k8sClient.Create(context.TODO(), storagePod1)
	k8sClient.Create(context.TODO(), storagePod2)
	k8sClient.Create(context.TODO(), storagePod3)
	k8sClient.Create(context.TODO(), storagePod4)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t,
		[]string{storagePod1.Name, storagePod2.Name},
		podControl.DeletePodName,
	)
}

func TestDeleteStorageClusterWithFinalizers(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := createStorageCluster()
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp

	driverName := "mock-driver"
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	// Empty delete condition should not remove finalizer
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(nil, nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)

	k8sClient.Create(context.TODO(), k8sNode)
	k8sClient.Create(context.TODO(), storagePod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{storagePod.Name}, podControl.DeletePodName)

	updatedCluster := &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.Len(t, updatedCluster.Status.Conditions, 1)
	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, updatedCluster.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, updatedCluster.Status.Conditions[0].Status)
	require.Equal(t, "DeleteInProgress", updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If storage driver returns error, then controller should not return error but raise an event
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(nil, fmt.Errorf("delete error"))

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	raisedEvent := <-recorder.Events
	require.Contains(t, raisedEvent,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
	require.Contains(t, raisedEvent, "delete error")

	updatedCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.Len(t, updatedCluster.Status.Conditions, 1)
	require.Equal(t, corev1alpha1.ClusterConditionTypeDelete, updatedCluster.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha1.ClusterOperationInProgress, updatedCluster.Status.Conditions[0].Status)
	require.Equal(t, "DeleteInProgress", updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition is not present already, then add to the cluster
	updatedCluster.Status.Conditions = []corev1alpha1.ClusterCondition{
		{
			Type: corev1alpha1.ClusterConditionTypeInstall,
		},
	}
	k8sClient.Update(context.TODO(), updatedCluster)
	condition := &corev1alpha1.ClusterCondition{
		Type:   corev1alpha1.ClusterConditionTypeDelete,
		Status: corev1alpha1.ClusterOperationFailed,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.Len(t, updatedCluster.Status.Conditions, 2)
	require.Equal(t, *condition, updatedCluster.Status.Conditions[1])
	require.Equal(t, "DeleteFailed", updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition is present, then update it
	condition = &corev1alpha1.ClusterCondition{
		Type:   corev1alpha1.ClusterConditionTypeDelete,
		Status: corev1alpha1.ClusterOperationTimeout,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.Len(t, updatedCluster.Status.Conditions, 2)
	require.Equal(t, *condition, updatedCluster.Status.Conditions[1])
	require.Equal(t, "DeleteTimeout", updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition status is completed, then remove delete finalizer
	condition = &corev1alpha1.ClusterCondition{
		Type:   corev1alpha1.ClusterConditionTypeDelete,
		Status: corev1alpha1.ClusterOperationCompleted,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.Len(t, updatedCluster.Status.Conditions, 2)
	require.Equal(t, *condition, updatedCluster.Status.Conditions[1])
	require.Empty(t, updatedCluster.Finalizers)
}

func TestUpdateStorageClusterWithRollingUpdateStrategy(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)
	require.Equal(t, int64(1), revisions.Items[0].Revision)

	require.Len(t, podControl.ControllerRefs, 1)
	require.Equal(t, *clusterRef, podControl.ControllerRefs[0])

	// Verify that the revision hash matches that of the new pod
	require.Len(t, podControl.Templates, 1)
	require.Equal(t, revisions.Items[0].Labels[defaultStorageClusterUniqueLabelKey],
		podControl.Templates[0].Labels[defaultStorageClusterUniqueLabelKey])

	// Test case: Changing the cluster spec -
	// A new revision should be created for the new cluster spec
	// Also the pod should be changed with the updated spec
	cluster.Spec.Image = "new/image"
	k8sClient.Update(context.TODO(), cluster)
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	oldPod.Status.Conditions = append(oldPod.Status.Conditions, v1.PodCondition{
		Type:   v1.PodReady,
		Status: v1.ConditionTrue,
	})
	k8sClient.Create(context.TODO(), oldPod)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// New revision should be created for the updated cluster spec
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, int64(2), revisions.Items[1].Revision)

	// The old pod should be marked for deletion
	require.Empty(t, podControl.Templates)
	require.Empty(t, podControl.ControllerRefs)
	require.Len(t, podControl.DeletePodName, 1)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// Test case: Running reconcile again should start a new pod with new
	// revision hash.
	err = k8sClient.Delete(context.TODO(), oldPod)
	require.NoError(t, err)
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// New revision should not be created as the cluster spec is unchanged
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)

	require.Empty(t, podControl.DeletePodName)

	require.Len(t, podControl.ControllerRefs, 1)
	require.Equal(t, *clusterRef, podControl.ControllerRefs[0])

	// New revision's hash should match that of the new pod.
	require.Len(t, podControl.Templates, 1)
	require.Equal(t, revisions.Items[1].Labels[defaultStorageClusterUniqueLabelKey],
		podControl.Templates[0].Labels[defaultStorageClusterUniqueLabelKey])
}

func TestUpdateStorageClusterShouldNotExceedMaxUnavailable(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)
	k8sNode4 := createK8sNode("k8s-node-4", 10)
	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), k8sNode3)
	k8sClient.Create(context.TODO(), k8sNode4)

	// Pods that are already running on the k8s nodes with same hash
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod1 := createStoragePod(cluster, "old-pod-1", k8sNode1.Name, storageLabels)
	oldPod1.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}

	oldPod2 := oldPod1.DeepCopy()
	oldPod2.Name = "old-pod-2"
	oldPod2.Spec.NodeName = k8sNode2.Name

	oldPod3 := oldPod1.DeepCopy()
	oldPod3.Name = "old-pod-3"
	oldPod3.Spec.NodeName = k8sNode3.Name

	oldPod4 := oldPod1.DeepCopy()
	oldPod4.Name = "old-pod-4"
	oldPod4.Spec.NodeName = k8sNode4.Name

	k8sClient.Create(context.TODO(), oldPod1)
	k8sClient.Create(context.TODO(), oldPod2)
	k8sClient.Create(context.TODO(), oldPod3)
	k8sClient.Create(context.TODO(), oldPod4)

	// Should delete pods only up to maxUnavailable value. In this case - 2 pods
	maxUnavailable := intstr.FromInt(2)
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type: corev1alpha1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1alpha1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	cluster.Spec.Image = "test/image:v2"
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, podControl.DeletePodName, 2)

	// Next reconcile loop should not delete more pods as 2 are already down.
	// If a pod is marked for deletion but not actually deleted do not send it
	// for deletion again.
	deletedPods := make([]*v1.Pod, 0)
	deletedPod1 := &v1.Pod{}
	testutil.Get(k8sClient, deletedPod1, podControl.DeletePodName[0], cluster.Namespace)
	deletedPods = append(deletedPods, deletedPod1)
	k8sClient.Delete(context.TODO(), deletedPod1)

	deletedPod2 := &v1.Pod{}
	testutil.Get(k8sClient, deletedPod2, podControl.DeletePodName[1], cluster.Namespace)
	deletionTimestamp := metav1.Now()
	deletedPod2.DeletionTimestamp = &deletionTimestamp
	deletedPod2.Status.Conditions[0].Status = v1.ConditionFalse
	k8sClient.Update(context.TODO(), deletedPod2)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)
	// One pod is still being deleted, so only 1 template created instead of 2
	require.Len(t, podControl.Templates, 1)

	// If the pods are created but not ready, even then no extra pod should be deleted
	replacedPod1, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	replacedPod1.Name = replacedPod1.GenerateName + "replaced-1"
	replacedPod1.Namespace = cluster.Namespace
	replacedPod1.Spec.NodeName = deletedPods[0].Spec.NodeName
	k8sClient.Create(context.TODO(), replacedPod1)

	k8sClient.Delete(context.TODO(), deletedPod2)
	deletedPods = append(deletedPods, deletedPod2)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)
	require.Len(t, podControl.Templates, 1)

	// If another update happens to storage cluster, we should account for non-running
	// and non-ready pods in max unavailable pods. Also we should delete an old not ready
	// pod before a running one.
	cluster.Spec.Image = "test/image:v3"
	k8sClient.Update(context.TODO(), cluster)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{replacedPod1.Name}, podControl.DeletePodName)
	require.Len(t, podControl.Templates, 1)

	// Once the new pods are up and in ready state, we should delete remaining
	// pods with older versions.
	k8sClient.Delete(context.TODO(), replacedPod1)

	replacedPod2, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	replacedPod2.Name = replacedPod2.GenerateName + "replaced-2"
	replacedPod2.Namespace = cluster.Namespace
	replacedPod2.Spec.NodeName = deletedPods[0].Spec.NodeName
	replacedPod2.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), replacedPod2)

	replacedPod3 := replacedPod2.DeepCopy()
	replacedPod3.Name = replacedPod2.GenerateName + "replaced-3"
	replacedPod3.Spec.NodeName = deletedPods[1].Spec.NodeName
	k8sClient.Create(context.TODO(), replacedPod3)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, podControl.DeletePodName, 2)
	require.NotContains(t, podControl.DeletePodName, replacedPod2.Name)
	require.NotContains(t, podControl.DeletePodName, replacedPod3.Name)
}

func TestUpdateStorageClusterWithPercentageMaxUnavailable(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)
	k8sNode4 := createK8sNode("k8s-node-4", 10)
	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), k8sNode3)
	k8sClient.Create(context.TODO(), k8sNode4)

	// Pods that are already running on the k8s nodes with same hash
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod1 := createStoragePod(cluster, "old-pod-1", k8sNode1.Name, storageLabels)
	oldPod1.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}

	oldPod2 := oldPod1.DeepCopy()
	oldPod2.Name = "old-pod-2"
	oldPod2.Spec.NodeName = k8sNode2.Name

	oldPod3 := oldPod1.DeepCopy()
	oldPod3.Name = "old-pod-3"
	oldPod3.Spec.NodeName = k8sNode3.Name

	oldPod4 := oldPod1.DeepCopy()
	oldPod4.Name = "old-pod-4"
	oldPod4.Spec.NodeName = k8sNode4.Name

	k8sClient.Create(context.TODO(), oldPod1)
	k8sClient.Create(context.TODO(), oldPod2)
	k8sClient.Create(context.TODO(), oldPod3)
	k8sClient.Create(context.TODO(), oldPod4)

	// Should delete pods only up to maxUnavailable value. In this case - 75%
	maxUnavailable := intstr.FromString("75%")
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type: corev1alpha1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1alpha1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	cluster.Spec.Image = "test/image:v2"
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.Templates)
	require.Len(t, podControl.DeletePodName, 3)

	// Next reconcile loop should not delete more pods as 75% are already down,
	// rather it should try to create 3 pods (75%).
	deletedPods := make([]*v1.Pod, 0)
	for _, name := range podControl.DeletePodName {
		pod := &v1.Pod{}
		testutil.Get(k8sClient, pod, name, cluster.Namespace)
		deletedPods = append(deletedPods, pod)
		k8sClient.Delete(context.TODO(), pod)
	}

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)
	require.Len(t, podControl.Templates, 3)

	// If the pods are up and ready, then the remaining pods are deleted
	replacedPod1, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	replacedPod1.Name = replacedPod1.GenerateName + "replaced-1"
	replacedPod1.Namespace = cluster.Namespace
	replacedPod1.Spec.NodeName = deletedPods[0].Spec.NodeName
	replacedPod1.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), replacedPod1)

	replacedPod2 := replacedPod1.DeepCopy()
	replacedPod2.Name = replacedPod1.GenerateName + "replaced-2"
	replacedPod2.Spec.NodeName = deletedPods[1].Spec.NodeName
	k8sClient.Create(context.TODO(), replacedPod2)

	replacedPod3 := replacedPod2.DeepCopy()
	replacedPod3.Name = replacedPod2.GenerateName + "replaced-3"
	replacedPod3.Spec.NodeName = deletedPods[2].Spec.NodeName
	k8sClient.Create(context.TODO(), replacedPod3)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, podControl.DeletePodName, 1)
	require.Empty(t, podControl.Templates)
}

func TestUpdateStorageClusterWithInvalidMaxUnavailableValue(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	// Reconcile should fail due to invalid maxUnavailable value in RollingUpdate strategy
	maxUnavailable := intstr.FromString("invalid-value")
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type: corev1alpha1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1alpha1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid value for MaxUnavailable")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
}

func TestUpdateStorageClusterShouldRestartPodIfItDoesNotHaveAnyHash(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that is already running but does not any revision hash associated
	runningPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	runningPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), runningPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{runningPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterImagePullSecret(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := &Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)
	storagePod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), storagePod)

	// TestCase: Add imagePullSecret
	cluster.Spec.ImagePullSecret = stringPtr("pull-secret")
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Update imagePullSecret
	// Replace old pod with new configuration so that it has the image pull secret
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	// Change the image pull secret
	cluster.Spec.ImagePullSecret = stringPtr("new-pull-secret")
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Remove imagePullSecret
	// Replace old pod with new configuration so that it has the new image pull secret
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	cluster.Spec.ImagePullSecret = nil
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterCustomImageRegistry(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := &Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)
	storagePod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), storagePod)

	// TestCase: Add customImageRegistry
	cluster.Spec.CustomImageRegistry = "registry.first"
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Update customImageRegistry
	// Replace old pod with new configuration so that it has the custom image registry
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	// Change the custom image registry
	cluster.Spec.CustomImageRegistry = "registry.second"
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Remove customImageRegistry
	// Replace old pod with new configuration so that it has the new custom image registry
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	cluster.Spec.CustomImageRegistry = ""
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterKvdbSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Change spec.kvdb.internal
	cluster.Spec.Kvdb = &corev1alpha1.KvdbSpec{
		Internal: true,
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add spec.kvdb.endpoints
	cluster.Spec.Kvdb.Endpoints = []string{"kvdb1", "kvdb2"}
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.kvdb.endpoints
	cluster.Spec.Kvdb.Endpoints = []string{"kvdb2"}
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.kvdb.authSecrets
	cluster.Spec.Kvdb.AuthSecret = "test-secret"
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterCloudStorageSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add spec.cloudStorage.deviceSpecs
	deviceSpecs := []string{"spec1", "spec2"}
	cluster.Spec.CloudStorage = &corev1alpha1.CloudStorageSpec{
		DeviceSpecs: &deviceSpecs,
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.deviceSpecs
	deviceSpecs = append(deviceSpecs, "spec3")
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add spec.cloudStorage.capacitySpecs
	cluster.Spec.CloudStorage.CapacitySpecs = []corev1alpha1.CloudStorageCapacitySpec{{MinIOPS: uint32(1000)}}
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.deviceSpecs
	cluster.Spec.CloudStorage.CapacitySpecs = append(
		cluster.Spec.CloudStorage.CapacitySpecs,
		corev1alpha1.CloudStorageCapacitySpec{MinIOPS: uint32(2000)},
	)
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.journalDeviceSpec
	journalDeviceSpec := "journal-dev-spec"
	cluster.Spec.CloudStorage.JournalDeviceSpec = &journalDeviceSpec
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.systemMetadataDeviceSpec
	metadataDeviceSpec := "metadata-dev-spec"
	cluster.Spec.CloudStorage.SystemMdDeviceSpec = &metadataDeviceSpec
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.kvdbDeviceSpec
	kvdbDeviceSpec := "kvdb-dev-spec"
	cluster.Spec.CloudStorage.KvdbDeviceSpec = &kvdbDeviceSpec
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.maxStorageNodes
	maxStorageNodes := uint32(3)
	cluster.Spec.CloudStorage.MaxStorageNodes = &maxStorageNodes
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.maxStorageNodesPerZone
	cluster.Spec.CloudStorage.MaxStorageNodesPerZone = &maxStorageNodes
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterStorageSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add spec.storage.devices
	devices := []string{"spec1", "spec2"}
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		Devices: &devices,
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.devices
	devices = append(devices, "spec3")
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.journalDevice
	journalDevice := "journal-dev"
	cluster.Spec.Storage.JournalDevice = &journalDevice
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.systemMetadataDevice
	metadataDevice := "metadata-dev"
	cluster.Spec.Storage.SystemMdDevice = &metadataDevice
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.kvdbDevice
	kvdbDevice := "kvdb-dev"
	cluster.Spec.Storage.KvdbDevice = &kvdbDevice
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.useAll
	boolValue := true
	cluster.Spec.Storage.UseAll = &boolValue
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.useAllWithPartitions
	cluster.Spec.Storage.UseAllWithPartitions = &boolValue
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.forceUseDisks
	cluster.Spec.Storage.ForceUseDisks = &boolValue
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterNetworkSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Change spec.network.dataInterface
	nwInterface := "eth0"
	cluster.Spec.Network = &corev1alpha1.NetworkSpec{
		DataInterface: &nwInterface,
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.network.mgmtInterface
	cluster.Spec.Network.MgmtInterface = &nwInterface
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterEnvVariables(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add spec.env
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  "key1",
			Value: "value1",
		},
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.env
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{Name: "key2", Value: "value2"})
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterRuntimeOptions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add spec.runtimeOptions
	cluster.Spec.RuntimeOpts = map[string]string{
		"key1": "value1",
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.runtimeOptions
	cluster.Spec.RuntimeOpts["key1"] = "value2"
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterSecretsProvider(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add spec.secretsProvider
	secretsProvider := "vault"
	cluster.Spec.SecretsProvider = &secretsProvider
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.secretsProvider
	secretsProvider = "aws-kms"
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterStartPort(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add spec.startPort
	startPort := uint32(1000)
	cluster.Spec.StartPort = &startPort
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.startPort
	startPort = uint32(2000)
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterFeatureGates(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add spec.featureGates
	cluster.Spec.FeatureGates = map[string]string{
		"feature1": "enabled",
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.featureGates
	cluster.Spec.FeatureGates["feature1"] = "disabled"
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterNodeSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	useAllDevices := true
	cluster.Spec.Storage = &corev1alpha1.StorageSpec{
		UseAll: &useAllDevices,
	}
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  "CLUSTER_ENV",
			Value: "cluster_value",
		},
	}
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sNode.Labels = map[string]string{
		"test":  "foo",
		"extra": "label",
	}
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Add node specific storage configuration.
	// Should start with that instead of cluster level configuration.
	devices := []string{"dev1", "dev2"}
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{
		{
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					Devices: &devices,
				},
				Network: &corev1alpha1.NetworkSpec{
					DataInterface: stringPtr("dface_1"),
					MgmtInterface: stringPtr("mface_1"),
				},
				Env: []v1.EnvVar{
					{
						Name:  "NODE_ENV",
						Value: "node_value_1",
					},
					{
						Name:  "COMMON_ENV",
						Value: "node_value_1",
					},
				},
				RuntimeOpts: map[string]string{
					"node_rt_1": "node_rt_value_1",
				},
			},
		},
		{
			// Should ignore spec blocks if it has invalid label selectors
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "test",
							Operator: "InvalidOperator",
						},
					},
				},
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					ForceUseDisks: &useAllDevices,
				},
			},
		},
		{
			Selector: corev1alpha1.NodeSelector{
				NodeName: "k8s-node",
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					UseAllWithPartitions: &useAllDevices,
				},
				Network: &corev1alpha1.NetworkSpec{
					DataInterface: stringPtr("dface_2"),
					MgmtInterface: stringPtr("mface_2"),
				},
				Env: []v1.EnvVar{
					{
						Name:  "NODE_ENV",
						Value: "node_value_2",
					},
					{
						Name:  "COMMON_ENV",
						Value: "node_value_2",
					},
				},
				RuntimeOpts: map[string]string{
					"node_rt_2": "node_rt_value_2",
				},
			},
		},
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change node specific storage configuration.
	devices = append(devices, "spec3")
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs := &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change node specific network configuration.
	cluster.Spec.Nodes[0].Network.DataInterface = stringPtr("new_data_interface")
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change existing runtime option in node specific configuration.
	cluster.Spec.Nodes[0].RuntimeOpts["node_rt_1"] = "changed_value"
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add runtime option in node specific configuration.
	cluster.Spec.Nodes[0].RuntimeOpts["new_rt_option"] = "new_value"
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change env var value in node specific configuration.
	cluster.Spec.Nodes[0].Env[0].Value = "changed_value"
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add env var in node specific configuration.
	cluster.Spec.Nodes[0].Env = append(cluster.Spec.Nodes[0].Env, v1.EnvVar{
		Name:  "ADD_ENV",
		Value: "newly_added_env",
	})
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change env var value in cluster configuration.
	cluster.Spec.Env[0].Value = "changed_cluster_value"
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add env var in cluster configuration.
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  "ADD_CLUSTER_ENV",
		Value: "newly_added_cluster_env",
	})
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change env var value in cluster configuration which is already
	// overridden in node level configuration. As nothing will be changed in the final
	// spec, pod should not restart.
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  "COMMON_ENV",
		Value: "cluster_value",
	})
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change selector in node block such tha it still matches the same node.
	// Should not restart the pod as the node level configuration is unchanged.
	cluster.Spec.Nodes[0].Selector.LabelSelector.MatchLabels["extra"] = "label"
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change selector in node block so it the block does not match
	// the node. Start using configuration from another spec block that matches.
	cluster.Spec.Nodes[0].Selector.LabelSelector.MatchLabels["test"] = "bar"
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove node specific configuration.
	// Should start using cluster level configuration.
	cluster.Spec.Nodes = nil
	k8sClient.Update(context.TODO(), cluster)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	oldPod.Labels[defaultStorageClusterUniqueLabelKey] = revs.Items[len(revs.Items)-1].Labels[defaultStorageClusterUniqueLabelKey]
	k8sClient.Update(context.TODO(), oldPod)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterK8sNodeChanges(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	useAllDevices := true
	cluster.Spec.Nodes = []corev1alpha1.NodeSpec{
		{
			Selector: corev1alpha1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					UseAll: &useAllDevices,
				},
			},
		},
	}
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sNode.Labels = map[string]string{
		"test":  "foo",
		"extra": "label",
	}
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	encodedNodeLabels, _ := json.Marshal(k8sNode.Labels)
	oldPod.Annotations = map[string]string{annotationNodeLabels: string(encodedNodeLabels)}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Change node labels so that existing spec block does not match.
	// Start using configuration from another spec block that matches.
	k8sNode.Labels["test"] = "bar"
	k8sClient.Update(context.TODO(), k8sNode)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove kubernetes nodes. The pod should be marked for deletion.
	k8sClient.Delete(context.TODO(), k8sNode)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

}

func TestUpdateStorageClusterShouldNotRestartPodsForSomeOptions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Change spec.updateStrategy
	maxUnavailable := intstr.FromInt(10)
	cluster.Spec.UpdateStrategy = corev1alpha1.StorageClusterUpdateStrategy{
		Type: corev1alpha1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1alpha1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	k8sClient.Update(context.TODO(), cluster)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should not be deleted as pod restart is not needed
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.deleteStrategy
	cluster.Spec.DeleteStrategy = &corev1alpha1.StorageClusterDeleteStrategy{
		Type: corev1alpha1.UninstallStorageClusterStrategyType,
	}
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.revisionHistoryLimit
	revisionHistoryLimit := int32(5)
	cluster.Spec.RevisionHistoryLimit = &revisionHistoryLimit
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.version
	cluster.Spec.Version = "1.0.0"
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.imagePullPolicy
	cluster.Spec.ImagePullPolicy = v1.PullNever
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.userInterface
	cluster.Spec.UserInterface = &corev1alpha1.UserInterfaceSpec{Enabled: true}
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.stork
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{Image: "test/image"}
	k8sClient.Update(context.TODO(), cluster)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)
}

func TestUpdateStorageClusterShouldRestartPodIfItsHistoryHasInvalidSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	cluster.Spec.Image = "image/v2"
	invalidRevision, err := getRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	invalidRevision.Data.Raw = []byte("{}")
	err = k8sClient.Create(context.TODO(), invalidRevision)
	require.NoError(t, err)

	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	storageLabels[defaultStorageClusterUniqueLabelKey] = invalidRevision.Labels[defaultStorageClusterUniqueLabelKey]
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	k8sClient.Create(context.TODO(), oldPod)

	// TestCase: Should restart pod if it's corresponding revision does not have spec
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Should restart pod if it's corresponding revision has empty spec
	invalidRevision.Data.Raw = []byte("{\"spec\": \"\"}")
	err = k8sClient.Update(context.TODO(), invalidRevision)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateClusterShouldDedupOlderRevisionsInHistory(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Image = "test/image:v1"
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)
	firstRevision := revisions.Items[0].DeepCopy()
	require.Equal(t, int64(1), firstRevision.Revision)

	// Test case: Changing the cluster spec -
	// A new revision should be created for the new cluster spec.
	// Create some duplicate revisions that will be deduplicated later.
	dupHistory1 := firstRevision.DeepCopy()
	dupHistory1.Name = historyName(cluster.Name, "00001")
	dupHistory1.Labels[defaultStorageClusterUniqueLabelKey] = "00001"
	dupHistory1.Revision = firstRevision.Revision + 1
	k8sClient.Create(context.TODO(), dupHistory1)

	dupHistory2 := firstRevision.DeepCopy()
	dupHistory2.Name = historyName(cluster.Name, "00002")
	dupHistory2.Labels[defaultStorageClusterUniqueLabelKey] = "00002"
	dupHistory2.Revision = firstRevision.Revision + 2
	k8sClient.Create(context.TODO(), dupHistory2)

	// The created pod should have the hash of first revision
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, firstRevision.Labels[defaultStorageClusterUniqueLabelKey],
		oldPod.Labels[defaultStorageClusterUniqueLabelKey])
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	k8sClient.Create(context.TODO(), oldPod)

	cluster.Spec.Image = "test/image:v2"
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Latest revision should be created for the updated cluster spec.
	// There were already 3 revisions in the history, now it should be 4.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 4)
	require.Equal(t, int64(4), revisions.Items[3].Revision)

	// Test case: Changing the cluster spec back to the first version -
	// The revision number in the existing controller revision should be
	// updated to the latest number. The hash is going to remain the same.
	// Hence, new revision does not need to be created. Older duplicate
	// revisions should be removed and pod's hash should be updated to latest.
	cluster.Spec.Image = "test/image:v1"
	k8sClient.Update(context.TODO(), cluster)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// New revision should not be created as the cluster spec is unchanged.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, dupHistory2.Name, revisions.Items[0].Name)
	require.Equal(t, int64(5), revisions.Items[0].Revision)
	require.Equal(t, int64(4), revisions.Items[1].Revision)

	// No pod should be marked for deletion as the pod already has same spec
	// as the current spec. Check only if the pod's hash has been updated to
	// the latest duplicate version in history.
	require.Empty(t, podControl.DeletePodName)

	updatedPod := &v1.Pod{}
	testutil.Get(k8sClient, updatedPod, oldPod.Name, oldPod.Namespace)
	require.Equal(t, dupHistory2.Labels[defaultStorageClusterUniqueLabelKey],
		updatedPod.Labels[defaultStorageClusterUniqueLabelKey])
}

func TestUpdateClusterShouldHandleHashCollisions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Image = "image/v1"
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	fakeClient := fake.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	operatorops.SetInstance(operatorops.New(fakeClient))

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	// TestCase: Simulate that the cluster got deleted while constructing history.
	// Create a colliding revision that has the same name as the current revision
	// to be created but the cluster spec does not match the current cluster spec.
	actualRevision, _ := getRevision(k8sClient, cluster, driverName)
	cluster.Spec.Image = "image/v2"
	collidingRevision, _ := getRevision(k8sClient, cluster, driverName)
	collidingRevision.Name = actualRevision.Name
	k8sClient.Create(context.TODO(), collidingRevision)
	cluster.Spec.Image = "image/v1"

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("\"%s\" not found", cluster.Name))

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster := &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.Nil(t, currCluster.Status.CollisionCount)

	// New revision should not be created
	revisions := &appsv1.ControllerRevisionList{}
	testutil.List(k8sClient, revisions)
	require.Len(t, revisions.Items, 1)

	// TestCase: Hash collision with two revisions should result in an error, but the
	// CollisionCount should be increased so it does not conflict on next reconcile.
	_, err = fakeClient.CoreV1alpha1().StorageClusters(cluster.Namespace).Create(cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.Equal(t, int32(1), *currCluster.Status.CollisionCount)

	// New revision should not be created
	testutil.List(k8sClient, revisions)
	require.Len(t, revisions.Items, 1)

	// TestCase: If the collision count of current cluster and newly retrieved
	// cluster do not match, then error out and retry until the current cluster
	// gets the latest value from the api server. Do not increase the collision
	// count in this case.
	actualRevision, _ = getRevision(k8sClient, currCluster, driverName)
	cluster.Spec.Image = "image/v2"
	collidingRevision, _ = getRevision(k8sClient, cluster, driverName)
	collidingRevision.Name = actualRevision.Name
	k8sClient.Create(context.TODO(), collidingRevision)
	cluster.Spec.Image = "image/v1"

	result, err = controller.Reconcile(request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "found a stale collision count")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.Equal(t, int32(1), *currCluster.Status.CollisionCount)

	// New revision should not be created
	testutil.List(k8sClient, revisions)
	require.Len(t, revisions.Items, 2)

	// TestCase: Hash collision with two revisions should result in an error, but the
	// CollisionCount should be increased to avoid conflict on next reconcile.
	_, err = fakeClient.CoreV1alpha1().StorageClusters(cluster.Namespace).Update(currCluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.Equal(t, int32(2), *currCluster.Status.CollisionCount)

	// New revision should not be created
	testutil.List(k8sClient, revisions)
	require.Len(t, revisions.Items, 2)

	// TestCase: As hash collision has be handed in previous reconcile but increasing
	// the CollisionCount, we should not get error now during reconcile and a new
	// revision should be created.
	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	// There should be 3 revisions because we created 2 colliding ones above
	testutil.List(k8sClient, revisions)
	require.Len(t, revisions.Items, 3)
}

func TestUpdateClusterShouldDedupRevisionsAnywhereInHistory(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Image = "test/image:v1"
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	k8sClient.Create(context.TODO(), k8sNode)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)
	firstRevision := revisions.Items[0].DeepCopy()
	require.Equal(t, int64(1), firstRevision.Revision)

	// Test case: Changing the cluster spec -
	// A new revision should be created for the new cluster spec.
	// Create some duplicate revisions that will be deduplicated later.
	dupHistory1 := firstRevision.DeepCopy()
	dupHistory1.Name = historyName(cluster.Name, "00001")
	dupHistory1.Labels[defaultStorageClusterUniqueLabelKey] = "00001"
	dupHistory1.Revision = firstRevision.Revision + 1
	k8sClient.Create(context.TODO(), dupHistory1)

	// The created pod should have the hash of first revision
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, firstRevision.Labels[defaultStorageClusterUniqueLabelKey],
		oldPod.Labels[defaultStorageClusterUniqueLabelKey])
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	k8sClient.Create(context.TODO(), oldPod)

	cluster.Spec.Image = "test/image:v2"
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Latest revision should be created for the updated cluster spec.
	// There were already 2 revisions in the history, now it should be 3.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 3)
	require.Equal(t, int64(3), revisions.Items[2].Revision)

	// Test case: Changing the cluster spec back to the first version -
	// The revision number in the existing controller revision should be
	// updated to the latest number. The hash is going to remain the same.
	// Hence, new revision does not need to be created. Older duplicate
	// revisions should be removed and pod's hash should be updated to latest.
	dupHistory2 := firstRevision.DeepCopy()
	dupHistory2.Name = historyName(cluster.Name, "00002")
	dupHistory2.Labels[defaultStorageClusterUniqueLabelKey] = "00002"
	dupHistory2.Revision = revisions.Items[2].Revision + 1
	k8sClient.Create(context.TODO(), dupHistory2)

	cluster.Spec.Image = "test/image:v1"
	k8sClient.Update(context.TODO(), cluster)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// New revision should not be created as the cluster spec is unchanged.
	// The latest revision should be unchanged, but previous one need should
	// be deleted.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, dupHistory2.Name, revisions.Items[1].Name)
	require.Equal(t, int64(4), revisions.Items[1].Revision)
	require.Equal(t, int64(3), revisions.Items[0].Revision)

	// No pod should be marked for deletion as the pod already has same spec
	// as the current spec. Check only if the pod's hash has been updated to
	// the latest duplicate version in history.
	require.Empty(t, podControl.DeletePodName)

	updatedPod := &v1.Pod{}
	testutil.Get(k8sClient, updatedPod, oldPod.Name, oldPod.Namespace)
	require.Equal(t, dupHistory2.Labels[defaultStorageClusterUniqueLabelKey],
		updatedPod.Labels[defaultStorageClusterUniqueLabelKey])
}

func TestHistoryCleanup(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	revisionLimit := int32(1)
	cluster.Spec.RevisionHistoryLimit = &revisionLimit
	cluster.Spec.Image = "test/image:v1"
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()

	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	cluster.Spec.Image = "test/image:v2"
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, int64(1), revisions.Items[0].Revision)
	require.Equal(t, int64(2), revisions.Items[1].Revision)

	// Test case: Change cluster spec to add another revision.
	// Ensure that the older revision gets deleted as it is not used.
	runningPod1, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[3], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, revisions.Items[1].Labels[defaultStorageClusterUniqueLabelKey],
		runningPod1.Labels[defaultStorageClusterUniqueLabelKey])
	runningPod1.Name = runningPod1.GenerateName + "1"
	runningPod1.Namespace = cluster.Namespace
	runningPod1.Spec.NodeName = k8sNode1.Name

	cluster.Spec.Image = "test/image:v3"
	k8sClient.Update(context.TODO(), cluster)

	// Reset the fake pod controller
	podControl.Templates = nil

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, int64(2), revisions.Items[0].Revision)
	require.Equal(t, int64(3), revisions.Items[1].Revision)

	// Test case: Changing spec again to create another revision.
	// The history should not get deleted this time although it crosses
	// the limit because there are pods referring the older revisions.
	k8sClient.Create(context.TODO(), runningPod1)

	runningPod2, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, revisions.Items[1].Labels[defaultStorageClusterUniqueLabelKey],
		runningPod2.Labels[defaultStorageClusterUniqueLabelKey])
	runningPod2.Name = runningPod2.GenerateName + "2"
	runningPod2.Namespace = cluster.Namespace
	runningPod2.Spec.NodeName = k8sNode2.Name
	k8sClient.Create(context.TODO(), runningPod2)

	cluster.Spec.Image = "test/image:v4"
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 3)
	require.Equal(t, int64(2), revisions.Items[0].Revision)
	require.Equal(t, int64(3), revisions.Items[1].Revision)
	require.Equal(t, int64(4), revisions.Items[2].Revision)

	// Test case: Changing spec again to create another revision.
	// The unused revisions should be deleted from history. Delete
	// an existing pod and ensure it's revision if older than limit
	// should also get removed.
	err = k8sClient.Delete(context.TODO(), runningPod1)
	require.NoError(t, err)

	cluster.Spec.Image = "test/image:v5"
	k8sClient.Update(context.TODO(), cluster)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, int64(3), revisions.Items[0].Revision)
	require.Equal(t, int64(5), revisions.Items[1].Revision)
}

func replaceOldPod(
	oldPod *v1.Pod,
	cluster *corev1alpha1.StorageCluster,
	controller *Controller,
	podControl *k8scontroller.FakePodControl,
) *v1.Pod {
	// Delete the old pod
	controller.client.Delete(context.TODO(), oldPod)

	// Reconcile once to let the controller create a template for the new pod
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	controller.Reconcile(request)

	// Create the new pod from the template that fake pod controller received
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	newPod, _ := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	newPod.Name = oldPod.Name
	newPod.Namespace = cluster.Namespace
	newPod.Spec.NodeName = oldPod.Spec.NodeName
	newPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	controller.client.Create(context.TODO(), newPod)

	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil
	return newPod
}

func createStorageCluster() *corev1alpha1.StorageCluster {
	maxUnavailable := intstr.FromInt(defaultMaxUnavailablePods)
	revisionLimit := int32(defaultRevisionHistoryLimit)
	return &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			UID:        "test-uid",
			Name:       "test-cluster",
			Namespace:  "test-ns",
			Finalizers: []string{deleteFinalizerName},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			ImagePullPolicy: v1.PullAlways,
			Stork: &corev1alpha1.StorkSpec{
				Enabled: false,
			},
			RevisionHistoryLimit: &revisionLimit,
			UpdateStrategy: corev1alpha1.StorageClusterUpdateStrategy{
				Type: corev1alpha1.RollingUpdateStorageClusterStrategyType,
				RollingUpdate: &corev1alpha1.RollingUpdateStorageCluster{
					MaxUnavailable: &maxUnavailable,
				},
			},
		},
	}
}

func createRevision(
	k8sClient client.Client,
	cluster *corev1alpha1.StorageCluster,
	driverName string,
) (string, error) {
	history, err := getRevision(k8sClient, cluster, driverName)
	if err != nil {
		return "", err
	}
	if err := k8sClient.Create(context.TODO(), history); err != nil {
		return "", err
	}
	return history.Labels[defaultStorageClusterUniqueLabelKey], nil
}

func getRevision(
	k8sClient client.Client,
	cluster *corev1alpha1.StorageCluster,
	driverName string,
) (*appsv1.ControllerRevision, error) {
	patch, err := getPatch(cluster)
	if err != nil {
		return nil, err
	}

	hash := computeHash(&cluster.Spec, cluster.Status.CollisionCount)
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      historyName(cluster.Name, hash),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				labelKeyName:                        cluster.Name,
				labelKeyDriverName:                  driverName,
				defaultStorageClusterUniqueLabelKey: hash,
			},
			Annotations:     cluster.Annotations,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, controllerKind)},
		},
		Data: runtime.RawExtension{Raw: patch},
	}, nil
}

func createStoragePod(
	cluster *corev1alpha1.StorageCluster,
	podName, nodeName string,
	labels map[string]string,
) *v1.Pod {
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
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

func createK8sNode(nodeName string, allowedPods int) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
		Status: v1.NodeStatus{
			Allocatable: map[v1.ResourceName]resource.Quantity{
				v1.ResourcePods: resource.MustParse(strconv.Itoa(allowedPods)),
			},
		},
	}
}

func stringSlicePtr(slice []string) *[]string {
	return &slice
}

func stringPtr(str string) *string {
	return &str
}
