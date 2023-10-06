package component

import (
	"context"
	"fmt"
	"path"
	"path/filepath"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringapi "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metaerrors "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PrometheusComponentName name of the Prometheus component
	PrometheusComponentName = "Prometheus"
	// PrometheusOperatorServiceAccountName name of the prometheus operator service account
	PrometheusOperatorServiceAccountName = "px-prometheus-operator"
	// PrometheusOperatorClusterRoleName name of the prometheus operator cluster role
	PrometheusOperatorClusterRoleName = "px-prometheus-operator"
	// PrometheusOperatorClusterRoleBindingName name of the prometheus operator cluster role binding
	PrometheusOperatorClusterRoleBindingName = "px-prometheus-operator"
	// PrometheusOperatorDeploymentName name of the prometheus operator deployment
	PrometheusOperatorDeploymentName = "px-prometheus-operator"
	// PrometheusServiceAccountName name of the prometheus service account
	PrometheusServiceAccountName = "px-prometheus"
	// PrometheusClusterRoleName name of the prometheus cluster role
	PrometheusClusterRoleName = "px-prometheus"
	// PrometheusClusterRoleBindingName name of the prometheus cluster role binding
	PrometheusClusterRoleBindingName = "px-prometheus"
	// PrometheusServiceName name of the prometheus service
	PrometheusServiceName = "px-prometheus"
	// PrometheusInstanceName name of the prometheus instance
	PrometheusInstanceName = "px-prometheus"

	defaultRunAsUser = 65534
)

type prometheus struct {
	k8sClient         client.Client
	k8sVersion        *version.Version
	scheme            *runtime.Scheme
	recorder          record.EventRecorder
	isOperatorCreated bool
}

func (c *prometheus) Name() string {
	return PrometheusComponentName
}

func (c *prometheus) Priority() int32 {
	return DefaultComponentPriority
}

func (c *prometheus) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.k8sVersion = &k8sVersion
	c.scheme = scheme
	c.recorder = recorder
}

func (c *prometheus) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *prometheus) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.Enabled
}

func (c *prometheus) Reconcile(cluster *corev1.StorageCluster) error {
	if c.k8sVersion != nil && c.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22) {
		if err := c.createPrometheusCRDs(); err != nil {
			return err
		}
	}
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createOperatorServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createOperatorClusterRole(); err != nil {
		return err
	}
	if err := c.createOperatorClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}
	if err := c.createOperatorDeployment(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createPrometheusServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createPrometheusClusterRole(); err != nil {
		return err
	}
	if err := c.createPrometheusClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}
	if err := c.createPrometheusService(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createPrometheusInstance(cluster, ownerRef); metaerrors.IsNoMatchError(err) {
		gvk := schema.GroupVersionKind{
			Group:   monitoringapi.GroupName,
			Version: monitoringv1.Version,
			Kind:    monitoringv1.PrometheusesKind,
		}
		if resourcePresent, _ := coreops.Instance().ResourceExists(gvk); resourcePresent {
			var clnt client.Client
			clnt, err = k8sutil.NewK8sClient(c.scheme)
			if err == nil {
				c.k8sClient = clnt
				err = c.createPrometheusInstance(cluster, ownerRef)
			}
		}
		if err != nil {
			c.warningEvent(cluster, util.FailedComponentReason,
				fmt.Sprintf("Failed to create Prometheus object for Portworx. Ensure Prometheus is deployed correctly. %v", err))
			return nil
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (c *prometheus) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	err := k8sutil.DeletePrometheus(c.k8sClient, PrometheusInstanceName, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, PrometheusServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, PrometheusClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PrometheusClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, PrometheusServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, PrometheusOperatorServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, PrometheusOperatorClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PrometheusOperatorClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, PrometheusOperatorDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	c.MarkDeleted()
	return nil
}

func (c *prometheus) MarkDeleted() {
	c.isOperatorCreated = false
}

// createPrometheusCRDs registers all CRDs needed by prometheus operator
// with prometheus operator upgraded to 0.50.0, it no longer registers CRDs automatically
// check for details: https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack/crds
func (c *prometheus) createPrometheusCRDs() error {
	filename := path.Join(pxutil.SpecsBaseDir(), "prometheus-crd-*.yaml")
	files, err := filepath.Glob(filename)
	if err != nil {
		return err
	}
	for _, file := range files {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := k8sutil.ParseObjectFromFile(file, c.scheme, crd); err != nil {
			return err
		}
		if err := k8sutil.CreateOrUpdateCRD(crd); err != nil {
			return err
		}
	}
	return nil
}

func (c *prometheus) createOperatorServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PrometheusOperatorServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *prometheus) createPrometheusServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PrometheusServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

// RBAC rule reference: https://github.com/prometheus-operator/prometheus-operator/blob/release-0.50/Documentation/rbac.md
func (c *prometheus) createOperatorClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: PrometheusOperatorClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"extensions"},
					Resources: []string{"thirdpartyresources"},
					Verbs:     []string{"*"},
				},
				{
					APIGroups: []string{"apiextensions.k8s.io"},
					Resources: []string{"customresourcedefinitions"},
					Verbs:     []string{"*"},
				},
				{
					APIGroups: []string{"monitoring.coreos.com"},
					Resources: []string{
						"alertmanagers",
						"alertmanagers/finalizers",
						"alertmanagerconfigs",
						"prometheuses",
						"prometheuses/status",
						"prometheuses/finalizers",
						"thanosrulers",
						"thanosrulers/finalizers",
						"servicemonitors",
						"podmonitors",
						"probes",
						"prometheusrules",
					},
					Verbs: []string{"*"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"statefulsets"},
					Verbs:     []string{"*"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps", "secrets"},
					Verbs:     []string{"*"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"list", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"services", "services/finalizers", "endpoints"},
					Verbs:     []string{"get", "create", "update", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"namespaces"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"networking.k8s.io"},
					Resources: []string{"ingresses"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{"anyuid"},
					Verbs:         []string{"use"},
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

// RBAC rule reference: https://github.com/prometheus-operator/prometheus-operator/blob/release-0.50/Documentation/rbac.md
func (c *prometheus) createPrometheusClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: PrometheusClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"nodes", "nodes/metrics", "services", "endpoints", "pods"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get"},
				},
				{
					APIGroups: []string{"networking.k8s.io"},
					Resources: []string{"ingresses"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					NonResourceURLs: []string{"/metrics", "/metrics/cadvisor", "/federate"},
					Verbs:           []string{"get"},
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

func (c *prometheus) createOperatorClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: PrometheusOperatorClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      PrometheusOperatorServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     PrometheusOperatorClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *prometheus) createPrometheusClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: PrometheusClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      PrometheusServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     PrometheusClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *prometheus) createOperatorDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	existingDeployment := &appsv1.Deployment{}
	getErr := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      PrometheusOperatorDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if getErr != nil && !errors.IsNotFound(getErr) {
		return getErr
	}

	var existingImageName string
	if len(existingDeployment.Spec.Template.Spec.Containers) > 0 {
		existingImageName = existingDeployment.Spec.Template.Spec.Containers[0].Image
	}

	imageName := util.GetImageURN(
		cluster,
		cluster.Status.DesiredImages.PrometheusOperator,
	)

	deployment := getPrometheusOperatorDeploymentSpec(cluster, ownerRef, imageName)
	modified := existingImageName != imageName ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if !c.isOperatorCreated || errors.IsNotFound(getErr) || modified {
		if err := k8sutil.CreateOrUpdateDeployment(c.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isOperatorCreated = true
	return nil
}

func getPrometheusOperatorDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	operatorImage string,
) *appsv1.Deployment {
	replicas := int32(1)
	runAsNonRoot := true
	runAsUser := int64(defaultRunAsUser)
	labels := map[string]string{
		"k8s-app": PrometheusOperatorDeploymentName,
	}
	selectorLabels := map[string]string{
		"k8s-app": PrometheusOperatorDeploymentName,
	}
	configReloaderImageName := util.GetImageURN(
		cluster,
		cluster.Status.DesiredImages.PrometheusConfigMapReload,
	)
	prometheusConfigReloaderImageName := util.GetImageURN(
		cluster,
		cluster.Status.DesiredImages.PrometheusConfigReloader,
	)
	args := make([]string, 0)
	args = append(args,
		fmt.Sprintf("-namespaces=%s", cluster.Namespace),
		fmt.Sprintf("--kubelet-service=%s/kubelet", cluster.Namespace),
		fmt.Sprintf("--prometheus-config-reloader=%s", prometheusConfigReloaderImageName),
	)
	if configReloaderImageName != "" {
		args = append(args, fmt.Sprintf("--config-reloader-image=%s", configReloaderImageName))
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PrometheusOperatorDeploymentName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          selectorLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: PrometheusOperatorServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            "px-prometheus-operator",
							Image:           operatorImage,
							ImagePullPolicy: pxutil.ImagePullPolicy(cluster),
							Args:            args,
							Ports: []v1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: int32(8080),
								},
							},
						},
					},
					SecurityContext: &v1.PodSecurityContext{
						RunAsNonRoot: &runAsNonRoot,
						RunAsUser:    &runAsUser,
						RunAsGroup:   &runAsUser,
						FSGroup:      &runAsUser,
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
	deployment.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)

	return deployment
}

func (c *prometheus) createPrometheusService(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PrometheusServiceName,
			Namespace:       clusterNamespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: map[string]string{
				"prometheus": PrometheusInstanceName,
			},
			Ports: []v1.ServicePort{
				{
					Name:       "web",
					Port:       int32(9090),
					TargetPort: intstr.FromInt(9090),
				},
			},
		},
	}

	return k8sutil.CreateOrUpdateService(c.k8sClient, newService, ownerRef)
}

func (c *prometheus) createPrometheusInstance(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	replicas := int32(1)
	prometheusImageName := util.GetImageURN(
		cluster,
		cluster.Status.DesiredImages.Prometheus,
	)

	prometheusInst := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PrometheusInstanceName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: monitoringv1.PrometheusSpec{
			CommonPrometheusFields: monitoringv1.CommonPrometheusFields{
				Image:              &prometheusImageName,
				LogLevel:           "debug",
				ServiceAccountName: PrometheusServiceAccountName,
				ServiceMonitorSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "prometheus",
							Operator: metav1.LabelSelectorOpIn,
							Values: []string{
								PxServiceMonitor,
								PxBackupServiceMonitor,
							},
						},
					},
				},
			},
			RuleSelector: &metav1.LabelSelector{
				MatchLabels: prometheusRuleLabels(),
			},
		},
	}

	if cluster.Spec.Monitoring.Prometheus.Resources.Limits != nil {
		prometheusInst.Spec.Resources.Limits = cluster.Spec.Monitoring.Prometheus.Resources.Limits
	} else {
		prometheusInst.Spec.Resources.Limits = map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:              resource.MustParse("1"),
			v1.ResourceEphemeralStorage: resource.MustParse("5Gi"),
		}
	}

	if cluster.Spec.Monitoring.Prometheus.Resources.Requests != nil {
		prometheusInst.Spec.Resources.Requests = cluster.Spec.Monitoring.Prometheus.Resources.Requests
	} else {
		prometheusInst.Spec.Resources.Requests = map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("400Mi"),
		}
	}

	if cluster.Spec.Monitoring.Prometheus.SecurityContext != nil {
		prometheusInst.Spec.SecurityContext = cluster.Spec.Monitoring.Prometheus.SecurityContext
	} else {
		runAsNonRoot := true
		runAsUser := int64(defaultRunAsUser)
		prometheusInst.Spec.SecurityContext = &v1.PodSecurityContext{
			RunAsNonRoot: &runAsNonRoot,
			RunAsUser:    &runAsUser,
			RunAsGroup:   &runAsUser,
			FSGroup:      &runAsUser,
		}
	}

	if cluster.Spec.Monitoring.Prometheus.Replicas != nil {
		prometheusInst.Spec.Replicas = cluster.Spec.Monitoring.Prometheus.Replicas
	} else {
		prometheusInst.Spec.Replicas = &replicas
	}

	if cluster.Spec.Monitoring.Prometheus.Retention != "" {
		prometheusInst.Spec.Retention = monitoringv1.Duration(cluster.Spec.Monitoring.Prometheus.Retention)
	}

	if cluster.Spec.Monitoring.Prometheus.RetentionSize != "" {
		prometheusInst.Spec.RetentionSize = monitoringv1.ByteSize(cluster.Spec.Monitoring.Prometheus.RetentionSize)
	}

	if cluster.Spec.Monitoring.Prometheus.Storage != nil {
		prometheusInst.Spec.Storage = cluster.Spec.Monitoring.Prometheus.Storage
	}

	if cluster.Spec.Monitoring.Prometheus.Volumes != nil {
		prometheusInst.Spec.Volumes = cluster.Spec.Monitoring.Prometheus.Volumes
	}

	if cluster.Spec.Monitoring.Prometheus.VolumeMounts != nil {
		prometheusInst.Spec.VolumeMounts = cluster.Spec.Monitoring.Prometheus.VolumeMounts
	}

	if cluster.Spec.Monitoring.Prometheus.AlertManager != nil &&
		cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled {
		prometheusInst.Spec.Alerting = &monitoringv1.AlertingSpec{
			Alertmanagers: []monitoringv1.AlertmanagerEndpoints{
				{
					Namespace: cluster.Namespace,
					Name:      AlertManagerServiceName,
					Port:      intstr.FromString(alertManagerPortName),
				},
			},
		}
	}

	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		prometheusInst.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *cluster.Spec.ImagePullSecret,
			},
		)
	}

	if cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.RemoteWriteEndpoint != "" {
		prometheusInst.Spec.RemoteWrite = []monitoringv1.RemoteWriteSpec{
			{
				URL: fmt.Sprintf("%s/api/prom/push", cluster.Spec.Monitoring.Prometheus.RemoteWriteEndpoint),
			},
		}
	}

	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			prometheusInst.Spec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(cluster.Spec.Placement.Tolerations) > 0 {
			prometheusInst.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range cluster.Spec.Placement.Tolerations {
				prometheusInst.Spec.Tolerations = append(
					prometheusInst.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	return k8sutil.CreateOrUpdatePrometheus(c.k8sClient, prometheusInst, ownerRef)
}

func (c *prometheus) warningEvent(
	cluster *corev1.StorageCluster,
	reason, message string,
) {
	logrus.Warn(message)
	c.recorder.Event(cluster, v1.EventTypeWarning, reason, message)
}

// RegisterPrometheusComponent registers the Prometheus component
func RegisterPrometheusComponent() {
	Register(PrometheusComponentName, &prometheus{})
}

func init() {
	RegisterPrometheusComponent()
}
