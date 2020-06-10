package portworx

import (
	"context"
	"fmt"
	"strings"
	"time"

	version "github.com/hashicorp/go-version"
	"github.com/libopenstorage/operator/drivers/storage"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	storkDriverName                   = "pxd"
	defaultPortworxImage              = "portworx/oci-monitor"
	defaultSDKPort                    = 9020
	defaultSecretsProvider            = "k8s"
	defaultTokenLifetime              = time.Hour * 24
	defaultSelfSignedIssuer           = "portworx.io"
	envKeyNodeWiperImage              = "PX_NODE_WIPER_IMAGE"
	envKeyPortworxEnableTLS           = "PX_ENABLE_TLS"
	storageClusterDeleteMsg           = "Portworx service NOT removed. Portworx drives and data NOT wiped."
	storageClusterUninstallMsg        = "Portworx service removed. Portworx drives and data NOT wiped."
	storageClusterUninstallAndWipeMsg = "Portworx service removed. Portworx drives and data wiped."
	labelPortworxVersion              = "PX Version"
)

var (
	// getVersionManifest is extracted as a var for testing
	getVersionManifest = manifest.GetVersions
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
		{
			Name:  pxutil.EnvKeyPortworxServiceName,
			Value: component.PxAPIServiceName,
		},
	}
}

func (p *portworx) GetSelectorLabels() map[string]string {
	return pxutil.SelectorLabels()
}

func (p *portworx) SetDefaultsOnStorageCluster(toUpdate *corev1alpha1.StorageCluster) {
	if toUpdate.Status.DesiredImages == nil {
		toUpdate.Status.DesiredImages = &corev1alpha1.ComponentImages{}
	}

	if pxutil.IsPortworxEnabled(toUpdate) {
		if toUpdate.Spec.Stork == nil {
			toUpdate.Spec.Stork = &corev1alpha1.StorkSpec{
				Enabled: true,
			}
		}
		setPortworxDefaults(toUpdate)
	}

	removeDeprecatedFields(toUpdate)

	toUpdate.Spec.Image = strings.TrimSpace(toUpdate.Spec.Image)
	toUpdate.Spec.Version = pxutil.GetImageTag(toUpdate.Spec.Image)
	pxVersionChanged := toUpdate.Spec.Version == "" ||
		toUpdate.Spec.Version != toUpdate.Status.Version

	if pxVersionChanged || hasComponentChanged(toUpdate) {
		release := getVersionManifest(toUpdate, p.recorder)
		if toUpdate.Spec.Version == "" {
			if toUpdate.Spec.Image == "" {
				toUpdate.Spec.Image = defaultPortworxImage
			}
			toUpdate.Spec.Image = toUpdate.Spec.Image + ":" + release.PortworxVersion
			toUpdate.Spec.Version = release.PortworxVersion
		}
		toUpdate.Status.Version = toUpdate.Spec.Version

		if autoUpdateStork(toUpdate) &&
			(toUpdate.Status.DesiredImages.Stork == "" || pxVersionChanged) {
			toUpdate.Status.DesiredImages.Stork = release.Components.Stork
		}

		if autoUpdateAutopilot(toUpdate) &&
			(toUpdate.Status.DesiredImages.Autopilot == "" || pxVersionChanged) {
			toUpdate.Status.DesiredImages.Autopilot = release.Components.Autopilot
		}

		if autoUpdateLighthouse(toUpdate) &&
			(toUpdate.Status.DesiredImages.UserInterface == "" || pxVersionChanged) {
			toUpdate.Status.DesiredImages.UserInterface = release.Components.Lighthouse
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

	setDefaultAutopilotProviders(toUpdate)
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
		if err := u.RunNodeWiper(removeData, p.recorder); err != nil {
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

func (p *portworx) normalEvent(
	cluster *corev1alpha1.StorageCluster,
	reason, message string,
) {
	logrus.Info(message)
	p.recorder.Event(cluster, v1.EventTypeNormal, reason, message)
}

func (p *portworx) warningEvent(
	cluster *corev1alpha1.StorageCluster,
	reason, message string,
) {
	logrus.Warn(message)
	p.recorder.Event(cluster, v1.EventTypeWarning, reason, message)
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

func setPortworxDefaults(toUpdate *corev1alpha1.StorageCluster) {
	t, err := newTemplate(toUpdate)
	if err != nil {
		return
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

	setSecuritySpecDefaults(toUpdate)
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
			if nodeSpecCopy.Storage.KvdbDevice == nil && toUpdate.Spec.Storage.KvdbDevice != nil {
				nodeSpecCopy.Storage.KvdbDevice = stringPtr(*toUpdate.Spec.Storage.KvdbDevice)
			}
		}
		updatedNodeSpecs = append(updatedNodeSpecs, *nodeSpecCopy)
	}
	toUpdate.Spec.Nodes = updatedNodeSpecs
}

func setSecuritySpecDefaults(toUpdate *corev1alpha1.StorageCluster) {
	// all default values if one is not provided below.
	defaultAuthTemplate := &corev1alpha1.AuthSpec{
		Authenticators: &corev1alpha1.AuthenticatorsSpec{
			SelfSigned: &corev1alpha1.SelfSignedSpec{
				Issuer:        stringPtr(defaultSelfSignedIssuer),
				TokenLifetime: metav1DurationPtr(defaultTokenLifetime),
			},
		},
	}

	if toUpdate.Spec.Security != nil {
		if toUpdate.Spec.Security.Enabled {
			if toUpdate.Spec.Security.Auth != nil && (*toUpdate.Spec.Security.Auth != corev1alpha1.AuthSpec{}) {
				if toUpdate.Spec.Security.Auth.Authenticators != nil && (*toUpdate.Spec.Security.Auth.Authenticators != corev1alpha1.AuthenticatorsSpec{}) {
					if toUpdate.Spec.Security.Auth.Authenticators.SelfSigned != nil && (*toUpdate.Spec.Security.Auth.Authenticators.SelfSigned != corev1alpha1.SelfSignedSpec{}) {
						selfSignedIssuerEnvVal := pxutil.GetClusterEnvVarValue(context.TODO(), toUpdate, pxutil.EnvKeyPortworxAuthJwtIssuer)
						if toUpdate.Spec.Security.Auth.Authenticators.SelfSigned.Issuer != nil && (*toUpdate.Spec.Security.Auth.Authenticators.SelfSigned.Issuer != "") {
							// leave as is, non-nil and non-empty
						} else {
							// use environment variable if passed, otherwise use default
							if selfSignedIssuerEnvVal == "" {
								toUpdate.Spec.Security.Auth.Authenticators.SelfSigned.Issuer = defaultAuthTemplate.Authenticators.SelfSigned.Issuer
							} else {
								toUpdate.Spec.Security.Auth.Authenticators.SelfSigned.Issuer = &selfSignedIssuerEnvVal
							}
						}
						if toUpdate.Spec.Security.Auth.Authenticators.SelfSigned.TokenLifetime == nil {
							toUpdate.Spec.Security.Auth.Authenticators.SelfSigned.TokenLifetime = defaultAuthTemplate.Authenticators.SelfSigned.TokenLifetime
						}
					} else {
						toUpdate.Spec.Security.Auth.Authenticators.SelfSigned = defaultAuthTemplate.Authenticators.SelfSigned
					}
				} else {
					toUpdate.Spec.Security.Auth.Authenticators = defaultAuthTemplate.Authenticators
				}
			} else {
				// security enabled, but no auth configuration
				toUpdate.Spec.Security.Auth = defaultAuthTemplate
			}
		}
	}
}

func setDefaultAutopilotProviders(
	toUpdate *corev1alpha1.StorageCluster,
) {
	if toUpdate.Spec.Autopilot != nil && toUpdate.Spec.Autopilot.Enabled &&
		len(toUpdate.Spec.Autopilot.Providers) == 0 {
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

func removeDeprecatedFields(
	cluster *corev1alpha1.StorageCluster,
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
				cluster.Spec.Monitoring.Prometheus = &corev1alpha1.PrometheusSpec{}
			}
			cluster.Spec.Monitoring.Prometheus.ExportMetrics = true
		}
		cluster.Spec.Monitoring.EnableMetrics = nil
	}
}

func hasComponentChanged(cluster *corev1alpha1.StorageCluster) bool {
	return hasStorkChanged(cluster) ||
		hasAutopilotChanged(cluster) ||
		hasLighthouseChanged(cluster)
}

func hasStorkChanged(cluster *corev1alpha1.StorageCluster) bool {
	return autoUpdateStork(cluster) && cluster.Status.DesiredImages.Stork == ""
}

func hasAutopilotChanged(cluster *corev1alpha1.StorageCluster) bool {
	return autoUpdateAutopilot(cluster) && cluster.Status.DesiredImages.Autopilot == ""
}

func hasLighthouseChanged(cluster *corev1alpha1.StorageCluster) bool {
	return autoUpdateLighthouse(cluster) && cluster.Status.DesiredImages.UserInterface == ""
}

func autoUpdateStork(cluster *corev1alpha1.StorageCluster) bool {
	return cluster.Spec.Stork != nil &&
		cluster.Spec.Stork.Enabled &&
		cluster.Spec.Stork.Image == ""
}

func autoUpdateAutopilot(cluster *corev1alpha1.StorageCluster) bool {
	return cluster.Spec.Autopilot != nil &&
		cluster.Spec.Autopilot.Enabled &&
		cluster.Spec.Autopilot.Image == ""
}

func autoUpdateLighthouse(cluster *corev1alpha1.StorageCluster) bool {
	return cluster.Spec.UserInterface != nil &&
		cluster.Spec.UserInterface.Enabled &&
		cluster.Spec.UserInterface.Image == ""
}

func init() {
	if err := storage.Register(pxutil.DriverName, &portworx{}); err != nil {
		logrus.Panicf("Error registering portworx storage driver: %v", err)
	}
}
