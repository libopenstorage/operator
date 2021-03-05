package k8s

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/apis/core/v1/helper"
)

// AddOrUpdateStoragePodTolerations adds tolerations to the given pod spec that are required for running storage pods
// as they need to tolerate built-in taints in the system
// TODO: make the storage cluster pod a critical pod to guarantee scheduling
func AddOrUpdateStoragePodTolerations(podSpec *v1.PodSpec) {
	// StorageCluster pods shouldn't be deleted by NodeController in case of node problems.
	// Add infinite toleration for taint notReady:NoExecute here to survive taint-based
	// eviction enforced by NodeController when node turns not ready.
	helper.AddOrUpdateTolerationInPodSpec(podSpec, &v1.Toleration{
		Key:      v1.TaintNodeNotReady,
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoExecute,
	})

	// StorageCluster pods shouldn't be deleted by NodeController in case of node problems.
	// Add infinite toleration for taint unreachable:NoExecute here to survive taint-based
	// eviction enforced by NodeController when node turns unreachable.
	helper.AddOrUpdateTolerationInPodSpec(podSpec, &v1.Toleration{
		Key:      v1.TaintNodeUnreachable,
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoExecute,
	})

	// All StorageCluster pods should tolerate MemoryPressure, DiskPressure, Unschedulable
	// and NetworkUnavailable and OutOfDisk taints.
	helper.AddOrUpdateTolerationInPodSpec(podSpec, &v1.Toleration{
		Key:      v1.TaintNodeDiskPressure,
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoSchedule,
	})

	helper.AddOrUpdateTolerationInPodSpec(podSpec, &v1.Toleration{
		Key:      v1.TaintNodeMemoryPressure,
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoSchedule,
	})

	helper.AddOrUpdateTolerationInPodSpec(podSpec, &v1.Toleration{
		Key:      v1.TaintNodePIDPressure,
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoSchedule,
	})

	helper.AddOrUpdateTolerationInPodSpec(podSpec, &v1.Toleration{
		Key:      v1.TaintNodeUnschedulable,
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoSchedule,
	})

	helper.AddOrUpdateTolerationInPodSpec(podSpec, &v1.Toleration{
		Key:      v1.TaintNodeNetworkUnavailable,
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoSchedule,
	})
}
