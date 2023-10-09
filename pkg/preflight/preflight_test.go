package preflight

import (
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	"github.com/libopenstorage/cloudops"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
)

func TestDefaultProviders(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
	}

	// TestCase: init default preflight checker
	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	err := InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)

	c := Instance()
	require.Empty(t, c.ProviderName())
	require.Empty(t, c.K8sDistributionName())
	require.NoError(t, c.CheckCloudDrivePermission(cluster))

	// TestCase: init azure cloud provider
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-3"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	err = InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)

	c = Instance()
	require.Equal(t, cloudops.Azure, c.ProviderName())
	require.Empty(t, c.K8sDistributionName())
	require.NoError(t, c.CheckCloudDrivePermission(cluster))

	// TestCase: init gce cloud provider
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "gce://node-id-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Spec: v1.NodeSpec{ProviderID: "gce://node-id-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Spec: v1.NodeSpec{ProviderID: "gce://node-id-3"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	err = InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)

	c = Instance()
	require.Equal(t, cloudops.GCE, c.ProviderName())
	require.Empty(t, c.K8sDistributionName())

	// TestCase: init gke cloud provider
	versionClient := fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.26.5-gke.1200",
	}
	coreops.SetInstance(coreops.New(versionClient))
	err = InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)
	require.True(t, IsGKE())

	c = Instance()
	require.Equal(t, cloudops.GCE, c.ProviderName())
	require.Equal(t, gkeDistribution, c.K8sDistributionName())

	// TestCase: init aws cloud provider
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-3"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	err = InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)

	c = Instance()
	require.Equal(t, string(cloudops.AWS), c.ProviderName())
	require.Empty(t, c.K8sDistributionName())
	require.NoError(t, c.CheckCloudDrivePermission(cluster))
	require.False(t, IsEKS())

	// TestCase: init eks cloud provider
	versionClient = fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.21.14-eks-ba74326",
	}
	coreops.SetInstance(coreops.New(versionClient))
	err = InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)
	require.True(t, IsEKS())

	c = Instance()
	require.Equal(t, string(cloudops.AWS), c.ProviderName())
	require.Equal(t, eksDistribution, c.K8sDistributionName())
	err = c.CheckCloudDrivePermission(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "env variable AWS_ZONE is not set")
}
