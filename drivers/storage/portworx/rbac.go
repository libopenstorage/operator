package portworx

import (
	"fmt"
	"regexp"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1beta2"
	v1 "k8s.io/api/core/v1"
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
	pxServiceName             = "portworx-service"
	pvcServiceAccountName     = "portworx-pvc-controller"
	pvcClusterRoleName        = "portworx-pvc-controller"
	pvcClusterRoleBindingName = "portworx-pvc-controller"
	pvcDeploymentName         = "portworx-pvc-controller"
)

var (
	kbVerRegex     = regexp.MustCompile(`^(v\d+\.\d+\.\d+).*`)
	controllerKind = corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
)

func (p *portworx) PreInstall(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)

	if !p.serviceAccountCreated {
		if err := createServiceAccount(cluster.Namespace, ownerRef); err != nil {
			return err
		}
		p.serviceAccountCreated = true
	}
	if !p.clusterRoleCreated {
		if err := createClusterRole(ownerRef); err != nil {
			return err
		}
		p.clusterRoleCreated = true
	}
	if !p.clusterRoleBindingCreated {
		if err := createClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
			return err
		}
		p.clusterRoleBindingCreated = true
	}
	if !p.roleCreated {
		if err := createRole(cluster.Namespace, ownerRef); err != nil {
			return err
		}
		p.roleCreated = true
	}
	if !p.roleBindingCreated {
		if err := createRoleBinding(cluster.Namespace, ownerRef); err != nil {
			return err
		}
		p.roleBindingCreated = true
	}

	t, err := newTemplate(cluster)
	if err != nil {
		return err
	}

	if !p.portworxSerivceCreated {
		if err := p.createPortworxSerivce(t, ownerRef); err != nil {
			return err
		}
		p.portworxSerivceCreated = true
	}

	if t.needsPVCController {
		if !p.pvcControllerServiceAccountCreated {
			if err := createPVCControllerServiceAccount(cluster.Namespace, ownerRef); err != nil {
				return err
			}
			p.pvcControllerServiceAccountCreated = true
		}
		if !p.pvcControllerClusterRoleCreated {
			if err := createPVCControllerClusterRole(ownerRef); err != nil {
				return err
			}
			p.pvcControllerClusterRoleCreated = true
		}
		if !p.pvcControllerClusterRoleBindingCreated {
			if err := createPVCControllerClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
				return err
			}
			p.pvcControllerClusterRoleBindingCreated = true
		}
		if !p.pvcControllerDeploymentCreated {
			if err := p.createPVCControllerDeployment(t, ownerRef); err != nil {
				return err
			}
			p.pvcControllerDeploymentCreated = true
		}
	}

	return nil
}
func (p *portworx) unsetInstallParams(cluster *corev1alpha1.StorageCluster) error {
	p.serviceAccountCreated = false
	p.clusterRoleCreated = false
	p.clusterRoleBindingCreated = false
	p.roleCreated = false
	p.roleBindingCreated = false
	p.portworxSerivceCreated = false
	p.pvcControllerServiceAccountCreated = false
	p.pvcControllerClusterRoleCreated = false
	p.pvcControllerClusterRoleBindingCreated = false
	p.pvcControllerDeploymentCreated = false
	return nil
}

func createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Debugf("Creating/updating %s service account", pxServiceAccountName)
	return createOrUpdateServiceAccount(
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func createPVCControllerServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Debugf("Creating/updating %s service account", pvcServiceAccountName)
	return createOrUpdateServiceAccount(
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func createRole(clusterNamespace string, ownerRef *metav1.OwnerReference) error {
	logrus.Debugf("Creating/updating %s role", pxRoleName)
	return createOrUpdateRole(
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

func createRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Debugf("Creating/updating %s role binding", pxRoleBindingName)
	return createOrUpdateRoleBinding(
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxRoleBindingName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		},
		ownerRef,
	)
}

func createClusterRole(ownerRef *metav1.OwnerReference) error {
	logrus.Debugf("Creating/updating %s cluster role", pxClusterRoleName)
	return createOrUpdateClusterRole(
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
		ownerRef,
	)
}

func createPVCControllerClusterRole(ownerRef *metav1.OwnerReference) error {
	logrus.Debugf("Creating/updating %s cluster role", pvcClusterRoleName)
	return createOrUpdateClusterRole(
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		ownerRef,
	)
}

func createClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Debugf("Creating/updating %s cluster role binding", pxClusterRoleBindingName)
	return createOrUpdateClusterRoleBinding(
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		ownerRef,
	)
}

func createPVCControllerClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Debugf("Creating/updating %s cluster role binding", pvcClusterRoleBindingName)
	return createOrUpdateClusterRoleBinding(
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		ownerRef,
	)
}

func (p *portworx) createPortworxSerivce(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Debugf("Creating/updating %s service", pxServiceName)
	labels := p.GetSelectorLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxServiceName,
			Namespace:       t.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
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

	return createOrUpdateService(newService, ownerRef)
}

func (p *portworx) createPVCControllerDeployment(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Debugf("Creating/updating %s deployment", pvcDeploymentName)
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

	k8sVersion, err := k8s.Instance().GetVersion()
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
			Name:            pvcDeploymentName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
													pvcDeploymentName,
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

	return createOrUpdateDeployment(newDeployment, ownerRef)
}

func createOrUpdateServiceAccount(
	sa *v1.ServiceAccount,
	ownerRef *metav1.OwnerReference,
) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateServiceAccount(sa)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	existingSA, err := k8sOps.GetServiceAccount(sa.Name, sa.Namespace)
	if err != nil {
		return err
	}

	sa.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	for _, o := range existingSA.OwnerReferences {
		if o.UID != ownerRef.UID {
			sa.OwnerReferences = append(sa.OwnerReferences, o)
		}
	}

	_, err = k8sOps.UpdateServiceAccount(sa)
	return err
}

func createOrUpdateRole(
	role *rbacv1.Role,
	ownerRef *metav1.OwnerReference,
) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateRole(role)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	existingRole, err := k8sOps.GetRole(role.Name, role.Namespace)
	if err != nil {
		return err
	}

	role.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	for _, o := range existingRole.OwnerReferences {
		if o.UID != ownerRef.UID {
			role.OwnerReferences = append(role.OwnerReferences, o)
		}
	}

	_, err = k8sOps.UpdateRole(role)
	return err
}

func createOrUpdateRoleBinding(
	rb *rbacv1.RoleBinding,
	ownerRef *metav1.OwnerReference,
) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateRoleBinding(rb)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	existingRB, err := k8sOps.GetRoleBinding(rb.Name, rb.Namespace)
	if err != nil {
		return err
	}

	rb.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	for _, o := range existingRB.OwnerReferences {
		if o.UID != ownerRef.UID {
			rb.OwnerReferences = append(rb.OwnerReferences, o)
		}
	}

	_, err = k8sOps.UpdateRoleBinding(rb)
	return err
}

func createOrUpdateClusterRole(
	cr *rbacv1.ClusterRole,
	ownerRef *metav1.OwnerReference,
) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateClusterRole(cr)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	existingCR, err := k8sOps.GetClusterRole(cr.Name)
	if err != nil {
		return err
	}

	cr.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	for _, o := range existingCR.OwnerReferences {
		if o.UID != ownerRef.UID {
			cr.OwnerReferences = append(cr.OwnerReferences, o)
		}
	}

	_, err = k8sOps.UpdateClusterRole(cr)
	return err
}

func createOrUpdateClusterRoleBinding(
	crb *rbacv1.ClusterRoleBinding,
	ownerRef *metav1.OwnerReference,
) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateClusterRoleBinding(crb)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	existingCRB, err := k8sOps.GetClusterRoleBinding(crb.Name)
	if err != nil {
		return err
	}

	crb.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	for _, o := range existingCRB.OwnerReferences {
		if o.UID != ownerRef.UID {
			crb.OwnerReferences = append(crb.OwnerReferences, o)
		}
	}

	_, err = k8sOps.UpdateClusterRoleBinding(crb)
	return err
}

func createOrUpdateService(
	svc *v1.Service,
	ownerRef *metav1.OwnerReference,
) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateService(svc)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	existingSvc, err := k8sOps.GetService(svc.Name, svc.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get service %v/%v: %v",
			svc.Name, svc.Namespace, err)
	}

	existingSvc.Labels = svc.Labels
	existingSvc.Spec.Selector = svc.Spec.Selector
	existingSvc.Spec.Type = svc.Spec.Type
	existingSvc.Spec.Ports = svc.Spec.Ports

	ownerRefPresent := false
	for _, o := range existingSvc.OwnerReferences {
		if o.UID == ownerRef.UID {
			ownerRefPresent = true
			break
		}
	}
	if !ownerRefPresent {
		if existingSvc.OwnerReferences == nil {
			existingSvc.OwnerReferences = make([]metav1.OwnerReference, 0)
		}
		existingSvc.OwnerReferences = append(existingSvc.OwnerReferences, *ownerRef)
	}

	_, err = k8sOps.UpdateService(existingSvc)
	return err
}

func createOrUpdateDeployment(
	deployment *apps.Deployment,
	ownerRef *metav1.OwnerReference,
) error {
	k8sOps := k8s.Instance()
	_, err := k8sOps.CreateDeployment(deployment)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	existingDeployment, err := k8sOps.GetDeployment(deployment.Name, deployment.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get service %v/%v: %v",
			deployment.Name, deployment.Namespace, err)
	}

	existingDeployment.Labels = deployment.Labels
	existingDeployment.Annotations = deployment.Annotations
	existingDeployment.Spec = deployment.Spec

	ownerRefPresent := false
	for _, o := range existingDeployment.OwnerReferences {
		if o.UID == ownerRef.UID {
			ownerRefPresent = true
			break
		}
	}
	if !ownerRefPresent {
		if existingDeployment.OwnerReferences == nil {
			existingDeployment.OwnerReferences = make([]metav1.OwnerReference, 0)
		}
		existingDeployment.OwnerReferences = append(existingDeployment.OwnerReferences, *ownerRef)
	}

	_, err = k8sOps.UpdateDeployment(existingDeployment)
	return err
}
