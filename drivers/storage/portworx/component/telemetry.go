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
	// ServiceAccountNamePxTelemetry is name of service account px-telemetry
	ServiceAccountNamePxTelemetry = "px-telemetry"
	// ClusterRoleNamePxTelemetry is name of cluster role px-telemetry
	ClusterRoleNamePxTelemetry = "px-telemetry"
	// ClusterRoleBindingNamePxTelemetry is name of cluster role binding px-telemetry
	ClusterRoleBindingNamePxTelemetry = "px-telemetry"

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
)

type telemetry struct {
	k8sClient                              client.Client
	sdkConn                                *grpc.ClientConn
	isCCMGoSupported                       bool
	isCollectorDeploymentCreated           bool
	isDeploymentRegistrationServiceCreated bool
	isDeploymentPhonehonmeClusterCreated   bool
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
	t.isDeploymentPhonehonmeClusterCreated = false
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
	if cluster.Status.ClusterUID == "" {
		logrus.Warn("clusterUID is empty, wait for it to reconcile telemetry components")
		return nil
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
	if err := t.reconcileCCMGoServiceAccount(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMGoClusterRolesAndClusterRoleBindings(cluster); err != nil {
		return err
	}
	if err := t.reconcileCCMGoRolesAndRoleBindings(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMGoRegistrationService(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMGoPhonehomeCluster(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.reconcileCCMGoMetricsCollectorV2(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGo(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteServiceAccount(t.k8sClient, ServiceAccountNamePxTelemetry, cluster.Namespace, *ownerRef); err != nil {
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
	// Delete service account used by metrics collector v1
	if err := k8sutil.DeleteServiceAccount(t.k8sClient, CollectorServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
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
	// Delete cluster role and cluster role binding used by metrics collector v1
	if err := k8sutil.DeleteClusterRole(t.k8sClient, CollectorClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(t.k8sClient, CollectorClusterRoleBindingName); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoClusterRoleAndClusterRoleBinding() error {
	if err := k8sutil.DeleteClusterRole(t.k8sClient, ClusterRoleNamePxTelemetry); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(t.k8sClient, ClusterRoleBindingNamePxTelemetry); err != nil {
		return err
	}
	return nil
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
	// Create metrics collector role and role binding
	if err := t.createCollectorRole(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role %s/%s", cluster.Namespace, CollectorRoleName)
		return err
	}
	if err := t.createCollectorRoleBinding(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create role binding %s/%s", cluster.Namespace, CollectorRoleBindingName)
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoRolesAndRoleBindings(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
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
	if err := k8sutil.DeleteRole(t.k8sClient, CollectorRoleName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteRoleBinding(t.k8sClient, CollectorRoleBindingName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) reconcileCCMGoRegistrationService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Delete unused old components from ccm java
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryCCMProxyConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	// Create cm registration-config from config_properties_px.yaml
	if err := t.createCCMGoConfigMapRegistrationConfig(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to create cm registration-config from config_properties_px.yaml ")
		return err
	}
	// Create cm proxy-config-register from envoy-config-register.yaml
	if err := t.createCCMGoConfigMapProxyConfigRegister(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to create cm proxy-config-register from envoy-config-register.yaml")
		return err
	}
	// Create deployment registration-service
	if err := t.createDeploymentRegistrationService(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create deployment %s/%s", cluster.Namespace, DeploymentNameRegistrationService)
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoRegistrationService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameRegistrationConfig, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameProxyConfigRegister, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(t.k8sClient, DeploymentNameRegistrationService, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	t.isDeploymentRegistrationServiceCreated = false
	return nil
}

func (t *telemetry) reconcileCCMGoPhonehomeCluster(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// Reconcile config maps for log uploader
	// Creat cm proxy-config-rest from envoy-config-rest.yaml
	if err := t.createCCMGoConfigMapProxyConfigRest(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to creat cm proxy-config-rest from envoy-config-rest.yaml")
		return err
	}
	// Create cm tls-certificate from tls_certificate_sds_secret.yaml
	if err := t.createCCMGoConfigMapTLSCertificate(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to creat cm proxy-config-rest from envoy-config-rest.yaml")
		return err
	}
	// Create cm px-telemetry-config from ccm.properties and location
	// CCM Java creates this cm as well, let's delete it and create a new one
	if err := t.createCCMGoConfigMapPXTelemetryConfig(cluster, ownerRef); err != nil {
		logrus.WithError(err).Error("failed to create cm px-telemetry-config from ccm.properties and location")
		return err
	}
	// create deployment phonehone-cluster
	if err := t.createDeploymentPhonehomeCluster(cluster, ownerRef); err != nil {
		logrus.WithError(err).Errorf("failed to create deployment %s/%s", cluster.Namespace, DeploymentNamePhonehomeCluster)
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoPhonehomeCluster(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteDeployment(t.k8sClient, DeploymentNamePhonehomeCluster, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameProxyConfigRest, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNameTLSCertificate, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, ConfigMapNamePxTelemetryConfig, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) reconcileCCMGoMetricsCollectorV2(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := t.createCollectorProxyConfigMap(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.createCollectorConfigMap(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.createCollectorDeployment(cluster, ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) deleteCCMGoMetricsCollectorV2(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	if err := k8sutil.DeleteConfigMap(t.k8sClient, CollectorProxyConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteConfigMap(t.k8sClient, CollectorConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(t.k8sClient, CollectorDeploymentName, cluster.Namespace, *ownerRef); err != nil {
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
				Name:            ServiceAccountNamePxTelemetry,
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
				Name: ClusterRoleNamePxTelemetry,
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
				Name: ClusterRoleBindingNamePxTelemetry,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      ServiceAccountNamePxTelemetry,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     ClusterRoleNamePxTelemetry,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
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
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	deployment.Spec.Template.Spec.Containers = containers
	deployment.Spec.Template.Spec.ServiceAccountName = ServiceAccountNamePxTelemetry
	pxutil.ApplyStorageClusterSettings(cluster, deployment)

	existingDeployment, err := k8sutil.GetDeployment(t.k8sClient, deployment)
	if err != nil {
		return err
	}

	equal, _ := util.DeepEqualDeployment(deployment, existingDeployment)
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
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
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
	deployment.Spec.Template.Spec.ServiceAccountName = ServiceAccountNamePxTelemetry
	pxutil.ApplyStorageClusterSettings(cluster, deployment)

	existingDeployment, err := k8sutil.GetDeployment(t.k8sClient, deployment)
	if err != nil {
		return err
	}

	equal, _ := util.DeepEqualDeployment(deployment, existingDeployment)
	if !t.isDeploymentPhonehonmeClusterCreated || !equal {
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
			Name:      ServiceAccountNamePxTelemetry,
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
