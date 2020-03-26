package component

import (
	"fmt"
	"path"

	monitoringapi "github.com/coreos/prometheus-operator/pkg/apis/monitoring"
	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metaerrors "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// MonitoringComponentName name of the Monitoring component
	MonitoringComponentName = "Monitoring"
	// PxServiceMonitor name of the Portworx service monitor
	PxServiceMonitor = "portworx"
	// PxBackupServiceMonitor name of the px backup service monitor
	PxBackupServiceMonitor = "px-backup"
	// PxPrometheusRule name of the prometheus rule object for Portworx
	PxPrometheusRule = "portworx"

	pxPrometheusRuleFile = "portworx-prometheus-rule.yaml"
)

type monitoring struct {
	k8sClient client.Client
	scheme    *runtime.Scheme
	recorder  record.EventRecorder
}

func (c *monitoring) Initialize(
	k8sClient client.Client,
	_ version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.scheme = scheme
	c.recorder = recorder
}

func (c *monitoring) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.ExportMetrics
}

func (c *monitoring) Reconcile(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createServiceMonitor(cluster, ownerRef); metaerrors.IsNoMatchError(err) {
		if success := c.retryCreate(monitoringv1.ServiceMonitorsKind, c.createServiceMonitor, cluster, err); !success {
			return nil
		}
	} else if err != nil {
		return err
	}
	if err := c.createPrometheusRule(cluster, ownerRef); metaerrors.IsNoMatchError(err) {
		if success := c.retryCreate(monitoringv1.PrometheusRuleKind, c.createPrometheusRule, cluster, err); !success {
			return nil
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (c *monitoring) Delete(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	err := k8sutil.DeleteServiceMonitor(c.k8sClient, PxServiceMonitor, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	err = k8sutil.DeletePrometheusRule(c.k8sClient, PxPrometheusRule, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	return nil
}

func (c *monitoring) MarkDeleted() {}

func (c *monitoring) createServiceMonitor(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	svcMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:            PxServiceMonitor,
			Namespace:       cluster.Namespace,
			Labels:          serviceMonitorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Endpoints: []monitoringv1.Endpoint{
				{
					Port: pxutil.PortworxRESTPortName,
				},
			},
			NamespaceSelector: monitoringv1.NamespaceSelector{
				Any: true,
			},
			Selector: metav1.LabelSelector{
				MatchLabels: pxutil.SelectorLabels(),
			},
		},
	}

	// In case kvdb spec is nil, we will default to internal kvdb
	if cluster.Spec.Kvdb == nil || cluster.Spec.Kvdb.Internal {
		svcMonitor.Spec.Endpoints = append(
			svcMonitor.Spec.Endpoints,
			monitoringv1.Endpoint{Port: pxutil.PortworxKVDBPortName},
		)
	}

	return k8sutil.CreateOrUpdateServiceMonitor(c.k8sClient, svcMonitor, ownerRef)
}

func (c *monitoring) createPrometheusRule(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	filename := path.Join(pxutil.SpecsBaseDir(), pxPrometheusRuleFile)
	prometheusRule := &monitoringv1.PrometheusRule{}
	if err := k8sutil.ParseObjectFromFile(filename, c.scheme, prometheusRule); err != nil {
		return err
	}
	prometheusRule.ObjectMeta = metav1.ObjectMeta{
		Name:            PxPrometheusRule,
		Namespace:       cluster.Namespace,
		Labels:          prometheusRuleLabels(),
		OwnerReferences: []metav1.OwnerReference{*ownerRef},
	}
	return k8sutil.CreateOrUpdatePrometheusRule(c.k8sClient, prometheusRule, ownerRef)
}

func (c *monitoring) warningEvent(
	cluster *corev1alpha1.StorageCluster,
	reason, message string,
) {
	logrus.Warn(message)
	c.recorder.Event(cluster, v1.EventTypeWarning, reason, message)
}

func (c *monitoring) retryCreate(
	kind string,
	createFunc func(*corev1alpha1.StorageCluster, *metav1.OwnerReference) error,
	cluster *corev1alpha1.StorageCluster,
	err error,
) bool {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	gvk := schema.GroupVersionKind{
		Group:   monitoringapi.GroupName,
		Version: monitoringv1.Version,
		Kind:    kind,
	}
	if resourcePresent, _ := k8s.Instance().ResourceExists(gvk); resourcePresent {
		var clnt client.Client
		clnt, err = k8sutil.NewK8sClient(c.scheme)
		if err == nil {
			c.k8sClient = clnt
			err = createFunc(cluster, ownerRef)
		}
	}
	if err != nil {
		c.warningEvent(cluster, util.FailedComponentReason,
			fmt.Sprintf("Failed to create %s object for Portworx. Ensure Prometheus is deployed correctly. %v", kind, err))
		return false
	}
	return true
}

func serviceMonitorLabels() map[string]string {
	return map[string]string{
		"name": PxServiceMonitor,
		"app":  PxBackupServiceMonitor,
	}
}

func prometheusRuleLabels() map[string]string {
	return map[string]string{
		"prometheus": "portworx",
	}
}

// RegisterMonitoringComponent registers the Monitoring component
func RegisterMonitoringComponent() {
	Register(MonitoringComponentName, &monitoring{})
}

func init() {
	RegisterMonitoringComponent()
}
