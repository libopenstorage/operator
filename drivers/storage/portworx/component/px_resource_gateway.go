package component

import (
	"context"
	"fmt"
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
	// PxResourceGatewayStr common string value for pxResourceGateway k8s subparts
	PxResourceGatewayStr = "px-resource-gateway"

	// names for px-resource-gateway kubernetes resources
	//
	// PxResourceGatewayServiceAccountName name of the pxResourceGateway service account
	PxResourceGatewayServiceAccountName = PxResourceGatewayStr
	// PxResourceGatewayRoleName name of the pxResourceGateway cluster role
	PxResourceGatewayRoleName = PxResourceGatewayStr
	// PxResourceGatewayRoleBindingName name of the pxResourceGateway cluster role binding
	PxResourceGatewayRoleBindingName = PxResourceGatewayStr
	// PxResourceGatewayDeploymentName name of the pxResourceGateway deployment
	PxResourceGatewayDeploymentName = PxResourceGatewayStr
	// PxResourceGatewayServiceName name of the pxResourceGateway service
	PxResourceGatewayServiceName = PxResourceGatewayStr
	// PxResourceGatewayContainerName name of the pxResourceGateway container
	PxResourceGatewayContainerName = PxResourceGatewayStr
	// PxResourceGatewayLabelName label name for pxResourceGateway
	PxResourceGatewayLabelName = PxResourceGatewayStr

	// configuration values for px-resource-gateway deployment and service
	//
	// PxResourceGatewayPortName name of the pxResourceGateway port
	PxResourceGatewayPortName = "px-res-gate" // must be no more than 15 characters
	// PxResourceGatewayDeploymentHost is the host pxResourceGateway deployment listens on
	PxResourceGatewayDeploymentHost = "0.0.0.0"
	// PxResourceGatewayPort common port for pxResourceGateway components
	PxResourceGatewayPort = 50051
	// PxResourceGatewayDeploymentPort is the port pxResourceGateway deployment listens on
	PxResourceGatewayDeploymentPort = PxResourceGatewayPort
	// PxResourceGatewayServicePort is the port pxResourceGateway service listens on
	PxResourceGatewayServicePort = PxResourceGatewayPort

	// configuration values for px-resource-gateway server
	//
	// PxResourceGatewayConfigmapName is the name of the configmap used by px-resource-gateway
	PxResourceGatewayConfigmapName = "px-resource-gateway"
	// PxResourceGatewayConfigmapUpdatePeriod is the time period between configmap updates
	PxResourceGatewayConfigmapUpdatePeriod = "1s"
	// PxResourceGatewayDeadNodeTimeout is the time period after which a dead node is removed
	PxResourceGatewayDeadNodeTimeout = "5s"
)

var defaultPxResourceGatewayCommandArgs = map[string]string{
	"serverHost":            PxResourceGatewayDeploymentHost,
	"serverPort":            string(PxResourceGatewayPort),
	"configMapName":         PxResourceGatewayConfigmapName,
	"namespace":             "",
	"configMapUpdatePeriod": PxResourceGatewayConfigmapUpdatePeriod,
	"deadNodeTimeout":       PxResourceGatewayDeadNodeTimeout,
}

// pxResourceGateway is the PortworxComponent implementation for the px-resource-gateway component
type pxResourceGateway struct {
	k8sClient client.Client
}

func (p *pxResourceGateway) Name() string {
	return PxResourceGatewayComponentName
}

func (p *pxResourceGateway) Priority() int32 {
	return DefaultComponentPriority
}

func (p *pxResourceGateway) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	p.k8sClient = k8sClient
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
	if err := p.createRole(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createRoleBinding(cluster.Namespace, ownerRef); err != nil {
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
	if err := k8sutil.DeleteRole(p.k8sClient, PxResourceGatewayRoleName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRoleBinding(p.k8sClient, PxResourceGatewayRoleBindingName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, PxResourceGatewayDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	p.MarkDeleted()
	return nil
}

func (p *pxResourceGateway) MarkDeleted() {}

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

func (p *pxResourceGateway) createRole(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRole(
		p.k8sClient,
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PxResourceGatewayRoleName,
				Namespace: clusterNamespace,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"create", "get", "list", "patch", "update", "watch"},
				},
			},
		},
		ownerRef,
	)
}

func (p *pxResourceGateway) createRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRoleBinding(
		p.k8sClient,
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PxResourceGatewayRoleBindingName,
				Namespace: clusterNamespace,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "ServiceAccount",
					Name: PxResourceGatewayServiceAccountName,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "Role",
				Name:     PxResourceGatewayRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

// getCommand returns the command to run the px-resource-gateway server with custom configuration
// it uses the configuration values from the StorageCluster spec and sets default values if not present
func (p *pxResourceGateway) getCommand(cluster *corev1.StorageCluster) []string {
	args := map[string]string{}

	// parse user arguments from the StorageCluster spec
	for k, v := range cluster.Spec.PxResourceGateway.Args {
		key := strings.TrimLeft(k, "-")
		if len(key) > 0 && len(v) > 0 {
			args[key] = v
		}
	}

	// override some arguments with default
	args["configMapLabels"] = defaultPxResourceGatewayCommandArgs["configMapLabels"]

	// fill in the missing arguments with default values
	defaultPxResourceGatewayCommandArgs["namespace"] = cluster.Namespace // set namespace
	for k, v := range defaultPxResourceGatewayCommandArgs {
		if _, ok := args[k]; !ok {
			args[k] = v
		}
	}

	argList := make([]string, 0)
	for k, v := range args {
		argList = append(argList, fmt.Sprintf("--%s=%s", k, v))
	}
	sort.Strings(argList)

	command := append([]string{"/px-resource-gateway"}, argList...)
	return command
}

func (p *pxResourceGateway) createDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// get the existing deployment
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

	// get the target deployment
	imageName := cluster.Status.DesiredImages.PxResourceGateway
	imageName = util.GetImageURN(cluster, imageName)
	command := p.getCommand(cluster)
	targetDeployment := p.getPxResourceGatewayDeploymentSpec(cluster, ownerRef, imageName, command)

	// compare existing and target deployments and create/update the deployment if necessary
	isPodTemplateEqual, _ := util.DeepEqualPodTemplate(&targetDeployment.Spec.Template, &existingDeployment.Spec.Template)
	if !isPodTemplateEqual {
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, targetDeployment, ownerRef); err != nil {
			return err
		}
	}
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
) *appsv1.Deployment {
	commonLabels := map[string]string{
		"name": PxResourceGatewayLabelName,
		"tier": "control-plane",
	}

	replicas := int32(1)
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PxResourceGatewayDeploymentName,
			Namespace:       cluster.Namespace,
			Labels:          commonLabels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: commonLabels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: commonLabels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: PxResourceGatewayServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            PxResourceGatewayContainerName,
							Image:           imageName,
							ImagePullPolicy: imagePullPolicy,
							Command:         command,
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

	if cluster.Spec.PxResourceGateway.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *cluster.Spec.PxResourceGateway.Resources
	}
	deployment.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)

	return deployment
}

// RegisterPxResourceGatewayComponent registers the PxResourceGateway component
func RegisterPxResourceGatewayComponent() {
	Register(PxResourceGatewayComponentName, &pxResourceGateway{})
}

func init() {
	RegisterPxResourceGatewayComponent()
}
