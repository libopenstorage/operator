package portworx

import (
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/portworx/sched-ops/k8s"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	pxServiceAccountName     = "px-account"
	pxServiceName            = "portworx-service"
	pxSecretsNamespace       = "portworx"
	pxClusterRoleName        = "node-get-put-list-role"
	pxClusterRoleBindingName = "node-role-binding"
	pxRoleName               = "px-role"
	pxRoleBindingName        = "px-role-binding"
)

func (p *portworx) PreInstall(cluster *corev1alpha1.StorageCluster) error {
	if !p.serviceAccountCreated {
		if err := createServiceAccount(cluster.Namespace); err != nil {
			return err
		}
	}
	if !p.secretsNamespaceCreated {
		if err := createSecretsNamespace(); err != nil {
			return err
		}
		p.secretsNamespaceCreated = true
	}
	if !p.clusterRoleCreated {
		if err := createClusterRole(); err != nil {
			return err
		}
		p.clusterRoleCreated = true
	}
	if !p.clusterRoleBindingCreated {
		if err := createClusterRoleBinding(cluster.Namespace); err != nil {
			return err
		}
		p.clusterRoleBindingCreated = true
	}
	if !p.roleCreated {
		if err := createRole(); err != nil {
			return err
		}
		p.roleCreated = true
	}
	if !p.roleBindingCreated {
		if err := createRoleBinding(cluster.Namespace); err != nil {
			return err
		}
		p.roleBindingCreated = true
	}
	if !p.portworxSerivceCreated {
		if err := p.createPortworxSerivce(cluster); err != nil {
			return err
		}
		p.portworxSerivceCreated = true
	}
	return nil
}

func createServiceAccount(clusterNamespace string) error {
	sa := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceAccountName,
			Namespace: clusterNamespace,
		},
	}
	_, err := k8s.Instance().CreateServiceAccount(sa)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func createRole() error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleName,
			Namespace: pxSecretsNamespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "create", "update", "patch"},
			},
		},
	}
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateRole(role)
	if errors.IsAlreadyExists(err) {
		if _, err = k8sOps.UpdateRole(role); err != nil {
			return err
		}
	}
	return err
}

func createRoleBinding(clusterNamespace string) error {
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleBindingName,
			Namespace: pxSecretsNamespace,
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
	}

	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateRoleBinding(roleBinding)
	if errors.IsAlreadyExists(err) {
		if _, err = k8sOps.UpdateRoleBinding(roleBinding); err != nil {
			return err
		}
	}
	return err
}

func createClusterRole() error {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: pxClusterRoleName,
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
				Verbs:     []string{"get", "update", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "delete", "watch", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims", "persistentvolumes"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "update", "list", "create"},
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
		},
	}

	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateClusterRole(clusterRole)
	if errors.IsAlreadyExists(err) {
		if _, err = k8sOps.UpdateClusterRole(clusterRole); err != nil {
			return err
		}
	}
	return err
}

func createClusterRoleBinding(clusterNamespace string) error {
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: pxClusterRoleBindingName,
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
	}

	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateClusterRoleBinding(binding)
	if errors.IsAlreadyExists(err) {
		if _, err = k8sOps.UpdateClusterRoleBinding(binding); err != nil {
			return err
		}
	}
	return err
}

func createSecretsNamespace() error {
	_, err := k8s.Instance().CreateNamespace(pxSecretsNamespace, nil)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (p *portworx) createPortworxSerivce(cluster *corev1alpha1.StorageCluster) error {
	t, err := newTemplate(cluster)
	if err != nil {
		return err
	}

	labels := p.GetSelectorLabels()
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "px-api",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9001),
					TargetPort: intstr.FromInt(t.startPort),
				},
				{
					Name:       "px-kvdb",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9019),
					TargetPort: intstr.FromInt(t.startPort + 18),
				},
				{
					Name:       "px-sdk",
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
	if !t.isAKS && !t.isGKE && !t.isEKS {
		service.Spec.Type = v1.ServiceTypeNodePort
	}

	k8sOps := k8s.Instance()
	_, err = k8sOps.CreateService(service)
	if errors.IsAlreadyExists(err) {
		if _, err = k8sOps.UpdateService(service); err != nil {
			return err
		}
	}
	return err
}
