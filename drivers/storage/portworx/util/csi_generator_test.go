package util

import (
	"testing"

	version "github.com/hashicorp/go-version"
	"github.com/stretchr/testify/require"
)

func TestCSIImages(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.12.5")
	gen := NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "", false)
	images := gen.GetCSIImages()
	require.Equal(t, "quay.io/k8scsi/csi-attacher:v0.4.2", images.Attacher)
	require.Equal(t, "quay.io/k8scsi/driver-registrar:v0.4.2", images.Registrar)
	require.Equal(t, "quay.io/k8scsi/csi-provisioner:v0.4.3", images.Provisioner)

	k8sVersion, _ = version.NewSemver("1.13.5")
	gen = NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "", false)
	images = gen.GetCSIImages()
	require.Equal(t, "docker.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0", images.NodeRegistrar)
	require.Equal(t, "docker.io/openstorage/csi-provisioner:v1.6.1-1", images.Provisioner)
	require.Equal(t, "quay.io/openstorage/csi-snapshotter:v1.2.2-1", images.Snapshotter)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0", images.Resizer)

	k8sVersion, _ = version.NewSemver("1.14.5")
	gen = NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "", false)
	images = gen.GetCSIImages()
	require.Equal(t, "docker.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0", images.NodeRegistrar)
	require.Equal(t, "docker.io/openstorage/csi-provisioner:v1.6.1-1", images.Provisioner)
	require.Equal(t, "docker.io/openstorage/csi-snapshotter:v1.2.2-1", images.Snapshotter)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0", images.Resizer)

	k8sVersion, _ = version.NewSemver("1.18.5")
	gen = NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "", false)
	images = gen.GetCSIImages()
	require.Equal(t, "docker.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0", images.NodeRegistrar)
	require.Equal(t, "docker.io/openstorage/csi-provisioner:v2.2.2-1", images.Provisioner)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-snapshotter:v3.0.3", images.Snapshotter)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0", images.Resizer)

	k8sVersion, _ = version.NewSemver("1.20.4")
	gen = NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "", false)
	images = gen.GetCSIImages()
	require.Equal(t, "docker.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0", images.NodeRegistrar)
	require.Equal(t, "docker.io/openstorage/csi-provisioner:v3.2.1-1", images.Provisioner)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-snapshotter:v6.2.2", images.Snapshotter)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0", images.Resizer)

	k8sVersion, _ = version.NewSemver("1.20.4")
	gen = NewCSIGenerator(*k8sVersion, version.Version{}, false, false, "", true)
	images = gen.GetCSIImages()
	require.Equal(t, "docker.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0", images.NodeRegistrar)
	require.Equal(t, "docker.io/openstorage/csi-provisioner:v3.2.1-1", images.Provisioner)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-snapshotter:v6.2.2", images.Snapshotter)
	require.Equal(t, "registry.k8s.io/sig-storage/snapshot-controller:v6.2.2", images.SnapshotController)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0", images.Resizer)

	k8sVersion, _ = version.NewSemver("1.23.4")
	gen = NewCSIGenerator(*k8sVersion, *pxVer2_10, false, false, "", true)
	images = gen.GetCSIImages()
	require.Equal(t, "docker.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0", images.NodeRegistrar)
	require.Equal(t, "docker.io/openstorage/csi-provisioner:v3.2.1-1", images.Provisioner)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-snapshotter:v6.2.2", images.Snapshotter)
	require.Equal(t, "registry.k8s.io/sig-storage/snapshot-controller:v6.2.2", images.SnapshotController)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0", images.Resizer)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-external-health-monitor-controller:v0.7.0", images.HealthMonitorController)

	k8sVersion, _ = version.NewSemver("1.23.4")
	gen = NewCSIGenerator(*k8sVersion, *pxVer2_13, false, false, "", true)
	images = gen.GetCSIImages()
	require.Equal(t, "docker.io/openstorage/csi-attacher:v1.2.1-1", images.Attacher)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0", images.NodeRegistrar)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-provisioner:v3.5.0", images.Provisioner)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-snapshotter:v6.2.2", images.Snapshotter)
	require.Equal(t, "registry.k8s.io/sig-storage/snapshot-controller:v6.2.2", images.SnapshotController)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-resizer:v1.8.0", images.Resizer)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-external-health-monitor-controller:v0.7.0", images.HealthMonitorController)
}
