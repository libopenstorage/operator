package utils

import (
	"path"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
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
	cluster.Spec.Image = specImages["version"]
	cluster.Namespace = PxNamespace
	env, err := addDefaultEnvVars(cluster.Spec.Env, specGenURL)
	if err != nil {
		return err
	}
	cluster.Spec.Env = env
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
	liveCluster, err := CreateStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deployment
	logrus.Infof("Validate StorageCluster %s", liveCluster.Name)
	err = testutil.ValidateStorageCluster(pxSpecImages, liveCluster, DefaultValidateDeployTimeout, DefaultValidateDeployRetryInterval, true, "")
	require.NoError(t, err)

	return liveCluster
}

// UpdateStorageCluster updates the given storage cluster on k8s
func UpdateStorageCluster(cluster *corev1.StorageCluster) (*corev1.StorageCluster, error) {
	logrus.Infof("Update StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	return operator.Instance().UpdateStorageCluster(cluster)
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

// ValidateStorageClusterComponents validates storage cluster components
func ValidateStorageClusterComponents(cluster *corev1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}
