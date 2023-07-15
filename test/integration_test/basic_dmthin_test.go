//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/test/integration_test/cloud_provider"
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
	pxVer2_13_0, _ = version.NewVersion("2.13.0")
)

func defaultDmthinSpec(t *testing.T) *corev1.StorageCluster {
	objects, err := ci_utils.ParseSpecs("storagecluster/storagecluster-with-all-components.yaml")
	require.NoError(t, err)
	cluster, ok := objects[0].(*corev1.StorageCluster)
	require.True(t, ok)
	cluster.Name = "test-stc"
	cluster.Namespace = "kube-system"
	cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled = false
	cluster.Annotations = map[string]string{
		"portworx.io/misc-args": "-T PX-StoreV2",
	}
	return cluster
}

var testDmthinCases = []types.TestCase{
	{
		TestName:        "BasicInstallDmthinWithMetadataDevice",
		TestrailCaseIDs: []string{""},
		TestSpec: func(t *testing.T) interface{} {
			provider := cloud_provider.GetCloudProvider()
			cluster := defaultDmthinSpec(t)
			cloudSpec := &corev1.CloudStorageSpec{}
			cloudSpec.DeviceSpecs = provider.GetDefaultDataDrives()
			cloudSpec.SystemMdDeviceSpec = provider.GetDefaultMetadataDrive()
			cluster.Spec.CloudStorage = cloudSpec
			return cluster
		},
		TestFunc: BasicInstallDmthin,
	},
	{
		TestName:        "BasicInstallDmthinWithMetadataAndKVDBDevice",
		TestrailCaseIDs: []string{""},
		TestSpec: func(t *testing.T) interface{} {
			provider := cloud_provider.GetCloudProvider()
			cluster := defaultDmthinSpec(t)
			cloudSpec := &corev1.CloudStorageSpec{}
			cloudSpec.DeviceSpecs = provider.GetDefaultDataDrives()
			cloudSpec.SystemMdDeviceSpec = provider.GetDefaultMetadataDrive()
			cloudSpec.KvdbDeviceSpec = provider.GetDefaultKvdbDrive()
			cluster.Spec.CloudStorage = cloudSpec
			return cluster
		},
		TestFunc: BasicInstallDmthin,
	},
}

func TestStorageClusterBasicDMthin(t *testing.T) {
	for _, testCase := range testDmthinCases {
		testCase.RunTest(t)
	}
}

func BasicInstallDmthin(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
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

		logrus.Infof("MYD: url: %v, image: %v", ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)

		// Construct StorageCluster
		err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
		require.NoError(t, err)

		// Deploy PX and validate
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Verify we see PXStoreV2 in pxctl status output
		out, _, err := ci_utils.RunPxCmd("/usr/local/bin/pxctl status| grep 'PX-StoreV2'|wc -l")
		require.NoError(t, err)
		out = strings.TrimSpace(out)
		val, err := strconv.Atoi(out)
		require.NoError(t, err)
		require.Greaterf(t, val, 0, "pxctl status doesn't have PX-StoreV2")
		// Verify we see PXStoreV2 in pxctl sv pool show. This further verifies that pools were created using cloud drives
		out, _, err = ci_utils.RunPxCmd("/usr/local/bin/pxctl sv pool show| grep 'PX-StoreV2' | wc -l")
		require.NoError(t, err)
		out = strings.TrimSpace(out)
		val, err = strconv.Atoi(out)
		require.NoError(t, err)
		require.Greaterf(t, val, 0, "pxctl status doesn't have PX-StoreV2")

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
}
