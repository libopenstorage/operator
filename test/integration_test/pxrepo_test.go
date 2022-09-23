//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/operator/drivers/storage/portworx"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	k8sv1 "k8s.io/api/core/v1"
)

var (
	pxVer2_10, _ = version.NewVersion("2.10")
)

var pxrepoTestCases = []types.TestCase{
	{
		TestName:        "InstallAirGapped",
		TestrailCaseIDs: []string{"C58604", "C58605"},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "px-repo-test-negative"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)
			testEnv := []string{
				// $TEST_SKIP_EXTRACT_PX_MODULE=true prevents unpacking of internal KO-archive
				"TEST_SKIP_EXTRACT_PX_MODULE=true",
				// also setting up pre-exec to clean up various KO locations  (potentially left by previous PX installs)
				"PRE-EXEC=rmmod -f px ; rm -fr /opt/pwx/bin/px.ko /opt/pwx/bin/px.ko.xz /var/lib/osd/pxfs /opt/pwx/oci/rootfs/home/px-fuse/px.ko",
			}
			for _, s := range testEnv {
				sp := strings.Split(s, "=")
				cluster.Spec.Env = append(cluster.Spec.Env, k8sv1.EnvVar{Name: sp[0], Value: sp[1]})
			}
			cluster.Annotations = map[string]string{
				// Adding `-air-gapped` param stops download of kernel-headers and/or KO-modules from mirrors
				"portworx.io/misc-args": "-air-gapped",
			}
			cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			}
			return cluster
		},
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				testSpec := tc.TestSpec(t)
				cluster, ok := testSpec.(*corev1.StorageCluster)
				require.True(t, ok)

				// Populate default values to empty fields first
				k8sVersion, _ := version.NewVersion(ci_utils.K8sVersion)
				portworx.SetPortworxDefaults(cluster, k8sVersion)
				// Record pre-deploy timestamp
				installTime := time.Now()
				// Deploy cluster
				_, err := ci_utils.CreateStorageCluster(cluster)
				require.NoError(t, err)

				// Validate cluster deployment
				// -- WARNING: please make sure kernel sources/headers are not installed on the nodes, or the test will fail
				// -- e.g. `cd /usr/src; dpkg --purge linux-headers-generic linux-generic *`
				logrus.Infof("Validate StorageCluster %s has failed events", cluster.Name)
				err = test.ValidateStorageClusterFailedEvents(cluster, ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, "reason=FileSystemDependency", installTime, "")
				require.NoError(t, err)

				ci_utils.UninstallAndValidateStorageCluster(cluster, t)
			}
		},
		ShouldSkip: func(tc *types.TestCase) bool {
			// need px-operator v1.7, and px v2.10 or higher
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_7) ||
				ci_utils.GetPxVersionFromSpecGenURL(ci_utils.PxSpecGenURL).LessThan(pxVer2_10)
		},
	},

	{
		TestName:        "InstallAirGappedWithPXRepo",
		TestrailCaseIDs: []string{"C58604", "C58605"},
		TestSpec: func(t *testing.T) interface{} {
			cluster := &corev1.StorageCluster{}
			cluster.Name = "px-repo-included"
			err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
			require.NoError(t, err)
			testEnv := []string{
				// $TEST_SKIP_EXTRACT_PX_MODULE prevents unpacking of internal KO-archive
				"TEST_SKIP_EXTRACT_PX_MODULE=true",
				// set up pre-exec to clean up various px.ko module locations
				"PRE-EXEC=rmmod -f px ; rm -fr /opt/pwx/bin/px.ko /opt/pwx/bin/px.ko.xz /var/lib/osd/pxfs /opt/pwx/oci/rootfs/home/px-fuse/px.ko",
			}
			for _, s := range testEnv {
				sp := strings.Split(s, "=")
				cluster.Spec.Env = append(cluster.Spec.Env, k8sv1.EnvVar{Name: sp[0], Value: sp[1]})
			}
			// Adding `-air-gapped` param stops download of kernel-headers and/or KO-modules from mirrors
			cluster.Annotations = map[string]string{
				"portworx.io/misc-args": "-air-gapped",
			}
			// Initiate PX-Repo container
			cluster.Spec.PxRepo = &corev1.PxRepoSpec{
				Enabled: true,
				Image:   "docker.io/portworx/px-repo:latest",
			}
			cluster.Spec.DeleteStrategy = &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			}
			return cluster
		},
		TestFunc: BasicInstall,
		ShouldSkip: func(tc *types.TestCase) bool {
			return ci_utils.PxOperatorVersion.LessThan(ci_utils.PxOperatorVer1_7) ||
				ci_utils.GetPxVersionFromSpecGenURL(ci_utils.PxSpecGenURL).LessThan(pxVer2_10)
		},
	},
}

func TestPxRepo(t *testing.T) {
	for _, testCase := range pxrepoTestCases {
		testCase.RunTest(t)
	}
}
