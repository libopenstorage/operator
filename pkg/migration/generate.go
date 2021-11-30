package migration

import (
	"context"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

const (
	storkDeploymentName              = "stork"
	prometheusOperatorDeploymentName = "prometheus-operator"
	serviceMonitorName               = "portworx-prometheus-sm"
)

func (h *Handler) addStorkSpec(cluster *corev1.StorageCluster) error {
	dep, err := h.getDeployment(storkDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	}
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: dep != nil,
	}
	return nil
}

func (h *Handler) addAutopilotSpec(cluster *corev1.StorageCluster) error {
	dep, err := h.getDeployment(component.AutopilotDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	}
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: dep != nil,
	}
	return nil
}

func (h *Handler) addPVCControllerSpec(cluster *corev1.StorageCluster) error {
	dep, err := h.getDeployment(component.PVCDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	} else if dep == nil {
		return nil
	}
	// Explicitly enable pvc controller in kube system namespace as operator won't
	// enable it by default if running in kube-system namespace
	if cluster.Namespace == "kube-system" {
		cluster.Annotations[pxutil.AnnotationPVCController] = "true"
	}
	return nil
}

func (h *Handler) addMonitoringSpec(cluster *corev1.StorageCluster) error {
	// Check if metrics need to be exported
	svcMonitorFound := true
	svcMonitor := &monitoringv1.ServiceMonitor{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      serviceMonitorName,
			Namespace: cluster.Namespace,
		},
		svcMonitor,
	)
	if errors.IsNotFound(err) {
		svcMonitorFound = false
	} else if err != nil {
		return err
	}
	if cluster.Spec.Monitoring == nil {
		cluster.Spec.Monitoring = &corev1.MonitoringSpec{}
	}
	cluster.Spec.Monitoring.Prometheus = &corev1.PrometheusSpec{
		ExportMetrics: svcMonitorFound,
	}

	// Check for alert manager
	alertManagerFound := true
	alertManager := &monitoringv1.Alertmanager{}
	err = h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      component.AlertManagerInstanceName,
			Namespace: cluster.Namespace,
		},
		alertManager,
	)
	if errors.IsNotFound(err) {
		alertManagerFound = false
	} else if err != nil {
		return err
	}
	cluster.Spec.Monitoring.Prometheus.AlertManager = &corev1.AlertManagerSpec{
		Enabled: alertManagerFound,
	}

	if !cluster.Spec.Monitoring.Prometheus.ExportMetrics &&
		!cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled {
		return nil
	}

	// Check for prometheus operator
	dep, err := h.getDeployment(prometheusOperatorDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	}
	if dep != nil {
		if dep.Labels["k8s-app"] != prometheusOperatorDeploymentName {
			return nil
		}
		cluster.Spec.Monitoring.Prometheus.Enabled = true
	}

	return nil
}

func (h *Handler) getDeployment(name, namespace string) (*appsv1.Deployment, error) {
	dep := &appsv1.Deployment{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		dep,
	)
	if errors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return dep, nil
}
