// +build integrationtest

package integrationtest

import (
	"testing"
	"time"

	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
)

func TestBasic(t *testing.T) {
	t.Run("simpleInstall", testInstallWithEmptySpecWithAllDefaults)
}

func testInstallWithEmptySpecWithAllDefaults(t *testing.T) {
	pxImageList := make(map[string]string)
	cluster, err := createStorageClusterFromSpec("empty_spec.yaml")
	require.NoError(t, err)
	err = testutil.ValidateStorageCluster(pxImageList, cluster, 15*time.Minute, 15*time.Second)
	require.NoError(t, err)

	err = testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)
	err = testutil.ValidateUninstallStorageCluster(cluster, 15*time.Minute, 30*time.Second)
	require.NoError(t, err)
}
