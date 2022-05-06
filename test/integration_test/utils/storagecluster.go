package utils

import (
	"path"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	if cluster.Namespace == "" {
		cluster.Namespace = PxNamespace
	}

	// Add OCP annotation
	if IsOcp {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/is-openshift"] = "true"
	}

	// Add EKS annotation
	if IsEks {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/is-eks"] = "true"
	}

	// Populate cloud storage
	if len(PxDeviceSpecs) != 0 {
		pxDeviceSpecs := strings.Split(PxDeviceSpecs, ";")
		cloudCommon := corev1.CloudStorageCommon{
			DeviceSpecs:    &pxDeviceSpecs,
			KvdbDeviceSpec: &PxKvdbSpec,
		}
		cloudProvider := &CloudProvider
		cloudStorage := &corev1.CloudStorageSpec{
			Provider:           cloudProvider,
			CloudStorageCommon: cloudCommon,
		}

		cluster.Spec.CloudStorage = cloudStorage
	}

	// Populate default ENV vars
	var envVarList []v1.EnvVar
	env, err := addDefaultEnvVars(cluster.Spec.Env, specGenURL)
	if err != nil {
		return err
	}
	envVarList = append(envVarList, env...)

	// Populate extra ENV vars, if any were passed
	if len(PxEnvVars) != 0 {
		vars := strings.Split(PxEnvVars, ",")
		for _, v := range vars {
			keyvalue := strings.Split(v, "=")
			envVarList = append(envVarList, v1.EnvVar{Name: keyvalue[0], Value: keyvalue[1]})
		}
	}
	cluster.Spec.Env = envVarList

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
	if cluster.Namespace != PxNamespace {
		ns := &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}
		if err := CreateObjects([]runtime.Object{ns}); err != nil {
			return nil, err
		}
	}
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

// UpdateStorageCluster updates the given StorageCluster and return latest live version of it
func UpdateStorageCluster(cluster *corev1.StorageCluster) (*corev1.StorageCluster, error) {
	logrus.Infof("Update StorageCluster %s in %s", cluster.Name, cluster.Namespace)

	cluster, err := operator.Instance().UpdateStorageCluster(cluster)
	if err != nil {
		return nil, err
	}

	return cluster, nil
}

// UpdateAndValidateStorageCluster update, validate and return new StorageCluster
func UpdateAndValidateStorageCluster(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	logrus.Infof("Validate StorageCluster %s", latestLiveCluster.Name)
	err = testutil.ValidateStorageCluster(pxSpecImages, latestLiveCluster, DefaultValidateUpdateTimeout, DefaultValidateUpdateRetryInterval, true, "")
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidatePvcController update StorageCluster, validates PVC Controller components only and return latest version of live StorageCluster
func UpdateAndValidatePvcController(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, k8sVersion string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidatePvcController(pxSpecImages, latestLiveCluster, k8sVersion, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidateStork update StorageCluster, validates Stork components only and return latest version of live StorageCluster
func UpdateAndValidateStork(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, k8sVersion string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidateStork(pxSpecImages, latestLiveCluster, k8sVersion, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidateAutopilot update StorageCluster, validates Autopilot components only and return latest version of live StorageCluster
func UpdateAndValidateAutopilot(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidateAutopilot(pxSpecImages, latestLiveCluster, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidateMonitoring update StorageCluster, validates Monitoring components only and return latest version of live StorageCluster
func UpdateAndValidateMonitoring(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidateMonitoring(pxSpecImages, latestLiveCluster, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
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

	// Delete namespace if not kube-system
	if cluster.Namespace != PxNamespace {
		ns := &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}
		err = DeleteObjects([]runtime.Object{ns})
		require.NoError(t, err)
		err = ValidateObjectsAreTerminated([]runtime.Object{ns}, false)
		require.NoError(t, err)
	}
}

// ValidateStorageClusterComponents validates storage cluster components
func ValidateStorageClusterComponents(cluster *corev1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}
