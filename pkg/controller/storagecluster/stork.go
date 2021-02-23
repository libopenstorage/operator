package storagecluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	schedulerv1 "k8s.io/kubernetes/pkg/scheduler/api/v1"
)

const (
	storkConfigMapName               = "stork-config"
	storkServiceAccountName          = "stork"
	storkClusterRoleName             = "stork"
	storkClusterRoleBindingName      = "stork"
	storkServiceName                 = "stork-service"
	storkDeploymentName              = "stork"
	storkContainerName               = "stork"
	storkSnapshotStorageClassName    = "stork-snapshot-sc"
	storkSchedServiceAccountName     = "stork-scheduler"
	storkSchedClusterRoleName        = "stork-scheduler"
	storkSchedClusterRoleBindingName = "stork-scheduler"
	storkSchedDeploymentName         = "stork-scheduler"
	storkSchedContainerName          = "stork-scheduler"
	storkServicePort                 = 8099
)

const (
	defaultStorkCPU         = "0.1"
	annotationStorkCPU      = constants.OperatorPrefix + "/stork-cpu"
	annotationStorkSchedCPU = constants.OperatorPrefix + "/stork-scheduler-cpu"
	userVolumeNamePrefix    = "user-"
)

func (c *Controller) syncStork(
	cluster *corev1.StorageCluster,
) error {
	if cluster.Spec.Stork != nil && cluster.Spec.Stork.Enabled {
		_, err := c.Driver.GetStorkDriverName()
		if err == nil {
			if err := c.setupStork(cluster); err != nil {
				msg := fmt.Sprintf("Failed to setup Stork. %v", err)
				k8sutil.WarningEvent(c.recorder, cluster, util.FailedComponentReason, msg)
			}
			return nil
		}
		logrus.Warnf("Cannot install Stork for %s driver: %v", c.Driver.String(), err)
	}
	if err := c.removeStork(cluster); err != nil {
		msg := fmt.Sprintf("Failed to cleanup Stork. %v", err)
		k8sutil.WarningEvent(c.recorder, cluster, util.FailedComponentReason, msg)
	}
	return nil
}

func (c *Controller) setupStork(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := c.createStorkConfigMap(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkClusterRole(); err != nil {
		return err
	}
	if err := c.createStorkClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}
	if err := c.createStorkService(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkDeployment(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkSnapshotStorageClass(); err != nil {
		return err
	}
	return c.setupStorkScheduler(cluster)
}

func (c *Controller) setupStorkScheduler(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := c.createStorkSchedServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkSchedClusterRole(); err != nil {
		return err
	}
	if err := c.createStorkSchedClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}
	if err := c.createStorkSchedDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *Controller) removeStork(cluster *corev1.StorageCluster) error {
	namespace := cluster.Namespace
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := k8sutil.DeleteConfigMap(c.client, storkConfigMapName, namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(c.client, storkServiceAccountName, namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.client, storkClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.client, storkClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.client, storkServiceName, namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.client, storkDeploymentName, namespace, *ownerRef); err != nil {
		return err
	}
	c.isStorkDeploymentCreated = false
	if err := k8sutil.DeleteStorageClass(c.client, storkSnapshotStorageClassName); err != nil {
		return err
	}
	return c.removeStorkScheduler(namespace, ownerRef)
}

func (c *Controller) removeStorkScheduler(
	namespace string,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteServiceAccount(c.client, storkSchedServiceAccountName, namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.client, storkSchedClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.client, storkSchedClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.client, storkSchedDeploymentName, namespace, *ownerRef); err != nil {
		return err
	}
	c.isStorkSchedDeploymentCreated = false
	return nil
}

func (c *Controller) createStorkConfigMap(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	policy := schedulerv1.Policy{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Policy",
			APIVersion: "v1",
		},
		ExtenderConfigs: []schedulerv1.ExtenderConfig{
			{
				URLPrefix: fmt.Sprintf(
					"http://%s.%s:%d",
					storkServiceName, clusterNamespace, storkServicePort,
				),
				FilterVerb:       "filter",
				PrioritizeVerb:   "prioritize",
				Weight:           5,
				EnableHTTPS:      false,
				NodeCacheCapable: false,
				HTTPTimeout:      5 * time.Minute,
			},
		},
	}
	policyConfig, err := json.Marshal(policy)
	if err != nil {
		return err
	}

	return k8sutil.CreateOrUpdateConfigMap(
		c.client,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkConfigMapName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				"policy.cfg": string(policyConfig),
			},
		},
		ownerRef,
	)
}

func (c *Controller) createStorkSnapshotStorageClass() error {
	return k8sutil.CreateStorageClass(
		c.client,
		&storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: storkSnapshotStorageClassName,
			},
			Provisioner: "stork-snapshot",
		},
	)
}

func (c *Controller) createStorkServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.client,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *Controller) createStorkSchedServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.client,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkSchedServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *Controller) createStorkClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.client,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: storkClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"*"},
					Resources: []string{"*"},
					Verbs:     []string{"*"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.RestrictedPSPName},
					Verbs:         []string{"use"},
				},
			},
		},
	)
}

func (c *Controller) createStorkSchedClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.client,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: storkSchedClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"endpoints"},
					Verbs:     []string{"get", "create", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"", "events.k8s.io"},
					Resources: []string{"events"},
					Verbs:     []string{"create", "update", "patch"},
				},
				{
					APIGroups:     []string{""},
					ResourceNames: []string{"kube-scheduler"},
					Resources:     []string{"endpoints"},
					Verbs:         []string{"get", "delete", "update", "patch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"bindings", "pods/binding"},
					Verbs:     []string{"create"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods/status"},
					Verbs:     []string{"update", "patch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"replicationcontrollers", "services"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"apps", "extensions"},
					Resources: []string{"replicasets"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"statefulsets"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"policy"},
					Resources: []string{"poddisruptionbudgets"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims", "persistentvolumes"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses", "csinodes"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"coordination.k8s.io"},
					Resources: []string{"leases"},
					Verbs:     []string{"get", "list", "watch", "create", "update"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.RestrictedPSPName},
					Verbs:         []string{"use"},
				},
			},
		},
	)
}

func (c *Controller) createStorkClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.client,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: storkClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      storkServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     storkClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *Controller) createStorkSchedClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.client,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: storkSchedClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      storkSchedServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     storkSchedClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *Controller) createStorkService(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateService(
		c.client,
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkServiceName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: v1.ServiceSpec{
				Selector: getStorkServiceLabels(),
				Ports: []v1.ServicePort{
					{
						Name:       "extender",
						Protocol:   v1.ProtocolTCP,
						Port:       int32(storkServicePort),
						TargetPort: intstr.FromInt(storkServicePort),
					},
					{
						Name:       "webhook",
						Protocol:   v1.ProtocolTCP,
						Port:       int32(443),
						TargetPort: intstr.FromInt(443),
					},
				},
				Type: v1.ServiceTypeClusterIP,
			},
		},
		ownerRef,
	)
}

func (c *Controller) createStorkDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	storkDriverName, err := c.Driver.GetStorkDriverName()
	if err != nil {
		return err
	}
	imageName := getDesiredStorkImage(cluster)
	if imageName == "" {
		return fmt.Errorf("stork image cannot be empty")
	}

	existingDeployment := &apps.Deployment{}
	err = c.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      storkDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	targetCPU := defaultStorkCPU
	if cpuStr, ok := cluster.Annotations[annotationStorkCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	args := map[string]string{
		"verbose":                 "true",
		"leader-elect":            "true",
		"health-monitor-interval": "120",
		"lock-object-namespace":   cluster.Namespace,
	}
	for k, v := range cluster.Spec.Stork.Args {
		key := strings.TrimLeft(k, "-")
		if len(key) > 0 && len(v) > 0 {
			args[key] = v
		}
	}
	args["driver"] = storkDriverName

	argList := make([]string, 0)
	for k, v := range args {
		argList = append(argList, fmt.Sprintf("--%s=%s", k, v))
	}
	sort.Strings(argList)
	command := append([]string{"/stork"}, argList...)

	imageName = util.GetImageURN(cluster.Spec.CustomImageRegistry, imageName)
	hostNetwork := cluster.Spec.Stork.HostNetwork != nil && *cluster.Spec.Stork.HostNetwork

	envMap := c.Driver.GetStorkEnvMap(cluster)
	if envMap == nil {
		envMap = make(map[string]*v1.EnvVar)
	}
	envMap["STORK-NAMESPACE"] = &v1.EnvVar{
		Name:  "STORK-NAMESPACE",
		Value: cluster.Namespace,
	}
	for _, env := range cluster.Spec.Stork.Env {
		envMap[env.Name] = env.DeepCopy()
	}
	envVars := make([]v1.EnvVar, 0, len(envMap))
	for _, env := range envMap {
		envVars = append(envVars, *env)
	}
	sort.Sort(k8sutil.EnvByName(envVars))

	volumes, volumeMounts := getDesiredStorkVolumesAndMounts(cluster)

	var existingImage string
	var existingCommand []string
	var existingEnvs []v1.EnvVar
	var existingMounts []v1.VolumeMount
	var existingCPUQuantity resource.Quantity
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == storkContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingEnvs = append([]v1.EnvVar{}, c.Env...)
			sort.Sort(k8sutil.EnvByName(existingEnvs))
			existingMounts = append([]v1.VolumeMount{}, c.VolumeMounts...)
			sort.Sort(k8sutil.VolumeMountByName(existingMounts))
			existingCPUQuantity = c.Resources.Requests[v1.ResourceCPU]
			break
		}
	}
	existingVolumes := append([]v1.Volume{}, existingDeployment.Spec.Template.Spec.Volumes...)
	sort.Sort(k8sutil.VolumeByName(existingVolumes))

	// Check if image, envs, cpu or args are modified
	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		!reflect.DeepEqual(existingEnvs, envVars) ||
		!reflect.DeepEqual(existingVolumes, volumes) ||
		!reflect.DeepEqual(existingMounts, volumeMounts) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0 ||
		existingDeployment.Spec.Template.Spec.HostNetwork != hostNetwork ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if !c.isStorkDeploymentCreated || modified {
		deployment := c.getStorkDeploymentSpec(cluster, ownerRef, imageName,
			command, envVars, volumes, volumeMounts, targetCPUQuantity)
		if err = k8sutil.CreateOrUpdateDeployment(c.client, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isStorkDeploymentCreated = true
	return nil
}

func (c *Controller) getStorkDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	envVars []v1.EnvVar,
	volumes []v1.Volume,
	volumeMounts []v1.VolumeMount,
	cpuQuantity resource.Quantity,
) *apps.Deployment {
	pullPolicy := imagePullPolicy(cluster)
	deploymentLabels := map[string]string{
		"tier": "control-plane",
	}
	templateLabels := getStorkServiceLabels()
	for k, v := range deploymentLabels {
		templateLabels[k] = v
	}

	replicas := int32(3)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkDeploymentName,
			Namespace: cluster.Namespace,
			Annotations: map[string]string{
				"scheduler.alpha.kubernetes.io/critical-pod": "",
			},
			Labels:          deploymentLabels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: apps.DeploymentSpec{
			Strategy: apps.DeploymentStrategy{
				Type: apps.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &apps.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: templateLabels,
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
					ServiceAccountName: storkServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            storkContainerName,
							Image:           imageName,
							ImagePullPolicy: pullPolicy,
							Command:         command,
							Env:             envVars,
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
													storkDeploymentName,
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

	if cluster.Spec.Stork.HostNetwork != nil {
		deployment.Spec.Template.Spec.HostNetwork = *cluster.Spec.Stork.HostNetwork
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

	if len(volumes) > 0 {
		deployment.Spec.Template.Spec.Volumes = volumes
	}
	if len(volumeMounts) > 0 {
		deployment.Spec.Template.Spec.Containers[0].VolumeMounts = volumeMounts
	}

	return deployment
}

func (c *Controller) createStorkSchedDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	targetCPU := defaultStorkCPU
	if cpuStr, ok := cluster.Annotations[annotationStorkSchedCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	kubeSchedImage := "gcr.io/google_containers/kube-scheduler-amd64"
	if k8sutil.IsNewKubernetesRegistry(c.kubernetesVersion) {
		kubeSchedImage = "k8s.gcr.io/kube-scheduler-amd64"
	}
	imageName := util.GetImageURN(
		cluster.Spec.CustomImageRegistry,
		kubeSchedImage+":v"+c.kubernetesVersion.String(),
	)

	command := []string{
		"/usr/local/bin/kube-scheduler",
		"--address=0.0.0.0",
		"--leader-elect=true",
		"--scheduler-name=stork",
		"--policy-configmap=" + storkConfigMapName,
		"--policy-configmap-namespace=" + cluster.Namespace,
		"--lock-object-name=stork-scheduler",
	}

	existingDeployment := &apps.Deployment{}
	err = c.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      storkSchedDeploymentName,
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
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == storkSchedContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingCPUQuantity = c.Resources.Requests[v1.ResourceCPU]
		}
	}

	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0 ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if !c.isStorkSchedDeploymentCreated || modified {
		deployment := getStorkSchedDeploymentSpec(cluster, ownerRef, imageName, command, targetCPUQuantity)
		if err = k8sutil.CreateOrUpdateDeployment(c.client, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isStorkSchedDeploymentCreated = true
	return nil
}

func getStorkSchedDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	cpuQuantity resource.Quantity,
) *apps.Deployment {
	pullPolicy := imagePullPolicy(cluster)
	templateLabels := map[string]string{
		"tier":      "control-plane",
		"component": "scheduler",
	}
	deploymentLabels := map[string]string{
		"name": storkSchedDeploymentName,
	}
	for k, v := range templateLabels {
		deploymentLabels[k] = v
	}

	replicas := int32(3)

	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            storkSchedDeploymentName,
			Namespace:       cluster.Namespace,
			Labels:          deploymentLabels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: apps.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: templateLabels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:   storkSchedDeploymentName,
					Labels: templateLabels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: storkSchedServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            storkSchedDeploymentName,
							Image:           imageName,
							ImagePullPolicy: pullPolicy,
							Command:         command,
							LivenessProbe: &v1.Probe{
								InitialDelaySeconds: 15,
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(10251),
									},
								},
							},
							ReadinessProbe: &v1.Probe{
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(10251),
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
													storkSchedDeploymentName,
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

	return deployment
}

func getDesiredStorkImage(cluster *corev1.StorageCluster) string {
	if cluster.Spec.Stork.Image != "" {
		return cluster.Spec.Stork.Image
	} else if cluster.Status.DesiredImages != nil {
		return cluster.Status.DesiredImages.Stork
	}
	return ""
}

func getDesiredStorkVolumesAndMounts(
	cluster *corev1.StorageCluster,
) ([]v1.Volume, []v1.VolumeMount) {
	volumeSpecs := make([]corev1.VolumeSpec, 0)
	for _, v := range cluster.Spec.Stork.Volumes {
		vCopy := v.DeepCopy()
		vCopy.Name = userVolumeName(v.Name)
		volumeSpecs = append(volumeSpecs, *vCopy)
	}

	volumes, volumeMounts := util.ExtractVolumesAndMounts(volumeSpecs)
	sort.Sort(k8sutil.VolumeByName(volumes))
	sort.Sort(k8sutil.VolumeMountByName(volumeMounts))
	return volumes, volumeMounts
}

func getStorkServiceLabels() map[string]string {
	return map[string]string{
		"name": "stork",
	}
}

func imagePullPolicy(cluster *corev1.StorageCluster) v1.PullPolicy {
	imagePullPolicy := v1.PullAlways
	if cluster.Spec.ImagePullPolicy == v1.PullNever ||
		cluster.Spec.ImagePullPolicy == v1.PullIfNotPresent {
		imagePullPolicy = cluster.Spec.ImagePullPolicy
	}
	return imagePullPolicy
}

func userVolumeName(name string) string {
	return userVolumeNamePrefix + name
}
