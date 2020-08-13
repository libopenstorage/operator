package util

import (
	"testing"

	version "github.com/hashicorp/go-version"
	"github.com/stretchr/testify/require"
)

func TestCSIImages(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.12.5")
	gen := NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "")
	images := gen.GetCSIImages()
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v0.4.2", images.Attacher)
	require.Equal(t, "quay.io/k8scsi/driver-registrar:v0.4.2", images.Registrar)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v0.4.3", images.Provisioner)

	k8sVersion, _ = version.NewSemver("1.13.5")
	gen = NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "")
	images = gen.GetCSIImages()
	require.Equal(t, "quay.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0", images.NodeRegistrar)
	require.Equal(t, "quay.io/openstorage/csi-provisioner:v1.4.0-1", images.Provisioner)
	require.Equal(t, "quay.io/openstorage/csi-snapshotter:v1.2.2-1", images.Snapshotter)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v0.3.0", images.Resizer)

	k8sVersion, _ = version.NewSemver("1.14.5")
	gen = NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "")
	images = gen.GetCSIImages()
	require.Equal(t, "quay.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0", images.NodeRegistrar)
	require.Equal(t, "quay.io/openstorage/csi-provisioner:v1.4.0-1", images.Provisioner)
	require.Equal(t, "quay.io/k8scsi/csi-snapshotter:v2.0.0", images.Snapshotter)
	require.Equal(t, "quay.io/k8scsi/csi-resizer:v0.3.0", images.Resizer)
}
