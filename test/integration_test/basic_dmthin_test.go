//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/cloud_provider"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	pxVer3_0_0, _ = version.NewVersion("3.0.0")
)

type TestOptionsPresent int

const (
	PxStoreV2Present TestOptionsPresent = iota
	MetadataPresent
	AutoJournalPresent
	KvdbDevicePresent
	JournalPresent
	MaxOptions
)

type OptionsArr [MaxOptions]bool

func defaultDmthinSpec(t *testing.T, addDmthinOption bool) *corev1.StorageCluster {
	objects, err := ci_utils.ParseSpecs("storagecluster/storagecluster-with-all-components.yaml")
	require.NoError(t, err)
	cluster, ok := objects[0].(*corev1.StorageCluster)
	require.True(t, ok)
	cluster.Name = "test-stc"
	cluster.Namespace = "kube-system"
	cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled = false
	if addDmthinOption {
		cluster.Annotations = map[string]string{
			"portworx.io/misc-args": "-T PX-StoreV2",
		}
	}
	return cluster
}

func getTestName(opt OptionsArr) string {
	baseName := "BasicInstallDmthinWith"
	testName := baseName
	if opt[PxStoreV2Present] {
		testName += "PxStoreV2"
	}
	if opt[MetadataPresent] {
		testName += "Metadata"
	}
	if opt[KvdbDevicePresent] {
		testName += "KVDB"
	}
	if opt[JournalPresent] {
		testName += "Journal"
	} else if opt[AutoJournalPresent] {
		testName += "AutoJournal"
	}
	if baseName == testName {
		// If none of the options were picked, add "Nothing" in the name for completion
		testName += "Nothing"
	}
	return testName
}

func generateSingleTestCase(o OptionsArr) types.TestCase {
	testCase := types.TestCase{}
	testCase.TestName = getTestName(o)

	testCase.TestSpec = func(t *testing.T) interface{} {
		provider := cloud_provider.GetCloudProvider()
		cluster := defaultDmthinSpec(t, o[PxStoreV2Present])
		cloudSpec := &corev1.CloudStorageSpec{}
		cloudSpec.DeviceSpecs = provider.GetDefaultDataDrives()
		if o[MetadataPresent] {
			cloudSpec.SystemMdDeviceSpec = provider.GetDefaultMetadataDrive()
		}
		if o[JournalPresent] {
			cloudSpec.JournalDeviceSpec = provider.GetDefaultJournalDrive()
		}
		if o[AutoJournalPresent] {
			autoStr := "auto"
			cloudSpec.JournalDeviceSpec = &autoStr
		}
		if o[KvdbDevicePresent] {
			cloudSpec.KvdbDeviceSpec = provider.GetDefaultKvdbDrive()
		}

		cluster.Spec.CloudStorage = cloudSpec
		return cluster
	}
	testCase.TestFunc = BasicInstallDmthin
	return testCase
}

// This function generates a permutation of all possible test cases based on the input array. `idx` argument helps determine the index from which to generate the permutations.
// In addition, this will remove duplicate test cases which might get generated in a permutation.
func generateTestCases(opt OptionsArr, idx uint) map[string]types.TestCase {
	var retVal map[string]types.TestCase

	if idx == uint(len(opt)) {
		singleTestCase := generateSingleTestCase(opt)
		retVal[singleTestCase.TestName] = singleTestCase
		return retVal
	}
	retVal = generateTestCases(opt, idx+1)

	opt[idx] = true
	with := generateTestCases(opt, idx+1)
	for s, testCase := range with {
		if _, ok := retVal[s]; !ok {
			retVal[s] = testCase
		}
	}
	return retVal
}

func TestStorageClusterDmthinWithoutPxStoreV2Option(t *testing.T) {
	opt := OptionsArr{}
	basicInstallTestCases := generateTestCases(opt, 1)
	for _, testCase := range basicInstallTestCases {
		testCase.RunTest(t)
	}
}

func TestStorageClusterDmthinWithPxStoreV2Option(t *testing.T) {
	opt := OptionsArr{}
	opt[PxStoreV2Present] = true
	basicInstallTestCases := generateTestCases(opt, 1)
	for _, testCase := range basicInstallTestCases {
		testCase.RunTest(t)
	}
}

func getTotalNodes(t *testing.T, cluster *corev1.StorageCluster) uint {
	allNodes, err := test.GetExpectedPxNodeList(cluster)
	require.NoError(t, err, "Could not get expected px node list")
	return uint(len(allNodes))
}

func isKvdbPresent(t *testing.T) bool {
	/*
		[root@nrevanna-67-3 /]# pxctl cd list
		Cloud Drives Summary
		        Number of nodes in the cluster:  3
		        Number of storage nodes:  3
		        List of storage nodes:  [29838698-cd54-4162-a411-c5e41e625cf4 780fd7c1-4822-48af-83ab-be75dedfe0cd 7e4f397e-acd9-462b-9cac-f9dcada166b6]
		        List of storage less nodes:  []

		Drive Set List
		        NodeID                                  InstanceID                              Zone    State   Datastore(s)            Drive IDs
		        29838698-cd54-4162-a411-c5e41e625cf4    422d1e08-ef83-dff3-1759-5b92ec32281a    default In Use  js500-1-Dev-9DS2        [datastore-860957] fcd/_00a8/_0012/e62015a3309e4dbcb309b06c252bb1e5.vmdk(kvdb), [datastore-860957] fcd/_00a8/_009a/497896b57223486092a15637f739bf0e.vmdk(data)
		        780fd7c1-4822-48af-83ab-be75dedfe0cd    422dc07a-7af5-4fde-d9f3-5b90c2a727f3    default In Use  js500-1-Dev-9DS2        [datastore-860957] fcd/_00a8/_0026/452fc88fc3d54fdfa60e2618d79344c7.vmdk(data), [datastore-860957] fcd/_00a8/_004a/4411cb48d4d14785ae2273f3debe2c38.vmdk(kvdb)
		        7e4f397e-acd9-462b-9cac-f9dcada166b6    422d28c5-3b01-db78-0975-055c2c13eee3    default In Use  js500-1-Dev-9DS2        [datastore-860957] fcd/_00a8/_0026/18d9e204295242feb20e4c6cb68ea2ec.vmdk(data), [datastore-860957] fcd/_00a8/_0026/53112157081741c482d6a044e0d44353.vmdk(kvdb)
	*/
	out, _, err := ci_utils.RunPxCmdRetry("pxctl cd list")
	require.NoError(t, err)
	splitOut := strings.Split(strings.TrimSpace(out), "Drive Set List")
	require.Greater(t, len(splitOut), 1, "pxctl cd list is not in the expected format")
	return strings.Contains(splitOut[1], "(kvdb)")
}

func isJournalDevicePresent(t *testing.T) bool {
	/*
		[root@nrevanna-67-3 /]# pxctl sv pool show
		PX drive configuration:
		Pool ID: 0
		        Type:  Default
		        UUID:  fd892369-ff42-486d-b8f1-295078b1186b
		        IO Priority:  HIGH
		        Labels:  beta.kubernetes.io/arch=amd64,kubernetes.io/arch=amd64,medium=STORAGE_MEDIUM_MAGNETIC,topology.portworx.io/hypervisor=HostSystem-host-296554,kubernetes.io/os=linux,topology.portworx.io/datacenter=CNBU,beta.kubernetes.io/os=linux,iopriority=HIGH,topology.portworx.io/node=422d1e08-ef83-dff3-1759-5b92ec32281a,kubernetes.io/hostname=nrevanna-67-3
		        Size: 46 GiB
		        Status: Online
		        Has metadata:  Yes
		        Balanced:  Yes
		        Drives:
		        1: /dev/sdu2, Total size 46 GiB, Online
		        Cache Drives:
		        No Cache drives found in this pool
		Journal Device:
		        1: /dev/sdu1, STORAGE_MEDIUM_MAGNETIC
	*/
	out, _, err := ci_utils.RunPxCmdRetry("pxctl sv pool show")
	require.NoError(t, err)
	return strings.Contains(out, "Journal Device:")
}

func isPxStoreV2(t *testing.T, cluster *corev1.StorageCluster) bool {
	totalNodes := getTotalNodes(t, cluster)

	// Verify we see PXStoreV2 in pxctl status output.
	out, _, err := ci_utils.RunPxCmdRetry("/usr/local/bin/pxctl status")
	require.NoError(t, err, "Unexpected error when retrieving pxctl status")
	val := ci_utils.AnalyzePxctlStatus(t, out).PxStoreV2NodeCount
	pxStoreV2 := val > 0
	if pxStoreV2 {
		require.Equal(t, val, totalNodes, "Mismatch in number of nodes having PX-StoreV2")
	}

	// Verify we see PXStoreV2 in pxctl sv pool show. This further verifies that pools were created using cloud drives
	out, _, err = ci_utils.RunPxCmdRetry("/usr/local/bin/pxctl sv pool show")
	require.NoError(t, err)
	out = strings.TrimSpace(out)
	val = uint(strings.Count(strings.ToLower(out), "px-storev2"))
	if pxStoreV2 {
		require.Greaterf(t, val, uint(0), "pxctl status doesn't have PX-StoreV2")
	} else {
		require.Equal(t, val, uint(0), "pxctl status doesn't have PX-StoreV2")
	}
	return pxStoreV2
}

func installAndValidate(t *testing.T, tc *types.TestCase, specGenURL string, specImages map[string]string) *corev1.StorageCluster {
	testSpec := tc.TestSpec(t)
	cluster, ok := testSpec.(*corev1.StorageCluster)
	require.True(t, ok)

	// Check if we need to create custom namespace
	if cluster.Namespace != ci_utils.PxNamespace {
		logrus.Debugf("Attempting to create custom namespace %s", cluster.Namespace)
		ns := &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}

		err := ci_utils.CreateObjects([]runtime.Object{ns})
		if err != nil {
			if errors.IsAlreadyExists(err) {
				logrus.Warnf("Namespace %s already exists", cluster.Namespace)
			} else {
				require.NoError(t, err)
			}
		}
	}

	// Construct StorageCluster
	err := ci_utils.ConstructStorageCluster(cluster, specGenURL, specImages)
	require.NoError(t, err)

	// Deploy PX and validate
	cluster = ci_utils.DeployAndValidateStorageCluster(cluster, specImages, t)
	require.NoError(t, err, "Deploy and Validate error")
	return cluster
}

func UninstallPX(t *testing.T, cluster *corev1.StorageCluster) {
	// Wipe PX and validate
	ci_utils.UninstallAndValidateStorageCluster(cluster, t)

	// Delete namespace if custom
	if cluster.Namespace != ci_utils.PxNamespace {
		ns := &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}
		err := ci_utils.DeleteObjects([]runtime.Object{ns})
		require.NoError(t, err)
		err = ci_utils.ValidateObjectsAreTerminated([]runtime.Object{ns}, false)
		require.NoError(t, err)
	}
}

func BasicInstallDmthin(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		cluster := installAndValidate(t, tc, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
		pxStoreV2 := isPxStoreV2(t, cluster)
		require.True(t, pxStoreV2, "Expecting pxStoreV2 to be installed")
		isJournalPresent := isJournalDevicePresent(t)
		require.False(t, isJournalPresent, "Journal device found in a dmthin setup")
		isKvdbDiskPresent := isKvdbPresent(t)
		require.False(t, isKvdbDiskPresent, "KVDB device found in a dmthin setup")
		UninstallPX(t, cluster)
	}
}
