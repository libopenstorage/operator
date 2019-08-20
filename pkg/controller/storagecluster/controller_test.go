package storagecluster

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

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

	// Populate version from image
	cluster.Spec.Image = "test/image:1.2.3"
	cluster.Spec.Version = ""
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Equal(t, "1.2.3", cluster.Spec.Version)

	// Don't populate version from image if not tag present
	cluster.Spec.Image = "test/image"
	cluster.Spec.Version = ""
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Version)

	// Don't populate version if image not present
	cluster.Spec.Image = ""
	cluster.Spec.Version = ""
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Version)
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

func TestStorageClusterDefaultsForStork(t *testing.T) {
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

	// Stork should be enabled be default
	err := controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Stork.Enabled)
	require.Equal(t, defaultStorkImage, cluster.Spec.Stork.Image)

	// Stork should use default image if not specified and enabled in spec
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Stork.Enabled)
	require.Equal(t, defaultStorkImage, cluster.Spec.Stork.Image)

	// Stork should use default image if empty and enabled in spec
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
		Image:   "  ",
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Stork.Enabled)
	require.Equal(t, defaultStorkImage, cluster.Spec.Stork.Image)

	// Stork should not use default image if image already present in spec
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: true,
		Image:   "testimage",
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.True(t, cluster.Spec.Stork.Enabled)
	require.Equal(t, "testimage", cluster.Spec.Stork.Image)

	// Stork should not use default image if disabled in spec
	cluster.Spec.Stork = &corev1alpha1.StorkSpec{
		Enabled: false,
	}
	err = controller.setStorageClusterDefaults(cluster)
	require.NoError(t, err)
	require.False(t, cluster.Spec.Stork.Enabled)
	require.Empty(t, cluster.Spec.Stork.Image)
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
	cluster := defaultStorageCluster()
	cluster.Spec.Stork.Enabled = true
	cluster.Annotations = map[string]string{
		annotationStorkCPU: "invalid-cpu",
	}
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().GetStorkDriverName().Return("mock", nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to install/update stork")
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, failedSyncReason))
}

func TestStoragePodGetsScheduled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	clusterRef := metav1.NewControllerRef(cluster, controllerKind)

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode2 := createK8sNode("k8s-node-2", 1)

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	expectedPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Labels: map[string]string{
				labelKeyName:       cluster.Name,
				labelKeyDriverName: driverName,
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{Name: "test"}},
		},
	}
	addOrUpdateStoragePodTolerations(expectedPod)
	expectedPodTemplate := &v1.PodTemplateSpec{
		ObjectMeta: expectedPod.ObjectMeta,
		Spec:       expectedPod.Spec,
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
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).
		Return(expectedPodTemplate.Spec, nil).
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

func TestFailedStoragePodsGetRemoved(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

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
		fmt.Sprintf("%v %v", v1.EventTypeWarning, failedStoragePodReason))

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
	cluster := defaultStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

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

func TestStoragePodFailureDueToInsufficientResources(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
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
	expectedEvent := fmt.Sprintf("%v %v", v1.EventTypeWarning, failedPlacementReason)
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
	cluster := defaultStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	podSpec := v1.PodSpec{
		NodeSelector: map[string]string{
			"test": "node2",
		},
		Affinity: &v1.Affinity{
			NodeAffinity: &v1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchFields: []v1.NodeSelectorRequirement{
								{
									Key:      schedulerapi.NodeFieldSelectorKeyNodeName,
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"k8s-node-1"},
								},
							},
						},
					},
				},
			},
		},
	}
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(podSpec, nil).AnyTimes()
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

func TestStoragePodFailureDueToPodNotFitsHostPorts(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	podSpec := v1.PodSpec{
		Containers: []v1.Container{
			{
				Ports: []v1.ContainerPort{{HostPort: int32(10001)}},
			},
		},
	}
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(podSpec, nil).AnyTimes()
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
	k8sNode2 := createK8sNode("k8s-node-2", 10)

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod := createStoragePod(cluster, "running-pod", k8sNode1.Name, storageLabels)
	// Create a pod that uses the same ports as our storage pod
	conflictingPod := createStoragePod(cluster, "conflicting-pod", k8sNode2.Name, nil)
	conflictingPod.OwnerReferences = nil
	conflictingPod.Spec.Containers = []v1.Container{
		{
			Ports: []v1.ContainerPort{{HostPort: int32(10001)}},
		},
	}

	k8sClient.Create(context.TODO(), k8sNode1)
	k8sClient.Create(context.TODO(), k8sNode2)
	k8sClient.Create(context.TODO(), runningPod)
	k8sClient.Create(context.TODO(), conflictingPod)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
		},
	}
	result, err := controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	// No need to raise events if pod couldn't fit hosts due to ports.
	// Verify no pod is created due to port conflict on node2. Also pod from node1
	// should get removed due to port conflict
	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{runningPod.Name}, podControl.DeletePodName)
}

func TestStoragePodFailureDueToTaintsTolerationsNotMatch(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).Times(6)

	// This will create a revision which we will map to our pre-created pods
	rev1Hash, err := createRevision(k8sClient, cluster, driverName)
	require.NoError(t, err)

	// Kubernetes node with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode1.Spec.Taints = []v1.Taint{
		{
			Key:    "foo",
			Value:  "bar",
			Effect: v1.TaintEffectNoExecute,
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

	// Pods that are already running on the k8s nodes with same hash
	storageLabels[defaultStorageClusterUniqueLabelKey] = rev1Hash
	runningPod := createStoragePod(cluster, "running-pod", k8sNode1.Name, storageLabels)

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

	// No need to raise events if pod cannot be scheduled due to node taints.
	// Verify no pod is created due to node taints. Also existing pod should
	// be removed if taint is NoExecute
	require.Empty(t, recorder.Events)
	require.Empty(t, podControl.Templates)
	require.ElementsMatch(t, []string{runningPod.Name}, podControl.DeletePodName)

	// Pod should get scheduled if there are tolerations for node taints
	podSpec := v1.PodSpec{
		Tolerations: []v1.Toleration{
			{
				Key:    "foo",
				Value:  "bar",
				Effect: v1.TaintEffectNoSchedule,
			},
		},
	}
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(podSpec, nil).Times(6)

	result, err = controller.Reconcile(request)
	require.NoError(t, err)
	require.Empty(t, result)

	require.Empty(t, recorder.Events)
	require.Len(t, podControl.Templates, 1)
}

func TestFailureDuringCreateDeletePods(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{
		Err: fmt.Errorf("pod control error"),
	}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

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
	require.Contains(t, err.Error(), "pod control error, pod control error")
	require.Empty(t, result)

	// Verify there is event raised for failure to create/delete pods
	require.Len(t, recorder.Events, 2)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, failedStoragePodReason))
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, failedSyncReason))
}

func TestTimeoutFailureDuringCreatePods(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	podControl := &k8scontroller.FakePodControl{
		Err: errors.NewTimeoutError("timeout error", 0),
	}
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

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
	cluster := defaultStorageCluster()
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
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
	cluster := defaultStorageCluster()
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any())
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().PreInstall(gomock.Any()).Return(nil)
	driver.EXPECT().UpdateDriver(gomock.Any()).Return(nil)
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
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
		fmt.Sprintf("%v %v update status error", v1.EventTypeWarning, failedSyncReason))

	newCluster := &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, newCluster, cluster.Name, cluster.Namespace)
	require.Equal(t, "Offline", newCluster.Status.Phase)
}

func TestFailedPreInstallFromDriver(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
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
		fmt.Sprintf("%v %v", v1.EventTypeWarning, failedSyncReason))
}

func TestUpdateDriverWithInstanceInformation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := defaultStorageCluster()

	// Kubernetes node with resources to create a pod
	k8sNode1 := createK8sNode("k8s-node-1", 1)
	k8sNode1.Labels = map[string]string{failureDomainZoneKey: "z1"}
	k8sNode2 := createK8sNode("k8s-node-2", 1)
	k8sNode2.Labels = map[string]string{failureDomainZoneKey: "z1"}
	k8sNode3 := createK8sNode("k8s-node-3", 1)
	k8sNode3.Labels = map[string]string{failureDomainZoneKey: "z2"}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode1, k8sNode2, k8sNode3)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().PreInstall(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().UpdateStorageClusterStatus(gomock.Any()).Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()
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

	cluster := defaultStorageCluster()
	cluster.Finalizers = []string{}
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp

	driverName := "mock-driver"
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil)

	// Kubernetes node with enough resources to create new pods
	k8sNode1 := createK8sNode("k8s-node-1", 10)
	k8sNode2 := createK8sNode("k8s-node-2", 10)
	k8sNode3 := createK8sNode("k8s-node-3", 10)

	// Only pods that are associated with this cluster should get deleted
	storagePod1 := createStoragePod(cluster, "pod-1", k8sNode1.Name, storageLabels)
	storagePod2 := createStoragePod(cluster, "pod-2", k8sNode2.Name, storageLabels)

	storagePod3 := createStoragePod(cluster, "pod-3", k8sNode3.Name, nil)
	storagePod3.OwnerReferences = nil

	otherCluster := defaultStorageCluster()
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

	cluster := defaultStorageCluster()
	deletionTimeStamp := metav1.Now()
	cluster.DeletionTimestamp = &deletionTimeStamp

	driverName := "mock-driver"
	storageLabels := map[string]string{
		labelKeyName:       cluster.Name,
		labelKeyDriverName: driverName,
	}

	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	podControl := &k8scontroller.FakePodControl{}
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:     k8sClient,
		Driver:     driver,
		podControl: podControl,
		recorder:   recorder,
	}

	// Empty delete condition should not remove finalizer
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(nil, nil)
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return(driverName).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any()).Return(v1.PodSpec{}, nil).AnyTimes()

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
	require.Empty(t, updatedCluster.Status.Conditions)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If storage driver returns error, then controller should return error as well
	driver.EXPECT().DeleteStorage(gomock.Any()).Return(nil, fmt.Errorf("delete error"))

	result, err = controller.Reconcile(request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "delete error")
	require.Empty(t, result)

	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v", v1.EventTypeWarning, failedSyncReason))

	updatedCluster = &corev1alpha1.StorageCluster{}
	testutil.Get(k8sClient, updatedCluster, cluster.Name, cluster.Namespace)
	require.Empty(t, updatedCluster.Status.Conditions)
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition is not present already, then add to the cluster
	updatedCluster.Status.Conditions = []corev1alpha1.ClusterCondition{
		{
			Type: corev1alpha1.ClusterConditionTypeInstall,
		},
	}
	k8sClient.Update(context.TODO(), updatedCluster)
	condition := &corev1alpha1.ClusterCondition{
		Type: corev1alpha1.ClusterConditionTypeDelete,
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
	require.Equal(t, []string{deleteFinalizerName}, updatedCluster.Finalizers)

	// If delete condition is present, then update it
	condition = &corev1alpha1.ClusterCondition{
		Type:   corev1alpha1.ClusterConditionTypeDelete,
		Status: corev1alpha1.ClusterOperationInProgress,
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

func defaultStorageCluster() *corev1alpha1.StorageCluster {
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
	patch, err := getPatch(cluster)
	if err != nil {
		return "", err
	}

	hash := computeHash(&cluster.Spec, cluster.Status.CollisionCount)
	history := &appsv1.ControllerRevision{
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
	}

	if err := k8sClient.Create(context.TODO(), history); err != nil {
		return "", err
	}
	return hash, nil
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
