// +build fafb

package integrationtest

import (
	"fmt"
	"k8s.io/apimachinery/pkg/api/errors"
	"sort"
	"strconv"
	"strings"
	"testing"

	k8sv1 "k8s.io/api/core/v1"

	v1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// node* is to be used in the Node section of the StorageCluster spec. node0 will select the
// alphabetically 1st PX node, node1 will select the 2nd, and so on
const (
	nodeReplacePrefix = "replaceWithNodeNumber"

	node0 = nodeReplacePrefix + "0"
	node1 = nodeReplacePrefix + "1"
	node2 = nodeReplacePrefix + "2"
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

	internalKVDB     = v1.KvdbSpec{Internal: true}
	autoJournalDrive = &v1.CloudStorageSpec{
		CloudStorageCommon: v1.CloudStorageCommon{
			KvdbDeviceSpec:    &size32,
			JournalDeviceSpec: &auto, // TODO: Once PWX-21364 is fixed, add this back in!
			DeviceSpecs:       &disks100,
		},
	}

	// simpleClusterSpec is a StorageCluster with internal KVDB and auto journal drives
	simpleClusterSpec = v1.StorageClusterSpec{
		Kvdb:         &internalKVDB,
		CloudStorage: autoJournalDrive,
	}
)

type TestrailCase struct {
	ID                      string
	Spec                    v1.StorageClusterSpec
	BackendRequirements     BackendRequirements
	ShouldStartSuccessfully bool
}

func (trc *TestrailCase) PopulateStorageCluster(cluster *v1.StorageCluster) error {
	cluster.Name = makeDNS1123Compatible(trc.ID)
	cluster.Spec.CloudStorage = trc.Spec.CloudStorage
	cluster.Spec.Kvdb = trc.Spec.Kvdb

	names, err := testutil.GetExpectedPxNodeNameList(cluster)
	if err != nil {
		return err
	}
	// Sort for consistent order between multiple tests
	sort.Strings(names)

	// For each node, if the selector looks like "replaceWithNodeNumberN", replace it with
	// the name of the Nth eligible Portworx node
	for i := range trc.Spec.Nodes {
		if !strings.HasPrefix(trc.Spec.Nodes[i].Selector.NodeName, nodeReplacePrefix) {
			continue
		}

		num := strings.TrimPrefix(trc.Spec.Nodes[i].Selector.NodeName, nodeReplacePrefix)
		parsedNum, err := strconv.Atoi(num)
		if err != nil {
			return err
		}

		if parsedNum >= len(names) {
			return fmt.Errorf("requested node index %d is larger than eligible worker node count %d", parsedNum, len(names))
		}

		trc.Spec.Nodes[i].Selector.NodeName = names[parsedNum]
	}

	cluster.Spec.Nodes = trc.Spec.Nodes

	return nil
}

var flashArrayPositiveInstallTests = []TestrailCase{
	// C55129: https://portworx.testrail.net/index.php?/cases/view/55129
	// Install PX through operator with internal KVDB on FlashArray
	//    and
	// C56411: https://portworx.testrail.net/index.php?/cases/view/56411
	// Install PX through operator with one valid FlashArray
	{
		ID:                      "C55129-C56411",
		ShouldStartSuccessfully: true,
		BackendRequirements: BackendRequirements{
			RequiredArrays: 1,
		},
		Spec: v1.StorageClusterSpec{
			Kvdb: &internalKVDB,
			CloudStorage: &v1.CloudStorageSpec{
				CloudStorageCommon: v1.CloudStorageCommon{
					KvdbDeviceSpec: &size32,
					DeviceSpecs:    &disks100,
				},
			},
		},
	},
	//// C55130: https://portworx.testrail.net/index.php?/cases/view/55130
	// Install PX through operator with internal KVDB and journal drive on FlashArray
	// TODO: Blocked due to PWX-21364, retest
	{
		ID:                      "C55130",
		ShouldStartSuccessfully: true,
		BackendRequirements: BackendRequirements{
			RequiredArrays: 1,
		},
		Spec: v1.StorageClusterSpec{
			Kvdb: &internalKVDB,
			CloudStorage: &v1.CloudStorageSpec{
				CloudStorageCommon: v1.CloudStorageCommon{
					KvdbDeviceSpec:    &size32,
					JournalDeviceSpec: &size3,
					DeviceSpecs:       &disks100,
				},
			},
		},
	},
	// C56414: https://portworx.testrail.net/index.php?/cases/view/56414
	// Install PX through operator with internal KVDB and auto journal drive on FlashArray
	//    and
	// C55003: https://portworx.testrail.net/index.php?/cases/view/55003
	// Uninstall PX through operator with Pure arrays, after clean install
	// TODO: Blocked due to PWX-21364, retest
	{
		ID:                      "C56414-C55003", // TODO: handle multiple test IDs cleanly(?) (or just have multiple tests)
		ShouldStartSuccessfully: true,
		BackendRequirements: BackendRequirements{
			RequiredArrays: 1,
		},
		Spec: simpleClusterSpec,
	},
	// C55133: https://portworx.testrail.net/index.php?/cases/view/55133
	// Install PX through operator with heterogeneous cluster, with different nodes having different capacity drives
	{
		ID:                      "C55133",
		ShouldStartSuccessfully: true,
		BackendRequirements: BackendRequirements{
			RequiredArrays: 1,
		},
		Spec: v1.StorageClusterSpec{
			Kvdb:         &internalKVDB,
			CloudStorage: autoJournalDrive,
			Nodes: []v1.NodeSpec{
				{
					Selector: v1.NodeSelector{
						NodeName: node0,
					},
					CloudStorage: &v1.CloudStorageNodeSpec{
						CloudStorageCommon: v1.CloudStorageCommon{
							DeviceSpecs:       &disks200,
							JournalDeviceSpec: &auto,
							KvdbDeviceSpec:    &size32,
						},
					},
				},
			},
		},
	},
	// C55134: https://portworx.testrail.net/index.php?/cases/view/55134
	// Install PX through operator on FlashArray with storage and storageless nodes
	// TODO: Needs to remove prereqs from storageless node before running test
	//{
	//	ID:                      "C55134",
	//	ShouldStartSuccessfully: true,
	//	BackendRequirements: BackendRequirements{
	//		RequiredArrays: 1,
	//	},
	//	Spec: v1.StorageClusterSpec{
	//		Kvdb: &internalKVDB,
	//		CloudStorage: &v1.CloudStorageSpec{
	//			CloudStorageCommon: v1.CloudStorageCommon{
	//				DeviceSpecs: &disks100,
	//				KvdbDeviceSpec:    &size32,
	//				// TODO: re-enable journal drives
	//			},
	//			MaxStorageNodes: &threeStorageNodes, // TODO: change this to using the node selector
	//		},
	//	},
	//},
	//C56412: https://portworx.testrail.net/index.php?/cases/view/56412
	//Install PX through operator with multiple FlashArrays (all valid)
	{
		ID:                      "C56412",
		ShouldStartSuccessfully: true,
		BackendRequirements: BackendRequirements{
			RequiredArrays: 2,
			InvalidArrays:  0,
		},
		Spec: simpleClusterSpec,
	},
	//C56413: https://portworx.testrail.net/index.php?/cases/view/56413
	//Install PX through operator with both valid and invalid FlashArrays
	{
		ID:                      "C56413",
		ShouldStartSuccessfully: true,
		BackendRequirements: BackendRequirements{
			RequiredArrays: 2,
			InvalidArrays:  1,
		},
		Spec: simpleClusterSpec,
	},
}

/*var flashArrayNegativeInstallTests = []TestrailCase{
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
	for _, testCase := range flashArrayPositiveInstallTests {
		t.Run(testCase.ID, matrixTest(testCase))
	}
}

func matrixTest(c TestrailCase) func(*testing.T) {
	return func(t *testing.T) {
		// Check that we have enough backends to support this test: skip early
		fleetBackends := GenerateFleetOrSkip(t, pxNamespace, c.BackendRequirements)

		// Get versions from URL
		logrus.Infof("Get component images from versions URL")
		imageListMap, err := testutil.GetImagesFromVersionURL(pxSpecGenURL)
		require.NoError(t, err)

		// Construct Portworx StorageCluster object
		cluster, err := constructStorageCluster(pxSpecGenURL, imageListMap)
		require.NoError(t, err)

		c.PopulateStorageCluster(cluster)

		// Add extra env variables
		releaseManifestURL := pxSpecGenURL
		if !strings.HasSuffix(releaseManifestURL, "/") {
			releaseManifestURL += "/"
		}
		releaseManifestURL += "version"

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
		logrus.Infof("Create or update %s in %s", OutputSecretName, pxNamespace)
		err = createPureSecret(fleetBackends, pxNamespace)
		require.NoError(t, err)

		// Deploy cluster
		logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
		cluster, err = createStorageCluster(cluster)
		require.NoError(t, err)

		// Validate cluster deployment
		logrus.Infof("Validate StorageCluster %s", cluster.Name)
		err = testutil.ValidateStorageCluster(imageListMap, cluster, defaultValidateDeployTimeout, defaultValidateDeployRetryInterval, c.ShouldStartSuccessfully, "")
		require.NoError(t, err)

		// Delete cluster
		logrus.Infof("Delete StorageCluster %s", cluster.Name)
		err = testutil.UninstallStorageCluster(cluster)
		require.NoError(t, err)

		// Validate cluster deletion
		logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
		err = testutil.ValidateUninstallStorageCluster(cluster, defaultValidateUninstallTimeout, defaultValidateUninstallRetryInterval)
		require.NoError(t, err)

		// Delete px-pure-secret
		logrus.Infof("Delete Secret %s", OutputSecretName)
		err = deletePureSecretIfExists(cluster.Namespace)
		require.NoError(t, err)
	}
}

func createPureSecret(config DiscoveryConfig, namespace string) error {
	pureJSON, err := config.DumpJSON()
	if err != nil {
		return err
	}

	_, err = coreops.Instance().UpdateSecretData(OutputSecretName, namespace, map[string][]byte{
		"pure.json": pureJSON,
	})
	return err
}

func deletePureSecretIfExists(namespace string) error {
	if err := coreops.Instance().DeleteSecret(OutputSecretName, namespace); !errors.IsNotFound(err) {
		return err
	}
	return nil
}
