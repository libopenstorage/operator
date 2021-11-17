package migration

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libopenstorage/operator/drivers/storage"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	v1helper "k8s.io/kubernetes/pkg/apis/core/v1/helper"
	pluginhelper "k8s.io/kubernetes/pkg/scheduler/framework/plugins/helper"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	portworxDaemonSetName          = "portworx"
	portworxContainerName          = "portworx"
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

	wait.PollImmediateInfinite(migrationRetryIntervalFunc(), func() (bool, error) {
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

		if !h.isMigrationApproved(cluster) {
			return false, nil
		}

		if err := h.processMigration(cluster, pxDaemonSet); err != nil {
			logrus.Errorf("Migration failed, will retry in %v. %v", migrationRetryIntervalFunc(), err)
			return false, nil
		}

		logrus.Infof("Migration completed successfully")
		return true, nil
	})
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
	// TODO: Implement this
	// 1. Backup existing specs
	// 2. Migrate components

	nodeList := &v1.NodeList{}
	if err := h.client.List(context.TODO(), nodeList, &client.ListOptions{}); err != nil {
		return err
	}
	nodes, err := h.markAllNodesAsPending(nodeList)
	if err != nil {
		return err
	}

	// Unblock operator reconcile loop to start managing the storagecluster
	cluster.Status.Phase = constants.PhaseMigrationInProgress
	if err := h.client.Status().Update(context.TODO(), cluster, &client.UpdateOptions{}); err != nil {
		return err
	}

	if err := h.updateDaemonsetToRunOnPendingNodes(ds); err != nil {
		return err
	}

	portworxNodes := sortedPortworxNodes(cluster, nodes)
	for _, node := range portworxNodes {
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
		if err := h.waitForPortworxPod(cluster, node.Name, nodeLog); err != nil {
			return err
		}

		if err := h.markMigrationAsDone(node); err != nil {
			return err
		}

		nodeLog.Infof("Portworx pod migration status: %s", node.Labels[constants.LabelPortworxDaemonsetMigration])
	}

	logrus.Infof("Deleting portworx DaemonSet")
	if err := h.deletePortworxDaemonSet(ds); err != nil {
		return err
	}

	logrus.Infof("Removing migration label from all nodes")
	if err := h.unmarkAllDoneNodes(); err != nil {
		return err
	}

	return nil
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
			nodeLog.Errorf("Failed to list daemonset portworx pods. %v", err)
			return false, nil
		}

		podPresent := false
		for _, pod := range podList.Items {
			owner := metav1.GetControllerOf(&pod)
			if owner != nil && owner.UID == ds.UID && pod.Spec.NodeName == nodeName {
				podPresent = true
				break
			}
		}
		if podPresent {
			nodeLog.Debugf("DaemonSet portworx pod is still present")
			return false, nil
		}

		nodeLog.Debugf("DaemonSet portworx pod is no longer present")
		return true, nil
	})
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
			nodeLog.Errorf("Failed to list operator managed portworx pods. %v", err)
			return false, nil
		}

		var portworxPod *v1.Pod
		for _, pod := range podList.Items {
			owner := metav1.GetControllerOf(&pod)
			if owner != nil && owner.UID == cluster.UID && pod.Spec.NodeName == nodeName {
				portworxPod = pod.DeepCopy()
				break
			}
		}
		if portworxPod == nil {
			nodeLog.Debugf("Operator managed portworx pod not found")
			return false, nil
		}
		if portworxPod.DeletionTimestamp != nil || !podutil.IsPodReady(portworxPod) {
			nodeLog.Debugf("Operator managed portworx pod is not ready")
			return false, nil
		}

		nodeLog.Debugf("Operator managed portworx pod is ready")
		return true, nil
	})
}

func (h *Handler) createStorageCluster(
	ds *appsv1.DaemonSet,
) (*corev1.StorageCluster, error) {
	stc := h.constructStorageCluster(ds)

	// TODO: Handle enable stork
	// TODO: Handle enable autopilot
	// TODO: Handle enable monitoring

	logrus.Infof("Creating StorageCluster %v/%v for migration", stc.Namespace, stc.Name)
	err := h.client.Create(context.TODO(), stc)
	if err == nil {
		stc.Status.Phase = constants.PhaseAwaitingApproval
		err = h.client.Status().Update(context.TODO(), stc)
	}
	return stc, err
}

func (h *Handler) constructStorageCluster(ds *appsv1.DaemonSet) *corev1.StorageCluster {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
	}

	c := getPortworxContainer(ds)
	cluster.Spec.Image = c.Image
	cluster.Spec.ImagePullPolicy = c.ImagePullPolicy
	if len(ds.Spec.Template.Spec.ImagePullSecrets) > 0 {
		cluster.Spec.ImagePullSecret = stringPtr(ds.Spec.Template.Spec.ImagePullSecrets[0].Name)
	}

	envMap := map[string]*v1.EnvVar{}
	kvdbSecret := struct {
		ca       string
		cert     string
		key      string
		aclToken string
		userpwd  string
	}{}
	miscArgs := ""

	for i := 0; i < len(c.Args); i++ {
		arg := c.Args[i]
		if arg == "-c" {
			cluster.Name = c.Args[i+1]
			i++
		} else if arg == "-x" {
			i++
		} else if arg == "-a" || arg == "-A" || arg == "-f" {
			initStorageSpec(cluster)
			if arg == "-a" {
				cluster.Spec.Storage.UseAll = boolPtr(true)
			}
			if arg == "-A" {
				cluster.Spec.Storage.UseAllWithPartitions = boolPtr(true)
			}
			if arg == "-f" {
				cluster.Spec.Storage.ForceUseDisks = boolPtr(true)
			}
		} else if arg == "-s" && strings.HasPrefix(c.Args[i+i], "/") {
			initStorageSpec(cluster)
			devices := []string{}
			if cluster.Spec.Storage.Devices != nil {
				devices = *cluster.Spec.Storage.Devices
			}
			devices = append(devices, c.Args[i+1])
			cluster.Spec.Storage.Devices = &devices
			i++
		} else if arg == "-j" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			cluster.Spec.Storage.JournalDevice = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-metadata" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			cluster.Spec.Storage.SystemMdDevice = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-kvdb_dev" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			cluster.Spec.Storage.KvdbDevice = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-cache" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			devices := []string{}
			if cluster.Spec.Storage.CacheDevices != nil {
				devices = *cluster.Spec.Storage.CacheDevices
			}
			devices = append(devices, c.Args[i+1])
			cluster.Spec.Storage.CacheDevices = &devices
			i++
		} else if arg == "-s" && !strings.HasPrefix(c.Args[i+i], "/") {
			initCloudStorageSpec(cluster)
			devices := []string{}
			if cluster.Spec.CloudStorage.DeviceSpecs != nil {
				devices = *cluster.Spec.CloudStorage.DeviceSpecs
			}
			devices = append(devices, c.Args[i+1])
			cluster.Spec.CloudStorage.DeviceSpecs = &devices
			i++
		} else if arg == "-j" && !strings.HasPrefix(c.Args[i+1], "/") {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.JournalDeviceSpec = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-metadata" && !strings.HasPrefix(c.Args[i+1], "/") {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.SystemMdDeviceSpec = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-kvdb_dev" && !strings.HasPrefix(c.Args[i+1], "/") {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.KvdbDeviceSpec = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-max_drive_set_count" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.MaxStorageNodes = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-max_storage_nodes_per_zone" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.MaxStorageNodesPerZone = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-max_storage_nodes_per_zone_per_nodegroup" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.MaxStorageNodesPerZonePerNodeGroup = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-node_pool_label" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.NodePoolLabel = c.Args[i+1]
			i++
		} else if arg == "-cloud_provider" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.Provider = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-b" {
			initKvdbSpec(cluster)
			cluster.Spec.Kvdb.Internal = true
		} else if arg == "-k" {
			initKvdbSpec(cluster)
			cluster.Spec.Kvdb.Endpoints = strings.Split(c.Args[i+1], ",")
			i++
		} else if arg == "-ca" {
			kvdbSecret.ca = c.Args[i+1]
			i++
		} else if arg == "-cert" {
			kvdbSecret.cert = c.Args[i+1]
			i++
		} else if arg == "-key" {
			kvdbSecret.key = c.Args[i+1]
			i++
		} else if arg == "-acltoken" {
			kvdbSecret.aclToken = c.Args[i+1]
			i++
		} else if arg == "-userpwd" {
			kvdbSecret.userpwd = c.Args[i+1]
			i++
		} else if arg == "-d" {
			initNetworkSpec(cluster)
			cluster.Spec.Network.DataInterface = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-m" {
			initNetworkSpec(cluster)
			cluster.Spec.Network.MgmtInterface = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-secret_type" {
			cluster.Spec.SecretsProvider = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-r" {
			cluster.Spec.StartPort = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-rt_opts" {
			initRuntimeOptions(cluster)
			rtOpts := strings.Split(c.Args[i+1], ",")
			for _, opt := range rtOpts {
				s := strings.Split(opt, "=")
				cluster.Spec.RuntimeOpts[s[0]] = s[1]
			}
			i++
		} else if arg == "-marketplace_name" {
			envMap["MARKETPLACE_NAME"] = &v1.EnvVar{
				Name:  "MARKETPLACE_NAME",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-csi_endpoint" {
			envMap["CSI_ENDPOINT"] = &v1.EnvVar{
				Name:  "CSI_ENDPOINT",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-csiversion" {
			envMap["PORTWORX_CSIVERSION"] = &v1.EnvVar{
				Name:  "PORTWORX_CSIVERSION",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-oidc_issuer" {
			envMap["PORTWORX_AUTH_OIDC_ISSUER"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_OIDC_ISSUER",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-oidc_client_id" {
			envMap["PORTWORX_AUTH_OIDC_CLIENTID"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_OIDC_CLIENTID",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-oidc_custom_claim_namespace" {
			envMap["PORTWORX_AUTH_OIDC_CUSTOM_NAMESPACE"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_OIDC_CUSTOM_NAMESPACE",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_issuer" {
			envMap["PORTWORX_AUTH_JWT_ISSUER"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_ISSUER",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_shared_secret" {
			envMap["PORTWORX_AUTH_JWT_SHAREDSECRET"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_SHAREDSECRET",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_rsa_pubkey_file" {
			envMap["PORTWORX_AUTH_JWT_RSA_PUBKEY"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_RSA_PUBKEY",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_ecds_pubkey_file" {
			envMap["PORTWORX_AUTH_JWT_ECDS_PUBKEY"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_ECDS_PUBKEY",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-username_claim" {
			envMap["PORTWORX_AUTH_USERNAME_CLAIM"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_USERNAME_CLAIM",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-auth_system_key" {
			envMap["PORTWORX_AUTH_SYSTEM_KEY"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_SYSTEM_KEY",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "--log" {
			cluster.Annotations[pxutil.AnnotationLogFile] = c.Args[i+1]
			i++
		} else {
			miscArgs += arg + " "
		}
	}

	// Populate env variables from args and env vars of portworx container
	for _, env := range c.Env {
		if env.Name == "PX_TEMPLATE_VERSION" {
			continue
		}
		envMap[env.Name] = env.DeepCopy()
	}
	if len(envMap) > 0 {
		cluster.Spec.Env = []v1.EnvVar{}
	}
	for _, env := range envMap {
		cluster.Spec.Env = append(cluster.Spec.Env, *env)
	}

	if len(miscArgs) > 0 {
		cluster.Annotations[pxutil.AnnotationMiscArgs] = strings.TrimSpace(miscArgs)
	}

	// Fill the placement strategy based on node affinity and tolerations
	if ds.Spec.Template.Spec.Affinity != nil &&
		ds.Spec.Template.Spec.Affinity.NodeAffinity != nil {
		cluster.Spec.Placement = &corev1.PlacementSpec{
			NodeAffinity: ds.Spec.Template.Spec.Affinity.NodeAffinity.DeepCopy(),
		}
	}
	if len(ds.Spec.Template.Spec.Tolerations) > 0 {
		if cluster.Spec.Placement == nil {
			cluster.Spec.Placement = &corev1.PlacementSpec{
				Tolerations: []v1.Toleration{},
			}
		}
		for _, t := range ds.Spec.Template.Spec.Tolerations {
			cluster.Spec.Placement.Tolerations = append(cluster.Spec.Placement.Tolerations, *(t.DeepCopy()))
		}
	}

	// Populate the update strategy
	if ds.Spec.UpdateStrategy.Type == appsv1.RollingUpdateDaemonSetStrategyType {
		cluster.Spec.UpdateStrategy.Type = corev1.RollingUpdateStorageClusterStrategyType
		maxUnavailable := intstr.FromInt(1)
		if ds.Spec.UpdateStrategy.RollingUpdate != nil {
			maxUnavailable = *ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable
		}
		cluster.Spec.UpdateStrategy.RollingUpdate = &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		}
	} else if ds.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
		cluster.Spec.UpdateStrategy.Type = corev1.OnDeleteStorageClusterStrategyType
	}

	// Enable CSI
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == "csi-node-driver-registrar" {
			cluster.Spec.FeatureGates = map[string]string{
				"CSI": "true",
			}
			break
		}
	}

	_, hasSystemKey := envMap["PORTWORX_AUTH_SYSTEM_KEY"]
	_, hasJWTIssuer := envMap["PORTWORX_AUTH_JWT_ISSUER"]
	if hasSystemKey && hasJWTIssuer {
		// We don't support OIDC mode of authentication in the security spec.
		// User who are using OIDC will continue to use the env variables and
		// won't need to enable security through spec.security in StorageCluster.
		cluster.Spec.Security = &corev1.SecuritySpec{
			Enabled: true,
			Auth: &corev1.AuthSpec{
				GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
			},
		}
	}

	// TODO: Handle kvdb secret
	// TODO: Handle volumes
	// TODO: Handle custom annotations
	// TODO: Handle custom image registry

	return cluster
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
		selectorTerms[i].MatchExpressions = append(term.MatchExpressions, v1.NodeSelectorRequirement{
			Key:      constants.LabelPortworxDaemonsetMigration,
			Operator: v1.NodeSelectorOpIn,
			Values:   []string{constants.LabelValueMigrationPending},
		})
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
		return selectedNodes[i].Name < selectedNodes[j].Name
	})

	return selectedNodes
}

func fitsNode(pod *v1.Pod, node *v1.Node) bool {
	fitsNodeAffinity := pluginhelper.PodMatchesNodeSelectorAndAffinityTerms(pod, node)
	fitsTaints := v1helper.TolerationsTolerateTaintsWithFilter(pod.Spec.Tolerations, node.Spec.Taints, func(t *v1.Taint) bool {
		return t.Effect == v1.TaintEffectNoExecute || t.Effect == v1.TaintEffectNoSchedule
	})
	return fitsNodeAffinity && fitsTaints
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
			return c.Args[i+1]
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

func initStorageSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.Storage == nil {
		cluster.Spec.Storage = &corev1.StorageSpec{}
	}
}

func initCloudStorageSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.CloudStorage == nil {
		cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	}
}

func initNetworkSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.Network == nil {
		cluster.Spec.Network = &corev1.NetworkSpec{}
	}
}

func initKvdbSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.Kvdb == nil {
		cluster.Spec.Kvdb = &corev1.KvdbSpec{}
	}
}

func initRuntimeOptions(cluster *corev1.StorageCluster) {
	if cluster.Spec.RuntimeOpts == nil {
		cluster.Spec.RuntimeOpts = map[string]string{}
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func stringPtr(value string) *string {
	return &value
}

func stringSlicePtr(value []string) *[]string {
	return &value
}

func uint32Ptr(strValue string) *uint32 {
	v, _ := strconv.Atoi(strValue)
	value := uint32(v)
	return &value
}

func guestAccessTypePtr(value corev1.GuestAccessType) *corev1.GuestAccessType {
	return &value
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
