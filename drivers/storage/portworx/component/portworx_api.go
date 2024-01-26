package component

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PortworxAPIComponentName name of the Portworx API component
	PortworxAPIComponentName = "Portworx API"
	// PxAPIServiceName name of the Portworx API service
	PxAPIServiceName = "portworx-api"
	// PxAPIDaemonSetName name of the Portworx API daemon set
	PxAPIDaemonSetName = "portworx-api"
)

var (
	csiRegistrarAdditionPxVersion, _ = version.NewVersion("2.13")
)

type portworxAPI struct {
	isCreated bool
	k8sClient client.Client
}

func (c *portworxAPI) Name() string {
	return PortworxAPIComponentName
}

func (c *portworxAPI) Priority() int32 {
	return DefaultComponentPriority
}

func (c *portworxAPI) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *portworxAPI) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *portworxAPI) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsPortworxEnabled(cluster)
}

func (c *portworxAPI) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createService(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createDaemonSet(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *portworxAPI) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteService(c.k8sClient, PxAPIServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDaemonSet(c.k8sClient, PxAPIDaemonSetName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	c.MarkDeleted()
	return nil
}

func (c *portworxAPI) MarkDeleted() {
	c.isCreated = false
}

func (c *portworxAPI) createService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getPortworxAPIServiceLabels()
	if customLabels := util.GetCustomLabels(cluster, k8sutil.Service, PxAPIServiceName); customLabels != nil {
		for k, v := range customLabels {
			// Custom labels should not overwrite builtin portworx labels
			if _, ok := labels[k]; !ok {
				labels[k] = v
			}
		}
	}

	startPort := pxutil.StartPort(cluster)
	sdkTargetPort := 9020
	restGatewayTargetPort := 9021
	pxAPITLSPort := 9023
	if startPort != pxutil.DefaultStartPort {
		sdkTargetPort = startPort + 16
		restGatewayTargetPort = startPort + 17
		pxAPITLSPort = startPort + 19
	}

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PxAPIServiceName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: getPortworxAPIServiceLabels(),
			Type:     v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Name:       pxutil.PortworxRESTPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9001),
					TargetPort: intstr.FromInt(startPort),
				},
				{
					Name:       pxutil.PortworxSDKPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9020),
					TargetPort: intstr.FromInt(sdkTargetPort),
				},
				{
					Name:       "px-rest-gateway",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9021),
					TargetPort: intstr.FromInt(restGatewayTargetPort),
				},
			},
		},
	}

	newService.Annotations = util.GetCustomAnnotations(cluster, k8sutil.Service, PxAPIServiceName)

	// TLS secured port 9023 added in PX 2.9.0, only add it is 2.9.0 or later
	pxAPITLSVersion, _ := version.NewVersion("2.9.0")
	if pxutil.GetPortworxVersion(cluster).GreaterThanOrEqual(pxAPITLSVersion) {
		newService.Spec.Ports = append(newService.Spec.Ports,
			v1.ServicePort{
				Name:       pxutil.PortworxRESTTLSPortName,
				Protocol:   v1.ProtocolTCP,
				Port:       int32(9023),
				TargetPort: intstr.FromInt(pxAPITLSPort),
			})
	}

	serviceType := pxutil.ServiceType(cluster, PxAPIServiceName)
	if serviceType != "" {
		newService.Spec.Type = serviceType
	}

	return k8sutil.CreateOrUpdateService(c.k8sClient, newService, ownerRef)
}

func (c *portworxAPI) createDaemonSet(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	existingDaemonSet := &appsv1.DaemonSet{}
	getErr := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      PxAPIDaemonSetName,
			Namespace: cluster.Namespace,
		},
		existingDaemonSet,
	)
	if getErr != nil && !errors.IsNotFound(getErr) {
		return getErr
	}

	var existingPauseImageName string
	var csiNodeRegistrarImageName string
	var existingCsiDriverRegistrarImageName string
	if len(existingDaemonSet.Spec.Template.Spec.Containers) > 0 {
		existingPauseImageName = existingDaemonSet.Spec.Template.Spec.Containers[0].Image
	}

	pauseImageName := pxutil.ImageNamePause

	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.Pause != "" {
		pauseImageName = cluster.Status.DesiredImages.Pause
	}

	//  If px version is greater than 2.13 then update daemonset when csi-node-driver-registrar image changes if CSI is enabled
	pxVersion := pxutil.GetPortworxVersion(cluster)
	removeCSIRegistrar := false
	checkCSIDriverRegistrarChange := false
	if pxutil.IsCSIEnabled(cluster) && pxVersion.GreaterThanOrEqual(csiRegistrarAdditionPxVersion) && len(existingDaemonSet.Spec.Template.Spec.Containers) > 1 {
		existingCsiDriverRegistrarImageName = existingDaemonSet.Spec.Template.Spec.Containers[1].Image
		csiNodeRegistrarImageName = cluster.Status.DesiredImages.CSINodeDriverRegistrar
		csiNodeRegistrarImageName = util.GetImageURN(cluster, csiNodeRegistrarImageName)
		checkCSIDriverRegistrarChange = true
	} else if !pxutil.IsCSIEnabled(cluster) && pxVersion.GreaterThanOrEqual(csiRegistrarAdditionPxVersion) && len(existingDaemonSet.Spec.Template.Spec.Containers) > 1 {
		// When CSI is disabled, remove the csi-node-driver-registrar container from the daemonset
		removeCSIRegistrar = true

	}

	pauseImageName = util.GetImageURN(cluster, pauseImageName)
	serviceAccount := pxutil.PortworxServiceAccountName(cluster)
	existingServiceAccount := existingDaemonSet.Spec.Template.Spec.ServiceAccountName

	modified := existingPauseImageName != pauseImageName || removeCSIRegistrar ||
		(checkCSIDriverRegistrarChange && (existingCsiDriverRegistrarImageName != csiNodeRegistrarImageName)) ||
		existingServiceAccount != serviceAccount ||
		util.HasPullSecretChanged(cluster, existingDaemonSet.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDaemonSet.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDaemonSet.Spec.Template.Spec.Tolerations)
	if !c.isCreated || errors.IsNotFound(getErr) || modified {
		daemonSet := getPortworxAPIDaemonSetSpec(cluster, ownerRef, pauseImageName)
		if err := k8sutil.CreateOrUpdateDaemonSet(c.k8sClient, daemonSet, ownerRef); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func getPortworxAPIDaemonSetSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
) *appsv1.DaemonSet {
	maxUnavailable := intstr.FromString("100%")
	startPort := pxutil.StartPort(cluster)

	newDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PxAPIDaemonSetName,
			Namespace:       cluster.Namespace,
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
					ServiceAccountName: pxutil.PortworxServiceAccountName(cluster),
					RestartPolicy:      v1.RestartPolicyAlways,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:            "portworx-api",
							Image:           imageName,
							ImagePullPolicy: pxutil.ImagePullPolicy(cluster),
							ReadinessProbe: &v1.Probe{
								PeriodSeconds: int32(10),
								ProbeHandler: v1.ProbeHandler{
									HTTPGet: &v1.HTTPGetAction{
										Host: "127.0.0.1",
										Path: "/status",
										Port: intstr.FromInt(startPort),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// If CSI is enabled then run the csi-node-driver-registrar pods in the same daemonset
	// Do this only if portworx version is greater than 2.13
	pxVersion := pxutil.GetPortworxVersion(cluster)
	if pxutil.IsCSIEnabled(cluster) && pxVersion.GreaterThanOrEqual(csiRegistrarAdditionPxVersion) {
		csiRegistrar := csiRegistrarContainer(cluster)
		if csiRegistrar != nil {
			newDaemonSet.Spec.Template.Spec.Containers = append(newDaemonSet.Spec.Template.Spec.Containers, *csiRegistrar)
			newDaemonSet.Spec.Template.Spec.Volumes = getCSIContainerVolume(cluster)
		} else {
			logrus.Warn("CSI enabled, but no CSI-Registrar container info")
		}
	}

	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		newDaemonSet.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *cluster.Spec.ImagePullSecret,
			},
		)
	}

	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			newDaemonSet.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if cluster.Spec.Placement != nil {
			if len(cluster.Spec.Placement.Tolerations) > 0 {
				newDaemonSet.Spec.Template.Spec.Tolerations = make([]v1.Toleration, 0)
				for _, toleration := range cluster.Spec.Placement.Tolerations {
					newDaemonSet.Spec.Template.Spec.Tolerations = append(
						newDaemonSet.Spec.Template.Spec.Tolerations,
						*(toleration.DeepCopy()),
					)
				}
			}
		}
	}
	newDaemonSet.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(newDaemonSet.Spec.Template.ObjectMeta)

	return newDaemonSet
}

func getPortworxAPIServiceLabels() map[string]string {
	return map[string]string{
		"name": PxAPIServiceName,
	}
}

// RegisterPortworxAPIComponent registers the Portworx API component
func RegisterPortworxAPIComponent() {
	Register(PortworxAPIComponentName, &portworxAPI{})
}

func init() {
	RegisterPortworxAPIComponent()
}

// Function to return container specs for csi-node-driver-registrar
func csiRegistrarContainer(cluster *corev1.StorageCluster) *v1.Container {
	k8sVersion, _, _ := k8sutil.GetFullVersion()
	deprecatedCSIDriverName := pxutil.UseDeprecatedCSIDriverName(cluster)
	disableCSIAlpha := pxutil.DisableCSIAlpha(cluster)
	kubeletPath := pxutil.KubeletPath(cluster)
	includeSnapshotController := pxutil.IncludeCSISnapshotController(cluster)
	pxVersion := pxutil.GetPortworxVersion(cluster)
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)
	csiGenerator := pxutil.NewCSIGenerator(*k8sVersion, *pxVersion,
		deprecatedCSIDriverName, disableCSIAlpha, kubeletPath, includeSnapshotController)

	var csiConfig *pxutil.CSIConfiguration
	if pxutil.IsCSIEnabled(cluster) {
		csiConfig = csiGenerator.GetCSIConfiguration()
	} else {
		csiConfig = csiGenerator.GetBasicCSIConfiguration()
	}

	container := v1.Container{
		ImagePullPolicy: imagePullPolicy,
		Env: []v1.EnvVar{
			{
				Name:  "ADDRESS",
				Value: "/csi/csi.sock",
			},
			{
				Name: "KUBE_NODE_NAME",
				ValueFrom: &v1.EnvVarSource{
					FieldRef: &v1.ObjectFieldSelector{
						FieldPath: "spec.nodeName",
					},
				},
			},
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      "csi-driver-path",
				MountPath: "/csi",
			},
			{
				Name:      "registration-dir",
				MountPath: "/registration",
			},
		},
	}

	if cluster.Status.DesiredImages.CSINodeDriverRegistrar != "" {
		container.Name = pxutil.CSIRegistrarContainerName
		container.Image = util.GetImageURN(
			cluster,
			cluster.Status.DesiredImages.CSINodeDriverRegistrar,
		)
		container.Args = []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			fmt.Sprintf("--kubelet-registration-path=%s/csi.sock", csiConfig.DriverBasePath()),
		}
	} else if cluster.Status.DesiredImages.CSIDriverRegistrar != "" {
		container.Name = "csi-driver-registrar"
		container.Image = util.GetImageURN(
			cluster,
			cluster.Status.DesiredImages.CSIDriverRegistrar,
		)
		container.Args = []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			"--mode=node-register",
			fmt.Sprintf("--kubelet-registration-path=%s/csi.sock", csiConfig.DriverBasePath()),
		}
	}

	if container.Name == "" {
		return nil
	}
	return &container
}

// Returns the volume specs for the csi container
func getCSIContainerVolume(cluster *corev1.StorageCluster) []v1.Volume {
	k8sVersion, _, _ := k8sutil.GetFullVersion()
	deprecatedCSIDriverName := pxutil.UseDeprecatedCSIDriverName(cluster)
	disableCSIAlpha := pxutil.DisableCSIAlpha(cluster)
	kubeletPath := pxutil.KubeletPath(cluster)
	includeSnapshotController := pxutil.IncludeCSISnapshotController(cluster)
	pxVersion := pxutil.GetPortworxVersion(cluster)
	csiGenerator := pxutil.NewCSIGenerator(*k8sVersion, *pxVersion,
		deprecatedCSIDriverName, disableCSIAlpha, kubeletPath, includeSnapshotController)

	if !pxutil.IsCSIEnabled(cluster) {
		return []v1.Volume{}
	}

	csiConfig := csiGenerator.GetCSIConfiguration()
	volumes := make([]v1.Volume, 0, 2)

	volume1 := v1.Volume{
		Name: "registration-dir",
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: kubeletPath + "/plugins_registry",
				Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
			},
		},
	}

	if csiConfig.UseOlderPluginsDirAsRegistration {
		volume1.HostPath.Path = kubeletPath + "/plugins"
	}

	volumes = append(volumes, volume1)

	volume2 := v1.Volume{
		Name: "csi-driver-path",
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: csiConfig.DriverBasePath(),
				Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
			},
		},
	}
	volumes = append(volumes, volume2)
	return volumes
}
