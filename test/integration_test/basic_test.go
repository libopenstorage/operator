// +build integrationtest

package integrationtest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBasic(t *testing.T) {
	t.Run("simpleInstall", testInstallWithEmptySpecWithAllDefaults)
}

func testInstallWithEmptySpecWithAllDefaults(t *testing.T) {
	cluster, err := createStorageClusterFromSpec("empty_spec.yaml")
	require.NoError(t, err)
	err = validateStorageCluster(cluster, 15*time.Minute, 15*time.Second)
	require.NoError(t, err)

	err = uninstallStorageCluster(cluster)
	require.NoError(t, err)
	err = validateUninstallStorageCluster(cluster, 15*time.Minute, 30*time.Second)
	require.NoError(t, err)
}
