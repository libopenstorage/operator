package v1alpha2

import (
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// StorageNodeStatusResourceName is name for "storagenodestatus" resource
	StorageNodeStatusResourceName = "storagenodestatus"
	// StorageNodeStatusResourcePlural is plural for "storagenodestatus" resource
	StorageNodeStatusResourcePlural = "storagenodestatuses"
	// StorageNodeStatusShortName is the shortname for "storagenodestatus" resource
	StorageNodeStatusShortName = "sns"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StorageNodeStatus represents the status of all storage cluster nodes
type StorageNodeStatus struct {
	meta.TypeMeta   `json:",inline"`
	meta.ObjectMeta `json:"metadata,omitempty"`
	Status          NodeStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StorageNodeStatusList is a list of statuses for storage nodes
type StorageNodeStatusList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []StorageNodeStatus `json:"items"`
}

// CloudStorageConfig is the cloud storage configuration for this node
type CloudStorageConfig struct {
	// Type of cloud storage
	Type string `json:"type,omitempty"`
	// Size of cloud storage
	SizeInGiB uint64 `json:"sizeInGiB,omitempty"`
	// IOPS provided by cloud storage
	IOPS uint32 `json:"iops,omitmepty"`
	// Options are additional options to the storage
	Options map[string]string `json:"options,omitempty"`
}

// NodeConfig is the current configuration for a node
type NodeConfig struct {
	// CloudStorage is the current cloud configuration
	CloudStorage []CloudStorageConfig `json:"cloudStorage,omitempty"`
	// StorageInstancesPerZone is the number of storage instances per zone
	StorageInstancesPerZone int `json:"storageInstancesPerZone,omitempty"`
}

// NodeStatus contains the status of the storage node
type NodeStatus struct {
	// NodeUID unique identifier for the node
	NodeUID string `json:"nodeUid,omitempty"`
	// Network details used by the storage driver
	Network NetworkStatus `json:"network,omitempty"`
	// Geo topology information for a node
	Geo Geography `json:"geography,omitempty"`
	// Conditions is an array of current node conditions
	Conditions []NodeCondition `json:"conditions,omitempty"`
	// Config is the configured spec for the node
	Config NodeConfig `json:"nodeSpec,omitempty"`
}

// NetworkStatus network status of the storage node
type NetworkStatus struct {
	// DataIP is the IP address used by storage driver for data traffic
	DataIP string `json:"dataIP,omitempty"`
	// MgmtIP is the IP address used by storage driver for management traffic
	MgmtIP string `json:"mgmtIP,omitempty"`
}

// NodeCondition contains condition information for a storage node
type NodeCondition struct {
	// Type of the node condition
	Type NodeConditionType `json:"type,omitempty"`
	// Status of the condition
	Status ConditionStatus `json:"status,omitempty"`
	// Reason is human readable message indicating details about the condition status
	Reason string `json:"reason,omitempty"`
}

// NodeConditionType is the enum type for different node conditions
type NodeConditionType string

// These are valid conditions of the storage node. They correspond to different
// components in the storage cluster node.
const (
	// NodeState is used for overall state of the node
	NodeState NodeConditionType = "NodeState"
	// StorageState is used for the state of storage in the node
	StorageState NodeConditionType = "StorageState"
)

// ConditionStatus is the enum type for node condition statuses
type ConditionStatus string

// These are valid statuses of different node conditions.
const (
	// NodeOnline means the node condition is online and healthy
	NodeOnline ConditionStatus = "Online"
	// NodeInit means the node condition is in intializing state
	NodeInit ConditionStatus = "Intializing"
	// NodeNotInQuorum means the node is not in quorum
	NodeNotInQuorum ConditionStatus = "NotInQuorum"
	// NodeMaintenance means the node condition is in maintenance state
	NodeMaintenance ConditionStatus = "Maintenance"
	// NodeDecommissioned means the node condition is in decommissioned state
	NodeDecommissioned ConditionStatus = "Decommissioned"
	// NodeDegraded means the node condition is in degraded state
	NodeDegraded ConditionStatus = "Degraded"
	// NodeOffline means the node condition is in offline state
	NodeOffline ConditionStatus = "Offline"
	// NodeUnknown means the node condition is not known
	NodeUnknown ConditionStatus = "Unknown"
)

func init() {
	SchemeBuilder.Register(&StorageNodeStatus{}, &StorageNodeStatusList{})
}
