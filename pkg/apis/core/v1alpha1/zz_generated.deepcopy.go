// +build !ignore_autogenerated

/*
Copyright 2019 Openstorage.org

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Code generated by deepcopy-gen. DO NOT EDIT.

package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AuthSpec) DeepCopyInto(out *AuthSpec) {
	*out = *in
	if in.GuestAccess != nil {
		in, out := &in.GuestAccess, &out.GuestAccess
		*out = new(GuestAccessType)
		**out = **in
	}
	if in.SelfSigned != nil {
		in, out := &in.SelfSigned, &out.SelfSigned
		*out = new(SelfSignedSpec)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AuthSpec.
func (in *AuthSpec) DeepCopy() *AuthSpec {
	if in == nil {
		return nil
	}
	out := new(AuthSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AutopilotSpec) DeepCopyInto(out *AutopilotSpec) {
	*out = *in
	if in.Providers != nil {
		in, out := &in.Providers, &out.Providers
		*out = make([]DataProviderSpec, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Args != nil {
		in, out := &in.Args, &out.Args
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	if in.Env != nil {
		in, out := &in.Env, &out.Env
		*out = make([]v1.EnvVar, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AutopilotSpec.
func (in *AutopilotSpec) DeepCopy() *AutopilotSpec {
	if in == nil {
		return nil
	}
	out := new(AutopilotSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *CloudStorageCapacitySpec) DeepCopyInto(out *CloudStorageCapacitySpec) {
	*out = *in
	if in.Options != nil {
		in, out := &in.Options, &out.Options
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new CloudStorageCapacitySpec.
func (in *CloudStorageCapacitySpec) DeepCopy() *CloudStorageCapacitySpec {
	if in == nil {
		return nil
	}
	out := new(CloudStorageCapacitySpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *CloudStorageSpec) DeepCopyInto(out *CloudStorageSpec) {
	*out = *in
	if in.DeviceSpecs != nil {
		in, out := &in.DeviceSpecs, &out.DeviceSpecs
		*out = new([]string)
		if **in != nil {
			in, out := *in, *out
			*out = make([]string, len(*in))
			copy(*out, *in)
		}
	}
	if in.CapacitySpecs != nil {
		in, out := &in.CapacitySpecs, &out.CapacitySpecs
		*out = make([]CloudStorageCapacitySpec, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.JournalDeviceSpec != nil {
		in, out := &in.JournalDeviceSpec, &out.JournalDeviceSpec
		*out = new(string)
		**out = **in
	}
	if in.SystemMdDeviceSpec != nil {
		in, out := &in.SystemMdDeviceSpec, &out.SystemMdDeviceSpec
		*out = new(string)
		**out = **in
	}
	if in.KvdbDeviceSpec != nil {
		in, out := &in.KvdbDeviceSpec, &out.KvdbDeviceSpec
		*out = new(string)
		**out = **in
	}
	if in.MaxStorageNodes != nil {
		in, out := &in.MaxStorageNodes, &out.MaxStorageNodes
		*out = new(uint32)
		**out = **in
	}
	if in.MaxStorageNodesPerZone != nil {
		in, out := &in.MaxStorageNodesPerZone, &out.MaxStorageNodesPerZone
		*out = new(uint32)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new CloudStorageSpec.
func (in *CloudStorageSpec) DeepCopy() *CloudStorageSpec {
	if in == nil {
		return nil
	}
	out := new(CloudStorageSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ClusterCondition) DeepCopyInto(out *ClusterCondition) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ClusterCondition.
func (in *ClusterCondition) DeepCopy() *ClusterCondition {
	if in == nil {
		return nil
	}
	out := new(ClusterCondition)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *CommonConfig) DeepCopyInto(out *CommonConfig) {
	*out = *in
	if in.Network != nil {
		in, out := &in.Network, &out.Network
		*out = new(NetworkSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.Storage != nil {
		in, out := &in.Storage, &out.Storage
		*out = new(StorageSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.Env != nil {
		in, out := &in.Env, &out.Env
		*out = make([]v1.EnvVar, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.RuntimeOpts != nil {
		in, out := &in.RuntimeOpts, &out.RuntimeOpts
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new CommonConfig.
func (in *CommonConfig) DeepCopy() *CommonConfig {
	if in == nil {
		return nil
	}
	out := new(CommonConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ComponentImages) DeepCopyInto(out *ComponentImages) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ComponentImages.
func (in *ComponentImages) DeepCopy() *ComponentImages {
	if in == nil {
		return nil
	}
	out := new(ComponentImages)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *DataProviderSpec) DeepCopyInto(out *DataProviderSpec) {
	*out = *in
	if in.Params != nil {
		in, out := &in.Params, &out.Params
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new DataProviderSpec.
func (in *DataProviderSpec) DeepCopy() *DataProviderSpec {
	if in == nil {
		return nil
	}
	out := new(DataProviderSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Geography) DeepCopyInto(out *Geography) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Geography.
func (in *Geography) DeepCopy() *Geography {
	if in == nil {
		return nil
	}
	out := new(Geography)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *KvdbSpec) DeepCopyInto(out *KvdbSpec) {
	*out = *in
	if in.Endpoints != nil {
		in, out := &in.Endpoints, &out.Endpoints
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new KvdbSpec.
func (in *KvdbSpec) DeepCopy() *KvdbSpec {
	if in == nil {
		return nil
	}
	out := new(KvdbSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *MonitoringSpec) DeepCopyInto(out *MonitoringSpec) {
	*out = *in
	if in.EnableMetrics != nil {
		in, out := &in.EnableMetrics, &out.EnableMetrics
		*out = new(bool)
		**out = **in
	}
	if in.Prometheus != nil {
		in, out := &in.Prometheus, &out.Prometheus
		*out = new(PrometheusSpec)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new MonitoringSpec.
func (in *MonitoringSpec) DeepCopy() *MonitoringSpec {
	if in == nil {
		return nil
	}
	out := new(MonitoringSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetworkSpec) DeepCopyInto(out *NetworkSpec) {
	*out = *in
	if in.DataInterface != nil {
		in, out := &in.DataInterface, &out.DataInterface
		*out = new(string)
		**out = **in
	}
	if in.MgmtInterface != nil {
		in, out := &in.MgmtInterface, &out.MgmtInterface
		*out = new(string)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetworkSpec.
func (in *NetworkSpec) DeepCopy() *NetworkSpec {
	if in == nil {
		return nil
	}
	out := new(NetworkSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetworkStatus) DeepCopyInto(out *NetworkStatus) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetworkStatus.
func (in *NetworkStatus) DeepCopy() *NetworkStatus {
	if in == nil {
		return nil
	}
	out := new(NetworkStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NodeCondition) DeepCopyInto(out *NodeCondition) {
	*out = *in
	in.LastTransitionTime.DeepCopyInto(&out.LastTransitionTime)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NodeCondition.
func (in *NodeCondition) DeepCopy() *NodeCondition {
	if in == nil {
		return nil
	}
	out := new(NodeCondition)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NodeSelector) DeepCopyInto(out *NodeSelector) {
	*out = *in
	if in.LabelSelector != nil {
		in, out := &in.LabelSelector, &out.LabelSelector
		*out = new(metav1.LabelSelector)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NodeSelector.
func (in *NodeSelector) DeepCopy() *NodeSelector {
	if in == nil {
		return nil
	}
	out := new(NodeSelector)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NodeSpec) DeepCopyInto(out *NodeSpec) {
	*out = *in
	in.Selector.DeepCopyInto(&out.Selector)
	in.CommonConfig.DeepCopyInto(&out.CommonConfig)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NodeSpec.
func (in *NodeSpec) DeepCopy() *NodeSpec {
	if in == nil {
		return nil
	}
	out := new(NodeSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NodeStatus) DeepCopyInto(out *NodeStatus) {
	*out = *in
	out.Network = in.Network
	in.Storage.DeepCopyInto(&out.Storage)
	out.Geo = in.Geo
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]NodeCondition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NodeStatus.
func (in *NodeStatus) DeepCopy() *NodeStatus {
	if in == nil {
		return nil
	}
	out := new(NodeStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PlacementSpec) DeepCopyInto(out *PlacementSpec) {
	*out = *in
	if in.NodeAffinity != nil {
		in, out := &in.NodeAffinity, &out.NodeAffinity
		*out = new(v1.NodeAffinity)
		(*in).DeepCopyInto(*out)
	}
	if in.Tolerations != nil {
		in, out := &in.Tolerations, &out.Tolerations
		*out = make([]v1.Toleration, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PlacementSpec.
func (in *PlacementSpec) DeepCopy() *PlacementSpec {
	if in == nil {
		return nil
	}
	out := new(PlacementSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PrometheusSpec) DeepCopyInto(out *PrometheusSpec) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PrometheusSpec.
func (in *PrometheusSpec) DeepCopy() *PrometheusSpec {
	if in == nil {
		return nil
	}
	out := new(PrometheusSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *RollingUpdateStorageCluster) DeepCopyInto(out *RollingUpdateStorageCluster) {
	*out = *in
	if in.MaxUnavailable != nil {
		in, out := &in.MaxUnavailable, &out.MaxUnavailable
		*out = new(intstr.IntOrString)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new RollingUpdateStorageCluster.
func (in *RollingUpdateStorageCluster) DeepCopy() *RollingUpdateStorageCluster {
	if in == nil {
		return nil
	}
	out := new(RollingUpdateStorageCluster)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SecuritySpec) DeepCopyInto(out *SecuritySpec) {
	*out = *in
	if in.Auth != nil {
		in, out := &in.Auth, &out.Auth
		*out = new(AuthSpec)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SecuritySpec.
func (in *SecuritySpec) DeepCopy() *SecuritySpec {
	if in == nil {
		return nil
	}
	out := new(SecuritySpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *SelfSignedSpec) DeepCopyInto(out *SelfSignedSpec) {
	*out = *in
	if in.Issuer != nil {
		in, out := &in.Issuer, &out.Issuer
		*out = new(string)
		**out = **in
	}
	if in.TokenLifetime != nil {
		in, out := &in.TokenLifetime, &out.TokenLifetime
		*out = new(metav1.Duration)
		**out = **in
	}
	if in.SharedSecret != nil {
		in, out := &in.SharedSecret, &out.SharedSecret
		*out = new(string)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new SelfSignedSpec.
func (in *SelfSignedSpec) DeepCopy() *SelfSignedSpec {
	if in == nil {
		return nil
	}
	out := new(SelfSignedSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *Storage) DeepCopyInto(out *Storage) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new Storage.
func (in *Storage) DeepCopy() *Storage {
	if in == nil {
		return nil
	}
	out := new(Storage)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageCluster) DeepCopyInto(out *StorageCluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageCluster.
func (in *StorageCluster) DeepCopy() *StorageCluster {
	if in == nil {
		return nil
	}
	out := new(StorageCluster)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *StorageCluster) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageClusterDeleteStrategy) DeepCopyInto(out *StorageClusterDeleteStrategy) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageClusterDeleteStrategy.
func (in *StorageClusterDeleteStrategy) DeepCopy() *StorageClusterDeleteStrategy {
	if in == nil {
		return nil
	}
	out := new(StorageClusterDeleteStrategy)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageClusterList) DeepCopyInto(out *StorageClusterList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]StorageCluster, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageClusterList.
func (in *StorageClusterList) DeepCopy() *StorageClusterList {
	if in == nil {
		return nil
	}
	out := new(StorageClusterList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *StorageClusterList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageClusterSpec) DeepCopyInto(out *StorageClusterSpec) {
	*out = *in
	in.UpdateStrategy.DeepCopyInto(&out.UpdateStrategy)
	if in.DeleteStrategy != nil {
		in, out := &in.DeleteStrategy, &out.DeleteStrategy
		*out = new(StorageClusterDeleteStrategy)
		**out = **in
	}
	if in.AutoUpdateComponents != nil {
		in, out := &in.AutoUpdateComponents, &out.AutoUpdateComponents
		*out = new(AutoUpdateComponentStrategyType)
		**out = **in
	}
	if in.RevisionHistoryLimit != nil {
		in, out := &in.RevisionHistoryLimit, &out.RevisionHistoryLimit
		*out = new(int32)
		**out = **in
	}
	if in.Placement != nil {
		in, out := &in.Placement, &out.Placement
		*out = new(PlacementSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.ImagePullSecret != nil {
		in, out := &in.ImagePullSecret, &out.ImagePullSecret
		*out = new(string)
		**out = **in
	}
	if in.Kvdb != nil {
		in, out := &in.Kvdb, &out.Kvdb
		*out = new(KvdbSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.CloudStorage != nil {
		in, out := &in.CloudStorage, &out.CloudStorage
		*out = new(CloudStorageSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.SecretsProvider != nil {
		in, out := &in.SecretsProvider, &out.SecretsProvider
		*out = new(string)
		**out = **in
	}
	if in.StartPort != nil {
		in, out := &in.StartPort, &out.StartPort
		*out = new(uint32)
		**out = **in
	}
	if in.FeatureGates != nil {
		in, out := &in.FeatureGates, &out.FeatureGates
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	in.CommonConfig.DeepCopyInto(&out.CommonConfig)
	if in.UserInterface != nil {
		in, out := &in.UserInterface, &out.UserInterface
		*out = new(UserInterfaceSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.Stork != nil {
		in, out := &in.Stork, &out.Stork
		*out = new(StorkSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.Autopilot != nil {
		in, out := &in.Autopilot, &out.Autopilot
		*out = new(AutopilotSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.Monitoring != nil {
		in, out := &in.Monitoring, &out.Monitoring
		*out = new(MonitoringSpec)
		(*in).DeepCopyInto(*out)
	}
	if in.Nodes != nil {
		in, out := &in.Nodes, &out.Nodes
		*out = make([]NodeSpec, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Security != nil {
		in, out := &in.Security, &out.Security
		*out = new(SecuritySpec)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageClusterSpec.
func (in *StorageClusterSpec) DeepCopy() *StorageClusterSpec {
	if in == nil {
		return nil
	}
	out := new(StorageClusterSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageClusterStatus) DeepCopyInto(out *StorageClusterStatus) {
	*out = *in
	if in.CollisionCount != nil {
		in, out := &in.CollisionCount, &out.CollisionCount
		*out = new(int32)
		**out = **in
	}
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]ClusterCondition, len(*in))
		copy(*out, *in)
	}
	out.Storage = in.Storage
	if in.DesiredImages != nil {
		in, out := &in.DesiredImages, &out.DesiredImages
		*out = new(ComponentImages)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageClusterStatus.
func (in *StorageClusterStatus) DeepCopy() *StorageClusterStatus {
	if in == nil {
		return nil
	}
	out := new(StorageClusterStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageClusterUpdateStrategy) DeepCopyInto(out *StorageClusterUpdateStrategy) {
	*out = *in
	if in.RollingUpdate != nil {
		in, out := &in.RollingUpdate, &out.RollingUpdate
		*out = new(RollingUpdateStorageCluster)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageClusterUpdateStrategy.
func (in *StorageClusterUpdateStrategy) DeepCopy() *StorageClusterUpdateStrategy {
	if in == nil {
		return nil
	}
	out := new(StorageClusterUpdateStrategy)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageNode) DeepCopyInto(out *StorageNode) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageNode.
func (in *StorageNode) DeepCopy() *StorageNode {
	if in == nil {
		return nil
	}
	out := new(StorageNode)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *StorageNode) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageNodeCloudDriveConfig) DeepCopyInto(out *StorageNodeCloudDriveConfig) {
	*out = *in
	if in.Options != nil {
		in, out := &in.Options, &out.Options
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageNodeCloudDriveConfig.
func (in *StorageNodeCloudDriveConfig) DeepCopy() *StorageNodeCloudDriveConfig {
	if in == nil {
		return nil
	}
	out := new(StorageNodeCloudDriveConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageNodeCloudDriveConfigs) DeepCopyInto(out *StorageNodeCloudDriveConfigs) {
	*out = *in
	if in.DriveConfigs != nil {
		in, out := &in.DriveConfigs, &out.DriveConfigs
		*out = make([]StorageNodeCloudDriveConfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageNodeCloudDriveConfigs.
func (in *StorageNodeCloudDriveConfigs) DeepCopy() *StorageNodeCloudDriveConfigs {
	if in == nil {
		return nil
	}
	out := new(StorageNodeCloudDriveConfigs)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageNodeList) DeepCopyInto(out *StorageNodeList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]StorageNode, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageNodeList.
func (in *StorageNodeList) DeepCopy() *StorageNodeList {
	if in == nil {
		return nil
	}
	out := new(StorageNodeList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *StorageNodeList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageNodeSpec) DeepCopyInto(out *StorageNodeSpec) {
	*out = *in
	in.CloudStorage.DeepCopyInto(&out.CloudStorage)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageNodeSpec.
func (in *StorageNodeSpec) DeepCopy() *StorageNodeSpec {
	if in == nil {
		return nil
	}
	out := new(StorageNodeSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageSpec) DeepCopyInto(out *StorageSpec) {
	*out = *in
	if in.UseAll != nil {
		in, out := &in.UseAll, &out.UseAll
		*out = new(bool)
		**out = **in
	}
	if in.UseAllWithPartitions != nil {
		in, out := &in.UseAllWithPartitions, &out.UseAllWithPartitions
		*out = new(bool)
		**out = **in
	}
	if in.ForceUseDisks != nil {
		in, out := &in.ForceUseDisks, &out.ForceUseDisks
		*out = new(bool)
		**out = **in
	}
	if in.Devices != nil {
		in, out := &in.Devices, &out.Devices
		*out = new([]string)
		if **in != nil {
			in, out := *in, *out
			*out = make([]string, len(*in))
			copy(*out, *in)
		}
	}
	if in.JournalDevice != nil {
		in, out := &in.JournalDevice, &out.JournalDevice
		*out = new(string)
		**out = **in
	}
	if in.SystemMdDevice != nil {
		in, out := &in.SystemMdDevice, &out.SystemMdDevice
		*out = new(string)
		**out = **in
	}
	if in.KvdbDevice != nil {
		in, out := &in.KvdbDevice, &out.KvdbDevice
		*out = new(string)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageSpec.
func (in *StorageSpec) DeepCopy() *StorageSpec {
	if in == nil {
		return nil
	}
	out := new(StorageSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageStatus) DeepCopyInto(out *StorageStatus) {
	*out = *in
	out.TotalSize = in.TotalSize.DeepCopy()
	out.UsedSize = in.UsedSize.DeepCopy()
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageStatus.
func (in *StorageStatus) DeepCopy() *StorageStatus {
	if in == nil {
		return nil
	}
	out := new(StorageStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorkSpec) DeepCopyInto(out *StorkSpec) {
	*out = *in
	if in.Args != nil {
		in, out := &in.Args, &out.Args
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	if in.Env != nil {
		in, out := &in.Env, &out.Env
		*out = make([]v1.EnvVar, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.HostNetwork != nil {
		in, out := &in.HostNetwork, &out.HostNetwork
		*out = new(bool)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorkSpec.
func (in *StorkSpec) DeepCopy() *StorkSpec {
	if in == nil {
		return nil
	}
	out := new(StorkSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *UserInterfaceSpec) DeepCopyInto(out *UserInterfaceSpec) {
	*out = *in
	if in.Env != nil {
		in, out := &in.Env, &out.Env
		*out = make([]v1.EnvVar, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new UserInterfaceSpec.
func (in *UserInterfaceSpec) DeepCopy() *UserInterfaceSpec {
	if in == nil {
		return nil
	}
	out := new(UserInterfaceSpec)
	in.DeepCopyInto(out)
	return out
}
