package storagecluster

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	coreops "github.com/portworx/sched-ops/k8s/core"
	kubevirt "github.com/portworx/sched-ops/k8s/kubevirt-dynamic"
)

type KubevirtManager interface {
	// ClusterHasVMPods returns true if the cluster has any KubeVirt VM Pods (running or not)
	ClusterHasVMPods() (bool, error)

	// GetVMPodsToEvictByNode returns a map of node name to a list of virt-launcher pods that need to be evicted
	GetVMPodsToEvictByNode(wantNodes map[string]bool) (map[string][]*util.VMPodEviction, error)

	// StartEvictingVMPods starts live-migrating the virt-launcher pods to other nodes
	StartEvictingVMPods(virtLauncherPods []*util.VMPodEviction, controllerRevisionHash string,
		failedToEvictVMEventFunc func(message string))
}

type kubevirtManagerImpl struct {
	coreOps     coreops.Ops
	kubevirtOps kubevirt.Ops
}

func newKubevirtManager() KubevirtManager {
	return &kubevirtManagerImpl{
		coreOps:     coreops.Instance(),
		kubevirtOps: kubevirt.Instance(),
	}
}

func newKubevirtManagerForTesting(coreOps coreops.Ops, kubevirtOps kubevirt.Ops) KubevirtManager {
	return &kubevirtManagerImpl{
		kubevirtOps: kubevirtOps,
		coreOps:     coreOps,
	}
}

func (k *kubevirtManagerImpl) ClusterHasVMPods() (bool, error) {
	virtLauncherPods, err := k.getVirtLauncherPods()
	if err != nil {
		return false, err
	}
	return len(virtLauncherPods) > 0, nil
}

func (k *kubevirtManagerImpl) GetVMPodsToEvictByNode(wantNodes map[string]bool) (map[string][]*util.VMPodEviction, error) {
	virtLauncherPodsByNode := map[string][]*util.VMPodEviction{}
	// get a list of virt-launcher pods for each node
	virtLauncherPods, err := k.getVirtLauncherPods()
	if err != nil {
		return nil, err
	}
	for _, pod := range virtLauncherPods {
		if !wantNodes[pod.Spec.NodeName] {
			continue
		}
		shouldEvict, migrInProgress, err := k.shouldLiveMigrateVM(&pod)
		if err != nil {
			return nil, err
		}
		if shouldEvict {
			virtLauncherPodsByNode[pod.Spec.NodeName] = append(
				virtLauncherPodsByNode[pod.Spec.NodeName],
				&util.VMPodEviction{
					PodToEvict:              pod,
					LiveMigrationInProgress: migrInProgress,
				},
			)
		}
	}
	return virtLauncherPodsByNode, nil
}

func (k *kubevirtManagerImpl) StartEvictingVMPods(
	evictions []*util.VMPodEviction, controllerRevisionHash string, failedToEvictVMEventFunc func(message string),
) {
	ctx := context.TODO()
OUTER:
	for _, eviction := range evictions {
		pod := &eviction.PodToEvict
		if eviction.LiveMigrationInProgress {
			// Wait until the next Reconcile() cycle to check if the live-migration is completed.
			logrus.Infof("Skipping eviction of virt-launcher pod %s/%s until the next reconcile cycle",
				pod.Namespace, pod.Name)
			continue
		}
		vmiName := k.getVMIName(pod)
		if vmiName == "" {
			// vmName should not be empty. Don't pause upgrade for such badly formed pods.
			logrus.Warnf("Failed to get VMI name for virt-launcher pod %s/%s", pod.Namespace, pod.Name)
			continue
		}
		migrations, err := k.getVMIMigrations(pod.Namespace, vmiName)
		if err != nil {
			logrus.Warnf("Cannot evict pod %s/%s: %v", pod.Namespace, pod.Name, err)
			continue
		}
		for _, migration := range migrations {
			if !migration.Completed {
				logrus.Infof("VM live-migration %s/%s is in progress (%s) for VM %s",
					pod.Namespace, migration.Name, migration.Phase, vmiName)
				continue OUTER
			}
			if migration.Annotations[constants.AnnotationVMIMigrationSourceNode] == pod.Spec.NodeName &&
				migration.Annotations[constants.AnnotationControllerRevisionHashKey] == controllerRevisionHash {

				if migration.Failed {
					msg := fmt.Sprintf("Live migration %s failed for VM %s/%s on node %s. "+
						"Stop or migrate the VM so that the update of the storage node can proceed.",
						migration.Name, pod.Namespace, vmiName, pod.Spec.NodeName)
					logrus.Warnf(msg)
					failedToEvictVMEventFunc(msg)
				} else {
					// We should not have to evict the same VM twice in the same upgrade. That probably means
					// something went wrong elsewhere. Let's avoid creating too many live-migrations unnecessarily.
					msg := fmt.Sprintf("Live migration %s has already succeeded for VM %s/%s on node %s. "+
						"But the VM pod %s is still running. Stop or migrate the VM if it is still running node %s.",
						migration.Name, pod.Namespace, vmiName, pod.Spec.NodeName, pod.Name, pod.Spec.NodeName)
					logrus.Warnf(msg)
					failedToEvictVMEventFunc(msg)
				}
				continue OUTER
			}
		}
		// Check if the VMI is still pointing to the same node as the virt-launcher pod. We already checked for this
		// in shouldLiveMigrateVM() but we need to check again here because the VMI could have been live-migrated in
		// the meantime. This reduces the chance of unnecessary live-migrations but does not close the hole fully.
		vmi, err := k.kubevirtOps.GetVirtualMachineInstance(ctx, pod.Namespace, vmiName)
		if err != nil {
			logrus.Warnf("Failed to get VMI %s when evicting pod %s/%s: %v", vmiName, pod.Namespace, pod.Name, err)
			continue
		}
		if vmi.NodeName != pod.Spec.NodeName {
			logrus.Infof("VMI %s/%s is running on node %s, not on node %s. Eviction not needed for pod %s.",
				pod.Namespace, vmiName, vmi.NodeName, pod.Spec.NodeName, pod.Name)
			continue
		}
		// All checks passed. Start the live-migration.
		labels := map[string]string{
			constants.OperatorLabelManagedByKey: constants.OperatorLabelManagedByValue,
		}
		annotations := map[string]string{
			constants.AnnotationControllerRevisionHashKey: controllerRevisionHash,
			constants.AnnotationVMIMigrationSourceNode:    pod.Spec.NodeName,
		}
		logrus.Infof("Starting live-migration of VM %s/%s to evict the virt-launcher pod %s from node %s",
			pod.Namespace, vmiName, pod.Name, pod.Spec.NodeName)
		_, err = k.kubevirtOps.CreateVirtualMachineInstanceMigrationWithParams(
			ctx, pod.Namespace, vmiName, "", "", annotations, labels)
		if err != nil {
			logrus.Warnf("Failed to start live migration of VM %s/%s: %v", pod.Namespace, vmiName, err)
		}
	}
}

func (k *kubevirtManagerImpl) getVMIMigrations(
	vmiNamespace, vmiName string,
) ([]*kubevirt.VirtualMachineInstanceMigration, error) {

	var ret []*kubevirt.VirtualMachineInstanceMigration
	migrations, err := k.kubevirtOps.ListVirtualMachineInstanceMigrations(
		context.TODO(), vmiNamespace, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list VM live-migrations in namespace %s: %w", vmiNamespace, err)
	}
	for _, migration := range migrations {
		if migration.VMIName == vmiName {
			ret = append(ret, migration)
		}
	}
	return ret, nil
}

func (k *kubevirtManagerImpl) shouldLiveMigrateVM(virtLauncherPod *v1.Pod) (bool, bool, error) {
	// we only care about the pods that are not in a terminal state
	if virtLauncherPod.Status.Phase == v1.PodSucceeded || virtLauncherPod.Status.Phase == v1.PodFailed {
		return false, false, nil
	}
	vmiName := k.getVMIName(virtLauncherPod)
	if vmiName == "" {
		logrus.Warnf("Failed to get VMI name for virt-launcher pod %s/%s. Skipping live-migration.",
			virtLauncherPod.Namespace, virtLauncherPod.Name)
		return false, false, nil
	}
	migrations, err := k.getVMIMigrations(virtLauncherPod.Namespace, vmiName)
	if err != nil {
		return false, false, err
	}
	for _, migration := range migrations {
		if !migration.Completed {
			// We already checked that the virt-launcher pod is in not in a terminal state.
			// There is a live-migration in progress for the VMI.
			// Wait for the live-migration to finish before determining if we need to evict this pod.
			// Return "shouldEvict=true" and deal with it later.
			logrus.Infof("Will check whether to evict pod %s/%s after the live-migration %s (%s) is completed.",
				virtLauncherPod.Namespace, virtLauncherPod.Name, migration.Name, migration.Phase)
			return true, true, nil
		}
	}
	// get VMI to check if the VM is live-migratable and if it is running on the same node as the virt-launcher pod
	vmi, err := k.kubevirtOps.GetVirtualMachineInstance(context.TODO(), virtLauncherPod.Namespace, vmiName)
	if err != nil {
		if !errors.IsNotFound(err) {
			return false, false, fmt.Errorf("failed to get VMI %s/%s: %w", virtLauncherPod.Namespace, vmiName, err)
		}
		logrus.Warnf("VMI %s/%s was not found; skipping live-migration: %v", virtLauncherPod.Namespace, vmiName, err)
		return false, false, nil
	}
	// We already checked that there is no live migration in progress for this VMI.
	// Ignore this pod if VMI says that the VM is running on another node. This can happen if
	// the live migration that we started in the previous Reconcile() has completed but the source pod is still in
	// the Running phase. We don't need to evict this pod, so don't start another live-migration unnecessarily.
	if vmi.NodeName != virtLauncherPod.Spec.NodeName {
		logrus.Infof("VMI %s/%s is running on node %s, not on node %s. Skipping eviction of pod %s.",
			virtLauncherPod.Namespace, vmiName, vmi.NodeName, virtLauncherPod.Spec.NodeName, virtLauncherPod.Name)
		return false, false, nil
	}
	// Ignore the VMs that are not live-migratable.
	return vmi.LiveMigratable, false, nil
}

func (k *kubevirtManagerImpl) getVirtLauncherPods() ([]v1.Pod, error) {
	virtLauncherPods, err := k.coreOps.ListPods(map[string]string{"kubevirt.io": "virt-launcher"})
	if err != nil {
		return nil, fmt.Errorf("failed to list virt-launcher pods: %w", err)
	}
	if virtLauncherPods == nil {
		return nil, nil
	}
	return virtLauncherPods.Items, nil
}

func (k *kubevirtManagerImpl) getVMIName(virtLauncherPod *v1.Pod) string {
	for _, ownerReference := range virtLauncherPod.OwnerReferences {
		if ownerReference.Kind == "VirtualMachineInstance" {
			return ownerReference.Name
		}
	}
	return ""
}
