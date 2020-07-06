package component

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/pkg/auth"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SecurityComponentName is the name for registering this component
	SecurityComponentName = "Security"
	// SecurityTokenBufferLength is the time ahead of the token
	// expiration that we will start refreshing the token
	SecurityTokenBufferLength = time.Minute * 1
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

	err = c.maintainAuthTokenSecret(cluster, ownerRef, pxutil.SecurityPXAdminTokenSecretName, "system.admin", []string{"*"})
	if err != nil {
		return fmt.Errorf("failed to maintain auth token secret %s: %s ", pxutil.SecurityPXAdminTokenSecretName, err.Error())
	}

	err = c.maintainAuthTokenSecret(cluster, ownerRef, pxutil.SecurityPXUserTokenSecretName, "system.user", []string{})
	if err != nil {
		return fmt.Errorf("failed to maintain auth token secret %s: %s ", pxutil.SecurityPXUserTokenSecretName, err.Error())
	}

	return nil
}

// Delete deletes the component if present
func (c *security) Delete(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	// delete token secrets - these are ephemeral and can be recreated easily
	err := c.deleteSecret(cluster, ownerRef, pxutil.SecurityPXAdminTokenSecretName)
	if err != nil {
		return err
	}

	err = c.deleteSecret(cluster, ownerRef, pxutil.SecurityPXUserTokenSecretName)
	if err != nil {
		return err
	}

	// Only delete auth secrets if a cluster wipe has been initiated
	if cluster.DeletionTimestamp == nil ||
		cluster.Spec.DeleteStrategy == nil ||
		cluster.Spec.DeleteStrategy.Type == "" {
		return nil
	}

	// only deleted our default generated one. If they provide a secret name in the spec, do not delete it.
	err = c.deleteSecret(cluster, nil, pxutil.SecurityPXSharedSecretSecretName)
	if err != nil {
		return err
	}

	err = c.deleteSecret(cluster, nil, pxutil.SecurityPXSystemSecretsSecretName)
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

	// check for pre-existing secret
	secret := &v1.Secret{}
	c.k8sClient.Get(context.TODO(), types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      secretName,
	}, secret)
	if errors.IsNotFound(err) || len(secret.Data) == 0 {
		privateKey, err = generateAuthSecret()
		if err != nil {
			return "", err
		}

		return privateKey, nil
	} else if err != nil {
		return "", err
	}

	return string(secret.Data[secretKey]), nil
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
		*cluster.Spec.Security.Auth.SelfSigned.SharedSecret,
		pxutil.SecuritySharedSecretKey,
	)
	if err != nil {
		return err
	}

	internalSystemSecretKey, err = c.getPrivateKeyOrGenerate(
		cluster,
		pxutil.EnvKeyPortworxAuthSystemKey,
		pxutil.SecurityPXSystemSecretsSecretName,
		pxutil.SecuritySystemSecretKey,
	)
	if err != nil {
		return err
	}

	sharedSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      *cluster.Spec.Security.Auth.SelfSigned.SharedSecret,
			Namespace: cluster.Namespace,
		}, Data: map[string][]byte{
			pxutil.SecuritySharedSecretKey: []byte(sharedSecretKey),
		},
	}
	systemKeysSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.SecurityPXSystemSecretsSecretName,
			Namespace: cluster.Namespace,
		}, Data: map[string][]byte{
			pxutil.SecuritySystemSecretKey: []byte(internalSystemSecretKey),
		},
	}

	err = k8sutil.CreateOrUpdateSecret(c.k8sClient, sharedSecret, nil)
	if err != nil {
		return err
	}

	err = k8sutil.CreateOrUpdateSecret(c.k8sClient, systemKeysSecret, nil)
	if err != nil {
		return err
	}

	return nil
}

func getTokenExpiry(token string) (int64, error) {
	t, _, err := new(jwt.Parser).ParseUnverified(token, &jwt.StandardClaims{})
	if err != nil {
		return 0, fmt.Errorf("failed to parse authorization token: %s", err.Error())
	}

	claims, ok := t.Claims.(*jwt.StandardClaims)
	if !ok {
		return 0, fmt.Errorf("failed to get token claims")
	}

	return claims.ExpiresAt, nil
}

// maintainAuthTokenSecret maintains a PX auth token inside of a given k8s secret.
// Before token expiration, the token is refreshed for the user.
func (c *security) maintainAuthTokenSecret(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	authTokenSecretName string,
	role string,
	groups []string,
) error {

	expired, err := c.isTokenSecretExpired(cluster, authTokenSecretName)
	if err != nil {
		return err
	}
	if expired {
		// Get PX auth secret from k8s secret.
		var authSecret string
		authSecret, err = pxutil.GetSecretValue(context.TODO(), cluster, c.k8sClient, *cluster.Spec.Security.Auth.SelfSigned.SharedSecret, pxutil.SecuritySharedSecretKey)
		if err != nil {
			return err
		}

		// Generate token and add to metadata.
		claims := &auth.Claims{
			Issuer:  *cluster.Spec.Security.Auth.SelfSigned.Issuer,
			Subject: fmt.Sprintf("%s@portworx.io", authTokenSecretName),
			Name:    authTokenSecretName,
			Email:   fmt.Sprintf("%s@portworx.io", authTokenSecretName),
			Roles:   []string{role},
			Groups:  groups,
		}
		token, err := pxutil.GenerateToken(cluster, authSecret, claims)
		if err != nil {
			return fmt.Errorf("failed to generate token: %v", err.Error())
		}

		secret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            authTokenSecretName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: map[string][]byte{
				pxutil.SecurityAuthTokenKey: []byte(token),
			},
		}
		err = k8sutil.CreateOrUpdateSecret(c.k8sClient, secret, ownerRef)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *security) isTokenSecretExpired(
	cluster *corev1alpha1.StorageCluster,
	authTokenSecretName string,
) (bool, error) {

	var authToken string
	authToken, err := pxutil.GetSecretValue(context.TODO(), cluster, c.k8sClient, authTokenSecretName, pxutil.SecurityAuthTokenKey)
	if errors.IsNotFound(err) {
		// Secret treated as expired if not found
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to check token secret expiration %s: %s ", authTokenSecretName, err.Error())
	}

	// Get token expiry from fetched token and add to cache
	exp, err := getTokenExpiry(authToken)
	if err != nil {
		return false, err
	}

	// add some buffer to prevent missing a token refresh
	tokenExpiryBuffer := time.Now().Add(SecurityTokenBufferLength).Unix()
	if tokenExpiryBuffer > exp {
		return true, nil
	}

	return false, nil
}

func generateAuthSecret() (string, error) {
	var randBytes = make([]byte, 128)
	_, err := rand.Read(randBytes)
	if err != nil {
		return "", err
	}

	password := pxutil.EncodeBase64(randBytes)
	return string(password[:64]), nil
}

func (c *security) deleteSecret(
	cluster *corev1alpha1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	name string,
) error {
	if ownerRef == nil {
		return k8sutil.DeleteSecret(c.k8sClient, name, cluster.Namespace)
	}
	return k8sutil.DeleteSecret(c.k8sClient, name, cluster.Namespace, *ownerRef)
}

// RegisterSecurityComponent registers the security component
func RegisterSecurityComponent() {
	Register(SecurityComponentName, &security{})
}

func init() {
	RegisterSecurityComponent()
}
