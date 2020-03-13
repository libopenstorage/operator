package component

import (
	"context"
	"fmt"

	monitoringapi "github.com/coreos/prometheus-operator/pkg/apis/monitoring"
	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
	// DefaultPrometheusOperatorImage is the default prometheus operator image
	DefaultPrometheusOperatorImage = "quay.io/coreos/prometheus-operator:v0.34.0"
)

type prometheus struct {
	k8sClient         client.Client
	scheme            *runtime.Scheme
	recorder          record.EventRecorder
	isOperatorCreated bool
}

func (c *prometheus) Initialize(
	k8sClient client.Client,
	_ version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.scheme = scheme
	c.recorder = recorder
}

func (c *prometheus) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.Enabled
}

func (c *prometheus) Reconcile(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createOperatorServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createOperatorClusterRole(ownerRef); err != nil {
		return err
	}
	if err := c.createOperatorClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createOperatorDeployment(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createPrometheusServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createPrometheusClusterRole(ownerRef); err != nil {
		return err
	}
	if err := c.createPrometheusClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
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

func (c *prometheus) Delete(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	err := k8sutil.DeletePrometheus(c.k8sClient, PrometheusInstanceName, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, PrometheusServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, PrometheusClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PrometheusClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, PrometheusServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, PrometheusOperatorServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, PrometheusOperatorClusterRoleName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, PrometheusOperatorClusterRoleBindingName, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, PrometheusOperatorDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (c *prometheus) MarkDeleted() {
	c.isOperatorCreated = false
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

func (c *prometheus) createOperatorClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PrometheusOperatorClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
						"prometheuses",
						"prometheuses/finalizers",
						"servicemonitors",
						"prometheusrules",
						"podmonitors",
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
					Resources: []string{"services", "endpoints"},
					Verbs:     []string{"get", "create", "update"},
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
			},
		},
		ownerRef,
	)
}

func (c *prometheus) createPrometheusClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		c.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PrometheusClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"nodes", "services", "endpoints", "pods"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get"},
				},
				{
					NonResourceURLs: []string{"/metrics", "/federate"},
					Verbs:           []string{"get"},
				},
			},
		},
		ownerRef,
	)
}

func (c *prometheus) createOperatorClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PrometheusOperatorClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		ownerRef,
	)
}

func (c *prometheus) createPrometheusClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PrometheusClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
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
		ownerRef,
	)
}

func (c *prometheus) createOperatorDeployment(
	cluster *corev1alpha1.StorageCluster,
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

	modified := util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if !c.isOperatorCreated || errors.IsNotFound(getErr) || modified {
		deployment := getPrometheusOperatorDeploymentSpec(cluster, ownerRef)
		if err := k8sutil.CreateOrUpdateDeployment(c.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isOperatorCreated = true
	return nil
}

func getPrometheusOperatorDeploymentSpec(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) *appsv1.Deployment {
	replicas := int32(1)
	runAsNonRoot := true
	runAsUser := int64(65534)
	labels := map[string]string{
		"k8s-app": PrometheusOperatorDeploymentName,
	}
	operatorImage := util.GetImageURN(cluster.Spec.CustomImageRegistry, DefaultPrometheusOperatorImage)
	args := make([]string, 0)
	args = append(args,
		fmt.Sprintf("-namespaces=%s", cluster.Namespace),
		fmt.Sprintf("--kubelet-service=%s/kubelet", cluster.Namespace),
		"--config-reloader-image=quay.io/coreos/configmap-reload:v0.0.1",
	)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PrometheusOperatorDeploymentName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
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
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	replicas := int32(1)

	prometheusInst := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PrometheusInstanceName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: monitoringv1.PrometheusSpec{
			Replicas:           &replicas,
			LogLevel:           "debug",
			ServiceAccountName: PrometheusServiceAccountName,
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: serviceMonitorLabels(),
			},
			RuleSelector: &metav1.LabelSelector{
				MatchLabels: prometheusRuleLabels(),
			},
			Resources: v1.ResourceRequirements{
				Requests: map[v1.ResourceName]resource.Quantity{
					v1.ResourceMemory: resource.MustParse("400Mi"),
				},
			},
		},
	}

	if cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.RemoteWriteEndpoint != "" {
		prometheusInst.Spec.RemoteWrite = []monitoringv1.RemoteWriteSpec{
			{
				URL: fmt.Sprintf("http://%s/api/prom/push", cluster.Spec.Monitoring.Prometheus.RemoteWriteEndpoint),
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
	cluster *corev1alpha1.StorageCluster,
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
