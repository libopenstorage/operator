// +build integrationtest

package integrationtest

import (
	"testing"
	"time"

	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestBasic(t *testing.T) {
	t.Run("simpleInstall", testInstallWithEmptySpecWithAllDefaults)
}

func testInstallWithEmptySpecWithAllDefaults(t *testing.T) {
	// Deploy cluster from spec
	logrus.Infof("Create StorageCluster from spec")
	cluster, err := createStorageClusterFromSpec("empty_spec.yaml")
	require.NoError(t, err)

	// Validate cluster deployment
	logrus.Infof("Get component images from versions URL")
	imageListMap, err := testutil.GetImagesFromVersionURL(defaultPxSpecGenURL, defaultPxSpecGenEndpoint)
	require.NoError(t, err)

	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateStorageClusterTimeout, defaultValidateStorageClusterRetryInterval, "")
	require.NoError(t, err)

	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, 15*time.Minute, 30*time.Second)
	require.NoError(t, err)
}
