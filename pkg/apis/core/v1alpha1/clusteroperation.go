package v1alpha1

import (
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ClusterOperationResourceName is name for "clusteroperation" resource
	ClusterOperationResourceName = "clusteroperation"
	// ClusterOperationResourcePlural is plural for "clusteroperation" resource
	ClusterOperationResourcePlural = "clusteroperations"
	// ClusterOperationShortName  is the shortname for "clusteroperation" resource
	ClusterOperationShortName = "cor"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:deepcopy-gen=true

// ClusterOperation represents a ClusterOperation resource
type ClusterOperation struct {
	meta_v1.TypeMeta   `json:",inline"`
	meta_v1.ObjectMeta `json:"metadata"`
	Spec               ClusterOperationSpec   `json:"spec"`
	Status             ClusterOperationStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterOperationList is a list of ClusterOperation
type ClusterOperationList struct {
	meta_v1.TypeMeta `json:",inline"`
	meta_v1.ListMeta `json:"metadata"`
	Items            []ClusterOperation `json:"items"`
}

// ClusterOperationSpec is the spec used to define a ClusterOperation
type ClusterOperationSpec struct {
	Operation string `json:"operation"`
	// Params are the opaque parameters that will be used for the above action
	Params map[string]string `json:"params"`
}

// TODO: the section below is in progress

// ClusterOperationStatus is the status of a cluster operation resource
type ClusterOperationStatus struct {
	//Stage  ClusterOperationStageType `json:"stage"`
	//Status string                    `json:"status"`
}

//// ClusterOperationStatusType is types of statuses of a ClusterOperation objects
//type ClusterOperationStatusType string

//const (
//	// ClusterOperationStatusInitial is an initial state of cluster operations
//	ClusterOperationStatusInitial ClusterOperationStatusType = ""
//	// ClusterOperationStatusPending is an pending state of cluster operations
//	ClusterOperationStatusPending ClusterOperationStatusType = "Pending"
//	// ClusterOperationStatusInProgress is an in progress state of cluster operations
//	ClusterOperationStatusInProgress ClusterOperationStatusType = "InProgress"
//	// ClusterOperationStatusFailed is a failed state of cluster operations
//	ClusterOperationStatusFailed ClusterOperationStatusType = "Failed"
//	// ClusterOperationStatusSuccessful is a successful state of cluster operations
//	ClusterOperationStatusSuccessful ClusterOperationStatusType = "Successful"
//)

// ClusterOperationStageType is the stage of the ClusterOperation object
//type ClusterOperationStageType string
//
//const ( //TODO: Stolen from storagenodecluster statuses. Define own stages
//	// ClusterOperationStageInitial is when the cluster operation is just created
//	ClusterOperationStageInitial ClusterOperationStageType = ""
//	// ClusterOperationStagePreChecks is when the cluster operation is going through prechecks
//	ClusterOperationStagePreChecks ClusterOperationStageType = "PreChecks"
//	// ClusterOperationStageFinal is when the cluster operation is in a final state
//	ClusterOperationStageFinal ClusterOperationStageType = "Final"
//)

func init() {
	SchemeBuilder.Register(&ClusterOperation{}, &ClusterOperationList{})
}
