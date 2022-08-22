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

	roleFileNameTelemetry                        = "px-telemetry-role.yaml"
	roleBindingFileNameTelemetry                 = "px-telemetry-role-binding.yaml"
	configFileNameTelemetryRegister              = "config_properties_px.yaml"
	configFileNameTelemetryRegisterProxy         = "envoy-config-register.yaml"
	configFileNameTelemetryRegisterCustomerProxy = "envoy-config-register-customer-proxy.yaml"
	configFileNameTelemetryPhonehome             = "ccm.properties"
	configFileNameTelemetryPhonehomeProxy        = "envoy-config-rest.yaml"
	configFileNameTelemetryTLSCertificate        = "tls_certificate_sds_secret.yaml"
	deploymentFileNameTelemetryRegistration      = "registration-service.yaml"
	daemonsetFileNameTelemetryPhonehome          = "phonehome-cluster.yaml"

	configParameterApplianceID         = "APPLIANCE_ID"
	configParameterComponentSN         = "COMPONENT_SN"
	configParameterProductVersion      = "PRODUCT_VERSION"
	configParameterRegisterProxyURL    = "REGISTER_PROXY_URL"
	configParameterRestProxyURL        = "REST_PROXY_URL"
	configParameterCertSecretNamespace = "CERT_SECRET_NAMESPACE"
	configParameterCustomProxyPort     = "CUSTOM_PROXY_PORT"
	configParameterPortworxPort        = "PORTWORX_PORT"
	containerNameTelemetryRegistration = "registration"
	containerNameLogUploader           = "log-upload-service"
	containerNameTelemetryProxy        = "envoy"
	portNameLogUploaderContainer       = "loguploader"

	productionArcusLocation         = "external"
	productionArcusRestProxyURL     = "rest.cloud-support.purestorage.com"
	productionArcusRegisterProxyURL = "register.cloud-support.purestorage.com"
	stagingArcusLocation            = "internal"
	stagingArcusRestProxyURL        = "rest.staging-cloud-support.purestorage.com"
	stagingArcusRegisterProxyURL    = "register.staging-cloud-support.purestorage.com"

	// port for log uploader, avoid 1970 as used by old ccm service, to be configurable in the future
	logUploaderPort = 9090
)

type telemetry struct {
	k8sClient                              client.Client
	sdkConn                                *grpc.ClientConn
	isCCMGoSupported                       bool
	isCollectorDeploymentCreated           bool
	isDeploymentRegistrationServiceCreated bool
	isDaemonSetTelemetryPhonehonmeCreated  bool
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
	t.isDeploymentRegistrationServiceCreated = false
	t.isDaemonSetTelemetryPhonehonmeCreated = false
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
	if err := t.reconcileCCMGoTelemetryPhonehome(cluster, ownerRef); err != nil {
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
) error {
	// Delete unused old components from ccm java
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryCCMProxyConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	// Reconcile config maps for log uploader
	// Create cm for phonehome
	if err := t.createCCMGoConfigMapTelemetryPhonehome(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s from %s", ConfigMapNameTelemetryPhonehome, configFileNameTelemetryPhonehome)
		return err
	}
	// Creat cm for phonehome proxy
	if err := t.createCCMGoConfigMapTelemetryPhonehomeProxy(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s from %s", ConfigMapNameTelemetryPhonehomeProxy, configFileNameTelemetryPhonehomeProxy)
		return err
	}
	// Create cm for TLS certificate
	if err := t.createCCMGoConfigMapTLSCertificate(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s from %s", ConfigMapNameTelemetryTLSCertificate, configFileNameTelemetryTLSCertificate)
		return err
	}
	// create daemonset for phonehome
	if err := t.createDaemonSetTelemetryPhonehome(cluster, ownerRef); err != nil {
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
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTelemetryTLSCertificate, cluster.Namespace, *ownerRef); err != nil {
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

func (t *telemetry) reconcileCCMGoTelemetryCollectorV2(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Delete metrics collector V1 if exists
	if err := t.deleteMetricsCollectorV1(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	// Deploy metrics collector V2 with all new object names
	if err := t.createCollectorConfigMap(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s", ConfigMapNameTelemetryCollectorV2)
		return err
	}
	if err := t.createCollectorProxyConfigMap(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create cm %s", ConfigMapNameTelemetryCollectorProxyV2)
		return err
	}
	if err := t.createCollectorDeployment(cluster, ownerRef); err != nil {
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
		logrus.Infof("telemetry cert %s/%s not found", cluster.Namespace, TelemetryCertName)
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

// create cm registration-config from config_properties_px.yaml
func (t *telemetry) createCCMGoConfigMapRegister(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config, err := readConfigMapDataFromFile(configFileNameTelemetryRegister, map[string]string{
		configParameterCertSecretNamespace: cluster.Namespace,
	})
	if err != nil {
		return err
	}
	return k8sutil.CreateOrUpdateConfigMap(
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
}

// create cm proxy-config-register from envoy-config-register.yaml
func (t *telemetry) createCCMGoConfigMapRegisterProxy(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	configFileName := configFileNameTelemetryRegisterProxy
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
}

// creat cm proxy-config-rest from envoy-config-rest.yaml
func (t *telemetry) createCCMGoConfigMapTelemetryPhonehomeProxy(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	configFileName := configFileNameTelemetryPhonehomeProxy
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
}

// create cm tls-certificate from tls_certificate_sds_secret.yaml
func (t *telemetry) createCCMGoConfigMapTLSCertificate(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config, err := readConfigMapDataFromFile(configFileNameTelemetryTLSCertificate, nil)
	if err != nil {
		return err
	}
	return k8sutil.CreateOrUpdateConfigMap(
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
}

// create cm px-telemetry-config from ccm.properties and location
func (t *telemetry) createCCMGoConfigMapTelemetryPhonehome(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config, err := readConfigMapDataFromFile(configFileNameTelemetryPhonehome, map[string]string{
		configParameterPortworxPort: fmt.Sprintf(`"%v"`, logUploaderPort),
	})
	if err != nil {
		return err
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
					port.HostPort = logUploaderPort
					port.ContainerPort = logUploaderPort
				}
			}
		} else if container.Name == containerNameTelemetryProxy {
			container.Image = proxyImage
		}
	}
	for i := 0; i < len(daemonset.Spec.Template.Spec.InitContainers); i++ {
		container := &daemonset.Spec.Template.Spec.InitContainers[i]
		if container.Name == "init-cont" {
			container.Image = proxyImage
			break
		}
	}
	daemonset.Name = DaemonSetNameTelemetryPhonehome
	daemonset.Namespace = cluster.Namespace
	daemonset.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	daemonset.Spec.Template.Spec.ServiceAccountName = ServiceAccountNameTelemetry
	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &daemonset.Spec.Template.Spec)

	existingDaemonSet := &appsv1.DaemonSet{}
	if err := k8sutil.GetDaemonSet(t.k8sClient, daemonset.Name, daemonset.Namespace, existingDaemonSet); err != nil {
		return err
	}

	equal, _ := util.DeepEqualPodTemplate(&daemonset.Spec.Template, &existingDaemonSet.Spec.Template)
	if !t.isDaemonSetTelemetryPhonehonmeCreated || !equal {
		logrus.WithFields(logrus.Fields{
			"isCreated": t.isDaemonSetTelemetryPhonehonmeCreated,
			"equal":     equal,
		}).Infof("will create/update the daemonset %s/%s", daemonset.Namespace, daemonset.Name)
		if err := k8sutil.CreateOrUpdateDaemonSet(t.k8sClient, daemonset, ownerRef); err != nil {
			return err
		}
	}

	t.isDaemonSetTelemetryPhonehonmeCreated = true
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
