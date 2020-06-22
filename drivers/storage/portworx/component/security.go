package component

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SecurityComponentName is the name for registering this component
	SecurityComponentName = "Security"
	// SecuritySharedSecretKey is the key for accessing the jwt shared secret
	SecuritySharedSecretKey = "shared-secret"
	// SecuritySystemSecretKey is the key for accessing the system secret auth key
	SecuritySystemSecretKey = "system-secret"
)

type security struct {
	k8sClient client.Client
}

// Initialize initializes the componenet
func (c *security) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

// IsEnabled checks if the components needs to be enabled based on the StorageCluster
func (c *security) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return pxutil.SecurityEnabled(cluster)
}

// Reconcile reconciles the component to match the current state of the StorageCluster
func (c *security) Reconcile(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	err := c.createPrivateKeysSecret(cluster, ownerRef)
	if err != nil {
		return err
	}

	return nil
}

// Delete deletes the component if present
func (c *security) Delete(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	// Only delete the component if a cluster wipe has been initiated
	if cluster.DeletionTimestamp == nil ||
		cluster.Spec.DeleteStrategy == nil ||
		cluster.Spec.DeleteStrategy.Type == "" {
		return nil
	}

	err := c.deletePrivateKeysSecret(cluster, ownerRef)
	if err != nil {
		return err
	}

	return nil
}

// MarkDeleted marks the component as deleted in situations like StorageCluster deletion
func (c *security) MarkDeleted() {

}

func (c *security) getPrivateKeyOrGenerate(cluster *corev1alpha1.StorageCluster, envVarKey, secretName, secretKey string) (string, error) {
	var privateKey string
	var err error

	// check for pre-configured shared secret
	for _, envVar := range cluster.Spec.Env {
		if envVar.Name == envVarKey {
			privateKey, err = pxutil.GetValueFromEnvVar(context.TODO(), c.k8sClient, &envVar, cluster.Namespace)
			if err != nil {
				return "", err
			}

			return privateKey, nil
		}
	}

	// If we did not find a secret, generate and add it
	if privateKey == "" {
		privateKey, err = generateAuthSecret()
		if err != nil {
			return "", err
		}
	}

	return privateKey, nil
}

func (c *security) createPrivateKeysSecret(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	var sharedSecretKey, internalSystemSecretKey string
	var err error

	sharedSecretKey, err = c.getPrivateKeyOrGenerate(
		cluster,
		pxutil.EnvKeyPortworxAuthJwtSharedSecret,
		pxutil.SecurityPXAuthKeysSecretName,
		SecuritySharedSecretKey,
	)
	if err != nil {
		return err
	}

	internalSystemSecretKey, err = c.getPrivateKeyOrGenerate(
		cluster,
		pxutil.EnvKeyPortworxAuthSystemKey,
		pxutil.SecurityPXAuthKeysSecretName,
		SecuritySystemSecretKey,
	)
	if err != nil {
		return err
	}

	privateKeysSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.SecurityPXAuthKeysSecretName,
			Namespace: cluster.Namespace,
		},
		StringData: map[string]string{
			SecuritySharedSecretKey: sharedSecretKey,
			SecuritySystemSecretKey: internalSystemSecretKey,
		},
	}

	err = k8sutil.CreateOrUpdateSecret(c.k8sClient, privateKeysSecret, ownerRef)
	if err != nil {
		return err
	}

	return nil
}

func generateAuthSecret() (string, error) {
	var randBytes = make([]byte, 128)
	_, err := rand.Read(randBytes)
	if err != nil {
		return "", err
	}

	password := base64.StdEncoding.EncodeToString([]byte(randBytes))
	password = password[:64]

	return string(password), nil
}

func (c *security) deletePrivateKeysSecret(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.DeleteSecret(c.k8sClient, pxutil.SecurityPXAuthKeysSecretName, cluster.Namespace, *ownerRef)
}

// RegisterSecurityComponent registers the security component
func RegisterSecurityComponent() {
	Register(SecurityComponentName, &security{})
}

func init() {
	RegisterSecurityComponent()
}
