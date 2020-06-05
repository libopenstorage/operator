package manifest

import (
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/sirupsen/logrus"
)

const (
	// envKeyReleaseManifestURL is an environment variable to override the
	// default release manifest download URL
	envKeyReleaseManifestURL = "PX_RELEASE_MANIFEST_URL"
	// DefaultPortworxVersion is the default portworx version that will be used
	// if none specified and if version manifest could not be fetched
	DefaultPortworxVersion = "2.5.2"
	defaultStorkImage      = "openstorage/stork:2.4.1"
	defaultAutopilotImage  = "portworx/autopilot:1.2.1"
	defaultLighthouseImage = "portworx/px-lighthouse:2.0.7"
	defaultNodeWiperImage  = "portworx/px-node-wiper:2.5.0"
)

// Release is a single release object with images for different components
type Release struct {
	Stork      string `yaml:"stork,omitempty"`
	Lighthouse string `yaml:"lighthouse,omitempty"`
	Autopilot  string `yaml:"autopilot,omitempty"`
	NodeWiper  string `yaml:"nodeWiper,omitempty"`
}

// Version is the response structure from a versions source
type Version struct {
	PortworxVersion string  `yaml:"version,omitempty"`
	Components      Release `yaml:"components,omitempty"`
}

type manifest interface {
	Get() (*Version, error)
}

// GetVersions returns the version manifest for the given cluster version
// The version manifest contains all the images of corresponding components
// that are to be installed with given cluster version.
func GetVersions(
	cluster *corev1alpha1.StorageCluster,
) *Version {
	var m manifest
	ver := pxutil.GetImageTag(cluster.Spec.Image)
	m = newDeprecatedManifest(ver)
	rel, err := m.Get()
	if err != nil {
		logrus.Errorf("Using default versions due to: %v", err)
		return defaultRelease()
	}

	fillDefaults(rel)
	return rel
}

func fillDefaults(rel *Version) {
	if rel.Components.Stork == "" {
		rel.Components.Stork = defaultStorkImage
	}
	if rel.Components.Autopilot == "" {
		rel.Components.Autopilot = defaultAutopilotImage
	}
	if rel.Components.Lighthouse == "" {
		rel.Components.Lighthouse = defaultLighthouseImage
	}
	if rel.Components.NodeWiper == "" {
		rel.Components.NodeWiper = defaultNodeWiperImage
	}
}

func defaultRelease() *Version {
	return &Version{
		PortworxVersion: DefaultPortworxVersion,
		Components: Release{
			Stork:      defaultStorkImage,
			Autopilot:  defaultAutopilotImage,
			Lighthouse: defaultLighthouseImage,
			NodeWiper:  defaultNodeWiperImage,
		},
	}
}
