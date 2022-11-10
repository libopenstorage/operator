package cloudprovider

import (
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNew(t *testing.T) {
	cp := New("foo")
	require.NotNil(t, cp, "Unexpected error on New")
	require.Equal(t, "foo", cp.Name(), "Unexpected name of default provider")

	cp = New(azureName)
	require.NotNil(t, cp, "Unexpected error on New")
	require.Equal(t, azureName, cp.Name(), "Unexpected name of default provider")
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
			Labels: map[string]string{failureDomainZoneKey: "bar"},
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
				failureDomainZoneKey:        "bar",
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
				failureDomainZoneKey:      "bar",
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
				failureDomainZoneKey:      "bar",
				v1.LabelZoneFailureDomain: "pxzone3"},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "bar", zone, "Unexpected zone returned")
}

func TestAzureGetZoneNodeNil(t *testing.T) {
	cp := New(azureName)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(nil)
	require.Error(t, err, "Expected an error on nil Node object")
	require.Equal(t, "", zone, "Unexpected zone returned")
}

func TestAzureGetZoneAvailabilityZone(t *testing.T) {
	cp := New(azureName)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				failureDomainZoneKey:   "region-0",
				failureDomainRegionKey: "region",
			},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "region-0", zone, "Unexpected zone returned")
}

func TestAzureGetZoneAvailabilitySet(t *testing.T) {
	cp := New(azureName)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				failureDomainZoneKey:   "0",
				failureDomainRegionKey: "region",
			},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "", zone, "Unexpected zone returned")
}

func TestAzureGetZoneNoRegion(t *testing.T) {
	cp := New(azureName)
	require.NotNil(t, cp, "Unexpected error on New")

	zone, err := cp.GetZone(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Labels: map[string]string{
				failureDomainZoneKey: "region-0",
			},
		},
	})
	require.NoError(t, err, "Expected an error on nil Node object")
	require.Equal(t, "", zone, "Unexpected zone returned")
}
