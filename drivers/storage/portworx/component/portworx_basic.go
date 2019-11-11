package component

import (
	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PortworxBasicComponentName name of the Portworx Basic component
	PortworxBasicComponentName = "Portworx Basic"
	pxClusterRoleName          = "portworx"
	pxClusterRoleBindingName   = "portworx"
	pxRoleName                 = "portworx"
	pxRoleBindingName          = "portworx"
	pxServiceName              = "portworx-service"
)

type portworxBasic struct {
	k8sClient client.Client
}

func (c *portworxBasic) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *portworxBasic) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return true
}

func (c *portworxBasic) Reconcile(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createClusterRole(ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createRole(cluster.Namespace, ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createPortworxService(cluster, ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	return nil
}

func (c *portworxBasic) Delete(cluster *corev1alpha1.StorageCluster) error {
	return nil
}

func (c *portworxBasic) MarkDeleted() {}

func (c *portworxBasic) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxutil.PortworxServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *portworxBasic) createRole(clusterNamespace string, ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateRole(
		c.k8sClient,
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

func (c *portworxBasic) createRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRoleBinding(
		c.k8sClient,
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxRoleBindingName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pxutil.PortworxServiceAccountName,
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

func (c *portworxBasic) createClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
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

func (c *portworxBasic) createClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pxutil.PortworxServiceAccountName,
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

func (c *portworxBasic) createPortworxService(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	labels := pxutil.SelectorLabels()
	startPort := pxutil.StartPort(cluster)

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxServiceName,
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
					Name:       pxutil.PortworxKVDBPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9019),
					TargetPort: intstr.FromInt(startPort + 18),
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

// RegisterPortworxBasicComponent registers the Portworx Basic component
func RegisterPortworxBasicComponent() {
	Register(PortworxBasicComponentName, &portworxBasic{})
}

func init() {
	RegisterPortworxBasicComponent()
}
