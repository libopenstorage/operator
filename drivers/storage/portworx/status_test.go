package portworx

import (
	"context"
	"testing"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
)

func TestSetupContextWithToken(t *testing.T) {
	var defaultSecret = []v1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pxutil.SecurityPXSharedSecretSecretName,
				Namespace: "ns",
			},
			Data: map[string][]byte{
				pxutil.SecuritySharedSecretKey: []byte("mysecret"),
			},
		},
	}

	var defaultConfigMap = []v1.ConfigMap{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pxutil.SecurityPXSharedSecretSecretName,
				Namespace: "ns",
			},
			// no data in secret
			Data: map[string]string{
				pxutil.SecuritySharedSecretKey: "mysecret",
			},
		},
	}

	tt := []struct {
		// test name
		name              string
		pxSecretName      string
		pxConfigMapName   string
		pxSharedSecretKey string // secret stored as env variable value
		initialSecrets    []v1.Secret
		initialConfigMaps []v1.ConfigMap

		expectError      bool
		expectTokenAdded bool
		expectedError    string
	}{
		{
			name:              "Shared secret key should add token to context",
			pxSecretName:      "",
			pxSharedSecretKey: "mysecret",

			expectTokenAdded: true,
			expectError:      false,
		},
		{
			name:              "Default config map should add token to context",
			pxConfigMapName:   defaultConfigMap[0].Name,
			initialConfigMaps: defaultConfigMap,

			expectTokenAdded: true,
			expectError:      false,
		},
		{
			name:           "Default secret should generate and add token to context",
			pxSecretName:   defaultSecret[0].Name,
			initialSecrets: defaultSecret,

			expectTokenAdded: true,
			expectError:      false,
		},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			k8sClient := testutil.FakeK8sClient()

			// create all initial k8s resources
			for _, initialSecret := range tc.initialSecrets {
				err := k8sClient.Create(context.Background(), &initialSecret)
				assert.NoError(t, err)
			}
			for _, initialCM := range tc.initialConfigMaps {
				err := k8sClient.Create(context.Background(), &initialCM)
				assert.NoError(t, err)
			}

			cluster := &corev1.StorageCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "testcluster",
					Namespace: "ns",
				},
				Spec: corev1.StorageClusterSpec{
					Security: &corev1.SecuritySpec{
						Enabled: true,
					},
				},
			}
			coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
			reregisterComponents()
			driver := portworx{}
			driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
			setSecuritySpecDefaults(cluster)
			cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

			err := driver.PreInstall(cluster)
			assert.NoError(t, err)

			// set env vars
			if tc.pxSharedSecretKey != "" {
				cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
					Name:  pxutil.EnvKeyPortworxAuthJwtSharedSecret,
					Value: tc.pxSharedSecretKey,
				})
			}

			// assign valueFrom secrets
			if tc.pxSecretName != "" {
				cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
					Name: pxutil.EnvKeyPortworxAuthJwtSharedSecret,
					ValueFrom: &v1.EnvVarSource{
						SecretKeyRef: &v1.SecretKeySelector{
							LocalObjectReference: v1.LocalObjectReference{
								Name: tc.pxSecretName,
							},
							Key: pxutil.SecuritySharedSecretKey,
						},
					},
				})
			}

			// assign valueFrom configmaps
			if tc.pxConfigMapName != "" {
				cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
					Name: pxutil.EnvKeyPortworxAuthJwtSharedSecret,
					ValueFrom: &v1.EnvVarSource{
						ConfigMapKeyRef: &v1.ConfigMapKeySelector{
							LocalObjectReference: v1.LocalObjectReference{
								Name: tc.pxConfigMapName,
							},
							Key: pxutil.SecuritySharedSecretKey,
						},
					},
				})
			}
			// setup context and assert
			p := portworx{
				k8sClient: k8sClient,
			}
			ctx, err := pxutil.SetupContextWithToken(context.Background(), cluster, p.k8sClient)
			if tc.expectError {
				assert.Error(t, err)
				assert.EqualError(t, err, tc.expectedError)
			} else {
				assert.NoError(t, err)
			}

			if tc.expectTokenAdded {
				md, ok := metadata.FromOutgoingContext(ctx)
				assert.Equal(t, ok, true, "Expected metadata to be found")
				authValue := md.Get("authorization")
				assert.Equal(t, len(authValue), 1, "Expected authorization token to be found")
			} else {
				_, ok := metadata.FromOutgoingContext(ctx)
				assert.Equal(t, false, ok, "Expected no metadata to be found")
			}
		})
	}
}
