//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/libopenstorage/cloudops"
	awsops "github.com/libopenstorage/cloudops/aws"
	gceops "github.com/libopenstorage/cloudops/gce"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	compute "google.golang.org/api/compute/v1"
)

var (
	disksEncAzure  = []string{"type=Premium_LRS,size=100,diskEncryptionSetID=invalid"}
	disksEncGce    = []string{"type=pd-standard,size=150,kms=invalid"}
	disksTagsAzure = []string{"type=Premium_LRS,size=65,tags=key:val;"}
	disksTagsEks   = []string{"type=gp3,size=55,tags=key:val;"}
	disksTagsGce   = []string{"type=pd-standard,size=80,tags=key:val"}
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

			return cluster
		},
		TestFunc: EncryptedDiskInstallFail,
		ShouldSkip: func(tc *types.TestCase) bool {
			return !ci_utils.IsAks && !ci_utils.IsGke

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

var TestDiskTaggingCases = []types.TestCase{
	{
		TestName:        "SimpleTags",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "px-cluster-simpletags"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)
			if ci_utils.IsAks {
				cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						DeviceSpecs: &disksTagsAzure,
					},
				}
			}
			if ci_utils.IsGke {
				cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						DeviceSpecs: &disksTagsGce,
					},
				}
			}

			if ci_utils.IsEks {
				cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						DeviceSpecs: &disksTagsEks,
					},
				}
			}

			return cluster
		},
		TestFunc: CheckTags,
		ShouldSkip: func(tc *types.TestCase) bool {
			return !ci_utils.IsEks && !ci_utils.IsGke && !ci_utils.IsAks
		},
	},
}

func CheckTags(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		cluster, ok := testSpec.(*corev1.StorageCluster)
		require.True(t, ok)

		logrus.Infof("Deploy and validate StorageCluster %s ", cluster.Name)
		cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

		logrus.Infof("Checking tags on disk")
		labelClusterUID := "labelClusterUID"

		if ci_utils.IsEks {
			awsClient, err := awsops.NewClient()
			require.NoError(t, err)
			result, err := awsClient.Enumerate(nil, map[string]string{labelClusterUID: string(cluster.UID)}, "")
			require.NoError(t, err)
			volumes := result[cloudops.SetIdentifierNone]
			require.Greater(t, len(volumes), 0, "No volume found")
			if len(volumes) > 0 {
				logrus.Infof("Found %v volumes", len(volumes))
				for _, vol := range volumes {
					ec2Vol := vol.(*ec2.Volume)
					tags, err := awsClient.Tags(*ec2Vol.VolumeId)
					require.NoError(t, err)
					if tags["pxtype"] == "data" {
						require.Equal(t, tags["key"], "val", "Volume Tag did not have the correct value")
					}
				}
			}
		}

		if ci_utils.IsGke {
			gkeClient, err := gceops.NewClient()
			require.NoError(t, err)
			result, err := gkeClient.Enumerate(nil, map[string]string{labelClusterUID: string(cluster.UID)}, "")
			require.NoError(t, err)
			volumes := result[cloudops.SetIdentifierNone]
			require.Greater(t, len(volumes), 0, "No volume found")
			if len(volumes) > 0 {
				logrus.Infof("Found %v volumes", len(volumes))
				for _, vol := range volumes {
					gceVol := vol.(*compute.Disk)
					tags, err := gkeClient.Tags((*gceVol).Name)
					require.NoError(t, err)
					if tags["pxtype"] == "data" {
						require.Equal(t, tags["key"], "val", "Volume Tag did not have the correct value")
					}
				}
			}
		}
	}
}

func TestCloudDisk(t *testing.T) {
	for _, testCase := range TestCloudDiskEncryptionCases {
		testCase.RunTest(t)
	}

	for _, testCase := range TestDiskTaggingCases {
		testCase.RunTest(t)
	}
}
