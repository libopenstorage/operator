package v1alpha1

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
	Spec            StorageNodeSpec `json:"spec,omitempty"`
	Status          NodeStatus      `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StorageNodeStatusList is a list of statuses for storage nodes
type StorageNodeStatusList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []StorageNodeStatus `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StorageNodeStatus{}, &StorageNodeStatusList{})
}
