package migration

import (
	"context"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	if err := h.deleteObject("node-role-binding", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("node-get-put-list-role", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("px-role-binding", &rbacv1.RoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("px-role", &rbacv1.Role{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("px-account", &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deletePortworxAPI(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject("portworx-api", &appsv1.DaemonSet{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("portworx-api", &v1.Service{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteStorkComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject("stork", &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-service", &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-config", &v1.ConfigMap{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-role-binding", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-role", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-account", &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-scheduler", &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-scheduler-role-binding", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-scheduler-role", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("stork-scheduler-account", &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteAutopilotComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject("autopilot", &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("autopilot", &v1.Service{}, cluster); err != nil {
		return err
	}
	// TODO: The config should be copied to the storage cluster spec before deleting,
	// so user modifications are persisted
	if err := h.deleteObject("autopilot-config", &v1.ConfigMap{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("autopilot-role-binding", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("autopilot-role", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("autopilot-account", &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deletePVCControllerComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject("portworx-pvc-controller", &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("portworx-pvc-controller-role-binding", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("portworx-pvc-controller-role", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("portworx-pvc-controller-account", &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteCSIComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject("px-csi-ext", &appsv1.Deployment{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("px-csi-service", &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("px-csi-role-binding", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("px-csi-role", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("px-csi-account", &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteMonitoringComponent(cluster *corev1.StorageCluster) error {
	if err := h.deleteObject("portworx-prometheus-sm", &monitoringv1.ServiceMonitor{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("portworx", &monitoringv1.PrometheusRule{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("portworx", &monitoringv1.Alertmanager{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("alertmanager-portworx", &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("prometheus", &monitoringv1.Prometheus{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("prometheus", &v1.Service{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("prometheus", &rbacv1.ClusterRoleBinding{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("prometheus", &rbacv1.ClusterRole{}, cluster); err != nil {
		return err
	}
	if err := h.deleteObject("prometheus", &v1.ServiceAccount{}, cluster); err != nil {
		return err
	}
	return nil
}

func (h *Handler) deleteObject(
	name string,
	obj client.Object,
	cluster *corev1.StorageCluster,
) error {
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: cluster.Namespace,
		},
		obj,
	)
	if errors.IsNotFound(err) {
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
