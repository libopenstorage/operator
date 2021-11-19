// +build fafb

package integrationtest

import (
	"fmt"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	coreops "github.com/portworx/sched-ops/k8s/core"
)

// node* is to be used in the Node section of the StorageCluster spec. node0 will select the
// alphabetically 1st PX node, node1 will select the 2nd, and so on
const (
	node0 = ci_utils.NodeReplacePrefix + "0"
	node1 = ci_utils.NodeReplacePrefix + "1"
	node2 = ci_utils.NodeReplacePrefix + "2"
)

var (
	auto    = "auto"    // Used for things like auto-journal drives
	size3   = "size=3"  // Recommended journal drive size, more than this is useless: https://docs.portworx.com/install-with-other/operate-and-maintain/performance-and-tuning/tuning/
	size32  = "size=32" // Default kvdb size from spec gen
	size50  = "size=50"
	size100 = "size=100"
	size200 = "size=200"

	disks100 = []string{size100}
	disks200 = []string{size200}

	threeStorageNodes = uint32(3)

	internalKVDB     = corev1.KvdbSpec{Internal: true}
	autoJournalDrive = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			KvdbDeviceSpec:    &size32,
			JournalDeviceSpec: &auto,
			DeviceSpecs:       &disks100,
		},
	}

	// simpleClusterSpec is a StorageCluster with internal KVDB and auto journal drives
	simpleClusterSpec = corev1.StorageClusterSpec{
		Kvdb:         &internalKVDB,
		CloudStorage: autoJournalDrive,
	}
)

// TODO: add readable test names
var flashArrayPositiveInstallTests = []types.TestCase{
	//// C55130: https://portworx.testrail.net/index.php?/cases/view/55130
	// Install PX through operator with internal KVDB and journal drive on FlashArray
	{
		TestName:        "C55130",
		TestrailCaseIDs: []string{"C55130"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			Spec: corev1.StorageClusterSpec{
				Kvdb: &internalKVDB,
				CloudStorage: &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						KvdbDeviceSpec:    &size32,
						JournalDeviceSpec: &size3,
						DeviceSpecs:       &disks100,
					},
				},
			},
		}),
		ShouldStartSuccessfully: true,
		PureBackendRequirements: &types.PureBackendRequirements{
			RequiredArrays: 1,
		},
		TestFunc: BasicInstallWithFAFB,
	},
	// C55129: https://portworx.testrail.net/index.php?/cases/view/55129
	// Install PX through operator with internal KVDB on FlashArray
	//    and
	// C56414: https://portworx.testrail.net/index.php?/cases/view/56414
	// Install PX through operator with internal KVDB and auto journal drive on FlashArray
	//    and
	// C56411: https://portworx.testrail.net/index.php?/cases/view/56411
	// Install PX through operator with one valid FlashArray
	//    and
	// C55003: https://portworx.testrail.net/index.php?/cases/view/55003
	// Uninstall PX through operator with Pure arrays, after clean install
	{

		TestName:                "C55129-C56414-C56411-C55003",
		TestrailCaseIDs:         []string{"C55129", "C56414", "C56411", "C55003"},
		TestSpec:                ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{}),
		ShouldStartSuccessfully: true,
		PureBackendRequirements: &types.PureBackendRequirements{
			RequiredArrays: 1,
		},
		TestFunc: BasicInstallWithFAFB,
	},
	// C55133: https://portworx.testrail.net/index.php?/cases/view/55133
	// Install PX through operator with heterogeneous cluster, with different nodes having different capacity drives
	{
		TestName:        "C55133",
		TestrailCaseIDs: []string{"C55133"},
		TestSpec: ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
			Spec: corev1.StorageClusterSpec{
				Kvdb:         &internalKVDB,
				CloudStorage: autoJournalDrive,
				Nodes: []corev1.NodeSpec{
					{
						Selector: corev1.NodeSelector{
							NodeName: node0,
						},
						CloudStorage: &corev1.CloudStorageNodeSpec{
							CloudStorageCommon: corev1.CloudStorageCommon{
								DeviceSpecs:       &disks200,
								JournalDeviceSpec: &auto,
								KvdbDeviceSpec:    &size32,
							},
						},
					},
				},
			},
		}),
		ShouldStartSuccessfully: true,
		PureBackendRequirements: &types.PureBackendRequirements{
			RequiredArrays: 1,
		},
		TestFunc: BasicInstallWithFAFB,
	},
	// C55134: https://portworx.testrail.net/index.php?/cases/view/55134
	// Install PX through operator on FlashArray with storage and storageless nodes
	// TODO: Needs to remove prereqs from storageless node before running test
	//{
	//	TestCase: TestCase{
	//		TestrailCaseIDs: []string{
	//			"C55134",
	//		},
	//		Spec: corev1.StorageClusterSpec{
	//			Kvdb: &internalKVDB,
	//			CloudStorage: &corev1.CloudStorageSpec{
	//				CloudStorageCommon: corev1.CloudStorageCommon{
	//					DeviceSpecs: &disks100,
	//					KvdbDeviceSpec:    &size32,
	//					JournalDeviceSpec: &auto,
	//				},
	//			},
	//			Nodes: []corev1.NodeSpec{
	//				{
	//					Selector: corev1.NodeSelector{
	//						NodeName: node0,
	//					},
	//					CloudStorage: nil,
	//				},
	//			},
	//		},
	//		ShouldStartSuccessfully: true,
	//	},
	//	PureBackendRequirements: PureBackendRequirements{
	//		RequiredArrays: 1,
	//	},
	//},
	//C56412: https://portworx.testrail.net/index.php?/cases/view/56412
	//Install PX through operator with multiple FlashArrays (all valid)
	{
		TestName:                "C56412",
		TestrailCaseIDs:         []string{"C56412"},
		TestSpec:                ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{}),
		ShouldStartSuccessfully: true,
		PureBackendRequirements: &types.PureBackendRequirements{
			RequiredArrays: 2,
			InvalidArrays:  0,
		},
		TestFunc: BasicInstallWithFAFB,
	},
	//C56413: https://portworx.testrail.net/index.php?/cases/view/56413
	//Install PX through operator with both valid and invalid FlashArrays
	{
		TestName:                "C56413",
		TestrailCaseIDs:         []string{"C56413"},
		TestSpec:                ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{}),
		ShouldStartSuccessfully: true,
		PureBackendRequirements: &types.PureBackendRequirements{
			RequiredArrays: 2,
			InvalidArrays:  1,
		},
		TestFunc: BasicInstallWithFAFB,
	},
}

/*var flashArrayNegativeInstallTests = []TestCase{ // TODO: need to be sure we have the right way to detect failure to start
	// C54982: https://portworx.testrail.net/index.php?/cases/view/54982
	// Install PX with invalid credentials
	// C55004: https://portworx.testrail.net/index.php?/cases/view/55004
	// Uninstall PX through operators with Pure arrays, after failed install
	{
		ID:                      "C54982-C55004",
		ShouldStartSuccessfully: false,
		// TODO: needs invalid credentials
		Spec:     simpleClusterSpec,
	},
	// C54981: https://portworx.testrail.net/index.php?/cases/view/54981
	// Install PX with only FlashBlades w/o iSCSI packages installed.
	{
		ID:                      "C54981",
		ShouldStartSuccessfully: false, // assuming running with no other cloud provider creds
		// TODO: needs some way to uninstall and reinstall iSCSI
		// TODO: needs only FlashBlades
		Spec:     simpleClusterSpec,
	},
	// C54983: https://portworx.testrail.net/index.php?/cases/view/54983
	// Install PX on node with missing required utilities
	{
		ID:                      "C54983",
		ShouldStartSuccessfully: false,
		// TODO: needs some way to uninstall utilities or files and add them back afterwards
		Spec:     simpleClusterSpec,
	},
}*/

func TestFAFBPositiveInstallation(t *testing.T) {
	ci_utils.LoadSourceConfigOrFail(t, ci_utils.PxNamespace)
	for _, testCase := range flashArrayPositiveInstallTests {
		if testCase.ShouldSkip == nil {
			testCase.ShouldSkip = ShouldSkipFAFBTest
		}
		testCase.RunTest(t)
	}
}

func BasicInstallWithFAFB(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		// Check that we have enough backends to support this test: skip early
		require.NotEmpty(t, tc.PureBackendRequirements)
		fleetBackends := ci_utils.GenerateFleet(t, ci_utils.PxNamespace, tc.PureBackendRequirements)

		// Construct Portworx StorageCluster object
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		err := ci_utils.PopulateStorageCluster(tc, cluster)
		require.NoError(t, err)

		// Add extra env variables
		releaseManifestURL, err := testutil.ConstructPxReleaseManifestURL(ci_utils.PxSpecGenURL)
		require.NoError(t, err)

		cluster.Spec.Env = append(cluster.Spec.Env, []k8sv1.EnvVar{
			{
				Name:  "PX_RELEASE_MANIFEST_URL",
				Value: releaseManifestURL,
			},
			{
				Name:  "PX_LOGLEVEL",
				Value: "debug",
			},
		}...)

		// Create px-pure-secret
		logrus.Infof("Create or update %s in %s", ci_utils.OutputSecretName, ci_utils.PxNamespace)
		err = createPureSecret(fleetBackends, ci_utils.PxNamespace)
		require.NoError(t, err)

		// Install, validation and deletion
		BasicInstall(tc)(t)

		// Delete px-pure-secret
		logrus.Infof("Delete Secret %s", ci_utils.OutputSecretName)
		err = deletePureSecretIfExists(cluster.Namespace)
		require.NoError(t, err)

		// TODO: mark test completion using testrail IDs
	}
}

func createPureSecret(config types.DiscoveryConfig, namespace string) error {
	pureJSON, err := config.DumpJSON()
	if err != nil {
		return err
	}

	if err = deletePureSecretIfExists(namespace); err != nil {
		return err
	}

	// Wait a little bit
	time.Sleep(time.Second * 5)

	_, err = coreops.Instance().CreateSecret(&k8sv1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ci_utils.OutputSecretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"pure.json": pureJSON,
		},
	})
	return err
}

func deletePureSecretIfExists(namespace string) error {
	if err := coreops.Instance().DeleteSecret(ci_utils.OutputSecretName, namespace); !errors.IsNotFound(err) && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// ShouldSkipFAFBTest If not enough devices exist to meet the requirements or the source secret does not
// exist, the test will be skipped.
func ShouldSkipFAFBTest(tc *types.TestCase) bool {
	if !ci_utils.SourceConfigPresent {
		fmt.Println("Source config not present, skipping")
		return true
	}

	req := tc.PureBackendRequirements
	// Check that we have enough devices to meet the requirements
	if req.RequiredArrays > 0 && len(ci_utils.FAFBSourceConfig.Arrays) < req.RequiredArrays {
		fmt.Printf("Test requires %d FlashArrays but only %d provided, skipping\n", req.RequiredArrays, len(ci_utils.FAFBSourceConfig.Arrays))
		return true
	}
	if req.RequiredBlades > 0 && len(ci_utils.FAFBSourceConfig.Blades) < req.RequiredBlades {
		fmt.Printf("Test requires %d FlashBlades but only %d provided, skipping\n", req.RequiredBlades, len(ci_utils.FAFBSourceConfig.Blades))
		return true
	}
	return false
}
