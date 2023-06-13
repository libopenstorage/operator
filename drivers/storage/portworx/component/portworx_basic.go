package component

import (
	"context"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// PortworxBasicComponentName name of the Portworx Basic component
	PortworxBasicComponentName = "Portworx Basic"
	// PxClusterRoleName name of the Portworx cluster role
	PxClusterRoleName = "portworx"
	// PxClusterRoleBindingName name of the Portworx cluster role binding
	PxClusterRoleBindingName = "portworx"
	// PxRoleName name of the Portworx role
	PxRoleName = "portworx"
	// PxRoleBindingName name of the Portworx role binding
	PxRoleBindingName = "portworx"
)

type portworxBasic struct {
	k8sClient client.Client
}

func (c *portworxBasic) Name() string {
	return PortworxBasicComponentName
}

func (c *portworxBasic) Priority() int32 {
	return DefaultComponentPriority
}

func (c *portworxBasic) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *portworxBasic) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return false
}

func (c *portworxBasic) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsPortworxEnabled(cluster)
}

func (c *portworxBasic) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	saName := pxutil.PortworxServiceAccountName(cluster)
	if saName == pxutil.DefaultPortworxServiceAccountName {
		if err := c.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
			return NewError(ErrCritical, err)
		}
	}
	if err := c.createClusterRole(); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createClusterRoleBinding(saName, cluster.Namespace); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.prepareForSecrets(saName, cluster, ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	if err := c.createPortworxService(cluster, ownerRef); err != nil {
		return NewError(ErrCritical, err)
	}
	if cluster.Spec.Kvdb != nil && cluster.Spec.Kvdb.Internal {
		if err := c.createPortworxKVDBService(cluster, ownerRef); err != nil {
			// This should not block portworx installation
			return err
		}
	}
	return nil
}

func (c *portworxBasic) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, pxutil.DefaultPortworxServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, PxClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PxClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, pxutil.PortworxServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, pxutil.PortworxKVDBServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	secretsNamespace := getSecretsNamespace(cluster)
	if secretsNamespace == cluster.Namespace {
		if err := k8sutil.DeleteRole(c.k8sClient, PxRoleName, secretsNamespace, *ownerRef); err != nil {
			return err
		}
		if err := k8sutil.DeleteRoleBinding(c.k8sClient, PxRoleBindingName, secretsNamespace, *ownerRef); err != nil {
			return err
		}
	} else {
		if err := k8sutil.DeleteRole(c.k8sClient, PxRoleName, secretsNamespace); err != nil {
			return err
		}
		if err := k8sutil.DeleteRoleBinding(c.k8sClient, PxRoleBindingName, secretsNamespace); err != nil {
			return err
		}
	}

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
				Name:            pxutil.DefaultPortworxServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *portworxBasic) prepareForSecrets(
	saName string,
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	secretsNamespace := getSecretsNamespace(cluster)

	if secretsNamespace != cluster.Namespace {
		if err := c.createNamespace(secretsNamespace); err != nil {
			return err
		}
	}
	if err := c.createRole(cluster, secretsNamespace, ownerRef); err != nil {
		return err
	}
	if err := c.createRoleBinding(cluster, saName, secretsNamespace, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *portworxBasic) createNamespace(namespace string) error {
	existingNamespace := &v1.Namespace{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name: namespace,
		},
		existingNamespace,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating Namespace %s", namespace)
		ns := &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		return c.k8sClient.Create(context.TODO(), ns)
	} else if err != nil {
		return err
	}
	return nil
}

func (c *portworxBasic) createRole(
	cluster *corev1.StorageCluster,
	namespace string,
	ownerRef *metav1.OwnerReference,
) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PxRoleName,
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "create", "update", "patch", "delete"},
			},
		},
	}

	if cluster.Namespace == namespace {
		role.OwnerReferences = []metav1.OwnerReference{*ownerRef}
		return k8sutil.CreateOrUpdateRole(c.k8sClient, role, ownerRef)
	}

	// Remove ownership information from the object as Kubernetes
	// does not handle cross-namespace ownership. This code should
	// be removed eventually when existing customers are upgraded.
	existingRole := &rbacv1.Role{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      role.Name,
			Namespace: role.Namespace,
		},
		existingRole,
	)
	if err == nil && len(existingRole.OwnerReferences) > 0 {
		existingRole.OwnerReferences = nil
		logrus.Infof("Removing ownership from %s/%s Role", role.Namespace, role.Name)
		if err := c.k8sClient.Update(context.TODO(), existingRole); err != nil {
			return err
		}
		role.ResourceVersion = existingRole.ResourceVersion
	}

	return k8sutil.CreateOrUpdateRole(c.k8sClient, role, nil)
}

func (c *portworxBasic) createRoleBinding(
	cluster *corev1.StorageCluster,
	saName string,
	bindingNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PxRoleBindingName,
			Namespace: bindingNamespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: cluster.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     PxRoleName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if cluster.Namespace == bindingNamespace {
		roleBinding.OwnerReferences = []metav1.OwnerReference{*ownerRef}
		return k8sutil.CreateOrUpdateRoleBinding(c.k8sClient, roleBinding, ownerRef)
	}

	// Remove ownership information from the object as Kubernetes
	// does not handle cross-namespace ownership. This code should
	// be removed eventually when existing customers are upgraded.
	existingRoleBinding := &rbacv1.RoleBinding{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      roleBinding.Name,
			Namespace: roleBinding.Namespace,
		},
		existingRoleBinding,
	)
	if err == nil && len(existingRoleBinding.OwnerReferences) > 0 {
		existingRoleBinding.OwnerReferences = nil
		logrus.Infof("Removing ownership from %s/%s RoleBinding", roleBinding.Namespace, roleBinding.Name)
		if err := c.k8sClient.Update(context.TODO(), existingRoleBinding); err != nil {
			return err
		}
		roleBinding.ResourceVersion = existingRoleBinding.ResourceVersion
	}

	return k8sutil.CreateOrUpdateRoleBinding(c.k8sClient, roleBinding, nil)
}

func (c *portworxBasic) createClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list", "watch"},
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
					Resources: []string{"pods/exec"},
					Verbs:     []string{"create"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims", "persistentvolumes"},
					Verbs:     []string{"get", "list", "create", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "list", "create", "update"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"deployments"},
					Verbs:     []string{"get", "list", "create", "update", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"services"},
					Verbs:     []string{"get", "list", "create", "update", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"endpoints"},
					Verbs:     []string{"get", "list", "create", "update", "delete"},
				},
				{
					APIGroups: []string{"portworx.io"},
					Resources: []string{"volumeplacementstrategies"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses", "csinodes"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"volumeattachments"},
					Verbs:     []string{"get", "list", "create", "delete", "update"},
				},
				{
					APIGroups: []string{"stork.libopenstorage.org"},
					Resources: []string{"backuplocations"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"", "events.k8s.io"},
					Resources: []string{"events"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
				},
				{
					APIGroups: []string{"core.libopenstorage.org"},
					Resources: []string{"*"},
					Verbs:     []string{"*"},
				},
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{PxSCCName},
					Verbs:         []string{"use"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.PrivilegedPSPName},
					Verbs:         []string{"use"},
				},
				{
					APIGroups: []string{"certificates.k8s.io"},
					Resources: []string{"certificatesigningrequests"},
					Verbs:     []string{"get", "list", "create", "watch", "delete", "update"},
				},
			},
		},
	)
}

func (c *portworxBasic) createClusterRoleBinding(
	saName, clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      saName,
					Namespace: clusterNamespace,
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

func (c *portworxBasic) createPortworxService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	service := getPortworxServiceSpec(cluster, ownerRef)
	return k8sutil.CreateOrUpdateService(c.k8sClient, service, ownerRef)
}

func getPortworxServiceSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) *v1.Service {
	labels := pxutil.SelectorLabels()
	startPort := pxutil.StartPort(cluster)
	_, sdkTargetPort, restGatewayTargetPort, pxAPITLSPort := getTargetPorts(startPort)

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: cluster.Namespace,
			Labels:    labels,
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
			},
		},
	}

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

	if ownerRef != nil {
		newService.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	}

	newService.Annotations = util.GetCustomAnnotations(cluster, k8sutil.Service, pxutil.PortworxServiceName)

	serviceType := pxutil.ServiceType(cluster, pxutil.PortworxServiceName)
	if serviceType != "" {
		newService.Spec.Type = serviceType
	}

	return newService
}

func (c *portworxBasic) createPortworxKVDBService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	service := getPortworxKVDBServiceSpec(cluster, ownerRef)
	return k8sutil.CreateOrUpdateService(c.k8sClient, service, ownerRef)
}

func getPortworxKVDBServiceSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) *v1.Service {
	labels := pxutil.SelectorLabels()
	startPort := pxutil.StartPort(cluster)
	kvdbTargetPort, _, _, _ := getTargetPorts(startPort)

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxKVDBServiceName,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: v1.ServiceSpec{
			Selector: map[string]string{
				constants.LabelKeyKVDBPod: constants.LabelValueTrue,
			},
			Type: v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Name:       pxutil.PortworxKVDBPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9019),
					TargetPort: intstr.FromInt(kvdbTargetPort),
				},
			},
		},
	}

	if ownerRef != nil {
		newService.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	}

	newService.Annotations = util.GetCustomAnnotations(cluster, k8sutil.Service, pxutil.PortworxKVDBServiceName)

	serviceType := pxutil.ServiceType(cluster, pxutil.PortworxKVDBServiceName)
	if serviceType != "" {
		newService.Spec.Type = serviceType
	}

	return newService
}

func getTargetPorts(startPort int) (int, int, int, int) {
	kvdbTargetPort := 9019
	sdkTargetPort := 9020
	restGatewayTargetPort := 9021
	pxAPITLSPort := 9023
	if startPort != pxutil.DefaultStartPort {
		kvdbTargetPort = startPort + 15
		sdkTargetPort = startPort + 16
		restGatewayTargetPort = startPort + 17
		pxAPITLSPort = startPort + 19
	}
	return kvdbTargetPort, sdkTargetPort, restGatewayTargetPort, pxAPITLSPort
}

func getSecretsNamespace(cluster *corev1.StorageCluster) string {
	for _, env := range cluster.Spec.Env {
		if env.Name == pxutil.EnvKeyPortworxSecretsNamespace {
			return env.Value
		}
	}
	return cluster.Namespace
}

// RegisterPortworxBasicComponent registers the Portworx Basic component
func RegisterPortworxBasicComponent() {
	Register(PortworxBasicComponentName, &portworxBasic{})
}

func init() {
	RegisterPortworxBasicComponent()
}
