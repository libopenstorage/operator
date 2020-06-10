package portworx

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (p *portworx) UpdateStorageClusterStatus(
	cluster *corev1alpha1.StorageCluster,
) error {
	if cluster.Status.Phase == "" {
		cluster.Status.ClusterName = cluster.Name
		cluster.Status.Phase = string(corev1alpha1.ClusterInit)
		return nil
	}

	if !pxutil.IsPortworxEnabled(cluster) {
		cluster.Status.Phase = string(corev1alpha1.ClusterOnline)
		return nil
	}

	var err error
	p.sdkConn, err = pxutil.GetPortworxConn(p.sdkConn, p.k8sClient, cluster.Namespace)
	if err != nil {
		p.updateRemainingStorageNodesWithoutError(cluster, nil)
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
	if cluster.Status.Phase != string(corev1alpha1.ClusterOnline) &&
		newClusterStatus == corev1alpha1.ClusterOnline {
		msg := fmt.Sprintf("Storage cluster %v online", cluster.GetName())
		p.normalEvent(cluster, util.ClusterOnlineReason, msg)
	}

	cluster.Status.Phase = string(newClusterStatus)
	cluster.Status.ClusterName = pxCluster.Cluster.Name
	cluster.Status.ClusterUID = pxCluster.Cluster.Id

	return p.updateStorageNodes(cluster)
}

func (p *portworx) updateStorageNodes(
	cluster *corev1alpha1.StorageCluster,
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

		storageNode, err := p.updateStorageNodeSpec(cluster, node)
		if err != nil {
			msg := fmt.Sprintf("Failed to update StorageNode for nodeID %v: %v", node.Id, err)
			p.warningEvent(cluster, util.FailedSyncReason, msg)
			continue
		}

		err = p.updateStorageNodeStatus(storageNode, node)
		if err != nil {
			msg := fmt.Sprintf("Failed to update StorageNode status for nodeID %v: %v", node.Id, err)
			p.warningEvent(cluster, util.FailedSyncReason, msg)
		}
	}

	return p.updateRemainingStorageNodes(cluster, currentPxNodes)
}

func (p *portworx) updateRemainingStorageNodesWithoutError(
	cluster *corev1alpha1.StorageCluster,
	currentPxNodes map[string]bool,
) {
	if err := p.updateRemainingStorageNodes(cluster, nil); err != nil {
		logrus.Warn(err)
	}
}

func (p *portworx) updateRemainingStorageNodes(
	cluster *corev1alpha1.StorageCluster,
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

	storageNodes := &corev1alpha1.StorageNodeList{}
	if err = p.k8sClient.List(context.TODO(), storageNodes, &client.ListOptions{}); err != nil {
		return fmt.Errorf("failed to get a list of StorageNode: %v", err)
	}

	for _, storageNode := range storageNodes.Items {
		pxNodeExists := currentPxNodes[storageNode.Name]
		pxPodExists := currentPxPodNodes[storageNode.Name]
		if !pxNodeExists && !pxPodExists {
			logrus.Debugf("Deleting orphan StorageNode %v/%v",
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
			if newPhase != string(corev1alpha1.NodeInitStatus) &&
				newPhase != string(corev1alpha1.NodeFailedStatus) {
				newPhase = string(corev1alpha1.NodeUnknownStatus)
			}
			if storageNode.Status.Phase != newPhase {
				storageNodeCopy := storageNode.DeepCopy()
				storageNodeCopy.Status.Phase = newPhase
				logrus.Debugf("Updating StorageNode %v/%v status",
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

func (p *portworx) updateStorageNodeSpec(
	cluster *corev1alpha1.StorageCluster,
	node *api.StorageNode,
) (*corev1alpha1.StorageNode, error) {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	storageNode := &corev1alpha1.StorageNode{}
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
		logrus.Debugf("Creating StorageNode %s/%s", storageNode.Namespace, storageNode.Name)
		err = p.k8sClient.Create(context.TODO(), storageNode)
	} else if !reflect.DeepEqual(originalStorageNode, storageNode) {
		logrus.Debugf("Updating StorageNode %s/%s", storageNode.Namespace, storageNode.Name)
		err = p.k8sClient.Update(context.TODO(), storageNode)
	}
	return storageNode, err
}

func (p *portworx) updateStorageNodeStatus(
	storageNode *corev1alpha1.StorageNode,
	node *api.StorageNode,
) error {
	originalStorageNodeStatus := storageNode.Status.DeepCopy()
	storageNode.Status.NodeUID = node.Id
	storageNode.Status.Network = corev1alpha1.NetworkStatus{
		DataIP: node.DataIp,
		MgmtIP: node.MgmtIp,
	}
	nodeStateCondition := &corev1alpha1.NodeCondition{
		Type:   corev1alpha1.NodeStateCondition,
		Status: mapNodeStatus(node.Status),
	}
	operatorops.Instance().UpdateStorageNodeCondition(&storageNode.Status, nodeStateCondition)
	storageNode.Status.Phase = getStorageNodePhase(&storageNode.Status)

	if !reflect.DeepEqual(originalStorageNodeStatus, &storageNode.Status) {
		logrus.Debugf("Updating StorageNode %s/%s status",
			storageNode.Namespace, storageNode.Name)
		return p.k8sClient.Status().Update(context.TODO(), storageNode)
	}

	return nil
}

func mapClusterStatus(status api.Status) corev1alpha1.ClusterConditionStatus {
	switch status {
	case api.Status_STATUS_NONE:
		fallthrough
	case api.Status_STATUS_INIT:
		fallthrough
	case api.Status_STATUS_OFFLINE:
		fallthrough
	case api.Status_STATUS_ERROR:
		return corev1alpha1.ClusterOffline

	case api.Status_STATUS_NOT_IN_QUORUM:
		fallthrough
	case api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE:
		return corev1alpha1.ClusterNotInQuorum

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
		return corev1alpha1.ClusterOnline

	case api.Status_STATUS_DECOMMISSION:
		fallthrough
	default:
		return corev1alpha1.ClusterUnknown
	}
}

func mapNodeStatus(status api.Status) corev1alpha1.NodeConditionStatus {
	switch status {
	case api.Status_STATUS_NONE:
		fallthrough
	case api.Status_STATUS_OFFLINE:
		fallthrough
	case api.Status_STATUS_ERROR:
		fallthrough
	case api.Status_STATUS_NEEDS_REBOOT:
		return corev1alpha1.NodeOfflineStatus

	case api.Status_STATUS_INIT:
		return corev1alpha1.NodeInitStatus

	case api.Status_STATUS_NOT_IN_QUORUM:
		fallthrough
	case api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE:
		return corev1alpha1.NodeNotInQuorumStatus

	case api.Status_STATUS_MAINTENANCE:
		return corev1alpha1.NodeMaintenanceStatus

	case api.Status_STATUS_OK:
		fallthrough
	case api.Status_STATUS_STORAGE_DOWN:
		return corev1alpha1.NodeOnlineStatus

	case api.Status_STATUS_DECOMMISSION:
		return corev1alpha1.NodeDecommissionedStatus

	case api.Status_STATUS_STORAGE_DEGRADED:
		fallthrough
	case api.Status_STATUS_STORAGE_REBALANCE:
		fallthrough
	case api.Status_STATUS_STORAGE_DRIVE_REPLACE:
		return corev1alpha1.NodeDegradedStatus

	default:
		return corev1alpha1.NodeUnknownStatus
	}
}

func getStorageNodePhase(status *corev1alpha1.NodeStatus) string {
	latestTime := metav1.NewTime(time.Time{})
	var latestCondition *corev1alpha1.NodeCondition

	for _, condition := range status.Conditions {
		if latestTime.Before(&condition.LastTransitionTime) ||
			latestTime.Equal(&condition.LastTransitionTime) {
			latestCondition = condition.DeepCopy()
			latestTime = condition.LastTransitionTime
		}
	}

	// If no condition or status found return Initializing phase.
	// Also if the InitCondition is the latest condition and it has succeeded,
	// then keep the node phase as Initializing
	if latestCondition == nil || latestCondition.Status == "" ||
		(latestCondition.Type == corev1alpha1.NodeInitCondition &&
			latestCondition.Status == corev1alpha1.NodeSucceededStatus) {
		return string(corev1alpha1.NodeInitStatus)
	}
	return string(latestCondition.Status)
}
