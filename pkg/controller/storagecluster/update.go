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

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// rollingUpdate deletes old storage cluster pods making sure that no more than
// cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable pods are unavailable
func (c *Controller) rollingUpdate(cluster *corev1alpha1.StorageCluster, hash string) error {
	nodeToStoragePods, err := c.getNodeToStoragePods(cluster)
	if err != nil {
		return fmt.Errorf("couldn't get node to storage pod mapping for storage cluster %v: %v",
			cluster.Name, err)
	}

	_, oldPods := c.getAllStorageClusterPods(cluster, nodeToStoragePods, hash)
	maxUnavailable, numUnavailable, err := c.getUnavailableNumbers(cluster, nodeToStoragePods)
	if err != nil {
		return fmt.Errorf("couldn't get unavailable numbers: %v", err)
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
		logrus.Debugf("Marking pod %s/%s for deletion", cluster.Name, pod.Name)
		oldPodsToDelete = append(oldPodsToDelete, pod.Name)
	}

	logrus.Debugf("Marking old pods for deletion")
	for _, pod := range oldAvailablePods {
		if numUnavailable >= maxUnavailable {
			logrus.Debugf("Number of unavailable StorageCluster pods: %d, is equal "+
				"to or exceeds allowed maximum: %d", numUnavailable, maxUnavailable)
			break
		}
		logrus.Debugf("Marking pod %s/%s for deletion", cluster.Name, pod.Name)
		oldPodsToDelete = append(oldPodsToDelete, pod.Name)
		numUnavailable++
	}
	return c.syncNodes(cluster, oldPodsToDelete, []string{}, hash)
}

// constructHistory finds all histories controlled by the given StorageCluster, and
// update current history revision number, or create current history if needed to.
// It also deduplicates current history, and adds missing unique labels to existing histories.
func (c *Controller) constructHistory(
	cluster *corev1alpha1.StorageCluster,
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
		if _, ok := history.Labels[defaultStorageClusterUniqueLabelKey]; !ok {
			toUpdate := history.DeepCopy()
			toUpdate.Labels[defaultStorageClusterUniqueLabelKey] = toUpdate.Name
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
	cluster *corev1alpha1.StorageCluster,
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
	canAdoptFunc := controller.RecheckDeletionTimestamp(func() (metav1.Object, error) {
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

	selector := c.storageClusterSelectorLabels(cluster)
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
	return cm.ClaimControllerRevisions(histories)
}

func (c *Controller) snapshot(
	cluster *corev1alpha1.StorageCluster,
	revision int64,
) (*apps.ControllerRevision, error) {
	patch, err := getPatch(cluster)
	if err != nil {
		return nil, err
	}

	hash := computeHash(&cluster.Spec, cluster.Status.CollisionCount)
	name := historyName(cluster.Name, hash)
	historyLabels := c.storageClusterSelectorLabels(cluster)
	historyLabels[defaultStorageClusterUniqueLabelKey] = hash

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
		updateErr := c.client.Status().Update(context.TODO(), currSC)
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
	cluster *corev1alpha1.StorageCluster,
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
			if pod.Labels[defaultStorageClusterUniqueLabelKey] != keepCur.Labels[defaultStorageClusterUniqueLabelKey] {
				toUpdate := pod.DeepCopy()
				if toUpdate.Labels == nil {
					toUpdate.Labels = make(map[string]string)
				}
				toUpdate.Labels[defaultStorageClusterUniqueLabelKey] = keepCur.Labels[defaultStorageClusterUniqueLabelKey]
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

func (c *Controller) getAllStorageClusterPods(
	cluster *corev1alpha1.StorageCluster,
	nodeToStoragePods map[string][]*v1.Pod,
	hash string,
) ([]*v1.Pod, []*v1.Pod) {
	var newPods []*v1.Pod
	var oldPods []*v1.Pod

	for _, pods := range nodeToStoragePods {
		for _, pod := range pods {
			// If the returned error is not nil we have a parse error.
			// The controller handles this via the hash.
			if c.isPodUpdated(cluster, pod, hash) {
				newPods = append(newPods, pod)
			} else {
				oldPods = append(oldPods, pod)
			}
		}
	}
	return newPods, oldPods
}

func (c *Controller) getUnavailableNumbers(
	cluster *corev1alpha1.StorageCluster,
	nodeToStoragePods map[string][]*v1.Pod,
) (int, int, error) {
	logrus.Debugf("Getting unavailable numbers")
	nodeList := &v1.NodeList{}
	err := c.client.List(context.TODO(), nodeList, &client.ListOptions{})
	if err != nil {
		return -1, -1, fmt.Errorf("couldn't get list of nodes during rolling "+
			"update of storage cluster  %#v: %v", cluster, err)
	}

	var numUnavailable, desiredNumberScheduled int
	for _, node := range nodeList.Items {
		wantToRun, _, _, err := c.nodeShouldRunStoragePod(&node, cluster)
		if err != nil {
			return -1, -1, err
		}
		if !wantToRun {
			continue
		}
		desiredNumberScheduled++
		storagePods, exists := nodeToStoragePods[node.Name]
		if !exists {
			numUnavailable++
			continue
		}
		available := false
		for _, pod := range storagePods {
			// for the purposes of update we ensure that the pod is
			// both available and not terminating
			if podutil.IsPodReady(pod) && pod.DeletionTimestamp == nil {
				available = true
				break
			}
		}
		if !available {
			numUnavailable++
		}
	}

	maxUnavailable, err := intstr.GetValueFromIntOrPercent(
		cluster.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable,
		desiredNumberScheduled,
		true,
	)
	if err != nil {
		return -1, -1, fmt.Errorf("invalid value for MaxUnavailable: %v", err)
	}
	logrus.Debugf("StorageCluster %s/%s, maxUnavailable: %d, numUnavailable: %d",
		cluster.Namespace, cluster.Name, maxUnavailable, numUnavailable)
	return maxUnavailable, numUnavailable, nil
}

func (c *Controller) cleanupHistory(
	cluster *corev1alpha1.StorageCluster,
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
			if hash := pod.Labels[defaultStorageClusterUniqueLabelKey]; len(hash) > 0 {
				liveHashes[hash] = true
			}
		}
	}

	// Find all live history with the above hashes
	liveHistory := make(map[string]bool)
	for _, history := range old {
		if hash := history.Labels[defaultStorageClusterUniqueLabelKey]; liveHashes[hash] {
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

// isPodUpdated checks if pod contains label value that matches the hash.
// We consider the pod to be really updated if certain fields (that need
// pod restart) have changed, else we consider the pod to be updated.
func (c *Controller) isPodUpdated(
	cluster *corev1alpha1.StorageCluster,
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

	var oldNodeLabels map[string]string
	if encodedLabels, exists := pod.Annotations[annotationNodeLabels]; exists {
		if err := json.Unmarshal([]byte(encodedLabels), &oldNodeLabels); err != nil {
			logrus.Warnf("Unable to decode old node labels stored on the pod. %v", err)
		}
	}
	if oldNodeLabels == nil {
		oldNodeLabels = node.Labels
	}

	podHash := pod.Labels[defaultStorageClusterUniqueLabelKey]
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
	return matched
}

// matchSelectedFields checks only whether certain fields in the spec have changed.
// The pod does not need to restart on changes to fields like UpdateStrategy or
// ImagePullPolicy. So only match fields that affect the storage pods.
func matchSelectedFields(
	cluster *corev1alpha1.StorageCluster,
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

	oldSpec := &corev1alpha1.StorageClusterSpec{}
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
	} else if !reflect.DeepEqual(oldSpec.FeatureGates, currentSpec.FeatureGates) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.Network, currentSpec.Network) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.Storage, currentSpec.Storage) {
		return false, nil
	} else if !reflect.DeepEqual(oldSpec.RuntimeOpts, currentSpec.RuntimeOpts) {
		return false, nil
	} else if !isEnvEqual(oldSpec.Env, currentSpec.Env) {
		return false, nil
	}
	return true, nil
}

// clusterSpecForNode returns the corresponding StorageCluster spec for given node.
// If there is node specific configuration in the cluster, it will merge it with
// the top level cluster spec and return the updated cluster spec.
func clusterSpecForNode(
	node *v1.Node,
	clusterSpec *corev1alpha1.StorageClusterSpec,
) *corev1alpha1.StorageClusterSpec {
	var (
		nodeLabels       = labels.Set(node.Labels)
		matchingNodeSpec *corev1alpha1.NodeSpec
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
	clusterSpec *corev1alpha1.StorageClusterSpec,
	nodeSpec *corev1alpha1.NodeSpec,
) {
	if nodeSpec == nil {
		return
	}
	if nodeSpec.Storage != nil {
		clusterSpec.Storage = nodeSpec.Storage.DeepCopy()
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
	cluster *corev1alpha1.StorageCluster,
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
func getPatch(cluster *corev1alpha1.StorageCluster) ([]byte, error) {
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
