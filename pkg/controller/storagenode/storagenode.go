package storagenode

import (
	"context"
	"fmt"
	"time"

	"github.com/libopenstorage/operator/pkg/constants"

	k8scontroller "k8s.io/kubernetes/pkg/controller"

	"github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
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
	_              reconcile.Reconciler = &Controller{}
	crdBaseDir                          = getCRDBasePath
	controllerKind                      = corev1alpha1.SchemeGroupVersion.WithKind("StorageNode")
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

func (c *Controller) syncStorageNode(storagenode *corev1alpha1.StorageNode) error {
	// currently the only thing we do here is if this storagenode has drives (aks not storageless), ensure the PX pods
	// running on this node has the storage label set
	c.log(storagenode).Infof("Reconciling StorageNode")
	ownerRefs := storagenode.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		c.log(storagenode).Warnf("owner reference not set for storagenode: %s", storagenode.Name)
		return nil
	}

	owner := ownerRefs[0]
	if owner.Kind != "StorageCluster" {
		return fmt.Errorf("unknown owner kind: %s for storage node: %s", owner.Kind, storagenode.Name)
	}

	cluster := &corev1alpha1.StorageCluster{}
	err := c.client.Get(context.TODO(), client.ObjectKey{
		Name:      owner.Name,
		Namespace: storagenode.Namespace,
	}, cluster)
	if err != nil {
		return err
	}

	// ensure kvdb pods are present on nodes running kvdb
	// list kvdb nodes on the node for this cluster
	kvdbPodList := &v1.PodList{}
	fieldSelector := fields.SelectorFromSet(map[string]string{"nodeName": storagenode.Name})
	err = c.client.List(context.TODO(), kvdbPodList, &client.ListOptions{
		Namespace:     storagenode.Namespace,
		LabelSelector: labels.SelectorFromSet(c.kvdbPodLabels(cluster)),
		FieldSelector: fieldSelector,
	})
	if err != nil {
		return err
	}

	c.log(storagenode).Infof("[debug] dumping pods on node: %s", storagenode.Name)
	for _, p := range kvdbPodList.Items {
		c.log(storagenode).Infof("[debug] pods on node: [%s] %s", p.Namespace, p.Name)
	}

	if isNodeRunningKVDB(storagenode) { // create kvdb pod if not present
		node := &v1.Node{}
		isBeingDeleted := false
		err = c.client.Get(context.TODO(), client.ObjectKey{Name: storagenode.Name}, node)
		if err != nil {
			c.log(storagenode).Warnf("failed to get node: %s due to: %s", storagenode.Name, err)
		} else {
			isBeingDeleted, err = k8s.IsNodeBeingDeleted(node, c.client)
			if err != nil {
				c.log(storagenode).Warnf("failed to check if node: %s is being deleted due to: %v", node.Name, err)
			}
		}

		if !isBeingDeleted && len(kvdbPodList.Items) == 0 {
			c.log(storagenode).Infof("[debug] need to ensure kvdb pods on: %s", storagenode.Name)
			pod, err := c.createKVDBPod(cluster, storagenode)
			if err != nil {
				return err
			}

			c.log(storagenode).Infof("creating pod: %v", pod)
			err = c.client.Create(context.TODO(), pod)
			if err != nil {
				return err
			}
		}
	} else { // delete pods if present
		c.log(storagenode).Infof("[debug] need to ensure NO kvdb pods")
		for _, p := range kvdbPodList.Items {
			c.log(storagenode).Debugf("deleting kvdb pod: %s/%s", p.Namespace, p.Name)
			err = c.podControl.DeletePod(p.Namespace, p.Name, storagenode)
			if err != nil {
				c.log(storagenode).Warnf("failed to delete pod: %s/%s due to: %v", p.Namespace, p.Name, err)
			}
		}
	}

	// sync the storage labels on pods
	portworxPodList := &v1.PodList{}
	pxLabels := c.Driver.GetSelectorLabels()
	err = c.client.List(
		context.TODO(),
		portworxPodList,
		&client.ListOptions{
			Namespace:     storagenode.Namespace,
			LabelSelector: labels.SelectorFromSet(pxLabels),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to get list of portworx pods. %v", err)
	}

	for _, pod := range portworxPodList.Items {
		podCopy := pod.DeepCopy()
		controllerRef := metav1.GetControllerOf(podCopy)
		if controllerRef != nil && controllerRef.UID == owner.UID &&
			len(pod.Spec.NodeName) != 0 && storagenode.Name == podCopy.Spec.NodeName {
			updateNeeded := false
			value, present := podCopy.GetLabels()[constants.StoragePodLabelKey]
			if canNodeServeStorage(storagenode) { // node has storage
				// ensure pod has quorum member label
				if value != constants.TrueLabelValue {
					if podCopy.Labels == nil {
						podCopy.Labels = make(map[string]string)
					}
					podCopy.Labels[constants.StoragePodLabelKey] = constants.TrueLabelValue
					updateNeeded = true
				}
			} else if present { // ensure pod does not have quorum member label
				delete(podCopy.Labels, constants.StoragePodLabelKey)
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

	newPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-kvdb-", c.Driver.String()),
			Namespace:    storageNode.Namespace,
			Labels:       c.kvdbPodLabels(cluster),
		},
		Spec: podSpec,
	}
	return newPod, nil
}

func (c *Controller) kvdbPodLabels(cluster *corev1alpha1.StorageCluster) map[string]string {
	return map[string]string{
		constants.LabelKeyName:       cluster.Name,
		constants.LabelKeyDriverName: c.Driver.String(),
		constants.KVDBPodLabelKey:    constants.TrueLabelValue,
	}
}

func (c *Controller) log(storageNode *corev1alpha1.StorageNode) *logrus.Entry {
	fields := logrus.Fields{
		"node": storageNode.Name,
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
