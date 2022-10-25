package portworx

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	kvdb_api "github.com/portworx/kvdb/api/bootstrap"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// pxEntriesKey is key which holds all the bootstrap entries
	pxEntriesKey = "px-entries"
)

func (p *portworx) UpdateStorageClusterStatus(
	cluster *corev1.StorageCluster,
) error {
	if cluster.Status.Phase == "" {
		cluster.Status.ClusterName = cluster.Name
		cluster.Status.Phase = string(corev1.ClusterInit)
		return nil
	}

	if !pxutil.IsPortworxEnabled(cluster) {
		cluster.Status.Phase = string(corev1.ClusterOnline)
		return nil
	}

	var err error
	p.sdkConn, err = pxutil.GetPortworxConn(p.sdkConn, p.k8sClient, cluster.Namespace)
	if err != nil {
		p.updateRemainingStorageNodesWithoutError(cluster, nil)
		if cluster.Status.Phase == string(corev1.ClusterInit) &&
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

	newClusterStatus := mapClusterStatus(pxCluster.Cluster.Status)
	if cluster.Status.Phase != string(corev1.ClusterOnline) &&
		newClusterStatus == corev1.ClusterOnline {
		msg := fmt.Sprintf("Storage cluster %v online", cluster.GetName())
		p.normalEvent(cluster, util.ClusterOnlineReason, msg)
	}

	cluster.Status.Phase = string(newClusterStatus)
	cluster.Status.ClusterName = pxCluster.Cluster.Name
	cluster.Status.ClusterUID = pxCluster.Cluster.Id

	return p.updateStorageNodes(cluster)
}

func (p *portworx) getKvdbMap(
	cluster *corev1.StorageCluster,
) map[string]*kvdb_api.BootstrapEntry {
	// If cluster is running internal kvdb, get current bootstrap nodes
	kvdbNodeMap := make(map[string]*kvdb_api.BootstrapEntry)
	if cluster.Spec.Kvdb != nil && cluster.Spec.Kvdb.Internal {
		clusterID := pxutil.GetClusterID(cluster)
		strippedClusterName := strings.ToLower(pxutil.ConfigMapNameRegex.ReplaceAllString(clusterID, ""))
		cmName := fmt.Sprintf("%s%s", pxutil.InternalEtcdConfigMapPrefix, strippedClusterName)

		cm := &v1.ConfigMap{}
		err := p.k8sClient.Get(context.TODO(), types.NamespacedName{
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

	kvdbNodeMap := p.getKvdbMap(cluster)

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

		storageNode, err := p.createOrUpdateStorageNode(cluster, node)
		if err != nil {
			msg := fmt.Sprintf("Failed to update StorageNode for nodeID %v: %v", node.Id, err)
			p.warningEvent(cluster, util.FailedSyncReason, msg)
			continue
		}

		err = p.updateStorageNodeStatus(storageNode, node, kvdbNodeMap)
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
	// Find all k8s nodes where Portworx pods are running
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
		return fmt.Errorf("failed to get list of portworx pods. %v", err)
	}

	currentPxPodNodes := make(map[string]bool)
	for _, pod := range portworxPodList.Items {
		controllerRef := metav1.GetControllerOf(&pod)
		if controllerRef != nil && controllerRef.UID == cluster.UID && len(pod.Spec.NodeName) != 0 {
			currentPxPodNodes[pod.Spec.NodeName] = true
		}
	}

	storageNodes := &corev1.StorageNodeList{}
	if err = p.k8sClient.List(context.TODO(), storageNodes, &client.ListOptions{}); err != nil {
		return fmt.Errorf("failed to get a list of StorageNode: %v", err)
	}

	for _, storageNode := range storageNodes.Items {
		pxNodeExists := currentPxNodes[storageNode.Name]
		pxPodExists := currentPxPodNodes[storageNode.Name]
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
			// enumerate, then it's either still initializing, failed or removed from cluster.
			// If it's not initializing or failed, then change the node phase to Unknown.
			newPhase := getStorageNodePhase(&storageNode.Status)
			if newPhase != string(corev1.NodeInitStatus) &&
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

func (p *portworx) createOrUpdateStorageNode(
	cluster *corev1.StorageCluster,
	node *api.StorageNode,
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
	storageNode.Name = node.SchedulerNodeName
	storageNode.Namespace = cluster.Namespace
	storageNode.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	storageNode.Labels = p.GetSelectorLabels()

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
) error {
	originalTotalSize := storageNode.Status.Storage.TotalSize
	storageNode.Status.Storage.TotalSize = *resource.NewQuantity(0, resource.BinarySI)
	originalUsedSize := storageNode.Status.Storage.UsedSize
	storageNode.Status.Storage.UsedSize = *resource.NewQuantity(0, resource.BinarySI)
	originalStorageNodeStatus := storageNode.Status.DeepCopy()

	storageNode.Status.NodeUID = node.Id
	storageNode.Status.Network = corev1.NetworkStatus{
		DataIP: node.DataIp,
		MgmtIP: node.MgmtIp,
	}
	nodeStateCondition := &corev1.NodeCondition{
		Type:   corev1.NodeStateCondition,
		Status: mapNodeStatus(node.Status),
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

	kvdbEntry, present := kvdbNodeMap[storageNode.Status.NodeUID]
	if present && kvdbEntry != nil {
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

	operatorops.Instance().UpdateStorageNodeCondition(&storageNode.Status, nodeStateCondition)
	storageNode.Status.Phase = getStorageNodePhase(&storageNode.Status)

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

func mapClusterStatus(status api.Status) corev1.ClusterConditionStatus {
	switch status {
	case api.Status_STATUS_NONE:
		fallthrough
	case api.Status_STATUS_INIT:
		fallthrough
	case api.Status_STATUS_OFFLINE:
		fallthrough
	case api.Status_STATUS_ERROR:
		return corev1.ClusterOffline

	case api.Status_STATUS_NOT_IN_QUORUM:
		fallthrough
	case api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE:
		return corev1.ClusterNotInQuorum

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
		return corev1.ClusterOnline

	case api.Status_STATUS_DECOMMISSION:
		fallthrough
	default:
		return corev1.ClusterUnknown
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
		fallthrough
	case api.Status_STATUS_STORAGE_DOWN:
		return corev1.NodeOnlineStatus

	case api.Status_STATUS_DECOMMISSION:
		return corev1.NodeDecommissionedStatus

	case api.Status_STATUS_STORAGE_DEGRADED:
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
