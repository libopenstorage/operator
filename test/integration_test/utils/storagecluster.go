package utils

import (
	"path"
	"strings"
	"testing"

	"github.com/libopenstorage/cloudops"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/hashicorp/go-version"
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
	var envVarList []v1.EnvVar

	cluster.Spec.Image = specImages["version"]
	if cluster.Namespace == "" {
		cluster.Namespace = PxNamespace
	}

	if cluster.Namespace != PxNamespace {
		// If this is OCP on vSphere, attempt to find and copy PX vSphere secret to custom namespace
		if IsOcp && CloudProvider == cloudops.Vsphere {
			logrus.Debugf("This is OpenShift cluster and PX will be deployed in custom namespace %s, attempting to find and copy PX vSphere secret to custom namespace %s", cluster.Namespace, cluster.Namespace)
			if err := testutil.FindAndCopyVsphereSecretToCustomNamespace(cluster.Namespace); err != nil {
				return err
			}
		}
	}

	// Add OCP annotation
	if IsOcp {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/is-openshift"] = "true"

		// Create PX vSphere Env var credentials, if PX vSphere secret exists
		if CloudProvider == cloudops.Vsphere {
			ocpEnvVarCreds, err := testutil.CreateVsphereCredentialEnvVarsFromSecret(cluster.Namespace)
			if err != nil {
				return err
			}

			if ocpEnvVarCreds != nil {
				envVarList = append(envVarList, ocpEnvVarCreds...)
			}
		}
	}

	// Add EKS annotation
	if IsEks {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/is-eks"] = "true"
	}

	// Add AKS annotation
	if IsAks {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/is-aks"] = "true"
	}

	// Add GKE annotation
	if IsGke {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/is-gke"] = "true"
	}

	// Add OKE annotation
	if IsOke {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		cluster.Annotations["portworx.io/is-oke"] = "true"
	}

	// Add custom annotations
	if len(PxCustomAnnotations) != 0 {
		if cluster.Annotations == nil {
			cluster.Annotations = make(map[string]string)
		}
		annotations := strings.Split(PxCustomAnnotations, ",")
		for _, annotation := range annotations {
			keyvalue := strings.Split(annotation, ":")
			cluster.Annotations[keyvalue[0]] = strings.TrimSpace(keyvalue[1])
		}
	}

	// Populate cloud storage
	if len(PxDeviceSpecs) != 0 {
		pxDeviceSpecs := strings.Split(PxDeviceSpecs, ";")
		cloudCommon := corev1.CloudStorageCommon{
			DeviceSpecs:    &pxDeviceSpecs,
			KvdbDeviceSpec: &PxKvdbSpec,
		}
		cloudProvider := &CloudProvider
		// TODO Hardcoding maxStorageNodesPerZone to 5 as a workaround
		// until we have better way to do this
		maxStorageNodesPerZone := uint32(5)
		cloudStorage := &corev1.CloudStorageSpec{
			Provider:               cloudProvider,
			CloudStorageCommon:     cloudCommon,
			MaxStorageNodesPerZone: &maxStorageNodesPerZone,
		}

		cluster.Spec.CloudStorage = cloudStorage
	}

	// Populate default ENV vars
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
	return operator.Instance().CreateStorageCluster(cluster)
}

// DeployStorageCluster creates StorageCluster with Portworx defaults
func DeployStorageCluster(cluster *corev1.StorageCluster, pxSpecImages map[string]string) (*corev1.StorageCluster, error) {
	// Populate default values to empty fields first
	k8sVersion, _ := version.NewVersion(K8sVersion)
	if err := portworx.SetPortworxDefaults(cluster, k8sVersion); err != nil {
		return nil, err
	}

	// Deploy StorageCluster
	existingCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if errors.IsNotFound(err) {
		return CreateStorageCluster(cluster)
	} else if err != nil {
		return nil, err
	}
	return existingCluster, nil
}

// DeployAndValidateStorageCluster creates and validates StorageCluster
func DeployAndValidateStorageCluster(cluster *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	// Create StorageCluster
	cluster, err := DeployStorageCluster(cluster, pxSpecImages)
	require.NoError(t, err)

	// Validate StorageCluster deployment
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

	newCluster, err := operator.Instance().UpdateStorageCluster(cluster)
	if err != nil {
		return nil, err
	}

	return newCluster, nil
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
func UpdateAndValidatePvcController(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidatePvcController(pxSpecImages, latestLiveCluster, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidateStork update StorageCluster, validates Stork components only and return latest version of live StorageCluster
func UpdateAndValidateStork(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidateStork(pxSpecImages, latestLiveCluster, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
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

	err = testutil.ValidateMonitoring(pxSpecImages, latestLiveCluster, latestLiveCluster, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidateSecurity update StorageCluster, validates Security components only and return latest version of live StorageCluster
func UpdateAndValidateSecurity(cluster *corev1.StorageCluster, previouslyEnabled bool, f func(*corev1.StorageCluster) *corev1.StorageCluster, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidateSecurity(latestLiveCluster, previouslyEnabled, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidateCSI update StorageCluster, validates CSI components only and return latest version of live StorageCluster
func UpdateAndValidateCSI(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidateCSI(pxSpecImages, latestLiveCluster, DefaultValidateComponentTimeout, DefaultValidateComponentRetryInterval)
	require.NoError(t, err)

	return latestLiveCluster
}

// UpdateAndValidateGrafana update StorageCluster, validates grafana and return latest version of live StorageCluster
func UpdateAndValidateGrafana(cluster *corev1.StorageCluster, f func(*corev1.StorageCluster) *corev1.StorageCluster, pxSpecImages map[string]string, t *testing.T) *corev1.StorageCluster {
	liveCluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	newCluster := f(liveCluster)

	latestLiveCluster, err := UpdateStorageCluster(newCluster)
	require.NoError(t, err)

	err = testutil.ValidateGrafana(pxSpecImages, latestLiveCluster)
	require.NoError(t, err)

	return latestLiveCluster
}

// UninstallAndValidateStorageCluster uninstall and validate the cluster deletion
func UninstallAndValidateStorageCluster(cluster *corev1.StorageCluster, t *testing.T) {
	// Delete cluster
	logrus.Infof("Delete StorageCluster [%s]", cluster.Name)
	err := testutil.UninstallStorageCluster(cluster)
	require.NoError(t, err)

	// Validate cluster deletion
	logrus.Infof("Validate StorageCluster [%s] deletion", cluster.Name)
	err = testutil.ValidateUninstallStorageCluster(cluster, DefaultValidateUninstallTimeout, DefaultValidateUninstallRetryInterval)
	require.NoError(t, err)
}

// ValidateStorageClusterComponents validates storage cluster components
func ValidateStorageClusterComponents(cluster *corev1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}
