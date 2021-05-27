package component

import (
	"context"
	"fmt"
	"strings"

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LighthouseComponentName name of the Lighthouse component
	LighthouseComponentName = "Lighthouse"
	// LhServiceAccountName name of the Lighthouse service account
	LhServiceAccountName = "px-lighthouse"
	// LhClusterRoleName name of the Lighthouse cluster role
	LhClusterRoleName = "px-lighthouse"
	// LhClusterRoleBindingName name of the Lighthouse cluster role binding
	LhClusterRoleBindingName = "px-lighthouse"
	// LhServiceName name of the Lighthouse service
	LhServiceName = "px-lighthouse"
	// LhDeploymentName name of the Lighthouse deployment
	LhDeploymentName = "px-lighthouse"
	// LhContainerName name of the Lighthouse container
	LhContainerName = "px-lighthouse"
	// LhConfigInitContainerName name of the config-init container
	LhConfigInitContainerName = "config-init"
	// LhConfigSyncContainerName name of the config-sync container
	LhConfigSyncContainerName = "config-sync"
	// LhStorkConnectorContainerName name of the stork-connector container
	LhStorkConnectorContainerName = "stork-connector"
	// EnvKeyLhConfigSyncImage env variable name used to override the default
	// config-sync container image
	EnvKeyLhConfigSyncImage = "LIGHTHOUSE_CONFIG_SYNC_IMAGE"
	// EnvKeyLhStorkConnectorImage env variable name used to override the default
	// stork-connector container image
	EnvKeyLhStorkConnectorImage = "LIGHTHOUSE_STORK_CONNECTOR_IMAGE"

	defaultLhConfigSyncImage     = "portworx/lh-config-sync"
	defaultLhStorkConnectorImage = "portworx/lh-stork-connector"
)

type lighthouse struct {
	isCreated bool
	k8sClient client.Client
}

func (c *lighthouse) Name() string {
	return LighthouseComponentName
}

func (c *lighthouse) Priority() int32 {
	return DefaultComponentPriority
}

func (c *lighthouse) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *lighthouse) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.UserInterface != nil && cluster.Spec.UserInterface.Enabled
}

func (c *lighthouse) Reconcile(cluster *corev1.StorageCluster) error {
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
	if err := c.createService(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *lighthouse) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, LhServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, LhClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, LhClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, LhServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, LhDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	c.MarkDeleted()
	return nil
}

func (c *lighthouse) MarkDeleted() {
	c.isCreated = false
}

func (c *lighthouse) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            LhServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *lighthouse) createClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: LhClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"extensions", "apps"},
					Resources: []string{"deployments"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "create", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "create", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"services"},
					Verbs:     []string{"get", "list", "watch", "create"},
				},
				{
					APIGroups: []string{"stork.libopenstorage.org"},
					Resources: []string{"*"},
					Verbs:     []string{"get", "list", "create", "delete", "update"},
				},
				{
					APIGroups: []string{"monitoring.coreos.com"},
					Resources: []string{
						"alertmanagers",
						"prometheuses",
						"prometheuses/finalizers",
						"servicemonitors",
						"prometheusrules",
					},
					Verbs: []string{"*"},
				},
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{"privileged", "anyuid"},
					Verbs:         []string{"use"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.PrivilegedPSPName},
					Verbs:         []string{"use"},
				},
			},
		},
	)
}

func (c *lighthouse) createClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: LhClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      LhServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     LhClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *lighthouse) createService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getLighthouseLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            LhServiceName,
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       int32(80),
					TargetPort: intstr.FromInt(80),
				},
				{
					Name:       "https",
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}

	serviceType := pxutil.ServiceType(cluster)
	if serviceType != "" {
		newService.Spec.Type = serviceType
	} else if !pxutil.IsAKS(cluster) && !pxutil.IsGKE(cluster) && !pxutil.IsEKS(cluster) {
		newService.Spec.Type = v1.ServiceTypeNodePort
	}

	return k8sutil.CreateOrUpdateService(c.k8sClient, newService, ownerRef)
}

func (c *lighthouse) createDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	lhImage := getDesiredLighthouseImage(cluster)
	if lhImage == "" {
		return fmt.Errorf("lighthouse image cannot be empty")
	}

	existingDeployment := &appsv1.Deployment{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      LhDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	existingLhImage := k8sutil.GetImageFromDeployment(existingDeployment, LhContainerName)
	existingConfigInitImage := k8sutil.GetImageFromDeployment(existingDeployment, LhConfigInitContainerName)
	existingConfigSyncImage := k8sutil.GetImageFromDeployment(existingDeployment, LhConfigSyncContainerName)
	existingStorkConnectorImage := k8sutil.GetImageFromDeployment(existingDeployment, LhStorkConnectorContainerName)

	imageTag := ""
	partitions := strings.Split(lhImage, ":")
	if len(partitions) > 1 {
		imageTag = partitions[len(partitions)-1]
	}

	configSyncImage := k8sutil.GetValueFromEnv(EnvKeyLhConfigSyncImage, cluster.Spec.UserInterface.Env)
	if len(configSyncImage) == 0 {
		configSyncImage = defaultLhConfigSyncImage
		if len(imageTag) > 0 {
			configSyncImage = fmt.Sprintf("%s:%s", configSyncImage, imageTag)
		}
	}
	storkConnectorImage := k8sutil.GetValueFromEnv(EnvKeyLhStorkConnectorImage, cluster.Spec.UserInterface.Env)
	if len(storkConnectorImage) == 0 {
		storkConnectorImage = defaultLhStorkConnectorImage
		if len(imageTag) > 0 {
			storkConnectorImage = fmt.Sprintf("%s:%s", storkConnectorImage, imageTag)
		}
	}

	lhImage = util.GetImageURN(cluster, lhImage)
	configSyncImage = util.GetImageURN(cluster, configSyncImage)
	storkConnectorImage = util.GetImageURN(cluster, storkConnectorImage)

	modified := lhImage != existingLhImage ||
		configSyncImage != existingConfigInitImage ||
		configSyncImage != existingConfigSyncImage ||
		storkConnectorImage != existingStorkConnectorImage ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if !c.isCreated || modified {
		deployment := getLighthouseDeploymentSpec(cluster, ownerRef, lhImage, configSyncImage, storkConnectorImage)
		if err = k8sutil.CreateOrUpdateDeployment(c.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func getLighthouseDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	lhImageName string,
	configSyncImageName string,
	storkConnectorImageName string,
) *appsv1.Deployment {
	labels := getLighthouseLabels()
	replicas := int32(1)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            LhDeploymentName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          labels,
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
				},
				Spec: v1.PodSpec{
					ServiceAccountName: LhServiceAccountName,
					InitContainers: []v1.Container{
						{
							Name:            LhConfigInitContainerName,
							Image:           configSyncImageName,
							ImagePullPolicy: imagePullPolicy,
							Args:            []string{"init"},
							Env: []v1.EnvVar{
								{
									Name:  pxutil.EnvKeyPortworxNamespace,
									Value: cluster.Namespace,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:            LhContainerName,
							Image:           lhImageName,
							ImagePullPolicy: imagePullPolicy,
							Args:            []string{"-kubernetes", "true"},
							Ports: []v1.ContainerPort{
								{
									ContainerPort: int32(80),
								},
								{
									ContainerPort: int32(443),
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
						{
							Name:            LhConfigSyncContainerName,
							Image:           configSyncImageName,
							ImagePullPolicy: imagePullPolicy,
							Args:            []string{"sync"},
							Env: []v1.EnvVar{
								{
									Name:  pxutil.EnvKeyPortworxNamespace,
									Value: cluster.Namespace,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
						{
							Name:            LhStorkConnectorContainerName,
							Image:           storkConnectorImageName,
							ImagePullPolicy: imagePullPolicy,
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "config",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
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
			deployment.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
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

	return deployment
}

func getDesiredLighthouseImage(cluster *corev1.StorageCluster) string {
	if cluster.Spec.UserInterface.Image != "" {
		return cluster.Spec.UserInterface.Image
	} else if cluster.Status.DesiredImages != nil {
		return cluster.Status.DesiredImages.UserInterface
	}
	return ""
}

func getLighthouseLabels() map[string]string {
	return map[string]string{
		"tier": "px-web-console",
	}
}

// RegisterLighthouseComponent registers the Lighthouse component
func RegisterLighthouseComponent() {
	Register(LighthouseComponentName, &lighthouse{})
}

func init() {
	RegisterLighthouseComponent()
}
