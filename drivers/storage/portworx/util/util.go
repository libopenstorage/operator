package util

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/shlex"
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/pkg/auth"
	"github.com/libopenstorage/openstorage/pkg/grpcserver"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DriverName name of the portworx driver
	DriverName = "portworx"
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

	// AnnotationIsPKS annotation indicating whether it is a PKS cluster
	AnnotationIsPKS = pxAnnotationPrefix + "/is-pks"
	// AnnotationIsGKE annotation indicating whether it is a GKE cluster
	AnnotationIsGKE = pxAnnotationPrefix + "/is-gke"
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

	// EnvKeyPXImage key for the environment variable that specifies Portworx image
	EnvKeyPXImage = "PX_IMAGE"
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
	// EnvKeyStorkPXSharedSecret is an environment variable defining the shared secret for Stork
	EnvKeyStorkPXSharedSecret = "PX_SHARED_SECRET"
	// EnvKeyStorkPXJwtIssuer is an environment variable defining the jwt issuer for Stork
	EnvKeyStorkPXJwtIssuer = "PX_JWT_ISSUER"
	// EnvKeyPortworxAuthStorkKey is an environment variable for the auth secret
	// that stork and the operator use to communicate with portworx
	EnvKeyPortworxAuthStorkKey = "PORTWORX_AUTH_STORK_KEY"
	// EnvKeyPortworxEssentials env var to deploy Portworx Essentials cluster
	EnvKeyPortworxEssentials = "PORTWORX_ESSENTIALS"
	// EnvKeyMarketplaceName env var for the name of the source marketplace
	EnvKeyMarketplaceName = "MARKETPLACE_NAME"

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
	ImageNamePause = "k8s.gcr.io/pause:3.1"

	// userVolumeNamePrefix prefix used for user volume names to avoid name conflicts with existing volumes
	userVolumeNamePrefix = "user-"
	pxAnnotationPrefix   = "portworx.io"
	labelKeyName         = "name"
	defaultSDKPort       = 9020
)

var (
	// SpecsBaseDir functions returns the base directory for specs. This is extracted as
	// variable for testing. DO NOT change the value of the function unless for testing.
	SpecsBaseDir = getSpecsBaseDir
)

// IsPortworxEnabled returns true if portworx is not explicitly disabled using the annotation
func IsPortworxEnabled(cluster *corev1.StorageCluster) bool {
	disabled, err := strconv.ParseBool(cluster.Annotations[constants.AnnotationDisableStorage])
	return err != nil || !disabled
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

// IsAKS returns true if the annotation has an AKS annotation and is true value
func IsAKS(cluster *corev1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsAKS])
	return err == nil && enabled
}

// IsEKS returns true if the annotation has an EKS annotation and is true value
func IsEKS(cluster *corev1.StorageCluster) bool {
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

// ServiceType returns the k8s service type from cluster annotations if present
func ServiceType(cluster *corev1.StorageCluster) v1.ServiceType {
	var serviceType v1.ServiceType
	if val, exists := cluster.Annotations[AnnotationServiceType]; exists {
		st := v1.ServiceType(val)
		if st == v1.ServiceTypeClusterIP ||
			st == v1.ServiceTypeNodePort ||
			st == v1.ServiceTypeLoadBalancer {
			serviceType = st
		}
	}
	return serviceType
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

// GetPortworxVersion returns the Portworx version based on the image provided.
// We first look at spec.Image, if not valid image tag found, we check the PX_IMAGE
// env variable. If that is not present or invalid semvar, then we fallback to an
// annotation portworx.io/px-version; else we return int max as the version.
func GetPortworxVersion(cluster *corev1.StorageCluster) *version.Version {
	var (
		err       error
		pxVersion *version.Version
	)

	pxImage := cluster.Spec.Image
	for _, env := range cluster.Spec.Env {
		if env.Name == EnvKeyPXImage {
			pxImage = env.Value
			break
		}
	}

	parts := strings.Split(pxImage, ":")
	if len(parts) >= 2 {
		pxVersionStr := parts[len(parts)-1]
		pxVersion, err = version.NewSemver(pxVersionStr)
		if err != nil {
			logrus.Warnf("Invalid PX version %s extracted from image name: %v", pxVersionStr, err)
			if pxVersionStr, exists := cluster.Annotations[AnnotationPXVersion]; exists {
				pxVersion, err = version.NewSemver(pxVersionStr)
				if err != nil {
					logrus.Warnf("Invalid PX version %s extracted from annotation: %v", pxVersionStr, err)
				}
			}
		}
	}

	if pxVersion == nil {
		pxVersion, _ = version.NewVersion(strconv.FormatInt(math.MaxInt64, 10))
	}
	return pxVersion
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

	endpoint = fmt.Sprintf("%s:%d", endpoint, sdkPort)
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
