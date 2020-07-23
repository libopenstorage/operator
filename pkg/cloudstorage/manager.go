package cloudstorage

import (
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
)

// CloudDriveConfig is the configuration for a single drive
type CloudDriveConfig struct {
	// Type of cloud storage
	Type string `json:"type,omitempty"`
	// Size of cloud storage
	SizeInGiB uint64 `json:"sizeInGiB,omitempty"`
	// IOPS provided by cloud storage
	IOPS uint32 `json:"iops,omitempty"`
	// Options are additional options to the storage
	Options map[string]string `json:"options,omitempty"`
}

// Config is the cloud storage configuration for a node
// TODO: This struct should eventually move under StorageNode
type Config struct {
	// CloudStorage is the current cloud configuration
	CloudStorage []CloudDriveConfig `json:"cloudStorage,omitempty"`
	// StorageNodesPerZone is the number of storage instances per zone
	StorageInstancesPerZone int32 `json:"storageInstancesPerZone,omitempty"`
}

// Manager provides an interface to interact with cloud storage
// provisioner. It is an abstraction layer to interact with the APIs in
// libopenstorage/cloudops repository
type Manager interface {
	// CreateStorageDistributionMatrix creates a config map which contains
	// the cloud specific storage distribution matrix
	CreateStorageDistributionMatrix() error
	// GetStorageNodeConfig based on the cloud provider will return
	// the storage configuration for a single node
	GetStorageNodeConfig([]corev1.CloudStorageCapacitySpec, int) (*Config, error)
	// GetInstancesPerZoneNum calculates an amount of StorageNode instances per zone
	GetInstancesPerZoneNum(int) int
}
