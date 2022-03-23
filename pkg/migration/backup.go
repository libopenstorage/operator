package migration

import (
	"context"
	"strings"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// BackupConfigMapName is the name of configmap that contains Daemonset backup.
	// The backup is generated before migration starts and will not be overwritten.
	BackupConfigMapName = "px-daemonset-backup"

	// CollectionConfigMapName is the name of configmap that collects customer daemonset setup.
	// We can collect customer environment by asking customer to send the content of this configmap to us.
	// The configmap will be overwritten as long as daemonset exists.
	CollectionConfigMapName = "px-daemonset-collection"
)

func (h *Handler) backup(configMapName, namespace string, overwrite bool) error {
	if !overwrite {
		existingConfigMap := &v1.ConfigMap{}
		err := h.client.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      configMapName,
				Namespace: namespace,
			},
			existingConfigMap,
		)
		// Only proceed when the configmap does not exist.
		if !errors.IsNotFound(err) {
			return err
		}
	}

	var objs []client.Object
	if err := h.getDaemonSetComponent(namespace, &objs); err != nil {
		return err
	}

	if err := h.getPortworxAPI(namespace, &objs); err != nil {
		return err
	}
	if err := h.getPortworxRbac(namespace, &objs); err != nil {
		return err
	}

	if err := h.getStorkComponent(namespace, &objs); err != nil {
		return err
	}

	if err := h.getAutopilotComponent(namespace, &objs); err != nil {
		return err
	}
	if err := h.getCSIComponent(namespace, &objs); err != nil {
		return err
	}
	if err := h.getPVCControllerComponent(namespace, &objs); err != nil {
		return err
	}
	if err := h.getMonitoringComponent(namespace, &objs); err != nil {
		return err
	}

	var sb strings.Builder
	for _, obj := range objs {
		bytes, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}

		sb.WriteString(string(bytes))
		sb.WriteString("---\n")
	}

	data := map[string]string{
		"backup": sb.String(),
	}

	return k8sutil.CreateOrUpdateConfigMap(h.client,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: namespace,
			},
			Data: data,
		},
		nil)
}

func (h *Handler) addObject(
	name, namespace string,
	obj client.Object,
	objs *[]client.Object,
) error {
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		obj,
	)
	if meta.IsNoMatchError(err) || errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	*objs = append(*objs, obj)

	return nil
}

func (h *Handler) getDaemonSetComponent(namespace string, objs *[]client.Object) error {
	if err := h.addObject(portworxDaemonSetName, namespace, &appsv1.DaemonSet{}, objs); err != nil {
		return err
	}

	if err := h.addObject(pxServiceName, namespace, &v1.Service{}, objs); err != nil {
		return err
	}

	return nil
}

func (h *Handler) getStorkComponent(namespace string, objs *[]client.Object) error {
	if err := h.addObject(storkDeploymentName, namespace, &appsv1.Deployment{}, objs); err != nil {
		return err
	}
	if err := h.addObject(storkServiceName, namespace, &v1.Service{}, objs); err != nil {
		return err
	}
	if err := h.addObject(storkConfigName, namespace, &v1.ConfigMap{}, objs); err != nil {
		return err
	}
	if err := h.addObject(storkRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(storkRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(storkAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	if err := h.addObject(schedDeploymentName, namespace, &appsv1.Deployment{}, objs); err != nil {
		return err
	}
	if err := h.addObject(schedRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(schedRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(schedAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	return nil
}

func (h *Handler) getPortworxRbac(namespace string, objs *[]client.Object) error {
	if err := h.addObject(pxClusterRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pxClusterRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pxRoleBindingName, namespace, &rbacv1.RoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pxRoleName, namespace, &rbacv1.Role{}, objs); err != nil {
		return err
	}

	if err := h.addObject(secretsNamespace, "", &v1.Namespace{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pxRoleBindingName, secretsNamespace, &rbacv1.RoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pxRoleName, secretsNamespace, &rbacv1.Role{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pxAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	return nil
}

func (h *Handler) getPortworxAPI(namespace string, objs *[]client.Object) error {
	if err := h.addObject(pxAPIDaemonSetName, namespace, &appsv1.DaemonSet{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pxAPIServiceName, namespace, &v1.Service{}, objs); err != nil {
		return err
	}
	return nil
}

func (h *Handler) getAutopilotComponent(namespace string, objs *[]client.Object) error {
	if err := h.addObject(autopilotDeploymentName, namespace, &appsv1.Deployment{}, objs); err != nil {
		return err
	}
	if err := h.addObject(autopilotServiceName, namespace, &v1.Service{}, objs); err != nil {
		return err
	}
	if err := h.addObject(autopilotConfigName, namespace, &v1.ConfigMap{}, objs); err != nil {
		return err
	}
	if err := h.addObject(autopilotRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(autopilotRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(autopilotAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	return nil
}

func (h *Handler) getPVCControllerComponent(namespace string, objs *[]client.Object) error {
	if err := h.addObject(pvcDeploymentName, namespace, &appsv1.Deployment{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pvcRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pvcRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(pvcAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	return nil
}

func (h *Handler) getCSIComponent(namespace string, objs *[]client.Object) error {
	if err := h.addObject(csiDeploymentName, namespace, &appsv1.Deployment{}, objs); err != nil {
		return err
	}
	if err := h.addObject(csiServiceName, namespace, &v1.Service{}, objs); err != nil {
		return err
	}
	if err := h.addObject(csiRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(csiRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(csiAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	return nil
}

func (h *Handler) getMonitoringComponent(namespace string, objs *[]client.Object) error {
	if err := h.addObject(serviceMonitorName, namespace, &monitoringv1.ServiceMonitor{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusRuleName, namespace, &monitoringv1.PrometheusRule{}, objs); err != nil {
		return err
	}
	if err := h.addObject(alertManagerName, namespace, &monitoringv1.Alertmanager{}, objs); err != nil {
		return err
	}
	if err := h.addObject(alertManagerServiceName, namespace, &v1.Service{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusInstanceName, namespace, &monitoringv1.Prometheus{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusServiceName, namespace, &v1.Service{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusOpDeploymentName, namespace, &appsv1.Deployment{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusOpRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusOpRoleName, "", &rbacv1.ClusterRole{}, objs); err != nil {
		return err
	}
	if err := h.addObject(prometheusOpAccountName, namespace, &v1.ServiceAccount{}, objs); err != nil {
		return err
	}
	if err := h.addObject(component.TelemetryConfigMapName, namespace, &v1.ConfigMap{}, objs); err != nil {
		return err
	}
	return nil
}
