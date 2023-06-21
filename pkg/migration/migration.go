package migration

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	schedulehelper "k8s.io/component-helpers/scheduling/corev1"
	affinityhelper "k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/libopenstorage/operator/drivers/storage"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	portworxDaemonSetName          = "portworx"
	portworxContainerName          = "portworx"
	defaultSecretsNamespace        = "portworx"
	migrationRetryInterval         = 30 * time.Second
	podWaitInterval                = 10 * time.Second
	daemonSetPodTerminationTimeout = 5 * time.Minute
	operatorPodReadyTimeout        = 10 * time.Minute
)

// These function variables are introduced for unit testing
var (
	migrationRetryIntervalFunc         = getMigrationRetryInterval
	daemonSetPodTerminationTimeoutFunc = getDaemonSetPodTerminationTimeout
	operatorPodReadyTimeoutFunc        = getOperatorPodReadyTimeout
)

// Handler object that carries out migration of Portworx Daemonset
// and it's components to operator managed StorageCluster object
type Handler struct {
	ctrl   *storagecluster.Controller
	client client.Client
	driver storage.Driver
}

// New creates a new instance of migration handler
func New(ctrl *storagecluster.Controller) *Handler {
	return &Handler{
		ctrl:   ctrl,
		client: ctrl.GetKubernetesClient(),
		driver: ctrl.Driver,
	}
}

// Start starts the migration
func (h *Handler) Start() {
	var pxDaemonSet *appsv1.DaemonSet

	pollErr := wait.PollImmediateInfinite(migrationRetryIntervalFunc(), func() (bool, error) {
		var err error
		pxDaemonSet, err = h.getPortworxDaemonSet(pxDaemonSet)
		if errors.IsNotFound(err) {
			logrus.Infof("Migration is not needed")
			return true, nil
		} else if err != nil {
			logrus.Errorf("Failed to get portworx DaemonSet. %v", err)
			return false, nil
		}

		cluster, err := h.createStorageClusterIfAbsent(pxDaemonSet)
		if err != nil {
			logrus.Errorf("Migration failed to create StorageCluster. %v", err)
			return false, nil
		}

		err = h.backup(CollectionConfigMapName, cluster.Namespace, true)
		if err != nil {
			logrus.Errorf("Failed to collect daemonset components. %v", err)
			return false, nil
		}

		err = h.dryRun(cluster, pxDaemonSet)
		if err != nil {
			k8sutil.WarningEvent(h.ctrl.GetEventRecorder(), cluster, util.MigrationDryRunFailedReason,
				fmt.Sprintf("Dry-run failed: %v", err))
			return false, nil
		}

		if !h.isMigrationApproved(cluster) {
			return false, nil
		}

		err = h.backup(BackupConfigMapName, cluster.Namespace, false)
		if err != nil {
			logrus.Errorf("Failed to backup daemonset components. %v", err)
			return false, nil
		}

		if err := h.processMigration(cluster, pxDaemonSet); err != nil {
			k8sutil.WarningEvent(h.ctrl.GetEventRecorder(), cluster, util.MigrationFailedReason,
				fmt.Sprintf("Migration failed, will retry in %v, %v", migrationRetryIntervalFunc(), err))

			updatedCluster := &corev1.StorageCluster{}
			if err := h.client.Get(context.TODO(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, updatedCluster); err != nil {
				logrus.Errorf("Failed to get StorageCluster. %v", err)
			}
			updatedCluster.Status.Phase = string(corev1.ClusterStateDegraded)
			if err := k8sutil.UpdateStorageClusterStatus(h.client, updatedCluster); err != nil {
				logrus.Errorf("Failed to update StorageCluster status. %v", err)
			}
			return false, nil
		}

		k8sutil.InfoEvent(h.ctrl.GetEventRecorder(), cluster, util.MigrationCompletedReason,
			"Migration completed successfully",
		)
		return true, nil
	})

	if pollErr != nil {
		logrus.Errorf("Failed while polling for the migration. %v", pollErr)
	}
}

func (h *Handler) createStorageClusterIfAbsent(ds *appsv1.DaemonSet) (*corev1.StorageCluster, error) {
	clusterName := getPortworxClusterName(ds)
	stc := &corev1.StorageCluster{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      clusterName,
			Namespace: ds.Namespace,
		},
		stc,
	)
	if err == nil {
		return stc, nil
	} else if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	stc, err = h.createStorageCluster(ds)
	// TODO: Create dry run specs
	return stc, err
}

func (h *Handler) processMigration(
	cluster *corev1.StorageCluster,
	ds *appsv1.DaemonSet,
) error {
	nodeList := &v1.NodeList{}
	if err := h.client.List(context.TODO(), nodeList, &client.ListOptions{}); err != nil {
		return err
	}
	nodes, err := h.markAllNodesAsPending(nodeList)
	if err != nil {
		return err
	}

	// TODO: This can be done using the storagecluster status. Once we have the status reported
	// correctly as we go through different phases of migration we can use that instead of this
	// internal annotation.
	// Block the component migration until the portworx pod migration is finished.
	cluster.Annotations[constants.AnnotationPauseComponentMigration] = "true"
	if err := h.client.Update(context.TODO(), cluster, &client.UpdateOptions{}); err != nil {
		return err
	}
	// Unblock operator reconcile loop to start managing the storagecluster
	util.UpdateStorageClusterCondition(cluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeMigration,
		Status: corev1.ClusterConditionStatusInProgress,
	})
	if err := k8sutil.UpdateStorageClusterStatus(h.client, cluster); err != nil {
		return err
	}

	if err := h.updateDaemonsetToRunOnPendingNodes(ds); err != nil {
		return err
	}

	portworxNodes := sortedPortworxNodes(cluster, nodes)
	if cluster.Spec.UpdateStrategy.Type == corev1.OnDeleteStorageClusterStrategyType {
		if err := h.portworxNodesOnDeleteUpdate(cluster, ds, portworxNodes); err != nil {
			return err
		}
	} else if err := h.portworxNodesRollingUpdate(cluster, ds, portworxNodes); err != nil {
		return err
	}

	logrus.Infof("Starting migration of components")
	logrus.Infof("Deleting old components")
	if err := h.deleteComponents(cluster); err != nil {
		return err
	}

	updatedCluster := &corev1.StorageCluster{}
	if err := h.client.Get(context.TODO(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, updatedCluster); err != nil {
		return err
	}
	// Notify operator to start installing the new components
	logrus.Infof("Starting operator managed components")
	delete(updatedCluster.Annotations, constants.AnnotationPauseComponentMigration)
	if err := h.client.Update(context.TODO(), updatedCluster, &client.UpdateOptions{}); err != nil {
		return err
	}

	// TODO: Wait for all components to be up, before marking the migration as completed

	// Mark migration as completed in the cluster condition list
	updatedCluster = &corev1.StorageCluster{}
	if err := h.client.Get(context.TODO(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, updatedCluster); err != nil {
		return err
	}
	util.UpdateStorageClusterCondition(updatedCluster, &corev1.ClusterCondition{
		Source: pxutil.PortworxComponentName,
		Type:   corev1.ClusterConditionTypeMigration,
		Status: corev1.ClusterConditionStatusCompleted,
	})
	if err := k8sutil.UpdateStorageClusterStatus(h.client, updatedCluster); err != nil {
		return err
	}

	// TODO: once daemonset is deleted, if we restart operator all code after this line
	// will not be re-executed, so we should delete daemonset after everything is finished.
	logrus.Infof("Deleting portworx DaemonSet")
	if err := h.deletePortworxDaemonSet(ds); err != nil {
		return err
	}

	// Unmark the nodes after the daemonset has been deleted, else it will create pods again
	logrus.Infof("Removing migration label from all nodes")
	if err := h.unmarkAllDoneNodes(); err != nil {
		return err
	}

	// TODO: Remove the daemonset migration annotation, once we start adding the migration
	// events and conditions in the status. That way there is a more permanent record that
	// this cluster is a result of migration. After we have added that we can remove the
	// migration-approved annotation after a successful migration.

	return nil
}

func (h *Handler) portworxNodesRollingUpdate(
	cluster *corev1.StorageCluster,
	ds *appsv1.DaemonSet,
	portworxNodes []*v1.Node,
) error {
	for _, node := range portworxNodes {
		stc := &corev1.StorageCluster{}
		err := h.client.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
			stc,
		)
		if err != nil {
			return err
		} else if stc.Spec.UpdateStrategy.Type != corev1.RollingUpdateStorageClusterStrategyType {
			return fmt.Errorf("StorageCluster update strategy changed during migration, expect %s, actual %s",
				corev1.RollingUpdateStorageClusterStrategyType, stc.Spec.UpdateStrategy.Type)
		}

		nodeLog := logrus.WithField("node", node.Name)
		nodeLog.Infof("Starting migration of portworx pod")

		value := node.Labels[constants.LabelPortworxDaemonsetMigration]
		if value == constants.LabelValueMigrationDone {
			nodeLog.Infof("Portworx pod already migrated")
			continue
		} else if value == constants.LabelValueMigrationSkip {
			nodeLog.Infof("Portworx pod migration skipped")
			continue
		}

		if err := h.markMigrationAsStarting(node); err != nil {
			return err
		}

		// Wait for daemonset pod to terminate, else it causes conflicts with
		// the operator managed portworx pod
		if err := h.waitForDaemonSetPodTermination(ds, node.Name, nodeLog); err != nil {
			return err
		}

		if err := h.markMigrationAsInProgress(node); err != nil {
			return err
		}

		// Wait until operator managed portworx pod is ready
		if err := h.waitForPortworxPod(stc, node.Name, nodeLog); err != nil {
			return err
		}

		if err := h.markMigrationAsDone(node); err != nil {
			return err
		}

		nodeLog.Infof("Portworx pod migration status: %s", node.Labels[constants.LabelPortworxDaemonsetMigration])
	}
	return nil
}

// portworxNodesOnDeleteUpdate expects user to mark the nodes as Starting manually, will exit until:
// 1. All portworx nodes are marked as migrated
// 2. StorageCluster not found
// 3. StorageCluster update strategy changed by the user, retry migration from the beginning
func (h *Handler) portworxNodesOnDeleteUpdate(
	cluster *corev1.StorageCluster,
	ds *appsv1.DaemonSet,
	portworxNodes []*v1.Node,
) error {
	return wait.PollImmediateInfinite(migrationRetryIntervalFunc(), func() (bool, error) {
		stc := &corev1.StorageCluster{}
		err := h.client.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
			stc,
		)
		if err != nil {
			if errors.IsNotFound(err) {
				return false, err
			}
			return false, nil
		} else if stc.Spec.UpdateStrategy.Type != corev1.OnDeleteStorageClusterStrategyType {
			return false, fmt.Errorf("StorageCluster update strategy changed during migration, expect %s, actual %s",
				corev1.OnDeleteStorageClusterStrategyType, stc.Spec.UpdateStrategy.Type)
		}

		allNodesMigrated := true
		for _, n := range portworxNodes {
			nodeLog := logrus.WithField("node", n.Name)
			node := &v1.Node{}
			if err := h.client.Get(context.TODO(), types.NamespacedName{Name: n.Name}, node); err != nil {
				nodeLog.Errorf("Failed to get node. %v", err)
				return false, nil
			}
			value := node.Labels[constants.LabelPortworxDaemonsetMigration]
			if value == constants.LabelValueMigrationDone || value == constants.LabelValueMigrationSkip {
				continue
			} else if value == constants.LabelValueMigrationPending {
				allNodesMigrated = false
			} else if value == constants.LabelValueMigrationStarting {
				allNodesMigrated = false
				// mark the node as InProgress if DaemonSet pod not found
				portworxPod, err := h.getDaemonSetPortworxPod(ds, node.Name)
				if err != nil {
					nodeLog.Errorf("Failed to list daemonset portworx pods. %v", err)
					return false, nil
				}
				if portworxPod == nil {
					nodeLog.Infof("DaemonSet portworx pod is no longer present")
					if err := h.markMigrationAsInProgress(node); err != nil {
						return false, nil
					}
				}
			} else if value == constants.LabelValueMigrationInProgress {
				// mark the node as Done if operator portworx pod found
				portworxPod, err := h.getOperatorPortworxPod(cluster, node.Name)
				if err != nil {
					nodeLog.Errorf("Failed to list operator managed portworx pods. %v", err)
					return false, nil
				}
				if portworxPod != nil && portworxPod.DeletionTimestamp == nil && podutil.IsPodReady(portworxPod) {
					nodeLog.Infof("Operator managed portworx pod is ready")
					if err := h.markMigrationAsDone(node); err != nil {
						return false, nil
					}
					continue
				}
				allNodesMigrated = false
			} else {
				allNodesMigrated = false
				nodeLog.Errorf("Unexpected daemonset migration label value: %s", value)
			}
		}
		return allNodesMigrated, nil
	})
}

func (h *Handler) waitForDaemonSetPodTermination(
	ds *appsv1.DaemonSet,
	nodeName string,
	nodeLog *logrus.Entry,
) error {
	return wait.PollImmediate(podWaitInterval, daemonSetPodTerminationTimeoutFunc(), func() (bool, error) {
		node := &v1.Node{}
		if err := h.client.Get(context.TODO(), types.NamespacedName{Name: nodeName}, node); err != nil {
			nodeLog.Errorf("Failed to get node. %v", err)
			return false, nil
		}
		value := node.Labels[constants.LabelPortworxDaemonsetMigration]
		if value == constants.LabelValueMigrationSkip {
			return true, nil
		}

		portworxPod, err := h.getDaemonSetPortworxPod(ds, nodeName)
		if err != nil {
			nodeLog.Errorf("Failed to list daemonset portworx pods. %v", err)
			return false, nil
		}
		if portworxPod != nil {
			nodeLog.Debugf("DaemonSet portworx pod is still present")
			return false, nil
		}

		nodeLog.Infof("DaemonSet portworx pod is no longer present")
		return true, nil
	})
}

func (h *Handler) getDaemonSetPortworxPod(
	ds *appsv1.DaemonSet,
	nodeName string,
) (*v1.Pod, error) {
	podList := &v1.PodList{}
	fieldSelector := fields.SelectorFromSet(map[string]string{"nodeName": nodeName})
	err := h.client.List(
		context.TODO(),
		podList,
		&client.ListOptions{
			Namespace:     ds.Namespace,
			FieldSelector: fieldSelector,
			LabelSelector: labels.SelectorFromSet(map[string]string{"name": "portworx"}),
		},
	)
	if err != nil {
		return nil, err
	}
	for _, pod := range podList.Items {
		owner := metav1.GetControllerOf(&pod)
		if owner != nil && owner.UID == ds.UID && pod.Spec.NodeName == nodeName {
			return pod.DeepCopy(), nil
		}
	}
	return nil, nil
}

func (h *Handler) waitForPortworxPod(
	cluster *corev1.StorageCluster,
	nodeName string,
	nodeLog *logrus.Entry,
) error {
	return wait.PollImmediate(podWaitInterval, operatorPodReadyTimeoutFunc(), func() (bool, error) {
		node := &v1.Node{}
		if err := h.client.Get(context.TODO(), types.NamespacedName{Name: nodeName}, node); err != nil {
			nodeLog.Errorf("Failed to get node. %v", err)
			return false, nil
		}
		value := node.Labels[constants.LabelPortworxDaemonsetMigration]
		if value == constants.LabelValueMigrationSkip {
			return true, nil
		}

		portworxPod, err := h.getOperatorPortworxPod(cluster, nodeName)
		if err != nil {
			nodeLog.Errorf("Failed to list operator managed portworx pods. %v", err)
			return false, nil
		}
		if portworxPod == nil {
			nodeLog.Debugf("Operator managed portworx pod not found")
			return false, nil
		}
		if portworxPod.DeletionTimestamp != nil || !podutil.IsPodReady(portworxPod) {
			nodeLog.Debugf("Operator managed portworx pod is not ready")
			return false, nil
		}

		nodeLog.Infof("Operator managed portworx pod is ready")
		return true, nil
	})
}

func (h *Handler) getOperatorPortworxPod(
	cluster *corev1.StorageCluster,
	nodeName string,
) (*v1.Pod, error) {
	podList := &v1.PodList{}
	fieldSelector := fields.SelectorFromSet(map[string]string{"nodeName": nodeName})
	err := h.client.List(
		context.TODO(),
		podList,
		&client.ListOptions{
			Namespace:     cluster.Namespace,
			FieldSelector: fieldSelector,
			LabelSelector: labels.SelectorFromSet(h.ctrl.StorageClusterSelectorLabels(cluster)),
		},
	)
	if err != nil {
		return nil, err
	}
	for _, pod := range podList.Items {
		owner := metav1.GetControllerOf(&pod)
		if owner != nil && owner.UID == cluster.UID && pod.Spec.NodeName == nodeName {
			return pod.DeepCopy(), nil
		}
	}
	return nil, nil
}

func (h *Handler) isMigrationApproved(cluster *corev1.StorageCluster) bool {
	approved, err := strconv.ParseBool(cluster.Annotations[constants.AnnotationMigrationApproved])
	return err == nil && approved
}

func (h *Handler) markMigrationAsStarting(n *v1.Node) error {
	node := &v1.Node{}
	if err := h.client.Get(context.TODO(), types.NamespacedName{Name: n.Name}, node); err != nil {
		return err
	}
	n.Labels = node.Labels
	value := node.Labels[constants.LabelPortworxDaemonsetMigration]
	if value == constants.LabelValueMigrationPending {
		node.Labels[constants.LabelPortworxDaemonsetMigration] = constants.LabelValueMigrationStarting
		return h.client.Update(context.TODO(), node, &client.UpdateOptions{})
	}
	return nil
}

func (h *Handler) markMigrationAsInProgress(n *v1.Node) error {
	node := &v1.Node{}
	if err := h.client.Get(context.TODO(), types.NamespacedName{Name: n.Name}, node); err != nil {
		return err
	}
	n.Labels = node.Labels
	value := node.Labels[constants.LabelPortworxDaemonsetMigration]
	if value == constants.LabelValueMigrationStarting {
		node.Labels[constants.LabelPortworxDaemonsetMigration] = constants.LabelValueMigrationInProgress
		return h.client.Update(context.TODO(), node, &client.UpdateOptions{})
	}
	return nil
}

func (h *Handler) markMigrationAsDone(n *v1.Node) error {
	node := &v1.Node{}
	if err := h.client.Get(context.TODO(), types.NamespacedName{Name: n.Name}, node); err != nil {
		return err
	}
	n.Labels = node.Labels
	value := node.Labels[constants.LabelPortworxDaemonsetMigration]
	if value == constants.LabelValueMigrationInProgress {
		node.Labels[constants.LabelPortworxDaemonsetMigration] = constants.LabelValueMigrationDone
		return h.client.Update(context.TODO(), node, &client.UpdateOptions{})
	}
	return nil
}

func (h *Handler) markAllNodesAsPending(nodeList *v1.NodeList) ([]*v1.Node, error) {
	nodes := []*v1.Node{}
	for _, n := range nodeList.Items {
		node := n.DeepCopy()
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		if v := node.Labels[constants.LabelPortworxDaemonsetMigration]; strings.TrimSpace(v) == "" {
			node.Labels[constants.LabelPortworxDaemonsetMigration] = constants.LabelValueMigrationPending
			if err := h.client.Update(context.TODO(), node, &client.UpdateOptions{}); err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (h *Handler) unmarkAllDoneNodes() error {
	nodeList := &v1.NodeList{}
	if err := h.client.List(context.TODO(), nodeList, &client.ListOptions{}); err != nil {
		return err
	}

	for _, node := range nodeList.Items {
		value := node.Labels[constants.LabelPortworxDaemonsetMigration]
		if value == constants.LabelValueMigrationPending || value == constants.LabelValueMigrationDone {
			delete(node.Labels, constants.LabelPortworxDaemonsetMigration)
			if err := h.client.Update(context.TODO(), &node, &client.UpdateOptions{}); err != nil {
				logrus.Errorf("Failed to remove migration label from node: %v. %v", node.Name, err)
			}
		}
	}
	return nil
}

func (h *Handler) getPortworxDaemonSet(ds *appsv1.DaemonSet) (*appsv1.DaemonSet, error) {
	if ds == nil {
		return h.findPortworxDaemonSet()
	}

	pxDaemonSet := &appsv1.DaemonSet{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      portworxDaemonSetName,
			Namespace: ds.Namespace,
		},
		pxDaemonSet,
	)
	return pxDaemonSet, err
}

func (h *Handler) findPortworxDaemonSet() (*appsv1.DaemonSet, error) {
	dsList := &appsv1.DaemonSetList{}
	if err := h.client.List(context.TODO(), dsList, &client.ListOptions{}); err != nil {
		return nil, fmt.Errorf("failed to list daemonsets: %v", err)
	}

	for _, ds := range dsList.Items {
		if ds.Name == portworxDaemonSetName {
			return ds.DeepCopy(), nil
		}
	}

	return nil, errors.NewNotFound(appsv1.Resource("DaemonSet"), portworxDaemonSetName)
}

func (h *Handler) deletePortworxDaemonSet(ds *appsv1.DaemonSet) error {
	pxDaemonSet := &appsv1.DaemonSet{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      ds.Name,
			Namespace: ds.Namespace,
		},
		pxDaemonSet,
	)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	return h.client.Delete(context.TODO(), pxDaemonSet)
}

func (h *Handler) updateDaemonsetToRunOnPendingNodes(ds *appsv1.DaemonSet) error {
	// Change node affinity rules to enable rolling migration of pods from daemonset
	// to operator managed pods
	addMigrationConstraints(&ds.Spec.Template.Spec)
	// Set the update strategy to OnDelete to avoid restart of the daemonset
	// pods due to the node affinity changes
	ds.Spec.UpdateStrategy.Type = appsv1.OnDeleteDaemonSetStrategyType
	return h.client.Update(context.TODO(), ds, &client.UpdateOptions{})
}

func addMigrationConstraints(podSpec *v1.PodSpec) {
	if podSpec.Affinity == nil {
		podSpec.Affinity = &v1.Affinity{}
	}
	if podSpec.Affinity.NodeAffinity == nil {
		podSpec.Affinity.NodeAffinity = &v1.NodeAffinity{}
	}
	if podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &v1.NodeSelector{}
	}
	if len(podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
		podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = []v1.NodeSelectorTerm{{}}
	}
	selectorTerms := podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	for i, term := range selectorTerms {
		if term.MatchExpressions == nil {
			term.MatchExpressions = make([]v1.NodeSelectorRequirement, 0)
		}
		// Skip appending migration constraints if it's already there
		foundMigrationKey := false
		for _, expression := range selectorTerms[i].MatchExpressions {
			if expression.Key == constants.LabelPortworxDaemonsetMigration {
				foundMigrationKey = true
				break
			}
		}
		if !foundMigrationKey {
			selectorTerms[i].MatchExpressions = append(term.MatchExpressions, v1.NodeSelectorRequirement{
				Key:      constants.LabelPortworxDaemonsetMigration,
				Operator: v1.NodeSelectorOpIn,
				Values:   []string{constants.LabelValueMigrationPending},
			})
		}
	}
	podSpec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = selectorTerms
}

func sortedPortworxNodes(cluster *corev1.StorageCluster, nodes []*v1.Node) []*v1.Node {
	selectedNodes := []*v1.Node{}
	for _, node := range nodes {
		simulationPod := newSimulationPod(cluster)
		if fitsNode(simulationPod, node) {
			selectedNodes = append(selectedNodes, node.DeepCopy())
		} else {
			logrus.Infof("Node %v deemed to be unfit for portworx pod", node.Name)
		}
	}

	sort.Slice(selectedNodes, func(i, j int) bool {
		// Sort based on label first: Skip, Done, InProgress, Starting, Pending
		// then based on name
		labelMap := map[string]int{
			constants.LabelValueMigrationSkip:       0,
			constants.LabelValueMigrationDone:       1,
			constants.LabelValueMigrationInProgress: 2,
			constants.LabelValueMigrationStarting:   3,
			constants.LabelValueMigrationPending:    4,
		}
		iLabel := selectedNodes[i].Labels[constants.LabelPortworxDaemonsetMigration]
		jLabel := selectedNodes[j].Labels[constants.LabelPortworxDaemonsetMigration]
		if iLabel == jLabel {
			return selectedNodes[i].Name < selectedNodes[j].Name
		}
		return labelMap[iLabel] < labelMap[jLabel]
	})

	return selectedNodes
}

func fitsNode(pod *v1.Pod, node *v1.Node) bool {
	fitsNodeAffinity, err := affinityhelper.GetRequiredNodeAffinity(pod).Match(node)
	if err != nil {
		logrus.Warnf("Failed to check if the pod fits the node %s. %v", node.Name, err)
		return false
	}
	_, taintsUntolerated := schedulehelper.FindMatchingUntoleratedTaint(node.Spec.Taints, pod.Spec.Tolerations, func(t *v1.Taint) bool {
		return t.Effect == v1.TaintEffectNoExecute || t.Effect == v1.TaintEffectNoSchedule
	})
	return fitsNodeAffinity && !taintsUntolerated
}

func newSimulationPod(cluster *corev1.StorageCluster) *v1.Pod {
	simulationPod := &v1.Pod{}
	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			simulationPod.Spec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}
		if len(cluster.Spec.Placement.Tolerations) > 0 {
			simulationPod.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, t := range cluster.Spec.Placement.Tolerations {
				simulationPod.Spec.Tolerations = append(simulationPod.Spec.Tolerations, *(t.DeepCopy()))
			}
		}
	}
	k8sutil.AddOrUpdateStoragePodTolerations(&simulationPod.Spec)
	return simulationPod
}

func getPortworxClusterName(ds *appsv1.DaemonSet) string {
	c := getPortworxContainer(ds)
	for i, arg := range c.Args {
		if arg == "-c" {
			return getStorageClusterNameFromClusterID(c.Args[i+1])
		}
	}
	return ""
}

func getPortworxContainer(ds *appsv1.DaemonSet) *v1.Container {
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == portworxContainerName {
			return c.DeepCopy()
		}
	}
	return nil
}

func getMigrationRetryInterval() time.Duration {
	return migrationRetryInterval
}

func getDaemonSetPodTerminationTimeout() time.Duration {
	return daemonSetPodTerminationTimeout
}

func getOperatorPodReadyTimeout() time.Duration {
	return operatorPodReadyTimeout
}
