package storagecluster

import (
	"testing"

	"github.com/golang/mock/gomock"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	fakeop "github.com/libopenstorage/operator/pkg/client/clientset/versioned/fake"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestStorageClusterDefaults(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
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
			Name:      "px-cluster",
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
			Name:      "px-cluster",
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
			Name:      "px-cluster",
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
			Name:      "px-cluster",
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

func TestStoragePodGetsScheduled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	driverName := "mock-driver"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-cluster",
			Namespace:       "test-ns",
			OwnerReferences: []metav1.OwnerReference{{UID: "test-uid"}},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: false,
			},
		},
	}

	// Kubernetes node with resources to create a pod
	k8sNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "k8s-node-1",
		},
		Status: v1.NodeStatus{
			Allocatable: map[v1.ResourceName]resource.Quantity{
				v1.ResourcePods: resource.MustParse("1"),
			},
		},
	}

	k8s.Instance().SetClient(
		fake.NewSimpleClientset(), nil, nil,
		fakeop.NewSimpleClientset(cluster), nil, nil, nil, nil,
	)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, k8sNode)
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
	require.Len(t, podControl.Templates, 1)
	require.Equal(t, expectedPodTemplate, &podControl.Templates[0])
	require.Len(t, podControl.ControllerRefs, 1)
	require.Equal(t, metav1.NewControllerRef(cluster, controllerKind), &podControl.ControllerRefs[0])
}
