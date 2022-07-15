//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

var (
	disksEnc = []string{"type=Premium_LRS,size=100,diskEncryptionSetID=invalid"}
)

var TestAzureDiskEncryptionCases = []types.TestCase{
	{
		TestName:        "InstallWithInvalidDiskEncryptionSetID",
		TestrailCaseIDs: []string{"C82917"},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "px-cluster-azure-incorrect-enc"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)

			cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs: &disksEnc,
				},
			}
			cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			}
			return cluster
		},
		TestFunc: EncryptedDiskInstallFail,
	},
	{
		TestName:        "InstallWithDiskEncryptionSetID",
		TestrailCaseIDs: []string{"C82915"},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "px-cluster-azure-enc"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)

			cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			}
			return cluster
		},
		TestFunc: EncryptedDiskInstallPass,
	},
}

func EncryptedDiskInstallFail(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		// Record pre-deploy timestamp
		installTime := time.Now()
		// Deploy cluster
		logrus.Infof("Deploy StorageCluster %s", cluster.Name)
		_, err := ci_utils.CreateStorageCluster(cluster)
		require.NoError(t, err)

		// Validate cluster deployment
		logrus.Infof("Validate StorageCluster %s has failed events", cluster.Name)
		err = test.ValidateStorageClusterInstallFailedWithEvents(cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, "reason=NodeStartFailure", installTime)
		require.NoError(t, err)

		// Wipe PX and validate
		logrus.Infof("Uninstall StorageCluster %s", cluster.Name)
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func EncryptedDiskInstallPass(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		encryptedDiskParam := false
		// Validate if a diskEncryptionSetID param is present in deviceSpec
		logrus.Infof("Checking if a diskEncryptionSetID param is present in deviceSpec")
		if cluster.Spec.CloudStorage != nil {
			if cluster.Spec.CloudStorage.DeviceSpecs != nil {
				deviceSpecs := *cluster.Spec.CloudStorage.DeviceSpecs
				for _, s := range deviceSpecs {
					if strings.Contains(s, "diskEncryptionSetID=") {
						encryptedDiskParam = true
						logrus.Infof("Param diskEncryptionSetID is present in deviceSpec")
						break
					}
				}
			}
		}
		require.True(t, encryptedDiskParam, "failed to validate the presence of diskEncryptionSetID in deviceSpec")

		// Deploy PX and validate
		logrus.Infof("Deploy and validate StorageCluster %s ", cluster.Name)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Wipe PX and validate
		logrus.Infof("Uninstall StorageCluster %s", cluster.Name)
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func TestAzure(t *testing.T) {
	for _, testCase := range TestAzureDiskEncryptionCases {
		testCase.RunTest(t)
	}
}
