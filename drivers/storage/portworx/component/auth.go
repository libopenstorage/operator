package component

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	osauth "github.com/libopenstorage/openstorage/pkg/auth"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// AuthComponentName is the name for registering this component
	AuthComponentName = "Auth"
	// AuthTokenBufferLength is the time ahead of the token
	// expiration that we will start refreshing the token
	AuthTokenBufferLength = time.Minute * 1
	// AuthSystemGuestRoleName is the role name to maintain for the guest role
	AuthSystemGuestRoleName = "system.guest"
)

// GuestRoleEnabled is the default configuration for the guest role
var GuestRoleEnabled = api.SdkRole{
	Name: AuthSystemGuestRoleName,
	Rules: []*api.SdkRule{
		{
			Services: []string{"mountattach", "volume", "cloudbackup", "migrate"},
			Apis:     []string{"*"},
		},
		{
			Services: []string{"identity"},
			Apis:     []string{"version"},
		},
		{
			Services: []string{
				"cluster",
				"node",
			},
			Apis: []string{
				"inspect*",
				"enumerate*",
			},
		},
	},
}

// GuestRoleDisabled is the disabled configuration for the guest role
var GuestRoleDisabled = api.SdkRole{
	Name: AuthSystemGuestRoleName,
	Rules: []*api.SdkRule{
		{
			Services: []string{"!*"},
			Apis:     []string{"!*"},
		},
	},
}

type auth struct {
	k8sClient            client.Client
	sdkConn              *grpc.ClientConn
	resourceVersionCache map[string]string
}

func (a *auth) Name() string {
	return AuthComponentName
}

func (a *auth) Priority() int32 {
	return int32(0)
}

// Initialize initializes the componenet
func (a *auth) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	a.k8sClient = k8sClient
	a.resourceVersionCache = make(map[string]string)
}

// IsEnabled checks if the components needs to be enabled based on the StorageCluster
func (a *auth) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.AuthEnabled(&cluster.Spec)
}

// Reconcile reconciles the component to match the current state of the StorageCluster
func (a *auth) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	err := a.createPrivateKeysSecret(cluster, ownerRef)
	if err != nil {
		return err
	}

	err = a.maintainAuthTokenSecret(cluster, ownerRef, pxutil.SecurityPXAdminTokenSecretName, "system.admin", []string{"*"})
	if err != nil {
		return fmt.Errorf("failed to maintain auth token secret %s: %s ", pxutil.SecurityPXAdminTokenSecretName, err.Error())
	}

	err = a.maintainAuthTokenSecret(cluster, ownerRef, pxutil.SecurityPXUserTokenSecretName, "system.user", []string{})
	if err != nil {
		return fmt.Errorf("failed to maintain auth token secret %s: %s ", pxutil.SecurityPXUserTokenSecretName, err.Error())
	}

	err = a.updateSystemGuestRole(cluster)
	if err != nil {
		return fmt.Errorf("failed to update system guest role: %s ", err.Error())
	}

	return nil
}

// Delete deletes the component if present
func (a *auth) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	// delete token secrets - these are ephemeral and can be recreated easily
	err := a.deleteSecret(cluster, ownerRef, pxutil.SecurityPXAdminTokenSecretName)
	if err != nil {
		return err
	}

	err = a.deleteSecret(cluster, ownerRef, pxutil.SecurityPXUserTokenSecretName)
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
	err = a.deleteSecret(cluster, nil, pxutil.SecurityPXSharedSecretSecretName)
	if err != nil {
		return err
	}

	err = a.deleteSecret(cluster, nil, pxutil.SecurityPXSystemSecretsSecretName)
	if err != nil {
		return err
	}

	a.closeSdkConn()
	return nil
}

// MarkDeleted marks the component as deleted in situations like StorageCluster deletion
func (a *auth) MarkDeleted() {

}

func (a *auth) getPrivateKeyOrGenerate(cluster *corev1.StorageCluster, envVarKey, secretName, secretKey string) (string, error) {
	var privateKey string
	var err error

	// check for pre-configured shared secret
	for _, envVar := range cluster.Spec.Env {
		if envVar.Name == envVarKey {
			privateKey, err = pxutil.GetValueFromEnvVar(context.TODO(), a.k8sClient, &envVar, cluster.Namespace)
			if err != nil {
				return "", err

			}
			return privateKey, nil
		}
	}

	// check for pre-existing secret
	secret := &v1.Secret{}
	a.k8sClient.Get(context.TODO(), types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      secretName,
	}, secret)
	if errors.IsNotFound(err) || len(secret.Data) == 0 || string(secret.Data[secretKey]) == "" {
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

func (a *auth) createPrivateKeysSecret(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	var sharedSecretKey, internalSystemSecretKey string
	var err error

	sharedSecretKey, err = a.getPrivateKeyOrGenerate(
		cluster,
		pxutil.EnvKeyPortworxAuthJwtSharedSecret,
		*cluster.Spec.Security.Auth.SelfSigned.SharedSecret,
		pxutil.SecuritySharedSecretKey,
	)
	if err != nil {
		return err
	}

	internalSystemSecretKey, err = a.getPrivateKeyOrGenerate(
		cluster,
		pxutil.EnvKeyPortworxAuthSystemKey,
		pxutil.SecurityPXSystemSecretsSecretName,
		pxutil.SecuritySystemSecretKey,
	)
	if err != nil {
		return err
	}

	appsSecretKey, err := a.getPrivateKeyOrGenerate(
		cluster,
		pxutil.EnvKeyPortworxAuthSystemAppsKey,
		pxutil.SecurityPXSystemSecretsSecretName,
		pxutil.SecurityAppsSecretKey,
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
			pxutil.SecurityAppsSecretKey:   []byte(appsSecretKey),
		},
	}

	err = k8sutil.CreateOrAppendToSecret(a.k8sClient, sharedSecret, nil)
	if err != nil {
		return err
	}

	err = k8sutil.CreateOrAppendToSecret(a.k8sClient, systemKeysSecret, nil)
	if err != nil {
		return err
	}

	return nil
}

func getTokenClaims(token string) (*jwt.StandardClaims, error) {
	t, _, err := new(jwt.Parser).ParseUnverified(token, &jwt.StandardClaims{})
	if err != nil {
		return nil, fmt.Errorf("failed to parse authorization token: %s", err.Error())
	}

	claims, ok := t.Claims.(*jwt.StandardClaims)
	if !ok {
		return nil, fmt.Errorf("failed to get token claims")
	}

	return claims, nil
}

func (a *auth) createToken(
	cluster *corev1.StorageCluster,
	authTokenSecretName string,
	role string,
	authSecret string,
	groups []string) (string, error) {
	// Generate token
	claims := osauth.Claims{
		Issuer:  *cluster.Spec.Security.Auth.SelfSigned.Issuer,
		Subject: fmt.Sprintf("%s@%s", authTokenSecretName, *cluster.Spec.Security.Auth.SelfSigned.Issuer),
		Name:    authTokenSecretName,
		Email:   fmt.Sprintf("%s@%s", authTokenSecretName, *cluster.Spec.Security.Auth.SelfSigned.Issuer),
		Roles:   []string{role},
		Groups:  groups,
	}
	tokenDuration, err := pxutil.ParseExtendedDuration(*cluster.Spec.Security.Auth.SelfSigned.TokenLifetime)
	if err != nil {
		return "", fmt.Errorf("failed to parse token lifetime: %v", err.Error())
	}
	token, err := pxutil.GenerateToken(cluster, authSecret, &claims, tokenDuration)
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %v", err.Error())
	}

	return token, nil

}

// maintainAuthTokenSecret maintains a PX auth token inside of a given k8s secret.
// Before token expiration, the token is refreshed for the user.
func (a *auth) maintainAuthTokenSecret(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	authTokenSecretName string,
	role string,
	groups []string,
) error {

	// Check if token is expired or the signature has changed
	updateNeeded, err := a.isTokenSecretRefreshRequired(cluster, authTokenSecretName)
	if err != nil {
		return err
	}
	if updateNeeded {
		k8sAuthSecret := v1.Secret{}
		err = a.k8sClient.Get(context.TODO(),
			types.NamespacedName{
				Name:      *cluster.Spec.Security.Auth.SelfSigned.SharedSecret,
				Namespace: cluster.Namespace,
			},
			&k8sAuthSecret,
		)
		if err != nil {
			return fmt.Errorf("failed to get shared secret: %s", err.Error())
		}

		// Get auth secret and generate token
		authSecret, err := pxutil.GetSecretKeyValue(cluster, &k8sAuthSecret, pxutil.SecuritySharedSecretKey)
		if err != nil {
			return err
		}

		token, err := a.createToken(cluster, authTokenSecretName, role, authSecret, groups)
		if err != nil {
			return err
		}

		// Store new token
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
		err = k8sutil.CreateOrUpdateSecret(a.k8sClient, secret, ownerRef)
		if err != nil {
			return err
		}

		// Cache new resource version

		// Store which resource version the given authTokenSecret was last generated with
		a.resourceVersionCache[authTokenSecretName] = k8sAuthSecret.ResourceVersion
	}

	return nil
}

func (a *auth) isTokenSecretRefreshRequired(
	cluster *corev1.StorageCluster,
	authTokenSecretName string,
) (bool, error) {

	// Get auth secret and key value inside
	k8sAuthSecret := v1.Secret{}
	err := a.k8sClient.Get(context.TODO(),
		types.NamespacedName{
			Name:      *cluster.Spec.Security.Auth.SelfSigned.SharedSecret,
			Namespace: cluster.Namespace,
		},
		&k8sAuthSecret,
	)
	if err != nil {
		return true, err
	}
	// Check if auth secret has been updated for the given authTokenSecret
	lastResourceVersion, ok := a.resourceVersionCache[authTokenSecretName]
	if k8sAuthSecret.ResourceVersion != lastResourceVersion || !ok {
		return true, nil
	}

	// Get Token Secret
	k8sTokenSecret := v1.Secret{}
	err = a.k8sClient.Get(context.TODO(),
		types.NamespacedName{
			Name:      authTokenSecretName,
			Namespace: cluster.Namespace,
		},
		&k8sTokenSecret,
	)
	if errors.IsNotFound(err) {
		// Secret treated as expired if not found.
		// Return authSecretValue for token generation.
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to check token secret expiration %s: %s ", authTokenSecretName, err.Error())
	}

	// Get auth token
	authToken, err := pxutil.GetSecretKeyValue(cluster, &k8sTokenSecret, pxutil.SecurityAuthTokenKey)
	if errors.IsNotFound(err) {
		// Secret treated as expired if not found
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to check token secret expiration %s: %s ", authTokenSecretName, err.Error())
	}

	// Get token expiry from fetched token and add to cache
	claims, err := getTokenClaims(authToken)
	if err != nil {
		return false, err
	}

	// add some buffer to prevent missing a token refresh
	currentTimeWithBuffer := time.Now().Add(AuthTokenBufferLength).Unix()
	if currentTimeWithBuffer > claims.ExpiresAt {
		// token has expired
		return true, nil
	}

	// Get issuer to check if it has changed
	if claims.Issuer != *cluster.Spec.Security.Auth.SelfSigned.Issuer {
		// issuer has changed
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

func (a *auth) deleteSecret(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	name string,
) error {
	if ownerRef == nil {
		return k8sutil.DeleteSecret(a.k8sClient, name, cluster.Namespace)
	}
	return k8sutil.DeleteSecret(a.k8sClient, name, cluster.Namespace, *ownerRef)
}

// closeSdkConn closes the sdk connection and resets it to nil
func (a *auth) closeSdkConn() {
	if a.sdkConn == nil {
		return
	}

	if err := a.sdkConn.Close(); err != nil {
		logrus.Errorf("failed to close sdk connection: %s", err.Error())
	}
	a.sdkConn = nil
}

func (a *auth) updateSystemGuestRole(cluster *corev1.StorageCluster) error {
	if cluster.Status.Phase == "" || cluster.Status.Phase == string(corev1.ClusterInit) {
		return nil
	}

	if *cluster.Spec.Security.Auth.GuestAccess != corev1.GuestRoleEnabled &&
		*cluster.Spec.Security.Auth.GuestAccess != corev1.GuestRoleDisabled &&
		*cluster.Spec.Security.Auth.GuestAccess != corev1.GuestRoleManaged {
		return fmt.Errorf("invalid guest access type: %s", *cluster.Spec.Security.Auth.GuestAccess)
	}

	// Guest access added in PX 2.6.0, skip this feature if below 2.6.0
	systemGuestMinimumVersion, err := version.NewVersion("2.6.0")
	if err != nil {
		return err
	}
	if pxutil.GetPortworxVersion(cluster).LessThan(systemGuestMinimumVersion) {
		return nil
	}

	// managed, do not interfere with system.guest role
	if *cluster.Spec.Security.Auth.GuestAccess == corev1.GuestRoleManaged {
		return nil
	}

	a.sdkConn, err = pxutil.GetPortworxConn(a.sdkConn, a.k8sClient, cluster.Namespace)
	if err != nil {
		return err
	}

	roleClient := api.NewOpenStorageRoleClient(a.sdkConn)
	ctx, err := pxutil.SetupContextWithToken(context.Background(), cluster, a.k8sClient)
	if err != nil {
		a.closeSdkConn()
		return err
	}

	// Only updated when required
	var desiredRole api.SdkRole
	if *cluster.Spec.Security.Auth.GuestAccess == corev1.GuestRoleEnabled {
		desiredRole = GuestRoleEnabled
	} else {
		desiredRole = GuestRoleDisabled
	}
	currentRoleResp, err := roleClient.Inspect(ctx, &api.SdkRoleInspectRequest{
		Name: AuthSystemGuestRoleName,
	})
	if err != nil {
		a.closeSdkConn()
		return nil
	}
	currentRole := *currentRoleResp.GetRole()
	if currentRole.String() != desiredRole.String() {
		_, err = roleClient.Update(ctx, &api.SdkRoleUpdateRequest{
			Role: &desiredRole,
		})
		if err != nil {
			a.closeSdkConn()
			return err
		}
	}

	return nil
}

// RegisterAuthComponent registers the auth component
func RegisterAuthComponent() {
	Register(AuthComponentName, &auth{})
}

func init() {
	RegisterAuthComponent()
}
