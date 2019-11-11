package component

import (
	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PortworxAPIComponentName name of the Portworx API component
	PortworxAPIComponentName = "Portworx API"
	pxAPIServiceName         = "portworx-api"
	pxAPIDaemonSetName       = "portworx-api"
)

type portworxAPI struct {
	isCreated bool
	k8sClient client.Client
}

func (c *portworxAPI) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *portworxAPI) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return true
}

func (c *portworxAPI) Reconcile(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createService(cluster, ownerRef); err != nil {
		return err
	}
	if !c.isCreated {
		if err := c.createDaemonSet(cluster, ownerRef); err != nil {
			return err
		}
		c.isCreated = true
	}
	return nil
}

func (c *portworxAPI) Delete(cluster *corev1alpha1.StorageCluster) error {
	return nil
}

func (c *portworxAPI) MarkDeleted() {
	c.isCreated = false
}

func (c *portworxAPI) createService(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getPortworxAPIServiceLabels()
	startPort := pxutil.StartPort(cluster)

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxAPIServiceName,
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
					TargetPort: intstr.FromInt(startPort + 19),
				},
				{
					Name:       "px-rest-gateway",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9021),
					TargetPort: intstr.FromInt(startPort + 20),
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
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	imageName := util.GetImageURN(cluster.Spec.CustomImageRegistry, "k8s.gcr.io/pause:3.1")
	maxUnavailable := intstr.FromString("100%")
	startPort := pxutil.StartPort(cluster)

	newDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxAPIDaemonSetName,
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
					ServiceAccountName: pxutil.PortworxServiceAccountName,
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

	if cluster.Spec.Placement != nil && cluster.Spec.Placement.NodeAffinity != nil {
		newDaemonSet.Spec.Template.Spec.Affinity = &v1.Affinity{
			NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	return k8sutil.CreateOrUpdateDaemonSet(c.k8sClient, newDaemonSet, ownerRef)
}

func getPortworxAPIServiceLabels() map[string]string {
	return map[string]string{
		"name": pxAPIServiceName,
	}
}

// RegisterPortworxAPIComponent registers the Portworx API component
func RegisterPortworxAPIComponent() {
	Register(PortworxAPIComponentName, &portworxAPI{})
}

func init() {
	RegisterPortworxAPIComponent()
}
