package util

import (
	"path"

	version "github.com/hashicorp/go-version"
)

const (
	// CSIDriverName name of the portworx CSI driver
	CSIDriverName = "pxd.portworx.com"
	// DeprecatedCSIDriverName old name of the portworx CSI driver
	DeprecatedCSIDriverName = "com.openstorage.pxd"
)

// CSIConfiguration holds the versions of the all the CSI sidecar containers,
// containers, CSI Version, and other flags
type CSIConfiguration struct {
	Version       string
	Attacher      string
	Snapshotter   string
	Resizer       string
	NodeRegistrar string
	Provisioner   string
	// old pre-Kube 1.13 registrar
	Registrar string
	// For Kube v1.12 and v1.13 we need to create the CRD
	// See: https://kubernetes-csi.github.io/docs/csi-node-object.html#enabling-csinodeinfo-on-kubernetes
	// When enabled it also means we need to use the Alpha version of CsiNodeInfo RBAC
	CreateCsiNodeCrd bool
	// Before Kube v1.13 the registration directory must be /var/lib/kubelet/plugins instead
	// of /var/lib/kubelet/plugins_registry
	UseOlderPluginsDirAsRegistration bool
	// DriverName is decided based on the PX Version unless the deprecated flag is provided.
	DriverName string
	// UseDeployment decides whether to use a StatefulSet or Deployment for the CSI side cars.
	UseDeployment bool
	// IncludeAttacher dictates whether we include the csi-attacher sidecar or not.
	IncludeAttacher bool
	// IncludeResizer dicates whether or not to include the resizer sidecar.
	IncludeResizer bool
	// IncludeSnapshotter dicates whether or not to include the snapshotter sidecar.
	IncludeSnapshotter bool
	// IncludeCsiDriverInfo dictates whether or not to add the CSIDriver object.
	IncludeCsiDriverInfo bool
	// IncludeConfigMapsForLeases is used only in Kubernetes 1.13 for leader election.
	// In Kubernetes Kubernetes 1.14+ leader election does not use configmaps.
	IncludeEndpointsAndConfigMapsForLeases bool
}

// CSIGenerator contains information needed to generate CSI side car versions
type CSIGenerator struct {
	kubeVersion             version.Version
	pxVersion               version.Version
	useDeprecatedDriverName bool
	disableAlpha            bool
}

// NewCSIGenerator returns a version generator
func NewCSIGenerator(
	kubeVersion version.Version,
	pxVersion version.Version,
	useDeprecatedDriverName bool,
	disableAlpha bool,
) *CSIGenerator {
	return &CSIGenerator{
		kubeVersion:             kubeVersion,
		pxVersion:               pxVersion,
		useDeprecatedDriverName: useDeprecatedDriverName,
		disableAlpha:            disableAlpha,
	}
}

// GetBasicCSIConfiguration returns a basic CSI configuration
func (g *CSIGenerator) GetBasicCSIConfiguration() *CSIConfiguration {
	cv := new(CSIConfiguration)
	cv.DriverName = g.driverName()
	k8sVer1_14, _ := version.NewVersion("1.14")
	if g.kubeVersion.GreaterThan(k8sVer1_14) || g.kubeVersion.Equal(k8sVer1_14) {
		cv.IncludeCsiDriverInfo = true
	}
	return cv
}

// GetCSIConfiguration returns the appropriate side car versions
// for the specified Kubernetes and Portworx versions
func (g *CSIGenerator) GetCSIConfiguration() *CSIConfiguration {
	k8sVer1_12, _ := version.NewVersion("1.12")
	k8sVer1_13, _ := version.NewVersion("1.13")
	k8sVer1_14, _ := version.NewVersion("1.14")
	k8sVer1_16, _ := version.NewVersion("1.16")
	k8sVer1_17, _ := version.NewVersion("1.17")
	pxVer2_1, _ := version.NewVersion("2.1")
	pxVer2_2, _ := version.NewVersion("2.2")

	var cv *CSIConfiguration
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
		cv.IncludeEndpointsAndConfigMapsForLeases = true
	}

	// Enable resizer sidecar when PX >= 2.2 and k8s >= 1.14.0
	// Our CSI driver only supports resizing in PX 2.2.
	// Resize support is Alpha starting k8s 1.14
	if g.pxVersion.GreaterThan(pxVer2_2) || g.pxVersion.Equal(pxVer2_2) {
		if g.kubeVersion.GreaterThan(k8sVer1_16) || g.kubeVersion.Equal(k8sVer1_16) {
			cv.IncludeResizer = true
		} else if (g.kubeVersion.GreaterThan(k8sVer1_14) || g.kubeVersion.Equal(k8sVer1_14)) && !g.disableAlpha {
			cv.IncludeResizer = true
		}
	}

	// Snapshotter alpha support in k8s 1.16
	if (g.kubeVersion.GreaterThan(k8sVer1_12) || g.kubeVersion.Equal(k8sVer1_12)) && !g.disableAlpha {
		// Snapshotter support is alpha starting k8s 1.12
		cv.IncludeSnapshotter = true
	} else if g.kubeVersion.GreaterThan(k8sVer1_17) || g.kubeVersion.Equal(k8sVer1_17) {
		// Snapshotter support is beta starting k8s 1.17
		cv.IncludeSnapshotter = true
	}

	// Check if we need to setup the CsiNodeInfo CRD
	// If 1.12.0 <= KubeVer < 1.14.0 create the CRD
	if (g.kubeVersion.GreaterThan(k8sVer1_12) || g.kubeVersion.Equal(k8sVer1_12)) &&
		g.kubeVersion.LessThan(k8sVer1_14) {
		cv.CreateCsiNodeCrd = true
	}

	// Setup registration correctly
	if g.kubeVersion.LessThan(k8sVer1_13) {
		cv.UseOlderPluginsDirAsRegistration = true
	}

	// Set the CSI driver name
	cv.DriverName = g.driverName()

	// If we have k8s version < v1.14, we include the attacher.
	// This is because the CSIDriver object was alpha until 1.14+
	// To enable the CSIDriver object support, a feature flag would be needed.
	// Instead, we'll just include the attacher if k8s < 1.14.
	if g.kubeVersion.LessThan(k8sVer1_14) {
		cv.IncludeAttacher = true
		cv.IncludeCsiDriverInfo = false
	} else {
		// k8s >= 1.14 we can remove the attacher sidecar
		// in favor of the CSIDriver object.
		cv.IncludeAttacher = false
		cv.IncludeCsiDriverInfo = true
	}

	return cv
}

func (g *CSIGenerator) driverName() string {
	pxVer2_2, _ := version.NewVersion("2.2")
	// PX Versions <2.2.0 will always use the deprecated CSI Driver Name.
	// PX Versions >=2.2.0 will default to the new name
	if g.useDeprecatedDriverName || g.pxVersion.LessThan(pxVer2_2) {
		return DeprecatedCSIDriverName
	}
	return CSIDriverName
}

func (g *CSIGenerator) setSidecarContainerVersionsV1_0() *CSIConfiguration {
	return &CSIConfiguration{
		Attacher:      "quay.io/openstorage/csi-attacher:v1.2.1-1",
		NodeRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
		Provisioner:   "quay.io/openstorage/csi-provisioner:v1.4.0-1",
		Snapshotter:   "quay.io/openstorage/csi-snapshotter:v2.0.0",
		Resizer:       "quay.io/k8scsi/csi-resizer:v0.3.0",
		// Single registrar has been deprecated
		Registrar: "",
		// Deployments are support in 1.x
		UseDeployment: true,
	}
}

func (g *CSIGenerator) setSidecarContainerVersionsV0_4() *CSIConfiguration {
	pxVer2_1, _ := version.NewVersion("2.1")
	c := &CSIConfiguration{
		Attacher:    "quay.io/k8scsi/csi-attacher:v0.4.2",
		Registrar:   "quay.io/k8scsi/driver-registrar:v0.4.2",
		Provisioner: "quay.io/k8scsi/csi-provisioner:v0.4.3",
		Snapshotter: "",
		// no split driver registrars. These are for Csi 1.0 in Kube 1.13+
		NodeRegistrar: "",
		// Deployments not supported yet, so use statefulset
		UseDeployment: false,
	}

	// Force CSI 0.3 for Portworx version 2.1
	if g.pxVersion.GreaterThan(pxVer2_1) || g.pxVersion.Equal(pxVer2_1) {
		c.Version = "0.3"
	}
	return c
}

// DriverBasePath returns the basepath under which the CSI driver is stored
func (c *CSIConfiguration) DriverBasePath() string {
	return path.Join("/var/lib/kubelet/plugins", c.DriverName)
}
