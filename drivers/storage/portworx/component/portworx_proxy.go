package component

import (
	"context"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	api "k8s.io/kubernetes/pkg/apis/core"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PortworxProxyComponent name of the Portworx Proxy component. This component
	// runs in the kube-system namespace if the cluster is running outside. This
	// ensures that k8s in-tree driver traffic gets routed to the Portworx nodes.
	PortworxProxyComponent = "Portworx Proxy"
	// PxProxyServiceAccountName name of the Portworx proxy service account
	PxProxyServiceAccountName = "portworx-proxy"
	// PxProxyClusterRoleBindingName name of the Portworx proxy cluster role binding
	PxProxyClusterRoleBindingName = "portworx-proxy"
	// PxProxyDaemonSetName name of the Portworx proxy daemon set
	PxProxyDaemonSetName = "portworx-proxy"
	pxProxyContainerName = "portworx-proxy"
)

type portworxProxy struct {
	isCreated bool
	k8sClient client.Client
}

func (c *portworxProxy) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *portworxProxy) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return cluster.Namespace != api.NamespaceSystem &&
		pxutil.StartPort(cluster) != pxutil.DefaultStartPort &&
		pxutil.IsPortworxEnabled(cluster)
}

func (c *portworxProxy) Reconcile(cluster *corev1alpha1.StorageCluster) error {
	if err := c.createServiceAccount(); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createClusterRoleBinding(); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createPortworxService(cluster); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createDaemonSet(cluster); err != nil {
		return NewError(ErrCritical, err)
	}
	return nil
}

func (c *portworxProxy) Delete(cluster *corev1alpha1.StorageCluster) error {
	if cluster.Namespace == api.NamespaceSystem {
		// If the cluster namespace is kube-system, then there is nothing to delete.
		// Also, we do not want to delete portworx-service if running in kube-system
		return nil
	}

	if err := k8sutil.DeleteServiceAccount(c.k8sClient, PxProxyServiceAccountName, api.NamespaceSystem); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PxProxyClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, pxutil.PortworxServiceName, api.NamespaceSystem); err != nil {
		return err
	}
	if err := k8sutil.DeleteDaemonSet(c.k8sClient, PxProxyDaemonSetName, api.NamespaceSystem); err != nil {
		return err
	}
	c.MarkDeleted()
	return nil
}

func (c *portworxProxy) MarkDeleted() {
	c.isCreated = false
}

func (c *portworxProxy) createServiceAccount() error {
	sa := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PxProxyServiceAccountName,
			Namespace: api.NamespaceSystem,
		},
	}

	// Remove ownership information from the object as Kuberentes
	// does not handle cross-namespace ownership. This code should
	// be removed eventually when existing customers are upgraded.
	existingSA := &v1.ServiceAccount{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      sa.Name,
			Namespace: sa.Namespace,
		},
		existingSA,
	)
	if err == nil && len(existingSA.OwnerReferences) > 0 {
		existingSA.OwnerReferences = nil
		logrus.Debugf("Updating %s/%s ServiceAccount", sa.Namespace, sa.Name)
		if err := c.k8sClient.Update(context.TODO(), existingSA); err != nil {
			return err
		}
		sa.ResourceVersion = existingSA.ResourceVersion
	}

	return k8sutil.CreateOrUpdateServiceAccount(c.k8sClient, sa, nil)
}

func (c *portworxProxy) createClusterRoleBinding() error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxProxyClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      PxProxyServiceAccountName,
					Namespace: api.NamespaceSystem,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     PxClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *portworxProxy) createPortworxService(
	cluster *corev1alpha1.StorageCluster,
) error {
	service := getPortworxServiceSpec(cluster, nil)
	service.Namespace = api.NamespaceSystem
	service.Labels = getPortworxProxyServiceLabels()
	service.Spec.Selector = getPortworxProxyServiceLabels()

	// Remove ownership information from the object as Kuberentes
	// does not handle cross-namespace ownership. This code should
	// be removed eventually when existing customers are upgraded.
	existingSvc := &v1.Service{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      service.Name,
			Namespace: service.Namespace,
		},
		existingSvc,
	)
	if err == nil && len(existingSvc.OwnerReferences) > 0 {
		existingSvc.OwnerReferences = nil
		logrus.Debugf("Updating %s/%s Service", service.Namespace, service.Name)
		if err := c.k8sClient.Update(context.TODO(), existingSvc); err != nil {
			return err
		}
	}

	return k8sutil.CreateOrUpdateService(c.k8sClient, service, nil)
}

func (c *portworxProxy) createDaemonSet(
	cluster *corev1alpha1.StorageCluster,
) error {
	existingDaemonSet := &appsv1.DaemonSet{}
	getErr := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      PxProxyDaemonSetName,
			Namespace: api.NamespaceSystem,
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

	imageName := util.GetImageURN(cluster.Spec.CustomImageRegistry, "k8s.gcr.io/pause:3.1")

	modified := existingImageName != imageName ||
		util.HasPullSecretChanged(cluster, existingDaemonSet.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDaemonSet.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDaemonSet.Spec.Template.Spec.Tolerations)

	if !c.isCreated || errors.IsNotFound(getErr) || modified {
		daemonSet := getPortworxProxyDaemonSetSpec(cluster, imageName)

		// Remove ownership information from the object as Kuberentes
		// does not handle cross-namespace ownership. This code should
		// be removed eventually when existing customers are upgraded.
		if !errors.IsNotFound(getErr) && len(existingDaemonSet.OwnerReferences) > 0 {
			existingDaemonSet.OwnerReferences = nil
			logrus.Debugf("Updating %s/%s DaemonSet", daemonSet.Namespace, daemonSet.Name)
			if err := c.k8sClient.Update(context.TODO(), existingDaemonSet); err != nil {
				return err
			}
		}

		if err := k8sutil.CreateOrUpdateDaemonSet(c.k8sClient, daemonSet, nil); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func getPortworxProxyDaemonSetSpec(
	cluster *corev1alpha1.StorageCluster,
	imageName string,
) *appsv1.DaemonSet {
	maxUnavailable := intstr.FromString("100%")
	startPort := pxutil.StartPort(cluster)

	newDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PxProxyDaemonSetName,
			Namespace: api.NamespaceSystem,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: getPortworxProxyServiceLabels(),
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: getPortworxProxyServiceLabels(),
				},
				Spec: v1.PodSpec{
					ServiceAccountName: PxProxyServiceAccountName,
					RestartPolicy:      v1.RestartPolicyAlways,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:            pxProxyContainerName,
							Image:           imageName,
							ImagePullPolicy: pxutil.ImagePullPolicy(cluster),
							ReadinessProbe: &v1.Probe{
								PeriodSeconds: 10,
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Host: "127.0.0.1",
										Path: "/health",
										Port: intstr.FromInt(startPort + 14),
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

func getPortworxProxyServiceLabels() map[string]string {
	return map[string]string{
		"name": PxProxyDaemonSetName,
	}
}

// RegisterPortworxProxyComponent registers the Portworx proxy component
func RegisterPortworxProxyComponent() {
	Register(PortworxProxyComponent, &portworxProxy{})
}

func init() {
	RegisterPortworxProxyComponent()
}
