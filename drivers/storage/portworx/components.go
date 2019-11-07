package portworx

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metaerrors "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	pxServiceAccountName                   = "portworx"
	pxClusterRoleName                      = "portworx"
	pxClusterRoleBindingName               = "portworx"
	pxRoleName                             = "portworx"
	pxRoleBindingName                      = "portworx"
	pxServiceName                          = "portworx-service"
	pxAPIServiceName                       = "portworx-api"
	pxAPIDaemonSetName                     = "portworx-api"
	pvcServiceAccountName                  = "portworx-pvc-controller"
	pvcClusterRoleName                     = "portworx-pvc-controller"
	pvcClusterRoleBindingName              = "portworx-pvc-controller"
	pvcDeploymentName                      = "portworx-pvc-controller"
	pvcContainerName                       = "portworx-pvc-controller-manager"
	lhServiceAccountName                   = "px-lighthouse"
	lhClusterRoleName                      = "px-lighthouse"
	lhClusterRoleBindingName               = "px-lighthouse"
	lhServiceName                          = "px-lighthouse"
	lhDeploymentName                       = "px-lighthouse"
	lhContainerName                        = "px-lighthouse"
	lhConfigInitContainerName              = "config-init"
	lhConfigSyncContainerName              = "config-sync"
	lhStorkConnectorContainerName          = "stork-connector"
	autopilotConfigMapName                 = "autopilot-config"
	autopilotServiceAccountName            = "autopilot"
	autopilotClusterRoleName               = "autopilot"
	autopilotClusterRoleBindingName        = "autopilot"
	autopilotDeploymentName                = "autopilot"
	autopilotContainerName                 = "autopilot"
	pxServiceMonitor                       = "portworx"
	pxPrometheusRule                       = "portworx"
	csiServiceAccountName                  = "px-csi"
	csiClusterRoleName                     = "px-csi"
	csiClusterRoleBindingName              = "px-csi"
	csiServiceName                         = "px-csi-service"
	csiApplicationName                     = "px-csi-ext"
	csiProvisionerContainerName            = "csi-external-provisioner"
	csiAttacherContainerName               = "csi-attacher"
	csiSnapshotterContainerName            = "csi-snapshotter"
	csiResizerContainerName                = "csi-resizer"
	pxRESTPortName                         = "px-api"
	pxSDKPortName                          = "px-sdk"
	pxKVDBPortName                         = "px-kvdb"
	portworxProvisioner                    = "kubernetes.io/portworx-volume"
	pxDbStorageClass                       = "px-db"
	pxReplicatedStorageClass               = "px-replicated"
	pxDbLocalSnapshotStorageClass          = "px-db-local-snapshot"
	pxDbCloudSnapshotStorageClass          = "px-db-cloud-snapshot"
	pxDbEncryptedStorageClass              = "px-db-encrypted"
	pxReplicatedEncryptedStorageClass      = "px-replicated-encrypted"
	pxDbLocalSnapshotEncryptedStorageClass = "px-db-local-snapshot-encrypted"
	pxDbCloudSnapshotEncryptedStorageClass = "px-db-cloud-snapshot-encrypted"
)

const (
	defaultPVCControllerCPU      = "200m"
	defaultAutopilotCPU          = "0.1"
	defaultLighthouseImageTag    = "2.0.4"
	envKeyPortworxNamespace      = "PX_NAMESPACE"
	envKeyPortworxEnableTLS      = "PX_ENABLE_TLS"
	defaultLhConfigSyncImage     = "portworx/lh-config-sync"
	defaultLhStorkConnectorImage = "portworx/lh-stork-connector"
	envKeyLhConfigSyncImage      = "LIGHTHOUSE_CONFIG_SYNC_IMAGE"
	envKeyLhStorkConnectorImage  = "LIGHTHOUSE_STORK_CONNECTOR_IMAGE"
	portworxSpecsDir             = "/configs"
	pxPrometheusRuleFile         = "portworx-prometheus-rule.yaml"
)

var (
	kbVerRegex            = regexp.MustCompile(`^(v\d+\.\d+\.\d+).*`)
	controllerKind        = corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
	specsBaseDir          = getSpecsBaseDir
	autopilotConfigParams = map[string]bool{
		"min_poll_interval": true,
	}
)

func (p *portworx) installComponents(cluster *corev1alpha1.StorageCluster) error {
	t, err := newTemplate(cluster)
	if err != nil {
		return err
	}

	if err = p.setupPortworxRBAC(t.cluster); err != nil {
		return err
	}
	if err = p.setupPortworxService(t); err != nil {
		return err
	}

	if err = p.setupPortworxAPI(t); err != nil {
		msg := fmt.Sprintf("Failed to setup Portworx API. %v", err)
		p.warningEvent(cluster, util.FailedComponentReason, msg)
	}

	if err = p.createPortworxStorageClasses(t); err != nil {
		msg := fmt.Sprintf("Failed to create default Portworx storage classes. %v", err)
		p.warningEvent(cluster, util.FailedComponentReason, msg)
	}

	if err = p.createCustomResourceDefinitions(); err != nil {
		return err
	}

	if t.needsPVCController {
		if err = p.setupPVCController(t); err != nil {
			msg := fmt.Sprintf("Failed to setup PVC controller. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	} else {
		if err = p.removePVCController(t.cluster); err != nil {
			msg := fmt.Sprintf("Failed to cleanup PVC controller. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	}

	if cluster.Spec.UserInterface != nil && cluster.Spec.UserInterface.Enabled {
		if err = p.setupLighthouse(t); err != nil {
			msg := fmt.Sprintf("Failed to setup Lighthouse. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	} else {
		if err = p.removeLighthouse(t.cluster); err != nil {
			msg := fmt.Sprintf("Failed to cleanup Lighthouse. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	}

	if cluster.Spec.Autopilot != nil && cluster.Spec.Autopilot.Enabled {
		if err = p.setupAutopilot(t); err != nil {
			msg := fmt.Sprintf("Failed to setup Autopilot. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	} else {
		if err = p.removeAutopilot(t.cluster); err != nil {
			msg := fmt.Sprintf("Failed to cleanup Autopilot. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	}

	if cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.EnableMetrics {
		if err = p.setupMonitoring(t); err != nil {
			msg := fmt.Sprintf("Failed to setup monitoring components. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	} else {
		if err = p.removeMonitoring(t.cluster); err != nil {
			msg := fmt.Sprintf("Failed to cleanup monitoring components. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	}

	if FeatureCSI.isEnabled(cluster.Spec.FeatureGates) {
		if err = p.setupCSI(t); err != nil {
			msg := fmt.Sprintf("Failed to setup CSI components. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	} else {
		if err = p.removeCSI(t); err != nil {
			msg := fmt.Sprintf("Failed to cleanup CSI components. %v", err)
			p.warningEvent(cluster, util.FailedComponentReason, msg)
		}
	}

	return nil
}

func (p *portworx) setupPortworxRBAC(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := p.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createClusterRole(ownerRef); err != nil {
		return err
	}
	if err := p.createClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createRole(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupPortworxService(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	return p.createPortworxService(t, ownerRef)
}

func (p *portworx) setupPortworxAPI(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createPortworxAPIService(t, ownerRef); err != nil {
		return err
	}
	if !p.pxAPIDaemonSetCreated {
		if err := p.createPortworxAPIDaemonSet(t, ownerRef); err != nil {
			return err
		}
		p.pxAPIDaemonSetCreated = true
	}
	return nil
}

func (p *portworx) setupPVCController(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createPVCControllerServiceAccount(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createPVCControllerClusterRole(ownerRef); err != nil {
		return err
	}
	if err := p.createPVCControllerClusterRoleBinding(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createPVCControllerDeployment(t, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupLighthouse(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createLighthouseServiceAccount(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseClusterRole(ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseClusterRoleBinding(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseService(t, ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseDeployment(t, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupAutopilot(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createAutopilotConfigMap(t, ownerRef); err != nil {
		return err
	}
	if err := p.createAutopilotServiceAccount(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createAutopilotClusterRole(ownerRef); err != nil {
		return err
	}
	if err := p.createAutopilotClusterRoleBinding(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createAutopilotDeployment(t, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupMonitoring(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createServiceMonitor(t.cluster, ownerRef); metaerrors.IsNoMatchError(err) {
		p.warningEvent(t.cluster, util.FailedComponentReason,
			fmt.Sprintf("Failed to create ServiceMonitor for Portworx. Ensure Prometheus is deployed correctly. %v", err))
		return nil
	} else if err != nil {
		return err
	}
	if err := p.createPrometheusRule(t.cluster, ownerRef); metaerrors.IsNoMatchError(err) {
		p.warningEvent(t.cluster, util.FailedComponentReason,
			fmt.Sprintf("Failed to create PrometheusRule for Portworx. Ensure Prometheus is deployed correctly. %v", err))
		return nil
	} else if err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupCSI(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createCSIServiceAccount(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createCSIClusterRole(t, ownerRef); err != nil {
		return err
	}
	if err := p.createCSIClusterRoleBinding(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createCSIService(t, ownerRef); err != nil {
		return err
	}
	if t.csiVersions.includeCsiDriverInfo {
		if err := p.createCSIDriver(t, ownerRef); err != nil {
			return err
		}
	}
	if t.csiVersions.useDeployment {
		if err := k8sutil.DeleteStatefulSet(p.k8sClient, csiApplicationName, t.cluster.Namespace, *ownerRef); err != nil {
			return err
		}
		if err := p.createCSIDeployment(t, ownerRef); err != nil {
			return err
		}
	} else {
		if err := k8sutil.DeleteDeployment(p.k8sClient, csiApplicationName, t.cluster.Namespace, *ownerRef); err != nil {
			return err
		}
		if err := p.createCSIStatefulSet(t, ownerRef); err != nil {
			return err
		}
	}
	if t.csiVersions.createCsiNodeCrd && !p.csiNodeInfoCRDCreated {
		if err := createCSINodeInfoCRD(); err != nil {
			return err
		}
		p.csiNodeInfoCRDCreated = true
	}
	return nil
}

func (p *portworx) createCustomResourceDefinitions() error {
	if !p.volumePlacementStrategyCRDCreated {
		if err := createVolumePlacementStrategyCRD(); err != nil {
			return err
		}
		p.volumePlacementStrategyCRDCreated = true
	}
	return nil
}

func (p *portworx) removePVCController(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	// We don't delete the service account for PVC controller because it is part of CSV. If
	// we disable PVC controller then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(p.k8sClient, pvcClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, pvcClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, pvcDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	p.pvcControllerDeploymentCreated = false
	return nil
}

func (p *portworx) removeLighthouse(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	// We don't delete the service account for Lighthouse because it is part of CSV. If
	// we disable Lighthouse then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(p.k8sClient, lhClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, lhClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(p.k8sClient, lhServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, lhDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	p.lhDeploymentCreated = false
	return nil
}

func (p *portworx) removeAutopilot(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := k8sutil.DeleteConfigMap(p.k8sClient, autopilotConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(p.k8sClient, autopilotServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(p.k8sClient, autopilotClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, autopilotClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, autopilotDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	p.autopilotDeploymentCreated = false
	return nil
}

func (p *portworx) removeMonitoring(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	err := k8sutil.DeleteServiceMonitor(p.k8sClient, pxServiceMonitor, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	err = k8sutil.DeletePrometheusRule(p.k8sClient, pxPrometheusRule, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	return nil
}

func (p *portworx) removeCSI(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	// We don't delete the service account for CSI because it is part of CSV. If
	// we disable CSI then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(p.k8sClient, csiClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, csiClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(p.k8sClient, csiServiceName, t.cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteStatefulSet(p.k8sClient, csiApplicationName, t.cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, csiApplicationName, t.cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	p.csiApplicationCreated = false
	if t.csiVersions.includeCsiDriverInfo {
		if err := k8sutil.DeleteCSIDriver(p.k8sClient, t.csiVersions.driverName, *ownerRef); err != nil {
			return err
		}
	}
	return nil
}

func (p *portworx) unsetInstallParams() {
	p.pxAPIDaemonSetCreated = false
	p.volumePlacementStrategyCRDCreated = false
	p.pvcControllerDeploymentCreated = false
	p.lhDeploymentCreated = false
	p.autopilotDeploymentCreated = false
	p.csiApplicationCreated = false
	p.csiNodeInfoCRDCreated = false
}

func createVolumePlacementStrategyCRD() error {
	logrus.Debugf("Creating VolumePlacementStrategy CRD")

	resource := k8s.CustomResource{
		Plural: "volumeplacementstrategies",
		Group:  "portworx.io",
	}

	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", resource.Plural, resource.Group),
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group: resource.Group,
			Versions: []apiextensionsv1beta1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta2",
					Served:  true,
					Storage: true,
				},
				{
					Name:    "v1beta1",
					Served:  false,
					Storage: false,
				},
			},
			Scope: apiextensionsv1beta1.ClusterScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Singular:   "volumeplacementstrategy",
				Plural:     resource.Plural,
				Kind:       "VolumePlacementStrategy",
				ShortNames: []string{"vps", "vp"},
			},
		},
	}

	err := k8s.Instance().RegisterCRD(crd)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return k8s.Instance().ValidateCRD(resource, 1*time.Minute, 5*time.Second)
}

func (p *portworx) createPortworxStorageClasses(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	docAnnotations := map[string]string{
		"params/docs":              "https://docs.portworx.com/scheduler/kubernetes/dynamic-provisioning.html",
		"params/fs":                "Filesystem to be laid out: none|xfs|ext4",
		"params/block_size":        "Block size",
		"params/repl":              "Replication factor for the volume: 1|2|3",
		"params/secure":            "Flag to create an encrypted volume: true|false",
		"params/shared":            "Flag to create a globally shared namespace volume which can be used by multiple pods: true|false",
		"params/priority_io":       "IO Priority: low|medium|high",
		"params/io_profile":        "IO Profile can be used to override the I/O algorithm Portworx uses for the volumes: db|sequential|random|cms",
		"params/aggregation_level": "Specifies the number of replication sets the volume can be aggregated from",
		"params/sticky":            "Flag to create sticky volumes that cannot be deleted until the flag is disabled",
		"params/journal":           "Flag to indicate if you want to use journal device for the volume's metadata. This will use the journal device that you used when installing Portworx. It is recommended to use a journal device to absorb PX metadata writes",
	}

	storageClasses := []*storagev1.StorageClass{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxDbStorageClass,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
				Annotations:     docAnnotations,
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel:   "3",
				api.SpecIoProfile: "db",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxDbEncryptedStorageClass,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
				Annotations: map[string]string{
					"params/note": "Ensure that you have a cluster-wide secret created in the configured secrets provider",
				},
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel:   "3",
				api.SpecIoProfile: "db",
				api.SpecSecure:    "true",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxReplicatedStorageClass,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel: "2",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxReplicatedEncryptedStorageClass,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel: "2",
				api.SpecSecure:  "true",
			},
		},
	}

	if t.cluster.Spec.Stork != nil && t.cluster.Spec.Stork.Enabled {
		storageClasses = append(storageClasses,
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pxDbLocalSnapshotStorageClass,
					OwnerReferences: []metav1.OwnerReference{*ownerRef},
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
				},
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pxDbLocalSnapshotEncryptedStorageClass,
					OwnerReferences: []metav1.OwnerReference{*ownerRef},
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					api.SpecSecure:  "true",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
				},
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pxDbCloudSnapshotStorageClass,
					OwnerReferences: []metav1.OwnerReference{*ownerRef},
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
				},
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pxDbCloudSnapshotEncryptedStorageClass,
					OwnerReferences: []metav1.OwnerReference{*ownerRef},
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					api.SpecSecure:  "true",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
				},
			},
		)
	}

	for _, sc := range storageClasses {
		if err := k8sutil.CreateStorageClass(p.k8sClient, sc); err != nil {
			return err
		}
	}
	return nil
}

func createCSINodeInfoCRD() error {
	logrus.Debugf("Creating CSINodeInfo CRD")

	resource := k8s.CustomResource{
		Plural: "csinodeinfos",
		Group:  "csi.storage.k8s.io",
	}

	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", resource.Plural, resource.Group),
			Labels: map[string]string{
				"addonmanager.kubernetes.io/mode": "Reconcile",
			},
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   resource.Group,
			Version: "v1alpha1",
			Scope:   apiextensionsv1beta1.ClusterScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural: resource.Plural,
				Kind:   "CSINodeInfo",
			},
			Validation: &apiextensionsv1beta1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1beta1.JSONSchemaProps{
					Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
						"spec": {
							Description: "Specification of CSINodeInfo",
							Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
								"drivers": {
									Description: "List of CSI drivers running on the node and their specs.",
									Type:        "array",
									Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
										Schema: &apiextensionsv1beta1.JSONSchemaProps{
											Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
												"name": {
													Description: "The CSI driver that this object refers to.",
													Type:        "string",
												},
												"nodeID": {
													Description: "The node from the driver point of view.",
													Type:        "string",
												},
												"topologyKeys": {
													Description: "List of keys supported by the driver.",
													Type:        "array",
													Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
														Schema: &apiextensionsv1beta1.JSONSchemaProps{
															Type: "string",
														},
													},
												},
											},
										},
									},
								},
							},
						},
						"status": {
							Description: "Status of CSINodeInfo",
							Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
								"drivers": {
									Description: "List of CSI drivers running on the node and their statuses.",
									Type:        "array",
									Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
										Schema: &apiextensionsv1beta1.JSONSchemaProps{
											Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
												"name": {
													Description: "The CSI driver that this object refers to.",
													Type:        "string",
												},
												"available": {
													Description: "Whether the CSI driver is installed.",
													Type:        "boolean",
												},
												"volumePluginMechanism": {
													Description: "Indicates to external components the required mechanism " +
														"to use for any in-tree plugins replaced by this driver.",
													Type:    "string",
													Pattern: "in-tree|csi",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := k8s.Instance().RegisterCRD(crd)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return k8s.Instance().ValidateCRD(resource, 1*time.Minute, 5*time.Second)
}

func (p *portworx) createCSIDriver(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateCSIDriver(
		p.k8sClient,
		&storagev1beta1.CSIDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name:            t.csiVersions.driverName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: storagev1beta1.CSIDriverSpec{
				AttachRequired: boolPtr(false),
				PodInfoOnMount: boolPtr(false),
			},
		},
		ownerRef,
	)
}

func (p *portworx) createAutopilotConfigMap(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	config := "providers:"
	for _, provider := range t.cluster.Spec.Autopilot.Providers {
		keys := make([]string, 0, len(provider.Params))
		for k := range provider.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var params string
		for _, k := range keys {
			params += fmt.Sprintf("%s=%s,", k, provider.Params[k])
		}
		params = strings.TrimRight(params, ",")
		config += fmt.Sprintf(`
- name: %s
  type: %s
  params: %s`,
			provider.Name, provider.Type, params)
	}

	for key, value := range t.cluster.Spec.Autopilot.Args {
		if _, exists := autopilotConfigParams[key]; exists {
			config += fmt.Sprintf("\n%s: %s", key, value)
		}
	}

	return k8sutil.CreateOrUpdateConfigMap(
		p.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            autopilotConfigMapName,
				Namespace:       t.cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				"config.yaml": config,
			},
		},
		ownerRef,
	)
}

func (p *portworx) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createLighthouseServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createAutopilotServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            autopilotServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createCSIServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            csiServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createRole(clusterNamespace string, ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateRole(
		p.k8sClient,
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxRoleName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list", "create", "update", "patch"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRoleBinding(
		p.k8sClient,
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxRoleBindingName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pxServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "Role",
				Name:     pxRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list", "watch", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims", "persistentvolumes"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "list", "create", "update"},
				},
				{
					APIGroups:     []string{"extensions"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{"privileged"},
					Verbs:         []string{"use"},
				},
				{
					APIGroups: []string{"portworx.io"},
					Resources: []string{"volumeplacementstrategies"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"stork.libopenstorage.org"},
					Resources: []string{"backuplocations"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"events"},
					Verbs:     []string{"create"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes"},
					Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims"},
					Verbs:     []string{"get", "list", "watch", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch", "create", "delete"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"endpoints", "services"},
					Verbs:     []string{"get", "create", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"events"},
					Verbs:     []string{"watch", "create", "update", "patch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"serviceaccounts"},
					Verbs:     []string{"get", "create"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "create", "update"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createLighthouseClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"extensions", "apps"},
					Resources: []string{"deployments"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "create", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "create", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"services"},
					Verbs:     []string{"get", "list", "watch", "create"},
				},
				{
					APIGroups: []string{"stork.libopenstorage.org"},
					Resources: []string{"*"},
					Verbs:     []string{"get", "list", "create", "delete", "update"},
				},
				{
					APIGroups: []string{"monitoring.coreos.com"},
					Resources: []string{
						"alertmanagers",
						"prometheuses",
						"prometheuses/finalizers",
						"servicemonitors",
						"prometheusrules",
					},
					Verbs: []string{"*"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createAutopilotClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            autopilotClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"*"},
					Resources: []string{"*"},
					Verbs:     []string{"*"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createCSIClusterRole(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:            csiClusterRoleName,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"extensions"},
				Resources:     []string{"podsecuritypolicies"},
				ResourceNames: []string{"privileged"},
				Verbs:         []string{"use"},
			},
			{
				APIGroups: []string{"apiextensions.k8s.io"},
				Resources: []string{"customresourcedefinitions"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumes"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims/status"},
				Verbs:     []string{"update", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"volumeattachments"},
				Verbs:     []string{"get", "list", "watch", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"list", "watch", "create", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{"snapshot.storage.k8s.io"},
				Resources: []string{
					"volumesnapshots",
					"volumesnapshotcontents",
					"volumesnapshotclasses",
					"volumesnapshots/status",
				},
				Verbs: []string{"get", "list", "watch", "create", "delete", "update"},
			},
			{
				APIGroups: []string{"csi.storage.k8s.io"},
				Resources: []string{"csidrivers"},
				Verbs:     []string{"create", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"endpoints"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"*"},
			},
		},
	}

	k8sVer1_14, err := version.NewVersion("1.14")
	if err != nil {
		return err
	}

	if t.csiVersions.createCsiNodeCrd {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{"csi.storage.k8s.io"},
				Resources: []string{"csinodeinfos"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
		)
	} else if t.k8sVersion.GreaterThan(k8sVer1_14) || t.k8sVersion.Equal(k8sVer1_14) {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"csinodes"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
		)
	}

	if t.csiVersions.includeEndpointsAndConfigMapsForLeases {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
			},
		)
	}
	return k8sutil.CreateOrUpdateClusterRole(p.k8sClient, clusterRole, ownerRef)
}

func (p *portworx) createClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pxServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     pxClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pvcServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     pvcClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createLighthouseClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      lhServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     lhClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createAutopilotClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            autopilotClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      autopilotServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     autopilotClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createCSIClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            csiClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      csiServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     csiClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPortworxService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	labels := p.GetSelectorLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxServiceName,
			Namespace:       t.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Name:       pxRESTPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9001),
					TargetPort: intstr.FromInt(t.startPort),
				},
				{
					Name:       pxKVDBPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9019),
					TargetPort: intstr.FromInt(t.startPort + 18),
				},
				{
					Name:       pxSDKPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9020),
					TargetPort: intstr.FromInt(t.startPort + 19),
				},
				{
					Name:       "px-rest-gateway",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9021),
					TargetPort: intstr.FromInt(t.startPort + 20),
				},
			},
		},
	}

	if t.serviceType != "" {
		newService.Spec.Type = t.serviceType
	}

	return k8sutil.CreateOrUpdateService(p.k8sClient, newService, ownerRef)
}

func (p *portworx) createPortworxAPIService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getPortworxAPIServiceLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxAPIServiceName,
			Namespace:       t.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Name:       pxRESTPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9001),
					TargetPort: intstr.FromInt(t.startPort),
				},
				{
					Name:       pxSDKPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9020),
					TargetPort: intstr.FromInt(t.startPort + 19),
				},
				{
					Name:       "px-rest-gateway",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9021),
					TargetPort: intstr.FromInt(t.startPort + 20),
				},
			},
		},
	}

	if t.serviceType != "" {
		newService.Spec.Type = t.serviceType
	}

	return k8sutil.CreateOrUpdateService(p.k8sClient, newService, ownerRef)
}

func (p *portworx) createLighthouseService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getLighthouseLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            lhServiceName,
			Namespace:       t.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       int32(80),
					TargetPort: intstr.FromInt(80),
				},
				{
					Name:       "https",
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}

	if t.serviceType != "" {
		newService.Spec.Type = t.serviceType
	} else if !t.isAKS && !t.isGKE && !t.isEKS {
		newService.Spec.Type = v1.ServiceTypeNodePort
	}

	return k8sutil.CreateOrUpdateService(p.k8sClient, newService, ownerRef)
}

func (p *portworx) createCSIService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateService(
		p.k8sClient,
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            csiServiceName,
				Namespace:       t.cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: v1.ServiceSpec{
				ClusterIP: "None",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerDeployment(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	targetCPU := defaultPVCControllerCPU
	if cpuStr, ok := t.cluster.Annotations[annotationPVCControllerCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	imageName := util.GetImageURN(t.cluster.Spec.CustomImageRegistry,
		"gcr.io/google_containers/kube-controller-manager-amd64:v"+t.k8sVersion.String())

	command := []string{
		"kube-controller-manager",
		"--leader-elect=true",
		"--address=0.0.0.0",
		"--controllers=persistentvolume-binder,persistentvolume-expander",
		"--use-service-account-credentials=true",
	}
	if t.isOpenshift {
		command = append(command, "--leader-elect-resource-lock=endpoints")
	} else {
		command = append(command, "--leader-elect-resource-lock=configmaps")
	}

	existingDeployment := &appsv1.Deployment{}
	err = p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      pvcDeploymentName,
			Namespace: t.cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var existingImage string
	var existingCommand []string
	var existingCPUQuantity resource.Quantity
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == pvcContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingCPUQuantity = c.Resources.Requests[v1.ResourceCPU]
		}
	}

	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0

	if !p.pvcControllerDeploymentCreated || modified {
		deployment := getPVCControllerDeploymentSpec(t, ownerRef, imageName, command, targetCPUQuantity)
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	p.pvcControllerDeploymentCreated = true
	return nil
}

func getPVCControllerDeploymentSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	cpuQuantity resource.Quantity,
) *appsv1.Deployment {
	replicas := int32(3)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	labels := map[string]string{
		"name": pvcDeploymentName,
		"tier": "control-plane",
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pvcDeploymentName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels: map[string]string{
				"tier": "control-plane",
			},
			Annotations: map[string]string{
				"scheduler.alpha.kubernetes.io/critical-pod": "",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"scheduler.alpha.kubernetes.io/critical-pod": "",
					},
				},
				Spec: v1.PodSpec{
					ServiceAccountName: pvcServiceAccountName,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:    pvcContainerName,
							Image:   imageName,
							Command: command,
							LivenessProbe: &v1.Probe{
								FailureThreshold:    8,
								TimeoutSeconds:      15,
								InitialDelaySeconds: 15,
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Host:   "127.0.0.1",
										Path:   "/healthz",
										Port:   intstr.FromInt(10252),
										Scheme: v1.URISchemeHTTP,
									},
								},
							},
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuQuantity,
								},
							},
						},
					},
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "name",
												Operator: metav1.LabelSelectorOpIn,
												Values: []string{
													pvcDeploymentName,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (p *portworx) createLighthouseDeployment(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	if t.cluster.Spec.UserInterface.Image == "" {
		return fmt.Errorf("lighthouse image cannot be empty")
	}

	existingDeployment := &appsv1.Deployment{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      lhDeploymentName,
			Namespace: t.cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	existingLhImage := getImageFromDeployment(existingDeployment, lhContainerName)
	existingConfigInitImage := getImageFromDeployment(existingDeployment, lhConfigInitContainerName)
	existingConfigSyncImage := getImageFromDeployment(existingDeployment, lhConfigSyncContainerName)
	existingStorkConnectorImage := getImageFromDeployment(existingDeployment, lhStorkConnectorContainerName)

	imageTag := defaultLighthouseImageTag
	partitions := strings.Split(t.cluster.Spec.UserInterface.Image, ":")
	if len(partitions) > 1 {
		imageTag = partitions[len(partitions)-1]
	}

	configSyncImage := getImageFromEnv(envKeyLhConfigSyncImage, t.cluster.Spec.UserInterface.Env)
	if len(configSyncImage) == 0 {
		configSyncImage = fmt.Sprintf("%s:%s", defaultLhConfigSyncImage, imageTag)
	}
	storkConnectorImage := getImageFromEnv(envKeyLhStorkConnectorImage, t.cluster.Spec.UserInterface.Env)
	if len(storkConnectorImage) == 0 {
		storkConnectorImage = fmt.Sprintf("%s:%s", defaultLhStorkConnectorImage, imageTag)
	}

	imageRegistry := t.cluster.Spec.CustomImageRegistry
	lhImage := util.GetImageURN(imageRegistry, t.cluster.Spec.UserInterface.Image)
	configSyncImage = util.GetImageURN(imageRegistry, configSyncImage)
	storkConnectorImage = util.GetImageURN(imageRegistry, storkConnectorImage)

	modified := lhImage != existingLhImage ||
		configSyncImage != existingConfigInitImage ||
		configSyncImage != existingConfigSyncImage ||
		storkConnectorImage != existingStorkConnectorImage

	if !p.lhDeploymentCreated || modified {
		deployment := getLighthouseDeploymentSpec(t, ownerRef, lhImage, configSyncImage, storkConnectorImage)
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	p.lhDeploymentCreated = true
	return nil
}

func getLighthouseDeploymentSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	lhImageName string,
	configSyncImageName string,
	storkConnectorImageName string,
) *appsv1.Deployment {
	labels := getLighthouseLabels()
	replicas := int32(1)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            lhDeploymentName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: lhServiceAccountName,
					InitContainers: []v1.Container{
						{
							Name:            lhConfigInitContainerName,
							Image:           configSyncImageName,
							ImagePullPolicy: t.imagePullPolicy,
							Args:            []string{"init"},
							Env: []v1.EnvVar{
								{
									Name:  envKeyPortworxNamespace,
									Value: t.cluster.Namespace,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:            lhContainerName,
							Image:           lhImageName,
							ImagePullPolicy: t.imagePullPolicy,
							Args:            []string{"-kubernetes", "true"},
							Ports: []v1.ContainerPort{
								{
									ContainerPort: int32(80),
								},
								{
									ContainerPort: int32(443),
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
						{
							Name:            lhConfigSyncContainerName,
							Image:           configSyncImageName,
							ImagePullPolicy: t.imagePullPolicy,
							Args:            []string{"sync"},
							Env: []v1.EnvVar{
								{
									Name:  envKeyPortworxNamespace,
									Value: t.cluster.Namespace,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
						{
							Name:            lhStorkConnectorContainerName,
							Image:           storkConnectorImageName,
							ImagePullPolicy: t.imagePullPolicy,
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "config",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
}

func (p *portworx) createAutopilotDeployment(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	if t.cluster.Spec.Autopilot.Image == "" {
		return fmt.Errorf("autopilot image cannot be empty")
	}

	existingDeployment := &appsv1.Deployment{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      autopilotDeploymentName,
			Namespace: t.cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	targetCPU := defaultAutopilotCPU
	if cpuStr, ok := t.cluster.Annotations[annotationAutopilotCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	args := map[string]string{
		"config":    "/etc/config/config.yaml",
		"log-level": "debug",
	}
	for k, v := range t.cluster.Spec.Autopilot.Args {
		if _, exists := autopilotConfigParams[k]; exists {
			continue
		}
		key := strings.TrimLeft(k, "-")
		if len(key) > 0 && len(v) > 0 {
			args[key] = v
		}
	}

	argList := make([]string, 0)
	for k, v := range args {
		argList = append(argList, fmt.Sprintf("--%s=%s", k, v))
	}
	sort.Strings(argList)
	command := append([]string{"/autopilot"}, argList...)

	imageName := util.GetImageURN(
		t.cluster.Spec.CustomImageRegistry,
		t.cluster.Spec.Autopilot.Image,
	)

	envVars := make([]v1.EnvVar, 0)
	for _, env := range t.cluster.Spec.Autopilot.Env {
		envCopy := env.DeepCopy()
		envVars = append(envVars, *envCopy)
	}
	sort.Sort(envByName(envVars))

	var existingImage string
	var existingCommand []string
	var existingEnvs []v1.EnvVar
	var existingCPUQuantity resource.Quantity
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == autopilotContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingEnvs = append([]v1.EnvVar{}, c.Env...)
			sort.Sort(envByName(existingEnvs))
			existingCPUQuantity = c.Resources.Requests[v1.ResourceCPU]
			break
		}
	}

	// Check if image, envs, cpu or args are modified
	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		!reflect.DeepEqual(existingEnvs, envVars) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0

	if !p.autopilotDeploymentCreated || modified {
		deployment := p.getAutopilotDeploymentSpec(t, ownerRef, imageName,
			command, envVars, targetCPUQuantity)
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	p.autopilotDeploymentCreated = true
	return nil
}

func (p *portworx) getAutopilotDeploymentSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	envVars []v1.EnvVar,
	cpuQuantity resource.Quantity,
) *appsv1.Deployment {
	deploymentLabels := map[string]string{
		"tier": "control-plane",
	}
	templateLabels := map[string]string{
		"name": "autopilot",
		"tier": "control-plane",
	}

	replicas := int32(1)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotDeploymentName,
			Namespace: t.cluster.Namespace,
			Annotations: map[string]string{
				"scheduler.alpha.kubernetes.io/critical-pod": "",
			},
			Labels:          deploymentLabels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: templateLabels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"scheduler.alpha.kubernetes.io/critical-pod": "",
					},
					Labels: templateLabels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: autopilotServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            autopilotContainerName,
							Image:           imageName,
							ImagePullPolicy: t.imagePullPolicy,
							Command:         command,
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuQuantity,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config-volume",
									MountPath: "/etc/config",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "config-volume",
							VolumeSource: v1.VolumeSource{
								ConfigMap: &v1.ConfigMapVolumeSource{
									LocalObjectReference: v1.LocalObjectReference{
										Name: autopilotConfigMapName,
									},
									Items: []v1.KeyToPath{
										{
											Key:  "config.yaml",
											Path: "config.yaml",
										},
									},
								},
							},
						},
					},
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "name",
												Operator: metav1.LabelSelectorOpIn,
												Values: []string{
													autopilotDeploymentName,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if t.cluster.Spec.ImagePullSecret != nil && *t.cluster.Spec.ImagePullSecret != "" {
		deployment.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *t.cluster.Spec.ImagePullSecret,
			},
		)
	}

	if len(envVars) > 0 {
		deployment.Spec.Template.Spec.Containers[0].Env = envVars
	}

	return deployment
}

func (p *portworx) createCSIDeployment(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	existingDeployment := &appsv1.Deployment{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      csiApplicationName,
			Namespace: t.cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var (
		existingProvisionerImage = getImageFromDeployment(existingDeployment, csiProvisionerContainerName)
		existingAttacherImage    = getImageFromDeployment(existingDeployment, csiAttacherContainerName)
		existingSnapshotterImage = getImageFromDeployment(existingDeployment, csiSnapshotterContainerName)
		existingResizerImage     = getImageFromDeployment(existingDeployment, csiResizerContainerName)
		provisionerImage         string
		attacherImage            string
		snapshotterImage         string
		resizerImage             string
	)

	provisionerImage = util.GetImageURN(
		t.cluster.Spec.CustomImageRegistry,
		t.csiVersions.provisioner,
	)
	if t.csiVersions.includeAttacher && t.csiVersions.attacher != "" {
		attacherImage = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			t.csiVersions.attacher,
		)
	}
	if t.csiVersions.snapshotter != "" {
		snapshotterImage = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			t.csiVersions.snapshotter,
		)
	}
	if t.csiVersions.includeResizer && t.csiVersions.resizer != "" {
		resizerImage = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			t.csiVersions.resizer,
		)
	}

	if !p.csiApplicationCreated ||
		provisionerImage != existingProvisionerImage ||
		attacherImage != existingAttacherImage ||
		snapshotterImage != existingSnapshotterImage ||
		resizerImage != existingResizerImage {
		deployment := getCSIDeploymentSpec(t, ownerRef,
			provisionerImage, attacherImage, snapshotterImage, resizerImage)
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	p.csiApplicationCreated = true
	return nil
}

func getCSIDeploymentSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	provisionerImage, attacherImage string,
	snapshotterImage, resizerImage string,
) *appsv1.Deployment {
	replicas := int32(3)
	labels := map[string]string{
		"app": "px-csi-driver",
	}

	leaderElectionType := "leases"
	provisionerLeaderElectionType := "leases"
	if t.csiVersions.includeEndpointsAndConfigMapsForLeases {
		leaderElectionType = "configmaps"
		provisionerLeaderElectionType = "endpoints"
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            csiApplicationName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: csiServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            csiProvisionerContainerName,
							Image:           provisionerImage,
							ImagePullPolicy: t.imagePullPolicy,
							Args: []string{
								"--v=3",
								"--provisioner=" + t.csiVersions.driverName,
								"--csi-address=$(ADDRESS)",
								"--enable-leader-election",
								"--leader-election-type=" + provisionerLeaderElectionType,
							},
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							SecurityContext: &v1.SecurityContext{
								Privileged: boolPtr(true),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "socket-dir",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: t.csiBasePath(),
									Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
								},
							},
						},
					},
				},
			},
		},
	}

	if t.csiVersions.includeAttacher && attacherImage != "" {
		deployment.Spec.Template.Spec.Containers = append(
			deployment.Spec.Template.Spec.Containers,
			v1.Container{
				Name:            csiAttacherContainerName,
				Image:           attacherImage,
				ImagePullPolicy: t.imagePullPolicy,
				Args: []string{
					"--v=3",
					"--csi-address=$(ADDRESS)",
					"--leader-election=true",
					"--leader-election-type=" + leaderElectionType,
				},
				Env: []v1.EnvVar{
					{
						Name:  "ADDRESS",
						Value: "/csi/csi.sock",
					},
				},
				SecurityContext: &v1.SecurityContext{
					Privileged: boolPtr(true),
				},
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "socket-dir",
						MountPath: "/csi",
					},
				},
			},
		)
	}

	if snapshotterImage != "" {
		deployment.Spec.Template.Spec.Containers = append(
			deployment.Spec.Template.Spec.Containers,
			v1.Container{
				Name:            csiSnapshotterContainerName,
				Image:           snapshotterImage,
				ImagePullPolicy: t.imagePullPolicy,
				Args: []string{
					"--v=3",
					"--csi-address=$(ADDRESS)",
					"--snapshotter=" + t.csiVersions.driverName,
					"--leader-election=true",
					"--leader-election-type=" + leaderElectionType,
				},
				Env: []v1.EnvVar{
					{
						Name:  "ADDRESS",
						Value: "/csi/csi.sock",
					},
				},
				SecurityContext: &v1.SecurityContext{
					Privileged: boolPtr(true),
				},
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "socket-dir",
						MountPath: "/csi",
					},
				},
			},
		)
	}

	if t.csiVersions.includeResizer && resizerImage != "" {
		deployment.Spec.Template.Spec.Containers = append(
			deployment.Spec.Template.Spec.Containers,
			v1.Container{
				Name:            csiResizerContainerName,
				Image:           resizerImage,
				ImagePullPolicy: t.imagePullPolicy,
				Args: []string{
					"--v=3",
					"--csi-address=$(ADDRESS)",
					"--leader-election=true",
				},
				Env: []v1.EnvVar{
					{
						Name:  "ADDRESS",
						Value: "/csi/csi.sock",
					},
				},
				SecurityContext: &v1.SecurityContext{
					Privileged: boolPtr(true),
				},
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "socket-dir",
						MountPath: "/csi",
					},
				},
			},
		)
	}

	if t.cluster.Spec.Placement != nil && t.cluster.Spec.Placement.NodeAffinity != nil {
		deployment.Spec.Template.Spec.Affinity = &v1.Affinity{
			NodeAffinity: t.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	return deployment
}

func (p *portworx) createCSIStatefulSet(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	existingSS := &appsv1.StatefulSet{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      csiApplicationName,
			Namespace: t.cluster.Namespace,
		},
		existingSS,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var (
		existingProvisionerImage = getImageFromStatefulSet(existingSS, csiProvisionerContainerName)
		existingAttacherImage    = getImageFromStatefulSet(existingSS, csiAttacherContainerName)
		provisionerImage         string
		attacherImage            string
	)

	provisionerImage = util.GetImageURN(
		t.cluster.Spec.CustomImageRegistry,
		t.csiVersions.provisioner,
	)
	attacherImage = util.GetImageURN(
		t.cluster.Spec.CustomImageRegistry,
		t.csiVersions.attacher,
	)

	if !p.csiApplicationCreated ||
		provisionerImage != existingProvisionerImage ||
		attacherImage != existingAttacherImage {
		statefulSet := getCSIStatefulSetSpec(t, ownerRef, provisionerImage, attacherImage)
		if err = k8sutil.CreateOrUpdateStatefulSet(p.k8sClient, statefulSet, ownerRef); err != nil {
			return err
		}
	}
	p.csiApplicationCreated = true
	return nil
}

func getCSIStatefulSetSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	provisionerImage, attacherImage string,
) *appsv1.StatefulSet {
	replicas := int32(1)
	labels := map[string]string{
		"app": "px-csi-driver",
	}

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            csiApplicationName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: csiServiceName,
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: csiServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            csiProvisionerContainerName,
							Image:           provisionerImage,
							ImagePullPolicy: t.imagePullPolicy,
							Args: []string{
								"--v=3",
								"--provisioner=" + t.csiVersions.driverName,
								"--csi-address=$(ADDRESS)",
							},
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							SecurityContext: &v1.SecurityContext{
								Privileged: boolPtr(true),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
						},
						{
							Name:            csiAttacherContainerName,
							Image:           attacherImage,
							ImagePullPolicy: t.imagePullPolicy,
							Args: []string{
								"--v=3",
								"--csi-address=$(ADDRESS)",
							},
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							SecurityContext: &v1.SecurityContext{
								Privileged: boolPtr(true),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "socket-dir",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: t.csiBasePath(),
									Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
								},
							},
						},
					},
				},
			},
		},
	}

	if t.cluster.Spec.Placement != nil && t.cluster.Spec.Placement.NodeAffinity != nil {
		statefulSet.Spec.Template.Spec.Affinity = &v1.Affinity{
			NodeAffinity: t.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	return statefulSet
}

func (p *portworx) createPortworxAPIDaemonSet(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	imageName := util.GetImageURN(t.cluster.Spec.CustomImageRegistry, "k8s.gcr.io/pause:3.1")

	maxUnavailable := intstr.FromString("100%")
	newDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxAPIDaemonSetName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: getPortworxAPIServiceLabels(),
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: getPortworxAPIServiceLabels(),
				},
				Spec: v1.PodSpec{
					ServiceAccountName: pxServiceAccountName,
					RestartPolicy:      v1.RestartPolicyAlways,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:            "portworx-api",
							Image:           imageName,
							ImagePullPolicy: v1.PullAlways,
							ReadinessProbe: &v1.Probe{
								PeriodSeconds: int32(10),
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Host: "127.0.0.1",
										Path: "/status",
										Port: intstr.FromInt(t.startPort),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if t.cluster.Spec.Placement != nil && t.cluster.Spec.Placement.NodeAffinity != nil {
		newDaemonSet.Spec.Template.Spec.Affinity = &v1.Affinity{
			NodeAffinity: t.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	return k8sutil.CreateOrUpdateDaemonSet(p.k8sClient, newDaemonSet, ownerRef)
}

func (p *portworx) createServiceMonitor(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	svcMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceMonitor,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				"name": pxServiceMonitor,
			},
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Endpoints: []monitoringv1.Endpoint{
				{
					Port: pxRESTPortName,
				},
			},
			NamespaceSelector: monitoringv1.NamespaceSelector{
				MatchNames: []string{cluster.Namespace},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: p.GetSelectorLabels(),
			},
		},
	}

	// In case kvdb spec is nil, we will default to internal kvdb
	if cluster.Spec.Kvdb == nil || cluster.Spec.Kvdb.Internal {
		svcMonitor.Spec.Endpoints = append(
			svcMonitor.Spec.Endpoints,
			monitoringv1.Endpoint{Port: pxKVDBPortName},
		)
	}

	return k8sutil.CreateOrUpdateServiceMonitor(p.k8sClient, svcMonitor, ownerRef)
}

func (p *portworx) createPrometheusRule(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	filename := path.Join(specsBaseDir(), pxPrometheusRuleFile)
	prometheusRule := &monitoringv1.PrometheusRule{}
	if err := k8sutil.ParseObjectFromFile(filename, p.scheme, prometheusRule); err != nil {
		return err
	}
	prometheusRule.ObjectMeta = metav1.ObjectMeta{
		Name:      pxPrometheusRule,
		Namespace: cluster.Namespace,
		Labels: map[string]string{
			"prometheus": "portworx",
		},
		OwnerReferences: []metav1.OwnerReference{*ownerRef},
	}
	return k8sutil.CreateOrUpdatePrometheusRule(p.k8sClient, prometheusRule, ownerRef)
}

func getPortworxAPIServiceLabels() map[string]string {
	return map[string]string{
		"name": pxAPIServiceName,
	}
}

func getLighthouseLabels() map[string]string {
	return map[string]string{
		"tier": "px-web-console",
	}
}

func getImageFromDeployment(deployment *appsv1.Deployment, containerName string) string {
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	for _, c := range deployment.Spec.Template.Spec.InitContainers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return ""
}

func getImageFromStatefulSet(ss *appsv1.StatefulSet, containerName string) string {
	for _, c := range ss.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return ""
}

func getImageFromEnv(imageKey string, envs []v1.EnvVar) string {
	for _, env := range envs {
		if env.Name == imageKey {
			return env.Value
		}
	}
	return ""
}

func getSpecsBaseDir() string {
	return portworxSpecsDir
}

type envByName []v1.EnvVar

func (e envByName) Len() int      { return len(e) }
func (e envByName) Swap(i, j int) { e[i], e[j] = e[j], e[i] }
func (e envByName) Less(i, j int) bool {
	return e[i].Name < e[j].Name
}
