package manifest

import (
	"fmt"
	"net/url"
	"path"

	version "github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
)

const (
	defaultVersionManifestBaseURL = "https://install.portworx.com"
)

type remote struct {
	cluster *corev1alpha1.StorageCluster
}

func newRemoteManifest(cluster *corev1alpha1.StorageCluster) manifest {
	return &remote{
		cluster: cluster,
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

	logrus.Debugf("Getting version manifest from %v", manifestURL)
	return downloadVersionManifest(manifestURL)
}

func downloadVersionManifest(
	manifestURL string,
) (*Version, error) {
	body, err := getManifestFromURL(manifestURL)
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
