package component

import (
	"fmt"
	"strings"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// TLSComponentName is the name for registering this component
	TLSComponentName = "TLS"
	// SupportedRootFolderForTLSCerts: all TLS cert files need to be placed here or in a subfolder.
	SupportedRootFolderForTLSCerts = "/etc/pwx"
)

type tls struct {
}

func (t *tls) Name() string {
	return TLSComponentName
}

func (t *tls) Priority() int32 {
	return int32(0) // same as auth component
}

// Initialize initializes the componenet
func (t *tls) Initialize(
	_ client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,

) {
}

// IsEnabled checks if the components needs to be enabled based on the StorageCluster
func (t *tls) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTLSEnabledOnCluster(&cluster.Spec)
}

// Reconcile reconciles the component to match the current state of the StorageCluster
func (t *tls) Reconcile(cluster *corev1.StorageCluster) error {
	if err := t.validateTLSSpecs(cluster); err != nil {
		return err
	}
	return nil
}

func (t *tls) validateTLSSpecs(cluster *corev1.StorageCluster) error {
	if pxutil.IsTLSEnabledOnCluster(&cluster.Spec) {
		tls := cluster.Spec.Security.TLS
		// validate that any file paths are rooted to our mounted folder: /etc/pwx
		if err := validateCertLocation("serverCert", tls.ServerCert); err != nil {
			return err
		}
		if err := validateCertLocation("serverKey", tls.ServerKey); err != nil {
			return err
		}
		// rootCA is optional iff both serverCert and serverKey supplied. By now we've determined that valid values of serverCert and serverKey have been supplied
		if tls.RootCA == nil || (tls.RootCA.FileName == nil && tls.RootCA.SecretRef == nil) {
			return nil // empty rootCA is acceptable ... assume certs are signed by well known CA
		}
		// rootCA is not empty: it needs to be a valid value
		if err := validateCertLocation("rootCA", tls.RootCA); err != nil {
			return err
		}
	}
	return nil
}

func validateCertLocation(certName string, certLocation *corev1.CertLocation) error {
	// pxutil.IsEmptyOrNilCertLocation will also catch this scenario where no file specified, and only one of server cert or key specified
	//   special casing here because we want a more helpful message than "must specify location of tls certificate as either file or kubernetes secret"
	if certLocation != nil && pxutil.IsEmptyOrNilStringPtr(certLocation.FileName) && certLocation.SecretRef != nil && util.IsPartialSecretRef(certLocation.SecretRef) {
		return NewError(ErrCritical, fmt.Errorf("%s: for kubernetes secret must specify both name and key", certName))
	}

	// should not be nil
	if pxutil.IsEmptyOrNilCertLocation(certLocation) {
		return NewError(ErrCritical, fmt.Errorf("%s: must specify location of tls certificate as either file or kubernetes secret", certName))
	}

	// should not specify both file location and k8s secret
	if !pxutil.IsEmptyOrNilSecretReference(certLocation.SecretRef) && !pxutil.IsEmptyOrNilStringPtr(certLocation.FileName) {
		return NewError(ErrCritical, fmt.Errorf("%s: can not specify both filename and secretRef as source for tls certs. ", certName))
	}
	// file must be under mounted folder /etc/pwx
	if !pxutil.IsEmptyOrNilStringPtr(certLocation.FileName) {
		if strings.HasPrefix(*certLocation.FileName, SupportedRootFolderForTLSCerts) {
			return nil // all good
		}
		return NewError(ErrCritical, fmt.Errorf("%s: file path (%s) not under folder %s", certName, *certLocation.FileName, SupportedRootFolderForTLSCerts))

	}
	return nil
}

// Delete deletes the component if present
func (t *tls) Delete(cluster *corev1.StorageCluster) error {
	return nil
}

func (t *tls) MarkDeleted() {
}

// RegisterAuthComponent registers the auth component
func RegisterTLSComponent() {
	Register(TLSComponentName, &tls{})
}

func init() {
	RegisterTLSComponent()
}
