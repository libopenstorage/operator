package portworx

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	kvdb_api "github.com/portworx/kvdb/api/bootstrap"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
)

const (
	// pxEntriesKey is key which holds all the bootstrap entries
	pxEntriesKey = "px-entries"
)

func (p *portworx) UpdateStorageClusterStatus(
	cluster *corev1.StorageCluster,
	clusterHash string,
) error {
	if cluster.Status.Phase == "" {
		cluster.Status.ClusterName = cluster.Name
		cluster.Status.Phase = string(corev1.ClusterStateInit)
		return nil
	}

	convertDeprecatedClusterStatus(cluster)
	// Update runtime status and StorageNodes from SDK server
	if err := p.updatePortworxRuntimeStatus(cluster); err != nil {
		return err
	}

	// Update migration status
	p.updatePortworxMigrationStatus(cluster)

	// Update install and update status
	storageNodes := &corev1.StorageNodeList{}
	if err := p.k8sClient.List(context.TODO(), storageNodes, &client.ListOptions{}); err != nil {
		logrus.Warnf("failed to get a list of StorageNode: %v", err)
	}
	p.updatePortworxInstallStatus(cluster, storageNodes.Items)
	p.updatePortworxUpdateStatus(cluster, storageNodes.Items, clusterHash)

	// Iterate all status conditions to conclude the storage cluster state
	newState := getStorageClusterState(cluster)
	cluster.Status.Phase = string(newState)
	return nil
}

func convertDeprecatedClusterStatus(
	cluster *corev1.StorageCluster,
) {
	// Handling ALL deprecated storage cluster phases here and using empty timestamp to distinguish
	switch cluster.Status.Phase {
	case string(corev1.ClusterConditionStatusFailed):
		cluster.Status.Phase = string(corev1.ClusterStateDegraded)
	case constants.PhaseAwaitingApproval:
		cluster.Status.Phase = string(corev1.ClusterStateInit)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:             pxutil.PortworxComponentName,
			Type:               corev1.ClusterConditionTypeMigration,
			Status:             corev1.ClusterConditionStatusPending,
			LastTransitionTime: metav1.Time{},
		})
	case constants.PhaseMigrationInProgress:
		cluster.Status.Phase = string(corev1.ClusterStateInit)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:             pxutil.PortworxComponentName,
			Type:               corev1.ClusterConditionTypeMigration,
			Status:             corev1.ClusterConditionStatusInProgress,
			LastTransitionTime: metav1.Time{},
		})

	case string(corev1.ClusterConditionTypeDelete) + string(corev1.ClusterConditionStatusInProgress):
		fallthrough
	case string(corev1.ClusterConditionTypeDelete) + string(corev1.ClusterConditionStatusCompleted):
		fallthrough
	case string(corev1.ClusterConditionTypeDelete) + string(corev1.ClusterConditionStatusFailed):
		status := strings.TrimPrefix(cluster.Status.Phase, string(corev1.ClusterConditionTypeDelete))
		cluster.Status.Phase = string(corev1.ClusterStateUninstall)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:             pxutil.PortworxComponentName,
			Type:               corev1.ClusterConditionTypeDelete,
			Status:             corev1.ClusterConditionStatus(status),
			LastTransitionTime: metav1.Time{},
		})
	case string(corev1.ClusterConditionStatusOnline):
		cluster.Status.Phase = string(corev1.ClusterStateRunning)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:             pxutil.PortworxComponentName,
			Type:               corev1.ClusterConditionTypeRuntimeState,
			Status:             corev1.ClusterConditionStatusOnline,
			LastTransitionTime: metav1.Time{},
		})
	case string(corev1.ClusterConditionStatusOffline):
		fallthrough
	case string(corev1.ClusterConditionStatusUnknown):
		fallthrough
	case string(corev1.ClusterConditionStatusNotInQuorum):
		cluster.Status.Phase = string(corev1.ClusterStateDegraded)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:             pxutil.PortworxComponentName,
			Type:               corev1.ClusterConditionTypeRuntimeState,
			Status:             corev1.ClusterConditionStatus(cluster.Status.Phase),
			LastTransitionTime: metav1.Time{},
		})
	}
}

func (p *portworx) updatePortworxRuntimeStatus(
	cluster *corev1.StorageCluster,
) error {
	if !pxutil.IsPortworxEnabled(cluster) {
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source: pxutil.PortworxComponentName,
			Type:   corev1.ClusterConditionTypeRuntimeState,
			Status: corev1.ClusterConditionStatusOnline,
		})
		return nil
	}

	var err error
	p.sdkConn, err = pxutil.GetPortworxConn(p.sdkConn, p.k8sClient, cluster.Namespace)
	if err != nil {
		p.updateRemainingStorageNodesWithoutError(cluster, nil)
		if pxutil.IsFreshInstall(cluster) &&
			strings.HasPrefix(err.Error(), pxutil.ErrMsgGrpcConnection) {
			// Don't return grpc connection error during initialization,
			// as SDK server won't be up anyway
			logrus.Warn(err)
			return nil
		}
		return err
	}

	clusterClient := api.NewOpenStorageClusterClient(p.sdkConn)
	ctx, err := pxutil.SetupContextWithToken(context.Background(), cluster, p.k8sClient)
	if err != nil {
		return err
	}
	pxCluster, err := clusterClient.InspectCurrent(ctx, &api.SdkClusterInspectCurrentRequest{})
	if err != nil {
		if closeErr := p.sdkConn.Close(); closeErr != nil {
			logrus.Warnf("Failed to close grpc connection. %v", closeErr)
		}
		p.sdkConn = nil
		p.updateRemainingStorageNodesWithoutError(cluster, nil)
		return fmt.Errorf("failed to inspect cluster: %v", err)
	} else if pxCluster.Cluster == nil {
		p.updateRemainingStorageNodesWithoutError(cluster, nil)
		return fmt.Errorf("empty ClusterInspect response")
	}

	runtimeStatus := mapPortworxRuntimeStatus(pxCluster.Cluster.Status)
	condition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeRuntimeState)
	if (condition == nil || condition.Status != corev1.ClusterConditionStatusOnline) &&
		runtimeStatus == corev1.ClusterConditionStatusOnline {
		msg := fmt.Sprintf("Storage cluster %v online", cluster.GetName())
		p.normalEvent(cluster, util.ClusterOnlineReason, msg)
	}

	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeRuntimeState,
		Status: runtimeStatus,
	})

	cluster.Status.ClusterName = pxCluster.Cluster.Name
	cluster.Status.ClusterUID = pxCluster.Cluster.Id

	return p.updateStorageNodes(cluster)
}

func (p *portworx) updatePortworxMigrationStatus(
	cluster *corev1.StorageCluster,
) {
	// Mark migration as completed when portworx daemonset got deleted
	// TODO: check other component condition here
	migrationCondition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeMigration)
	if migrationCondition != nil && migrationCondition.Status == corev1.ClusterConditionStatusInProgress {
		ds := &appsv1.DaemonSet{}
		err := p.k8sClient.Get(context.TODO(), types.NamespacedName{
			Name:      constants.PortworxDaemonSetName,
			Namespace: cluster.Namespace,
		}, ds)
		if errors.IsNotFound(err) {
			util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
				Source: pxutil.PortworxComponentName,
				Type:   corev1.ClusterConditionTypeMigration,
				Status: corev1.ClusterConditionStatusCompleted,
			})
		}
	}
}

// updatePortworxInstallStatus updates portworx install status, expected status: InProgress, Completed
// If there's 1 node that has Initializing status and portworx is not initialized, mark as PortworxInstallInProgress
// If all nodes status are updated according to SDK server response and portworx is installing, mark as PortworxInstallCompleted
func (p *portworx) updatePortworxInstallStatus(
	cluster *corev1.StorageCluster,
	storageNodes []corev1.StorageNode,
) {
	if len(storageNodes) == 0 {
		logrus.Debugf("no storage nodes provided to update portworx install status")
		return
	}
	installCondition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeInstall)
	// Install only happens once on fresh install
	if installCondition != nil && installCondition.Status == corev1.ClusterConditionStatusCompleted {
		return
	}

	initializingCount := 0
	for _, storageNode := range storageNodes {
		if storageNode.Status.Phase == string(corev1.NodeInitStatus) {
			initializingCount += 1
		}
	}

	if initializingCount == 0 && installCondition != nil && installCondition.Status == corev1.ClusterConditionStatusInProgress {
		msg := fmt.Sprintf("Portworx installation completed on %v nodes", len(storageNodes))
		logrus.Info(msg)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:  pxutil.PortworxComponentName,
			Type:    corev1.ClusterConditionTypeInstall,
			Status:  corev1.ClusterConditionStatusCompleted,
			Message: msg,
		})
	} else if initializingCount != 0 {
		msg := fmt.Sprintf("Portworx installation completed on %v/%v nodes, %v nodes remaining", len(storageNodes)-initializingCount, len(storageNodes), initializingCount)
		logrus.Info(msg)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:  pxutil.PortworxComponentName,
			Type:    corev1.ClusterConditionTypeInstall,
			Status:  corev1.ClusterConditionStatusInProgress,
			Message: msg,
		})
	}
}

// updatePortworxUpdateStatus updates portworx update status, expected status: InProgress, Completed
// If there's 1 node that has NodeUpdateInProgress status, mark as PortworxUpdateInProgress
// If all nodes status are updated according to SDK server response and portworx is updating, mark as PortworxUpdateCompleted
func (p *portworx) updatePortworxUpdateStatus(
	cluster *corev1.StorageCluster,
	storageNodes []corev1.StorageNode,
	clusterHash string,
) {
	if len(storageNodes) == 0 {
		logrus.Debugf("no storage nodes provided to update portworx update status")
		return
	}
	nodesToPods, err := p.getNodesToPortworxPods(cluster)
	if err != nil {
		return
	}

	updatingCount := 0
	for _, storageNode := range storageNodes {
		if storageNode.Status.Phase == string(corev1.NodeUpdateStatus) {
			updatingCount += 1
			continue
		}
		portworxPods := nodesToPods[storageNode.Name]
		if len(portworxPods) != 1 {
			logrus.Warnf("failed to get px pod associated with StorageNode %v/%v, found %v portworx pods",
				storageNode.Namespace, storageNode.Name, len(nodesToPods[storageNode.Name]))
			return
		}
		if p.needsStorageNodeUpdate(cluster, &storageNode, portworxPods[0], clusterHash) {
			updatingCount += 1
			continue
		}
	}

	updateCondition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeUpdate)
	if updatingCount == 0 && updateCondition != nil && updateCondition.Status == corev1.ClusterConditionStatusInProgress {
		// TODO: PWX-29455 calculate the upgrade time duration
		msg := "Portworx update completed"
		logrus.Info(msg)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:  pxutil.PortworxComponentName,
			Type:    corev1.ClusterConditionTypeUpdate,
			Status:  corev1.ClusterConditionStatusCompleted,
			Message: msg,
		})
	} else if updatingCount != 0 {
		msg := fmt.Sprintf("Portworx update in progress, %v nodes remaining", updatingCount)
		logrus.Info(msg)
		util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
			Source:  pxutil.PortworxComponentName,
			Type:    corev1.ClusterConditionTypeUpdate,
			Status:  corev1.ClusterConditionStatusInProgress,
			Message: msg,
		})
	}
}

// getPortworxConditions returns a map of portworx conditions with type as keys
func getPortworxConditions(
	cluster *corev1.StorageCluster,
) map[corev1.ClusterConditionType]*corev1.ClusterCondition {
	pxConditions := make(map[corev1.ClusterConditionType]*corev1.ClusterCondition)
	for _, condition := range cluster.Status.Conditions {
		if condition.Source == pxutil.PortworxComponentName {
			pxConditions[condition.Type] = condition.DeepCopy()
		}
	}
	return pxConditions
}

// getStorageClusterState iterates storage cluster condition list and returns the cluster state
func getStorageClusterState(
	cluster *corev1.StorageCluster,
) corev1.ClusterState {
	if len(cluster.Status.Conditions) == 0 {
		return corev1.ClusterStateInit
	}

	// TODO: put more components into consideration in the future, currently the list only contains PX conditions
	pxConditions := getPortworxConditions(cluster)
	if len(pxConditions) == 0 {
		return corev1.ClusterStateInit
	}

	// Portworx delete conditions:
	// InProgress, Completed, Failed
	deleteCondition := pxConditions[corev1.ClusterConditionTypeDelete]
	if deleteCondition != nil {
		return corev1.ClusterStateUninstall
	}

	// DaemonSet migration conditions:
	// Pending, InProgress, Completed
	migrationCondition := pxConditions[corev1.ClusterConditionTypeMigration]
	if migrationCondition != nil && migrationCondition.Status != corev1.ClusterConditionStatusCompleted {
		return corev1.ClusterStateInit
	}

	// Portworx install conditions:
	// InProgress, Completed
	installCondition := pxConditions[corev1.ClusterConditionTypeInstall]
	if installCondition != nil && installCondition.Status == corev1.ClusterConditionStatusInProgress {
		return corev1.ClusterStateInit
	}

	// Portworx runtime state conditions:
	// Online, Offline, NotInQuorum, Unknown
	runtimeCondition := pxConditions[corev1.ClusterConditionTypeRuntimeState]
	if pxConditions[corev1.ClusterConditionTypeRuntimeState] == nil {
		return corev1.ClusterStateInit
	} else if runtimeCondition.Status == corev1.ClusterConditionStatusOnline {
		return corev1.ClusterStateRunning
	} else if runtimeCondition.Status == corev1.ClusterConditionStatusOffline ||
		runtimeCondition.Status == corev1.ClusterConditionStatusNotInQuorum ||
		runtimeCondition.Status == corev1.ClusterConditionStatusUnknown {
		return corev1.ClusterStateDegraded
	}

	return corev1.ClusterStateUnknown
}

func GetKvdbMap(k8sClient client.Client,
	cluster *corev1.StorageCluster,
) map[string]*kvdb_api.BootstrapEntry {
	// If cluster is running internal kvdb, get current bootstrap nodes
	kvdbNodeMap := make(map[string]*kvdb_api.BootstrapEntry)
	if cluster.Spec.Kvdb != nil && cluster.Spec.Kvdb.Internal {
		clusterID := pxutil.GetClusterID(cluster)
		strippedClusterName := strings.ToLower(pxutil.ConfigMapNameRegex.ReplaceAllString(clusterID, ""))
		cmName := fmt.Sprintf("%s%s", pxutil.InternalEtcdConfigMapPrefix, strippedClusterName)

		cm := &v1.ConfigMap{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{
			Name:      cmName,
			Namespace: bootstrapCloudDriveNamespace,
		}, cm)
		if err != nil {
			logrus.Warnf("failed to get internal kvdb bootstrap config map: %v", err)
		}

		// Get the bootstrap entries
		entriesBlob, ok := cm.Data[pxEntriesKey]
		if ok {
			kvdbNodeMap, err = blobToBootstrapEntries([]byte(entriesBlob))
			if err != nil {
				logrus.Warnf("failed to get internal kvdb bootstrap config map: %v", err)
			}
		}
	}
	return kvdbNodeMap
}

func (p *portworx) updateStorageNodes(
	cluster *corev1.StorageCluster,
) error {
	nodeClient := api.NewOpenStorageNodeClient(p.sdkConn)
	ctx, err := pxutil.SetupContextWithToken(context.Background(), cluster, p.k8sClient)
	if err != nil {
		return err
	}
	nodeEnumerateResponse, err := nodeClient.EnumerateWithFilters(
		ctx,
		&api.SdkNodeEnumerateWithFiltersRequest{},
	)
	if err != nil {
		p.updateRemainingStorageNodesWithoutError(cluster, nil)
		return fmt.Errorf("failed to enumerate nodes: %v", err)
	}

	kvdbNodeMap := GetKvdbMap(p.k8sClient, cluster)
	nodesToPods, err := p.getNodesToPortworxPods(cluster)
	if err != nil {
		return err
	}

	// Find all k8s nodes where Portworx is actually running
	currentPxNodes := make(map[string]bool)
	for _, node := range nodeEnumerateResponse.Nodes {
		if node.SchedulerNodeName == "" {
			k8sNode, err := coreops.Instance().SearchNodeByAddresses(
				[]string{node.DataIp, node.MgmtIp, node.Hostname},
			)
			if err != nil {
				logrus.Warnf("Unable to find kubernetes node name for nodeID %v: %v", node.Id, err)
				continue
			}
			node.SchedulerNodeName = k8sNode.Name
		}

		currentPxNodes[node.SchedulerNodeName] = true

		portworxPods := nodesToPods[node.SchedulerNodeName]
		portworxPod := &v1.Pod{}
		if len(portworxPods) != 1 {
			logrus.Warnf("failed to get px pod associated with node %v, found %v portworx pods",
				node.SchedulerNodeName, len(portworxPods))
		} else {
			portworxPod = portworxPods[0]
		}

		storageNode, err := p.createOrUpdateStorageNode(cluster, node, portworxPod)
		if err != nil {
			msg := fmt.Sprintf("Failed to update StorageNode for nodeID %v: %v", node.Id, err)
			p.warningEvent(cluster, util.FailedSyncReason, msg)
			continue
		}

		updateStorageNode := p.needsStorageNodeUpdate(cluster, storageNode, portworxPod, "")
		err = p.updateStorageNodeStatus(storageNode, node, kvdbNodeMap, updateStorageNode)
		if err != nil {
			msg := fmt.Sprintf("Failed to update StorageNode status for nodeID %v: %v", node.Id, err)
			p.warningEvent(cluster, util.FailedSyncReason, msg)
		}
	}

	return p.updateRemainingStorageNodes(cluster, currentPxNodes)
}

func (p *portworx) updateRemainingStorageNodesWithoutError(
	cluster *corev1.StorageCluster,
	currentPxNodes map[string]bool,
) {
	if err := p.updateRemainingStorageNodes(cluster, nil); err != nil {
		logrus.Warn(err)
	}
}

func (p *portworx) updateRemainingStorageNodes(
	cluster *corev1.StorageCluster,
	currentPxNodes map[string]bool,
) error {
	nodesToPods, err := p.getNodesToPortworxPods(cluster)
	if err != nil {
		return err
	}

	storageNodes := &corev1.StorageNodeList{}
	if err = p.k8sClient.List(context.TODO(), storageNodes, &client.ListOptions{}); err != nil {
		return fmt.Errorf("failed to get a list of StorageNode: %v", err)
	}

	for _, storageNode := range storageNodes.Items {
		pxNodeExists := currentPxNodes[storageNode.Name]
		pxPods, pxPodExists := nodesToPods[storageNode.Name]
		if !pxNodeExists && !pxPodExists {
			logrus.Infof("Deleting orphan StorageNode %v/%v",
				storageNode.Namespace, storageNode.Name)

			err = p.k8sClient.Delete(context.TODO(), storageNode.DeepCopy())
			if err != nil && !errors.IsNotFound(err) {
				msg := fmt.Sprintf("Failed to delete StorageNode %v/%v: %v",
					storageNode.Namespace, storageNode.Name, err)
				p.warningEvent(cluster, util.FailedSyncReason, msg)
			}
		} else if !pxNodeExists && pxPodExists {
			// If the portworx pod exists, but corresponding portworx node is missing in
			// enumerate, then it's either still initializing, upgrading, failed or removed from cluster.
			// If it's not initializing, upgrading or failed, then change the node phase to Unknown.
			if len(pxPods) != 1 {
				logrus.Warnf("failed to get px pod associated with StorageNode %v/%v, found %v portworx pods",
					storageNode.Namespace, storageNode.Name, len(pxPods))
				continue
			} else if p.needsStorageNodeUpdate(cluster, &storageNode, pxPods[0], "") {
				// Update the NodeState to Updating
				nodeStateCondition := &corev1.NodeCondition{
					Type:   corev1.NodeStateCondition,
					Status: corev1.NodeUpdateStatus,
				}
				operatorops.Instance().UpdateStorageNodeCondition(&storageNode.Status, nodeStateCondition)
			}
			newPhase := getStorageNodePhase(&storageNode.Status)
			if newPhase != string(corev1.NodeInitStatus) &&
				newPhase != string(corev1.NodeUpdateStatus) &&
				newPhase != string(corev1.NodeFailedStatus) {
				newPhase = string(corev1.NodeUnknownStatus)
			}
			if storageNode.Status.Phase != newPhase {
				storageNodeCopy := storageNode.DeepCopy()
				storageNodeCopy.Status.Phase = newPhase
				logrus.Infof("Updating StorageNode %v/%v status",
					storageNode.Namespace, storageNode.Name)
				err = p.k8sClient.Status().Update(context.TODO(), storageNodeCopy)
				if err != nil && !errors.IsNotFound(err) {
					msg := fmt.Sprintf("Failed to update StorageNode %v/%v: %v",
						storageNode.Namespace, storageNode.Name, err)
					p.warningEvent(cluster, util.FailedSyncReason, msg)
				}
			}
		}
	}
	return nil
}

// getNodesToPortworxPods find all k8s nodes where Portworx pods are running
func (p *portworx) getNodesToPortworxPods(
	cluster *corev1.StorageCluster,
) (map[string][]*v1.Pod, error) {
	pxLabels := p.GetSelectorLabels()
	portworxPodList := &v1.PodList{}
	err := p.k8sClient.List(
		context.TODO(),
		portworxPodList,
		&client.ListOptions{
			Namespace:     cluster.Namespace,
			LabelSelector: labels.SelectorFromSet(pxLabels),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get list of portworx pods. %v", err)
	}

	// Create a map of node name to px pod list
	nodesToPods := make(map[string][]*v1.Pod)
	for _, pod := range portworxPodList.Items {
		controllerRef := metav1.GetControllerOf(&pod)
		if controllerRef != nil && controllerRef.UID == cluster.UID && len(pod.Spec.NodeName) != 0 {
			nodesToPods[pod.Spec.NodeName] = append(nodesToPods[pod.Spec.NodeName], pod.DeepCopy())
		}
	}
	return nodesToPods, nil
}

// needsStorageNodeUpdate checks if a storage node needs update, there are 3 cases:
// 1. check if storage node is updated according to px pod hash
// 2. check if storage node is updated according to StorageCluster hash
// 3. hash is updated but still needs node update
func (p *portworx) needsStorageNodeUpdate(
	cluster *corev1.StorageCluster,
	storageNode *corev1.StorageNode,
	portworxPod *v1.Pod,
	clusterHash string,
) bool {
	// storage cluster hash will override pod hash to check overall update progress
	expectedHash := portworxPod.Labels[util.DefaultStorageClusterUniqueLabelKey]
	if clusterHash != "" {
		// checking cluster level if update is needed
		if !p.IsPodUpdated(cluster, portworxPod) {
			return true
		}
		expectedHash = clusterHash
	}
	if expectedHash == "" {
		return false
	}
	storageNodeHash := storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey]
	return storageNodeHash != "" && storageNodeHash != expectedHash
}

func (p *portworx) createOrUpdateStorageNode(
	cluster *corev1.StorageCluster,
	node *api.StorageNode,
	pod *v1.Pod,
) (*corev1.StorageNode, error) {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	storageNode := &corev1.StorageNode{}
	getErr := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      node.SchedulerNodeName,
			Namespace: cluster.Namespace,
		},
		storageNode,
	)
	if getErr != nil && !errors.IsNotFound(getErr) {
		return nil, getErr
	}

	originalStorageNode := storageNode.DeepCopy()
	originalHash := originalStorageNode.Labels[util.DefaultStorageClusterUniqueLabelKey]
	storageNode.Name = node.SchedulerNodeName
	storageNode.Namespace = cluster.Namespace
	storageNode.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	storageNode.Labels = p.GetSelectorLabels()
	// If px pod is ready, update storage node hash, otherwise preserve the old hash
	if podutil.IsPodReady(pod) {
		storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey] = pod.Labels[util.DefaultStorageClusterUniqueLabelKey]
	} else if originalHash != "" {
		storageNode.Labels[util.DefaultStorageClusterUniqueLabelKey] = originalHash
	}

	if version, ok := node.NodeLabels[labelPortworxVersion]; ok {
		storageNode.Spec.Version = version
	} else {
		partitions := strings.Split(cluster.Spec.Image, ":")
		if len(partitions) > 1 {
			storageNode.Spec.Version = partitions[len(partitions)-1]
		}
	}

	var err error
	if errors.IsNotFound(getErr) {
		logrus.Infof("Creating StorageNode %s/%s", storageNode.Namespace, storageNode.Name)
		err = p.k8sClient.Create(context.TODO(), storageNode)
	} else if !reflect.DeepEqual(originalStorageNode, storageNode) {
		logrus.Debugf("Updating StorageNode %s/%s", storageNode.Namespace, storageNode.Name)
		err = p.k8sClient.Update(context.TODO(), storageNode)
	}
	return storageNode, err
}

func (p *portworx) updateStorageNodeStatus(
	storageNode *corev1.StorageNode,
	node *api.StorageNode,
	kvdbNodeMap map[string]*kvdb_api.BootstrapEntry,
	updateStorageNode bool,
) error {
	if storageNode.Status.Storage == nil {
		storageNode.Status.Storage = &corev1.StorageStatus{}
	}
	originalTotalSize := storageNode.Status.Storage.TotalSize
	storageNode.Status.Storage.TotalSize = *resource.NewQuantity(0, resource.BinarySI)
	originalUsedSize := storageNode.Status.Storage.UsedSize
	storageNode.Status.Storage.UsedSize = *resource.NewQuantity(0, resource.BinarySI)
	originalStorageNodeStatus := storageNode.Status.DeepCopy()

	storageNode.Status.NodeUID = node.Id
	storageNode.Status.Network = &corev1.NetworkStatus{
		DataIP: node.DataIp,
		MgmtIP: node.MgmtIp,
	}
	nodeStateCondition := &corev1.NodeCondition{
		Type:   corev1.NodeStateCondition,
		Status: mapNodeStatus(node.Status),
	}
	if updateStorageNode {
		nodeStateCondition.Status = corev1.NodeUpdateStatus
	}

	var (
		totalSizeInBytes, usedSizeInBytes int64
	)
	for _, pool := range node.Pools {
		totalSizeInBytes += int64(pool.TotalSize)
		usedSizeInBytes += int64(pool.Used)
	}
	totalSize := resource.NewQuantity(totalSizeInBytes, resource.BinarySI)
	usedSize := resource.NewQuantity(usedSizeInBytes, resource.BinarySI)
	hasStorage := !totalSize.IsZero()

	kvdbEntry, present := kvdbNodeMap[storageNode.Status.NodeUID]
	hasKvdb := present && kvdbEntry != nil
	if hasKvdb {
		nodeKVDBCondition := &corev1.NodeCondition{
			Type:   corev1.NodeKVDBCondition,
			Status: mapKVDBState(kvdbEntry.State),
			Message: fmt.Sprintf("node is kvdb %s listening on %s",
				mapKVDBNodeType(kvdbEntry.Type), kvdbEntry.IP),
		}

		operatorops.Instance().UpdateStorageNodeCondition(&storageNode.Status, nodeKVDBCondition)
	} else {
		// remove if present
		k := 0
		for _, cond := range storageNode.Status.Conditions {
			if cond.Type != corev1.NodeKVDBCondition {
				storageNode.Status.Conditions[k] = cond
				k++
			}
		}

		storageNode.Status.Conditions = storageNode.Status.Conditions[:k]
	}

	storageNode.Status.NodeAttributes = &corev1.NodeAttributes{
		Storage: &hasStorage,
		KVDB:    &hasKvdb,
	}

	operatorops.Instance().UpdateStorageNodeCondition(&storageNode.Status, nodeStateCondition)
	storageNode.Status.Phase = getStorageNodePhase(&storageNode.Status)

	if os, exists := node.NodeLabels[labelOperatingSystem]; exists {
		storageNode.Status.OperatingSystem = os
	}
	if kernel, exists := node.NodeLabels[labelKernelVersion]; exists {
		storageNode.Status.KernelVersion = kernel
	}

	if !reflect.DeepEqual(originalStorageNodeStatus, &storageNode.Status) ||
		totalSize.Cmp(originalTotalSize) != 0 ||
		usedSize.Cmp(originalUsedSize) != 0 {
		storageNode.Status.Storage.TotalSize = *totalSize
		storageNode.Status.Storage.UsedSize = *usedSize
		logrus.Debugf("Updating StorageNode %s/%s status",
			storageNode.Namespace, storageNode.Name)
		return p.k8sClient.Status().Update(context.TODO(), storageNode)
	}

	return nil
}

func mapPortworxRuntimeStatus(status api.Status) corev1.ClusterConditionStatus {
	switch status {
	case api.Status_STATUS_NONE:
		fallthrough
	case api.Status_STATUS_INIT:
		fallthrough
	case api.Status_STATUS_OFFLINE:
		fallthrough
	case api.Status_STATUS_ERROR:
		return corev1.ClusterConditionStatusOffline

	case api.Status_STATUS_NOT_IN_QUORUM:
		fallthrough
	case api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE:
		return corev1.ClusterConditionStatusNotInQuorum

	case api.Status_STATUS_OK:
		fallthrough
	case api.Status_STATUS_MAINTENANCE:
		fallthrough
	case api.Status_STATUS_NEEDS_REBOOT:
		fallthrough
	case api.Status_STATUS_STORAGE_DOWN:
		fallthrough
	case api.Status_STATUS_STORAGE_DEGRADED:
		fallthrough
	case api.Status_STATUS_STORAGE_REBALANCE:
		fallthrough
	case api.Status_STATUS_STORAGE_DRIVE_REPLACE:
		return corev1.ClusterConditionStatusOnline

	case api.Status_STATUS_DECOMMISSION:
		fallthrough
	default:
		return corev1.ClusterConditionStatusUnknown
	}
}

func mapKVDBState(state kvdb_api.NodeState) corev1.NodeConditionStatus {
	switch state {
	case kvdb_api.BootstrapNodeStateInProgress:
		return corev1.NodeInitStatus
	case kvdb_api.BootstrapNodeStateOperational:
		return corev1.NodeOnlineStatus
	case kvdb_api.BootstrapNodeStateSuspectDown:
		return corev1.NodeOfflineStatus
	case kvdb_api.BootstrapNodeStateNone:
		fallthrough
	default:
		return corev1.NodeUnknownStatus
	}
}

func mapKVDBNodeType(nodeType kvdb_api.NodeType) string {
	switch nodeType {
	case kvdb_api.BootstrapNodeTypeLeader:
		return "leader"
	case kvdb_api.BootstrapNodeTypeMember:
		return "member"
	case kvdb_api.BootstrapNodeTypeNone:
		fallthrough
	default:
		return ""
	}
}

func mapNodeStatus(status api.Status) corev1.NodeConditionStatus {
	switch status {
	case api.Status_STATUS_NONE:
		fallthrough
	case api.Status_STATUS_OFFLINE:
		fallthrough
	case api.Status_STATUS_ERROR:
		fallthrough
	case api.Status_STATUS_NEEDS_REBOOT:
		return corev1.NodeOfflineStatus

	case api.Status_STATUS_INIT:
		return corev1.NodeInitStatus

	case api.Status_STATUS_NOT_IN_QUORUM:
		fallthrough
	case api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE:
		return corev1.NodeNotInQuorumStatus

	case api.Status_STATUS_MAINTENANCE:
		return corev1.NodeMaintenanceStatus

	case api.Status_STATUS_OK:
		return corev1.NodeOnlineStatus

	case api.Status_STATUS_DECOMMISSION:
		return corev1.NodeDecommissionedStatus

	case api.Status_STATUS_STORAGE_DEGRADED:
		fallthrough
	case api.Status_STATUS_STORAGE_DOWN:
		fallthrough
	case api.Status_STATUS_STORAGE_REBALANCE:
		fallthrough
	case api.Status_STATUS_STORAGE_DRIVE_REPLACE:
		return corev1.NodeDegradedStatus

	default:
		return corev1.NodeUnknownStatus
	}
}

func getStorageNodePhase(status *corev1.NodeStatus) string {
	var nodeInitCondition *corev1.NodeCondition
	var nodeStateCondition *corev1.NodeCondition

	for _, condition := range status.Conditions {
		if condition.Type == corev1.NodeInitCondition {
			nodeInitCondition = condition.DeepCopy()
		} else if condition.Type == corev1.NodeStateCondition {
			nodeStateCondition = condition.DeepCopy()
		}
	}

	if nodeInitCondition == nil || nodeInitCondition.Status == corev1.NodeSucceededStatus {
		if nodeStateCondition != nil && nodeStateCondition.Status != "" {
			return string(nodeStateCondition.Status)
		}
		return string(corev1.NodeInitStatus)
	} else if nodeStateCondition == nil ||
		nodeStateCondition.LastTransitionTime.Before(&nodeInitCondition.LastTransitionTime) {
		// If portworx restarts and updates the NodeInit condition, it would have a
		// more recent timestamp than operator updated NodeState condition.
		return string(nodeInitCondition.Status)
	}
	return string(nodeStateCondition.Status)
}

func blobToBootstrapEntries(
	entriesBlob []byte,
) (map[string]*kvdb_api.BootstrapEntry, error) {

	var bEntries []*kvdb_api.BootstrapEntry
	if err := json.Unmarshal(entriesBlob, &bEntries); err != nil {
		return nil, err
	}

	// return as a map by ID to facilitate callers
	retMap := make(map[string]*kvdb_api.BootstrapEntry)
	for _, e := range bEntries {
		retMap[e.ID] = e
	}
	return retMap, nil
}
