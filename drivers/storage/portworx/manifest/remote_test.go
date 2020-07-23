package manifest

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"testing"

	version "github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/api/core/v1"
)

func TestRemoteManifestWithMatchingVersion(t *testing.T) {
	pxVersion := "3.2.1"
	k8sVersion, _ := version.NewSemver("1.15.0")
	expectedManifestURL := manifestURLFromVersion(pxVersion)
	expectedManifestURL = expectedManifestURL + "?kbver=" + k8sVersion.String()
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, expectedManifestURL, url)
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
version: 3.2.1
components:
  stork: stork/image:3.2.1
`))),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + pxVersion,
		},
	}

	r, err := newRemoteManifest(cluster, k8sVersion).Get()
	require.NoError(t, err)
	require.Equal(t, pxVersion, r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)
}

func TestRemoteManifestWithoutMatchingVersion(t *testing.T) {
	pxVersion := "3.2.1"
	k8sVersion, _ := version.NewSemver("1.15.0")
	expectedManifestURL := manifestURLFromVersion(pxVersion)
	expectedManifestURL = expectedManifestURL + "?kbver=" + k8sVersion.String()
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, expectedManifestURL, url)
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
version: 3.2.1.1
components:
  stork: stork/image:3.2.1.1
`))),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:" + pxVersion,
		},
	}

	// Even if the image version does not match the one
	// returned by manifest, we still return those versions
	r, err := newRemoteManifest(cluster, k8sVersion).Get()
	require.NoError(t, err)
	require.Equal(t, "3.2.1.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1.1", r.Components.Stork)
}

func TestRemoteManifestWithoutVersion(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	expectedManifestURL := manifestURLFromVersion("")
	expectedManifestURL = expectedManifestURL + "?kbver=" + k8sVersion.String()
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, expectedManifestURL, url)
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
version: 3.2.1
components:
  stork: stork/image:3.2.1
`))),
		}, nil
	}

	// TestCase: Cluster with no Portworx image tag
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image",
		},
	}

	r, err := newRemoteManifest(cluster, k8sVersion).Get()
	require.NoError(t, err)
	require.Equal(t, "3.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)

	// TestCase: Cluster with no Portworx image
	cluster.Spec.Image = ""

	r, err = newRemoteManifest(cluster, k8sVersion).Get()
	require.NoError(t, err)
	require.Equal(t, "3.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)
}

func TestRemoteManifestWithInvalidVersion(t *testing.T) {
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:invalid_version",
		},
	}

	r, err := newRemoteManifest(cluster, nil).Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid Portworx version")
	require.Nil(t, r)
}

func TestRemoteManifestWithCustomURL(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	customURL := "http://edge-install/3.3.3/customversion"
	customURL = customURL + "?kbver=" + k8sVersion.String()
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, customURL, url)
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
version: 3.2.1
components:
  stork: stork/image:3.2.1
`))),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:custom_version",
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyReleaseManifestURL,
						Value: customURL,
					},
				},
			},
		},
	}

	// If a custom manifest URL is given we just blindly
	// return whatever versions are returned
	r, err := newRemoteManifest(cluster, k8sVersion).Get()
	require.NoError(t, err)
	require.Equal(t, "3.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)
}

func TestRemoteManifestWithInvalidResponse(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`invalid_yaml`))),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:3.2.1",
		},
	}

	r, err := newRemoteManifest(cluster, k8sVersion).Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot unmarshal")
	require.Nil(t, r)
}

func TestRemoteManifestWithEmptyResponse(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte{})),
		}, nil
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:3.2.1",
		},
	}

	r, err := newRemoteManifest(cluster, k8sVersion).Get()
	require.Equal(t, err, ErrReleaseNotFound)
	require.Nil(t, r)
}

func TestRemoteManifestWithFailedRequest(t *testing.T) {
	k8sVersion, _ := version.NewSemver("1.15.0")
	httpGet = func(url string) (*http.Response, error) {
		return nil, fmt.Errorf("http error")
	}

	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			Image: "px/image:3.2.1",
		},
	}

	// TestCase: HTTP request failed
	r, err := newRemoteManifest(cluster, k8sVersion).Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "http error")
	require.Nil(t, r)

	// TestCase: Failed to read the response body
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(&failedReader{}),
		}, nil
	}

	r, err = newRemoteManifest(cluster, k8sVersion).Get()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Read failed")
	require.Nil(t, r)
}
