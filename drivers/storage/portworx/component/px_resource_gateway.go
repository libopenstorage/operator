package component

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
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
	// PxResourceGatewayComponentName name of the PxResourceGateway component
	PxResourceGatewayComponentName = "PxResourceGateway"
	// PxResourceGatewayString common string value for pxResourceGateway k8s subparts
	PxResourceGatewayString = "px-resource-gateway"
	// PxResourceGatewayServiceAccountName name of the pxResourceGateway service account
	PxResourceGatewayServiceAccountName = PxResourceGatewayString
	// PxResourceGatewayClusterRoleName name of the pxResourceGateway cluster role
	PxResourceGatewayClusterRoleName = PxResourceGatewayString
	// PxResourceGatewayClusterRoleBindingName name of the pxResourceGateway cluster role binding
	PxResourceGatewayClusterRoleBindingName = PxResourceGatewayString
	// PxResourceGatewayDeploymentName name of the pxResourceGateway deployment
	PxResourceGatewayDeploymentName = PxResourceGatewayString
	// PxResourceGatewayServiceName name of the pxResourceGateway service
	PxResourceGatewayServiceName = PxResourceGatewayString
	// PxResourceGatewayContainerName name of the pxResourceGateway container
	PxResourceGatewayContainerName = PxResourceGatewayString
	// PxResourceGatewayLabelName label name for pxResourceGateway
	PxResourceGatewayLabelName = PxResourceGatewayString
	// PxResourceGatewayPortName name of the pxResourceGateway port
	PxResourceGatewayPortName = "px-res-gate" // must be no more than 15 characters
	// PxResourceGatewayPort common port for pxResourceGateway components
	PxResourceGatewayPort = 50051
	// PxResourceGatewayDeploymentPort is the port pxResourceGateway deployment listens on
	PxResourceGatewayDeploymentPort = PxResourceGatewayPort
	// PxResourceGatewayServicePort is the port pxResourceGateway service listens on
	PxResourceGatewayServicePort = PxResourceGatewayPort
	// defaultPxResourceGatewayCPU default CPU request for pxResourceGateway
	defaultPxResourceGatewayCPU = "0.1"
	// defaultPxResourceGatewayCPULimit default CPU limit for pxResourceGateway
	defaultPxResourceGatewayCPULimit = "0.25"
)

type pxResourceGateway struct {
	isCreated  bool
	k8sClient  client.Client
	k8sVersion version.Version
}

func (p *pxResourceGateway) Name() string {
	return PxResourceGatewayComponentName
}

func (p *pxResourceGateway) Priority() int32 {
	return DefaultComponentPriority
}

func (p *pxResourceGateway) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	p.k8sClient = k8sClient
	p.k8sVersion = k8sVersion
}

func (p *pxResourceGateway) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (p *pxResourceGateway) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.PxResourceGateway != nil && cluster.Spec.PxResourceGateway.Enabled
}

func (p *pxResourceGateway) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := p.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createClusterRole(); err != nil {
		return err
	}
	if err := p.createClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}
	if err := p.createDeployment(cluster, ownerRef); err != nil {
		return err
	}
	if err := p.createService(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *pxResourceGateway) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(p.k8sClient, PxResourceGatewayServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(p.k8sClient, PxResourceGatewayClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, PxResourceGatewayClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, PxResourceGatewayDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	p.MarkDeleted()
	return nil
}

func (p *pxResourceGateway) MarkDeleted() {
	p.isCreated = false
}

func (p *pxResourceGateway) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PxResourceGatewayServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *pxResourceGateway) createClusterRole() error {
	rules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"create", "get", "list", "patch", "update", "watch"},
		},
	}
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxResourceGatewayClusterRoleName,
			},
			Rules: rules,
		},
	)
}

func (p *pxResourceGateway) createClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxResourceGatewayClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      PxResourceGatewayServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     PxResourceGatewayClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (p *pxResourceGateway) createDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	imageName := p.getDesiredPxResourceGatewayImage(cluster)
	if imageName == "" {
		return fmt.Errorf("pxResourceGateway image cannot be empty")
	}

	existingDeployment := &appsv1.Deployment{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      PxResourceGatewayDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	targetCPU := defaultPxResourceGatewayCPU
	if cpuStr, ok := cluster.Annotations[pxutil.AnnotationPxResourceGatewayCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	targetCPULimitQuantity, err := resource.ParseQuantity(defaultPxResourceGatewayCPULimit)
	if err != nil {
		return err
	}

	args := map[string]string{}
	for k, v := range cluster.Spec.PxResourceGateway.Args {
		if _, exists := autopilotConfigParams[k]; exists {
			continue
		}
		key := strings.TrimLeft(k, "-")
		if len(key) > 0 && len(v) > 0 {
			args[key] = v
		}
	}

	argList := make([]string, 0)
	for k, v := range args {
		argList = append(argList, fmt.Sprintf("--%s=%s", k, v))
	}
	sort.Strings(argList)
	command := append([]string{"/px-resource-gateway"}, argList...)

	imageName = util.GetImageURN(cluster, imageName)

	var existingImage string
	var existingCommand []string
	var existingEnvs []v1.EnvVar
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == PxResourceGatewayContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingEnvs = append([]v1.EnvVar{}, c.Env...)
			sort.Sort(k8sutil.EnvByName(existingEnvs))
			break
		}
	}

	targetDeployment := p.getPxResourceGatewayDeploymentSpec(cluster, ownerRef, imageName,
		command, targetCPUQuantity, targetCPULimitQuantity)

	// Check if the deployment has changed
	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		util.HasResourcesChanged(existingDeployment.Spec.Template.Spec.Containers[0].Resources, targetDeployment.Spec.Template.Spec.Containers[0].Resources) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)
	if !p.isCreated || modified {
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, targetDeployment, ownerRef); err != nil {
			return err
		}
	}
	p.isCreated = true
	return nil
}

func (p *pxResourceGateway) createService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateService(
		p.k8sClient,
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PxResourceGatewayServiceName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: v1.ServiceSpec{
				Type: v1.ServiceTypeClusterIP,
				Selector: map[string]string{
					"name": PxResourceGatewayLabelName,
				},
				Ports: []v1.ServicePort{
					{
						Name:       PxResourceGatewayPortName,
						Port:       PxResourceGatewayServicePort,
						TargetPort: intstr.FromInt(PxResourceGatewayDeploymentPort),
						Protocol:   v1.ProtocolTCP,
					},
				},
			},
		},
		ownerRef,
	)
}

func (p *pxResourceGateway) getPxResourceGatewayDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	cpuQuantity resource.Quantity,
	cpuLimitQuantity resource.Quantity,
) *appsv1.Deployment {
	deploymentLabels := map[string]string{
		"name": PxResourceGatewayLabelName,
		"tier": "control-plane",
	}
	templateLabels := map[string]string{
		"name": PxResourceGatewayLabelName,
		"tier": "control-plane",
	}
	selectorLabels := map[string]string{
		"name": PxResourceGatewayLabelName,
		"tier": "control-plane",
	}

	replicas := int32(1)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PxResourceGatewayDeploymentName,
			Namespace: cluster.Namespace,
			Annotations: map[string]string{
				"scheduler.alpha.kubernetes.io/critical-pod": "",
			},
			Labels:          deploymentLabels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"scheduler.alpha.kubernetes.io/critical-pod": "",
					},
					Labels: templateLabels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: PxResourceGatewayServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            PxResourceGatewayContainerName,
							Image:           imageName,
							ImagePullPolicy: imagePullPolicy,
							Command:         command,
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuQuantity,
								},
								Limits: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuLimitQuantity,
								},
							},
							SecurityContext: &v1.SecurityContext{
								AllowPrivilegeEscalation: boolPtr(false),
								Privileged:               boolPtr(false),
							},
							Ports: []v1.ContainerPort{
								{
									Name:          PxResourceGatewayPortName,
									ContainerPort: PxResourceGatewayDeploymentPort,
									Protocol:      v1.ProtocolTCP,
								},
							},
							Env: []v1.EnvVar{
								{
									Name:  "PX_RESOURCE_GATEWAY_PORT",
									Value: fmt.Sprintf("%d", PxResourceGatewayDeploymentPort),
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

	// If resources is specified in the spec, the resources specified by annotation (such as portworx.io/pxResourceGateway-cpu)
	// will be overwritten.
	if cluster.Spec.PxResourceGateway.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *cluster.Spec.PxResourceGateway.Resources
	}
	deployment.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)

	return deployment
}

func (p *pxResourceGateway) getDesiredPxResourceGatewayImage(cluster *corev1.StorageCluster) string {
	if cluster.Spec.PxResourceGateway.Image != "" {
		return cluster.Spec.PxResourceGateway.Image
	} else if cluster.Status.DesiredImages != nil {
		return cluster.Status.DesiredImages.PxResourceGateway
	}
	return ""
}

// RegisterPxResourceGatewayComponent registers the PxResourceGateway component
func RegisterPxResourceGatewayComponent() {
	Register(PxResourceGatewayComponentName, &pxResourceGateway{})
}

func init() {
	RegisterPxResourceGatewayComponent()
}
