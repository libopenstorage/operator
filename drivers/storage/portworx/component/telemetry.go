package component

import (
	"context"
	cryptoTls "crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/preflight"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// ServiceAccountNameTelemetry is name of telemetry service account
	ServiceAccountNameTelemetry = "px-telemetry"
	// ClusterRoleNameTelemetry is name of telemetry cluster role
	ClusterRoleNameTelemetry = "px-telemetry"
	// ClusterRoleBindingNameTelemetry is name of telemetry cluster role binding
	ClusterRoleBindingNameTelemetry = "px-telemetry"
	// RoleNameTelemetry is name of telemetry role
	RoleNameTelemetry = "px-telemetry"
	// RoleBindingNameTelemetry is name of telemetry role binding
	RoleBindingNameTelemetry = "px-telemetry"
	// ConfigMapNameTelemetryRegister is name of config map for registration
	ConfigMapNameTelemetryRegister = "px-telemetry-register"
	// ConfigMapNameTelemetryRegisterProxy is name of config map for registration proxy
	ConfigMapNameTelemetryRegisterProxy = "px-telemetry-register-proxy"
	// ConfigMapNameTelemetryPhonehome is name of config map for phonehome
	ConfigMapNameTelemetryPhonehome = "px-telemetry-phonehome"
	// ConfigMapNameTelemetryPhonehomeProxy is name of config map for phonehome proxy
	ConfigMapNameTelemetryPhonehomeProxy = "px-telemetry-phonehome-proxy"
	// ConfigMapNameTelemetryTLSCertificate is name of config map tls-certificate
	ConfigMapNameTelemetryTLSCertificate = "px-telemetry-tls-certificate"
	// ConfigMapNameTelemetryCollectorV2 is name of config map for metrics collector
	ConfigMapNameTelemetryCollectorV2 = "px-telemetry-collector"
	// ConfigMapNameTelemetryCollectorProxyV2 is name of config map for metrics collector proxy
	ConfigMapNameTelemetryCollectorProxyV2 = "px-telemetry-collector-proxy"
	// DeploymentNameTelemetryRegistration is name of telemetry registration deployment
	DeploymentNameTelemetryRegistration = "px-telemetry-registration"
	// DaemonSetNameTelemetryPhonehome is name of phonehome daemonset
	DaemonSetNameTelemetryPhonehome = "px-telemetry-phonehome"
	// DeploymentNameTelemetryCollectorV2 is name of telemetry metrics collector
	DeploymentNameTelemetryCollectorV2 = "px-telemetry-metrics-collector"

	roleFileNameTelemetry                            = "px-telemetry-role.yaml"
	roleBindingFileNameTelemetry                     = "px-telemetry-role-binding.yaml"
	configFileNameTelemetryRegister                  = "config_properties_px.yaml"
	configFileNameTelemetryRegisterProxy             = "envoy-config-register.yaml"
	configFileNameTelemetryRegisterCustomHttpProxy   = "envoy-config-register-custom-http-proxy.yaml"
	configFileNameTelemetryRegisterCustomHttpsProxy  = "envoy-config-register-custom-https-proxy.yaml"
	configFileNameTelemetryPhonehome                 = "ccm.properties"
	configFileNameTelemetryPhonehomeProxy            = "envoy-config-rest.yaml"
	configFileNameTelemetryPhonehomeCustomHttpProxy  = "envoy-config-rest-custom-http-proxy.yaml"
	configFileNameTelemetryPhonehomeCustomHttpsProxy = "envoy-config-rest-custom-https-proxy.yaml"
	configFileNameTelemetryCollectorProxy            = "envoy-config-collector.yaml"
	configFileNameTelemetryCollectorCustomHttpProxy  = "envoy-config-collector-custom-http-proxy.yaml"
	configFileNameTelemetryCollectorCustomHttpsProxy = "envoy-config-collector-custom-https-proxy.yaml"
	configFileNameTelemetryTLSCertificate            = "tls_certificate_sds_secret.yaml"
	configFileNameTelemetryCustomProxyTLS            = "envoy-config-custom-proxy-tls.yaml"
	deploymentFileNameTelemetryRegistration          = "registration-service.yaml"
	deploymentFileNameTelemetryCollectorV2           = "metrics-collector-deployment.yaml"
	daemonsetFileNameTelemetryPhonehome              = "phonehome-cluster.yaml"

	configParameterApplianceID                           = "APPLIANCE_ID"
	configParameterComponentSN                           = "COMPONENT_SN"
	configParameterProductVersion                        = "PRODUCT_VERSION"
	configParameterRegisterProxyURL                      = "REGISTER_PROXY_URL"
	configParameterRestProxyURL                          = "REST_PROXY_URL"
	configParameterCertSecretNamespace                   = "CERT_SECRET_NAMESPACE"
	configParameterCustomProxyAddress                    = "CUSTOM_PROXY_ADDRESS"
	configParameterCustomProxyPort                       = "CUSTOM_PROXY_PORT"
	configParameterCustomProxyBasicAuth                  = "BASIC_AUTH"
	configParameterPortworxPort                          = "PORTWORX_PORT"
	configParameterRegisterCloudSupportPort              = "REGISTER_CLOUD_SUPPORT_PORT"
	configParameterRestCloudSupportPort                  = "REST_CLOUD_SUPPORT_PORT"
	configParameterCloudSupportTCPProxyPort              = "CLOUD_SUPPORT_TCP_PROXY_PORT"
	configParameterCloudSupportEnvoyInternalRedirectPort = "CLOUD_SUPPORT_ENVOY_INTERNAL_REDIRECT_PORT"
	containerNameTelemetryRegistration                   = "registration"
	containerNameLogUploader                             = "log-upload-service"
	containerNameTelemetryProxy                          = "envoy"
	containerNameTelemetryCollector                      = "collector"
	portNameLogUploaderContainer                         = "loguploader"
	portNameEnvoy                                        = "envoy"

	productionArcusLocation         = "external"
	productionArcusRestProxyURL     = "rest.cloud-support.purestorage.com"
	productionArcusRegisterProxyURL = "register.cloud-support.purestorage.com"
	stagingArcusLocation            = "internal"
	stagingArcusRestProxyURL        = "rest.staging-cloud-support.purestorage.com"
	stagingArcusRegisterProxyURL    = "register.staging-cloud-support.purestorage.com"

	// Ports for telemetry components
	defaultCCMListeningPort            = 9024
	defaultCCMListeningPortForPXge2138 = 9029
	defaultCollectorPort               = 10000
	defaultRegisterPort                = 12001
	defaultPhonehomePort               = 12002

	arcusPingInterval = 6 * time.Second
	arcusPingRetry    = 5
)

type telemetry struct {
	k8sClient                              client.Client
	sdkConn                                *grpc.ClientConn
	isCCMGoSupported                       bool
	isCollectorDeploymentCreated           bool
	isDeploymentRegistrationServiceCreated bool
	isDaemonSetTelemetryPhonehomeCreated   bool
	usePxProxy                             bool
	reconcileMetricsCollector              *bool
}

func (t *telemetry) Name() string {
	return TelemetryComponentName
}

func (t *telemetry) Priority() int32 {
	return DefaultComponentPriority
}

func (t *telemetry) Initialize(k8sClient client.Client, _ version.Version, _ *runtime.Scheme, _ record.EventRecorder) {
	t.k8sClient = k8sClient
	// Set flag whether to use PX custom proxy for ccm components,
	// set to false if allowing ccm to access Pure1 cloud directly
	t.usePxProxy = true
}

func (t *telemetry) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (t *telemetry) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec)
}

func (t *telemetry) MarkDeleted() {
	t.isCollectorDeploymentCreated = false
	t.isDeploymentRegistrationServiceCreated = false
	t.isDaemonSetTelemetryPhonehomeCreated = false
}

// RegisterTelemetryComponent registers the telemetry  component
func RegisterTelemetryComponent() {
	Register(TelemetryComponentName, &telemetry{})
}

func (t *telemetry) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := pxutil.SetTelemetryCertOwnerRef(cluster, ownerRef, t.k8sClient); err != nil {
		return err
	}
	if err := t.shouldReconcileMetricsCollector(cluster); err != nil {
		return err
	}
	t.isCCMGoSupported = pxutil.IsCCMGoSupported(pxutil.GetPortworxVersion(cluster))
	if t.isCCMGoSupported {
		return t.reconcileCCMGo(cluster, ownerRef)
	}
	return t.reconcileCCMJava(cluster, ownerRef)
}

func (t *telemetry) Delete(cluster *corev1.StorageCluster) error {
	// When disabling telemetry, try to cleanup both new and old components
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	if err := t.deleteCCMJava(cluster, ownerRef); err != nil {
		return err
	}

	if err := t.deleteCCMGo(cluster, ownerRef); err != nil {
		return err
	}
	t.closeSdkConn()

	t.MarkDeleted()
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

// reconcileCCMGo installs CCM Go on px 2.12+
func (t *telemetry) reconcileCCMGo(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Reconcile config maps for log uploader
	// Attempt to create the cm for phonehome early for reference
	restart, err := t.createCCMGoConfigMapTelemetryPhonehome(cluster, ownerRef)
	if err != nil {
		logrus.WithError(err).Warnf("failed to create cm %s from %s", ConfigMapNameTelemetryPhonehome, configFileNameTelemetryPhonehome)
	}
	if cluster.Status.ClusterUID == "" {
		logrus.Warn("clusterUID is empty, wait for it to reconcile telemetry components")
		return nil
	}
	if err := t.reconcileCCMGoServiceAccount(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMGoClusterRolesAndClusterRoleBindings(cluster); err != nil {
		return err
	}
	if err := t.reconcileCCMGoRolesAndRoleBindings(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMGoTelemetryRegistration(cluster, ownerRef); err != nil {
		return err
	}
	// Create cm for TLS certificate
	if err := t.createCCMGoConfigMapTLSCertificate(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s from %s", ConfigMapNameTelemetryTLSCertificate, configFileNameTelemetryTLSCertificate)
		return err
	}
	if err := t.reconcileCCMGoTelemetryPhonehome(cluster, ownerRef, restart); err != nil {
		return err
	}
	if err := t.reconcileCCMGoTelemetryCollectorV2(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGo(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteServiceAccount(t.k8sClient, ServiceAccountNameTelemetry, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := t.deleteCCMGoClusterRoleAndClusterRoleBinding(); err != nil {
		return err
	}
	if err := t.deleteCCMGoRolesAndRoleBindings(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.deleteCCMGoRegistrationService(cluster, ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryTLSCertificate, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := t.deleteCCMGoPhonehomeCluster(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.deleteCCMGoMetricsCollectorV2(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) reconcileCCMGoServiceAccount(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Create service account for registration service, log uploader and metrics collector v2
	if err := t.createServiceAccountPxTelemetry(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) reconcileCCMGoClusterRolesAndClusterRoleBindings(
	cluster *corev1.StorageCluster,
) error {
	// Create cluster role and cluster role binding for registration service, log uploader and metrics collector v2
	if err := t.createClusterRolePxTelemetry(); err != nil {
		return err
	}
	if err := t.createClusterRoleBindingPxTelemetry(cluster.Namespace); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoClusterRoleAndClusterRoleBinding() error {
	if err := k8sutil.DeleteClusterRole(t.k8sClient, ClusterRoleNameTelemetry); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(t.k8sClient, ClusterRoleBindingNameTelemetry); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) reconcileCCMGoRolesAndRoleBindings(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Create telemetry role and role binding
	if err := createRoleFromFile(t.k8sClient, roleFileNameTelemetry, RoleNameTelemetry, cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role %s/%s", cluster.Namespace, RoleNameTelemetry)
		return err
	}
	if err := createRoleBindingFromFile(t.k8sClient, roleBindingFileNameTelemetry, RoleBindingNameTelemetry, cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role binding %s/%s", cluster.Namespace, RoleBindingNameTelemetry)
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoRolesAndRoleBindings(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteRoleBinding(t.k8sClient, RoleBindingNameTelemetry, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRole(t.k8sClient, RoleNameTelemetry, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) reconcileCCMGoTelemetryRegistration(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Create cm for register
	if err := t.createCCMGoConfigMapRegister(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s from %s", ConfigMapNameTelemetryRegister, configFileNameTelemetryRegister)
		return err
	}
	// Create cm for register proxy
	if err := t.createCCMGoConfigMapRegisterProxy(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s from %s", ConfigMapNameTelemetryRegisterProxy, configFileNameTelemetryRegisterProxy)
		return err
	}
	// Create deployment for register
	if err := t.createDeploymentTelemetryRegistration(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create deployment %s/%s", cluster.Namespace, DeploymentNameTelemetryRegistration)
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoRegistrationService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryRegister, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryRegisterProxy, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(t.k8sClient, DeploymentNameTelemetryRegistration, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	t.isDeploymentRegistrationServiceCreated = false
	return nil
}

func (t *telemetry) reconcileCCMGoTelemetryPhonehome(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	restart bool,
) error {
	// Delete unused old components from ccm java
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryCCMProxyConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	// Create cm for phonehome proxy
	if err := t.createCCMGoConfigMapTelemetryPhonehomeProxy(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s from %s", ConfigMapNameTelemetryPhonehomeProxy, configFileNameTelemetryPhonehomeProxy)
		return err
	}
	// Create daemonset for phonehome
	if err := t.createDaemonSetTelemetryPhonehome(cluster, ownerRef, restart); err != nil {
		logrus.WithError(err).Errorf("failed to create daemonset %s/%s", cluster.Namespace, DaemonSetNameTelemetryPhonehome)
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoPhonehomeCluster(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryPhonehome, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDaemonSet(t.k8sClient, DaemonSetNameTelemetryPhonehome, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

// PWX-27401 only reconcile metrics collector if it's already running
func (t *telemetry) shouldReconcileMetricsCollector(
	cluster *corev1.StorageCluster,
) error {
	// reconcile metrics collector by default, keep the code in case we need to disable it again
	if t.reconcileMetricsCollector == nil {
		t.reconcileMetricsCollector = boolPtr(true)
		return nil
	} else if t.reconcileMetricsCollector != nil {
		return nil
	}
	// Check if collector V1 or V2 deployment exists
	existingDeployment := &appsv1.Deployment{}
	for _, deploymentName := range []string{CollectorDeploymentName, DeploymentNameTelemetryCollectorV2} {
		err := t.k8sClient.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      deploymentName,
				Namespace: cluster.Namespace,
			},
			existingDeployment,
		)
		if err == nil {
			t.reconcileMetricsCollector = boolPtr(true)
			return nil
		} else if !errors.IsNotFound(err) {
			return err
		}
	}
	t.reconcileMetricsCollector = boolPtr(false)
	return nil
}

func (t *telemetry) reconcileCCMGoTelemetryCollectorV2(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if t.reconcileMetricsCollector == nil || !*t.reconcileMetricsCollector {
		return nil
	}

	// Delete metrics collector V1 if exists
	if err := t.deleteMetricsCollectorV1(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	// Deploy metrics collector V2 with all new object names
	if err := t.createCollectorConfigMap(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s", ConfigMapNameTelemetryCollectorV2)
		return err
	}
	if err := t.createCCMGoConfigMapCollectorProxyV2(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s", ConfigMapNameTelemetryCollectorProxyV2)
		return err
	}
	if err := t.createDeploymentTelemetryCollectorV2(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create deployment %s/%s", cluster.Namespace, DeploymentNameTelemetryCollectorV2)
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoMetricsCollectorV2(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryCollectorV2, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(t.k8sClient, DeploymentNameTelemetryCollectorV2, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	t.isCollectorDeploymentCreated = false
	return nil
}

func (t *telemetry) createServiceAccountPxTelemetry(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		t.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ServiceAccountNameTelemetry,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (t *telemetry) createClusterRolePxTelemetry() error {
	return k8sutil.CreateOrUpdateClusterRole(
		t.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: ClusterRoleNameTelemetry,
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

func (t *telemetry) createClusterRoleBindingPxTelemetry(clusterNamespace string) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		t.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: ClusterRoleBindingNameTelemetry,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      ServiceAccountNameTelemetry,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     ClusterRoleNameTelemetry,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (t *telemetry) createCCMGoConfigMapRegister(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	registerCloudSupportPort, _, _ := getCCMCloudSupportPorts(cluster, defaultRegisterPort)
	restCloudSupportPort, _, _ := getCCMCloudSupportPorts(cluster, defaultPhonehomePort)
	config, err := readConfigMapDataFromFile(configFileNameTelemetryRegister, map[string]string{
		configParameterCertSecretNamespace:      cluster.Namespace,
		configParameterRegisterCloudSupportPort: fmt.Sprint(registerCloudSupportPort),
		configParameterRestCloudSupportPort:     fmt.Sprint(restCloudSupportPort),
	})
	if err != nil {
		return err
	}
	_, err = k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameTelemetryRegister,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameTelemetryRegister: config,
			},
		},
		ownerRef,
	)
	return err
}

func (t *telemetry) createCCMGoConfigMapRegisterProxy(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	configFileName := configFileNameTelemetryRegisterProxy
	cloudSupportPort, tcpProxyPort, envoyRedirectPort := getCCMCloudSupportPorts(cluster, defaultRegisterPort)
	replaceMap := map[string]string{
		configParameterApplianceID:              cluster.Status.ClusterUID,
		configParameterComponentSN:              cluster.Name,
		configParameterProductVersion:           pxutil.GetPortworxVersion(cluster).String(),
		configParameterRegisterProxyURL:         getArcusRegisterProxyURL(cluster),
		configParameterRegisterCloudSupportPort: fmt.Sprint(cloudSupportPort),
	}

	_, proxy := pxutil.GetPxProxyEnvVarValue(cluster)
	if proxy != "" && t.usePxProxy {
		host, port, authHeader, err := pxutil.ParsePxProxyURL(proxy)
		if err != nil {
			logrus.Errorf("failed to get custom proxy address and port from proxy %s: %v", proxy, err)
			return k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryRegisterProxy, cluster.Namespace, *ownerRef)
		}
		replaceMap[configParameterCloudSupportTCPProxyPort] = fmt.Sprint(tcpProxyPort)
		replaceMap[configParameterCloudSupportEnvoyInternalRedirectPort] = fmt.Sprint(envoyRedirectPort)
		replaceMap[configParameterCustomProxyAddress] = host
		replaceMap[configParameterCustomProxyPort] = port
		replaceMap[configParameterCustomProxyBasicAuth] = authHeader

		if authHeader == "" {
			configFileName = configFileNameTelemetryRegisterCustomHttpProxy
		} else {
			configFileName = configFileNameTelemetryRegisterCustomHttpsProxy
		}
	}

	config, err := readConfigMapDataFromFile(configFileName, replaceMap)
	if err != nil {
		return err
	}

	_, err = k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameTelemetryRegisterProxy,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameTelemetryRegisterProxy: config,
			},
		},
		ownerRef,
	)
	return err
}

func (t *telemetry) createCCMGoConfigMapTelemetryPhonehomeProxy(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	configFileName := configFileNameTelemetryPhonehomeProxy
	cloudSupportPort, tcpProxyPort, envoyRedirectPort := getCCMCloudSupportPorts(cluster, defaultPhonehomePort)
	replaceMap := map[string]string{
		configParameterApplianceID:          cluster.Status.ClusterUID,
		configParameterProductVersion:       pxutil.GetPortworxVersion(cluster).String(),
		configParameterRestProxyURL:         getArcusRestProxyURL(cluster),
		configParameterRestCloudSupportPort: fmt.Sprint(cloudSupportPort),
	}

	_, proxy := pxutil.GetPxProxyEnvVarValue(cluster)
	if proxy != "" && t.usePxProxy {
		host, port, authHeader, err := pxutil.ParsePxProxyURL(proxy)
		if err != nil {
			logrus.Errorf("failed to get custom proxy address and port from %s: %v", proxy, err)
			return k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryPhonehomeProxy, cluster.Namespace, *ownerRef)
		}

		replaceMap[configParameterCloudSupportTCPProxyPort] = fmt.Sprint(tcpProxyPort)
		replaceMap[configParameterCloudSupportEnvoyInternalRedirectPort] = fmt.Sprint(envoyRedirectPort)
		replaceMap[configParameterCustomProxyAddress] = host
		replaceMap[configParameterCustomProxyPort] = port
		replaceMap[configParameterCustomProxyBasicAuth] = authHeader

		if authHeader == "" {
			configFileName = configFileNameTelemetryPhonehomeCustomHttpProxy
		} else {
			configFileName = configFileNameTelemetryPhonehomeCustomHttpsProxy
		}
	}

	config, err := readConfigMapDataFromFile(configFileName, replaceMap)
	if err != nil {
		return err
	}

	_, err = k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameTelemetryPhonehomeProxy,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameTelemetryPhonehomeProxy: config,
			},
		},
		ownerRef,
	)
	return err
}

func (t *telemetry) createCCMGoConfigMapCollectorProxyV2(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// TODO: share same config template with phonehome, component-sn field is needed here
	configFileName := configFileNameTelemetryCollectorProxy
	cloudSupportPort, tcpProxyPort, envoyRedirectPort := getCCMCloudSupportPorts(cluster, defaultCollectorPort)
	replaceMap := map[string]string{
		configParameterApplianceID:          cluster.Status.ClusterUID,
		configParameterProductVersion:       pxutil.GetPortworxVersion(cluster).String(),
		configParameterRestProxyURL:         getArcusRestProxyURL(cluster),
		configParameterRestCloudSupportPort: fmt.Sprint(cloudSupportPort),
		configParameterComponentSN:          "portworx-metrics-node",
	}

	_, proxy := pxutil.GetPxProxyEnvVarValue(cluster)
	if proxy != "" && t.usePxProxy {
		host, port, authHeader, err := pxutil.ParsePxProxyURL(proxy)
		if err != nil {
			logrus.Errorf("failed to get custom proxy address and port from %s: %v", proxy, err)
			return k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryCollectorProxyV2, cluster.Namespace, *ownerRef)
		}

		replaceMap[configParameterCloudSupportTCPProxyPort] = fmt.Sprint(tcpProxyPort)
		replaceMap[configParameterCloudSupportEnvoyInternalRedirectPort] = fmt.Sprint(envoyRedirectPort)
		replaceMap[configParameterCustomProxyAddress] = host
		replaceMap[configParameterCustomProxyPort] = port
		replaceMap[configParameterCustomProxyBasicAuth] = authHeader

		if authHeader == "" {
			configFileName = configFileNameTelemetryCollectorCustomHttpProxy
		} else {
			configFileName = configFileNameTelemetryCollectorCustomHttpsProxy
		}
	}

	config, err := readConfigMapDataFromFile(configFileName, replaceMap)
	if err != nil {
		return err
	}

	_, err = k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameTelemetryCollectorProxyV2,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				CollectorProxyConfigFileName: config,
			},
		},
		ownerRef,
	)
	return err
}

func (t *telemetry) createCCMGoConfigMapTLSCertificate(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config, err := readConfigMapDataFromFile(configFileNameTelemetryTLSCertificate, nil)
	if err != nil {
		return err
	}
	_, err = k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameTelemetryTLSCertificate,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameTelemetryTLSCertificate: config,
			},
		},
		ownerRef,
	)
	return err
}

func (t *telemetry) createCCMGoConfigMapTelemetryPhonehome(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) (bool, error) {
	cloudSupportPort, _, _ := getCCMCloudSupportPorts(cluster, defaultPhonehomePort)
	config, err := readConfigMapDataFromFile(configFileNameTelemetryPhonehome, map[string]string{
		configParameterPortworxPort:         fmt.Sprint(GetCCMListeningPort(cluster)),
		configParameterRestCloudSupportPort: fmt.Sprint(cloudSupportPort),
	})
	if err != nil {
		return false, err
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ConfigMapNameTelemetryPhonehome,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string]string{
				configFileNameTelemetryPhonehome: config,
				TelemetryArcusLocationFilename:   getArcusTelemetryLocation(cluster),
			},
		},
		ownerRef,
	)
}

func (t *telemetry) createDeploymentTelemetryRegistration(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	deployment, err := k8sutil.GetDeploymentFromFile(deploymentFileNameTelemetryRegistration, pxutil.SpecsBaseDir())
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
	for i := 0; i < len(deployment.Spec.Template.Spec.Containers); i++ {
		container := &deployment.Spec.Template.Spec.Containers[i]
		if container.Name == containerNameTelemetryRegistration {
			container.Image = telemetryImage
		} else if container.Name == containerNameTelemetryProxy {
			container.Image = proxyImage
		}
	}
	deployment.Name = DeploymentNameTelemetryRegistration
	deployment.Namespace = cluster.Namespace
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	deployment.Spec.Template.Spec.ServiceAccountName = ServiceAccountNameTelemetry
	deployment.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)
	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &deployment.Spec.Template.Spec)

	existingDeployment := &appsv1.Deployment{}
	if err := k8sutil.GetDeployment(t.k8sClient, deployment.Name, deployment.Namespace, existingDeployment); err != nil {
		return err
	}

	equal, _ := util.DeepEqualPodTemplate(&deployment.Spec.Template, &existingDeployment.Spec.Template)
	if !t.isDeploymentRegistrationServiceCreated || !equal {
		logrus.WithFields(logrus.Fields{
			"isCreated": t.isDeploymentRegistrationServiceCreated,
			"equal":     equal,
		}).Infof("will create/update the deployment %s/%s", deployment.Namespace, deployment.Name)
		if err := k8sutil.CreateOrUpdateDeployment(t.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}

	t.isDeploymentRegistrationServiceCreated = true
	return nil
}

func (t *telemetry) createDaemonSetTelemetryPhonehome(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	restart bool,
) error {
	daemonset, err := k8sutil.GetDaemonSetFromFile(daemonsetFileNameTelemetryPhonehome, pxutil.SpecsBaseDir())
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
	for i := 0; i < len(daemonset.Spec.Template.Spec.Containers); i++ {
		container := &daemonset.Spec.Template.Spec.Containers[i]
		if container.Name == containerNameLogUploader {
			container.Image = logUploaderImage
			for j := 0; j < len(container.Ports); j++ {
				port := &container.Ports[j]
				if port.Name == portNameLogUploaderContainer {
					port.HostPort = int32(GetCCMListeningPort(cluster))
					port.ContainerPort = int32(GetCCMListeningPort(cluster))
				}
			}
		} else if container.Name == containerNameTelemetryProxy {
			container.Image = proxyImage
			for j := 0; j < len(container.Ports); j++ {
				port := &container.Ports[j]
				cloudSupportPort, _, _ := getCCMCloudSupportPorts(cluster, defaultPhonehomePort)
				if port.Name == portNameEnvoy {
					port.HostPort = int32(cloudSupportPort)
					port.ContainerPort = int32(cloudSupportPort)
				}
			}
		}
	}
	for i := 0; i < len(daemonset.Spec.Template.Spec.InitContainers); i++ {
		container := &daemonset.Spec.Template.Spec.InitContainers[i]
		if container.Name == "init-cont" {
			container.Image = proxyImage
			break
		}
	}

	if preflight.IsPKS() { // Volumes hostPath locations change for PKS
		for _, v := range daemonset.Spec.Template.Spec.Volumes {
			if v.Name == "etcpwx" {
				v.VolumeSource.HostPath.Path = "/var/vcap/store/etc/pwx"
			} else if v.Name == "varcores" {
				v.VolumeSource.HostPath.Path = "/var/vcap/store/cores"
			}
		}
	}

	daemonset.Name = DaemonSetNameTelemetryPhonehome
	daemonset.Namespace = cluster.Namespace
	daemonset.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	daemonset.Spec.Template.Spec.ServiceAccountName = ServiceAccountNameTelemetry
	daemonset.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(daemonset.Spec.Template.ObjectMeta)
	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &daemonset.Spec.Template.Spec)

	if restart {
		t.isDaemonSetTelemetryPhonehomeCreated = false
		logrus.Infof("will restart daemonset %s/%s to load latest configs", daemonset.Namespace, daemonset.Name)
		if err := k8sutil.DeleteDaemonSet(t.k8sClient, DaemonSetNameTelemetryPhonehome, cluster.Namespace, *ownerRef); err != nil {
			return err
		}
	}

	existingDaemonSet := &appsv1.DaemonSet{}
	if err := k8sutil.GetDaemonSet(t.k8sClient, daemonset.Name, daemonset.Namespace, existingDaemonSet); err != nil {
		return err
	}

	equal, _ := util.DeepEqualPodTemplate(&daemonset.Spec.Template, &existingDaemonSet.Spec.Template)
	if !t.isDaemonSetTelemetryPhonehomeCreated || !equal {
		logrus.WithFields(logrus.Fields{
			"isCreated": t.isDaemonSetTelemetryPhonehomeCreated,
			"equal":     equal,
		}).Infof("will create/update the daemonset %s/%s", daemonset.Namespace, daemonset.Name)
		if err := k8sutil.CreateOrUpdateDaemonSet(t.k8sClient, daemonset, ownerRef); err != nil {
			return err
		}
	}

	t.isDaemonSetTelemetryPhonehomeCreated = true
	return nil
}

func (t *telemetry) createDeploymentTelemetryCollectorV2(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	deployment, err := k8sutil.GetDeploymentFromFile(deploymentFileNameTelemetryCollectorV2, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	collectorImage, err := getDesiredCollectorImage(cluster)
	if err != nil {
		return err
	}
	proxyImage, err := getDesiredProxyImage(cluster)
	if err != nil {
		return err
	}
	for i := 0; i < len(deployment.Spec.Template.Spec.Containers); i++ {
		container := &deployment.Spec.Template.Spec.Containers[i]
		if container.Name == containerNameTelemetryCollector {
			container.Image = collectorImage
		} else if container.Name == containerNameTelemetryProxy {
			container.Image = proxyImage
		}
	}
	for i := 0; i < len(deployment.Spec.Template.Spec.InitContainers); i++ {
		container := &deployment.Spec.Template.Spec.InitContainers[i]
		if container.Name == "init-cont" {
			container.Image = proxyImage
			break
		}
	}
	deployment.Name = DeploymentNameTelemetryCollectorV2
	deployment.Namespace = cluster.Namespace
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	deployment.Spec.Template.Spec.ServiceAccountName = ServiceAccountNameTelemetry
	deployment.Spec.Template.ObjectMeta = k8sutil.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)
	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &deployment.Spec.Template.Spec)

	existingDeployment := &appsv1.Deployment{}
	if err := k8sutil.GetDeployment(t.k8sClient, deployment.Name, deployment.Namespace, existingDeployment); err != nil {
		return err
	}

	equal, _ := util.DeepEqualPodTemplate(&deployment.Spec.Template, &existingDeployment.Spec.Template)
	if !t.isCollectorDeploymentCreated || !equal {
		logrus.WithFields(logrus.Fields{
			"isCreated": t.isCollectorDeploymentCreated,
			"equal":     equal,
		}).Infof("will create/update the deployment %s/%s", deployment.Namespace, deployment.Name)
		if err := k8sutil.CreateOrUpdateDeployment(t.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}

	t.isCollectorDeploymentCreated = true
	return nil
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

// CanAccessArcusRegisterEndpoint checks if telemetry registration endpoint is reachable
// return true immediately if it can reach to Arcus
// return false after failing 5 times in a row
func CanAccessArcusRegisterEndpoint(
	cluster *corev1.StorageCluster,
	proxy string,
) bool {
	endpoint := getArcusRegisterProxyURL(cluster)
	logrus.Debugf("checking whether telemetry registration endpoint %s is accessible on cluster %s",
		endpoint, cluster.Name)

	url, _ := url.Parse(fmt.Sprintf("https://%s:443/auth/1.0/ping", endpoint))
	request := &http.Request{
		Method: "GET",
		URL:    url,
		Header: map[string][]string{
			// 3 headers are required here by the API, but can use dummy values here.
			// cluster UUID can be empty, so not using it for appliance-id here.
			"product-name": {"portworx"},
			"appliance-id": {"portworx"},
			"component-sn": {cluster.Name},
		},
	}
	client := &http.Client{}
	if proxy != "" {
		if !strings.HasPrefix(strings.ToLower(proxy), "http://") {
			proxy = "http://" + proxy
		}
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			logrus.WithError(err).Errorf("failed to parse http proxy %s for checking Pure1 connectivity", proxy)
			return false
		}
		client.Transport = &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &cryptoTls.Config{InsecureSkipVerify: true},
		}
	}
	for i := 1; i <= arcusPingRetry; i++ {
		response, err := client.Do(request)
		warnMsg := fmt.Sprintf("failed to access telemetry registration endpoint %s", endpoint)
		if err != nil {
			logrus.WithError(err).Warnf(warnMsg)
		} else if response.StatusCode != 200 {
			// Only consider 200 as a successful ping with a properly constructed request.
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			logrus.WithFields(logrus.Fields{
				"code": response.StatusCode,
				"body": string(body),
			}).Warnf(warnMsg)
		} else {
			logrus.Infof("telemetry registration endpoint %s is accessible on cluster %s",
				endpoint, cluster.Name)
			return true
		}
		if i != arcusPingRetry {
			logrus.Debugf("failed to ping Pure1, retrying...")
			time.Sleep(arcusPingInterval)
		}
	}
	return false
}

// GetDesiredTelemetryImage returns desired telemetry container image
func GetDesiredTelemetryImage(cluster *corev1.StorageCluster) (string, error) {
	if cluster.Spec.Monitoring.Telemetry.Image != "" {
		return util.GetImageURN(cluster, cluster.Spec.Monitoring.Telemetry.Image), nil
	}

	if cluster.Status.DesiredImages != nil {
		return util.GetImageURN(cluster, cluster.Status.DesiredImages.Telemetry), nil
	}

	return "", fmt.Errorf("telemetry image is empty")
}

func getDesiredLogUploaderImage(cluster *corev1.StorageCluster) (string, error) {
	if cluster.Spec.Monitoring.Telemetry.LogUploaderImage != "" {
		return util.GetImageURN(cluster, cluster.Spec.Monitoring.Telemetry.LogUploaderImage), nil
	}

	if cluster.Status.DesiredImages != nil {
		return util.GetImageURN(cluster, cluster.Status.DesiredImages.LogUploader), nil
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
			Name:      ServiceAccountNameTelemetry,
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
	fileBytes, err := os.ReadFile(filepath)
	if err != nil {
		return "", err
	}
	data := string(fileBytes)
	for k, v := range replace {
		data = strings.ReplaceAll(data, k, v)
	}

	return data, nil
}

func GetCCMListeningPort(cluster *corev1.StorageCluster) int {
	defCCMPort := defaultCCMListeningPort

	pxVer2138, _ := version.NewVersion("2.13.8")
	if pxutil.GetPortworxVersion(cluster).GreaterThanOrEqual(pxVer2138) {
		defCCMPort = defaultCCMListeningPortForPXge2138
	}

	startPort := pxutil.StartPort(cluster)
	if startPort == pxutil.DefaultStartPort {
		return defCCMPort
	}
	return startPort + 20
}

func getCCMCloudSupportPorts(cluster *corev1.StorageCluster, port int) (int, int, int) {
	offset := pxutil.StartPort(cluster) - pxutil.DefaultStartPort
	cloudSupportPort := port + offset
	tcpProxyPort := cloudSupportPort + 2
	envoyRedirectPort := cloudSupportPort + 4
	return cloudSupportPort, tcpProxyPort, envoyRedirectPort
}

func init() {
	RegisterTelemetryComponent()
}
