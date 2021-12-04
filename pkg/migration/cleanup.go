package migration

import (
	"context"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	pxClusterRoleName           = "node-get-put-list-role"
	pxClusterRoleBindingName    = "node-role-binding"
	pxRoleName                  = "px-role"
	pxRoleBindingName           = "px-role-binding"
	pxAccountName               = "px-account"
	pxServiceName               = "portworx-service"
	pxAPIDaemonSetName          = "portworx-api"
	pxAPIServiceName            = "portworx-api"
	storkDeploymentName         = "stork"
	storkServiceName            = "stork-service"
	storkConfigName             = "stork-config"
	storkRoleName               = "stork-role"
	storkRoleBindingName        = "stork-role-binding"
	storkAccountName            = "stork-account"
	schedDeploymentName         = "stork-scheduler"
	schedRoleName               = "stork-scheduler-role"
	schedRoleBindingName        = "stork-scheduler-role-binding"
	schedAccountName            = "stork-scheduler-account"
	autopilotDeploymentName     = "autopilot"
	autopilotConfigName         = "autopilot-config"
	autopilotServiceName        = "autopilot"
	autopilotRoleName           = "autopilot-role"
	autopilotRoleBindingName    = "autopilot-role-binding"
	autopilotAccountName        = "autopilot-account"
	pvcDeploymentName           = "portworx-pvc-controller"
	pvcRoleName                 = "portworx-pvc-controller-role"
	pvcRoleBindingName          = "portworx-pvc-controller-role-binding"
	pvcAccountName              = "portworx-pvc-controller-account"
	csiDeploymentName           = "px-csi-ext"
	csiServiceName              = "px-csi-service"
	csiRoleName                 = "px-csi-role"
	csiRoleBindingName          = "px-csi-role-binding"
	csiAccountName              = "px-csi-account"
	prometheusOpDeploymentName  = "prometheus-operator"
	prometheusOpRoleName        = "prometheus-operator"
	prometheusOpRoleBindingName = "prometheus-operator"
	prometheusOpAccountName     = "prometheus-operator"
	prometheusInstanceName      = "prometheus"
	prometheusServiceName       = "prometheus"
	prometheusRoleName          = "prometheus"
	prometheusRoleBindingName   = "prometheus"
	prometheusAccountName       = "prometheus"
	serviceMonitorName          = "portworx-prometheus-sm"
	prometheusRuleName          = "portworx"
	alertManagerName            = "portworx"
	alertManagerServiceName     = "alertmanager-portworx"
	secretsNamespace            = "portworx"
)

func (h *Handler) deleteComponents(cluster *corev1.StorageCluster) error {
	if err := h.deleteStorkComponent(cluster); err != nil {
		return err
	}
	if err := h.deleteAutopilotComponent(cluster); err != nil {
		return err
	}
	if err := h.deleteCSIComponent(cluster); err != nil {
		return err
	}
	if err := h.deletePVCControllerComponent(cluster); err != nil {
		return err
	}
	if err := h.deleteMonitoringComponent(cluster); err != nil {
		return err
	}
	if err := h.deletePortworxAPI(cluster); err != nil {
		return err
	}
	if err := h.deletePortworxRbac(cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deletePortworxRbac(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject(pxClusterRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pxClusterRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pxRoleBindingName, cluster.Namespace, &rbacv1.RoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pxRoleName, cluster.Namespace, &rbacv1.Role{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pxRoleBindingName, secretsNamespace, &rbacv1.RoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pxRoleName, secretsNamespace, &rbacv1.Role{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pxAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deletePortworxAPI(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject(pxAPIDaemonSetName, cluster.Namespace, &appsv1.DaemonSet{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pxAPIServiceName, cluster.Namespace, &v1.Service{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteStorkComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject(storkDeploymentName, cluster.Namespace, &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(storkServiceName, cluster.Namespace, &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(storkConfigName, cluster.Namespace, &v1.ConfigMap{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(storkRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(storkRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(storkAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(schedDeploymentName, cluster.Namespace, &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(schedRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(schedRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(schedAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteAutopilotComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject(autopilotDeploymentName, cluster.Namespace, &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(autopilotServiceName, cluster.Namespace, &v1.Service{}, cluster); err != nil {
		return err
	}
	// TODO: The config should be copied to the storage cluster spec before deleting,
	// so user modifications are persisted
	if err := h.deleteObject(autopilotConfigName, cluster.Namespace, &v1.ConfigMap{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(autopilotRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(autopilotRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(autopilotAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deletePVCControllerComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject(pvcDeploymentName, cluster.Namespace, &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pvcRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pvcRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(pvcAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteCSIComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject(csiDeploymentName, cluster.Namespace, &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(csiServiceName, cluster.Namespace, &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(csiRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(csiRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(csiAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteMonitoringComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject(serviceMonitorName, cluster.Namespace, &monitoringv1.ServiceMonitor{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusRuleName, cluster.Namespace, &monitoringv1.PrometheusRule{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(alertManagerName, cluster.Namespace, &monitoringv1.Alertmanager{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(alertManagerServiceName, cluster.Namespace, &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusInstanceName, cluster.Namespace, &monitoringv1.Prometheus{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusServiceName, cluster.Namespace, &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusOpDeploymentName, cluster.Namespace, &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusOpRoleBindingName, "", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusOpRoleName, "", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject(prometheusOpAccountName, cluster.Namespace, &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteObject(
	name, namespace string,
	obj client.Object,
	cluster *corev1.StorageCluster,
) error {
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		obj,
	)
	if meta.IsNoMatchError(err) {
		return nil
	} else if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	// Delete the object if it is not owned by the StorageCluster
	owner := metav1.GetControllerOf(obj)
	if owner == nil || owner.UID != cluster.UID {
		return h.client.Delete(context.TODO(), obj)
	}
	return nil
}
