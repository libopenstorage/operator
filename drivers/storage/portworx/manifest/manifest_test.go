package manifest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"testing"

	version "github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	yaml "gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

func TestManifestWithNewerPortworxVersion(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	expected := &Version{
		PortworxVersion: "2.6.0",
		Components: Release{
			Stork:                     "image/stork:2.6.0",
			Autopilot:                 "image/autopilo:2.6.0",
			Lighthouse:                "image/lighthouse:2.6.0",
			NodeWiper:                 "image/nodewiper:2.6.0",
			CSIProvisioner:            "image/csi-provisioner:2.6.0",
			Prometheus:                "image/prometheus:2.6.0",
			PrometheusOperator:        "image/prometheus-operator:2.6.0",
			PrometheusConfigMapReload: "image/configmap-reload:2.6.0",
			PrometheusConfigReloader:  "image/prometheus-config-reloader:2.6.0",
			AlertManager:              "image/alertmanager:2.6.0",
			Telemetry:                 "image/ccm-service:3.2.11",
			MetricsCollector:          "purestorage/realtime-metrics:1.0.1",
			MetricsCollectorProxy:     "envoyproxy/envoy:v1.21.4",
			DynamicPlugin:             "portworx/portworx-dynamic-plugin:1.1.0",
			DynamicPluginProxy:        "nginxinc/nginx-unprivileged:1.25",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(expected)
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	m := Instance()
	m.Init(testutil.FakeK8sClient(), nil, k8sVersion)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, expected, rel)
}

func TestManifestWithNewerPortworxVersionAndConfigMapPresent(t *testing.T) {
	expected := &Version{
		PortworxVersion: "2.6.0",
		Components: Release{
			Stork:                     "image/stork:2.6.0",
			Autopilot:                 "image/autopilo:2.6.0",
			Lighthouse:                "image/lighthouse:2.6.0",
			NodeWiper:                 "image/nodewiper:2.6.0",
			Prometheus:                "image/prometheus:2.6.0",
			PrometheusOperator:        "image/prometheus-operator:2.6.0",
			PrometheusConfigMapReload: "image/configmap-reload:2.6.0",
			PrometheusConfigReloader:  "image/prometheus-config-reloader:2.6.0",
			AlertManager:              "image/alertmanager:2.6.0",
			Telemetry:                 "image/ccm-service:2.6.0",
			MetricsCollector:          "purestorage/realtime-metrics:1.0.0",
			MetricsCollectorProxy:     "envoyproxy/envoy:v1.21.4",
			DynamicPlugin:             "portworx/portworx-dynamic-plugin:1.1.0",
			DynamicPluginProxy:        "nginxinc/nginx-unprivileged:1.25",
		},
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}
	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)
	// Add this to ensure configmap takes precedence over remote endpoint
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte(`
version: 3.2.1
components:
  stork: stork/image:3.2.1
`))),
		}, nil
	}

	m := Instance()
	m.Init(k8sClient, nil, nil)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, expected, rel)
}

func TestManifestWithNewerPortworxVersionAndFailure(t *testing.T) {
	httpGet = func(url string) (*http.Response, error) {
		return nil, fmt.Errorf("http error")
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.6.0",
		},
	}
	recorder := record.NewFakeRecorder(10)

	m := Instance()
	m.Init(testutil.FakeK8sClient(), recorder, k8sVersion)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, defaultRelease(k8sVersion), rel)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Using default version",
			v1.EventTypeWarning, util.FailedComponentReason))
}

func TestManifestWithOlderPortworxVersion(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	err := os.Symlink(linkPath, manifestDir)
	require.NoError(t, err)
	os.Remove(path.Join(linkPath, remoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, remoteReleaseManifest))
		os.RemoveAll(manifestDir)
		setupHTTPFailure()
	}()

	expected := &Version{
		PortworxVersion: "2.5.0",
		Components: Release{
			Stork:                     "image/stork:2.5.0",
			Autopilot:                 "image/autopilo:2.5.0",
			Lighthouse:                "image/lighthouse:2.5.0",
			NodeWiper:                 "image/nodewiper:2.5.0",
			Prometheus:                "image/prometheus:2.5.0",
			PrometheusOperator:        "image/prometheus-operator:2.5.0",
			PrometheusConfigMapReload: "image/configmap-reload:2.5.0",
			PrometheusConfigReloader:  "image/prometheus-config-reloader:2.5.0",
			AlertManager:              "image/alertmanager:2.6.0",
			Telemetry:                 "image/ccm-service:3.2.11",
			MetricsCollector:          "purestorage/realtime-metrics:1.0.1",
			MetricsCollectorProxy:     "envoyproxy/envoy:v1.21.4",
			DynamicPlugin:             "portworx/portworx-dynamic-plugin:1.1.0",
			DynamicPluginProxy:        "nginxinc/nginx-unprivileged:1.25",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(map[string]interface{}{
			"releases": map[string]*Release{
				expected.PortworxVersion: &expected.Components,
			},
		})
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	m := Instance()
	m.Init(testutil.FakeK8sClient(), nil, nil)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, expected, rel)
}

func TestManifestWithOlderPortworxVersionAndFailure(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	err := os.Symlink(linkPath, manifestDir)
	require.NoError(t, err)
	os.Remove(path.Join(linkPath, remoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, remoteReleaseManifest))
		os.RemoveAll(manifestDir)
		setupHTTPFailure()
	}()

	httpGet = func(url string) (*http.Response, error) {
		// Sending newer manifest to ensure if this gets called by older
		// manifest reader it will fail and also ensures that in case
		// newer manifest reader was called this would be the response
		body, _ := yaml.Marshal(map[string]*Release{
			"2.5.0": {},
		})
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:2.5.0",
		},
	}
	recorder := record.NewFakeRecorder(10)

	m := Instance()
	m.Init(testutil.FakeK8sClient(), recorder, k8sVersion)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, defaultRelease(k8sVersion), rel)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Using default version",
			v1.EventTypeWarning, util.FailedComponentReason))
}

func TestManifestWithKnownNonSemvarPortworxVersion(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	expected := &Version{
		PortworxVersion: "edge",
		Components: Release{
			Stork:                     "image/stork:2.6.0",
			Autopilot:                 "image/autopilo:2.6.0",
			Lighthouse:                "image/lighthouse:2.6.0",
			NodeWiper:                 "image/nodewiper:2.6.0",
			CSIProvisioner:            "image/csi-provisioner:2.6.0",
			Prometheus:                "image/prometheus:2.6.0",
			PrometheusOperator:        "image/prometheus-operator:2.6.0",
			PrometheusConfigMapReload: "image/configmap-reload:2.6.0",
			PrometheusConfigReloader:  "image/prometheus-config-reloader:2.6.0",
			AlertManager:              "image/alertmanager:2.6.0",
			Telemetry:                 "image/ccm-go:1.0.0",
			TelemetryProxy:            "purestorage/telemetry-envoy:1.0.0",
			LogUploader:               "purestorage/log-upload:1.0.0",
			MetricsCollector:          "purestorage/realtime-metrics:1.0.1",
			DynamicPlugin:             "portworx/portworx-dynamic-plugin:1.1.0",
			DynamicPluginProxy:        "nginxinc/nginx-unprivileged:1.25",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(expected)
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyReleaseManifestURL,
						Value: "http://custom-url",
					},
				},
			},
		},
	}

	m := Instance()
	m.Init(testutil.FakeK8sClient(), nil, k8sVersion)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, expected, rel)
}

func TestManifestWithUnknownNonSemvarPortworxVersion(t *testing.T) {
	httpGet = func(url string) (*http.Response, error) {
		// Return empty response without any versions
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte{})),
		}, nil
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:edge",
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyReleaseManifestURL,
						Value: "http://custom-url",
					},
				},
			},
		},
	}
	recorder := record.NewFakeRecorder(10)

	m := Instance()
	m.Init(testutil.FakeK8sClient(), recorder, k8sVersion)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, defaultRelease(k8sVersion), rel)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Using default version",
			v1.EventTypeWarning, util.FailedComponentReason))
}

func TestManifestWithoutPortworxVersion(t *testing.T) {
	expected := &Version{
		PortworxVersion: "2.6.0",
		Components: Release{
			Stork:                     "image/stork:2.6.0",
			Autopilot:                 "image/autopilo:2.6.0",
			Lighthouse:                "image/lighthouse:2.6.0",
			NodeWiper:                 "image/nodewiper:2.6.0",
			Prometheus:                "image/prometheus:2.6.0",
			PrometheusOperator:        "image/prometheus-operator:2.6.0",
			PrometheusConfigMapReload: "image/configmap-reload:2.6.0",
			PrometheusConfigReloader:  "image/prometheus-config-reloader:2.6.0",
			AlertManager:              "image/alertmanager:2.6.0",
			Telemetry:                 "image/ccm-service:3.2.11",
			MetricsCollector:          "purestorage/realtime-metrics:1.0.1",
			MetricsCollectorProxy:     "envoyproxy/envoy:v1.21.4",
			DynamicPlugin:             "portworx/portworx-dynamic-plugin:1.1.0",
			DynamicPluginProxy:        "nginxinc/nginx-unprivileged:1.25",
		},
	}
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image",
		},
	}
	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	m := Instance()
	m.Init(k8sClient, nil, nil)
	r := m.GetVersions(cluster, true)
	require.Equal(t, expected, r)
}

func TestManifestWithPartialComponents(t *testing.T) {
	expected := &Version{
		PortworxVersion: "2.11.0",
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	// TestCase: Partial components, use defaults for remaining
	expected.Components = Release{
		Stork:          "image/stork:3.0.0",
		NodeWiper:      "image/nodewiper:3.0.0",
		Prometheus:     "image/prometheus:3.0.0",
		CSIProvisioner: "image/csiprovisioner:3.0.0",
	}
	body, _ = yaml.Marshal(expected)
	versionsConfigMap.Data[VersionConfigMapKey] = string(body)
	err := k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)

	m := Instance()
	m.Init(k8sClient, nil, k8sVersion)
	rel := m.GetVersions(cluster, true)
	fillDefaults(expected, k8sVersion)
	require.Equal(t, expected, rel)
	require.Equal(t, "image/stork:3.0.0", rel.Components.Stork)
	require.Equal(t, "image/nodewiper:3.0.0", rel.Components.NodeWiper)
	require.Equal(t, defaultAutopilotImage, rel.Components.Autopilot)
	require.Equal(t, defaultLighthouseImage, rel.Components.Lighthouse)
	require.Equal(t, "image/prometheus:3.0.0", rel.Components.Prometheus)
	require.Equal(t, DefaultPrometheusOperatorImage, rel.Components.PrometheusOperator)
	require.Equal(t, defaultPrometheusConfigMapReloadImage, rel.Components.PrometheusConfigMapReload)
	require.Equal(t, defaultPrometheusConfigReloaderImage, rel.Components.PrometheusConfigReloader)
	require.Equal(t, defaultAlertManagerImage, rel.Components.AlertManager)
	require.Equal(t, defaultCCMJavaImage, rel.Components.Telemetry)
	require.Equal(t, "image/csiprovisioner:3.0.0", rel.Components.CSIProvisioner)
	require.Empty(t, rel.Components.CSIAttacher)

	// TestCase: No components at all, use all default components
	expected.PortworxVersion = "2.13.0"
	expected.Components = Release{}
	body, _ = yaml.Marshal(expected)
	versionsConfigMap.Data[VersionConfigMapKey] = string(body)
	err = k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)

	k8sVersion, _ = version.NewSemver("1.21.0")
	m.Init(k8sClient, nil, k8sVersion)
	rel = m.GetVersions(cluster, true)
	require.Equal(t, expected.PortworxVersion, rel.PortworxVersion)
	require.Equal(t, defaultRelease(k8sVersion).Components, rel.Components)
	require.Equal(t, "registry.k8s.io/sig-storage/csi-provisioner:v3.5.0", rel.Components.CSIProvisioner)

	// TestCase: No components at all, without k8s version
	expected.Components = Release{}
	body, _ = yaml.Marshal(expected)
	versionsConfigMap.Data[VersionConfigMapKey] = string(body)
	err = k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)

	m.Init(k8sClient, nil, nil)
	rel = m.GetVersions(cluster, true)
	require.Equal(t, expected.PortworxVersion, rel.PortworxVersion)
	require.Equal(t, defaultRelease(nil).Components, rel.Components)
	require.Empty(t, rel.Components.CSIProvisioner)
}

func TestManifestFillPrometheusDefaults(t *testing.T) {
	expected := &Version{
		PortworxVersion: "3.0.0",
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	body, _ = yaml.Marshal(expected)
	versionsConfigMap.Data[VersionConfigMapKey] = string(body)
	err := k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)

	m := Instance()
	m.Init(k8sClient, nil, k8sVersion)
	rel := m.GetVersions(cluster, true)
	fillDefaults(expected, k8sVersion)
	require.Equal(t, expected, rel)
	require.Equal(t, defaultPrometheusImage, rel.Components.Prometheus)
	require.Equal(t, DefaultPrometheusOperatorImage, rel.Components.PrometheusOperator)
	require.Equal(t, defaultPrometheusConfigMapReloadImage, rel.Components.PrometheusConfigMapReload)
	require.Equal(t, defaultPrometheusConfigReloaderImage, rel.Components.PrometheusConfigReloader)
	require.Equal(t, defaultAlertManagerImage, rel.Components.AlertManager)

	// TestCase: For k8s 1.22, default Prometheus images should be updated
	k8sVersion, _ = version.NewSemver("1.22.0")
	m.Init(k8sClient, nil, k8sVersion)
	rel = m.GetVersions(cluster, true)
	require.Equal(t, "quay.io/prometheus/prometheus:v2.35.0", rel.Components.Prometheus)
	require.Equal(t, "quay.io/prometheus-operator/prometheus-operator:v0.56.3", rel.Components.PrometheusOperator)
	require.Equal(t, "", rel.Components.PrometheusConfigMapReload)
	require.Equal(t, "quay.io/prometheus-operator/prometheus-config-reloader:v0.56.3", rel.Components.PrometheusConfigReloader)
	require.Equal(t, "quay.io/prometheus/alertmanager:v0.24.0", rel.Components.AlertManager)
}

func TestManifestWithForceFlagAndNewerManifest(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	expected := &Version{
		PortworxVersion: "2.6.0",
		Components: Release{
			Stork: "image/stork:2.6.0",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(expected)
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	m := &manifest{}
	SetInstance(m)
	m.Init(testutil.FakeK8sClient(), nil, k8sVersion)

	// TestCase: Should return the expected versions correct first time
	rel := m.GetVersions(cluster, false)
	require.Equal(t, expected.Components.Stork, rel.Components.Stork)

	// TestCase: Should not return the updated version if not forced
	expected.Components.Stork = "image/stork:2.6.1"
	rel = m.GetVersions(cluster, false)
	require.Equal(t, "image/stork:2.6.0", rel.Components.Stork)

	// TestCase: Should return the updated version if forced
	rel = m.GetVersions(cluster, true)
	require.Equal(t, "image/stork:2.6.1", rel.Components.Stork)
}

func TestManifestWithForceFlagAndOlderManifest(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	err := os.Symlink(linkPath, manifestDir)
	require.NoError(t, err)
	os.Remove(path.Join(linkPath, remoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, remoteReleaseManifest))
		os.RemoveAll(manifestDir)
		setupHTTPFailure()
	}()

	expected := &Version{
		PortworxVersion: "2.5.0",
		Components: Release{
			Stork: "image/stork:2.5.0",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(map[string]interface{}{
			"releases": map[string]*Release{
				expected.PortworxVersion: &expected.Components,
			},
		})
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	m := &manifest{}
	SetInstance(m)
	m.Init(testutil.FakeK8sClient(), nil, nil)

	// TestCase: Should return the expected versions correct first time
	rel := m.GetVersions(cluster, false)
	require.Equal(t, expected.Components.Stork, rel.Components.Stork)

	// TestCase: Should not return the updated version if not forced
	expected.Components.Stork = "image/stork:2.5.1"
	rel = m.GetVersions(cluster, false)
	require.Equal(t, "image/stork:2.5.0", rel.Components.Stork)

	// TestCase: Should return the updated version if forced
	rel = m.GetVersions(cluster, true)
	require.Equal(t, "image/stork:2.5.1", rel.Components.Stork)
}

func TestManifestWithForceFlagAndConfigMapManifest(t *testing.T) {
	expected := &Version{
		PortworxVersion: "2.6.0",
		Components: Release{
			Stork: "image/stork:2.6.0",
		},
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}
	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	m := &manifest{}
	SetInstance(m)
	m.Init(k8sClient, nil, nil)

	// TestCase: Should return the expected versions correct first time
	rel := m.GetVersions(cluster, false)
	require.Equal(t, expected.Components.Stork, rel.Components.Stork)

	// TestCase: Should return the updated version even if not forced
	expected.Components.Stork = "image/stork:2.5.1"
	body, _ = yaml.Marshal(expected)
	versionsConfigMap.Data[VersionConfigMapKey] = string(body)
	err := k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)

	rel = m.GetVersions(cluster, true)
	require.Equal(t, "image/stork:2.5.1", rel.Components.Stork)
}

func TestManifestOnCacheExpiryAndNewerVersion(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	expected := &Version{
		PortworxVersion: "2.6.0",
		Components: Release{
			Stork: "image/stork:2.6.0",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(expected)
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	m := &manifest{}
	SetInstance(m)
	m.Init(testutil.FakeK8sClient(), nil, k8sVersion)

	// TestCase: Should return the expected versions correct first time
	rel := m.GetVersions(cluster, false)
	require.Equal(t, expected.Components.Stork, rel.Components.Stork)

	// TestCase: Should not return the updated version if cache has not expired
	expected.Components.Stork = "image/stork:2.6.1"
	rel = m.GetVersions(cluster, false)
	require.Equal(t, "image/stork:2.6.0", rel.Components.Stork)

	// TestCase: Should return the updated version if cache has expired
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  envKeyReleaseManifestRefreshInterval,
			Value: "0",
		},
	}
	rel = m.GetVersions(cluster, false)
	require.Equal(t, "image/stork:2.6.1", rel.Components.Stork)
}

func TestManifestOnCacheExpiryAndOlderVersion(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	err := os.Symlink(linkPath, manifestDir)
	require.NoError(t, err)
	os.Remove(path.Join(linkPath, remoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, remoteReleaseManifest))
		os.RemoveAll(manifestDir)
		setupHTTPFailure()
	}()

	expected := &Version{
		PortworxVersion: "2.5.0",
		Components: Release{
			Stork: "image/stork:2.5.0",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(map[string]interface{}{
			"releases": map[string]*Release{
				expected.PortworxVersion: &expected.Components,
			},
		})
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	m := &manifest{}
	SetInstance(m)
	m.Init(testutil.FakeK8sClient(), nil, nil)

	// TestCase: Should return the expected versions correct first time
	rel := m.GetVersions(cluster, false)
	require.Equal(t, expected.Components.Stork, rel.Components.Stork)

	// TestCase: Should not return the updated version if cache has not expired
	expected.Components.Stork = "image/stork:2.5.1"
	rel = m.GetVersions(cluster, false)
	require.Equal(t, "image/stork:2.5.0", rel.Components.Stork)

	// TestCase: Should return the updated version if cache has expired
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  envKeyReleaseManifestRefreshInterval,
			Value: "0",
		},
	}
	os.Setenv(envKeyReleaseManifestRefreshInterval, "0")
	rel = m.GetVersions(cluster, false)
	require.Equal(t, "image/stork:2.5.1", rel.Components.Stork)
}

func TestManifestFillTelemetryDefaults(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.20.0")
	expected := &Version{
		PortworxVersion: "2.9.0",
	}
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}
	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			VersionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	// TestCase: default CCM Java images
	m := Instance()
	m.Init(k8sClient, nil, k8sVersion)
	rel := m.GetVersions(cluster, true)
	require.Equal(t, defaultCCMJavaImage, rel.Components.Telemetry)
	require.Equal(t, defaultCollectorProxyImage, rel.Components.MetricsCollectorProxy)
	require.Equal(t, defaultCollectorImage, rel.Components.MetricsCollector)
	require.Empty(t, rel.Components.LogUploader)
	require.Empty(t, rel.Components.TelemetryProxy)

	// TestCase: default CCM Go images
	expected = &Version{
		PortworxVersion: "2.12.0",
	}
	cluster.Spec.Image = "px/image:" + expected.PortworxVersion
	body, _ = yaml.Marshal(expected)
	versionsConfigMap.Data[VersionConfigMapKey] = string(body)
	err := k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)
	rel = m.GetVersions(cluster, true)
	require.Equal(t, defaultCCMGoImage, rel.Components.Telemetry)
	require.Equal(t, defaultCCMGoProxyImage, rel.Components.TelemetryProxy)
	require.Equal(t, defaultLogUploaderImage, rel.Components.LogUploader)
	require.Equal(t, defaultCollectorImage, rel.Components.MetricsCollector)
	require.Empty(t, rel.Components.MetricsCollectorProxy)

	// TestCase: default non-SemVerCCM use run CCM Go images
	expected = &Version{
		PortworxVersion: "abc_abc",
	}
	cluster.Spec.Image = "px/image:" + expected.PortworxVersion
	body, _ = yaml.Marshal(expected)
	versionsConfigMap.Data[VersionConfigMapKey] = string(body)
	err = k8sClient.Update(context.TODO(), versionsConfigMap)
	require.NoError(t, err)
	rel = m.GetVersions(cluster, true)
	require.Equal(t, defaultCCMGoImage, rel.Components.Telemetry)
	require.Equal(t, defaultCCMGoProxyImage, rel.Components.TelemetryProxy)
	require.Equal(t, defaultLogUploaderImage, rel.Components.LogUploader)
	require.Equal(t, defaultCollectorImage, rel.Components.MetricsCollector)
	require.Empty(t, rel.Components.MetricsCollectorProxy)
}

func TestMain(m *testing.M) {
	manifestCleanup(m)
	setupHTTPFailure()
	code := m.Run()
	manifestCleanup(m)
	httpGet = http.Get
	os.Exit(code)
}
