package manifest

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	version "github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// envKeyReleaseManifestURL is an environment variable to override the
	// default release manifest download URL
	envKeyReleaseManifestURL             = pxutil.EnvKeyPXReleaseManifestURL
	envKeyReleaseManifestRefreshInterval = "PX_RELEASE_MANIFEST_REFRESH_INTERVAL_MINS"

	// DefaultPortworxVersion is the default portworx version that will be used
	// if none specified and if version manifest could not be fetched
	DefaultPortworxVersion = "2.13.8"
	defaultStorkImage      = "openstorage/stork:23.8.0"
	defaultAutopilotImage  = "portworx/autopilot:1.3.11"
	defaultNodeWiperImage  = "portworx/px-node-wiper:2.13.2"

	// DefaultPrometheusOperatorImage is the default Prometheus operator image for k8s 1.21 and below
	DefaultPrometheusOperatorImage        = "quay.io/coreos/prometheus-operator:v0.34.0"
	defaultPrometheusImage                = "quay.io/prometheus/prometheus:v2.7.1"
	defaultPrometheusConfigMapReloadImage = "quay.io/coreos/configmap-reload:v0.0.1"
	defaultPrometheusConfigReloaderImage  = "quay.io/coreos/prometheus-config-reloader:v0.34.0"
	defaultAlertManagerImage              = "quay.io/prometheus/alertmanager:v0.17.0"
	// DefaultGrafanaImage is the default grafana image to use
	DefaultGrafanaImage = "grafana/grafana:7.5.17"

	defaultManifestRefreshInterval = 3 * time.Hour
)

var (
	instance      Manifest
	once          sync.Once
	pxVer2_5_7, _ = version.NewVersion("2.5.7")
	pxVer3_1_0, _ = version.NewVersion("3.1.0")
)

// Release is a single release object with images for different components
// -note: please keep in sync w/ `storagecluster.ComponentImages` structure
type Release struct {
	Stork                      string `yaml:"stork,omitempty"`
	Lighthouse                 string `yaml:"lighthouse,omitempty"`
	Autopilot                  string `yaml:"autopilot,omitempty"`
	NodeWiper                  string `yaml:"nodeWiper,omitempty"`
	CSIDriverRegistrar         string `yaml:"csiDriverRegistrar,omitempty"`
	CSINodeDriverRegistrar     string `yaml:"csiNodeDriverRegistrar,omitempty"`
	CSIProvisioner             string `yaml:"csiProvisioner,omitempty"`
	CSIAttacher                string `yaml:"csiAttacher,omitempty"`
	CSIResizer                 string `yaml:"csiResizer,omitempty"`
	CSISnapshotter             string `yaml:"csiSnapshotter,omitempty"`
	CSISnapshotController      string `yaml:"csiSnapshotController,omitempty"`
	CSIHealthMonitorController string `yaml:"csiHealthMonitorController,omitempty"`
	Prometheus                 string `yaml:"prometheus,omitempty"`
	Grafana                    string `yaml:"grafana,omitempty"`
	AlertManager               string `yaml:"alertManager,omitempty"`
	PrometheusOperator         string `yaml:"prometheusOperator,omitempty"`
	PrometheusConfigMapReload  string `yaml:"prometheusConfigMapReload,omitempty"`
	PrometheusConfigReloader   string `yaml:"prometheusConfigReloader,omitempty"`
	Telemetry                  string `yaml:"telemetry,omitempty"`
	MetricsCollector           string `yaml:"metricsCollector,omitempty"`
	MetricsCollectorProxy      string `yaml:"metricsCollectorProxy,omitempty"`
	LogUploader                string `yaml:"logUploader,omitempty"`
	TelemetryProxy             string `yaml:"telemetryProxy,omitempty"` // Use a new field for easy backward compatibility
	KubeScheduler              string `yaml:"kubeScheduler,omitempty"`
	KubeControllerManager      string `yaml:"kubeControllerManager,omitempty"`
	Pause                      string `yaml:"pause,omitempty"`
	DynamicPlugin              string `yaml:"dynamicPlugin,omitempty"`
	DynamicPluginProxy         string `yaml:"dynamicPluginProxy,omitempty"`
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
	GetVersions(*corev1.StorageCluster, bool) (*Version, error)
	// CanAccessRemoteManifest is used to test remote manifest to decide whether it's air-gapped env.
	CanAccessRemoteManifest(cluster *corev1.StorageCluster) bool
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

func (m *manifest) CanAccessRemoteManifest(cluster *corev1.StorageCluster) bool {
	provider := newRemoteManifest(cluster, m.k8sVersion)

	var err error
	maxAttempts := 5
	for i := 0; i < maxAttempts; i++ {
		_, err = provider.Get()
		if err == nil {
			return true
		}
		time.Sleep(time.Second)
	}

	logrus.Infof("Could not access remote manifest after %d attempts: %v", maxAttempts, err)
	return false
}

// GetVersions returns the version manifest for the given cluster version
// The version manifest contains all the images of corresponding components
// that are to be installed with given cluster version.
func (m *manifest) GetVersions(
	cluster *corev1.StorageCluster,
	force bool,
) (*Version, error) {
	var provider versionProvider
	ver := pxutil.GetImageTag(cluster.Spec.Image)
	currPxVer, err := version.NewSemver(ver)
	if currPxVer == nil || err != nil {
		// note, dev-bulids like `c2bb2a0_14e4543` won't parse correctly, so adding this as a failback
		currPxVer = pxutil.GetPortworxVersion(cluster)
	}
	if currPxVer != nil && currPxVer.LessThan(pxVer2_5_7) {
		provider = newDeprecatedManifest(ver)
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
		return m.cachedVersions.DeepCopy(), nil
	}

	// Retry every 2 seconds for 5 times.
	var rel *Version
	for i := 0; i < 5; i++ {
		rel, err = provider.Get()
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		msg := fmt.Sprintf("versions from px-versions ConfigMap cannot be fetched and URL is unreachable due to: %v", err)
		return nil, fmt.Errorf(msg)
	}

	if currPxVer != nil && currPxVer.GreaterThanOrEqual(pxVer3_1_0) && rel.Components.NodeWiper == "" {
		rel.Components.NodeWiper = cluster.Spec.Image
	}
	fillDefaults(rel, m.k8sVersion)
	m.lastUpdated = time.Now()
	m.cachedVersions = rel
	return rel.DeepCopy(), nil
}

func fillDefaults(
	rel *Version,
	k8sVersion *version.Version,
) {
	// uses oci image after 3.10, before that use 2.13
	if rel.Components.NodeWiper == "" {
		rel.Components.NodeWiper = defaultNodeWiperImage
	}
	fillStorkDefaults(rel, k8sVersion)
	// Grafana is always default
	fillGrafanaDefaults(rel)
	fillK8sDefaults(rel, k8sVersion)
}

func fillStorkDefaults(
	rel *Version,
	k8sVersion *version.Version,
) {
	if rel.Components.KubeScheduler == "" {
		rel.Components.KubeScheduler = k8sutil.GetDefaultKubeSchedulerImage(k8sVersion)
	}
}

func fillK8sDefaults(
	rel *Version,
	k8sVersion *version.Version,
) {
	if rel.Components.KubeControllerManager == "" {
		rel.Components.KubeControllerManager = k8sutil.GetDefaultKubeControllerManagerImage(k8sVersion)
	}

	if rel.Components.Pause == "" {
		rel.Components.Pause = pxutil.ImageNamePause
	}
}

func fillGrafanaDefaults(
	rel *Version,
) {
	if rel.Components.Grafana == "" {
		rel.Components.Grafana = DefaultGrafanaImage
	}
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
