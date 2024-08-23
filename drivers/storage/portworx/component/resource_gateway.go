package component

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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
	// ResourceGatewayComponentName name of the ResourceGateway component
	ResourceGatewayComponentName = "ResourceGateway"
	// resourceGatewayStr common string value for resourceGateway k8s subparts
	resourceGatewayStr = "resource-gateway"

	// names for resource-gateway kubernetes resources
	//
	// ResourceGatewayServiceAccountName name of the resourceGateway service account
	ResourceGatewayServiceAccountName = resourceGatewayStr
	// ResourceGatewayRoleName name of the resourceGateway cluster role
	ResourceGatewayRoleName = resourceGatewayStr
	// ResourceGatewayRoleBindingName name of the resourceGateway cluster role binding
	ResourceGatewayRoleBindingName = resourceGatewayStr
	// ResourceGatewayDeploymentName name of the resourceGateway deployment
	ResourceGatewayDeploymentName = resourceGatewayStr
	// ResourceGatewayServiceName name of the resourceGateway service
	ResourceGatewayServiceName = resourceGatewayStr
	// ResourceGatewayContainerName name of the resourceGateway container
	ResourceGatewayContainerName = resourceGatewayStr
	// ResourceGatewayLabelName label name for resourceGateway
	ResourceGatewayLabelName = resourceGatewayStr

	// configuration values for resource-gateway deployment and service
	//
	// resourceGatewayPortName name of the resourceGateway port
	resourceGatewayPortName = "resource-gate" // must be no more than 15 characters
	// resourceGatewayDeploymentHost is the host resourceGateway deployment listens on
	resourceGatewayDeploymentHost = "0.0.0.0"
	// resourceGatewayPort common port for resourceGateway components
	resourceGatewayPort = 50051
	// resourceGatewayDeploymentPort is the port resourceGateway deployment listens on
	resourceGatewayDeploymentPort = resourceGatewayPort
	// resourceGatewayServicePort is the port resourceGateway service listens on
	resourceGatewayServicePort = resourceGatewayPort

	// configuration for resource-gateway deployment liveliness probe
	//
	// resourceGatewayLivenessProbeInitialDelaySeconds is the initial delay for the resource-gateway liveness probe
	resourceGatewayLivenessProbeInitialDelaySeconds = 10
	// resourceGatewayLivenessProbePeriodSeconds is the period for the resource-gateway liveness probe
	resourceGatewayLivenessProbePeriodSeconds = 2
)

var defaultResourceGatewayCommandArgs = map[string]string{
	"serverHost": resourceGatewayDeploymentHost,
	"serverPort": strconv.Itoa(resourceGatewayPort),
}

// resourceGateway is the PortworxComponent implementation for the resource-gateway component
type resourceGateway struct {
	k8sClient client.Client
}

func (r *resourceGateway) Name() string {
	return ResourceGatewayComponentName
}

func (r *resourceGateway) Priority() int32 {
	return DefaultComponentPriority
}

func (r *resourceGateway) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	r.k8sClient = k8sClient
}

func (r *resourceGateway) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (r *resourceGateway) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.ResourceGateway != nil && cluster.Spec.ResourceGateway.Enabled
}

func (r *resourceGateway) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := r.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := r.createRole(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := r.createRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := r.createDeployment(cluster, ownerRef); err != nil {
		return err
	}
	if err := r.createService(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (r *resourceGateway) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(r.k8sClient, ResourceGatewayServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRole(r.k8sClient, ResourceGatewayRoleName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRoleBinding(r.k8sClient, ResourceGatewayRoleBindingName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(r.k8sClient, ResourceGatewayDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(r.k8sClient, ResourceGatewayServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (r *resourceGateway) MarkDeleted() {}

func (r *resourceGateway) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		r.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ResourceGatewayServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (r *resourceGateway) createRole(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRole(
		r.k8sClient,
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ResourceGatewayRoleName,
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

func (r *resourceGateway) createRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRoleBinding(
		r.k8sClient,
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ResourceGatewayRoleBindingName,
				Namespace: clusterNamespace,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "ServiceAccount",
					Name: ResourceGatewayServiceAccountName,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "Role",
				Name:     ResourceGatewayRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (r *resourceGateway) getImage(cluster *corev1.StorageCluster) string {
	image := cluster.Spec.ResourceGateway.Image
	if image == "" {
		image = cluster.Status.DesiredImages.ResourceGateway
	}
	if image != "" {
		image = util.GetImageURN(cluster, image)
	}
	return image
}

// getCommand returns the command to run the resource-gateway server with custom configuration
// it uses the configuration values from the StorageCluster spec and sets default values if not present
func (r *resourceGateway) getCommand(cluster *corev1.StorageCluster) []string {
	args := map[string]string{}

	// parse user arguments from the StorageCluster spec
	for k, v := range cluster.Spec.ResourceGateway.Args {
		key := strings.TrimLeft(k, "-")
		if len(key) > 0 && len(v) > 0 {
			args[key] = v
		}
	}

	// fill in the missing arguments with default values
	defaultResourceGatewayCommandArgs["namespace"] = cluster.Namespace // set namespace
	for k, v := range defaultResourceGatewayCommandArgs {
		if _, ok := args[k]; !ok {
			args[k] = v
		}
	}

	argList := make([]string, 0)
	for k, v := range args {
		argList = append(argList, fmt.Sprintf("--%s=%s", k, v))
	}
	sort.Strings(argList)

	command := append([]string{"/resource-gateway"}, argList...)
	return command
}

func (r *resourceGateway) createDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// get the existing deployment
	existingDeployment := &appsv1.Deployment{}
	err := r.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      ResourceGatewayDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	// get the target deployment
	imageName := r.getImage(cluster)
	if imageName == "" {
		return fmt.Errorf("px reosurce gateway image cannot be empty")
	}
	command := r.getCommand(cluster)
	targetDeployment := r.getResourceGatewayDeploymentSpec(cluster, ownerRef, imageName, command)

	// compare existing and target deployments and create/update the deployment if necessary
	isPodTemplateEqual, _ := util.DeepEqualPodTemplate(&targetDeployment.Spec.Template, &existingDeployment.Spec.Template)
	if !isPodTemplateEqual {
		if err = k8sutil.CreateOrUpdateDeployment(r.k8sClient, targetDeployment, ownerRef); err != nil {
			return err
		}
	}
	return nil
}

func (r *resourceGateway) createService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateService(
		r.k8sClient,
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ResourceGatewayServiceName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: v1.ServiceSpec{
				Type: v1.ServiceTypeClusterIP,
				Selector: map[string]string{
					"name": ResourceGatewayLabelName,
				},
				Ports: []v1.ServicePort{
					{
						Name:       resourceGatewayPortName,
						Port:       resourceGatewayServicePort,
						TargetPort: intstr.FromInt(resourceGatewayDeploymentPort),
						Protocol:   v1.ProtocolTCP,
					},
				},
			},
		},
		ownerRef,
	)
}

func (r *resourceGateway) getResourceGatewayDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
) *appsv1.Deployment {
	commonLabels := map[string]string{
		"name": ResourceGatewayLabelName,
		"tier": "control-plane",
	}

	replicas := int32(1)
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	// get the security environment variables
	envMap := map[string]*v1.EnvVar{}
	pxutil.PopulateSecurityEnvironmentVariables(cluster, envMap)
	envList := make([]v1.EnvVar, 0)
	for _, env := range envMap {
		envList = append(envList, *env)
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ResourceGatewayDeploymentName,
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
					ServiceAccountName: ResourceGatewayServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            ResourceGatewayContainerName,
							Image:           imageName,
							ImagePullPolicy: imagePullPolicy,
							Env:             envList,
							Command:         command,
							SecurityContext: &v1.SecurityContext{
								AllowPrivilegeEscalation: boolPtr(false),
								Privileged:               boolPtr(false),
							},
							Ports: []v1.ContainerPort{
								{
									Name:          resourceGatewayPortName,
									ContainerPort: resourceGatewayDeploymentPort,
									Protocol:      v1.ProtocolTCP,
								},
							},
							LivenessProbe: &v1.Probe{
								ProbeHandler: v1.ProbeHandler{
									GRPC: &v1.GRPCAction{
										Port: resourceGatewayServicePort,
									},
								},
								InitialDelaySeconds: resourceGatewayLivenessProbeInitialDelaySeconds,
								PeriodSeconds:       resourceGatewayLivenessProbePeriodSeconds,
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

	if cluster.Spec.ResourceGateway.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *cluster.Spec.ResourceGateway.Resources
	}

	return deployment
}

// RegisterResourceGatewayComponent registers the ResourceGateway component
func RegisterResourceGatewayComponent() {
	Register(ResourceGatewayComponentName, &resourceGateway{})
}

func init() {
	RegisterResourceGatewayComponent()
}
