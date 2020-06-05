package manifest

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"testing"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/stretchr/testify/require"
	yaml "gopkg.in/yaml.v2"
	"k8s.io/api/core/v1"
)

func TestManifestWithNewerPortworxVersion(t *testing.T) {
	expected := &Version{
		PortworxVersion: "2.6.0",
		Components: Release{
			Stork:      "image/stork:2.6.0",
			Autopilot:  "image/autopilo:2.6.0",
			Lighthouse: "image/lighthouse:2.6.0",
			NodeWiper:  "image/nodewiper:2.6.0",
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

	rel := GetVersions(cluster)
	require.Equal(t, expected, rel)
}

func TestManifestWithNewerPortworxVersionAndFailure(t *testing.T) {
	httpGet = func(url string) (*http.Response, error) {
		return nil, fmt.Errorf("http error")
	}

	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.6.0",
		},
	}

	rel := GetVersions(cluster)
	require.Equal(t, defaultRelease(), rel)
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
			Stork:      "image/stork:2.5.0",
			Autopilot:  "image/autopilo:2.5.0",
			Lighthouse: "image/lighthouse:2.5.0",
			NodeWiper:  "image/nodewiper:2.5.0",
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

	rel := GetVersions(cluster)
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

	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:2.5.0",
		},
	}

	rel := GetVersions(cluster)
	require.Equal(t, defaultRelease(), rel)
}

func TestManifestWithKnownNonSemvarPortworxVersion(t *testing.T) {
	expected := &Version{
		PortworxVersion: "edge",
		Components: Release{
			Stork:      "image/stork:2.6.0",
			Autopilot:  "image/autopilo:2.6.0",
			Lighthouse: "image/lighthouse:2.6.0",
			NodeWiper:  "image/nodewiper:2.6.0",
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

	rel := GetVersions(cluster)
	require.Equal(t, expected, rel)
}

func TestManifestWithUnknownNonSemvarPortworxVersion(t *testing.T) {
	httpGet = func(url string) (*http.Response, error) {
		// Return empty response without any versions
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte{})),
		}, nil
	}

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

	rel := GetVersions(cluster)
	require.Equal(t, defaultRelease(), rel)
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

	cluster := &corev1alpha1.StorageCluster{
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "px/image:" + expected.PortworxVersion,
		},
	}

	// TestCase: Partial components, use defaults for remaining
	expected.Components = Release{
		Stork:     "image/stork:3.0.0",
		NodeWiper: "image/nodewiper:3.0.0",
	}

	rel := GetVersions(cluster)
	fillDefaults(expected)
	require.Equal(t, expected, rel)
	require.Equal(t, "image/stork:3.0.0", rel.Components.Stork)
	require.Equal(t, "image/nodewiper:3.0.0", rel.Components.NodeWiper)
	require.Equal(t, defaultAutopilotImage, rel.Components.Autopilot)
	require.Equal(t, defaultLighthouseImage, rel.Components.Lighthouse)

	// TestCase: No components at all, use all default components
	expected.Components = Release{}

	rel = GetVersions(cluster)
	require.Equal(t, expected.PortworxVersion, rel.PortworxVersion)
	require.Equal(t, defaultRelease().Components, rel.Components)
}

func TestMain(m *testing.M) {
	manifestCleanup(m)
	setupHTTPFailure()
	code := m.Run()
	manifestCleanup(m)
	httpGet = http.Get
	os.Exit(code)
}
