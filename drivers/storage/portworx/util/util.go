package util

import (
	"math"
	"strconv"
	"strings"

	"github.com/hashicorp/go-version"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// DriverName name of the portworx driver
	DriverName = "portworx"
	// DefaultStartPort is the default start port for Portworx
	DefaultStartPort = 9001
	// PortworxSpecsDir is the directory where all the Portworx specs are stored
	PortworxSpecsDir = "/configs"

	// PortworxServiceAccountName name of the Portworx service account
	PortworxServiceAccountName = "portworx"
	// PortworxServiceName name of the Portworx Kubernetes service
	PortworxServiceName = "portworx-service"
	// PortworxRESTPortName name of the Portworx API port
	PortworxRESTPortName = "px-api"
	// PortworxSDKPortName name of the Portworx SDK port
	PortworxSDKPortName = "px-sdk"
	// PortworxKVDBPortName name of the Portworx internal KVDB port
	PortworxKVDBPortName = "px-kvdb"

	// AnnotationIsPKS annotation indicating whether it is a PKS cluster
	AnnotationIsPKS = pxAnnotationPrefix + "/is-pks"
	// AnnotationIsGKE annotation indicating whether it is a GKE cluster
	AnnotationIsGKE = pxAnnotationPrefix + "/is-gke"
	// AnnotationIsAKS annotation indicating whether it is an AKS cluster
	AnnotationIsAKS = pxAnnotationPrefix + "/is-aks"
	// AnnotationIsEKS annotation indicating whether it is an EKS cluster
	AnnotationIsEKS = pxAnnotationPrefix + "/is-eks"
	// AnnotationIsOpenshift annotation indicating whether it is an OpenShift cluster
	AnnotationIsOpenshift = pxAnnotationPrefix + "/is-openshift"
	// AnnotationPVCController annotation indicating whether to deploy a PVC controller
	AnnotationPVCController = pxAnnotationPrefix + "/pvc-controller"
	// AnnotationPVCControllerCPU annotation for overriding the default CPU for PVC
	// controller deployment
	AnnotationPVCControllerCPU = pxAnnotationPrefix + "/pvc-controller-cpu"
	// AnnotationAutopilotCPU annotation for overriding the default CPU for Autopilot
	AnnotationAutopilotCPU = pxAnnotationPrefix + "/autopilot-cpu"
	// AnnotationServiceType annotation indicating k8s service type for all services
	// deployed by the operator
	AnnotationServiceType = pxAnnotationPrefix + "/service-type"
	// AnnotationPXVersion annotation indicating the portworx semantic version
	AnnotationPXVersion = pxAnnotationPrefix + "/px-version"

	// EnvKeyPXImage key for the environment variable that specifies Portworx image
	EnvKeyPXImage = "PX_IMAGE"
	// EnvKeyPortworxNamespace key for the env var which tells namespace in which
	// Portworx is installed
	EnvKeyPortworxNamespace = "PX_NAMESPACE"
	// EnvKeyDeprecatedCSIDriverName key for the env var that can force Portworx
	// to use the deprecated CSI driver name
	EnvKeyDeprecatedCSIDriverName = "PORTWORX_USEDEPRECATED_CSIDRIVERNAME"

	pxAnnotationPrefix = "portworx.io"
	labelKeyName       = "name"
)

var (
	// SpecsBaseDir functions returns the base directory for specs. This is extracted as
	// variable for testing. DO NOT change the value of the function unless for testing.
	SpecsBaseDir = getSpecsBaseDir
)

// IsPortworxEnabled returns true if portworx is not explicitly disabled using the annotation
func IsPortworxEnabled(cluster *corev1alpha1.StorageCluster) bool {
	disabled, err := strconv.ParseBool(cluster.Annotations[storagecluster.AnnotationDisableStorage])
	return err != nil || !disabled
}

// IsPKS returns true if the annotation has a PKS annotation and is true value
func IsPKS(cluster *corev1alpha1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsPKS])
	return err == nil && enabled
}

// IsGKE returns true if the annotation has a GKE annotation and is true value
func IsGKE(cluster *corev1alpha1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsGKE])
	return err == nil && enabled
}

// IsAKS returns true if the annotation has an AKS annotation and is true value
func IsAKS(cluster *corev1alpha1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsAKS])
	return err == nil && enabled
}

// IsEKS returns true if the annotation has an EKS annotation and is true value
func IsEKS(cluster *corev1alpha1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsEKS])
	return err == nil && enabled
}

// IsOpenshift returns true if the annotation has an OpenShift annotation and is true value
func IsOpenshift(cluster *corev1alpha1.StorageCluster) bool {
	enabled, err := strconv.ParseBool(cluster.Annotations[AnnotationIsOpenshift])
	return err == nil && enabled
}

// ServiceType returns the k8s service type from cluster annotations if present
func ServiceType(cluster *corev1alpha1.StorageCluster) v1.ServiceType {
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

// ImagePullPolicy returns the image pull policy from the cluster spec if present,
// else returns v1.PullAlways
func ImagePullPolicy(cluster *corev1alpha1.StorageCluster) v1.PullPolicy {
	imagePullPolicy := v1.PullAlways
	if cluster.Spec.ImagePullPolicy == v1.PullNever ||
		cluster.Spec.ImagePullPolicy == v1.PullIfNotPresent {
		imagePullPolicy = cluster.Spec.ImagePullPolicy
	}
	return imagePullPolicy
}

// StartPort returns the start from the cluster if present,
// else return the default start port
func StartPort(cluster *corev1alpha1.StorageCluster) int {
	startPort := DefaultStartPort
	if cluster.Spec.StartPort != nil {
		startPort = int(*cluster.Spec.StartPort)
	}
	return startPort
}

// UseDeprecatedCSIDriverName returns true if the cluster env variables has
// an override, else returns false.
func UseDeprecatedCSIDriverName(cluster *corev1alpha1.StorageCluster) bool {
	for _, env := range cluster.Spec.Env {
		if env.Name == EnvKeyDeprecatedCSIDriverName {
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
func GetPortworxVersion(cluster *corev1alpha1.StorageCluster) *version.Version {
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

// SelectorLabels returns the labels that are used to select Portworx pods
func SelectorLabels() map[string]string {
	return map[string]string{
		labelKeyName: DriverName,
	}
}

// StorageClusterKind returns the GroupVersionKind for StorageCluster
func StorageClusterKind() schema.GroupVersionKind {
	return corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
}

func getSpecsBaseDir() string {
	return PortworxSpecsDir
}
