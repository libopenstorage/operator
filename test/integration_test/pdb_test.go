package integrationtest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	"github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"

	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	minSupportedK8sVersionForPdb, _ = version.NewVersion("1.21.0")
)
var testStorageClusterPDBCases = []types.TestCase{
	{
		TestName:        "PDBWithStoragelessNode",
		TestrailCaseIDs: []string{"C58606"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		ShouldSkip: func(tc *types.TestCase) bool {
			if len(ci_utils.PxDeviceSpecs) == 0 {
				logrus.Info("--portworx-device-specs is empty, cannot run PDBWithStoragelessNode test")
				return true
			}
			kbVer, err := testutil.GetK8SVersion()
			if err != nil {
				logrus.Info("Skipping PDB test due to :", err)
				return true
			}
			k8sVersion, _ := version.NewVersion(kbVer)
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_5_0) && k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: StoragelessNodePDB,
	},
	{
		TestName:        "OverridePDBUsingAnnotation",
		TestrailCaseIDs: []string{"C58607"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		ShouldSkip: func(tc *types.TestCase) bool {
			kbVer, err := testutil.GetK8SVersion()
			if err != nil {
				logrus.Info("Skipping PDB test due to :", err)
				return true
			}
			k8sVersion, _ := version.NewVersion(kbVer)
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_5_0) && k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: OverridePDBUsingAnnotation,
	},
}

func StoragelessNodePDB(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Assuming there are more than 3 nodes in a zone
		*cluster.Spec.CloudStorage.MaxStorageNodesPerZone = uint32(3)
		logrus.Info("Validating PDB with storageless nodes using maxstoragenodesperzone value: ", *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)

	}
}

func OverridePDBUsingAnnotation(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)
		//Add annotations to override the default PDB
		k8snodecount, err := ci_utils.GetK8sNodeCount()
		require.NoError(t, err)

		// Override PDB value with (number of k8s nodes -2) to check if allowed disruptions is 2
		cluster.Annotations = make(map[string]string)
		logrus.Infof("Validating PDB using minAvailable value: %d", k8snodecount-2)
		cluster.Annotations["portworx.io/storage-pdb-min-available"] = fmt.Sprintf("%d", k8snodecount-2)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Override PDB with value 1 and ensure minAvailable value does not get changed
		logrus.Infof("Validating PDB using minAvailable value: 1")
		cluster.Annotations["portworx.io/storage-pdb-min-available"] = "1"
		cluster, err = ci_utils.UpdateStorageCluster(cluster)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval, true, "")
		require.NoError(t, err)

		// Override PDB with value equal to storage nodes and ensure minAvailable value does not get changed
		logrus.Infof("Validating PDB using minAvailable value: %d", k8snodecount)
		cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		require.NoError(t, err)
		cluster.Annotations["portworx.io/storage-pdb-min-available"] = fmt.Sprintf("%d", k8snodecount)
		cluster, err = ci_utils.UpdateStorageCluster(cluster)
		require.NoError(t, err)
		err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval, true, "")
		require.NoError(t, err)

		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func TestStorageClusterPDB(t *testing.T) {
	for _, tc := range testStorageClusterPDBCases {
		tc.RunTest(t)
	}
}
