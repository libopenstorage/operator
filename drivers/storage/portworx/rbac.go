package portworx

import (
	"fmt"
	"regexp"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1beta2"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	pxServiceAccountName      = "portworx"
	pxClusterRoleName         = "portworx"
	pxClusterRoleBindingName  = "portworx"
	pxRoleName                = "portworx"
	pxRoleBindingName         = "portworx"
	pxSecretsNamespace        = "portworx"
	pxServiceName             = "portworx-service"
	pvcServiceAccountName     = "portworx-pvc-controller"
	pvcClusterRoleName        = "portworx-pvc-controller"
	pvcClusterRoleBindingName = "portworx-pvc-controller"
	pvcDeploymentName         = "portworx-pvc-controller"
)

var (
	kbVerRegex = regexp.MustCompile(`^(v\d+\.\d+\.\d+).*`)
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

	t, err := newTemplate(cluster)
	if err != nil {
		return err
	}

	if !p.portworxSerivceCreated {
		if err := p.createPortworxSerivce(t); err != nil {
			return err
		}
		p.portworxSerivceCreated = true
	}

	if t.needsPVCController {
		if !p.pvcControllerServiceAccountCreated {
			if err := createPVCControllerServiceAccount(cluster.Namespace); err != nil {
				return err
			}
			p.pvcControllerServiceAccountCreated = true
		}
		if !p.pvcControllerClusterRoleCreated {
			if err := createPVCControllerClusterRole(); err != nil {
				return err
			}
			p.pvcControllerClusterRoleCreated = true
		}
		if !p.pvcControllerClusterRoleBindingCreated {
			if err := createPVCControllerClusterRoleBinding(cluster.Namespace); err != nil {
				return err
			}
			p.pvcControllerClusterRoleBindingCreated = true
		}
		if !p.pvcControllerDeploymentCreated {
			if err := p.createPVCControllerDeployment(t); err != nil {
				return err
			}
			p.pvcControllerDeploymentCreated = true
		}
	}

	return nil
}

func createServiceAccount(clusterNamespace string) error {
	logrus.Debugf("Creating/updating %s service account", pxServiceAccountName)
	return createOrUpdateServiceAccount(&v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceAccountName,
			Namespace: clusterNamespace,
		},
	})
}

func createPVCControllerServiceAccount(clusterNamespace string) error {
	logrus.Debugf("Creating/updating %s service account", pvcServiceAccountName)
	return createOrUpdateServiceAccount(
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcServiceAccountName,
				Namespace: clusterNamespace,
			},
		},
	)
}

func createRole() error {
	logrus.Debugf("Creating/updating %s role", pxRoleName)
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
		_, err = k8sOps.UpdateRole(role)
		return err
	}
	return err
}

func createRoleBinding(clusterNamespace string) error {
	logrus.Debugf("Creating/updating %s role binding", pxRoleBindingName)
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
		_, err = k8sOps.UpdateRoleBinding(roleBinding)
		return err
	}
	return err
}

func createClusterRole() error {
	logrus.Debugf("Creating/updating %s cluster role", pxClusterRoleName)
	return createOrUpdateClusterRole(
		&rbacv1.ClusterRole{
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
		},
	)
}

func createPVCControllerClusterRole() error {
	logrus.Debugf("Creating/updating %s cluster role", pvcClusterRoleName)
	return createOrUpdateClusterRole(
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvcClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes"},
					Verbs:     []string{"create", "delete", "get", "list", "update", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims"},
					Verbs:     []string{"get", "list", "update", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"create", "delete", "get", "list", "watch"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"endpoints", "services"},
					Verbs:     []string{"create", "delete", "get", "update"},
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
					Verbs:     []string{"create", "update", "patch", "watch"},
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
	)
}

func createClusterRoleBinding(clusterNamespace string) error {
	logrus.Debugf("Creating/updating %s cluster role binding", pxClusterRoleBindingName)
	return createOrUpdateClusterRoleBinding(
		&rbacv1.ClusterRoleBinding{
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
		},
	)
}

func createPVCControllerClusterRoleBinding(clusterNamespace string) error {
	logrus.Debugf("Creating/updating %s cluster role binding", pvcClusterRoleBindingName)
	return createOrUpdateClusterRoleBinding(
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvcClusterRoleBindingName,
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
	)
}

func createSecretsNamespace() error {
	logrus.Debugf("Creating %s namespace", pxSecretsNamespace)
	_, err := k8s.Instance().CreateNamespace(pxSecretsNamespace, nil)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (p *portworx) createPortworxSerivce(t *template) error {
	logrus.Debugf("Creating/updating %s service", pxServiceName)
	k8sOps := k8s.Instance()
	labels := p.GetSelectorLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: t.cluster.Namespace,
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
		newService.Spec.Type = v1.ServiceTypeNodePort
	}

	_, err := k8sOps.CreateService(newService)
	if errors.IsAlreadyExists(err) {
		service, err := k8sOps.GetService(pxServiceName, t.cluster.Namespace)
		if err != nil {
			return fmt.Errorf("failed to get service %v/%v: %v",
				pxServiceName, t.cluster.Namespace, err)
		}
		service.Labels = newService.Labels
		service.Spec.Selector = newService.Spec.Selector
		service.Spec.Type = newService.Spec.Type
		service.Spec.Ports = newService.Spec.Ports
		if _, err = k8sOps.UpdateService(service); err != nil {
			return fmt.Errorf("failed to update service %v/%v: %v",
				pxServiceName, t.cluster.Namespace, err)
		}
		return nil
	}
	return err
}

func (p *portworx) createPVCControllerDeployment(t *template) error {
	logrus.Debugf("Creating/updating %s deployment", pvcDeploymentName)
	k8sOps := k8s.Instance()
	cpuQuantity, err := resource.ParseQuantity("200m")
	if err != nil {
		return err
	}
	containerArgs := []string{
		"kube-controller-manager",
		"--leader-elect=true",
		"--address=0.0.0.0",
		"--controllers=persistentvolume-binder,persistentvolume-expander",
		"--use-service-account-credentials=true",
	}
	if t.isOpenshift {
		containerArgs = append(containerArgs, "--leader-elect-resource-lock=endpoints")
	} else {
		containerArgs = append(containerArgs, "--leader-elect-resource-lock=configmaps")
	}

	replicas := int32(3)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	k8sVersion, err := k8sOps.GetVersion()
	if err != nil {
		return err
	}
	matches := kbVerRegex.FindStringSubmatch(k8sVersion.GitVersion)
	if len(matches) < 2 {
		return fmt.Errorf("invalid kubernetes version received: %v", k8sVersion.GitVersion)
	}
	imageRegistry := t.cluster.Annotations[annotationCustomRegistry]
	imageName := getImageURN(imageRegistry,
		"gcr.io/google_containers/kube-controller-manager-amd64:"+matches[1])

	labels := map[string]string{
		"name": pvcDeploymentName,
		"tier": "control-plane",
	}

	newDeployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcDeploymentName,
			Namespace: t.cluster.Namespace,
			Labels: map[string]string{
				"tier": "control-plane",
			},
			Annotations: map[string]string{
				"scheduler.alpha.kubernetes.io/critical-pod": "",
			},
		},
		Spec: apps.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Strategy: apps.DeploymentStrategy{
				Type: apps.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &apps.RollingUpdateDeployment{
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
							Name:    "portworx-pvc-controller-manager",
							Image:   imageName,
							Command: containerArgs,
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
													"portworx-pvc-controller",
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

	_, err = k8sOps.CreateDeployment(newDeployment)
	if errors.IsAlreadyExists(err) {
		deployment, err := k8sOps.GetDeployment(pvcDeploymentName, t.cluster.Namespace)
		if err != nil {
			return fmt.Errorf("failed to get deployment %v/%v: %v",
				pvcDeploymentName, t.cluster.Namespace, err)
		}
		deployment.Labels = newDeployment.Labels
		deployment.Annotations = newDeployment.Annotations
		deployment.Spec = newDeployment.Spec
		if _, err = k8sOps.UpdateDeployment(deployment); err != nil {
			return fmt.Errorf("failed to update deployment %v/%v: %v",
				pvcDeploymentName, t.cluster.Namespace, err)
		}
		return nil
	}
	return err
}

func createOrUpdateServiceAccount(sa *v1.ServiceAccount) error {
	_, err := k8s.Instance().CreateServiceAccount(sa)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func createOrUpdateClusterRole(cr *rbacv1.ClusterRole) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateClusterRole(cr)
	if errors.IsAlreadyExists(err) {
		_, err = k8sOps.UpdateClusterRole(cr)
		return err
	}
	return err
}

func createOrUpdateClusterRoleBinding(crb *rbacv1.ClusterRoleBinding) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateClusterRoleBinding(crb)
	if errors.IsAlreadyExists(err) {
		_, err = k8sOps.UpdateClusterRoleBinding(crb)
		return err
	}
	return err
}
