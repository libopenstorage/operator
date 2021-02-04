package integrationtest

import (
	"strings"
	"testing"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestUpgrade(t *testing.T) {
	t.Run("testUpgradeStorageCluster", testUpgradeStorageCluster)
}

func testUpgradeStorageCluster(t *testing.T) {
	var err error
	var lastHopURL string
	var cluster *corev1.StorageCluster
	clusterName := "upgrade-test"

	upgradeHopURLs := strings.Split(pxUpgradeHopsURLList, ",")

	for ind, hopURL := range upgradeHopURLs {
		if ind == 0 {
			logrus.Infof("Deploying starting cluster using %s", hopURL)
		} else {
			logrus.Infof("Upgrading from %s to %s", lastHopURL, hopURL)
		}
		lastHopURL = hopURL

		// Get versions from URL
		logrus.Infof("Get component images from versions URL")
		imageListMap, err := testutil.GetImagesFromVersionURL(hopURL)
		require.NoError(t, err)

		if ind == 0 {
			// Construct Portworx StorageCluster object
			cluster, err = constructStorageCluster(hopURL, imageListMap)
			require.NoError(t, err)

			cluster.Name = clusterName

			// Deploy cluster
			logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
			cluster, err = createStorageCluster(cluster)
			require.NoError(t, err)
		} else {
			// Update cluster
			logrus.Infof("Update StorageCluster %s in %s", cluster.Name, cluster.Namespace)
			cluster, err = updateStorageCluster(cluster, hopURL, imageListMap)
			require.NoError(t, err)
		}

		// Validate cluster deployment
		logrus.Infof("Validate StorageCluster %s", cluster.Name)
		err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateUpgradeTimeout, defaultValidateUpgradeRetryInterval, "")
		require.NoError(t, err)
	}

	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
	require.NoError(t, err)
}
