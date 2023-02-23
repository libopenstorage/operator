//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
)

var (
	disksEncAzure = []string{"type=Premium_LRS,size=100,diskEncryptionSetID=invalid"}
	disksEncGce   = []string{"type=pd-standard,size=150,kms=invalid"}
	disksEncOke   = []string{"type=pd-standard,size=150,kms=invalid"}
)

var TestCloudDiskEncryptionCases = []types.TestCase{
	{
		TestName:        "InstallWithInvalidKey",
		TestrailCaseIDs: []string{"C83461"},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "px-cluster-incorrect-enc"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)
			if ci_utils.IsAks {
				cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						DeviceSpecs: &disksEncAzure,
					},
				}
			}
			if ci_utils.IsGke {
				cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						DeviceSpecs: &disksEncGce,
					},
				}
			}
			if ci_utils.IsOke {
				cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						DeviceSpecs: &disksEncOke,
					},
				}
			}

			return cluster
		},
		TestFunc: EncryptedDiskInstallFail,
		ShouldSkip: func(tc *types.TestCase) bool {
			return !ci_utils.IsAks && !ci_utils.IsGke && !ci_utils.IsOke

		},
	},
	{
		TestName:        "InstallWithValidKey",
		TestrailCaseIDs: []string{"C83463,C83464"},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "px-cluster-enc"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)

			return cluster
		},
		TestFunc: EncryptedDiskInstallPass,
		ShouldSkip: func(tc *types.TestCase) bool {
			return !ci_utils.IsAks && !ci_utils.IsGke
		},
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
		err = test.ValidateStorageClusterInstallFailedWithEvents(cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, "", installTime, "NodeStartFailure")
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
		logrus.Infof("Checking if a customer encryption key param is present in deviceSpec")
		if cluster.Spec.CloudStorage != nil {
			if cluster.Spec.CloudStorage.DeviceSpecs != nil {
				deviceSpecs := *cluster.Spec.CloudStorage.DeviceSpecs
				for _, s := range deviceSpecs {
					if ci_utils.IsAks &&
						strings.Contains(s, "diskEncryptionSetID=") {
						encryptedDiskParam = true
						logrus.Infof("Param diskEncryptionSetID is present in deviceSpec")
						break
					}
					if ci_utils.IsGke &&
						strings.Contains(s, "kms=") {
						encryptedDiskParam = true
						logrus.Infof("Param kms is present in deviceSpec")
						break
					}
					if ci_utils.IsOke &&
						strings.Contains(s, "kms=") {
						encryptedDiskParam = true
						logrus.Infof("Param kms is present in deviceSpec")
						break
					}
				}
			}
		}
		if encryptedDiskParam == false {
			logrus.Warn("Failed to validate the presence of customer encryption key in deviceSpec")
			return
		}

		// Deploy PX and validate
		logrus.Infof("Deploy and validate StorageCluster %s ", cluster.Name)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		// Wipe PX and validate
		logrus.Infof("Uninstall StorageCluster %s", cluster.Name)
		ci_utils.UninstallAndValidateStorageCluster(cluster, t)
	}
}

func TestCloudDisk(t *testing.T) {
	for _, testCase := range TestCloudDiskEncryptionCases {
		testCase.RunTest(t)
	}
}
