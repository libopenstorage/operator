package component

import (
	"fmt"
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// TelemetryComponentName name of the telemetry component
	TelemetryComponentName = "Portworx Telemetry"
	// TelemetryConfigMapName is the name of the config map that stores the telemetry configuration for CCM
	TelemetryConfigMapName = "px-telemetry-config"
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
	// CollectorConfigFileName is file name of the collector pod config.
	CollectorConfigFileName = "portworx.yaml"
	// CollectorConfigMapName is name of config map for metrics collector.
	CollectorConfigMapName = "px-collector-config"
	// CollectorProxyConfigMapName is name of the config map for envoy proxy.
	CollectorProxyConfigMapName = "px-proxy-config"
	// CollectorDeploymentName is name of metrics collector deployment.
	CollectorDeploymentName = "px-metrics-collector"
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

func (t *telemetry) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec)
}

func (t *telemetry) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := t.createConfigMap(cluster, ownerRef); err != nil {
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

func (t *telemetry) deployMetricsCollector(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
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

	if err := t.createCollectorDeployment(cluster, ownerRef); err != nil {
		return err
	}

	return nil
}

func (t *telemetry) createCollectorDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if t.isCollectorDeploymentCreated {
		return nil
	}

	replicas := int32(1)
	labels := map[string]string{
		"role": "realtime-metrics-collector",
	}
	runAsUser := int64(1111)
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
					Containers: []v1.Container{
						{
							Name:  "collector",
							Image: manifest.DefaultCollectorImage,
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
							Image: manifest.DefaultEnvoyImage,
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

	return k8sutil.CreateOrUpdateDeployment(t.k8sClient, deployment, ownerRef)
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
					Name:      "default",
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
	if !present || len(arcusLocation) == 0 {
		arcusLocation = "rest.cloud-support.purestorage.com"
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
`, cluster.UID, pxutil.GetPortworxVersion(cluster), arcusLocation, arcusLocation)

	data := map[string]string{
		CollectorConfigFileName: config,
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
	config := fmt.Sprintf(
		`
scrapeConfig:
  interval: 10
  k8sConfig:
    pods:
    - podSelector:
        name: portworx
      namespace: %s
      endpoint: metrics
      port: 9001
    - podSelector:
        name: autopilot
      namespace: %s
      endpoint: metrics
      port: 9001
forwardConfig:
  url: http://localhost:10000/metrics/1.0/pure1-metrics-pb
    `, cluster.Namespace, cluster.Namespace)

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
        "path": "/dev/null"
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

func init() {
	RegisterTelemetryComponent()
}
