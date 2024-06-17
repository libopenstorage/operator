package component

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	authv1 "k8s.io/api/authentication/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/apis/core"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PortworxServiceAccountTokenComponentName name of the Service Account Token component used by Portworx
	PortworxServiceAccountTokenComponentName = "Portworx Service Account Token"
	// TokenRefreshTimeKey time to refresh the service account token
	TokenRefreshTimeKey = "tokenRefreshTime"
)

var tokenExpirationSeconds = int64(12 * 60 * 60)

type portworxServiceAccountToken struct {
	k8sClient client.Client
}

func (t *portworxServiceAccountToken) Name() string {
	return PortworxServiceAccountTokenComponentName
}

func (t *portworxServiceAccountToken) Priority() int32 {
	// serviceaccount is a prerequisite for creaing token
	return int32(101)
}

func (t *portworxServiceAccountToken) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	t.k8sClient = k8sClient
}

func (t *portworxServiceAccountToken) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (t *portworxServiceAccountToken) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.PxServiceAccountTokenRefreshEnabled(cluster)
}

func (t *portworxServiceAccountToken) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := t.createTokenSecretIfNotExist(cluster, ownerRef); err != nil {
		return err
	}
	if err := t.maintainTokenSecret(cluster, ownerRef); err != nil {
		return fmt.Errorf("failed to maintain the token secret for px container. %v", err)
	}
	return nil
}

func (t *portworxServiceAccountToken) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	err := k8sutil.DeleteSecret(t.k8sClient, pxutil.PortworxServiceAccountTokenSecretName, cluster.Namespace, *ownerRef)
	if err != nil {
		return err
	}
	return nil
}

func (c *portworxServiceAccountToken) MarkDeleted() {}

func (t *portworxServiceAccountToken) createTokenSecretIfNotExist(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	secret := &v1.Secret{}
	err := t.k8sClient.Get(context.TODO(),
		types.NamespacedName{
			Name:      pxutil.PortworxServiceAccountTokenSecretName,
			Namespace: cluster.Namespace,
		}, secret)
	if err != nil {
		if errors.IsNotFound(err) || len(secret.Data) == 0 || len(secret.Data[v1.ServiceAccountTokenKey]) == 0 {
			token, err := generateToken(cluster)
			if err != nil {
				return err
			}
			rootCaCrt, _ := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
			curTime := time.Now()
			tokenRefreshTimeBytes, err := curTime.Add(time.Duration(tokenExpirationSeconds / 2)).MarshalBinary()
			if err != nil {
				return fmt.Errorf("error marshalling current time to bytes. %v", err)
			}
			secret := &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pxutil.PortworxServiceAccountTokenSecretName,
					Namespace:       cluster.Namespace,
					OwnerReferences: []metav1.OwnerReference{*ownerRef},
				},
				Type: v1.SecretTypeOpaque,
				Data: map[string][]byte{
					core.ServiceAccountTokenKey:     token,
					core.ServiceAccountRootCAKey:    rootCaCrt,
					core.ServiceAccountNamespaceKey: []byte(cluster.Namespace),
					TokenRefreshTimeKey:             tokenRefreshTimeBytes,
				},
			}
			if err := k8sutil.CreateOrUpdateSecret(t.k8sClient, secret, ownerRef); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("error getting the token secret for px from k8s. %v", err)
		}
	}
	return nil
}

func (t *portworxServiceAccountToken) maintainTokenSecret(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	secret := &v1.Secret{}
	err := t.k8sClient.Get(context.TODO(),
		types.NamespacedName{
			Name:      pxutil.PortworxServiceAccountTokenSecretName,
			Namespace: cluster.Namespace,
		}, secret)
	if err != nil {
		return fmt.Errorf("error getting the secret containing token for px container. %v", err)
	}
	needRefresh, err := isTokenRefreshRequired(secret)
	if err != nil {
		return err
	}
	if needRefresh {
		newToken, err := generateToken(cluster)
		if err != nil {
			return err
		}
		curTime := time.Now()
		tokenRefreshTimeBytes, err := curTime.Add(time.Duration(tokenExpirationSeconds / 2)).MarshalBinary()
		if err != nil {
			return fmt.Errorf("error marshalling current time to bytes. %v", err)
		}
		secret.Data[core.ServiceAccountTokenKey] = newToken
		secret.Data[TokenRefreshTimeKey] = tokenRefreshTimeBytes
		err = k8sutil.CreateOrUpdateSecret(t.k8sClient, secret, ownerRef)
		if err != nil {
			return err
		}
	}
	return nil
}

func generateToken(cluster *corev1.StorageCluster) ([]byte, error) {
	tokenRequest := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			Audiences:         []string{"px"},
			ExpirationSeconds: &tokenExpirationSeconds,
		},
	}
	tokenResp, err := coreops.Instance().CreateToken(pxutil.PortworxServiceAccountName(cluster), cluster.Namespace, tokenRequest)
	if err != nil {
		return nil, fmt.Errorf("error creating token from k8s. %v", err)
	}
	return []byte(tokenResp.Status.Token), nil
}

func isTokenRefreshRequired(secret *v1.Secret) (bool, error) {
	expiryTime := time.Time{}
	if err := expiryTime.UnmarshalBinary(secret.Data[TokenRefreshTimeKey]); err != nil {
		return false, fmt.Errorf("error converting expiry time bytes to struct. %v", err)
	}
	if time.Now().After(expiryTime) {
		return true, nil
	}
	return false, nil
}

func RegisterPortworxServiceAccountTokenComponent() {
	Register(PortworxServiceAccountTokenComponentName, &portworxServiceAccountToken{})
}

func init() {
	RegisterPortworxServiceAccountTokenComponent()
}
