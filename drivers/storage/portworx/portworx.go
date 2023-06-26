package portworx

import (
	"context"
	stderr "errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	version "github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	api "k8s.io/kubernetes/pkg/apis/core"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storageapi "github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/operator/drivers/storage"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/cloudprovider"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	"github.com/libopenstorage/operator/pkg/preflight"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
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
	defaultEksCloudStorageType        = "gp3"
	defaultEksCloudStorageDeviceSize  = "150"
)

var (
	ErrNodeListEmpty = stderr.New("no nodes were available for px installation")
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

func (p *portworx) Validate(cluster *corev1.StorageCluster) error {
	podSpec, err := p.GetStoragePodSpec(cluster, "")
	if err != nil {
		return err
	}

	preFlighter := NewPreFlighter(cluster, p.k8sClient, podSpec)

	// Start the pre-flight container. The pre-flight checks at this time are specific to enabling DMthin
	err = preFlighter.RunPreFlight()
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			return err
		}
		logrus.Debugf("pre-flight: container already running...")
	}

	defer func() {
		// Clean up the pre-flight pods
		logrus.Infof("pre-flight: cleaning pre-flight ds...")
		if derr := preFlighter.DeletePreFlight(); derr != nil {
			logrus.Errorf("pre-flight: error deleting pre-flight: %v", derr)
		}
	}()

	cnt := 0
	//  Wait for all the pre-flight pods to finish
	for {
		time.Sleep(3 * time.Second) // Pause before status check
		completed, inProgress, total, err := preFlighter.GetPreFlightStatus()
		if err != nil {
			logrus.Errorf("pre-flight: error getting pre-flight status: %v", err)
			return err
		}
		logrus.Infof("pre-flight: Completed [%v] In Progress [%v] Total [%v]", completed, inProgress, total)

		if total != 0 && completed == total {
			logrus.Infof("pre-flight: completed...")
			break
		}

		// Add five minute timeout.  If we do reconcile loop check we will need a different way.
		cnt++
		if cnt == 200 { // 3s * 100 = 300s (10 mins)
			err = fmt.Errorf("pre-flight: pre-flight status check timed out")
			logrus.Errorf("%v", err)
			return err
		}
	}

	// Process all the StorageNode.Status.Checks
	var storageNodes []*corev1.StorageNode

	storageNodes, err = p.storageNodesList(cluster)
	if err == nil {
		err = preFlighter.ProcessPreFlightResults(p.recorder, storageNodes)
		if err != nil {
			logrus.Errorf("pre-flight: Error processing results: %v", err)
		}
	} else {
		logrus.Errorf("pre-flight incomplete: Error getting storage node list: %v", err)
	}

	return err
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

	if pxutil.AuthEnabled(&cluster.Spec) {
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
	pxutil.AppendTLSEnv(&cluster.Spec, envMap)

	return envMap
}

func (p *portworx) GetSelectorLabels() map[string]string {
	return pxutil.SelectorLabels()
}

func (p *portworx) setMaxStorageNodePerZone(toUpdate *corev1.StorageCluster, upgradingPortworx bool) error {
	// Set max node per zone
	// if no value is set for any of max_storage_nodes*, try to see if can set a default value
	if toUpdate.Spec.CloudStorage != nil &&
		toUpdate.Spec.CloudStorage.MaxStorageNodesPerZonePerNodeGroup == nil &&
		toUpdate.Spec.CloudStorage.MaxStorageNodesPerZone == nil &&
		toUpdate.Spec.CloudStorage.MaxStorageNodes == nil {
		maxStorageNodesPerZone := uint32(0)
		var err error

		nodeList := &v1.NodeList{}
		err = p.k8sClient.List(context.TODO(), nodeList, &client.ListOptions{})
		if err != nil {
			return err
		}

		if pxutil.IsFreshInstall(toUpdate) {
			// Let's do this only when it's a fresh install of px
			maxStorageNodesPerZone, err = p.getDefaultMaxStorageNodesPerZone(toUpdate, nodeList)
			if err != nil {
				logrus.Errorf("could not set default value for max_storage_nodes_per_zone (first install): %v", err)
			}
		} else if upgradingPortworx {
			// Upgrade scenario
			// If PX image is not changing, we don't have to update the values. This is to prevent pod restarts
			maxStorageNodesPerZone, err = p.getCurrentMaxStorageNodesPerZone(toUpdate, nodeList)
			if err != nil {
				logrus.Errorf("could not set a default value for max_storage_nodes_per_zone %v", err)
			}
		}
		if err == nil && maxStorageNodesPerZone != 0 {
			toUpdate.Spec.CloudStorage.MaxStorageNodesPerZone = &maxStorageNodesPerZone
			logrus.Infof("setting spec.cloudStorage.maxStorageNodesPerZone %v", maxStorageNodesPerZone)
		}
	}

	return nil
}

func (p *portworx) getDefaultStorageNodesDisaggregatedMode(
	cluster *corev1.StorageCluster,
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
	nodeTypeZoneMap, err := cloudprovider.GetZoneMap(p.k8sClient, util.NodeTypeKey, util.StorageNodeValue)
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
		// Let's not count zones with no nodes
		if value < minValue && value != 0 {
			minValue = value
		}
		if prevValue != math.MaxUint64 && prevValue != value {
			k8sutil.InfoEvent(
				p.recorder, cluster, util.UnevenStorageNodesReason,
				fmt.Sprintf("Uneven number of storage nodes labelled across zones."+
					" %v has %v, %v has %v", prevKey, prevValue, key, value),
			)
		}
		prevKey = key
		prevValue = value
	}
	if totalNodes == 0 {
		// no node is labelled with portworx.io/node-type
		nodeTypeZoneMap, err = cloudprovider.GetZoneMap(p.k8sClient, util.NodeTypeKey, util.StoragelessNodeValue)
		if err == nil {
			for _, value := range nodeTypeZoneMap {
				totalNodes += value
			}
			if totalNodes > 0 {
				k8sutil.InfoEvent(
					p.recorder, cluster, util.AllStoragelessNodesReason,
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
func (p *portworx) getDefaultMaxStorageNodesPerZone(
	cluster *corev1.StorageCluster,
	nodeList *v1.NodeList,
) (uint32, error) {
	if len(nodeList.Items) == 0 {
		return 0, nil
	}

	// Check if storage nodes are explicitly set using labels
	storageNodes, disaggregatedMode, err := p.getDefaultStorageNodesDisaggregatedMode(cluster)
	if err != nil {
		return 0, err
	}
	if disaggregatedMode {
		return uint32(storageNodes), nil
	}

	zoneMap, err := cloudprovider.GetZoneMap(p.k8sClient, "", "")
	if err != nil {
		return 0, err
	}
	numZones := len(zoneMap)
	if numZones <= 0 {
		return 0, ErrNodeListEmpty
	}
	storageNodes = uint64(len(nodeList.Items) / numZones)
	return uint32(storageNodes), nil
}

func (p *portworx) getCurrentMaxStorageNodesPerZone(
	cluster *corev1.StorageCluster,
	nodeList *v1.NodeList,
) (uint32, error) {
	storageNodeMap := make(map[string]*storageapi.StorageNode)

	if pxutil.IsPortworxEnabled(cluster) {
		storageNodeList, err := p.GetStorageNodes(cluster)
		if err != nil {
			logrus.Errorf("Couldn't get storage node list: %v", err)
			return 0, err
		}
		for _, storageNode := range storageNodeList {
			if len(storageNode.SchedulerNodeName) != 0 && len(storageNode.Pools) > 0 {
				storageNodeMap[storageNode.SchedulerNodeName] = storageNode
			}
		}
		zoneMap := make(map[string]uint32)
		cloudProvider := cloudprovider.Get()
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

func (p *portworx) SetDefaultsOnStorageCluster(toUpdate *corev1.StorageCluster) error {
	if toUpdate.Annotations == nil {
		toUpdate.Annotations = make(map[string]string)
	}

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

		if err := SetPortworxDefaults(toUpdate, p.k8sVersion); err != nil {
			return err
		}

		if err := p.setTelemetryDefaults(toUpdate); err != nil {
			return err
		}
	}

	// Initialize the preflight check annotation
	if preflight.RequiresCheck() {
		if _, ok := toUpdate.Annotations[pxutil.AnnotationPreflightCheck]; !ok {
			toUpdate.Annotations[pxutil.AnnotationPreflightCheck] = "true"
		}
	}

	if preflight.IsEKS() {
		toUpdate.Annotations[pxutil.AnnotationIsEKS] = "true"
	}

	removeDeprecatedFields(toUpdate)

	toUpdate.Spec.Image = strings.TrimSpace(toUpdate.Spec.Image)
	toUpdate.Spec.Version = pxutil.GetImageTag(toUpdate.Spec.Image)
	pxVersionChanged := pxEnabled &&
		(toUpdate.Spec.Version == "" || toUpdate.Spec.Version != toUpdate.Status.Version)

	if err := p.setMaxStorageNodePerZone(toUpdate, pxVersionChanged); err != nil {
		return err
	}

	if pxVersionChanged || autoUpdateComponents(toUpdate) || p.hasComponentChanged(toUpdate) {
		// Force latest versions only if the component update strategy is Once
		force := pxVersionChanged || (toUpdate.Spec.AutoUpdateComponents != nil &&
			*toUpdate.Spec.AutoUpdateComponents == corev1.OnceAutoUpdate)
		release := manifest.Instance().GetVersions(toUpdate, force)

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
			toUpdate.Status.DesiredImages.Telemetry = release.Components.Telemetry
		}

		if autoUpdateTelemetryProxy(toUpdate) &&
			(toUpdate.Status.DesiredImages.TelemetryProxy == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.TelemetryProxy = release.Components.TelemetryProxy
		}

		if autoUpdateMetricsCollector(toUpdate) &&
			(toUpdate.Status.DesiredImages.MetricsCollector == "" ||
				(!pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(toUpdate)) &&
					toUpdate.Status.DesiredImages.MetricsCollectorProxy == "") ||
				(pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(toUpdate)) &&
					toUpdate.Status.DesiredImages.TelemetryProxy == "") ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.MetricsCollector = release.Components.MetricsCollector
			if !pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(toUpdate)) {
				toUpdate.Status.DesiredImages.MetricsCollectorProxy = release.Components.MetricsCollectorProxy
			} else {
				toUpdate.Status.DesiredImages.MetricsCollectorProxy = ""
			}
		}

		if autoUpdateLogUploader(toUpdate) &&
			(toUpdate.Status.DesiredImages.LogUploader == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.LogUploader = release.Components.LogUploader
		}

		if autoUpdatePxRepo(toUpdate) &&
			(toUpdate.Status.DesiredImages.PxRepo == "" ||
				pxVersionChanged ||
				autoUpdateComponents(toUpdate)) {
			toUpdate.Status.DesiredImages.PxRepo = release.Components.PxRepo
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

		// set misc image defaults
		imagesData := []struct {
			desiredImage *string
			releaseImage string
		}{
			{&toUpdate.Status.DesiredImages.KubeControllerManager, release.Components.KubeControllerManager},
			{&toUpdate.Status.DesiredImages.KubeScheduler, release.Components.KubeScheduler},
			{&toUpdate.Status.DesiredImages.Pause, release.Components.Pause},
		}
		for _, v := range imagesData {
			if *v.desiredImage == "" {
				*v.desiredImage = v.releaseImage
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

	if !autoUpdatePxRepo(toUpdate) {
		toUpdate.Status.DesiredImages.PxRepo = ""
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

	setAutopilotDefaults(toUpdate)
	setTLSDefaults(toUpdate)
	return nil
}

func (p *portworx) PreInstall(cluster *corev1.StorageCluster) error {
	if err := p.validateCleanup(cluster); err != nil {
		return err
	}

	if err := p.validateEssentials(); err != nil {
		return err
	}

	if err := p.validateFACDTopology(cluster); err != nil {
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
				noKindErr := &meta.NoKindMatchError{}
				if stderr.As(err, &noKindErr) || strings.Contains(err.Error(), "no matches for kind ") {
					logrus.WithError(err).Tracef("No component `%s` to delete", comp.Name())
					continue
				}
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

func (p *portworx) validateFACDTopology(cluster *corev1.StorageCluster) error {
	facdTopologyEnv := ""
	for _, env := range cluster.Spec.Env {
		if env.Name == "FACD_TOPOLOGY_ENABLED" {
			facdTopologyEnv = strings.ToLower(env.Value)
		}
	}

	if facdTopologyEnv == "true" {
		// FACD topology is enabled. See if we already have the annotation marking this as valid, or if not, check if this is a fresh install

		// TODO: once we support upgrading to FACD topology, it will also be valid after whatever version supports that
		isValid := false
		if pxutil.IsFreshInstall(cluster) {
			if cluster.Annotations == nil {
				cluster.Annotations = make(map[string]string)
			}
			cluster.Annotations[pxutil.AnnotationFACDTopology] = "true"
			isValid = true
		}
		if !isValid {
			if a, ok := cluster.Annotations[pxutil.AnnotationFACDTopology]; ok && a != "" {
				isValid = true
			}
		}

		if !isValid {
			// Fail the request!
			return fmt.Errorf("FACD topology cannot be enabled on existing clusters")
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
		if pxutil.IsFreshInstall(cluster) && strings.HasPrefix(err.Error(), pxutil.ErrMsgGrpcConnection) {
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
		condition := &corev1.ClusterCondition{
			Source:  pxutil.PortworxComponentName,
			Type:    corev1.ClusterConditionTypeDelete,
			Status:  corev1.ClusterConditionStatusCompleted,
			Message: storageClusterDeleteMsg,
		}
		return condition, nil
	}

	// Portworx needs to be removed if DeleteStrategy is specified
	removeData := false
	completeMsg := storageClusterUninstallMsg
	if cluster.Spec.DeleteStrategy.Type == corev1.UninstallAndWipeStorageClusterStrategyType {
		removeData = true
		completeMsg = storageClusterUninstallAndWipeMsg
	}

	u := NewUninstaller(cluster, p.k8sClient)
	completed, inProgress, total, err := u.GetNodeWiperStatus()
	if err != nil && errors.IsNotFound(err) {
		deleteCondition := util.GetStorageClusterCondition(cluster, pxutil.PortworxComponentName, corev1.ClusterConditionTypeDelete)
		if deleteCondition != nil && deleteCondition.Status == corev1.ClusterConditionStatusCompleted {
			return &corev1.ClusterCondition{
				Source:  pxutil.PortworxComponentName,
				Type:    corev1.ClusterConditionTypeDelete,
				Status:  corev1.ClusterConditionStatusCompleted,
				Message: completeMsg,
			}, nil
		}
		if err := u.RunNodeWiper(removeData, p.recorder); err != nil {
			return &corev1.ClusterCondition{
				Source:  pxutil.PortworxComponentName,
				Type:    corev1.ClusterConditionTypeDelete,
				Status:  corev1.ClusterConditionStatusFailed,
				Message: "Failed to run node wiper: " + err.Error(),
			}, nil
		}
		return &corev1.ClusterCondition{
			Source:  pxutil.PortworxComponentName,
			Type:    corev1.ClusterConditionTypeDelete,
			Status:  corev1.ClusterConditionStatusInProgress,
			Message: "Started node wiper daemonset",
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
			logrus.Infof("Deleting portworx metadata")
			if err := u.WipeMetadata(); err != nil {
				logrus.Errorf("Failed to delete portworx metadata: %v", err)
				return &corev1.ClusterCondition{
					Source:  pxutil.PortworxComponentName,
					Type:    corev1.ClusterConditionTypeDelete,
					Status:  corev1.ClusterConditionStatusFailed,
					Message: "Failed to wipe metadata: " + err.Error(),
				}, nil
			}
		}
		return &corev1.ClusterCondition{
			Source:  pxutil.PortworxComponentName,
			Type:    corev1.ClusterConditionTypeDelete,
			Status:  corev1.ClusterConditionStatusCompleted,
			Message: completeMsg,
		}, nil
	}

	return &corev1.ClusterCondition{
		Source:  pxutil.PortworxComponentName,
		Type:    corev1.ClusterConditionTypeDelete,
		Status:  corev1.ClusterConditionStatusInProgress,
		Message: fmt.Sprintf("Wipe operation still in progress: Completed [%v] In Progress [%v] Total [%v]", completed, inProgress, total),
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
func SetPortworxDefaults(toUpdate *corev1.StorageCluster, k8sVersion *version.Version) error {
	if k8sVersion == nil {
		k8sVersion = pxutil.MinimumSupportedK8sVersion
	}
	t, err := newTemplate(toUpdate, "")
	if err != nil {
		return err
	}

	if toUpdate.Spec.SecretsProvider == nil {
		toUpdate.Spec.SecretsProvider = stringPtr(defaultSecretsProvider)
	}
	startPort := uint32(t.startPort)
	toUpdate.Spec.StartPort = &startPort

	setPortworxKvdbDefaults(toUpdate)

	setPortworxStorageSpecDefaults(toUpdate)

	setNodeSpecDefaults(toUpdate)

	setPlacementDefaults(toUpdate, t, k8sVersion)

	if err := setCSIDefaults(toUpdate, t, k8sVersion); err != nil {
		return err
	}

	setSecuritySpecDefaults(toUpdate)

	return nil
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

func setPortworxKvdbDefaults(toUpdate *corev1.StorageCluster) {
	if toUpdate.Spec.Kvdb == nil {
		toUpdate.Spec.Kvdb = &corev1.KvdbSpec{}
	}
	if len(toUpdate.Spec.Kvdb.Endpoints) == 0 {
		toUpdate.Spec.Kvdb.Internal = true
	}
}

func setPortworxStorageSpecDefaults(toUpdate *corev1.StorageCluster) {
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
		if preflight.RunningOnCloud() {
			setPortworxCloudStorageSpecDefaults(toUpdate)
			if toUpdate.Spec.CloudStorage != nil {
				initializeStorageSpec = false
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

	if toUpdate.Spec.CloudStorage != nil {
		providerName := toUpdate.Spec.CloudStorage.Provider

		// if provider is not set, let's check if it's vSphere
		if providerName == nil || len(*providerName) == 0 {
			provider := pxutil.GetCloudProvider(toUpdate)
			if len(provider) > 0 {
				providerName = &provider
			}
		}

		if providerName != nil &&
			preflight.Instance().ProviderName() != *providerName {
			preflight.Instance().SetProvider(*providerName)
		}
		toUpdate.Spec.CloudStorage.Provider = providerName
	}

	if toUpdate.Spec.CloudStorage != nil && preflight.IsEKS() {
		// Set default drive type to gp3 if not specified
		for i := 0; i < len(toUpdate.Spec.CloudStorage.CapacitySpecs); i++ {
			spec := &toUpdate.Spec.CloudStorage.CapacitySpecs[i]
			if spec.Options == nil {
				spec.Options = make(map[string]string)
			}
			if _, ok := spec.Options["type"]; !ok {
				spec.Options["type"] = defaultEksCloudStorageType
			}
		}
	}
}

func setPortworxCloudStorageSpecDefaults(toUpdate *corev1.StorageCluster) {
	if preflight.IsEKS() {
		if toUpdate.Spec.CloudStorage == nil {
			toUpdate.Spec.CloudStorage = &corev1.CloudStorageSpec{}
		}
		if len(toUpdate.Spec.CloudStorage.CapacitySpecs) == 0 &&
			toUpdate.Spec.CloudStorage.DeviceSpecs == nil {
			defaultEKSCloudStorageDevice := fmt.Sprintf("type=%s,size=%s", defaultEksCloudStorageType, defaultEksCloudStorageDeviceSize)
			deviceSpecs := []string{defaultEKSCloudStorageDevice}
			toUpdate.Spec.CloudStorage.DeviceSpecs = &deviceSpecs
		}
	}
	// TODO: add default cloud storage specs for other providers
}

func setPlacementDefaults(toUpdate *corev1.StorageCluster, t *template, k8sVersion *version.Version) {
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
}

func setCSIDefaults(toUpdate *corev1.StorageCluster, t *template, k8sVersion *version.Version) error {
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

	// Enable CSI if running on k8s 1.26+
	if toUpdate.Spec.CSI == nil {
		var err error
		// Refresh k8s version, so we don't have to restart operator. We have seen issues
		// that operator still caches old k8s version after k8s upgrade.
		k8sVersion, err = k8sutil.GetVersion()
		if err != nil {
			return err
		}

		// We update CSI in StorageCluster instead of changing IsEnabled API in CSI component, due to
		// IsEnabled does not return error, if k8s.GetVersion() fails, IsEnabled will return wrong result.
		if k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_26) {
			logrus.Infof("Enable CSI on k8s 1.26+, current k8s version %s", k8sVersion.String())
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

	return nil
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
		if pxutil.AuthEnabled(&toUpdate.Spec) {
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
				// auth enabled, but no auth configuration
				toUpdate.Spec.Security.Auth = defaultAuthTemplate
			}
		}
	}
}

func setAutopilotDefaults(
	toUpdate *corev1.StorageCluster,
) {
	if toUpdate.Spec.Autopilot == nil || !toUpdate.Spec.Autopilot.Enabled {
		return
	}

	if len(toUpdate.Spec.Autopilot.Providers) == 0 {
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

func setTLSDefaults(
	toUpdate *corev1.StorageCluster,
) {
	sec := toUpdate.Spec.Security
	secEnabled := true

	if pxutil.GetPortworxVersion(toUpdate).LessThan(pxutil.MinimumPxVersionAutoTLS) {
		// px version too low for auto-ssl setup, bail out..
		return
	} else if sec == nil || !sec.Enabled || sec.TLS == nil || sec.TLS.Enabled == nil || !*sec.TLS.Enabled {
		// security/TLS not enabled
		secEnabled = false
		// proceed to check env-vars
	}

	type listOpType struct {
		list    *[]v1.EnvVar
		enabled bool
	}
	var listOps []listOpType

	if toUpdate.Spec.Autopilot != nil {
		listOps = append(listOps, listOpType{
			&toUpdate.Spec.Autopilot.Env,
			toUpdate.Spec.Autopilot.Enabled && secEnabled,
		})
	}
	if toUpdate.Spec.Stork != nil {
		listOps = append(listOps, listOpType{
			&toUpdate.Spec.Stork.Env,
			toUpdate.Spec.Stork.Enabled && secEnabled,
		})
	}

	// update env-lists, set (or remove) PX_ENABLE_TLS=true
	for _, listOp := range listOps {
		newEnv := make([]v1.EnvVar, 0, len(*listOp.list)+1)
		updated := false
		if listOp.enabled {
			// enabled component -- add/update PX_ENABLE_TLS=true
			for _, ev := range *listOp.list {
				if ev.Name == pxutil.EnvKeyPortworxEnableTLS {
					ev.Value = "true"
					updated = true
				}
				newEnv = append(newEnv, ev)
			}
			if !updated {
				newEnv = append(newEnv, v1.EnvVar{
					Name:  pxutil.EnvKeyPortworxEnableTLS,
					Value: "true",
				})
				updated = true
			}
		} else {
			// disabled component -- remove PX_ENABLE_TLS
			for _, ev := range *listOp.list {
				if ev.Name == pxutil.EnvKeyPortworxEnableTLS {
					updated = true
					continue
				}
				newEnv = append(newEnv, ev)
			}
		}
		if updated {
			*listOp.list = newEnv
		}
	}
}

// setTelemetryDefaults validates and sets telemetry values
// Telemetry will be disabled if:
// 1. It's disabled explicitly
// 2. HTTPS proxy is configured
// 3. HTTP proxy url is not in a format of hostname:port
// 4. PX version incompatible
// 5. Cannot ping Arcus endpoint when first time enabled (telemetry cert is not created yet)
// Otherwise it will be enabled by default
func (p *portworx) setTelemetryDefaults(
	toUpdate *corev1.StorageCluster,
) error {
	// Telemetry is disabled explicitly then leave it as is
	if toUpdate.Spec.Monitoring != nil &&
		toUpdate.Spec.Monitoring.Telemetry != nil &&
		!toUpdate.Spec.Monitoring.Telemetry.Enabled {
		return nil
	}

	pxVersion := pxutil.GetPortworxVersion(toUpdate)
	proxyType, proxy := pxutil.GetPxProxyEnvVarValue(toUpdate)

	// Telemetry is not supported in those cases, set to disabled
	var err error
	if pxVersion.LessThan(pxutil.MinimumPxVersionCCM) {
		// PX version is lower than 2.8
		err = fmt.Errorf("telemetry is not supported on Portworx version: %s", pxVersion)
	} else if !pxutil.IsCCMGoSupported(pxVersion) {
		// CCM Java case, PX version is between 2.8 and 2.12, don't set any default values
		return nil
	} else if proxyType == pxutil.EnvKeyPortworxHTTPProxy {
		// CCM Go is supported, but HTTP proxy cannot be split into host and port
		if _, _, proxyFormatErr := pxutil.SplitPxProxyHostPort(proxy); proxyFormatErr != nil {
			err = fmt.Errorf("telemetry is not supported with proxy in a format of: %s", proxy)
		}
	} else if proxyType == pxutil.EnvKeyPortworxHTTPSProxy {
		// CCM Go is not supported with https proxy
		// TODO: remove when HTTPS proxy is supported
		err = fmt.Errorf("telemetry is not supported with secure proxy: %s", proxy)
	}
	if toUpdate.Spec.Monitoring == nil {
		toUpdate.Spec.Monitoring = &corev1.MonitoringSpec{}
	}
	if toUpdate.Spec.Monitoring.Telemetry == nil {
		toUpdate.Spec.Monitoring.Telemetry = &corev1.TelemetrySpec{}
	}
	if err != nil {
		if pxutil.IsTelemetryEnabled(toUpdate.Spec) {
			msg := fmt.Sprintf("telemetry will be disabled: %v", err)
			k8sutil.WarningEvent(p.recorder, toUpdate, util.TelemetryDisabledReason, msg)
		}
		toUpdate.Spec.Monitoring.Telemetry.Enabled = false
		toUpdate.Spec.Monitoring.Telemetry.Image = ""
		toUpdate.Spec.Monitoring.Telemetry.LogUploaderImage = ""
		return nil
	}

	// Telemetry CCM Go is supported, at this point telemetry is enabled or can be enabled potentially
	// If the secret exists, means telemetry was enabled before and registered already, enable telemetry directly
	// If the telemetry secret doesn't exist, check if Arcus is reachable first before enabling telemetry:
	// * If Arcus is not reachable, registration will fail anyway, or it's an air-gapped cluster, disable telemetry
	// * If Arcus is reachable, enable telemetry by default
	secret := &v1.Secret{}
	err = p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      pxutil.TelemetryCertName,
			Namespace: toUpdate.Namespace,
		},
		secret,
	)
	if errors.IsNotFound(err) {
		// If telemetry secret is not created yet, set telemetry as disabled if the registration endpoint is not reachable
		// Only ping Arcus when telemetry secret is not found, otherwise the cluster was already registered before
		if canAccess := component.CanAccessArcusRegisterEndpoint(toUpdate, proxy); !canAccess {
			toUpdate.Spec.Monitoring.Telemetry.Enabled = false
			toUpdate.Spec.Monitoring.Telemetry.Image = ""
			toUpdate.Spec.Monitoring.Telemetry.LogUploaderImage = ""
			msg := "telemetry will be disabled: cannot reach to Pure1"
			k8sutil.WarningEvent(p.recorder, toUpdate, util.TelemetryDisabledReason, msg)
			return nil
		}
	} else if err != nil {
		return err
	}

	// Set telemetry as enabled if telemetry secret exists or the registration endpoint is reachable
	if !pxutil.IsTelemetryEnabled(toUpdate.Spec) {
		k8sutil.InfoEvent(p.recorder, toUpdate, util.TelemetryEnabledReason, "telemetry will be enabled by default")
		toUpdate.Spec.Monitoring.Telemetry.Enabled = true
	}
	return nil
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
		p.hasPrometheusVersionChanged(cluster) ||
		hasPxRepoChanged(cluster) ||
		p.hasKubernetesVersionChanged(cluster)
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
			(!pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster)) &&
				cluster.Status.DesiredImages.MetricsCollectorProxy == "") ||
			(pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster)) &&
				cluster.Status.DesiredImages.TelemetryProxy == ""))
}

func hasLogUploaderChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdateLogUploader(cluster) &&
		cluster.Status.DesiredImages.LogUploader == ""
}

func hasPxRepoChanged(cluster *corev1.StorageCluster) bool {
	return autoUpdatePxRepo(cluster) && cluster.Status.DesiredImages.PxRepo == ""
}

func hasPrometheusChanged(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.Enabled &&
		cluster.Status.DesiredImages.PrometheusOperator == ""
}

func (p *portworx) hasKubernetesVersionChanged(cluster *corev1.StorageCluster) bool {
	if p.k8sVersion == nil {
		return false
	} else if cluster.Status.DesiredImages.KubeControllerManager == "" &&
		cluster.Status.DesiredImages.KubeScheduler == "" {
		return false
	}

	list := []string{cluster.Status.DesiredImages.KubeControllerManager, cluster.Status.DesiredImages.KubeScheduler}
	for _, img := range list {
		parts := strings.Split(img, ":")
		if len(parts) > 1 {
			v, err := version.NewVersion(parts[1])
			if err == nil && !v.Equal(p.k8sVersion) {
				return true
			}
		}
	}
	return false
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
	if pxutil.IsTelemetryEnabled(cluster.Spec) {
		if cluster.Spec.Monitoring.Telemetry.Image == "" {
			return true
		} else if pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster)) &&
			!strings.Contains(cluster.Spec.Monitoring.Telemetry.Image, "ccm-go") {
			// reset telemetry image when new ccm is supported but old ccm image is set explicitly
			cluster.Spec.Monitoring.Telemetry.Image = ""
			return true
		}
	}
	return false
}

func autoUpdateTelemetryProxy(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec) &&
		pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster))
}

func autoUpdateMetricsCollector(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec)
}

func autoUpdateLogUploader(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec) &&
		pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster)) &&
		cluster.Spec.Monitoring.Telemetry.LogUploaderImage == ""
}

func autoUpdatePxRepo(cluster *corev1.StorageCluster) bool {
	return pxutil.IsPxRepoEnabled(cluster.Spec) &&
		cluster.Spec.PxRepo.Image == ""
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
