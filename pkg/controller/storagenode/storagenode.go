package storagenode

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/operator/drivers/storage"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
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
	ControllerName        = "storagenode-controller"
	storageNodeCRDFile    = "core_v1_storagenode_crd.yaml"
	validateCRDInterval   = 5 * time.Second
	validateCRDTimeout    = 1 * time.Minute
	crdBasePath           = "/crds"
	deprecatedCRDBasePath = "/crds/deprecated"
)

var (
	_                    reconcile.Reconciler = &Controller{}
	crdBaseDir                                = getCRDBasePath
	deprecatedCRDBaseDir                      = getDeprecatedCRDBasePath
)

// Controller reconciles a StorageCluster object
type Controller struct {
	Driver storage.Driver
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
	ctrl     controller.Controller
	// Node to NodeInfo map
	nodeInfoMap map[string]*k8s.NodeInfo
}

// Init initialize the storage storagenode controller
func (c *Controller) Init(mgr manager.Manager) error {
	c.client = mgr.GetClient()
	c.scheme = mgr.GetScheme()
	c.recorder = mgr.GetEventRecorderFor(ControllerName)
	c.nodeInfoMap = make(map[string]*k8s.NodeInfo)

	var err error
	// Create a new controller
	c.ctrl, err = controller.New(ControllerName, mgr, controller.Options{Reconciler: c})
	if err != nil {
		return err
	}

	return nil
}

// StartWatch starts the watch on the StorageNode
func (c *Controller) StartWatch() error {
	err := c.ctrl.Watch(
		&source.Kind{Type: &corev1.StorageNode{}},
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
func (c *Controller) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	// Fetch the StorageNode instance
	storagenode := &corev1.StorageNode{}
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
		if strings.Contains(err.Error(), k8s.UpdateRevisionConflictErr) {
			logrus.Warnf("failed to sync StorageNode %s/%s: %v", storagenode.Namespace, storagenode.Name, err)
			return reconcile.Result{}, nil
		}
		k8s.WarningEvent(c.recorder, storagenode, util.FailedSyncReason, err.Error())
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// RegisterCRD registers the storage node CRD
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
	// Create and validate StorageNode CRD
	crd, err := k8s.GetCRDFromFile(storageNodeCRDFile, crdBaseDir())
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

	resource := fmt.Sprintf("%s.%s", corev1.StorageNodeResourcePlural, corev1.SchemeGroupVersion.Group)
	err = apiextensionsops.Instance().ValidateCRD(resource, validateCRDTimeout, validateCRDInterval)
	if err != nil {
		return err
	}

	return nil
}

func (c *Controller) createOrUpdateDeprecatedCRD() error {
	// Create and validate StorageNode CRD
	crd, err := k8s.GetV1beta1CRDFromFile(storageNodeCRDFile, deprecatedCRDBaseDir())
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
		Plural: corev1.StorageNodeResourcePlural,
		Group:  corev1.SchemeGroupVersion.Group,
	}
	err = apiextensionsops.Instance().ValidateCRDV1beta1(resource, validateCRDTimeout, validateCRDInterval)
	if err != nil {
		return err
	}

	return nil
}

func (c *Controller) syncStorageNode(storageNode *corev1.StorageNode) error {
	c.log(storageNode).Infof("Reconciling StorageNode")
	owner := metav1.GetControllerOf(storageNode)
	if owner == nil {
		c.log(storageNode).Warnf("owner reference not set")
		return nil
	}

	if owner.Kind != "StorageCluster" {
		return fmt.Errorf("unknown owner kind: %s for storage node: %s", owner.Kind, storageNode.Name)
	}

	cluster := &corev1.StorageCluster{}
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
	cluster *corev1.StorageCluster,
	storageNode *corev1.StorageNode,
) error {
	if cluster.Spec.Kvdb != nil && !cluster.Spec.Kvdb.Internal {
		return nil
	}

	// Do not use c.client.List API, as it would hit controller runtime cache, which does not count pod in outOfPods status,
	// operator would keep creating kvdb pods in this case.
	// https://portworx.atlassian.net/browse/CEE-400
	// https://portworx.atlassian.net/browse/OPERATOR-809
	podList, err := coreops.Instance().GetPods(cluster.Namespace, c.kvdbPodLabels(cluster))
	if err != nil {
		return err
	}
	var kvdbPods []v1.Pod
	for _, p := range podList.Items {
		if p.Spec.NodeName == storageNode.Name {
			// Let's delete the pod so that a new one will be created.
			reason := strings.ToLower(p.Status.Reason)
			if reason == "outofpods" || reason == "evicted" || reason == "terminated" {
				logrus.Warningf("Found pod with %s status, will delete it: %+v", p.Status.Reason, p)
				err = coreops.Instance().DeletePod(p.Name, p.Namespace, false)
				if err != nil {
					return err
				}
			} else {
				kvdbPods = append(kvdbPods, p)
			}
		}
	}

	if isNodeRunningKVDB(storageNode) { // create kvdb pod if not present
		node := &v1.Node{}
		isBeingDeleted := false
		isRecentlyCreatedAfterNodeCordoned := false
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
			isRecentlyCreatedAfterNodeCordoned = k8s.IsPodRecentlyCreatedAfterNodeCordoned(node, c.nodeInfoMap, cluster)
		}

		if !isBeingDeleted && !isRecentlyCreatedAfterNodeCordoned && len(kvdbPods) == 0 {
			pod, err := c.createKVDBPod(cluster, storageNode)
			if err != nil {
				return err
			}

			c.log(storageNode).Infof("creating kvdb pod: %s/%s", pod.Namespace, pod.Name)
			_, err = coreops.Instance().CreatePod(pod)
			if err != nil {
				return err
			}

			nodeInfo, ok := c.nodeInfoMap[storageNode.Name]
			if ok {
				nodeInfo.LastPodCreationTime = time.Now()
			} else {
				c.nodeInfoMap[storageNode.Name] = &k8s.NodeInfo{
					NodeName:             storageNode.Name,
					LastPodCreationTime:  time.Now(),
					CordonedRestartDelay: constants.DefaultCordonedRestartDelay,
				}
			}
		}
	} else { // delete pods if present
		for _, p := range kvdbPods {
			c.log(storageNode).Debugf("deleting kvdb pod: %s/%s", p.Namespace, p.Name)
			err = coreops.Instance().DeletePod(p.Name, p.Namespace, false)
			if err != nil {
				c.log(storageNode).Warnf("failed to delete pod: %s/%s due to: %v", p.Namespace, p.Name, err)
			}
		}
	}
	return nil
}
func (c *Controller) syncStorage(
	cluster *corev1.StorageCluster,
	storageNode *corev1.StorageNode,
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
			FieldSelector: fields.SelectorFromSet(map[string]string{"nodeName": storageNode.Name}),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to get list of portworx pods. %v", err)
	}

	for _, pod := range portworxPodList.Items {
		podCopy := pod.DeepCopy()
		controllerRef := metav1.GetControllerOf(podCopy)
		if controllerRef != nil &&
			controllerRef.UID == cluster.UID && pod.DeletionTimestamp == nil &&
			storageNode.Name == pod.Spec.NodeName {
			updateNeeded := false
			value, storageLabelPresent := podCopy.GetLabels()[constants.LabelKeyStoragePod]
			if canNodeServeStorage(storageNode) { // node has storage
				if value != constants.LabelValueTrue {
					if podCopy.Labels == nil {
						podCopy.Labels = make(map[string]string)
					}
					podCopy.Labels[constants.LabelKeyStoragePod] = constants.LabelValueTrue
					updateNeeded = true
				}
			} else {
				if storageLabelPresent {
					c.log(storageNode).Debugf("Removing storage label from pod: %s/%s",
						podCopy.Namespace, pod.Name)
					delete(podCopy.Labels, constants.LabelKeyStoragePod)
					updateNeeded = true
				}

				value, present := podCopy.Annotations[constants.AnnotationPodSafeToEvict]
				if (!present || value != constants.LabelValueTrue) &&
					!isNodeRunningKVDB(storageNode) {
					c.log(storageNode).Debugf("Adding %s annotation to pod: %s/%s",
						constants.AnnotationPodSafeToEvict, pod.Namespace, pod.Name)
					if podCopy.Annotations == nil {
						podCopy.Annotations = make(map[string]string)
					}
					podCopy.Annotations[constants.AnnotationPodSafeToEvict] = constants.LabelValueTrue
					updateNeeded = true
				}
			}

			if updateNeeded {
				// TODO: get latest pod to update
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
	cluster *corev1.StorageCluster,
	storageNode *corev1.StorageNode,
) (*v1.Pod, error) {
	podSpec, err := c.Driver.GetKVDBPodSpec(cluster, storageNode.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create kvdb pod template: %v", err)
	}

	k8s.AddOrUpdateStoragePodTolerations(&podSpec)
	ownerRef := metav1.NewControllerRef(storageNode, corev1.SchemeGroupVersion.WithKind("StorageNode"))
	newPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName:    fmt.Sprintf("%s-kvdb-", c.Driver.String()),
			Namespace:       storageNode.Namespace,
			Labels:          c.kvdbPodLabels(cluster),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: podSpec,
	}
	return newPod, nil
}

func (c *Controller) kvdbPodLabels(cluster *corev1.StorageCluster) map[string]string {
	return map[string]string{
		constants.LabelKeyClusterName: cluster.Name,
		constants.LabelKeyDriverName:  c.Driver.String(),
		constants.LabelKeyKVDBPod:     constants.LabelValueTrue,
	}
}

func (c *Controller) log(storageNode *corev1.StorageNode) *logrus.Entry {
	fields := logrus.Fields{
		"storagenode": storageNode.Name,
	}

	return logrus.WithFields(fields)
}

func getCRDBasePath() string {
	return crdBasePath
}

func getDeprecatedCRDBasePath() string {
	return deprecatedCRDBasePath
}

func canNodeServeStorage(storagenode *corev1.StorageNode) bool {
	if storagenode.Status.NodeAttributes == nil ||
		storagenode.Status.NodeAttributes.Storage == nil ||
		!*storagenode.Status.NodeAttributes.Storage {
		return false
	}

	// look for node status condition
	for _, cond := range storagenode.Status.Conditions {
		if cond.Type == corev1.NodeStateCondition {
			if cond.Status == corev1.NodeOnlineStatus ||
				cond.Status == corev1.NodeMaintenanceStatus ||
				cond.Status == corev1.NodeDegradedStatus {
				return true
			}
		}
	}
	return false
}

func isNodeRunningKVDB(storagenode *corev1.StorageNode) bool {
	return storagenode.Status.NodeAttributes != nil &&
		storagenode.Status.NodeAttributes.KVDB != nil &&
		*storagenode.Status.NodeAttributes.KVDB
}
