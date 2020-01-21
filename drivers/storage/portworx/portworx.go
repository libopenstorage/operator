package portworx

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"

	version "github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/openstorage/pkg/grpcserver"
	"github.com/libopenstorage/operator/drivers/storage"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	storkDriverName                   = "pxd"
	defaultPortworxImage              = "portworx/oci-monitor"
	defaultPortworxVersion            = "2.3.2"
	edgePortworxVersion               = "edge"
	defaultLighthouseImage            = "portworx/px-lighthouse:2.0.6"
	defaultAutopilotImage             = "portworx/autopilot:1.0.0"
	defaultStorkImage                 = "openstorage/stork:2.3.1"
	defaultSDKPort                    = 9020
	defaultSecretsProvider            = "k8s"
	defaultNodeWiperImage             = "portworx/px-node-wiper:2.1.2-rc1"
	envKeyNodeWiperImage              = "PX_NODE_WIPER_IMAGE"
	envKeyPortworxEnableTLS           = "PX_ENABLE_TLS"
	storageClusterDeleteMsg           = "Portworx service NOT removed. Portworx drives and data NOT wiped."
	storageClusterUninstallMsg        = "Portworx service removed. Portworx drives and data NOT wiped."
	storageClusterUninstallAndWipeMsg = "Portworx service removed. Portworx drives and data wiped."
	labelPortworxVersion              = "PX Version"
)

type portworx struct {
	k8sClient          client.Client
	k8sVersion         *version.Version
	scheme             *runtime.Scheme
	recorder           record.EventRecorder
	sdkConn            *grpc.ClientConn
	zoneToInstancesMap map[string]int
	cloudProvider      string
}

func (p *portworx) String() string {
	return pxutil.DriverName
}

func (p *portworx) Init(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) error {
	if k8sClient == nil {
		return fmt.Errorf("kubernetes client cannot be nil")
	}
	p.k8sClient = k8sClient
	if scheme == nil {
		return fmt.Errorf("kubernetes scheme cannot be nil")
	}
	p.scheme = scheme
	if recorder == nil {
		return fmt.Errorf("event recorder cannot be nil")
	}
	p.recorder = recorder
	k8sVersion, err := k8sutil.GetVersion()
	if err != nil {
		return err
	}
	p.k8sVersion = k8sVersion

	p.initializeComponents()
	return nil
}

func (p *portworx) initializeComponents() {
	for _, comp := range component.GetAll() {
		comp.Initialize(p.k8sClient, *p.k8sVersion, p.scheme, p.recorder)
	}
}

func (p *portworx) UpdateDriver(info *storage.UpdateDriverInfo) error {
	p.zoneToInstancesMap = info.ZoneToInstancesMap
	p.cloudProvider = info.CloudProvider
	return nil
}

func (p *portworx) GetStorkDriverName() (string, error) {
	return storkDriverName, nil
}

func (p *portworx) GetStorkEnvList(cluster *corev1alpha1.StorageCluster) []v1.EnvVar {
	return []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
}

func (p *portworx) GetSelectorLabels() map[string]string {
	return pxutil.SelectorLabels()
}

func (p *portworx) SetDefaultsOnStorageCluster(toUpdate *corev1alpha1.StorageCluster) {
	releases, err := manifest.NewReleaseManifest()
	if err != nil {
		logrus.Warnf(err.Error())
	}

	isPortworxEnabled := pxutil.IsPortworxEnabled(toUpdate)
	if len(strings.TrimSpace(toUpdate.Spec.Image)) == 0 {
		toUpdate.Spec.Image = defaultPortworxImage + ":" + defaultPortworxImageVersion(releases)
	}

	t, err := newTemplate(toUpdate)
	if err != nil {
		return
	}

	if isPortworxEnabled {
		setPortworxDefaults(toUpdate, t)
	}

	components, err := componentVersions(releases, t.pxVersion)
	if err != nil {
		logrus.Warnf(err.Error())
	}

	setComponentDefaults(toUpdate, components, isPortworxEnabled)
}

func (p *portworx) PreInstall(cluster *corev1alpha1.StorageCluster) error {
	for componentName, comp := range component.GetAll() {
		if comp.IsEnabled(cluster) {
			err := comp.Reconcile(cluster)
			if ce, ok := err.(*component.Error); ok &&
				ce.Code() == component.ErrCritical {
				return err
			} else if err != nil {
				msg := fmt.Sprintf("Failed to setup %s. %v", componentName, err)
				p.warningEvent(cluster, util.FailedComponentReason, msg)
			}
		} else {
			if err := comp.Delete(cluster); err != nil {
				msg := fmt.Sprintf("Failed to cleanup %v. %v", componentName, err)
				p.warningEvent(cluster, util.FailedComponentReason, msg)
			}
		}
	}
	return nil
}

func (p *portworx) DeleteStorage(
	cluster *corev1alpha1.StorageCluster,
) (*corev1alpha1.ClusterCondition, error) {
	p.markComponentsAsDeleted()

	if cluster.Spec.DeleteStrategy == nil || !pxutil.IsPortworxEnabled(cluster) {
		// No Delete strategy provided or Portworx not installed through the operator,
		// then do not wipe Portworx
		status := &corev1alpha1.ClusterCondition{
			Type:   corev1alpha1.ClusterConditionTypeDelete,
			Status: corev1alpha1.ClusterOperationCompleted,
			Reason: storageClusterDeleteMsg,
		}
		return status, nil
	}

	// Portworx needs to be removed if DeleteStrategy is specified
	removeData := false
	completeMsg := storageClusterUninstallMsg
	if cluster.Spec.DeleteStrategy.Type == corev1alpha1.UninstallAndWipeStorageClusterStrategyType {
		removeData = true
		completeMsg = storageClusterUninstallAndWipeMsg
	}

	u := NewUninstaller(cluster, p.k8sClient)
	completed, inProgress, total, err := u.GetNodeWiperStatus()
	if err != nil && errors.IsNotFound(err) {
		// Run the node wiper
		nodeWiperImage := k8sutil.GetValueFromEnv(envKeyNodeWiperImage, cluster.Spec.Env)
		if err := u.RunNodeWiper(nodeWiperImage, removeData); err != nil {
			return &corev1alpha1.ClusterCondition{
				Type:   corev1alpha1.ClusterConditionTypeDelete,
				Status: corev1alpha1.ClusterOperationFailed,
				Reason: "Failed to run node wiper: " + err.Error(),
			}, nil
		}
		return &corev1alpha1.ClusterCondition{
			Type:   corev1alpha1.ClusterConditionTypeDelete,
			Status: corev1alpha1.ClusterOperationInProgress,
			Reason: "Started node wiper daemonset",
		}, nil
	} else if err != nil {
		// We could not get the node wiper status and it does exist
		return nil, err
	}

	if completed != 0 && total != 0 && completed == total {
		// all the nodes are wiped
		if removeData {
			logrus.Debugf("Deleting portworx metadata")
			if err := u.WipeMetadata(); err != nil {
				logrus.Errorf("Failed to delete portworx metadata: %v", err)
				return &corev1alpha1.ClusterCondition{
					Type:   corev1alpha1.ClusterConditionTypeDelete,
					Status: corev1alpha1.ClusterOperationFailed,
					Reason: "Failed to wipe metadata: " + err.Error(),
				}, nil
			}
		}
		return &corev1alpha1.ClusterCondition{
			Type:   corev1alpha1.ClusterConditionTypeDelete,
			Status: corev1alpha1.ClusterOperationCompleted,
			Reason: completeMsg,
		}, nil
	}

	return &corev1alpha1.ClusterCondition{
		Type:   corev1alpha1.ClusterConditionTypeDelete,
		Status: corev1alpha1.ClusterOperationInProgress,
		Reason: fmt.Sprintf("Wipe operation still in progress: Completed [%v] In Progress [%v] Total [%v]", completed, inProgress, total),
	}, nil
}

func (p *portworx) markComponentsAsDeleted() {
	for _, comp := range component.GetAll() {
		comp.MarkDeleted()
	}
}

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

	clientConn, err := p.getPortworxClient(cluster)
	if err != nil {
		return err
	}

	clusterClient := api.NewOpenStorageClusterClient(clientConn)
	pxCluster, err := clusterClient.InspectCurrent(context.TODO(), &api.SdkClusterInspectCurrentRequest{})
	if err != nil {
		if closeErr := p.sdkConn.Close(); closeErr != nil {
			logrus.Warnf("Failed to close grpc connection. %v", closeErr)
		}
		p.sdkConn = nil
		return fmt.Errorf("failed to inspect cluster: %v", err)
	} else if pxCluster.Cluster == nil {
		return fmt.Errorf("empty ClusterInspect response")
	}

	cluster.Status.Phase = string(mapClusterStatus(pxCluster.Cluster.Status))
	cluster.Status.ClusterName = pxCluster.Cluster.Name
	cluster.Status.ClusterUID = pxCluster.Cluster.Id

	return p.updateStorageNodes(clientConn, cluster)
}

func (p *portworx) updateStorageNodes(
	clientConn *grpc.ClientConn,
	cluster *corev1alpha1.StorageCluster,
) error {
	nodeClient := api.NewOpenStorageNodeClient(clientConn)
	nodeEnumerateResponse, err := nodeClient.EnumerateWithFilters(
		context.TODO(),
		&api.SdkNodeEnumerateWithFiltersRequest{},
	)
	if err != nil {
		return fmt.Errorf("failed to enumerate nodes: %v", err)
	}

	currentNodes := make(map[string]bool)

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	for _, node := range nodeEnumerateResponse.Nodes {
		if node.SchedulerNodeName == "" {
			k8sNode, err := k8s.Instance().SearchNodeByAddresses(
				[]string{node.DataIp, node.MgmtIp, node.Hostname},
			)
			if err != nil {
				msg := fmt.Sprintf("Unable to find kubernetes node name for nodeID %v: %v", node.Id, err)
				p.warningEvent(cluster, util.FailedSyncReason, msg)
				continue
			}
			node.SchedulerNodeName = k8sNode.Name
		}

		currentNodes[node.SchedulerNodeName] = true

		phase := mapNodeStatus(node.Status)
		storageNode := &corev1alpha1.StorageNode{
			ObjectMeta: metav1.ObjectMeta{
				Name:            node.SchedulerNodeName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
				Labels:          p.GetSelectorLabels(),
			},
			Status: corev1alpha1.NodeStatus{
				NodeUID: node.Id,
				Network: corev1alpha1.NetworkStatus{
					DataIP: node.DataIp,
					MgmtIP: node.MgmtIp,
				},
				Phase: string(phase),
				// TODO: Add a human readable reason with the status
				Conditions: []corev1alpha1.NodeCondition{
					{
						Type:   corev1alpha1.NodeState,
						Status: phase,
					},
				},
			},
		}

		if version, ok := node.NodeLabels[labelPortworxVersion]; ok {
			storageNode.Spec = corev1alpha1.StorageNodeSpec{
				Version: version,
			}
		} else {
			partitions := strings.Split(cluster.Spec.Image, ":")
			if len(partitions) > 1 {
				storageNode.Spec = corev1alpha1.StorageNodeSpec{
					Version: partitions[len(partitions)-1],
				}
			}
		}

		err = k8sutil.CreateOrUpdateStorageNode(p.k8sClient, storageNode, ownerRef)
		if err != nil {
			msg := fmt.Sprintf("Failed to update status for nodeID %v: %v", node.Id, err)
			p.warningEvent(cluster, util.FailedSyncReason, msg)
		}
	}

	nodeStatusList := &corev1alpha1.StorageNodeList{}
	if err = p.k8sClient.List(context.TODO(), nodeStatusList, &client.ListOptions{}); err != nil {
		return fmt.Errorf("failed to get a list of StorageNode: %v", err)
	}

	for _, nodeStatus := range nodeStatusList.Items {
		if _, exists := currentNodes[nodeStatus.Name]; !exists {
			logrus.Debugf("Deleting orphan StorageNode %v/%v",
				nodeStatus.Namespace, nodeStatus.Name)
			err = p.k8sClient.Delete(context.TODO(), nodeStatus.DeepCopy())
			if err != nil && !errors.IsNotFound(err) {
				msg := fmt.Sprintf("Failed to delete StorageNode %v/%v: %v",
					nodeStatus.Namespace, nodeStatus.Name, err)
				p.warningEvent(cluster, util.FailedSyncReason, msg)
			}
		}
	}
	return nil
}

func (p *portworx) getPortworxClient(
	cluster *corev1alpha1.StorageCluster,
) (*grpc.ClientConn, error) {
	if p.sdkConn != nil {
		return p.sdkConn, nil
	}

	pxService := &v1.Service{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
		},
		pxService,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get k8s service spec: %v", err)
	} else if len(pxService.Spec.ClusterIP) == 0 {
		return nil, fmt.Errorf("failed to get endpoint for portworx volume driver")
	}

	endpoint := pxService.Spec.ClusterIP
	sdkPort := defaultSDKPort

	// Get the ports from service
	for _, pxServicePort := range pxService.Spec.Ports {
		if pxServicePort.Name == pxutil.PortworxSDKPortName && pxServicePort.Port != 0 {
			sdkPort = int(pxServicePort.Port)
		}
	}

	endpoint = fmt.Sprintf("%s:%d", endpoint, sdkPort)
	return p.getGrpcConn(endpoint)
}

func (p *portworx) warningEvent(
	cluster *corev1alpha1.StorageCluster,
	reason, message string,
) {
	logrus.Warn(message)
	p.recorder.Event(cluster, v1.EventTypeWarning, reason, message)
}

func (p *portworx) getGrpcConn(endpoint string) (*grpc.ClientConn, error) {
	dialOptions, err := getDialOptions(isTLSEnabled())
	if err != nil {
		return nil, err
	}
	p.sdkConn, err = grpcserver.Connect(endpoint, dialOptions)
	if err != nil {
		return nil, fmt.Errorf("error connecting to GRPC server [%s]: %v", endpoint, err)
	}
	return p.sdkConn, nil
}

func (p *portworx) storageNodeToCloudSpec(storageNodes []*corev1alpha1.StorageNode, cluster *corev1alpha1.StorageCluster) *cloudstorage.Config {

	res := &cloudstorage.Config{
		CloudStorage:            []cloudstorage.CloudDriveConfig{},
		StorageInstancesPerZone: cluster.Status.Storage.StorageNodesPerZone,
	}
	for _, storageNode := range storageNodes {
		if storageNode.Spec.CloudStorage.DriveConfigs != nil {
			for _, conf := range storageNode.Spec.CloudStorage.DriveConfigs {
				c := cloudstorage.CloudDriveConfig{
					Type:      conf.Type,
					SizeInGiB: conf.SizeInGiB,
					IOPS:      conf.IOPS,
					Options:   conf.Options,
				}
				res.CloudStorage = append(res.CloudStorage, c)
			}
			return res
		}
	}
	return nil
}

func getDialOptions(tls bool) ([]grpc.DialOption, error) {
	if !tls {
		return []grpc.DialOption{grpc.WithInsecure()}, nil
	}
	capool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("failed to load CA system certs: %v", err)
	}
	return []grpc.DialOption{grpc.WithTransportCredentials(
		credentials.NewClientTLSFromCert(capool, ""),
	)}, nil
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

func mapNodeStatus(status api.Status) corev1alpha1.ConditionStatus {
	switch status {
	case api.Status_STATUS_NONE:
		fallthrough
	case api.Status_STATUS_OFFLINE:
		fallthrough
	case api.Status_STATUS_ERROR:
		fallthrough
	case api.Status_STATUS_NEEDS_REBOOT:
		return corev1alpha1.NodeOffline

	case api.Status_STATUS_INIT:
		return corev1alpha1.NodeInit

	case api.Status_STATUS_NOT_IN_QUORUM:
		fallthrough
	case api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE:
		return corev1alpha1.NodeNotInQuorum

	case api.Status_STATUS_MAINTENANCE:
		return corev1alpha1.NodeMaintenance

	case api.Status_STATUS_OK:
		fallthrough
	case api.Status_STATUS_STORAGE_DOWN:
		return corev1alpha1.NodeOnline

	case api.Status_STATUS_DECOMMISSION:
		return corev1alpha1.NodeDecommissioned

	case api.Status_STATUS_STORAGE_DEGRADED:
		fallthrough
	case api.Status_STATUS_STORAGE_REBALANCE:
		fallthrough
	case api.Status_STATUS_STORAGE_DRIVE_REPLACE:
		return corev1alpha1.NodeDegraded

	default:
		return corev1alpha1.NodeUnknown
	}
}

func isTLSEnabled() bool {
	enabled, err := strconv.ParseBool(os.Getenv(envKeyPortworxEnableTLS))
	return err == nil && enabled
}

func componentVersions(releases *manifest.ReleaseManifest, pxVersion *version.Version) (manifest.Release, error) {
	if releases == nil {
		return manifest.Release{}, fmt.Errorf("release manifest is empty")
	}
	components, err := releases.GetFromVersion(pxVersion)
	if err != nil {
		logrus.Debugf("Could not find an entry for portworx %v in release manifest: %v", pxVersion, err)
		components, err = releases.Get(edgePortworxVersion)
		if err != nil {
			logrus.Debugf("Could not find an entry for 'edge' in release manifest: %v", err)
			components, err = releases.GetDefault()
			if err != nil {
				return manifest.Release{}, fmt.Errorf("error getting default release from manifest: %v", err)
			}
		}
	}
	return *components, nil
}

func defaultPortworxImageVersion(releases *manifest.ReleaseManifest) string {
	if releases != nil && len(releases.DefaultRelease) > 0 {
		return releases.DefaultRelease
	}
	return defaultPortworxVersion
}

func setPortworxDefaults(
	toUpdate *corev1alpha1.StorageCluster,
	t *template,
) {
	partitions := strings.Split(toUpdate.Spec.Image, ":")
	if len(partitions) > 1 {
		toUpdate.Spec.Version = partitions[len(partitions)-1]
	}

	if toUpdate.Spec.Kvdb == nil {
		toUpdate.Spec.Kvdb = &corev1alpha1.KvdbSpec{}
	}
	if len(toUpdate.Spec.Kvdb.Endpoints) == 0 {
		toUpdate.Spec.Kvdb.Internal = true
	}
	if toUpdate.Spec.SecretsProvider == nil {
		toUpdate.Spec.SecretsProvider = stringPtr(defaultSecretsProvider)
	}
	startPort := uint32(t.startPort)
	toUpdate.Spec.StartPort = &startPort

	// If no storage spec is provided, initialize one where Portworx takes all available drives
	if toUpdate.Spec.CloudStorage == nil && toUpdate.Spec.Storage == nil {
		toUpdate.Spec.Storage = &corev1alpha1.StorageSpec{}
	}
	if toUpdate.Spec.Storage != nil {
		if toUpdate.Spec.Storage.Devices == nil &&
			(toUpdate.Spec.Storage.UseAllWithPartitions == nil || !*toUpdate.Spec.Storage.UseAllWithPartitions) &&
			toUpdate.Spec.Storage.UseAll == nil {
			toUpdate.Spec.Storage.UseAll = boolPtr(true)
		}
	}

	setNodeSpecDefaults(toUpdate)

	if toUpdate.Spec.Placement == nil || toUpdate.Spec.Placement.NodeAffinity == nil {
		toUpdate.Spec.Placement = &corev1alpha1.PlacementSpec{
			NodeAffinity: &v1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: t.getSelectorRequirements(),
						},
					},
				},
			},
		}
	}
}

func setNodeSpecDefaults(toUpdate *corev1alpha1.StorageCluster) {
	if len(toUpdate.Spec.Nodes) == 0 {
		return
	}

	updatedNodeSpecs := make([]corev1alpha1.NodeSpec, 0)
	for _, nodeSpec := range toUpdate.Spec.Nodes {
		nodeSpecCopy := nodeSpec.DeepCopy()
		if nodeSpec.Storage == nil {
			nodeSpecCopy.Storage = toUpdate.Spec.Storage.DeepCopy()
		} else if toUpdate.Spec.Storage != nil {
			// Devices, UseAll and UseAllWithPartitions should be set exclusive of each other, if not already
			// set by the user in the node spec.
			if nodeSpecCopy.Storage.Devices == nil &&
				(nodeSpecCopy.Storage.UseAll == nil || !*nodeSpecCopy.Storage.UseAll) &&
				(nodeSpecCopy.Storage.UseAllWithPartitions == nil || !*nodeSpecCopy.Storage.UseAllWithPartitions) &&
				toUpdate.Spec.Storage.Devices != nil {
				devices := append(make([]string, 0), *toUpdate.Spec.Storage.Devices...)
				nodeSpecCopy.Storage.Devices = &devices
			}
			if nodeSpecCopy.Storage.UseAllWithPartitions == nil &&
				(nodeSpecCopy.Storage.UseAll == nil || !*nodeSpecCopy.Storage.UseAll) &&
				nodeSpecCopy.Storage.Devices == nil &&
				toUpdate.Spec.Storage.UseAllWithPartitions != nil {
				nodeSpecCopy.Storage.UseAllWithPartitions = boolPtr(*toUpdate.Spec.Storage.UseAllWithPartitions)
			}
			if nodeSpecCopy.Storage.UseAll == nil &&
				(nodeSpecCopy.Storage.UseAllWithPartitions == nil || !*nodeSpecCopy.Storage.UseAllWithPartitions) &&
				nodeSpecCopy.Storage.Devices == nil &&
				toUpdate.Spec.Storage.UseAll != nil {
				nodeSpecCopy.Storage.UseAll = boolPtr(*toUpdate.Spec.Storage.UseAll)
			}
			if nodeSpecCopy.Storage.ForceUseDisks == nil && toUpdate.Spec.Storage.ForceUseDisks != nil {
				nodeSpecCopy.Storage.ForceUseDisks = boolPtr(*toUpdate.Spec.Storage.ForceUseDisks)
			}
			if nodeSpecCopy.Storage.JournalDevice == nil && toUpdate.Spec.Storage.JournalDevice != nil {
				nodeSpecCopy.Storage.JournalDevice = stringPtr(*toUpdate.Spec.Storage.JournalDevice)
			}
			if nodeSpecCopy.Storage.SystemMdDevice == nil && toUpdate.Spec.Storage.SystemMdDevice != nil {
				nodeSpecCopy.Storage.SystemMdDevice = stringPtr(*toUpdate.Spec.Storage.SystemMdDevice)
			}
		}
		updatedNodeSpecs = append(updatedNodeSpecs, *nodeSpecCopy)
	}
	toUpdate.Spec.Nodes = updatedNodeSpecs
}

func setComponentDefaults(
	toUpdate *corev1alpha1.StorageCluster,
	components manifest.Release,
	isPortworxEnabled bool,
) {
	// Use the lighthouse image from release manifest if the current image is not locked,
	// else keep using the existing image. If the current image is empty then use the
	// default image from manifest else a hardcoded one if absent in manifest.
	if toUpdate.Spec.UserInterface != nil &&
		toUpdate.Spec.UserInterface.Enabled {
		toUpdate.Spec.UserInterface.Image = strings.TrimSpace(toUpdate.Spec.UserInterface.Image)
		if len(components.Lighthouse) > 0 {
			if !toUpdate.Spec.UserInterface.LockImage ||
				len(toUpdate.Spec.UserInterface.Image) == 0 {
				toUpdate.Spec.UserInterface.Image = components.Lighthouse
			}
		} else if len(toUpdate.Spec.UserInterface.Image) == 0 {
			toUpdate.Spec.UserInterface.Image = defaultLighthouseImage
		}
	}

	// Use the autopilot image from release manifest if the current image is not locked,
	// else keep using the existing image. If the current image is empty then use the
	// default image from manifest else a hardcoded one if absent in manifest.
	if toUpdate.Spec.Autopilot != nil &&
		toUpdate.Spec.Autopilot.Enabled {
		toUpdate.Spec.Autopilot.Image = strings.TrimSpace(toUpdate.Spec.Autopilot.Image)
		if len(components.Autopilot) > 0 {
			if !toUpdate.Spec.Autopilot.LockImage ||
				len(toUpdate.Spec.Autopilot.Image) == 0 {
				toUpdate.Spec.Autopilot.Image = components.Autopilot
			}
		} else if len(toUpdate.Spec.Autopilot.Image) == 0 {
			toUpdate.Spec.Autopilot.Image = defaultAutopilotImage
		}

		if len(toUpdate.Spec.Autopilot.Providers) == 0 {
			toUpdate.Spec.Autopilot.Providers = []corev1alpha1.DataProviderSpec{
				{
					Name: "default",
					Type: "prometheus",
					Params: map[string]string{
						"url": "http://prometheus:9090",
					},
				},
			}
		}
	}

	// Enable stork by default, only if portworx is enabled
	if toUpdate.Spec.Stork == nil && isPortworxEnabled {
		toUpdate.Spec.Stork = &corev1alpha1.StorkSpec{
			Enabled: true,
		}
	}
	// Use the stork image from release manifest if the current image is not locked,
	// else keep using the existing image. If the current image is empty then use the
	// default image from manifest else a hardcoded one if absent in manifest.
	if toUpdate.Spec.Stork != nil &&
		toUpdate.Spec.Stork.Enabled {
		toUpdate.Spec.Stork.Image = strings.TrimSpace(toUpdate.Spec.Stork.Image)
		if len(components.Stork) > 0 {
			if !toUpdate.Spec.Stork.LockImage ||
				len(toUpdate.Spec.Stork.Image) == 0 {
				toUpdate.Spec.Stork.Image = components.Stork
			}
		} else if len(toUpdate.Spec.Stork.Image) == 0 {
			toUpdate.Spec.Stork.Image = defaultStorkImage
		}
	}
}

func init() {
	if err := storage.Register(pxutil.DriverName, &portworx{}); err != nil {
		logrus.Panicf("Error registering portworx storage driver: %v", err)
	}
}
