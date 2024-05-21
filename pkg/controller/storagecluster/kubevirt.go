package storagecluster

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/libopenstorage/operator/pkg/constants"
	coreops "github.com/portworx/sched-ops/k8s/core"
	kubevirt "github.com/portworx/sched-ops/k8s/kubevirt-dynamic"
)

type KubevirtManager interface {
	// ClusterHasVMPods returns true if the cluster has any KubeVirt VM Pods (running or not)
	ClusterHasVMPods() (bool, error)

	// GetVMPodsToEvictByNode returns a map of node name to a list of virt-launcher pods that are live-migratable
	GetVMPodsToEvictByNode() (map[string][]v1.Pod, error)

	// StartEvictingVMPods starts live-migrating the virt-launcher pods to other nodes
	StartEvictingVMPods(virtLauncherPods []v1.Pod, controllerRevisionHash string,
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

func (k *kubevirtManagerImpl) GetVMPodsToEvictByNode() (map[string][]v1.Pod, error) {
	virtLauncherPodsByNode := map[string][]v1.Pod{}
	// get a list of virt-launcher pods for each node
	virtLauncherPods, err := k.getVirtLauncherPods()
	if err != nil {
		return nil, err
	}
	for _, pod := range virtLauncherPods {
		shouldEvict, err := k.shouldLiveMigrateVM(&pod)
		if err != nil {
			return nil, err
		}
		if shouldEvict {
			virtLauncherPodsByNode[pod.Spec.NodeName] = append(virtLauncherPodsByNode[pod.Spec.NodeName], pod)
		}
	}
	return virtLauncherPodsByNode, nil
}

func (k *kubevirtManagerImpl) StartEvictingVMPods(
	virtLauncherPods []v1.Pod, controllerRevisionHash string, failedToEvictVMEventFunc func(message string),
) {
	ctx := context.TODO()
OUTER:
	for _, pod := range virtLauncherPods {
		vmiName := k.getVMIName(&pod)
		if vmiName == "" {
			// vmName should not be empty. Don't pause upgrade for such badly formed pods.
			logrus.Warnf("Failed to get VMI name for virt-launcher pod %s/%s", pod.Namespace, pod.Name)
			continue
		}
		migrations, err := k.kubevirtOps.ListVirtualMachineInstanceMigrations(ctx, pod.Namespace, metav1.ListOptions{})
		if err != nil {
			logrus.Warnf("Failed to list VM live-migrations in namespace %s: %v", pod.Namespace, err)
			continue
		}
		for _, migration := range migrations {
			if migration.VMIName == vmiName {
				if !migration.Completed {
					logrus.Infof("VM live-migration %s/%s is in progress (%s) for VM %s",
						pod.Namespace, migration.Name, migration.Phase, vmiName)
					continue OUTER
				}
				if migration.Failed &&
					migration.Annotations[constants.AnnotationVMIMigrationSourceNode] == pod.Spec.NodeName &&
					migration.Annotations[constants.AnnotationControllerRevisionHashKey] == controllerRevisionHash {

					msg := fmt.Sprintf("Live migration %s failed for VM %s/%s on node %s. "+
						"Stop or migrate the VM so that the update of the storage node can proceed.",
						migration.Name, pod.Namespace, vmiName, pod.Spec.NodeName)
					logrus.Warnf(msg)
					failedToEvictVMEventFunc(msg)
					continue OUTER
				}
			}
		}
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

func (k *kubevirtManagerImpl) shouldLiveMigrateVM(virtLauncherPod *v1.Pod) (bool, error) {
	// we only care about the pods that are not in a terminal state
	if virtLauncherPod.Status.Phase == v1.PodSucceeded || virtLauncherPod.Status.Phase == v1.PodFailed {
		return false, nil
	}
	// ignore the VMs that are not live-migratable
	vmiName := k.getVMIName(virtLauncherPod)
	if vmiName == "" {
		logrus.Warnf("Failed to get VMI name for virt-launcher pod %s/%s. Skipping live-migration.",
			virtLauncherPod.Namespace, virtLauncherPod.Name)
		return false, nil
	}
	vmi, err := k.kubevirtOps.GetVirtualMachineInstance(context.TODO(), virtLauncherPod.Namespace, vmiName)
	if err != nil {
		if !errors.IsNotFound(err) {
			return false, fmt.Errorf("failed to get VMI %s/%s: %w", virtLauncherPod.Namespace, vmiName, err)
		}
		logrus.Warnf("VMI %s/%s was not found; skipping live-migration: %v", virtLauncherPod.Namespace, vmiName, err)
		return false, nil
	}
	return vmi.LiveMigratable, nil
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
