package cloudprovider

import (
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/operator/pkg/preflight"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
)

func TestNew(t *testing.T) {
	cp := New("foo")
	require.NotNil(t, cp, "Unexpected error on New")
	require.Equal(t, "foo", cp.Name(), "Unexpected name of default provider")

	cp = New(cloudops.Azure)
	require.NotNil(t, cp, "Unexpected error on New")
	require.Equal(t, cloudops.Azure, cp.Name(), "Unexpected name of default provider")
}

func TestDefaultGetZoneNodeNil(t *testing.T) {
	cp := New("default")
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(nil)
	require.Error(t, err, "Expected an error on nil Node object")
	require.Equal(t, "", zone, "Unexpected zone returned")
}

func TestDefaultGetZone(t *testing.T) {
	cp := New("default")
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node1",
			Labels: map[string]string{v1.LabelTopologyZone: "bar"},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "bar", zone, "Unexpected zone returned")
}

func TestDefaultGetZonePriority(t *testing.T) {
	cp := New("default")
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				v1.LabelTopologyZone:        "bar",
				"topology.portworx.io/zone": "pxzone1",
				"px/zone":                   "pxzone2",
				v1.LabelZoneFailureDomain:   "pxzone3"},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "pxzone1", zone, "Unexpected zone returned")

	zone, err = cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				v1.LabelTopologyZone:      "bar",
				"px/zone":                 "pxzone2",
				v1.LabelZoneFailureDomain: "pxzone3"},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "pxzone2", zone, "Unexpected zone returned")

	zone, err = cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				v1.LabelTopologyZone:      "bar",
				v1.LabelZoneFailureDomain: "pxzone3"},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "bar", zone, "Unexpected zone returned")
}

func TestAzureGetZoneNodeNil(t *testing.T) {
	cp := New(cloudops.Azure)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(nil)
	require.Error(t, err, "Expected an error on nil Node object")
	require.Equal(t, "", zone, "Unexpected zone returned")
}

func TestAzureGetZoneAvailabilityZone(t *testing.T) {
	cp := New(cloudops.Azure)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				v1.LabelTopologyZone:   "region-0",
				v1.LabelTopologyRegion: "region",
			},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "region-0", zone, "Unexpected zone returned")
}

func TestAzureGetZoneAvailabilitySet(t *testing.T) {
	cp := New(cloudops.Azure)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				v1.LabelTopologyZone:   "0",
				v1.LabelTopologyRegion: "region",
			},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "", zone, "Unexpected zone returned")
}

func TestAzureGetZoneNoRegion(t *testing.T) {
	cp := New(cloudops.Azure)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				v1.LabelTopologyZone: "region-0",
			},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "", zone, "Unexpected zone returned")
}

func TestGetProvidersFromPreflightChecker(t *testing.T) {
	// TestCase: get default provider when preflight not initialized
	preflight.SetInstance(nil)
	cp := Get()
	require.NotNil(t, cp)
	require.Empty(t, cp.Name())

	// TestCase: get default provider
	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	err := preflight.InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)
	cp = Get()
	require.Empty(t, cp.Name())

	// TestCase: get azure cloud provider
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Spec: v1.NodeSpec{ProviderID: "azure://node-id-3"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	err = preflight.InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)
	cp = Get()
	require.Equal(t, cloudops.Azure, cp.Name())

	// TestCase: init aws cloud provider
	fakeK8sNodes = &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Spec: v1.NodeSpec{ProviderID: "aws://node-id-3"}},
	}}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeK8sNodes)))
	err = preflight.InitPreflightChecker(testutil.FakeK8sClient(fakeK8sNodes))
	require.NoError(t, err)
	cp = Get()
	require.Equal(t, string(cloudops.AWS), cp.Name())
}
