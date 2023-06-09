package component

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringapi "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metaerrors "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// AlertManagerComponentName name of the AlertManager component
	AlertManagerComponentName = "AlertManager"
	// AlertManagerServiceName name of the alert manager service
	AlertManagerServiceName = "alertmanager-portworx"
	// AlertManagerInstanceName name of the alert manager instance
	AlertManagerInstanceName = "portworx"
	// AlertManagerConfigSecretName name of the secret containing alert manager config
	AlertManagerConfigSecretName = "alertmanager-portworx"

	alertManagerPortName = "web"
	alertManagerLabelKey = "alertmanager"
)

type alertManager struct {
	k8sClient client.Client
	scheme    *runtime.Scheme
	recorder  record.EventRecorder
}

func (c *alertManager) Name() string {
	return AlertManagerComponentName
}

func (c *alertManager) Priority() int32 {
	return DefaultComponentPriority
}

func (c *alertManager) Initialize(
	k8sClient client.Client,
	_ version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.scheme = scheme
	c.recorder = recorder
}

func (c *alertManager) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *alertManager) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.AlertManager != nil &&
		cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled
}

func (c *alertManager) Reconcile(cluster *corev1.StorageCluster) error {
	err := c.k8sClient.Get(context.TODO(), types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      AlertManagerConfigSecretName,
	}, &v1.Secret{})
	if errors.IsNotFound(err) {
		c.warningEvent(cluster, util.FailedComponentReason,
			fmt.Sprintf("Missing secret '%s/%s' containing alert manager config. Create the secret with "+
				"a valid config to use Alert Manager.", cluster.Namespace, AlertManagerConfigSecretName))
		return nil
	} else if err != nil {
		return err
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createAlertManagerService(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createAlertManagerInstance(cluster, ownerRef); metaerrors.IsNoMatchError(err) {
		gvk := schema.GroupVersionKind{
			Group:   monitoringapi.GroupName,
			Version: monitoringv1.Version,
			Kind:    monitoringv1.AlertmanagersKind,
		}
		if resourcePresent, _ := coreops.Instance().ResourceExists(gvk); resourcePresent {
			var clnt client.Client
			clnt, err = k8sutil.NewK8sClient(c.scheme)
			if err == nil {
				c.k8sClient = clnt
				err = c.createAlertManagerInstance(cluster, ownerRef)
			}
		}
		if err != nil {
			c.warningEvent(cluster, util.FailedComponentReason,
				fmt.Sprintf("Failed to create AlertManager object for Portworx. Ensure Prometheus is deployed correctly. %v", err))
			return nil
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (c *alertManager) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteService(c.k8sClient, AlertManagerServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	err := k8sutil.DeleteAlertManager(c.k8sClient, AlertManagerInstanceName, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	return nil
}

func (c *alertManager) MarkDeleted() {
}

func (c *alertManager) createAlertManagerService(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            AlertManagerServiceName,
			Namespace:       clusterNamespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: alertManagerLabels(),
			Ports: []v1.ServicePort{
				{
					Name:       alertManagerPortName,
					Port:       int32(9093),
					TargetPort: intstr.FromInt(9093),
				},
			},
		},
	}

	return k8sutil.CreateOrUpdateService(c.k8sClient, newService, ownerRef)
}

func (c *alertManager) createAlertManagerInstance(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	replicas := int32(3)
	imageName := util.GetImageURN(cluster, cluster.Status.DesiredImages.AlertManager)

	alertManagerInst := &monitoringv1.Alertmanager{
		ObjectMeta: metav1.ObjectMeta{
			Name:            AlertManagerInstanceName,
			Namespace:       cluster.Namespace,
			Labels:          alertManagerLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: monitoringv1.AlertmanagerSpec{
			Image:    &imageName,
			Replicas: &replicas,
		},
	}

	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		alertManagerInst.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *cluster.Spec.ImagePullSecret,
			},
		)
	}

	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			alertManagerInst.Spec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(cluster.Spec.Placement.Tolerations) > 0 {
			alertManagerInst.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range cluster.Spec.Placement.Tolerations {
				alertManagerInst.Spec.Tolerations = append(
					alertManagerInst.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	return k8sutil.CreateOrUpdateAlertManager(c.k8sClient, alertManagerInst, ownerRef)
}

func alertManagerLabels() map[string]string {
	return map[string]string{
		alertManagerLabelKey: AlertManagerInstanceName,
	}
}

func (c *alertManager) warningEvent(
	cluster *corev1.StorageCluster,
	reason, message string,
) {
	logrus.Warn(message)
	c.recorder.Event(cluster, v1.EventTypeWarning, reason, message)
}

// RegisterAlertManagerComponent registers the AlertManager component
func RegisterAlertManagerComponent() {
	Register(AlertManagerComponentName, &alertManager{})
}

func init() {
	RegisterAlertManagerComponent()
}
