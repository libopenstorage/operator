package portworx

import (
	"fmt"

	version "github.com/hashicorp/go-version"
)

const (
	// CSIDriverName name of the portworx CSI driver
	CSIDriverName = "pxd.portworx.com"
	// DeprecatedCSIDriverName old name of the portworx CSI driver
	DeprecatedCSIDriverName = "com.openstorage.pxd"
)

// csiVersions holds the versions of the all the CSI sidecar containers
type csiVersions struct {
	version       string
	attacher      string
	snapshotter   string
	resizer       string
	nodeRegistrar string
	provisioner   string
	// old pre-Kube 1.13 registrar
	registrar string
	// For Kube v1.12 and v1.13 we need to create the CRD
	// See: https://kubernetes-csi.github.io/docs/csi-node-object.html#enabling-csinodeinfo-on-kubernetes
	// When enabled it also means we need to use the Alpha version of CsiNodeInfo RBAC
	createCsiNodeCrd bool
	// Before Kube v1.13 the registration directory must be /var/lib/kubelet/plugins instead
	// of /var/lib/kubelet/plugins_registry
	useOlderPluginsDirAsRegistration bool
	// driverName is decided based on the PX Version unless the deprecated flag is provided.
	driverName string
	// useDeployment decides whether to use a StatefulSet or Deployment for the CSI side cars.
	useDeployment bool
	// includeAttacher dictates whether we include the csi-attacher sidecar or not.
	includeAttacher bool
	// includeResizer dicates whether or not to include the resizer sidecar.
	includeResizer bool
	// includeCsiDriverInfo dictates whether or not to add the CsiDriverInfo object.
	includeCsiDriverInfo bool
	// includeConfigMapsForLeases is used only in Kubernetes 1.13 for leader election.
	// In Kubernetes Kubernetes 1.14+ leader election does not use configmaps.
	includeEndpointsAndConfigMapsForLeases bool
}

// csiGenerator contains information needed to generate CSI side car versions
type csiGenerator struct {
	kubeVersion             version.Version
	pxVersion               version.Version
	useDeprecatedDriverName bool
}

// newCSIGenerator returns a version generator
func newCSIGenerator(
	kubeVersion version.Version,
	pxVersion version.Version,
	useDeprecatedDriverName bool,
) *csiGenerator {
	return &csiGenerator{
		kubeVersion:             kubeVersion,
		pxVersion:               pxVersion,
		useDeprecatedDriverName: useDeprecatedDriverName,
	}
}

// getSidecarContainerVersions returns the appropriate side car versions
// for the specified Kubernetes and Portworx versions
func (g *csiGenerator) getSidecarContainerVersions() (csiVersions, error) {
	k8sVer1_11, _ := version.NewVersion("1.11")
	k8sVer1_12, _ := version.NewVersion("1.12")
	k8sVer1_13, _ := version.NewVersion("1.13")
	k8sVer1_14, _ := version.NewVersion("1.14")
	pxVer2_1, _ := version.NewVersion("2.1")
	pxVer2_2, _ := version.NewVersion("2.2")

	// Only 1.11+ is supported because only CSI 0.3 or CSI 1.x are supported
	if g.kubeVersion.LessThan(k8sVer1_11) {
		return csiVersions{}, fmt.Errorf("kubernetes version %v does not have a "+
			"compatible CSI version with the version of Portworx", g.kubeVersion.String())
	}

	var cv *csiVersions
	if g.pxVersion.GreaterThan(pxVer2_1) || g.pxVersion.Equal(pxVer2_1) {
		// Portworx version >= 2.1
		// Can only use Csi 1.0 in Kubernetes 1.13+
		if g.kubeVersion.GreaterThan(k8sVer1_13) || g.kubeVersion.Equal(k8sVer1_13) {
			cv = g.setSidecarContainerVersionsV1_0()
		} else {
			cv = g.setSidecarContainerVersionsV0_4()
		}
	} else {
		cv = g.setSidecarContainerVersionsV0_4()
	}

	// Check if configmaps are necessary for leader election.
	// If it is  >=1.13.0 and and <1.14.0
	if (g.kubeVersion.GreaterThan(k8sVer1_13) || g.kubeVersion.Equal(k8sVer1_13)) &&
		g.kubeVersion.LessThan(k8sVer1_14) {
		cv.includeEndpointsAndConfigMapsForLeases = true
	}

	// Feature flags for Portworx version >= 2.2
	if g.pxVersion.GreaterThan(pxVer2_2) || g.pxVersion.Equal(pxVer2_2) {
		// Resizing supported in portworx >= 2.2
		cv.includeResizer = true
	}

	// Check if we need to setup the CsiNodeInfo CRD
	// If 1.12.0 <= KubeVer < 1.14.0 create the CRD
	if (g.kubeVersion.GreaterThan(k8sVer1_12) || g.kubeVersion.Equal(k8sVer1_12)) &&
		g.kubeVersion.LessThan(k8sVer1_14) {
		cv.createCsiNodeCrd = true
	}

	// Setup registration correctly
	if g.kubeVersion.LessThan(k8sVer1_13) {
		cv.useOlderPluginsDirAsRegistration = true
	}

	// Set the CSI driver name
	// - PX Versions <2.2.0 will always use the deprecated CSI Driver Name.
	// - PX Versions >=2.2.0 will default to the new name
	if g.useDeprecatedDriverName || g.pxVersion.LessThan(pxVer2_2) {
		cv.driverName = DeprecatedCSIDriverName
	} else {
		cv.driverName = CSIDriverName
	}

	// If we have k8s version < v1.14, we include the attacher.
	// This is because the CSIDriver object was alpha until 1.14+
	// To enable the CSIDriver object support, a feature flag would be needed.
	// Instead, we'll just include the attacher if k8s < 1.14.
	if g.kubeVersion.LessThan(k8sVer1_14) {
		cv.includeAttacher = true
		cv.includeCsiDriverInfo = false
	} else {
		// k8s >= 1.14 we can remove the attacher sidecar
		// in favor of the CSIDriver object.
		cv.includeAttacher = false
		cv.includeCsiDriverInfo = true
	}

	return *cv, nil
}

func (g *csiGenerator) setSidecarContainerVersionsV1_0() *csiVersions {
	return &csiVersions{
		attacher:      "quay.io/openstorage/csi-attacher:v1.2.1-1",
		nodeRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
		provisioner:   "quay.io/openstorage/csi-provisioner:v1.3.0-1",
		snapshotter:   "quay.io/openstorage/csi-snapshotter:v1.2.0-1",
		resizer:       "quay.io/openstorage/csi-resizer:v0.2.0-1",
		// Single registrar has been deprecated
		registrar: "",
		// Deployments are support in 1.x
		useDeployment: true,
	}
}

func (g *csiGenerator) setSidecarContainerVersionsV0_4() *csiVersions {
	pxVer2_1, _ := version.NewVersion("2.1")
	c := &csiVersions{
		attacher:    "quay.io/k8scsi/csi-attacher:v0.4.2",
		registrar:   "quay.io/k8scsi/driver-registrar:v0.4.2",
		provisioner: "quay.io/k8scsi/csi-provisioner:v0.4.2",
		snapshotter: "",
		// no split driver registrars. These are for Csi 1.0 in Kube 1.13+
		nodeRegistrar: "",
		// Deployments not supported yet, so use statefulset
		useDeployment: false,
	}

	// Force CSI 0.3 for Portworx version 2.1
	if g.pxVersion.GreaterThan(pxVer2_1) || g.pxVersion.Equal(pxVer2_1) {
		c.version = "0.3"
	}
	return c
}
