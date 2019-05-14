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
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/integer"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	daemonutil "k8s.io/kubernetes/pkg/controller/daemon/util"
	"k8s.io/kubernetes/pkg/scheduler/algorithm"
	"k8s.io/kubernetes/pkg/scheduler/algorithm/predicates"
	schedulercache "k8s.io/kubernetes/pkg/scheduler/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	slowStartInitialBatchSize           = 1
	validateCRDInterval                 = 5 * time.Second
	validateCRDTimeout                  = 1 * time.Minute
	controllerName                      = "storagecluster-controller"
	operatorPrefix                      = "operator.libopenstorage.org"
	labelKeyName                        = operatorPrefix + "/name"
	labelKeyDriverName                  = operatorPrefix + "/driver"
	deleteFinalizerName                 = operatorPrefix + "/delete"
	nodeNameIndex                       = "nodeName"
	defaultStorageClusterUniqueLabelKey = apps.ControllerRevisionHashLabelKey
	defaultRevisionHistoryLimit         = 10
	defaultMaxUnavailablePods           = 1
)

// Reasons for StorageCluster events
const (
	// failedPlacementReason is added to an event when operator can't schedule a Pod to a specified node.
	failedPlacementReason = "FailedPlacement"
	// failedStoragePodReason is added to an event when the status of a Pod of a StorageCluster is 'Failed'.
	failedStoragePodReason = "FailedStoragePod"
	// failedSyncReason is added to an event when the status the cluster could not be synced.
	failedSyncReason = "FailedSync"
)

var _ reconcile.Reconciler = &Controller{}

var (
	controllerKind = corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
	kbVerRegex     = regexp.MustCompile(`^(v\d+\.\d+\.\d+).*`)
)

// Controller reconciles a StorageCluster object
type Controller struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client                        client.Client
	scheme                        *runtime.Scheme
	recorder                      record.EventRecorder
	podControl                    k8scontroller.PodControlInterface
	crControl                     k8scontroller.ControllerRevisionControlInterface
	Driver                        storage.Driver
	isStorkServiceCreated         bool
	isStorkDeploymentCreated      bool
	isStorkSchedDeploymentCreated bool
}

// Init initialize the storage cluster controller
func (c *Controller) Init(mgr manager.Manager) error {
	err := c.createCRD()
	if err != nil {
		return err
	}

	c.client = mgr.GetClient()
	c.scheme = mgr.GetScheme()
	c.recorder = mgr.GetRecorder(controllerName)

	// Create a new controller
	ctrl, err := controller.New(controllerName, mgr, controller.Options{Reconciler: c})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource StorageCluster
	err = ctrl.Watch(
		&source.Kind{Type: &corev1alpha1.StorageCluster{}},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		return err
	}

	// Watch for changes to Pods that belong to StorageCluster object
	err = ctrl.Watch(
		&source.Kind{Type: &v1.Pod{}},
		&handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &corev1alpha1.StorageCluster{},
		},
	)
	if err != nil {
		return err
	}

	// Watch for changes to ControllerRevisions that belong to StorageCluster object
	err = ctrl.Watch(
		&source.Kind{Type: &apps.ControllerRevision{}},
		&handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &corev1alpha1.StorageCluster{},
		},
	)
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
	// Create controller revision control interface object to manage histories
	// for storage cluster objects
	c.crControl = k8scontroller.RealControllerRevisionControl{
		KubeClient: clientset,
	}

	// Add nodeName field index to the cache indexer
	err = mgr.GetCache().IndexField(&v1.Pod{}, nodeNameIndex, indexByPodNodeName)
	if err != nil {
		return fmt.Errorf("error setting node name index on pod cache: %v", err)
	}

	return nil
}

// Reconcile reads that state of the cluster for a StorageCluster object and makes changes based on
// the state read and what is in the StorageCluster.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (c *Controller) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log := logrus.WithFields(map[string]interface{}{
		"Request.Namespace": request.Namespace,
		"Request.Name":      request.Name,
	})
	log.Infof("Reconciling StorageCluster")

	// Fetch the StorageCluster instance
	instance := &corev1alpha1.StorageCluster{}
	err := c.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup
			// logic use finalizers. Return and don't requeue.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if err := c.syncStorageCluster(instance); err != nil {
		c.recorder.Event(instance, v1.EventTypeWarning, failedSyncReason, err.Error())
		logrus.Warnf("failed to sync: %v", err)
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// createCRD creates the CRD for StorageCluster object
func (c *Controller) createCRD() error {
	resource := k8s.CustomResource{
		Name:       corev1alpha1.StorageClusterResourceName,
		Plural:     corev1alpha1.StorageClusterResourcePlural,
		Group:      corev1alpha1.SchemeGroupVersion.Group,
		Version:    corev1alpha1.SchemeGroupVersion.Version,
		Scope:      apiextensionsv1beta1.NamespaceScoped,
		Kind:       reflect.TypeOf(corev1alpha1.StorageCluster{}).Name(),
		ShortNames: []string{corev1alpha1.StorageClusterShortName},
	}
	err := k8s.Instance().CreateCRD(resource)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return k8s.Instance().ValidateCRD(resource, validateCRDTimeout, validateCRDInterval)
}

func (c *Controller) syncStorageCluster(
	cluster *corev1alpha1.StorageCluster,
) error {
	if cluster.DeletionTimestamp != nil {
		logrus.Infof("Storage cluster %v/%v has been marked for deletion",
			cluster.Namespace, cluster.Name)
		return c.deleteStorageCluster(cluster)
	}

	// Set defaults in the storage cluster object if not set
	if err := c.setStorageClusterDefaults(cluster); err != nil {
		return fmt.Errorf("failed to update StorageCluster %v/%v with default values: %v",
			cluster.Namespace, cluster.Name, err)
	}

	// Ensure Stork is deployed with right configuration
	if err := c.syncStork(cluster); err != nil {
		return fmt.Errorf("failed to install/update stork: %v", err)
	}

	// Construct histories of the StorageCluster, and get the hash of current history
	cur, old, err := c.constructHistory(cluster)
	if err != nil {
		return fmt.Errorf("failed to construct revisions of StorageCluster %v/%v: %v",
			cluster.Namespace, cluster.Name, err)
	}
	hash := cur.Labels[defaultStorageClusterUniqueLabelKey]

	// TODO: Don't process a storage cluster until all its previous creations and
	// deletions have been processed.
	err = c.manage(cluster, hash)
	if err != nil {
		return err
	}

	switch cluster.Spec.UpdateStrategy.Type {
	case corev1alpha1.OnDeleteStorageClusterStrategyType:
	case corev1alpha1.RollingUpdateStorageClusterStrategyType:
		if err := c.rollingUpdate(cluster, hash); err != nil {
			return err
		}
	}

	err = c.cleanupHistory(cluster, old)
	if err != nil {
		return fmt.Errorf("failed to clean up revisions of StorageCluster %v/%v: %v",
			cluster.Namespace, cluster.Name, err)
	}

	// Update status of the cluster
	// TODO: Update the complete status of the cluster even from the storage driver
	cluster.Status.ClusterName = cluster.Name
	_, err = k8s.Instance().UpdateStorageClusterStatus(cluster)
	return err
}

func (c *Controller) deleteStorageCluster(
	cluster *corev1alpha1.StorageCluster,
) error {
	// get all the storage pods
	nodeToStoragePods, err := c.getNodeToStoragePods(cluster)
	if err != nil {
		return fmt.Errorf("couldn't get node to storage cluster pods mapping for storage cluster %v: %v",
			cluster.Name, err)
	}

	podsToDelete := make([]string, 0)
	for _, storagePods := range nodeToStoragePods {
		for _, storagePod := range storagePods {
			podsToDelete = append(podsToDelete, storagePod.Name)
		}
	}

	// Hash param can be empty as we are only deleting pods
	if err := c.syncNodes(cluster, podsToDelete, nil, ""); err != nil {
		return err
	}

	if deleteFinalizerExists(cluster) {
		toDelete := cluster.DeepCopy()
		deleteClusterCondition, err := c.Driver.DeleteStorage(toDelete)
		if err != nil {
			logrus.Errorf("Failed to delete storage: %v", err)
			return fmt.Errorf("driver failed to delete storage: %v", err)
		}
		// Check if there is an existing delete condition and overwrite it
		foundIndex := -1
		for i, deleteCondition := range toDelete.Status.Conditions {
			if deleteCondition.Type == corev1alpha1.ClusterConditionTypeDelete {
				foundIndex = i
				break
			}
		}
		if foundIndex == -1 {
			toDelete.Status.Conditions = append(toDelete.Status.Conditions, *deleteClusterCondition)
		} else {
			toDelete.Status.Conditions[foundIndex] = *deleteClusterCondition
		}
		if toDelete, err = k8s.Instance().UpdateStorageClusterStatus(toDelete); err != nil {
			return fmt.Errorf("error updating delete status for StorageCluster %v/%v: %v",
				toDelete.Namespace, toDelete.Name, err)
		}

		if deleteClusterCondition.Status == corev1alpha1.ClusterOperationCompleted {
			newFinalizers := removeDeleteFinalizer(toDelete.Finalizers)
			toDelete.Finalizers = newFinalizers
			if err := c.client.Update(context.TODO(), toDelete); err != nil {
				return err
			}
		}
	}

	c.isStorkServiceCreated = false
	c.isStorkDeploymentCreated = false
	c.isStorkSchedDeploymentCreated = false

	return nil
}

func (c *Controller) manage(
	cluster *corev1alpha1.StorageCluster,
	hash string,
) error {
	// Run the pre install hook for the driver to ensure we are ready to create storage pods
	if err := c.Driver.PreInstall(cluster); err != nil {
		return fmt.Errorf("failed to run preinstall hooks for %v/%v: %v",
			cluster.Namespace, cluster.Name, err)
	}

	// Find out the pods which are created for the nodes by StorageCluster
	nodeToStoragePods, err := c.getNodeToStoragePods(cluster)
	if err != nil {
		return fmt.Errorf("couldn't get node to storage cluster pods mapping for storage cluster %v: %v",
			cluster.Name, err)
	}

	// For each node, if the node is running the storage pod but isn't supposed to, kill the storage pod.
	// If the node is supposed to run the storage pod, but isn't, create the storage pod on the node.
	nodeList := &v1.NodeList{}
	err = c.client.List(context.TODO(), &client.ListOptions{}, nodeList)
	if err != nil {
		return fmt.Errorf("couldn't get list of nodes when syncing storage cluster %#v: %v",
			cluster, err)
	}

	var nodesNeedingStoragePods, podsToDelete []string
	for _, node := range nodeList.Items {
		nodesNeedingStoragePodsOnNode, podsToDeleteOnNode, err := c.podsShouldBeOnNode(&node, nodeToStoragePods, cluster)
		if err != nil {
			continue
		}

		nodesNeedingStoragePods = append(nodesNeedingStoragePods, nodesNeedingStoragePodsOnNode...)
		podsToDelete = append(podsToDelete, podsToDeleteOnNode...)
	}

	if err := c.syncNodes(cluster, podsToDelete, nodesNeedingStoragePods, hash); err != nil {
		return err
	}

	return nil
	// TODO: Update the status of the pods in the CR (available, running, etc)
}

// syncNodes deletes given pods and creates new storage pods on the given nodes
func (c *Controller) syncNodes(
	cluster *corev1alpha1.StorageCluster,
	podsToDelete, nodesNeedingStoragePods []string,
	hash string,
) error {
	createDiff := len(nodesNeedingStoragePods)
	deleteDiff := len(podsToDelete)

	// Error channel to communicate back failures
	// Make the buffer big enough to avoid any blocking
	errCh := make(chan error, createDiff+deleteDiff)

	podTemplate := c.createPodTemplate(cluster, hash)
	logrus.Debugf("Nodes needing storage pods for storage cluster %v: %+v, creating %d",
		cluster.Name, nodesNeedingStoragePods, createDiff)

	createWait := sync.WaitGroup{}

	// Batch the pod creates. Batch sizes start at slowStartInitialBatchSize
	// and double with each successful iteration in a kind of "slow start".
	// This handles attempts to start large numbers of pods that would
	// likely all fail with the same error. For example a project with a
	// low quota that attempts to create a large number of pods will be
	// prevented from spamming the API service with the pod create requests
	// after one of its pods fails.  Conveniently, this also prevents the
	// event spam that those failures would generate.
	batchSize := integer.IntMin(createDiff, slowStartInitialBatchSize)
	for pos := 0; pos < createDiff; batchSize, pos = integer.IntMin(2*batchSize, createDiff-(pos+batchSize)), pos+batchSize {
		errorCount := len(errCh)
		createWait.Add(batchSize)
		for i := pos; i < pos+batchSize; i++ {
			go func(idx int) {
				defer createWait.Done()
				err := c.podControl.CreatePodsOnNode(
					nodesNeedingStoragePods[idx],
					cluster.Namespace,
					&podTemplate,
					cluster,
					metav1.NewControllerRef(cluster, controllerKind),
				)
				if err != nil && errors.IsTimeout(err) {
					// TODO
					// Pod is created but its initialization has timed out. If the initialization
					// is successful eventually, the controller will observe the creation via the
					// informer. If the initialization fails, or if the pod keeps uninitialized
					// for a long time, the informer will not receive any update, and the controller
					// will create a new pod.
					return
				}
				if err != nil {
					logrus.Warnf("Failed creation of storage pod on node %v: %v", nodesNeedingStoragePods[idx], err)
					errCh <- err
				}
			}(i)
		}
		createWait.Wait()
		skippedPods := createDiff - batchSize
		if errorCount < len(errCh) && skippedPods > 0 {
			logrus.Debugf("Slow-start failure. Skipping creation of %d pods", skippedPods)
			// The skipped pods will be retried later. The next controller resync will
			// retry the slow start process.
			break
		}
	}

	logrus.Debugf("Pods to delete for storage cluster %s: %+v, deleting %d",
		cluster.Name, podsToDelete, deleteDiff)
	deleteWait := sync.WaitGroup{}
	deleteWait.Add(deleteDiff)
	for i := 0; i < deleteDiff; i++ {
		go func(idx int) {
			defer deleteWait.Done()
			err := c.podControl.DeletePod(
				cluster.Namespace,
				podsToDelete[idx],
				cluster,
			)
			if err != nil {
				logrus.Warnf("Failed deletion of storage pod %v: %v", podsToDelete[idx], err)
				errCh <- err
			}
		}(i)
	}
	deleteWait.Wait()

	errors := []error{}
	close(errCh)
	for err := range errCh {
		errors = append(errors, err)
	}
	return utilerrors.NewAggregate(errors)
}

func (c *Controller) podsShouldBeOnNode(
	node *v1.Node,
	nodeToStoragePods map[string][]*v1.Pod,
	cluster *corev1alpha1.StorageCluster,
) (nodesNeedingStoragePods, podsToDelete []string, err error) {
	wantToRun, shouldSchedule, shouldContinueRunning, err := c.nodeShouldRunStoragePod(node, cluster)
	if err != nil {
		return
	}

	storagePods, exists := nodeToStoragePods[node.Name]

	switch {
	case wantToRun && !shouldSchedule:
		// If storage pod is supposed to run, but cannot be scheduled, add to suspended list.
		// TODO
	case shouldSchedule && !exists:
		nodesNeedingStoragePods = append(nodesNeedingStoragePods, node.Name)
	case shouldContinueRunning:
		// If a storage pod failed, delete it.
		// If there's no storage pod left on this node, we will create it in the next sync loop
		var storagePodsRunning []*v1.Pod
		for _, pod := range storagePods {
			if pod.DeletionTimestamp != nil {
				continue
			}
			if pod.Status.Phase == v1.PodFailed {
				msg := fmt.Sprintf("Found failed storage pod %s on node %s, will try to kill it", pod.Name, node.Name)
				logrus.Warnf(msg)
				c.recorder.Eventf(cluster, v1.EventTypeWarning, failedStoragePodReason, msg)
				podsToDelete = append(podsToDelete, pod.Name)
			} else {
				storagePodsRunning = append(storagePodsRunning, pod)
			}
		}
		// If storage pod is supposed to be running on node, but more that 1 storage pods
		// are running; delete the excess storage pods. Sort the storage pods by creation
		// time, so the oldest is preserved.
		if len(storagePodsRunning) > 1 {
			sort.Sort(podByCreationTimestampAndPhase(storagePodsRunning))
			for i := 1; i < len(storagePodsRunning); i++ {
				podsToDelete = append(podsToDelete, storagePodsRunning[i].Name)
			}
		}
	case !shouldContinueRunning && exists:
		// If storage pod isn't supposed to run on node, but it is, delete all storage pods on node.
		for _, pod := range storagePods {
			podsToDelete = append(podsToDelete, pod.Name)
		}
	}

	return nodesNeedingStoragePods, podsToDelete, nil
}

// nodeShouldRunStoragePod simulates a storage pod on the given node which helps
// us determine whether we want to run the pod on that node, or if the pod should
// to be scheduled on the node, or to allow running if it is already running.
func (c *Controller) nodeShouldRunStoragePod(
	node *v1.Node,
	cluster *corev1alpha1.StorageCluster,
) (wantToRun, shouldSchedule, shouldContinueRunning bool, err error) {
	newPod := c.newPod(cluster, node.Name)
	wantToRun, shouldSchedule, shouldContinueRunning = true, true, true

	// TODO: We should get rid of simulate and let the scheduler try to deploy
	// the pods on the nodes based off the tolerations. The scheduler can handle
	// the resource checks.
	reasons, nodeInfo, err := c.simulate(newPod, node, cluster)
	if err != nil {
		logrus.Debugf("StorageCluster Predicates failed on node %s for storage cluster '%s' "+
			"due to unexpected error: %v", node.Name, cluster.Name, err)
		return false, false, false, err
	}

	var insufficientResourceErr error
	for _, r := range reasons {
		logrus.Debugf("StorageCluster Predicates failed on node %s for storage cluster '%s'"+
			" for reason: %v", node.Name, cluster.Name, r.GetReason())

		switch reason := r.(type) {
		case *predicates.InsufficientResourceError:
			insufficientResourceErr = reason
		case *predicates.PredicateFailureError:
			var emitEvent bool
			// we try to partition predicates into two partitions here:
			// intentional on the part of the operator and not.
			switch reason {
			// intentional
			case
				predicates.ErrNodeSelectorNotMatch,
				predicates.ErrPodNotMatchHostName,
				predicates.ErrNodeLabelPresenceViolated,
				// this one is probably intentional since it's a workaround for not having
				// pod hard anti affinity.
				predicates.ErrPodNotFitsHostPorts:
				return false, false, false, nil
			case predicates.ErrTaintsTolerationsNotMatch:
				// StorageCluster is expected to respect taints and tolerations
				fitsNoExecute, _, err := predicates.PodToleratesNodeNoExecuteTaints(newPod, nil, nodeInfo)
				if err != nil {
					return false, false, false, err
				}
				if !fitsNoExecute {
					return false, false, false, nil
				}
				wantToRun, shouldSchedule = false, false
			// unintentional
			case
				predicates.ErrDiskConflict,
				predicates.ErrVolumeZoneConflict,
				predicates.ErrMaxVolumeCountExceeded,
				predicates.ErrNodeUnderMemoryPressure,
				predicates.ErrNodeUnderDiskPressure:
				// wantToRun and shouldContinueRunning are likely true here
				shouldSchedule = false
				emitEvent = true
			// unexpected
			case
				predicates.ErrPodAffinityNotMatch,
				predicates.ErrServiceAffinityViolated:
				logrus.Warnf("unexpected predicate failure reason: %s", reason.GetReason())
				return false, false, false,
					fmt.Errorf("unexpected reason: StorageCluster Predicates should not return reason %s", reason.GetReason())
			default:
				logrus.Warnf("unknown predicate failure reason: %s", reason.GetReason())
				wantToRun, shouldSchedule, shouldContinueRunning = false, false, false
				emitEvent = true
			}
			if emitEvent {
				c.recorder.Eventf(cluster, v1.EventTypeWarning, failedPlacementReason,
					"failed to place pod on %q: %s", node.Name, reason.GetReason)
			}
		}
	}
	// only emit this event if insufficient resource is the only thing
	// preventing the storage cluster from scheduling
	if shouldSchedule && insufficientResourceErr != nil {
		c.recorder.Eventf(cluster, v1.EventTypeWarning, failedPlacementReason,
			"failed to place pod on %q: %s", node.Name, insufficientResourceErr.Error())
		shouldSchedule = false
	}
	return
}

func (c *Controller) createPodTemplate(
	cluster *corev1alpha1.StorageCluster,
	hash string,
) v1.PodTemplateSpec {
	pod := c.newPod(cluster, "")
	newTemplate := v1.PodTemplateSpec{
		ObjectMeta: pod.ObjectMeta,
		Spec:       pod.Spec,
	}

	if len(hash) > 0 {
		newTemplate.ObjectMeta.Labels[defaultStorageClusterUniqueLabelKey] = hash
	}
	return newTemplate
}

func (c *Controller) newPod(cluster *corev1alpha1.StorageCluster, nodeName string) *v1.Pod {
	newPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Labels:    c.storageClusterSelectorLabels(cluster),
		},
		// TODO: add node specific spec here for heterogeneous config
		Spec: c.Driver.GetStoragePodSpec(cluster),
	}
	newPod.Spec.NodeName = nodeName
	// Add default tolerations for StorageCluster pods
	addOrUpdateStoragePodTolerations(newPod)
	return newPod
}

func (c *Controller) simulate(
	newPod *v1.Pod,
	node *v1.Node,
	cluster *corev1alpha1.StorageCluster,
) ([]algorithm.PredicateFailureReason, *schedulercache.NodeInfo, error) {
	podList := &v1.PodList{}
	fieldSelector := fields.SelectorFromSet(map[string]string{nodeNameIndex: node.Name})
	err := c.client.List(context.TODO(), &client.ListOptions{FieldSelector: fieldSelector}, podList)
	if err != nil {
		return nil, nil, err
	}

	nodeInfo := schedulercache.NewNodeInfo()
	if err = nodeInfo.SetNode(node); err != nil {
		logrus.Warnf("Error setting setting node object in cache: %v", err)
	}

	for _, pod := range podList.Items {
		// Ignore pods that belong to the storage cluster when taking into account
		// whether a storage cluster should bind to a node.
		if isControlledByStorageCluster(&pod, cluster.GetUID()) {
			continue
		}
		nodeInfo.AddPod(&pod)
	}

	_, reasons, err := checkPredicates(newPod, nodeInfo)
	return reasons, nodeInfo, err
}

func (c *Controller) setStorageClusterDefaults(cluster *corev1alpha1.StorageCluster) error {
	toUpdate := cluster.DeepCopy()

	updateStrategy := &toUpdate.Spec.UpdateStrategy
	if updateStrategy.Type == "" {
		updateStrategy.Type = corev1alpha1.RollingUpdateStorageClusterStrategyType
	}
	if updateStrategy.Type == corev1alpha1.RollingUpdateStorageClusterStrategyType {
		if updateStrategy.RollingUpdate == nil {
			updateStrategy.RollingUpdate = &corev1alpha1.RollingUpdateStorageCluster{}
		}
		if updateStrategy.RollingUpdate.MaxUnavailable == nil {
			// Set default MaxUnavailable as 1 by default.
			maxUnavailable := intstr.FromInt(defaultMaxUnavailablePods)
			updateStrategy.RollingUpdate.MaxUnavailable = &maxUnavailable
		}
	}

	if toUpdate.Spec.RevisionHistoryLimit == nil {
		toUpdate.Spec.RevisionHistoryLimit = new(int32)
		*toUpdate.Spec.RevisionHistoryLimit = defaultRevisionHistoryLimit
	}

	if toUpdate.Spec.ImagePullPolicy == "" {
		toUpdate.Spec.ImagePullPolicy = v1.PullAlways
	}

	foundDeleteFinalizer := false
	for _, finalizer := range toUpdate.Finalizers {
		if finalizer == deleteFinalizerName {
			foundDeleteFinalizer = true
			break
		}
	}
	if !foundDeleteFinalizer {
		toUpdate.Finalizers = append(toUpdate.Finalizers, deleteFinalizerName)
	}

	c.Driver.SetDefaultsOnStorageCluster(toUpdate)

	// Update the spec only if anything has changed
	if !reflect.DeepEqual(cluster.Spec, toUpdate.Spec) || !foundDeleteFinalizer {
		err := c.client.Update(context.TODO(), toUpdate)
		if err != nil {
			return err
		}
		cluster.Spec = *toUpdate.Spec.DeepCopy()
	}
	return nil
}

func isControlledByStorageCluster(pod *v1.Pod, uid types.UID) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.UID == uid {
			return true
		}
	}
	return false
}

// checkPredicates checks if a StorageCluster's pod can be scheduled on a node using GeneralPredicates
// and PodToleratesNodeTaints predicate
func checkPredicates(
	pod *v1.Pod,
	nodeInfo *schedulercache.NodeInfo,
) (bool, []algorithm.PredicateFailureReason, error) {
	var predicateFails []algorithm.PredicateFailureReason

	fit, reasons, err := predicates.PodToleratesNodeTaints(pod, nil, nodeInfo)
	if err != nil {
		return false, predicateFails, err
	}
	if !fit {
		predicateFails = append(predicateFails, reasons...)
	}
	fit, reasons, err = predicates.GeneralPredicates(pod, nil, nodeInfo)
	if err != nil {
		return false, predicateFails, err
	}
	if !fit {
		predicateFails = append(predicateFails, reasons...)
	}

	return len(predicateFails) == 0, predicateFails, nil
}

func (c *Controller) getNodeToStoragePods(
	cluster *corev1alpha1.StorageCluster,
) (map[string][]*v1.Pod, error) {
	claimedPods, err := c.getStoragePods(cluster)
	if err != nil {
		return nil, err
	}

	nodeToPodsMap := make(map[string][]*v1.Pod)
	for _, pod := range claimedPods {
		nodeName, err := daemonutil.GetTargetNodeName(pod)
		if err != nil {
			logrus.Warnf("Failed to get target node name of Pod %v in StorageCluster %v",
				pod.Name, cluster.Name)
			continue
		}
		nodeToPodsMap[nodeName] = append(nodeToPodsMap[nodeName], pod)
	}

	return nodeToPodsMap, nil
}

func (c *Controller) getStoragePods(
	cluster *corev1alpha1.StorageCluster,
) ([]*v1.Pod, error) {
	// List all pods to include those that don't match the selector anymore but
	// have a ControllerRef pointing to this controller.
	podList := &v1.PodList{}
	err := c.client.List(context.TODO(), &client.ListOptions{Namespace: cluster.Namespace}, podList)
	if err != nil {
		return nil, err
	}

	allPods := make([]*v1.Pod, 0)
	for _, pod := range podList.Items {
		podCopy := pod.DeepCopy()
		allPods = append(allPods, podCopy)
	}

	// If any adoptions are attempted, we should first recheck for deletion with
	// an uncached quorum read sometime after listing Pods
	undeletedCluster := k8scontroller.RecheckDeletionTimestamp(func() (metav1.Object, error) {
		fresh, err := k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
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
	// Use ControllerRefManager to adopt/orphan as needed. Pods that don't match the
	// labels but are owned by this storage cluster are released (disowned). Pods that
	// match the labels and do not have ref to this storage cluster are owned by it.
	cm := k8scontroller.NewPodControllerRefManager(
		c.podControl,
		cluster,
		labels.SelectorFromSet(selector),
		controllerKind,
		undeletedCluster,
	)
	return cm.ClaimPods(allPods)
}

func (c *Controller) storageClusterSelectorLabels(cluster *corev1alpha1.StorageCluster) map[string]string {
	labels := c.Driver.GetSelectorLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelKeyName] = cluster.Name
	labels[labelKeyDriverName] = c.Driver.String()
	return labels
}

type podByCreationTimestampAndPhase []*v1.Pod

func (o podByCreationTimestampAndPhase) Len() int {
	return len(o)
}

func (o podByCreationTimestampAndPhase) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}

func (o podByCreationTimestampAndPhase) Less(i, j int) bool {
	// Scheduled Pod first
	if len(o[i].Spec.NodeName) != 0 && len(o[j].Spec.NodeName) == 0 {
		return true
	}

	if len(o[i].Spec.NodeName) == 0 && len(o[j].Spec.NodeName) != 0 {
		return false
	}

	if o[i].CreationTimestamp.Equal(&o[j].CreationTimestamp) {
		return o[i].Name < o[j].Name
	}
	return o[i].CreationTimestamp.Before(&o[j].CreationTimestamp)
}
