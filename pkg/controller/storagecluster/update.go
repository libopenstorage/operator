/*
Copyright 2015 The Kubernetes Authors.
Modifications Copyright 2019 The Libopenstorage Authors.

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

package storagecluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storageapi "github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	operatorutil "github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	k8sNodeUpdateAttempts = 5
)

// rollingUpdate deletes old storage cluster pods making sure that no more than
// cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable pods are unavailable
func (c *Controller) rollingUpdate(cluster *corev1.StorageCluster, hash string, nodeList *v1.NodeList) error {
	logrus.Debug("Perform rolling update")
	nodeToStoragePods, err := c.getNodeToStoragePods(cluster)
	if err != nil {
		return fmt.Errorf("couldn't get node to storage pod mapping for storage cluster %v: %v",
			cluster.Name, err)
	}
	_, oldPods := c.groupStorageClusterPods(cluster, nodeToStoragePods, hash)

	var storageNodeList []*storageapi.StorageNode
	if storagePodsEnabled(cluster) {
		storageNodeList, err = c.Driver.GetStorageNodes(cluster)
		if err != nil {
			return fmt.Errorf("couldn't get list of storage nodes during rolling "+
				"update of storage cluster %s/%s: %v", cluster.Namespace, cluster.Name, err)
		}
	}

	maxUnavailable, unavailableNodes, availablePods, err := c.getPodAvailability(cluster, nodeToStoragePods, storageNodeList)
	if err != nil {
		return fmt.Errorf("couldn't get unavailable numbers: %v", err)
	}
	numUnavailable := len(unavailableNodes)
	oldPodsMap := map[string]*v1.Pod{}
	for _, pod := range oldPods {
		oldPodsMap[pod.Name] = pod
	}
	// Remove the unschedulable label on node after PX has been upgraded on the node
	for _, pods := range nodeToStoragePods {
		for _, pod := range pods {
			if _, ok := oldPodsMap[pod.Name]; ok {
				continue
			}
			if _, ok := availablePods[pod.Name]; ok {
				if err := c.removeNodeUnschedulableLabel(pod.Spec.NodeName); err != nil {
					return err
				}
			}
		}
	}

	oldAvailablePods, oldUnavailablePods := splitByAvailablePods(oldPods)
	// for oldPods delete all not running pods
	var oldPodsToDelete []string
	logrus.Debugf("Marking all unavailable old pods for deletion")
	for _, pod := range oldUnavailablePods {
		// Skip terminating pods. We won't delete them again
		if pod.DeletionTimestamp != nil {
			continue
		}
		logrus.Infof("Marking pod %s/%s for deletion", cluster.Name, pod.Name)
		oldPodsToDelete = append(oldPodsToDelete, pod.Name)
	}

	// TODO: Max unavailable kvdb nodes has been set to 1 assuming default setup of 3 KVDB nodes.
	// Need to generalise code for 5 KVDB nodes

	// get the number of kvdb members which are unavailable in case of internal kvdb
	numUnavailableKvdb := -1
	var kvdbNodes map[string]bool
	if !util.IsFreshInstall(cluster) {
		numUnavailableKvdb, kvdbNodes, err = c.getKVDBNodeAvailability(cluster)
		if err != nil {
			return err
		}
	}

	nodesMarkedUnschedulable := map[string]bool{}
	for _, node := range nodeList.Items {
		if nodeMarkedUnschedulable(&node) {
			nodesMarkedUnschedulable[node.Name] = true
		}
	}
	// Non disruptive upgrade of nodes only if update strategy is rolling and if allow disruption is false
	if cluster.Spec.UpdateStrategy.RollingUpdate == nil || cluster.Spec.UpdateStrategy.RollingUpdate.Disruption == nil || !*cluster.Spec.UpdateStrategy.RollingUpdate.Disruption.Allow {
		oldAvailablePods, err = c.parallelUpgradeNodesList(cluster, oldAvailablePods, unavailableNodes, storageNodeList)
		if err != nil {
			return err
		}
	}

	logrus.Debugf("Marking old pods for deletion")
	// sort the pods such that the pods on the nodes that we marked unschedulable are deleted first
	// Otherwise, we will end up marking too many nodes as unschedulable (or ping-ponging VMs between the nodes).
	sort.Slice(oldAvailablePods, func(i, j int) bool {
		iUnschedulable := nodesMarkedUnschedulable[oldAvailablePods[i].Spec.NodeName]
		jUnschedulable := nodesMarkedUnschedulable[oldAvailablePods[j].Spec.NodeName]

		return iUnschedulable && !jUnschedulable
	})
	for _, pod := range oldAvailablePods {
		if numUnavailable >= maxUnavailable {
			logrus.Infof("Number of unavailable StorageCluster pods: %d, is equal "+
				"to or exceeds allowed maximum: %d", numUnavailable, maxUnavailable)
			break
		}

		// check if pod is running in a node which has internal kvdb running in it
		// numUnavailableKvdb will be 0 or more when it is not a fresh install. We want to skip this for fresh install
		if _, isKvdbNode := kvdbNodes[pod.Spec.NodeName]; numUnavailableKvdb >= 0 && cluster.Spec.Kvdb != nil && cluster.Spec.Kvdb.Internal && isKvdbNode {
			// if number of unavailable kvdb nodes is greater than or equal to 1, or lesser than 3 entries are present in the kvdb map
			// then dont restart portworx on this node
			if numUnavailableKvdb > 0 || len(kvdbNodes) < 3 {
				logrus.Infof("One or more KVDB members are down, temporarily skipping update for this node to prevent KVDB from going out of quorum ")
				continue
			} else {
				numUnavailableKvdb++
			}
		}
		logrus.Infof("Marking pod %s/%s for deletion", cluster.Name, pod.Name)
		oldPodsToDelete = append(oldPodsToDelete, pod.Name)
		numUnavailable++
	}

	if len(oldPodsToDelete) > 0 && !forceContinueUpgrade(cluster) {
		isUpgrading, err := k8s.IsClusterBeingUpgraded(c.client)
		if err != nil {
			logrus.Warnf("Failed to detect Kubernetes cluster upgrade status. %v", err)
		} else if isUpgrading {
			k8s.InfoEvent(c.recorder, cluster, operatorutil.UpdatePausedReason,
				"The rolling update of the storage cluster has been paused due to an ongoing OpenShift upgrade. "+
					"Storage nodes will only be updated on pod deletion or after the OpenShift upgrade is completed.")
			return nil
		}
	}

	// check if we should live-migrate VMs before updating the storage node
	virtLauncherPodsByNode := map[string][]*operatorutil.VMPodEviction{}
	if len(oldPodsToDelete) > 0 && !forceContinueUpgrade(cluster) && evictVMsDuringUpdate(cluster) {
		vmPodsPresent, err := c.kubevirt.ClusterHasVMPods()
		if err != nil {
			return err
		}
		if vmPodsPresent {
			// add unschedulable label to the nodes that have pods to be deleted so that
			// stork does not schedule any new virt-launcher pods on them
			evictionNodes := map[string]bool{}
			for _, podName := range oldPodsToDelete {
				pod := oldPodsMap[podName]
				if pod == nil {
					// This should never happen. Log and continue.
					logrus.Warnf("Pod %s not found in oldPodsMap; cannot add unschedulable label to the node", podName)
					continue
				}
				if pod.Spec.NodeName == "" {
					logrus.Warnf("Pod %s has no node name; cannot add unschedulable label to the node", podName)
					continue
				}
				if err := c.addNodeUnschedulableAnnotation(pod.Spec.NodeName); err != nil {
					return err
				}
				evictionNodes[pod.Spec.NodeName] = true
			}
			// get the VM pods after labeling the nodes since the list may have changed
			virtLauncherPodsByNode, err = c.kubevirt.GetVMPodsToEvictByNode(evictionNodes)
			if err != nil {
				return err
			}
		}
	}

	var oldPodsToDeletePruned []string
	for _, podName := range oldPodsToDelete {
		pod := oldPodsMap[podName]
		if pod == nil {
			// This should never happen. Log and continue.
			logrus.Warnf("Pod %s not found in oldPodsMap; cannot check for VMs", podName)
			oldPodsToDeletePruned = append(oldPodsToDeletePruned, podName)
			continue
		}
		if len(virtLauncherPodsByNode[pod.Spec.NodeName]) == 0 {
			oldPodsToDeletePruned = append(oldPodsToDeletePruned, podName)
			continue
		}
		msg := fmt.Sprintf(
			"The update of the storage node %s has been paused because there are %d KubeVirt VMs running on the node. "+
				"Portworx will live-migrate the VMs and the storage node will be updated after "+
				"there are no VMs left on this node.",
			pod.Spec.NodeName, len(virtLauncherPodsByNode[pod.Spec.NodeName]))
		logrus.Warnf(msg)

		// raise event on StorageNode object if found, else on StorageCluster. Problem with raising event
		// on StorageCluster is that the events for different nodes get deduped. So, we don't see all events.
		var eventObj runtime.Object
		node, err := operatorops.Instance().GetStorageNode(pod.Spec.NodeName, cluster.Namespace)
		if err != nil {
			logrus.Warnf("Failed to get storage node %s/%s; raising event on storageCluster: %v",
				cluster.Namespace, pod.Spec.NodeName, err)
			eventObj = cluster
		} else {
			eventObj = node
		}
		k8s.WarningEvent(c.recorder, eventObj, operatorutil.UpdatePausedReason, msg)

		failedToEvictVMEventFunc := func(msg string) {
			k8s.WarningEvent(c.recorder, eventObj, operatorutil.FailedToEvictVM, msg)
		}
		c.kubevirt.StartEvictingVMPods(virtLauncherPodsByNode[pod.Spec.NodeName], hash, failedToEvictVMEventFunc)
	}

	return c.syncNodes(cluster, oldPodsToDeletePruned, []string{}, hash)
}

func (c *Controller) addNodeUnschedulableAnnotation(nodeName string) error {
	return nodeUnschedulableAnnotationHelperWithRetries(c.client, nodeName, true)
}

func (c *Controller) removeNodeUnschedulableLabel(nodeName string) error {
	return nodeUnschedulableAnnotationHelperWithRetries(c.client, nodeName, false)
}

func nodeUnschedulableAnnotationHelperWithRetries(
	k8sClient client.Client, nodeName string, unschedulable bool,
) error {
	var err error
	for i := 0; i < k8sNodeUpdateAttempts; i++ {
		err = nodeUnschedulableAnnotationHelper(k8sClient, nodeName, unschedulable)
		if err == nil || !errors.IsConflict(err) {
			return err
		}
		logrus.Warnf("Conflict while updating annotation %s on node %s in attempt %d: %v",
			constants.AnnotationUnschedulable, nodeName, i, err)
	}
	return err
}

func nodeUnschedulableAnnotationHelper(k8sClient client.Client, nodeName string, unschedulable bool) error {
	ctx := context.TODO()
	node := &v1.Node{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		if errors.IsNotFound(err) {
			logrus.Warnf("Node %s not found; cannot update annotation (unschedulable=%v)", nodeName, unschedulable)
			return nil
		}
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	needsUpdate := false
	val := nodeMarkedUnschedulable(node)
	if unschedulable && !val {
		logrus.Infof("Adding annotation %s to node %s", constants.AnnotationUnschedulable, nodeName)
		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}
		node.Annotations[constants.AnnotationUnschedulable] = "true"
		needsUpdate = true
	} else if !unschedulable && val {
		logrus.Infof("Removing annotation %s from node %s", constants.AnnotationUnschedulable, nodeName)
		delete(node.Annotations, constants.AnnotationUnschedulable)
		needsUpdate = true
	}
	if !needsUpdate {
		return nil
	}
	if err := k8sClient.Update(ctx, node); err != nil {
		return fmt.Errorf("failed to update annotation %s on node %s: %w",
			constants.AnnotationUnschedulable, nodeName, err)
	}
	return nil
}

// function to return the number of unavailable kvdb members and a list of the nodes which should have kvdb running in them
func (c *Controller) getKVDBNodeAvailability(cluster *corev1.StorageCluster) (int, map[string]bool, error) {

	var kvdbStorageNodeList = make(map[string]bool)
	unavailableKvdbNodes := 0
	if storagePodsEnabled(cluster) {
		storageNodeList, err := c.Driver.GetStorageNodes(cluster)
		if err != nil {
			return -1, nil, fmt.Errorf("couldn't get list of storage nodes during rolling "+
				"update of storage cluster %s/%s: %v", cluster.Namespace, cluster.Name, err)
		}

		var kvdbMap map[string]bool
		kvdbMap, err = c.Driver.GetKVDBMembers(cluster)
		if err != nil {
			return -1, nil, fmt.Errorf("couldn't get KVDB members due to %s", err)
		}

		for _, storageNode := range storageNodeList {
			isHealthy, present := kvdbMap[storageNode.Id]
			if present {
				if !isHealthy {
					unavailableKvdbNodes++
				}
				kvdbStorageNodeList[storageNode.SchedulerNodeName] = true
			}

		}

	}

	return unavailableKvdbNodes, kvdbStorageNodeList, nil

}

func (c *Controller) parallelUpgradeNodesList(cluster *corev1.StorageCluster, pods []*v1.Pod, unavailableNodes []string, storageNodeList []*storageapi.StorageNode) ([]*v1.Pod, error) {
	if len(pods) == 0 {
		return pods, nil
	}

	if storagePodsEnabled(cluster) {
		if !util.ClusterSupportsParallelUpgrade(storageNodeList) {
			logrus.Infof("Skipping parallel upgrade as not all nodes are running minimum required version : 3.1.2")
			return pods, nil
		}
		nodeNameToIdMap := make(map[string]string)
		nodesToUpgrade := make([]string, 0)
		nodeIdToPodMap := make(map[string]*v1.Pod)
		for _, storageNode := range storageNodeList {
			nodeNameToIdMap[storageNode.SchedulerNodeName] = storageNode.Id
		}

		for _, pod := range pods {
			if nodeId, ok := nodeNameToIdMap[pod.Spec.NodeName]; ok {
				nodesToUpgrade = append(nodesToUpgrade, nodeId)
				nodeIdToPodMap[nodeId] = pod
			}

		}

		nodesSelectedForUpgrade, err := c.Driver.GetNodesSelectedForUpgrade(cluster, nodesToUpgrade, unavailableNodes)
		if err != nil {
			return nil, fmt.Errorf("cannot upgrade nodes in parallel: %v", err)
		}

		podsToUpgrade := make([]*v1.Pod, 0)
		for _, nodeId := range nodesSelectedForUpgrade {
			if pod, ok := nodeIdToPodMap[nodeId]; ok {
				podsToUpgrade = append(podsToUpgrade, pod)
			}
		}
		return podsToUpgrade, nil
	}

	return pods, nil

}

// annotateStoragePod annotate storage pods with custom annotations along with known annotations,
// if no custom annotations created, only known annotations will be retained.
// this function will not update the pod right away, actual update will be handled outside
func (c *Controller) annotateStoragePod(
	cluster *corev1.StorageCluster,
	pod *v1.Pod,
) {
	annotations := make(map[string]string)
	// Keep only known storage pod annotations
	if podAnnotations := pod.GetAnnotations(); podAnnotations != nil {
		for _, knownKeys := range constants.KnownStoragePodAnnotations {
			if v, ok := podAnnotations[knownKeys]; ok {
				annotations[knownKeys] = v
			}
		}
	}
	// Add custom annotations if exist
	if customAnnotations := operatorutil.GetCustomAnnotations(cluster, k8s.Pod, ComponentName); customAnnotations != nil {
		for k, v := range customAnnotations {
			annotations[k] = v
		}
	}

	pod.SetAnnotations(annotations)
}

// constructHistory finds all histories controlled by the given StorageCluster, and
// update current history revision number, or create current history if needed to.
// It also deduplicates current history, and adds missing unique labels to existing histories.
func (c *Controller) constructHistory(
	cluster *corev1.StorageCluster,
) (cur *apps.ControllerRevision, old []*apps.ControllerRevision, err error) {
	var histories []*apps.ControllerRevision
	var currentHistories []*apps.ControllerRevision
	histories, err = c.controlledHistories(cluster)
	if err != nil {
		return nil, nil, err
	}

	for _, history := range histories {
		// Add the unique label if it's not already added to the history
		// We use history name instead of computing hash, so that we don't
		// need to worry about hash collision
		if _, ok := history.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey]; !ok {
			toUpdate := history.DeepCopy()
			toUpdate.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey] = toUpdate.Name
			err = c.client.Update(context.TODO(), toUpdate)
			if err != nil {
				return nil, nil, err
			}
			history = toUpdate.DeepCopy()
		}
		// Compare histories with storage cluster to separate cur and old history
		found := false
		found, err = match(cluster, history)
		if err != nil {
			return nil, nil, err
		}
		if found {
			currentHistories = append(currentHistories, history)
		} else {
			old = append(old, history)
		}
	}

	currRevision := maxRevision(old) + 1
	switch len(currentHistories) {
	case 0:
		// Create a new history if the current one isn't found
		cur, err = c.snapshot(cluster, currRevision)
		if err != nil {
			return nil, nil, err
		}
	default:
		cur, err = c.dedupCurHistories(cluster, currentHistories)
		if err != nil {
			return nil, nil, err
		}
		// Update revision number if necessary
		if cur.Revision < currRevision {
			toUpdate := cur.DeepCopy()
			toUpdate.Revision = currRevision
			err = c.client.Update(context.TODO(), toUpdate)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return cur, old, err
}

// controlledHistories returns all ControllerRevisions controlled by the given StorageCluster.
// This also reconciles ControllerRef by adopting/orphaning.
func (c *Controller) controlledHistories(
	cluster *corev1.StorageCluster,
) ([]*apps.ControllerRevision, error) {
	// List all histories to include those that don't match the selector anymore
	// but have a ControllerRef pointing to the controller.
	historyList := &apps.ControllerRevisionList{}
	err := c.client.List(context.TODO(), historyList, &client.ListOptions{Namespace: cluster.Namespace})
	if err != nil {
		return nil, err
	}

	histories := make([]*apps.ControllerRevision, 0)
	for _, history := range historyList.Items {
		historyCopy := history.DeepCopy()
		histories = append(histories, historyCopy)
	}

	// If any adoptions are attempted, we should first recheck for deletion with
	// an uncached quorum read sometime after listing histories
	canAdoptFunc := controller.RecheckDeletionTimestamp(func(ctx context.Context) (metav1.Object, error) {
		fresh, err := operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			return nil, err
		}
		if fresh.UID != cluster.UID {
			return nil, fmt.Errorf("original StorageCluster %v/%v is gone, got uid %v, wanted %v",
				cluster.Namespace, cluster.Name, fresh.UID, cluster.UID)
		}
		return fresh, nil
	})

	selector := c.StorageClusterSelectorLabels(cluster)
	// Use ControllerRefManager to adopt/orphan as needed. Histories that don't match the
	// labels but are owned by this storage cluster are released (disowned). Histories that
	// match the labels and do not have ref to this storage cluster are owned by it.
	cm := controller.NewControllerRevisionControllerRefManager(
		c.crControl,
		cluster,
		labels.SelectorFromSet(selector),
		controllerKind,
		canAdoptFunc,
	)
	return cm.ClaimControllerRevisions(context.TODO(), histories)
}

func (c *Controller) snapshot(
	cluster *corev1.StorageCluster,
	revision int64,
) (*apps.ControllerRevision, error) {
	patch, err := getPatch(cluster)
	if err != nil {
		return nil, err
	}

	hash := computeHash(&cluster.Spec, cluster.Status.CollisionCount)
	name := historyName(cluster.Name, hash)
	historyLabels := c.StorageClusterSelectorLabels(cluster)
	historyLabels[operatorutil.DefaultStorageClusterUniqueLabelKey] = hash

	history := &apps.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       cluster.Namespace,
			Labels:          historyLabels,
			Annotations:     cluster.Annotations,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cluster, controllerKind)},
		},
		Data:     runtime.RawExtension{Raw: patch},
		Revision: revision,
	}

	createErr := c.client.Create(context.TODO(), history)
	if errors.IsAlreadyExists(createErr) {
		// TODO: Is it okay to get from cache?
		existedHistory := &apps.ControllerRevision{}
		getErr := c.client.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      name,
				Namespace: cluster.Namespace,
			},
			existedHistory,
		)
		if getErr != nil {
			return nil, fmt.Errorf("error getting history that matched current "+
				"hash %s: %v", name, getErr)
		}
		// Check if we already created it
		ok, matchErr := match(cluster, existedHistory)
		if matchErr != nil {
			return nil, matchErr
		}
		if ok {
			return existedHistory, nil
		}

		// Handle name collisions between different history. Get the latest StorageCluster
		// from the API server to make sure collision count is only increased when necessary.
		currSC, getErr := operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if getErr != nil {
			return nil, fmt.Errorf("error getting StorageCluster %v/%v to get the latest "+
				"collision count: %v", cluster.Namespace, cluster.Name, getErr)
		}
		// If the collision count used to compute hash was in fact stale,
		// there's no need to bump collision count; retry again
		if !reflect.DeepEqual(currSC.Status.CollisionCount, cluster.Status.CollisionCount) {
			return nil, fmt.Errorf("found a stale collision count (%d, expected %d) of"+
				" StorageCluster %v while processing; will retry until it is updated",
				cluster.Status.CollisionCount, currSC.Status.CollisionCount, cluster.Name)
		}

		if currSC.Status.CollisionCount == nil {
			currSC.Status.CollisionCount = new(int32)
		}
		*currSC.Status.CollisionCount++
		updateErr := k8s.UpdateStorageClusterStatus(c.client, currSC)
		if updateErr != nil {
			return nil, updateErr
		}
		logrus.Debugf("Found a hash collision for StorageCluster %v -"+
			" bumping collisionCount to %d to resolve it",
			cluster.Name, *currSC.Status.CollisionCount)
		return nil, createErr
	}
	return history, createErr
}

func (c *Controller) dedupCurHistories(
	cluster *corev1.StorageCluster,
	curHistories []*apps.ControllerRevision,
) (*apps.ControllerRevision, error) {
	if len(curHistories) == 1 {
		return curHistories[0], nil
	}

	var maxRevision int64
	var keepCur *apps.ControllerRevision
	for _, cur := range curHistories {
		if cur.Revision >= maxRevision {
			keepCur = cur
			maxRevision = cur.Revision
		}
	}

	// Clean up duplicates and relabel pods
	for _, cur := range curHistories {
		if cur.Name == keepCur.Name {
			continue
		}
		// Relabel pods before dedup
		pods, err := c.getStoragePods(cluster)
		if err != nil {
			return nil, err
		}
		for _, pod := range pods {
			if pod.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey] != keepCur.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey] {
				toUpdate := pod.DeepCopy()
				if toUpdate.Labels == nil {
					toUpdate.Labels = make(map[string]string)
				}
				toUpdate.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey] = keepCur.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey]
				err = c.client.Update(context.TODO(), toUpdate)
				if err != nil {
					return nil, err
				}
			}
		}

		// Remove duplicates
		err = c.client.Delete(context.TODO(), cur)
		if err != nil {
			return nil, err
		}
	}
	return keepCur, nil
}

// groupStorageClusterPods divides all pods into 2 groups,
// new pods will be retained while old pods will be deleted
func (c *Controller) groupStorageClusterPods(
	cluster *corev1.StorageCluster,
	nodeToStoragePods map[string][]*v1.Pod,
	hash string,
) ([]*v1.Pod, []*v1.Pod) {
	var newPods []*v1.Pod
	var oldPods []*v1.Pod

	for _, pods := range nodeToStoragePods {
		for _, pod := range pods {
			// If the returned error is not nil we have a parse error.
			// The controller handles this via the hash.
			if c.syncStoragePod(cluster, pod, hash) {
				newPods = append(newPods, pod)
			} else {
				oldPods = append(oldPods, pod)
			}
		}
	}
	return newPods, oldPods
}

// getPodAvailability returns info about available and unavailable driver pods.
//
// Return values in the order of appearance:
//
//	int: configured value for max number of unavailable pods
//	int: number of pods currently unavailable
//	map[string]*v1.Pod: map of *available* pods keyed by the pod name
//	err error: error if any
func (c *Controller) getPodAvailability(
	cluster *corev1.StorageCluster,
	nodeToStoragePods map[string][]*v1.Pod,
	storageNodeList []*storageapi.StorageNode,
) (int, []string, map[string]*v1.Pod, error) {
	logrus.Debugf("Getting unavailable numbers")
	availablePods := map[string]*v1.Pod{}
	nodeList := &v1.NodeList{}
	unavailableNodes := []string{}
	err := c.client.List(context.TODO(), nodeList, &client.ListOptions{})
	if err != nil {
		return -1, unavailableNodes, nil, fmt.Errorf("couldn't get list of nodes during rolling "+
			"update of storage cluster %s/%s: %v", cluster.Namespace, cluster.Name, err)
	}

	storageNodeMap := make(map[string]*storageapi.StorageNode)
	var nonK8sStorageNodes []*storageapi.StorageNode

	if storagePodsEnabled(cluster) {
		for _, storageNode := range storageNodeList {
			if len(storageNode.SchedulerNodeName) == 0 {
				nonK8sStorageNodes = append(nonK8sStorageNodes, storageNode)
			} else {
				if existing, ok := storageNodeMap[storageNode.SchedulerNodeName]; ok {
					// During a k8s upgrade, it may happen that a new node is started on the same k8s host using
					// a scheduler node name that was previously used by a different node. In this case, if we have
					// a storage node that is healthy, then we can ignore the old node which is not healthy as it will
					// either find a new k8s node or it will be a storageless node that will get auto-decommissioned.
					// It is mostly not possible that a storageless node will replace a storage node on the same node.
					// But if it does, then refer - https://purestorage.atlassian.net/browse/PWX-38505 for more details.
					if existing.Status != storageapi.Status_STATUS_OK && storageNode.Status == storageapi.Status_STATUS_OK {
						storageNodeMap[storageNode.SchedulerNodeName] = storageNode
					}
				} else {
					storageNodeMap[storageNode.SchedulerNodeName] = storageNode
				}
			}
		}
	}
	var numUnavailable, desiredNumberScheduled int
	for _, node := range nodeList.Items {
		// If the node has a storage node and it is not healthy.
		storageNode, storageNodeExists := storageNodeMap[node.Name]
		if storageNodeExists {
			delete(storageNodeMap, node.Name)
			if storageNode.Status != storageapi.Status_STATUS_OK {
				logrus.WithField("StorageNode", fmt.Sprintf("%s/%s", cluster.Namespace, node.Name)).
					Info("Storage node is not healthy")
				numUnavailable++
				unavailableNodes = append(unavailableNodes, storageNode.Id)
				continue
			}
		}

		wantToRun, _, err := c.nodeShouldRunStoragePod(&node, cluster)
		logrus.WithFields(logrus.Fields{
			"node":              node.Name,
			"wantToRun":         wantToRun,
			"storageNodeExists": storageNodeExists,
		}).WithError(err).Debug("check node should run storage pod")
		if err != nil {
			return -1, unavailableNodes, nil, err
		}

		if !wantToRun {
			continue
		}

		desiredNumberScheduled++
		storagePods, exists := nodeToStoragePods[node.Name]
		if !exists {
			numUnavailable++
			if storageNodeExists {
				unavailableNodes = append(unavailableNodes, storageNode.Id)
			}
			continue
		}
		available := false
		for _, pod := range storagePods {
			// for the purposes of update we ensure that the pod is
			// both available and not terminating
			if podutil.IsPodReady(pod) && pod.DeletionTimestamp == nil {
				// Wait for MinReadySeconds after pod is ready.
				if podutil.IsPodAvailable(pod, cluster.Spec.UpdateStrategy.RollingUpdate.MinReadySeconds, metav1.Now()) {
					available = true
					availablePods[pod.Name] = pod
				} else {
					logrus.Infof("Pod %s has not been ready for MinReadySeconds (%d), wait to perform rolling updates",
						pod.Name, cluster.Spec.UpdateStrategy.RollingUpdate.MinReadySeconds)
				}
				break
			}
		}
		if !available {
			if storageNodeExists {
				unavailableNodes = append(unavailableNodes, storageNode.Id)
			}
			numUnavailable++
		}
	}

	for _, storageNode := range storageNodeMap {
		nonK8sStorageNodes = append(nonK8sStorageNodes, storageNode)
	}

	// For the storage nodes that do not have a corresponding k8s node.
	for _, storageNode := range nonK8sStorageNodes {
		if storageNode.Status != storageapi.Status_STATUS_OK {
			logrus.WithField("StorageNode", storageNode).Info("Storage node is not healthy")
			numUnavailable++
			unavailableNodes = append(unavailableNodes, storageNode.Id)
		}
	}

	maxUnavailable, err := intstr.GetValueFromIntOrPercent(
		cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable,
		desiredNumberScheduled,
		true,
	)
	if err != nil {
		return -1, unavailableNodes, nil, fmt.Errorf("invalid value for MaxUnavailable: %v", err)
	}
	logrus.Debugf("StorageCluster %s/%s, maxUnavailable: %d, numUnavailable: %d",
		cluster.Namespace, cluster.Name, maxUnavailable, numUnavailable)
	return maxUnavailable, unavailableNodes, availablePods, nil
}

func (c *Controller) cleanupHistory(
	cluster *corev1.StorageCluster,
	old []*apps.ControllerRevision,
) error {
	nodesToStoragePods, err := c.getNodeToStoragePods(cluster)
	if err != nil {
		return fmt.Errorf("couldn't get node to storage pod mapping for storage cluster %v: %v",
			cluster.Name, err)
	}

	toKeep := int(*cluster.Spec.RevisionHistoryLimit)
	toKill := len(old) - toKeep
	if toKill <= 0 {
		return nil
	}

	// Find all hashes of live pods
	liveHashes := make(map[string]bool)
	for _, pods := range nodesToStoragePods {
		for _, pod := range pods {
			if hash := pod.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey]; len(hash) > 0 {
				liveHashes[hash] = true
			}
		}
	}

	// Find all live history with the above hashes
	liveHistory := make(map[string]bool)
	for _, history := range old {
		if hash := history.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey]; liveHashes[hash] {
			liveHistory[history.Name] = true
		}
	}

	// Clean up old history from smallest to highest revision (from oldest to newest)
	sort.Sort(historiesByRevision(old))
	for _, history := range old {
		if toKill <= 0 {
			break
		}
		if liveHistory[history.Name] {
			continue
		}
		// Clean up
		err := c.client.Delete(context.TODO(), history)
		if err != nil {
			return err
		}
		toKill--
	}

	return nil
}

// syncStoragePod checks if pod contains label value that matches the hash,
// and returns whether the pod will be retained.
// We return true to retain the pod if certain fields (that need pod restart) have not changed,
// and synchronize pod's hash with the storage cluster to ensure consistency if they are different,
// else we return false to restart the pod.
func (c *Controller) syncStoragePod(
	cluster *corev1.StorageCluster,
	pod *v1.Pod,
	hash string,
) bool {
	node := &v1.Node{}
	err := c.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name: pod.Spec.NodeName,
		},
		node,
	)
	if err != nil {
		logrus.Warnf("Unable to get Kubernetes node %v for pod %v/%v. %v",
			pod.Spec.NodeName, pod.Namespace, pod.Name, err)
		return false
	}

	// This only does some driver specific checks to see if the pod is
	// updated or not. All the other fields in the StorageCluster spec
	// are compared below with corresponding history revision.
	if updated := c.Driver.IsPodUpdated(cluster, pod); !updated {
		return false
	}

	var oldNodeLabels map[string]string
	if encodedLabels, exists := pod.Annotations[constants.AnnotationNodeLabels]; exists {
		if err := json.Unmarshal([]byte(encodedLabels), &oldNodeLabels); err != nil {
			logrus.Warnf("Unable to decode old node labels stored on the pod. %v", err)
		}
	}
	if oldNodeLabels == nil {
		oldNodeLabels = node.Labels
	}

	podHash := pod.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey]
	// If the hash on pod is same as the current cluster's hash and node labels
	// have not changed then there is no update needed for the pod.
	if len(hash) > 0 && podHash == hash && reflect.DeepEqual(oldNodeLabels, node.Labels) {
		return true
	}

	podHistory := &apps.ControllerRevision{}
	podHistoryName := historyName(cluster.Name, podHash)
	err = c.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      podHistoryName,
			Namespace: cluster.Namespace,
		},
		podHistory,
	)
	if err != nil {
		logrus.Warnf("Unable to get storage cluster revision %v for pod %v/%v. %v",
			podHistoryName, pod.Namespace, pod.Name, err)
		return false
	}

	logrus.Debugf("Checking if relevant fields in pod %v/%v have changed since %v",
		pod.Namespace, pod.Name, podHash)

	// If the selected fields match then the pods can be considered as updated
	matched, err := matchSelectedFields(cluster, podHistory, node, oldNodeLabels)
	if err != nil {
		logrus.Warnf("Could not check the diff between current pod and latest spec. %v", err)
		return false
	}

	if matched {
		// Updating custom annotation will change the cluster hash,
		// pod is treated as updated here but its hash is not consistent with the storage cluster,
		// so update the custom annotations here first if necessary, then set the storage cluster hash
		// to the pod to avoid checking selected fields or set annotations over and over again
		c.annotateStoragePod(cluster, pod)
		pod.Labels[operatorutil.DefaultStorageClusterUniqueLabelKey] = hash
		if err := c.client.Update(context.TODO(), pod); err != nil {
			errMsg := fmt.Sprintf("Unable to update storage pod: %v", err)
			if strings.Contains(err.Error(), k8s.UpdateRevisionConflictErr) {
				logrus.Warnf(errMsg)
			} else {
				k8s.WarningEvent(c.recorder, cluster, operatorutil.FailedStoragePodReason, errMsg)
			}
		}
	}

	return matched
}

// matchSelectedFields checks only whether certain fields in the spec have changed.
// The pod does not need to restart on changes to fields like UpdateStrategy or
// ImagePullPolicy. So only match fields that affect the storage pods.
func matchSelectedFields(
	cluster *corev1.StorageCluster,
	history *apps.ControllerRevision,
	node *v1.Node,
	oldNodeLabels map[string]string,
) (bool, error) {
	var raw map[string]interface{}
	err := json.Unmarshal(history.Data.Raw, &raw)
	if err != nil {
		return false, err
	}

	spec, ok := raw["spec"].(map[string]interface{})
	if !ok {
		return false, nil
	}
	delete(spec, "$patch")

	rawHistory, err := json.Marshal(spec)
	if err != nil {
		return false, err
	}

	oldSpec := &corev1.StorageClusterSpec{}
	err = json.Unmarshal(rawHistory, oldSpec)
	if err != nil {
		return false, err
	}

	oldNode := node.DeepCopy()
	oldNode.Labels = oldNodeLabels
	oldSpec = clusterSpecForNode(oldNode, oldSpec)
	currentSpec := clusterSpecForNode(node, &cluster.Spec)

	if oldSpec.Image != currentSpec.Image {
		return false, nil
	} else if oldSpec.CustomImageRegistry != currentSpec.CustomImageRegistry {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.ImagePullSecret, currentSpec.ImagePullSecret) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.Kvdb, currentSpec.Kvdb) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.CloudStorage, currentSpec.CloudStorage) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.SecretsProvider, currentSpec.SecretsProvider) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.StartPort, currentSpec.StartPort) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.Network, currentSpec.Network) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.Storage, currentSpec.Storage) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.RuntimeOpts, currentSpec.RuntimeOpts) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.Resources, currentSpec.Resources) {
		return false, nil
	} else if !elementsMatch(oldSpec.Env, currentSpec.Env) {
		return false, nil
	} else if !elementsMatch(oldSpec.Volumes, currentSpec.Volumes) {
		return false, nil
	} else if !doesTelemetryMatch(oldSpec, currentSpec) && !util.IsCCMGoSupported(util.GetPortworxVersion(cluster)) {
		// only check for old px version, ccm upgrade should be triggered by px upgrade
		return false, nil
	} else if isBounceRequired(oldSpec, currentSpec) {
		return false, nil
	}
	return true, nil
}

func doesTelemetryMatch(oldSpec, currentSpec *corev1.StorageClusterSpec) bool {
	// not enabled in both (covers nil cases)
	if !util.IsTelemetryEnabled(*oldSpec) && !util.IsTelemetryEnabled(*currentSpec) {
		return true
	}

	if !util.IsTelemetryEnabled(*oldSpec) && util.IsTelemetryEnabled(*currentSpec) ||
		util.IsTelemetryEnabled(*oldSpec) && !util.IsTelemetryEnabled(*currentSpec) {
		return false
	}

	return reflect.DeepEqual(oldSpec.Monitoring.Telemetry, currentSpec.Monitoring.Telemetry)
}

// isBounceRequired handles miscellaneous fields that requrie a pod bounce
func isBounceRequired(oldSpec, currentSpec *corev1.StorageClusterSpec) bool {
	return isSecurityBounceRequired(oldSpec, currentSpec) || isCSIBounceRequired(oldSpec, currentSpec)
}

// isCSIBounceRequired handles CSI fields that requrie a pod bounce
func isCSIBounceRequired(oldSpec, currentSpec *corev1.StorageClusterSpec) bool {
	// Spec changed from enabled -> disabled or vice versa
	return isCSIEnabled(oldSpec) != isCSIEnabled(currentSpec)
}

// isCSIEnabled checks if CSI is enabled with CSI spec
// or deprecated CSI feature gate
func isCSIEnabled(spec *corev1.StorageClusterSpec) bool {
	// Check if enabled in spec
	if spec.CSI != nil && spec.CSI.Enabled {
		return true
	}

	// Even though feature gate is deprecated, we must check
	// its previous value to determine if a bounce is required.
	if len(spec.FeatureGates) > 0 {
		csiFeatureFlag, featureGateSet := spec.FeatureGates[string(util.FeatureCSI)]
		if featureGateSet {
			csiFeatureFlagEnabled, _ := strconv.ParseBool(csiFeatureFlag)
			if csiFeatureFlagEnabled {
				return true
			}
		}
	}

	return false
}

// isSecurityBounceRequired specific security spec fields that require a pod bounce
func isSecurityBounceRequired(oldSpec, currentSpec *corev1.StorageClusterSpec) bool {
	// security disabled or nil updated to enabled
	if (oldSpec.Security == nil || !oldSpec.Security.Enabled) && currentSpec.Security != nil && currentSpec.Security.Enabled {
		return true
	}
	// security enabled updated to disabled or nil
	if (oldSpec.Security != nil && oldSpec.Security.Enabled) && (currentSpec.Security == nil || !currentSpec.Security.Enabled) {
		return true
	}

	// security enabled and certain field is updated
	if currentSpec.Security != nil && currentSpec.Security.Enabled {
		// safe to assume currentSpec.Security.Auth.SelfSigned is non-nil as it will always be have defaults.
		// individual fields may be nil though, so use DeepEqual to safely check for nil too.
		if !reflect.DeepEqual(currentSpec.Security.Auth.SelfSigned.Issuer, oldSpec.Security.Auth.SelfSigned.Issuer) {
			return true
		} else if !reflect.DeepEqual(currentSpec.Security.Auth.SelfSigned.SharedSecret, oldSpec.Security.Auth.SelfSigned.SharedSecret) {
			return true
		}
	}

	return false
}

// clusterSpecForNode returns the corresponding StorageCluster spec for given node.
// If there is node specific configuration in the cluster, it will merge it with
// the top level cluster spec and return the updated cluster spec.
func clusterSpecForNode(
	node *v1.Node,
	clusterSpec *corev1.StorageClusterSpec,
) *corev1.StorageClusterSpec {
	var (
		nodeLabels       = labels.Set(node.Labels)
		matchingNodeSpec *corev1.NodeSpec
	)

	for _, nodeSpec := range clusterSpec.Nodes {
		if nodeSpec.Selector.NodeName == node.Name {
			matchingNodeSpec = nodeSpec.DeepCopy()
			break
		} else if len(nodeSpec.Selector.NodeName) == 0 {
			nodeSelector, err := metav1.LabelSelectorAsSelector(nodeSpec.Selector.LabelSelector)
			if err != nil {
				logrus.Warnf("Failed to parse label selector %#v: %v", nodeSpec.Selector.LabelSelector, err)
				continue
			}
			if nodeSelector.Matches(nodeLabels) {
				matchingNodeSpec = nodeSpec.DeepCopy()
				break
			}
		}
	}

	newClusterSpec := clusterSpec.DeepCopy()
	overwriteClusterSpecWithNodeSpec(newClusterSpec, matchingNodeSpec)
	return newClusterSpec
}

// overwriteClusterSpecWithNodeSpec updates the input cluster spec configuration
// with the given node spec. This makes it easy to have the complete node
// configuration in one place at the cluster level.
func overwriteClusterSpecWithNodeSpec(
	clusterSpec *corev1.StorageClusterSpec,
	nodeSpec *corev1.NodeSpec,
) {
	if nodeSpec == nil {
		return
	}
	if nodeSpec.Storage != nil {
		clusterSpec.Storage = nodeSpec.Storage.DeepCopy()
	}
	if nodeSpec.CloudStorage != nil {
		if clusterSpec.CloudStorage == nil {
			clusterSpec.CloudStorage = &corev1.CloudStorageSpec{}
		}
		clusterSpec.CloudStorage.CloudStorageCommon = *(nodeSpec.CloudStorage.CloudStorageCommon.DeepCopy())
	}
	if nodeSpec.Network != nil {
		clusterSpec.Network = nodeSpec.Network.DeepCopy()
	}
	if len(nodeSpec.Env) > 0 {
		envMap := make(map[string]*v1.EnvVar)
		for _, clusterEnv := range clusterSpec.Env {
			envMap[clusterEnv.Name] = clusterEnv.DeepCopy()
		}
		for _, nodeEnv := range nodeSpec.Env {
			envMap[nodeEnv.Name] = nodeEnv.DeepCopy()
		}
		clusterSpec.Env = make([]v1.EnvVar, 0)
		for _, env := range envMap {
			clusterSpec.Env = append(clusterSpec.Env, *env)
		}
	}
	if len(nodeSpec.RuntimeOpts) > 0 {
		clusterSpec.RuntimeOpts = make(map[string]string)
		for k, v := range nodeSpec.RuntimeOpts {
			clusterSpec.RuntimeOpts[k] = v
		}
	}
}

// splitByAvailablePods splits provided storage cluster pods by availability
func splitByAvailablePods(pods []*v1.Pod) ([]*v1.Pod, []*v1.Pod) {
	unavailablePods := []*v1.Pod{}
	availablePods := []*v1.Pod{}
	for _, pod := range pods {
		if podutil.IsPodReady(pod) {
			availablePods = append(availablePods, pod)
		} else {
			unavailablePods = append(unavailablePods, pod)
		}
	}
	return availablePods, unavailablePods
}

// maxRevision returns the max revision number of the given list of histories
func maxRevision(histories []*apps.ControllerRevision) int64 {
	max := int64(0)
	for _, history := range histories {
		if history.Revision > max {
			max = history.Revision
		}
	}
	return max
}

// match checks if the given StorageCluster's template matches the template
// stored in the given history.
func match(
	cluster *corev1.StorageCluster,
	history *apps.ControllerRevision,
) (bool, error) {
	patch, err := getPatch(cluster)
	if err != nil {
		return false, err
	}
	return bytes.Equal(patch, history.Data.Raw), nil
}

// getPatch returns a strategic merge patch that can be applied to restore a StorageCluster
// to a previous version. If the returned error is nil the patch is valid.
func getPatch(cluster *corev1.StorageCluster) ([]byte, error) {
	clusterBytes, err := json.Marshal(cluster)
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	err = json.Unmarshal(clusterBytes, &raw)
	if err != nil {
		return nil, err
	}
	objCopy := make(map[string]interface{})

	// Create a patch of the StorageCluster that replaces spec
	spec := raw["spec"].(map[string]interface{})
	spec["$patch"] = "replace"
	objCopy["spec"] = spec
	return json.Marshal(objCopy)
}

type historiesByRevision []*apps.ControllerRevision

func (h historiesByRevision) Len() int      { return len(h) }
func (h historiesByRevision) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h historiesByRevision) Less(i, j int) bool {
	return h[i].Revision < h[j].Revision
}
