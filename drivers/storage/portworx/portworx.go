package portworx

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/openstorage/pkg/grpcserver"
	storage "github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// driverName is the name of the portworx storage driver implementation
	driverName                        = "portworx"
	storkDriverName                   = "pxd"
	labelKeyName                      = "name"
	defaultPortworxImage              = "portworx/oci-monitor:2.1.3"
	defaultLighthouseImage            = "portworx/px-lighthouse:2.0.4"
	defaultStartPort                  = 9001
	defaultSDKPort                    = 9020
	defaultSecretsProvider            = "k8s"
	defaultNodeWiperImage             = "portworx/px-node-wiper:2.1.2-rc1"
	envKeyNodeWiperImage              = "PX_NODE_WIPER_IMAGE"
	storageClusterDeleteMsg           = "Portworx service NOT removed. Portworx drives and data NOT wiped."
	storageClusterUninstallMsg        = "Portworx service removed. Portworx drives and data NOT wiped."
	storageClusterUninstallAndWipeMsg = "Portworx service removed. Portworx drives and data wiped."
	failedSyncReason                  = "FailedSync"
	labelPortworxVersion              = "PX Version"
)

type portworx struct {
	k8sClient                         client.Client
	recorder                          record.EventRecorder
	pxAPIDaemonSetCreated             bool
	volumePlacementStrategyCRDCreated bool
	pvcControllerDeploymentCreated    bool
	lhDeploymentCreated               bool
	csiStatefulSetCreated             bool
	sdkConn                           *grpc.ClientConn
	zoneToInstancesMap                map[string]int
	cloudProvider                     string
}

func (p *portworx) String() string {
	return driverName
}

func (p *portworx) Init(k8sClient client.Client, recorder record.EventRecorder) error {
	if k8sClient == nil {
		return fmt.Errorf("kubernetes client cannot be nil")
	}
	p.k8sClient = k8sClient
	if recorder == nil {
		return fmt.Errorf("event recorder cannot be nil")
	}
	p.recorder = recorder
	return nil
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
			Name:  envKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
	}
}

func (p *portworx) GetSelectorLabels() map[string]string {
	return map[string]string{
		labelKeyName: driverName,
	}
}

func (p *portworx) SetDefaultsOnStorageCluster(toUpdate *corev1alpha1.StorageCluster) {
	if len(strings.TrimSpace(toUpdate.Spec.Image)) == 0 {
		toUpdate.Spec.Image = defaultPortworxImage
	}
	if toUpdate.Spec.UserInterface != nil &&
		toUpdate.Spec.UserInterface.Enabled &&
		len(strings.TrimSpace(toUpdate.Spec.UserInterface.Image)) == 0 {
		toUpdate.Spec.UserInterface.Image = defaultLighthouseImage
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
	startPort := uint32(defaultStartPort)
	if toUpdate.Spec.StartPort == nil {
		toUpdate.Spec.StartPort = &startPort
	}
	if toUpdate.Spec.Placement == nil || toUpdate.Spec.Placement.NodeAffinity == nil {
		t, err := newTemplate(toUpdate)
		if err != nil {
			return
		}
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

func (p *portworx) PreInstall(cluster *corev1alpha1.StorageCluster) error {
	return p.installComponents(cluster)
}

func (p *portworx) DeleteStorage(
	cluster *corev1alpha1.StorageCluster,
) (*corev1alpha1.ClusterCondition, error) {
	p.unsetInstallParams()

	if cluster.Spec.DeleteStrategy == nil {
		// No Delete strategy provided. Do not wipe portworx
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
		nodeWiperImage := getImageFromEnv(envKeyNodeWiperImage, cluster.Spec.Env)
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

func (p *portworx) UpdateStorageClusterStatus(
	cluster *corev1alpha1.StorageCluster,
) error {
	if cluster.Status.Phase == "" {
		cluster.Status.ClusterName = cluster.Name
		cluster.Status.Phase = string(corev1alpha1.ClusterInit)
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

	return p.updateNodeStatuses(clientConn, cluster)
}

func (p *portworx) updateNodeStatuses(
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

	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	for _, node := range nodeEnumerateResponse.Nodes {
		if node.SchedulerNodeName == "" {
			k8sNode, err := k8s.Instance().SearchNodeByAddresses(
				[]string{node.DataIp, node.MgmtIp, node.Hostname},
			)
			if err != nil {
				msg := fmt.Sprintf("Unable to find kubernetes node name for nodeID %v: %v", node.Id, err)
				p.warningEvent(cluster, failedSyncReason, msg)
				continue
			}
			node.SchedulerNodeName = k8sNode.Name
		}

		currentNodes[node.SchedulerNodeName] = true

		phase := mapNodeStatus(node.Status)
		nodeStatus := &corev1alpha1.StorageNodeStatus{
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
			nodeStatus.Spec = corev1alpha1.StorageNodeStatusSpec{
				Version: version,
			}
		} else {
			partitions := strings.Split(cluster.Spec.Image, ":")
			if len(partitions) > 1 {
				nodeStatus.Spec = corev1alpha1.StorageNodeStatusSpec{
					Version: partitions[len(partitions)-1],
				}
			}
		}

		err = k8sutil.CreateOrUpdateStorageNodeStatus(p.k8sClient, nodeStatus, ownerRef)
		if err != nil {
			msg := fmt.Sprintf("Failed to update status for nodeID %v: %v", node.Id, err)
			p.warningEvent(cluster, failedSyncReason, msg)
		}
	}

	nodeStatusList := &corev1alpha1.StorageNodeStatusList{}
	if err = p.k8sClient.List(context.TODO(), &client.ListOptions{}, nodeStatusList); err != nil {
		return fmt.Errorf("failed to get a list of StorageNodeStatus: %v", err)
	}

	for _, nodeStatus := range nodeStatusList.Items {
		if _, exists := currentNodes[nodeStatus.Name]; !exists {
			logrus.Debugf("Deleting orphan StorageNodeStatus %v/%v",
				nodeStatus.Namespace, nodeStatus.Name)
			err = p.k8sClient.Delete(context.TODO(), nodeStatus.DeepCopy())
			if err != nil && !errors.IsNotFound(err) {
				msg := fmt.Sprintf("Failed to delete StorageNodeStatus %v/%v: %v",
					nodeStatus.Namespace, nodeStatus.Name, err)
				p.warningEvent(cluster, failedSyncReason, msg)
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
			Name:      pxServiceName,
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
		if pxServicePort.Name == pxSDKPortName && pxServicePort.Port != 0 {
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

func init() {
	if err := storage.Register(driverName, &portworx{}); err != nil {
		logrus.Panicf("Error registering portworx storage driver: %v", err)
	}
}
