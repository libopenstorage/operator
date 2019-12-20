package k8s

import (
	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PrometheusOps is an interface to perform Prometheus object operations
type PrometheusOps interface {
	ServiceMonitorOps
	PrometheusPodOps
	PrometheusRuleOps
	AlertManagerOps
}

// PrometheusPodOps is an interface to perform Prometheus operations
type PrometheusPodOps interface {
	// ListPrometheuses lists all prometheus instances in a given namespace
	ListPrometheuses(namespace string) (*monitoringv1.PrometheusList, error)
	// GetPrometheus gets the prometheus instance that matches the given name
	GetPrometheus(name, namespace string) (*monitoringv1.Prometheus, error)
	// CreatePrometheus creates the given prometheus
	CreatePrometheus(*monitoringv1.Prometheus) (*monitoringv1.Prometheus, error)
	// UpdatePrometheus updates the given prometheus
	UpdatePrometheus(*monitoringv1.Prometheus) (*monitoringv1.Prometheus, error)
	// DeletePrometheus deletes the given prometheus
	DeletePrometheus(name, namespace string) error
}

// ServiceMonitorOps is an interface to perform ServiceMonitor operations
type ServiceMonitorOps interface {
	// ListServiceMonitors lists all servicemonitors in a given namespace
	ListServiceMonitors(namespace string) (*monitoringv1.ServiceMonitorList, error)
	// GetServiceMonitor gets the service monitor instance that matches the given name
	GetServiceMonitor(name, namespace string) (*monitoringv1.ServiceMonitor, error)
	// CreateServiceMonitor creates the given service monitor
	CreateServiceMonitor(*monitoringv1.ServiceMonitor) (*monitoringv1.ServiceMonitor, error)
	// UpdateServiceMonitor updates the given service monitor
	UpdateServiceMonitor(*monitoringv1.ServiceMonitor) (*monitoringv1.ServiceMonitor, error)
	// DeleteServiceMonitor deletes the given service monitor
	DeleteServiceMonitor(name, namespace string) error
}

// PrometheusRuleOps is an interface to perform PrometheusRule operations
type PrometheusRuleOps interface {
	// ListPrometheusRule creates the given prometheus rule
	ListPrometheusRules(namespace string) (*monitoringv1.PrometheusRuleList, error)
	// GetPrometheusRule gets the prometheus rule that matches the given name
	GetPrometheusRule(name, namespace string) (*monitoringv1.PrometheusRule, error)
	// CreatePrometheusRule creates the given prometheus rule
	CreatePrometheusRule(*monitoringv1.PrometheusRule) (*monitoringv1.PrometheusRule, error)
	// UpdatePrometheusRule updates the given prometheus rule
	UpdatePrometheusRule(*monitoringv1.PrometheusRule) (*monitoringv1.PrometheusRule, error)
	// DeletePrometheusRule deletes the given prometheus rule
	DeletePrometheusRule(name, namespace string) error
}

// AlertManagerOps is an interface to perform AlertManager operations
type AlertManagerOps interface {
	// ListAlertManagerss lists all alertmanager instances in a given namespace
	ListAlertManagers(namespace string) (*monitoringv1.AlertmanagerList, error)
	// GetAlertManager gets the alert manager that matches the given name
	GetAlertManager(name, namespace string) (*monitoringv1.Alertmanager, error)
	// CreateAlertManager creates the given alert manager
	CreateAlertManager(*monitoringv1.Alertmanager) (*monitoringv1.Alertmanager, error)
	// UpdateAlertManager updates the given alert manager
	UpdateAlertManager(*monitoringv1.Alertmanager) (*monitoringv1.Alertmanager, error)
	// DeleteAlertManager deletes the given alert manager
	DeleteAlertManager(name, namespace string) error
}

func (k *k8sOps) ListPrometheuses(namespace string) (*monitoringv1.PrometheusList, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().Prometheuses(namespace).List(metav1.ListOptions{})
}

func (k *k8sOps) GetPrometheus(name string, namespace string) (*monitoringv1.Prometheus, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().Prometheuses(namespace).Get(name, metav1.GetOptions{})
}

func (k *k8sOps) CreatePrometheus(prometheus *monitoringv1.Prometheus) (*monitoringv1.Prometheus, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	ns := prometheus.Namespace
	if len(ns) == 0 {
		ns = v1.NamespaceDefault
	}

	return k.prometheusClient.MonitoringV1().Prometheuses(ns).Create(prometheus)
}

func (k *k8sOps) UpdatePrometheus(prometheus *monitoringv1.Prometheus) (*monitoringv1.Prometheus, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().Prometheuses(prometheus.Namespace).Update(prometheus)
}

func (k *k8sOps) DeletePrometheus(name, namespace string) error {
	if err := k.initK8sClient(); err != nil {
		return err
	}

	return k.prometheusClient.MonitoringV1().Prometheuses(namespace).Delete(name, &metav1.DeleteOptions{
		PropagationPolicy: &deleteForegroundPolicy,
	})
}

func (k *k8sOps) ListServiceMonitors(namespace string) (*monitoringv1.ServiceMonitorList, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().ServiceMonitors(namespace).List(metav1.ListOptions{})
}

func (k *k8sOps) GetServiceMonitor(name string, namespace string) (*monitoringv1.ServiceMonitor, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().ServiceMonitors(namespace).Get(name, metav1.GetOptions{})
}

func (k *k8sOps) CreateServiceMonitor(serviceMonitor *monitoringv1.ServiceMonitor) (*monitoringv1.ServiceMonitor, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	ns := serviceMonitor.Namespace
	if len(ns) == 0 {
		ns = v1.NamespaceDefault
	}

	return k.prometheusClient.MonitoringV1().ServiceMonitors(ns).Create(serviceMonitor)
}

func (k *k8sOps) UpdateServiceMonitor(serviceMonitor *monitoringv1.ServiceMonitor) (*monitoringv1.ServiceMonitor, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().ServiceMonitors(serviceMonitor.Namespace).Update(serviceMonitor)
}

func (k *k8sOps) DeleteServiceMonitor(name, namespace string) error {
	if err := k.initK8sClient(); err != nil {
		return err
	}

	return k.prometheusClient.MonitoringV1().ServiceMonitors(namespace).Delete(name, &metav1.DeleteOptions{
		PropagationPolicy: &deleteForegroundPolicy,
	})
}

func (k *k8sOps) ListPrometheusRules(namespace string) (*monitoringv1.PrometheusRuleList, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().PrometheusRules(namespace).List(metav1.ListOptions{})
}

func (k *k8sOps) GetPrometheusRule(name string, namespace string) (*monitoringv1.PrometheusRule, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().PrometheusRules(namespace).Get(name, metav1.GetOptions{})
}

func (k *k8sOps) CreatePrometheusRule(rule *monitoringv1.PrometheusRule) (*monitoringv1.PrometheusRule, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	ns := rule.Namespace
	if len(ns) == 0 {
		ns = v1.NamespaceDefault
	}

	return k.prometheusClient.MonitoringV1().PrometheusRules(ns).Create(rule)
}

func (k *k8sOps) UpdatePrometheusRule(rule *monitoringv1.PrometheusRule) (*monitoringv1.PrometheusRule, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().PrometheusRules(rule.Namespace).Update(rule)
}

func (k *k8sOps) DeletePrometheusRule(name, namespace string) error {
	if err := k.initK8sClient(); err != nil {
		return err
	}

	return k.prometheusClient.MonitoringV1().PrometheusRules(namespace).Delete(name, &metav1.DeleteOptions{
		PropagationPolicy: &deleteForegroundPolicy,
	})
}

func (k *k8sOps) ListAlertManagers(namespace string) (*monitoringv1.AlertmanagerList, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().Alertmanagers(namespace).List(metav1.ListOptions{})
}

func (k *k8sOps) GetAlertManager(name string, namespace string) (*monitoringv1.Alertmanager, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().Alertmanagers(namespace).Get(name, metav1.GetOptions{})
}

func (k *k8sOps) CreateAlertManager(alertmanager *monitoringv1.Alertmanager) (*monitoringv1.Alertmanager, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	ns := alertmanager.Namespace
	if len(ns) == 0 {
		ns = v1.NamespaceDefault
	}

	return k.prometheusClient.MonitoringV1().Alertmanagers(ns).Create(alertmanager)
}

func (k *k8sOps) UpdateAlertManager(alertmanager *monitoringv1.Alertmanager) (*monitoringv1.Alertmanager, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.prometheusClient.MonitoringV1().Alertmanagers(alertmanager.Namespace).Update(alertmanager)
}

func (k *k8sOps) DeleteAlertManager(name, namespace string) error {
	if err := k.initK8sClient(); err != nil {
		return err
	}

	return k.prometheusClient.MonitoringV1().Alertmanagers(namespace).Delete(name, &metav1.DeleteOptions{
		PropagationPolicy: &deleteForegroundPolicy,
	})
}
