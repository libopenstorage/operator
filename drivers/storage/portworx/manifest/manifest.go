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
	v1 "k8s.io/api/core/v1"
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
	DefaultPortworxVersion     = "2.13.0"
	defaultStorkImage          = "openstorage/stork:2.12.4"
	defaultAutopilotImage      = "portworx/autopilot:1.3.7"
	defaultLighthouseImage     = "portworx/px-lighthouse:2.0.7"
	defaultNodeWiperImage      = "portworx/px-node-wiper:2.13.1"
	defaultCCMJavaImage        = "purestorage/ccm-service:3.2.11"
	defaultCollectorProxyImage = "envoyproxy/envoy:v1.21.4"
	defaultCollectorImage      = "purestorage/realtime-metrics:1.0.4"
	defaultCCMGoImage          = "purestorage/ccm-go:1.0.3"
	defaultLogUploaderImage    = "purestorage/log-upload:1.0.0"
	defaultCCMGoProxyImage     = "purestorage/telemetry-envoy:1.0.1"
	defaultPxRepoImage         = "portworx/px-repo:1.1.0"

	// DefaultPrometheusOperatorImage is the default Prometheus operator image for k8s 1.21 and below
	DefaultPrometheusOperatorImage = "quay.io/coreos/prometheus-operator:v0.34.0"
	// DefaultPrometheusOperatorImage is the default grafana image to use
	DefaultGrafanaImage                   = "grafana/grafana:7.5.17"
	defaultPrometheusImage                = "quay.io/prometheus/prometheus:v2.7.1"
	defaultPrometheusConfigMapReloadImage = "quay.io/coreos/configmap-reload:v0.0.1"
	defaultPrometheusConfigReloaderImage  = "quay.io/coreos/prometheus-config-reloader:v0.34.0"
	defaultAlertManagerImage              = "quay.io/prometheus/alertmanager:v0.17.0"

	// Default new Prometheus images for k8s 1.22+
	defaultNewPrometheusOperatorImage       = "quay.io/prometheus-operator/prometheus-operator:v0.56.3"
	defaultNewPrometheusImage               = "quay.io/prometheus/prometheus:v2.35.0"
	defaultNewPrometheusConfigReloaderImage = "quay.io/prometheus-operator/prometheus-config-reloader:v0.56.3"
	defaultNewAlertManagerImage             = "quay.io/prometheus/alertmanager:v0.24.0"

	// default dynamic plugin images
	DefaultDynamicPluginImage      = "portworx/portworx-dynamic-plugin:1.1.0"
	DefaultDynamicPluginProxyImage = "nginxinc/nginx-unprivileged:1.23"

	defaultManifestRefreshInterval = 3 * time.Hour
)

var (
	instance Manifest
	once     sync.Once
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
	PxRepo                     string `yaml:"pxRepo,omitempty"`
	KubeScheduler              string `yaml:"kubeScheduler,omitempty"`
	KubeControllerManager      string `yaml:"kubeControllerManager,omitempty"`
	Pause                      string `yaml:"pause,omitempty"`
	DynamicPlugin              string `yaml:"dynamicPlugin,omitempty"`
	DynamicPluginProxy         string `yaml:"dynamicPluginProxy,omitempty"`
	CsiLivenessProbe           string `yaml:"csiLivenessProbe,omitempty"`
	CsiWindowsDriver           string `yaml:"csiWindowsDriver,omitempty"`
	CsiWindowsNodeRegistrar    string `yaml:"csiWindowsNodeRegistrar,omitempty"`
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
) *Version {
	var provider versionProvider
	ver := pxutil.GetImageTag(cluster.Spec.Image)
	currPxVer, err := version.NewSemver(ver)
	if err == nil {
		pxVer2_5_7, _ := version.NewVersion("2.5.7")
		if currPxVer.LessThan(pxVer2_5_7) {
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

	// Bug: if it fails due to temporarily network issue, we should retry.
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
			Stork:              defaultStorkImage,
			Autopilot:          defaultAutopilotImage,
			Lighthouse:         defaultLighthouseImage,
			NodeWiper:          defaultNodeWiperImage,
			PxRepo:             defaultPxRepoImage,
			DynamicPlugin:      DefaultDynamicPluginImage,
			DynamicPluginProxy: DefaultDynamicPluginProxyImage,
		},
	}
	fillCSIDefaults(rel, k8sVersion)
	fillPrometheusDefaults(rel, k8sVersion)
	fillGrafanaDefaults(rel, k8sVersion)
	fillTelemetryDefaults(rel)
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
	if rel.Components.PxRepo == "" {
		rel.Components.PxRepo = defaultPxRepoImage
	}

	if rel.Components.DynamicPlugin == "" {
		rel.Components.DynamicPlugin = DefaultDynamicPluginImage
	}

	if rel.Components.DynamicPluginProxy == "" {
		rel.Components.DynamicPluginProxy = DefaultDynamicPluginProxyImage
	}
	fillCSIDefaults(rel, k8sVersion)
	fillPrometheusDefaults(rel, k8sVersion)
	fillGrafanaDefaults(rel, k8sVersion)
	fillTelemetryDefaults(rel)
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
	csiGenerator := pxutil.NewCSIGenerator(*k8sVersion, *pxVersion, false, false, "", true)
	csiImages := csiGenerator.GetCSIImages()

	rel.Components.CSIProvisioner = csiImages.Provisioner
	rel.Components.CSIAttacher = csiImages.Attacher
	rel.Components.CSIDriverRegistrar = csiImages.Registrar
	rel.Components.CSINodeDriverRegistrar = csiImages.NodeRegistrar
	rel.Components.CSIResizer = csiImages.Resizer
	rel.Components.CSISnapshotter = csiImages.Snapshotter
	rel.Components.CSISnapshotController = csiImages.SnapshotController
	rel.Components.CSIHealthMonitorController = csiImages.HealthMonitorController
	rel.Components.CsiLivenessProbe = csiImages.LivenessProbe
	rel.Components.CsiWindowsDriver = csiImages.CsiDriverInstaller
	rel.Components.CsiWindowsNodeRegistrar = csiImages.CsiWindowsNodeRegistrar
}

func fillPrometheusDefaults(
	rel *Version,
	k8sVersion *version.Version,
) {
	if k8sVersion != nil && k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) {
		if rel.Components.Prometheus == "" {
			rel.Components.Prometheus = defaultNewPrometheusImage
		}
		if rel.Components.PrometheusOperator == "" {
			rel.Components.PrometheusOperator = defaultNewPrometheusOperatorImage
		}
		if rel.Components.PrometheusConfigReloader == "" {
			rel.Components.PrometheusConfigReloader = defaultNewPrometheusConfigReloaderImage
		}
		if rel.Components.AlertManager == "" {
			rel.Components.AlertManager = defaultNewAlertManagerImage
		}
		return
	}

	if rel.Components.Prometheus == "" {
		rel.Components.Prometheus = defaultPrometheusImage
	}
	if rel.Components.PrometheusOperator == "" {
		rel.Components.PrometheusOperator = DefaultPrometheusOperatorImage
	}
	if rel.Components.PrometheusConfigMapReload == "" {
		rel.Components.PrometheusConfigMapReload = defaultPrometheusConfigMapReloadImage
	}
	if rel.Components.PrometheusConfigReloader == "" {
		rel.Components.PrometheusConfigReloader = defaultPrometheusConfigReloaderImage
	}
	if rel.Components.AlertManager == "" {
		rel.Components.AlertManager = defaultAlertManagerImage
	}
}

func fillGrafanaDefaults(
	rel *Version,
	k8sVersion *version.Version,
) {
	if rel.Components.Grafana == "" {
		rel.Components.Grafana = DefaultGrafanaImage
	}
}

func fillTelemetryDefaults(
	rel *Version,
) {
	pxVersion, err := version.NewSemver(rel.PortworxVersion)
	if err == nil && !pxutil.IsCCMGoSupported(pxVersion) {
		if rel.Components.Telemetry == "" {
			rel.Components.Telemetry = defaultCCMJavaImage
		}
		if rel.Components.MetricsCollectorProxy == "" {
			rel.Components.MetricsCollectorProxy = defaultCollectorProxyImage
		}
	} else {
		if rel.Components.Telemetry == "" {
			rel.Components.Telemetry = defaultCCMGoImage
		}
		if rel.Components.LogUploader == "" {
			rel.Components.LogUploader = defaultLogUploaderImage
		}
		if rel.Components.TelemetryProxy == "" {
			rel.Components.TelemetryProxy = defaultCCMGoProxyImage
		}
	}
	if rel.Components.MetricsCollector == "" {
		rel.Components.MetricsCollector = defaultCollectorImage
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
