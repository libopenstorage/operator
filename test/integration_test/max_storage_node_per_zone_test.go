//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	"github.com/stretchr/testify/require"
	"testing"
)

var BaseTestSpec = func(t *testing.T) interface{} {
	objects, err := ci_utils.ParseSpecs("storagecluster/storagecluster-with-all-components.yaml")
	require.NoError(t, err)
	cluster, ok := objects[0].(*corev1.StorageCluster)
	require.True(t, ok)
	return cluste
}
var testMaxStorageNodePerZoneCases = []types.TestCase{
	{
		TestName:        "3 nodes 3 zones should set maxStorageNodePerZone to 1",
		TestrailCaseIDs: []string{"T17933595"},
		TestSpec:        BaseTestSpec,
		TestFunc:        InstallAndValidateMaxStorageNodePerZone,
		ResourceSpec:    {3, 3, 1},
	},
}

func TestStorageClusterBasic(t *testing.T) {
	for _, testCase := range testMaxStorageNodePerZoneCases {
		testCase.RunTest(t)
	}
}

func InstallAndValidateMaxStorageNodePerZone(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		resourceSpec := tc.ResourceSpec
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Deploy PX and validate
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		require.Equal(t, resourceSpec.MaxStorageNodePerZone, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	}
}
