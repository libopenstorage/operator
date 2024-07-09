package integrationtest

import (
	"fmt"
	//"math"
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
	pxVer3_1_2, _ = version.NewVersion("3.1.2")
)

var testNodePDBCases = []types.TestCase{
	{
		TestName:        "CreateNodePDBBasic",
		TestrailCaseIDs: []string{"C299571", "C299572"},
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
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer24_2_0) || k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: CreateNodePDBBasic,
	},
	{
		TestName:        "CreateNodePDBWithStoragelessNode",
		TestrailCaseIDs: []string{"C299573"},
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
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer24_2_0) || k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: CreateNodePDBWithStoragelessNode,
	},
	{
		TestName:        "MaxNodesAvailableForUpgrade",
		TestrailCaseIDs: []string{"C299574", "C299575"},
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
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer24_2_0) || k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: MaxNodesAvailableForUpgrade,
	},
	{
		TestName:        "NodePDBDisablingParallelUpgrade",
		TestrailCaseIDs: []string{"C299576", "C299577"},
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
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer24_2_0) || k8sVersion.LessThan(minSupportedK8sVersionForPdb)
		},
		TestFunc: NodePDBDisablingParallelUpgrade,
	},
}

func CreateNodePDBBasic(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		pxVersion := testutil.GetPortworxVersion(cluster)

		if pxVersion.GreaterThanOrEqual(pxVer3_1_2) {
			logrus.Infof("Validating Node PDB names and default minAvailable")
			err := testutil.ValidateNodePDB(cluster, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval)
			require.NoError(t, err)
		}
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}

}
func CreateNodePDBWithStoragelessNode(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		*cluster.Spec.CloudStorage.MaxStorageNodesPerZone = uint32(3)
		logrus.Info("Validating PDB with storageless nodes using maxstoragenodesperzone value: ", *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)

	}
}

func MaxNodesAvailableForUpgrade(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		pxVersion := testutil.GetPortworxVersion(cluster)

		if pxVersion.GreaterThanOrEqual(pxVer3_1_2) {
			err := ci_utils.CordonNodes()
			require.NoError(t, err)

			logrus.Infof("Validating number of nodes ready for upgrade without minAvailable annotation")
			err = testutil.ValidateNodesSelectedForUpgrade(cluster, -1, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval)
			require.NoError(t, err)

			k8snodecount, err := ci_utils.GetNonMasterK8sNodeCount()
			require.NoError(t, err)
			cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
			require.NoError(t, err)
			cluster.Annotations["portworx.io/storage-pdb-min-available"] = fmt.Sprintf("%d", k8snodecount-1)
			cluster, err = ci_utils.UpdateStorageCluster(cluster)
			require.NoError(t, err)

			logrus.Infof("Validating number of nodes ready for upgrade with minAvailable annotation %d", k8snodecount-1)
			err = testutil.ValidateNodesSelectedForUpgrade(cluster, k8snodecount-1, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval)
			require.NoError(t, err)

			err = ci_utils.UncordonNodes()
			require.NoError(t, err)
		}
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func NodePDBDisablingParallelUpgrade(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/disable-non-disruptive-upgrade"] = "true"
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		pxVersion := testutil.GetPortworxVersion(cluster)
		if pxVersion.GreaterThanOrEqual(pxVer3_1_2) {
			err := ci_utils.CordonNodes()
			require.NoError(t, err)
			k8snodecount, err := ci_utils.GetNonMasterK8sNodeCount()
			require.NoError(t, err)
			logrus.Infof("Validating number of nodes ready for upgrade without minAvailable annotation after disabling non-disruptive upgrade")
			err = testutil.ValidateNodesSelectedForUpgrade(cluster, k8snodecount-1, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval)
			require.NoError(t, err)

			cluster, err = operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
			require.NoError(t, err)
			cluster.Annotations["portworx.io/storage-pdb-min-available"] = fmt.Sprintf("%d", k8snodecount-2)
			cluster, err = ci_utils.UpdateStorageCluster(cluster)
			require.NoError(t, err)
			logrus.Infof("Validating number of nodes ready for upgrade with minAvailable annotation %d after disabling non-disruptive upgrade", k8snodecount-2)
			err = testutil.ValidateNodesSelectedForUpgrade(cluster, k8snodecount-2, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval)
			require.NoError(t, err)

			err = ci_utils.UncordonNodes()
			require.NoError(t, err)
		}
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)

	}
}
func TestNodePDB(t *testing.T) {
	for _, tc := range testNodePDBCases {
		tc.RunTest(t)
	}
}
