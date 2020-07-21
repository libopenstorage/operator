package storagenode

import (
	"context"
	"fmt"
	"time"

	"github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	k8scontroller "k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// ControllerName is the name of the controller
	ControllerName          = "storagenode-controller"
	storageNodeCRDFile      = "core_v1alpha1_storagenode_crd.yaml"
	validateCRDInterval     = 5 * time.Second
	validateCRDTimeout      = 1 * time.Minute
	crdBasePath             = "/crds"
	storageNodeStatusPlural = "storagenodestatuses"
)

var (
	_          reconcile.Reconciler = &Controller{}
	crdBaseDir                      = getCRDBasePath
)

// Controller reconciles a StorageCluster object
type Controller struct {
	Driver storage.Driver
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client     client.Client
	scheme     *runtime.Scheme
	recorder   record.EventRecorder
	podControl k8scontroller.PodControlInterface
}

// Init initialize the storage storagenode controller
func (c *Controller) Init(mgr manager.Manager) error {
	c.client = mgr.GetClient()
	c.scheme = mgr.GetScheme()
	c.recorder = mgr.GetEventRecorderFor(ControllerName)

	// Create pod control interface object to manage pods under storage cluster
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("error getting kubernetes client: %v", err)
	}
	c.podControl = k8scontroller.RealPodControl{
		KubeClient: clientset,
		Recorder:   c.recorder,
	}

	// Create a new controller
	ctrl, err := controller.New(ControllerName, mgr, controller.Options{Reconciler: c})
	if err != nil {
		return err
	}

	// Watch for changes to StorageNode
	err = ctrl.Watch(
		&source.Kind{Type: &corev1alpha1.StorageNode{}},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		return err
	}

	return nil
}

// Reconcile reconciles based on the status of the StorageNode object.
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (c *Controller) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// Fetch the StorageNode instance
	storagenode := &corev1alpha1.StorageNode{}
	err := c.client.Get(context.TODO(), request.NamespacedName, storagenode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if err := c.syncStorageNode(storagenode); err != nil {
		k8s.WarningEvent(c.recorder, storagenode, util.FailedSyncReason, err.Error())
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// RegisterCRD registers the storage node CRD
func (c *Controller) RegisterCRD() error {
	// Create and validate StorageNode CRD
	crd, err := k8s.GetCRDFromFile(storageNodeCRDFile, crdBaseDir())
	if err != nil {
		return err
	}
	err = apiextensionsops.Instance().RegisterCRD(crd)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	resource := apiextensionsops.CustomResource{
		Plural: corev1alpha1.StorageNodeResourcePlural,
		Group:  corev1alpha1.SchemeGroupVersion.Group,
	}
	err = apiextensionsops.Instance().ValidateCRD(resource, validateCRDTimeout, validateCRDInterval)
	if err != nil {
		return err
	}

	// Delete StorageNodeStatus CRD as it is not longer used
	nodeStatusCRDName := fmt.Sprintf("%s.%s",
		storageNodeStatusPlural,
		corev1alpha1.SchemeGroupVersion.Group,
	)
	err = apiextensionsops.Instance().DeleteCRD(nodeStatusCRDName)
	if err != nil && !errors.IsNotFound(err) {
		logrus.Warnf("Failed to delete CRD %s: %v", nodeStatusCRDName, err)
	}
	return nil
}

func (c *Controller) syncStorageNode(storageNode *corev1alpha1.StorageNode) error {
	c.log(storageNode).Infof("Reconciling StorageNode")
	ownerRefs := storageNode.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		c.log(storageNode).Warnf("owner reference not set")
		return nil
	}

	owner := ownerRefs[0]
	if owner.Kind != "StorageCluster" {
		return fmt.Errorf("unknown owner kind: %s for storage node: %s", owner.Kind, storageNode.Name)
	}

	cluster := &corev1alpha1.StorageCluster{}
	err := c.client.Get(context.TODO(), client.ObjectKey{
		Name:      owner.Name,
		Namespace: storageNode.Namespace,
	}, cluster)
	if err != nil {
		return err
	}

	if err := c.syncKVDB(cluster, storageNode); err != nil {
		return err
	}

	if err := c.syncStorage(cluster, storageNode); err != nil {
		return err
	}

	return nil
}

func (c *Controller) syncKVDB(
	cluster *corev1alpha1.StorageCluster,
	storageNode *corev1alpha1.StorageNode,
) error {
	if cluster.Spec.Kvdb != nil && !cluster.Spec.Kvdb.Internal {
		return nil
	}

	// ensure kvdb pods are present on nodes running kvdb
	// list kvdb nodes on the node for this cluster
	kvdbPodList := &v1.PodList{}
	fieldSelector := fields.SelectorFromSet(map[string]string{"nodeName": storageNode.Name})
	err := c.client.List(context.TODO(), kvdbPodList, &client.ListOptions{
		Namespace:     storageNode.Namespace,
		LabelSelector: labels.SelectorFromSet(c.kvdbPodLabels(cluster)),
		FieldSelector: fieldSelector,
	})
	if err != nil {
		return err
	}

	if isNodeRunningKVDB(storageNode) { // create kvdb pod if not present
		node := &v1.Node{}
		isBeingDeleted := false
		err = c.client.Get(context.TODO(), client.ObjectKey{Name: storageNode.Name}, node)
		if err != nil {
			if errors.IsNotFound(err) {
				c.log(storageNode).Debugf("Kubernetes node is no longer present")
				isBeingDeleted = true
			} else {
				c.log(storageNode).Warnf("failed to get node: %s due to: %s", storageNode.Name, err)
			}
		} else {
			isBeingDeleted, err = k8s.IsNodeBeingDeleted(node, c.client)
			if err != nil {
				c.log(storageNode).Warnf("failed to check if node: %s is being deleted due to: %v", node.Name, err)
			}
		}

		if !isBeingDeleted && len(kvdbPodList.Items) == 0 {
			pod, err := c.createKVDBPod(cluster, storageNode)
			if err != nil {
				return err
			}

			c.log(storageNode).Infof("creating kvdb pod: %s/%s", pod.Namespace, pod.Name)
			err = c.client.Create(context.TODO(), pod)
			if err != nil {
				return err
			}
		}
	} else { // delete pods if present
		for _, p := range kvdbPodList.Items {
			c.log(storageNode).Debugf("deleting kvdb pod: %s/%s", p.Namespace, p.Name)
			err = c.client.Delete(context.TODO(), &p)
			if err != nil {
				c.log(storageNode).Warnf("failed to delete pod: %s/%s due to: %v", p.Namespace, p.Name, err)
			}
		}
	}
	return nil
}
func (c *Controller) syncStorage(
	cluster *corev1alpha1.StorageCluster,
	storageNode *corev1alpha1.StorageNode,
) error {
	// sync the storage labels on pods
	portworxPodList := &v1.PodList{}
	pxLabels := c.Driver.GetSelectorLabels()
	err := c.client.List(
		context.TODO(),
		portworxPodList,
		&client.ListOptions{
			Namespace:     storageNode.Namespace,
			LabelSelector: labels.SelectorFromSet(pxLabels),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to get list of portworx pods. %v", err)
	}

	for _, pod := range portworxPodList.Items {
		podCopy := pod.DeepCopy()
		controllerRef := metav1.GetControllerOf(podCopy)
		if controllerRef != nil && controllerRef.UID == cluster.UID &&
			len(pod.Spec.NodeName) != 0 && storageNode.Name == podCopy.Spec.NodeName {
			updateNeeded := false
			value, present := podCopy.GetLabels()[constants.LabelKeyStoragePod]
			if canNodeServeStorage(storageNode) { // node has storage
				if value != constants.LabelValueTrue {
					if podCopy.Labels == nil {
						podCopy.Labels = make(map[string]string)
					}
					podCopy.Labels[constants.LabelKeyStoragePod] = constants.LabelValueTrue
					updateNeeded = true
				}
			} else if present {
				c.log(storageNode).Debugf("removing storage label from pod: %s/%s",
					podCopy.Namespace, pod.Name)
				delete(podCopy.Labels, constants.LabelKeyStoragePod)
				updateNeeded = true
			}

			if updateNeeded {
				if err := c.client.Update(context.TODO(), podCopy); err != nil {
					return err
				}
			}
			break // found pod we were looking for, no need to check other pods
		}
	}
	return nil
}

func (c *Controller) createKVDBPod(
	cluster *corev1alpha1.StorageCluster,
	storageNode *corev1alpha1.StorageNode,
) (*v1.Pod, error) {
	podSpec, err := c.Driver.GetKVDBPodSpec(cluster, storageNode.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create kvdb pod template: %v", err)
	}

	k8s.AddOrUpdateStoragePodTolerations(&podSpec)
	podSpec.NodeName = storageNode.Name

	trueVar := true
	newPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-kvdb-", c.Driver.String()),
			Namespace:    storageNode.Namespace,
			Labels:       c.kvdbPodLabels(cluster),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         corev1alpha1.SchemeGroupVersion.String(),
					Kind:               "StorageNode",
					Name:               storageNode.Namespace,
					UID:                storageNode.UID,
					Controller:         &trueVar,
					BlockOwnerDeletion: &trueVar,
				},
			},
		},
		Spec: podSpec,
	}
	return newPod, nil
}

func (c *Controller) kvdbPodLabels(cluster *corev1alpha1.StorageCluster) map[string]string {
	return map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  c.Driver.String(),
		constants.LabelKeyKVDBPod:     constants.LabelValueTrue,
	}
}

func (c *Controller) log(storageNode *corev1alpha1.StorageNode) *logrus.Entry {
	fields := logrus.Fields{
		"storagenode": storageNode.Name,
	}

	return logrus.WithFields(fields)
}

func getCRDBasePath() string {
	return crdBasePath
}

func canNodeServeStorage(storagenode *corev1alpha1.StorageNode) bool {
	if storagenode.Status.Storage.TotalSize.IsZero() {
		return false
	}

	// look for node status condition
	for _, cond := range storagenode.Status.Conditions {
		if cond.Type == corev1alpha1.NodeStateCondition {
			if cond.Status == corev1alpha1.NodeOnlineStatus ||
				cond.Status == corev1alpha1.NodeMaintenanceStatus ||
				cond.Status == corev1alpha1.NodeDegradedStatus {
				return true
			}
		}
	}
	return false
}

func isNodeRunningKVDB(storageNode *corev1alpha1.StorageNode) bool {
	for _, cond := range storageNode.Status.Conditions {
		if cond.Type == corev1alpha1.NodeKVDBCondition {
			return true
		}
	}

	return false
}
