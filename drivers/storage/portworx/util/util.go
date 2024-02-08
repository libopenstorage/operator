package util

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/shlex"
	"github.com/hashicorp/go-version"
	ocpconfig "github.com/openshift/api/config/v1"
	routev1 "github.com/openshift/api/route/v1"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/openstorage/pkg/auth"
	"github.com/libopenstorage/openstorage/pkg/grpcserver"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// DriverName name of the portworx driver
	DriverName = "portworx"
	// PortworxComponentName name of portworx component to show in the cluster conditions
	PortworxComponentName = "Portworx"
	// DefaultStartPort is the default start port for Portworx
	DefaultStartPort = 9001
	// DefaultOpenshiftStartPort is the default start port for Portworx on OpenShift
	DefaultOpenshiftStartPort = 17001
	// PortworxSpecsDir is the directory where all the Portworx specs are stored
	PortworxSpecsDir = "/configs"

	// DefaultPortworxServiceAccountName default name of the Portworx service account
	DefaultPortworxServiceAccountName = "portworx"
	// PortworxServiceName name of the Portworx Kubernetes service
	PortworxServiceName = "portworx-service"
	// PortworxKVDBServiceName name of the Portworx KVDB Kubernetes service
	PortworxKVDBServiceName = "portworx-kvdb-service"
	// CSIRegistrarContainerName name of the Portworx CSI node driver registrar container
	CSIRegistrarContainerName = "csi-node-driver-registrar"
	// TelemetryContainerName name of the Portworx telemetry container
	TelemetryContainerName = "telemetry"
	// PortworxRESTPortName name of the Portworx API port
	PortworxRESTPortName = "px-api"
	// PortworxSDKPortName name of the Portworx SDK port
	PortworxSDKPortName = "px-sdk"
	// PortworxKVDBPortName name of the Portworx internal KVDB port
	PortworxKVDBPortName = "px-kvdb"
	// EssentialsSecretName name of the Portworx Essentials secret
	EssentialsSecretName = "px-essential"
	// EssentialsUserIDKey is the secret key for Essentials user ID
	EssentialsUserIDKey = "px-essen-user-id"
	// EssentialsOSBEndpointKey is the secret key for Essentials OSB endpoint
	EssentialsOSBEndpointKey = "px-osb-endpoint"
	// EnvKeyKubeletDir env var to set custom kubelet directory
	EnvKeyKubeletDir = "KUBELET_DIR"

	// AnnotationIsPKS annotation indicating whether it is a PKS cluster
	AnnotationIsPKS = pxAnnotationPrefix + "/is-pks"
	// AnnotationIsGKE annotation indicating whether it is a GKE cluster
	AnnotationIsGKE = pxAnnotationPrefix + "/is-gke"
	// AnnotationIsOKE annotation indicating whether it is a OKE cluster
	AnnotationIsOKE = pxAnnotationPrefix + "/is-oke"
	// AnnotationIsAKS annotation indicating whether it is an AKS cluster
	AnnotationIsAKS = pxAnnotationPrefix + "/is-aks"
	// AnnotationIsEKS annotation indicating whether it is an EKS cluster
	AnnotationIsEKS = pxAnnotationPrefix + "/is-eks"
	// AnnotationIsIKS annotation indicating whether it is an IKS cluster
	AnnotationIsIKS = pxAnnotationPrefix + "/is-iks"
	// AnnotationIsOpenshift annotation indicating whether it is an OpenShift cluster
	AnnotationIsOpenshift = pxAnnotationPrefix + "/is-openshift"
	// AnnotationPVCController annotation indicating whether to deploy a PVC controller
	AnnotationPVCController = pxAnnotationPrefix + "/pvc-controller"
	// AnnotationPVCControllerCPU annotation for overriding the default CPU for PVC
	// controller deployment
	AnnotationPVCControllerCPU = pxAnnotationPrefix + "/pvc-controller-cpu"
	// AnnotationPVCControllerPort annotation for overriding the default port for PVC
	// controller deployment
	AnnotationPVCControllerPort = pxAnnotationPrefix + "/pvc-controller-port"
	// AnnotationPVCControllerSecurePort annotation for overriding the default secure
	// port for PVC controller deployment
	AnnotationPVCControllerSecurePort = pxAnnotationPrefix + "/pvc-controller-secure-port"
	// AnnotationAutopilotCPU annotation for overriding the default CPU for Autopilot
	AnnotationAutopilotCPU = pxAnnotationPrefix + "/autopilot-cpu"
	// AnnotationServiceType annotation indicating k8s service type for all services
	// deployed by the operator
	AnnotationServiceType = pxAnnotationPrefix + "/service-type"
	// AnnotationLogFile annotation to specify the log file path where portworx
	// logs need to be redirected
	AnnotationLogFile = pxAnnotationPrefix + "/log-file"
	// AnnotationMiscArgs annotation to specify miscellaneous arguments that will
	// be passed to portworx container directly without any interpretation
	AnnotationMiscArgs = pxAnnotationPrefix + "/misc-args"
	// AnnotationPXVersion annotation indicating the portworx semantic version
	AnnotationPXVersion = pxAnnotationPrefix + "/px-version"
	// AnnotationStorkVersion annotation indicating the stork semantic version
	AnnotationStorkVersion = pxAnnotationPrefix + "/stork-version"
	// AnnotationDisableStorageClass annotation to disable installing default portworx
	// storage classes
	AnnotationDisableStorageClass = pxAnnotationPrefix + "/disable-storage-class"
	// AnnotationRunOnMaster annotation to enable running Portworx on master nodes
	AnnotationRunOnMaster = pxAnnotationPrefix + "/run-on-master"
	// AnnotationPodDisruptionBudget annotation indicating whether to create pod disruption budgets
	AnnotationPodDisruptionBudget = pxAnnotationPrefix + "/pod-disruption-budget"
	// AnnotationStoragePodDisruptionBudget annotation to specify the min available value of the px-storage
	// pod disruption budget
	AnnotationStoragePodDisruptionBudget = pxAnnotationPrefix + "/storage-pdb-min-available"
	// AnnotationPodSecurityPolicy annotation indicating whether to enable creation
	// of pod security policies
	AnnotationPodSecurityPolicy = pxAnnotationPrefix + "/pod-security-policy"
	// AnnotationPortworxProxy annotation indicating whether to enable creation of
	// portworx proxy for Portworx in-tree driver
	AnnotationPortworxProxy = pxAnnotationPrefix + "/portworx-proxy"
	// AnnotationTelemetryArcusLocation annotation indicates the location (internal/external) of Arcus
	// that CCM should use
	AnnotationTelemetryArcusLocation = pxAnnotationPrefix + "/arcus-location"
	// AnnotationHostPid configures hostPid flag for portworx pod.
	AnnotationHostPid = pxAnnotationPrefix + "/host-pid"
	// AnnotationDNSPolicy configures dns policy for portworx pod.
	AnnotationDNSPolicy = pxAnnotationPrefix + "/dns-policy"
	// AnnotationClusterID overwrites portworx cluster ID, which is the storage cluster name by default
	AnnotationClusterID = pxAnnotationPrefix + "/cluster-id"
	// AnnotationDisableCSRAutoApprove annotation to set priority for SCCs.
	AnnotationSCCPriority = pxAnnotationPrefix + "/scc-priority"
	// AnnotationPreflightCheck do preflight check before installing Portworx
	AnnotationPreflightCheck = pxAnnotationPrefix + "/preflight-check"
	// AnnotationFACDTopology is added when FACD topology was successfully installed on a *new* cluster (it's blocked for existing clusters)
	AnnotationFACDTopology = pxAnnotationPrefix + "/facd-topology"
	// AnnotationIsPrivileged [=false] used to remove privileged containers requirement
	AnnotationIsPrivileged = pxAnnotationPrefix + "/privileged"
	// AnnotationAppArmorPrefix controls which AppArmor profile will be used per container.
	AnnotationAppArmorPrefix = "container.apparmor.security.beta.kubernetes.io/"
	// AnnotationServerTLSMinVersion sets up TLS-servers w/ requested TLS as minimal version
	AnnotationServerTLSMinVersion = pxAnnotationPrefix + "/tls-min-version"
	// AnnotationServerTLSCipherSuites sets up TLS-servers w/ requested cipher suites
	AnnotationServerTLSCipherSuites = pxAnnotationPrefix + "/tls-cipher-suites"

	// EnvKeyPXImage key for the environment variable that specifies Portworx image
	EnvKeyPXImage = "PX_IMAGE"
	// EnvKeyPXReleaseManifestURL key for the environment variable that specifies release manifest
	EnvKeyPXReleaseManifestURL = "PX_RELEASE_MANIFEST_URL"
	// EnvKeyPortworxNamespace key for the env var which tells namespace in which
	// Portworx is installed
	EnvKeyPortworxNamespace = "PX_NAMESPACE"
	// EnvKeyPortworxServiceAccount key for the env var which tells custom Portworx
	// service account
	EnvKeyPortworxServiceAccount = "PX_SERVICE_ACCOUNT"
	// EnvKeyPortworxServiceName key for the env var which tells the name of the
	// portworx service to be used
	EnvKeyPortworxServiceName = "PX_SERVICE_NAME"
	// EnvKeyPortworxSecretsNamespace key for the env var which tells the namespace
	// where portworx should look for secrets
	EnvKeyPortworxSecretsNamespace = "PX_SECRETS_NAMESPACE"
	// EnvKeyDeprecatedCSIDriverName key for the env var that can force Portworx
	// to use the deprecated CSI driver name
	EnvKeyDeprecatedCSIDriverName = "PORTWORX_USEDEPRECATED_CSIDRIVERNAME"
	// EnvKeyDisableCSIAlpha key for the env var that is used to disable CSI
	// alpha features
	EnvKeyDisableCSIAlpha = "PORTWORX_DISABLE_CSI_ALPHA"
	// EnvKeyPortworxEnableTLS is a flag for enabling operator TLS with PX
	EnvKeyPortworxEnableTLS = "PX_ENABLE_TLS"
	// EnvKeyPortworxAuthSystemKey is the environment variable name for the PX security secret
	EnvKeyPortworxAuthSystemKey = "PORTWORX_AUTH_SYSTEM_KEY"
	// EnvKeyPortworxAuthJwtSharedSecret is an environment variable defining the PX Security JWT secret
	EnvKeyPortworxAuthJwtSharedSecret = "PORTWORX_AUTH_JWT_SHAREDSECRET"
	// EnvKeyPortworxAuthJwtIssuer is an environment variable defining the PX Security JWT Issuer
	EnvKeyPortworxAuthJwtIssuer = "PORTWORX_AUTH_JWT_ISSUER"
	// EnvKeyPortworxAuthSystemAppsKey is an environment variable defining the PX Security shared secret for Portworx Apps
	EnvKeyPortworxAuthSystemAppsKey = "PORTWORX_AUTH_SYSTEM_APPS_KEY"
	// EnvKeyPXSharedSecret is an environment variable defining the shared secret
	EnvKeyPXSharedSecret = "PX_SHARED_SECRET"
	// EnvKeyStorkPXJwtIssuer is an environment variable defining the jwt issuer for Stork
	EnvKeyStorkPXJwtIssuer = "PX_JWT_ISSUER"
	// EnvKeyPortworxAuthStorkKey is an environment variable for the auth secret
	// that stork and the operator use to communicate with portworx
	EnvKeyPortworxAuthStorkKey = "PORTWORX_AUTH_STORK_KEY"
	// EnvKeyPortworxEssentials env var to deploy Portworx Essentials cluster
	EnvKeyPortworxEssentials = "PORTWORX_ESSENTIALS"
	// EnvKeyMarketplaceName env var for the name of the source marketplace
	EnvKeyMarketplaceName = "MARKETPLACE_NAME"
	// EnvKeyPortworxHTTPProxy env var to use http proxy
	EnvKeyPortworxHTTPProxy = "PX_HTTP_PROXY"
	// EnvKeyPortworxHTTPSProxy env var to use https proxy
	EnvKeyPortworxHTTPSProxy = "PX_HTTPS_PROXY"

	// SecurityPXSystemSecretsSecretName is the secret name for PX security system secrets
	SecurityPXSystemSecretsSecretName = "px-system-secrets"
	// SecurityPXSharedSecretSecretName is the secret name for the PX Security shared secret
	SecurityPXSharedSecretSecretName = "px-shared-secret"
	// SecuritySharedSecretKey is the key for accessing the jwt shared secret
	SecuritySharedSecretKey = "shared-secret"
	// SecuritySystemSecretKey is the key for accessing the system secret auth key
	SecuritySystemSecretKey = "system-secret"
	// SecurityAppsSecretKey is the secret key for the apps issuer
	SecurityAppsSecretKey = "apps-secret"
	// SecurityAuthTokenKey is the key for accessing a PX auth token in a k8s secret
	SecurityAuthTokenKey = "auth-token"
	// SecurityPXAdminTokenSecretName is the secret name for storing an auto-generated admin token
	SecurityPXAdminTokenSecretName = "px-admin-token"
	// SecurityPXUserTokenSecretName is the secret name for storing an auto-generated user token
	SecurityPXUserTokenSecretName = "px-user-token"
	// SecurityPortworxAppsIssuer is the issuer for portworx apps to communicate with the PX SDK
	SecurityPortworxAppsIssuer = "apps.portworx.io"
	// SecurityPortworxStorkIssuer is the issuer for stork to communicate with the PX SDK pre-2.6
	SecurityPortworxStorkIssuer = "stork.openstorage.io"

	// ErrMsgGrpcConnection error message if failed to connect to GRPC server
	ErrMsgGrpcConnection = "error connecting to GRPC server"
	// ImageNamePause is the container image to use for the pause container
	ImageNamePause = k8sutil.DefaultK8SRegistryPath + "/pause:3.1"

	// userVolumeNamePrefix prefix used for user volume names to avoid name conflicts with existing volumes
	userVolumeNamePrefix = "user-"
	pxAnnotationPrefix   = "portworx.io"
	labelKeyName         = "name"
	defaultSDKPort       = 9020

	// InternalEtcdConfigMapPrefix is prefix of the internal kvdb configmap.
	InternalEtcdConfigMapPrefix = "px-bootstrap-"
	// CloudDriveConfigMapPrefix is prefix of the cloud drive configmap.
	CloudDriveConfigMapPrefix = "px-cloud-drive-"

	// TelemetryCertName is name of the telemetry cert.
	TelemetryCertName = "pure-telemetry-certs"
	// HttpProtocolPrefix is the prefix for HTTP protocol
	HttpProtocolPrefix = "http://"
	// HttpsProtocolPrefix is the prefix for HTTPS protocol
	HttpsProtocolPrefix = "https://"

	// VsphereInstallModeLocal env value for Vsphere 'local' install
	VsphereInstallModeLocal = "local"

	// NdoeLabelPortworxVersion is the label key in the node labels that has the
	// Portworx version of that node.
	NodeLabelPortworxVersion = "PX Version"

	ClusterOperatorVersion              = "config.openshift.io/v1"
	ClusterOperatorKind                 = "ClusterOperator"
	OpenshiftAPIServer                  = "openshift-apiserver"
	OpenshiftPrometheusSupportedVersion = "4.12"
	Openshift_4_15_Version              = "4.15"
	OpenshiftPxSupportedVersion         = "4.13"
	// OpenshiftMonitoringRouteName name of OCP user-workload route
	OpenshiftMonitoringRouteName = "thanos-querier"
	// OpenshiftMonitoringRouteName namespace of OCP user-workload route
	OpenshiftMonitoringNamespace = "openshift-monitoring"
)

var (
	// SpecsBaseDir functions returns the base directory for specs. This is extracted as
	// variable for testing. DO NOT change the value of the function unless for testing.
	SpecsBaseDir = getSpecsBaseDir

	// MinimumSupportedK8sVersion minimum k8s version PX supports
	MinimumSupportedK8sVersion, _ = version.NewVersion("v1.12.0")
	// MinimumPxVersionCCM minimum PX version to install ccm
	MinimumPxVersionCCM, _ = version.NewVersion("2.8")
	// MinimumPxVersionCCMGO minimum PX version to install ccm-go
	MinimumPxVersionCCMGO, _ = version.NewVersion("2.12")
	// MinimumPxVersionMetricsCollector minimum PX version to install metrics collector
	MinimumPxVersionMetricsCollector, _ = version.NewVersion("2.9.1")
	// MinimumPxVersionAutoTLS is a minimal PX version that supports "auto-TLS" setup
	MinimumPxVersionAutoTLS, _ = version.NewVersion("4.0.0")

	// ConfigMapNameRegex regex of configMap.
	ConfigMapNameRegex = regexp.MustCompile("[^a-zA-Z0-9]+")
)

func getStrippedClusterName(cluster *corev1.StorageCluster) string {
	return strings.ToLower(ConfigMapNameRegex.ReplaceAllString(GetClusterID(cluster), ""))
}

// GetInternalEtcdConfigMapName gets name of internal etcd configMap.
func GetInternalEtcdConfigMapName(cluster *corev1.StorageCluster) string {
	return fmt.Sprintf("%s%s", InternalEtcdConfigMapPrefix, getStrippedClusterName(cluster))
}

// GetCloudDriveConfigMapName gets name of cloud drive configMap.
func GetCloudDriveConfigMapName(cluster *corev1.StorageCluster) string {
	return fmt.Sprintf("%s%s", CloudDriveConfigMapPrefix, getStrippedClusterName(cluster))
}

// IsPortworxEnabled returns true if portworx is not explicitly disabled using the annotation
func IsPortworxEnabled(cluster *corev1.StorageCluster) bool {
	disabled, err := strconv.ParseBool(cluster.Annotations[constants.AnnotationDisableStorage])
	return err != nil || !disabled
}

// IsCSIEnabled returns true if CSI is not disabled by the feature flag
func IsCSIEnabled(cluster *corev1.StorageCluster) bool {
	return IsPortworxEnabled(cluster) && cluster.Spec.CSI != nil && cluster.Spec.CSI.Enabled
}

// IsPKS returns true if the annotation has a PKS annotation and is true value
func IsPKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsPKS])
	return err == nil && enabled
}

// IsGKE returns true if the annotation has a GKE annotation and is true value
func IsGKE(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsGKE])
	return err == nil && enabled
}

// IsOKE returns true if the annotation has a OKE annotation and is true value
func IsOKE(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsOKE])
	return err == nil && enabled
}

// IsAKS returns true if the annotation has an AKS annotation and is true value
func IsAKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsAKS])
	return err == nil && enabled
}

// IsEKS returns true if the annotation has an EKS annotation and is true value
func IsEKS(cluster *corev1.StorageCluster) bool {
	// TODO: use cloud provider to determine EKS
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsEKS])
	return err == nil && enabled
}

// IsIKS returns true if the annotation has an IKS annotation and is true value
func IsIKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsIKS])
	return err == nil && enabled
}

// IsOpenshift returns true if the annotation has an OpenShift annotation and is true value
func IsOpenshift(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsOpenshift])
	return err == nil && enabled
}

// IsPrivileged returns true "privileged" annotation is MISSING, or NOT set to FALSE
func IsPrivileged(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsPrivileged])
	return err != nil || enabled
}

// GetClusterEnvValue helper routine to get the env value from cluster spec.
func GetClusterEnvValue(cluster *corev1.StorageCluster, envName string) (string, bool) {
	for _, env := range cluster.Spec.Env {
		if env.Name == envName {
			return env.Value, true
		}
	}
	return "", false
}

// IsVsphere returns true if VSPHERE_VCENTER is present in the spec
func IsVsphere(cluster *corev1.StorageCluster) bool {
	envValue, exists := GetClusterEnvValue(cluster, "VSPHERE_VCENTER")
	if exists && len(envValue) > 0 {
		return true
	}
	return false
}

// GetCloudProvider returns the cloud provider string
func GetCloudProvider(cluster *corev1.StorageCluster) string {
	if IsVsphere(cluster) {
		return cloudops.Vsphere
	}
	// TODO: implement conditions for other providers
	return ""
}

// IsHostPidEnabled returns if hostPid should be set to true for portworx pod.
func IsHostPidEnabled(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationHostPid])
	return err == nil && enabled
}

// RunOnMaster returns true if the annotation has truth value for running on master
func RunOnMaster(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationRunOnMaster])
	return err == nil && enabled
}

// StorageClassEnabled returns true if default portworx storage classes are disabled
func StorageClassEnabled(cluster *corev1.StorageCluster) bool {
	disabled, err := strconv.ParseBool(cluster.Annotations[AnnotationDisableStorageClass])
	return err != nil || !disabled
}

// PodDisruptionBudgetEnabled returns true if the annotation is absent, or does not
// have a false value. By default we always create PDBs.
func PodDisruptionBudgetEnabled(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationPodDisruptionBudget])
	return err != nil || enabled
}

// PodSecurityPolicyEnabled returns true if the PSP annotation is present and has true value
func PodSecurityPolicyEnabled(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationPodSecurityPolicy])
	return err == nil && enabled
}

// ServiceType returns the k8s service type from cluster annotations if present
// 1. backward compatible format: "LoadBalancer", "ClusterIP" or "NodePort"
// 2. control different services: "portworx-service:LoadBalancer;portworx-api:ClusterIP;portworx-kvdb-service:NodePort"
func ServiceType(cluster *corev1.StorageCluster, serviceName string) v1.ServiceType {
	var serviceType v1.ServiceType
	val, exist := cluster.Annotations[AnnotationServiceType]
	if !exist || val == "" {
		return serviceType
	}

	valParts := strings.Split(val, ";")
	for _, p := range valParts {
		annotationParts := strings.Split(p, ":")
		if len(annotationParts) == 1 {
			// No ":" found, so we expect this to be the backward compatible case
			serviceType = v1.ServiceType(annotationParts[0])
			break
		} else if (len(annotationParts) == 2) && (annotationParts[0] == serviceName) {
			serviceType = v1.ServiceType(annotationParts[1])
			break
		}
	}

	if serviceType == v1.ServiceTypeClusterIP ||
		serviceType == v1.ServiceTypeNodePort ||
		serviceType == v1.ServiceTypeLoadBalancer ||
		serviceType == v1.ServiceTypeExternalName {
		return serviceType
	}

	return ""
}

// MiscArgs returns the miscellaneous arguments from the cluster's annotations
func MiscArgs(cluster *corev1.StorageCluster) ([]string, error) {
	if cluster.Annotations[AnnotationMiscArgs] != "" {
		return shlex.Split(cluster.Annotations[AnnotationMiscArgs])
	}
	return nil, nil
}

// ImagePullPolicy returns the image pull policy from the cluster spec if present,
// else returns v1.PullAlways
func ImagePullPolicy(cluster *corev1.StorageCluster) v1.PullPolicy {
	imagePullPolicy := v1.PullAlways
	if cluster.Spec.ImagePullPolicy == v1.PullNever ||
		cluster.Spec.ImagePullPolicy == v1.PullIfNotPresent {
		imagePullPolicy = cluster.Spec.ImagePullPolicy
	}
	return imagePullPolicy
}

// IsStorkEnabled returns true is Stork scheduler is enabled in StorageCluster
func IsStorkEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Stork != nil &&
		cluster.Spec.Stork.Enabled
}

// StartPort returns the start from the cluster if present,
// else return the default start port
func StartPort(cluster *corev1.StorageCluster) int {
	startPort := DefaultStartPort
	if cluster.Spec.StartPort != nil {
		startPort = int(*cluster.Spec.StartPort)
	} else if IsOpenshift(cluster) {
		startPort = DefaultOpenshiftStartPort
	}
	return startPort
}

// KubeletPath returns the kubelet path
func KubeletPath(cluster *corev1.StorageCluster) string {
	for _, env := range cluster.Spec.Env {
		if env.Name == EnvKeyKubeletDir && len(env.Value) > 0 {
			return env.Value
		}
	}

	if IsPKS(cluster) {
		return "/var/vcap/data/kubelet"
	}

	return "/var/lib/kubelet"
}

// PortworxServiceAccountName returns name of the portworx service account
func PortworxServiceAccountName(cluster *corev1.StorageCluster) string {
	for _, env := range cluster.Spec.Env {
		if env.Name == EnvKeyPortworxServiceAccount && len(env.Value) > 0 {
			return env.Value
		}
	}
	return DefaultPortworxServiceAccountName
}

// UseDeprecatedCSIDriverName returns true if the cluster env variables has
// an override, else returns false.
func UseDeprecatedCSIDriverName(cluster *corev1.StorageCluster) bool {
	for _, env := range cluster.Spec.Env {
		if env.Name == EnvKeyDeprecatedCSIDriverName {
			value, err := strconv.ParseBool(env.Value)
			return err == nil && value
		}
	}
	return false
}

// DisableCSIAlpha returns true if the cluster env variables has a variable to disable
// CSI alpha features, else returns false.
func DisableCSIAlpha(cluster *corev1.StorageCluster) bool {
	for _, env := range cluster.Spec.Env {
		if env.Name == EnvKeyDisableCSIAlpha {
			value, err := strconv.ParseBool(env.Value)
			return err == nil && value
		}
	}
	return false
}

// IncludeCSISnapshotController determines if the end user has indicated
// whether or not to include the CSI snapshot-controller
func IncludeCSISnapshotController(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.CSI != nil && cluster.Spec.CSI.InstallSnapshotController != nil && *cluster.Spec.CSI.InstallSnapshotController
}

// GetPortworxVersion returns the Portworx version based on the Spec data provided.
// We first try to extract the image from the PX_IMAGE env variable if specified,
// If not specified then we use Spec.Image, if the version extraction fails for any
// reason, we look for annotation portworx.io/px-version if that fails or does not exist.
// Then we try to extract the version from PX_RELEASE_MANIFEST_URL env variable, else
// we return int max as the version.
func GetPortworxVersion(cluster *corev1.StorageCluster) *version.Version {
	var (
		err         error
		pxVersion   *version.Version
		manifestURL string
	)

	pxImage := strings.TrimSpace(cluster.Spec.Image)

	for _, env := range cluster.Spec.Env {
		if env.Name == EnvKeyPXImage {
			pxImage = env.Value
		} else if env.Name == EnvKeyPXReleaseManifestURL {
			manifestURL = env.Value
		}
	}

	parts := strings.Split(pxImage, ":")
	if len(parts) >= 2 {
		pxVersionStr := parts[len(parts)-1]
		pxVersion, err = version.NewSemver(pxVersionStr)
		if err == nil {
			logrus.Infof("Using version extracted from image name: %s", pxVersionStr)
			return pxVersion
		}
		logrus.Warnf("Invalid PX version %s extracted from image name: %v", pxVersionStr, err)
		pxVersion = nil
	}

	if pxVersion == nil {
		if pxVersionStr, exists := cluster.Annotations[AnnotationPXVersion]; exists {
			pxVersion, err = version.NewSemver(pxVersionStr)
			if err == nil {
				logrus.Infof("Using version extracted from annotation: %s", pxVersionStr)
				return pxVersion
			}
			logrus.Warnf("Invalid PX version %s extracted from annotation: %v", pxVersionStr, err)
			pxVersion = nil
		}
	}

	if len(manifestURL) > 0 && pxVersion == nil {
		pxVersionStr := getPortworxVersionFromManifestURL(manifestURL)
		pxVersion, err = version.NewSemver(pxVersionStr)
		if err == nil {
			logrus.Infof("Using version extracted from manifest url: %s", pxVersionStr)
			return pxVersion
		}
		logrus.Warnf("Invalid PX version %s extracted from release manifest url: %v", pxVersionStr, err)
	}

	if pxVersion == nil {
		pxVersion, _ = version.NewVersion(strconv.FormatInt(math.MaxInt64, 10))
		logrus.Infof("Using default max version")
	}
	return pxVersion
}

func getPortworxVersionFromManifestURL(url string) string {
	regex := regexp.MustCompile(`.*portworx\.com\/(.*)\/version`)
	version := regex.FindStringSubmatch(url)
	if len(version) >= 2 {
		return version[1]
	}
	return ""
}

// GetStorkVersion returns the stork version based on the image provided.
// We first look at spec.Stork.Image. If that is not present or invalid semvar, then we fallback to an
// annotation portworx.io/stork-version; else we return int max as the version.
func GetStorkVersion(cluster *corev1.StorageCluster) *version.Version {
	defaultVersion, _ := version.NewVersion(strconv.FormatInt(math.MaxInt64, 10))
	if cluster.Spec.Stork == nil || !cluster.Spec.Stork.Enabled {
		logrus.Warnf("could not find stork version from cluster spec")
		return defaultVersion
	}

	// get correct stork image to check
	var storkImage = cluster.Spec.Stork.Image
	if storkImage == "" {
		storkImage = cluster.Status.DesiredImages.Stork
	}

	// parse storkImage
	var storkVersion *version.Version
	var err error
	parts := strings.Split(storkImage, ":")
	if len(parts) >= 2 {
		storkVersionStr := parts[len(parts)-1]
		storkVersion, err = version.NewSemver(storkVersionStr)
		if err != nil {
			logrus.Warnf("Invalid Stork version %s extracted from image name: %v", storkVersionStr, err)
			if storkVersionStr, exists := cluster.Annotations[AnnotationStorkVersion]; exists {
				storkVersion, err = version.NewSemver(storkVersionStr)
				if err != nil {
					logrus.Warnf("Invalid Stork version %s extracted from annotation: %v", storkVersionStr, err)
				}
			}
		}
	} else {
		logrus.Warnf("could not find stork version from cluster spec")
		return defaultVersion
	}

	if storkVersion == nil {
		return defaultVersion
	}

	return storkVersion
}

// GetImageTag returns the tag of the image
func GetImageTag(image string) string {
	if parts := strings.Split(image, ":"); len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return ""
}

// SelectorLabels returns the labels that are used to select Portworx pods
func SelectorLabels() map[string]string {
	return map[string]string{
		labelKeyName: DriverName,
	}
}

// StorageClusterKind returns the GroupVersionKind for StorageCluster
func StorageClusterKind() schema.GroupVersionKind {
	return corev1.SchemeGroupVersion.WithKind("StorageCluster")
}

// GetClusterEnvVarValue returns the environment variable value for a cluster.
// Note: This strictly gets the Value, not ValueFrom
func GetClusterEnvVarValue(ctx context.Context, cluster *corev1.StorageCluster, envKey string) string {
	for _, envVar := range cluster.Spec.Env {
		if envVar.Name == envKey {
			return envVar.Value
		}
	}

	return ""
}

// GetPxProxyEnvVarValue returns the PX_HTTP(S)_PROXY environment variable value for a cluster.
// Note: we only expect one proxy for the telemetry CCM container but we prefer https over http if both are specified
func GetPxProxyEnvVarValue(cluster *corev1.StorageCluster) (string, string) {
	httpProxy := ""
	for _, env := range cluster.Spec.Env {
		key, val := env.Name, env.Value
		if key == EnvKeyPortworxHTTPSProxy {
			return EnvKeyPortworxHTTPSProxy, val
		} else if key == EnvKeyPortworxHTTPProxy {
			httpProxy = val
		}
	}
	if httpProxy != "" {
		return EnvKeyPortworxHTTPProxy, httpProxy
	}
	return "", ""
}

var (
	authHeader string
)

// ParsePxProxy trims protocol prefix then splits the proxy address of the form "host:port" with possible basic authentication credential
func ParsePxProxyURL(proxy string) (string, string, string, error) {
	if strings.Contains(proxy, "@") {
		proxyUrl, err := url.Parse(proxy)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to parse px proxy url %s", proxy)
		}
		username := proxyUrl.User.Username()
		password, _ := proxyUrl.User.Password()
		encodedAuth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		authHeader = fmt.Sprintf("Basic %s", encodedAuth)
		host, port, err := net.SplitHostPort(proxyUrl.Host)
		if err != nil {
			return "", "", "", err
		} else if host == "" || port == "" || encodedAuth == "" {
			return "", "", "", fmt.Errorf("failed to split px proxy to get host and port %s", proxy)
		}
		return host, port, authHeader, nil
	} else {
		proxy = strings.TrimPrefix(proxy, HttpProtocolPrefix)
		proxy = strings.TrimPrefix(proxy, HttpsProtocolPrefix) // treat https proxy as http proxy if no credential provided
		host, port, err := net.SplitHostPort(proxy)
		if err != nil {
			return "", "", "", err
		} else if host == "" || port == "" {
			return "", "", "", fmt.Errorf("failed to split px proxy to get host and port %s", proxy)
		}
		return host, port, authHeader, nil
	}
}

// GetValueFromEnvVar returns the value of v1.EnvVar Value or ValueFrom
func GetValueFromEnvVar(ctx context.Context, client client.Client, envVar *v1.EnvVar, namespace string) (string, error) {
	if valueFrom := envVar.ValueFrom; valueFrom != nil {
		if valueFrom.SecretKeyRef != nil {
			key := valueFrom.SecretKeyRef.Key
			secretName := valueFrom.SecretKeyRef.Name

			// Get secret key
			secret := &v1.Secret{}
			err := client.Get(ctx, types.NamespacedName{
				Name:      secretName,
				Namespace: namespace,
			}, secret)
			if err != nil {
				return "", err
			}
			value := secret.Data[key]
			if len(value) == 0 {
				return "", fmt.Errorf("failed to find env var value %s in secret %s in namespace %s", key, secretName, namespace)
			}

			return string(value), nil
		} else if valueFrom.ConfigMapKeyRef != nil {
			cmName := valueFrom.ConfigMapKeyRef.Name
			key := valueFrom.ConfigMapKeyRef.Key
			configMap := &v1.ConfigMap{}
			if err := client.Get(ctx, types.NamespacedName{
				Name:      cmName,
				Namespace: namespace,
			}, configMap); err != nil {
				return "", err
			}

			value, ok := configMap.Data[key]
			if !ok {
				return "", fmt.Errorf("failed to find env var value %s in configmap %s in namespace %s", key, cmName, namespace)
			}

			return value, nil
		}
	} else {
		return envVar.Value, nil
	}

	return "", nil
}

func getSpecsBaseDir() string {
	return PortworxSpecsDir
}

// GetPortworxConn returns a new Portworx SDK client
func GetPortworxConn(sdkConn *grpc.ClientConn, k8sClient client.Client, namespace string) (*grpc.ClientConn, error) {
	if sdkConn != nil {
		return sdkConn, nil
	}

	pxService := &v1.Service{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      PortworxServiceName,
			Namespace: namespace,
		},
		pxService,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get k8s service spec: %v", err)
	} else if len(pxService.Spec.ClusterIP) == 0 {
		return nil, fmt.Errorf("failed to get endpoint for portworx volume driver")
	}

	endpoint := pxService.Spec.ClusterIP
	sdkPort := defaultSDKPort

	// Get the ports from service
	for _, pxServicePort := range pxService.Spec.Ports {
		if pxServicePort.Name == PortworxSDKPortName && pxServicePort.Port != 0 {
			sdkPort = int(pxServicePort.Port)
		}
	}

	endpoint = net.JoinHostPort(endpoint, strconv.Itoa(sdkPort))
	return GetGrpcConn(endpoint)
}

// GetGrpcConn creates a new gRPC connection to a given endpoint
func GetGrpcConn(endpoint string) (*grpc.ClientConn, error) {
	dialOptions, err := GetDialOptions(IsTLSEnabled())
	if err != nil {
		return nil, err
	}
	sdkConn, err := grpcserver.ConnectWithTimeout(endpoint, dialOptions, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%s [%s]: %v", ErrMsgGrpcConnection, endpoint, err)
	}
	return sdkConn, nil
}

// GetDialOptions is a gRPC utility to get dial options for a connection
func GetDialOptions(tls bool) ([]grpc.DialOption, error) {
	if !tls {
		return []grpc.DialOption{grpc.WithInsecure()}, nil
	}
	capool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("failed to load CA system certs: %v", err)
	}
	return []grpc.DialOption{grpc.WithTransportCredentials(
		credentials.NewClientTLSFromCert(capool, ""),
	)}, nil
}

// IsTLSEnabled checks if TLS is enabled for the operator
func IsTLSEnabled() bool {
	enabled, err := strconv.ParseBool(os.Getenv(EnvKeyPortworxEnableTLS))
	return err == nil && enabled
}

// GenerateToken generates an auth token given a secret key
func GenerateToken(
	cluster *corev1.StorageCluster,
	secretkey string,
	claims *auth.Claims,
	duration time.Duration,
) (string, error) {
	signature, err := auth.NewSignatureSharedSecret(secretkey)
	if err != nil {
		return "", err
	}
	token, err := auth.Token(claims, signature, &auth.Options{
		Expiration: time.Now().
			Add(duration).Unix(),
	})
	if err != nil {
		return "", err
	}

	return token, nil
}

// GetSecretKeyValue gets any key value from a k8s secret
func GetSecretKeyValue(
	cluster *corev1.StorageCluster,
	secret *v1.Secret,
	secretKey string,
) (string, error) {
	// check for secretName
	value, ok := secret.Data[secretKey]
	if !ok || len(value) <= 0 {
		return "", fmt.Errorf("failed to get key %s inside secret %s/%s", secretKey, cluster.Namespace, secret.Name)
	}

	return string(value), nil
}

// GetSecretValue gets any secret key value from k8s and decodes to a string value
func GetSecretValue(
	ctx context.Context,
	cluster *corev1.StorageCluster,
	k8sClient client.Client,
	secretName,
	secretKey string,
) (string, error) {
	// check for secretName
	secret := v1.Secret{}
	err := k8sClient.Get(ctx,
		types.NamespacedName{
			Name:      secretName,
			Namespace: cluster.Namespace,
		},
		&secret,
	)
	if err != nil {
		return "", err
	}

	return GetSecretKeyValue(cluster, &secret, secretKey)
}

// SecurityEnabled checks if the security flag is set for a cluster
func SecurityEnabled(cluster *corev1.StorageCluster) bool {
	return cluster.Spec.Security != nil && cluster.Spec.Security.Enabled
}

// SetupContextWithToken Gets token or from secret for authenticating with the SDK server
func SetupContextWithToken(ctx context.Context, cluster *corev1.StorageCluster, k8sClient client.Client) (context.Context, error) {
	// auth not declared in cluster spec
	if !SecurityEnabled(cluster) {
		return ctx, nil
	}

	pxAppsSecret, err := GetSecretValue(ctx, cluster, k8sClient, SecurityPXSystemSecretsSecretName, SecurityAppsSecretKey)
	if err != nil {
		return ctx, fmt.Errorf("failed to get portworx apps secret: %v", err.Error())
	}
	if pxAppsSecret == "" {
		return ctx, nil
	}

	// Generate token and add to metadata.
	name := "operator"
	pxAppsIssuerVersion, err := version.NewVersion("2.6.0")
	if err != nil {
		return ctx, err
	}
	pxVersion := GetPortworxVersion(cluster)
	issuer := SecurityPortworxAppsIssuer
	if !pxVersion.GreaterThanOrEqual(pxAppsIssuerVersion) {
		issuer = SecurityPortworxStorkIssuer
	}
	token, err := GenerateToken(cluster, pxAppsSecret, &auth.Claims{
		Issuer:  issuer,
		Subject: fmt.Sprintf("%s@%s", name, issuer),
		Name:    name,
		Email:   fmt.Sprintf("%s@%s", name, issuer),
		Roles:   []string{"system.admin"},
		Groups:  []string{"*"},
	}, 24*time.Hour)
	if err != nil {
		return ctx, fmt.Errorf("failed to generate token: %v", err.Error())
	}

	md := metadata.New(map[string]string{
		"authorization": "bearer " + token,
	})
	return metadata.NewOutgoingContext(ctx, md), nil
}

// EncodeBase64 encode a given src byte slice.
func EncodeBase64(src []byte) []byte {
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(src)))
	base64.StdEncoding.Encode(encoded, src)

	return encoded
}

// EssentialsEnabled returns true if the env var has an override
// to deploy an PX Essentials cluster
func EssentialsEnabled() bool {
	enabled, err := strconv.ParseBool(os.Getenv(EnvKeyPortworxEssentials))
	return err == nil && enabled
}

// ParseExtendedDuration returns the duration associated with a string
// This function supports seconds, minutes, hours, days, and years.
// i.e. 5s, 5m, 5h, 5d, and 5y
func ParseExtendedDuration(s string) (time.Duration, error) {
	return auth.ParseToDuration(s)
}

// UserVolumeName returns modified volume name for the user given volume name
func UserVolumeName(name string) string {
	return userVolumeNamePrefix + name
}

// IsTelemetryEnabled returns true is telemetry is enabled
func IsTelemetryEnabled(spec corev1.StorageClusterSpec) bool {
	return spec.Monitoring != nil &&
		spec.Monitoring.Telemetry != nil &&
		spec.Monitoring.Telemetry.Enabled
}

// IsCCMGoSupported returns true if px version is higher than 2.12
func IsCCMGoSupported(pxVersion *version.Version) bool {
	return pxVersion.GreaterThanOrEqual(MinimumPxVersionCCMGO)
}

// IsMetricsCollectorSupported returns true if px version is higher than 2.9.1
func IsMetricsCollectorSupported(pxVersion *version.Version) bool {
	return pxVersion.GreaterThanOrEqual(MinimumPxVersionMetricsCollector)
}

// ApplyStorageClusterSettingsToPodSpec applies settings from StorageCluster to pod spec of any component
// Which includes:
//
//	custom image registry for images
//	ImagePullPolicy
//	ImagePullSecret
//	affinity
//	toleration
func ApplyStorageClusterSettingsToPodSpec(cluster *corev1.StorageCluster, podSpec *v1.PodSpec) {
	var containers []*v1.Container
	for i := 0; i < len(podSpec.Containers); i++ {
		containers = append(containers, &podSpec.Containers[i])
	}
	for i := 0; i < len(podSpec.InitContainers); i++ {
		containers = append(containers, &podSpec.InitContainers[i])
	}
	for _, container := range containers {
		// Change image to custom repository if it has not been done
		if !strings.HasPrefix(container.Image, strings.TrimSuffix(cluster.Spec.CustomImageRegistry, "/")) {
			container.Image = util.GetImageURN(cluster, container.Image)
		}
		container.ImagePullPolicy = ImagePullPolicy(cluster)
	}

	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *cluster.Spec.ImagePullSecret,
			},
		)
	}

	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			podSpec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(cluster.Spec.Placement.Tolerations) > 0 {
			podSpec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range cluster.Spec.Placement.Tolerations {
				podSpec.Tolerations = append(
					podSpec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}
}

// GetClusterID returns portworx instance cluster ID
func GetClusterID(cluster *corev1.StorageCluster) string {
	if cluster.Annotations[AnnotationClusterID] != "" {
		return cluster.Annotations[AnnotationClusterID]
	}
	return cluster.Name
}

func MinAvailableForStoragePDB(cluster *corev1.StorageCluster) (int, error) {
	if cluster.Annotations[AnnotationStoragePodDisruptionBudget] != "" {
		return strconv.Atoi(cluster.Annotations[AnnotationStoragePodDisruptionBudget])
	}
	return -1, nil
}

// CountStorageNodes counts how many px storage node are there on given k8s cluster,
// use this to count number of storage pods as well
func CountStorageNodes(
	cluster *corev1.StorageCluster,
	sdkConn *grpc.ClientConn,
	k8sClient client.Client,
) (int, error) {
	nodeClient := api.NewOpenStorageNodeClient(sdkConn)
	ctx, err := SetupContextWithToken(context.Background(), cluster, k8sClient)
	if err != nil {
		return -1, err
	}

	nodeEnumerateResponse, err := nodeClient.EnumerateWithFilters(
		ctx,
		&api.SdkNodeEnumerateWithFiltersRequest{},
	)
	if err != nil {
		return -1, fmt.Errorf("failed to enumerate nodes: %v", err)
	}

	k8sNodeList := &v1.NodeList{}
	err = k8sClient.List(context.TODO(), k8sNodeList)
	if err != nil {
		return -1, err
	}
	k8sNodesStoragePodCouldRun := make(map[string]bool)
	for _, node := range k8sNodeList.Items {
		shouldRun, shouldContinueRunning, err := k8sutil.CheckPredicatesForStoragePod(&node, cluster, nil)
		if err != nil {
			return -1, err
		}
		if shouldRun || shouldContinueRunning {
			k8sNodesStoragePodCouldRun[node.Name] = true
		}
	}

	storageNodesCount := 0
	for _, node := range nodeEnumerateResponse.Nodes {
		if node.SchedulerNodeName == "" {
			k8sNode, err := coreops.Instance().SearchNodeByAddresses(
				[]string{node.DataIp, node.MgmtIp, node.Hostname},
			)
			if err != nil {
				logrus.Warnf("Unable to find kubernetes node name for nodeID %v: %v", node.Id, err)
				continue
			}
			node.SchedulerNodeName = k8sNode.Name
		}
		if len(node.Pools) > 0 && node.Pools[0] != nil {
			if _, ok := k8sNodesStoragePodCouldRun[node.SchedulerNodeName]; ok {
				storageNodesCount++
			}
		}
	}

	return storageNodesCount, nil
}

func CleanupObject(obj client.Object) {
	obj.SetGenerateName("")
	obj.SetUID("")
	obj.SetResourceVersion("")
	obj.SetGeneration(0)
	obj.SetSelfLink("")
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetFinalizers(nil)
	obj.SetOwnerReferences(nil)
	obj.SetManagedFields(nil)
}

func SetTelemetryCertOwnerRef(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	k8sClient client.Client,
) error {
	secret := &v1.Secret{}
	err := k8sClient.Get(
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

	// Only delete the secret when delete strategy is UninstallAndWipe
	deleteCert := cluster.Spec.DeleteStrategy != nil &&
		cluster.Spec.DeleteStrategy.Type == corev1.UninstallAndWipeStorageClusterStrategyType

	referenceMap := make(map[types.UID]*metav1.OwnerReference)
	for _, ref := range secret.OwnerReferences {
		referenceMap[ref.UID] = &ref
	}

	_, ownerSet := referenceMap[ownerRef.UID]
	if deleteCert && !ownerSet {
		referenceMap[ownerRef.UID] = ownerRef
	} else if !deleteCert && ownerSet {
		delete(referenceMap, ownerRef.UID)
	} else {
		return nil
	}

	var references []metav1.OwnerReference
	for _, v := range referenceMap {
		references = append(references, *v)
	}
	secret.OwnerReferences = references

	return k8sClient.Update(context.TODO(), secret)
}

// IsFreshInstall checks whether it's a fresh Portworx install
func IsFreshInstall(cluster *corev1.StorageCluster) bool {
	// To handle failures during fresh install e.g. validation falures,
	// extra check for px runtime states is added here to avoid unexpected behaviors
	return cluster.Status.Phase == "" ||
		cluster.Status.Phase == string(corev1.ClusterStateInit) ||
		(cluster.Status.Phase == string(corev1.ClusterStateDegraded) &&
			util.GetStorageClusterCondition(cluster, PortworxComponentName, corev1.ClusterConditionTypeRuntimeState) == nil)
}

// GetTLSMinVersion gets requested TLS version and validates it
func GetTLSMinVersion(cluster *corev1.StorageCluster) (string, error) {
	req := cluster.Annotations[AnnotationServerTLSMinVersion]
	if req == "" {
		return "", nil
	}
	req = strings.Trim(req, " \t")

	switch strings.ToUpper(req) {
	case "VERSIONTLS10":
		return "VersionTLS10", nil
	case "VERSIONTLS11":
		return "VersionTLS11", nil
	case "VERSIONTLS12":
		return "VersionTLS12", nil
	case "VERSIONTLS13":
		return "VersionTLS13", nil
	}

	return "", fmt.Errorf("invalid TLS version: expected one of VersionTLS1{0..3}, got %s", req)
}

// GetTLSCipherSuites gets requested TLS ciphers suites and validates it
// - RETURN: the normalized comma-separated list of cipher suites, or error if requested unknown cipher
func GetTLSCipherSuites(cluster *corev1.StorageCluster) (string, error) {
	req := cluster.Annotations[AnnotationServerTLSCipherSuites]
	if req == "" {
		return "", nil
	}

	csMap := make(map[string]bool)
	for _, c := range tls.CipherSuites() {
		csMap[c.Name] = true
	}
	icsMap := make(map[string]bool)
	for _, c := range tls.InsecureCipherSuites() {
		icsMap[c.Name] = true
	}

	req = strings.ToUpper(req)
	parts := strings.FieldsFunc(req, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ':' || r == ';' || r == ','
	})
	outList := make([]string, 0, len(parts))

	for _, p := range parts {
		if p == "" {
			// nop..
		} else if _, has := csMap[p]; has {
			outList = append(outList, p)
		} else if _, has = icsMap[p]; has {
			logrus.Warnf("Requested insecure cipher suite %s", p)
			outList = append(outList, p)
		} else {
			return "", fmt.Errorf("unknown cipher suite %s", p)
		}
	}
	return strings.Join(outList, ","), nil
}

// AppendUserVolumeMounts appends "user" vol specs to the pod spec
//   - note, the user volume specs will override container mounts, if the mount
//     destination directory is the same
//   - caveat: caller needs to ensure that the volume specs NAMES are unique
func AppendUserVolumeMounts(
	podSpec *v1.PodSpec,
	userVolSpecList []corev1.VolumeSpec,
) {
	if podSpec == nil {
		return
	} else if len(userVolSpecList) == 0 {
		return
	}

	// make map of user-volumes, also append vols to pod spec
	usrSpecMap := make(map[string]corev1.VolumeSpec)
	for _, v := range userVolSpecList {
		usrSpecMap[v.MountPath] = v
		podSpec.Volumes = append(podSpec.Volumes, v1.Volume{
			Name:         UserVolumeName(v.Name),
			VolumeSource: v.VolumeSource,
		})
	}

	// update container volumes, when destination-dir matches
	for idx1, cntr := range podSpec.Containers {
		for idx2, cv := range cntr.VolumeMounts {
			if uv, has := usrSpecMap[cv.MountPath]; has {
				logrus.Debugf("Replacing container %s:%s mount '%s' with user-mount '%s'",
					cntr.Name, cv.MountPath, cv.Name, uv.Name)

				podSpec.Containers[idx1].VolumeMounts[idx2] = v1.VolumeMount{
					Name:             UserVolumeName(uv.Name),
					MountPath:        uv.MountPath,
					ReadOnly:         uv.ReadOnly,
					MountPropagation: uv.MountPropagation,
				}
			}
		}
	}
}

func IsSupportedOCPVersion(k8sClient client.Client, targetVersion string) (bool, error) {
	gvk := schema.GroupVersionKind{
		Kind:    ClusterOperatorKind,
		Version: ClusterOperatorVersion,
	}

	exists, err := coreops.Instance().ResourceExists(gvk)
	if err != nil {
		return false, err
	}

	if exists {
		operator := &ocpconfig.ClusterOperator{}
		err := k8sClient.Get(
			context.TODO(),
			types.NamespacedName{
				Name: OpenshiftAPIServer,
			},
			operator,
		)

		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}

		for _, v := range operator.Status.Versions {
			if v.Name == OpenshiftAPIServer && isVersionSupported(v.Version, targetVersion) {
				return true, nil
			}
		}
	}

	return false, nil
}

func GetOCPPrometheusHost(k8sClient client.Client) (string, error) {

	route := &routev1.Route{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      OpenshiftMonitoringRouteName,
			Namespace: OpenshiftMonitoringNamespace,
		},
		route,
	)
	if err != nil {
		return "", fmt.Errorf("error fetching route %s", err.Error())
	}

	if route.Spec.Host == "" {
		return "", fmt.Errorf("host is empty")
	}

	return "https://" + route.Spec.Host, nil

}

func isVersionSupported(current, target string) bool {
	targetVersion, err := version.NewVersion(target)
	if err != nil {
		logrus.Errorf("Error during parsing version : %s ", err)
		return false
	}

	currentVersion, err := version.NewVersion(current)
	if err != nil {
		logrus.Errorf("Error during parsing version : %s ", err)
		return false
	}

	return currentVersion.Core().GreaterThanOrEqual(targetVersion)
}
