package component

import (
	"context"
	"fmt"
	"io/ioutil"
	"path"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
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

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// TelemetryComponentName name of the telemetry component
	TelemetryComponentName = "Portworx Telemetry"
	// TelemetryCCMProxyConfigMapName is the name of the config map that stores the PX_HTTP(S)_PROXY env var
	TelemetryCCMProxyConfigMapName = "px-ccm-service-proxy-config"
	// TelemetryCCMProxyFilePath path of the http proxy file
	TelemetryCCMProxyFilePath = "/cache/network/http_proxy"
	// TelemetryCCMProxyFileName name of the http proxy file
	TelemetryCCMProxyFileName = "http_proxy"
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
	// CollectorServiceAccountName is name of the metrics collector service account
	CollectorServiceAccountName = "px-metrics-collector"
	// TelemetryConfigMapName is the name of the config map that stores the telemetry configuration for CCM
	TelemetryConfigMapName = "px-telemetry-config"
	// TelemetryPropertiesFilename is the name of the CCM properties file
	TelemetryPropertiesFilename = "ccm.properties"
	// TelemetryArcusLocationFilename is name of the file storing the location of arcus endpoint to use
	TelemetryArcusLocationFilename = "location"
	// TelemetryCertName is name of the telemetry cert.
	TelemetryCertName = "pure-telemetry-certs"

	// RoleNameSecretManager is name of role secret-manager
	RoleNameSecretManager = "secret-manager"
	// RoleBindingNameSecretManager is name of role binding registerrolebinding
	RoleBindingNameSecretManager = "registerrolebinding"
	// RoleNameSTCReader is name of role stc-reader
	RoleNameSTCReader = "stc-reader"
	// RoleBindingNameSTCReader is name of role binding stcrolebinding
	RoleBindingNameSTCReader = "stcrolebinding"
	// ConfigMapNameRegistrationConfig is name of config map registration-config
	ConfigMapNameRegistrationConfig = "registration-config"
	// ConfigMapNameProxyConfigRegister is name of config map proxy-config-register
	ConfigMapNameProxyConfigRegister = "proxy-config-register"
	// ConfigMapNameProxyConfigRest is name of config map proxy-config-rest
	ConfigMapNameProxyConfigRest = "proxy-config-rest"
	// ConfigMapNameTLSCertificate is name of config map tls-certificate
	ConfigMapNameTLSCertificate = "tls-certificate"
	// ConfigMapNamePxTelemetryConfig is name of config map px-telemetry-config
	ConfigMapNamePxTelemetryConfig = TelemetryConfigMapName
	// DeploymentNameRegistrationService is name of deployment registration-servicea
	DeploymentNameRegistrationService = "registration-service"
	// DeploymentNamePhonehomeCluster is name of deployment phonehome-cluster
	DeploymentNamePhonehomeCluster = "phonehome-cluster"

	roleFileNameSecretManager             = "secret-manager-role.yaml"
	roleBindingFileNameSecretManager      = "secret-manager-role-binding.yaml"
	roleFileNameSTCReader                 = "stc-reader-role.yaml"
	roleBindingFileNameSTCReader          = "stc-reader-role-binding.yaml"
	deploymentFileNameRegistrationService = "registration-service.yaml"
	deploymentFileNamePhonehomeCluster    = "phonehome-cluster.yaml"
	containerNameRegistrationService      = "registration"
	containerNameLogUploader              = "log-upload-service"
	containerNameTelemetryProxy           = "envoy"
	portNameLogUploaderContainer          = "loguploader"

	configFileNameRegistrationConfig               = "config_properties_px.yaml"
	configFileNameProxyConfigRegister              = "envoy-config-register.yaml"
	configFileNameProxyConfigRegisterCustomerProxy = "envoy-config-register-customer-proxy.yaml"
	configFileNameProxyConfigRest                  = "envoy-config-rest.yaml"
	configFileNameTLSCertificate                   = "tls_certificate_sds_secret.yaml"
	configFileNamePXTelemetryConfig                = TelemetryPropertiesFilename
	configParameterApplianceID                     = "APPLIANCE_ID"
	configParameterComponentSN                     = "COMPONENT_SN"
	configParameterProductVersion                  = "PRODUCT_VERSION"
	configParameterRegisterProxyURL                = "REGISTER_PROXY_URL"
	configParameterRestProxyURL                    = "REST_PROXY_URL"
	configParameterCertSecretNamespace             = "CERT_SECRET_NAMESPACE"
	configParameterCustomProxyPort                 = "CUSTOM_PROXY_PORT"
	configParameterPortworxPort                    = "PORTWORX_PORT"

	productionArcusLocation         = "external"
	productionArcusRestProxyURL     = "rest.cloud-support.purestorage.com"
	productionArcusRegisterProxyURL = "register.cloud-support.purestorage.com"
	stagingArcusLocation            = "internal"
	stagingArcusRestProxyURL        = "rest.staging-cloud-support.purestorage.com"
	stagingArcusRegisterProxyURL    = "register.staging-cloud-support.purestorage.com"

	// port for log uploader, avoid 1970 as used by old ccm service, to be configurable in the future
	logUploaderPort = 9090

	defaultCollectorMemoryRequest = "64Mi"
	defaultCollectorMemoryLimit   = "128Mi"
	defaultCollectorCPU           = "0.2"
)

type telemetry struct {
	k8sClient                    client.Client
	sdkConn                      *grpc.ClientConn
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

func (t *telemetry) MarkDeleted() {
	t.isCollectorDeploymentCreated = false
}

// RegisterTelemetryComponent registers the telemetry  component
func RegisterTelemetryComponent() {
	Register(TelemetryComponentName, &telemetry{})
}

func (t *telemetry) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := t.setTelemetryCertOwnerRef(cluster, ownerRef); err != nil {
		return err
	}

	if pxutil.RunCCMGo(cluster) {
		// Remove old ccm components first if exist
		if err := t.deleteCCMJava(cluster, ownerRef); err != nil {
			return err
		}
		return t.reconcileCCMGo(cluster, ownerRef)
	}
	return t.reconcileCCMJava(cluster, ownerRef)
}

func (t *telemetry) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := t.deleteCCMJava(cluster, ownerRef); err != nil {
		return err
	}

	if err := t.deleteCCMGo(cluster, ownerRef); err != nil {
		return err
	}

	// Both ccm java and go create this config map with same name, only delete it when disabled
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNamePxTelemetryConfig, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	t.closeSdkConn()
	return nil
}

// closeSdkConn closes the sdk connection and resets it to nil
func (t *telemetry) closeSdkConn() {
	if t.sdkConn == nil {
		return
	}

	if err := t.sdkConn.Close(); err != nil {
		logrus.Errorf("Failed to close sdk connection: %s", err.Error())
	}
	t.sdkConn = nil
}

// reconcileCCMJava installs CCM Java on older px versions
func (t *telemetry) reconcileCCMJava(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := t.createCCMJavaConfigMap(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMJavaProxyConfigMap(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.deployMetricsCollector(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

// reconcileCCMGo installs CCM Go on px 2.12+
func (t *telemetry) reconcileCCMGo(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	logrus.Infof("JLIAO: reconcile new ccm-go")
	if err := t.reconcileCCMGoRolesAndRoleBindings(cluster, ownerRef); err != nil {
		return err
	}
	if cluster.Status.ClusterUID == "" {
		logrus.Warn("clusterUID is empty, wait for it to fill telemetry proxy config")
		return nil
	}
	if err := t.reconcileCCMGoConfigMaps(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMGoDeployments(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

// Pure-telemetry-certs is created by ccm container outside of operator, we shall
// set owner ref to StorageCluster so it gets deleted.
func (t *telemetry) setTelemetryCertOwnerRef(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	secret := &v1.Secret{}
	err := t.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      TelemetryCertName,
			Namespace: cluster.Namespace,
		},
		secret,
	)

	// The cert is created after ccm container starts, so we may not have it for a while.
	if errors.IsNotFound(err) {
		logrus.Infof("JLIAO: not found")
		return nil
	} else if err != nil {
		return err
	}

	for _, o := range secret.OwnerReferences {
		if o.UID == ownerRef.UID {
			return nil
		}
	}

	secret.OwnerReferences = append(secret.OwnerReferences, *ownerRef)

	return t.k8sClient.Update(context.TODO(), secret)
}

func (t *telemetry) deleteCCMJava(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryCCMProxyConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	if err := t.deleteMetricsCollector(cluster.Namespace, ownerRef); err != nil {
		return err
	}

	return nil
}

func (t *telemetry) deleteCCMGo(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteDeployment(t.k8sClient, DeploymentNamePhonehomeCluster, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(t.k8sClient, DeploymentNameRegistrationService, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameRegistrationConfig, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameProxyConfigRegister, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameProxyConfigRest, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTLSCertificate, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRoleBinding(t.k8sClient, RoleBindingNameSecretManager, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRole(t.k8sClient, RoleNameSecretManager, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRoleBinding(t.k8sClient, RoleBindingNameSTCReader, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRole(t.k8sClient, RoleNameSTCReader, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
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

	t.isCollectorDeploymentCreated = false
	return nil
}

func (t *telemetry) deployMetricsCollector(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	pxVer2_9_1, _ := version.NewVersion("2.9.1")
	pxVersion := pxutil.GetPortworxVersion(cluster)
	if pxVersion.LessThan(pxVer2_9_1) {
		// Run metrics collector only for portworx version 2.9.1+
		return nil
	}

	if len(cluster.Status.ClusterUID) == 0 {
		logrus.Warn("clusterUID is empty, wait for it to fill collector proxy config")
		return nil
	}

	if err := t.createCollectorServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}

	if err := t.createCCMJavaProxyConfigMap(cluster, ownerRef); err != nil {
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
					ResourceNames: []string{PxSCCName},
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
	if equal, _ := util.DeepEqualDeployment(deployment, existingDeployment); !equal {
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

func (t *telemetry) createCollectorServiceAccount(
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

func (t *telemetry) getCollectorDeployment(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) (*appsv1.Deployment, error) {
	collectorImage, err := getDesiredCollectorImage(cluster)
	if err != nil {
		return nil, err
	}

	collectorProxyImage, err := getDesiredProxyImage(cluster)
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

func (t *telemetry) createCCMJavaProxyConfigMap(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	arcusRestProxyURL := getArcusRestProxyURL(cluster)
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
`, cluster.Status.ClusterUID, pxutil.GetPortworxVersion(cluster), arcusRestProxyURL, arcusRestProxyURL)

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

func (t *telemetry) createCCMJavaConfigMap(
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
		TelemetryPropertiesFilename:    config,
		TelemetryArcusLocationFilename: getArcusTelemetryLocation(cluster),
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

func (t *telemetry) reconcileCCMJavaProxyConfigMap(
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

func (t *telemetry) reconcileCCMGoRolesAndRoleBindings(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Create secret manager role and role binding
	if err := createRoleFromFile(t.k8sClient, roleFileNameSecretManager, RoleNameSecretManager, cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role %s/%s", cluster.Namespace, RoleNameSecretManager)
		return err
	}
	if err := createRoleBindingFromFile(t.k8sClient, roleBindingFileNameSecretManager, RoleBindingNameSecretManager, cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role binding %s/%s", cluster.Namespace, RoleBindingNameSecretManager)
		return err
	}
	// Create stc reader role and role binding
	if err := createRoleFromFile(t.k8sClient, roleFileNameSTCReader, RoleNameSTCReader, cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role %s/%s", cluster.Namespace, RoleNameSTCReader)
		return err
	}
	if err := createRoleBindingFromFile(t.k8sClient, roleBindingFileNameSTCReader, RoleBindingNameSTCReader, cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role binding %s/%s", cluster.Namespace, RoleBindingNameSTCReader)
		return err
	}
	return nil
}

// reconcileCCMGoConfigMaps reconcile 5 config maps in total
func (t *telemetry) reconcileCCMGoConfigMaps(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Reconcile config maps for registration service
	// Create cm registration-config from config_properties_px.yaml
	if err := t.createCCMGoConfigMapRegistrationConfig(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to create cm registration-config from config_properties_px.yaml ")
		return err
	}
	logrus.Infof("JLIAO: create cm registration-config from config_properties_px.yaml")
	// Create cm proxy-config-register from envoy-config-register.yaml
	if err := t.createCCMGoConfigMapProxyConfigRegister(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to create cm proxy-config-register from envoy-config-register.yaml")
		return err
	}
	logrus.Infof("JLIAO: create cm proxy-config-register from envoy-config-register.yaml")

	// Reconcile config maps for log uploader
	// Creat cm proxy-config-rest from envoy-config-rest.yaml
	if err := t.createCCMGoConfigMapProxyConfigRest(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to creat cm proxy-config-rest from envoy-config-rest.yaml")
		return err
	}
	logrus.Infof("JLIAO: creat cm proxy-config-rest from envoy-config-rest.yaml")
	// Create cm tls-certificate from tls_certificate_sds_secret.yaml
	if err := t.createCCMGoConfigMapTLSCertificate(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to creat cm proxy-config-rest from envoy-config-rest.yaml")
		return err
	}
	logrus.Infof("JLIAO: create cm tls-certificate from tls_certificate_sds_secret.yaml")
	// Create cm px-telemetry-config from ccm.properties and location
	// CCM Java creates this cm as well, let's delete it and create a new one
	if err := t.createCCMGoConfigMapPXTelemetryConfig(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to create cm px-telemetry-config from ccm.properties and location")
		return err
	}
	logrus.Infof("JLIAO: create cm px-telemetry-config from ccm.properties and location")

	return nil
}

func (t *telemetry) reconcileCCMGoDeployments(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Create deployment registration-service
	if err := t.createDeploymentRegistrationService(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create deployment %s/%s", cluster.Namespace, DeploymentNameRegistrationService)
		return err
	}
	logrus.Infof("JLIAO: create deployment registration-service")
	// create deployment phonehone-cluster
	if err := t.createDeploymentPhonehomeCluster(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create deployment %s/%s", cluster.Namespace, DeploymentNamePhonehomeCluster)
		return err
	}
	logrus.Infof("JLIAO: create deployment phonehone-cluster")
	return nil
}

// create cm registration-config from config_properties_px.yaml
func (t *telemetry) createCCMGoConfigMapRegistrationConfig(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config, err := readConfigMapDataFromFile(configFileNameRegistrationConfig, map[string]string{
		configParameterCertSecretNamespace: cluster.Namespace,
	})
	if err != nil {
		return err
	}
	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameRegistrationConfig,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameRegistrationConfig: config,
			},
		},
		ownerRef,
	)
}

// create cm proxy-config-register from envoy-config-register.yaml
func (t *telemetry) createCCMGoConfigMapProxyConfigRegister(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	configFileName := configFileNameProxyConfigRegister
	replaceMap := map[string]string{
		configParameterApplianceID:    cluster.Status.ClusterUID,
		configParameterComponentSN:    cluster.Name,
		configParameterProductVersion: pxutil.GetPortworxVersion(cluster).String(),
	}

	proxy := pxutil.GetPxProxyEnvVarValue(cluster)
	if proxy == "" {
		replaceMap[configParameterRegisterProxyURL] = getArcusRegisterProxyURL(cluster)
	}
	// TODO: support custom proxy, extract proxy address and port

	config, err := readConfigMapDataFromFile(configFileName, replaceMap)
	if err != nil {
		return err
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameProxyConfigRegister,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameProxyConfigRegister: config,
			},
		},
		ownerRef,
	)
}

// creat cm proxy-config-rest from envoy-config-rest.yaml
func (t *telemetry) createCCMGoConfigMapProxyConfigRest(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	configFileName := configFileNameProxyConfigRest
	replaceMap := map[string]string{
		configParameterApplianceID:    cluster.Status.ClusterUID,
		configParameterProductVersion: pxutil.GetPortworxVersion(cluster).String(),
	}

	proxy := pxutil.GetPxProxyEnvVarValue(cluster)
	if proxy == "" {
		replaceMap[configParameterRestProxyURL] = getArcusRestProxyURL(cluster)
	}
	// TODO: support custom proxy

	config, err := readConfigMapDataFromFile(configFileName, replaceMap)
	if err != nil {
		return err
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameProxyConfigRest,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameProxyConfigRest: config,
			},
		},
		ownerRef,
	)
}

// create cm tls-certificate from tls_certificate_sds_secret.yaml
func (t *telemetry) createCCMGoConfigMapTLSCertificate(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config, err := readConfigMapDataFromFile(configFileNameTLSCertificate, nil)
	if err != nil {
		return err
	}
	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameTLSCertificate,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameTLSCertificate: config,
			},
		},
		ownerRef,
	)
}

// create cm px-telemetry-config from ccm.properties and location
func (t *telemetry) createCCMGoConfigMapPXTelemetryConfig(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config, err := readConfigMapDataFromFile(configFileNamePXTelemetryConfig, map[string]string{
		configParameterPortworxPort: fmt.Sprintf(`"%v"`, logUploaderPort),
	})
	if err != nil {
		return err
	}
	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNamePxTelemetryConfig,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNamePXTelemetryConfig: config,
				TelemetryArcusLocationFilename:  getArcusTelemetryLocation(cluster),
			},
		},
		ownerRef,
	)
}

func (t *telemetry) createDeploymentRegistrationService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	deployment, err := k8sutil.GetDeploymentFromFile(deploymentFileNameRegistrationService, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	telemetryImage, err := GetDesiredTelemetryImage(cluster)
	if err != nil {
		return err
	}
	proxyImage, err := getDesiredProxyImage(cluster)
	if err != nil {
		return err
	}
	var containers []v1.Container
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == containerNameRegistrationService {
			c.Image = telemetryImage
		} else if c.Name == containerNameTelemetryProxy {
			c.Image = proxyImage
		}
		containers = append(containers, *c.DeepCopy())
	}
	deployment.Name = DeploymentNameRegistrationService
	deployment.Namespace = cluster.Namespace
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	deployment.Spec.Template.Spec.Containers = containers
	return k8sutil.CreateOrUpdateDeployment(t.k8sClient, deployment, ownerRef)
}

func (t *telemetry) createDeploymentPhonehomeCluster(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	deployment, err := k8sutil.GetDeploymentFromFile(deploymentFileNamePhonehomeCluster, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	logUploaderImage, err := getDesiredLogUploaderImage(cluster)
	if err != nil {
		return err
	}
	proxyImage, err := getDesiredProxyImage(cluster)
	if err != nil {
		return err
	}
	var containers []v1.Container
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == containerNameLogUploader {
			c.Image = logUploaderImage
			var ports []v1.ContainerPort
			for _, p := range c.Ports {
				if p.Name == portNameLogUploaderContainer {
					p.HostPort = logUploaderPort
					p.ContainerPort = logUploaderPort
				}
				ports = append(ports, *p.DeepCopy())
			}
			c.Ports = ports
		} else if c.Name == containerNameTelemetryProxy {
			c.Image = proxyImage
		}
		containers = append(containers, *c.DeepCopy())
	}
	deployment.Name = DeploymentNamePhonehomeCluster
	deployment.Namespace = cluster.Namespace
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	deployment.Spec.Template.Spec.Affinity = &v1.Affinity{
		NodeAffinity: cluster.Spec.Placement.NodeAffinity,
	}
	deployment.Spec.Template.Spec.Containers = containers
	deployment.Spec.Template.Spec.InitContainers = []v1.Container{{
		Name:  "init-cont",
		Image: "bitnami/kubectl:1.24.3",
		Command: []string{
			"/bin/bash", "-c",
			fmt.Sprintf("cert=$(kubectl get secrets -n %s --field-selector=metadata.name=pure-telemetry-certs -ojson |jq .items[0].data.cert); until [[ ${#cert} -gt 4 ]]; do echo waiting for cert generation; sleep 4; cert=$(kubectl get secrets -n %s --field-selector=metadata.name=pure-telemetry-certs -ojson |jq .items[0].data.cert); done;",
				cluster.Namespace,
				cluster.Namespace),
		},
	}}

	// Count number of replicas using number of storage node on this cluster
	t.sdkConn, err = pxutil.GetPortworxConn(t.sdkConn, t.k8sClient, cluster.Namespace)
	if err != nil {
		return err
	}

	replicasCount, err := pxutil.CountStorageNodes(cluster, t.sdkConn, t.k8sClient)
	if err != nil {
		t.closeSdkConn()
		return err
	}
	num := int32(replicasCount)
	deployment.Spec.Replicas = &num

	return k8sutil.CreateOrUpdateDeployment(t.k8sClient, deployment, ownerRef)
}

func getArcusTelemetryLocation(cluster *corev1.StorageCluster) string {
	if cluster.Annotations[pxutil.AnnotationTelemetryArcusLocation] != "" {
		location := strings.ToLower(strings.TrimSpace(cluster.Annotations[pxutil.AnnotationTelemetryArcusLocation]))
		if location == stagingArcusLocation {
			return location
		}
	}
	return productionArcusLocation
}

func getArcusRestProxyURL(cluster *corev1.StorageCluster) string {
	if getArcusTelemetryLocation(cluster) == stagingArcusLocation {
		return stagingArcusRestProxyURL
	}
	return productionArcusRestProxyURL
}

func getArcusRegisterProxyURL(cluster *corev1.StorageCluster) string {
	if getArcusTelemetryLocation(cluster) == stagingArcusLocation {
		return stagingArcusRegisterProxyURL
	}
	return productionArcusRegisterProxyURL
}

// GetDesiredTelemetryImage returns desired telemetry container image
func GetDesiredTelemetryImage(cluster *corev1.StorageCluster) (string, error) {
	if pxutil.IsTelemetryEnabled(cluster.Spec) {
		if cluster.Spec.Monitoring.Telemetry.Image != "" {
			return util.GetImageURN(cluster, cluster.Spec.Monitoring.Telemetry.Image), nil
		}

		if cluster.Status.DesiredImages != nil {
			return util.GetImageURN(cluster, cluster.Status.DesiredImages.Telemetry), nil
		}
	}
	return "", fmt.Errorf("telemetry image is empty")
}

func getDesiredLogUploaderImage(cluster *corev1.StorageCluster) (string, error) {
	if pxutil.IsTelemetryEnabled(cluster.Spec) {
		if cluster.Spec.Monitoring.Telemetry.ImageLogUpload != "" {
			return util.GetImageURN(cluster, cluster.Spec.Monitoring.Telemetry.ImageLogUpload), nil
		}

		if cluster.Status.DesiredImages != nil {
			return util.GetImageURN(cluster, cluster.Status.DesiredImages.LogUploader), nil
		}
	}
	return "", fmt.Errorf("log uploader image is empty")
}

func getDesiredProxyImage(cluster *corev1.StorageCluster) (string, error) {
	if cluster.Status.DesiredImages != nil {
		if pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster)) {
			return util.GetImageURN(cluster, cluster.Status.DesiredImages.TelemetryProxy), nil
		}
		return util.GetImageURN(cluster, cluster.Status.DesiredImages.MetricsCollectorProxy), nil
	}
	return "", fmt.Errorf("telemetry proxy image is empty")
}

func getDesiredCollectorImage(cluster *corev1.StorageCluster) (string, error) {
	if cluster.Status.DesiredImages != nil {
		return util.GetImageURN(cluster, cluster.Status.DesiredImages.MetricsCollector), nil
	}
	return "", fmt.Errorf("metrics collector image is empty")
}

func createRoleFromFile(
	k8sClient client.Client,
	filename string,
	name string,
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	role, err := k8sutil.GetRoleFromFile(filename, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	role.Name = name
	role.Namespace = cluster.Namespace
	role.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	return k8sutil.CreateOrUpdateRole(
		k8sClient,
		role,
		ownerRef,
	)
}

func createRoleBindingFromFile(
	k8sClient client.Client,
	filename string,
	name string,
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	roleBinding, err := k8sutil.GetRoleBindingFromFile(filename, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	roleBinding.Name = name
	roleBinding.Namespace = cluster.Namespace
	roleBinding.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	roleBinding.Subjects = []rbacv1.Subject{
		{
			Kind:      "ServiceAccount",
			Name:      "default",
			APIGroup:  "",
			Namespace: cluster.Namespace,
		},
	}
	return k8sutil.CreateOrUpdateRoleBinding(
		k8sClient,
		roleBinding,
		ownerRef,
	)
}

func readConfigMapDataFromFile(
	filename string,
	replace map[string]string,
) (string, error) {
	filepath := path.Join(pxutil.SpecsBaseDir(), filename)
	fileBytes, err := ioutil.ReadFile(filepath)
	if err != nil {
		return "", err
	}
	data := string(fileBytes)
	for k, v := range replace {
		data = strings.ReplaceAll(data, k, v)
	}

	return data, nil
}

func init() {
	RegisterTelemetryComponent()
}
