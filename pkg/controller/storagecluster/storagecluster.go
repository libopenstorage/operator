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
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-version"
	storageapi "github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/cloudprovider"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	daemonutil "k8s.io/kubernetes/pkg/controller/daemon/util"
	"k8s.io/utils/integer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/libopenstorage/operator/drivers/storage"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// ControllerName is the name of the controller
	ControllerName = "storagecluster-controller"
	// ComponentName is the component name of the storage cluster
	ComponentName                       = "storage"
	slowStartInitialBatchSize           = 1
	validateCRDInterval                 = 5 * time.Second
	validateCRDTimeout                  = 1 * time.Minute
	deleteFinalizerName                 = constants.OperatorPrefix + "/delete"
	nodeNameIndex                       = "nodeName"
	defaultStorageClusterUniqueLabelKey = apps.ControllerRevisionHashLabelKey
	defaultRevisionHistoryLimit         = 10
	defaultMaxUnavailablePods           = 1
	failureDomainZoneKey                = v1.LabelZoneFailureDomainStable
	crdBasePath                         = "/crds"
	deprecatedCRDBasePath               = "/crds/deprecated"
	storageClusterCRDFile               = "core_v1_storagecluster_crd.yaml"
	minSupportedK8sVersion              = "1.21.0"
)

var _ reconcile.Reconciler = &Controller{}

var (
	controllerKind       = corev1.SchemeGroupVersion.WithKind("StorageCluster")
	crdBaseDir           = getCRDBasePath
	deprecatedCRDBaseDir = getDeprecatedCRDBasePath
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
	kubernetesVersion             *version.Version
	isStorkDeploymentCreated      bool
	isStorkSchedDeploymentCreated bool
	ctrl                          controller.Controller
	// Node to NodeInfo map
	nodeInfoMap map[string]*k8s.NodeInfo
}

// Init initialize the storage cluster controller
func (c *Controller) Init(mgr manager.Manager) error {
	var err error
	c.client = mgr.GetClient()
	c.scheme = mgr.GetScheme()
	c.recorder = mgr.GetEventRecorderFor(ControllerName)
	c.nodeInfoMap = make(map[string]*k8s.NodeInfo)

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
	// Create controller revision control interface object to manage histories
	// for storage cluster objects
	c.crControl = k8scontroller.RealControllerRevisionControl{
		KubeClient: clientset,
	}

	// Add nodeName field index to the cache indexer
	err = mgr.GetCache().IndexField(context.TODO(), &v1.Pod{}, nodeNameIndex, indexByPodNodeName)
	if err != nil {
		return fmt.Errorf("error setting node name index on pod cache: %v", err)
	}

	return nil
}

// StartWatch starts the watch on the StorageCluster
func (c *Controller) StartWatch() error {
	err := c.ctrl.Watch(
		&source.Kind{Type: &corev1.StorageCluster{}},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		return err
	}

	// Watch for changes to Pods that belong to StorageCluster object
	err = c.ctrl.Watch(
		&source.Kind{Type: &v1.Pod{}},
		&handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &corev1.StorageCluster{},
		},
	)
	if err != nil {
		return err
	}

	// Watch for changes to ControllerRevisions that belong to StorageCluster object
	err = c.ctrl.Watch(
		&source.Kind{Type: &apps.ControllerRevision{}},
		&handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &corev1.StorageCluster{},
		},
	)
	if err != nil {
		return err
	}

	return nil
}

// GetKubernetesClient returns the kubernetes client used by the controller
func (c *Controller) GetKubernetesClient() client.Client {
	return c.client
}

// SetKubernetesClient sets the kubernetes client to be used by the controller.
// This method is only used for testing.
func (c *Controller) SetKubernetesClient(client client.Client) {
	c.client = client
}

// GetEventRecorder returns the event recorder.
func (c *Controller) GetEventRecorder() record.EventRecorder {
	return c.recorder
}

// SetEventRecorder sets event recorder for test.
func (c *Controller) SetEventRecorder(r record.EventRecorder) {
	c.recorder = r
}

// Reconcile reads that state of the cluster for a StorageCluster object and makes changes based on
// the state read and what is in the StorageCluster.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (c *Controller) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logrus.WithFields(map[string]interface{}{
		"Request.Namespace": request.Namespace,
		"Request.Name":      request.Name,
	})
	log.Infof("Reconciling StorageCluster")

	// Fetch the StorageCluster instance
	cluster := &corev1.StorageCluster{}
	err := c.client.Get(context.TODO(), request.NamespacedName, cluster)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if err := c.validate(cluster); err != nil {
		k8s.WarningEvent(c.recorder, cluster, util.FailedValidationReason, err.Error())
		cluster.Status.Phase = string(corev1.ClusterOperationFailed)
		if updateErr := k8s.UpdateStorageClusterStatus(c.client, cluster); updateErr != nil {
			logrus.Errorf("Failed to update StorageCluster status. %v", updateErr)
		}
		return reconcile.Result{}, err
	}

	if c.waitingForMigrationApproval(cluster) {
		k8s.InfoEvent(
			c.recorder, cluster, util.MigrationPendingReason,
			fmt.Sprintf("To proceed with the migration, set the %s annotation on the "+
				"StorageCluster to 'true'", constants.AnnotationMigrationApproved),
		)
		return reconcile.Result{}, nil
	}

	if err := c.syncStorageCluster(cluster); err != nil {
		k8s.WarningEvent(c.recorder, cluster, util.FailedSyncReason, err.Error())
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (c *Controller) waitingForMigrationApproval(cluster *corev1.StorageCluster) bool {
	label, migrationLabelPresent := cluster.Annotations[constants.AnnotationMigrationApproved]
	if !migrationLabelPresent {
		return false
	}

	approved, err := strconv.ParseBool(label)
	if err != nil {
		// Wait until valid boolean value is provided
		return true
	}

	// Even if the migration is approved, wait for the status to come out of initial
	// or waiting migration approval state. This gives time to the migration routine,
	// to prepare the cluster for migration after the approval.
	return !approved || cluster.Status.Phase == "" ||
		cluster.Status.Phase == constants.PhaseAwaitingApproval
}

func (c *Controller) validate(cluster *corev1.StorageCluster) error {
	if err := c.validateK8sVersion(); err != nil {
		return err
	}
	if err := c.validateSingleCluster(cluster); err != nil {
		return err
	}
	if err := c.validateCustomAnnotations(cluster); err != nil {
		return err
	}
	if err := c.validateCloudStorageLabelKey(cluster); err != nil {
		return err
	}
	if err := c.Driver.Validate(); err != nil {
		return err
	}

	return nil
}

func (c *Controller) validateK8sVersion() error {
	var err error
	if c.kubernetesVersion == nil {
		c.kubernetesVersion, err = k8s.GetVersion()
		if err != nil {
			return err
		}
	}
	minVersion, err := version.NewVersion(minSupportedK8sVersion)
	if err != nil {
		return err
	}
	if c.kubernetesVersion.LessThan(minVersion) {
		return fmt.Errorf("minimum supported kubernetes version by the operator is %s",
			minSupportedK8sVersion)
	}
	return nil
}

func (c *Controller) validateSingleCluster(current *corev1.StorageCluster) error {
	// If the current cluster has the delete finalizer then it has been already reconciled
	for _, finalizer := range current.Finalizers {
		if finalizer == deleteFinalizerName {
			return nil
		}
	}

	// If the cluster current cluster does not have the finalizer, check if any existing
	// StorageClusters have the finalizer. If none of them have it, then current just got
	// lucky and is the only StorageCluster that will get reconciled; else if a cluster
	// is found with the finalizer then we cannot process current as we support only 1.
	clusterList := &corev1.StorageClusterList{}
	err := c.client.List(context.TODO(), clusterList, &client.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list storage clusters. %v", err)
	}
	for _, cluster := range clusterList.Items {
		if cluster.Name == current.Name && cluster.Namespace == current.Namespace {
			continue
		}
		for _, finalizer := range cluster.Finalizers {
			if finalizer == deleteFinalizerName {
				return fmt.Errorf("only one StorageCluster is allowed in a Kubernetes cluster. "+
					"StorageCluster %s/%s already exists", cluster.Namespace, cluster.Name)
			}
		}
	}

	return nil
}

// validateCustomAnnotations validate all custom annotations in the spec
func (c *Controller) validateCustomAnnotations(current *corev1.StorageCluster) error {
	if current.Spec.Metadata == nil || current.Spec.Metadata.Annotations == nil {
		return nil
	}
	for mapKey := range current.Spec.Metadata.Annotations {
		split := strings.Split(mapKey, "/")
		if len(split) != 2 {
			return fmt.Errorf("malformed custom annotation locator: %s", mapKey)
		}
	}

	return nil
}

func getKeysFromNodeSelector(ns *corev1.NodeSelector) map[string]bool {
	keys := make(map[string]bool)

	if ns.LabelSelector != nil {
		for k := range ns.LabelSelector.MatchLabels {
			keys[k] = true
		}

		for _, r := range ns.LabelSelector.MatchExpressions {
			keys[r.Key] = true
		}
	}

	return keys
}

func (c *Controller) validateCloudStorageLabelKey(cluster *corev1.StorageCluster) error {
	storagePods, err := c.getStoragePods(cluster)
	if err != nil {
		return err
	}

	// This is to prevent an existing cluster from running into failure due to the validation.
	if len(storagePods) > 0 {
		logrus.Debug("existing storage pod found, skipping cloud storage node selector validation")
		return nil
	}

	key, err := c.getCloudStorageLabelKey(cluster)
	if err != nil {
		return err
	}

	if cluster.Spec.CloudStorage != nil &&
		cluster.Spec.CloudStorage.NodePoolLabel != "" &&
		key != "" &&
		cluster.Spec.CloudStorage.NodePoolLabel != key {
		return fmt.Errorf("node pool label key incorrect, expected %s, actual %s", key, cluster.Spec.CloudStorage.NodePoolLabel)
	}

	return nil
}

func (c *Controller) getCloudStorageLabelKey(cluster *corev1.StorageCluster) (string, error) {
	if len(cluster.Spec.Nodes) == 0 {
		return "", nil
	}

	key := ""
	for _, node := range cluster.Spec.Nodes {
		if node.CloudStorage == nil {
			continue
		}

		if node.Selector.NodeName != "" {
			return "", fmt.Errorf("should not use nodeName as cloud storage node selector")
		}

		keys := getKeysFromNodeSelector(&node.Selector)
		if len(keys) != 1 {
			return "", fmt.Errorf("it's required to have only 1 key in cloud storage node label selector, key(s) %v, node %+v", keys, node)
		}

		keyTemp := ""
		for k := range keys {
			keyTemp = k
		}

		if key == "" {
			key = keyTemp
		} else if key != keyTemp {
			return "", fmt.Errorf("can not have different key in cloud storage node label selector: %s and %s", key, keyTemp)
		}
	}

	return key, nil
}

// RegisterCRD registers and validates CRDs
func (c *Controller) RegisterCRD() error {
	k8sVersion, err := k8s.GetVersion()
	if err != nil {
		return fmt.Errorf("error parsing Kubernetes version '%s'. %v", k8sVersion, err)
	}

	k8s1_16, err := version.NewVersion("1.16")
	if err != nil {
		return fmt.Errorf("error parsing version '1.16': %v", err)
	}

	if k8sVersion.GreaterThanOrEqual(k8s1_16) {
		return c.createOrUpdateCRD()
	}
	return c.createOrUpdateDeprecatedCRD()
}

func (c *Controller) createOrUpdateCRD() error {
	// Create and validate StorageCluster CRD
	crd, err := k8s.GetCRDFromFile(storageClusterCRDFile, crdBaseDir())
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

	resource := fmt.Sprintf("%s.%s", corev1.StorageClusterResourcePlural, corev1.SchemeGroupVersion.Group)
	err = apiextensionsops.Instance().ValidateCRD(resource, validateCRDTimeout, validateCRDInterval)
	if err != nil {
		return err
	}

	return nil
}

func (c *Controller) createOrUpdateDeprecatedCRD() error {
	// Create and validate StorageCluster CRD
	crd, err := k8s.GetV1beta1CRDFromFile(storageClusterCRDFile, deprecatedCRDBaseDir())
	if err != nil {
		return err
	}
	latestCRD, err := apiextensionsops.Instance().GetCRDV1beta1(crd.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		if err = apiextensionsops.Instance().RegisterCRDV1beta1(crd); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		crd.ResourceVersion = latestCRD.ResourceVersion
		if _, err := apiextensionsops.Instance().UpdateCRDV1beta1(crd); err != nil {
			return err
		}
	}

	resource := apiextensionsops.CustomResource{
		Plural: corev1.StorageClusterResourcePlural,
		Group:  corev1.SchemeGroupVersion.Group,
	}
	err = apiextensionsops.Instance().ValidateCRDV1beta1(resource, validateCRDTimeout, validateCRDInterval)
	if err != nil {
		return err
	}

	return nil
}

func (c *Controller) syncStorageCluster(
	cluster *corev1.StorageCluster,
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
		return err
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
	case corev1.OnDeleteStorageClusterStrategyType:
	case corev1.RollingUpdateStorageClusterStrategyType:
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
	return c.updateStorageClusterStatus(cluster)
}

func (c *Controller) deleteStorageCluster(
	cluster *corev1.StorageCluster,
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
		deleteClusterCondition, driverErr := c.Driver.DeleteStorage(toDelete)
		if driverErr != nil {
			msg := fmt.Sprintf("Driver failed to delete storage. %v", driverErr)
			k8s.WarningEvent(c.recorder, toDelete, util.FailedSyncReason, msg)
		}
		// Check if there is an existing delete condition and overwrite it
		foundIndex := -1
		for i, deleteCondition := range toDelete.Status.Conditions {
			if deleteCondition.Type == corev1.ClusterConditionTypeDelete {
				foundIndex = i
				break
			}
		}
		if foundIndex == -1 {
			if deleteClusterCondition == nil {
				deleteClusterCondition = &corev1.ClusterCondition{
					Type:   corev1.ClusterConditionTypeDelete,
					Status: corev1.ClusterOperationInProgress,
				}
				if driverErr != nil {
					deleteClusterCondition.Reason = err.Error()
				}
			}
			foundIndex = len(toDelete.Status.Conditions)
			toDelete.Status.Conditions = append(toDelete.Status.Conditions, *deleteClusterCondition)
		} else if deleteClusterCondition != nil {
			// Update existing delete condition only if we have the latest Delete condition
			toDelete.Status.Conditions[foundIndex] = *deleteClusterCondition
		}

		toDelete.Status.Phase = string(corev1.ClusterConditionTypeDelete) + string(toDelete.Status.Conditions[foundIndex].Status)
		if err := k8s.UpdateStorageClusterStatus(c.client, toDelete); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("error updating delete status for StorageCluster %v/%v: %v",
				toDelete.Namespace, toDelete.Name, err)
		}

		if toDelete.Status.Conditions[foundIndex].Status == corev1.ClusterOperationCompleted {
			// We should update telemetry cert ownerRef here, telemetry cert is created by PX, and the ownerRef
			// should be updated by telemetry component. However there is race condition that causes telemetry
			// component not setting it correct, it happens when uninstallStrategy is changed in StorageCluster
			// spec then uninstall immediately, as component reconcile takes 30 second to trigger.
			ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
			if err := pxutil.SetTelemetryCertOwnerRef(cluster, ownerRef, c.client); err != nil {
				return err
			}

			if err := c.removeMigrationLabels(); err != nil {
				return fmt.Errorf("failed to remove migration labels from nodes: %v", err)
			}

			if err := c.removeResources(toDelete.Namespace); err != nil {
				return fmt.Errorf("failed to remove resources: %v", err)
			}

			if err := c.removeResources("kube-system"); err != nil {
				return fmt.Errorf("failed to remove resources: %v", err)
			}

			newFinalizers := removeDeleteFinalizer(toDelete.Finalizers)
			toDelete.Finalizers = newFinalizers
			if err := c.client.Update(context.TODO(), toDelete); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}

	if err := c.removeStork(cluster); err != nil {
		msg := fmt.Sprintf("Failed to cleanup Stork. %v", err)
		k8s.WarningEvent(c.recorder, cluster, util.FailedComponentReason, msg)
	}
	return nil
}

func (c *Controller) removeResources(namespace string) error {
	objs, err := k8s.GetAllObjects(c.client, namespace)
	if err != nil {
		return err
	}

	for _, obj := range objs {
		gcAnnotated := false
		if obj.GetAnnotations() != nil {
			gcAnnotated, _ = strconv.ParseBool(obj.GetAnnotations()[constants.AnnotationGarbageCollection])
		}

		if gcAnnotated || c.gcNeeded(obj) {
			logrus.Infof("Garbage collect object %s %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
			if err := c.client.Delete(context.TODO(), obj); err != nil {
				return err
			}
		}
	}
	return nil
}

// All external object should use the GC annotation for operator to delete them.
// For backward compatibility, we still delete some objects without the annotation.
func (c *Controller) gcNeeded(obj client.Object) bool {
	if obj == nil {
		return false
	}

	if obj.GetObjectKind().GroupVersionKind().Kind == "ConfigMap" {
		if obj.GetName() == "px-attach-driveset-lock" ||
			strings.HasPrefix(obj.GetName(), "px-bringup-queue-lock") {
			return true
		}
	}

	return false
}

func (c *Controller) removeMigrationLabels() error {
	nodeList := &v1.NodeList{}
	if err := c.client.List(context.TODO(), nodeList, &client.ListOptions{}); err != nil {
		return err
	}
	for _, node := range nodeList.Items {
		if _, ok := node.Labels[constants.LabelPortworxDaemonsetMigration]; ok {
			delete(node.Labels, constants.LabelPortworxDaemonsetMigration)
			if err := c.client.Update(context.TODO(), &node, &client.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Controller) updateStorageClusterStatus(
	cluster *corev1.StorageCluster,
) error {
	toUpdate := cluster.DeepCopy()
	if err := c.Driver.UpdateStorageClusterStatus(toUpdate); err != nil {
		k8s.WarningEvent(c.recorder, cluster, util.FailedSyncReason, err.Error())
	}
	return k8s.UpdateStorageClusterStatus(c.client, toUpdate)
}

func (c *Controller) manage(
	cluster *corev1.StorageCluster,
	hash string,
) error {
	// Run the pre install hook for the driver to ensure we are ready to create storage pods
	if err := c.Driver.PreInstall(cluster); err != nil {
		return fmt.Errorf("pre-install hook failed: %v", err)
	}

	// Find out the pods which are created for the nodes by StorageCluster
	nodeToStoragePods, err := c.getNodeToStoragePods(cluster)
	if err != nil {
		return fmt.Errorf("couldn't get node to storage cluster pods mapping for storage cluster %v: %v",
			cluster.Name, err)
	}
	var nodesNeedingStoragePods, podsToDelete []string

	nodeList := &v1.NodeList{}
	err = c.client.List(context.TODO(), nodeList, &client.ListOptions{})
	if err != nil {
		return fmt.Errorf("couldn't get list of nodes when syncing storage cluster %#v: %v",
			cluster, err)
	}

	// For each node, if the node is running the storage pod but isn't supposed to, kill the storage pod.
	// If the node is supposed to run the storage pod, but isn't, create the storage pod on the node.
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
}

// syncNodes deletes given pods and creates new storage pods on the given nodes
func (c *Controller) syncNodes(
	cluster *corev1.StorageCluster,
	podsToDelete, nodesNeedingStoragePods []string,
	hash string,
) error {
	nodesNeedingStoragePods, podTemplates, err := c.podTemplatesForNodes(cluster, nodesNeedingStoragePods, hash)
	if err != nil {
		return err
	}

	createDiff := len(nodesNeedingStoragePods)
	deleteDiff := len(podsToDelete)

	// Error channel to communicate back failures
	// Make the buffer big enough to avoid any blocking
	errCh := make(chan error, createDiff+deleteDiff)
	createWait := sync.WaitGroup{}

	msg := fmt.Sprintf("Nodes needing storage pods for storage cluster %v: %+v, creating %d",
		cluster.Name, nodesNeedingStoragePods, createDiff)
	if createDiff > 0 {
		logrus.Infof(msg)
	} else {
		logrus.Debugf(msg)
	}

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
				nodeName := nodesNeedingStoragePods[idx]
				err := c.podControl.CreatePods(
					context.TODO(),
					cluster.Namespace,
					podTemplates[idx],
					cluster,
					metav1.NewControllerRef(cluster, controllerKind),
				)
				if err != nil && !errors.IsTimeout(err) {
					logrus.Warnf("Failed creation of storage pod on node %s: %v", nodeName, err)
					errCh <- err
				} else {
					// Pod created, store nodeInfo into the map, reset creation time if exists
					nodeInfo, ok := c.nodeInfoMap[nodeName]
					if ok {
						nodeInfo.LastPodCreationTime = time.Now()
					} else {
						c.nodeInfoMap[nodeName] = &k8s.NodeInfo{
							NodeName:             nodeName,
							LastPodCreationTime:  time.Now(),
							CordonedRestartDelay: constants.DefaultCordonedRestartDelay,
						}
					}
					if errors.IsTimeout(err) {
						// TODO
						// Pod is created but its initialization has timed out. If the initialization
						// is successful eventually, the controller will observe the creation via the
						// informer. If the initialization fails, or if the pod keeps uninitialized
						// for a long time, the informer will not receive any update, and the controller
						// will create a new pod.
						return
					}
					c.createStorageNode(cluster, nodesNeedingStoragePods[idx])
				}
			}(i)
		}
		createWait.Wait()
		skippedPods := createDiff - batchSize
		if errorCount < len(errCh) && skippedPods > 0 {
			logrus.Infof("Slow-start failure. Skipping creation of %d pods", skippedPods)
			// The skipped pods will be retried later. The next controller resync will
			// retry the slow start process.
			break
		}
	}

	msg = fmt.Sprintf("Pods to delete for storage cluster %s: %+v, deleting %d",
		cluster.Name, podsToDelete, deleteDiff)
	if deleteDiff > 0 {
		logrus.Infof(msg)
	} else {
		logrus.Debugf(msg)
	}

	deleteWait := sync.WaitGroup{}
	deleteWait.Add(deleteDiff)
	for i := 0; i < deleteDiff; i++ {
		go func(idx int) {
			defer deleteWait.Done()
			err := c.podControl.DeletePod(
				context.TODO(),
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

// podTemplatesForNodes returns a list storage pod templates for given list of
// nodes where a storage pod needs to be created.
func (c *Controller) podTemplatesForNodes(
	cluster *corev1.StorageCluster,
	nodesNeedingStoragePods []string,
	hash string,
) ([]string, []*v1.PodTemplateSpec, error) {
	nodeList := &v1.NodeList{}
	err := c.client.List(context.TODO(), nodeList, &client.ListOptions{})
	if err != nil {
		return nil, nil, err
	}

	remainingNodes := make(map[string]*v1.Node)
	for _, nodeName := range nodesNeedingStoragePods {
		for _, node := range nodeList.Items {
			if node.Name == nodeName {
				remainingNodes[nodeName] = node.DeepCopy()
				break
			}
		}
	}

	podTemplates := make([]*v1.PodTemplateSpec, 0)
	nodesNeedingStoragePods = make([]string, 0)

	for _, nodeSpec := range cluster.Spec.Nodes {
		if len(remainingNodes) == 0 {
			break
		}

		nodeGroup := make([]*v1.Node, 0)
		// Group all nodes that match the current node spec
		if nodeSpec.Selector.NodeName != "" {
			if node, exists := remainingNodes[nodeSpec.Selector.NodeName]; exists {
				nodeGroup = append(nodeGroup, node)
			} else {
				continue
			}
		} else {
			nodeSelector, err := metav1.LabelSelectorAsSelector(nodeSpec.Selector.LabelSelector)
			if err != nil {
				logrus.Warnf("Failed to parse label selector %#v: %v", nodeSpec.Selector.LabelSelector, err)
				continue
			}
			for _, node := range remainingNodes {
				if nodeSelector.Matches(labels.Set(node.Labels)) {
					nodeGroup = append(nodeGroup, node)
				}
			}
		}

		if len(nodeGroup) > 0 {
			// Create a cluster spec where all node configuration is copied at the cluster
			// level. As a result, the storage driver can just use the cluster spec to create
			// a pod template for this node group. All nodes in a group will have the same
			// configuration as they come from the same matching node spec.
			clusterForNodeGroup := cluster.DeepCopy()
			overwriteClusterSpecWithNodeSpec(&clusterForNodeGroup.Spec, nodeSpec.DeepCopy())

			if err := c.createPodTemplateForNodeGroup(
				clusterForNodeGroup, nodeGroup,
				&nodesNeedingStoragePods, &podTemplates,
				remainingNodes, hash,
			); err != nil {
				return nil, nil, err
			}
		}
	}

	// If there are nodes that do not match any of the node specs, then create pod
	// templates for those using just the cluster level configuration.
	if len(remainingNodes) > 0 {
		nodeGroup := make([]*v1.Node, 0)
		for _, node := range remainingNodes {
			nodeGroup = append(nodeGroup, node)
		}

		if err := c.createPodTemplateForNodeGroup(
			cluster, nodeGroup,
			&nodesNeedingStoragePods, &podTemplates,
			remainingNodes, hash,
		); err != nil {
			return nil, nil, err
		}
	}

	return nodesNeedingStoragePods, podTemplates, nil
}

// createPodTemplateForNodeGroup creates pod templates for the given list of nodes.
// It makes a deep copy for each pod so it is easier during pod creation.
func (c *Controller) createPodTemplateForNodeGroup(
	cluster *corev1.StorageCluster,
	nodeGroup []*v1.Node,
	nodesNeedingStoragePods *[]string,
	podTemplates *[]*v1.PodTemplateSpec,
	remainingNodes map[string]*v1.Node,
	hash string,
) error {
	for _, node := range nodeGroup {
		podTemplate, err := c.CreatePodTemplate(cluster, node, hash)
		if err != nil {
			return err
		}
		*nodesNeedingStoragePods = append(*nodesNeedingStoragePods, node.Name)
		*podTemplates = append(*podTemplates, podTemplate.DeepCopy())
		delete(remainingNodes, node.Name)
	}
	return nil
}

func (c *Controller) podsShouldBeOnNode(
	node *v1.Node,
	nodeToStoragePods map[string][]*v1.Pod,
	cluster *corev1.StorageCluster,
) (nodesNeedingStoragePods, podsToDelete []string, err error) {
	shouldRun, shouldContinueRunning, err := c.nodeShouldRunStoragePod(node, cluster)
	logrus.WithFields(logrus.Fields{
		"node":                  node.Name,
		"shouldRun":             shouldRun,
		"shouldContinueRunning": shouldContinueRunning,
	}).WithError(err).Debug("check node should run storage pod")
	if err != nil {
		return
	}

	storagePods, exists := nodeToStoragePods[node.Name]

	switch {
	case shouldRun && !exists:
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
				k8s.WarningEvent(c.recorder, cluster, util.FailedStoragePodReason, msg)
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
			if pod.DeletionTimestamp != nil {
				continue
			}
			podsToDelete = append(podsToDelete, pod.Name)
		}
	}

	return nodesNeedingStoragePods, podsToDelete, nil
}

// nodeShouldRunStoragePod checks a set of preconditions against a (node, storagecluster) and
// returns a summary. Returned booleans are:
//   - shouldRun:
//     Returns true when a pod should run on the node if a storage pod is not already
//     running on that node.
//   - shouldContinueRunning:
//     Returns true when the pod should continue running on a node if a storage pod is
//     already running on that node.
func (c *Controller) nodeShouldRunStoragePod(
	node *v1.Node,
	cluster *corev1.StorageCluster,
) (bool, bool, error) {
	if !storagePodsEnabled(cluster) {
		return false, false, nil
	}

	// If node is being deleted, don't need to schedule new storage pods
	isBeingDeleted, err := k8s.IsNodeBeingDeleted(node, c.client)
	if err != nil {
		c.log(cluster).Warnf("failed to check if node: %s is being deleted due to: %v", node.Name, err)
	}

	if isBeingDeleted {
		logrus.Infof("node: %s is in the process of being deleted. Will not create new pods here.", node.Name)
		return false, true, nil
	}

	if k8s.IsPodRecentlyCreatedAfterNodeCordoned(node, c.nodeInfoMap, cluster) {
		// Storage pod is created recently, should let the pod continue running without creating a new pod.
		return false, true, nil
	}

	return k8s.CheckPredicatesForStoragePod(node, cluster, c.StorageClusterSelectorLabels(cluster))
}

// CreatePodTemplate creates a Portworx pod template spec.
func (c *Controller) CreatePodTemplate(
	cluster *corev1.StorageCluster,
	node *v1.Node,
	hash string,
) (v1.PodTemplateSpec, error) {
	podSpec, err := c.Driver.GetStoragePodSpec(cluster, node.Name)
	if err != nil {
		return v1.PodTemplateSpec{}, fmt.Errorf("failed to create pod template: %v", err)
	}
	k8s.AddOrUpdateStoragePodTolerations(&podSpec)

	podSpec.NodeName = node.Name
	newTemplate := v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Labels:    c.StorageClusterSelectorLabels(cluster),
		},
		Spec: podSpec,
	}

	if len(node.Labels) > 0 {
		encodedNodeLabels, err := json.Marshal(node.Labels)
		if err != nil {
			return v1.PodTemplateSpec{}, fmt.Errorf("failed to encode node labels")
		}
		newTemplate.Annotations = map[string]string{constants.AnnotationNodeLabels: string(encodedNodeLabels)}
	}
	if customAnnotations := util.GetCustomAnnotations(cluster, k8s.Pod, ComponentName); customAnnotations != nil {
		if newTemplate.Annotations == nil {
			newTemplate.Annotations = make(map[string]string)
		}
		for k, v := range customAnnotations {
			newTemplate.Annotations[k] = v
		}
	}
	if len(hash) > 0 {
		newTemplate.Labels[defaultStorageClusterUniqueLabelKey] = hash
	}
	return newTemplate, nil
}
func (c *Controller) getCurrentMaxStorageNodesPerZone(
	cluster *corev1.StorageCluster,
	nodeList *v1.NodeList,
	cloudProvider cloudprovider.Ops,
) (uint32, error) {
	storageNodeMap := make(map[string]*storageapi.StorageNode)

	if storagePodsEnabled(cluster) {
		storageNodeList, err := c.Driver.GetStorageNodes(cluster)
		if err != nil {
			logrus.Errorf("Couldn't get storage node list %v", err)
			return 0, err
		}
		for _, storageNode := range storageNodeList {
			if len(storageNode.SchedulerNodeName) != 0 && len(storageNode.Pools) > 0 {
				storageNodeMap[storageNode.SchedulerNodeName] = storageNode
			}
		}
		zoneMap := make(map[string]uint32)
		for _, node := range nodeList.Items {
			if _, ok := storageNodeMap[node.Name]; ok {
				zone, err := cloudProvider.GetZone(&node)
				if err != nil {
					return 0, err
				}
				count := zoneMap[zone]
				zoneMap[zone] = count + 1
			}
		}
		maxValue := uint32(0)
		for _, value := range zoneMap {
			if value > maxValue {
				maxValue = value
			}
		}
		return maxValue, nil
	}
	return 0, fmt.Errorf("storage disabled")
}

func getDefaultStorageNodesDisaggregatedMode(
	nodeList *v1.NodeList,
	cluster *corev1.StorageCluster,
	recorder record.EventRecorder,
) (uint64, bool, error) {
	// Check if ENABLE_ASG_STORAGE_PARTITIONING is set to false. If not, we can look at the 'portworx.io/node-type' label
	for _, envVar := range cluster.Spec.Env {
		if envVar.Name == util.StoragePartitioningEnvKey {
			if envVar.Value == "false" {
				return 0, false, nil
			}
			break
		}
	}

	// This will return a zone map of storage nodes in each zones
	nodeTypeZoneMap, err := getZoneMap(nodeList, util.NodeTypeKey, util.StorageNodeValue)
	if err != nil {
		return 0, false, err
	}
	totalNodes := uint64(0)
	// We'll find the zone with least number of labels
	minValue := uint64(math.MaxUint64)
	prevKey := ""
	prevValue := uint64(math.MaxUint64)
	for key, value := range nodeTypeZoneMap {
		totalNodes += value
		if value < minValue {
			minValue = value
		}
		if prevValue != math.MaxUint64 && prevValue != value {
			k8s.InfoEvent(
				recorder, cluster, util.UnevenStorageNodesReason,
				fmt.Sprintf("Uneven number of storage nodes labelled across zones."+
					" %v has %v, %v has %v", prevKey, prevValue, key, value),
			)
		}
		prevKey = key
		prevValue = value
	}
	if totalNodes == 0 {
		// no node is labelled with portworx.io/node-type
		nodeTypeZoneMap, err = getZoneMap(nodeList, util.NodeTypeKey, util.StoragelessNodeValue)
		if err == nil {
			for _, value := range nodeTypeZoneMap {
				totalNodes += value
			}
			if totalNodes > 0 {
				k8s.InfoEvent(
					recorder, cluster, util.AllStoragelessNodesReason,
					fmt.Sprintf("%v nodes marked as storageless, none marked as storage nodes", totalNodes),
				)
				return 0, true, fmt.Errorf("storageless nodes found. None marked as storage node")
			}
		}
		return 0, false, err
	}
	return minValue, true, nil
}

// getDefaultMaxStorageNodesPerZone aims to return a good value for MaxStorageNodesPerZone with the
// intention of having at least 3 nodes in the cluster.
func (c *Controller) getDefaultMaxStorageNodesPerZone(
	nodeList *v1.NodeList,
	cluster *corev1.StorageCluster,
	recorder record.EventRecorder,
) (uint32, error) {
	if len(nodeList.Items) == 0 {
		return 0, nil
	}
	filteredList := v1.NodeList{}
	for _, node := range nodeList.Items {
		shouldRun, _, err := c.nodeShouldRunStoragePod(&node, cluster)
		if err != nil {
			return 0, err
		}
		if shouldRun {
			filteredList.Items = append(filteredList.Items, node)
		}
	}

	// Check if storage nodes are explicitly set using labels
	storageNodes, disaggregatedMode, err := getDefaultStorageNodesDisaggregatedMode(&filteredList, cluster, recorder)
	if err != nil {
		return 0, err
	}
	if disaggregatedMode {
		return uint32(storageNodes), nil
	}

	zoneMap, err := getZoneMap(&filteredList, "", "")
	if err != nil {
		return 0, err
	}
	numZones := len(zoneMap)
	storageNodes = uint64(len(filteredList.Items) / numZones)
	return uint32(storageNodes), nil
}

func (c *Controller) isPxImageBeingUpdated(toUpdate *corev1.StorageCluster) bool {
	pxEnabled := storagePodsEnabled(toUpdate)
	newVersion := pxutil.GetImageTag(strings.TrimSpace(toUpdate.Spec.Image))
	return pxEnabled &&
		(toUpdate.Spec.Version == "" || newVersion != toUpdate.Status.Version)
}

func getZoneMap(nodeList *v1.NodeList, filterLabelKey string, filterLabelValue string) (map[string]uint64, error) {
	cloudProviderName := getCloudProviderName(nodeList)
	cloudProvider := cloudprovider.New(cloudProviderName)
	zoneMap := map[string]uint64{}
	for _, node := range nodeList.Items {
		if zone, err := cloudProvider.GetZone(&node); err == nil {
			if len(filterLabelKey) > 0 {
				value, ok := node.Labels[filterLabelKey]
				// If provided filterLabelKey is not found or the value does not match with
				// the provided filterLabelValue, no need to count in the zoneMap
				if !ok || value != filterLabelValue {
					if _, ok := zoneMap[zone]; !ok {
						zoneMap[zone] = 0
					}
					continue
				}
				// provided filterLabel's value and key matched, let's count them in the zoneMap
			}
			instancesCount := zoneMap[zone]
			zoneMap[zone] = instancesCount + 1
		} else {
			logrus.Errorf("count not find zone information: %v", err)
			return nil, err
		}
	}
	if len(nodeList.Items) == 0{
		return nil, fmt.Errorf("node list is empty") 
	}
	return zoneMap, nil
}

func getCloudProviderName(nodeList *v1.NodeList) string {
	var cloudProviderName string
	for _, node := range nodeList.Items {
		// Get the cloud provider
		// From kubernetes node spec:  <ProviderName>://<ProviderSpecificNodeID>
		if len(node.Spec.ProviderID) != 0 {
			tokens := strings.Split(node.Spec.ProviderID, "://")
			if len(tokens) == 2 {
				cloudProviderName = tokens[0]
				break
			} // else provider id is invalid
		}
	}
	return cloudProviderName
}

func (c *Controller) setStorageClusterDefaults(cluster *corev1.StorageCluster) error {
	toUpdate := cluster.DeepCopy()

	updateStrategy := &toUpdate.Spec.UpdateStrategy
	if updateStrategy.Type == "" {
		updateStrategy.Type = corev1.RollingUpdateStorageClusterStrategyType
	}
	if updateStrategy.Type == corev1.RollingUpdateStorageClusterStrategyType {
		if updateStrategy.RollingUpdate == nil {
			updateStrategy.RollingUpdate = &corev1.RollingUpdateStorageCluster{}
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

	key, err := c.getCloudStorageLabelKey(cluster)
	if err != nil {
		return err
	}
	if key != "" &&
		(toUpdate.Spec.CloudStorage == nil || toUpdate.Spec.CloudStorage.NodePoolLabel == "") {
		if toUpdate.Spec.CloudStorage == nil {
			toUpdate.Spec.CloudStorage = &corev1.CloudStorageSpec{}
		}

		toUpdate.Spec.CloudStorage.NodePoolLabel = key
	}

	nodeList := &v1.NodeList{}
	err = c.client.List(context.TODO(), nodeList, &client.ListOptions{})
	if err != nil {
		return fmt.Errorf("couldn't get list of nodes when syncing storage cluster %#v: %v",
			toUpdate, err)
	}

	cloudProviderName := getCloudProviderName(nodeList)
	cloudProvider := cloudprovider.New(cloudProviderName)
	zoneMap, err := getZoneMap(nodeList, "", "")
	if err != nil {
		return err
	}

	if err := c.Driver.UpdateDriver(&storage.UpdateDriverInfo{
		ZoneToInstancesMap: zoneMap,
		CloudProvider:      cloudProviderName,
	}); err != nil {
		logrus.Debugf("Failed to update driver: %v", err)
	}

	// if no value is set for any of max_storage_nodes*, try to see if can set a default value
	if toUpdate.Spec.CloudStorage != nil &&
		toUpdate.Spec.CloudStorage.MaxStorageNodesPerZonePerNodeGroup == nil &&
		toUpdate.Spec.CloudStorage.MaxStorageNodesPerZone == nil &&
		toUpdate.Spec.CloudStorage.MaxStorageNodes == nil {
		maxStorageNodesPerZone := uint32(0)
		err = nil
		if toUpdate.Status.Phase == "" {
			// Let's do this only when it's a fresh install of px
			maxStorageNodesPerZone, err = c.getDefaultMaxStorageNodesPerZone(nodeList, toUpdate, c.recorder)
			if err != nil {
				logrus.Errorf("could not set defult value for max_storage_nodes_per_zone (first install): %v", err)
			}
		} else {
			// Upgrade scenario
			if c.isPxImageBeingUpdated(toUpdate) {
				// If PX image is not changing, we don't have to update the values. This is to prevent pod restarts
				maxStorageNodesPerZone, err = c.getCurrentMaxStorageNodesPerZone(cluster, nodeList, cloudProvider)
				if err != nil {
					logrus.Errorf("could not set a default value for max_storage_nodes_per_zone %v", err)
				}
			}
		}
		if err == nil && maxStorageNodesPerZone != 0 {
			toUpdate.Spec.CloudStorage.MaxStorageNodesPerZone = &maxStorageNodesPerZone
			logrus.Infof("setting spec.cloudStorage.maxStorageNodesPerZone %v", maxStorageNodesPerZone)
		}
	}

	if err := c.Driver.SetDefaultsOnStorageCluster(toUpdate); err != nil {
		return err
	}

	// Update the cluster only if anything has changed
	if !reflect.DeepEqual(cluster, toUpdate) {
		toUpdate.DeepCopyInto(cluster)
		if err := c.client.Update(context.TODO(), cluster); err != nil {
			return err
		}

		cluster.Status = *toUpdate.Status.DeepCopy()
		if err := c.client.Status().Update(context.TODO(), cluster); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) getNodeToStoragePods(
	cluster *corev1.StorageCluster,
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
	cluster *corev1.StorageCluster,
) ([]*v1.Pod, error) {
	// List all pods to include those that don't match the selector anymore but
	// have a ControllerRef pointing to this controller.
	podList := &v1.PodList{}
	err := c.client.List(context.TODO(), podList, &client.ListOptions{Namespace: cluster.Namespace})
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
	undeletedCluster := k8scontroller.RecheckDeletionTimestamp(func(ctx context.Context) (metav1.Object, error) {
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
	return cm.ClaimPods(context.TODO(), allPods)
}

func (c *Controller) createStorageNode(
	cluster *corev1.StorageCluster,
	nodeName string,
) {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            nodeName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          c.Driver.GetSelectorLabels(),
		},
		Status: corev1.NodeStatus{
			Phase: string(corev1.NodeInitStatus),
		},
	}
	err := c.client.Create(context.TODO(), storageNode)
	if err == nil {
		err = c.client.Status().Update(context.TODO(), storageNode)
		if err != nil {
			logrus.Warnf("Failed to update status of StorageNode %s/%s. %v",
				nodeName, cluster.Namespace, err)
		}
	} else if !errors.IsAlreadyExists(err) {
		logrus.Warnf("Failed to create StorageNode %s/%s. %v", nodeName, cluster.Namespace, err)
	}
}

// StorageClusterSelectorLabels returns the selector labels for child objects of given storagecluster
func (c *Controller) StorageClusterSelectorLabels(cluster *corev1.StorageCluster) map[string]string {
	clusterLabels := c.Driver.GetSelectorLabels()
	if clusterLabels == nil {
		clusterLabels = make(map[string]string)
	}
	clusterLabels[constants.LabelKeyClusterName] = cluster.Name
	clusterLabels[constants.LabelKeyDriverName] = c.Driver.String()
	return clusterLabels
}

func (c *Controller) log(clus *corev1.StorageCluster) *logrus.Entry {
	logFields := logrus.Fields{
		"cluster": clus.Name,
	}

	return logrus.WithFields(logFields)
}

func storagePodsEnabled(
	cluster *corev1.StorageCluster,
) bool {
	disabled, err := strconv.ParseBool(cluster.Annotations[constants.AnnotationDisableStorage])
	return err != nil || !disabled
}

func getCRDBasePath() string {
	return crdBasePath
}

func getDeprecatedCRDBasePath() string {
	return deprecatedCRDBasePath
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
