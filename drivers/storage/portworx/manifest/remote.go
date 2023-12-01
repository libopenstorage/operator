package manifest

import (
	"fmt"
	"net/url"
	"path"

	version "github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
)

const (
	defaultVersionManifestBaseURL = "https://install.portworx.com"
)

type remote struct {
	cluster    *corev1.StorageCluster
	k8sVersion *version.Version
}

func newRemoteManifest(
	cluster *corev1.StorageCluster,
	k8sVersion *version.Version,
) versionProvider {
	return &remote{
		cluster:    cluster,
		k8sVersion: k8sVersion,
	}
}

func (m *remote) Get() (*Version, error) {
	// Use env var override if present. Should be used for testing
	// unreleased versions
	manifestURL := k8sutil.GetValueFromEnv(envKeyReleaseManifestURL, m.cluster.Spec.Env)
	if len(manifestURL) == 0 {
		pxVersion := pxutil.GetImageTag(m.cluster.Spec.Image)
		if len(pxVersion) > 0 {
			// If px version is an invalid semvar, then return the default release
			_, err := version.NewSemver(pxVersion)
			if err != nil {
				return nil, fmt.Errorf(
					"invalid Portworx version extracted from image %v. %v",
					m.cluster.Spec.Image, err)
			}
		}

		manifestURL = manifestURLFromVersion(pxVersion)
	}

	logrus.Infof("Getting version manifest from %v", manifestURL)
	return m.downloadVersionManifest(manifestURL)
}

func (m *remote) downloadVersionManifest(
	manifestURL string,
) (*Version, error) {
	u, err := url.Parse(manifestURL)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Add("kbver", m.k8sVersion.String())
	u.RawQuery = params.Encode()

	_, proxy := pxutil.GetPxProxyEnvVarValue(m.cluster)
	body, err := m.getManifestFromURL(u.String(), proxy)
	if err != nil {
		return nil, err
	}

	return parseVersionManifest(body)
}

func manifestURLFromVersion(version string) string {
	u, _ := url.Parse(defaultVersionManifestBaseURL)
	if len(version) > 0 {
		u.Path = path.Join(u.Path, version)
	}
	u.Path = path.Join(u.Path, "version")
	return u.String()
}
