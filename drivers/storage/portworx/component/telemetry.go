package component

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// TelemetryComponentName name of the telemetry component
	TelemetryComponentName = "Portworx Telemetry"
	// TelemetryConfigMapName is the name of the config map that stores the telemetry configuration for CCM
	TelemetryConfigMapName = "px-telemetry-config"
	// TelemetryCCMProxyConfigMapName is the name of the config map that stores the PX_HTTP(S)_PROXY env var
	TelemetryCCMProxyConfigMapName = "px-ccm-service-proxy-config"
	// TelemetryCCMProxyFilePath path of the http proxy file
	TelemetryCCMProxyFilePath = "/cache/network/http_proxy"
	// TelemetryCCMProxyFileName name of the http proxy file
	TelemetryCCMProxyFileName = "http_proxy"
	// TelemetryPropertiesFilename is the name of the CCM properties file
	TelemetryPropertiesFilename = "ccm.properties"
	// TelemetryArcusLocationFilename is name of the file storing the location of arcus endpoint to use
	TelemetryArcusLocationFilename = "location"
	// TelemetryCertName is name of the telemetry cert.
	TelemetryCertName = "pure-telemetry-certs"
	// CollectorRoleName is name of the Role for metrics collector.
	CollectorRoleName = "px-metrics-collector"
	// CollectorRoleBindingName is name of the role binding for metrics collector.
	CollectorRoleBindingName = "px-metrics-collector"
	// CollectorClusterRoleName name of the metrics collector cluster role
	CollectorClusterRoleName = "px-metrics-collector"
	// CollectorClusterRoleBindingName name of the metrics collector cluster role binding
	CollectorClusterRoleBindingName = "px-metrics-collector"
	// CollectorConfigFileName is file name of the collector pod config.
	CollectorConfigFileName = "portworx.yaml"
	// CollectorProxyConfigFileName is file name of envoy config.
	CollectorProxyConfigFileName = "envoy-config.yaml"
	// CollectorConfigMapName is name of config map for metrics collector.
	CollectorConfigMapName = "px-collector-config"
	// CollectorProxyConfigMapName is name of the config map for envoy proxy.
	CollectorProxyConfigMapName = "px-collector-proxy-config"
	// CollectorDeploymentName is name of metrics collector deployment.
	CollectorDeploymentName = "px-metrics-collector"
	// ExternalArcusLocation is the external location customers use, pointing to prod.
	ExternalArcusLocation = "rest.cloud-support.purestorage.com"
	// InternalArcusLocation is for internal use only.
	InternalArcusLocation = "rest.staging-cloud-support.purestorage.com"
	// CollectorServiceAccountName is name of the metrics collector service account
	CollectorServiceAccountName = "px-metrics-collector"

	defaultCollectorMemoryRequest = "64Mi"
	defaultCollectorMemoryLimit   = "128Mi"
	defaultCollectorCPU           = "0.2"
)

type telemetry struct {
	k8sClient                    client.Client
	isCollectorDeploymentCreated bool
}

func (t *telemetry) Name() string {
	return TelemetryComponentName
}

func (t *telemetry) Priority() int32 {
	return DefaultComponentPriority
}

func (t *telemetry) Initialize(k8sClient client.Client, _ version.Version, _ *runtime.Scheme, _ record.EventRecorder) {
	t.k8sClient = k8sClient
}

func (t *telemetry) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (t *telemetry) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec)
}

func (t *telemetry) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := t.createConfigMap(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMProxyConfigMap(cluster, ownerRef); err != nil {
		return err
	}

	if err := t.deployMetricsCollector(cluster, ownerRef); err != nil {
		return err
	}

	return nil
}

func (t *telemetry) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryCCMProxyConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	if err := t.deleteMetricsCollector(cluster.Namespace, ownerRef); err != nil {
		return err
	}

	t.MarkDeleted()
	return nil
}

func (t *telemetry) MarkDeleted() {
	t.isCollectorDeploymentCreated = false
}

// RegisterTelemetryComponent registers the telemetry  component
func RegisterTelemetryComponent() {
	Register(TelemetryComponentName, &telemetry{})
}

func (t *telemetry) deleteMetricsCollector(namespace string, ownerRef *metav1.OwnerReference) error {
	if err := k8sutil.DeleteServiceAccount(t.k8sClient, CollectorServiceAccountName, namespace, *ownerRef); err != nil {
		return err
	}

	if err := k8sutil.DeleteConfigMap(t.k8sClient, CollectorProxyConfigMapName, namespace, *ownerRef); err != nil {
		return err
	}

	if err := k8sutil.DeleteConfigMap(t.k8sClient, CollectorConfigMapName, namespace, *ownerRef); err != nil {
		return err
	}

	if err := k8sutil.DeleteRole(t.k8sClient, CollectorRoleName, namespace, *ownerRef); err != nil {
		return err
	}

	if err := k8sutil.DeleteRoleBinding(t.k8sClient, CollectorRoleBindingName, namespace, *ownerRef); err != nil {
		return err
	}

	if err := k8sutil.DeleteClusterRole(t.k8sClient, CollectorClusterRoleName); err != nil {
		return err
	}

	if err := k8sutil.DeleteClusterRoleBinding(t.k8sClient, CollectorClusterRoleBindingName); err != nil {
		return err
	}

	if err := k8sutil.DeleteDeployment(t.k8sClient, CollectorDeploymentName, namespace, *ownerRef); err != nil {
		return err
	}

	return nil
}

func (t *telemetry) deployMetricsCollector(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if len(cluster.Status.ClusterUID) == 0 {
		logrus.Warn("clusterUID is empty, wait for it to fill collector proxy config")
		return nil
	}

	if err := t.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}

	if err := t.createProxyConfigMap(cluster, ownerRef); err != nil {
		return err
	}

	if err := t.createCollectorConfigMap(cluster, ownerRef); err != nil {
		return err
	}

	if err := t.createCollectorRole(cluster, ownerRef); err != nil {
		return err
	}

	if err := t.createCollectorRoleBinding(cluster, ownerRef); err != nil {
		return err
	}

	if err := t.createCollectorClusterRole(); err != nil {
		return err
	}

	if err := t.createCollectorClusterRoleBinding(cluster.Namespace); err != nil {
		return err
	}

	if err := t.createCollectorDeployment(cluster, ownerRef); err != nil {
		return err
	}

	return nil
}

func (t *telemetry) createCollectorClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		t.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: CollectorClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{"privileged"},
					Verbs:         []string{"use"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.PrivilegedPSPName},
					Verbs:         []string{"use"},
				},
			},
		},
	)
}

func (t *telemetry) createCollectorClusterRoleBinding(
	clusterNamespace string,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		t.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: CollectorClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      CollectorServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     CollectorClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (t *telemetry) createCollectorDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	deployment, err := t.getCollectorDeployment(cluster, ownerRef)
	if err != nil {
		return err
	}

	existingDeployment := &appsv1.Deployment{}
	err = t.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      CollectorDeploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	modified := false
	if equal, _ := util.DeploymentDeepEqual(deployment, existingDeployment); !equal {
		modified = true
	}

	if !t.isCollectorDeploymentCreated || modified {
		logrus.WithFields(logrus.Fields{
			"isCreated": t.isCollectorDeploymentCreated,
			"modified":  modified,
		}).Info("will create/update the deployment.")
		if err = k8sutil.CreateOrUpdateDeployment(t.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}

	t.isCollectorDeploymentCreated = true
	return nil
}

func (t *telemetry) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		t.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            CollectorServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (t *telemetry) getDesiredCollectorImage(cluster *corev1.StorageCluster) (string, error) {
	if cluster.Status.DesiredImages != nil {
		return util.GetImageURN(cluster, cluster.Status.DesiredImages.MetricsCollector), nil
	}
	return "", fmt.Errorf("metrics collector image is empty")
}

func (t *telemetry) getDesiredCollectorProxyImage(cluster *corev1.StorageCluster) (string, error) {
	if cluster.Status.DesiredImages != nil {
		return util.GetImageURN(cluster, cluster.Status.DesiredImages.MetricsCollectorProxy), nil
	}
	return "", fmt.Errorf("metrics collector proxy image is empty")
}

func (t *telemetry) getCollectorDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) (*appsv1.Deployment, error) {
	collectorImage, err := t.getDesiredCollectorImage(cluster)
	if err != nil {
		return nil, err
	}

	collectorProxyImage, err := t.getDesiredCollectorProxyImage(cluster)
	if err != nil {
		return nil, err
	}

	replicas := int32(1)
	labels := map[string]string{
		"role": "realtime-metrics-collector",
	}
	runAsUser := int64(1111)
	cpuQuantity, err := resource.ParseQuantity(defaultCollectorCPU)
	if err != nil {
		return nil, err
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            CollectorDeploymentName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: v1.PodSpec{
					ServiceAccountName: CollectorServiceAccountName,
					Containers: []v1.Container{
						{
							Name:  "collector",
							Image: collectorImage,
							SecurityContext: &v1.SecurityContext{
								RunAsUser: &runAsUser,
							},
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse(defaultCollectorMemoryRequest),
									v1.ResourceCPU:    cpuQuantity,
								},
								Limits: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse(defaultCollectorMemoryLimit),
								},
							},

							Env: []v1.EnvVar{
								{
									Name:  "CONFIG",
									Value: "config/" + CollectorConfigFileName,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      CollectorConfigMapName,
									MountPath: "/config",
									ReadOnly:  true,
								},
							},
							Ports: []v1.ContainerPort{
								{
									Name:          "collector",
									ContainerPort: 80,
									Protocol:      "TCP",
								},
							},
						},
						{
							Name:  "envoy",
							Image: collectorProxyImage,
							SecurityContext: &v1.SecurityContext{
								RunAsUser: &runAsUser,
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      CollectorProxyConfigMapName,
									MountPath: "/config",
									ReadOnly:  true,
								},
								{
									Name:      TelemetryCertName,
									MountPath: "/appliance-cert",
									ReadOnly:  true,
								},
							},
							Args: []string{
								"envoy",
								"--config-path",
								"/config/envoy-config.yaml",
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: CollectorConfigMapName,
							VolumeSource: v1.VolumeSource{
								ConfigMap: &v1.ConfigMapVolumeSource{
									LocalObjectReference: v1.LocalObjectReference{
										Name: CollectorConfigMapName,
									},
								},
							},
						},
						{
							Name: CollectorProxyConfigMapName,
							VolumeSource: v1.VolumeSource{
								ConfigMap: &v1.ConfigMapVolumeSource{
									LocalObjectReference: v1.LocalObjectReference{
										Name: CollectorProxyConfigMapName,
									},
								},
							},
						},
						{
							Name: TelemetryCertName,
							VolumeSource: v1.VolumeSource{
								Secret: &v1.SecretVolumeSource{
									SecretName: TelemetryCertName,
								},
							},
						},
					},
				},
			},
		},
	}

	pxutil.ApplyStorageClusterSettings(cluster, deployment)

	return deployment, nil
}

func (t *telemetry) createCollectorRole(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRole(
		t.k8sClient,
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:            CollectorRoleName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
			},
		},
		ownerRef,
	)
}

func (t *telemetry) createCollectorRoleBinding(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRoleBinding(
		t.k8sClient,
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            CollectorRoleBindingName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      CollectorServiceAccountName,
					Namespace: cluster.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "Role",
				Name:     CollectorRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (t *telemetry) createProxyConfigMap(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	arcusLocation, present := cluster.Annotations[pxutil.AnnotationTelemetryArcusLocation]
	if !present || len(arcusLocation) == 0 || strings.ToLower(arcusLocation) == "external" {
		arcusLocation = ExternalArcusLocation
	} else if strings.ToLower(arcusLocation) == "internal" {
		arcusLocation = InternalArcusLocation
	}

	config := fmt.Sprintf(
		`
admin:
  address:
    socket_address:
      address: 127.0.0.1
      port_value: 9901

static_resources:
  listeners:
  - name: listener_cloud_support
    address:
      socket_address:
        address: 127.0.0.1
        port_value: 10000
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: ingress_http
          access_log:
          - name: envoy.access_loggers.stdout
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.access_loggers.stream.v3.StdoutAccessLog
          http_filters:
          - name: envoy.filters.http.router
          route_config:
            name: local_route
            virtual_hosts:
            - name: local_service
              domains: ["*"]
              routes:
              - match:
                  prefix: "/"
                request_headers_to_add:
                - header:
                   key: "product-name"
                   value: "portworx"
                - header:
                   key: "appliance-id"
                   value: "%s"
                - header:
                   key: "component-sn"
                   value: "portworx-metrics-node"
                - header:
                   key: "product-version"
                   value: "%s"
                route:
                  host_rewrite_literal: %s
                  cluster: cluster_cloud_support
  clusters:
  - name: cluster_cloud_support
    type: STRICT_DNS
    dns_lookup_family: V4_ONLY
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: cluster_cloud_support
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: %s
                port_value: 443
    transport_socket:
      name: envoy.transport_sockets.tls
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
        common_tls_context:
          tls_certificates:
          - certificate_chain:
              filename: /appliance-cert/cert
            private_key:
              filename: /appliance-cert/private_key
`, cluster.Status.ClusterUID, pxutil.GetPortworxVersion(cluster), arcusLocation, arcusLocation)

	data := map[string]string{
		CollectorProxyConfigFileName: config,
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            CollectorProxyConfigMapName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: data,
		},
		ownerRef,
	)
}

func (t *telemetry) createCollectorConfigMap(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	pxSelectorLabels := pxutil.SelectorLabels()
	var selectorStr string
	for k, v := range pxSelectorLabels {
		selectorStr += fmt.Sprintf("\n        %s: %s", k, v)
	}

	port := pxutil.StartPort(cluster)

	config := fmt.Sprintf(
		`
scrapeConfig:
  interval: 10
  k8sConfig:
    pods:
    - podSelector:%s
      namespace: %s
      endpoint: metrics
      port: %d
forwardConfig:
  url: http://localhost:10000/metrics/1.0/pure1-metrics-pb`,
		selectorStr,
		cluster.Namespace,
		port)

	data := map[string]string{
		CollectorConfigFileName: config,
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            CollectorConfigMapName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: data,
		},
		ownerRef,
	)
}

func (t *telemetry) createConfigMap(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config := fmt.Sprintf(`{
      "product_name": "portworx",
       "logging": {
         "array_info_path": "/dev/null"
       },
       "features": {
         "appliance_info": "config",
         "cert_store": "k8s",
         "config_reload": "file",
         "env_info": "file",
         "scheduled_log_uploader":"disabled",
         "upload": "enabled"
       },
      "cert": {
        "activation": {
              "private": "/dev/null",
              "public": "/dev/null"
        },
        "registration_enabled": "true",
        "no_rel_cert_enabled": "true",
        "appliance": {
          "current_cert_dir": "/etc/pwx/ccm/cert"
        }
      },
      "k8s": {
        "cert_secret_name": "pure-telemetry-certs",
        "cert_secret_namespace": "%s"
     },
     "cloud": {
       "array_loc_file_path": "/etc/ccm/%s"
     },
      "server": {
        "hostname": "0.0.0.0"
      },
      "logupload": {
        "logfile_patterns": [
            "/var/cores/*diags*",
            "/var/cores/auto/*diags*",
            "/var/cores/*px-cores*",
            "/var/cores/*.heap",
            "/var/cores/*.stack",
            "/var/cores/.alerts/alerts*"
        ],
        "skip_patterns": [],
        "additional_files": [
            "/etc/pwx/config.json",
            "/var/cores/.alerts/alerts.log",
            "/var/cores/px_etcd_watch.log",
            "/var/cores/px_cache_mon.log",
            "/var/cores/px_cache_mon_watch.log",
            "/var/cores/px_healthmon_watch.log",
            "/var/cores/px_event_watch.log"
        ],
        "phonehome_sent": "/var/cache/phonehome.sent"
      },
      "xml_rpc": {},
      "standalone": {
        "version": "1.0.0",
        "controller_sn": "SA-0",
        "component_name": "SA-0",
        "product_name": "portworx",
        "appliance_id_path": "/etc/pwx/cluster_uuid"
      },
      "subscription": {
        "use_appliance_id": "true"
      },
      "proxy": {
        "path": "/cache/network/http_proxy"
      }
    }
`, cluster.Namespace, TelemetryArcusLocationFilename)

	data := map[string]string{
		TelemetryPropertiesFilename: config,
	}

	if location, present := cluster.Annotations[pxutil.AnnotationTelemetryArcusLocation]; present && len(location) > 0 {
		data[TelemetryArcusLocationFilename] = location
	} else {
		data[TelemetryArcusLocationFilename] = "external"
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            TelemetryConfigMapName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: data,
		},
		ownerRef,
	)
}

func (t *telemetry) reconcileCCMProxyConfigMap(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	proxy := pxutil.GetPxProxyEnvVarValue(cluster)
	// Delete the existing config map if portworx proxy is empty
	if proxy == "" {
		return k8sutil.DeleteConfigMap(t.k8sClient, TelemetryCCMProxyConfigMapName, cluster.Namespace, *ownerRef)
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            TelemetryCCMProxyConfigMapName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				TelemetryCCMProxyFileName: proxy,
			},
		},
		ownerRef,
	)
}

func init() {
	RegisterTelemetryComponent()
}
