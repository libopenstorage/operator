package portworx

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/libopenstorage/operator/pkg/util"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
			err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(0))
			require.NoError(t, err)
			setSecuritySpecDefaults(cluster)
			cluster.Spec.Security.Auth.GuestAccess = guestAccessTypePtr(corev1.GuestRoleManaged)

			err = driver.PreInstall(cluster)
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

func TestUpdateStorageNodePhase(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	err := mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	require.NoError(t, err)
	defer mockSdk.Stop()

	setupEtcHosts(t, sdkServerIP, pxutil.PortworxServiceName+".kube-test")
	defer restoreEtcHosts(t)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Status: corev1.StorageClusterStatus{
			Phase: string(corev1.ClusterStateInit),
		},
	}
	latestHash := "latest-hash"

	testCases := []struct {
		name          string
		pxNode        *api.StorageNode
		storageNode   *corev1.StorageNode
		expectedPhase string
	}{
		{
			name: "with-no-conditions",
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{},
			},
			expectedPhase: "Initializing",
		},
		{
			name: "with-only-succeeded-init-condition",
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeInitCondition,
							Status:             corev1.NodeSucceededStatus,
							LastTransitionTime: metav1.NewTime(time.Now()),
						},
					},
				},
			},
			expectedPhase: "Initializing",
		},
		{
			name: "with-succeeded-init-condition-and-state-condition-without-status",
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeInitCondition,
							Status:             corev1.NodeSucceededStatus,
							LastTransitionTime: metav1.NewTime(time.Now()),
						},
						{
							Type:               corev1.NodeStateCondition,
							LastTransitionTime: metav1.NewTime(time.Now().Add(time.Minute)),
						},
					},
				},
			},
			expectedPhase: "Initializing",
		},
		{
			name: "with-old-succeeded-init-condition-and-valid-state-condition",
			pxNode: &api.StorageNode{
				Id:                "id",
				SchedulerNodeName: "k8s-node",
				Status:            api.Status_STATUS_NOT_IN_QUORUM,
			},
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeInitCondition,
							Status:             corev1.NodeSucceededStatus,
							LastTransitionTime: metav1.NewTime(time.Now().Add(time.Minute * -1)),
						},
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "NotInQuorum",
		},
		{
			name: "with-new-succeeded-init-condition-and-valid-state-condition",
			pxNode: &api.StorageNode{
				Id:                "id",
				SchedulerNodeName: "k8s-node",
				Status:            api.Status_STATUS_NOT_IN_QUORUM,
			},
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeInitCondition,
							Status:             corev1.NodeSucceededStatus,
							LastTransitionTime: metav1.NewTime(time.Now().Add(time.Minute)),
						},
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "NotInQuorum",
		},
		{
			name: "with-no-init-condition-and-valid-state-condition",
			pxNode: &api.StorageNode{
				Id:                "id",
				SchedulerNodeName: "k8s-node",
				Status:            api.Status_STATUS_NOT_IN_QUORUM,
			},
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "NotInQuorum",
		},
		{
			name: "with-old-failed-init-condition-and-valid-state-condition",
			pxNode: &api.StorageNode{
				Id:                "id",
				SchedulerNodeName: "k8s-node",
				Status:            api.Status_STATUS_NOT_IN_QUORUM,
			},
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeInitCondition,
							Status:             corev1.NodeFailedStatus,
							LastTransitionTime: metav1.NewTime(time.Now().Add(time.Minute * -1)),
						},
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "NotInQuorum",
		},
		{
			name: "with-new-failed-init-condition-and-valid-state-condition",
			pxNode: &api.StorageNode{
				Id:                "id",
				SchedulerNodeName: "k8s-node",
				Status:            api.Status_STATUS_NOT_IN_QUORUM,
			},
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeInitCondition,
							Status:             corev1.NodeFailedStatus,
							LastTransitionTime: metav1.NewTime(time.Now().Add(time.Minute)),
						},
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "Failed",
		},
		{
			name: "with-old-hash-should-mark-as-upgrading-in-existing-nodes",
			pxNode: &api.StorageNode{
				Id:                "id",
				SchedulerNodeName: "k8s-node",
				Status:            api.Status_STATUS_OK,
			},
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						util.DefaultStorageClusterUniqueLabelKey: "old-hash",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "Updating",
		},
		{
			name:   "with-old-hash-should-mark-as-upgrading-in-remaining-nodes",
			pxNode: nil,
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						util.DefaultStorageClusterUniqueLabelKey: "old-hash",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "Updating",
		},
		{
			name: "with-latest-hash-and-validate-status-phase",
			pxNode: &api.StorageNode{
				Id:                "id",
				SchedulerNodeName: "k8s-node",
				Status:            api.Status_STATUS_OK,
			},
			storageNode: &corev1.StorageNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-node",
					Namespace: cluster.Namespace,
					Labels: map[string]string{
						util.DefaultStorageClusterUniqueLabelKey: latestHash,
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						// NodeState condition will be automatically added
					},
				},
			},
			expectedPhase: "Online",
		},
	}

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := testutil.FakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxutil.PortworxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxutil.PortworxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pxPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx-pod",
			Namespace:       cluster.Namespace,
			Labels:          pxutil.SelectorLabels(),
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.PodSpec{
			NodeName: "k8s-node",
			Containers: []v1.Container{{
				Name: "portworx",
				Args: []string{
					"-c", "px-cluster",
				},
			}},
		},
	}
	pxPod.Labels[util.DefaultStorageClusterUniqueLabelKey] = latestHash
	err = k8sClient.Create(context.TODO(), pxPod.DeepCopy())
	require.NoError(t, err)

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), gomock.Any()).
		Return(expectedClusterResp, nil).
		AnyTimes()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := portworx{
				k8sClient: k8sClient,
			}
			err := k8sClient.Create(context.TODO(), tc.storageNode, &client.CreateOptions{})
			require.NoError(t, err)

			expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
				Nodes: []*api.StorageNode{tc.pxNode},
			}
			mockNodeServer.EXPECT().
				EnumerateWithFilters(gomock.Any(), gomock.Any()).
				Return(expectedNodeEnumerateResp, nil).
				Times(1)

			err = driver.UpdateStorageClusterStatus(cluster, latestHash)
			require.NoError(t, err)

			storageNode := &corev1.StorageNode{}
			err = testutil.Get(k8sClient, storageNode, tc.storageNode.Name, cluster.Namespace)
			require.NoError(t, err)
			require.Equal(t, tc.expectedPhase, storageNode.Status.Phase)

			err = testutil.Delete(k8sClient, tc.storageNode)
			require.NoError(t, err)
		})
	}
}
