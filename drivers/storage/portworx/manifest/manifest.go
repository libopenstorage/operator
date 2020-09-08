package manifest

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	version "github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// envKeyReleaseManifestURL is an environment variable to override the
	// default release manifest download URL
	envKeyReleaseManifestURL             = "PX_RELEASE_MANIFEST_URL"
	envKeyReleaseManifestRefreshInterval = "PX_RELEASE_MANIFEST_REFRESH_INTERVAL_MINS"
	// DefaultPortworxVersion is the default portworx version that will be used
	// if none specified and if version manifest could not be fetched
	DefaultPortworxVersion                = "2.5.2"
	defaultStorkImage                     = "openstorage/stork:2.4.1"
	defaultAutopilotImage                 = "portworx/autopilot:1.2.1"
	defaultLighthouseImage                = "portworx/px-lighthouse:2.0.7"
	defaultNodeWiperImage                 = "portworx/px-node-wiper:2.5.0"
	defaultPrometheusImage                = "quay.io/prometheus/prometheus:v2.7.1"
	defaultPrometheusOperatorImage        = "quay.io/coreos/prometheus-operator:v0.34.0"
	defaultPrometheusConfigMapReloadImage = "quay.io/coreos/configmap-reload:v0.0.1"
	defaultPrometheusConfigReloaderImage  = "quay.io/coreos/prometheus-config-reloader:v0.34.0"
	defaultManifestRefreshInterval        = 3 * time.Hour
)

var (
	instance Manifest
	once     sync.Once
)

// Release is a single release object with images for different components
type Release struct {
	Stork                     string `yaml:"stork,omitempty"`
	Lighthouse                string `yaml:"lighthouse,omitempty"`
	Autopilot                 string `yaml:"autopilot,omitempty"`
	NodeWiper                 string `yaml:"nodeWiper,omitempty"`
	CSIDriverRegistrar        string `yaml:"csiDriverRegistrar,omitempty"`
	CSINodeDriverRegistrar    string `yaml:"csiNodeDriverRegistrar,omitempty"`
	CSIProvisioner            string `yaml:"csiProvisioner,omitempty"`
	CSIAttacher               string `yaml:"csiAttacher,omitempty"`
	CSIResizer                string `yaml:"csiResizer,omitempty"`
	CSISnapshotter            string `yaml:"csiSnapshotter,omitempty"`
	Prometheus                string `yaml:"prometheus,omitempty"`
	PrometheusOperator        string `yaml:"prometheusOperator,omitempty"`
	PrometheusConfigMapReload string `yaml:"prometheusConfigMapReload,omitempty"`
	PrometheusConfigReloader  string `yaml:"prometheusConfigReloader,omitempty"`
}

// Version is the response structure from a versions source
type Version struct {
	PortworxVersion string  `yaml:"version,omitempty"`
	Components      Release `yaml:"components,omitempty"`
}

// versionProvider is an interface for different providers that
// can return version information
type versionProvider interface {
	Get() (*Version, error)
}

// Manifest is an interface for getting versions information
type Manifest interface {
	// Init initialize the manifest
	Init(client.Client, record.EventRecorder, *version.Version)
	// GetVersions return the correct release versions for given cluster
	GetVersions(*corev1.StorageCluster, bool) *Version
}

// Instance returns a single instance of Manifest if present, else
// creates a new one and returns it
func Instance() Manifest {
	once.Do(func() {
		if instance == nil {
			instance = &manifest{}
		}
	})
	return instance
}

// SetInstance can be used to set Manifest singleton instance
// This will be helpful for testing with a mock instance
func SetInstance(m Manifest) {
	instance = m
}

type manifest struct {
	k8sClient      client.Client
	recorder       record.EventRecorder
	k8sVersion     *version.Version
	cachedVersions *Version
	lastUpdated    time.Time
}

func (m *manifest) Init(
	k8sClient client.Client,
	recorder record.EventRecorder,
	k8sVersion *version.Version,
) {
	m.k8sClient = k8sClient
	m.recorder = recorder
	m.k8sVersion = k8sVersion
}

// GetVersions returns the version manifest for the given cluster version
// The version manifest contains all the images of corresponding components
// that are to be installed with given cluster version.
func (m *manifest) GetVersions(
	cluster *corev1.StorageCluster,
	force bool,
) *Version {
	var provider versionProvider
	ver := pxutil.GetImageTag(cluster.Spec.Image)
	currPxVer, err := version.NewSemver(ver)
	if err == nil {
		pxVer2_6, _ := version.NewVersion("2.6")
		if currPxVer.LessThan(pxVer2_6) {
			provider = newDeprecatedManifest(ver)
		}
	}

	if provider == nil {
		provider, err = newConfigMapManifest(m.k8sClient, cluster)
		if err != nil {
			logrus.Debugf("Unable to get versions from ConfigMap. %v", err)
			provider = newRemoteManifest(cluster, m.k8sVersion)
		}
	}

	cacheExpired := m.lastUpdated.Add(refreshInterval(cluster)).Before(time.Now())
	if _, ok := provider.(*configMap); !ok && !cacheExpired && !force {
		return m.cachedVersions.DeepCopy()
	}

	rel, err := provider.Get()
	if err != nil {
		msg := fmt.Sprintf("Using default version due to: %v", err)
		logrus.Error(msg)
		m.recorder.Event(cluster, v1.EventTypeWarning, util.FailedComponentReason, msg)
		return defaultRelease(m.k8sVersion)
	}

	fillDefaults(rel, m.k8sVersion)
	m.lastUpdated = time.Now()
	m.cachedVersions = rel
	return rel.DeepCopy()
}

func defaultRelease(
	k8sVersion *version.Version,
) *Version {
	rel := &Version{
		PortworxVersion: DefaultPortworxVersion,
		Components: Release{
			Stork:                     defaultStorkImage,
			Autopilot:                 defaultAutopilotImage,
			Lighthouse:                defaultLighthouseImage,
			NodeWiper:                 defaultNodeWiperImage,
			Prometheus:                defaultPrometheusImage,
			PrometheusOperator:        defaultPrometheusOperatorImage,
			PrometheusConfigMapReload: defaultPrometheusConfigMapReloadImage,
			PrometheusConfigReloader:  defaultPrometheusConfigReloaderImage,
		},
	}
	fillCSIDefaults(rel, k8sVersion)
	return rel
}

func fillDefaults(
	rel *Version,
	k8sVersion *version.Version,
) {
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
	if rel.Components.Prometheus == "" {
		rel.Components.Prometheus = defaultPrometheusImage
	}
	if rel.Components.PrometheusOperator == "" {
		rel.Components.PrometheusOperator = defaultPrometheusOperatorImage
	}
	if rel.Components.PrometheusConfigMapReload == "" {
		rel.Components.PrometheusConfigMapReload = defaultPrometheusConfigMapReloadImage
	}
	if rel.Components.PrometheusConfigReloader == "" {
		rel.Components.PrometheusConfigReloader = defaultPrometheusConfigReloaderImage
	}
	fillCSIDefaults(rel, k8sVersion)
}

func fillCSIDefaults(
	rel *Version,
	k8sVersion *version.Version,
) {
	if k8sVersion == nil || rel.Components.CSIProvisioner != "" {
		return
	}

	logrus.Debugf("CSI images not found in manifest, using default")
	pxVersion, _ := version.NewSemver(DefaultPortworxVersion)
	csiGenerator := pxutil.NewCSIGenerator(*k8sVersion, *pxVersion, false, false, "")
	csiImages := csiGenerator.GetCSIImages()

	rel.Components.CSIProvisioner = csiImages.Provisioner
	rel.Components.CSIAttacher = csiImages.Attacher
	rel.Components.CSIDriverRegistrar = csiImages.Registrar
	rel.Components.CSINodeDriverRegistrar = csiImages.NodeRegistrar
	rel.Components.CSIResizer = csiImages.Resizer
	rel.Components.CSISnapshotter = csiImages.Snapshotter
}

func refreshInterval(cluster *corev1.StorageCluster) time.Duration {
	intervalStr := k8sutil.GetValueFromEnv(envKeyReleaseManifestRefreshInterval, cluster.Spec.Env)
	if interval, err := strconv.Atoi(intervalStr); err == nil {
		return time.Duration(interval) * time.Minute
	}
	return defaultManifestRefreshInterval
}

// DeepCopy returns a deepcopy of the version object
func (in *Version) DeepCopy() *Version {
	if in == nil {
		return nil
	}
	out := new(Version)
	out.PortworxVersion = in.PortworxVersion
	out.Components = in.Components
	return out
}
