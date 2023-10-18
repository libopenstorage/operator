package portworx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	version "github.com/hashicorp/go-version"
	ocp_secv1 "github.com/openshift/api/security/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/cloudprovider"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/libopenstorage/operator/pkg/preflight"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/kvdb"
	"github.com/portworx/kvdb/api/bootstrap/k8s"
	"github.com/portworx/kvdb/consul"
	e2 "github.com/portworx/kvdb/etcd/v2"
	e3 "github.com/portworx/kvdb/etcd/v3"
	"github.com/portworx/kvdb/mem"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
)

func TestString(t *testing.T) {
	driver := portworx{}
	require.Equal(t, pxutil.DriverName, driver.String())
}

func TestInit(t *testing.T) {
	driver := portworx{}
	k8sClient := testutil.FakeK8sClient()
	scheme := runtime.NewScheme()
	recorder := record.NewFakeRecorder(0)

	// Nil k8s client
	err := driver.Init(nil, scheme, recorder)
	require.EqualError(t, err, "kubernetes client cannot be nil")

	// Nil k8s scheme
	err = driver.Init(k8sClient, nil, recorder)
	require.EqualError(t, err, "kubernetes scheme cannot be nil")

	// Nil k8s event recorder
	err = driver.Init(k8sClient, scheme, nil)
	require.EqualError(t, err, "event recorder cannot be nil")

	// Valid k8s client
	err = driver.Init(k8sClient, scheme, recorder)
	require.NoError(t, err)
	require.Equal(t, k8sClient, driver.k8sClient)
}

func TestValidate(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPreflightCheck: "true",
			},
		},
	}

	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxPreFlightDaemonSetName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			UID:             types.UID("preflight-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}

	checks := []corev1.CheckResult{
		{
			Type:    "status",
			Reason:  "oci-mon: pre-flight completed",
			Success: true,
		},
	}

	status := corev1.NodeStatus{
		Checks: checks,
	}

	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: status,
	}

	preFlightPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "preflight-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: preflightDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:  "portworx",
					Ready: true,
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	k8sClient := testutil.FakeK8sClient(preflightDS)

	err := k8sClient.Create(context.TODO(), preFlightPod1)
	require.NoError(t, err)

	preflightDS.Status.DesiredNumberScheduled = int32(1)
	err = k8sClient.Status().Update(context.TODO(), preflightDS)
	require.NoError(t, err)

	recorder := record.NewFakeRecorder(100)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.Validate(cluster)
	require.NoError(t, err)
	require.Contains(t, cluster.Annotations[pxutil.AnnotationMiscArgs], "-T px-storev2")
	require.NotEmpty(t, recorder.Events)
	<-recorder.Events // Pop first event which is Default telemetry enabled event
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeNormal, util.PassPreFlight, "Enabling PX-StoreV2"))
	require.Contains(t, *cluster.Spec.CloudStorage.SystemMdDeviceSpec, DefCmetaAWS)

	//
	// Validate Pre-flight Daemonset Pod Spec
	//
	cluster.Spec.Image = "portworx/oci-image:3.0.0"
	cluster.Annotations = map[string]string{
		pxutil.AnnotationPreflightCheck: "true",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	podSpec, err := driver.GetStoragePodSpec(cluster, "")
	require.NoError(t, err)
	require.Equal(t, 2, len(podSpec.Containers))

	// phone-home cm volume mount, should not exists since pre-flight is enabled
	for _, cnt := range podSpec.Containers {
		for _, volumeMount := range cnt.VolumeMounts {
			require.True(t, volumeMount.Name != "ccm-phonehome-config")
		}
	}

	preFlighter := NewPreFlighter(cluster, k8sClient, podSpec)
	require.NotNil(t, preFlighter)

	// / Create preflighter podSpec
	preflightDSCheck, err := preFlighter.CreatePreFlightDaemonsetSpec(clusterRef)
	require.NoError(t, err)
	require.NotNil(t, preflightDSCheck)

	// Make sure the phone-home cm volume mount, still does not exists
	for _, cnt := range preflightDSCheck.Spec.Template.Spec.Containers {
		for _, volumeMount := range cnt.VolumeMounts {
			require.True(t, volumeMount.Name != "ccm-phonehome-config")
		}
	}

	// Check OCP Security component setting function
	expectedSCC := testutil.GetExpectedSCC(t, "portworxSCC.yaml")
	scc := &ocp_secv1.SecurityContextConstraints{}
	err = testutil.Get(k8sClient, scc, expectedSCC.Name, "")
	require.NotNil(t, err)

	expectedPxRestrictedSCC := testutil.GetExpectedSCC(t, "portworxRestrictedSCC.yaml")
	pxRestrictedSCC := &ocp_secv1.SecurityContextConstraints{}
	err = testutil.Get(k8sClient, pxRestrictedSCC, expectedPxRestrictedSCC.Name, "")
	require.NotNil(t, err)

	// Install with SCC enabled
	crd := testutil.GetExpectedCRDV1(t, "sccCrd.yaml")
	err = k8sClient.Create(context.TODO(), crd)
	require.NoError(t, err)

	err = createSecurityContextForValidate(recorder, cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, scc, expectedSCC.Name, "")
	require.NoError(t, err)
	require.Equal(t, expectedSCC, scc)

	err = testutil.Get(k8sClient, pxRestrictedSCC, expectedPxRestrictedSCC.Name, "")
	require.NoError(t, err)
	require.Equal(t, expectedSCC, scc)

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
}

func TestValidateCheckFailure(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPreflightCheck: "true",
			},
		},
	}

	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxPreFlightDaemonSetName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			UID:             types.UID("preflight-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}

	checks := []corev1.CheckResult{
		{
			Type:    "usage",
			Reason:  "px-runc: PX-StoreV2 unsupported storage disk spec: 'consume unused' (-A/-a) options not valid with PX-StoreV2",
			Success: false,
		},
		{
			Type:    "status",
			Reason:  "oci-mon: pre-flight completed",
			Success: true,
		},
	}

	status := corev1.NodeStatus{
		Checks: checks,
	}

	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: status,
	}

	preFlightPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "preflight-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: preflightDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:  "portworx",
					Ready: true,
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient(preflightDS)

	err := k8sClient.Create(context.TODO(), preFlightPod1)
	require.NoError(t, err)

	preflightDS.Status.DesiredNumberScheduled = int32(1)
	err = k8sClient.Status().Update(context.TODO(), preflightDS)
	require.NoError(t, err)

	recorder := record.NewFakeRecorder(100)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.Validate(cluster)
	require.NoError(t, err)
	require.NotEmpty(t, recorder.Events)
	require.Len(t, recorder.Events, 3)
	<-recorder.Events // Pop first event which is Default telemetry enabled event
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeWarning, util.FailedPreFlight, "usage pre-flight check failed"))
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeNormal, util.PassPreFlight, "Not enabling PX-StoreV2"))
}

func TestValidateMissingRequiredCheck(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPreflightCheck: "true",
			},
		},
	}

	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxPreFlightDaemonSetName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			UID:             types.UID("preflight-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}

	checks := []corev1.CheckResult{
		{},
	}

	status := corev1.NodeStatus{
		Checks: checks,
	}

	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: status,
	}

	preFlightPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "preflight-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: preflightDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:  "portworx",
					Ready: true,
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient(preflightDS)

	err := k8sClient.Create(context.TODO(), preFlightPod1)
	require.NoError(t, err)

	preflightDS.Status.DesiredNumberScheduled = int32(1)
	err = k8sClient.Status().Update(context.TODO(), preflightDS)
	require.NoError(t, err)

	recorder := record.NewFakeRecorder(100)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.Validate(cluster)
	require.NoError(t, err)
	require.NotEmpty(t, recorder.Events)
	for i := 0; i < len(recorder.Events); i++ { // Get last record
		<-recorder.Events
	}
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeNormal, util.PassPreFlight, "Not enabling PX-StoreV2"))
}

func TestValidateVsphere(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	// force vsphere
	env := make([]v1.EnvVar, 1)
	env[0].Name = "VSPHERE_VCENTER"
	env[0].Value = "some.vcenter.server.com"
	cluster.Spec.Env = env

	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxPreFlightDaemonSetName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			UID:             types.UID("preflight-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}

	checks := []corev1.CheckResult{
		{
			Type:    "status",
			Reason:  "oci-mon: pre-flight completed",
			Success: true,
		},
	}

	status := corev1.NodeStatus{
		Checks: checks,
	}

	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: status,
	}

	preFlightPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "preflight-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: preflightDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:  "portworx",
					Ready: true,
				},
			},
		},
	}

	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient(preflightDS)

	err := k8sClient.Create(context.TODO(), preFlightPod1)
	require.NoError(t, err)

	preflightDS.Status.DesiredNumberScheduled = int32(1)
	err = k8sClient.Status().Update(context.TODO(), preflightDS)
	require.NoError(t, err)

	recorder := record.NewFakeRecorder(100)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.Validate(cluster)
	require.NoError(t, err)
	require.Contains(t, cluster.Annotations[pxutil.AnnotationMiscArgs], "-T px-storev2")
	require.NotEmpty(t, recorder.Events)
	<-recorder.Events // Pop first event which is Default telemetry enabled event
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeNormal, util.PassPreFlight, "Enabling PX-StoreV2"))
	require.Contains(t, *cluster.Spec.CloudStorage.SystemMdDeviceSpec, DefCmetaVsphere)
}

func TestValidatePure(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxPreFlightDaemonSetName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			UID:             types.UID("preflight-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}

	checks := []corev1.CheckResult{
		{
			Type:    "status",
			Reason:  "oci-mon: pre-flight completed",
			Success: true,
		},
	}

	status := corev1.NodeStatus{
		Checks: checks,
	}

	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: status,
	}

	preFlightPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "preflight-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: preflightDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:  "portworx",
					Ready: true,
				},
			},
		},
	}

	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient(preflightDS)

	err := k8sClient.Create(context.TODO(), preFlightPod1)
	require.NoError(t, err)

	preflightDS.Status.DesiredNumberScheduled = int32(1)
	err = k8sClient.Status().Update(context.TODO(), preflightDS)
	require.NoError(t, err)

	recorder := record.NewFakeRecorder(100)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	origCluster := cluster.DeepCopy() // No pure env

	// force pure ISCSI
	env := make([]v1.EnvVar, 1)
	env[0].Name = "PURE_FLASHARRAY_SAN_TYPE"
	env[0].Value = "ISCSI"
	cluster.Spec.Env = env

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.Validate(cluster)
	require.NoError(t, err)
	require.Contains(t, cluster.Annotations[pxutil.AnnotationMiscArgs], "-T px-storev2")

	actual, err := driver.GetStoragePodSpec(cluster, "testNode")
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.Contains(t, strings.Join(actual.Containers[0].Args, " "), "-cloud_provider "+cloudops.Pure)
	require.Contains(t, strings.Join(actual.Containers[0].Args, " "), "-metadata "+DefCmetaPure)

	require.NotEmpty(t, recorder.Events)
	<-recorder.Events // Pop first event which is Default telemetry enabled event
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeNormal, util.PassPreFlight, "Enabling PX-StoreV2"))
	require.Contains(t, *cluster.Spec.CloudStorage.SystemMdDeviceSpec, DefCmetaPure)

	// Below just validate that changing the PURE_FLASHARRAY_SAN_TYPE value will still set the
	// -cloud_provider param correctly.

	cluster = origCluster // Reset Cluster with no pure env
	// force pure ISCSI
	env = make([]v1.EnvVar, 1)
	env[0].Name = "PURE_FLASHARRAY_SAN_TYPE"
	env[0].Value = "FC"
	cluster.Spec.Env = env

	actual, err = driver.GetStoragePodSpec(cluster, "testNode")
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.NotContains(t, strings.Join(actual.Containers[0].Args, " "), "-cloud_provider "+cloudops.Pure)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	actual, err = driver.GetStoragePodSpec(cluster, "testNode")
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.Contains(t, strings.Join(actual.Containers[0].Args, " "), "-cloud_provider "+cloudops.Pure)
}

func TestValidateAzure(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxPreFlightDaemonSetName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			UID:             types.UID("preflight-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
		},
	}

	checks := []corev1.CheckResult{
		{
			Type:    "status",
			Reason:  "oci-mon: pre-flight completed",
			Success: true,
		},
	}

	status := corev1.NodeStatus{
		Checks: checks,
	}

	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "node-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Status: status,
	}

	preFlightPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "preflight-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: preflightDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:  "portworx",
					Ready: true,
				},
			},
		},
	}

	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-1"}},
	}}

	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	k8sClient := testutil.FakeK8sClient(cluster)

	err := k8sClient.Create(context.TODO(), preflightDS)
	require.NoError(t, err)

	err = k8sClient.Create(context.TODO(), preFlightPod1)
	require.NoError(t, err)

	preflightDS.Status.DesiredNumberScheduled = int32(1)
	err = k8sClient.Status().Update(context.TODO(), preflightDS)
	require.NoError(t, err)

	recorder := record.NewFakeRecorder(100)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.Validate(cluster)
	require.NoError(t, err)
	require.Contains(t, cluster.Annotations[pxutil.AnnotationMiscArgs], "-T px-storev2")

	actual, err := driver.GetStoragePodSpec(cluster, "testNode")
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.Contains(t, strings.Join(actual.Containers[0].Args, " "), "-cloud_provider "+cloudops.Azure)
	require.Contains(t, strings.Join(actual.Containers[0].Args, " "), "-metadata "+DefCmetaAzure)

	require.NotEmpty(t, recorder.Events)
	<-recorder.Events // Pop first event which is Default telemetry enabled event
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v %s", v1.EventTypeNormal, util.PassPreFlight, "Enabling PX-StoreV2"))
	require.Contains(t, *cluster.Spec.CloudStorage.SystemMdDeviceSpec, DefCmetaAzure)
}

func TestShouldPreflightRun(t *testing.T) {
	driver := portworx{}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}

	maxStorageNodes := uint32(1)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.CloudStorage.MaxStorageNodes = &maxStorageNodes
	k8sClient := testutil.FakeK8sClient(cluster)

	// TestCase: aws cloud provider with image < 3.0
	logrus.Infof("check aws cloud w/PX < 3.0...")
	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-1"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))

	cluster.Spec.Image = "portworx/oci-image:2.9.0"

	err := preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.False(t, driver.preflightShouldRun(cluster))
	logrus.Infof("aws cloud w/PX < 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: aws cloud provider with image >= 3.0
	logrus.Infof("check aws cloud w/PX >= 3.0...")
	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.True(t, driver.preflightShouldRun(cluster))
	logrus.Infof("aws cloud w/PX >= 3.0, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Vsphere cloud provider with image <= 3.0
	logrus.Infof("check vsphere cloud w/PX <= 3.0...")
	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	// force vsphere
	env := make([]v1.EnvVar, 2)
	env[0].Name = "VSPHERE_VCENTER"
	env[0].Value = "some.vcenter.server.com"
	cluster.Spec.Env = env

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.False(t, driver.preflightShouldRun(cluster))
	logrus.Infof("vshpere cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Vsphere cloud provider with image >= 3.1
	logrus.Infof("check vsphere cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.True(t, driver.preflightShouldRun(cluster))
	logrus.Infof("vshpere cloud w/PX >= 3.1, preflight will run")

	// TestCase: Vsphere cloud provider with Install mode 'local'
	logrus.Infof("check vsphere cloud w/local install mode...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"
	env[1].Name = "VSPHERE_INSTALL_MODE"
	env[1].Value = "local"
	cluster.Spec.Env = env

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.False(t, driver.preflightShouldRun(cluster))
	logrus.Infof("vshpere cloud w/local install mode will not run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
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

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.False(t, driver.preflightShouldRun(cluster))
	logrus.Infof("Pure cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Pure cloud provider with image >= 3.1
	logrus.Infof("check Pure cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.True(t, driver.preflightShouldRun(cluster))
	logrus.Infof("Pure cloud w/PX >= 3.1, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Azure cloud provider with image <= 3.0
	logrus.Infof("check Azure cloud w/PX <= 3.0...")
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-1"}},
	}}

	versionClient := fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.26.5",
	}
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient = testutil.FakeK8sClient(fakeK8sNodes)

	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	c := preflight.Instance()
	require.Equal(t, cloudops.Azure, c.ProviderName()) // Make sure Azure

	setPortworxStorageSpecDefaults(cluster)
	require.False(t, driver.preflightShouldRun(cluster))
	logrus.Infof("Azure cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Azure cloud provider with image >= 3.1
	logrus.Infof("check Azure cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	setPortworxStorageSpecDefaults(cluster)
	require.True(t, driver.preflightShouldRun(cluster))
	logrus.Infof("Pure Azure w/PX >= 3.1, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
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

	setPortworxStorageSpecDefaults(cluster)
	logrus.Infof("vsphere cloud with PKS and PX >= 3.1 will not run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

}

func TestPreflightAnnotations(t *testing.T) {
	driver := portworx{}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}

	maxStorageNodes := uint32(1)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.CloudStorage.MaxStorageNodes = &maxStorageNodes
	k8sClient := testutil.FakeK8sClient(cluster)

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
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok := cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	logrus.Infof("aws cloud w/PX < 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: aws cloud provider with image >= 3.0
	logrus.Infof("check aws cloud w/PX >= 3.0...")
	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "true", check)
	logrus.Infof("aws cloud w/PX >= 3.0, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Vsphere cloud provider with image <= 3.0
	logrus.Infof("check vsphere cloud w/PX <= 3.0...")
	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	// force vsphere
	env := make([]v1.EnvVar, 2)
	env[0].Name = "VSPHERE_VCENTER"
	env[0].Value = "some.vcenter.server.com"
	cluster.Spec.Env = env

	require.Equal(t, string(cloudops.Vsphere), pxutil.GetCloudProvider(cluster)) // Make sure Vsphere

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	logrus.Infof("vshpere cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Vsphere cloud provider with image >= 3.1
	logrus.Infof("check vsphere cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "true", check)
	logrus.Infof("vshpere cloud w/PX >= 3.1, preflight will run")

	// TestCase: Vsphere cloud provider with Install mode 'local'
	logrus.Infof("check vsphere cloud w/local install mode...")
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	cluster.Spec.Image = "portworx/oci-image:3.1.0"
	env[1].Name = "VSPHERE_INSTALL_MODE"
	env[1].Value = "local"
	cluster.Spec.Env = env

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	logrus.Infof("vshpere cloud w/local install mode will not run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
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

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	logrus.Infof("Pure cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Pure cloud provider with image >= 3.1
	logrus.Infof("check Pure cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "true", check)
	logrus.Infof("Pure cloud w/PX >= 3.1, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Azure cloud provider with image <= 3.0
	logrus.Infof("check Azure cloud w/PX <= 3.0...")
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-1"}},
	}}

	versionClient := fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.26.5",
	}
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient = testutil.FakeK8sClient(fakeK8sNodes)

	cluster.Spec.Image = "portworx/oci-image:3.0.0"

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	c := preflight.Instance()
	require.Equal(t, cloudops.Azure, c.ProviderName()) // Make sure Azure

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	logrus.Infof("Azure cloud w/PX <= 3.0, preflight will not run")

	// Reset preflight for other tests
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Azure cloud provider with image >= 3.1
	logrus.Infof("check Azure cloud w/PX >= 3.1...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "true", check)
	logrus.Infof("Pure Azure w/PX >= 3.1, preflight will run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
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

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	logrus.Infof("vsphere cloud with PKS and PX >= 3.1 will not run")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	cluster.Spec.CloudStorage.Provider = nil
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	// TestCase: Azure cloud provider with image >= 3.1 w/skip annotation
	logrus.Infof("check Azure cloud w/PX >= 3.1 w/skip annotation...")
	cluster.Spec.Image = "portworx/oci-image:3.1.0"
	cluster.Annotations[pxutil.AnnotationPreflightCheck] = "skip"
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "skip", check)
	logrus.Infof("Pure Azure w/PX >= 3.1, preflight will run, skip exists")

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	cluster.Spec.CloudStorage.Provider = nil
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
}

func TestGetSelectorLabels(t *testing.T) {
	driver := portworx{}
	expectedLabels := map[string]string{"name": pxutil.DriverName}
	require.Equal(t, expectedLabels, driver.GetSelectorLabels())
}

func TestGetStorkDriverName(t *testing.T) {
	driver := portworx{}
	actualStorkDriverName, err := driver.GetStorkDriverName()
	require.NoError(t, err)
	require.Equal(t, storkDriverName, actualStorkDriverName)
}

func TestGetStorkEnvMap(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
	}

	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	cluster.Status.DesiredImages = &corev1.ComponentImages{
		Stork: "stork/image:2.5.0",
	}
	envVars := driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 4)
	require.Equal(t, cluster.Namespace, envVars[pxutil.EnvKeyPortworxNamespace].Value)
	require.Equal(t, component.PxAPIServiceName, envVars[pxutil.EnvKeyPortworxServiceName].Value)
	require.Equal(t, pxutil.SecurityPXSystemSecretsSecretName,
		envVars[pxutil.EnvKeyPXSharedSecret].ValueFrom.SecretKeyRef.Name)
	require.Equal(t, pxutil.SecurityAppsSecretKey,
		envVars[pxutil.EnvKeyPXSharedSecret].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, "apps.portworx.io", envVars[pxutil.EnvKeyStorkPXJwtIssuer].Value)

	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
		Image:   "stork/image:2.5.0",
	}
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 4)
	require.Len(t, envVars, 4)
	require.Equal(t, cluster.Namespace, envVars[pxutil.EnvKeyPortworxNamespace].Value)
	require.Equal(t, component.PxAPIServiceName, envVars[pxutil.EnvKeyPortworxServiceName].Value)
	require.Equal(t, pxutil.SecurityPXSystemSecretsSecretName,
		envVars[pxutil.EnvKeyPXSharedSecret].ValueFrom.SecretKeyRef.Name)
	require.Equal(t, pxutil.SecurityAppsSecretKey,
		envVars[pxutil.EnvKeyPXSharedSecret].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, "apps.portworx.io", envVars[pxutil.EnvKeyStorkPXJwtIssuer].Value)

	cluster.Spec.Image = "portworx/image:2.5.0"
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 4)
	require.Equal(t, pxutil.SecurityPXSystemSecretsSecretName,
		envVars[pxutil.EnvKeyPXSharedSecret].ValueFrom.SecretKeyRef.Name)
	require.Equal(t, pxutil.SecurityAppsSecretKey,
		envVars[pxutil.EnvKeyPXSharedSecret].ValueFrom.SecretKeyRef.Key)
	require.Equal(t, "stork.openstorage.io", envVars[pxutil.EnvKeyStorkPXJwtIssuer].Value)

	cluster.Spec.Security.Enabled = false
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 2)

	// validate the TLS related env variables are added
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			Enabled: boolPtr(false),
		},
		TLS: &corev1.TLSSpec{
			Enabled: boolPtr(true),
			RootCA: &corev1.CertLocation{
				FileName: stringPtr("somefile"),
			},
			ServerCert: &corev1.CertLocation{
				FileName: stringPtr("certfile"),
			},
			ServerKey: &corev1.CertLocation{
				FileName: stringPtr("keyfile"),
			},
		},
	}
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 5)
	require.Equal(t, cluster.Namespace, envVars[pxutil.EnvKeyPortworxNamespace].Value)
	require.Equal(t, component.PxAPIServiceName, envVars[pxutil.EnvKeyPortworxServiceName].Value)
	// PX_ENABLE_TLS env set
	require.Equal(t, pxutil.EnvKeyPortworxEnableTLS, envVars[pxutil.EnvKeyPortworxEnableTLS].Name)
	require.Equal(t, "true", envVars[pxutil.EnvKeyPortworxEnableTLS].Value)
	// PX_CA_CERT_SECRET env set
	require.Equal(t, pxutil.EnvKeyCASecretName, envVars[pxutil.EnvKeyCASecretName].Name)
	require.Equal(t, pxutil.DefaultCASecretName, envVars[pxutil.EnvKeyCASecretName].Value)
	// PX_CA_CERT_SECRET_KEY env set
	require.Equal(t, pxutil.EnvKeyCASecretKey, envVars[pxutil.EnvKeyCASecretKey].Name)
	require.Equal(t, pxutil.DefaultCASecretKey, envVars[pxutil.EnvKeyCASecretKey].Value)

	// validate commercially signed tls certificates (no CA cert supplied)
	cluster.Spec.Security.TLS.RootCA = nil
	envVars = driver.GetStorkEnvMap(cluster)
	require.Len(t, envVars, 3)
	// PX_ENABLE_TLS env set
	require.Equal(t, pxutil.EnvKeyPortworxEnableTLS, envVars[pxutil.EnvKeyPortworxEnableTLS].Name)
	require.Equal(t, "true", envVars[pxutil.EnvKeyPortworxEnableTLS].Value)
}

func TestSetDefaultsOnStorageCluster(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedPlacement := &corev1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpExists,
							},
							{
								Key:      "node-role.kubernetes.io/worker",
								Operator: v1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
		},
	}

	createTelemetrySecret(t, k8sClient, cluster.Namespace)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// Use default image from release manifest when spec.image is not set
	require.Equal(t, DefaultPortworxImage+":2.10.0", cluster.Spec.Image)
	require.Equal(t, "2.10.0", cluster.Spec.Version)
	require.Equal(t, "2.10.0", cluster.Status.Version)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(pxutil.DefaultStartPort), *cluster.Spec.StartPort)
	require.True(t, *cluster.Spec.Storage.UseAll)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// Use default image from release manifest when spec.image has empty string
	cluster.Spec.Image = "  "
	cluster.Spec.Version = "  "
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, DefaultPortworxImage+":2.10.0", cluster.Spec.Image)
	require.Equal(t, "2.10.0", cluster.Spec.Version)
	require.Equal(t, "2.10.0", cluster.Status.Version)

	// Don't use default image when spec.image has a value
	cluster.Spec.Image = "foo/image:1.0.0"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "foo/image:1.0.0", cluster.Spec.Image)
	require.Equal(t, "1.0.0", cluster.Spec.Version)
	require.Equal(t, "1.0.0", cluster.Status.Version)

	// Populate version and image tag even if not present
	cluster.Spec.Image = "test/image"
	cluster.Spec.Version = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "test/image:2.10.0", cluster.Spec.Image)
	require.Equal(t, "2.10.0", cluster.Spec.Version)
	require.Equal(t, "2.10.0", cluster.Status.Version)

	// Empty kvdb spec should still set internal kvdb as default
	cluster.Spec.Kvdb = &corev1.KvdbSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Kvdb.Internal)

	// Should not overwrite complete kvdb spec if endpoints are empty
	cluster.Spec.Kvdb = &corev1.KvdbSpec{
		AuthSecret: "test-secret",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, "test-secret", cluster.Spec.Kvdb.AuthSecret)

	// If endpoints are set don't set internal kvdb
	cluster.Spec.Kvdb = &corev1.KvdbSpec{
		Endpoints: []string{"endpoint1"},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, cluster.Spec.Kvdb.Internal)

	// Don't overwrite secrets provider if already set
	cluster.Spec.SecretsProvider = stringPtr("aws-kms")
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "aws-kms", *cluster.Spec.SecretsProvider)

	// Don't overwrite secrets provider if set to empty
	cluster.Spec.SecretsProvider = stringPtr("")
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "", *cluster.Spec.SecretsProvider)

	// Don't overwrite start port if already set
	startPort := uint32(10001)
	cluster.Spec.StartPort = &startPort
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, uint32(10001), *cluster.Spec.StartPort)

	// Do not use default storage config if cloud storage config present
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.Storage = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Storage)

	// Add default storage config if cloud storage and storage config are both present
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.Storage = &corev1.StorageSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, *cluster.Spec.Storage.UseAll)

	// Do no use default storage config if devices is not nil
	devices := make([]string, 0)
	cluster.Spec.Storage = &corev1.StorageSpec{
		Devices: &devices,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	devices = append(devices, "/dev/sda")
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	// Do not set useAll if useAllWithPartitions is true
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Storage.UseAll)

	// Should set useAll if useAllWithPartitions is false
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(false),
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, *cluster.Spec.Storage.UseAll)

	// Do not change useAll if already has a value
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll: boolPtr(false),
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, *cluster.Spec.Storage.UseAll)

	// Add default placement if node placement is nil
	cluster.Spec.Placement = &corev1.PlacementSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// By default monitoring is not enabled
	require.Nil(t, cluster.Spec.Monitoring.EnableMetrics)

	// If metrics was enabled previosly, enable it in prometheus spec
	// and remove the enableMetrics config
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		EnableMetrics: boolPtr(true),
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Monitoring.Prometheus.ExportMetrics)
	require.Nil(t, cluster.Spec.Monitoring.EnableMetrics)

	// If prometheus is enabled but metrics is explicitly disabled,
	// then do no enable it and remove it from config
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		EnableMetrics: boolPtr(false),
		Prometheus: &corev1.PrometheusSpec{
			Enabled: true,
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, cluster.Spec.Monitoring.Prometheus.ExportMetrics)
	require.Nil(t, cluster.Spec.Monitoring.EnableMetrics)
}

func TestSetDefaultsOnStorageClusterOnEKS(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// TestCase: default cloud provider
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	_, ok := cluster.Annotations[pxutil.AnnotationIsEKS]
	require.False(t, ok)
	check, ok := cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)
	require.NotNil(t, cluster.Spec.Storage)
	require.Nil(t, cluster.Spec.CloudStorage)

	// TestCase: test eks on PX version < 3.0
	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-3"}},
	}}
	versionClient := fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.14-eks-ba74326",
	}
	coreops.SetInstance(coreops.New(versionClient))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
	cluster.Spec = corev1.StorageClusterSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	_, ok = cluster.Annotations[pxutil.AnnotationIsEKS]
	require.True(t, ok)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "false", check)

	delete(cluster.Annotations, pxutil.AnnotationPreflightCheck)
	cluster.Spec.Image = "portworx/oci-image:3.0.0"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	check, ok = cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)
	require.Equal(t, "true", check)

	// Check cloud storage spec
	require.Nil(t, cluster.Spec.Storage)
	require.NotNil(t, cluster.Spec.CloudStorage.DeviceSpecs)
	expectedCloudStorageDevice := "type=gp3,size=150"
	require.Contains(t, *cluster.Spec.CloudStorage.DeviceSpecs, expectedCloudStorageDevice)
	require.True(t, cluster.Spec.Kvdb.Internal)

	// Check cloud storage spec when capacity spec specified
	cluster.Spec = corev1.StorageClusterSpec{
		CloudStorage: &corev1.CloudStorageSpec{
			CapacitySpecs: []corev1.CloudStorageCapacitySpec{
				{
					MinCapacityInGiB: 500,
					MinIOPS:          1000,
				},
				{
					MinCapacityInGiB: 700,
					Options: map[string]string{
						"type": "io1",
					},
				},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Storage)
	require.NotNil(t, cluster.Spec.CloudStorage)
	require.Empty(t, cluster.Spec.CloudStorage.DeviceSpecs)
	require.Len(t, cluster.Spec.CloudStorage.CapacitySpecs, 2)
	require.Equal(t, "gp3", cluster.Spec.CloudStorage.CapacitySpecs[0].Options["type"])
	require.Equal(t, "io1", cluster.Spec.CloudStorage.CapacitySpecs[1].Options["type"])

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
}

func TestStorageClusterPlacementDefaults(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.23.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	k8sClient := testutil.FakeK8sClient()

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// TestCase: placement spec below k8s 1.24
	expectedPlacement := &corev1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpExists,
							},
							{
								Key:      "node-role.kubernetes.io/worker",
								Operator: v1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// TestCase: placement above k8s 1.24
	expectedPlacement = &corev1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
							{
								Key:      "node-role.kubernetes.io/control-plane",
								Operator: v1.NodeSelectorOpDoesNotExist,
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpExists,
							},
							{
								Key:      "node-role.kubernetes.io/worker",
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/control-plane",
								Operator: v1.NodeSelectorOpExists,
							},
							{
								Key:      "node-role.kubernetes.io/worker",
								Operator: v1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
		},
	}
	cluster.Spec.Placement = nil
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.24.0",
	}
	driver.k8sVersion, _ = k8sutil.GetVersion()
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestSetDefaultsOnStorageClusterWithPortworxDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "true",
			},
		},
	}

	// No defaults should be set
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, corev1.StorageClusterSpec{}, cluster.Spec)

	// Use default component versions if components are enabled
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)
}

func TestStorageClusterDefaultsForLighthouse(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{
		k8sClient: testutil.FakeK8sClient(),
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable lighthouse if nothing specified in the user interface spec
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)

	// Don't use default Lighthouse image if disabled
	// Also reset lockImage flag if Lighthouse is disabled
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled:   false,
		LockImage: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.False(t, cluster.Spec.UserInterface.LockImage)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)

	// Use image from release manifest if no image present
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Use given spec image if specified and reset desired image in status
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
		Image:   "custom/lighthouse-image:1.2.3",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "custom/lighthouse-image:1.2.3", cluster.Spec.UserInterface.Image)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)
	require.False(t, cluster.Spec.UserInterface.LockImage)

	// Reset lockImage flag even when spec image is set as it is deprecated
	cluster.Spec.UserInterface.LockImage = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, cluster.Spec.UserInterface.LockImage)
	require.Equal(t, "custom/lighthouse-image:1.2.3", cluster.Spec.UserInterface.Image)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)

	// Use image from release manifest if spec image is reset
	cluster.Spec.UserInterface.Image = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Use image from release manifest if desired was reset
	cluster.Status.DesiredImages.UserInterface = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)

	// Do not overwrite desired lighthouse image even if
	// some other component has changed
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.UserInterface = "portworx/px-lighthouse:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)

	// Don't change desired image if auto update of components is not set
	cluster.Spec.AutoUpdateComponents = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:old", cluster.Status.DesiredImages.UserInterface)

	// For upgrades from existing cluster, if image is not locked
	// we need to reset it the first time as previously it was
	// overwritten by the operator.
	// Resetting status.version to simulate first run by the operator
	cluster.Status.Version = ""
	cluster.Spec.UserInterface.Image = "portworx/px-lighthouse:existing"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.UserInterface.Image)
	require.Equal(t, "portworx/px-lighthouse:"+newCompVersion(), cluster.Status.DesiredImages.UserInterface)

	// Reset desired image if lighthouse has been disabled
	cluster.Spec.UserInterface.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.UserInterface)
}

func TestStorageClusterDefaultsForPxRepo(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable by default
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo)
	require.Empty(t, cluster.Status.DesiredImages.PxRepo)

	// Don't use default image if disabled
	cluster.Spec.PxRepo = &corev1.PxRepoSpec{
		Enabled: false,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Empty(t, cluster.Status.DesiredImages.PxRepo)

	// Use image from release manifest if no image present
	cluster.Spec.PxRepo.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:"+compVersion(), cluster.Status.DesiredImages.PxRepo)

	// Use given spec image if specified and reset desired image in status
	cluster.Spec.PxRepo = &corev1.PxRepoSpec{
		Enabled: true,
		Image:   "custom/pxrepo-image:1.2.3",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "custom/pxrepo-image:1.2.3", cluster.Spec.PxRepo.Image)
	require.Empty(t, cluster.Status.DesiredImages.PxRepo)

	// Use image from release manifest if spec image is reset
	cluster.Spec.PxRepo.Image = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:"+compVersion(), cluster.Status.DesiredImages.PxRepo)

	// Use image from release manifest if desired was reset
	cluster.Status.DesiredImages.PxRepo = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-repo:"+compVersion(), cluster.Status.DesiredImages.PxRepo)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.PxRepo = "portworx/px-repo:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-repo:old", cluster.Status.DesiredImages.PxRepo)

	// Do not overwrite desired image even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-repo:old", cluster.Status.DesiredImages.PxRepo)

	// Change desired image if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.PxRepo = "portworx/px-repo:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:"+newCompVersion(), cluster.Status.DesiredImages.PxRepo)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.PxRepo = "portworx/px-repo:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:"+newCompVersion(), cluster.Status.DesiredImages.PxRepo)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.PxRepo = "portworx/px-repo:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:"+compVersion(), cluster.Status.DesiredImages.PxRepo)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.PxRepo = "portworx/px-repo:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:"+newCompVersion(), cluster.Status.DesiredImages.PxRepo)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.PxRepo = "portworx/px-repo:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:old", cluster.Status.DesiredImages.PxRepo)

	// Don't change desired image if auto update of components is not set
	cluster.Spec.AutoUpdateComponents = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.PxRepo.Image)
	require.Equal(t, "portworx/px-repo:old", cluster.Status.DesiredImages.PxRepo)

	// Reset desired image if component is disabled
	cluster.Spec.PxRepo.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.PxRepo)
}

func TestStorageClusterDefaultsForAutopilot(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable autopilot if nothing specified in the autopilot spec
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)

	// Don't use default Autopilot image if disabled
	// Also reset lockImage flag if Autopilot is disabled
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled:   false,
		LockImage: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.False(t, cluster.Spec.Autopilot.LockImage)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)

	// Use image from release manifest if no image present
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Use given spec image if specified and reset desired image in status
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: true,
		Image:   "custom/autopilot-image:1.2.3",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "custom/autopilot-image:1.2.3", cluster.Spec.Autopilot.Image)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)
	require.False(t, cluster.Spec.Autopilot.LockImage)

	// Reset lockImage flag even when spec image is set as it is deprecated
	cluster.Spec.Autopilot.LockImage = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, cluster.Spec.Autopilot.LockImage)
	require.Equal(t, "custom/autopilot-image:1.2.3", cluster.Spec.Autopilot.Image)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)

	// Use image from release manifest if spec image is reset
	cluster.Spec.Autopilot.Image = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Use image from release manifest if desired was reset
	cluster.Status.DesiredImages.Autopilot = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)

	// Do not overwrite desired autopilot image even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+compVersion(), cluster.Status.DesiredImages.Autopilot)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Autopilot = "portworx/autopilot:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)

	// Don't change desired image if auto update of components is not set
	cluster.Spec.AutoUpdateComponents = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:old", cluster.Status.DesiredImages.Autopilot)

	// For upgrades from existing cluster, if image is not locked
	// we need to reset it the first time as previously it was
	// overwritten by the operator.
	// Resetting status.version to simulate first run by the operator
	cluster.Status.Version = ""
	cluster.Spec.Autopilot.Image = "portworx/autopilot:existing"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Autopilot.Image)
	require.Equal(t, "portworx/autopilot:"+newCompVersion(), cluster.Status.DesiredImages.Autopilot)

	// Reset desired image if autopilot has been disabled
	cluster.Spec.Autopilot.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Autopilot)

	// Check default autopilot provider is set if not specified
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	providers := cluster.Spec.Autopilot.Providers
	require.Equal(t, 1, len(providers))
	require.Equal(t, "prometheus", providers[0].Type)
	require.Equal(t, component.AutopilotDefaultProviderEndpoint, providers[0].Params["url"])
}

func TestStorageClusterDefaultsForStork(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Stork should be enabled by default
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Stork.Enabled)

	// Don't use default Stork image if disabled
	// Also reset lockImage flag if Stork is disabled
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled:   false,
		LockImage: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.False(t, cluster.Spec.Stork.LockImage)
	require.Empty(t, cluster.Status.DesiredImages.Stork)

	// Use image from release manifest if no image present
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Use given spec image if specified and reset desired image in status
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
		Image:   "custom/stork-image:1.2.3",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "custom/stork-image:1.2.3", cluster.Spec.Stork.Image)
	require.Empty(t, cluster.Status.DesiredImages.Stork)
	require.False(t, cluster.Spec.Stork.LockImage)

	// Reset lockImage flag even when spec image is set as it is deprecated
	cluster.Spec.Stork.LockImage = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, cluster.Spec.Stork.LockImage)
	require.Equal(t, "custom/stork-image:1.2.3", cluster.Spec.Stork.Image)
	require.Empty(t, cluster.Status.DesiredImages.Stork)

	// Use image from release manifest if spec image is reset
	cluster.Spec.Stork.Image = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Use image from release manifest if desired was reset
	cluster.Status.DesiredImages.Stork = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)

	// Do not overwrite desired stork image even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Change desired image if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+compVersion(), cluster.Status.DesiredImages.Stork)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Stork = "openstorage/stork:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)

	// Don't change desired image if auto update of components is not set
	cluster.Spec.AutoUpdateComponents = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:old", cluster.Status.DesiredImages.Stork)

	// For upgrades from existing cluster, if image is not locked
	// we need to reset it the first time as previously it was
	// overwritten by the operator.
	// Resetting status.version to simulate first run by the operator
	cluster.Status.Version = ""
	cluster.Spec.Stork.Image = "openstorage/stork:existing"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Stork.Image)
	require.Equal(t, "openstorage/stork:"+newCompVersion(), cluster.Status.DesiredImages.Stork)

	// Reset desired image if stork has been disabled
	cluster.Spec.Stork.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Stork)
}

func TestStorageClusterDefaultsForCSI(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.12.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.9.0.1",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// Simulate DesiredImages.CSISnapshotController being empty for old operator version w/o this image
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	// SnapshotController image should be empty
	require.Empty(t, cluster.Status.DesiredImages.CSISnapshotController)

	// Enable CSI by default for a new install.
	// Enable Snapshot controller, desired image should be set
	trueBool := true
	cluster.Spec.CSI.InstallSnapshotController = &trueBool
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, pxutil.IsCSIEnabled(cluster))
	require.NotEmpty(t, cluster.Status.DesiredImages.CSIProvisioner)
	require.NotEmpty(t, cluster.Status.DesiredImages.CSISnapshotController)

	// Feature gate should not be set when CSI is enabled
	require.NotContains(t, cluster.Spec.FeatureGates, string(pxutil.FeatureCSI))

	// Don't enable CSI by default for existing cluster
	cluster.Spec.FeatureGates = nil
	cluster.Spec.CSI = nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, pxutil.IsCSIEnabled(cluster))
	require.Empty(t, cluster.Status.DesiredImages.CSIProvisioner)

	// Enable CSI if running in k3s cluster
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.18.4+k3s",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, pxutil.IsCSIEnabled(cluster))
	require.NotEmpty(t, cluster.Status.DesiredImages.CSIProvisioner)

	// Snapshot controller and CRDs not installed by default
	require.Nil(t, cluster.Spec.CSI.InstallSnapshotController)

	// Use images from release manifest if enabled
	cluster.Spec.FeatureGates = map[string]string{
		string(pxutil.FeatureCSI): "true",
	}
	cluster.Spec.CSI.InstallSnapshotController = boolPtr(true)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)
	require.Equal(t, "quay.io/k8scsi/csi-node-driver-registrar:v1.2.3",
		cluster.Status.DesiredImages.CSINodeDriverRegistrar)
	require.Equal(t, "quay.io/k8scsi/driver-registrar:v1.2.3",
		cluster.Status.DesiredImages.CSIDriverRegistrar)
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v1.2.3",
		cluster.Status.DesiredImages.CSIAttacher)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v1.2.3",
		cluster.Status.DesiredImages.CSIResizer)
	require.Equal(t, "quay.io/k8scsi/csi-snapshotter:v1.2.3",
		cluster.Status.DesiredImages.CSISnapshotter)
	require.Equal(t, "quay.io/k8scsi/snapshot-controller:v1.2.3",
		cluster.Status.DesiredImages.CSISnapshotController)

	// Use images from release manifest if desired was reset
	cluster.Status.DesiredImages.CSIProvisioner = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Do not overwrite desired images if nothing has changed
	cluster.Status.DesiredImages.CSIProvisioner = "k8scsi/csi-provisioner:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "k8scsi/csi-provisioner:old",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Do not overwrite desired images even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "k8scsi/csi-provisioner:old",
		cluster.Status.DesiredImages.CSIProvisioner)
	require.Equal(t, "portworx/px-lighthouse:2.3.4",
		cluster.Status.DesiredImages.UserInterface)

	// Change desired images if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.CSIProvisioner = "k8scsi/csi-provisioner:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Change desired images if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.CSIProvisioner = "k8scsi/csi-provisioner:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v1.2.3",
		cluster.Status.DesiredImages.CSIProvisioner)

	// Reset desired images if CSI has been disabled
	cluster.Spec.CSI.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.CSIProvisioner)
	require.Empty(t, cluster.Status.DesiredImages.CSIAttacher)
	require.Empty(t, cluster.Status.DesiredImages.CSIDriverRegistrar)
	require.Empty(t, cluster.Status.DesiredImages.CSINodeDriverRegistrar)
	require.Empty(t, cluster.Status.DesiredImages.CSIResizer)
	require.Empty(t, cluster.Status.DesiredImages.CSISnapshotter)

	// If CSI feature flag is true and CSI Spec is false, honor feature flag true
	cluster.Spec.CSI.Enabled = false
	cluster.Spec.FeatureGates = map[string]string{
		string(pxutil.FeatureCSI): "true",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, pxutil.IsCSIEnabled(cluster))
	require.True(t, cluster.Spec.CSI.Enabled)
	require.NotContains(t, cluster.Spec.FeatureGates, pxutil.FeatureCSI)

	// If CSI feature flag is false and CSI Spec is false, honor feature flag false
	cluster.Spec.CSI.Enabled = false
	cluster.Spec.FeatureGates = map[string]string{
		string(pxutil.FeatureCSI): "false",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, pxutil.IsCSIEnabled(cluster))
	require.False(t, cluster.Spec.CSI.Enabled)
	require.NotContains(t, cluster.Spec.FeatureGates, pxutil.FeatureCSI)

	// If CSI feature flag is true and CSI Spec is empty, honor feature flag true and create CSI spec.
	cluster.Spec.CSI = nil
	cluster.Spec.FeatureGates = map[string]string{
		string(pxutil.FeatureCSI): "true",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, pxutil.IsCSIEnabled(cluster))
	require.True(t, cluster.Spec.CSI.Enabled)
	require.Nil(t, cluster.Spec.FeatureGates)

	cluster.Spec.CSI = nil
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.25.0",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.CSI)

	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.26.0",
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.CSI.Enabled)
}

func TestStorageClusterDefaultsForPrometheus(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Don't enable prometheus if monitoring spec is nil
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)

	// Don't enable prometheus if prometheus spec is nil
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)

	// Don't enable prometheus if nothing specified in prometheus spec
	cluster.Spec.Monitoring.Prometheus = &corev1.PrometheusSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)

	// Use images from release manifest if enabled
	cluster.Spec.Monitoring.Prometheus.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/prometheus/prometheus:v1.2.3",
		cluster.Status.DesiredImages.Prometheus)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)
	require.Equal(t, "quay.io/coreos/prometheus-config-reloader:v1.2.3",
		cluster.Status.DesiredImages.PrometheusConfigReloader)
	require.Equal(t, "quay.io/coreos/configmap-reload:v1.2.3",
		cluster.Status.DesiredImages.PrometheusConfigMapReload)

	// Use images from release manifest if desired was reset
	cluster.Status.DesiredImages.PrometheusOperator = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Do not overwrite desired images if nothing has changed
	cluster.Status.DesiredImages.PrometheusOperator = "coreos/prometheus-operator:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "coreos/prometheus-operator:old",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Do not overwrite desired images even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "coreos/prometheus-operator:old",
		cluster.Status.DesiredImages.PrometheusOperator)
	require.Equal(t, "portworx/px-lighthouse:2.3.4",
		cluster.Status.DesiredImages.UserInterface)

	// Change desired images if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.PrometheusOperator = "coreos/prometheus-operator:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Change desired images if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.PrometheusOperator = "coreos/prometheus-operator:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/coreos/prometheus-operator:v1.2.3",
		cluster.Status.DesiredImages.PrometheusOperator)

	// Reset desired images if prometheus has been disabled
	cluster.Spec.Monitoring.Prometheus.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.PrometheusOperator)
	require.Empty(t, cluster.Status.DesiredImages.PrometheusConfigReloader)
	require.Empty(t, cluster.Status.DesiredImages.PrometheusConfigMapReload)
}

func TestStorageClusterDefaultsForAlertManager(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.8.0",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// Don't enable alert manager if monitoring spec is nil
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring)
	require.Empty(t, cluster.Status.DesiredImages.AlertManager)

	// Don't enable alert manager if prometheus spec is nil
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.AlertManager)

	// Don't enable alert manager if alert manager spec is nil
	cluster.Spec.Monitoring.Prometheus = &corev1.PrometheusSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus.AlertManager)
	require.Empty(t, cluster.Status.DesiredImages.AlertManager)

	// Don't enable alert manager if nothing specified in alert manager spec
	cluster.Spec.Monitoring.Prometheus.AlertManager = &corev1.AlertManagerSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus.AlertManager)
	require.Empty(t, cluster.Status.DesiredImages.AlertManager)

	// Use images from release manifest if enabled
	cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/prometheus/alertmanager:v1.2.3",
		cluster.Status.DesiredImages.AlertManager)

	// Use images from release manifest if desired was reset
	cluster.Status.DesiredImages.AlertManager = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/prometheus/alertmanager:v1.2.3",
		cluster.Status.DesiredImages.AlertManager)

	// Do not overwrite desired images if nothing has changed
	cluster.Status.DesiredImages.AlertManager = "prometheus/alertmanager:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "prometheus/alertmanager:old",
		cluster.Status.DesiredImages.AlertManager)

	// Do not overwrite desired images even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "prometheus/alertmanager:old",
		cluster.Status.DesiredImages.AlertManager)
	require.Equal(t, "portworx/px-lighthouse:2.3.4",
		cluster.Status.DesiredImages.UserInterface)

	// Change desired images if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.AlertManager = "prometheus/alertmanager:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/prometheus/alertmanager:v1.2.3",
		cluster.Status.DesiredImages.AlertManager)

	// Change desired images if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.AlertManager = "prometheus/alertmanager:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "quay.io/prometheus/alertmanager:v1.2.3",
		cluster.Status.DesiredImages.AlertManager)

	// Reset desired images if alert manager has been disabled
	cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.AlertManager)
}

func TestStorageClusterDefaultsForGrafana(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.8.0",
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// Don't enable grafana by default
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring)
	require.Empty(t, cluster.Status.DesiredImages.Grafana)

	// Don't enable grafana if prometheus spec is nil
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Prometheus)
	require.Empty(t, cluster.Status.DesiredImages.Grafana)

	// Don't enable grafana if grafana spec is nil
	cluster.Spec.Monitoring.Prometheus = &corev1.PrometheusSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Grafana)
	require.Empty(t, cluster.Status.DesiredImages.Grafana)

	// Don't enable grafana if nothing specified in grafana spec
	cluster.Spec.Monitoring.Grafana = &corev1.GrafanaSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Grafana)
	require.Empty(t, cluster.Status.DesiredImages.Grafana)

	// Use images from release manifest if enabled
	cluster.Spec.Monitoring.Prometheus.Enabled = true
	cluster.Spec.Monitoring.Grafana.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "docker.io/grafana/grafana:v1.2.3",
		cluster.Status.DesiredImages.Grafana)

	// Use images from release manifest if desired was reset
	cluster.Status.DesiredImages.Grafana = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "docker.io/grafana/grafana:v1.2.3",
		cluster.Status.DesiredImages.Grafana)

	// Do not overwrite desired images if nothing has changed
	cluster.Status.DesiredImages.Grafana = "grafana/grafana:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "grafana/grafana:old",
		cluster.Status.DesiredImages.Grafana)

	// Do not overwrite desired images even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "grafana/grafana:old",
		cluster.Status.DesiredImages.Grafana)
	require.Equal(t, "portworx/px-lighthouse:2.3.4",
		cluster.Status.DesiredImages.UserInterface)

	// Change desired images if px image is not set (new cluster)
	cluster.Spec.Image = ""
	cluster.Status.DesiredImages.Grafana = "grafana/grafana:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "docker.io/grafana/grafana:v1.2.3",
		cluster.Status.DesiredImages.Grafana)

	// Change desired images if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Status.DesiredImages.Grafana = "grafana/grafana:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "docker.io/grafana/grafana:v1.2.3",
		cluster.Status.DesiredImages.Grafana)

	// Reset desired images if grafana has been disabled
	cluster.Spec.Monitoring.Grafana.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Grafana)
}

func TestStorageClusterDefaultsForNodeSpecsWithStorage(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Node specs should be nil if already nil
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Nodes)

	// Node specs should be empty if already empty
	cluster.Spec.Nodes = make([]corev1.NodeSpec, 0)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Len(t, cluster.Spec.Nodes, 0)

	// Empty storage spec at node level should copy spec from cluster level
	// - If cluster level config is empty, we should use the default storage config
	cluster.Spec.Nodes = []corev1.NodeSpec{{}}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, &corev1.StorageSpec{UseAll: boolPtr(true)}, cluster.Spec.Nodes[0].Storage)

	// - If cluster level config is not empty, use it as is
	cluster.Spec.Nodes = []corev1.NodeSpec{{}}
	clusterStorageSpec := &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	cluster.Spec.Storage = clusterStorageSpec.DeepCopy()
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, clusterStorageSpec, cluster.Spec.Nodes[0].Storage)

	// Do not set node spec storage fields if not set at the cluster level
	cluster.Spec.Storage = nil
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.SystemMdDevice)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.KvdbDevice)

	// Set node spec storage fields from cluster storage spec, if empty at node level
	// If devices is set, then no need to set UseAll and UseAllWithPartitions as it
	// does not matter.
	clusterDevices := []string{"dev1", "dev2"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(false),
		UseAllWithPartitions: boolPtr(false),
		Devices:              &clusterDevices,
		ForceUseDisks:        boolPtr(true),
		JournalDevice:        stringPtr("journal"),
		SystemMdDevice:       stringPtr("metadata"),
		KvdbDevice:           stringPtr("kvdb"),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, clusterDevices, *cluster.Spec.Nodes[0].Storage.Devices)
	require.Equal(t, "journal", *cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Equal(t, "metadata", *cluster.Spec.Nodes[0].Storage.SystemMdDevice)
	require.Equal(t, "kvdb", *cluster.Spec.Nodes[0].Storage.KvdbDevice)

	// If devices is set and empty, even then no need to set UseAll and UseAllWithPartitions,
	// as devices take precedence over them.
	clusterDevices = make([]string, 0)
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
		Devices:              &clusterDevices,
		ForceUseDisks:        boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, clusterDevices, *cluster.Spec.Nodes[0].Storage.Devices)

	// If cluster devices is nil, then set UseAllWithPartitions at node level
	// if set at cluster level. Do not set UseAll as UseAllWithPartitions takes
	// precedence over it.
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
		ForceUseDisks:        boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAll)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)

	// If cluster devices is nil and UseAllWithPartitions is false, then set UseAll
	// at node level if set at cluster level.
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(false),
		ForceUseDisks:        boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)

	// If cluster devices is nil and UseAllWithPartitions is nil, then set UseAll
	// at node level if set at cluster level.
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:        boolPtr(true),
		ForceUseDisks: boolPtr(true),
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{},
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.True(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.True(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.Nil(t, cluster.Spec.Nodes[0].Storage.Devices)

	// Cache devices are present at cluster level and not node level, node spec should
	// use cluster level storage.
	cacheDevices := []string{"/dev1", "/dev2"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		CacheDevices: &cacheDevices,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.ElementsMatch(t, cacheDevices, *cluster.Spec.Nodes[0].Storage.CacheDevices)

	// Should not overwrite storage spec from cluster level, if present at node level
	nodeDevices := []string{"node-dev1", "node-dev2"}
	cluster.Spec.Nodes[0].Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(false),
		UseAllWithPartitions: boolPtr(false),
		Devices:              &nodeDevices,
		ForceUseDisks:        boolPtr(false),
		JournalDevice:        stringPtr("node-journal"),
		SystemMdDevice:       stringPtr("node-metadata"),
		KvdbDevice:           stringPtr("node-kvdb"),
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAll)
	require.False(t, *cluster.Spec.Nodes[0].Storage.UseAllWithPartitions)
	require.False(t, *cluster.Spec.Nodes[0].Storage.ForceUseDisks)
	require.ElementsMatch(t, nodeDevices, *cluster.Spec.Nodes[0].Storage.Devices)
	require.Equal(t, "node-journal", *cluster.Spec.Nodes[0].Storage.JournalDevice)
	require.Equal(t, "node-metadata", *cluster.Spec.Nodes[0].Storage.SystemMdDevice)
	require.Equal(t, "node-kvdb", *cluster.Spec.Nodes[0].Storage.KvdbDevice)
}

func TestStorageClusterDefaultsForNodeSpecsWithCloudStorage(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{k8sClient: testutil.FakeK8sClient()}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Node specs should be nil if already nil
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Nodes)

	// Node specs should be empty if already empty
	cluster.Spec.Nodes = make([]corev1.NodeSpec, 0)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Len(t, cluster.Spec.Nodes, 0)

	// Empty cloudstorage spec at node level should copy spec from cluster level
	cluster.Spec.Nodes = []corev1.NodeSpec{{}}
	clusterStorageSpec := &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			JournalDeviceSpec: stringPtr("type=journal"),
		},
	}
	cluster.Spec.CloudStorage = clusterStorageSpec.DeepCopy()
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, clusterStorageSpec.CloudStorageCommon, cluster.Spec.Nodes[0].CloudStorage.CloudStorageCommon)

	// Do not set node spec cloudstorage fields if not set at the cluster level
	cluster.Spec.CloudStorage = nil
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CloudStorage: &corev1.CloudStorageNodeSpec{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Nodes[0].CloudStorage.DeviceSpecs)
	require.Nil(t, cluster.Spec.Nodes[0].CloudStorage.JournalDeviceSpec)
	require.Nil(t, cluster.Spec.Nodes[0].CloudStorage.SystemMdDeviceSpec)
	require.Nil(t, cluster.Spec.Nodes[0].CloudStorage.KvdbDeviceSpec)
	require.Nil(t, cluster.Spec.Nodes[0].CloudStorage.MaxStorageNodesPerZonePerNodeGroup)

	// Do not set default storage spec if node cloud storage spec exists
	cluster.Spec.Storage = nil
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CloudStorage: &corev1.CloudStorageNodeSpec{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Storage)
	require.Nil(t, cluster.Spec.Nodes[0].Storage)

	// Set node spec cloudstorage fields from cluster cloudstorage spec, if empty at node level
	clusterDeviceSpecs := []string{"type=dev1", "type=dev2"}
	maxStorageNodes := uint32(3)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs:                        &clusterDeviceSpecs,
			JournalDeviceSpec:                  stringPtr("type=journal"),
			SystemMdDeviceSpec:                 stringPtr("type=metadata"),
			KvdbDeviceSpec:                     stringPtr("type=kvdb"),
			MaxStorageNodesPerZonePerNodeGroup: &maxStorageNodes,
		},
	}
	cluster.Spec.Nodes = []corev1.NodeSpec{
		{
			CloudStorage: &corev1.CloudStorageNodeSpec{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.ElementsMatch(t, clusterDeviceSpecs, *cluster.Spec.Nodes[0].CloudStorage.DeviceSpecs)
	require.Equal(t, "type=journal", *cluster.Spec.Nodes[0].CloudStorage.JournalDeviceSpec)
	require.Equal(t, "type=metadata", *cluster.Spec.Nodes[0].CloudStorage.SystemMdDeviceSpec)
	require.Equal(t, "type=kvdb", *cluster.Spec.Nodes[0].CloudStorage.KvdbDeviceSpec)
	require.Equal(t, maxStorageNodes, *cluster.Spec.Nodes[0].CloudStorage.MaxStorageNodesPerZonePerNodeGroup)

	// Should not overwrite storage spec from cluster level, if present at node level
	nodeDeviceSpecs := []string{"type=node-dev1", "type=node-dev2"}
	maxStorageNodesForNodeGroup := uint32(3)
	cluster.Spec.Nodes[0].CloudStorage = &corev1.CloudStorageNodeSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs:                        &nodeDeviceSpecs,
			JournalDeviceSpec:                  stringPtr("type=node-journal"),
			SystemMdDeviceSpec:                 stringPtr("type=node-metadata"),
			KvdbDeviceSpec:                     stringPtr("type=node-kvdb"),
			MaxStorageNodesPerZonePerNodeGroup: &maxStorageNodesForNodeGroup,
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.ElementsMatch(t, nodeDeviceSpecs, *cluster.Spec.Nodes[0].CloudStorage.DeviceSpecs)
	require.Equal(t, "type=node-journal", *cluster.Spec.Nodes[0].CloudStorage.JournalDeviceSpec)
	require.Equal(t, "type=node-metadata", *cluster.Spec.Nodes[0].CloudStorage.SystemMdDeviceSpec)
	require.Equal(t, "type=node-kvdb", *cluster.Spec.Nodes[0].CloudStorage.KvdbDeviceSpec)
	require.Equal(t, maxStorageNodesForNodeGroup, *cluster.Spec.Nodes[0].CloudStorage.MaxStorageNodesPerZonePerNodeGroup)
}

func TestStorageClusterDefaultsForPlugin(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, cluster.Status.DesiredImages.DynamicPlugin, "portworx/portworx-dynamic-plugin:1.1.0")
	require.Equal(t, cluster.Status.DesiredImages.DynamicPluginProxy, "nginxinc/nginx-unprivileged:1.25")
}

func TestStorageClusterDefaultsForWindows(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.10.0",
		},
	}

	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, cluster.Status.DesiredImages.CsiWindowsDriver, "docker.io/portworx/px-windows-csi-driver:23.8.0")
	require.Equal(t, cluster.Status.DesiredImages.CsiLivenessProbe, "docker.io/portworx/livenessprobe:v2.10.0-windows")
	require.Equal(t, cluster.Status.DesiredImages.CsiWindowsNodeRegistrar, "docker.io/portworx/csi-node-driver-registrar:v2.8.0-windows")

}

func assertDefaultSecuritySpec(t *testing.T, cluster *corev1.StorageCluster, expectAuthDefaults bool) {
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under validation = \n, %v", string(s))

	require.NotNil(t, cluster.Spec.Security)
	require.Equal(t, true, cluster.Spec.Security.Enabled)
	if expectAuthDefaults {
		require.NotNil(t, true, cluster.Spec.Security.Auth.SelfSigned.Issuer)
		require.Equal(t, "operator.portworx.io", *cluster.Spec.Security.Auth.SelfSigned.Issuer)
		require.NotNil(t, true, cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
		duration, err := pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
		require.NoError(t, err)
		require.Equal(t, 24*time.Hour, duration)
	}
}

func TestStorageClusterDefaultsForSecurity(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.1.5.1",
		},
	}

	// Security spec should be nil, as it's disabled by default
	err := driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Security)

	// when security.enabled is false, no security fields should be populated.
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: false,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Nil(t, cluster.Spec.Security.Auth)
	require.Nil(t, cluster.Spec.Security.TLS)

	// security enabled, auth & tls missing - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth and tls empty - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth:    &corev1.AuthSpec{},
		TLS:     &corev1.TLSSpec{},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth empty & tls missing - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth:    &corev1.AuthSpec{},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth missing & tls empty - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS:     &corev1.TLSSpec{},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth has empty selfsignedSpec & tls missing - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			SelfSigned: &corev1.SelfSignedSpec{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls has empty RootCA - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			RootCA: &corev1.CertLocation{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls is enabled and has empty RootCA - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			Enabled: boolPtr(true),
			RootCA:  &corev1.CertLocation{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls has empty string for RootCA filename - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			RootCA: &corev1.CertLocation{
				FileName: stringPtr(""),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls is enabled has empty string for RootCA filename - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			Enabled: boolPtr(true),
			RootCA: &corev1.CertLocation{
				FileName: stringPtr(""),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls has empty ServerCert - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			ServerCert: &corev1.CertLocation{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls enabled and has empty ServerCert - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			Enabled:    boolPtr(true),
			ServerCert: &corev1.CertLocation{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls has empty string for ServerCert filename - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			ServerCert: &corev1.CertLocation{
				FileName: stringPtr(""),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls enabled and has empty string for ServerCert filename - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			Enabled: boolPtr(true),
			ServerCert: &corev1.CertLocation{
				FileName: stringPtr(""),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls has empty ServerKey - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			ServerKey: &corev1.CertLocation{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls enabled and has empty ServerKey - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			Enabled:   boolPtr(true),
			ServerKey: &corev1.CertLocation{},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls has empty string for ServerKey filename - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			ServerKey: &corev1.CertLocation{
				FileName: stringPtr(""),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// security enabled, auth is missing, tls enabled and has empty string for ServerKey filename - Check for default auth values
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		TLS: &corev1.TLSSpec{
			Enabled: boolPtr(true),
			ServerKey: &corev1.CertLocation{
				FileName: stringPtr(""),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			SelfSigned: &corev1.SelfSignedSpec{
				Issuer: stringPtr(""),
			},
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			GuestAccess: guestAccessTypePtr(corev1.GuestAccessType("")),
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
		Auth: &corev1.AuthSpec{
			GuestAccess: nil,
		},
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	assertDefaultSecuritySpec(t, cluster, true)

	// issuer, when manually set, is not overwritten.
	cluster.Spec.Security.Auth.SelfSigned.Issuer = stringPtr("myissuer.io")
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "myissuer.io", *cluster.Spec.Security.Auth.SelfSigned.Issuer)

	// token lifetime, when manually set, is not overwritten.
	cluster.Spec.Security.Auth.SelfSigned.TokenLifetime = stringPtr("1h")
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	duration, err := pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	require.NoError(t, err)
	require.Equal(t, 1*time.Hour, duration)

	// support for extended token durations
	cluster.Spec.Security.Auth.SelfSigned.TokenLifetime = stringPtr("1y")
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	duration, err = pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	require.NoError(t, err)
	require.Equal(t, time.Hour*24*365, duration)
}

func TestSetDefaultsOnStorageClusterForOpenshift(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsOpenshift: "true",
			},
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	expectedPlacement := &corev1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/infra",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
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
								Key:      "kubernetes.io/os",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
							{
								Key:      "node-role.kubernetes.io/infra",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpExists,
							},
							{
								Key:      "node-role.kubernetes.io/worker",
								Operator: v1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(pxutil.DefaultOpenshiftStartPort), *cluster.Spec.StartPort)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestValidationsForEssentials(t *testing.T) {
	component.DeregisterAllComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{}
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "True")

	// TestCase: Should fail if px-essential secret not present
	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"should be present to deploy a Portworx Essentials cluster")

	// TestCase: Should fail if essentials user id not present
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.EssentialsSecretName,
			Namespace: "kube-system",
		},
		Data: map[string][]byte{},
	}
	err = k8sClient.Create(context.TODO(), secret)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Essentials Entitlement ID (px-essen-user-id)")

	// TestCase: Should fail if essentials user id is empty
	secret.Data[pxutil.EssentialsUserIDKey] = []byte("")
	err = k8sClient.Update(context.TODO(), secret)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Essentials Entitlement ID (px-essen-user-id)")

	// TestCase: Should fail if OSB endpoint is not present
	secret.Data[pxutil.EssentialsUserIDKey] = []byte("user-id")
	err = k8sClient.Update(context.TODO(), secret)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Portworx OSB endpoint (px-osb-endpoint)")

	// TestCase: Should fail if OSB endpoint is empty
	secret.Data[pxutil.EssentialsOSBEndpointKey] = []byte("")
	err = k8sClient.Update(context.TODO(), secret)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.Equal(t, err.Error(), "secret kube-system/px-essential "+
		"does not have Portworx OSB endpoint (px-osb-endpoint)")

	// TestCase: Should not fail if both user id and osb endpoint present
	secret.Data[pxutil.EssentialsOSBEndpointKey] = []byte("osb-endpoint")
	err = k8sClient.Update(context.TODO(), secret)
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// TestCase: Should not fail if essentials is disabled
	err = k8sClient.Delete(context.TODO(), secret)
	require.NoError(t, err)
	os.Unsetenv(pxutil.EnvKeyPortworxEssentials)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)
}

func TestValidationsForFACDTopology(t *testing.T) {
	component.DeregisterAllComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				pxutil.AnnotationFACDTopology: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "FACD_TOPOLOGY_ENABLED",
						Value: "true",
					},
				},
			},
		},
	}

	// TestCase: Should succeed for new cluster
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// Reset cluster to clean "installed" state, then add var, should fail
	cluster = &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "FACD_TOPOLOGY_ENABLED",
						Value: "true",
					},
				},
			},
		},
	}
	cluster.Status.Phase = string(corev1.ClusterStateRunning)

	// TestCase: Should fail for existing cluster
	err = driver.PreInstall(cluster)
	require.Error(t, err)
}

func TestUpdateClusterStatusFirstTime(t *testing.T) {
	driver := portworx{}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	// Status should be set to initializing if not set
	require.Equal(t, cluster.Name, cluster.Status.ClusterName)
	require.Equal(t, string(corev1.ClusterStateInit), cluster.Status.Phase)
	require.Empty(t, cluster.Status.Conditions)
}

func TestUpdateClusterStatusWithPortworxDisabled(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "True",
			},
		},
	}

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	require.Equal(t, cluster.Name, cluster.Status.ClusterName)
	require.Equal(t, string(corev1.ClusterStateInit), cluster.Status.Phase)
	require.Empty(t, cluster.Status.Conditions)

	// If portworx is disabled, change status as online
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	require.Equal(t, cluster.Name, cluster.Status.ClusterName)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
}

func TestUpdateClusterStatusMarkMigrationCompleted(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "True",
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
			Conditions: []corev1.ClusterCondition{{
				Source: pxutil.PortworxComponentName,
				Type:   corev1.ClusterConditionTypeMigration,
				Status: corev1.ClusterConditionStatusInProgress,
			}},
		},
	}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.PortworxDaemonSetName,
			Namespace: "kube-test",
		},
	}
	k8sClient := testutil.FakeK8sClient(ds)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	// Migration is still in progress, PX is online
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateInit), cluster.Status.Phase)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeMigration)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Daemonset deleted
	err = k8sClient.Delete(context.TODO(), ds)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeMigration)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
}

func TestUpdateDeprecatedClusterStatus(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Online status
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:     "cluster-id",
			Name:   "cluster-name",
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)
	cluster.Status = corev1.StorageClusterStatus{
		Phase: string(corev1.ClusterConditionStatusOnline),
	}
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Offline status
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OFFLINE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)
	cluster.Status = corev1.StorageClusterStatus{
		Phase: string(corev1.ClusterConditionStatusOffline),
	}
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)
}

func TestUpdateClusterStatus(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:   "cluster-id",
			Name: "cluster-name",
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(2)
	// Migration not completed
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeMigration,
		Status: corev1.ClusterConditionStatusPending,
	})
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, "cluster-name", cluster.Status.ClusterName)
	require.Equal(t, "cluster-id", cluster.Status.ClusterUID)
	require.Equal(t, string(corev1.ClusterStateInit), cluster.Status.Phase)
	require.Len(t, cluster.Status.Conditions, 2)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeMigration)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusPending, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)
	// Migration completed
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeMigration,
		Status: corev1.ClusterConditionStatusCompleted,
	})
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, "cluster-name", cluster.Status.ClusterName)
	require.Equal(t, "cluster-id", cluster.Status.ClusterUID)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)

	// Status Init
	expectedClusterResp.Cluster.Status = api.Status_STATUS_INIT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)

	// Status Offline
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OFFLINE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)

	// Status Error
	expectedClusterResp.Cluster.Status = api.Status_STATUS_ERROR
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)

	// Status Decommission
	expectedClusterResp.Cluster.Status = api.Status_STATUS_DECOMMISSION
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusUnknown, condition.Status)

	// Status Maintenance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_MAINTENANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Status NeedsReboot
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NEEDS_REBOOT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Status NotInQuorum
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusNotInQuorum, condition.Status)

	// Status NotInQuorumNoStorage
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusNotInQuorum, condition.Status)

	// Status Ok
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Status StorageDown
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DOWN
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Status StorageDegraded
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Status StorageRebalance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Status StorageDriveReplace
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)

	// Status Invalid
	expectedClusterResp.Cluster.Status = api.Status(9999)
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(2)
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusUnknown, condition.Status)
	// Uninstall in progress
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusInProgress,
	})
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateUninstall), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusUnknown, condition.Status)
}

func TestPortworxInstallCondition(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}
	hash := "latest-hash"

	// Add Portworx Install InProgress condition
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:     "cluster-id",
			Name:   "cluster-name",
			Status: api.Status_STATUS_INIT,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)
	node1 := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		Status:            api.Status_STATUS_INIT,
	}
	node2 := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
		Status:            api.Status_STATUS_INIT,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateInit), cluster.Status.Phase)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Equal(t, "Portworx installation completed on 0/2 nodes, 2 nodes remaining", condition.Message)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.Empty(t, condition)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	storageNode := &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeInitStatus), storageNode.Status.Phase)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeInitStatus), storageNode.Status.Phase)

	// One node becomes ready
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)
	node1.Status = api.Status_STATUS_OK
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateInit), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.NotEmpty(t, condition)
	require.Equal(t, "Portworx installation completed on 1/2 nodes, 1 nodes remaining", condition.Message)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusNotInQuorum, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.Empty(t, condition)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeInitStatus), storageNode.Status.Phase)

	// All nodes become ready
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)
	node2.Status = api.Status_STATUS_OK
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Equal(t, "Portworx installation completed on 2 nodes", condition.Message)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.Empty(t, condition)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)

	// Init a 3rd node, install condition should not be updated
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)
	node3 := &api.StorageNode{
		Id:                "node-3",
		SchedulerNodeName: "node-three",
		Status:            api.Status_STATUS_INIT,
	}
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2, node3},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Equal(t, "Portworx installation completed on 2 nodes", condition.Message)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.Empty(t, condition)
}

func TestPortworxUpdateCondition(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
			Conditions: []corev1.ClusterCondition{{
				Source: pxutil.PortworxComponentName,
				Type:   corev1.ClusterConditionTypeRuntimeState,
				Status: corev1.ClusterConditionStatusOnline,
			}},
		},
	}
	hash := "v1"

	// Create fake k8s nodes and pods
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode2 := createK8sNode("k8s-node-2", 1)
	labels := driver.GetSelectorLabels()
	labels[util.DefaultStorageClusterUniqueLabelKey] = hash
	pod1 := createStoragePod(cluster, "px-pod-1", k8sNode1.Name, labels)
	pod1.Status = v1.PodStatus{
		Conditions: []v1.PodCondition{{
			Type:   v1.PodReady,
			Status: "True",
		}},
	}
	pod2 := createStoragePod(cluster, "px-pod-2", k8sNode2.Name, labels)
	pod2.Status = pod1.Status
	err = k8sClient.Create(context.TODO(), k8sNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), k8sNode2)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), pod1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), pod2)
	require.NoError(t, err)

	// Upgrade not triggered
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:     "cluster-id",
			Name:   "cluster-name",
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(5)
	node1 := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: k8sNode1.Name,
		Status:            api.Status_STATUS_OK,
	}
	node2 := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: k8sNode2.Name,
		Status:            api.Status_STATUS_OK,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.Empty(t, condition)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.Empty(t, condition)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	storageNode := &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode1.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, hash, storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode2.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, hash, storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	// Upgrade hash to v2, bounce pod1:
	// pod1 v2 not ready, storageNode1 v1 Upgrading, node1 Online
	// pod2 v1 ready, storageNode2 v1 Online, node2 Online
	hash = "v2"
	pod1.Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
	pod1.Status.Conditions[0].Status = "False"
	err = testutil.Update(k8sClient, pod1)
	require.NoError(t, err)

	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.Empty(t, condition)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Equal(t, "Portworx update in progress, 2 nodes remaining", condition.Message)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode1.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeUpdateStatus), storageNode.Status.Phase)
	require.Equal(t, "v1", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode2.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, "v1", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	// Upgrade hash to v3, pod1 v2 is ready, bounce pod2, node2 is unavailable from sdk server
	// pod1 v2 ready, storageNode1 v2 Online, node1 Online
	// pod2 v3 not ready, storageNode2 v1 Upgrading, node2 unavailable
	hash = "v3"
	pod1.Status.Conditions[0].Status = "True"
	err = testutil.Update(k8sClient, pod1)
	require.NoError(t, err)
	pod2.Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
	pod2.Status.Conditions[0].Status = "False"
	err = testutil.Update(k8sClient, pod2)
	require.NoError(t, err)

	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.Empty(t, condition)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Equal(t, "Portworx update in progress, 2 nodes remaining", condition.Message)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode1.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, "v2", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode2.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeUpdateStatus), storageNode.Status.Phase)
	require.Equal(t, "v1", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	// pod2 v3 is ready
	// pod1 v2 ready, storageNode1 v2 Online, node1 Online
	// pod2 v3 ready, storageNode2 v3 Online, node2 Online
	pod2.Status.Conditions[0].Status = "True"
	err = testutil.Update(k8sClient, pod2)
	require.NoError(t, err)

	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.Empty(t, condition)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Equal(t, "Portworx update in progress, 1 nodes remaining", condition.Message)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode1.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, "v2", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode2.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, "v3", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	// all pods upgraded to v3 and ready, upgrade completed
	// pod1 v3 ready, storageNode1 v3 Online, node1 Online
	// pod2 v3 ready, storageNode2 v3 Online, node2 Online
	pod1.Labels[util.DefaultStorageClusterUniqueLabelKey] = hash
	err = testutil.Update(k8sClient, pod1)
	require.NoError(t, err)

	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{node1, node2},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, hash)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	require.Empty(t, condition)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	require.NotEmpty(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Equal(t, "Portworx update completed", condition.Message)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode1.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, "v3", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])

	storageNode = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, storageNode, k8sNode2.Name, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNode.Status.Phase)
	require.Equal(t, "v3", storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey])
}

func TestUpdateClusterStatusForNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "10.0.1.1",
		MgmtIp:            "10.0.1.2",
		Status:            api.Status_STATUS_NONE,
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
		DataIp:            "10.0.2.1",
		MgmtIp:            "10.0.2.2",
		Status:            api.Status_STATUS_OK,
		Pools: []*api.StoragePool{
			{
				ID:        0,
				TotalSize: 21474836480,
				Used:      10737418240,
			},
			{
				ID:        1,
				TotalSize: 21474836480,
				Used:      2147483648,
			},
		},
		NodeLabels: map[string]string{
			labelOperatingSystem: "Ubuntu 16.04.7 LTS",
			labelKernelVersion:   "4.4.0-210-generic",
		},
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Status None
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-1", nodeStatus.Status.NodeUID)
	require.NotNil(t, nodeStatus.Status.Network)
	require.Equal(t, "10.0.1.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.1.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeStateCondition, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)
	require.NotNil(t, nodeStatus.Status.Storage)
	require.Equal(t, int64(0), nodeStatus.Status.Storage.TotalSize.Value())
	require.Equal(t, int64(0), nodeStatus.Status.Storage.UsedSize.Value())
	require.False(t, *nodeStatus.Status.NodeAttributes.Storage)
	require.False(t, *nodeStatus.Status.NodeAttributes.KVDB)
	require.Empty(t, nodeStatus.Status.OperatingSystem)
	require.Empty(t, nodeStatus.Status.KernelVersion)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-2", nodeStatus.Status.NodeUID)
	require.NotNil(t, nodeStatus.Status.Network)
	require.Equal(t, "10.0.2.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.2.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeStateCondition, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1.NodeOnlineStatus, nodeStatus.Status.Conditions[0].Status)
	require.NotNil(t, nodeStatus.Status.Storage)
	require.Equal(t, int64(42949672960), nodeStatus.Status.Storage.TotalSize.Value())
	require.Equal(t, int64(12884901888), nodeStatus.Status.Storage.UsedSize.Value())
	require.True(t, true, *nodeStatus.Status.NodeAttributes.Storage)
	require.False(t, *nodeStatus.Status.NodeAttributes.KVDB)
	require.Equal(t, "Ubuntu 16.04.7 LTS", nodeStatus.Status.OperatingSystem)
	require.Equal(t, "4.4.0-210-generic", nodeStatus.Status.KernelVersion)

	// Return only one node in enumerate for future tests
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne},
	}

	// Status Init
	expectedNodeOne.Status = api.Status_STATUS_INIT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, string(corev1.ClusterStateInit), nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeInitStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Offline
	expectedNodeOne.Status = api.Status_STATUS_OFFLINE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Error
	expectedNodeOne.Status = api.Status_STATUS_ERROR
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorum
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeNotInQuorumStatus, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorumNoStorage
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeNotInQuorumStatus, nodeStatus.Status.Conditions[0].Status)

	// Status NeedsReboot
	expectedNodeOne.Status = api.Status_STATUS_NEEDS_REBOOT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Offline", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOfflineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Decommission
	expectedNodeOne.Status = api.Status_STATUS_DECOMMISSION
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Decommissioned", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDecommissionedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Maintenance
	expectedNodeOne.Status = api.Status_STATUS_MAINTENANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Maintenance", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeMaintenanceStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Ok
	expectedNodeOne.Status = api.Status_STATUS_OK
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Online", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeOnlineStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDown
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DOWN
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDegradedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDegraded
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDegradedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageRebalance
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDegradedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDriveReplace
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Degraded", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeDegradedStatus, nodeStatus.Status.Conditions[0].Status)

	// Status Invalid
	expectedNodeOne.Status = api.Status(9999)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "Unknown", nodeStatus.Status.Phase)
	require.Equal(t, corev1.NodeUnknownStatus, nodeStatus.Status.Conditions[0].Status)
}

func TestUpdateClusterStatusForNodeVersions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "test/image:1.2.3.4",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		NodeLabels: map[string]string{
			"PX Version": "5.6.7.8",
		},
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "5.6.7.8", nodeStatus.Spec.Version)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", nodeStatus.Spec.Version)

	// If the PX image does not have a tag then don't update the version
	cluster.Spec.Image = "test/image"

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", nodeStatus.Spec.Version)

	// If the PX image does not have a tag then create without version
	err = k8sClient.Delete(context.TODO(), nodeStatus)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatus = &corev1.StorageNode{}
	err = testutil.Get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, nodeStatus.Spec.Version)
}

func TestUpdateClusterStatusWithoutPortworxService(t *testing.T) {
	// Fake client without service
	k8sClient := testutil.FakeK8sClient()

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	// TestCase: No storage nodes and portworx pods exist
	err := driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Empty(t, storageNodes.Items)

	// TestCase: Portworx pods exist, but no storage nodes present
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	err = k8sClient.Create(context.TODO(), pod1)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Contains(t, err.Error(), "not found")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Empty(t, storageNodes.Items)

	// TestCase: Portworx pods and storage nodes both exist
	storageNode1 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	storageNode2 := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-2",
			Namespace: cluster.Namespace,
		},
	}
	err = k8sClient.Create(context.TODO(), storageNode1)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storageNode2)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Contains(t, err.Error(), "not found")

	// Delete extra nodes that do not have corresponding pods
	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusServiceWithoutClusterIP(t *testing.T) {
	// Fake client with a service that does not have cluster ip
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	// TestCase: No storage nodes and portworx pods exist
	err := driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to get endpoint")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Empty(t, storageNodes.Items)

	// TestCase: Portworx pod exist and storage node exist
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	err = k8sClient.Create(context.TODO(), pod)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Contains(t, err.Error(), "failed to get endpoint")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusServiceGrpcServerError(t *testing.T) {
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "127.0.0.1",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	err := k8sClient.Create(context.TODO(), pod)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "error connecting to GRPC server")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If the cluster is initializing then do not return an error on
	// grpc connection timeout
	cluster.Status.Phase = string(corev1.ClusterStateInit)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusInspectClusterFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	err = k8sClient.Create(context.TODO(), pod)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	// Error from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("InspectCurrent error")).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "InspectCurrent error")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// Nil response from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(nil, nil).
		Times(1)

	// Reset the storage node status
	storageNode = storageNodes.Items[0].DeepCopy()
	storageNode.Status = corev1.NodeStatus{}
	err = k8sClient.Status().Update(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to inspect cluster")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// Nil cluster object in the response of InspectCurrent API
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: nil,
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	// Reset the storage node status
	storageNode = storageNodes.Items[0].DeepCopy()
	storageNode.Status = corev1.NodeStatus{}
	err = k8sClient.Status().Update(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty ClusterInspect response")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusEnumerateNodesFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-1",
		},
	}
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-1",
			Namespace: cluster.Namespace,
		},
	}
	err = k8sClient.Create(context.TODO(), pod)
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), storageNode)
	require.NoError(t, err)

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// TestCase: Error from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("node Enumerate error")).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "node Enumerate error")

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Nil response from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nil, nil).
		Times(1)

	// Reset the storage node status
	storageNode = storageNodes.Items[0].DeepCopy()
	storageNode.Status = corev1.NodeStatus{}
	err = k8sClient.Status().Update(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to enumerate nodes")

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Empty list of nodes should create StorageNode objects only for matching pods
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Reset the storage node status
	storageNode = storageNodes.Items[0].DeepCopy()
	storageNode.Status = corev1.NodeStatus{}
	err = k8sClient.Status().Update(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Nil list of nodes should create StorageNode objects only for matching pods
	expectedNodeEnumerateResp.Nodes = nil
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Reset the storage node status
	storageNode = storageNodes.Items[0].DeepCopy()
	storageNode.Status = corev1.NodeStatus{}
	err = k8sClient.Status().Update(context.TODO(), storageNode)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Empty list of nodes should not create any StorageNode objects
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = k8sClient.Delete(context.TODO(), pod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// TestCase: Nil list of nodes should not create any StorageNode objects
	expectedNodeEnumerateResp.Nodes = nil
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)
}

func TestUpdateClusterStatusShouldUpdateStatusIfChanged(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_ERROR,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)

	expectedNode := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "1.1.1.1",
		Status:            api.Status_STATUS_MAINTENANCE,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNode},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	require.Equal(t, string(corev1.ClusterStateDegraded), cluster.Status.Phase)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOffline, condition.Status)
	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1.NodeMaintenanceStatus, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.NotNil(t, nodeStatusList.Items[0].Status.Network)
	require.Equal(t, "1.1.1.1", nodeStatusList.Items[0].Status.Network.DataIP)

	// Update status based on the latest object
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	expectedNode.Status = api.Status_STATUS_OK
	expectedNode.DataIp = "2.2.2.2"
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		Times(1)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	require.Equal(t, string(corev1.ClusterStateRunning), cluster.Status.Phase)
	condition = util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ClusterConditionStatusOnline, condition.Status)
	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1.NodeOnlineStatus, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.NotNil(t, nodeStatusList.Items[0].Status.Network)
	require.Equal(t, "2.2.2.2", nodeStatusList.Items[0].Status.Network.DataIP)
}

func TestUpdateClusterStatusShouldUpdateNodePhaseBasedOnConditions(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// TestCases: Portworx node present in SDK output
	// TestCase: Node does not have any condition, node state condition will be
	// used to populate the node phase
	expectedNode := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "1.1.1.1",
		Status:            api.Status_STATUS_MAINTENANCE,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNode},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeMaintenanceStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Node has another condition which is newer than when the node state
	// was last transitioned
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Sleep is needed because fake k8s client stores the timestamp at sec granularity
	time.Sleep(time.Second)
	operatorops.Instance().UpdateStorageNodeCondition(
		&storageNodes.Items[0].Status,
		&corev1.NodeCondition{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeFailedStatus,
		},
	)
	err = k8sClient.Status().Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: NodeInit condition happened at the same time as NodeState, then
	// phase should have use NodeState as that is the latest response from SDK
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	commonTime := metav1.Now()
	conditions := make([]corev1.NodeCondition, len(storageNodes.Items[0].Status.Conditions))
	for i, c := range storageNodes.Items[0].Status.Conditions {
		conditions[i] = *c.DeepCopy()
		conditions[i].LastTransitionTime = commonTime
	}
	storageNodes.Items[0].Status.Conditions = conditions
	err = k8sClient.Status().Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeMaintenanceStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: Node state has transitioned
	expectedNode.Status = api.Status_STATUS_DECOMMISSION
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	time.Sleep(time.Second)
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeDecommissionedStatus), storageNodes.Items[0].Status.Phase)

	// TestCases: Portworx node not present in SDK output, but matching pod present
	// TestCase: If latest node condition is not Init or Failed (here it is Decommissioned)
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-one",
		},
	}
	err = k8sClient.Create(context.TODO(), pod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeUnknownStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If latest node condition is Failed then phase should be Failed
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeStateCondition,
			Status: corev1.NodeFailedStatus,
		},
	}
	err = k8sClient.Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If latest node condition is NodeInit with succeeded status
	// then phase should be Init
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeSucceededStatus,
		},
	}
	err = k8sClient.Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If latest node condition does not have status, phase should be Init
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeSucceededStatus,
		},
	}
	err = k8sClient.Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If no condition present, phase should be Init
	storageNodes.Items[0].Status.Conditions = []corev1.NodeCondition{}
	err = k8sClient.Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)

	// TestCase: If conditions are nil, phase should be Init
	storageNodes.Items[0].Status.Conditions = nil
	err = k8sClient.Update(context.TODO(), &storageNodes.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodes = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodes)
	require.NoError(t, err)
	require.Len(t, storageNodes.Items, 1)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodes.Items[0].Status.Phase)
}

func TestUpdateClusterStatusWithoutSchedulerNodeName(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(0),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:       "node-uid",
				DataIp:   "1.1.1.1",
				MgmtIp:   "2.2.2.2",
				Hostname: "node-hostname",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Fake a node object without matching ip address or hostname to storage node
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Fake a node object with matching data ip address
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{
							Type:    v1.NodeInternalIP,
							Address: "1.1.1.1",
						},
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching mgmt ip address
	err = testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	require.NoError(t, err)
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{
							Type:    v1.NodeExternalIP,
							Address: "2.2.2.2",
						},
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname
	err = testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	require.NoError(t, err)
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
				},
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{
							Type:    v1.NodeHostName,
							Address: "node-hostname",
						},
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname from labels
	err = testutil.Delete(k8sClient, &nodeStatusList.Items[0])
	require.NoError(t, err)
	coreops.SetInstance(
		coreops.New(
			fakek8sclient.NewSimpleClientset(&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-name",
					Labels: map[string]string{
						"kubernetes.io/hostname": "node-hostname",
					},
				},
			}),
		),
	)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldDeleteStorageNodeForNonExistingNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Node got removed from storage driver
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldNotDeleteStorageNodeIfPodExists(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	nodeEnumerateRespWithAllNodes := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
				Status:            api.Status_STATUS_OK,
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node got removed from portworx sdk response, but corresponding pod exists
	nodeEnumerateRespWithOneNode := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	nodeOnePod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-one",
		},
	}
	err = k8sClient.Create(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeUnknownStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Initializing state; should remain in Initializing state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	// No conditions means the node is initializing state
	storageNodeList.Items[0].Status.Conditions = nil
	err = k8sClient.Update(context.TODO(), &storageNodeList.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Failed state; should remain in Failed state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	// Latest condition is in failed state
	storageNodeList.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeFailedStatus,
		},
	}
	err = k8sClient.Update(context.TODO(), &storageNodeList.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, but pod labels do not match
	// that of portworx pods
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)

	nodeOnePod.Labels = nil
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct owner references
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.Labels = driver.GetSelectorLabels()
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)
	nodeOnePod.OwnerReferences[0].UID = types.UID("dummy")
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct node name
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)
	nodeOnePod.Spec.NodeName = "dummy"
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// exist in cluster's namespace
	err = k8sClient.Delete(context.TODO(), nodeOnePod)
	require.NoError(t, err)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithOneNode, nil).
		Times(1)
	nodeOnePod.Namespace = "dummy"
	nodeOnePod.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)
}

func TestUpdateClusterStatusShouldDeleteStorageNodeIfSchedulerNodeNameNotPresent(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Scheduler node name missing for storage node
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id: "node-1",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)

	// Deleting already deleted StorageNode should not throw error
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	nodeStatusList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldNotDeleteStorageNodeIfPodExistsAndScheduleNameAbsent(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	nodeEnumerateRespWithAllNodes := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
				Status:            api.Status_STATUS_OK,
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList := &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeOnlineStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Scheduler node name missing for storage node, but corresponding pod exists
	nodeEnumerateRespWithNoSchedName := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id: "node-1",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	nodeOnePod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod-1",
			Namespace:       cluster.Namespace,
			Labels:          driver.GetSelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "node-one",
		},
	}
	err = k8sClient.Create(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeUnknownStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Initializing state; should remain in Initializing state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	// No conditions means the node is initializing state
	storageNodeList.Items[0].Status.Conditions = nil
	err = k8sClient.Update(context.TODO(), &storageNodeList.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeInitStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, corresponding pod exists,
	// but portworx is in Failed state; should remain in Failed state.
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	// Latest condition is in failed state
	storageNodeList.Items[0].Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeInitCondition,
			Status: corev1.NodeFailedStatus,
		},
	}
	err = k8sClient.Update(context.TODO(), &storageNodeList.Items[0])
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)
	require.Equal(t, string(corev1.NodeFailedStatus), storageNodeList.Items[0].Status.Phase)

	// TestCase: Node is not present in portworx sdk response, but pod labels do not match
	// that of portworx pods
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)

	nodeOnePod.Labels = nil
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct owner references
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.Labels = driver.GetSelectorLabels()
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)
	nodeOnePod.OwnerReferences[0].UID = types.UID("dummy")
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// have correct node name
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)
	nodeOnePod.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)
	nodeOnePod.Spec.NodeName = "dummy"
	err = k8sClient.Update(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)

	// TestCase: Node is not present in portworx sdk response, and pod does not
	// exist in cluster's namespace
	err = k8sClient.Delete(context.TODO(), nodeOnePod)
	require.NoError(t, err)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithAllNodes, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 2)

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nodeEnumerateRespWithNoSchedName, nil).
		Times(1)
	nodeOnePod.Namespace = "dummy"
	nodeOnePod.ResourceVersion = ""
	err = k8sClient.Create(context.TODO(), nodeOnePod)
	require.NoError(t, err)

	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	storageNodeList = &corev1.StorageNodeList{}
	err = testutil.List(k8sClient, storageNodeList)
	require.NoError(t, err)
	require.Len(t, storageNodeList.Items, 1)
}

func TestDeleteClusterWithoutDeleteStrategy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.15.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController:     "true",
				pxutil.AnnotationPodSecurityPolicy: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:     "portworx/image:2.2",
			StartPort: &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled:       true,
					ExportMetrics: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	setSecuritySpecDefaults(cluster)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

	// Install all components
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

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

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.NotEmpty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.NotEmpty(t, rbList.Items)

	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceList.Items)

	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.NotEmpty(t, dsList.Items)

	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.NotEmpty(t, deploymentList.Items)

	prometheusList := &monitoringv1.PrometheusList{}
	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.NotEmpty(t, prometheusList.Items)

	smList := &monitoringv1.ServiceMonitorList{}
	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.NotEmpty(t, smList.Items)

	prList := &monitoringv1.PrometheusRuleList{}
	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.NotEmpty(t, prList.Items)

	csiDriverList := &storagev1beta1.CSIDriverList{}
	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.NotEmpty(t, csiDriverList.Items)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	secretList := &v1.SecretList{}
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 4)

	pspList := &policyv1beta1.PodSecurityPolicyList{}
	err = testutil.List(k8sClient, pspList)
	require.NoError(t, err)
	require.Len(t, pspList.Items, 2)

	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// If no delete strategy is provided, condition should be complete
	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Message)

	// Verify that all components have been removed
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Empty(t, serviceAccountList.Items)

	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Empty(t, clusterRoleList.Items)

	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Empty(t, crbList.Items)

	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)

	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)

	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Empty(t, deploymentList.Items)

	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.Empty(t, prometheusList.Items)

	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.Empty(t, smList.Items)

	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.Empty(t, prList.Items)

	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.Empty(t, csiDriverList.Items)

	// Storage classes should not get deleted if there is no
	// delete strategy
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	// Security secret keys should not be deleted,
	// but tokens should be deleted
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 2)
	secret := &v1.Secret{}
	err = testutil.Get(k8sClient, secret, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, cluster.Namespace)
	require.NoError(t, err)
	err = testutil.Get(k8sClient, secret, pxutil.SecurityPXSystemSecretsSecretName, cluster.Namespace)
	require.NoError(t, err)

	err = testutil.List(k8sClient, pspList)
	require.NoError(t, err)
	require.Empty(t, pspList.Items)
}

func TestDeleteClusterShouldResetSDKConnection(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})
	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.25.0",
	}
	coreops.SetInstance(coreops.New(versionClient))
	// Create driver object with the fake k8s client
	driver := &portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(&api.SdkNodeEnumerateWithFiltersResponse{}, nil).
		AnyTimes()

	// Force initialize a connection to the GRPC server
	_, err = driver.GetStorageNodes(cluster)
	require.NoError(t, err)

	require.NotNil(t, driver.sdkConn)

	// SDK connection should be closed on StorageCluster deletion
	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Nil(t, driver.sdkConn)
}

func TestDeleteClusterWithUninstallStrategy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.15.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController:     "true",
				pxutil.AnnotationPodSecurityPolicy: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},

			Image:     "portworx/image:2.2",
			StartPort: &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled:       true,
					ExportMetrics: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	setSecuritySpecDefaults(cluster)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

	// Install all components
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

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

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.NotEmpty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.NotEmpty(t, rbList.Items)

	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceList.Items)

	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.NotEmpty(t, dsList.Items)

	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.NotEmpty(t, deploymentList.Items)

	prometheusList := &monitoringv1.PrometheusList{}
	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.NotEmpty(t, prometheusList.Items)

	smList := &monitoringv1.ServiceMonitorList{}
	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.NotEmpty(t, smList.Items)

	prList := &monitoringv1.PrometheusRuleList{}
	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.NotEmpty(t, prList.Items)

	csiDriverList := &storagev1beta1.CSIDriverList{}
	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.NotEmpty(t, csiDriverList.Items)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	secretList := &v1.SecretList{}
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 4)

	pspList := &policyv1beta1.PodSecurityPolicyList{}
	err = testutil.List(k8sClient, pspList)
	require.NoError(t, err)
	require.Len(t, pspList.Items, 2)

	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Message)

	// Check wiper service account
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PxNodeWiperServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Check wiper cluster role
	expectedCR := testutil.GetExpectedClusterRole(t, "nodeWiperClusterRole.yaml")
	wiperCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, wiperCR, pxNodeWiperClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, wiperCR.Name)
	require.Empty(t, wiperCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, wiperCR.Rules)

	// Check wiper cluster role binding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "nodeWiperClusterRoleBinding.yaml")
	wiperCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, wiperCRB, pxNodeWiperClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, wiperCRB.Name)
	require.Empty(t, wiperCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, wiperCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, wiperCRB.RoleRef)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiper.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)

	// Verify that all components have been removed, except node wiper
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 1)
	require.Equal(t, component.PxNodeWiperServiceAccountName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleBindingName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)

	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Len(t, dsList.Items, 1)
	require.Equal(t, pxNodeWiperDaemonSetName, dsList.Items[0].Name)

	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Empty(t, deploymentList.Items)

	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.Empty(t, prometheusList.Items)

	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.Empty(t, smList.Items)

	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.Empty(t, prList.Items)

	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.Empty(t, csiDriverList.Items)

	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)

	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Empty(t, secretList.Items)

	err = testutil.List(k8sClient, pspList)
	require.NoError(t, err)
	require.Len(t, pspList.Items, 2)
}

func TestDeleteClusterWithCustomRepoRegistry(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	customRepo := "test-registry:1111/test-repo"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRepo,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/px-node-wiper:"+newCompVersion(),
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)

	// Flat registry should be used for image
	customRepo = "test-registry:111"
	cluster.Spec.CustomImageRegistry = customRepo + "//"
	err = testutil.Delete(k8sClient, wiperDS)
	require.NoError(t, err)

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/px-node-wiper:"+newCompVersion(),
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithCustomRegistry(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	customRegistry := "test-registry:1111"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/portworx/px-node-wiper:"+newCompVersion(),
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithImagePullPolicy(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullPolicy: v1.PullIfNotPresent,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, v1.PullIfNotPresent,
		wiperDS.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)
}

func TestDeleteClusterWithImagePullSecret(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	imagePullSecret := "registry-secret"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullSecret: &imagePullSecret,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, imagePullSecret,
		wiperDS.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)
}

func TestDeleteClusterWithTolerations(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	tolerations := []v1.Toleration{
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
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Placement: &corev1.PlacementSpec{
				Tolerations: tolerations,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t,
		tolerations,
		wiperDS.Spec.Template.Spec.Tolerations,
	)
}

func TestDeleteClusterWithNodeAffinity(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Placement: &corev1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
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
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check wiper daemonset
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
	require.Equal(t, cluster.Spec.Placement.NodeAffinity, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
}

func TestDeleteClusterWithCustomNodeWiperImage(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	customRegistry := "test-registry:1111"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyNodeWiperImage,
						Value: "test/node-wiper:v1",
					},
				},
			},
		},
	}

	_, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/test/node-wiper:v1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithUninstallStrategyForPKS(t *testing.T) {
	reregisterComponents()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Message)

	// Check wiper service account
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PxNodeWiperServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Check wiper cluster role
	expectedCR := testutil.GetExpectedClusterRole(t, "nodeWiperClusterRole.yaml")
	wiperCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, wiperCR, pxNodeWiperClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, wiperCR.Name)
	require.Empty(t, wiperCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, wiperCR.Rules)

	// Check wiper cluster role binding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "nodeWiperClusterRoleBinding.yaml")
	wiperCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, wiperCRB, pxNodeWiperClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, wiperCRB.Name)
	require.Empty(t, wiperCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, wiperCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, wiperCRB.RoleRef)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiperPKS.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithUninstallAndWipeStrategy(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.15.0",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	err := createFakeCRD(fakeExtClient, "csinodeinfos.csi.storage.k8s.io")
	require.NoError(t, err)
	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)
	startPort := uint32(10001)

	pxutil.SpecsBaseDir = func() string {
		return "../../../bin/configs"
	}
	defer func() {
		pxutil.SpecsBaseDir = func() string {
			return pxutil.PortworxSpecsDir
		}
	}()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationPVCController:     "true",
				pxutil.AnnotationPodSecurityPolicy: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},

			Image:     "portworx/image:2.2",
			StartPort: &startPort,
			UserInterface: &corev1.UserInterfaceSpec{
				Enabled: true,
				Image:   "portworx/px-lighthouse:test",
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Image:   "portworx/autopilot:test",
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					Enabled:       true,
					ExportMetrics: true,
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	setSecuritySpecDefaults(cluster)
	cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

	// Install all components
	err = driver.PreInstall(cluster)
	require.NoError(t, err)

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

	roleList := &rbacv1.RoleList{}
	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.NotEmpty(t, roleList.Items)

	rbList := &rbacv1.RoleBindingList{}
	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.NotEmpty(t, rbList.Items)

	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.NotEmpty(t, serviceList.Items)

	dsList := &appsv1.DaemonSetList{}
	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.NotEmpty(t, dsList.Items)

	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.NotEmpty(t, deploymentList.Items)

	prometheusList := &monitoringv1.PrometheusList{}
	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.NotEmpty(t, prometheusList.Items)

	smList := &monitoringv1.ServiceMonitorList{}
	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.NotEmpty(t, smList.Items)

	prList := &monitoringv1.PrometheusRuleList{}
	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.NotEmpty(t, prList.Items)

	csiDriverList := &storagev1beta1.CSIDriverList{}
	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.NotEmpty(t, csiDriverList.Items)

	storageClassList := &storagev1.StorageClassList{}
	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.NotEmpty(t, storageClassList.Items)

	secretList := &v1.SecretList{}
	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Len(t, secretList.Items, 4)

	pspList := &policyv1beta1.PodSecurityPolicyList{}
	err = testutil.List(k8sClient, pspList)
	require.NoError(t, err)
	require.Len(t, pspList.Items, 2)

	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Message)

	// Check wiper service account
	sa := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, sa, component.PxNodeWiperServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, sa.OwnerReferences, 1)
	require.Equal(t, cluster.Name, sa.OwnerReferences[0].Name)

	// Check wiper cluster role
	expectedCR := testutil.GetExpectedClusterRole(t, "nodeWiperClusterRole.yaml")
	wiperCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, wiperCR, pxNodeWiperClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCR.Name, wiperCR.Name)
	require.Empty(t, wiperCR.OwnerReferences)
	require.ElementsMatch(t, expectedCR.Rules, wiperCR.Rules)

	// Check wiper cluster role binding
	expectedCRB := testutil.GetExpectedClusterRoleBinding(t, "nodeWiperClusterRoleBinding.yaml")
	wiperCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, wiperCRB, pxNodeWiperClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedCRB.Name, wiperCRB.Name)
	require.Empty(t, wiperCRB.OwnerReferences)
	require.ElementsMatch(t, expectedCRB.Subjects, wiperCRB.Subjects)
	require.Equal(t, expectedCRB.RoleRef, wiperCRB.RoleRef)

	// Check wiper daemonset
	expectedDaemonSet := testutil.GetExpectedDaemonSet(t, "nodeWiperWithWipe.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)

	// Verify that all components have been removed, except node wiper
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 1)
	require.Equal(t, component.PxNodeWiperServiceAccountName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 1)
	require.Equal(t, pxNodeWiperClusterRoleBindingName, serviceAccountList.Items[0].Name)

	err = testutil.List(k8sClient, roleList)
	require.NoError(t, err)
	require.Empty(t, roleList.Items)

	err = testutil.List(k8sClient, rbList)
	require.NoError(t, err)
	require.Empty(t, rbList.Items)

	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Empty(t, serviceList.Items)

	err = testutil.List(k8sClient, dsList)
	require.NoError(t, err)
	require.Len(t, dsList.Items, 1)
	require.Equal(t, pxNodeWiperDaemonSetName, dsList.Items[0].Name)

	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Empty(t, deploymentList.Items)

	err = testutil.List(k8sClient, prometheusList)
	require.NoError(t, err)
	require.Empty(t, prometheusList.Items)

	err = testutil.List(k8sClient, smList)
	require.NoError(t, err)
	require.Empty(t, smList.Items)

	err = testutil.List(k8sClient, prList)
	require.NoError(t, err)
	require.Empty(t, prList.Items)

	err = testutil.List(k8sClient, csiDriverList)
	require.NoError(t, err)
	require.Empty(t, csiDriverList.Items)

	err = testutil.List(k8sClient, storageClassList)
	require.NoError(t, err)
	require.Empty(t, storageClassList.Items)

	err = testutil.List(k8sClient, secretList)
	require.NoError(t, err)
	require.Empty(t, secretList.Items)

	err = testutil.List(k8sClient, pspList)
	require.NoError(t, err)
	require.Len(t, pspList.Items, 2)
}

func TestDeleteClusterWithUninstallWhenNodeWiperCreated(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	reregisterComponents()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxNodeWiperDaemonSetName,
			Namespace:       cluster.Namespace,
			UID:             types.UID("wiper-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Contains(t, condition.Message,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Contains(t, condition.Message,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Contains(t, condition.Message,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallMsg)

	// Node wiper daemon set should be removed
	dsList := &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)

	// TestCase: Wiper daemonset should not be created again if already
	// completed and deleted
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusCompleted,
	})
	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallMsg)

	dsList = &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyWhenNodeWiperCreated(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	reregisterComponents()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	clusterRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxNodeWiperDaemonSetName,
			Namespace:       cluster.Namespace,
			UID:             types.UID("wiper-ds-uid"),
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Contains(t, condition.Message,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Contains(t, condition.Message,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusInProgress, condition.Status)
	require.Contains(t, condition.Message,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	// Node wiper daemon set should be removed
	dsList := &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)

	// TestCase: Wiper daemonset should not be created again if already
	// completed and deleted
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterConditionStatusCompleted,
	})
	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	dsList = &appsv1.DaemonSetList{}
	err = k8sClient.List(context.TODO(), dsList)
	require.NoError(t, err)
	require.Empty(t, dsList.Items)
}

func TestDeleteEssentialSecret(t *testing.T) {
	testDeleteEssentialSecret(t, true)
	testDeleteEssentialSecret(t, false)
}

func testDeleteEssentialSecret(t *testing.T, wipe bool) {
	deleteType := corev1.UninstallStorageClusterStrategyType
	if wipe {
		deleteType = corev1.UninstallAndWipeStorageClusterStrategyType
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: deleteType,
			},
		},
	}
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}

	essentialSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.EssentialsSecretName,
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS, wiperPod, essentialSecret)
	driver := portworx{
		k8sClient: k8sClient,
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)

	secrets := &v1.SecretList{}
	err = testutil.List(k8sClient, secrets)
	require.NoError(t, err)
	if wipe {
		require.Empty(t, secrets.Items)
	} else {
		require.Equal(t, 1, len(secrets.Items))
	}
}

func TestReinstall(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallStorageClusterStrategyType,
			},
		},
	}

	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	etcdConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.InternalEtcdConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	cloudDriveConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.CloudDriveConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}

	component.DeregisterAllComponents()
	k8sClient := testutil.FakeK8sClient(wiperDS, wiperPod, etcdConfigMap, cloudDriveConfigMap)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
	require.NoError(t, err)

	configMaps := &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Len(t, configMaps.Items, 2)

	// Uninstall without wipe
	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallMsg)

	// Check config maps are retained
	configMaps = &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Len(t, configMaps.Items, 2)

	// Reinstall with different name should be blocked.
	origName := cluster.Name
	cluster.Name = origName + "aaa"
	err = driver.PreInstall(cluster)
	require.NotNil(t, err)

	// Reinstall with same name should work.
	cluster.Name = origName
	err = driver.PreInstall(cluster)
	require.NoError(t, err)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveConfigMaps(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	etcdConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.InternalEtcdConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	cloudDriveConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.CloudDriveConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	pureConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pureStorageCloudDriveConfigMap,
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS, wiperPod, etcdConfigMap, cloudDriveConfigMap, pureConfigMap)
	driver := portworx{
		k8sClient: k8sClient,
	}

	configMaps := &v1.ConfigMapList{}
	err := testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Len(t, configMaps.Items, 3)

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	// Check config maps are deleted
	configMaps = &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Empty(t, configMaps.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveConfigMapsWhenOverwriteClusterID(t *testing.T) {
	clusterID := "clusterID_with-special-chars"
	strippedClusterName := "clusteridwithspecialchars"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationClusterID: clusterID,
			},
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	etcdConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.InternalEtcdConfigMapPrefix + strippedClusterName,
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	cloudDriveConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.CloudDriveConfigMapPrefix + strippedClusterName,
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	pureConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pureStorageCloudDriveConfigMap,
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	k8sClient := testutil.FakeK8sClient(wiperDS, wiperPod, etcdConfigMap, cloudDriveConfigMap, pureConfigMap)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	recorder := record.NewFakeRecorder(1)
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	configMaps := &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Len(t, configMaps.Items, 3)

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	// Check config maps are deleted
	configMaps = &v1.ConfigMapList{}
	err = testutil.List(k8sClient, configMaps)
	require.NoError(t, err)
	require.Empty(t, configMaps.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveKvdbData(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd://kvdb1.com:2001",
					"etcd://kvdb2.com:2001",
				},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Test etcd v3 without http/https
	kvdbMem, err := kvdb.New(mem.Name, pxKvdbPrefix, nil, nil, kvdb.LogFatalErrorCB)
	require.NoError(t, err)
	_, err = kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	require.NoError(t, err)
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err := kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit http
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:http://kvdb1.com:2001",
		"etcd:http://kvdb2.com:2001",
	}
	_, err = kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	require.NoError(t, err)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit https
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	_, err = kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	require.NoError(t, err)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd base version
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdBaseVersion, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	_, err = kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	require.NoError(t, err)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e2.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test consul
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.ConsulVersion1, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"consul:http://kvdb1.com:2001",
		"consul:http://kvdb2.com:2001",
	}
	_, err = kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	require.NoError(t, err)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, consul.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveKvdbDataWhenOverwriteClusterID(t *testing.T) {
	clusterID := "clusterID_with-special-chars"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationClusterID: clusterID,
			},
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd://kvdb1.com:2001",
					"etcd://kvdb2.com:2001",
				},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Test etcd v3 without http/https
	kvdbMem, err := kvdb.New(mem.Name, pxKvdbPrefix, nil, nil, kvdb.LogFatalErrorCB)
	require.NoError(t, err)
	_, err = kvdbMem.Put(clusterID+"/foo", "bar", 0)
	require.NoError(t, err)
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err := kvdbMem.Get(clusterID + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Contains(t, condition.Message, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(clusterID + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)
}

func TestDeleteClusterWithUninstallWipeStrategyFailedRemoveKvdbData(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{},
			},
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Fail if no kvdb endpoints given
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return kvdb.New(mem.Name, prefix, machines, opts, kvdb.LogFatalErrorCB)
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusFailed, condition.Status)
	require.Contains(t, condition.Message, "Failed to wipe metadata")

	// Fail if unknown kvdb type given in url
	cluster.Spec.Kvdb.Endpoints = []string{"zookeeper://kvdb.com:2001"}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusFailed, condition.Status)
	require.Contains(t, condition.Message, "Failed to wipe metadata")

	// Fail if unknown kvdb version found
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "zookeeper1", nil
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusFailed, condition.Status)
	require.Contains(t, condition.Message, "Failed to wipe metadata")

	// Fail if error getting kvdb version
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "", fmt.Errorf("kvdb version error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusFailed, condition.Status)
	require.Contains(t, condition.Message, "Failed to wipe metadata")
	require.Contains(t, condition.Message, "kvdb version error")

	// Fail if error initializing kvdb
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return nil, fmt.Errorf("kvdb initialize error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusFailed, condition.Status)
	require.Contains(t, condition.Message, "Failed to wipe metadata")
	require.Contains(t, condition.Message, "kvdb initialize error")
}

func TestDeleteClusterWithPortworxDisabled(t *testing.T) {
	driver := portworx{}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				constants.AnnotationDisableStorage: "1",
			},
		},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Message)

	// Uninstall delete strategy
	cluster.Spec.DeleteStrategy.Type = corev1.UninstallStorageClusterStrategyType
	cluster.Status = corev1.StorageClusterStatus{}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, pxutil.PortworxComponentName, condition.Source)
	require.Equal(t, corev1.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1.ClusterConditionStatusCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Message)

}

func TestUpdateStorageNodeKVDB(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	clusterName := "px-cluster"
	clusterNS := "kube-system"
	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+"."+clusterNS)
	defer testutil.RestoreEtcHosts(t)

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: clusterNS,
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: clusterNS,
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	cmName := k8s.GetBootstrapConfigMapName(cluster.GetName())

	// TEST 1: Add missing KVDB condition
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()
	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-one",
		SchedulerNodeName: "node-one",
		DataIp:            "10.0.1.1",
		MgmtIp:            "10.0.1.2",
		Status:            api.Status_STATUS_NONE,
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-two",
		SchedulerNodeName: "node-two",
		DataIp:            "10.0.2.1",
		MgmtIp:            "10.0.2.2",
		Status:            api.Status_STATUS_OK,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}

	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: clusterNS,
		},
		Data: map[string]string{
			pxEntriesKey: `[{"IP":"10.0.1.2","ID":"node-one","Index":0,"State":1,"Type":1,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-1.internal.kvdb","DataDirType":"KvdbDevice"},{"IP":"10.0.2.2","ID":"node-two","Index":2,"State":2,"Type":2,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-3.internal.kvdb","DataDirType":"KvdbDevice"}]`,
		},
	}

	err = driver.k8sClient.Create(context.TODO(), cm)
	require.NoError(t, err)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(3)
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	// check if both storage nodes exist and have the KVDB condition
	for _, n := range []string{"node-one", "node-two"} {
		found := false
		checkStorageNode := &corev1.StorageNode{}
		err = driver.k8sClient.Get(context.TODO(), client.ObjectKey{
			Name:      n,
			Namespace: clusterNS,
		}, checkStorageNode)
		require.NoError(t, err)

		for _, c := range checkStorageNode.Status.Conditions {
			if c.Type == corev1.NodeKVDBCondition {
				found = true
				break
			}
		}
		require.True(t, found)
		require.True(t, *checkStorageNode.Status.NodeAttributes.KVDB)
	}

	// TEST 2: Remove KVDB condition
	cm.Data[pxEntriesKey] = `[{"IP":"10.0.1.2","ID":"node-three","Index":0,"State":3,"Type":0,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-1.internal.kvdb","DataDirType":"KvdbDevice"},{"IP":"10.0.2.2","ID":"node-four","Index":2,"State":0,"Type":2,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-3.internal.kvdb","DataDirType":"KvdbDevice"}]`
	err = driver.k8sClient.Update(context.TODO(), cm)
	require.NoError(t, err)
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
	// check if both storage nodes exist and DONT have the KVDB condition
	for _, n := range []string{"node-one", "node-two"} {
		found := false
		checkStorageNode := &corev1.StorageNode{}
		err = driver.k8sClient.Get(context.TODO(), client.ObjectKey{
			Name:      n,
			Namespace: clusterNS,
		}, checkStorageNode)
		require.NoError(t, err)

		for _, c := range checkStorageNode.Status.Conditions {
			if c.Type == corev1.NodeKVDBCondition {
				found = true
				break
			}
		}
		require.False(t, found)
		require.False(t, *checkStorageNode.Status.NodeAttributes.KVDB)
	}

	// TEST 4: Check kvdb node state translations
	kvdbNodeStateTests := []struct {
		state                   int
		nodeType                int
		expectedConditionStatus corev1.NodeConditionStatus
		expectedNodeType        string
	}{
		{
			state:                   0,
			nodeType:                0,
			expectedConditionStatus: corev1.NodeUnknownStatus,
			expectedNodeType:        "",
		},
		{
			state:                   1,
			nodeType:                1,
			expectedConditionStatus: corev1.NodeInitStatus,
			expectedNodeType:        "leader",
		},
		{
			state:                   2,
			nodeType:                1,
			expectedConditionStatus: corev1.NodeOnlineStatus,
			expectedNodeType:        "leader",
		},
		{
			state:                   3,
			nodeType:                2,
			expectedConditionStatus: corev1.NodeOfflineStatus,
			expectedNodeType:        "member",
		},
		{
			state:                   4,
			nodeType:                2,
			expectedConditionStatus: corev1.NodeUnknownStatus,
			expectedNodeType:        "member",
		},
	}

	for _, kvdbNodeStateTest := range kvdbNodeStateTests {
		mockNodeServer.EXPECT().
			EnumerateWithFilters(gomock.Any(), gomock.Any()).
			Return(expectedNodeEnumerateResp, nil).
			Times(1)

		cm.Data[pxEntriesKey] = fmt.Sprintf(
			`[{"IP":"10.0.1.2","ID":"node-one","State":%d,"Type":%d,"Version":"v2","peerport":"9018","clientport":"9019"}]`,
			kvdbNodeStateTest.state, kvdbNodeStateTest.nodeType)
		err = driver.k8sClient.Update(context.TODO(), cm)
		require.NoError(t, err)
		err = driver.UpdateStorageClusterStatus(cluster, "")
		require.NoError(t, err)

		var (
			found        bool
			status       corev1.NodeConditionStatus
			conditionMsg string
		)
		checkStorageNode := &corev1.StorageNode{}
		err = driver.k8sClient.Get(context.TODO(), client.ObjectKey{
			Name:      "node-one",
			Namespace: clusterNS,
		}, checkStorageNode)
		require.NoError(t, err)
		for _, c := range checkStorageNode.Status.Conditions {
			if c.Type == corev1.NodeKVDBCondition {
				found = true
				status = c.Status
				conditionMsg = c.Message
				break
			}
		}
		require.True(t, found)
		require.True(t, *checkStorageNode.Status.NodeAttributes.KVDB)
		require.Equal(t, kvdbNodeStateTest.expectedConditionStatus, status)
		require.NotEmpty(t, conditionMsg)
		require.True(t, strings.Contains(conditionMsg, kvdbNodeStateTest.expectedNodeType))
	}

	// TEST 5: config map not found
	err = driver.k8sClient.Delete(context.TODO(), cm)
	require.NoError(t, err)
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)
}

func TestUpdateStorageNodeKVDBWhenOverwriteClusterID(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	clusterName := "px-cluster"
	clusterID := "clusterID_with-special-chars"
	clusterNS := "kube-system"
	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+"."+clusterNS)
	defer testutil.RestoreEtcHosts(t)

	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: clusterNS,
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: clusterNS,
			Annotations: map[string]string{
				pxutil.AnnotationClusterID: clusterID,
			},
		},
		Spec: corev1.StorageClusterSpec{
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}

	cmName := k8s.GetBootstrapConfigMapName(clusterID)

	// TEST 1: Add missing KVDB condition
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()
	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-one",
		SchedulerNodeName: "node-one",
		DataIp:            "10.0.1.1",
		MgmtIp:            "10.0.1.2",
		Status:            api.Status_STATUS_NONE,
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-two",
		SchedulerNodeName: "node-two",
		DataIp:            "10.0.2.1",
		MgmtIp:            "10.0.2.2",
		Status:            api.Status_STATUS_OK,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}

	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: clusterNS,
		},
		Data: map[string]string{
			pxEntriesKey: `[{"IP":"10.0.1.2","ID":"node-one","Index":0,"State":1,"Type":1,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-1.internal.kvdb","DataDirType":"KvdbDevice"},{"IP":"10.0.2.2","ID":"node-two","Index":2,"State":2,"Type":2,"Version":"v2","peerport":"9018","clientport":"9019","Domain":"portworx-3.internal.kvdb","DataDirType":"KvdbDevice"}]`,
		},
	}

	err = driver.k8sClient.Create(context.TODO(), cm)
	require.NoError(t, err)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)
	err = driver.UpdateStorageClusterStatus(cluster, "")
	require.NoError(t, err)

	// check if both storage nodes exist and have the KVDB condition
	for _, n := range []string{"node-one", "node-two"} {
		found := false
		checkStorageNode := &corev1.StorageNode{}
		err = driver.k8sClient.Get(context.TODO(), client.ObjectKey{
			Name:      n,
			Namespace: clusterNS,
		}, checkStorageNode)
		require.NoError(t, err)

		for _, c := range checkStorageNode.Status.Conditions {
			if c.Type == corev1.NodeKVDBCondition {
				found = true
				break
			}
		}
		require.True(t, found)
		require.True(t, *checkStorageNode.Status.NodeAttributes.KVDB)
	}
}

func fakeClientWithWiperPod(namespace string) client.Client {
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	return testutil.FakeK8sClient(wiperDS, wiperPod)
}

func TestIsPodUpdatedWithoutArgs(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pxcluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: For pod without containers, should return false
	pxPod := &v1.Pod{
		Spec: v1.PodSpec{},
	}
	isUpdated := driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: For pod with nil args, should return false
	pxPod.Spec.Containers = []v1.Container{
		{
			Name: pxContainerName,
		},
	}
	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: For pod with empty args, should return false
	pxPod.Spec.Containers = []v1.Container{
		{
			Name: pxContainerName,
			Args: make([]string, 0),
		},
	}
	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)
}

func TestIsPodUpdatedWithMiscArgs(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pxcluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: No misc args present in the cluster
	pxPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: pxContainerName,
					Args: []string{"-c", "pxcluster"},
				},
			},
		},
	}

	isUpdated := driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-foo bar --alpha beta",
	}
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"-foo", "bar",
		"--alpha", "beta",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod, with extra spaces
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "-foo   bar   --alpha   beta"

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod, with different order
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--alpha", "beta",
		"-foo", "bar",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: Cluster misc args are present in the pod, with changed values
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "--alpha beta -foo bazz"

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)
}

func TestIsPodUpdatedWithEssentialsArgs(t *testing.T) {
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pxcluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: If essentials is disabled and args don't have essentials arg
	pxPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: pxContainerName,
					Args: []string{"-c", "pxcluster"},
				},
			},
		},
	}

	isUpdated := driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: If essentials is disabled and args have essentials arg
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--oem", "esse",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: If essentials is disabled and args have essentials arg,
	// but present in misc args
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: " --oem    esse  ",
	}
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--oem", "esse",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	// TestCase: If essentials is enabled, but args don't have essentials arg
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "true")
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.False(t, isUpdated)

	// TestCase: If essentials is enabled, and args have essentials arg
	pxPod.Spec.Containers[0].Args = []string{
		"-c", "pxcluster",
		"--oem", "esse",
		"-b",
	}

	isUpdated = driver.IsPodUpdated(cluster, pxPod)
	require.True(t, isUpdated)

	os.Unsetenv(pxutil.EnvKeyPortworxEssentials)
}

func TestStorageClusterDefaultsForTelemetry(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sClient := testutil.FakeK8sClient()
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.7.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
		},
	}
	createTelemetrySecret(t, k8sClient, cluster.Namespace)

	// Disable telemetry for px version < 2.8.0
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)

	// Allow telemetry to be enabled for 2.8.0
	cluster.Spec.Image = "px/image:2.8.0"
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotEmpty(t, cluster.Spec.Monitoring) // telemetry is under monitoring
	require.NotEmpty(t, cluster.Spec.Monitoring.Telemetry)
	require.True(t, cluster.Spec.Monitoring.Telemetry.Enabled)
	require.NotEmpty(t, cluster.Status.DesiredImages.Telemetry)

	// disable it
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		Telemetry: &corev1.TelemetrySpec{
			Enabled: false,
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
	require.False(t, cluster.Spec.Monitoring.Telemetry.Enabled)

	// enabled
	cluster.Spec.Monitoring.Telemetry.Enabled = true
	cluster.Spec.Monitoring.Telemetry.Image = "portworx/px-telemetry:" + compVersion()
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-telemetry:"+compVersion(), cluster.Spec.Monitoring.Telemetry.Image)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)

	// Use image from release manifest if spec image is reset
	cluster.Spec.Monitoring.Telemetry.Image = ""
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.Equal(t, "portworx/px-telemetry:"+compVersion(), cluster.Status.DesiredImages.Telemetry)

	// Do not overwrite desired image if nothing has changed
	cluster.Status.DesiredImages.Telemetry = "portworx/px-telemetry:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-telemetry:old", cluster.Status.DesiredImages.Telemetry)

	// Do not overwrite desired autopilot image even if
	// some other component has changed
	cluster.Spec.UserInterface = &corev1.UserInterfaceSpec{
		Enabled: true,
	}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, "portworx/px-telemetry:old", cluster.Status.DesiredImages.Telemetry)
	require.Equal(t, "portworx/px-lighthouse:"+compVersion(), cluster.Status.DesiredImages.UserInterface)

	// Overwrite telemetry image and upgrade PX to 2.12, old image should be reset and use new one from manifest
	cluster.Spec.Image = "px/image: 2.12.0"
	cluster.Spec.Monitoring.Telemetry.Image = "portworx/px-telemetry:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.Equal(t, "portworx/px-telemetry:"+newCompVersion(), cluster.Status.DesiredImages.Telemetry)
	require.Equal(t, "purestorage/realtime-metrics:latest", cluster.Status.DesiredImages.MetricsCollector)
	require.Empty(t, cluster.Status.DesiredImages.MetricsCollectorProxy)
	require.Equal(t, "purestorage/envoy:1.2.3", cluster.Status.DesiredImages.TelemetryProxy)
	require.Equal(t, "purestorage/log-upload:1.2.3", cluster.Status.DesiredImages.LogUploader)

	// Set telemetry images explicitly
	cluster.Spec.Monitoring.Telemetry.Image = "portworx/ccm-go:1.2.3"
	cluster.Spec.Monitoring.Telemetry.LogUploaderImage = "portworx/log-upload:1.2.3"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
	require.Empty(t, cluster.Status.DesiredImages.LogUploader)
	require.Equal(t, "purestorage/envoy:1.2.3", cluster.Status.DesiredImages.TelemetryProxy)

	// Change desired image if px image has changed
	cluster.Spec.Image = "px/image:4.0.0"
	cluster.Spec.Monitoring.Telemetry.Image = ""
	cluster.Spec.Monitoring.Telemetry.LogUploaderImage = ""
	cluster.Status.DesiredImages.Telemetry = "portworx/px-telemetry:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.Equal(t, "portworx/px-telemetry:"+newCompVersion(), cluster.Status.DesiredImages.Telemetry)

	// Change desired image if auto update of components is always enabled
	updateStrategy := corev1.AlwaysAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Telemetry = "portworx/px-telemetry:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.Equal(t, "portworx/px-telemetry:"+compVersion(), cluster.Status.DesiredImages.Telemetry)

	// Change desired image if auto update of components is enabled once
	updateStrategy = corev1.OnceAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Telemetry = "portworx/px-telemetry:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.Equal(t, "portworx/px-telemetry:"+newCompVersion(), cluster.Status.DesiredImages.Telemetry)

	// Don't change desired image if auto update of components is never
	updateStrategy = corev1.NeverAutoUpdate
	cluster.Spec.AutoUpdateComponents = &updateStrategy
	cluster.Status.DesiredImages.Telemetry = "portworx/px-telemetry:old"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	require.Equal(t, "portworx/px-telemetry:old", cluster.Status.DesiredImages.Telemetry)

	// Reset desired image if telemetry has been disabled
	cluster.Spec.Monitoring.Telemetry.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Status.DesiredImages.Telemetry)
}

func TestGetStorageNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Node: mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: SDK response returns zero nodes
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil)

	nodes, err := driver.GetStorageNodes(cluster)
	require.NoError(t, err)
	require.Empty(t, nodes)

	// TestCase: SDK response returns some nodes
	expectedNodeEnumerateResp.Nodes = []*api.StorageNode{
		{
			Id: "node-one",
		},
		{
			Id: "node-two",
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(expectedNodeEnumerateResp, nil)

	nodes, err = driver.GetStorageNodes(cluster)
	require.NoError(t, err)
	require.Len(t, nodes, 2)

	// TestCase: SDK returns error
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("SDK error"))

	nodes, err = driver.GetStorageNodes(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SDK error")
	require.Nil(t, nodes)

	// TestCase: Error setting up tokens for auth enabled cluster
	cluster.Spec.Security = &corev1.SecuritySpec{
		Enabled: true,
	}

	nodes, err = driver.GetStorageNodes(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to get portworx apps secret")
	require.Nil(t, nodes)
}

func TestGetStorageNodesWithConnectionErrors(t *testing.T) {
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "127.0.0.1",
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: GRPC connection timeout, but with empty StorageCluster status
	nodes, err := driver.GetStorageNodes(cluster)
	require.NoError(t, err)
	require.Empty(t, nodes)

	// TestCase: GRPC connection timeout, but with Initializing StorageCluster status
	cluster.Status.Phase = string(corev1.ClusterStateInit)

	nodes, err = driver.GetStorageNodes(cluster)
	require.NoError(t, err)
	require.Empty(t, nodes)

	// TestCase: GRPC connection timeout, but with Online StorageCluster status
	cluster.Status.Phase = string(corev1.ClusterStateRunning)
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeRuntimeState,
		Status: corev1.ClusterConditionStatusOnline,
	})

	nodes, err = driver.GetStorageNodes(cluster)
	require.Error(t, err)
	require.Empty(t, nodes)
}

func TestStorageUpgradeClusterDefaultsMaxStorageNodesPerZone(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}
	totalNodes := uint32(12)

	/* 1 zone */
	k8sClient, _ := getK8sClientWithNodesZones(t, totalNodes, 1, cluster)
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	origVersion := "2.10.0"
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.Image = "oci-monitor:" + origVersion
	cluster.Spec.Version = origVersion
	cluster.Status.Version = origVersion

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotEqual(t, (*corev1.CloudStorageSpec)(nil), cluster.Spec.CloudStorage)
	require.Nil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotEqual(t, (*corev1.CloudStorageSpec)(nil), cluster.Spec.CloudStorage)
	require.Nil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)

	cluster.Annotations = map[string]string{}
	cluster.Annotations[constants.AnnotationDisableStorage] = strconv.FormatBool(true)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotEqual(t, (*corev1.CloudStorageSpec)(nil), cluster.Spec.CloudStorage)
	require.Nil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)

	// storage only
	testStoragelessNodesUpgrade(t, 24, 0)
	testStoragelessNodesUpgrade(t, 12, 0, 0)
	testStoragelessNodesUpgrade(t, 8, 0, 0, 0)
	testStoragelessNodesUpgrade(t, 6, 0, 0, 0, 0)

	// storageless
	testStoragelessNodesUpgrade(t, 14, 10)
	testStoragelessNodesUpgrade(t, 23, 1)
	testStoragelessNodesUpgrade(t, 7, 5, 5)
	testStoragelessNodesUpgrade(t, 7, 5, 10)
	testStoragelessNodesUpgrade(t, 12, 5, 0)
	testStoragelessNodesUpgrade(t, 3, 5, 7, 5)
	testStoragelessNodesUpgrade(t, 3, 5, 5, 5)
	testStoragelessNodesUpgrade(t, 3, 5, 8, 8)
	testStoragelessNodesUpgrade(t, 1, 7, 8, 8)
	testStoragelessNodesUpgrade(t, 8, 7, 0, 8)
	testStoragelessNodesUpgrade(t, 8, 1, 0, 0)
	testStoragelessNodesUpgrade(t, 0, 8, 8, 8)
	testStoragelessNodesUpgrade(t, 0, 6, 6, 6, 6)
	testStoragelessNodesUpgrade(t, 1, 6, 5, 6, 6)
	testStoragelessNodesUpgrade(t, 5, 1, 1, 1, 1)
	testStoragelessNodesUpgrade(t, 6, 1, 1, 1, 0)
	testStoragelessNodesUpgrade(t, 6, 1, 0, 0, 0)
}

func testStoragelessNodesUpgrade(t *testing.T, expectedValue uint32, storageless ...uint32) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	testutil.SetupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer testutil.RestoreEtcHosts(t)

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateRunning),
		},
	}

	totalNodes := uint32(24)
	zones := uint32(len(storageless))
	origVersion := "2.10.0"
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.Image = "oci-monitor:" + origVersion
	cluster.Spec.Version = origVersion
	cluster.Status.Version = "2.10.1"
	cluster.Spec.CloudStorage.MaxStorageNodesPerZone = nil
	k8sClient, expected := getK8sClientWithNodesZones(t, totalNodes, zones, cluster, storageless...)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), gomock.Any()).
		Return(&api.SdkNodeEnumerateWithFiltersResponse{Nodes: expected}, nil).
		AnyTimes()

	err = k8sClient.Create(context.TODO(), &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})
	require.NoError(t, err)
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotEqual(t, (*corev1.CloudStorageSpec)(nil), cluster.Spec.CloudStorage)
	if expectedValue == 0 {
		require.Nil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	} else {
		require.Equal(t, expectedValue, *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	}
}

func TestStorageClusterDefaultsMaxStorageNodesPerZoneDisaggregatedMode(t *testing.T) {
	// 24 node cluster will be created in each step
	testStoragelessNodesDisaggregatedMode(t, 24, 0)
	testStoragelessNodesDisaggregatedMode(t, 12, 0, 0)
	testStoragelessNodesDisaggregatedMode(t, 8, 0, 0, 0)
	testStoragelessNodesDisaggregatedMode(t, 6, 0, 0, 0, 0)

	testStoragelessNodesDisaggregatedMode(t, 14, 10)
	testStoragelessNodesDisaggregatedMode(t, 23, 1)
	testStoragelessNodesDisaggregatedMode(t, 7, 5, 5)
	testStoragelessNodesDisaggregatedMode(t, 2, 5, 10)
	testStoragelessNodesDisaggregatedMode(t, 7, 5, 0)
	testStoragelessNodesDisaggregatedMode(t, 1, 5, 7, 5)
	testStoragelessNodesDisaggregatedMode(t, 3, 5, 5, 5)
	testStoragelessNodesDisaggregatedMode(t, 3, 5, 8, 8)
	testStoragelessNodesDisaggregatedMode(t, 1, 7, 8, 8)
	testStoragelessNodesDisaggregatedMode(t, 1, 7, 0, 8)
	testStoragelessNodesDisaggregatedMode(t, 7, 1, 0, 0)
	testStoragelessNodesDisaggregatedMode(t, 0, 8, 8, 8)
	testStoragelessNodesDisaggregatedMode(t, 0, 6, 6, 6, 6)
	testStoragelessNodesDisaggregatedMode(t, 1, 6, 5, 6, 6)
	testStoragelessNodesDisaggregatedMode(t, 5, 1, 1, 1, 1)
	testStoragelessNodesDisaggregatedMode(t, 5, 1, 1, 1, 0)
	testStoragelessNodesDisaggregatedMode(t, 5, 1, 0, 0, 0)
}

func testStoragelessNodesDisaggregatedMode(t *testing.T, expectedValue uint32, storageless ...uint32) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}

	totalNodes := uint32(24)
	zones := uint32(len(storageless))
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.CloudStorage.MaxStorageNodesPerZone = nil
	k8sClient := getK8sClientWithNodesDisaggregated(t, totalNodes, zones, cluster, storageless...)
	recorder := record.NewFakeRecorder(10)

	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	driver.zoneToInstancesMap, err = cloudprovider.GetZoneMap(driver.k8sClient, "", "")
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CloudStorage)
	if expectedValue == 0 {
		require.Nil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	} else {
		require.Equal(t, expectedValue, *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	}
}

func TestDisaggregatedMissingLabels(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}

	totalNodes := uint32(6)
	zones := uint32(1)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	cluster.Spec.CloudStorage.MaxStorageNodesPerZone = nil
	k8sClient := getK8sClientWithNodesDisaggregated(t, totalNodes, zones, cluster, 0)
	// Create a node without any of the node-type or topology labels
	k8sNode := createK8sNode("Extra-node", 10)
	err := k8sClient.Create(context.TODO(), k8sNode)
	require.NoError(t, err)
	recorder := record.NewFakeRecorder(10)

	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	driver.zoneToInstancesMap, err = cloudprovider.GetZoneMap(driver.k8sClient, "", "")
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CloudStorage)
	require.NotNil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	require.Equal(t, uint32(6), *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
}

func TestGetDefaultMaxStorageNodesPerZone(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			CloudStorage: &corev1.CloudStorageSpec{
				MaxStorageNodesPerZone: nil,
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	err := driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	var nodeList v1.NodeList
	storageNodes, err := driver.getDefaultMaxStorageNodesPerZone(cluster, &nodeList)
	require.Equal(t, uint32(0), storageNodes)
	require.NoError(t, err)

	nodeList.Items = []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{
					v1.LabelTopologyZone:   "bar-pxzone1",
					v1.LabelTopologyRegion: "bar"},
			},
			Spec: v1.NodeSpec{
				ProviderID: "azure://",
			}},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node2",
				Labels: map[string]string{
					v1.LabelTopologyZone:   "foo-pxzone2",
					v1.LabelTopologyRegion: "foo"},
			},
			Spec: v1.NodeSpec{
				ProviderID: "azure://",
			}},
	}

	k8sClient = testutil.FakeK8sClient(cluster, &nodeList)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	storageNodes, err = driver.getDefaultMaxStorageNodesPerZone(cluster, &nodeList)
	require.Equal(t, uint32(1), storageNodes)
	require.NoError(t, err)

	k8sClient = testutil.FakeK8sClient(cluster)
	err = driver.Init(k8sClient, runtime.NewScheme(), recorder)
	require.NoError(t, err)

	storageNodes, err = driver.getDefaultMaxStorageNodesPerZone(cluster, &nodeList)
	require.Equal(t, uint32(0), storageNodes)
	require.Equal(t, ErrNodeListEmpty, err)
}

func TestStorageClusterDefaultsMaxStorageNodesPerZone(t *testing.T) {
	testClusterDefaultsMaxStorageNodesPerZoneCase1(t)
	// 1 zone
	testClusterDefaultsMaxStorageNodesPerZone(t, 6, 6, 1)
	// 2 zones
	testClusterDefaultsMaxStorageNodesPerZone(t, 4, 8, 2)
	testClusterDefaultsMaxStorageNodesPerZone(t, 4, 9, 2)
	// 3 zones
	testClusterDefaultsMaxStorageNodesPerZone(t, 3, 9, 3)
	testClusterDefaultsMaxStorageNodesPerZone(t, 3, 10, 3)
	testClusterDefaultsMaxStorageNodesPerZone(t, 3, 11, 3)
	testClusterDefaultsMaxStorageNodesPerZone(t, 33, 100, 3)
	// 4 zones
	testClusterDefaultsMaxStorageNodesPerZone(t, 2, 8, 4)
	testClusterDefaultsMaxStorageNodesPerZone(t, 25, 100, 4)
	testClusterDefaultsMaxStorageNodesPerZone(t, 25, 101, 4)
	testClusterDefaultsMaxStorageNodesPerZone(t, 25, 102, 4)
	testClusterDefaultsMaxStorageNodesPerZone(t, 25, 103, 4)
	testClusterDefaultsMaxStorageNodesPerZone(t, 26, 104, 4)
	testClusterDefaultsMaxStorageNodesPerZoneValueSpecified(t)
}

func TestHasKubernetesVersionChanged(t *testing.T) {
	dummyVer, err := version.NewVersion("1.2.3")
	require.NoError(t, err)

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	assert.False(t, driver.hasKubernetesVersionChanged(cluster))

	driver.k8sVersion = dummyVer
	assert.False(t, driver.hasKubernetesVersionChanged(cluster))

	cluster.Status.DesiredImages.KubeControllerManager = "foo:1.2.3"
	cluster.Status.DesiredImages.KubeScheduler = "bar:1.2.3"
	assert.False(t, driver.hasKubernetesVersionChanged(cluster))

	cluster.Status.DesiredImages.KubeControllerManager = "foo:9.9.9"
	cluster.Status.DesiredImages.KubeScheduler = "bar:1.2.3"
	assert.True(t, driver.hasKubernetesVersionChanged(cluster))

	cluster.Status.DesiredImages.KubeControllerManager = "foo:1.2.3"
	cluster.Status.DesiredImages.KubeScheduler = "bar:9.9.9"
	assert.True(t, driver.hasKubernetesVersionChanged(cluster))

	cluster.Status.DesiredImages.KubeControllerManager = "foo:9.9.9"
	cluster.Status.DesiredImages.KubeScheduler = "bar:9.9.9"
	assert.True(t, driver.hasKubernetesVersionChanged(cluster))
}

func testClusterDefaultsMaxStorageNodesPerZoneCase1(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}
	/* 1 zone */
	var err error
	k8sClient, _ := getK8sClientWithNodesZones(t, 6, 1, cluster)
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	driver.zoneToInstancesMap, err = cloudprovider.GetZoneMap(driver.k8sClient, "", "")
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Equal(t, (*corev1.CloudStorageSpec)(nil), cluster.Spec.CloudStorage)
	require.Equal(t, "", cluster.Status.Phase)

}

func testClusterDefaultsMaxStorageNodesPerZone(t *testing.T, expectedValue uint32, totalNodes uint32, zones uint32) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}
	k8sClient, _ := getK8sClientWithNodesZones(t, totalNodes, zones, cluster)
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	driver.zoneToInstancesMap, err = cloudprovider.GetZoneMap(driver.k8sClient, "", "")
	require.NoError(t, err)

	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotNil(t, cluster.Spec.CloudStorage)
	require.NotNil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	require.Equal(t, expectedValue, *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
}

func testClusterDefaultsMaxStorageNodesPerZoneValueSpecified(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.21.0",
	}
	coreops.SetInstance(coreops.New(versionClient))

	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
		},
	}

	/* Value specified */
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		MaxStorageNodesPerZone: new(uint32),
	}
	*cluster.Spec.CloudStorage.MaxStorageNodesPerZone = 7
	k8sClient, _ := getK8sClientWithNodesZones(t, 30, 3, cluster)
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	driver.zoneToInstancesMap, err = cloudprovider.GetZoneMap(driver.k8sClient, "", "")
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.NotEqual(t, (*corev1.CloudStorageSpec)(nil), cluster.Spec.CloudStorage)
	require.Equal(t, uint32(7), *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
}

func getK8sClientWithNodesZones(
	t *testing.T,
	nodeCount uint32,
	totalZones uint32,
	cluster *corev1.StorageCluster,
	storagelessCount ...uint32,
) (client.Client, []*api.StorageNode) {
	if len(storagelessCount) != 0 {
		require.Equal(t, uint32(len(storagelessCount)), totalZones)
	} else {
		storagelessCount = make([]uint32, totalZones)
	}
	expected := []*api.StorageNode{}
	k8sClient := testutil.FakeK8sClient(cluster)
	zoneCount := uint32(0)
	for node := uint32(0); node < nodeCount; node++ {
		nodename := "k8s-node-" + strconv.Itoa(int(node))
		k8sNode := createK8sNode(nodename, 10)
		zoneCount = zoneCount % totalZones
		k8sNode.Labels[v1.LabelTopologyZone] = "Zone-" + strconv.Itoa(int(zoneCount))
		err := k8sClient.Create(context.TODO(), k8sNode)
		require.NoError(t, err)

		pool := []*api.StoragePool{
			{},
			{},
		}
		if storagelessCount[zoneCount] > 0 {
			pool = []*api.StoragePool{}
			storagelessCount[zoneCount]--
		}

		node := api.StorageNode{
			SchedulerNodeName: nodename,
			Pools:             pool,
		}
		expected = append(expected, &node)
		zoneCount++
	}
	return k8sClient, expected
}

func getK8sClientWithNodesDisaggregated(
	t *testing.T,
	nodeCount uint32,
	totalZones uint32,
	cluster *corev1.StorageCluster,
	storagelessCount ...uint32,
) client.Client {
	if len(storagelessCount) != 0 {
		require.Equal(t, uint32(len(storagelessCount)), totalZones)
	} else {
		storagelessCount = make([]uint32, totalZones)
	}
	k8sClient := testutil.FakeK8sClient(cluster)
	zoneCount := uint32(0)
	for node := uint32(0); node < nodeCount; node++ {
		nodename := "k8s-node-" + strconv.Itoa(int(node))
		k8sNode := createK8sNode(nodename, 10)
		zoneCount = zoneCount % totalZones
		k8sNode.Labels[v1.LabelTopologyZone] = "Zone-" + strconv.Itoa(int(zoneCount))
		if storagelessCount[zoneCount] > 0 {
			storagelessCount[zoneCount]--
			k8sNode.Labels[util.NodeTypeKey] = util.StoragelessNodeValue
		} else {
			k8sNode.Labels[util.NodeTypeKey] = util.StorageNodeValue
		}
		err := k8sClient.Create(context.TODO(), k8sNode)
		require.NoError(t, err)
		zoneCount++
	}
	return k8sClient
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

func createStoragePod(
	cluster *corev1.StorageCluster,
	podName, nodeName string,
	labels map[string]string,
) *v1.Pod {
	clusterRef := metav1.NewControllerRef(cluster, corev1.SchemeGroupVersion.WithKind("StorageCluster"))
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            podName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*clusterRef},
		},
		Spec: v1.PodSpec{
			NodeName: nodeName,
			Containers: []v1.Container{{
				Name: "portworx",
				Args: []string{
					"-c", "px-cluster",
				},
			}},
		},
	}
}

func manifestSetup() {
	manifest.SetInstance(&fakeManifest{})
}

type fakeManifest struct {
	k8sVersion *version.Version
}

func (m *fakeManifest) Init(_ client.Client, _ record.EventRecorder, k8sVersion *version.Version) {
	m.k8sVersion = k8sVersion
}

func (m *fakeManifest) GetVersions(
	_ *corev1.StorageCluster,
	force bool,
) *manifest.Version {
	compVersion := compVersion()
	if force {
		compVersion = newCompVersion()
	}
	version := &manifest.Version{
		PortworxVersion: "2.10.0",
		Components: manifest.Release{
			Stork:                      "openstorage/stork:" + compVersion,
			Autopilot:                  "portworx/autopilot:" + compVersion,
			Lighthouse:                 "portworx/px-lighthouse:" + compVersion,
			NodeWiper:                  "portworx/px-node-wiper:" + compVersion,
			Telemetry:                  "portworx/px-telemetry:" + compVersion,
			CSIProvisioner:             "quay.io/k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "quay.io/k8scsi/csi-attacher:v1.2.3",
			CSIDriverRegistrar:         "quay.io/k8scsi/driver-registrar:v1.2.3",
			CSINodeDriverRegistrar:     "quay.io/k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "quay.io/k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "quay.io/k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "quay.io/k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "quay.io/k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "quay.io/prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "quay.io/coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "quay.io/coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "quay.io/coreos/configmap-reload:v1.2.3",
			AlertManager:               "quay.io/prometheus/alertmanager:v1.2.3",
			Grafana:                    "docker.io/grafana/grafana:v1.2.3",
			MetricsCollector:           "purestorage/realtime-metrics:latest",
			MetricsCollectorProxy:      "envoyproxy/envoy:v1.19.1",
			LogUploader:                "purestorage/log-upload:1.2.3",
			TelemetryProxy:             "purestorage/envoy:1.2.3",
			PxRepo:                     "portworx/px-repo:" + compVersion,
			DynamicPlugin:              "portworx/portworx-dynamic-plugin:1.1.0",
			DynamicPluginProxy:         "nginxinc/nginx-unprivileged:1.25",
			CsiLivenessProbe:           "docker.io/portworx/livenessprobe:v2.10.0-windows",
			CsiWindowsDriver:           "docker.io/portworx/px-windows-csi-driver:23.8.0",
			CsiWindowsNodeRegistrar:    "docker.io/portworx/csi-node-driver-registrar:v2.8.0-windows",
		},
	}
	if m.k8sVersion != nil && m.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) {
		version.Components.PrometheusOperator = "quay.io/prometheus-operator/prometheus-operator:v0.50.0"
		version.Components.Prometheus = "quay.io/prometheus/prometheus:v2.29.1"
		version.Components.PrometheusConfigMapReload = ""
		version.Components.PrometheusConfigReloader = "quay.io/prometheus-operator/prometheus-config-reloader:v0.50.0"
		version.Components.AlertManager = "quay.io/prometheus/alertmanager:v0.22.2"
	}
	return version
}

func (m *fakeManifest) CanAccessRemoteManifest(cluster *corev1.StorageCluster) bool {
	return false
}

func compVersion() string {
	return "2.3.4"
}

func newCompVersion() string {
	return "4.3.2"
}

func TestMain(m *testing.M) {
	manifestSetup()
	code := m.Run()
	os.Exit(code)
}
