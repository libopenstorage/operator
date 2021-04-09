package component

import (
	"context"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
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
			Selector: labels,
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
				{
					Name:       pxutil.PortworxRESTTLSPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9023),
					TargetPort: intstr.FromInt(pxAPITLSPort),
				},
			},
		},
	}

	serviceType := pxutil.ServiceType(cluster)
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

	var existingImageName string
	if len(existingDaemonSet.Spec.Template.Spec.Containers) > 0 {
		existingImageName = existingDaemonSet.Spec.Template.Spec.Containers[0].Image
	}

	imageName := util.GetImageURN(cluster.Spec.CustomImageRegistry, pxutil.ImageNamePause)
	serviceAccount := pxutil.PortworxServiceAccountName(cluster)
	existingServiceAccount := existingDaemonSet.Spec.Template.Spec.ServiceAccountName

	modified := existingImageName != imageName ||
		existingServiceAccount != serviceAccount ||
		util.HasPullSecretChanged(cluster, existingDaemonSet.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDaemonSet.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDaemonSet.Spec.Template.Spec.Tolerations)

	if !c.isCreated || errors.IsNotFound(getErr) || modified {
		daemonSet := getPortworxAPIDaemonSetSpec(cluster, ownerRef, imageName)
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
								Handler: v1.Handler{
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
