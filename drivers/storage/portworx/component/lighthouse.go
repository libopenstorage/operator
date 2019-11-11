package component

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
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
	LighthouseComponentName       = "Lighthouse"
	lhServiceAccountName          = "px-lighthouse"
	lhClusterRoleName             = "px-lighthouse"
	lhClusterRoleBindingName      = "px-lighthouse"
	lhServiceName                 = "px-lighthouse"
	lhDeploymentName              = "px-lighthouse"
	lhContainerName               = "px-lighthouse"
	lhConfigInitContainerName     = "config-init"
	lhConfigSyncContainerName     = "config-sync"
	lhStorkConnectorContainerName = "stork-connector"
	envKeyLhConfigSyncImage       = "LIGHTHOUSE_CONFIG_SYNC_IMAGE"
	envKeyLhStorkConnectorImage   = "LIGHTHOUSE_STORK_CONNECTOR_IMAGE"
	defaultLhConfigSyncImage      = "portworx/lh-config-sync"
	defaultLhStorkConnectorImage  = "portworx/lh-stork-connector"
	defaultLighthouseImageTag     = "2.0.4"
)

type lighthouse struct {
	isCreated bool
	k8sClient client.Client
}

func (c *lighthouse) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *lighthouse) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return cluster.Spec.UserInterface != nil && cluster.Spec.UserInterface.Enabled
}

func (c *lighthouse) Reconcile(cluster *corev1alpha1.StorageCluster) error {
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
	if err := c.createService(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *lighthouse) Delete(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	// We don't delete the service account for Lighthouse because it is part of CSV. If
	// we disable Lighthouse then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(c.k8sClient, lhClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, lhClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, lhServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, lhDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	c.isCreated = false
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
				Name:            lhServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *lighthouse) createClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
			},
		},
		ownerRef,
	)
}

func (c *lighthouse) createClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      lhServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     lhClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (c *lighthouse) createService(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getLighthouseLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            lhServiceName,
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
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if cluster.Spec.UserInterface.Image == "" {
		return fmt.Errorf("lighthouse image cannot be empty")
	}

	existingDeployment := &appsv1.Deployment{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      lhDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	existingLhImage := k8sutil.GetImageFromDeployment(existingDeployment, lhContainerName)
	existingConfigInitImage := k8sutil.GetImageFromDeployment(existingDeployment, lhConfigInitContainerName)
	existingConfigSyncImage := k8sutil.GetImageFromDeployment(existingDeployment, lhConfigSyncContainerName)
	existingStorkConnectorImage := k8sutil.GetImageFromDeployment(existingDeployment, lhStorkConnectorContainerName)

	imageTag := defaultLighthouseImageTag
	partitions := strings.Split(cluster.Spec.UserInterface.Image, ":")
	if len(partitions) > 1 {
		imageTag = partitions[len(partitions)-1]
	}

	configSyncImage := k8sutil.GetValueFromEnv(envKeyLhConfigSyncImage, cluster.Spec.UserInterface.Env)
	if len(configSyncImage) == 0 {
		configSyncImage = fmt.Sprintf("%s:%s", defaultLhConfigSyncImage, imageTag)
	}
	storkConnectorImage := k8sutil.GetValueFromEnv(envKeyLhStorkConnectorImage, cluster.Spec.UserInterface.Env)
	if len(storkConnectorImage) == 0 {
		storkConnectorImage = fmt.Sprintf("%s:%s", defaultLhStorkConnectorImage, imageTag)
	}

	imageRegistry := cluster.Spec.CustomImageRegistry
	lhImage := util.GetImageURN(imageRegistry, cluster.Spec.UserInterface.Image)
	configSyncImage = util.GetImageURN(imageRegistry, configSyncImage)
	storkConnectorImage = util.GetImageURN(imageRegistry, storkConnectorImage)

	modified := lhImage != existingLhImage ||
		configSyncImage != existingConfigInitImage ||
		configSyncImage != existingConfigSyncImage ||
		storkConnectorImage != existingStorkConnectorImage

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
	cluster *corev1alpha1.StorageCluster,
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

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            lhDeploymentName,
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
					ServiceAccountName: lhServiceAccountName,
					InitContainers: []v1.Container{
						{
							Name:            lhConfigInitContainerName,
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
							Name:            lhContainerName,
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
							Name:            lhConfigSyncContainerName,
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
							Name:            lhStorkConnectorContainerName,
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
