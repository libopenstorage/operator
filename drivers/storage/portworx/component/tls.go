package component

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"net"

	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// TLSComponentName is the name for registering this component
	TLSComponentName = "TLS"
	// SupportedRootFolderForTLSCerts is where all TLS cert files need to be placed (or in a subfolder)
	SupportedRootFolderForTLSCerts = "/etc/pwx"
)

type tls struct {
	k8sClient client.Client
}

func (t *tls) Name() string {
	return TLSComponentName
}

func (t *tls) Priority() int32 {
	return int32(0) // same as auth component
}

// Initialize initializes the componenet
func (t *tls) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,

) {
	t.k8sClient = k8sClient
}

func (t *tls) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
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
		tlsSpec := cluster.Spec.Security.TLS
		// Defaults in place for self-generated
		if tlsSpec.ServerCert != nil && tlsSpec.ServerKey != nil && tlsSpec.RootCA != nil {
			if tlsSpec.ServerCert.SecretRef != nil && tlsSpec.ServerKey.SecretRef != nil && tlsSpec.RootCA.SecretRef != nil {
				if tlsSpec.ServerCert.SecretRef.SecretName == pxutil.DefaultServerCertSecretName &&
					tlsSpec.ServerKey.SecretRef.SecretName == pxutil.DefaultServerKeySecretName &&
					tlsSpec.RootCA.SecretRef.SecretName == pxutil.DefaultCASecretName {
					return t.selfGenCerts(cluster)
				}
			}
		}
		// TODO what if both file and secret definied?

		// validate that any file paths are rooted to our mounted folder: /etc/pwx
		if err := validateCertLocation("serverCert", tlsSpec.ServerCert); err != nil {
			return err
		}
		if err := validateCertLocation("serverKey", tlsSpec.ServerKey); err != nil {
			return err
		}
		// rootCA is optional iff both serverCert and serverKey supplied. By now we've determined that valid values of serverCert and serverKey have been supplied
		if tlsSpec.RootCA == nil || (tlsSpec.RootCA.FileName == nil && tlsSpec.RootCA.SecretRef == nil) {
			return nil // empty rootCA is acceptable ... assume certs are signed by well known CA
		}
		// rootCA is not empty: it needs to be a valid value
		if err := validateCertLocation("rootCA", tlsSpec.RootCA); err != nil {
			return err
		}
	}
	return nil
}

func (t *tls) selfGenCerts(cluster *corev1.StorageCluster) error {
	sec, err := coreops.Instance().GetSecret(pxutil.DefaultCASecretName, cluster.Namespace)
	if err == nil && sec != nil { // Already generated
		return nil
	}

	logrus.Infof("selfGenCerts")
	// Create CA key & cert
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(0),
		Subject: pkix.Name{
			Organization: []string{"Pure Storage"},
			Country:      []string{"US"},
		},
		NotBefore: time.Now(),
		// TODO Expire sooner and refresh?
		NotAfter:              time.Now().AddDate(100, 0, 0), // 100 years
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return err
	}
	caPEM := new(bytes.Buffer)
	pem.Encode(caPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})
	/* TODO need to hold onto CA key after signing?
	caPrivKeyPEM := new(bytes.Buffer)
	pem.Encode(caPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caPrivKey),
	})
	*/

	// Create CA secret
	caSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.DefaultCASecretName,
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			pxutil.DefaultCASecretKey: caPEM.Bytes(),
		},
	}
	// TODO? ownerRef
	err = k8sutil.CreateOrUpdateSecret(t.k8sClient, caSecret, nil)
	if err != nil {
		return err
	}

	// Create server key & cert
	// TODO per-server? convert deployment storageNodesList to util?
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Pure Storage"},
			Country:      []string{"US"},
		},
		// TODO more?
		DNSNames:    []string{"portworx-service.kube-system", "portworx-api.kube-system", "localhost"}, //,DNS:{{ inventory_hostname }}}
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},                                //IP:{{ ansible_eth0.ipv4.address }},IP:{{ kubeup_host_ip
		NotBefore:   time.Now(),
		// TODO Expire sooner and refresh?
		NotAfter:     time.Now().AddDate(100, 0, 0), // 100 years
		SubjectKeyId: []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}

	// Sign server cert
	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, &certPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return err
	}
	certPEM := new(bytes.Buffer)
	pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})
	certPrivKeyPEM := new(bytes.Buffer)
	pem.Encode(certPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	})

	// Create server cert & key secrets
	serverCertSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.DefaultServerCertSecretName,
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			pxutil.DefaultServerCertSecretKey: certPEM.Bytes(),
		},
	}
	// TODO? ownerRef
	err = k8sutil.CreateOrUpdateSecret(t.k8sClient, serverCertSecret, nil)
	if err != nil {
		return err
	}

	serverKeySecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.DefaultServerKeySecretName,
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			pxutil.DefaultServerKeySecretKey: certPrivKeyPEM.Bytes(),
		},
	}
	// TODO? ownerRef
	err = k8sutil.CreateOrUpdateSecret(t.k8sClient, serverKeySecret, nil)
	if err != nil {
		return err
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

// RegisterTLSComponent registers the TLS component
func RegisterTLSComponent() {
	Register(TLSComponentName, &tls{})
}

func init() {
	RegisterTLSComponent()
}
