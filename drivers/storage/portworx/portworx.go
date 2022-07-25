package portworx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	version "github.com/hashicorp/go-version"
	storageapi "github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/operator/drivers/storage"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	api "k8s.io/kubernetes/pkg/apis/core"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	storkDriverName                   = "pxd"
	defaultPortworxImage              = "portworx/oci-monitor"
	defaultSecretsProvider            = "k8s"
	defaultTokenLifetime              = "24h"
	defaultSelfSignedIssuer           = "operator.portworx.io"
	envKeyNodeWiperImage              = "PX_NODE_WIPER_IMAGE"
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
	zoneToInstancesMap map[string]uint64
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

	manifest.Instance().Init(k8sClient, recorder, k8sVersion)
	p.initializeComponents()
	return nil
}

func (p *portworx) Validate() error {
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

func (p *portworx) GetStorkEnvMap(cluster *corev1.StorageCluster) map[string]*v1.EnvVar {
	envMap := map[string]*v1.EnvVar{
		pxutil.EnvKeyPortworxNamespace: {
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: cluster.Namespace,
		},
		pxutil.EnvKeyPortworxServiceName: {
			Name:  pxutil.EnvKeyPortworxServiceName,
			Value: component.PxAPIServiceName,
		},
	}

	if pxutil.SecurityEnabled(cluster) {
		envMap[pxutil.EnvKeyPXSharedSecret] = &v1.EnvVar{
			Name: pxutil.EnvKeyPXSharedSecret,
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: pxutil.SecurityPXSystemSecretsSecretName,
					},
					Key: pxutil.SecurityAppsSecretKey,
				},
			},
		}

		pxVersion := pxutil.GetPortworxVersion(cluster)
		pxAppsIssuerVersion, err := version.NewVersion("2.6.0")
		if err != nil {
			logrus.Errorf("failed to create PX version variable 2.6.0: %s", err.Error())
		}

		// apps issuer was added in PX version 2.6.0
		issuer := pxutil.SecurityPortworxStorkIssuer
		if pxVersion.GreaterThanOrEqual(pxAppsIssuerVersion) {
			issuer = pxutil.SecurityPortworxAppsIssuer
		}
		envMap[pxutil.EnvKeyStorkPXJwtIssuer] = &v1.EnvVar{
			Name:  pxutil.EnvKeyStorkPXJwtIssuer,
			Value: issuer,
		}
	}

	return envMap
}

func (p *portworx) GetSelectorLabels() map[string]string {
	return pxutil.SelectorLabels()
}

func (p *portworx) SetDefaultsOnStorageCluster(toUpdate *corev1.StorageCluster) {
	if toUpdate.Status.DesiredImages == nil {
		toUpdate.Status.DesiredImages = &corev1.ComponentImages{}
	}

	pxEnabled := pxutil.IsPortworxEnabled(toUpdate)
	if pxEnabled {
		if toUpdate.Spec.Stork == nil {
			toUpdate.Spec.Stork = &corev1.StorkSpec{
				Enabled: true,
			}
		}

		SetPortworxDefaults(toUpdate, p.k8sVersion)
	}

	removeDeprecatedFields(toUpdate)

	toUpdate.Spec.Image = strings.TrimSpace(toUpdate.Spec.Image)
	toUpdate.Spec.Version = pxutil.GetImageTag(toUpdate.Spec.Image)
	pxVersionChanged := pxEnabled &&
		(toUpdate.Spec.Version == "" || toUpdate.Spec.Version != toUpdate.Status.Version)

	if pxVersionChanged || autoUpdateComponents(toUpdate) || p.hasComponentChanged(toUpdate) {
		// Force latest versions only if the component update strategy is Once
		force := pxVersionChanged || (toUpdate.Spec.AutoUpdateComponents != nil &&
			*toUpdate.Spec.AutoUpdateComponents == corev1.OnceAutoUpdate)
		release := manifest.Instance().GetVersions(toUpdate, force)

		logrus.Infof("JLIAO: versions in set px default: %s", release.Components)
		logrus.Infof("JLIAO: has component changed %v", p.hasComponentChanged(toUpdate))
		logrus.Infof("JLIAO: telemetry , auto update: %v, has changed: %v", autoUpdateTelemetry(toUpdate), hasTelemetryChanged(toUpdate))
		logrus.Infof("JLIAO: telemetry proxy, auto update: %v, has changed: %v", autoUpdateTelemetryProxy(toUpdate), hasTelemetryProxyChanged(toUpdate))
		logrus.Infof("JLIAO: metrics, auto update: %v, has changed: %v", autoUpdateMetricsCollector(toUpdate), hasMetricsCollectorChanged(toUpdate))
		logrus.Infof("JLIAO: log uploader, auto update: %v, has changed: %v", autoUpdateLogUploader(toUpdate), hasLogUploaderChanged(toUpdate))

		if toUpdate.Spec.Version == "" && pxEnabled {
			if toUpdate.Spec.Image == "" {
				toUpdate.Spec.Image = defaultPortworxImage
			}
			toUpdate.Spec.Image = toUpdate.Spec.Image + ":" + release.PortworxVersion
			toUpdate.Spec.Version = release.PortworxVersion
		}

		toUpdate.Status.Version = toUpdate.Spec.Version

		if autoUpdateStork(toUpdate) &&
			(toUpdate.Status.DesiredImages.Stork == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.Stork = release.Components.Stork
		}

		if autoUpdateAutopilot(toUpdate) &&
			(toUpdate.Status.DesiredImages.Autopilot == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.Autopilot = release.Components.Autopilot
		}

		if autoUpdateLighthouse(toUpdate) &&
			(toUpdate.Status.DesiredImages.UserInterface == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.UserInterface = release.Components.Lighthouse
		}

		if autoUpdateTelemetry(toUpdate) &&
			(toUpdate.Status.DesiredImages.Telemetry == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			logrus.Infof("JLIAO: set telemetry desired image: %s", release.Components.Telemetry)
			toUpdate.Status.DesiredImages.Telemetry = release.Components.Telemetry
		}

		// Set desired telemetry proxy to determine whether to reconcile ccm java or go
		// if desired telemetry proxy is empty, then run ccm java, otherwise ccm go
		if autoUpdateTelemetryProxy(toUpdate) &&
			(toUpdate.Status.DesiredImages.TelemetryProxy == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			// Check if old ccm image is specified, if yes ccm upgrade should be blocked
			// TODO: do we have a better way instead of checking the image?
			blockTelemetryUpgrade := toUpdate.Spec.Monitoring.Telemetry.Image != "" &&
				!strings.Contains(toUpdate.Spec.Monitoring.Telemetry.Image, "ccm-go")
			if blockTelemetryUpgrade {
				logrus.Infof("ccm upgrade is blocked as the telemetry image is specified, will continue running old telemetry ")
			} else {
				logrus.Infof("JLIAO: set telemetry proxy desired image: %s", release.Components.Telemetry)
				toUpdate.Status.DesiredImages.TelemetryProxy = release.Components.TelemetryProxy
			}
		}
		logrus.Infof("JLIAO: px version: %s", pxutil.GetPortworxVersion(toUpdate))
		logrus.Infof("JLIAO: ccm-go supported: %v", pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(toUpdate)))

		if autoUpdateMetricsCollector(toUpdate) &&
			(toUpdate.Status.DesiredImages.MetricsCollector == "" ||
				toUpdate.Status.DesiredImages.MetricsCollectorProxy == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.MetricsCollector = release.Components.MetricsCollector
			toUpdate.Status.DesiredImages.MetricsCollectorProxy = release.Components.MetricsCollectorProxy
		}

		if autoUpdateLogUploader(toUpdate) &&
			(toUpdate.Status.DesiredImages.LogUploader == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			logrus.Infof("JLIAO: set loguploader desired image: %s", release.Components.LogUploader)
			toUpdate.Status.DesiredImages.LogUploader = release.Components.LogUploader
		}

		if pxutil.IsCSIEnabled(toUpdate) {
			if toUpdate.Status.DesiredImages.CSIProvisioner == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate) {
				toUpdate.Status.DesiredImages.CSIProvisioner = release.Components.CSIProvisioner
				toUpdate.Status.DesiredImages.CSINodeDriverRegistrar = release.Components.CSINodeDriverRegistrar
				toUpdate.Status.DesiredImages.CSIDriverRegistrar = release.Components.CSIDriverRegistrar
				toUpdate.Status.DesiredImages.CSIAttacher = release.Components.CSIAttacher
				toUpdate.Status.DesiredImages.CSIResizer = release.Components.CSIResizer
				toUpdate.Status.DesiredImages.CSISnapshotter = release.Components.CSISnapshotter
				toUpdate.Status.DesiredImages.CSIHealthMonitorController = release.Components.CSIHealthMonitorController
			}
			if autoUpdateCSISnapshotController(toUpdate) &&
				(toUpdate.Status.DesiredImages.CSISnapshotController == "" ||
					pxVersionChanged ||
					autoUpdateComponents(toUpdate)) {
				toUpdate.Status.DesiredImages.CSISnapshotController = release.Components.CSISnapshotController
			}
		}

		if toUpdate.Spec.Monitoring != nil && toUpdate.Spec.Monitoring.Prometheus != nil {
			prometheusVersionChanged := p.hasPrometheusVersionChanged(toUpdate)
			if toUpdate.Spec.Monitoring.Prometheus.Enabled &&
				(toUpdate.Status.DesiredImages.PrometheusOperator == "" || pxVersionChanged || prometheusVersionChanged) {
				toUpdate.Status.DesiredImages.Prometheus = release.Components.Prometheus
				toUpdate.Status.DesiredImages.PrometheusOperator = release.Components.PrometheusOperator
				toUpdate.Status.DesiredImages.PrometheusConfigMapReload = release.Components.PrometheusConfigMapReload
				toUpdate.Status.DesiredImages.PrometheusConfigReloader = release.Components.PrometheusConfigReloader
			}
			if toUpdate.Spec.Monitoring.Prometheus.AlertManager != nil &&
				toUpdate.Spec.Monitoring.Prometheus.AlertManager.Enabled &&
				(toUpdate.Status.DesiredImages.AlertManager == "" || pxVersionChanged || prometheusVersionChanged) {
				toUpdate.Status.DesiredImages.AlertManager = release.Components.AlertManager
			}
		}

		// Reset the component update strategy if it is 'Once', so that we don't
		// upgrade components again during next reconcile loop
		if toUpdate.Spec.AutoUpdateComponents != nil &&
			*toUpdate.Spec.AutoUpdateComponents == corev1.OnceAutoUpdate {
			toUpdate.Spec.AutoUpdateComponents = nil
		}
	}

	if !autoUpdateStork(toUpdate) {
		toUpdate.Status.DesiredImages.Stork = ""
	}

	if !autoUpdateAutopilot(toUpdate) {
		toUpdate.Status.DesiredImages.Autopilot = ""
	}

	if !autoUpdateLighthouse(toUpdate) {
		toUpdate.Status.DesiredImages.UserInterface = ""
	}

	if !autoUpdateTelemetry(toUpdate) {
		toUpdate.Status.DesiredImages.Telemetry = ""
	}

	if !autoUpdateTelemetryProxy(toUpdate) {
		toUpdate.Status.DesiredImages.TelemetryProxy = ""
	}

	if !autoUpdateMetricsCollector(toUpdate) {
		toUpdate.Status.DesiredImages.MetricsCollector = ""
		toUpdate.Status.DesiredImages.MetricsCollectorProxy = ""
	}

	if !autoUpdateLogUploader(toUpdate) {
		toUpdate.Status.DesiredImages.LogUploader = ""
	}

	if !pxutil.IsCSIEnabled(toUpdate) {
		toUpdate.Status.DesiredImages.CSIProvisioner = ""
		toUpdate.Status.DesiredImages.CSINodeDriverRegistrar = ""
		toUpdate.Status.DesiredImages.CSIDriverRegistrar = ""
		toUpdate.Status.DesiredImages.CSIAttacher = ""
		toUpdate.Status.DesiredImages.CSIResizer = ""
		toUpdate.Status.DesiredImages.CSISnapshotter = ""
		toUpdate.Status.DesiredImages.CSISnapshotController = ""
		toUpdate.Status.DesiredImages.CSIHealthMonitorController = ""
	} else if !autoUpdateCSISnapshotController(toUpdate) {
		toUpdate.Status.DesiredImages.CSISnapshotController = ""
	}

	if toUpdate.Spec.Monitoring == nil ||
		toUpdate.Spec.Monitoring.Prometheus == nil ||
		!toUpdate.Spec.Monitoring.Prometheus.Enabled {
		toUpdate.Status.DesiredImages.Prometheus = ""
		toUpdate.Status.DesiredImages.PrometheusOperator = ""
		toUpdate.Status.DesiredImages.PrometheusConfigMapReload = ""
		toUpdate.Status.DesiredImages.PrometheusConfigReloader = ""
	}

	if toUpdate.Spec.Monitoring == nil ||
		toUpdate.Spec.Monitoring.Prometheus == nil ||
		toUpdate.Spec.Monitoring.Prometheus.AlertManager == nil ||
		!toUpdate.Spec.Monitoring.Prometheus.AlertManager.Enabled {
		toUpdate.Status.DesiredImages.AlertManager = ""
	}

	setDefaultAutopilotProviders(toUpdate)
}

func (p *portworx) PreInstall(cluster *corev1.StorageCluster) error {
	if err := p.validateCleanup(cluster); err != nil {
		return err
	}

	if err := p.validateEssentials(); err != nil {
		return err
	}

	for _, comp := range component.GetAll() {
		if comp.IsPausedForMigration(cluster) {
			continue
		} else if comp.IsEnabled(cluster) {
			err := comp.Reconcile(cluster)
			if ce, ok := err.(*component.Error); ok &&
				ce.Code() == component.ErrCritical {
				return err
			} else if err != nil {
				msg := fmt.Sprintf("Failed to setup %s. %v", comp.Name(), err)
				p.warningEvent(cluster, util.FailedComponentReason, msg)
			}
		} else {
			if err := comp.Delete(cluster); err != nil {
				msg := fmt.Sprintf("Failed to cleanup %v. %v", comp.Name(), err)
				p.warningEvent(cluster, util.FailedComponentReason, msg)
			}
		}
	}
	return nil
}

// If px was uninstalled and reinstalled, validate if cleanup was done properly.
func (p portworx) validateCleanup(cluster *corev1.StorageCluster) error {
	cmList := &v1.ConfigMapList{}
	// list across all namespaces.
	if err := p.k8sClient.List(context.TODO(), cmList, &client.ListOptions{}); err != nil {
		return err
	}

	etcdConfigMapName := pxutil.GetInternalEtcdConfigMapName(cluster)
	cloudDriveConfigMapName := pxutil.GetCloudDriveConfigMapName(cluster)
	for _, cm := range cmList.Items {
		if (strings.HasPrefix(cm.Name, pxutil.InternalEtcdConfigMapPrefix) && cm.Name != etcdConfigMapName) ||
			(strings.HasPrefix(cm.Name, pxutil.CloudDriveConfigMapPrefix) && cm.Name != cloudDriveConfigMapName) {
			return fmt.Errorf("unexpected configmap %s/%s found, due to a previous Portworx cluster installation. Please ensure that previous Portworx cluster is completely uninstalled with UninstallAndWipe delete strategy before re-installing with a new name", cm.Namespace, cm.Name)
		}
	}
	return nil
}

func (p *portworx) validateEssentials() error {
	if pxutil.EssentialsEnabled() {
		resource := types.NamespacedName{
			Name:      pxutil.EssentialsSecretName,
			Namespace: api.NamespaceSystem,
		}
		secret := &v1.Secret{}
		err := p.k8sClient.Get(context.TODO(), resource, secret)
		if errors.IsNotFound(err) {
			return fmt.Errorf("secret %s/%s should be present to deploy a "+
				"Portworx Essentials cluster", api.NamespaceSystem, pxutil.EssentialsSecretName)
		} else if err != nil {
			return fmt.Errorf("unable to get secret %s/%s. %v",
				api.NamespaceSystem, pxutil.EssentialsSecretName, err)
		}
		if len(secret.Data[pxutil.EssentialsUserIDKey]) == 0 {
			return fmt.Errorf("secret %s/%s does not have Essentials Entitlement ID (%s)",
				api.NamespaceSystem, pxutil.EssentialsSecretName, pxutil.EssentialsUserIDKey)
		}
		if len(secret.Data[pxutil.EssentialsOSBEndpointKey]) == 0 {
			return fmt.Errorf("secret %s/%s does not have Portworx OSB endpoint (%s)",
				api.NamespaceSystem, pxutil.EssentialsSecretName, pxutil.EssentialsOSBEndpointKey)
		}
	}

	return nil
}

func (p *portworx) IsPodUpdated(
	cluster *corev1.StorageCluster,
	pod *v1.Pod,
) bool {
	portworxArgs := make([]string, 0)
	for _, c := range pod.Spec.Containers {
		if c.Name == pxContainerName {
			portworxArgs = append(portworxArgs, c.Args...)
			break
		}
	}
	if len(portworxArgs) == 0 {
		return false
	}

	var miscArgsStr string
	parts, err := pxutil.MiscArgs(cluster)
	if err != nil {
		logrus.Warnf("error parsing misc args: %v", err)
		return true
	}
	miscArgsStr = strings.Join(parts, " ")
	currArgsStr := strings.Join(portworxArgs, " ")

	if miscArgsChanged(currArgsStr, miscArgsStr) {
		return false
	} else if !pxutil.EssentialsEnabled() {
		if essentialsArgPresent(currArgsStr) &&
			!essentialsArgPresent(miscArgsStr) {
			return false
		}
	} else {
		if !essentialsArgPresent(currArgsStr) {
			return false
		}
	}
	return true
}

func (p *portworx) GetStorageNodes(
	cluster *corev1.StorageCluster,
) ([]*storageapi.StorageNode, error) {
	var err error
	p.sdkConn, err = pxutil.GetPortworxConn(p.sdkConn, p.k8sClient, cluster.Namespace)
	if err != nil {
		if (cluster.Status.Phase == "" || cluster.Status.Phase == string(corev1.ClusterInit)) &&
			strings.HasPrefix(err.Error(), pxutil.ErrMsgGrpcConnection) {
			// Don't return grpc connection error during initialization,
			// as SDK server won't be up anyway
			logrus.Warn(err)
			return []*storageapi.StorageNode{}, nil
		}
		return nil, err
	}

	nodeClient := storageapi.NewOpenStorageNodeClient(p.sdkConn)
	ctx, err := pxutil.SetupContextWithToken(context.Background(), cluster, p.k8sClient)
	if err != nil {
		return nil, err
	}
	nodeEnumerateResponse, err := nodeClient.EnumerateWithFilters(
		ctx,
		&storageapi.SdkNodeEnumerateWithFiltersRequest{},
	)
	if err != nil {
		return nil, err
	}

	return nodeEnumerateResponse.Nodes, nil
}

func (p *portworx) DeleteStorage(
	cluster *corev1.StorageCluster,
) (*corev1.ClusterCondition, error) {
	p.deleteComponents(cluster)

	// Close connection to Portworx GRPC server
	if p.sdkConn != nil {
		if err := p.sdkConn.Close(); err != nil {
			logrus.Warnf("Failed to close sdk connection: %v", err)
		}
		p.sdkConn = nil
	}

	if cluster.Spec.DeleteStrategy == nil || !pxutil.IsPortworxEnabled(cluster) {
		// No Delete strategy provided or Portworx not installed through the operator,
		// then do not wipe Portworx
		status := &corev1.ClusterCondition{
			Type:   corev1.ClusterConditionTypeDelete,
			Status: corev1.ClusterOperationCompleted,
			Reason: storageClusterDeleteMsg,
		}
		return status, nil
	}

	// Portworx needs to be removed if DeleteStrategy is specified
	removeData := false
	completeMsg := storageClusterUninstallMsg
	if cluster.Spec.DeleteStrategy.Type == corev1.UninstallAndWipeStorageClusterStrategyType {
		removeData = true
		completeMsg = storageClusterUninstallAndWipeMsg
	}

	deleteCompleted := string(corev1.ClusterConditionTypeDelete) +
		string(corev1.ClusterOperationCompleted)
	u := NewUninstaller(cluster, p.k8sClient)
	completed, inProgress, total, err := u.GetNodeWiperStatus()
	if err != nil && errors.IsNotFound(err) {
		if cluster.Status.Phase == deleteCompleted {
			return &corev1.ClusterCondition{
				Type:   corev1.ClusterConditionTypeDelete,
				Status: corev1.ClusterOperationCompleted,
				Reason: completeMsg,
			}, nil
		}
		if err := u.RunNodeWiper(removeData, p.recorder); err != nil {
			return &corev1.ClusterCondition{
				Type:   corev1.ClusterConditionTypeDelete,
				Status: corev1.ClusterOperationFailed,
				Reason: "Failed to run node wiper: " + err.Error(),
			}, nil
		}
		return &corev1.ClusterCondition{
			Type:   corev1.ClusterConditionTypeDelete,
			Status: corev1.ClusterOperationInProgress,
			Reason: "Started node wiper daemonset",
		}, nil
	} else if err != nil {
		// We could not get the node wiper status and it does exist
		return nil, err
	}

	if completed != 0 && total != 0 && completed == total {
		if err := u.DeleteNodeWiper(); err != nil {
			logrus.Errorf("Failed to cleanup node wiper. %v", err)
		}
		// all the nodes are wiped
		if removeData {
			logrus.Debugf("Deleting portworx metadata")
			if err := u.WipeMetadata(); err != nil {
				logrus.Errorf("Failed to delete portworx metadata: %v", err)
				return &corev1.ClusterCondition{
					Type:   corev1.ClusterConditionTypeDelete,
					Status: corev1.ClusterOperationFailed,
					Reason: "Failed to wipe metadata: " + err.Error(),
				}, nil
			}
		}
		return &corev1.ClusterCondition{
			Type:   corev1.ClusterConditionTypeDelete,
			Status: corev1.ClusterOperationCompleted,
			Reason: completeMsg,
		}, nil
	}

	return &corev1.ClusterCondition{
		Type:   corev1.ClusterConditionTypeDelete,
		Status: corev1.ClusterOperationInProgress,
		Reason: fmt.Sprintf("Wipe operation still in progress: Completed [%v] In Progress [%v] Total [%v]", completed, inProgress, total),
	}, nil
}

func (p *portworx) deleteComponents(cluster *corev1.StorageCluster) {
	for _, comp := range component.GetAll() {
		if err := comp.Delete(cluster); err != nil {
			msg := fmt.Sprintf("Failed to cleanup %v. %v", comp.Name(), err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	}
}

func (p *portworx) normalEvent(
	cluster *corev1.StorageCluster,
	reason, message string,
) {
	logrus.Info(message)
	p.recorder.Event(cluster, v1.EventTypeNormal, reason, message)
}

func (p *portworx) warningEvent(
	cluster *corev1.StorageCluster,
	reason, message string,
) {
	logrus.Warn(message)
	p.recorder.Event(cluster, v1.EventTypeWarning, reason, message)
}

func (p *portworx) storageNodeToCloudSpec(storageNodes []*corev1.StorageNode, cluster *corev1.StorageCluster) *cloudstorage.Config {
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

// SetPortworxDefaults populates default storage cluster spec values
func SetPortworxDefaults(toUpdate *corev1.StorageCluster, k8sVersion *version.Version) {
	if k8sVersion == nil {
		k8sVersion = pxutil.MinimumSupportedK8sVersion
	}
	t, err := newTemplate(toUpdate, "")
	if err != nil {
		return
	}

	if toUpdate.Spec.Kvdb == nil {
		toUpdate.Spec.Kvdb = &corev1.KvdbSpec{}
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
		// Only initialize storage spec when there's no node level cloud storage spec specified
		initializeStorageSpec := true
		for _, nodeSpec := range toUpdate.Spec.Nodes {
			if nodeSpec.CloudStorage != nil {
				initializeStorageSpec = false
				break
			}
		}
		if initializeStorageSpec {
			toUpdate.Spec.Storage = &corev1.StorageSpec{}
		}
	}
	if toUpdate.Spec.Storage != nil {
		if toUpdate.Spec.Storage.Devices == nil &&
			(toUpdate.Spec.Storage.UseAllWithPartitions == nil || !*toUpdate.Spec.Storage.UseAllWithPartitions) &&
			toUpdate.Spec.Storage.UseAll == nil {
			toUpdate.Spec.Storage.UseAll = boolPtr(true)
		}
	}

	if pxutil.IsTelemetryEnabled(toUpdate.Spec) && t.pxVersion.LessThan(pxutil.MinimumPxVersionCCM) {
		toUpdate.Spec.Monitoring.Telemetry.Enabled = false // telemetry not supported for < 2.8
		toUpdate.Spec.Monitoring.Telemetry.Image = ""
	}

	setNodeSpecDefaults(toUpdate)

	if toUpdate.Spec.Placement == nil {
		toUpdate.Spec.Placement = &corev1.PlacementSpec{}
	}
	if toUpdate.Spec.Placement.NodeAffinity == nil {
		toUpdate.Spec.Placement.NodeAffinity = &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: t.getSelectorTerms(k8sVersion),
			},
		}
	}
	if t.runOnMaster {
		masterTolerationFound := false
		for _, t := range toUpdate.Spec.Placement.Tolerations {
			if (t.Key == k8sutil.NodeRoleLabelMaster ||
				t.Key == k8sutil.NodeRoleLabelControlPlane) &&
				t.Effect == v1.TaintEffectNoSchedule {
				masterTolerationFound = true
				break
			}
		}
		if !masterTolerationFound {
			toUpdate.Spec.Placement.Tolerations = append(
				toUpdate.Spec.Placement.Tolerations,
				v1.Toleration{
					Key:    k8sutil.NodeRoleLabelMaster,
					Effect: v1.TaintEffectNoSchedule,
				},
			)
			if k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_24) {
				toUpdate.Spec.Placement.Tolerations = append(
					toUpdate.Spec.Placement.Tolerations,
					v1.Toleration{
						Key:    k8sutil.NodeRoleLabelControlPlane,
						Effect: v1.TaintEffectNoSchedule,
					},
				)
			}
		}
	}

	// Check if feature gate is set. If it is, honor the flag here and remove it.
	csiFeatureFlag, featureGateSet := toUpdate.Spec.FeatureGates[string(pxutil.FeatureCSI)]
	if featureGateSet {
		csiEnabled, err := strconv.ParseBool(csiFeatureFlag)
		if err != nil {
			csiEnabled = true
		}

		// Feature flag set, but CSI spec is not defined. In this case,
		// we want to use the feature flag value in the CSI spec.
		if toUpdate.Spec.CSI == nil {
			toUpdate.Spec.CSI = &corev1.CSISpec{
				Enabled: csiEnabled,
			}
		} else {
			// Set CSI spec as enabled if it's already enabled OR the flag is true.
			toUpdate.Spec.CSI.Enabled = toUpdate.Spec.CSI.Enabled || csiEnabled
		}

		// Always remove the feature flag. Clear the feature gates map if none are set.
		delete(toUpdate.Spec.FeatureGates, string(pxutil.FeatureCSI))
		if len(toUpdate.Spec.FeatureGates) == 0 {
			toUpdate.Spec.FeatureGates = nil
		}
	}

	// Enable CSI if running in k3s environment or
	// if this is a new Portworx installation
	if t.isK3s || toUpdate.Spec.Version == "" {
		if toUpdate.Spec.CSI == nil {
			toUpdate.Spec.CSI = &corev1.CSISpec{
				Enabled: true,
			}
		}
	}

	// Enable CSI snapshot controller by default if it's not configured on k8s 1.17+
	if pxutil.IsCSIEnabled(toUpdate) && toUpdate.Spec.CSI.InstallSnapshotController == nil {
		if k8sVersion != nil && k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_17) {
			toUpdate.Spec.CSI.InstallSnapshotController = boolPtr(true)
		}
	}

	setSecuritySpecDefaults(toUpdate)
}

func setNodeSpecDefaults(toUpdate *corev1.StorageCluster) {
	if len(toUpdate.Spec.Nodes) == 0 {
		return
	}

	updatedNodeSpecs := make([]corev1.NodeSpec, 0)
	for _, nodeSpec := range toUpdate.Spec.Nodes {
		// Populate node specs with all storage values, to make it explicit what values
		// every node group is using.
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
			if nodeSpecCopy.Storage.CacheDevices == nil && toUpdate.Spec.Storage.CacheDevices != nil {
				cacheDevices := append(make([]string, 0), *toUpdate.Spec.Storage.CacheDevices...)
				nodeSpecCopy.Storage.CacheDevices = &cacheDevices
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
			if nodeSpecCopy.Storage.KvdbDevice == nil && toUpdate.Spec.Storage.KvdbDevice != nil {
				nodeSpecCopy.Storage.KvdbDevice = stringPtr(*toUpdate.Spec.Storage.KvdbDevice)
			}
		}

		if toUpdate.Spec.CloudStorage != nil {
			if nodeSpec.CloudStorage == nil {
				nodeSpecCopy.CloudStorage = &corev1.CloudStorageNodeSpec{
					CloudStorageCommon: *(toUpdate.Spec.CloudStorage.CloudStorageCommon.DeepCopy()),
				}
			} else {
				if nodeSpecCopy.CloudStorage.DeviceSpecs == nil &&
					toUpdate.Spec.CloudStorage.DeviceSpecs != nil {
					deviceSpecs := append(make([]string, 0), *toUpdate.Spec.CloudStorage.DeviceSpecs...)
					nodeSpecCopy.CloudStorage.DeviceSpecs = &deviceSpecs
				}
				if nodeSpecCopy.CloudStorage.JournalDeviceSpec == nil &&
					toUpdate.Spec.CloudStorage.JournalDeviceSpec != nil {
					nodeSpecCopy.CloudStorage.JournalDeviceSpec = stringPtr(*toUpdate.Spec.CloudStorage.JournalDeviceSpec)
				}
				if nodeSpecCopy.CloudStorage.SystemMdDeviceSpec == nil &&
					toUpdate.Spec.CloudStorage.SystemMdDeviceSpec != nil {
					nodeSpecCopy.CloudStorage.SystemMdDeviceSpec = stringPtr(*toUpdate.Spec.CloudStorage.SystemMdDeviceSpec)
				}
				if nodeSpecCopy.CloudStorage.KvdbDeviceSpec == nil &&
					toUpdate.Spec.CloudStorage.KvdbDeviceSpec != nil {
					nodeSpecCopy.CloudStorage.KvdbDeviceSpec = stringPtr(*toUpdate.Spec.CloudStorage.KvdbDeviceSpec)
				}
				if nodeSpecCopy.CloudStorage.MaxStorageNodesPerZonePerNodeGroup == nil &&
					toUpdate.Spec.CloudStorage.MaxStorageNodesPerZonePerNodeGroup != nil {
					maxStorageNodes := *toUpdate.Spec.CloudStorage.MaxStorageNodesPerZonePerNodeGroup
					nodeSpecCopy.CloudStorage.MaxStorageNodesPerZonePerNodeGroup = &maxStorageNodes
				}
			}
		}

		updatedNodeSpecs = append(updatedNodeSpecs, *nodeSpecCopy)
	}
	toUpdate.Spec.Nodes = updatedNodeSpecs
}

func setSecuritySpecDefaults(toUpdate *corev1.StorageCluster) {
	// all default values if one is not provided below.
	defaultAuthTemplate := &corev1.AuthSpec{
		GuestAccess: guestAccessTypePtr(corev1.GuestRoleEnabled),
		SelfSigned: &corev1.SelfSignedSpec{
			Issuer:        stringPtr(defaultSelfSignedIssuer),
			TokenLifetime: stringPtr(defaultTokenLifetime),
			SharedSecret:  stringPtr(pxutil.SecurityPXSharedSecretSecretName),
		},
	}

	if toUpdate.Spec.Security != nil {
		if toUpdate.Spec.Security.Enabled {
			if toUpdate.Spec.Security.Auth != nil && (*toUpdate.Spec.Security.Auth != corev1.AuthSpec{}) {
				if toUpdate.Spec.Security.Auth.GuestAccess == nil || (*toUpdate.Spec.Security.Auth.GuestAccess == "") {
					// if not provided, enabled by default.
					toUpdate.Spec.Security.Auth.GuestAccess = defaultAuthTemplate.GuestAccess
				}
				if toUpdate.Spec.Security.Auth.SelfSigned != nil && (*toUpdate.Spec.Security.Auth.SelfSigned != corev1.SelfSignedSpec{}) {
					selfSignedIssuerEnvVal := pxutil.GetClusterEnvVarValue(context.TODO(), toUpdate, pxutil.EnvKeyPortworxAuthJwtIssuer)
					if toUpdate.Spec.Security.Auth.SelfSigned.Issuer != nil && (*toUpdate.Spec.Security.Auth.SelfSigned.Issuer != "") {
						// leave as is, non-nil and non-empty
					} else {
						// use environment variable if passed, otherwise use default
						if selfSignedIssuerEnvVal == "" {
							toUpdate.Spec.Security.Auth.SelfSigned.Issuer = defaultAuthTemplate.SelfSigned.Issuer
						} else {
							toUpdate.Spec.Security.Auth.SelfSigned.Issuer = &selfSignedIssuerEnvVal
						}
					}
					if toUpdate.Spec.Security.Auth.SelfSigned.TokenLifetime == nil {
						toUpdate.Spec.Security.Auth.SelfSigned.TokenLifetime = defaultAuthTemplate.SelfSigned.TokenLifetime
					}
					if toUpdate.Spec.Security.Auth.SelfSigned.SharedSecret == nil {
						toUpdate.Spec.Security.Auth.SelfSigned.SharedSecret = defaultAuthTemplate.SelfSigned.SharedSecret
					}
				} else {
					toUpdate.Spec.Security.Auth.SelfSigned = defaultAuthTemplate.SelfSigned
				}
			} else {
				// security enabled, but no auth configuration
				toUpdate.Spec.Security.Auth = defaultAuthTemplate
			}
		}
	}
}

func setDefaultAutopilotProviders(
	toUpdate *corev1.StorageCluster,
) {
	if toUpdate.Spec.Autopilot != nil && toUpdate.Spec.Autopilot.Enabled &&
		len(toUpdate.Spec.Autopilot.Providers) == 0 {
		toUpdate.Spec.Autopilot.Providers = []corev1.DataProviderSpec{
			{
				Name: "default",
				Type: "prometheus",
				Params: map[string]string{
					"url": component.AutopilotDefaultProviderEndpoint,
				},
			},
		}
	}
}

func removeDeprecatedFields(
	cluster *corev1.StorageCluster,
) {
	// For new clusters with this version of the operator we don't
	// want to reset the images if they are present
	existingCluster := cluster.Spec.Version != ""
	// If the image has been reset once, we should not do it again,
	// as the user could have chosen to override the image manually.
	// status.version is a newly introduced field and will be
	// populated first time when this version of operator runs.
	alreadyDone := cluster.Status.Version != ""

	// Remove the deprecated lockImage flag from all components
	if cluster.Spec.Stork != nil {
		if existingCluster && !alreadyDone && !cluster.Spec.Stork.LockImage {
			cluster.Spec.Stork.Image = ""
		}
		cluster.Spec.Stork.LockImage = false
	}

	if cluster.Spec.Autopilot != nil {
		if existingCluster && !alreadyDone && !cluster.Spec.Autopilot.LockImage {
			cluster.Spec.Autopilot.Image = ""
		}
		cluster.Spec.Autopilot.LockImage = false
	}

	if cluster.Spec.UserInterface != nil {
		if existingCluster && !alreadyDone && !cluster.Spec.UserInterface.LockImage {
			cluster.Spec.UserInterface.Image = ""
		}
		cluster.Spec.UserInterface.LockImage = false
	}

	// If the deprecated metrics flag is set, then remove it and set the corresponding
	// exportMetrics flag in Prometheus spec.
	if cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.EnableMetrics != nil {
		if *cluster.Spec.Monitoring.EnableMetrics {
			if cluster.Spec.Monitoring.Prometheus == nil {
				cluster.Spec.Monitoring.Prometheus = &corev1.PrometheusSpec{}
			}
			cluster.Spec.Monitoring.Prometheus.ExportMetrics = true
		}
		cluster.Spec.Monitoring.EnableMetrics = nil
	}
}

func (p *portworx) hasComponentChanged(cluster *corev1.StorageCluster) bool {
	return hasStorkChanged(cluster) ||
		hasAutopilotChanged(cluster) ||
		hasLighthouseChanged(cluster) ||
		hasCSIChanged(cluster) ||
		hasTelemetryChanged(cluster) ||
		hasTelemetryProxyChanged(cluster) ||
		hasMetricsCollectorChanged(cluster) ||
		hasLogUploaderChanged(cluster) ||
		hasPrometheusChanged(cluster) ||
		hasAlertManagerChanged(cluster) ||
		p.hasPrometheusVersionChanged(cluster)
}

func hasStorkChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateStork(cluster) && cluster.Status.DesiredImages.Stork == ""
}

func hasAutopilotChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateAutopilot(cluster) && cluster.Status.DesiredImages.Autopilot == ""
}

func hasLighthouseChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateLighthouse(cluster) && cluster.Status.DesiredImages.UserInterface == ""
}
func autoUpdateCSISnapshotController(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.CSI.InstallSnapshotController != nil && *cluster.Spec.CSI.InstallSnapshotController
}

func hasCSIChanged(cluster *corev1.StorageCluster) bool {
	return pxutil.IsCSIEnabled(cluster) &&
		(cluster.Status.DesiredImages.CSIProvisioner == "" || hasCSISnapshotControllerChanged(cluster))
}

func hasCSISnapshotControllerChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateCSISnapshotController(cluster) &&
		cluster.Status.DesiredImages.CSISnapshotController == ""

}

func hasTelemetryChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateTelemetry(cluster) &&
		cluster.Status.DesiredImages.Telemetry == ""
}

func hasTelemetryProxyChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateTelemetryProxy(cluster) &&
		cluster.Status.DesiredImages.TelemetryProxy == ""
}

func hasMetricsCollectorChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateMetricsCollector(cluster) &&
		(cluster.Status.DesiredImages.MetricsCollector == "" ||
			cluster.Status.DesiredImages.MetricsCollectorProxy == "")
}

func hasLogUploaderChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateLogUploader(cluster) &&
		cluster.Status.DesiredImages.LogUploader == ""
}

func hasPrometheusChanged(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.Enabled &&
		cluster.Status.DesiredImages.PrometheusOperator == ""
}

func (p *portworx) hasPrometheusVersionChanged(cluster *corev1.StorageCluster) bool {
	return p.k8sVersion != nil &&
		p.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) &&
		cluster.Status.DesiredImages.PrometheusOperator == manifest.DefaultPrometheusOperatorImage
}

func hasAlertManagerChanged(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.AlertManager != nil &&
		cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled &&
		cluster.Status.DesiredImages.AlertManager == ""
}

func autoUpdateStork(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Stork != nil &&
		cluster.Spec.Stork.Enabled &&
		cluster.Spec.Stork.Image == ""
}

func autoUpdateAutopilot(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Autopilot != nil &&
		cluster.Spec.Autopilot.Enabled &&
		cluster.Spec.Autopilot.Image == ""
}

func autoUpdateTelemetry(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec) &&
		cluster.Spec.Monitoring.Telemetry.Image == ""
}

func autoUpdateTelemetryProxy(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec) &&
		pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster))
}

func autoUpdateMetricsCollector(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec) &&
		!pxutil.RunCCMGo(cluster)
}

func autoUpdateLogUploader(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec) &&
		pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster)) &&
		pxutil.RunCCMGo(cluster) &&
		cluster.Spec.Monitoring.Telemetry.ImageLogUpload == ""
}

func autoUpdateLighthouse(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.UserInterface != nil &&
		cluster.Spec.UserInterface.Enabled &&
		cluster.Spec.UserInterface.Image == ""
}

func autoUpdateComponents(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.AutoUpdateComponents != nil &&
		(*cluster.Spec.AutoUpdateComponents == corev1.OnceAutoUpdate ||
			*cluster.Spec.AutoUpdateComponents == corev1.AlwaysAutoUpdate)
}

func miscArgsChanged(currentArgs, miscArgs string) bool {
	return !strings.Contains(currentArgs, miscArgs)
}

func essentialsArgPresent(args string) bool {
	return strings.Contains(args, "--oem esse")
}

func init() {
	if err := storage.Register(pxutil.DriverName, &portworx{}); err != nil {
		logrus.Panicf("Error registering portworx storage driver: %v", err)
	}
}
