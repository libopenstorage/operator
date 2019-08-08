package storage

import (
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/errors"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// UpdateDriverInfo consists of information that the controller
// wants to convey to the Driver with regards to the cluster and node state.
type UpdateDriverInfo struct {
	// ZoneToInstancesMap is a map of zone to number of instances in that zone
	ZoneToInstancesMap map[string]int
	// CloudProvider cloud provider where this cluster is running
	CloudProvider string
}

// Driver defines an external storage driver interface.
// Any driver that wants to be used with openstorage operator needs
// to implement these interfaces.
type Driver interface {
	// Init initializes the storage driver
	Init(client.Client, record.EventRecorder) error
	// String returns the string name of the driver
	String() string
	// UpdateDriver updates the driver with the current cluster and node conditions.
	// This API can be extended to provide information other than the StorageCluster
	// to the drivers from the controller reconcile loop.
	UpdateDriver(*UpdateDriverInfo) error
	// ClusterPluginInterface interface to manage storage cluster
	ClusterPluginInterface
	// StorkInterface interface to manage Stork related operations
	StorkInterface
}

// StorkInterface interface to manage Stork related operations
type StorkInterface interface {
	// GetStorkDriverName returns the string name of the driver in Stork
	GetStorkDriverName() (string, error)
	// GetStorkEnvList returns a list of env vars that need to be passed to Stork
	GetStorkEnvList(cluster *corev1alpha1.StorageCluster) []v1.EnvVar
}

// ClusterPluginInterface interface to manage storage cluster
type ClusterPluginInterface interface {
	// PreInstall the driver should do whatever it is needed before the pods
	// start to make sure the cluster comes up correctly. This should be
	// idempotent and subsequent calls should result in the same result.
	PreInstall(*corev1alpha1.StorageCluster) error
	// GetStoragePodSpec given the storage cluster spec it returns the pod spec
	GetStoragePodSpec(*corev1alpha1.StorageCluster) v1.PodSpec
	// GetSelectorLabels returns driver specific labels that are applied on the pods
	GetSelectorLabels() map[string]string
	// SetDefaultsOnStorageCluster sets the driver specific defaults on the storage
	// cluster spec if they are not set
	SetDefaultsOnStorageCluster(*corev1alpha1.StorageCluster)
	// UpdateStorageClusterStatus update the status of storage cluster
	UpdateStorageClusterStatus(*corev1alpha1.StorageCluster) error
	// DeleteStorage is going to uninstall and delete the storage service based on
	// StorageClusterDeleteStrategy. DeleteStorage should provide idempotent behavior
	// and subsequent calls should result in the same result.
	// If the storage service has already been deleted then it will return nil
	// If the storage service deletion is in progress then it will return the appropriate status
	DeleteStorage(*corev1alpha1.StorageCluster) (*corev1alpha1.ClusterCondition, error)
}

var (
	storageDrivers = make(map[string]Driver)
)

// Register registers the given storage driver
func Register(name string, d Driver) error {
	logrus.Debugf("Registering storage driver: %v", name)
	storageDrivers[name] = d
	return nil
}

// Get an external storage driver to be used with the operator
func Get(name string) (Driver, error) {
	d, ok := storageDrivers[name]
	if ok {
		return d, nil
	}
	return nil, &errors.ErrNotFound{
		ID:   name,
		Type: "StorageDriver",
	}
}
