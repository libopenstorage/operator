package migration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/portworx/sched-ops/task"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	"github.com/libopenstorage/operator/drivers/storage/portworx/mock"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
)

type ImageConfig struct {
	CustomImageRegistry string
	PortworxImage       string
	Components          manifest.Release
	PreserveFullPath    bool
}

func TestParseCustomImageRegistry(t *testing.T) {
	// Case: no custom registry
	pxImage := "portworx/oci-monitor:tag"
	desiredImages := []string{
		"portworx/stork:tag",
		"a/autopilot:tag",
		"docker.io/b/csi-provisioner:tag",
		"k8s.gcr.io/c/snapshot-controller:tag",
		"",
	}
	customRegistry, preserved := parseCustomImageRegistry(pxImage, desiredImages)
	require.Equal(t, "", customRegistry)
	require.False(t, preserved)

	// Case: custom registry with image names only
	pxImage = "registry.io/public:123/oci-monitor:tag"
	desiredImages = []string{
		"registry.io/public:123/stork:tag",
		"registry.io/public:123/autopilot:tag",
		"registry.io/public:123/csi-provisioner:tag",
		"registry.io/public:123/snapshot-controller:tag",
		"docker.io/openstorage/csi-attacher:tag",
		"",
	}
	customRegistry, preserved = parseCustomImageRegistry(pxImage, desiredImages)
	require.Equal(t, "registry.io/public:123", customRegistry)
	require.False(t, preserved)

	// Case: custom registry with full path
	pxImage = "registry.io/public:123/portworx/oci-monitor:tag"
	desiredImages = []string{
		"registry.io/public:123/a/stork:tag",
		"registry.io/public:123/portworx/autopilot:tag",
		"registry.io/public:123/k8s.gcr.io/openstorage/csi-provisioner:tag",
		"registry.io/public:123/sig-storage/snapshot-controller:tag",
		"",
	}
	customRegistry, preserved = parseCustomImageRegistry(pxImage, desiredImages)
	require.Equal(t, "registry.io/public:123", customRegistry)
	require.True(t, preserved)

	// Case: some component images don't have custom registry
	pxImage = "registry.io/oci-monitor:tag"
	desiredImages = []string{
		"registry.io/stork:tag",
		"registry.io/autopilot:tag",
		"k8s.gcr.io/openstorage/csi-provisioner:tag",
		"sig-storage/snapshot-controller:tag",
		"",
	}
	customRegistry, preserved = parseCustomImageRegistry(pxImage, desiredImages)
	require.Equal(t, "registry.io", customRegistry)
	require.False(t, preserved)

	pxImage = "registry.io/public:123/portworx/oci-monitor:tag"
	desiredImages = []string{
		"registry.io/public:123/a/stork:tag",
		"registry.io/public:123/portworx/autopilot:tag",
		"k8s.gcr.io/openstorage/csi-provisioner:tag",
		"sig-storage/snapshot-controller:tag",
		"",
	}
	customRegistry, preserved = parseCustomImageRegistry(pxImage, desiredImages)
	require.Equal(t, "registry.io/public:123", customRegistry)
	require.True(t, preserved)
}

func TestImageMigration(t *testing.T) {
	// Test without custom image registry.
	dsImages := ImageConfig{
		PortworxImage: "portworx/oci-monitor:2.9.1",
		Components: manifest.Release{
			Stork:                      "a/stork:2.7.0",
			Autopilot:                  "b/autopilot:1.3.1",
			Telemetry:                  "c/ccm-service:3.0.9",
			CSIProvisioner:             "quay.io/k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "quay.io/k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "quay.io/k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "quay.io/k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "quay.io/k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "quay.io/k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "quay.io/k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "quay.io/prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "quay.io/coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "quay.io/coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "quay.io/coreos/configmap-reload:v1.2.3",
			AlertManager:               "quay.io/prometheus/alertmanager:v1.2.3",
		},
	}

	expectedStcImages := ImageConfig{
		PortworxImage:       "portworx/oci-monitor:2.9.1",
		CustomImageRegistry: "",
		PreserveFullPath:    false,
		Components: manifest.Release{
			Stork:                      "a/stork:2.7.0",
			Autopilot:                  "b/autopilot:1.3.1",
			Telemetry:                  "c/ccm-service:3.0.9",
			CSIProvisioner:             "quay.io/k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "quay.io/k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "quay.io/k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "quay.io/k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "quay.io/k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "quay.io/k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "quay.io/k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "quay.io/prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "quay.io/coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "quay.io/coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "quay.io/coreos/configmap-reload:v1.2.3",
			AlertManager:               "quay.io/prometheus/alertmanager:v1.2.3",
		},
	}

	testImageMigration(t, dsImages, expectedStcImages, false, false)

	// Test with custom image registry. Component images should not have custom image registry prefix.
	dsImages = ImageConfig{
		PortworxImage: "test/oci-monitor:2.9.1",
		Components: manifest.Release{
			Stork:                      "test/a/stork:2.7.0",
			Autopilot:                  "test/b/autopilot:1.3.1",
			Telemetry:                  "test/c/ccm-service:3.0.9",
			CSIProvisioner:             "test/k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "test/k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "test/k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "test/k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "test/k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "test/k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "test/k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "test/prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "test/coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "test/coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "test/coreos/configmap-reload:v1.2.3",
			AlertManager:               "test/prometheus/alertmanager:v1.2.3",
		},
	}

	expectedStcImages = ImageConfig{
		PortworxImage:       "oci-monitor:2.9.1",
		CustomImageRegistry: "test",
		PreserveFullPath:    false,
		Components: manifest.Release{
			Stork:                      "a/stork:2.7.0",
			Autopilot:                  "b/autopilot:1.3.1",
			Telemetry:                  "c/ccm-service:3.0.9",
			CSIProvisioner:             "k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "coreos/configmap-reload:v1.2.3",
			AlertManager:               "prometheus/alertmanager:v1.2.3",
		},
	}

	testImageMigration(t, dsImages, expectedStcImages, false, false)

	// Test with custom image registry, and oci-monitor image has "portworx/" prefix
	// Component images should not have custom image registry prefix.
	dsImages = ImageConfig{
		PortworxImage: "test/portworx/oci-monitor:2.9.1",
		Components: manifest.Release{
			Stork:                      "test/portworx/stork:2.7.0",
			Autopilot:                  "test/b/autopilot:1.3.1",
			Telemetry:                  "test/c/ccm-service:3.0.9",
			CSIProvisioner:             "test/k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "test/k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "test/k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "test/k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "test/k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "test/k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "test/k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "test/prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "test/coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "test/coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "test/coreos/configmap-reload:v1.2.3",
			AlertManager:               "test/prometheus/alertmanager:v1.2.3",
		},
	}

	expectedStcImages = ImageConfig{
		PortworxImage:       "portworx/oci-monitor:2.9.1",
		CustomImageRegistry: "test",
		PreserveFullPath:    true,
		Components: manifest.Release{
			Stork:                      "portworx/stork:2.7.0",
			Autopilot:                  "b/autopilot:1.3.1",
			Telemetry:                  "c/ccm-service:3.0.9",
			CSIProvisioner:             "k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "coreos/configmap-reload:v1.2.3",
			AlertManager:               "prometheus/alertmanager:v1.2.3",
		},
	}

	testImageMigration(t, dsImages, expectedStcImages, false, false)

	// Test with custom image registry that has / in path, and oci-monitor image has "portworx/" prefix
	// Component images should not have custom image registry prefix.
	dsImages = ImageConfig{
		PortworxImage: "test/customRegistry/portworx/oci-monitor:2.9.1",
		Components: manifest.Release{
			Stork:                      "test/customRegistry/portworx/stork:2.7.0",
			Autopilot:                  "test/customRegistry/b/autopilot:1.3.1",
			Telemetry:                  "test/customRegistry/c/ccm-service:3.0.9",
			CSIProvisioner:             "test/customRegistry/k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "test/customRegistry/k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "test/customRegistry/k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "test/customRegistry/k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "test/customRegistry/k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "test/customRegistry/k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "test/customRegistry/k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "test/customRegistry/prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "test/customRegistry/coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "test/customRegistry/coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "test/customRegistry/coreos/configmap-reload:v1.2.3",
			AlertManager:               "test/customRegistry/prometheus/alertmanager:v1.2.3",
		},
	}

	expectedStcImages = ImageConfig{
		PortworxImage:       "portworx/oci-monitor:2.9.1",
		CustomImageRegistry: "test/customRegistry",
		PreserveFullPath:    true,
		Components: manifest.Release{
			Stork:                      "portworx/stork:2.7.0",
			Autopilot:                  "b/autopilot:1.3.1",
			Telemetry:                  "c/ccm-service:3.0.9",
			CSIProvisioner:             "k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "coreos/configmap-reload:v1.2.3",
			AlertManager:               "prometheus/alertmanager:v1.2.3",
		},
	}

	testImageMigration(t, dsImages, expectedStcImages, true, false)

	// Test with custom image registry, but component images do not have custom registry prefix.
	dsImages = ImageConfig{
		PortworxImage: "test/portworx/oci-monitor:2.9.1",
		Components: manifest.Release{
			Stork:                      "a/stork:2.7.0",
			Autopilot:                  "b/autopilot:1.3.1",
			Telemetry:                  "c/ccm-service:3.0.9",
			CSIProvisioner:             "k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "coreos/configmap-reload:v1.2.3",
			AlertManager:               "prometheus/alertmanager:v1.2.3",
		},
	}

	expectedStcImages = ImageConfig{
		PortworxImage:       "portworx/oci-monitor:2.9.1",
		CustomImageRegistry: "test",
		PreserveFullPath:    true,
		Components: manifest.Release{
			Stork:                      "a/stork:2.7.0",
			Autopilot:                  "b/autopilot:1.3.1",
			Telemetry:                  "c/ccm-service:3.0.9",
			CSIProvisioner:             "k8scsi/csi-provisioner:v1.2.3",
			CSIAttacher:                "k8scsi/csi-attacher:v1.2.3",
			CSINodeDriverRegistrar:     "k8scsi/csi-node-driver-registrar:v1.2.3",
			CSISnapshotter:             "k8scsi/csi-snapshotter:v1.2.3",
			CSIResizer:                 "k8scsi/csi-resizer:v1.2.3",
			CSISnapshotController:      "k8scsi/snapshot-controller:v1.2.3",
			CSIHealthMonitorController: "k8scsi/csi-health-monitor-controller:v1.2.3",
			Prometheus:                 "prometheus/prometheus:v1.2.3",
			PrometheusOperator:         "coreos/prometheus-operator:v1.2.3",
			PrometheusConfigReloader:   "coreos/prometheus-config-reloader:v1.2.3",
			PrometheusConfigMapReload:  "coreos/configmap-reload:v1.2.3",
			AlertManager:               "prometheus/alertmanager:v1.2.3",
		},
	}

	testImageMigration(t, dsImages, expectedStcImages, true, true)
}

func testImageMigration(t *testing.T, dsImages, expectedStcImages ImageConfig, airGapped bool, manifestConfigMapExists bool) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: dsImages.PortworxImage,
							Args: []string{
								"-c", clusterName,
							},
						},
						{
							Name:  pxutil.TelemetryContainerName,
							Image: dsImages.Components.Telemetry,
						},
						{
							Name:  pxutil.CSIRegistrarContainerName,
							Image: dsImages.Components.CSINodeDriverRegistrar,
						},
					},
				},
			},
		},
	}
	storkDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: dsImages.Components.Stork,
						},
					},
				},
			},
		},
	}
	autopilotDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.AutopilotDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: dsImages.Components.Autopilot,
						},
					},
				},
			},
		},
	}
	csiDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  csiProvisionerContainerName,
							Image: dsImages.Components.CSIProvisioner,
						},
						{
							Name:  csiAttacherContainerName,
							Image: dsImages.Components.CSIAttacher,
						},
						{
							Name:  csiSnapshotterContainerName,
							Image: dsImages.Components.CSISnapshotter,
						},
						{
							Name:  csiResizerContainerName,
							Image: dsImages.Components.CSIResizer,
						},
						{
							Name:  csiSnapshotControllerContainerName,
							Image: dsImages.Components.CSISnapshotController,
						},
						{
							Name:  csiHealthMonitorControllerContainerName,
							Image: dsImages.Components.CSIHealthMonitorController,
						},
					},
				},
			},
		},
	}
	promOpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusOpDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: dsImages.Components.PrometheusOperator,
							Args: []string{
								prometheusConfigMapReloaderArg + dsImages.Components.PrometheusConfigMapReload,
								prometheusConfigReloaderArg + dsImages.Components.PrometheusConfigReloader,
							},
						},
					},
				},
			},
		},
	}
	serviceMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceMonitorName,
			Namespace: ds.Namespace,
		},
	}
	alertManager := &monitoringv1.Alertmanager{
		ObjectMeta: metav1.ObjectMeta{
			Name:      alertManagerName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.AlertmanagerSpec{
			Image: &dsImages.Components.AlertManager,
		},
	}
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusInstanceName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.PrometheusSpec{
			RuleSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"prometheus": "portworx",
				},
			},
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": serviceMonitorName,
				},
			},
			Image: &dsImages.Components.Prometheus,
		},
	}

	k8sClient := testutil.FakeK8sClient(
		ds, storkDeployment, autopilotDeployment, csiDeployment,
		promOpDeployment, serviceMonitor, alertManager, prometheus,
	)

	if manifestConfigMapExists {
		k8sClient.Create(
			context.TODO(),
			&v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      manifest.DefaultConfigMapName,
					Namespace: ds.Namespace,
				},
			},
		)
	}

	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(!airGapped).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()

	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)

	require.Equal(t, expectedStcImages.PortworxImage, cluster.Spec.Image)
	require.Equal(t, expectedStcImages.CustomImageRegistry, cluster.Spec.CustomImageRegistry)
	require.Equal(t, expectedStcImages.PreserveFullPath, cluster.Spec.PreserveFullCustomImageRegistry)

	if manifestConfigMapExists {
		require.Equal(t, cluster.Status.DesiredImages, &corev1.ComponentImages{})
	} else {
		require.Equal(t, expectedStcImages.Components.Stork, cluster.Status.DesiredImages.Stork)
		require.Equal(t, expectedStcImages.Components.Autopilot, cluster.Status.DesiredImages.Autopilot)

		require.Equal(t, expectedStcImages.Components.CSINodeDriverRegistrar, cluster.Status.DesiredImages.CSINodeDriverRegistrar)
		require.Equal(t, expectedStcImages.Components.CSIProvisioner, cluster.Status.DesiredImages.CSIProvisioner)
		require.Equal(t, expectedStcImages.Components.CSIAttacher, cluster.Status.DesiredImages.CSIAttacher)
		require.Equal(t, expectedStcImages.Components.CSIResizer, cluster.Status.DesiredImages.CSIResizer)
		require.Equal(t, expectedStcImages.Components.CSISnapshotter, cluster.Status.DesiredImages.CSISnapshotter)
		require.Equal(t, expectedStcImages.Components.CSISnapshotController, cluster.Status.DesiredImages.CSISnapshotController)
		require.Equal(t, expectedStcImages.Components.CSIHealthMonitorController, cluster.Status.DesiredImages.CSIHealthMonitorController)
		require.Equal(t, expectedStcImages.Components.Prometheus, cluster.Status.DesiredImages.Prometheus)
		require.Equal(t, expectedStcImages.Components.AlertManager, cluster.Status.DesiredImages.AlertManager)
		require.Equal(t, expectedStcImages.Components.PrometheusOperator, cluster.Status.DesiredImages.PrometheusOperator)
		require.Equal(t, expectedStcImages.Components.PrometheusConfigMapReload, cluster.Status.DesiredImages.PrometheusConfigMapReload)
		require.Equal(t, expectedStcImages.Components.PrometheusConfigReloader, cluster.Status.DesiredImages.PrometheusConfigReloader)
		require.Equal(t, expectedStcImages.Components.Telemetry, cluster.Status.DesiredImages.Telemetry)

		cm := &v1.ConfigMap{}
		err = testutil.Get(k8sClient, cm, manifest.DefaultConfigMapName, ds.Namespace)
		if airGapped {
			require.NoError(t, err)
		} else {
			require.True(t, errors.IsNotFound(err))
		}
	}

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterIsCreatedFromOnPremDaemonset(t *testing.T) {
	clusterName := "px-cluster"
	maxUnavailable := intstr.FromInt(3)
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					ImagePullSecrets: []v1.LocalObjectReference{
						{
							Name: "pull-secret",
						},
					},
					Containers: []v1.Container{
						{
							Name:            "portworx",
							Image:           "portworx/test:version",
							ImagePullPolicy: v1.PullIfNotPresent,
							Args: []string{
								"-c", clusterName,
								"-x", "k8s",
								"-a", "-A", "-f",
								"-j", "/dev/sdb",
								"-metadata", "/dev/sdc",
								"-kvdb_dev", "/dev/sdd",
								"-cache", "/dev/sde1", "-cache", "/dev/sde2",
								"-s", "/dev/sda1", "-s", "/dev/sda2",
								"-b",
								"-d", "eth1",
								"-m", "eth2",
								"-secret_type", "vault",
								"-r", "10001",
								"-rt_opts", "opt1=100,opt2=999",
								"-marketplace_name", "OperatorHub",
								"-csi_endpoint", "csi/endpoint",
								"-csiversion", "0.3",
								"-oidc_issuer", "OIDC_ISSUER",
								"-oidc_client_id", "OIDC_CLIENT_ID",
								"-oidc_custom_claim_namespace", "OIDC_CUSTOM_NS",
								"--log", "/tmp/log/location",
								"-extra_arg1",
								"-extra_arg2", "value2",
								"-extra_arg3", "value3",
							},
							Env: []v1.EnvVar{
								{
									Name:  "PX_TEMPLATE_VERSION",
									Value: "v3",
								},
								{
									Name:  "TEST_ENV_1",
									Value: "value1",
								},
							},
						},
					},
					Affinity: &v1.Affinity{
						NodeAffinity: &v1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
								NodeSelectorTerms: []v1.NodeSelectorTerm{
									{
										MatchExpressions: []v1.NodeSelectorRequirement{
											{
												Key:      "px/enabled",
												Operator: v1.NodeSelectorOpNotIn,
												Values:   []string{"false"},
											},
										},
									},
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:      "taint_key",
							Operator: v1.TolerationOpExists,
							Effect:   v1.TaintEffectNoExecute,
						},
					},
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
				pxutil.AnnotationMiscArgs:             "-extra_arg1 -extra_arg2 value2 -extra_arg3 value3",
				pxutil.AnnotationLogFile:              "/tmp/log/location",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/test:version",
			ImagePullPolicy: v1.PullIfNotPresent,
			ImagePullSecret: stringPtr("pull-secret"),
			SecretsProvider: stringPtr("vault"),
			StartPort:       uint32Ptr("10001"),
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll:               boolPtr(true),
					UseAllWithPartitions: boolPtr(true),
					ForceUseDisks:        boolPtr(true),
					Devices:              stringSlicePtr([]string{"/dev/sda1", "/dev/sda2"}),
					CacheDevices:         stringSlicePtr([]string{"/dev/sde1", "/dev/sde2"}),
					JournalDevice:        stringPtr("/dev/sdb"),
					SystemMdDevice:       stringPtr("/dev/sdc"),
					KvdbDevice:           stringPtr("/dev/sdd"),
				},
				Network: &corev1.NetworkSpec{
					DataInterface: stringPtr("eth1"),
					MgmtInterface: stringPtr("eth2"),
				},
				RuntimeOpts: map[string]string{
					"opt1": "100",
					"opt2": "999",
				},
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_AUTH_OIDC_ISSUER",
						Value: "OIDC_ISSUER",
					},
					{
						Name:  "PORTWORX_AUTH_OIDC_CLIENTID",
						Value: "OIDC_CLIENT_ID",
					},
					{
						Name:  "PORTWORX_AUTH_OIDC_CUSTOM_NAMESPACE",
						Value: "OIDC_CUSTOM_NS",
					},
					{
						Name:  "MARKETPLACE_NAME",
						Value: "OperatorHub",
					},
					{
						Name:  "CSI_ENDPOINT",
						Value: "csi/endpoint",
					},
					{
						Name:  "PORTWORX_CSIVERSION",
						Value: "0.3",
					},
					{
						Name:  "TEST_ENV_1",
						Value: "value1",
					},
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
			Placement: &corev1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
								},
							},
						},
					},
				},
				Tolerations: []v1.Toleration{
					{
						Key:      "taint_key",
						Operator: v1.TolerationOpExists,
						Effect:   v1.TaintEffectNoExecute,
					},
				},
			},
			UpdateStrategy: corev1.StorageClusterUpdateStrategy{
				Type: corev1.RollingUpdateStorageClusterStrategyType,
				RollingUpdate: &corev1.RollingUpdateStorageCluster{
					MaxUnavailable: &maxUnavailable,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterIsCreatedFromCloudDaemonset(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            "portworx",
							Image:           "portworx/test:version",
							ImagePullPolicy: v1.PullNever,
							Args: []string{
								"-c", clusterName,
								"-s", "type=disk", "-s", "type=disk",
								"-j", "type=journal",
								"-metadata", "type=md",
								"-kvdb_dev", "type=kvdb",
								"-max_drive_set_count", "10",
								"-max_storage_nodes_per_zone", "5",
								"-max_storage_nodes_per_zone_per_nodegroup", "1",
								"-node_pool_label", "px/storage",
								"-cloud_provider", "aws",
								"-k", "etcd:http://etcd-1.com:1111,etcd:http://etcd-2.com:1111",
								"-jwt_issuer", "jwt_issuer",
								"-jwt_shared_secret", "shared_secret",
								"-jwt_rsa_pubkey_file", "rsa_file",
								"-jwt_ecds_pubkey_file", "ecds_file",
								"-username_claim", "username_claim",
								"-auth_system_key", "system_key",
							},
							Env: []v1.EnvVar{
								{
									Name:  "PX_TEMPLATE_VERSION",
									Value: "v3",
								},
								{
									Name:  "PORTWORX_CSIVERSION",
									Value: "0.3",
								},
								{
									Name:  "CSI_ENDPOINT",
									Value: "unix:///var/lib/kubelet/plugins/pxd.portworx.com/csi.sock",
								},
								{
									Name: "NODE_NAME",
									ValueFrom: &v1.EnvVarSource{
										FieldRef: &v1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
								{
									Name: "PX_NAMESPACE",
									ValueFrom: &v1.EnvVarSource{
										FieldRef: &v1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
								{
									Name:  "TEST_ENV_1",
									Value: "value1",
								},
								{
									Name:  "PX_SECRETS_NAMESPACE",
									Value: "custom-secrets-namespace",
								},
							},
						},
					},
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.OnDeleteDaemonSetStrategyType,
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/test:version",
			ImagePullPolicy: v1.PullNever,
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd:http://etcd-1.com:1111",
					"etcd:http://etcd-2.com:1111",
				},
			},
			CloudStorage: &corev1.CloudStorageSpec{
				Provider: stringPtr("aws"),
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs:                        stringSlicePtr([]string{"type=disk", "type=disk"}),
					JournalDeviceSpec:                  stringPtr("type=journal"),
					SystemMdDeviceSpec:                 stringPtr("type=md"),
					KvdbDeviceSpec:                     stringPtr("type=kvdb"),
					MaxStorageNodesPerZonePerNodeGroup: uint32Ptr("1"),
				},
				MaxStorageNodes:        uint32Ptr("10"),
				MaxStorageNodesPerZone: uint32Ptr("5"),
				NodePoolLabel:          "px/storage",
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_AUTH_JWT_ISSUER",
						Value: "jwt_issuer",
					},
					{
						Name:  "PORTWORX_AUTH_JWT_SHAREDSECRET",
						Value: "shared_secret",
					},
					{
						Name:  "PORTWORX_AUTH_JWT_RSA_PUBKEY",
						Value: "rsa_file",
					},
					{
						Name:  "PORTWORX_AUTH_JWT_ECDS_PUBKEY",
						Value: "ecds_file",
					},
					{
						Name:  "PORTWORX_AUTH_USERNAME_CLAIM",
						Value: "username_claim",
					},
					{
						Name:  "PORTWORX_AUTH_SYSTEM_KEY",
						Value: "system_key",
					},
					{
						Name:  "TEST_ENV_1",
						Value: "value1",
					},
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "custom-secrets-namespace",
					},
				},
			},
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
			UpdateStrategy: corev1.StorageClusterUpdateStrategy{
				Type: corev1.OnDeleteStorageClusterStrategyType,
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterDoesNotHaveSecretsNamespaceIfSameAsClusterNamespace(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: "portworx/test:version",
							Args: []string{
								"-c", clusterName,
							},
							Env: []v1.EnvVar{
								{
									Name:  "PX_SECRETS_NAMESPACE",
									Value: "kube-system",
								},
							},
						},
					},
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{Driver: driver}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	migrator := New(ctrl)

	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	go migrator.Start()

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/test:version",
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}

		return true, nil
	})
	require.NoError(t, err)

	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterWithAutoJournalAndOnPremStorage(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: "portworx/test:version",
							Args: []string{
								"-c", clusterName,
								"-s", "/dev/sda", "-s", "/dev/sdb",
								"-j", "auto",
							},
						},
					},
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/test:version",
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					Devices:       stringSlicePtr([]string{"/dev/sda", "/dev/sdb"}),
					JournalDevice: stringPtr("auto"),
				},
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterWithAutoJournalAndCloudStorage(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: "portworx/test:version",
							Args: []string{
								"-c", clusterName,
								"-s", "type=disk", "-s", "type=disk",
								"-j", "auto",
							},
						},
					},
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/test:version",
			CloudStorage: &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					DeviceSpecs:       stringSlicePtr([]string{"type=disk", "type=disk"}),
					JournalDeviceSpec: stringPtr("auto"),
				},
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestWhenStorageClusterIsAlreadyPresent(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{"-c", clusterName, "-a"},
						},
					},
				},
			},
		},
	}

	// Cluster with some missing params from the daemonset
	existingCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, existingCluster)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	stc := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, stc, clusterName, ds.Namespace)
	require.NoError(t, err)
	// The storage cluster object did not change even when the daemonset
	// is different now
	require.Nil(t, stc.Spec.Storage)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestWhenPortworxDaemonsetIsNotPresent(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	clusterList := &corev1.StorageClusterList{}
	err := testutil.List(k8sClient, clusterList)
	require.NoError(t, err)
	require.Empty(t, clusterList.Items)
}

func TestStorageClusterSpecWithComponents(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
						{
							Name: pxutil.CSIRegistrarContainerName,
						},
						{
							Name:  pxutil.TelemetryContainerName,
							Image: "image/ccm-service:2.6.0",
						},
					},
				},
			},
		},
	}
	storkDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: "portworx/stork:2.7.0",
							Env: []v1.EnvVar{
								{Name: "PX_SERVICE_NAME", Value: "portworx-api"},
								{Name: "PX_SHARED_SECRET", ValueFrom: &v1.EnvVarSource{
									SecretKeyRef: &v1.SecretKeySelector{
										LocalObjectReference: v1.LocalObjectReference{Name: "pxkeys"},
										Key:                  "stork-secret",
									},
								}},
								{Name: "EXTRA_ENV_STORK", Value: "value-stork"},
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "stork-secret", MountPath: "/secret"},
								{Name: "extra-volume-stork", MountPath: "/mountpath-stork"},
							},
						},
					},
					Volumes: []v1.Volume{
						{Name: "stork-secret", VolumeSource: v1.VolumeSource{
							Secret: &v1.SecretVolumeSource{
								SecretName: "stork-secret",
							},
						}},
						{Name: "extra-volume-stork", VolumeSource: v1.VolumeSource{
							HostPath: &v1.HostPathVolumeSource{Path: "/path-stork"},
						}},
					},
				},
			},
		},
	}
	autopilotDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.AutopilotDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: "/autopilot:1.3.1",
							Env: []v1.EnvVar{
								{Name: "EXTRA_ENV_AUTOPILOT", Value: "value-autopilot"},
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "config-volume", MountPath: "/etc/config"},
								{Name: "extra-volume-autopilot", MountPath: "/mountpath-autopilot"},
							},
						},
					},
					Volumes: []v1.Volume{
						{Name: "config-volume", VolumeSource: v1.VolumeSource{
							ConfigMap: &v1.ConfigMapVolumeSource{
								LocalObjectReference: v1.LocalObjectReference{Name: "autopilot-config"},
								Items: []v1.KeyToPath{
									{Key: "config.yaml", Path: "config.yaml"},
								},
							},
						}},
						{Name: "extra-volume-autopilot", VolumeSource: v1.VolumeSource{
							HostPath: &v1.HostPathVolumeSource{Path: "/path-autopilot"},
						}},
					},
				},
			},
		},
	}
	pvcControllerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.PVCDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	prometheusImage := "testPrometheusImage"
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusInstanceName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.PrometheusSpec{
			Image: &prometheusImage,
			RuleSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"prometheus": "portworx",
				},
			},
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": serviceMonitorName,
				},
			},
		},
	}
	serviceMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceMonitorName,
			Namespace: ds.Namespace,
		},
	}
	testAlertmanagerImage := "testAlertmanagerImage"
	alertManager := &monitoringv1.Alertmanager{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.AlertManagerInstanceName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.AlertmanagerSpec{
			Image: &testAlertmanagerImage,
		},
	}

	k8sClient := testutil.FakeK8sClient(
		ds, storkDeployment, autopilotDeployment,
		pvcControllerDeployment, prometheus,
		serviceMonitor, alertManager,
	)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}

		return true, nil
	})
	require.NoError(t, err)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CSI: &corev1.CSISpec{
				Enabled:                   true,
				InstallSnapshotController: boolPtr(true),
			},
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Env: []v1.EnvVar{
					{Name: "PX_SHARED_SECRET", ValueFrom: &v1.EnvVarSource{
						SecretKeyRef: &v1.SecretKeySelector{
							LocalObjectReference: v1.LocalObjectReference{Name: "pxkeys"},
							Key:                  "stork-secret",
						},
					}},
					{Name: "EXTRA_ENV_STORK", Value: "value-stork"},
				},
				Volumes: []corev1.VolumeSpec{
					{Name: "stork-secret", VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: "stork-secret",
						},
					}, MountPath: "/secret"},
					{Name: "extra-volume-stork", VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{Path: "/path-stork"},
					}, MountPath: "/mountpath-stork"},
				},
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: true,
				Env: []v1.EnvVar{
					{Name: "EXTRA_ENV_AUTOPILOT", Value: "value-autopilot"},
				},
				Volumes: []corev1.VolumeSpec{
					{Name: "extra-volume-autopilot", VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{Path: "/path-autopilot"},
					}, MountPath: "/mountpath-autopilot"},
				},
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: true,
					Enabled:       true,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: true,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
				},
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	require.ElementsMatch(t, expectedCluster.Spec.Autopilot.Env, cluster.Spec.Autopilot.Env)
	require.ElementsMatch(t, expectedCluster.Spec.Autopilot.Volumes, cluster.Spec.Autopilot.Volumes)
	require.ElementsMatch(t, expectedCluster.Spec.Stork.Env, cluster.Spec.Stork.Env)
	require.ElementsMatch(t, expectedCluster.Spec.Stork.Volumes, cluster.Spec.Stork.Volumes)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	expectedCluster.Spec.Autopilot.Env = nil
	cluster.Spec.Autopilot.Env = nil
	expectedCluster.Spec.Autopilot.Volumes = nil
	cluster.Spec.Autopilot.Volumes = nil
	expectedCluster.Spec.Stork.Env = nil
	cluster.Spec.Stork.Env = nil
	expectedCluster.Spec.Stork.Volumes = nil
	cluster.Spec.Stork.Volumes = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterSpecWithPVCControllerInKubeSystem(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}
	pvcControllerDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.PVCDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusInstanceName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.PrometheusSpec{
			RuleSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"prometheus": "portworx",
				},
			},
			ServiceMonitorSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": serviceMonitorName,
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, pvcControllerDeployment, prometheus)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
				pxutil.AnnotationPVCController:        "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					// Prometheus is not enabled although prometheus is present. This is because
					// the portworx metrics are not exported nor alert manager is running.
					Enabled: false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase:         constants.PhaseAwaitingApproval,
			DesiredImages: &corev1.ComponentImages{},
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status, cluster.Status)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestSuccessfulMigration(t *testing.T) {
	migrationRetryIntervalFunc = func() time.Duration {
		return 2 * time.Second
	}
	defer func() {
		migrationRetryIntervalFunc = getMigrationRetryInterval
	}()

	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
			UID:       "100",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}

	numNodes := 2
	nodes := []*v1.Node{}
	dsPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		nodes = append(nodes, constructNode(i))
		dsPods = append(dsPods, constructDaemonSetPod(ds, i))
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockCtrl := gomock.NewController(t)
	driver := testutil.MockDriver(mockCtrl)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	recorder := record.NewFakeRecorder(10)
	ctrl.SetEventRecorder(recorder)
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockCtrl)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	for i := 0; i < numNodes; i++ {
		err := k8sClient.Create(context.TODO(), nodes[i])
		require.NoError(t, err)
		err = k8sClient.Create(context.TODO(), dsPods[i])
		require.NoError(t, err)
	}

	// Start the migration handler
	migrator := New(ctrl)
	go migrator.Start()
	time.Sleep(2 * time.Second)

	// Check cluster's initial status before user approval
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, constants.PhaseAwaitingApproval, cluster.Status.Phase)

	// Approve migration
	cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	err = testutil.Update(k8sClient, cluster)
	require.NoError(t, err)

	// Validate the migration has started
	err = validateMigrationIsInProgress(k8sClient, cluster)
	require.NoError(t, err)

	time.Sleep(time.Second)

	// Validate daemonset has been updated
	currDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.NoError(t, err)

	require.Equal(t, appsv1.OnDeleteDaemonSetStrategyType, currDaemonSet.Spec.UpdateStrategy.Type)
	require.Equal(t, expectedDaemonSetAffinity(), currDaemonSet.Spec.Template.Spec.Affinity)

	// Validate components have been paused
	cluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, "true", cluster.Annotations[constants.AnnotationPauseComponentMigration])

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	// Delete the daemonset pod so migration can proceed on first node
	err = testutil.Delete(k8sClient, dsPods[0])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	operatorPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		operatorPods = append(operatorPods, constructOperatorPod(cluster, i))
	}

	// Create an operator pod so migration can proceed on first node
	err = k8sClient.Create(context.TODO(), operatorPods[0])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)

	// Delete daemonset pod and create an operator pod on second node for migration to proceed
	err = testutil.Delete(k8sClient, dsPods[1])
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), operatorPods[1])
	require.NoError(t, err)

	// Validate the migration has completed
	err = validateNodeStatus(k8sClient, nodes[0].Name, "")
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, "")
	require.NoError(t, err)

	currDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	close(recorder.Events)
	var msg string
	for e := range recorder.Events {
		msg += e
	}
	require.Contains(t, msg, "Migration completed successfully")

	cluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	_, annotationExists := cluster.Annotations[constants.AnnotationPauseComponentMigration]
	require.False(t, annotationExists)
}

func TestFailedMigrationRecoveredWithSkip(t *testing.T) {
	migrationRetryIntervalFunc = func() time.Duration {
		return 2 * time.Second
	}
	daemonSetPodTerminationTimeoutFunc = func() time.Duration {
		return 2 * time.Second
	}
	operatorPodReadyTimeoutFunc = func() time.Duration {
		return 2 * time.Second
	}
	defer func() {
		migrationRetryIntervalFunc = getMigrationRetryInterval
		daemonSetPodTerminationTimeoutFunc = getDaemonSetPodTerminationTimeout
		operatorPodReadyTimeoutFunc = getOperatorPodReadyTimeout
	}()

	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
			UID:       "100",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}

	numNodes := 3
	nodes := []*v1.Node{}
	dsPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		nodes = append(nodes, constructNode(i))
		dsPods = append(dsPods, constructDaemonSetPod(ds, i))
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockCtrl := gomock.NewController(t)
	driver := testutil.MockDriver(mockCtrl)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockCtrl)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	for i := 0; i < numNodes; i++ {
		err := k8sClient.Create(context.TODO(), nodes[i])
		require.NoError(t, err)
		err = k8sClient.Create(context.TODO(), dsPods[i])
		require.NoError(t, err)
	}

	// Start the migration handler
	migrator := New(ctrl)
	go migrator.Start()
	time.Sleep(2 * time.Second)

	// Check cluster's initial status before user approval
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, constants.PhaseAwaitingApproval, cluster.Status.Phase)

	// Approve migration
	cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	err = testutil.Update(k8sClient, cluster)
	require.NoError(t, err)

	// Validate the migration has started
	err = validateMigrationIsInProgress(k8sClient, cluster)
	require.NoError(t, err)

	operatorPods := []*v1.Pod{}
	for i := 1; i <= numNodes; i++ {
		operatorPods = append(operatorPods, constructOperatorPod(cluster, i))
	}

	// Delete daemonset pod and create an operator pod on first node for migration to proceed
	err = testutil.Delete(k8sClient, dsPods[0])
	require.NoError(t, err)
	err = k8sClient.Create(context.TODO(), operatorPods[0])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	// We wait until the daemonset termination wait routine fails
	time.Sleep(3 * time.Second)

	// Validate status of the nodes
	// The status should not change as the daemonset pod is not yet terminated
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationStarting)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationPending)
	require.NoError(t, err)

	// Mark the node as skipped
	node2 := &v1.Node{}
	err = testutil.Get(k8sClient, node2, nodes[1].Name, "")
	require.NoError(t, err)
	node2.Labels = map[string]string{
		constants.LabelPortworxDaemonsetMigration: constants.LabelValueMigrationSkip,
	}
	err = testutil.Update(k8sClient, node2)
	require.NoError(t, err)

	// Delete the third daemonset pod so third node can proceed
	err = testutil.Delete(k8sClient, dsPods[2])
	require.NoError(t, err)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)

	// We wait until the wait routine for pod to be ready fails
	time.Sleep(5 * time.Second)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)

	// Create the operator pod now, simulating it took much longer than usual,
	// but don't mark the pod as ready so it fails again
	operatorPods[2].Status.Conditions = nil
	err = k8sClient.Create(context.TODO(), operatorPods[2])
	require.NoError(t, err)

	// We wait until the wait routine for pod to be ready fails
	time.Sleep(5 * time.Second)

	// Validate status of the nodes
	err = validateNodeStatus(k8sClient, nodes[0].Name, constants.LabelValueMigrationDone)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, constants.LabelValueMigrationInProgress)
	require.NoError(t, err)

	// Mark the operator pod as ready now, simulating it took much longer for the pod
	// to get ready	and verify the migration still continued trying and eventually succeeded
	operatorPods[2].Status.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	err = k8sClient.Update(context.TODO(), operatorPods[2])
	require.NoError(t, err)

	// Validate the migration has completed
	// Do not remove the labels from skipped nodes as user has added that label
	err = validateNodeStatus(k8sClient, nodes[0].Name, "")
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[1].Name, constants.LabelValueMigrationSkip)
	require.NoError(t, err)
	err = validateNodeStatus(k8sClient, nodes[2].Name, "")
	require.NoError(t, err)

	currDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.True(t, errors.IsNotFound(err))
}

func TestOldComponentsAreDeleted(t *testing.T) {
	migrationRetryIntervalFunc = func() time.Duration {
		return 2 * time.Second
	}
	defer func() {
		migrationRetryIntervalFunc = getMigrationRetryInterval
	}()

	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-test",
			UID:       "100",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}
	pxClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: pxClusterRoleName,
		},
	}
	pxClusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: pxClusterRoleBindingName,
		},
	}
	pxRoleLocal := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleName,
			Namespace: ds.Namespace,
		},
	}
	pxRoleBindingLocal := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleBindingName,
			Namespace: ds.Namespace,
		},
	}
	pxRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleName,
			Namespace: secretsNamespace,
		},
	}
	pxRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxRoleBindingName,
			Namespace: secretsNamespace,
		},
	}
	pxAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxAccountName,
			Namespace: ds.Namespace,
		},
	}
	pxSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: ds.Namespace,
		},
	}
	pxAPIDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxAPIDaemonSetName,
			Namespace: ds.Namespace,
		},
	}
	pxAPISvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxAPIServiceName,
			Namespace: ds.Namespace,
		},
	}
	storkDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: "portworx/stork:2.7.0",
						},
					},
				},
			},
		},
	}
	storkSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkServiceName,
			Namespace: ds.Namespace,
		},
	}
	storkConfig := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkConfigName,
			Namespace: ds.Namespace,
		},
	}
	storkRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: storkRoleName,
		},
	}
	storkRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: storkRoleBindingName,
		},
	}
	storkAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkAccountName,
			Namespace: ds.Namespace,
		},
	}
	schedDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      schedDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	schedRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: schedRoleName,
		},
	}
	schedRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: schedRoleBindingName,
		},
	}
	schedAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      schedAccountName,
			Namespace: ds.Namespace,
		},
	}
	autopilotDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: "/autopilot:1.3.1",
						},
					},
				},
			},
		},
	}
	autopilotSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotServiceName,
			Namespace: ds.Namespace,
		},
	}
	autopilotConfig := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotConfigName,
			Namespace: ds.Namespace,
		},
	}
	autopilotRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: autopilotRoleName,
		},
	}
	autopilotRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: autopilotRoleBindingName,
		},
	}
	autopilotAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      autopilotAccountName,
			Namespace: ds.Namespace,
		},
	}
	pvcDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	pvcRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvcRoleName,
		},
	}
	pvcRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvcRoleBindingName,
		},
	}
	pvcAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcAccountName,
			Namespace: ds.Namespace,
		},
	}
	csiDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	csiSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiServiceName,
			Namespace: ds.Namespace,
		},
	}
	csiRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiRoleName,
		},
	}
	csiRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: csiRoleBindingName,
		},
	}
	csiAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiAccountName,
			Namespace: ds.Namespace,
		},
	}
	promOpDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusOpDeploymentName,
			Namespace: ds.Namespace,
		},
	}
	promOpRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusOpRoleName,
		},
	}
	promOpRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusOpRoleBindingName,
		},
	}
	promOpAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusOpAccountName,
			Namespace: ds.Namespace,
		},
	}
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusInstanceName,
			Namespace: ds.Namespace,
		},
	}
	prometheusSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prometheus",
			Namespace: ds.Namespace,
		},
	}
	prometheusRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusRoleName,
		},
	}
	prometheusRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: prometheusRoleBindingName,
		},
	}
	prometheusAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusAccountName,
			Namespace: ds.Namespace,
		},
	}
	serviceMonitor := &monitoringv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceMonitorName,
			Namespace: ds.Namespace,
		},
	}
	prometheusRule := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusRuleName,
			Namespace: ds.Namespace,
		},
	}
	testAlertmanagerImage := "testAlertmanagerImage"
	alertManager := &monitoringv1.Alertmanager{
		ObjectMeta: metav1.ObjectMeta{
			Name:      alertManagerName,
			Namespace: ds.Namespace,
		},
		Spec: monitoringv1.AlertmanagerSpec{
			Image: &testAlertmanagerImage,
		},
	}
	alertManagerSvc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      alertManagerServiceName,
			Namespace: ds.Namespace,
		},
	}

	k8sClient := testutil.FakeK8sClient(
		ds, pxAPIDaemonSet, pxAPISvc, pxSvc,
		pxClusterRole, pxClusterRoleBinding, pxRole, pxRoleBinding, pxRoleLocal, pxRoleBindingLocal, pxAccount,
		storkDeployment, storkSvc, storkConfig, storkRole, storkRoleBinding, storkAccount,
		schedDeployment, schedRole, schedRoleBinding, schedAccount,
		autopilotDeployment, autopilotConfig, autopilotSvc, autopilotRole, autopilotRoleBinding, autopilotAccount,
		pvcDeployment, pvcRole, pvcRoleBinding, pvcAccount,
		csiDeployment, csiSvc, csiRole, csiRoleBinding, csiAccount,
		serviceMonitor, prometheusRule, alertManager, alertManagerSvc,
		prometheus, prometheusSvc, prometheusRole, prometheusRoleBinding, prometheusAccount,
		promOpDeployment, promOpRole, promOpRoleBinding, promOpAccount,
	)
	mockCtrl := gomock.NewController(t)
	driver := testutil.MockDriver(mockCtrl)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockCtrl)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	// Start the migration handler
	migrator := New(ctrl)
	go migrator.Start()
	time.Sleep(2 * time.Second)

	// Check cluster's initial status before user approval
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, constants.PhaseAwaitingApproval, cluster.Status.Phase)

	// Approve migration
	cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	err = testutil.Update(k8sClient, cluster)
	require.NoError(t, err)

	time.Sleep(1 * time.Second)

	// Validate all components have been deleted
	pxClusterRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, pxClusterRole, pxClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	pxClusterRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pxClusterRoleBinding, pxClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pxRoleLocal = &rbacv1.Role{}
	err = testutil.Get(k8sClient, pxRoleLocal, pxRoleName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxRoleBindingLocal = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, pxRoleBindingLocal, pxRoleBindingName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxRole = &rbacv1.Role{}
	err = testutil.Get(k8sClient, pxRole, pxRoleName, secretsNamespace)
	require.True(t, errors.IsNotFound(err))

	pxRoleBinding = &rbacv1.RoleBinding{}
	err = testutil.Get(k8sClient, pxRoleBinding, pxRoleBindingName, secretsNamespace)
	require.True(t, errors.IsNotFound(err))

	pxAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pxAccount, pxAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxSvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxSvc, pxServiceName, ds.Namespace)
	require.False(t, errors.IsNotFound(err))

	pxAPIDaemonSet = &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, pxAPIDaemonSet, pxAPIDaemonSetName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pxAPISvc = &v1.Service{}
	err = testutil.Get(k8sClient, pxAPISvc, pxAPIServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSvc = &v1.Service{}
	err = testutil.Get(k8sClient, storkSvc, storkServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkConfig = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkConfig, storkConfigName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkRole, storkRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkRoleBinding, storkRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkAccount, storkAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, schedDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedRole, schedRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedRoleBinding, schedRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, schedAccount, schedAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, autopilotDeployment, autopilotDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotSvc = &v1.Service{}
	err = testutil.Get(k8sClient, autopilotSvc, autopilotServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotConfig = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, autopilotConfig, autopilotConfigName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	autopilotRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, autopilotRole, autopilotRoleName, "")
	require.True(t, errors.IsNotFound(err))

	autopilotRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, autopilotRoleBinding, autopilotRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	autopilotAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, autopilotAccount, autopilotAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pvcDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, pvcDeployment, pvcDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	pvcRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, pvcRole, pvcRoleName, "")
	require.True(t, errors.IsNotFound(err))

	pvcRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, pvcRoleBinding, pvcRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	pvcAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, pvcAccount, pvcAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, csiDeployment, csiDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiSvc = &v1.Service{}
	err = testutil.Get(k8sClient, csiSvc, csiServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	csiRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, csiRole, csiRoleName, "")
	require.True(t, errors.IsNotFound(err))

	csiRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, csiRoleBinding, csiRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	csiAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, csiAccount, csiAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	promOpDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, promOpDeployment, prometheusOpDeploymentName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	promOpRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, promOpRole, prometheusOpRoleName, "")
	require.True(t, errors.IsNotFound(err))

	promOpRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, promOpRoleBinding, prometheusOpRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	promOpAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, promOpAccount, prometheusOpAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheus = &monitoringv1.Prometheus{}
	err = testutil.Get(k8sClient, prometheus, prometheusInstanceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheusSvc = &v1.Service{}
	err = testutil.Get(k8sClient, prometheusSvc, prometheusServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheusRole = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, prometheusRole, prometheusRoleName, "")
	require.True(t, errors.IsNotFound(err))

	prometheusRoleBinding = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, prometheusRoleBinding, prometheusRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	prometheusAccount = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, prometheusAccount, prometheusAccountName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	serviceMonitor = &monitoringv1.ServiceMonitor{}
	err = testutil.Get(k8sClient, serviceMonitor, serviceMonitorName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	prometheusRule = &monitoringv1.PrometheusRule{}
	err = testutil.Get(k8sClient, prometheusRule, prometheusRuleName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	alertManager = &monitoringv1.Alertmanager{}
	err = testutil.Get(k8sClient, alertManager, alertManagerName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	alertManagerSvc = &v1.Service{}
	err = testutil.Get(k8sClient, alertManagerSvc, alertManagerServiceName, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	// Validate the migration has completed
	currDaemonSet := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, currDaemonSet, ds.Name, ds.Namespace)
	require.True(t, errors.IsNotFound(err))

	cluster = &corev1.StorageCluster{}
	err = testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	_, annotationExists := cluster.Annotations[constants.AnnotationPauseComponentMigration]
	require.False(t, annotationExists)
}

func TestStorageClusterWithExtraListOfVolumes(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
							VolumeMounts: []v1.VolumeMount{
								{Name: "diagsdump", MountPath: "/var/cores"},
								{Name: "volume1", MountPath: "/mountpath1"},
								{Name: "volume2", MountPath: "/mountpath2", ReadOnly: true},
							},
						},
					},
					Volumes: []v1.Volume{
						{Name: "diagsdump", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/var/cores"}}},
						{Name: "csi-driver-path", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/var/lib/kubelet/plugins/pxd.portworx.com"}}},
						{Name: "volume1", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/path1"}}},
						{Name: "volume2", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/path2"}}},
						{Name: "volume3", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/path3"}}},
					},
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	migrator := New(ctrl)

	go migrator.Start()
	time.Sleep(2 * time.Second)

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
			Volumes: []corev1.VolumeSpec{
				{Name: "volume1", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/path1"}}, MountPath: "/mountpath1"},
				{Name: "volume2", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/path2"}}, MountPath: "/mountpath2", ReadOnly: true},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	cluster := &corev1.StorageCluster{}
	err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	require.ElementsMatch(t, expectedCluster.Spec.Volumes, cluster.Spec.Volumes)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	expectedCluster.Spec.Volumes = nil
	cluster.Spec.Volumes = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterServiceTypeBothDefault(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: "portworx/test:version",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}
	pxService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-service",
			Namespace: "kube-system",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
		},
	}
	pxAPIService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-api",
			Namespace: "kube-system",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, pxService, pxAPIService)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}
	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/test:version",
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterServiceTypeBothNotDefault(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: "portworx/test:version",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}
	pxService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-service",
			Namespace: "kube-system",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeNodePort,
		},
	}
	pxAPIService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-api",
			Namespace: "kube-system",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, pxService, pxAPIService)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}
	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
				pxutil.AnnotationServiceType:          "portworx-service:NodePort;portworx-api:LoadBalancer",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/test:version",
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterServiceTypeOneNotDefault(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: "portworx/test:version",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}
	pxService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-service",
			Namespace: "kube-system",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
		},
	}
	pxAPIService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-api",
			Namespace: "kube-system",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, pxService, pxAPIService)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}
	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
				pxutil.AnnotationServiceType:          "portworx-api:LoadBalancer",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/test:version",
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterWithCustomAnnotations(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
			UID:       "100",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.AnnotationPodSafeToEvict: "false",
						"custom-annotation-key":            "custom-annotation-val",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
							},
						},
					},
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(ds)
	mockCtrl := gomock.NewController(t)
	driver := testutil.MockDriver(mockCtrl)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	recorder := record.NewFakeRecorder(10)
	ctrl.SetEventRecorder(recorder)
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockCtrl)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}

	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
			Metadata: &corev1.Metadata{
				Annotations: map[string]map[string]string{
					"pod/storage": {
						"custom-annotation-key": "custom-annotation-val",
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterExternalKvdbAuthReuseOldSecretWithCert(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
								"-k", "etcd:http://etcd-1.com:1111,etcd:http://etcd-2.com:1111",
								"-ca", "/etc/pwx/etcdcerts/kvdb-ca.crt",
								"-cert", "/etc/pwx/etcdcerts/kvdb.crt",
								"-key", "/etc/pwx/etcdcerts/kvdb.key",
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "etcdcerts",
							VolumeSource: v1.VolumeSource{
								Secret: &v1.SecretVolumeSource{
									SecretName: "px-kvdb-auth",
								},
							},
						},
					},
				},
			},
		},
	}
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-kvdb-auth",
			Namespace: "kube-system",
		},
		Data: map[string][]byte{
			"kvdb-ca.crt": []byte("ca file content"),
			"kvdb.crt":    []byte("cert file content"),
			"kvdb.key":    []byte("key file content"),
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, secret)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}
	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd:http://etcd-1.com:1111",
					"etcd:http://etcd-2.com:1111",
				},
				AuthSecret: secret.Name,
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterExternalKvdbAuthReuseOldSecretWithPassword(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
								"-k", "etcd:http://etcd-1.com:1111,etcd:http://etcd-2.com:1111",
								"-userpwd", "$PX_KVDB_USERNAME:$PX_KVDB_PASSWORD",
							},
							Env: []v1.EnvVar{
								{
									Name: "PX_KVDB_USERNAME",
									ValueFrom: &v1.EnvVarSource{
										SecretKeyRef: &v1.SecretKeySelector{
											LocalObjectReference: v1.LocalObjectReference{
												Name: "px-etcd-auth",
											},
										},
									},
								},
								{
									Name: "PX_KVDB_PASSWORD",
									ValueFrom: &v1.EnvVarSource{
										SecretKeyRef: &v1.SecretKeySelector{
											LocalObjectReference: v1.LocalObjectReference{
												Name: "px-etcd-auth",
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
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-etcd-auth",
			Namespace: "kube-system",
		},
		Data: map[string][]byte{
			"username": []byte("username"),
			"password": []byte("password"),
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, secret)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}
	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd:http://etcd-1.com:1111",
					"etcd:http://etcd-2.com:1111",
				},
				AuthSecret: secret.Name,
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterExternalKvdbAuthCreateNewSecretForDifferentCertNames(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "custom-ns",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
								"-k", "etcd:http://etcd-1.com:1111,etcd:http://etcd-2.com:1111",
								"-ca", "/etc/pwx/etcdcerts/ca",
								"-cert", "/etc/pwx/etcdcerts/cert",
								"-key", "/etc/pwx/etcdcerts/key",
								"-acltoken", "acltoken",
								"-userpwd", "user:password",
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "etcdcerts",
							VolumeSource: v1.VolumeSource{
								Secret: &v1.SecretVolumeSource{
									SecretName: "px-etcd-auth",
								},
							},
						},
					},
				},
			},
		},
	}
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-etcd-auth",
			Namespace: "custom-ns",
		},
		Data: map[string][]byte{
			"ca":        []byte("ca file content"),
			"cert":      []byte("cert file content"),
			"key":       []byte("key file content"),
			"acl-token": []byte("acltoken"),
			"username":  []byte("user"),
			"password":  []byte("password"),
		},
	}

	k8sClient := testutil.FakeK8sClient(ds, secret)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}
	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd:http://etcd-1.com:1111",
					"etcd:http://etcd-2.com:1111",
				},
				AuthSecret: "px-kvdb-auth-operator",
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	expectedSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-kvdb-auth-operator",
			Namespace: expectedCluster.Namespace,
		},
		Data: map[string][]byte{
			"kvdb-ca.crt": []byte("ca file content"),
			"kvdb.crt":    []byte("cert file content"),
			"kvdb.key":    []byte("key file content"),
			"acl-token":   []byte("acltoken"),
			"username":    []byte("user"),
			"password":    []byte("password"),
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	newSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, newSecret, expectedSecret.Name, expectedSecret.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSecret.Data, newSecret.Data)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func TestStorageClusterExternalKvdbAuthCreateNewSecretForPlainTextACLTokenAndPassword(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "kube-system",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "portworx",
							Args: []string{
								"-c", clusterName,
								"-k", "etcd:http://etcd-1.com:1111,etcd:http://etcd-2.com:1111",
								"-acltoken", "acltoken",
								"-userpwd", "username:password",
							},
						},
					},
				},
			},
		},
	}
	secretToDelete := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-kvdb-auth-operator",
			Namespace: "kube-system",
		},
		Data: map[string][]byte{},
	}

	k8sClient := testutil.FakeK8sClient(ds, secretToDelete)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(false).AnyTimes()
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	driver.EXPECT().GetSelectorLabels().AnyTimes()
	driver.EXPECT().String().AnyTimes()
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.16.0",
	}
	expectedCluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_SECRETS_NAMESPACE",
						Value: "portworx",
					},
				},
			},
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"etcd:http://etcd-1.com:1111",
					"etcd:http://etcd-2.com:1111",
				},
				AuthSecret: "px-kvdb-auth-operator",
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
			Stork: &corev1.StorkSpec{
				Enabled: false,
			},
			Autopilot: &corev1.AutopilotSpec{
				Enabled: false,
			},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{
					ExportMetrics: false,
					Enabled:       false,
					AlertManager: &corev1.AlertManagerSpec{
						Enabled: false,
					},
				},
				Telemetry: &corev1.TelemetrySpec{
					Enabled: false,
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			Phase: constants.PhaseAwaitingApproval,
		},
	}
	expectedSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-kvdb-auth-operator",
			Namespace: expectedCluster.Namespace,
		},
		Data: map[string][]byte{
			"acl-token": []byte("acltoken"),
			"username":  []byte("username"),
			"password":  []byte("password"),
		},
	}

	migrator := New(ctrl)
	go migrator.Start()
	cluster := &corev1.StorageCluster{}
	err := wait.PollImmediate(time.Millisecond*200, time.Second*15, func() (bool, error) {
		err := testutil.Get(k8sClient, cluster, clusterName, ds.Namespace)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, expectedCluster.Annotations, cluster.Annotations)
	require.ElementsMatch(t, expectedCluster.Spec.Env, cluster.Spec.Env)
	expectedCluster.Spec.Env = nil
	cluster.Spec.Env = nil
	require.Equal(t, expectedCluster.Spec, cluster.Spec)
	require.Equal(t, expectedCluster.Status.Phase, cluster.Status.Phase)

	newSecret := &v1.Secret{}
	err = testutil.Get(k8sClient, newSecret, expectedSecret.Name, expectedSecret.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSecret.Data, newSecret.Data)

	// Stop the migration process by removing the daemonset
	err = testutil.Delete(k8sClient, ds)
	require.NoError(t, err)
}

func validateMigrationIsInProgress(
	k8sClient client.Client,
	cluster *corev1.StorageCluster,
) error {
	f := func() (interface{}, bool, error) {
		currCluster := &corev1.StorageCluster{}
		if err := testutil.Get(k8sClient, currCluster, cluster.Name, cluster.Namespace); err != nil {
			return nil, true, err
		}
		if currCluster.Status.Phase != constants.PhaseMigrationInProgress {
			return nil, true, fmt.Errorf("migration status expected to be in progress")
		}
		return nil, false, nil
	}
	_, err := task.DoRetryWithTimeout(f, 35*time.Second, 2*time.Second)
	return err
}

func validateNodeStatus(
	k8sClient client.Client,
	nodeName, expectedStatus string,
) error {
	f := func() (interface{}, bool, error) {
		node := &v1.Node{}
		if err := testutil.Get(k8sClient, node, nodeName, ""); err != nil {
			return nil, true, err
		}
		currStatus := node.Labels[constants.LabelPortworxDaemonsetMigration]
		if currStatus != expectedStatus {
			return nil, true, fmt.Errorf("status of node %s: expected: %s, actual: %s",
				node.Name, expectedStatus, currStatus)
		}
		return nil, false, nil
	}
	_, err := task.DoRetryWithTimeout(f, 30*time.Second, 2*time.Second)
	return err
}

func constructNode(id int) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("k8s-node-%d", id),
		},
	}
}

func constructDaemonSetPod(ds *appsv1.DaemonSet, id int) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("daemonset-pod-%d", id),
			Namespace: ds.Namespace,
			Labels: map[string]string{
				"name": "portworx",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(ds, appsv1.SchemeGroupVersion.WithKind("DaemonSet")),
			},
		},
		Spec: v1.PodSpec{
			NodeName: fmt.Sprintf("k8s-node-%d", id),
		},
	}
}

func constructOperatorPod(cluster *corev1.StorageCluster, id int) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("operator-pod-%d", id),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelKeyDriverName:  "mock-driver",
				constants.LabelKeyClusterName: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cluster, corev1.SchemeGroupVersion.WithKind("StorageCluster")),
			},
		},
		Spec: v1.PodSpec{
			NodeName: fmt.Sprintf("k8s-node-%d", id),
		},
		Status: v1.PodStatus{
			Conditions: []v1.PodCondition{
				{
					Type:   v1.PodReady,
					Status: v1.ConditionTrue,
				},
			},
		},
	}
}

func expectedDaemonSetAffinity() *v1.Affinity {
	return &v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      constants.LabelPortworxDaemonsetMigration,
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{constants.LabelValueMigrationPending},
							},
						},
					},
				},
			},
		},
	}
}
