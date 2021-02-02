// +build integrationtest

package integrationtest

import (
	"flag"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s/operator"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	specDir = "./operator-test"

	defaultPxNamespace             = "kube-system"
	pxReleaseManifestURLEnvVarName = "PX_RELEASE_MANIFEST_URL"
	pxRegistryUserEnvVarName       = "REGISTRY_USER"
	pxRegistryPasswordEnvVarName   = "REGISTRY_PASS"

	defaultValidateStorageClusterTimeout       = 900 * time.Second
	defaultValidateStorageClusterRetryInterval = 30 * time.Second
	defaultValidateUninstallTimeout            = 900 * time.Second
	defaultValidateUninstallRetryInterval      = 30 * time.Second
)

var (
	pxDockerUsername string
	pxDockerPassword string

	pxSpecGenURL string
	pxEndpoint   string
)

func TestMain(m *testing.M) {
	flag.StringVar(&pxDockerUsername,
		"portworx-docker-username",
		"",
		"Portworx Docker username used for pull")
	flag.StringVar(&pxDockerPassword,
		"portworx-docker-password",
		"",
		"Portworx Docker password used for pull")
	flag.StringVar(&pxSpecGenURL,
		"portworx-spec-gen-url",
		"",
		"Portworx Spec Generator URL")
	flag.StringVar(&pxEndpoint,
		"portworx-endpoint",
		"",
		"Portworx Endpoint that defines the version of Portworx and its components")
	flag.Parse()
	os.Exit(m.Run())
}

// Here we make StorageCluster object and add all the common basic parameters that all StorageCluster should have
func constructStorageCluster() (*corev1.StorageCluster, error) {
	imageListMap, err := testutil.GetImagesFromVersionURL(pxSpecGenURL, pxEndpoint)
	if err != nil {
		return nil, err
	}

	cluster := &corev1.StorageCluster{}

	// Set Namespace
	cluster.Namespace = defaultPxNamespace

	// Set Portworx Image
	cluster.Spec.Image = imageListMap["version"]

	// Set release manifest URL in case of edge-install.portworx.com
	if strings.Contains(pxSpecGenURL, "edge") {
		releaseManifestURL, err := testutil.ConstructPxReleaseManifestURL(pxSpecGenURL, pxEndpoint)
		if err != nil {
			return nil, err
		}

		cluster.Spec.CommonConfig = corev1.CommonConfig{
			Env: []v1.EnvVar{
				{
					Name:  pxReleaseManifestURLEnvVarName,
					Value: releaseManifestURL,
				},
			},
		}

		// Add Portwrox Docker Credentials
		if pxDockerUsername != "" && pxDockerPassword != "" {
			newEnvVar := []v1.EnvVar{
				{
					Name:  pxRegistryUserEnvVarName,
					Value: pxDockerUsername,
				},
				{
					Name:  pxRegistryPasswordEnvVarName,
					Value: pxDockerPassword,
				},
			}
			cluster.Spec.CommonConfig.Env = append(cluster.Spec.CommonConfig.Env, newEnvVar...)
		}
	}

	return cluster, nil
}

func createStorageClusterFromSpec(filename string) (*corev1.StorageCluster, error) {
	filepath := path.Join(specDir, filename)
	scheme := runtime.NewScheme()
	cluster := &corev1.StorageCluster{}
	if err := k8sutil.ParseObjectFromFile(filepath, scheme, cluster); err != nil {
		return nil, err
	}
	if err := createStorageCluster(cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

func createStorageCluster(cluster *corev1.StorageCluster) error {
	_, err := operator.Instance().CreateStorageCluster(cluster)
	return err
}

func validateStorageClusterComponents(cluster *corev1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}
