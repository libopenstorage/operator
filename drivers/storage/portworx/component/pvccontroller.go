package component

import (
	"context"
	"reflect"
	"strconv"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PVCControllerComponentName name of the PVC controller component
	PVCControllerComponentName = "PVC Controller"
	// PVCServiceAccountName name of the PVC controller service account
	PVCServiceAccountName = "portworx-pvc-controller"
	// PVCClusterRoleName name of the PVC controller cluster role
	PVCClusterRoleName = "portworx-pvc-controller"
	// PVCClusterRoleBindingName name of the PVC controller cluster role binding
	PVCClusterRoleBindingName = "portworx-pvc-controller"
	// PVCDeploymentName name of the PVC controller deployment
	PVCDeploymentName = "portworx-pvc-controller"

	pvcContainerName        = "portworx-pvc-controller-manager"
	defaultPVCControllerCPU = "200m"
)

type pvcController struct {
	isCreated  bool
	k8sClient  client.Client
	k8sVersion version.Version
}

func (c *pvcController) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.k8sVersion = k8sVersion
}

func (c *pvcController) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[pxutil.AnnotationPVCController])
	if err == nil {
		return enabled
	}

	// Enable PVC controller for managed kubernetes services. Also enable it for openshift,
	// only if Portworx service is not deployed in kube-system namespace.
	if pxutil.IsPKS(cluster) || pxutil.IsEKS(cluster) ||
		pxutil.IsGKE(cluster) || pxutil.IsAKS(cluster) ||
		(pxutil.IsOpenshift(cluster) && cluster.Namespace != "kube-system") {
		return true
	}
	return false
}

func (c *pvcController) Reconcile(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createClusterRole(ownerRef); err != nil {
		return err
	}
	if err := c.createClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *pvcController) Delete(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	// We don't delete the service account for PVC controller because it is part of CSV. If
	// we disable PVC controller then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(c.k8sClient, PVCClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PVCClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, PVCDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	c.isCreated = false
	return nil
}

func (c *pvcController) MarkDeleted() {
	c.isCreated = false
}

func (c *pvcController) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PVCServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *pvcController) createClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PVCClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes"},
					Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims"},
					Verbs:     []string{"get", "list", "watch", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch", "create", "delete"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"endpoints", "services"},
					Verbs:     []string{"get", "create", "delete", "update"},
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
					Verbs:     []string{"watch", "create", "update", "patch"},
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

func (c *pvcController) createClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PVCClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      PVCServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     PVCClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (c *pvcController) createDeployment(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	targetCPU := defaultPVCControllerCPU
	if cpuStr, ok := cluster.Annotations[pxutil.AnnotationPVCControllerCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	imageName := util.GetImageURN(cluster.Spec.CustomImageRegistry,
		"gcr.io/google_containers/kube-controller-manager-amd64:v"+c.k8sVersion.String())

	command := []string{
		"kube-controller-manager",
		"--leader-elect=true",
		"--address=0.0.0.0",
		"--controllers=persistentvolume-binder,persistentvolume-expander",
		"--use-service-account-credentials=true",
	}
	if pxutil.IsOpenshift(cluster) {
		command = append(command, "--leader-elect-resource-lock=endpoints")
	} else {
		command = append(command, "--leader-elect-resource-lock=configmaps")
	}

	existingDeployment := &appsv1.Deployment{}
	err = c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      PVCDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var existingImage string
	var existingCommand []string
	var existingCPUQuantity resource.Quantity
	for _, container := range existingDeployment.Spec.Template.Spec.Containers {
		if container.Name == pvcContainerName {
			existingImage = container.Image
			existingCommand = container.Command
			existingCPUQuantity = container.Resources.Requests[v1.ResourceCPU]
		}
	}

	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0

	if !c.isCreated || modified {
		deployment := getPVCControllerDeploymentSpec(cluster, ownerRef, imageName, command, targetCPUQuantity)
		if err = k8sutil.CreateOrUpdateDeployment(c.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func getPVCControllerDeploymentSpec(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	cpuQuantity resource.Quantity,
) *appsv1.Deployment {
	replicas := int32(3)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	labels := map[string]string{
		"name": PVCDeploymentName,
		"tier": "control-plane",
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PVCDeploymentName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels: map[string]string{
				"tier": "control-plane",
			},
			Annotations: map[string]string{
				"scheduler.alpha.kubernetes.io/critical-pod": "",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
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
					ServiceAccountName: PVCServiceAccountName,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:    pvcContainerName,
							Image:   imageName,
							Command: command,
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
													PVCDeploymentName,
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
}

// RegisterPVCControllerComponent registers the PVC Controller component
func RegisterPVCControllerComponent() {
	Register(PVCControllerComponentName, &pvcController{})
}

func init() {
	RegisterPVCControllerComponent()
}
