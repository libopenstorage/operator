package component

import (
	"context"
	"os"
	"reflect"
	"sort"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	GrafanaServiceName       = "px-grafana"
	grafanaDefaultCPU        = "0.1"
	grafanaDefaultMemory     = "0.1"
	grafanaPort              = 3000
	grafanaLoginEndpoint     = "/login"
	pxGrafanaDashboardConfig = "grafana-dashboard-config.yaml"
	pxGrafanaDatasource      = "grafana-datasource.yaml"
	pxClusterDashboard       = "portworx-cluster-dashboard.json"
	pxEtcdDashboard          = "portworx-etcd-dashboard.json"
	pxNodeDashboard          = "portworx-node-dashboard.json"
	pxPerformanceDashboard   = "portworx-performance-dashboard.json"
	pxVolumeDashboard        = "portworx-volume-dashboard.json"

	PXGrafanaDatasourceConfigConfigMap = "px-grafana-datasource-config"
	PXGrafanaDashboardConfigConfigMap  = "px-grafana-dashboard-config"
	PXGrafanaDashboardsJsonConfigMap   = "px-grafana-dashboards-json"

	// GrafanaDeploymentName is the name for the operator managed grafana
	GrafanaDeploymentName = "px-grafana"
	// GrafanaContainerName is the name for the grafana container
	GrafanaContainerName = "grafana"
	// GrafanaComponentName is the name for identifying the grafana component
	GrafanaComponentName = "grafana"
)

var (
	grafanaDeploymentVolumes = []corev1.VolumeSpec{
		{
			Name:      "grafana-source-config",
			MountPath: "/etc/grafana/provisioning/datasources",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: PXGrafanaDatasourceConfigConfigMap,
					},
				},
			},
		},
		{
			Name:      "grafana-dashboard-config",
			MountPath: "/etc/grafana/provisioning/dashboards",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: PXGrafanaDashboardConfigConfigMap,
					},
				},
			},
		},
		{
			Name:      "grafana-dashboard-templates",
			MountPath: "/var/lib/grafana/dashboards",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: PXGrafanaDashboardsJsonConfigMap,
					},
				},
			},
		},
	}
)

type grafana struct {
	k8sClient        client.Client
	scheme           *runtime.Scheme
	recorder         record.EventRecorder
	isGrafanaCreated bool
}

func (c *grafana) Name() string {
	return GrafanaComponentName
}

func (c *grafana) Priority() int32 {
	return DefaultComponentPriority
}

func (c *grafana) Initialize(
	k8sClient client.Client,
	_ version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.scheme = scheme
	c.recorder = recorder
}

func (c *grafana) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *grafana) IsEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Monitoring != nil &&
		cluster.Spec.Monitoring.Grafana != nil &&
		cluster.Spec.Monitoring.Grafana.Enabled &&
		cluster.Spec.Monitoring.Prometheus != nil &&
		cluster.Spec.Monitoring.Prometheus.Enabled
}

func (c *grafana) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	if err := c.createConfigmapFromFiles([]string{pxGrafanaDashboardConfig}, PXGrafanaDashboardConfigConfigMap, cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createConfigmapFromFiles([]string{pxGrafanaDatasource}, PXGrafanaDatasourceConfigConfigMap, cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createConfigmapFromFiles([]string{pxClusterDashboard, pxEtcdDashboard, pxNodeDashboard, pxPerformanceDashboard, pxVolumeDashboard},
		PXGrafanaDashboardsJsonConfigMap, cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createGrafanaDeployment(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createGrafanaService(cluster, ownerRef); err != nil {
		return err
	}

	return nil
}

func (c *grafana) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	err := k8sutil.DeleteService(c.k8sClient, GrafanaServiceName, cluster.Namespace, *ownerRef)
	if err != nil {
		return err
	}
	err = k8sutil.DeleteDeployment(c.k8sClient, GrafanaDeploymentName, cluster.Namespace, *ownerRef)
	if err != nil {
		return err
	}
	err = k8sutil.DeleteConfigMap(c.k8sClient, PXGrafanaDashboardConfigConfigMap, cluster.Namespace, *ownerRef)
	if err != nil {
		return err
	}
	err = k8sutil.DeleteConfigMap(c.k8sClient, PXGrafanaDashboardsJsonConfigMap, cluster.Namespace, *ownerRef)
	if err != nil {
		return err
	}
	err = k8sutil.DeleteConfigMap(c.k8sClient, PXGrafanaDatasourceConfigConfigMap, cluster.Namespace, *ownerRef)
	if err != nil {
		return err
	}

	return nil
}

// MarkDeleted marks the component as deleted in situations like StorageCluster deletion
func (c *grafana) MarkDeleted() {}

func (c *grafana) createGrafanaDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	imageName := c.getDesiredGrafanaImage(cluster)
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
	imageName = util.GetImageURN(cluster, imageName)

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

func (c *grafana) getDesiredGrafanaVolumesAndMounts(cluster *corev1.StorageCluster) ([]v1.Volume, []v1.VolumeMount) {
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

func (c *grafana) getGrafanaDeploymentSpec(
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
	selectorLabels := map[string]string{
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
				MatchLabels: selectorLabels,
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
	deployment.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)

	return deployment
}

func (c *grafana) createGrafanaService(
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
				Name:            GrafanaServiceName,
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

func (c *grafana) getDesiredGrafanaImage(cluster *corev1.StorageCluster) string {
	if cluster.Status.DesiredImages != nil {
		return cluster.Status.DesiredImages.Grafana
	}
	return ""
}

func (c *grafana) createConfigmapFromFiles(filenames []string, name, ns string, ownerRef *metav1.OwnerReference) error {
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
func RegisterGrafanaComponent() {
	Register(GrafanaComponentName, &grafana{})
}

func init() {
	RegisterGrafanaComponent()
}
