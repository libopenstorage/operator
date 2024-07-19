package integrationtest

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"

	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/portworx/sched-ops/k8s/operator"
	policyops "github.com/portworx/sched-ops/k8s/policy"
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
	{
		TestName:        "NodesSelectedForUpgradeWithReplicas",
		TestrailCaseIDs: []string{"C299578", "C299579"},
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
		TestFunc: NodesSelectedForUpgradeWithReplicas,
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
		pxVersion := testutil.GetPortworxVersion(cluster)
		if pxVersion.GreaterThanOrEqual(pxVer3_1_2) {
			logrus.Infof("Validating Node PDB names and default minAvailable")
			err := testutil.ValidateNodePDB(cluster, ci_utils.DefaultValidateUpgradeTimeout, ci_utils.DefaultValidateUpgradeRetryInterval)
			require.NoError(t, err)
		}
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

func NodesSelectedForUpgradeWithReplicas(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, _ := testSpec.(*corev1.StorageCluster)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
		pxVersion := testutil.GetPortworxVersion(cluster)

		if pxVersion.GreaterThanOrEqual(pxVer3_1_2) {

			// Get px pods
			pods, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
			require.NoError(t, err)
			require.NotEmpty(t, pods.Items)

			//Create a volume of replica 2
			replicaNodes := ""
			var stdout, stderr bytes.Buffer

			storageNode, err := operator.Instance().GetStorageNode(pods.Items[0].Spec.NodeName, pods.Items[0].Namespace)
			require.NoError(t, err)
			replicaNodes = storageNode.Status.NodeUID
			storageNode, err = operator.Instance().GetStorageNode(pods.Items[1].Spec.NodeName, pods.Items[1].Namespace)
			require.NoError(t, err)
			replicaNodes = replicaNodes + "," + storageNode.Status.NodeUID

			tmpVolName := "testVol"
			logrus.Infof("Attempt volume creation on nodes %s", replicaNodes)
			err = ci_utils.RunInPortworxPod(&pods.Items[2], nil, &stdout, &stderr,
				"/opt/pwx/bin/pxctl", "volume", "create", "--repl", "2", "--nodes", replicaNodes, tmpVolName)
			require.Contains(t, stdout.String(), "Volume successfully created")
			require.NoError(t, err)

			// Cordon the nodes with volume replica
			for i := 0; i < 2; i++ {
				currNode, err := coreops.Instance().GetNodeByName(pods.Items[i].Spec.NodeName)
				require.NoError(t, err)
				currNode.Spec.Unschedulable = true
				_, err = coreops.Instance().UpdateNode(currNode)
				require.NoError(t, err)
			}

			// sleep for 30seconds to allow the PDB to get updated
			time.Sleep(30 * time.Second)

			// Validate the nodes selected for upgrade
			logrus.Infof("Validating only 1 node with volume replica is ready for upgrade")
			pdbs, err := policyops.Instance().ListPodDisruptionBudget(cluster.Namespace)
			require.NoError(t, err)
			isVolumeReplicaSelected := false
			for _, pdb := range pdbs.Items {
				if (strings.HasSuffix(pdb.Name, pods.Items[0].Spec.NodeName) || strings.HasSuffix(pdb.Name, pods.Items[1].Spec.NodeName)) && pdb.Spec.MinAvailable.IntValue() == 0 {
					require.False(t, isVolumeReplicaSelected)
					isVolumeReplicaSelected = true
				}
			}

			// Uncordon nodes
			logrus.Infof("Uncordoning nodes %s", replicaNodes)
			err = ci_utils.UncordonNodes()
			require.NoError(t, err)

			// Case2: When 1 px on node with volume replica is down
			logrus.Infof("Stopping PX on node %s", pods.Items[0].Spec.NodeName)
			err = coreops.Instance().AddLabelOnNode(pods.Items[0].Spec.NodeName, "px/service", "stop")
			require.NoError(t, err, "could not label node %s", pods.Items[0].Spec.NodeName)
			sleep4 := 30 * time.Second
			logrus.Infof("Sleeping for %s to allow portworx.service @%s to stop", sleep4, pods.Items[0].Spec.NodeName)
			time.Sleep(sleep4)
			// Cordon the nodes with volume replica
			for i := 0; i < 2; i++ {
				currNode, err := coreops.Instance().GetNodeByName(pods.Items[i].Spec.NodeName)
				require.NoError(t, err)
				currNode.Spec.Unschedulable = true
				_, err = coreops.Instance().UpdateNode(currNode)
				require.NoError(t, err)
			}
			// sleep for 30seconds to allow the PDB to get updated
			time.Sleep(30 * time.Second)
			// Neither of the nodes should be selected for upgrade
			logrus.Infof("Validating no cordoned nodes are ready for upgrade with 1 volume replica down")
			pdbs, err = policyops.Instance().ListPodDisruptionBudget(cluster.Namespace)
			require.NoError(t, err)
			for _, pdb := range pdbs.Items {
				if strings.HasPrefix(pdb.Name, "px-") && pdb.Name != "px-kvdb" {
					require.Equal(t, 1, pdb.Spec.MinAvailable.IntValue())
				}
			}

			// Bring the node back up
			logrus.Infof("Bringing portworx up on node %s", pods.Items[0].Spec.NodeName)
			err = coreops.Instance().RemoveLabelOnNode(pods.Items[0].Spec.NodeName, "px/service")
			require.NoError(t, err)
			err = coreops.Instance().AddLabelOnNode(pods.Items[0].Spec.NodeName, "px/service", "start")
			require.NoError(t, err, "could not label node %s", pods.Items[0].Spec.NodeName)
			time.Sleep(sleep4)

			// Uncordon nodes
			err = ci_utils.UncordonNodes()
			require.NoError(t, err)

			// delete the volumes created
			logrus.Infof("Cleaning up volumes on %s", replicaNodes)
			err = ci_utils.RunInPortworxPod(&pods.Items[2], nil, &stdout, &stderr, "/bin/sh", "-c", "/opt/pwx/bin/pxctl v delete --force "+tmpVolName)
			require.NoError(t, err)
			require.Contains(t, stdout.String(), "Volume "+tmpVolName+" successfully deleted")

		}
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func TestNodePDB(t *testing.T) {
	for _, tc := range testNodePDBCases {
		tc.RunTest(t)
	}
}
