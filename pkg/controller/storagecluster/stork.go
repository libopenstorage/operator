package storagecluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	apps "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
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
	annotationStorkCPU      = operatorPrefix + "/stork-cpu"
	annotationStorkSchedCPU = operatorPrefix + "/stork-scheduler-cpu"
)

func (c *Controller) syncStork(
	cluster *corev1alpha1.StorageCluster,
) error {
	if cluster.Spec.Stork != nil && cluster.Spec.Stork.Enabled {
		_, err := c.Driver.GetStorkDriverName()
		if err == nil {
			if err := c.setupStork(cluster); err != nil {
				msg := fmt.Sprintf("Failed to setup Stork. %v", err)
				c.warningEvent(cluster, util.FailedComponentReason, msg)
			}
			return nil
		}
		logrus.Warnf("Cannot install Stork for %s driver: %v", c.Driver.String(), err)
	}
	if err := c.removeStork(cluster); err != nil {
		msg := fmt.Sprintf("Failed to cleanup Stork. %v", err)
		c.warningEvent(cluster, util.FailedComponentReason, msg)
	}
	return nil
}

func (c *Controller) setupStork(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := c.createStorkConfigMap(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkClusterRole(ownerRef); err != nil {
		return err
	}
	if err := c.createStorkClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkService(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkDeployment(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkSnapshotStorageClass(ownerRef); err != nil {
		return err
	}
	return c.setupStorkScheduler(cluster)
}

func (c *Controller) setupStorkScheduler(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := c.createStorkSchedServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkSchedClusterRole(ownerRef); err != nil {
		return err
	}
	if err := c.createStorkSchedClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createStorkSchedDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *Controller) removeStork(cluster *corev1alpha1.StorageCluster) error {
	namespace := cluster.Namespace
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := k8sutil.DeleteConfigMap(c.client, storkConfigMapName, namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(c.client, storkServiceAccountName, namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.client, storkClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.client, storkClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.client, storkServiceName, namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.client, storkDeploymentName, namespace, *ownerRef); err != nil {
		return err
	}
	c.isStorkDeploymentCreated = false
	if err := k8sutil.DeleteStorageClass(c.client, storkSnapshotStorageClassName, *ownerRef); err != nil {
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
	if err := k8sutil.DeleteClusterRole(c.client, storkSchedClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.client, storkSchedClusterRoleBindingName, *ownerRef); err != nil {
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

func (c *Controller) createStorkSnapshotStorageClass(
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateStorageClass(
		c.client,
		&storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkSnapshotStorageClassName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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

func (c *Controller) createStorkClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.client,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"*"},
					Resources: []string{"*"},
					Verbs:     []string{"*"},
				},
			},
		},
		ownerRef,
	)
}

func (c *Controller) createStorkSchedClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.client,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkSchedClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
					Verbs:     []string{"get"},
				},
				{
					APIGroups: []string{""},
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
			},
		},
		ownerRef,
	)
}

func (c *Controller) createStorkClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.client,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		ownerRef,
	)
}

func (c *Controller) createStorkSchedClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.client,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkSchedClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		ownerRef,
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
						Protocol:   v1.ProtocolTCP,
						Port:       int32(storkServicePort),
						TargetPort: intstr.FromInt(storkServicePort),
					},
				},
				Type: v1.ServiceTypeClusterIP,
			},
		},
		ownerRef,
	)
}

func (c *Controller) createStorkDeployment(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	storkDriverName, err := c.Driver.GetStorkDriverName()
	if err != nil {
		return err
	}
	if cluster.Spec.Stork.Image == "" {
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

	imageName := util.GetImageURN(
		cluster.Spec.CustomImageRegistry,
		cluster.Spec.Stork.Image,
	)

	envVars := c.Driver.GetStorkEnvList(cluster)
	for _, env := range cluster.Spec.Stork.Env {
		envCopy := env.DeepCopy()
		envVars = append(envVars, *envCopy)
	}
	sort.Sort(envByName(envVars))

	var existingImage string
	var existingCommand []string
	var existingEnvs []v1.EnvVar
	var existingCPUQuantity resource.Quantity
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == storkContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingEnvs = append([]v1.EnvVar{}, c.Env...)
			sort.Sort(envByName(existingEnvs))
			existingCPUQuantity = c.Resources.Requests[v1.ResourceCPU]
			break
		}
	}

	// Check if image, envs, cpu or args are modified
	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		!reflect.DeepEqual(existingEnvs, envVars) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0

	if !c.isStorkDeploymentCreated || modified {
		deployment := c.getStorkDeploymentSpec(cluster, ownerRef, imageName,
			command, envVars, targetCPUQuantity)
		if err = k8sutil.CreateOrUpdateDeployment(c.client, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isStorkDeploymentCreated = true
	return nil
}

func (c *Controller) getStorkDeploymentSpec(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	envVars []v1.EnvVar,
	cpuQuantity resource.Quantity,
) *apps.Deployment {
	imagePullPolicy := v1.PullAlways
	if cluster.Spec.ImagePullPolicy == v1.PullNever ||
		cluster.Spec.ImagePullPolicy == v1.PullIfNotPresent {
		imagePullPolicy = cluster.Spec.ImagePullPolicy
	}

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
							ImagePullPolicy: imagePullPolicy,
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

	return deployment
}

func (c *Controller) createStorkSchedDeployment(
	cluster *corev1alpha1.StorageCluster,
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

	imageName := util.GetImageURN(cluster.Spec.CustomImageRegistry,
		"gcr.io/google_containers/kube-scheduler-amd64:v"+c.kubernetesVersion.String())

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
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0

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
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	cpuQuantity resource.Quantity,
) *apps.Deployment {
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
							Name:    storkSchedDeploymentName,
							Image:   imageName,
							Command: command,
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

	return deployment
}

func getStorkServiceLabels() map[string]string {
	return map[string]string{
		"name": "stork",
	}
}

type envByName []v1.EnvVar

func (e envByName) Len() int      { return len(e) }
func (e envByName) Swap(i, j int) { e[i], e[j] = e[j], e[i] }
func (e envByName) Less(i, j int) bool {
	return e[i].Name < e[j].Name
}
