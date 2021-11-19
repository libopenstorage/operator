package utils

import (
	"path"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/libopenstorage/operator/drivers/storage/portworx"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s/operator"
)

const (
	// specDir is a directory with all the specs
	specDir = "./operator-test"
	// PxNamespace is a default namespace for StorageCluster
	PxNamespace = "kube-system"
)

// CreateStorageClusterTestSpecFunc creates a function that returns test specs for a test case
func CreateStorageClusterTestSpecFunc(cluster *corev1.StorageCluster) func(t *testing.T) interface{} {
	return func(t *testing.T) interface{} {
		err := ConstructStorageCluster(cluster, PxSpecGenURL, PxSpecImages)
		require.NoError(t, err)
		return cluster
	}
}

// ConstructStorageCluster makes StorageCluster object and add all the common basic parameters that all StorageCluster should have
func ConstructStorageCluster(cluster *corev1.StorageCluster, specGenURL string, specImages map[string]string) error {
	// Set Portworx Image
	cluster.Spec.Image = specImages["version"]

	// Set Namespace
	cluster.Namespace = PxNamespace

	// Populate default Env Vars
	if err := populateDefaultEnvVars(cluster, specGenURL); err != nil {
		return err
	}

	return nil
}

// CreateStorageClusterFromSpec creates a storage cluster from a file
func CreateStorageClusterFromSpec(filename string) (*corev1.StorageCluster, error) {
	filepath := path.Join(specDir, filename)
	scheme := runtime.NewScheme()
	cluster := &corev1.StorageCluster{}
	if err := k8sutil.ParseObjectFromFile(filepath, scheme, cluster); err != nil {
		return nil, err
	}
	return CreateStorageCluster(cluster)
}

// CreateStorageCluster creates the given storage cluster on k8s
func CreateStorageCluster(cluster *corev1.StorageCluster) (*corev1.StorageCluster, error) {
	logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	return operator.Instance().CreateStorageCluster(cluster)
}

// DeployAndValidateStorageCluster creates and validates the storage cluster
func DeployAndValidateStorageCluster(cluster *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	// Populate default values to empty fields first
	portworx.SetPortworxDefaults(cluster)
	// Deploy cluster
	cluster, err := CreateStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deployment
	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(pxSpecImages, cluster, DefaultValidateDeployTimeout, DefaultValidateDeployRetryInterval, true, "")
	require.NoError(t, err)

	// Get the latest version of StorageCluster
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	return liveCluster
}

// UpdateStorageCluster updates the given storage cluster on k8s
func UpdateStorageCluster(cluster *corev1.StorageCluster) (*corev1.StorageCluster, error) {
	logrus.Infof("Update StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	return operator.Instance().UpdateStorageCluster(cluster)
}

// UpdateAndValidateStorageCluster update, validate and return new StorageCluster
func UpdateAndValidateStorageCluster(cluster *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	logrus.Infof("Update StorageCluster %s in %s", cluster.Name, cluster.Namespace)

	cluster, err := operator.Instance().UpdateStorageCluster(cluster)
	require.NoError(t, err)

	// Sleep for 5 seconds to let operator start the update process
	logrus.Debug("Sleeping for 5 seconds...")
	time.Sleep(5 * time.Second)

	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(pxSpecImages, cluster, DefaultValidateUpdateTimeout, DefaultValidateUpdateRetryInterval, true, "")
	require.NoError(t, err)

	// Get the latest version of live StorageCluster
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)
	return liveCluster
}

// UninstallAndValidateStorageCluster uninstall and validate the cluster deletion
func UninstallAndValidateStorageCluster(cluster *corev1.StorageCluster, t *testing.T) {
	// Delete cluster
	logrus.Infof("Delete StorageCluster %s", cluster.Name)
	err := testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster %s deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, DefaultValidateUninstallTimeout, DefaultValidateUninstallRetryInterval)
	require.NoError(t, err)
}

// addEnvVarToStorageCluster will overwrite existing or add new env variables
func addEnvVarToStorageCluster(envVarList []v1.EnvVar, cluster *corev1.StorageCluster) {
	envMap := make(map[string]v1.EnvVar)
	var newEnvVarList []v1.EnvVar
	for _, env := range cluster.Spec.Env {
		envMap[env.Name] = env
	}
	for _, env := range envVarList {
		envMap[env.Name] = env
	}
	for _, env := range envMap {
		newEnvVarList = append(newEnvVarList, env)
	}
	cluster.Spec.Env = newEnvVarList
}

// ValidateStorageClusterComponents validates storage cluster components
func ValidateStorageClusterComponents(cluster *corev1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}

func populateDefaultEnvVars(cluster *corev1.StorageCluster, specGenURL string) error {
	var envVarList []v1.EnvVar

	// Set release manifest URL and Docker credentials in case of edge-install.portworx.com
	if strings.Contains(specGenURL, "edge") {
		releaseManifestURL, err := testutil.ConstructPxReleaseManifestURL(specGenURL)
		if err != nil {
			return err
		}

		// Add release manifest URL to Env Vars
		envVarList = append(envVarList, v1.EnvVar{Name: testutil.PxReleaseManifestURLEnvVarName, Value: releaseManifestURL})
	}

	// Add Portworx image properties, if specified
	if PxDockerUsername != "" && PxDockerPassword != "" {
		envVarList = append(envVarList,
			[]v1.EnvVar{
				{Name: testutil.PxRegistryUserEnvVarName, Value: PxDockerUsername},
				{Name: testutil.PxRegistryPasswordEnvVarName, Value: PxDockerPassword},
			}...)
	}
	if PxImageOverride != "" {
		envVarList = append(envVarList, v1.EnvVar{Name: testutil.PxImageEnvVarName, Value: PxImageOverride})
	}

	addEnvVarToStorageCluster(envVarList, cluster)

	return nil
}
