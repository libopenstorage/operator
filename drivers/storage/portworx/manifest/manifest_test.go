package manifest

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"testing"

	version "github.com/hashicorp/go-version"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	yaml "gopkg.in/yaml.v2"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

func TestManifestWithNewerPortworxVersion(t *testing.T) {
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
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(expected)
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	rel := GetVersions(cluster, testutil.FakeK8sClient(), nil, nil)
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
		},
	}

	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}
	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			versionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)
	// Add this to ensure configmap takes precedence over remote endpoint
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
version: 3.2.1
components:
  stork: stork/image:3.2.1
`))),
		}, nil
	}

	rel := GetVersions(cluster, k8sClient, nil, nil)
	require.Equal(t, expected, rel)
}

func TestManifestWithNewerPortworxVersionAndFailure(t *testing.T) {
	httpGet = func(url string) (*http.Response, error) {
		return nil, fmt.Errorf("http error")
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.6.0",
		},
	}
	recorder := record.NewFakeRecorder(10)

	rel := GetVersions(cluster, testutil.FakeK8sClient(), recorder, k8sVersion)
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
	os.Symlink(linkPath, manifestDir)
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
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(map[string]interface{}{
			"releases": map[string]*Release{
				expected.PortworxVersion: &expected.Components,
			},
		})
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	rel := GetVersions(cluster, testutil.FakeK8sClient(), nil, nil)
	require.Equal(t, expected, rel)
}

func TestManifestWithOlderPortworxVersionAndFailure(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	os.Symlink(linkPath, manifestDir)
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
			Body: ioutil.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.5.0",
		},
	}
	recorder := record.NewFakeRecorder(10)

	rel := GetVersions(cluster, testutil.FakeK8sClient(), recorder, k8sVersion)
	require.Equal(t, defaultRelease(k8sVersion), rel)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Using default version",
			v1.EventTypeWarning, util.FailedComponentReason))
}

func TestManifestWithKnownNonSemvarPortworxVersion(t *testing.T) {
	expected := &Version{
		PortworxVersion: "edge",
		Components: Release{
			Stork:                     "image/stork:2.6.0",
			Autopilot:                 "image/autopilo:2.6.0",
			Lighthouse:                "image/lighthouse:2.6.0",
			NodeWiper:                 "image/nodewiper:2.6.0",
			Prometheus:                "image/prometheus:2.6.0",
			PrometheusOperator:        "image/prometheus-operator:2.6.0",
			PrometheusConfigMapReload: "image/configmap-reload:2.6.0",
			PrometheusConfigReloader:  "image/prometheus-config-reloader:2.6.0",
		},
	}
	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(expected)
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
			CommonConfig: corev1alpha1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyReleaseManifestURL,
						Value: "http://custom-url",
					},
				},
			},
		},
	}

	rel := GetVersions(cluster, testutil.FakeK8sClient(), nil, nil)
	require.Equal(t, expected, rel)
}

func TestManifestWithUnknownNonSemvarPortworxVersion(t *testing.T) {
	httpGet = func(url string) (*http.Response, error) {
		// Return empty response without any versions
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte{})),
		}, nil
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:edge",
			CommonConfig: corev1alpha1.CommonConfig{
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

	rel := GetVersions(cluster, testutil.FakeK8sClient(), recorder, k8sVersion)
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
		},
	}
	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image",
		},
	}
	body, _ := yaml.Marshal(expected)
	versionsConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		Data: map[string]string{
			versionConfigMapKey: string(body),
		},
	}
	k8sClient := testutil.FakeK8sClient(versionsConfigMap)

	r := GetVersions(cluster, k8sClient, nil, nil)
	require.Equal(t, expected, r)
}

func TestManifestWithPartialComponents(t *testing.T) {
	expected := &Version{
		PortworxVersion: "3.0.0",
	}

	httpGet = func(url string) (*http.Response, error) {
		body, _ := yaml.Marshal(expected)
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader(body)),
		}, nil
	}

	k8sVersion, _ := version.NewSemver("1.16.8")
	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	// TestCase: Partial components, use defaults for remaining
	expected.Components = Release{
		Stork:          "image/stork:3.0.0",
		NodeWiper:      "image/nodewiper:3.0.0",
		Prometheus:     "image/prometheus:3.0.0",
		CSIProvisioner: "image/csiprovisioner:3.0.0",
	}

	rel := GetVersions(cluster, testutil.FakeK8sClient(), nil, k8sVersion)
	fillDefaults(expected, k8sVersion)
	require.Equal(t, expected, rel)
	require.Equal(t, "image/stork:3.0.0", rel.Components.Stork)
	require.Equal(t, "image/nodewiper:3.0.0", rel.Components.NodeWiper)
	require.Equal(t, defaultAutopilotImage, rel.Components.Autopilot)
	require.Equal(t, defaultLighthouseImage, rel.Components.Lighthouse)
	require.Equal(t, "image/prometheus:3.0.0", rel.Components.Prometheus)
	require.Equal(t, defaultPrometheusOperatorImage, rel.Components.PrometheusOperator)
	require.Equal(t, defaultPrometheusConfigMapReloadImage, rel.Components.PrometheusConfigMapReload)
	require.Equal(t, defaultPrometheusConfigReloaderImage, rel.Components.PrometheusConfigReloader)
	require.Equal(t, "image/csiprovisioner:3.0.0", rel.Components.CSIProvisioner)
	require.Empty(t, rel.Components.CSIAttacher)

	// TestCase: No components at all, use all default components
	expected.Components = Release{}

	rel = GetVersions(cluster, testutil.FakeK8sClient(), nil, k8sVersion)
	require.Equal(t, expected.PortworxVersion, rel.PortworxVersion)
	require.Equal(t, defaultRelease(k8sVersion).Components, rel.Components)
	require.Equal(t, "quay.io/openstorage/csi-provisioner:v1.4.0-1", rel.Components.CSIProvisioner)

	// TestCase: No components at all, without k8s version
	expected.Components = Release{}

	rel = GetVersions(cluster, testutil.FakeK8sClient(), nil, nil)
	require.Equal(t, expected.PortworxVersion, rel.PortworxVersion)
	require.Equal(t, defaultRelease(nil).Components, rel.Components)
	require.Empty(t, rel.Components.CSIProvisioner)
}

func TestMain(m *testing.M) {
	manifestCleanup(m)
	setupHTTPFailure()
	code := m.Run()
	manifestCleanup(m)
	httpGet = http.Get
	os.Exit(code)
}
