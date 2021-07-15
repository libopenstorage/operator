package storagecluster

import (
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	pxRepoServiceName            = "px-repo"
	pxRepoServicePort            = 8080
	pxRepoDeploymentName         = "px-repo"
	pxRepoReplicas               = int32(1)
	pxRepoContainerName          = "px-repo"
	pxRepoDefaultImagePullPolicy = v1.PullIfNotPresent
)

func (c *Controller) syncPxRepo(
	cluster *corev1.StorageCluster,
) error {
	enabled := cluster.Spec.PxRepo != nil && cluster.Spec.PxRepo.Enabled
	logrus.Infof("PX repo enabled: %t", enabled)

	ownerRef := metav1.NewControllerRef(cluster, controllerKind)

	if enabled {
		if err := c.createPxRepoService(cluster.Namespace, ownerRef); err != nil {
			return err
		}

		if err := c.createPxRepoDeployment(cluster, ownerRef); err != nil {
			return err
		}
	} else {
		if err := k8sutil.DeleteService(c.client, pxRepoServiceName, cluster.Namespace, *ownerRef); err != nil {
			return err
		}

		if err := k8sutil.DeleteDeployment(c.client, pxRepoDeploymentName, cluster.Namespace, *ownerRef); err != nil {
			return err
		}
	}

	return nil
}

func (c *Controller) createPxRepoService(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	service := v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxRepoServiceName,
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

	return k8sutil.CreateOrUpdateService(c.client, &service, ownerRef)
}

func getPxRepoDeploymentLabels() map[string]string {
	return map[string]string{
		"name": pxRepoDeploymentName,
	}
}

func (c *Controller) createPxRepoDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	replicas := pxRepoReplicas
	labels := getPxRepoDeploymentLabels()
	imagePullPolicy := pxRepoDefaultImagePullPolicy
	if len(cluster.Spec.PxRepo.ImagePullPolicy) == 0 {
		imagePullPolicy = v1.PullPolicy(cluster.Spec.PxRepo.ImagePullPolicy)
	}

	deployment := apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxRepoDeploymentName,
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
							Image:           cluster.Spec.PxRepo.Image,
							ImagePullPolicy: imagePullPolicy,
						},
					},
				},
			},
		},
	}

	return k8sutil.CreateOrUpdateDeployment(c.client, &deployment, ownerRef)
}
