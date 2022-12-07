// +build integrationtest

package integrationtest

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
)

var marketplaceOperatorTestCases = []types.TestCase{
	{
		TestName:        "BasicInstallViaOcpMarketplace",
		TestrailCaseIDs: []string{""},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		TestFunc: BasicInstallViaOcpMarketplace,
	},
}

func TestMarketplaceOperator(t *testing.T) {
	for _, tc := range marketplaceOperatorTestCases {
		tc.ShouldSkip = shouldSkipMarketplaceOperatorTests
		tc.RunTest(t)
	}
}

func shouldSkipMarketplaceOperatorTests(tc *types.TestCase) bool {
	return !ci_utils.IsOcp
}

func BasicInstallViaOcpMarketplace(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		// Deploy PX Operator via OCP Marketplace
		err := ci_utils.DeployAndValidatePxOperatorViaMarketplace()
		require.NoError(t, err)

		// Get the storage cluster to start with
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Create and validate StorageCluster
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Delete and validate StorageCluster deletion
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)

		// Delete and validate PX Operator via OCP Marketplace deletion
		err = ci_utils.DeleteAndValidatePxOperatorViaMarketplace()
		require.NoError(t, err)
	}
}
