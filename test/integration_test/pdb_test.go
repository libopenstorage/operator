package integrationtest

import (
	"fmt"
	"math"
	"testing"

	"github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	"github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	minSupportedK8sVersionForPdb, _ = version.NewVersion("1.21.0")
)
var testStorageClusterPDBCases = []types.TestCase{
	{
		TestName:        "PDBWithStoragelessNode",
		TestrailCaseIDs: []string{"C257095"},
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
				logrus.Info("Skipping PDB test due to Err: ", err)
				return true
			}
			k8sVersion, _ := version.NewVersion(kbVer)
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_5_0) || k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: StoragelessNodePDB,
	},
	{
		TestName:        "OverridePDBUsingValidAnnotation",
		TestrailCaseIDs: []string{"C257148"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		ShouldSkip: func(tc *types.TestCase) bool {
			kbVer, err := testutil.GetK8SVersion()
			if err != nil {
				logrus.Info("Skipping PDB test due to Err: ", err)
				return true
			}
			k8sVersion, _ := version.NewVersion(kbVer)
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer23_10_2) || k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: OverridePDBUsingValidAnnotation,
	},

	{
		TestName:        "OverridePDBUsingInvalidAnnotation",
		TestrailCaseIDs: []string{"C257146", "C257203"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test-stc"},
		}),
		ShouldSkip: func(tc *types.TestCase) bool {
			kbVer, err := testutil.GetK8SVersion()
			if err != nil {
				logrus.Info("Skipping PDB test due to Err: ", err)
				return true
			}
			k8sVersion, _ := version.NewVersion(kbVer)
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer24_1_0) || k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: OverridePDBUsingInvalidAnnotation,
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

func OverridePDBUsingValidAnnotation(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Do not count control plane nodes
		k8snodecount, err := ci_utils.GetNonMasterK8sNodeCount()
		require.NoError(t, err)

		// Override PDB value with (number of k8s nodes -2) to check if allowed disruptions is 2
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		logrus.Infof("Validating PDB using minAvailable value: %d", k8snodecount-2)
		cluster.Annotations["portworx.io/storage-pdb-min-available"] = fmt.Sprintf("%d", k8snodecount-2)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func OverridePDBUsingInvalidAnnotation(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Do not count control plane nodes
		k8snodecount, err := ci_utils.GetNonMasterK8sNodeCount()
		require.NoError(t, err)

		// Override PDB with value less than px quorum and ensure minAvailable value uses default calculation
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		quorumValue := math.Floor(float64(k8snodecount)/2) + 1
		logrus.Infof("Validating PDB using minAvailable value: %d", int(quorumValue)-1)
		cluster.Annotations["portworx.io/storage-pdb-min-available"] = fmt.Sprintf("%d", int(quorumValue)-1)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

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
