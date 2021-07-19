package storagecluster

import (
	"github.com/golang/mock/gomock"
	"github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func getMockCluster(mockCtrl *gomock.Controller) (*Controller, *corev1.StorageCluster) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			PxRepo: &corev1.PxRepoSpec{
				Enabled: true,
				Image:   "testImage",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:              k8sClient,
		Driver:              driver,
		kubernetesVersion:   k8sVersion,
		lastPodCreationTime: make(map[string]time.Time),
	}

	return &controller, cluster
}

func TestEnableDisablePxRepo(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	controller, cluster := getMockCluster(mockCtrl)

	// Disabled at first
	cluster.Spec.PxRepo.Enabled = false
	err := controller.syncPxRepo(cluster)
	require.NoError(t, err)

	service := &v1.Service{}
	err = testutil.Get(controller.client, service, pxRepoServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	deployment := &appsv1.Deployment{}
	err = testutil.Get(controller.client, deployment, pxRepoDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Enable the service
	cluster.Spec.PxRepo.Enabled = true
	err = controller.syncPxRepo(cluster)
	require.NoError(t, err)

	err = testutil.Get(controller.client, service, pxRepoServiceName, cluster.Namespace)
	require.NoError(t, err)

	err = testutil.Get(controller.client, deployment, pxRepoDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	// Disable the service
	cluster.Spec.PxRepo.Enabled = false
	err = controller.syncPxRepo(cluster)
	require.NoError(t, err)

	err = testutil.Get(controller.client, service, pxRepoServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	err = testutil.Get(controller.client, deployment, pxRepoDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))
}
