package component

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
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

	defaultPVCControllerInsecurePort = "10252"
	defaultPVCControllerSecurePort   = "10257"

	// AksPVCControllerInsecurePort is the PVC controller port and health check port, due to the default port
	// is already used on AKS we use different port for AKS.
	AksPVCControllerInsecurePort = "10260"
	// AksPVCControllerSecurePort is the PVC controller secure port.
	AksPVCControllerSecurePort = "10261"
)

var (
	pvcControllerDeploymentTemplateLabels = map[string]string{
		"name": PVCDeploymentName,
		"tier": "control-plane",
	}
)

type pvcController struct {
	isCreated  bool
	k8sClient  client.Client
	k8sVersion version.Version
}

func (c *pvcController) Name() string {
	return PVCControllerComponentName
}

func (c *pvcController) Priority() int32 {
	return DefaultComponentPriority
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

func (c *pvcController) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *pvcController) IsEnabled(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[pxutil.AnnotationPVCController])
	if err == nil {
		return enabled
	}

	// If portworx is disabled, then do not run pvc controller unless explicitly told to.
	if !pxutil.IsPortworxEnabled(cluster) {
		return false
	}

	// Enable PVC controller for managed kubernetes services. Also enable it
	// if Portworx is not deployed in kube-system namespace.
	if pxutil.IsPKS(cluster) || pxutil.IsEKS(cluster) ||
		pxutil.IsGKE(cluster) || pxutil.IsAKS(cluster) ||
		pxutil.IsOKE(cluster) || cluster.Namespace != "kube-system" {
		return true
	}
	return false
}

func (c *pvcController) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createClusterRole(); err != nil {
		return err
	}
	if err := c.createClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}
	if err := c.createDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *pvcController) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, PVCServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, PVCClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PVCClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, PVCDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	c.MarkDeleted()
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

func (c *pvcController) createClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: PVCClusterRoleName,
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
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"", "events.k8s.io"},
					Resources: []string{"events"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"serviceaccounts"},
					Verbs:     []string{"get", "create"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"serviceaccounts/token"},
					Verbs:     []string{"create"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "list", "watch", "create", "update"},
				},
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{PxRestrictedSCCName},
					Verbs:         []string{"use"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.PrivilegedPSPName},
					Verbs:         []string{"use"},
				},
				{
					APIGroups: []string{"coordination.k8s.io"},
					Resources: []string{"leases"},
					Verbs:     []string{"*"},
				},
			},
		},
	)
}

func (c *pvcController) createClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: PVCClusterRoleBindingName,
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
	)
}

func (c *pvcController) createDeployment(
	cluster *corev1.StorageCluster,
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

	imageName := "gcr.io/google_containers/kube-controller-manager-amd64"
	if k8sutil.IsNewKubernetesRegistry(&c.k8sVersion) {
		imageName = k8sutil.DefaultK8SRegistryPath + "/kube-controller-manager-amd64"
	}
	imageName = imageName + ":v" + c.k8sVersion.String()
	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.KubeControllerManager != "" {
		imageName = cluster.Status.DesiredImages.KubeControllerManager
	}
	imageName = util.GetImageURN(cluster, imageName)

	command := []string{
		"kube-controller-manager",
		"--leader-elect=true",
		"--controllers=persistentvolume-binder,persistentvolume-expander",
		"--use-service-account-credentials=true",
		"--leader-elect-resource-name=portworx-pvc-controller",
		fmt.Sprintf("--leader-elect-resource-namespace=%s", cluster.Namespace),
	}

	if c.k8sVersion.LessThan(k8sutil.K8sVer1_22) {
		command = append(command, "--address=0.0.0.0")
		if port, ok := cluster.Annotations[pxutil.AnnotationPVCControllerPort]; ok && port != "" {
			command = append(command, "--port="+port)
		} else if pxutil.IsAKS(cluster) {
			command = append(command, "--port="+AksPVCControllerInsecurePort)
		}
	}

	if securePort, ok := cluster.Annotations[pxutil.AnnotationPVCControllerSecurePort]; ok && securePort != "" {
		command = append(command, "--secure-port="+securePort)
	} else if pxutil.IsAKS(cluster) {
		command = append(command, "--secure-port="+AksPVCControllerSecurePort)
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

	updatedTopologySpreadConstraints, err := util.GetTopologySpreadConstraints(c.k8sClient, pvcControllerDeploymentTemplateLabels)
	if err != nil {
		return err
	}

	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0 ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations) ||
		util.HaveTopologySpreadConstraintsChanged(updatedTopologySpreadConstraints,
			existingDeployment.Spec.Template.Spec.TopologySpreadConstraints)

	if !c.isCreated || modified {
		deployment := c.getPVCControllerDeploymentSpec(cluster, ownerRef, imageName, command, targetCPUQuantity,
			updatedTopologySpreadConstraints)
		if err = k8sutil.CreateOrUpdateDeployment(c.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func (c *pvcController) getPVCControllerDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	cpuQuantity resource.Quantity,
	topologySpreadConstraints []v1.TopologySpreadConstraint,
) *appsv1.Deployment {
	replicas := int32(3)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	healthCheckPort := defaultPVCControllerInsecurePort
	healthCheckScheme := v1.URISchemeHTTP
	if c.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) {
		if port, ok := cluster.Annotations[pxutil.AnnotationPVCControllerSecurePort]; ok && port != "" {
			healthCheckPort = port
		} else if pxutil.IsAKS(cluster) {
			healthCheckPort = AksPVCControllerSecurePort
		} else {
			healthCheckPort = defaultPVCControllerSecurePort
		}
		healthCheckScheme = v1.URISchemeHTTPS
	} else if port, ok := cluster.Annotations[pxutil.AnnotationPVCControllerPort]; ok && port != "" {
		healthCheckPort = port
	} else if pxutil.IsAKS(cluster) {
		healthCheckPort = AksPVCControllerInsecurePort
	}

	deployment := &appsv1.Deployment{
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
				MatchLabels: pvcControllerDeploymentTemplateLabels,
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
					Labels: pvcControllerDeploymentTemplateLabels,
					Annotations: map[string]string{
						"scheduler.alpha.kubernetes.io/critical-pod": "",
					},
				},
				Spec: v1.PodSpec{
					ServiceAccountName: PVCServiceAccountName,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:            pvcContainerName,
							Image:           imageName,
							ImagePullPolicy: pxutil.ImagePullPolicy(cluster),
							Command:         command,
							LivenessProbe: &v1.Probe{
								FailureThreshold:    8,
								TimeoutSeconds:      15,
								InitialDelaySeconds: 15,
								ProbeHandler: v1.ProbeHandler{
									HTTPGet: &v1.HTTPGetAction{
										Host:   "127.0.0.1",
										Path:   "/healthz",
										Port:   intstr.Parse(healthCheckPort),
										Scheme: healthCheckScheme,
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

	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		deployment.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *cluster.Spec.ImagePullSecret,
			},
		)
	}

	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			deployment.Spec.Template.Spec.Affinity.NodeAffinity =
				cluster.Spec.Placement.NodeAffinity.DeepCopy()
		}

		if len(cluster.Spec.Placement.Tolerations) > 0 {
			deployment.Spec.Template.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range cluster.Spec.Placement.Tolerations {
				deployment.Spec.Template.Spec.Tolerations = append(
					deployment.Spec.Template.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	if len(topologySpreadConstraints) != 0 {
		deployment.Spec.Template.Spec.TopologySpreadConstraints = topologySpreadConstraints
	}

	return deployment
}

// RegisterPVCControllerComponent registers the PVC Controller component
func RegisterPVCControllerComponent() {
	Register(PVCControllerComponentName, &pvcController{})
}

func init() {
	RegisterPVCControllerComponent()
}
