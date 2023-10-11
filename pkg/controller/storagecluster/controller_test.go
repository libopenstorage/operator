package storagecluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/hashicorp/go-version"
	ocp_configv1 "github.com/openshift/api/config/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	kversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/libopenstorage/cloudops"
	storageapi "github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/operator/drivers/storage"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/fake"
	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/scheme"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/libopenstorage/operator/pkg/preflight"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
)

func TestInit(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	fakeClient := fakek8sclient.NewSimpleClientset()
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakeClient))
	recorder := record.NewFakeRecorder(10)

	mgr := mock.NewMockManager(mockCtrl)
	mockCache := mock.NewMockCache(mockCtrl)
	mockCache.EXPECT().
		IndexField(gomock.Any(), gomock.Any(), nodeNameIndex, gomock.Any()).
		Return(nil).
		AnyTimes()
	mgr.EXPECT().GetClient().Return(k8sClient).AnyTimes()
	mgr.EXPECT().GetScheme().Return(scheme.Scheme).AnyTimes()
	mgr.EXPECT().GetEventRecorderFor(gomock.Any()).Return(recorder).AnyTimes()
	mgr.EXPECT().GetConfig().Return(&rest.Config{
		Host:    "127.0.0.1",
		APIPath: "fake",
	}).AnyTimes()
	mgr.EXPECT().SetFields(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().GetCache().Return(mockCache).AnyTimes()
	mgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().GetLogger().Return(log.Log.WithName("test")).AnyTimes()

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
		GitVersion: "v1.16.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	group := corev1.SchemeGroupVersion.Group
	storageClusterCRDName := corev1.StorageClusterResourcePlural + "." + group

	// When the CRDs are created, just updated their status so the validation
	// does not get stuck until timeout.
	go func() {
		err := testutil.ActivateCRDWhenCreated(fakeExtClient, storageClusterCRDName)
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

	scCRD, err := fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageClusterCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, storageClusterCRDName, scCRD.Name)
	require.Equal(t, corev1.SchemeGroupVersion.Group, scCRD.Spec.Group)
	require.Len(t, scCRD.Spec.Versions, 2)
	require.Equal(t, corev1.SchemeGroupVersion.Version, scCRD.Spec.Versions[0].Name)
	require.True(t, scCRD.Spec.Versions[0].Served)
	require.True(t, scCRD.Spec.Versions[0].Storage)
	subresource := &apiextensionsv1.CustomResourceSubresources{
		Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
	}
	require.Equal(t, subresource, scCRD.Spec.Versions[0].Subresources)
	require.NotEmpty(t, scCRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties)
	require.Equal(t, "v1alpha1", scCRD.Spec.Versions[1].Name)
	require.False(t, scCRD.Spec.Versions[1].Served)
	require.False(t, scCRD.Spec.Versions[1].Storage)
	require.Equal(t, subresource, scCRD.Spec.Versions[1].Subresources)
	require.NotEmpty(t, scCRD.Spec.Versions[1].Schema.OpenAPIV3Schema)
	require.Empty(t, scCRD.Spec.Versions[1].Schema.OpenAPIV3Schema.Properties)
	require.Equal(t, apiextensionsv1.NamespaceScoped, scCRD.Spec.Scope)
	require.Equal(t, corev1.StorageClusterResourceName, scCRD.Spec.Names.Singular)
	require.Equal(t, corev1.StorageClusterResourcePlural, scCRD.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(corev1.StorageCluster{}).Name(), scCRD.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(corev1.StorageClusterList{}).Name(), scCRD.Spec.Names.ListKind)
	require.Equal(t, []string{corev1.StorageClusterShortName}, scCRD.Spec.Names.ShortNames)

	// If CRDs are already present, then should update it
	scCRD.ResourceVersion = "1000"
	_, err = fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Update(context.TODO(), scCRD, metav1.UpdateOptions{})
	require.NoError(t, err)

	// The fake client overwrites the status in Update call which real client
	// does not. This will keep the CRD activated so validation does not get stuck.
	go func() {
		err := keepCRDActivated(fakeExtClient, storageClusterCRDName)
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
	require.Equal(t, storageClusterCRDName, crds.Items[0].Name)

	scCRD, err = fakeExtClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageClusterCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "1000", scCRD.ResourceVersion)
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
	storageClusterCRDName := corev1.StorageClusterResourcePlural + "." + group

	// When the CRDs are created, just updated their status so the validation
	// does not get stuck until timeout.
	go func() {
		err := testutil.ActivateV1beta1CRDWhenCreated(fakeExtClient, storageClusterCRDName)
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

	scCRD, err := fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageClusterCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, storageClusterCRDName, scCRD.Name)
	require.Equal(t, corev1.SchemeGroupVersion.Group, scCRD.Spec.Group)
	require.Len(t, scCRD.Spec.Versions, 2)
	require.Equal(t, corev1.SchemeGroupVersion.Version, scCRD.Spec.Versions[0].Name)
	require.True(t, scCRD.Spec.Versions[0].Served)
	require.True(t, scCRD.Spec.Versions[0].Storage)
	require.Equal(t, "v1alpha1", scCRD.Spec.Versions[1].Name)
	require.False(t, scCRD.Spec.Versions[1].Served)
	require.False(t, scCRD.Spec.Versions[1].Storage)
	require.Equal(t, apiextensionsv1beta1.NamespaceScoped, scCRD.Spec.Scope)
	require.Equal(t, corev1.StorageClusterResourceName, scCRD.Spec.Names.Singular)
	require.Equal(t, corev1.StorageClusterResourcePlural, scCRD.Spec.Names.Plural)
	require.Equal(t, reflect.TypeOf(corev1.StorageCluster{}).Name(), scCRD.Spec.Names.Kind)
	require.Equal(t, reflect.TypeOf(corev1.StorageClusterList{}).Name(), scCRD.Spec.Names.ListKind)
	require.Equal(t, []string{corev1.StorageClusterShortName}, scCRD.Spec.Names.ShortNames)
	subresource := &apiextensionsv1beta1.CustomResourceSubresources{
		Status: &apiextensionsv1beta1.CustomResourceSubresourceStatus{},
	}
	require.Equal(t, subresource, scCRD.Spec.Subresources)
	require.NotEmpty(t, scCRD.Spec.Validation.OpenAPIV3Schema.Properties)

	// If CRDs are already present, then should update it
	scCRD.ResourceVersion = "1000"
	_, err = fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Update(context.TODO(), scCRD, metav1.UpdateOptions{})
	require.NoError(t, err)

	// The fake client overwrites the status in Update call which real client
	// does not. This will keep the CRD activated so validation does not get stuck.
	go func() {
		err := keepV1beta1CRDActivated(fakeExtClient, storageClusterCRDName)
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
	require.Equal(t, storageClusterCRDName, crds.Items[0].Name)

	scCRD, err = fakeExtClient.ApiextensionsV1beta1().
		CustomResourceDefinitions().
		Get(context.TODO(), storageClusterCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "1000", scCRD.ResourceVersion)
}

func TestKubernetesVersionValidation(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "v1.20.99",
	}
	coreops.SetInstance(coreops.New(fakeClient))

	cluster := &corev1.StorageCluster{
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
	result, err := controller.Reconcile(context.TODO(), request)
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

	result, err = controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), "invalid kubernetes version received")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v invalid kubernetes version received",
			v1.EventTypeWarning, util.FailedValidationReason))

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), updatedCluster.Status.Phase)
	require.Empty(t, updatedCluster.Status.Conditions)
}

func TestSingleClusterValidation(t *testing.T) {
	existingCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "main-cluster",
			Namespace:  "main-ns",
			Finalizers: []string{deleteFinalizerName},
		},
	}
	cluster := &corev1.StorageCluster{
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
	result, err := controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), fmt.Sprintf("only one StorageCluster is allowed in a Kubernetes cluster. "+
		"StorageCluster %s/%s already exists", existingCluster.Namespace, existingCluster.Name))

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v only one StorageCluster is allowed in a Kubernetes cluster. "+
			"StorageCluster %s/%s already exists", v1.EventTypeWarning, util.FailedValidationReason,
			existingCluster.Namespace, existingCluster.Name))

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), updatedCluster.Status.Phase)
}

func TestWaitForMigrationApproval(t *testing.T) {
	mockCtrl := gomock.NewController(t)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "invalid",
			},
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	k8sClient := testutil.FakeK8sClient(cluster)
	driver := testutil.MockDriver(mockCtrl)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	// TestCase: Migration annotation has invalid value
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%s %s To proceed with the migration, set the %s annotation on the "+
			"StorageCluster to 'true'", v1.EventTypeNormal, util.MigrationPendingReason,
			constants.AnnotationMigrationApproved))

	currentCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currentCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, currentCluster.Finalizers, 0)

	// TestCase: Migration is not approved
	currentCluster.Annotations[constants.AnnotationMigrationApproved] = "false"
	err = k8sClient.Update(context.TODO(), currentCluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%s %s To proceed with the migration, set the %s annotation on the "+
			"StorageCluster to 'true'", v1.EventTypeNormal, util.MigrationPendingReason,
			constants.AnnotationMigrationApproved))

	currentCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currentCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, currentCluster.Finalizers, 0)

	// TestCase: Migration is approved but status is not yet updated
	currentCluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	err = k8sClient.Update(context.TODO(), currentCluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%s %s To proceed with the migration, set the %s annotation on the "+
			"StorageCluster to 'true'", v1.EventTypeNormal, util.MigrationPendingReason,
			constants.AnnotationMigrationApproved))

	currentCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currentCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, currentCluster.Finalizers, 0)

	// TestCase: Migration is approved but status is still in AwaitingMigrationApproval phase
	util.UpdateStorageClusterCondition(currentCluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeMigration,
		Status: corev1.ClusterConditionStatusPending,
	})
	err = k8sClient.Status().Update(context.TODO(), currentCluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%s %s To proceed with the migration, set the %s annotation on the "+
			"StorageCluster to 'true'", v1.EventTypeNormal, util.MigrationPendingReason,
			constants.AnnotationMigrationApproved))

	currentCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currentCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, currentCluster.Finalizers, 0)

	// TestCase: Migration is approved and status shows migration in progress
	util.UpdateStorageClusterCondition(currentCluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeMigration,
		Status: corev1.ClusterConditionStatusInProgress,
	})

	err = k8sClient.Status().Update(context.TODO(), currentCluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, recorder.Events, 0)

	currentCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currentCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, currentCluster.Finalizers, 1)

	// TestCase: Migration is approved and status shows Failed
	currentCluster.Finalizers = nil
	err = k8sClient.Update(context.TODO(), currentCluster)
	require.NoError(t, err)
	currentCluster.Status.Phase = string(corev1.ClusterStateDegraded)
	err = k8sClient.Status().Update(context.TODO(), currentCluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, recorder.Events, 0)

	currentCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currentCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, currentCluster.Finalizers, 1)
}

func TestCloudStorageLabelSelector(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := createStorageCluster()
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)

	controller := Controller{
		client: k8sClient,
		Driver: driver,
	}

	getCloudStorageSpec := func(devType string) *corev1.CloudStorageNodeSpec {
		return &corev1.CloudStorageNodeSpec{
			CloudStorageCommon: corev1.CloudStorageCommon{
				DeviceSpecs: stringSlicePtr([]string{fmt.Sprintf("type=" + devType)}),
			},
		}
	}

	// NodeName selector
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				NodeName: "Test",
			},
			CloudStorage: getCloudStorageSpec("type1"),
		},
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mockDriverName").AnyTimes()
	err := controller.validateCloudStorageLabelKey(cluster)
	require.Error(t, err)

	// Empty selector
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node1",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type1"),
		},
		{
			Selector:     corev1.NodeSelector{},
			CloudStorage: getCloudStorageSpec("type2"),
		},
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mockDriverName").AnyTimes()
	err = controller.validateCloudStorageLabelKey(cluster)
	require.Error(t, err)

	// Two keys in one selector
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node1",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type1"),
		},
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test":  "node2",
						"test2": "node3",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type2"),
		},
	}
	err = controller.validateCloudStorageLabelKey(cluster)
	require.Error(t, err)

	// Different keys in selectors
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node1",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type1"),
		},
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test2": "node3",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type2"),
		},
	}
	err = controller.validateCloudStorageLabelKey(cluster)
	require.Error(t, err)

	// One node does not have cloud storage
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node1",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type1"),
		},
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test2": "node3",
					},
				},
			},
		},
	}
	err = controller.validateCloudStorageLabelKey(cluster)
	require.NoError(t, err)

	// Selector key is different from NodePoolLabel
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"key": "node1",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type1"),
		},
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"key": "node2",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type2"),
		},
	}
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		NodePoolLabel: "key2",
	}
	err = controller.validateCloudStorageLabelKey(cluster)
	require.Error(t, err)

	// Validation skipped if storage pods are present
	podLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  "mockDriverName",
	}
	storagePod := createStoragePod(cluster, "running-pod", "testNodeName", podLabels)
	err = controller.client.Create(context.TODO(), storagePod)
	require.NoError(t, err)
	err = controller.validateCloudStorageLabelKey(cluster)
	require.NoError(t, err)
	err = controller.client.Delete(context.TODO(), storagePod)
	require.NoError(t, err)

	// Node pool label correct
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"key": "node1",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type1"),
		},
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"key": "node2",
					},
				},
			},
			CloudStorage: getCloudStorageSpec("type2"),
		},
	}
	cluster.Spec.CloudStorage.NodePoolLabel = "key"
	err = controller.validateCloudStorageLabelKey(cluster)
	require.NoError(t, err)

	// Node pool label empty
	cluster.Spec.CloudStorage.NodePoolLabel = ""
	err = controller.validateCloudStorageLabelKey(cluster)
	require.NoError(t, err)

	// Node pool label get set by default
	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	cluster.Spec.CloudStorage.NodePoolLabel = ""
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, cluster.Spec.CloudStorage.NodePoolLabel, "key")
}

func TestStorageClusterDefaults(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
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

	controller.log(cluster).Debugf("testing default cluster")

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).AnyTimes()

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

	cluster := &corev1.StorageCluster{
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

	driver.EXPECT().UpdateDriver(gomock.Any()).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()

	// Use rolling update as default update strategy if nothing specified
	err := controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, 1, cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntValue())

	// Use default max unavailable if rolling update is set but empty
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type: corev1.RollingUpdateStorageClusterStrategyType,
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, 1, cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntValue())

	// Use default max unavailable if rolling update is set but max unavailable not set
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type:          corev1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1.RollingUpdateStorageCluster{},
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, 1, cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntValue())

	// Don't use default max unavailable if rolling update and max unavailable is set
	maxUnavailable := intstr.FromString("20%")
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type: corev1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1.RollingUpdateStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Equal(t, "20%", cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.String())

	// Don't overwrite update strategy is specified
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type: corev1.OnDeleteStorageClusterStrategyType,
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1.OnDeleteStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Nil(t, cluster.Spec.UpdateStrategy.RollingUpdate)
}

func TestStorageClusterDefaultsForFinalizer(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
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

	driver.EXPECT().UpdateDriver(gomock.Any()).AnyTimes()
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

	cluster := &corev1.StorageCluster{
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

	driver.EXPECT().UpdateDriver(gomock.Any()).AnyTimes()
	driver.EXPECT().
		SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(cluster *corev1.StorageCluster) {
			cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
				Type: corev1.OnDeleteStorageClusterStrategyType,
			}
			cluster.Spec.Stork = &corev1.StorkSpec{
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
	require.Equal(t, corev1.OnDeleteStorageClusterStrategyType, cluster.Spec.UpdateStrategy.Type)
	require.Empty(t, cluster.Spec.UpdateStrategy.RollingUpdate)

	// Should update cluster even if status is modified
	driver.EXPECT().
		SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(cluster *corev1.StorageCluster) {
			cluster.Status.Version = "1.2.3"
		})

	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)

	require.Equal(t, "1.2.3", cluster.Status.Version)
}

// When DaemonSet is present (old installation method), the reconcile loop should not proceed with installation.
func TestReconcileWithDaemonSet(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	recorder := record.NewFakeRecorder(10)
	cluster := createStorageCluster()
	daemonSet := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "portworx",
		},
	}
	daemonSetList := &appsv1.DaemonSetList{Items: []appsv1.DaemonSet{daemonSet}}

	k8sVersion, _ := version.NewVersion("1.19.0")
	k8sClient := testutil.FakeK8sClient(cluster, daemonSetList)
	driver := testutil.MockDriver(mockCtrl)
	controller := Controller{
		client:            k8sClient,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
		Driver:            driver,
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mockDriverName").AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	logrus.WithError(err).Info("Reconcile finished")
	require.NotNil(t, err)
	require.Empty(t, result)
	require.NotEmpty(t, recorder.Events)

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), updatedCluster.Status.Phase)
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
	result, err := controller.Reconcile(context.TODO(), request)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorkDriverName().Return("mock", nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// Reconcile should not fail on stork install failure. Only event should be raised.
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(fmt.Errorf("preinstall error"))
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.Contains(t, err.Error(), "preinstall error")
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	actualEvent := <-recorder.Events
	require.Contains(t, actualEvent,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
	require.Contains(t, actualEvent, "preinstall error")
}

func TestStorageClusterFailedSyncObjectModified(t *testing.T) {
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).Return(fmt.Errorf(k8s.UpdateRevisionConflictErr))
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	// When failed to sync StorageCluster due to object modified, don't raise an event
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, recorder.Events, 0)
}

func TestStoragePodsShouldNotBeScheduledIfDisabled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Annotations = map[string]string{
		constants.AnnotationDisableStorage: "true",
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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

func getDefaultNodeAffinity(k8sVersion *version.Version) *v1.NodeAffinity {
	var nodeSelectorTerms []v1.NodeSelectorTerm
	requirements1 := []v1.NodeSelectorRequirement{
		{
			Key:      "px/enabled",
			Operator: v1.NodeSelectorOpNotIn,
			Values:   []string{"false"},
		},
		{
			Key:      k8s.NodeRoleLabelMaster,
			Operator: v1.NodeSelectorOpDoesNotExist,
		},
	}
	if k8sVersion.GreaterThanOrEqual(k8s.K8sVer1_24) {
		requirements1 = append(requirements1, v1.NodeSelectorRequirement{
			Key:      k8s.NodeRoleLabelControlPlane,
			Operator: v1.NodeSelectorOpDoesNotExist,
		})
	}
	nodeSelectorTerms = append(nodeSelectorTerms, v1.NodeSelectorTerm{
		MatchExpressions: requirements1,
	})

	requirements2 := []v1.NodeSelectorRequirement{
		{
			Key:      "px/enabled",
			Operator: v1.NodeSelectorOpNotIn,
			Values:   []string{"false"},
		},
		{
			Key:      k8s.NodeRoleLabelMaster,
			Operator: v1.NodeSelectorOpExists,
		},
		{
			Key:      k8s.NodeRoleLabelWorker,
			Operator: v1.NodeSelectorOpExists,
		},
	}
	nodeSelectorTerms = append(nodeSelectorTerms, v1.NodeSelectorTerm{
		MatchExpressions: requirements2,
	})

	if k8sVersion.GreaterThanOrEqual(k8s.K8sVer1_24) {
		requirements3 := []v1.NodeSelectorRequirement{
			{
				Key:      "px/enabled",
				Operator: v1.NodeSelectorOpNotIn,
				Values:   []string{"false"},
			},
			{
				Key:      k8s.NodeRoleLabelControlPlane,
				Operator: v1.NodeSelectorOpExists,
			},
			{
				Key:      k8s.NodeRoleLabelWorker,
				Operator: v1.NodeSelectorOpExists,
			},
		}
		nodeSelectorTerms = append(nodeSelectorTerms, v1.NodeSelectorTerm{
			MatchExpressions: requirements3,
		})

	}

	return &v1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
			NodeSelectorTerms: nodeSelectorTerms,
		},
	}
}

func TestStoragePodGetsScheduled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Placement.NodeAffinity = getDefaultNodeAffinity(k8sVersion)
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode1.Labels["node-role.kubernetes.io/worker"] = ""

	// This node is labeled as master and worker, storage pod will be scheduled on it.
	k8sNode2 := createK8sNode("k8s-node-2", 1)
	k8sNode2.Labels["node-role.kubernetes.io/master"] = ""
	k8sNode2.Labels["node-role.kubernetes.io/worker"] = ""

	// This node is labeled as master, storage pod will not be scheduled on it.
	k8sNode3 := createK8sNode("k8s-node-3", 1)
	k8sNode3.Labels["node-role.kubernetes.io/master"] = ""

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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	expectedPodSpec := v1.PodSpec{
		Containers: []v1.Container{{Name: "test"}},
	}
	k8s.AddOrUpdateStoragePodTolerations(&expectedPodSpec)
	expectedPodSpec.Affinity = &v1.Affinity{
		NodeAffinity: getDefaultNodeAffinity(k8sVersion),
	}
	expectedPodTemplate := &v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Labels: map[string]string{
				constants.LabelKeyClusterName:       cluster.Name,
				constants.LabelKeyDriverName:        driverName,
				constants.OperatorLabelManagedByKey: constants.OperatorLabelManagedByValue,
			},
			Annotations: make(map[string]string),
		},
		Spec: expectedPodSpec,
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(c *corev1.StorageCluster) {
			hash := computeHash(&c.Spec, nil)
			expectedPodTemplate.Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
		})
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).
		Return(expectedPodSpec, nil).
		AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	podTemplate1 := expectedPodTemplate.DeepCopy()
	podTemplate1.Spec.NodeName = "k8s-node-1"
	podTemplate2 := expectedPodTemplate.DeepCopy()
	podTemplate2.Spec.NodeName = "k8s-node-2"
	expectedPodTemplates := []v1.PodTemplateSpec{
		*podTemplate1, *podTemplate2,
	}
	expectedPodTemplates[0].Annotations["operator.libopenstorage.org/node-labels"] =
		"{\"node-role.kubernetes.io/worker\":\"\"}"
	expectedPodTemplates[1].Annotations["operator.libopenstorage.org/node-labels"] =
		"{\"node-role.kubernetes.io/master\":\"\",\"node-role.kubernetes.io/worker\":\"\"}"
	require.ElementsMatch(t, expectedPodTemplates, podControl.Templates)
	require.Len(t, podControl.ControllerRefs, 2)
	require.Equal(t, *clusterRef, podControl.ControllerRefs[0])
	require.ElementsMatch(t,
		[]string{"k8s-node-1", "k8s-node-2"},
		[]string{podControl.Templates[0].Spec.NodeName, podControl.Templates[1].Spec.NodeName})
	require.Equal(t, *clusterRef, podControl.ControllerRefs[1])
}

func TestStoragePodGetsScheduledK8s1_24(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	k8sVersion := k8s.K8sVer1_24
	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Placement.NodeAffinity = getDefaultNodeAffinity(k8sVersion)
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode1.Labels["node-role.kubernetes.io/worker"] = ""

	// This node is labeled as control-plane and worker, storage pod will be scheduled on it.
	k8sNode2 := createK8sNode("k8s-node-2", 1)
	k8sNode2.Labels["node-role.kubernetes.io/control-plane"] = ""
	k8sNode2.Labels["node-role.kubernetes.io/worker"] = ""

	// This node is labled as control-plane, storage pod will not be scheduled on it.
	k8sNode3 := createK8sNode("k8s-node-3", 1)
	k8sNode3.Labels["node-role.kubernetes.io/control-plane"] = ""

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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	expectedPodSpec := v1.PodSpec{
		Containers: []v1.Container{{Name: "test"}},
	}
	k8s.AddOrUpdateStoragePodTolerations(&expectedPodSpec)
	expectedPodSpec.Affinity = &v1.Affinity{
		NodeAffinity: getDefaultNodeAffinity(k8sVersion),
	}
	expectedPodTemplate := &v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Labels: map[string]string{
				constants.LabelKeyClusterName:       cluster.Name,
				constants.LabelKeyDriverName:        driverName,
				constants.OperatorLabelManagedByKey: constants.OperatorLabelManagedByValue,
			},
			Annotations: make(map[string]string),
		},
		Spec: expectedPodSpec,
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(c *corev1.StorageCluster) {
			hash := computeHash(&c.Spec, nil)
			expectedPodTemplate.Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
		})
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).
		Return(expectedPodSpec, nil).
		AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	podTemplate1 := expectedPodTemplate.DeepCopy()
	podTemplate1.Spec.NodeName = "k8s-node-1"
	podTemplate2 := expectedPodTemplate.DeepCopy()
	podTemplate2.Spec.NodeName = "k8s-node-2"
	expectedPodTemplates := []v1.PodTemplateSpec{
		*podTemplate1, *podTemplate2,
	}
	expectedPodTemplates[0].Annotations["operator.libopenstorage.org/node-labels"] = "{\"node-role.kubernetes.io/worker\":\"\"}"
	expectedPodTemplates[1].Annotations["operator.libopenstorage.org/node-labels"] = "{\"node-role.kubernetes.io/control-plane\":\"\",\"node-role.kubernetes.io/worker\":\"\"}"
	require.ElementsMatch(t, expectedPodTemplates, podControl.Templates)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	clusterRef := metav1.NewControllerRef(cluster, controllerKind)
	storageLabels := map[string]string{"foo": "bar"}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(storageLabels).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	expectedStorageNode1 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            k8sNode1.Name,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
			Labels:          storageLabels,
			ResourceVersion: "2",
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
		},
	}
	expectedStorageNode2 := expectedStorageNode1.DeepCopy()
	expectedStorageNode2.Name = k8sNode2.Name
	expectedStorageNodes := []corev1.StorageNode{*expectedStorageNode1, *expectedStorageNode2}

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.ElementsMatch(t,
		expectedStorageNodes,
		storageNodes.Items,
	)

	// TestCase: Recreating the pods should not affect the created storage nodes
	pods := &v1.PodList{}
	err = testutil.List(k8sClient, pods)
	require.NoError(t, err)
	require.Empty(t, pods.Items)

	storageNodes.Items[0].Status.Phase = string(corev1.NodeOnlineStatus)
	err = k8sClient.Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 2)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNodes.Items[0].Status.Phase)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[1].Status.Phase)

	// TestCase: Should recreate the storage nodes when re-creating pods
	pods = &v1.PodList{}
	err = testutil.List(k8sClient, pods)
	require.NoError(t, err)
	require.Empty(t, pods.Items)

	err = k8sClient.Delete(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)
	err = k8sClient.Delete(context.TODO(), &storageNodes.Items[1])
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	storageNodes = &corev1.StorageNodeList{}
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
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll: &useAllDevices,
	}
	cluster.Spec.Network = &corev1.NetworkSpec{
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
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			// Match using node name
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node1",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: stringSlicePtr([]string{"dev1"}),
				},
				Network: &corev1.NetworkSpec{
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
			// CloudStorage: &corev1.CloudStorageNodeSpec{
			// 	CloudStorageCommon: corev1.CloudStorageCommon{
			// 		DeviceSpecs: stringSlicePtr([]string{"type=dev1"}),
			// 	},
			// },
		},
		{
			// Match using a label selector
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node2",
					},
				},
			},
		},
		{
			// Even though the labels match a valid node, if the node has already
			// matched a previous spec, then this spec will not be used by that node.
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node2",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Even though the node name matches a valid node, if the node has already
			// matched a previous spec, then this spec will not be used by that node.
			Selector: corev1.NodeSelector{
				NodeName: "k8s-node-2",
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Even though the labels match a valid node, if the node has already
			// matched a previous spec, then this spec will not be used by that node.
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "node1",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Selector with node name that does not exist. No pod should
			// be deployed with this configuration
			Selector: corev1.NodeSelector{
				NodeName: "non-existent-node",
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Selector with requirements that do not match any node. No pod
			// should be deployed with this configuration
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "not-matching-label",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: stringSlicePtr([]string{"unused"}),
				},
			},
		},
		{
			// Selector with invalid requirements. No pod should be
			// deployed with this configuration
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "test",
							Operator: "InvalidOperator",
						},
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	expectedPodSpec := v1.PodSpec{
		Containers: []v1.Container{{Name: "test"}},
	}
	k8s.AddOrUpdateStoragePodTolerations(&expectedPodSpec)
	expectedPodTemplate := &v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Labels: map[string]string{
				constants.LabelKeyClusterName:       cluster.Name,
				constants.LabelKeyDriverName:        driverName,
				constants.OperatorLabelManagedByKey: constants.OperatorLabelManagedByValue,
			},
		},
		Spec: expectedPodSpec,
	}
	podTemplate1 := expectedPodTemplate.DeepCopy()
	podTemplate1.Spec.NodeName = "k8s-node-1"
	podTemplate2 := expectedPodTemplate.DeepCopy()
	podTemplate2.Spec.NodeName = "k8s-node-2"
	podTemplate3 := expectedPodTemplate.DeepCopy()
	podTemplate3.Spec.NodeName = "k8s-node-3"
	expectedPodTemplates := []v1.PodTemplateSpec{
		*podTemplate1, *podTemplate2, *podTemplate3,
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).
		Do(func(c *corev1.StorageCluster) {
			hash := computeHash(&c.Spec, nil)
			expectedPodTemplates[0].Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
			expectedPodTemplates[1].Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
			expectedPodTemplates[2].Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
		})
	gomock.InOrder(
		driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-1").
			DoAndReturn(func(c *corev1.StorageCluster, _ string) (v1.PodSpec, error) {
				require.Equal(t, cluster.Spec.Nodes[0].Storage, c.Spec.Storage)
				// require.Equal(t, cluster.Spec.Nodes[0].CloudStorage.CloudStorageCommon,
				// c.Spec.CloudStorage.CloudStorageCommon)
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
				expectedPodTemplates[0].Annotations = map[string]string{constants.AnnotationNodeLabels: string(nodeLabels)}
				return expectedPodSpec, nil
			}).
			Times(1),
		driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-2").
			DoAndReturn(func(c *corev1.StorageCluster, _ string) (v1.PodSpec, error) {
				require.Empty(t, cluster.Spec.Nodes[1].CommonConfig, c.Spec.CommonConfig)
				nodeLabels, _ := json.Marshal(k8sNode2.Labels)
				expectedPodTemplates[1].Annotations = map[string]string{constants.AnnotationNodeLabels: string(nodeLabels)}
				return expectedPodSpec, nil
			}).
			Times(1),
		driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-3").
			DoAndReturn(func(c *corev1.StorageCluster, _ string) (v1.PodSpec, error) {
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
	result, err := controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes nodes with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash

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

	err = k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), failedPod)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), deletedPod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	maxUnavailable := intstr.FromInt(0)
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		RollingUpdate: &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	affinity := &v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchFields: []v1.NodeSelectorRequirement{
							{
								Key:      metav1.ObjectNameField,
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
	readyCondition := v1.PodCondition{
		Type:   v1.PodReady,
		Status: v1.ConditionTrue,
	}
	unscheduledPod1 := createStoragePod(cluster, "unscheduled-pod-1", "", storageLabels)
	unscheduledPod1.CreationTimestamp = metav1.Now()
	unscheduledPod1.Spec.Affinity = affinity
	unscheduledPod1.Status.Conditions = []v1.PodCondition{readyCondition}

	creationTimestamp := metav1.NewTime(time.Now().Add(1 * time.Minute))
	runningPod1 := createStoragePod(cluster, "running-pod-1", k8sNode.Name, storageLabels)
	runningPod1.CreationTimestamp = creationTimestamp
	runningPod1.Status.Conditions = []v1.PodCondition{readyCondition}
	runningPod2 := createStoragePod(cluster, "running-pod-2", k8sNode.Name, storageLabels)
	runningPod2.CreationTimestamp = creationTimestamp
	runningPod2.Status.Conditions = []v1.PodCondition{readyCondition}

	unscheduledPod2 := createStoragePod(cluster, "unscheduled-pod-2", "", storageLabels)
	unscheduledPod2.CreationTimestamp = metav1.NewTime(time.Now().Add(2 * time.Minute))
	unscheduledPod2.Spec.Affinity = affinity
	unscheduledPod2.Status.Conditions = []v1.PodCondition{readyCondition}

	extraRunningPod := createStoragePod(cluster, "extra-running-pod", k8sNode.Name, storageLabels)
	extraRunningPod.CreationTimestamp = metav1.NewTime(time.Now().Add(3 * time.Minute))
	extraRunningPod.Status.Conditions = []v1.PodCondition{readyCondition}

	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), unscheduledPod1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), unscheduledPod2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), extraRunningPod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
		constants.AnnotationDisableStorage: "1",
	}
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod := createStoragePod(cluster, "running-pod", k8sNode.Name, storageLabels)

	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Verify there is no event raised for the extra pods
	require.Empty(t, recorder.Events)

	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{runningPod.Name}, podControl.DeletePodName)
}

func TestStoragePodFailureDueToNodeSelectorNotMatch(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.Placement = &corev1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchFields: []v1.NodeSelectorRequirement{
							{
								Key:      metav1.ObjectNameField,
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"k8s-node-1"},
							},
							{
								Key:      metav1.ObjectNameField,
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"k8s-node-2"},
							},
							{
								Key:      metav1.ObjectNameField,
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"k8s-node-3"},
							},
						},
					},
				},
			},
		},
	}
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

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
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod := createStoragePod(cluster, "running-pod", k8sNode3.Name, storageLabels)

	err = k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	cluster.Spec.Placement = &corev1.PlacementSpec{
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).Times(3)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).Times(3)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).Times(3)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).Times(3)
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

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
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod1 := createStoragePod(cluster, "running-pod-1", k8sNode1.Name, storageLabels)
	runningPod2 := createStoragePod(cluster, "running-pod-2", k8sNode2.Name, storageLabels)
	runningPod3 := createStoragePod(cluster, "running-pod-3", k8sNode3.Name, storageLabels)

	err = k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), runningPod3)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// No pods should be deleted as they have tolerations for the node taints
	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.Empty(t, podControl.DeletePodName)

	// Case: Remove tolerations and the pods on nodes with NoExecute should be removed.
	// Pods on nodes with NoSchedule should NOT be removed
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Placement = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{runningPod1.Name}, podControl.DeletePodName)

	// Case: Delete a pod lacking NoSchedule toleration and it should not be started again
	err = k8sClient.Delete(context.TODO(), runningPod2)
	require.NoError(t, err)
	err = k8sClient.Delete(context.TODO(), runningPod1)
	require.NoError(t, err)
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll: &useAllDevices,
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-1").
		Return(v1.PodSpec{}, fmt.Errorf("pod template error for k8s-node-1")).
		Times(1)
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pod template error for k8s-node-1")
	require.Empty(t, result)

	existingCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, existingCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), existingCluster.Status.Phase)

	// Verify there is event raised for failure to create pod templates for node spec
	require.Len(t, recorder.Events, 1)
	eventMsg := <-recorder.Events
	require.Contains(t, eventMsg, fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
	require.Contains(t, eventMsg, "pod template error for k8s-node-1")

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	// When pod template creation passes for some and fails for others
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-1").
		Return(v1.PodSpec{}, nil).
		Times(1)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), "k8s-node-2").
		Return(v1.PodSpec{}, fmt.Errorf("pod template error for k8s-node-2")).
		Times(1)
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	result, err = controller.Reconcile(context.TODO(), request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pod template error for k8s-node-2")
	require.Empty(t, result)

	existingCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, existingCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), existingCluster.Status.Phase)

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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	failedPod := createStoragePod(cluster, "failed-pod", k8sNode1.Name, storageLabels)
	failedPod.Status = v1.PodStatus{
		Phase: v1.PodFailed,
	}

	err = k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), failedPod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pod control error")
	require.Empty(t, result)

	existingCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, existingCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), existingCluster.Status.Phase)

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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Verify there is no error or event raised for timeout errors
	// during pod creation
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	driver.EXPECT().
		UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).
		Do(func(c *corev1.StorageCluster, hash string) {
			c.Status.Phase = string(corev1.ClusterStateRunning)
			util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
				Source: pxutil.PortworxComponentName,
				Type:   corev1.ClusterConditionTypeRuntimeState,
				Status: corev1.ClusterConditionStatusOnline,
			})
		}).
		Return(nil)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 0)

	newCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, newCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), newCluster.Status.Phase)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	driver.EXPECT().
		UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).
		Do(func(c *corev1.StorageCluster, hash string) {
			c.Status.Phase = string(corev1.ClusterStateDegraded)
		}).
		Return(fmt.Errorf("update status error"))

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Equal(t, <-recorder.Events,
		fmt.Sprintf("%v %v update status error", v1.EventTypeWarning, util.FailedSyncReason))

	newCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, newCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), newCluster.Status.Phase)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).AnyTimes()
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
	result, err := controller.Reconcile(context.TODO(), request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pre-install error")
	require.Empty(t, result)

	existingCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, existingCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), existingCluster.Status.Phase)

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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	expectedDriverInfo := &storage.UpdateDriverInfo{
		ZoneToInstancesMap: map[string]uint64{
			"z1": 2,
			"z2": 1,
		},
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(expectedDriverInfo).Return(nil)
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	// Should contain cloud provider information if any node has it
	k8sNode2.Spec.ProviderID = "invalid"
	err = k8sClient.Update(context.TODO(), k8sNode2)
	require.NoError(t, err)
	k8sNode3.Spec.ProviderID = "testcloud://test-instance-id"
	err = k8sClient.Update(context.TODO(), k8sNode3)
	require.NoError(t, err)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(k8sNode1, k8sNode2, k8sNode3)))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	expectedDriverInfo.CloudProvider = "testcloud"
	driver.EXPECT().UpdateDriver(expectedDriverInfo).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	// Should not fail reconcile even if there is an error on UpdateDriver
	driver.EXPECT().UpdateDriver(expectedDriverInfo).Return(fmt.Errorf("update error"))

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)
}

func TestGarbageCollection(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := createStorageCluster()

	// Configmaps created by px are expected to be deleted https://portworx.atlassian.net/browse/OPERATOR-752
	configMaps := []v1.ConfigMap{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "px-attach-driveset-lock",
				Namespace: cluster.Namespace,
			},
			Data: map[string]string{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "px-attach-driveset-lock",
				Namespace: "kube-system",
			},
			Data: map[string]string{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "px-bringup-queue-lockb",
				Namespace: cluster.Namespace,
			},
			Data: map[string]string{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "px-bringup-queue-locka",
				Namespace: "kube-system",
			},
			Data: map[string]string{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cm1",
				Namespace: cluster.Namespace,
				Annotations: map[string]string{
					"operator.libopenstorage.org/garbage-collection": "true",
				},
			},
			Data: map[string]string{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cm2",
				Namespace: "kube-system",
				Annotations: map[string]string{
					"operator.libopenstorage.org/garbage-collection": "true",
				},
			},
			Data: map[string]string{},
		},
	}
	nonGCConfigMaps := []v1.ConfigMap{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "a",
				Namespace: cluster.Namespace,
			},
			Data: map[string]string{},
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	for _, cm := range configMaps {
		err := k8sClient.Create(context.TODO(), &cm)
		require.NoError(t, err)
	}
	for _, cm := range nonGCConfigMaps {
		err := k8sClient.Create(context.TODO(), &cm)
		require.NoError(t, err)
	}

	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("pxd").AnyTimes()
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	err := testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	condition := &corev1.ClusterCondition{
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusCompleted,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil).AnyTimes()

	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	for _, cm := range configMaps {
		obj := &v1.ConfigMap{}
		err = k8sClient.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      cm.Name,
				Namespace: cm.Namespace,
			},
			obj,
		)
		require.True(t, errors.IsNotFound(err))
	}
	for _, cm := range nonGCConfigMaps {
		obj := &v1.ConfigMap{}
		err = k8sClient.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      cm.Name,
				Namespace: cm.Namespace,
			},
			obj,
		)
		require.NoError(t, err)
	}
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
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

	err := k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storagePod1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storagePod2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storagePod3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storagePod4)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	// Empty delete condition should not remove finalizer
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(nil, nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)

	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storagePod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{storagePod.Name}, podControl.DeletePodName)

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, updatedCluster.Status.Conditions, 1)
	require.Equal(t, pxutil.PortworxComponentName, updatedCluster.Status.Conditions[0].Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, updatedCluster.Status.Conditions[0].Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, updatedCluster.Status.Conditions[0].Status)
	require.Equal(t, string(corev1.ClusterStateUninstall), updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If storage driver returns error, then controller should not return error but raise an event
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(nil, fmt.Errorf("delete error"))

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	raisedEvent := <-recorder.Events
	require.Contains(t, raisedEvent,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
	require.Contains(t, raisedEvent, "delete error")

	updatedCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, updatedCluster.Status.Conditions, 1)
	require.Equal(t, pxutil.PortworxComponentName, updatedCluster.Status.Conditions[0].Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, updatedCluster.Status.Conditions[0].Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, updatedCluster.Status.Conditions[0].Status)
	require.Equal(t, string(corev1.ClusterStateUninstall), updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition is not present already, then add to the cluster
	updatedCluster.Status.Conditions = []corev1.ClusterCondition{
		{
			Type: corev1.ClusterConditionTypeInstall,
		},
	}
	err = k8sClient.Update(context.TODO(), updatedCluster)
	require.NoError(t, err)
	condition := &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusFailed,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, updatedCluster.Status.Conditions, 2)
	c := util.GetStorageClusterCondition(updatedCluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeDelete)
	require.NotNil(t, condition)
	require.Equal(t, condition.Status, c.Status)
	require.Equal(t, string(corev1.ClusterStateUninstall), updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition is present, then update it
	condition = &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusTimeout,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, updatedCluster.Status.Conditions, 2)
	c = util.GetStorageClusterCondition(updatedCluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeDelete)
	require.NotNil(t, condition)
	require.Equal(t, condition.Status, c.Status)
	require.Equal(t, string(corev1.ClusterStateUninstall), updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition status is completed, then remove delete finalizer
	condition = &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusCompleted,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestDeleteStorageClusterShouldSetTelemetryCertOwnerRef(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := createStorageCluster()
	cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
		Type: corev1.UninstallAndWipeStorageClusterStrategyType,
	}
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp

	driverName := "mock-driver"
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	// Empty delete condition should not remove finalizer
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(nil, nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)

	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storagePod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{storagePod.Name}, podControl.DeletePodName)

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, updatedCluster.Status.Conditions, 1)
	require.Equal(t, pxutil.PortworxComponentName, updatedCluster.Status.Conditions[0].Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, updatedCluster.Status.Conditions[0].Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, updatedCluster.Status.Conditions[0].Status)
	require.Equal(t, string(corev1.ClusterStateUninstall), updatedCluster.Status.Phase)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	condition := &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusCompleted,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)

	telemetryCert := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.TelemetryCertName,
			Namespace: cluster.Namespace,
		},
	}
	err = k8sClient.Create(context.TODO(), telemetryCert)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	secret := &v1.Secret{}
	err = testutil.Get(k8sClient, secret, pxutil.TelemetryCertName, cluster.Namespace)
	require.NoError(t, err)

	require.Equal(t, len(secret.OwnerReferences), 1)
}

func TestDeleteStorageClusterShouldDeleteStork(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("pxd").AnyTimes()
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceAccountList.Items)

	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.NotEmpty(t, clusterRoleList.Items)

	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.NotEmpty(t, crbList.Items)

	configMapList := &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMapList)
	require.NoError(t, err)
	require.NotEmpty(t, configMapList.Items)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceList.Items)

	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.NotEmpty(t, deploymentList.Items)

	// On deleting the storage cluster, stork specs should
	// also get removed
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	condition := &corev1.ClusterCondition{
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusCompleted,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil).AnyTimes()

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Empty(t, serviceAccountList.Items)

	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Empty(t, clusterRoleList.Items)

	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Empty(t, crbList.Items)

	err = testutil.List(k8sClient, configMapList)
	require.NoError(t, err)
	require.Empty(t, configMapList.Items)

	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)

	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Empty(t, deploymentList.Items)
}

func TestDeleteStorageClusterShouldRemoveMigrationLabels(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := createStorageCluster()
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp

	driverName := "mock-driver"
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	condition := &corev1.ClusterCondition{
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusCompleted,
	}
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	// Migration done on node1
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode1.Labels[constants.LabelPortworxDaemonsetMigration] = constants.LabelValueMigrationDone
	err := k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	storagePod1 := createStoragePod(cluster, "pod-1", k8sNode1.Name, storageLabels)
	err = k8sClient.Create(context.TODO(), storagePod1)
	require.NoError(t, err)

	// Migration skipped/pending on node2/node3
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode2.Labels[constants.LabelPortworxDaemonsetMigration] = constants.LabelValueMigrationSkip
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	k8sNode3 := createK8sNode("k8s-node-3", 10)
	k8sNode3.Labels[constants.LabelPortworxDaemonsetMigration] = constants.LabelValueMigrationPending
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	nodeList := &v1.NodeList{}
	err = k8sClient.List(context.TODO(), nodeList, &client.ListOptions{})
	require.NoError(t, err)
	require.Len(t, nodeList.Items, 3)
	for _, node := range nodeList.Items {
		_, ok := node.Labels[constants.LabelPortworxDaemonsetMigration]
		require.False(t, ok)
	}
}

func TestRollingUpdateWithMinReadySeconds(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.UpdateStrategy.RollingUpdate.MinReadySeconds = 5
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	require.Equal(t, revisions.Items[0].Labels[util.DefaultStorageClusterUniqueLabelKey],
		podControl.Templates[0].Labels[util.DefaultStorageClusterUniqueLabelKey])

	// Test case: Changing the cluster spec -
	// A new revision should be created for the new cluster spec
	// Also the pod should be changed with the updated spec
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "new/image"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	oldPod.Status.Conditions = append(oldPod.Status.Conditions, v1.PodCondition{
		Type:               v1.PodReady,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	})
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	// Reconcile should not start rolling update as the pod was created less than minReadySeconds
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, podControl.DeletePodName, 0)

	time.Sleep(time.Second * time.Duration(cluster.Spec.UpdateStrategy.RollingUpdate.MinReadySeconds+1))

	// Reconcile should start rolling update
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion
	require.Empty(t, podControl.Templates)
	require.Empty(t, podControl.ControllerRefs)
	require.Len(t, podControl.DeletePodName, 1)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	require.Equal(t, revisions.Items[0].Labels[util.DefaultStorageClusterUniqueLabelKey],
		podControl.Templates[0].Labels[util.DefaultStorageClusterUniqueLabelKey])

	// Test case: Changing the cluster spec -
	// A new revision should be created for the new cluster spec
	// Also the pod should be changed with the updated spec
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "new/image"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	oldPod.Status.Conditions = append(oldPod.Status.Conditions, v1.PodCondition{
		Type:   v1.PodReady,
		Status: v1.ConditionTrue,
	})
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// New revision should be created for the updated cluster spec
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)

	// validate revision 1 and revision 2 exist
	require.ElementsMatch(t, []int64{1, 2}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})

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

	result, err = controller.Reconcile(context.TODO(), request)
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

	require.Len(t, podControl.Templates, 1)
	// validate revision 1 and revision 2 exist
	require.ElementsMatch(t, []int64{1, 2}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})

	// New revision's hash should match that of the new pod.
	var revision2 *appsv1.ControllerRevision = nil
	for i, rev := range revisions.Items {
		if rev.Revision == 2 {
			revision2 = &revisions.Items[i]
		}
	}
	require.Equal(t, revision2.Labels[util.DefaultStorageClusterUniqueLabelKey],
		podControl.Templates[0].Labels[util.DefaultStorageClusterUniqueLabelKey])
}

// When any storage node is unhealthy, we should not upgrade storage pod as it may cause
// storage cluster to lose quorum. The unhealthy storage node could be running on -
// - a Kubernetes node
// - a Kubernetes node where it is not supposed to run now (exluded later using taints, node affinity, etc)
// - outside the Kubernetes cluster
// These scenarios may happen if the user removes a node from Kubernetes for maintenance,
// and meanwhile tries to upgrade the storage cluster.
func TestUpdateStorageClusterBasedOnStorageNodeStatuses(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	var storageNodes []*storageapi.StorageNode
	storageNodes = append(storageNodes, createStorageNode("k8s-node-0", true))
	storageNodes = append(storageNodes, createStorageNode("k8s-node-1", true))
	storageNodes = append(storageNodes, createStorageNode("k8s-node-2", true))
	storageNodes = append(storageNodes, createStorageNode("not-k8s-node", false))

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(storageNodes, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash

	// Kubernetes node with enough resources to create new pods
	for i := 0; i < 3; i++ {
		k8sNode := createK8sNode(fmt.Sprintf("k8s-node-%d", i), 10)
		err = k8sClient.Create(context.TODO(), k8sNode)
		require.NoError(t, err)

		storagePod := createStoragePod(cluster, fmt.Sprintf("storage-pod-%d", i), k8sNode.Name, storageLabels)
		storagePod.Status.Conditions = []v1.PodCondition{
			{
				Type:   v1.PodReady,
				Status: v1.ConditionTrue,
			},
		}
		err = k8sClient.Create(context.TODO(), storagePod)
		require.NoError(t, err)
	}

	// TestCase: Change image pull secret to trigger portworx updates.
	// No update should happen as there is one unhealthy storage node.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.ImagePullSecret = stringPtr("pull-secret")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should not be marked for deletion.
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Mark the unhealthy storage node to healthy, the update should begin.
	storageNodes[3].Status = storageapi.Status_STATUS_OK
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod be marked for deletion.
	require.NotEmpty(t, podControl.DeletePodName)

	// TestCase: Storage node 0, which is also a k8s node, is unhealthy,
	// no storage pod should be upgraded.
	storageNodes[0].Status = storageapi.Status_STATUS_ERROR

	// Reset the test for upgrade.
	podControl.DeletePodName = []string{}
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.ImagePullSecret = stringPtr("updated-pull-secret")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, podControl.DeletePodName)

	// TestCase: K8s node should not run storage pod, but unhealthy storage node exists,
	// without any pod - no storage pod should be upgraded.
	storageNodes[0].Status = storageapi.Status_STATUS_OK
	storageNodes[3].SchedulerNodeName = "k8s-node-4"
	storageNodes[3].Status = storageapi.Status_STATUS_ERROR

	k8sNode4 := createK8sNode("k8s-node-4", 10)
	k8sNode4.Spec.Taints = []v1.Taint{
		{
			Key:    "key",
			Effect: v1.TaintEffectNoExecute,
		},
	}
	err = k8sClient.Create(context.TODO(), k8sNode4)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, podControl.DeletePodName)

	// TestCase: K8s node should not run storage pod, but storage is disabled,
	// so we cannot get the storage node information - upgrade should continue.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Annotations = map[string]string{constants.AnnotationDisableStorage: "true"}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.NotEmpty(t, podControl.DeletePodName)

	// TestCase: There is a k8s node should not run storage pod, but healthy storage node exists,
	// without any pod - upgrade should continue.
	storageNodes[3].Status = storageapi.Status_STATUS_OK

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion.
	require.NotEmpty(t, podControl.DeletePodName)

	// TestCase: There is a k8s node should not run storage pod, but healthy storage node exists,
	// with ready pod - upgrade should continue
	storagePod := createStoragePod(cluster, "storage-pod-4", "k8s-node-4", storageLabels)
	storagePod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), storagePod)
	require.NoError(t, err)

	// Reset the test for upgrade
	podControl.DeletePodName = []string{}

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion.
	require.NotEmpty(t, podControl.DeletePodName)

	// TestCase: There is a k8s node should not run storage pod, but healthy storage node exists,
	// with not ready pod - upgrade should continue
	storagePod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionFalse,
		},
	}
	err = k8sClient.Update(context.TODO(), storagePod)
	require.NoError(t, err)

	// Reset the test for upgrade
	podControl.DeletePodName = []string{}

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should not be marked for deletion.
	require.NotEmpty(t, podControl.DeletePodName)
}

func TestUpdateStorageClusterWithOpenshiftUpgrade(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cv := &ocp_configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
		Spec: ocp_configv1.ClusterVersionSpec{
			DesiredUpdate: &ocp_configv1.Update{
				Version: "1.2.3",
			},
		},
		Status: ocp_configv1.ClusterVersionStatus{
			History: []ocp_configv1.UpdateHistory{
				{
					Version: "1.2.3",
					State:   ocp_configv1.PartialUpdate,
				},
			},
		},
	}

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, cv)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		podControl:        podControl,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	require.Equal(t, revisions.Items[0].Labels[util.DefaultStorageClusterUniqueLabelKey],
		podControl.Templates[0].Labels[util.DefaultStorageClusterUniqueLabelKey])

	// TestCase: Changing the cluster spec -
	// A new revision should be created for the new cluster spec
	// But pod should not be changed as Openshift upgrade is in progress
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "new/image"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	oldPod.Status.Conditions = append(oldPod.Status.Conditions, v1.PodCondition{
		Type:   v1.PodReady,
		Status: v1.ConditionTrue,
	})
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeNormal, util.UpdatePausedReason))

	// The old pod should NOT be marked for deletion as an OpenShift
	// upgrade is in progress
	require.Empty(t, podControl.DeletePodName)

	// New revision should still be created for the updated cluster spec
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)

	// validate revision 1 and revision 2 exist
	require.ElementsMatch(t, []int64{1, 2}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})

	// TestCase: Continue upgrade if forced using annotation
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Annotations = map[string]string{
		constants.AnnotationForceContinueUpdate: "true",
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, recorder.Events)

	// The old pod should be marked for deletion
	require.Len(t, podControl.DeletePodName, 1)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
	require.Empty(t, podControl.Templates)
	require.Empty(t, podControl.ControllerRefs)
}

func TestUpdateStorageClusterShouldNotExceedMaxUnavailable(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)
	k8sNode4 := createK8sNode("k8s-node-4", 10)
	err := k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode4)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
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

	err = k8sClient.Create(context.TODO(), oldPod1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), oldPod2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), oldPod3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), oldPod4)
	require.NoError(t, err)

	// Should delete pods only up to maxUnavailable value. In this case - 2 pods
	maxUnavailable := intstr.FromInt(2)
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type: corev1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	cluster.Spec.Image = "test/image:v2"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Len(t, podControl.DeletePodName, 2)
	require.Empty(t, podControl.Templates)

	// Next reconcile loop should not delete more pods as 2 are already down.
	// If a pod is marked for deletion but not actually deleted do not send it
	// for deletion again.
	deletedPods := make([]*v1.Pod, 0)
	deletedPod1 := &v1.Pod{}
	err = testutil.Get(k8sClient, deletedPod1, podControl.DeletePodName[0], cluster.Namespace)
	require.NoError(t, err)
	deletedPods = append(deletedPods, deletedPod1)
	err = k8sClient.Delete(context.TODO(), deletedPod1)
	require.NoError(t, err)

	deletedPod2 := &v1.Pod{}
	err = testutil.Get(k8sClient, deletedPod2, podControl.DeletePodName[1], cluster.Namespace)
	require.NoError(t, err)
	deletionTimestamp := metav1.Now()
	deletedPod2.DeletionTimestamp = &deletionTimestamp
	deletedPod2.Status.Conditions[0].Status = v1.ConditionFalse
	err = k8sClient.Update(context.TODO(), deletedPod2)
	require.NoError(t, err)
	deletedPods = append(deletedPods, deletedPod2)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)
	// Both pods are deleted
	require.Len(t, podControl.Templates, 2)

	// If the pods are created but not ready, even then no extra pod should be deleted
	replacedPod1, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	replacedPod1.Name = replacedPod1.GenerateName + "replaced-1"
	replacedPod1.Namespace = cluster.Namespace
	replacedPod1.Spec.NodeName = deletedPods[0].Spec.NodeName
	err = k8sClient.Create(context.TODO(), replacedPod1)
	require.NoError(t, err)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)
	require.Len(t, podControl.Templates, 1)

	// If another update happens to storage cluster, we should account for non-running
	// and non-ready pods in max unavailable pods. Also we should delete an old not ready
	// pod before a running one.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v3"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{replacedPod1.Name}, podControl.DeletePodName)
	require.Len(t, podControl.Templates, 1)

	// Once the new pods are up and in ready state, we should delete remaining
	// pods with older versions.
	err = k8sClient.Delete(context.TODO(), replacedPod1)
	require.NoError(t, err)

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
	err = k8sClient.Create(context.TODO(), replacedPod2)
	require.NoError(t, err)

	replacedPod3 := replacedPod2.DeepCopy()
	replacedPod3.Name = replacedPod2.GenerateName + "replaced-3"
	replacedPod3.Spec.NodeName = deletedPods[1].Spec.NodeName
	replacedPod3.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), replacedPod3)
	require.NoError(t, err)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)
	k8sNode4 := createK8sNode("k8s-node-4", 10)
	err := k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode4)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
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

	err = k8sClient.Create(context.TODO(), oldPod1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), oldPod2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), oldPod3)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), oldPod4)
	require.NoError(t, err)

	// Should delete pods only up to maxUnavailable value. In this case - 75%
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	maxUnavailable := intstr.FromString("75%")
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type: corev1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	cluster.Spec.Image = "test/image:v2"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.Templates)
	require.Len(t, podControl.DeletePodName, 3)

	// Next reconcile loop should not delete more pods as 75% are already down,
	// rather it should try to create 3 pods (75%).
	deletedPods := make([]*v1.Pod, 0)
	for _, name := range podControl.DeletePodName {
		pod := &v1.Pod{}
		err = testutil.Get(k8sClient, pod, name, cluster.Namespace)
		require.NoError(t, err)
		deletedPods = append(deletedPods, pod)
		err = k8sClient.Delete(context.TODO(), pod)
		require.NoError(t, err)
	}

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
	err = k8sClient.Create(context.TODO(), replacedPod1)
	require.NoError(t, err)

	replacedPod2 := replacedPod1.DeepCopy()
	replacedPod2.Name = replacedPod1.GenerateName + "replaced-2"
	replacedPod2.Spec.NodeName = deletedPods[1].Spec.NodeName
	replacedPod2.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), replacedPod2)
	require.NoError(t, err)

	replacedPod3 := replacedPod2.DeepCopy()
	replacedPod3.Name = replacedPod2.GenerateName + "replaced-3"
	replacedPod3.Spec.NodeName = deletedPods[2].Spec.NodeName
	replacedPod3.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), replacedPod3)
	require.NoError(t, err)

	podControl.Templates = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// Reconcile should fail due to invalid maxUnavailable value in RollingUpdate strategy
	maxUnavailable := intstr.FromString("invalid-value")
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type: corev1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	err := k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid value for MaxUnavailable")

	existingCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, existingCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), existingCluster.Status.Phase)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))
}

func TestUpdateStorageClusterWhenDriverReportsPodNotUpdated(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(false).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)
	storagePod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), storagePod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterShouldRestartPodIfItDoesNotHaveAnyHash(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that is already running but does not any revision hash associated
	runningPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	runningPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), runningPod)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)
	storagePod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), storagePod)
	require.NoError(t, err)

	// TestCase: Add imagePullSecret
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.ImagePullSecret = stringPtr("pull-secret")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Update imagePullSecret
	// Replace old pod with new configuration so that it has the image pull secret
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	// Change the image pull secret
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.ImagePullSecret = stringPtr("new-pull-secret")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Remove imagePullSecret
	// Replace old pod with new configuration so that it has the new image pull secret
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.ImagePullSecret = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	storagePod := createStoragePod(cluster, "storage-pod", k8sNode.Name, storageLabels)
	storagePod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), storagePod)
	require.NoError(t, err)

	// TestCase: Add customImageRegistry
	cluster.Spec.CustomImageRegistry = "registry.first"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Update customImageRegistry
	// Replace old pod with new configuration so that it has the custom image registry
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	// Change the custom image registry
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CustomImageRegistry = "registry.second"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{storagePod.Name}, podControl.DeletePodName)

	// TestCase: Remove customImageRegistry
	// Replace old pod with new configuration so that it has the new custom image registry
	storagePod = replaceOldPod(storagePod, cluster, controller, podControl)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CustomImageRegistry = ""
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Change spec.kvdb.internal
	cluster.Spec.Kvdb = &corev1.KvdbSpec{
		Internal: true,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add spec.kvdb.endpoints
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Kvdb.Endpoints = []string{"kvdb1", "kvdb2"}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.kvdb.endpoints
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Kvdb.Endpoints = []string{"kvdb2"}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.kvdb.authSecrets
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Kvdb.AuthSecret = "test-secret"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterResourceRequirements(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add portworx container resources
	cluster.Spec.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("4Gi"),
			v1.ResourceCPU:    resource.MustParse("4"),
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Update portworx container resources
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("8Gi"),
			v1.ResourceCPU:    resource.MustParse("8"),
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Delete portworx container resources
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Resources = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.cloudStorage.deviceSpecs
	deviceSpecs := []string{"spec1", "spec2"}
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: &deviceSpecs,
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.deviceSpecs
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	deviceSpecs = append(deviceSpecs, "spec3")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add spec.cloudStorage.capacitySpecs
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CloudStorage.CapacitySpecs = []corev1.CloudStorageCapacitySpec{{MinIOPS: uint64(1000)}}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.deviceSpecs
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CloudStorage.CapacitySpecs = append(
		cluster.Spec.CloudStorage.CapacitySpecs,
		corev1.CloudStorageCapacitySpec{MinIOPS: uint64(2000)},
	)
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.journalDeviceSpec
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	journalDeviceSpec := "journal-dev-spec"
	cluster.Spec.CloudStorage.JournalDeviceSpec = &journalDeviceSpec
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.systemMetadataDeviceSpec
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	metadataDeviceSpec := "metadata-dev-spec"
	cluster.Spec.CloudStorage.SystemMdDeviceSpec = &metadataDeviceSpec
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.kvdbDeviceSpec
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	kvdbDeviceSpec := "kvdb-dev-spec"
	cluster.Spec.CloudStorage.KvdbDeviceSpec = &kvdbDeviceSpec
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.nodePoolLabel
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	nodePoolLabel := "node-pool-label"
	cluster.Spec.CloudStorage.NodePoolLabel = nodePoolLabel
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.maxStorageNodes
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	maxStorageNodes := uint32(3)
	cluster.Spec.CloudStorage.MaxStorageNodes = &maxStorageNodes
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.maxStorageNodesPerZone
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CloudStorage.MaxStorageNodesPerZone = &maxStorageNodes
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.maxStorageNodesPerZonePerNodeGroup
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CloudStorage.MaxStorageNodesPerZonePerNodeGroup = &maxStorageNodes
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add spec.cloudStorage.cloudProvider
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cloudProvider := "AWS"
	cluster.Spec.CloudStorage.Provider = &cloudProvider
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.cloudStorage.cloudProvider
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cloudProvider = "GKE"
	cluster.Spec.CloudStorage.Provider = &cloudProvider
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove spec.cloudStorage.cloudProvider
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CloudStorage.Provider = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.storage.devices
	devices := []string{"spec1", "spec2"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		Devices: &devices,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.devices
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	devices = append(devices, "spec3")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add spec.storage.cachedevices
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cacheDevices := []string{"/dev/sdc1", "/dev/sdc2"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		CacheDevices: &cacheDevices,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Update spec.storage.cachedevices
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cacheDevices = []string{"/dev/sdc1"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		CacheDevices: &cacheDevices,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove spec.storage.cachedevices
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Storage.CacheDevices = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.journalDevice
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	journalDevice := "journal-dev"
	cluster.Spec.Storage.JournalDevice = &journalDevice
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.systemMetadataDevice
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	metadataDevice := "metadata-dev"
	cluster.Spec.Storage.SystemMdDevice = &metadataDevice
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.kvdbDevice
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	kvdbDevice := "kvdb-dev"
	cluster.Spec.Storage.KvdbDevice = &kvdbDevice
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.useAll
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	boolValue := true
	cluster.Spec.Storage.UseAll = &boolValue
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.useAllWithPartitions
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Storage.UseAllWithPartitions = &boolValue
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.storage.forceUseDisks
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Storage.ForceUseDisks = &boolValue
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Change spec.network.dataInterface
	nwInterface := "eth0"
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: &nwInterface,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.network.mgmtInterface
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Network.MgmtInterface = &nwInterface
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.env
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  "key1",
			Value: "value1",
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.env
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{Name: "key2", Value: "value2"})
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.runtimeOptions
	cluster.Spec.RuntimeOpts = map[string]string{
		"key1": "value1",
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.runtimeOptions
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.RuntimeOpts["key1"] = "value2"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterVolumes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.volumes
	cluster.Spec.Volumes = []corev1.VolumeSpec{
		{
			Name:      "testvol",
			MountPath: "/var/testvol",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/host/test",
				},
			},
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change host path
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes[0].HostPath.Path = "/new/host/path"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change mount path
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes[0].MountPath = "/new/var/testvol"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change mount propagation
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	mountPropagation := v1.MountPropagationBidirectional
	cluster.Spec.Volumes[0].MountPropagation = &mountPropagation
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change readOnly param
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes[0].ReadOnly = true
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add secret type volume
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes = append(cluster.Spec.Volumes, corev1.VolumeSpec{
		Name:      "testvol2",
		MountPath: "/var/testvol2",
		VolumeSource: v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName: "volume-secret",
			},
		},
	})
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add configMap type volume
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes = append(cluster.Spec.Volumes, corev1.VolumeSpec{
		Name:      "testvol3",
		MountPath: "/var/testvol3",
		VolumeSource: v1.VolumeSource{
			ConfigMap: &v1.ConfigMapVolumeSource{
				LocalObjectReference: v1.LocalObjectReference{
					Name: "volume-configmap",
				},
			},
		},
	})
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add projected type volume
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes = append(cluster.Spec.Volumes, corev1.VolumeSpec{
		Name:      "testvol4",
		MountPath: "/var/testvol4",
		VolumeSource: v1.VolumeSource{
			Projected: &v1.ProjectedVolumeSource{
				Sources: []v1.VolumeProjection{
					{
						Secret: &v1.SecretProjection{
							LocalObjectReference: v1.LocalObjectReference{
								Name: "volume-projected-secret",
							},
						},
					},
					{
						ConfigMap: &v1.ConfigMapProjection{
							LocalObjectReference: v1.LocalObjectReference{
								Name: "volume-projected-configmap",
							},
						},
					},
				},
			},
		},
	})
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove volumes
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes = cluster.Spec.Volumes[:1]
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove all volumes
	err = testutil.Get(k8sClient, oldPod, oldPod.Name, cluster.Namespace)
	require.NoError(t, err)
	history, err := getRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = history.Labels[util.DefaultStorageClusterUniqueLabelKey]
	oldPod.Labels = storageLabels
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Volumes = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.secretsProvider
	secretsProvider := "vault"
	cluster.Spec.SecretsProvider = &secretsProvider
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.secretsProvider
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	secretsProvider = "aws-kms"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.startPort
	startPort := uint32(1000)
	cluster.Spec.StartPort = &startPort
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.startPort
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	startPort = uint32(2000)
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterCSISpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Spec.FeatureGates = map[string]string{
		string(pxutil.FeatureCSI): "true",
	}
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add spec.CSI.Enabled. Since feature gate was enabled,
	// this should not bounce pods
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CSI = &corev1.CSISpec{
		Enabled: false,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)

	require.NoError(t, err)
	require.Empty(t, result)
	// The old pod should NOT be marked for deletion, which means the pod
	// is detected to not be updated.
	require.Equal(t, []string(nil), podControl.DeletePodName)

	// TestCase: Change spec.CSI.Enabled to false
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.FeatureGates = nil
	cluster.Spec.CSI.Enabled = false
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change spec.CSI back to true
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.CSI.Enabled = true
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Snapshot controller installed should not bounce pods
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	trueBool := true
	cluster.Spec.CSI.InstallSnapshotController = &trueBool
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string(nil), podControl.DeletePodName)

	// TestCase: Change spec.CSI to nil, pods should bounce
	// GG 3rd condition
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.CSI = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: No spec.CSI changes
	_ = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.CSI = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string(nil), podControl.DeletePodName)
}

func TestUpdateCloudStorageClusterNodeSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	useAllDevices := true
	cluster.Spec.Storage = &corev1.StorageSpec{
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sNode.Labels = map[string]string{
		"test":  "foo",
		"extra": "label",
	}
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	deviceSpecs := []string{"type=dev1", "type=dev2"}
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Network: &corev1.NetworkSpec{
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

			CloudStorage: &corev1.CloudStorageNodeSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs: &deviceSpecs,
				},
			},
		},
	}

	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change node specific cloudstorage configuration.
	newDeviceSpecs := []string{"type=dev1", "type=dev2", "type=dev3"}
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.Nodes[0].CloudStorage.DeviceSpecs = &newDeviceSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	//Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs := &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)
	fmt.Println((cluster.Spec.Storage == nil), (cluster.Spec.CloudStorage == nil), (cluster.Spec.Nodes[0].Storage == nil), (cluster.Spec.Nodes[0].CloudStorage == nil))

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
	cluster.Spec.Storage = &corev1.StorageSpec{
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sNode.Labels = map[string]string{
		"test":  "foo",
		"extra": "label",
	}
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Add node specific storage configuration.
	// Should start with that instead of cluster level configuration.
	devices := []string{"dev1", "dev2"}
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: &devices,
				},
				Network: &corev1.NetworkSpec{
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
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "test",
							Operator: "InvalidOperator",
						},
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					ForceUseDisks: &useAllDevices,
				},
			},
		},
		{
			Selector: corev1.NodeSelector{
				NodeName: "k8s-node",
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAllWithPartitions: &useAllDevices,
				},
				Network: &corev1.NetworkSpec{
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
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change node specific storage configuration.
	newDevices := []string{"dev1", "dev2", "dev3"}
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)

	require.NoError(t, err)

	cluster.Spec.Nodes[0].Storage.Devices = &newDevices
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs := &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change node specific network configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes[0].Network.DataInterface = stringPtr("new_data_interface")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change existing runtime option in node specific configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes[0].RuntimeOpts["node_rt_1"] = "changed_value"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add runtime option in node specific configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes[0].RuntimeOpts["new_rt_option"] = "new_value"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change env var value in node specific configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes[0].Env[0].Value = "changed_value"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add env var in node specific configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes[0].Env = append(cluster.Spec.Nodes[0].Env, v1.EnvVar{
		Name:  "ADD_ENV",
		Value: "newly_added_env",
	})
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change env var value in cluster configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Env[0].Value = "changed_cluster_value"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Add env var in cluster configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  "ADD_CLUSTER_ENV",
		Value: "newly_added_cluster_env",
	})
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Change env var value in cluster configuration which is already
	// overridden in node level configuration. As nothing will be changed in the final
	// spec, pod should not restart.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
		Name:  "COMMON_ENV",
		Value: "cluster_value",
	})
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Change existing pod's hash to latest revision, to simulate new pod with latest spec
	revs = &appsv1.ControllerRevisionList{}
	err = k8sClient.List(context.TODO(), revs, &client.ListOptions{})
	require.NoError(t, err)
	oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestRevision(revs).Labels[util.DefaultStorageClusterUniqueLabelKey]
	err = k8sClient.Update(context.TODO(), oldPod)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change selector in node block such tha it still matches the same node.
	// Should not restart the pod as the node level configuration is unchanged.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	delete(cluster.Spec.Nodes[0].Selector.LabelSelector.MatchLabels, "test")
	cluster.Spec.Nodes[0].Selector.LabelSelector.MatchExpressions = []metav1.LabelSelectorRequirement{
		{
			Key:      "test",
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{"foo", "foo2"},
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change selector in node block so it the block does not match
	// the node. Start using configuration from another spec block that matches.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes[0].Selector.LabelSelector.MatchLabels = map[string]string{"test": "bar"}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove node specific configuration.
	// Should start using cluster level configuration.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Nodes = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll: &useAllDevices,
				},
			},
		},
	}
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	k8sNode.Labels = map[string]string{
		"test":  "foo",
		"extra": "label",
	}
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	encodedNodeLabels, _ := json.Marshal(k8sNode.Labels)
	oldPod.Annotations = map[string]string{constants.AnnotationNodeLabels: string(encodedNodeLabels)}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Change node labels so that existing spec block does not match.
	// Start using configuration from another spec block that matches.
	k8sNode.Labels["test"] = "bar"
	err = k8sClient.Update(context.TODO(), k8sNode)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should be marked for deletion, which means the pod
	// is detected to be updated.
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Remove kubernetes nodes. The pod should be marked for deletion.
	err = k8sClient.Delete(context.TODO(), k8sNode)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Change spec.updateStrategy
	maxUnavailable := intstr.FromInt(10)
	cluster.Spec.UpdateStrategy = corev1.StorageClusterUpdateStrategy{
		Type: corev1.RollingUpdateStorageClusterStrategyType,
		RollingUpdate: &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// The old pod should not be deleted as pod restart is not needed
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.deleteStrategy
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
		Type: corev1.UninstallStorageClusterStrategyType,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.revisionHistoryLimit
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	revisionHistoryLimit := int32(5)
	cluster.Spec.RevisionHistoryLimit = &revisionHistoryLimit
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.version
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Version = "1.0.0"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.imagePullPolicy
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.ImagePullPolicy = v1.PullNever
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.userInterface
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{Enabled: true}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	// TestCase: Change spec.stork
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Stork = &corev1.StorkSpec{Image: "test/image"}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
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
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	cluster.Spec.Image = "image/v2"
	invalidRevision, err := getRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)
	invalidRevision.Data.Raw = []byte("{}")
	err = k8sClient.Create(context.TODO(), invalidRevision)
	require.NoError(t, err)

	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = invalidRevision.Labels[util.DefaultStorageClusterUniqueLabelKey]
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	// TestCase: Should restart pod if it's corresponding revision does not have spec
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: Should restart pod if it's corresponding revision has empty spec
	invalidRevision.Data.Raw = []byte("{\"spec\": \"\"}")
	err = k8sClient.Update(context.TODO(), invalidRevision)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageClusterSecurity(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	oldPod.Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// TestCase: Change security to enabled
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: security enabled -> disabled
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security.Enabled = false
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: security disabled -> enabled
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			SelfSigned: &corev1.SelfSignedSpec{
				Issuer:       stringPtr("defaultissuer"),
				SharedSecret: stringPtr("defaultsecret"),
			},
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: update issuer
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security.Auth.SelfSigned.Issuer = stringPtr("newissuer")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: update shared secret
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security.Auth.SelfSigned.SharedSecret = stringPtr("newsecret")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: no change, no pod to delete.
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string(nil), podControl.DeletePodName)

	// TestCase: guest access type update, no pod to delete.
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleDisabled)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string(nil), podControl.DeletePodName)

	// TestCase: remove shared secret, set to nil
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security.Auth.SelfSigned.SharedSecret = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: tls disabled -> enabled
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security.TLS = &corev1.TLSSpec{
		Enabled: testutil.BoolPtr(true),
		RootCA: &corev1.CertLocation{
			FileName: stringPtr("/etc/pwx/ca.crt"),
		},
		ServerCert: &corev1.CertLocation{
			FileName: stringPtr("/etc/pwx/server.crt"),
		},
		ServerKey: &corev1.CertLocation{
			FileName: stringPtr("/etc/pwx/server.key"),
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: tls enabled -> disabled
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security.TLS = &corev1.TLSSpec{
		Enabled: testutil.BoolPtr(false),
		RootCA: &corev1.CertLocation{
			FileName: stringPtr("/etc/pwx/ca.crt"),
		},
		ServerCert: &corev1.CertLocation{
			FileName: stringPtr("/etc/pwx/server.crt"),
		},
		ServerKey: &corev1.CertLocation{
			FileName: stringPtr("/etc/pwx/server.key"),
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: tls disabled -> enabled
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Security.TLS.Enabled = testutil.BoolPtr(true)
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: update rootCA
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.Security.TLS.RootCA.FileName = stringPtr("/etc/pwx/newca.crt")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: update serverCert
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.Security.TLS.ServerCert.FileName = stringPtr("/etc/pwx/newcert.crt")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)

	// TestCase: update serverKey
	oldPod = replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.Security.TLS.ServerKey.FileName = stringPtr("/etc/pwx/new.key")
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
}

func TestUpdateStorageCustomAnnotations(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()
	cluster.Status.Phase = string(corev1.ClusterStateRunning)
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeRuntimeState,
		Status: corev1.ClusterConditionStatusOnline,
	})
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	storageLabels := map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  driverName,
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode := createK8sNode("k8s-node", 10)
	err = k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
	oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
	knownAnnotationKey := constants.AnnotationPodSafeToEvict
	oldPod.Annotations = map[string]string{
		knownAnnotationKey: "false",
	}
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	locator := fmt.Sprintf("%s/%s", k8s.Pod, ComponentName)
	customAnnotationKey := "custom-domain/custom-key"
	customAnnotationVal1 := "custom-val-1"
	customAnnotationVal2 := "custom-val-2"
	podPortworxAnnotation := map[string]string{
		customAnnotationKey: customAnnotationVal1,
	}

	// TestCase: Add portworx pod annotations to existing pod
	cluster.Spec.Metadata = &corev1.Metadata{}
	cluster.Spec.Metadata.Annotations = map[string]map[string]string{
		locator: podPortworxAnnotation,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	oldPod = &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: oldPod.Name, Namespace: oldPod.Namespace}}
	err = testutil.Get(k8sClient, oldPod, oldPod.Name, oldPod.Namespace)
	require.NoError(t, err)
	require.NotEmpty(t, oldPod.Annotations)
	val, ok := oldPod.Annotations[customAnnotationKey]
	require.True(t, ok)
	require.Equal(t, customAnnotationVal1, val)
	_, ok = oldPod.Annotations[knownAnnotationKey]
	require.True(t, ok)

	// TestCase: Update existing custom annotations
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	podPortworxAnnotation = map[string]string{
		customAnnotationKey: customAnnotationVal2,
	}
	cluster.Spec.Metadata.Annotations[locator] = podPortworxAnnotation
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	oldPod = &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: oldPod.Name, Namespace: oldPod.Namespace}}
	err = testutil.Get(k8sClient, oldPod, oldPod.Name, oldPod.Namespace)
	require.NoError(t, err)
	require.NotEmpty(t, oldPod.Annotations)
	val, ok = oldPod.Annotations[customAnnotationKey]
	require.True(t, ok)
	require.Equal(t, customAnnotationVal2, val)
	_, ok = oldPod.Annotations[knownAnnotationKey]
	require.True(t, ok)

	// TestCase: Add malformed custom annotation key
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Metadata.Annotations = map[string]map[string]string{
		"invalidkey": podPortworxAnnotation,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err = controller.Reconcile(context.TODO(), request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed custom annotation locator")
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	oldPod = &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: oldPod.Name, Namespace: oldPod.Namespace}}
	err = testutil.Get(k8sClient, oldPod, oldPod.Name, oldPod.Namespace)
	require.NoError(t, err)
	require.NotEmpty(t, oldPod.Annotations)
	_, ok = oldPod.Annotations[knownAnnotationKey]
	require.True(t, ok)

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), updatedCluster.Status.Phase)

	// TestCase: Add unsupported custom annotation key and remove previous one
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Metadata.Annotations = map[string]map[string]string{
		"service/storage": podPortworxAnnotation,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, podControl.DeletePodName)

	oldPod = &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: oldPod.Name, Namespace: oldPod.Namespace}}
	err = testutil.Get(k8sClient, oldPod, oldPod.Name, oldPod.Namespace)
	require.NoError(t, err)
	require.NotEmpty(t, oldPod.Annotations)
	_, ok = oldPod.Annotations[customAnnotationKey]
	require.False(t, ok)
	_, ok = oldPod.Annotations[knownAnnotationKey]
	require.True(t, ok)

	// TestCase: Newly created pod will pick up custom annotations
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	podPortworxAnnotation = map[string]string{
		customAnnotationKey: customAnnotationVal1,
	}
	cluster.Spec.Metadata.Annotations = map[string]map[string]string{
		locator: podPortworxAnnotation,
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	newPod := replaceOldPod(oldPod, cluster, &controller, podControl)
	err = testutil.Get(k8sClient, newPod, newPod.Name, newPod.Namespace)
	require.NoError(t, err)
	require.NotEmpty(t, newPod.Annotations)
	val, ok = newPod.Annotations[customAnnotationKey]
	require.True(t, ok)
	require.Equal(t, customAnnotationVal1, val)
	_, ok = oldPod.Annotations[knownAnnotationKey]
	require.True(t, ok)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	dupHistory1.Labels[util.DefaultStorageClusterUniqueLabelKey] = "00001"
	dupHistory1.Revision = firstRevision.Revision + 1
	dupHistory1.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), dupHistory1)
	require.NoError(t, err)

	dupHistory2 := firstRevision.DeepCopy()
	dupHistory2.Name = historyName(cluster.Name, "00002")
	dupHistory2.Labels[util.DefaultStorageClusterUniqueLabelKey] = "00002"
	dupHistory2.Revision = firstRevision.Revision + 2
	dupHistory2.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), dupHistory2)
	require.NoError(t, err)

	// The created pod should have the hash of first revision
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, firstRevision.Labels[util.DefaultStorageClusterUniqueLabelKey],
		oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey])
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v2"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Latest revision should be created for the updated cluster spec.
	// There were already 3 revisions in the history, now it should be 4.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 4)
	require.ElementsMatch(t,
		[]int64{1, 2, 3, 4},
		[]int64{
			revisions.Items[0].Revision,
			revisions.Items[1].Revision,
			revisions.Items[2].Revision,
			revisions.Items[3].Revision,
		},
	)

	// Test case: Changing the cluster spec back to the first version -
	// The revision number in the existing controller revision should be
	// updated to the latest number. The hash is going to remain the same.
	// Hence, new revision does not need to be created. Older duplicate
	// revisions should be removed and pod's hash should be updated to latest.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v1"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// New revision should not be created as the cluster spec is unchanged.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, dupHistory2.Name, revisions.Items[0].Name)
	require.ElementsMatch(t, []int64{4, 5}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})

	// No pod should be marked for deletion as the pod already has same spec
	// as the current spec. Check only if the pod's hash has been updated to
	// the latest duplicate version in history.
	require.Empty(t, podControl.DeletePodName)

	updatedPod := &v1.Pod{}
	err = testutil.Get(k8sClient, updatedPod, oldPod.Name, oldPod.Namespace)
	require.NoError(t, err)
	require.Equal(t, dupHistory2.Labels[util.DefaultStorageClusterUniqueLabelKey],
		updatedPod.Labels[util.DefaultStorageClusterUniqueLabelKey])
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	fakeClient := fake.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	operatorops.SetInstance(operatorops.New(fakeClient))

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	// TestCase: Simulate that the cluster got deleted while constructing history.
	// Create a colliding revision that has the same name as the current revision
	// to be created but the cluster spec does not match the current cluster spec.
	actualRevision, _ := getRevision(k8sClient, cluster, driverName)
	cluster.Spec.Image = "image/v2"
	collidingRevision, _ := getRevision(k8sClient, cluster, driverName)
	collidingRevision.Name = actualRevision.Name
	err := k8sClient.Create(context.TODO(), collidingRevision)
	require.NoError(t, err)
	cluster.Spec.Image = "image/v1"

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("\"%s\" not found", cluster.Name))

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, currCluster.Status.CollisionCount)
	require.Equal(t, string(corev1.ClusterStateDegraded), currCluster.Status.Phase)

	// New revision should not be created
	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)

	// TestCase: Hash collision with two revisions should result in an error, but the
	// CollisionCount should be increased so it does not conflict on next reconcile.
	_, err = fakeClient.CoreV1().
		StorageClusters(cluster.Namespace).
		Create(context.TODO(), cluster, metav1.CreateOptions{})
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, int32(1), *currCluster.Status.CollisionCount)
	require.Equal(t, string(corev1.ClusterStateDegraded), currCluster.Status.Phase)

	// New revision should not be created
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 1)

	// TestCase: If the collision count of current cluster and newly retrieved
	// cluster do not match, then error out and retry until the current cluster
	// gets the latest value from the api server. Do not increase the collision
	// count in this case.
	actualRevision, _ = getRevision(k8sClient, currCluster, driverName)
	cluster.Spec.Image = "image/v2"
	collidingRevision, _ = getRevision(k8sClient, cluster, driverName)
	collidingRevision.Name = actualRevision.Name
	err = k8sClient.Create(context.TODO(), collidingRevision)
	require.NoError(t, err)
	cluster.Spec.Image = "image/v1"

	result, err = controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "found a stale collision count")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, int32(1), *currCluster.Status.CollisionCount)
	require.Equal(t, string(corev1.ClusterStateDegraded), currCluster.Status.Phase)

	// New revision should not be created
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)

	// TestCase: Hash collision with two revisions should result in an error, but the
	// CollisionCount should be increased to avoid conflict on next reconcile.
	_, err = fakeClient.CoreV1().
		StorageClusters(cluster.Namespace).
		Update(context.TODO(), currCluster, metav1.UpdateOptions{})
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, util.FailedSyncReason))

	currCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, int32(2), *currCluster.Status.CollisionCount)
	require.Equal(t, string(corev1.ClusterStateDegraded), currCluster.Status.Phase)

	// New revision should not be created
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)

	// TestCase: As hash collision has be handed in previous reconcile but increasing
	// the CollisionCount, we should not get error now during reconcile and a new
	// revision should be created.
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	require.Empty(t, recorder.Events)

	// There should be 3 revisions because we created 2 colliding ones above
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode := createK8sNode("k8s-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
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
	dupHistory1.Labels[util.DefaultStorageClusterUniqueLabelKey] = "00001"
	dupHistory1.Revision = firstRevision.Revision + 1
	dupHistory1.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), dupHistory1)
	require.NoError(t, err)

	// The created pod should have the hash of first revision
	oldPod, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, firstRevision.Labels[util.DefaultStorageClusterUniqueLabelKey],
		oldPod.Labels[util.DefaultStorageClusterUniqueLabelKey])
	oldPod.Name = oldPod.GenerateName + "1"
	oldPod.Namespace = cluster.Namespace
	oldPod.Spec.NodeName = k8sNode.Name
	err = k8sClient.Create(context.TODO(), oldPod)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v2"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// Latest revision should be created for the updated cluster spec.
	// There were already 2 revisions in the history, now it should be 3.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 3)
	require.ElementsMatch(t, []int64{1, 2, 3},
		[]int64{revisions.Items[0].Revision, revisions.Items[1].Revision, revisions.Items[2].Revision},
	)

	// Test case: Changing the cluster spec back to the first version -
	// The revision number in the existing controller revision should be
	// updated to the latest number. The hash is going to remain the same.
	// Hence, new revision does not need to be created. Older duplicate
	// revisions should be removed and pod's hash should be updated to latest.
	dupHistory2 := firstRevision.DeepCopy()
	dupHistory2.Name = historyName(cluster.Name, "00002")
	dupHistory2.Labels[util.DefaultStorageClusterUniqueLabelKey] = "00002"
	dupHistory2.Revision = revisions.Items[2].Revision + 1
	dupHistory2.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), dupHistory2)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v1"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Reset the fake pod controller
	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	// New revision should not be created as the cluster spec is unchanged.
	// The latest revision should be unchanged, but previous one should be
	// deleted.
	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.Equal(t, dupHistory2.Name, revisions.Items[0].Name)
	require.ElementsMatch(t, []int64{3, 4}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})

	// No pod should be marked for deletion as the pod already has same spec
	// as the current spec. Check only if the pod's hash has been updated to
	// the latest duplicate version in history.
	require.Empty(t, podControl.DeletePodName)

	updatedPod := &v1.Pod{}
	err = testutil.Get(k8sClient, updatedPod, oldPod.Name, oldPod.Namespace)
	require.NoError(t, err)
	require.Equal(t, dupHistory2.Labels[util.DefaultStorageClusterUniqueLabelKey],
		updatedPod.Labels[util.DefaultStorageClusterUniqueLabelKey])
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
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	err := k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v2"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions := &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.ElementsMatch(t, []int64{1, 2}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})

	// Test case: Change cluster spec to add another revision.
	// Ensure that the older revision gets deleted as it is not used.
	runningPod1, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[3], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, latestRevision(revisions).Labels[util.DefaultStorageClusterUniqueLabelKey],
		runningPod1.Labels[util.DefaultStorageClusterUniqueLabelKey])
	runningPod1.Name = runningPod1.GenerateName + "1"
	runningPod1.Namespace = cluster.Namespace
	runningPod1.Spec.NodeName = k8sNode1.Name

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v3"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	// Reset the fake pod controller
	podControl.Templates = nil

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.ElementsMatch(t, []int64{2, 3}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})

	// Test case: Changing spec again to create another revision.
	// The history should not get deleted this time although it crosses
	// the limit because there are pods referring the older revisions.
	err = k8sClient.Create(context.TODO(), runningPod1)
	require.NoError(t, err)

	runningPod2, err := k8scontroller.GetPodFromTemplate(&podControl.Templates[0], cluster, clusterRef)
	require.NoError(t, err)
	require.Equal(t, latestRevision(revisions).Labels[util.DefaultStorageClusterUniqueLabelKey],
		runningPod2.Labels[util.DefaultStorageClusterUniqueLabelKey])
	runningPod2.Name = runningPod2.GenerateName + "2"
	runningPod2.Namespace = cluster.Namespace
	runningPod2.Spec.NodeName = k8sNode2.Name
	err = k8sClient.Create(context.TODO(), runningPod2)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v4"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 3)
	require.ElementsMatch(t, []int64{2, 3, 4},
		[]int64{revisions.Items[0].Revision, revisions.Items[1].Revision, revisions.Items[2].Revision})

	// Test case: Changing spec again to create another revision.
	// The unused revisions should be deleted from history. Delete
	// an existing pod and ensure it's revision if older than limit
	// should also get removed.
	err = k8sClient.Delete(context.TODO(), runningPod1)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	cluster.Spec.Image = "test/image:v5"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)

	revisions = &appsv1.ControllerRevisionList{}
	err = testutil.List(k8sClient, revisions)
	require.NoError(t, err)
	require.Len(t, revisions.Items, 2)
	require.ElementsMatch(t, []int64{3, 5}, []int64{revisions.Items[0].Revision, revisions.Items[1].Revision})
}

func TestNodeShouldRunStoragePod(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	cluster := createStorageCluster()

	now := metav1.Now()
	m2 := &cluster_v1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "m2",
			Namespace:         "default",
			DeletionTimestamp: &now,
		},
	}

	k8sClient := testutil.FakeK8sClient(cluster, m2)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-storage").AnyTimes()

	controller := Controller{
		Driver:      driver,
		client:      k8sClient,
		podControl:  podControl,
		recorder:    recorder,
		nodeInfoMap: make(map[string]*k8s.NodeInfo),
	}

	// TestCase: machine for node is being deleted
	k8sNode := createK8sNode("k8s-node-1", 1)
	k8sNode.Annotations = map[string]string{
		constants.AnnotationClusterAPIMachine: "m2",
	}
	controller.nodeInfoMap[k8sNode.Name] = &k8s.NodeInfo{
		NodeName:             k8sNode.Name,
		LastPodCreationTime:  time.Now().Add(-time.Hour),
		CordonedRestartDelay: constants.DefaultCordonedRestartDelay,
	}

	shouldRun, shouldContinueRunning, err := controller.nodeShouldRunStoragePod(k8sNode, cluster)
	require.NoError(t, err)
	require.False(t, shouldRun)
	require.True(t, shouldContinueRunning)

	// TestCase: machine for node is not found
	k8sNode.Annotations = map[string]string{
		constants.AnnotationClusterAPIMachine: "m3",
	}
	shouldRun, shouldContinueRunning, err = controller.nodeShouldRunStoragePod(k8sNode, cluster)
	require.NoError(t, err)
	require.True(t, shouldRun)
	require.True(t, shouldContinueRunning)

	// TestCase: node is recently cordoned
	k8sNode.Annotations = nil
	k8sNode.Spec.Unschedulable = true
	timeAdded := metav1.Now()
	k8sNode.Spec.Taints = []v1.Taint{
		{
			Key:       v1.TaintNodeUnschedulable,
			TimeAdded: &timeAdded,
		},
	}

	shouldRun, shouldContinueRunning, err = controller.nodeShouldRunStoragePod(k8sNode, cluster)
	require.NoError(t, err)
	require.False(t, shouldRun)
	require.True(t, shouldContinueRunning)

	// TestCase: node was cordoned for more than the default wait time ago
	timeAdded = metav1.NewTime(metav1.Now().Add(-constants.MaxCordonedRestartDelay))
	k8sNode.Spec.Taints = []v1.Taint{
		{
			Key:       v1.TaintNodeUnschedulable,
			TimeAdded: &timeAdded,
		},
	}

	shouldRun, shouldContinueRunning, err = controller.nodeShouldRunStoragePod(k8sNode, cluster)
	require.NoError(t, err)
	require.True(t, shouldRun)
	require.True(t, shouldContinueRunning)
}

func TestDoesTelemetryMatch(t *testing.T) {
	cases := []struct {
		match bool
		old   *corev1.StorageClusterSpec
		new   *corev1.StorageClusterSpec
	}{
		{
			old:   &corev1.StorageClusterSpec{},
			new:   &corev1.StorageClusterSpec{},
			match: true,
		},
		{
			old: &corev1.StorageClusterSpec{},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{},
			},
			match: true,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{},
			},
			new:   &corev1.StorageClusterSpec{},
			match: true,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{},
			},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: false,
					}},
			},
			match: true,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: false,
					}},
			},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{},
			},
			match: true,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
					}},
			},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{},
			},
			match: false,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{},
			},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
					}},
			},
			match: false,
		},
		{
			old: &corev1.StorageClusterSpec{},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
					}},
			},
			match: false,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: false,
					}},
			},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
					}},
			},
			match: false,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
					}},
			},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
					}},
			},
			match: true,
		},
		{
			old: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
						Image:   "foo",
					}},
			},
			new: &corev1.StorageClusterSpec{
				Monitoring: &corev1.MonitoringSpec{
					Telemetry: &corev1.TelemetrySpec{
						Enabled: true,
						Image:   "bar",
					}},
			},
			match: false,
		},
	}

	// UT test for match function
	for _, tc := range cases {
		actual := doesTelemetryMatch(tc.old, tc.new)
		require.Equal(t, tc.match, actual)
	}

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Test actual reconcile
	for _, tc := range cases {
		driverName := "mock-driver"
		cluster := createStorageCluster()
		// use monitoring spec from TC
		cluster.Spec.Monitoring = tc.old.Monitoring
		cluster.Spec.Image = "portworx:2.10.1"
		k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
		driver := testutil.MockDriver(mockCtrl)
		storageLabels := map[string]string{
			constants.LabelKeyClusterName: cluster.Name,
			constants.LabelKeyDriverName:  driverName,
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

		driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()
		driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
		driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
		driver.EXPECT().String().Return(driverName).AnyTimes()
		driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
		driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
		driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
		driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
		driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		driver.EXPECT().IsPodUpdated(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

		condition := &corev1.ClusterCondition{
			Type:   corev1.ClusterConditionTypeDelete,
			Status: corev1.ClusterConditionStatusCompleted,
		}
		driver.EXPECT().DeleteStorage(gomock.Any()).Return(condition, nil).AnyTimes()

		// This will create a revision which we will map to our pre-created pods
		rev1Hash, err := createRevision(k8sClient, cluster, driverName)
		require.NoError(t, err)

		// Kubernetes node with enough resources to create new pods
		k8sNode := createK8sNode("k8s-node", 10)
		err = k8sClient.Create(context.TODO(), k8sNode)
		require.NoError(t, err)

		// Pods that are already running on the k8s nodes with same hash
		storageLabels[util.DefaultStorageClusterUniqueLabelKey] = rev1Hash
		oldPod := createStoragePod(cluster, "old-pod", k8sNode.Name, storageLabels)
		oldPod.Status.Conditions = []v1.PodCondition{
			{
				Type:   v1.PodReady,
				Status: v1.ConditionTrue,
			},
		}
		err = k8sClient.Create(context.TODO(), oldPod)
		require.NoError(t, err)

		// update monitoring from test case spec
		cluster.Spec.Monitoring = tc.new.Monitoring
		err = k8sClient.Update(context.TODO(), cluster)
		require.NoError(t, err)

		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		result, err := controller.Reconcile(context.TODO(), request)
		require.NoError(t, err)
		require.Empty(t, result)

		if !tc.match {
			// The old pod should be marked for deletion, which means the pod
			// is detected to be updated.
			require.Equal(t, []string{oldPod.Name}, podControl.DeletePodName)
		}

		// teardown
		err = k8sClient.Delete(context.TODO(), k8sNode)
		require.NoError(t, err)
		err = k8sClient.Delete(context.TODO(), oldPod)
		require.NoError(t, err)
		err = k8sClient.Delete(context.TODO(), cluster)
		require.NoError(t, err)
		_, err = deleteRevision(k8sClient, cluster, driverName)
		require.NoError(t, err)
		request = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		result, err = controller.Reconcile(context.TODO(), request)
		require.NoError(t, err)
		require.Empty(t, result)
	}
}

func TestShouldPreflightRun(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	// TestCase: aws cloud provider with image < 3.0
	logrus.Infof("check aws cloud w/PX < 3.0...")
	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-1"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))

	cluster.Spec.Image = "portworx/oci-image:2.9.0"

	err := preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	require.Equal(t, string(cloudops.AWS), pxutil.GetCloudProvider(cluster)) // Make sure aws

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.False(t, preflightShouldRun(cluster))
	logrus.Infof("aws cloud w/PX < 3.0, preflight will not run")

	// Reset preflight for other tests
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: aws cloud provider with image >= 3.0
	logrus.Infof("check aws cloud w/PX >= 3.0...")
	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, preflightShouldRun(cluster))
	logrus.Infof("aws cloud w/PX >= 3.0, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Vsphere cloud provider with image <= 3.0
	logrus.Infof("check vsphere cloud w/PX <= 3.0...")
	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	// force vsphere
	env := make([]v1.EnvVar, 1)
	env[0].Name = "VSPHERE_VCENTER"
	env[0].Value = "some.vcenter.server.com"
	cluster.Spec.Env = env

	require.Equal(t, string(cloudops.Vsphere), pxutil.GetCloudProvider(cluster)) // Make sure Vsphere

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.False(t, preflightShouldRun(cluster))
	logrus.Infof("vshpere cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Vsphere cloud provider with image >= 3.1
	logrus.Infof("check vsphere cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, preflightShouldRun(cluster))
	logrus.Infof("vshpere cloud w/PX >= 3.1, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Pure cloud provider with image <= 3.0
	logrus.Infof("check Pure cloud w/PX <= 3.0...")
	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	// force Pure
	env = make([]v1.EnvVar, 1)
	env[0].Name = "PURE_FLASHARRAY_SAN_TYPE"
	env[0].Value = "FC"
	cluster.Spec.Env = env

	require.Equal(t, string(cloudops.Pure), pxutil.GetCloudProvider(cluster)) // Make sure Pure

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.False(t, preflightShouldRun(cluster))
	logrus.Infof("Pure cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Pure cloud provider with image >= 3.1
	logrus.Infof("check Pure cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, preflightShouldRun(cluster))
	logrus.Infof("Pure cloud w/PX >= 3.1, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Azure cloud provider with image <= 3.0
	logrus.Infof("check Azure cloud w/PX <= 3.0...")
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-1"}},
	}}

	versionClient := fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &kversion.Info{
		GitVersion: "v1.26.5",
	}
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient = testutil.FakeK8sClient(fakeK8sNodes)

	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	c := preflight.Instance()
	require.Equal(t, cloudops.Azure, c.ProviderName()) // Make sure Pure

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.False(t, preflightShouldRun(cluster))
	logrus.Infof("Azure cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Azure cloud provider with image >= 3.1
	logrus.Infof("check Azure cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, preflightShouldRun(cluster))
	logrus.Infof("Pure Azure w/PX >= 3.1, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Vsphere cloud provider with PKS and image >= 3.1
	logrus.Infof("check vsphere cloud with PKS and PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	env = make([]v1.EnvVar, 1)
	env[0].Name = "VSPHERE_VCENTER"
	env[0].Value = "some.vcenter.server.com"
	cluster.Spec.Env = env

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient = testutil.FakeK8sClient(cluster)
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: preflight.PksSystemNamespace,
		},
	}
	_, err = coreops.Instance().CreateNamespace(ns)
	require.NoError(t, err)

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
	require.True(t, preflight.IsPKS())

	driver.EXPECT().UpdateDriver(gomock.Any())
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.False(t, preflightShouldRun(cluster))
	logrus.Infof("vsphere cloud with PKS and PX >= 3.1 will not run")

}

func TestEKSPreflightCheck(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	preflightOps := mock.NewMockCheckerOps(mockCtrl)
	preflight.SetInstance(preflightOps)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
			Annotations: map[string]string{
				pxutil.AnnotationPreflightCheck: "true",
				pxutil.AnnotationIsEKS:          "true",
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(cluster)
	driver := testutil.MockDriver(mockCtrl)
	driver.EXPECT().Validate(gomock.Any()).Return(nil).AnyTimes()

	controller := Controller{
		client: k8sClient,
		Driver: driver,
	}

	// TestCase: eks cloud permission check failed
	errMsg := "eks cloud permission check failed"

	preflightOps.EXPECT().ProviderName().Return(string(cloudops.AWS)).AnyTimes()
	preflightOps.EXPECT().K8sDistributionName().Return("eks").AnyTimes()
	preflightOps.EXPECT().CheckCloudDrivePermission(cluster).Return(fmt.Errorf(errMsg))

	err := controller.runPreflightCheck(cluster)
	require.Error(t, err)
	require.Contains(t, errMsg, err.Error())

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	check, ok := cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypePreflight)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusFailed, condition.Status)

	// TestCase: without the permission fixed, preflight check should return error directly
	errMsg = "FATAL: preflight checks have failed on your cluster.  " +
		"Check events, logs and contact support to help make sure your cluster meets all prerequisites"
	err = controller.runPreflightCheck(cluster)
	require.Error(t, err)
	require.Contains(t, errMsg, err.Error())

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypePreflight)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusFailed, condition.Status)

	// TestCase: fix the permission then rerun preflight
	cluster.Annotations[pxutil.AnnotationPreflightCheck] = "true"
	// Cloud drive permission check will only be checked once when the status condition is not set.
	cluster.Status.Conditions = []corev1.ClusterCondition{}
	preflightOps.EXPECT().CheckCloudDrivePermission(cluster).Return(nil)
	err = controller.runPreflightCheck(cluster)
	require.NoError(t, err)

	// Pre-flight checks now contain DMthin checks which will not be executed unless cloud permission checks passes.
	// Since the DMthin checks will not be  executed the condition returned will be 'ClusterConditionStatusInProgress'.
	// This is good enough to determine if cloud permission is working. If it had failed DMthin would not be run and
	// the condition would have been ClusterConditionStatusFailed.
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypePreflight)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
}

func TestPreflightStorageNodeCreation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := createStorageCluster()

	cluster.Spec.Placement.NodeAffinity = &v1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
			NodeSelectorTerms: []v1.NodeSelectorTerm{
				{
					MatchExpressions: []v1.NodeSelectorRequirement{
						{
							Key:      "px/enabled",
							Operator: v1.NodeSelectorOpNotIn,
							Values:   []string{"false"},
						},
						{
							Key:      k8s.NodeRoleLabelControlPlane,
							Operator: v1.NodeSelectorOpExists,
						},
						{
							Key:      k8s.NodeRoleLabelWorker,
							Operator: v1.NodeSelectorOpExists,
						},
					},
				},
				{
					MatchExpressions: []v1.NodeSelectorRequirement{
						{
							Key:      "px/enabled",
							Operator: v1.NodeSelectorOpNotIn,
							Values:   []string{"false"},
						},
						{
							Key:      k8s.NodeRoleLabelMaster,
							Operator: v1.NodeSelectorOpDoesNotExist,
						},
						{
							Key:      k8s.NodeRoleLabelControlPlane,
							Operator: v1.NodeSelectorOpDoesNotExist,
						},
						{
							Key:      k8s.NodeRoleLabelInfra,
							Operator: v1.NodeSelectorOpDoesNotExist,
						},
					},
				},
			},
		},
	}

	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode2 := createK8sNode("k8s-node-2", 1)
	k8sNode3 := createK8sNode("k8s-node-3", 1)
	k8sNode4 := createK8sNode("k8s-node-4", 1)

	k8sNode1.Labels["node-role.kubernetes.io/worker"] = ""
	k8sNode2.Labels["node-role.kubernetes.io/worker"] = ""
	k8sNode3.Labels["node-role.kubernetes.io/control-plane"] = ""
	k8sNode4.Labels["node-role.kubernetes.io/worker"] = ""

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2, k8sNode3, k8sNode4)

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()

	controller := Controller{
		client: k8sClient,
		Driver: driver,
	}

	storageNodeNames := func(storageNodes *corev1.StorageNodeList) []string {
		sNames := []string{}

		for _, snode := range storageNodes.Items {
			sNames = append(sNames, snode.Name)
		}
		sort.Strings(sNames)
		return sNames
	}

	logrus.Infof("Skipping only 'control-plane' node")
	expectedStorageNodeNames := []string{"k8s-node-1", "k8s-node-2", "k8s-node-4"}
	err := controller.createPreFlightStorageNodes(cluster)
	require.NoError(t, err)

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, len(expectedStorageNodeNames))
	sNames := storageNodeNames(storageNodes)
	require.Equal(t, strings.Join(expectedStorageNodeNames, " "), strings.Join(sNames, " "))

	err = controller.deletePreFlightStorageNodes(cluster)
	require.NoError(t, err)

	logrus.Infof("Skipping 'control-plane' node & 'px/enabled=false' node")
	k8sNode2.Labels["px/enabled"] = "false"
	err = k8sClient.Update(context.TODO(), k8sNode2)
	require.NoError(t, err)

	expectedStorageNodeNames = []string{"k8s-node-1", "k8s-node-4"}

	err = controller.createPreFlightStorageNodes(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, len(expectedStorageNodeNames))
	sNames = storageNodeNames(storageNodes)
	require.Equal(t, strings.Join(expectedStorageNodeNames, " "), strings.Join(sNames, " "))

	err = controller.deletePreFlightStorageNodes(cluster)
	require.NoError(t, err)

	logrus.Infof("Skipping 'master' node & creating storage node for 'control-plane + worker'")
	// Remove px/enabled=false
	k8sNode2.Labels = map[string]string{"node-role.kubernetes.io/worker": ""}
	err = k8sClient.Update(context.TODO(), k8sNode2)
	require.NoError(t, err)

	// Add 'worker' to the 'control-plane' node
	k8sNode3.Labels["node-role.kubernetes.io/worker"] = ""
	err = k8sClient.Update(context.TODO(), k8sNode3)
	require.NoError(t, err)

	// Add 'master'
	k8sNode1.Labels = map[string]string{k8s.NodeRoleLabelMaster: ""}
	err = k8sClient.Update(context.TODO(), k8sNode1)
	require.NoError(t, err)

	expectedStorageNodeNames = []string{"k8s-node-2", "k8s-node-3", "k8s-node-4"}

	err = controller.createPreFlightStorageNodes(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, len(expectedStorageNodeNames))
	sNames = storageNodeNames(storageNodes)
	require.Equal(t, strings.Join(expectedStorageNodeNames, " "), strings.Join(sNames, " "))

	err = controller.deletePreFlightStorageNodes(cluster)
	require.NoError(t, err)

	logrus.Infof("Skipping 'infra' node only")
	// Remove 'master'
	k8sNode1.Labels = map[string]string{"node-role.kubernetes.io/worker": ""}
	err = k8sClient.Update(context.TODO(), k8sNode1)
	require.NoError(t, err)

	// Remove control-plane label
	k8sNode3.Labels = map[string]string{"node-role.kubernetes.io/worker": ""}
	err = k8sClient.Update(context.TODO(), k8sNode3)
	require.NoError(t, err)

	// Add 'infra'
	k8sNode4.Labels = map[string]string{k8s.NodeRoleLabelInfra: ""}
	err = k8sClient.Update(context.TODO(), k8sNode4)
	require.NoError(t, err)

	expectedStorageNodeNames = []string{"k8s-node-1", "k8s-node-2", "k8s-node-3"}

	err = controller.createPreFlightStorageNodes(cluster)
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, len(expectedStorageNodeNames))
	sNames = storageNodeNames(storageNodes)
	require.Equal(t, strings.Join(expectedStorageNodeNames, " "), strings.Join(sNames, " "))

	err = controller.deletePreFlightStorageNodes(cluster)
	require.NoError(t, err)
}

func TestStorageClusterStateDuringValidation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster",
			Namespace: "ns",
		},
	}

	k8sVersion, _ := version.NewVersion("1.19.0")
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	validationErr := fmt.Errorf("minimum supported kubernetes version by the operator is 1.21.0")
	driver.EXPECT().Validate(gomock.Any()).Return(validationErr).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	// Validation failed during fresh install
	result, err := controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), validationErr.Error())

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeWarning, util.FailedValidationReason, validationErr.Error()))

	updatedCluster := &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), updatedCluster.Status.Phase)
	require.True(t, pxutil.IsFreshInstall(updatedCluster))

	// Reconcile again and storage cluster phase should not be changed during fresh install
	result, err = controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), validationErr.Error())

	updatedCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), updatedCluster.Status.Phase)
	require.True(t, pxutil.IsFreshInstall(updatedCluster))

	// Validation failed when updating a running cluster
	updatedCluster.Status = corev1.StorageClusterStatus{
		Phase: string(corev1.ClusterStateRunning),
		Conditions: []corev1.ClusterCondition{
			{
				Source: pxutil.PortworxComponentName,
				Type:   corev1.ClusterConditionTypeRuntimeState,
				Status: corev1.ClusterConditionStatusOnline,
			},
		},
	}
	err = k8sClient.Update(context.TODO(), updatedCluster)
	require.NoError(t, err)

	result, err = controller.Reconcile(context.TODO(), request)
	require.Empty(t, result)
	require.Contains(t, err.Error(), validationErr.Error())

	updatedCluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), updatedCluster.Status.Phase)
	require.False(t, pxutil.IsFreshInstall(updatedCluster))
}

func TestStorageSpecValidation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster",
			Namespace: "ns",
		},
	}
	//Testcase: Cluster has both storage types and no node specs
	useAllDevices := true
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll: &useAllDevices,
	}
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: stringSlicePtr([]string{"type=dev1"}),
		},
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	driver := testutil.MockDriver(mockCtrl)
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}
	validationErr := fmt.Errorf("found spec 1 for storage and cloudStorage, ensure spec.storage fields are empty to use cloud storage")
	driver.EXPECT().Validate(gomock.Any()).Return(validationErr).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStorageNodes(gomock.Any()).Return(nil, nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}

	result, err := controller.Reconcile(context.TODO(), request)
	require.Contains(t, err.Error(), validationErr.Error())
	require.Error(t, err)
	require.Empty(t, result)

	//Testcase: Validate node having both storage and cloudstorage and cluster has cloudstorage

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.Storage = nil
	devices := []string{"dev1", "dev2"}
	deviceSpecs := []string{"type=dev1", "type=dev2"}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: &devices,
				},
			},
			CloudStorage: &corev1.CloudStorageNodeSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs: &deviceSpecs,
				},
			},
		},
	}

	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	validationErr = fmt.Errorf("found spec 2 for storage and cloudstorage on node")
	result, err = controller.Reconcile(context.TODO(), request)
	require.Contains(t, err.Error(), validationErr.Error())
	require.Error(t, err)
	require.Empty(t, result)

	//Testcase: Node has both specs and cluster has storage
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.CloudStorage = nil
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll: &useAllDevices,
	}

	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	validationErr = fmt.Errorf("found spec 2 for storage and cloudstorage on node")
	result, err = controller.Reconcile(context.TODO(), request)
	require.Contains(t, err.Error(), validationErr.Error())
	require.Error(t, err)
	require.Empty(t, result)

	//Testcase: Validate when cluster has both specs and node has cloud

	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: stringSlicePtr([]string{"type=dev1"}),
		},
	}

	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CloudStorage: &corev1.CloudStorageNodeSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs: &deviceSpecs,
				},
			},
		},
	}

	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	validationErr = fmt.Errorf("found spec 3 for storage and cloudStorage, ensure spec.storage fields are empty to use cloud storage")
	result, err = controller.Reconcile(context.TODO(), request)
	require.Contains(t, err.Error(), validationErr.Error())
	require.Error(t, err)
	require.Empty(t, result)

	//Testcase: When cluster has both and node has storage
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: &devices,
				},
			},
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	validationErr = fmt.Errorf("found spec 3 for storage and cloudStorage, ensure spec.storage fields are empty to use cloud storage")
	result, err = controller.Reconcile(context.TODO(), request)
	require.Contains(t, err.Error(), validationErr.Error())
	require.Error(t, err)
	require.Empty(t, result)

	//Update specs to have no error
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.CloudStorage = nil
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CloudStorage: &corev1.CloudStorageNodeSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs: &deviceSpecs,
				},
			},
		},
	}

	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	fmt.Println((cluster.Spec.Storage == nil), (cluster.Spec.CloudStorage == nil), (cluster.Spec.Nodes[0].Storage == nil), (cluster.Spec.Nodes[0].CloudStorage == nil))

	//ANother valid case
	err = testutil.Get(k8sClient, cluster, cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	cluster.Spec.Storage = nil
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: stringSlicePtr([]string{"type=dev1"}),
		},
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			Selector: corev1.NodeSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"test": "foo",
					},
				},
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices: &devices,
				},
			},
		},
	}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err = controller.Reconcile(context.TODO(), request)
	require.NoError(t, err)
	require.Empty(t, result)
	fmt.Println((cluster.Spec.Storage == nil), (cluster.Spec.CloudStorage == nil), (cluster.Spec.Nodes[0].Storage == nil), (cluster.Spec.Nodes[0].CloudStorage == nil))

}

func replaceOldPod(
	oldPod *v1.Pod,
	cluster *corev1.StorageCluster,
	controller *Controller,
	podControl *k8scontroller.FakePodControl,
) *v1.Pod {
	// Delete the old pod
	err := controller.client.Delete(context.TODO(), oldPod)
	logrus.Error(err)

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
	_, err = controller.Reconcile(context.TODO(), request)
	logrus.Error(err)

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
	err = controller.client.Create(context.TODO(), newPod)
	logrus.Error(err)

	podControl.Templates = nil
	podControl.ControllerRefs = nil
	podControl.DeletePodName = nil
	return newPod
}

func createStorageCluster() *corev1.StorageCluster {
	maxUnavailable := intstr.FromInt(defaultMaxUnavailablePods)
	revisionLimit := int32(defaultRevisionHistoryLimit)
	return &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			UID:        "test-uid",
			Name:       "test-cluster",
			Namespace:  "test-ns",
			Finalizers: []string{deleteFinalizerName},
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullPolicy: v1.PullAlways,
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Security: &corev1.SecuritySpec{
				Enabled: false,
			},
			RevisionHistoryLimit: &revisionLimit,
			UpdateStrategy: corev1.StorageClusterUpdateStrategy{
				Type: corev1.RollingUpdateStorageClusterStrategyType,
				RollingUpdate: &corev1.RollingUpdateStorageCluster{
					MaxUnavailable: &maxUnavailable,
				},
			},
			Placement: &corev1.PlacementSpec{},
		},
	}
}

func createRevision(
	k8sClient client.Client,
	cluster *corev1.StorageCluster,
	driverName string,
) (string, error) {
	history, err := getRevision(k8sClient, cluster, driverName)
	if err != nil {
		return "", err
	}
	if err := k8sClient.Create(context.TODO(), history); err != nil {
		return "", err
	}
	return history.Labels[util.DefaultStorageClusterUniqueLabelKey], nil
}

func deleteRevision(
	k8sClient client.Client,
	cluster *corev1.StorageCluster,
	driverName string,
) (string, error) {
	history, err := getRevision(k8sClient, cluster, driverName)
	if err != nil {
		return "", err
	}
	if err := k8sClient.Delete(context.TODO(), history); err != nil {
		return "", err
	}
	return history.Labels[util.DefaultStorageClusterUniqueLabelKey], nil
}

func getRevision(
	k8sClient client.Client,
	cluster *corev1.StorageCluster,
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
				constants.LabelKeyClusterName:            cluster.Name,
				constants.LabelKeyDriverName:             driverName,
				util.DefaultStorageClusterUniqueLabelKey: hash,
			},
			Annotations:     cluster.Annotations,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, controllerKind)},
		},
		Data: runtime.RawExtension{Raw: patch},
	}, nil
}

func createStoragePod(
	cluster *corev1.StorageCluster,
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

func createStorageNode(nodeName string, healthy bool) *storageapi.StorageNode {
	status := storageapi.Status_STATUS_OK
	if !healthy {
		status = storageapi.Status_STATUS_ERROR
	}
	return &storageapi.StorageNode{
		Status:            status,
		SchedulerNodeName: nodeName,
	}
}

func createK8sNode(nodeName string, allowedPods int) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeName,
			Labels: make(map[string]string),
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

func guestAccessTypePtr(val corev1.GuestAccessType) *corev1.GuestAccessType {
	return &val
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

func keepV1beta1CRDActivated(fakeClient *fakeextclient.Clientset, crdName string) error {
	return wait.Poll(1*time.Second, 1*time.Minute, func() (bool, error) {
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
			_, err = fakeClient.ApiextensionsV1beta1().
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

func TestIndexByPodNodeName(t *testing.T) {
	p := &v1.Pod{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{},
		Spec: v1.PodSpec{
			NodeName: "n1",
		},
		Status: v1.PodStatus{},
	}

	retVal := indexByPodNodeName(p)
	require.Equal(t, retVal, []string{"n1"})

	p.Spec.NodeName = ""
	retVal = indexByPodNodeName(p)
	require.Empty(t, retVal)

	retVal = indexByPodNodeName(&v1.Node{})
	require.Empty(t, retVal)
}

func latestRevision(revs *appsv1.ControllerRevisionList) *appsv1.ControllerRevision {
	if revs == nil || len(revs.Items) == 0 {
		return nil
	}

	latestRev := revs.Items[0].DeepCopy()
	for _, rev := range revs.Items {
		if rev.Revision > latestRev.Revision {
			latestRev = rev.DeepCopy()
		}
	}
	return latestRev
}
