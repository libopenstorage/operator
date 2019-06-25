package portworx

import (
	"fmt"

	version "github.com/hashicorp/go-version"
)

// csiVersions holds the versions of the all the CSI sidecar containers
type csiVersions struct {
	attacher         string
	snapshotter      string
	nodeRegistrar    string
	clusterRegistrar string
	version          string
	provisionerImage string
	// old pre-Kube 1.13 registrar
	registrar string
	// For Kube v1.12 and v1.13 we need to create the CRD
	// See: https://kubernetes-csi.github.io/docs/csi-node-object.html#enabling-csinodeinfo-on-kubernetes
	// When enabled it also means we need to use the Alpha version of CsiNodeInfo RBAC
	createCsiNodeCrd bool
	// Before Kube v1.13 the registration directory must be /var/lib/kubelet/plugins instead
	// of /var/lib/kubelet/plugins_registry
	useOlderPluginsDirAsRegistration bool
}

// csiGenerator contains information needed to generate CSI side car versions
type csiGenerator struct {
	kubeVer version.Version
}

// newCSIGenerator returns a version generator
func newCSIGenerator(kubeVer version.Version) *csiGenerator {
	return &csiGenerator{
		kubeVer: kubeVer,
	}
}

// getSidecarContainerVersions returns the appropriate side car versions
// for the specified Kubernetes and Portworx versions
func (g *csiGenerator) getSidecarContainerVersions() (csiVersions, error) {
	k8sVer1_11, _ := version.NewVersion("1.11")
	k8sVer1_12, _ := version.NewVersion("1.12")
	k8sVer1_13, _ := version.NewVersion("1.13")
	k8sVer1_14, _ := version.NewVersion("1.14")

	// Only 1.11+ is supported because only CSI 0.3 or CSI 1.x are supported
	if g.kubeVer.LessThan(k8sVer1_11) {
		return csiVersions{}, fmt.Errorf("kubernetes version %v does not have a "+
			"compatible CSI version with the version of Portworx", g.kubeVer.String())
	}

	var cv *csiVersions
	// Supported only for Portworx version >= 2.1
	// Can only use Csi 1.0 in Kubernetes 1.13+
	if g.kubeVer.GreaterThan(k8sVer1_13) || g.kubeVer.Equal(k8sVer1_13) {
		cv = g.setSidecarContainerVersionsV1_0()
	} else {
		cv = g.setSidecarContainerVersionsV0_4()
	}

	// Check if we need to setup the CsiNodeInfo CRD
	// If 1.12.0 <= KubeVer < 1.14.0 create the CRD
	if (g.kubeVer.GreaterThan(k8sVer1_12) || g.kubeVer.Equal(k8sVer1_12)) &&
		g.kubeVer.LessThan(k8sVer1_14) {
		cv.createCsiNodeCrd = true
	}

	// Setup registration correctly
	if g.kubeVer.LessThan(k8sVer1_13) {
		cv.useOlderPluginsDirAsRegistration = true
	}

	return *cv, nil
}

func (g *csiGenerator) setSidecarContainerVersionsV1_0() *csiVersions {
	return &csiVersions{
		attacher:         "1.1.1",
		nodeRegistrar:    "1.1.0",
		clusterRegistrar: "1.0.1",
		provisionerImage: "1.1.0",
		// This is still Alpha, let's not add it
		// snapshotter: "1.1.0",
		snapshotter: "",
		// Single registrar has been deprecated
		registrar: "",
	}
}

func (g *csiGenerator) setSidecarContainerVersionsV0_4() *csiVersions {
	return &csiVersions{
		version:          "0.3",
		attacher:         "0.4.2",
		registrar:        "0.4.2",
		provisionerImage: "0.4.2",
		// This is still Alpha, let's not add it
		// snapshotter: "0.4.1",
		snapshotter: "",
		// No split driver registrars. Those are for CSI 1.0 in Kube 1.13+
		nodeRegistrar:    "",
		clusterRegistrar: "",
	}
}
