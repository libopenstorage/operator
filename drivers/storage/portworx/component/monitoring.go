package component

import (
	"context"
	"fmt"
	"os"
	"path"
	"reflect"
	"sort"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringapi "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
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
	// MonitoringComponentName name of the Monitoring component
	MonitoringComponentName = "Monitoring"
	// PxServiceMonitor name of the Portworx service monitor
	PxServiceMonitor = "portworx"
	// PxBackupServiceMonitor name of the px backup service monitor
	PxBackupServiceMonitor = "px-backup"
	// PxPrometheusRule name of the prometheus rule object for Portworx
	PxPrometheusRule = "portworx"
	// GrafanaDeploymentName is the name for the operator managed grafana
	GrafanaDeploymentName = "px-grafana"
	// GrafanaContainerName is the name for the grafana container
	GrafanaContainerName = "grafana"

	grafanaServiceName   = "px-grafana"
	grafanaDefaultCPU    = "0.1"
	grafanaDefaultMemory = "0.1"
	grafanaPort          = 3000
	grafanaLoginEndpoint = "/login"
	pxPrometheusRuleFile = "portworx-prometheus-rule.yaml"

	pxGrafanaDashboardConfig = "grafana-dashboard-config.yaml"
	pxGrafanaDatasource      = "grafana-datasource.yaml"
	pxClusterDashboard       = "portworx-cluster-dashboard.json"
	pxEtcdDashboard          = "portworx-etcd-dashboard.json"
	pxNodeDashboard          = "portworx-node-dashboard.json"
	pxPerformanceDashboard   = "portworx-performance-dashboard.json"
	pxVolumeDashboard        = "portworx-volume-dashboard.json"

	pxGrafanaDatasourceConfigConfigMap = "px-grafana-datasource-config"
	pxGrafanaDashboardConfigConfigMap  = "px-grafana-dashboard-config"
	pxGrafanaDashboardsJsonConfigMap   = "px-grafana-dashboards-json"
)

var (
	grafanaDeploymentVolumes = []corev1.VolumeSpec{
		{
			Name:      "grafana-source-config",
			MountPath: "/etc/grafana/provisioning/datasources",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: pxGrafanaDatasourceConfigConfigMap,
					},
				},
			},
		},
		{
			Name:      "grafana-dash-config",
			MountPath: "/etc/grafana/provisioning/dashboards",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: pxGrafanaDashboardConfigConfigMap,
					},
				},
			},
		},
		{
			Name:      "dashboard-templates",
			MountPath: "/var/lib/grafana/dashboards",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: pxGrafanaDashboardsJsonConfigMap,
					},
				},
			},
		},
	}
)

type monitoring struct {
	k8sClient        client.Client
	scheme           *runtime.Scheme
	recorder         record.EventRecorder
	isGrafanaCreated bool
}

func (c *monitoring) Name() string {
	return MonitoringComponentName
}

func (c *monitoring) Priority() int32 {
	return DefaultComponentPriority
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

func (c *monitoring) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *monitoring) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.ExportMetrics
}

func (c *monitoring) Reconcile(cluster *corev1.StorageCluster) error {
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

	if cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.Grafana != nil &&
		cluster.Spec.Monitoring.Grafana.Enabled {
		if err := c.createConfigmapFromFiles([]string{pxGrafanaDashboardConfig}, pxGrafanaDashboardConfigConfigMap, cluster.Namespace, ownerRef); err != nil {
			return err
		}
		if err := c.createConfigmapFromFiles([]string{pxGrafanaDatasource}, pxGrafanaDatasourceConfigConfigMap, cluster.Namespace, ownerRef); err != nil {
			return err
		}
		if err := c.createConfigmapFromFiles([]string{pxClusterDashboard, pxEtcdDashboard, pxNodeDashboard, pxPerformanceDashboard, pxVolumeDashboard},
			pxGrafanaDashboardsJsonConfigMap, cluster.Namespace, ownerRef); err != nil {
			return err
		}
		if err := c.createGrafanaDeployment(cluster, ownerRef); err != nil {
			return err
		}
		if err := c.createGrafanaService(cluster, ownerRef); err != nil {
			return err
		}
	}

	return nil
}

func (c *monitoring) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	err := k8sutil.DeleteServiceMonitor(c.k8sClient, PxServiceMonitor, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	err = k8sutil.DeletePrometheusRule(c.k8sClient, PxPrometheusRule, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	err = k8sutil.DeleteDeployment(c.k8sClient, GrafanaDeploymentName, cluster.Namespace, *ownerRef)
	if err != nil && !metaerrors.IsNoMatchError(err) {
		return err
	}
	return nil
}

func (c *monitoring) MarkDeleted() {}

func (c *monitoring) createServiceMonitor(
	cluster *corev1.StorageCluster,
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
	cluster *corev1.StorageCluster,
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

func (c *monitoring) createGrafanaDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	imageName := c.getDesiredGrafanaImage(cluster)
	if imageName == "" {
		imageName = "grafana/grafana:10.0.1"
	}

	existingDeployment := &appsv1.Deployment{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      GrafanaDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	targetCPUQuantity, err := resource.ParseQuantity(grafanaDefaultCPU)
	if err != nil {
		return err
	}
	targetMemoryQuantity, err := resource.ParseQuantity(grafanaDefaultMemory)
	if err != nil {
		return err
	}

	volumes, volumeMounts := c.getDesiredGrafanaVolumesAndMounts(cluster)
	var existingImage string
	var existingMounts []v1.VolumeMount
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == GrafanaContainerName {
			existingImage = c.Image
			existingMounts = append([]v1.VolumeMount{}, c.VolumeMounts...)
			sort.Sort(k8sutil.VolumeMountByName(existingMounts))
			break
		}
	}
	existingVolumes := append([]v1.Volume{}, existingDeployment.Spec.Template.Spec.Volumes...)
	sort.Sort(k8sutil.VolumeByName(existingVolumes))

	targetDeployment := c.getGrafanaDeploymentSpec(cluster, ownerRef, imageName,
		volumes, volumeMounts, targetCPUQuantity, targetMemoryQuantity)
	// Check if the deployment has changed
	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingVolumes, volumes) ||
		!reflect.DeepEqual(existingMounts, volumeMounts) ||
		util.HasResourcesChanged(existingDeployment.Spec.Template.Spec.Containers[0].Resources, targetDeployment.Spec.Template.Spec.Containers[0].Resources) ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if !c.isGrafanaCreated || modified {
		if err = k8sutil.CreateOrUpdateDeployment(c.k8sClient, targetDeployment, ownerRef); err != nil {
			return err
		}
	}
	c.isGrafanaCreated = true
	return nil
}

func (c *monitoring) getDesiredGrafanaVolumesAndMounts(cluster *corev1.StorageCluster) ([]v1.Volume, []v1.VolumeMount) {
	volumeSpecs := make([]corev1.VolumeSpec, 0)
	for _, v := range grafanaDeploymentVolumes {
		vCopy := v.DeepCopy()
		volumeSpecs = append(volumeSpecs, *vCopy)
	}

	volumes, volumeMounts := util.ExtractVolumesAndMounts(volumeSpecs)
	sort.Sort(k8sutil.VolumeByName(volumes))
	sort.Sort(k8sutil.VolumeMountByName(volumeMounts))
	return volumes, volumeMounts
}

func (c *monitoring) getGrafanaDeploymentSpec(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	imageName string,
	volumes []v1.Volume,
	volumeMounts []v1.VolumeMount,
	cpuQuantity resource.Quantity,
	memoryQuantity resource.Quantity,
) *appsv1.Deployment {
	deploymentLabels := map[string]string{
		"app": "grafana",
	}
	templateLabels := map[string]string{
		"app": "grafana",
	}

	replicas := int32(1)
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            GrafanaDeploymentName,
			Namespace:       cluster.Namespace,
			Labels:          deploymentLabels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: templateLabels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: templateLabels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            GrafanaContainerName,
							Image:           imageName,
							ImagePullPolicy: imagePullPolicy,
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuQuantity,
								},
							},
							ReadinessProbe: &v1.Probe{
								ProbeHandler: v1.ProbeHandler{
									HTTPGet: &v1.HTTPGetAction{
										Path: grafanaLoginEndpoint,
										Port: intstr.FromInt(grafanaPort),
									},
								},
							},
							VolumeMounts: volumeMounts,
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
													GrafanaDeploymentName,
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

	// If resources is specified in the spec, the resources specified by annotation (such as portworx.io/autopilot-cpu)
	// will be overwritten.
	if cluster.Spec.Autopilot.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *cluster.Spec.Autopilot.Resources
	}

	return deployment
}

func (c *monitoring) createGrafanaService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	grafanaAppLabel := map[string]string{
		"app": "grafana",
	}
	return k8sutil.CreateOrUpdateService(
		c.k8sClient,
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            grafanaServiceName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
				Labels:          grafanaAppLabel,
			},
			Spec: v1.ServiceSpec{
				Type: v1.ServiceTypeClusterIP,
				Ports: []v1.ServicePort{
					{
						Port: grafanaPort,
					},
				},
				Selector: grafanaAppLabel,
			},
		},
		ownerRef,
	)
}

func (c *monitoring) warningEvent(
	cluster *corev1.StorageCluster,
	reason, message string,
) {
	logrus.Warn(message)
	c.recorder.Event(cluster, v1.EventTypeWarning, reason, message)
}

func (c *monitoring) retryCreate(
	kind string,
	createFunc func(*corev1.StorageCluster, *metav1.OwnerReference) error,
	cluster *corev1.StorageCluster,
	err error,
) bool {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	gvk := schema.GroupVersionKind{
		Group:   monitoringapi.GroupName,
		Version: monitoringv1.Version,
		Kind:    kind,
	}
	if resourcePresent, _ := coreops.Instance().ResourceExists(gvk); resourcePresent {
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

func (c *monitoring) getDesiredGrafanaImage(cluster *corev1.StorageCluster) string {
	if cluster.Status.DesiredImages != nil {
		return cluster.Status.DesiredImages.Grafana
	}
	return ""
}

func serviceMonitorLabels() map[string]string {
	return map[string]string{
		"name":       PxServiceMonitor,
		"prometheus": PxServiceMonitor,
	}
}

func prometheusRuleLabels() map[string]string {
	return map[string]string{
		"prometheus": "portworx",
	}
}

func (c *monitoring) createConfigmapFromFiles(filenames []string, name, ns string, ownerRef *metav1.OwnerReference) error {
	cm := &v1.ConfigMap{}
	cm.BinaryData = make(map[string][]byte)
	for _, filename := range filenames {
		fileDir := pxutil.SpecsBaseDir() + "/" + filename
		fileBytes, err := os.ReadFile(fileDir)
		if err != nil {
			return err
		}

		cm.BinaryData[filename] = fileBytes
	}

	cm.Name = name
	cm.Namespace = ns
	cm.OwnerReferences = []metav1.OwnerReference{*ownerRef}

	_, err := k8sutil.CreateOrUpdateConfigMap(c.k8sClient, cm, ownerRef)
	return err
}

// RegisterMonitoringComponent registers the Monitoring component
func RegisterMonitoringComponent() {
	Register(MonitoringComponentName, &monitoring{})
}

func init() {
	RegisterMonitoringComponent()
}
