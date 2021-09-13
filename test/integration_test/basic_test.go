// +build integrationtest

package integrationtest

import (
	"testing"

	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestBasic(t *testing.T) {
	t.Run("testBasicInstallWithAllDefaults", testBasicInstallWithAllDefaults)
}

func testBasicInstallWithAllDefaults(t *testing.T) {
	// Get versions from URL
	logrus.Infof("Get component images from versions URL")
	imageListMap, err := testutil.GetImagesFromVersionURL(pxSpecGenURL)
	require.NoError(t, err)

	// Construct Portworx StorageCluster object
	cluster, err := constructStorageCluster(pxSpecGenURL, imageListMap)
	require.NoError(t, err)

	cluster.Name = "simple-install"

	// Deploy cluster
	logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	cluster, err = createStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deployment
	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateDeployTimeout, defaultValidateDeployRetryInterval, true, "")
	require.NoError(t, err)

	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
	require.NoError(t, err)
}
