package component

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

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
	// AutopilotComponentName name of the Autopilot component
	AutopilotComponentName = "Autopilot"
	// AutopilotConfigMapName name of the autopilot config map
	AutopilotConfigMapName = "autopilot-config"
	// AutopilotServiceAccountName name of the autopilot service account
	AutopilotServiceAccountName = "autopilot"
	// AutopilotClusterRoleName name of the autopilot cluster role
	AutopilotClusterRoleName = "autopilot"
	// AutopilotClusterRoleBindingName name of the autopilot cluster role binding
	AutopilotClusterRoleBindingName = "autopilot"
	// AutopilotDeploymentName name of the autopilot deployment
	AutopilotDeploymentName = "autopilot"
	// AutopilotContainerName name of the autopilot container
	AutopilotContainerName = "autopilot"
	// AutopilotDefaultProviderEndpoint endpoint of default provider
	AutopilotDefaultProviderEndpoint = "http://px-prometheus:9090"
	// AutopilotDefaultReviewersKey is a key for default reviewers array in gitops config map
	AutopilotDefaultReviewersKey = "defaultReviewers"
	// OCPPrometheusUserWorkloadSecretPrefix name of OCP user-workload Prometheus secret
	OCPPrometheusUserWorkloadSecretPrefix = "prometheus-user-workload-token"
	// Autopilot Secret name for prometheus-user-workload-token
	AutopilotSecretName = "autopilot-prometheus-auth"
	defaultAutopilotCPU = "0.1"
)

var (
	autopilotConfigParams = map[string]bool{
		"min_poll_interval": true,
	}
	autopilotDeploymentVolumes = []corev1.VolumeSpec{
		{
			Name:      "config-volume",
			MountPath: "/etc/config",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: AutopilotConfigMapName,
					},
					Items: []v1.KeyToPath{
						{
							Key:  "config.yaml",
							Path: "config.yaml",
						},
					},
				},
			},
		},
		{
			Name:      "varcores",
			MountPath: "/var/cores",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/var/cores",
				},
			},
		},
	}

	openshiftDeploymentVolume = []corev1.VolumeSpec{
		{
			Name:      "token-volume",
			MountPath: "/var/local/secrets",
			ReadOnly:  true,
			VolumeSource: v1.VolumeSource{
				Secret: &v1.SecretVolumeSource{
					SecretName: AutopilotSecretName,
					Items: []v1.KeyToPath{
						{
							Key:  "token",
							Path: "token",
						},
					},
				},
			},
		},
		{
			Name:      "ca-cert-volume",
			MountPath: "/etc/ssl/certs",
			ReadOnly:  true,
			VolumeSource: v1.VolumeSource{
				Secret: &v1.SecretVolumeSource{
					SecretName: AutopilotSecretName,
					Items: []v1.KeyToPath{
						{
							Key:  "cacert",
							Path: "ca-certificates.crt",
						},
					},
				},
			},
		},
	}
)

type autopilot struct {
	isCreated               bool
	k8sClient               client.Client
	k8sVersion              version.Version
	isUserWorkloadSupported *bool
}

func (c *autopilot) Name() string {
	return AutopilotComponentName
}

func (c *autopilot) Priority() int32 {
	return DefaultComponentPriority
}

func (c *autopilot) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.k8sVersion = k8sVersion
}

func (c *autopilot) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *autopilot) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Autopilot != nil && cluster.Spec.Autopilot.Enabled
}

func (c *autopilot) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createConfigMap(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createClusterRole(); err != nil {
		return err
	}
	if err := c.createClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}
	if c.isOCPUserWorkloadSupported() {
		if err := c.createSecret(cluster.Namespace, ownerRef); err != nil {
			// log the error and proceed for deployment creation
			// if secret is created in next reconcilation loop successfully, deployment will be updated with volume mounts
			logrus.Errorf("Error during creating secret %v ", err)
		}
	}
	if err := c.createDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *autopilot) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteConfigMap(c.k8sClient, AutopilotConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, AutopilotServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, AutopilotClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, AutopilotClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, AutopilotDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if c.isOCPUserWorkloadSupported() {
		if err := k8sutil.DeleteSecret(c.k8sClient, AutopilotSecretName, cluster.Namespace, *ownerRef); err != nil {
			return err
		}
	}

	c.MarkDeleted()
	return nil
}

func (c *autopilot) MarkDeleted() {
	c.isCreated = false
	c.isUserWorkloadSupported = nil
}

func (c *autopilot) createConfigMap(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config := "providers:"
	for _, provider := range cluster.Spec.Autopilot.Providers {
		keys := make([]string, 0, len(provider.Params))
		for k := range provider.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var params string
		for _, k := range keys {
			params += fmt.Sprintf("%s=%s,", k, provider.Params[k])
		}
		params = strings.TrimRight(params, ",")
		config += fmt.Sprintf(`
- name: %s
  type: %s
  params: %s`,
			provider.Name, provider.Type, params)
	}

	if cluster.Spec.Autopilot.GitOps != nil {
		config += "\ngitops:"
		if cluster.Spec.Autopilot.GitOps.Type == "" {
			return fmt.Errorf("gitops Type field is required")
		}

		if len(cluster.Spec.Autopilot.GitOps.Params) == 0 {
			return fmt.Errorf("gitops params cannot be empty")
		}

		keys := make([]string, 0, len(cluster.Spec.Autopilot.GitOps.Params))
		for k := range cluster.Spec.Autopilot.GitOps.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		params := ""
		for _, key := range keys {
			val := cluster.Spec.Autopilot.GitOps.Params[key]
			if key == AutopilotDefaultReviewersKey {
				params += fmt.Sprintf(`
    %s:`, AutopilotDefaultReviewersKey)
				var reviewers []interface{} = val.([]interface{})
				for _, reviewer := range reviewers {
					if r, ok := reviewer.(string); ok {
						r = strings.TrimSpace(r)
						params += fmt.Sprintf(`
      - "%s"`, r)
					}
				}
				continue
			}
			params += fmt.Sprintf(`
    %s: %s`, key, val)
		}

		config += fmt.Sprintf(`
  name: %s
  type: %s
  params:%s`, cluster.Spec.Autopilot.GitOps.Name, cluster.Spec.Autopilot.GitOps.Type, params)
	}

	for key, value := range cluster.Spec.Autopilot.Args {
		if _, exists := autopilotConfigParams[key]; exists {
			config += fmt.Sprintf("\n%s: %s", key, value)
		}
	}

	_, err := k8sutil.CreateOrUpdateConfigMap(
		c.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            AutopilotConfigMapName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				"config.yaml": config,
			},
		},
		ownerRef,
	)
	return err
}

func (c *autopilot) createSecret(clusterNamespace string, ownerRef *metav1.OwnerReference) error {

	token, cert, err := c.getPrometheusTokenAndCert()
	if err != nil {
		return err
	}

	return k8sutil.CreateOrUpdateSecret(
		c.k8sClient,
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            AutopilotSecretName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string][]byte{
				"token":  []byte(token),
				"cacert": []byte(cert),
			},
		},
		ownerRef,
	)
}

func (c *autopilot) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            AutopilotServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *autopilot) createClusterRole() error {
	rules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{"", "events.k8s.io"},
			Resources: []string{"events"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"services", "secrets"},
			Verbs:     []string{"get"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"namespaces", "pods"},
			Verbs:     []string{"list"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"persistentvolumeclaims", "persistentvolumes"},
			Verbs:     []string{"get", "list", "update", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list", "patch", "update", "watch"},
		},
		{
			APIGroups: []string{"autopilot.libopenstorage.org"},
			Resources: []string{"actionapprovals", "autopilotrules", "autopilotruleobjects", "autopilotrules/finalizers"},
			Verbs:     []string{"*"},
		},
		{
			APIGroups: []string{"apiextensions.k8s.io"},
			Resources: []string{"customresourcedefinitions"},
			Verbs:     []string{"create", "get", "update"},
		},
		{
			APIGroups:     []string{"security.openshift.io"},
			Resources:     []string{"securitycontextconstraints"},
			ResourceNames: []string{"portworx-restricted"},
			Verbs:         []string{"use"},
		},
	}
	if c.k8sVersion.LessThan(k8sutil.K8sVer1_25) {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{"policy"},
			Resources:     []string{"podsecuritypolicies"},
			ResourceNames: []string{constants.RestrictedPSPName},
			Verbs:         []string{"use"},
		},
		)
	}
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: AutopilotClusterRoleName,
			},
			Rules: rules,
		},
	)
}

func (c *autopilot) createClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: AutopilotClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      AutopilotServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     AutopilotClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *autopilot) setDefaultAutopilotSecret(cluster *corev1.StorageCluster, envMap map[string]*v1.EnvVar) {
	if !pxutil.AuthEnabled(&cluster.Spec) {
		return
	}

	if _, exist := envMap[pxutil.EnvKeyPXSharedSecret]; !exist {
		envMap[pxutil.EnvKeyPXSharedSecret] = &v1.EnvVar{
			Name: pxutil.EnvKeyPXSharedSecret,
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					Key: pxutil.SecurityAppsSecretKey,
					LocalObjectReference: v1.LocalObjectReference{
						Name: pxutil.SecurityPXSystemSecretsSecretName,
					},
				},
			},
		}
	}
}

func (c *autopilot) createDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	imageName := c.getDesiredAutopilotImage(cluster)
	if imageName == "" {
		return fmt.Errorf("autopilot image cannot be empty")
	}

	existingDeployment := &appsv1.Deployment{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      AutopilotDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	targetCPU := defaultAutopilotCPU
	if cpuStr, ok := cluster.Annotations[pxutil.AnnotationAutopilotCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	args := map[string]string{
		"config":    "/etc/config/config.yaml",
		"log-level": "info",
	}
	for k, v := range cluster.Spec.Autopilot.Args {
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
	command := append([]string{"/autopilot"}, argList...)

	imageName = util.GetImageURN(cluster, imageName)

	envMap := make(map[string]*v1.EnvVar)
	envMap[pxutil.EnvKeyPortworxNamespace] = &v1.EnvVar{
		Name:  pxutil.EnvKeyPortworxNamespace,
		Value: cluster.Namespace,
	}
	pxutil.AppendTLSEnv(&cluster.Spec, envMap)

	// any user supplied env values will take precedence over those generated by us.
	for _, env := range cluster.Spec.Autopilot.Env {
		envMap[env.Name] = env.DeepCopy()
	}

	c.setDefaultAutopilotSecret(cluster, envMap)
	finalEnvVars := make([]v1.EnvVar, 0, len(envMap))
	for _, env := range envMap {
		finalEnvVars = append(finalEnvVars, *env)
	}
	sort.Sort(k8sutil.EnvByName(finalEnvVars))

	volumes, volumeMounts := c.getDesiredVolumesAndMounts(cluster)

	var existingImage string
	var existingCommand []string
	var existingEnvs []v1.EnvVar
	var existingMounts []v1.VolumeMount
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == AutopilotContainerName {
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

	targetDeployment := c.getAutopilotDeploymentSpec(cluster, ownerRef, imageName,
		command, finalEnvVars, volumes, volumeMounts, targetCPUQuantity)
	// Check if the deployment has changed
	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		!reflect.DeepEqual(existingEnvs, finalEnvVars) ||
		!reflect.DeepEqual(existingVolumes, volumes) ||
		!reflect.DeepEqual(existingMounts, volumeMounts) ||
		util.HasResourcesChanged(existingDeployment.Spec.Template.Spec.Containers[0].Resources, targetDeployment.Spec.Template.Spec.Containers[0].Resources) ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)
	if !c.isCreated || modified {
		if err = k8sutil.CreateOrUpdateDeployment(c.k8sClient, targetDeployment, ownerRef); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func (c *autopilot) getAutopilotDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	envVars []v1.EnvVar,
	volumes []v1.Volume,
	volumeMounts []v1.VolumeMount,
	cpuQuantity resource.Quantity,
) *appsv1.Deployment {
	deploymentLabels := map[string]string{
		"tier": "control-plane",
	}
	templateLabels := map[string]string{
		"name": "autopilot",
		"tier": "control-plane",
	}
	selectorLabels := map[string]string{
		"name": "autopilot",
		"tier": "control-plane",
	}

	replicas := int32(1)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AutopilotDeploymentName,
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
					ServiceAccountName: AutopilotServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            AutopilotContainerName,
							Image:           imageName,
							ImagePullPolicy: imagePullPolicy,
							Command:         command,
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuQuantity,
								},
							},
							VolumeMounts: volumeMounts,
							SecurityContext: &v1.SecurityContext{
								AllowPrivilegeEscalation: boolPtr(false),
								Privileged:               boolPtr(false),
							},
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
													AutopilotDeploymentName,
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

	if len(envVars) > 0 {
		deployment.Spec.Template.Spec.Containers[0].Env = envVars
	}

	// If resources is specified in the spec, the resources specified by annotation (such as portworx.io/autopilot-cpu)
	// will be overwritten.
	if cluster.Spec.Autopilot.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *cluster.Spec.Autopilot.Resources
	}
	deployment.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)

	return deployment
}

func (c *autopilot) getDesiredAutopilotImage(cluster *corev1.StorageCluster) string {
	if cluster.Spec.Autopilot.Image != "" {
		return cluster.Spec.Autopilot.Image
	} else if cluster.Status.DesiredImages != nil {
		return cluster.Status.DesiredImages.Autopilot
	}
	return ""
}

func (c *autopilot) getDesiredVolumesAndMounts(
	cluster *corev1.StorageCluster,
) ([]v1.Volume, []v1.VolumeMount) {
	volumeSpecs := make([]corev1.VolumeSpec, 0)

	if c.isOCPUserWorkloadSupported() && c.isAutopilotSecretCreated(cluster.Namespace) {
		for _, v := range openshiftDeploymentVolume {
			vCopy := v.DeepCopy()
			volumeSpecs = append(volumeSpecs, *vCopy)
		}
	}

	for _, v := range autopilotDeploymentVolumes {
		vCopy := v.DeepCopy()
		volumeSpecs = append(volumeSpecs, *vCopy)
	}
	for _, v := range cluster.Spec.Autopilot.Volumes {
		vCopy := v.DeepCopy()
		vCopy.Name = pxutil.UserVolumeName(v.Name)
		volumeSpecs = append(volumeSpecs, *vCopy)
	}

	volumes, volumeMounts := util.ExtractVolumesAndMounts(volumeSpecs)
	sort.Sort(k8sutil.VolumeByName(volumes))
	sort.Sort(k8sutil.VolumeMountByName(volumeMounts))
	return volumes, volumeMounts
}

func (c *autopilot) isAutopilotSecretCreated(namespace string) bool {

	secret := &v1.Secret{}

	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      AutopilotSecretName,
			Namespace: namespace,
		},
		secret,
	)

	if err == nil {
		return true
	}

	if err != nil && errors.IsNotFound(err) {
		return false
	}

	logrus.Errorf("error while fetching secret %s ", err)
	return false
}

func (c *autopilot) getPrometheusTokenAndCert() (encodedToken, caCert string, err error) {
	secrets := &v1.SecretList{}
	err = c.k8sClient.List(
		context.TODO(),
		secrets,
		client.InNamespace("openshift-user-workload-monitoring"),
	)

	if err != nil {
		return "", "", err
	}

	// Iterate through the secrets list to process prometheus-user-workload-token secret
	var secretFound bool
	for _, secret := range secrets.Items {

		if strings.HasPrefix(secret.Name, OCPPrometheusUserWorkloadSecretPrefix) {
			secretFound = true
			// Retrieve the token data from the secret as []byte
			tokenBytes, ok := secret.Data["token"]
			if !ok {
				return encodedToken, caCert, fmt.Errorf("token not found in secret")
			}

			// Retrieve the ca.cert data from the secret as []byte
			cert, ok := secret.Data["ca.crt"]
			if !ok {
				return encodedToken, caCert, fmt.Errorf("cert not found in secret")
			}

			encodedToken = string(tokenBytes)
			caCert = string(cert)
			break
		}
	}

	if !secretFound {
		return "", "", fmt.Errorf("prometheus-user-workload-token not found. Please make sure that user workload monitoring is enabled in openshift")
	}
	return encodedToken, caCert, nil
}

func (c *autopilot) isOCPUserWorkloadSupported() bool {
	if c.isUserWorkloadSupported == nil {
		isSupported, err := pxutil.IsSupportedOCPVersion(c.k8sClient, pxutil.OpenshiftPrometheusSupportedVersion)
		if err != nil {
			logrus.Errorf("Failed to check if OCP user workload monitoring is supported: %v", err)
			return false
		}
		c.isUserWorkloadSupported = &isSupported
	}
	return *c.isUserWorkloadSupported
}

// RegisterAutopilotComponent registers the Autopilot component
func RegisterAutopilotComponent() {
	Register(AutopilotComponentName, &autopilot{})
}

func init() {
	RegisterAutopilotComponent()
}
