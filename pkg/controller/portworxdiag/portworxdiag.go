package portworxdiag

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/libopenstorage/operator/drivers/storage"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	diagv1 "github.com/libopenstorage/operator/pkg/apis/portworx/v1"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// ControllerName is the name of the controller
	ControllerName      = "portworxdiag-controller"
	validateCRDInterval = 5 * time.Second
	validateCRDTimeout  = 1 * time.Minute
	crdBasePath         = "/crds"
	portworxDiagCRDFile = "portworx.io_portworxdiags.yaml"
)

var _ reconcile.Reconciler = &Controller{}

var (
	controllerKind = diagv1.SchemeGroupVersion.WithKind("PortworxDiag")
	crdBaseDir     = getCRDBasePath
)

// Controller reconciles a StorageCluster object
type Controller struct {
	client     client.Client
	scheme     *runtime.Scheme
	recorder   record.EventRecorder
	podControl k8scontroller.PodControlInterface
	Driver     storage.Driver
	ctrl       controller.Controller
	grpcConn   *grpc.ClientConn
}

// Init initialize the portworx diag controller.
func (c *Controller) Init(mgr manager.Manager) error {
	c.client = mgr.GetClient()
	c.scheme = mgr.GetScheme()
	c.recorder = mgr.GetEventRecorderFor(ControllerName)

	var err error
	// Create a new controller
	c.ctrl, err = controller.New(ControllerName, mgr, controller.Options{Reconciler: c})
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("error getting kubernetes client: %v", err)
	}
	// Create pod control interface object to manage pods under storage cluster
	c.podControl = k8scontroller.RealPodControl{
		KubeClient: clientset,
		Recorder:   c.recorder,
	}

	return nil
}

// StartWatch starts the watch on the PortworxDiag object type.
func (c *Controller) StartWatch() error {
	if c.ctrl == nil {
		return fmt.Errorf("controller not initialized to start a watch")
	}

	err := c.ctrl.Watch(
		&source.Kind{Type: &diagv1.PortworxDiag{}},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		return fmt.Errorf("failed to watch PortworxDiags: %v", err)
	}

	// Watch for changes to Pods that belong to PortworxDiag object
	err = c.ctrl.Watch(
		&source.Kind{Type: &v1.Pod{}},
		&handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &diagv1.PortworxDiag{},
		},
	)
	if err != nil {
		return err
	}

	return nil
}

func (c *Controller) getDiagObject(ctx context.Context, req reconcile.Request) (*diagv1.PortworxDiag, bool, error) {
	// List all PortworxDiag instances, pick out ours, and set it to "Pending" if other diags are running
	diags := &diagv1.PortworxDiagList{}
	err := c.client.List(context.TODO(), diags, &client.ListOptions{Namespace: req.Namespace})
	if err != nil {
		// Error reading the objects - requeue the request (no items returns a successful empty list).
		return nil, false, err
	}
	if len(diags.Items) == 0 {
		return nil, false, nil
	}

	// Sort all diags by creation timestamp
	sort.Slice(diags.Items, func(i, j int) bool {
		return diags.Items[i].CreationTimestamp.Before(&diags.Items[j].CreationTimestamp)
	})

	var diag *diagv1.PortworxDiag
	otherDiagRunning := false
	for _, d := range diags.Items { // Run diags in order of creation: if another is running before us, it was created earlier so let it go
		if d.Name == req.Name {
			diag = d.DeepCopy()
			break
		}
		if d.Status.Phase == diagv1.DiagStatusInProgress {
			otherDiagRunning = true
		}
	}
	return diag, otherDiagRunning, nil
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logrus.WithFields(map[string]interface{}{
		"Request.Namespace": req.Namespace,
		"Request.Name":      req.Name,
	})
	log.Infof("Reconciling PortworxDiag")

	diag, otherDiagRunning, err := c.getDiagObject(ctx, req)
	if err != nil {
		return reconcile.Result{}, err
	}
	if diag == nil {
		// Request objects not found, could have been deleted after reconcile request.
		return reconcile.Result{}, nil
	}

	if otherDiagRunning {
		logrus.Infof("Other diag is running, waiting for it to complete before starting a new one")
		err = c.patchPhase(diag, diagv1.DiagStatusPending, "Waiting for other PortworxDiag objects to complete before starting this one")
		if err != nil {
			k8s.WarningEvent(c.recorder, diag, util.FailedSyncReason, err.Error())
			return reconcile.Result{}, err
		}

		return reconcile.Result{}, nil
	}

	if err := c.syncPortworxDiag(diag); err != nil {
		// Ignore object revision conflict errors, as PortworxDiag could have been edited.
		// The next reconcile loop should be able to resolve the issue.
		if strings.Contains(err.Error(), k8s.UpdateRevisionConflictErr) {
			logrus.Warnf("failed to sync PortworxDiag %s: %v", req, err)
			return reconcile.Result{}, nil
		}

		k8s.WarningEvent(c.recorder, diag, util.FailedSyncReason, err.Error())
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (c *Controller) fetchSTC() (*corev1.StorageCluster, error) {
	stcs := &corev1.StorageClusterList{}
	err := c.client.List(context.TODO(), stcs, &client.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list StorageClusters: %v", err)
	}
	if len(stcs.Items) > 1 {
		return nil, fmt.Errorf("more than one StorageCluster found, stopping")
	}
	if len(stcs.Items) == 0 {
		return nil, fmt.Errorf("no StorageCluster found, stopping")
	}
	return &stcs.Items[0], nil
}

func (c *Controller) getDiagPods(ns, diagName string) (*v1.PodList, error) {
	pods := &v1.PodList{}
	err := c.client.List(context.TODO(), pods, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"name":                       PortworxDiagLabel,
			diagv1.LabelPortworxDiagName: diagName,
		}),
		Namespace: ns,
	})
	if err == nil || errors.IsNotFound(err) {
		return pods, nil
	}
	return nil, fmt.Errorf("failed to list existing diag pods: %v", err)
}

func getNodeToPodMap(podList *v1.PodList) map[string]*v1.Pod {
	pods := make(map[string]*v1.Pod)
	if podList == nil {
		return pods
	}
	for _, p := range podList.Items {
		tmp := p // To avoid referencing the loop variable
		pods[p.Spec.NodeName] = &tmp
	}
	return pods
}

func getNodeIDToStatusMap(nodeStatuses []diagv1.NodeStatus) map[string]string {
	statuses := make(map[string]string)
	for _, s := range nodeStatuses {
		if s.NodeID == "" {
			continue
		}
		statuses[s.NodeID] = s.Status
	}
	return statuses
}

func (c *Controller) getNodeIDsWithSelectedVolumes(diag *diagv1.PortworxDiag, stc *corev1.StorageCluster) ([]string, error) {
	if diag == nil || diag.Spec.Portworx == nil || diag.Spec.Portworx.VolumeSelector.IDs == nil || diag.Spec.Portworx.VolumeSelector.Labels == nil {
		return []string{}, nil
	}

	nodeIDMap := map[string]bool{}

	volumeClient := api.NewOpenStorageVolumeClient(c.grpcConn)
	ctx, err := pxutil.SetupContextWithToken(context.Background(), stc, c.client)
	if err != nil {
		return nil, err
	}

	for _, id := range diag.Spec.Portworx.VolumeSelector.IDs {

		// Inspect volume to get location of replicas
		volumeInspectResponse, err := volumeClient.Inspect(
			ctx,
			&api.SdkVolumeInspectRequest{
				VolumeId: id,
			},
		)
		if err != nil {
			logrus.Warnf("Failed to inspect volume %s: %v", id, err)
			// Try the next volume
			continue
		}

		// Add all nodes that have a replica of this volume
		for _, rs := range volumeInspectResponse.Volume.ReplicaSets {
			for _, r := range rs.Nodes {
				nodeIDMap[r] = true
			}
		}
	}

	// Inspect all volumes by labels
	if len(diag.Spec.Portworx.VolumeSelector.Labels) > 0 {
		volumeInspectResponse, err := volumeClient.InspectWithFilters(
			ctx,
			&api.SdkVolumeInspectWithFiltersRequest{
				Labels: diag.Spec.Portworx.VolumeSelector.Labels,
			},
		)
		if err != nil {
			logrus.Warnf("Failed to inspect volumes with labels %v: %v", diag.Spec.Portworx.VolumeSelector.Labels, err)
		} else {
			for _, v := range volumeInspectResponse.Volumes {

				// Add all nodes that have a replica of this volume
				for _, rs := range v.Volume.ReplicaSets {
					for _, r := range rs.Nodes {
						nodeIDMap[r] = true
					}
				}
			}
		}
	}

	// Convert to slice
	nodeIDs := make([]string, 0, len(nodeIDMap))
	for k := range nodeIDMap {
		nodeIDs = append(nodeIDs, k)
	}

	return nodeIDs, nil
}

type podReconcileStatus struct {
	podsToDelete         []*v1.Pod
	nodesToCreatePodsFor []string
	nodeStatusesToAdd    []*diagv1.NodeStatus
}

func shouldPodBeOnNode(targetNodeID string, targetNodeName string, k8sNodes []v1.Node, nodeIDsWithReplicas []string, diag *diagv1.PortworxDiag) bool {
	if diag == nil || diag.Spec.Portworx == nil {
		// Just in case, sanity check
		return false
	}

	// If we're selecting all nodes, short-circuit
	if diag.Spec.Portworx.NodeSelector.All {
		return true
	}
	// Check if this is one of the nodes matching our volume selector
	for _, id := range nodeIDsWithReplicas {
		if id == targetNodeID {
			return true
		}
	}

	// If there's no node selector and we're not selecting all nodes, collect none
	if len(diag.Spec.Portworx.NodeSelector.IDs) == 0 && len(diag.Spec.Portworx.NodeSelector.Labels) == 0 {
		return false
	}

	// Filter by node ID first since it's the one we already have (save on API calls)
	// If there's no IDs provided, do nothing and move on to the label match.
	for _, id := range diag.Spec.Portworx.NodeSelector.IDs {
		if id == targetNodeID {
			return true
		}
	}

	// Node ID didn't match, check if node labels match label selectors
	if len(diag.Spec.Portworx.NodeSelector.Labels) == 0 {
		return false
	}

	// Find node object in list
	var node *v1.Node
	for _, n := range k8sNodes {
		if strings.EqualFold(n.Name, targetNodeName) {
			nTemp := n // To avoid referencing the loop variable
			node = &nTemp
			break
		}
	}

	if node == nil {
		return false
	}

	// Check if node labels matches label selectors
	for k, v := range diag.Spec.Portworx.NodeSelector.Labels {
		label, ok := node.Labels[k]
		if !ok {
			return false // Label missing entirely
		}
		if label != v {
			return false // Label values don't match
		}
	}
	return true // All labels in selector passed
}

func (c *Controller) getNodesToHavePods(nodes *v1.NodeList, diag *diagv1.PortworxDiag, stc *corev1.StorageCluster, nodeIDToNodeName map[string]string, nodeIDToStatus map[string]string) (map[string]bool, error) {
	nodesToHavePods := map[string]bool{}

	// If we have node statuses populated already, use that as the master list to avoid recomputing labels
	// If not, then do the hard work of calculating which nodes should have pods and reconcile based on that
	if len(nodeIDToStatus) > 0 {
		for nodeID := range nodeIDToStatus {
			nodesToHavePods[nodeID] = true
		}
		return nodesToHavePods, nil
	}
	// If we have no spec to work with, return an empty list, nothing to do
	if diag.Spec.Portworx == nil {
		return nil, nil
	}

	if diag.Spec.Portworx.NodeSelector.All {
		// If we're selecting all nodes, just add all nodes
		for nodeID := range nodeIDToNodeName {
			nodesToHavePods[nodeID] = true
		}
		return nodesToHavePods, nil
	}

	// If we're not selecting all nodes, get the list of nodes with volumes we care about
	nodeIDsWithReplicas, err := c.getNodeIDsWithSelectedVolumes(diag, stc)
	if err != nil {
		return nil, err
	}

	for nodeID, nodeName := range nodeIDToNodeName {
		shouldExist := shouldPodBeOnNode(nodeID, nodeName, nodes.Items, nodeIDsWithReplicas, diag)
		if shouldExist {
			nodesToHavePods[nodeID] = true
		}
	}

	return nodesToHavePods, nil
}

// getPodsDiff will return the pods that need to be created and deleted, as well as how many pods exist (but are not complete) and
// how many are complete
func (c *Controller) getPodsDiff(pods *v1.PodList, nodes *v1.NodeList, diag *diagv1.PortworxDiag, stc *corev1.StorageCluster, nodeIDToNodeName map[string]string) (podReconcileStatus, error) {
	// Check on all of our storage nodes
	prs := podReconcileStatus{
		podsToDelete:         make([]*v1.Pod, 0),
		nodesToCreatePodsFor: make([]string, 0),
		nodeStatusesToAdd:    make([]*diagv1.NodeStatus, 0),
	}

	nodeToPod := getNodeToPodMap(pods)
	nodeIDToStatus := getNodeIDToStatusMap(diag.Status.NodeStatuses)

	nodesToHavePods, err := c.getNodesToHavePods(nodes, diag, stc, nodeIDToNodeName, nodeIDToStatus)
	if err != nil {
		return prs, err
	}

	for nodeID, nodeName := range nodeIDToNodeName {
		existingPod := nodeToPod[nodeName]

		_, shouldExist := nodesToHavePods[nodeID]

		status, ok := nodeIDToStatus[nodeID]
		if !ok && shouldExist {
			// This node is missing a status in the Diag, go and add it
			prs.nodeStatusesToAdd = append(prs.nodeStatusesToAdd, &diagv1.NodeStatus{NodeID: nodeID, Status: diagv1.NodeStatusPending, Message: ""})
			if existingPod == nil {
				// Also create a pod if it's missing
				prs.nodesToCreatePodsFor = append(prs.nodesToCreatePodsFor, nodeName)
			}
			continue
		}

		// This node exists in the status
		// Delete any pods that are complete/failed
		if status == diagv1.NodeStatusCompleted || status == diagv1.NodeStatusFailed {
			if existingPod != nil {
				prs.podsToDelete = append(prs.podsToDelete, existingPod)
			}
			continue
		}

		// If a pod shouldn't exist, don't add one
		if !shouldExist {
			// Delete any pods that shouldn't exist (shouldn't ever happen, but just in case)
			if existingPod != nil {
				prs.podsToDelete = append(prs.podsToDelete, existingPod)
			}
			continue
		}

		// Diag should still be running... create pod if it's missing
		if existingPod == nil {
			prs.nodesToCreatePodsFor = append(prs.nodesToCreatePodsFor, nodeName)
		}
		continue
	}

	return prs, nil
}

func getOverallPhase(statuses []diagv1.NodeStatus) (string, string) {
	// If all nodes are not yet started or empty: phase is "Not Yet Started"
	// If all nodes in status are complete: phase is "Completed"
	// If all nodes are failed: phase is "Failed"
	// If all nodes are either complete or failed: phase is "Partial Failure"
	// If at least one node is in progress: phase is "In Progress"
	// Worst case, return an "unknown" status

	if len(statuses) == 0 {
		return diagv1.DiagStatusPending, ""
	}

	phaseCount := map[string]int{}
	for _, n := range statuses {
		if _, ok := phaseCount[n.Status]; !ok {
			phaseCount[n.Status] = 1
			continue
		}
		phaseCount[n.Status] += 1
	}

	emptyPendingCount := 0
	if emptyCount, ok := phaseCount[""]; ok {
		emptyPendingCount += emptyCount
	}
	if pendingCount, ok := phaseCount[diagv1.NodeStatusPending]; ok {
		emptyPendingCount += pendingCount
	}

	if emptyPendingCount == len(statuses) {
		return diagv1.DiagStatusPending, ""
	}

	completeCount, ok := phaseCount[diagv1.NodeStatusCompleted]
	if ok && completeCount == len(statuses) {
		return diagv1.DiagStatusCompleted, "All diags collected successfully"
	}

	failedCount, ok := phaseCount[diagv1.NodeStatusFailed]
	if ok && failedCount == len(statuses) {
		return diagv1.DiagStatusFailed, "All diags failed to collect"
	}

	// Count "Pending" pods as "Failed" here, as if all the others have finished it probably means it can't be scheduled
	pendingCount, ok := phaseCount[diagv1.NodeStatusPending]
	if !ok {
		pendingCount = 0
	}

	if failedCount+pendingCount+completeCount == len(statuses) {
		return diagv1.DiagStatusPartialFailure, "Some diags failed to collect"
	}

	if inProgressCount, ok := phaseCount[diagv1.NodeStatusInProgress]; ok && inProgressCount > 0 {
		return diagv1.DiagStatusInProgress, "Diag collection is in progress"
	}

	return diagv1.DiagStatusUnknown, ""
}

func getMissingStatusPatch(diag *diagv1.PortworxDiag) map[string]interface{} {
	if diag.Status.Phase != "" || diag.Status.ClusterUUID != "" || diag.Status.NodeStatuses != nil {
		return nil
	}
	return map[string]interface{}{
		"op":    "add",
		"path":  "/status",
		"value": diagv1.PortworxDiagStatus{Phase: diagv1.DiagStatusPending, NodeStatuses: []diagv1.NodeStatus{}},
	}
}

func getChangedClusterUUIDPatch(diag *diagv1.PortworxDiag, stc *corev1.StorageCluster) map[string]interface{} {
	if diag.Status.ClusterUUID == stc.Status.ClusterUID {
		return nil
	}
	return map[string]interface{}{
		"op":    "add",
		"path":  "/status/clusterUuid",
		"value": stc.Status.ClusterUID,
	}
}

func getOverallPhasePatch(diag *diagv1.PortworxDiag) []map[string]interface{} {
	patches := []map[string]interface{}{}
	newPhase, newMessage := getOverallPhase(diag.Status.NodeStatuses)
	logrus.Debugf("New phase for PortworxDiag is '%s'", newPhase)
	if diag.Status.Phase != newPhase {
		op := "add"
		if diag.Status.Phase != "" {
			op = "replace"
		}
		patches = append(patches, map[string]interface{}{
			"op":    op,
			"path":  "/status/phase",
			"value": newPhase,
		})
	}
	if diag.Status.Message != newMessage {
		op := "add"
		if diag.Status.Message != "" {
			op = "replace"
		}
		patches = append(patches, map[string]interface{}{
			"op":    op,
			"path":  "/status/message",
			"value": newMessage,
		})
	}

	return patches
}

func getMissingNodeStatusesPatch(diag *diagv1.PortworxDiag, nodeStatusesToAdd []*diagv1.NodeStatus) []map[string]interface{} {
	if len(nodeStatusesToAdd) == 0 {
		return nil
	}
	patches := []map[string]interface{}{}
	if diag.Status.NodeStatuses == nil {
		patches = append(patches, map[string]interface{}{
			"op":    "add",
			"path":  "/status/nodes",
			"value": []diagv1.NodeStatus{},
		})
	}
	for _, toAdd := range nodeStatusesToAdd {
		patches = append(patches, map[string]interface{}{
			"op":    "add",
			"path":  "/status/nodes/-",
			"value": toAdd,
		})
	}
	return patches
}

func (c *Controller) updateDiagFields(diag *diagv1.PortworxDiag, stc *corev1.StorageCluster, prs *podReconcileStatus) error {
	patches := []map[string]interface{}{}

	if patch := getMissingStatusPatch(diag); patch != nil {
		patches = append(patches, patch)
	}

	if patch := getChangedClusterUUIDPatch(diag, stc); patch != nil {
		patches = append(patches, patch)
	}

	if phasePatches := getOverallPhasePatch(diag); len(phasePatches) > 0 {
		patches = append(patches, phasePatches...)
	}

	if phasePatches := getMissingNodeStatusesPatch(diag, prs.nodeStatusesToAdd); len(phasePatches) > 0 {
		patches = append(patches, phasePatches...)
	}

	body, err := json.Marshal(patches)
	if err != nil {
		return fmt.Errorf("failed to marshal json patch to JSON: %v", err)
	}

	err = c.client.Status().Patch(context.TODO(), diag, client.RawPatch(types.JSONPatchType, body))
	if err != nil {
		return fmt.Errorf("failed to update phase for PortworxDiag CR: %v", err)
	}
	return nil
}

func (c *Controller) patchPhase(diag *diagv1.PortworxDiag, newPhase string, newMessage string) error {
	patch := []map[string]interface{}{
		{
			"op":    "add",
			"path":  "/status/phase",
			"value": newPhase,
		},
		{
			"op":    "add",
			"path":  "/status/message",
			"value": newMessage,
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal json patch to JSON: %v", err)
	}
	err = c.client.Status().Patch(context.TODO(), diag, client.RawPatch(types.JSONPatchType, body))
	if err != nil {
		return fmt.Errorf("failed to update phase for PortworxDiag CR: %v", err)
	}
	return nil
}

func (c *Controller) syncPortworxDiag(diag *diagv1.PortworxDiag) error {
	// If diag is already done, don't do any more work
	switch diag.Status.Phase {
	case diagv1.DiagStatusPartialFailure:
		fallthrough
	case diagv1.DiagStatusCompleted:
		fallthrough
	case diagv1.DiagStatusFailed:
		logrus.Infof("PortworxDiag %s in namespace %s is already in status %s, no work to do", diag.Name, diag.Namespace, diag.Status.Phase)
		return nil
	default:
		break
	}

	logrus.Info("Enter syncPortworxDiag")
	stc, err := c.fetchSTC()
	if err != nil {
		logrus.WithError(err).Error("Failed to find StorageCluster object")
		k8s.WarningEvent(c.recorder, diag, util.FailedSyncReason, fmt.Sprintf("Failed to find StorageCluster object %s in namespace %s: %v", stc.Name, stc.Namespace, err))
		err = c.patchPhase(diag, diagv1.DiagStatusFailed, fmt.Sprintf("Failed to find StorageCluster object %s in namespace %s: %v", stc.Name, stc.Namespace, err))
		if err != nil {
			k8s.WarningEvent(c.recorder, diag, util.FailedSyncReason, fmt.Sprintf("failed to patch PortworxDiag with %s status: %v", diagv1.DiagStatusFailed, err))
			return fmt.Errorf("failed to patch PortworxDiag with %s status: %v", diagv1.DiagStatusFailed, err)
		}

		return fmt.Errorf("failed to find StorageCluster object: %v", err)
	}

	if stc.Namespace != diag.Namespace {
		logrus.Errorf("Diag %s in namespace %s is not in the same namespace as target cluster (namespace %s). Ensure the PortworxDiag object is created in the same namespace as the StorageCluster object", diag.Name, diag.Namespace, stc.Namespace)
		k8s.WarningEvent(c.recorder, diag, util.FailedSyncReason, fmt.Sprintf("Diag %s in namespace %s is not in the same namespace as target cluster (namespace %s). Ensure the PortworxDiag object is created in the same namespace as the StorageCluster object", diag.Name, diag.Namespace, stc.Namespace))
		err = c.patchPhase(diag, diagv1.DiagStatusFailed, fmt.Sprintf("Diag %s in namespace %s is not in the same namespace as target cluster (namespace %s). Ensure the PortworxDiag object is created in the same namespace as the StorageCluster object", diag.Name, diag.Namespace, stc.Namespace))
		if err != nil {
			k8s.WarningEvent(c.recorder, diag, util.FailedSyncReason, fmt.Sprintf("failed to patch PortworxDiag with %s status: %v", diagv1.DiagStatusFailed, err))
			return fmt.Errorf("failed to patch PortworxDiag with %s status: %v", diagv1.DiagStatusFailed, err)
		}

		return fmt.Errorf("diag %s in namespace %s is not in the same namespace as target cluster (namespace %s). Ensure the PortworxDiag object is created in the same namespace as the StorageCluster object", diag.Name, diag.Namespace, stc.Namespace)
	}

	conn, err := pxutil.GetPortworxConn(c.grpcConn, c.client, diag.Namespace)
	if err != nil {
		logrus.WithError(err).Warn("Failed to open Portworx GRPC connection, future calls will use the k8s client which may have outdated info")
	}
	c.grpcConn = conn

	// GetStorageNodeMapping will properly handle if conn is nil
	nodeNameToNodeID, nodeIDToNodeName, err := pxutil.GetStorageNodeMapping(stc, c.grpcConn, c.client)
	if err != nil {
		logrus.WithError(err).Error("Failed to get mapping from k8s nodes to Portworx node IDs")
		return err
	}

	pods, err := c.getDiagPods(diag.Namespace, diag.Name)
	if err != nil {
		return err
	}

	// List all k8s nodes in the cluster, but only if we have a label selector
	// Otherwise just pass an empty list through
	nodes := &v1.NodeList{Items: []v1.Node{}}
	if diag.Spec.Portworx.NodeSelector.Labels != nil {
		err = c.client.List(context.Background(), nodes)
		if err != nil {
			logrus.WithError(err).Error("Failed to list nodes in cluster")
			return fmt.Errorf("failed to list k8s nodes: %v", err)
		}
	}

	// Get what changes we need to make between real and desired
	prs, err := c.getPodsDiff(pods, nodes, diag, stc, nodeIDToNodeName)
	if err != nil {
		logrus.WithError(err).Error("Failed to check pods for required operations")
		return err
	}

	err = c.updateDiagFields(diag, stc, &prs)
	if err != nil {
		logrus.WithError(err).Error("Failed to update status fields in PortworxDiag CR")
		return err
	}

	if len(prs.nodesToCreatePodsFor) > 0 {
		logrus.Infof("Need to create diag pods for nodes: %v", prs.nodesToCreatePodsFor)

		// Create pods for these nodes
		for _, nodeName := range prs.nodesToCreatePodsFor {
			nodeID := nodeNameToNodeID[nodeName]
			podTemplate, err := makeDiagPodTemplate(stc, diag, stc.Namespace, nodeName, nodeID)
			if err != nil {
				logrus.WithError(err).Errorf("Failed to create diags collection pod template")
				k8s.WarningEvent(c.recorder, diag, "PodCreateFailed", fmt.Sprintf("Failed to create diags collection pod template: %v", err))
				continue
				// Don't exit entirely, keep trying to create the rest
			}
			err = c.podControl.CreatePods(context.TODO(), diag.Namespace, podTemplate, diag, metav1.NewControllerRef(diag, controllerKind))
			if err != nil {
				logrus.WithError(err).Warnf("Failed to create diags collection pod")
				k8s.WarningEvent(c.recorder, diag, "PodCreateFailed", fmt.Sprintf("Failed to create diags collection pod: %v", err))
				// Don't exit entirely, keep trying to create the rest
			}
		}
	}
	// TODO: what do we do if there are extra untracked pods? Unlikely, but possible
	if len(prs.podsToDelete) > 0 {
		logrus.Infof("Need to delete %d completed diag pods", len(prs.podsToDelete))
		// If there are any pods to delete that are completed, delete them
		for _, p := range prs.podsToDelete {
			err = c.podControl.DeletePod(context.TODO(), p.Namespace, p.Name, diag)
			if err != nil && !errors.IsNotFound(err) {
				logrus.WithError(err).Warnf("Failed to delete completed diags collection pod, it may still hang around")
				k8s.WarningEvent(c.recorder, diag, "PodDeleteFailed", fmt.Sprintf("Failed to delete completed diags collection pod: %v", err))
				// Don't exit, keep trying to clean up the rest
			}
		}
	}

	logrus.Info("syncPortworxDiag completed successfully")
	return nil
}

// RegisterCRD registers and validates CRDs
func (c *Controller) RegisterCRD() error {
	crd, err := k8s.GetCRDFromFile(portworxDiagCRDFile, crdBaseDir())
	if err != nil {
		return err
	}
	latestCRD, err := apiextensionsops.Instance().GetCRD(crd.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		if err = apiextensionsops.Instance().RegisterCRD(crd); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		crd.ResourceVersion = latestCRD.ResourceVersion
		if _, err := apiextensionsops.Instance().UpdateCRD(crd); err != nil {
			return err
		}
	}

	resource := fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group)
	return apiextensionsops.Instance().ValidateCRD(resource, validateCRDTimeout, validateCRDInterval)
}

func getCRDBasePath() string {
	return crdBasePath
}
