package storagecluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
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
	schedcomp "k8s.io/component-base/config/v1alpha1"
	schedconfig "k8s.io/kube-scheduler/config/v1beta3"
	schedconfigapi "k8s.io/kubernetes/pkg/scheduler/apis/config/v1beta3"
	"sigs.k8s.io/yaml"
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

	defaultKubeSchedulerPort = 10259

	// K8S scheduler policy decoder changed in this version.
	// https://github.com/kubernetes/kubernetes/blob/release-1.21/pkg/scheduler/scheduler.go#L306
	policyDecoderChangeVersion = "1.17.0"
	// Stork scheduler cannot run with kube-scheduler image > v1.22
	pinnedStorkSchedulerVersion                = "1.21.4"
	minK8sVersionForPinnedStorkScheduler       = "1.22.0"
	minK8sVersionForKubeSchedulerConfiguration = "1.23.0"
)

const (
	defaultStorkCPU         = "0.1"
	annotationStorkCPU      = constants.OperatorPrefix + "/stork-cpu"
	annotationStorkSchedCPU = constants.OperatorPrefix + "/stork-scheduler-cpu"
	userVolumeNamePrefix    = "user-"
)

var (
	storkServiceLabels = map[string]string{
		"name": "stork",
	}
	storkDeploymentLabels = map[string]string{
		"tier": "control-plane",
	}
	storkTemplateLabels = map[string]string{
		"name": "stork",
		"tier": "control-plane",
	}
	storkSchedulerDeploymentLabels = map[string]string{
		"tier":      "control-plane",
		"component": "scheduler",
		"name":      storkSchedDeploymentName,
	}
)

// SchedulerPolicy is a policy config for kubernetes scheduler. As the policy object
// is deprecated in k8s, we are adding it here locally until we migrate to the new
// KubeSchedulerConfiguration object
type SchedulerPolicy struct {
	Kind       string              `json:"kind"`
	APIVersion string              `json:"apiVersion"`
	Extenders  []SchedulerExtender `json:"extenders"`
}

// SchedulerExtender is the configuration to communicate with the external extender
type SchedulerExtender struct {
	URLPrefix        string `json:"urlPrefix,omitempty"`
	FilterVerb       string `json:"filterVerb,omitempty"`
	PrioritizeVerb   string `json:"prioritizeVerb,omitempty"`
	Weight           int64  `json:"weight,omitempty"`
	EnableHTTPS      bool   `json:"enableHttps,omitempty"`
	NodeCacheCapable bool   `json:"nodeCacheCapable,omitempty"`
	HTTPTimeout      int64  `json:"httpTimeout,omitempty"`
}

func (c *Controller) syncStork(
	cluster *corev1.StorageCluster,
) error {
	if util.ComponentsPausedForMigration(cluster) {
		return nil
	}

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
	policy := SchedulerPolicy{
		Kind:       "Policy",
		APIVersion: "kubescheduler.config.k8s.io/v1",
		Extenders: []SchedulerExtender{
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
				HTTPTimeout:      metav1.Duration{Duration: 5 * time.Minute}.Nanoseconds(),
			},
		},
	}

	leaderElect := true
	schedulerName := storkDeploymentName
	kubeSchedulerConfiguration := schedconfig.KubeSchedulerConfiguration{
		TypeMeta: metav1.TypeMeta{
			Kind:       "KubeSchedulerConfiguration",
			APIVersion: "kubescheduler.config.k8s.io/v1beta3",
		},
		LeaderElection: schedcomp.LeaderElectionConfiguration{
			LeaderElect:       &leaderElect,
			ResourceNamespace: clusterNamespace,
			ResourceName:      storkSchedDeploymentName,
			LeaseDuration:     metav1.Duration{Duration: 15 * time.Second},
			RenewDeadline:     metav1.Duration{Duration: 10 * time.Second},
			RetryPeriod:       metav1.Duration{Duration: 2 * time.Second},
			ResourceLock:      "leases",
		},
		Profiles: []schedconfig.KubeSchedulerProfile{
			{
				SchedulerName: &schedulerName,
			},
		},
		Extenders: []schedconfig.Extender{
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
				HTTPTimeout:      metav1.Duration{Duration: 5 * time.Minute},
			},
		},
	}

	// Auto fill the default configuration params
	schedconfigapi.SetDefaults_KubeSchedulerConfiguration(&kubeSchedulerConfiguration)

	k8sMinVersionForKubeSchedulerConfiguration, err := version.NewVersion(minK8sVersionForKubeSchedulerConfiguration)
	if err != nil {
		logrus.WithError(err).Errorf("Could not parse version %s", k8sMinVersionForKubeSchedulerConfiguration)
		return err
	}
	var policyConfig []byte
	var dataKey string
	if c.kubernetesVersion.GreaterThanOrEqual(k8sMinVersionForKubeSchedulerConfiguration) {
		policyConfig, err = yaml.Marshal(kubeSchedulerConfiguration)
		if err != nil {
			logrus.WithError(err).Errorf("Could not encode policy object")
			return err
		}
		dataKey = "stork-config.yaml"
	} else {
		policyConfig, err = json.Marshal(policy)
		if err != nil {
			logrus.WithError(err).Errorf("Could not encode policy object")
			return err
		}
		dataKey = "policy.cfg"
	}

	_, err = k8sutil.CreateOrUpdateConfigMap(
		c.client,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            storkConfigMapName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				dataKey: string(policyConfig),
			},
		},
		ownerRef,
	)
	return err
}

func (c *Controller) createStorkSnapshotStorageClass() error {
	allowVolumeExpansion := true
	return k8sutil.CreateStorageClass(
		c.client,
		&storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: storkSnapshotStorageClassName,
			},
			Provisioner:          "stork-snapshot",
			AllowVolumeExpansion: &allowVolumeExpansion,
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
					Resources: []string{"nodes", "namespaces"},
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
					Verbs:     []string{"get", "list", "watch", "update"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses", "csinodes", "csidrivers", "csistoragecapacities"},
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
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{component.PxSCCName},
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
				Selector: storkServiceLabels,
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

	imageName = util.GetImageURN(cluster, imageName)
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
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == storkContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingEnvs = append([]v1.EnvVar{}, c.Env...)
			sort.Sort(k8sutil.EnvByName(existingEnvs))
			existingMounts = append([]v1.VolumeMount{}, c.VolumeMounts...)
			sort.Sort(k8sutil.VolumeMountByName(existingMounts))
			break
		}
	}
	existingVolumes := append([]v1.Volume{}, existingDeployment.Spec.Template.Spec.Volumes...)
	sort.Sort(k8sutil.VolumeByName(existingVolumes))

	updatedTopologySpreadConstraints, err := util.GetTopologySpreadConstraints(c.client, storkTemplateLabels)
	if err != nil {
		return err
	}

	deployment := c.getStorkDeploymentSpec(cluster, ownerRef, imageName,
		command, envVars, volumes, volumeMounts, targetCPUQuantity, updatedTopologySpreadConstraints)

	// Check if image, envs, cpu or args are modified
	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		!reflect.DeepEqual(existingEnvs, envVars) ||
		!reflect.DeepEqual(existingVolumes, volumes) ||
		!reflect.DeepEqual(existingMounts, volumeMounts) ||
		util.HasResourcesChanged(existingDeployment.Spec.Template.Spec.Containers[0].Resources, deployment.Spec.Template.Spec.Containers[0].Resources) ||
		existingDeployment.Spec.Template.Spec.HostNetwork != hostNetwork ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations) ||
		util.HaveTopologySpreadConstraintsChanged(updatedTopologySpreadConstraints,
			existingDeployment.Spec.Template.Spec.TopologySpreadConstraints)

	if !c.isStorkDeploymentCreated || modified {
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
	topologySpreadConstraints []v1.TopologySpreadConstraint,
) *apps.Deployment {
	pullPolicy := imagePullPolicy(cluster)
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
			Labels:          storkDeploymentLabels,
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
				MatchLabels: storkTemplateLabels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"scheduler.alpha.kubernetes.io/critical-pod": "",
					},
					Labels: storkTemplateLabels,
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

	if len(topologySpreadConstraints) != 0 {
		deployment.Spec.Template.Spec.TopologySpreadConstraints = topologySpreadConstraints
	}

	// If resources is specified in the spec, the resources specified by annotation (such as portworx.io/stork-cpu)
	// will be overwritten.
	if cluster.Spec.Stork.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *cluster.Spec.Stork.Resources
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
		kubeSchedImage = k8sutil.DefaultK8SRegistryPath + "/kube-scheduler-amd64"
	}

	k8sMinVersionForPinnedStorkScheduler, err := version.NewVersion(minK8sVersionForPinnedStorkScheduler)
	if err != nil {
		logrus.WithError(err).Errorf("Could not parse version %s", k8sMinVersionForPinnedStorkScheduler)
		return err
	}

	k8sMinVersionForKubeSchedulerConfiguration, err := version.NewVersion(minK8sVersionForKubeSchedulerConfiguration)
	if err != nil {
		logrus.WithError(err).Errorf("Could not parse version %s", k8sMinVersionForKubeSchedulerConfiguration)
		return err
	}

	if c.kubernetesVersion.GreaterThanOrEqual(k8sMinVersionForPinnedStorkScheduler) &&
		c.kubernetesVersion.LessThan(k8sMinVersionForKubeSchedulerConfiguration) {
		kubeSchedImage = kubeSchedImage + ":v" + pinnedStorkSchedulerVersion
	} else {
		kubeSchedImage = kubeSchedImage + ":v" + c.kubernetesVersion.String()
	}
	imageName := kubeSchedImage
	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.KubeScheduler != "" {
		imageName = cluster.Status.DesiredImages.KubeScheduler
	}
	imageName = util.GetImageURN(cluster, imageName)

	var command []string
	if c.kubernetesVersion.GreaterThanOrEqual(k8sMinVersionForKubeSchedulerConfiguration) {
		command = []string{
			"/usr/local/bin/kube-scheduler",
			"--bind-address=0.0.0.0",
			"--config=/etc/kubernetes/stork-config.yaml",
		}
	} else {
		command = []string{
			"/usr/local/bin/kube-scheduler",
			"--address=0.0.0.0",
			"--leader-elect=true",
			"--scheduler-name=stork",
			"--policy-configmap=" + storkConfigMapName,
			"--policy-configmap-namespace=" + cluster.Namespace,
			"--lock-object-name=stork-scheduler",
		}
	}

	if val, ok := cluster.Spec.Stork.Args["verbose"]; ok && val == "true" {
		command = append(command, "--v=5")
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

	// To add the missing 'name: stork-scheduler' label from older deployments,
	// we need to re-create the deployment as the field is immutable.
	if err = c.checkForMissingSelectorLabel(existingDeployment); err != nil {
		return err
	}

	var existingImage string
	var existingCommand []string
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == storkSchedContainerName {
			existingImage = c.Image
			existingCommand = c.Command
		}
	}

	updatedTopologySpreadConstraints, err := util.GetTopologySpreadConstraints(c.client, storkSchedulerDeploymentLabels)
	if err != nil {
		return err
	}

	deployment := getStorkSchedDeploymentSpec(
		cluster,
		ownerRef,
		imageName,
		command,
		targetCPUQuantity,
		updatedTopologySpreadConstraints,
		c.kubernetesVersion.GreaterThanOrEqual(k8sMinVersionForKubeSchedulerConfiguration))

	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		util.HasResourcesChanged(existingDeployment.Spec.Template.Spec.Containers[0].Resources, deployment.Spec.Template.Spec.Containers[0].Resources) ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations) ||
		util.HaveTopologySpreadConstraintsChanged(updatedTopologySpreadConstraints,
			existingDeployment.Spec.Template.Spec.TopologySpreadConstraints)

	if !c.isStorkSchedDeploymentCreated || modified {
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
	topologySpreadConstraints []v1.TopologySpreadConstraint,
	needsKubeSchedulerConfiguration bool,
) *apps.Deployment {
	pullPolicy := imagePullPolicy(cluster)
	replicas := int32(3)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	var scheme v1.URIScheme
	var port int
	var volumeMounts []v1.VolumeMount
	var volumes []v1.Volume

	if needsKubeSchedulerConfiguration {
		scheme = v1.URISchemeHTTPS
		port = defaultKubeSchedulerPort
		volumeMounts = []v1.VolumeMount{
			{
				Name:      "scheduler-config",
				MountPath: "/etc/kubernetes",
			},
		}
		volumes = []v1.Volume{
			{
				Name: "scheduler-config",
				VolumeSource: v1.VolumeSource{
					ConfigMap: &v1.ConfigMapVolumeSource{
						LocalObjectReference: v1.LocalObjectReference{
							Name: "stork-config",
						},
					},
				},
			},
		}
	} else {
		scheme = v1.URISchemeHTTP
		port = 10251
		volumeMounts = nil
		volumes = nil
	}

	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            storkSchedDeploymentName,
			Namespace:       cluster.Namespace,
			Labels:          storkSchedulerDeploymentLabels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: apps.DeploymentSpec{
			Replicas: &replicas,
			Strategy: apps.DeploymentStrategy{
				Type: apps.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &apps.RollingUpdateDeployment{
					MaxSurge:       &maxSurge,
					MaxUnavailable: &maxUnavailable,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: storkSchedulerDeploymentLabels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:   storkSchedDeploymentName,
					Labels: storkSchedulerDeploymentLabels,
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
								ProbeHandler: v1.ProbeHandler{
									HTTPGet: &v1.HTTPGetAction{
										Path:   "/healthz",
										Port:   intstr.FromInt(port),
										Scheme: scheme,
									},
								},
							},
							ReadinessProbe: &v1.Probe{
								ProbeHandler: v1.ProbeHandler{
									HTTPGet: &v1.HTTPGetAction{
										Path:   "/healthz",
										Port:   intstr.FromInt(port),
										Scheme: scheme,
									},
								},
							},
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuQuantity,
								},
							},
							VolumeMounts: volumeMounts,
						},
					},
					Volumes: volumes,
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

	if len(topologySpreadConstraints) != 0 {
		deployment.Spec.Template.Spec.TopologySpreadConstraints = topologySpreadConstraints
	}

	// If resources is specified in the spec, the resources specified by annotation (such as portworx.io/stork-cpu)
	// will be overwritten.
	if cluster.Spec.Stork.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *cluster.Spec.Stork.Resources
	}

	return deployment
}

func (c *Controller) checkForMissingSelectorLabel(existingDeployment *apps.Deployment) error {
	if existingDeployment.Spec.Selector == nil {
		return nil
	}

	found := false
	for k, v := range existingDeployment.Spec.Selector.MatchLabels {
		if k == "name" && v == storkSchedDeploymentName {
			found = true
			break
		}
	}

	if !found {
		err := c.client.Delete(context.TODO(), existingDeployment)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		c.isStorkSchedDeploymentCreated = false
	}

	return nil
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
