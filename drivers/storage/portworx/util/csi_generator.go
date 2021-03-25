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
	Version string
	// For Kube v1.12 and v1.13 we need to create the CRD
	// See: https://kubernetes-csi.github.io/docs/csi-node-object.html#enabling-csinodeinfo-on-kubernetes
	// When enabled it also means we need to use the Alpha version of CsiNodeInfo RBAC
	CreateCsiNodeCrd bool
	// Before Kube v1.13 the registration directory must be /var/lib/kubelet/plugins instead
	// of /var/lib/kubelet/plugins_registry
	UseOlderPluginsDirAsRegistration bool
	// DriverName is decided based on the PX Version unless the deprecated flag is provided.
	DriverName string
	// DriverRegistrationBasePath is the base path for CSI driver registration path
	DriverRegistrationBasePath string
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
	// IncludeEphemeralSupport adds the ephemeral volume capability to our CSI driver.
	// We support the ephemeral CSI driver mode in PX 2.5+
	IncludeEphemeralSupport bool
}

// CSIImages holds the images of all the CSI sidecar containers
type CSIImages struct {
	NodeRegistrar string
	Registrar     string
	Provisioner   string
	Attacher      string
	Snapshotter   string
	Resizer       string
}

// CSIGenerator contains information needed to generate CSI side car versions
type CSIGenerator struct {
	kubeVersion             version.Version
	pxVersion               version.Version
	useDeprecatedDriverName bool
	disableAlpha            bool
	kubeletPath             string
}

// NewCSIGenerator returns a version generator
func NewCSIGenerator(
	kubeVersion version.Version,
	pxVersion version.Version,
	useDeprecatedDriverName bool,
	disableAlpha bool,
	kubeletPath string,
) *CSIGenerator {
	return &CSIGenerator{
		kubeVersion:             kubeVersion,
		pxVersion:               pxVersion,
		useDeprecatedDriverName: useDeprecatedDriverName,
		disableAlpha:            disableAlpha,
		kubeletPath:             kubeletPath,
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
	pxVer2_2, _ := version.NewVersion("2.2")

	var cv *CSIConfiguration
	if g.kubeVersion.GreaterThan(k8sVer1_13) || g.kubeVersion.Equal(k8sVer1_13) {
		cv = g.getDefaultConfigV1_0()
	} else {
		cv = g.getDefaultConfigV0_4()
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

	// k8s 1.16 and earlier, CSI registration path should be csi-plugins.
	// See https://github.com/kubernetes-csi/node-driver-registrar/pull/59/files#diff-04c6e90faac2675aa89e2176d2eec7d8R41
	if g.kubeVersion.LessThan(k8sVer1_17) {
		cv.DriverRegistrationBasePath = g.kubeletPath + "/csi-plugins"
	} else {
		cv.DriverRegistrationBasePath = g.kubeletPath + "/plugins"
	}

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

	pxVer2_5, _ := version.NewVersion("2.5")
	if g.pxVersion.GreaterThanOrEqual(pxVer2_5) && g.kubeVersion.GreaterThanOrEqual(k8sVer1_16) {
		cv.IncludeEphemeralSupport = true
	}

	return cv
}

// GetCSIImages returns the appropriate sidecar images for the
// specified Kubernetes and Portworx versions
func (g *CSIGenerator) GetCSIImages() *CSIImages {
	var csiImages *CSIImages
	k8sVer1_13, _ := version.NewVersion("1.13")
	if g.kubeVersion.GreaterThan(k8sVer1_13) || g.kubeVersion.Equal(k8sVer1_13) {
		csiImages = getSidecarContainerVersionsV1_0()
	} else {
		csiImages = getSidecarContainerVersionsV0_4()
	}

	k8sVer1_14, _ := version.NewVersion("1.14")
	if (g.kubeVersion.GreaterThan(k8sVer1_13) || g.kubeVersion.Equal(k8sVer1_13)) &&
		g.kubeVersion.LessThan(k8sVer1_14) {
		csiImages.Snapshotter = "quay.io/openstorage/csi-snapshotter:v1.2.2-1"
	}

	return csiImages
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

func (g *CSIGenerator) getDefaultConfigV1_0() *CSIConfiguration {
	return &CSIConfiguration{
		UseDeployment: true,
	}
}

func (g *CSIGenerator) getDefaultConfigV0_4() *CSIConfiguration {
	pxVer2_1, _ := version.NewVersion("2.1")
	c := &CSIConfiguration{
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
	return path.Join(c.DriverRegistrationBasePath, c.DriverName)
}

func getSidecarContainerVersionsV1_0() *CSIImages {
	return &CSIImages{
		Attacher:      "quay.io/openstorage/csi-attacher:v1.2.1-1",
		NodeRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
		Provisioner:   "quay.io/openstorage/csi-provisioner:v1.4.0-1",
		Snapshotter:   "quay.io/k8scsi/csi-snapshotter:v2.0.0",
		Resizer:       "quay.io/k8scsi/csi-resizer:v0.3.0",
	}
}

func getSidecarContainerVersionsV0_4() *CSIImages {
	return &CSIImages{
		Attacher:    "quay.io/k8scsi/csi-attacher:v0.4.2",
		Registrar:   "quay.io/k8scsi/driver-registrar:v0.4.2",
		Provisioner: "quay.io/k8scsi/csi-provisioner:v0.4.3",
	}
}
