package component

import (
	"context"
	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	apps "k8s.io/api/apps/v1"
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
	// PxRepoComponentName is name of the component.
	PxRepoComponentName = "PxRepo"
	// PxRepoServiceName is name of the px repo service.
	PxRepoServiceName = "px-repo"
	// PxRepoDeploymentName is name of px repo deployment
	PxRepoDeploymentName = "px-repo"

	pxRepoServicePort   = 8080
	pxRepoReplicas      = int32(1)
	pxRepoContainerName = "px-repo"
)

type pxrepo struct {
	k8sClient client.Client
}

func (p *pxrepo) Name() string {
	return PxRepoComponentName
}

func (p *pxrepo) Priority() int32 {
	return DefaultComponentPriority
}

func (p *pxrepo) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	p.k8sClient = k8sClient
}

func (p *pxrepo) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.PxRepo != nil && cluster.Spec.PxRepo.Enabled
}

func (p *pxrepo) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, corev1.SchemeGroupVersion.WithKind("StorageCluster"))

	if err := p.createPxRepoService(cluster.Namespace, ownerRef); err != nil {
		return err
	}

	if err := p.createPxRepoDeployment(cluster, ownerRef); err != nil {
		return err
	}

	return nil
}

func (p *pxrepo) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, corev1.SchemeGroupVersion.WithKind("StorageCluster"))

	if err := k8sutil.DeleteService(p.k8sClient, PxRepoServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	if err := k8sutil.DeleteDeployment(p.k8sClient, PxRepoDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	return nil
}

func (p *pxrepo) MarkDeleted() {
}

func (p *pxrepo) createPxRepoService(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	service := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PxRepoServiceName,
			Namespace:       clusterNamespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: getPxRepoDeploymentLabels(),
			Ports: []v1.ServicePort{
				{
					Name:       "default",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(pxRepoServicePort),
					TargetPort: intstr.FromInt(pxRepoServicePort),
				},
			},
			Type: v1.ServiceTypeClusterIP,
		},
	}

	return k8sutil.CreateOrUpdateService(p.k8sClient, &service, ownerRef)
}

func getPxRepoDeploymentLabels() map[string]string {
	return map[string]string{
		"name": PxRepoDeploymentName,
	}
}

func (p *pxrepo) createPxRepoDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	existingDeployment := &apps.Deployment{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      PxRepoDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	deployment := p.getPxRepoDeployment(cluster, ownerRef)

	// If existingDeployment does not exist, modified will be true.
	modified :=
		k8sutil.GetImageFromDeployment(existingDeployment, pxRepoContainerName) != k8sutil.GetImageFromDeployment(deployment, pxRepoContainerName) ||
			k8sutil.GetImagePullPolicyFromDeployment(existingDeployment, pxRepoContainerName) != k8sutil.GetImagePullPolicyFromDeployment(deployment, pxRepoContainerName) ||
			util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
			util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
			util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if modified {
		return k8sutil.CreateOrUpdateDeployment(p.k8sClient, deployment, ownerRef)
	}

	return nil
}

func (p *pxrepo) getPxRepoDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) *apps.Deployment {
	replicas := pxRepoReplicas
	labels := getPxRepoDeploymentLabels()

	deployment := apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PxRepoDeploymentName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: apps.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            pxRepoContainerName,
							Image:           util.GetImageURN(cluster, cluster.Spec.PxRepo.Image),
							ImagePullPolicy: pxutil.ImagePullPolicy(cluster),
						},
					},
				},
			},
		},
	}

	return &deployment
}

// RegisterPxRepoComponent registers the PxRepo component
func RegisterPxRepoComponent() {
	Register(PxRepoComponentName, &pxrepo{})
}

func init() {
	RegisterPxRepoComponent()
}
