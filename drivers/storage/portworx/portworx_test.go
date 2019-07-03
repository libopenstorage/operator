package portworx

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/openstorage/api"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/portworx/sched-ops/k8s"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestString(t *testing.T) {
	driver := portworx{}
	require.Equal(t, driverName, driver.String())
}

func TestInit(t *testing.T) {
	driver := portworx{}

	// Nil k8s client
	err := driver.Init(nil)
	require.EqualError(t, err, "kubernetes client cannot be nil")

	// Valid k8s client
	k8sClient := fake.NewFakeClient()
	err = driver.Init(k8sClient)
	require.NoError(t, err)
	require.Equal(t, k8sClient, driver.k8sClient)
}

func TestGetSelectorLabels(t *testing.T) {
	driver := portworx{}
	expectedLabels := map[string]string{labelKeyName: driverName}
	require.Equal(t, expectedLabels, driver.GetSelectorLabels())
}

func TestGetStorkDriverName(t *testing.T) {
	driver := portworx{}
	actualStorkDriverName, err := driver.GetStorkDriverName()
	require.NoError(t, err)
	require.Equal(t, storkDriverName, actualStorkDriverName)
}

func TestGetStorkEnvList(t *testing.T) {
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	envVars := driver.GetStorkEnvList(cluster)

	require.Len(t, envVars, 1)
	require.Equal(t, envKeyPortworxNamespace, envVars[0].Name)
	require.Equal(t, cluster.Namespace, envVars[0].Value)
}

func TestSetDefaultsOnStorageCluster(t *testing.T) {
	k8s.Instance().SetClient(fakek8sclient.NewSimpleClientset(), nil, nil, nil, nil, nil)
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedPlacement := &corev1alpha1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      "px/enabled",
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"false"},
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
						},
					},
				},
			},
		},
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(defaultStartPort), *cluster.Spec.StartPort)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// Empty kvdb spec should still set internal kvdb as default
	cluster.Spec.Kvdb = &corev1alpha1.KvdbSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)

	// Should not overwrite complete kvdb spec if endpoints are empty
	cluster.Spec.Kvdb = &corev1alpha1.KvdbSpec{
		AuthSecret: "test-secret",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, "test-secret", cluster.Spec.Kvdb.AuthSecret)

	// If endpoints are set don't set internal kvdb
	cluster.Spec.Kvdb = &corev1alpha1.KvdbSpec{
		Endpoints: []string{"endpoint1"},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.Kvdb.Internal)

	// Don't overwrite secrets provider if already set
	cluster.Spec.SecretsProvider = stringPtr("aws-kms")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "aws-kms", *cluster.Spec.SecretsProvider)

	// Don't overwrite secrets provider if set to empty
	cluster.Spec.SecretsProvider = stringPtr("")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "", *cluster.Spec.SecretsProvider)

	// Don't overwrite start port if already set
	startPort := uint32(10001)
	cluster.Spec.StartPort = &startPort
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, uint32(10001), *cluster.Spec.StartPort)

	// Add default placement if node placement is nil
	cluster.Spec.Placement = &corev1alpha1.PlacementSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestSetDefaultsOnStorageClusterForOpenshift(t *testing.T) {
	k8s.Instance().SetClient(fakek8sclient.NewSimpleClientset(), nil, nil, nil, nil, nil)
	driver := portworx{}
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsOpenshift: "true",
			},
		},
	}

	expectedPlacement := &corev1alpha1.PlacementSpec{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      "px/enabled",
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"false"},
							},
							{
								Key:      "node-role.kubernetes.io/infra",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
							{
								Key:      "node-role.kubernetes.io/master",
								Operator: v1.NodeSelectorOpDoesNotExist,
							},
						},
					},
				},
			},
		},
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(defaultStartPort), *cluster.Spec.StartPort)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestUpdateClusterStatus(t *testing.T) {
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
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateResponse{}
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:   "cluster-id",
			Name: "cluster-name",
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "cluster-name", cluster.Status.ClusterName)
	require.Equal(t, "cluster-id", cluster.Status.ClusterUID)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Init
	expectedClusterResp.Cluster.Status = api.Status_STATUS_INIT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Offline
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OFFLINE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Error
	expectedClusterResp.Cluster.Status = api.Status_STATUS_ERROR
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Decommission
	expectedClusterResp.Cluster.Status = api.Status_STATUS_DECOMMISSION
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Maintenance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_MAINTENANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status NeedsReboot
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NEEDS_REBOOT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status NotInQuorum
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status NotInQuorumNoStorage
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status Ok
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Running", cluster.Status.Phase)

	// Status StorageDown
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DOWN
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Running", cluster.Status.Phase)

	// Status StorageDegraded
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Degraded", cluster.Status.Phase)

	// Status StorageRebalance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Degraded", cluster.Status.Phase)

	// Status StorageDriveReplace
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Degraded", cluster.Status.Phase)

	// Status Invalid
	expectedClusterResp.Cluster.Status = api.Status(9999)
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Unknown", cluster.Status.Phase)
}

func TestUpdateClusterStatusForNodes(t *testing.T) {
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
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateResponse{
		NodeIds: []string{"node-1", "node-2"},
	}
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Status None
	expectedNodeOneResp := &api.SdkNodeInspectResponse{
		Node: &api.StorageNode{
			Id:                "node-1",
			SchedulerNodeName: "node-one",
			DataIp:            "10.0.1.1",
			MgmtIp:            "10.0.1.2",
			Status:            api.Status_STATUS_NONE,
		},
	}
	expectedNodeTwoResp := &api.SdkNodeInspectResponse{
		Node: &api.StorageNode{
			Id:                "node-2",
			SchedulerNodeName: "node-two",
			DataIp:            "10.0.2.1",
			MgmtIp:            "10.0.2.2",
			Status:            api.Status_STATUS_OK,
		},
	}
	gomock.InOrder(
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
			Return(expectedNodeOneResp, nil).
			Times(1),
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-2"}).
			Return(expectedNodeTwoResp, nil).
			Times(1),
	)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-1", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.1.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.1.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, corev1alpha1.NodeState, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-2", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.2.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.2.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, corev1alpha1.NodeState, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Return only one node id in enumerate for future tests
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateResponse{
		NodeIds: []string{"node-1"},
	}
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status Init
	expectedNodeOneResp.Node.Status = api.Status_STATUS_INIT
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeInit, nodeStatus.Status.Conditions[0].Status)

	// Status Offline
	expectedNodeOneResp.Node.Status = api.Status_STATUS_OFFLINE
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status Error
	expectedNodeOneResp.Node.Status = api.Status_STATUS_ERROR
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorum
	expectedNodeOneResp.Node.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorumNoStorage
	expectedNodeOneResp.Node.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status NeedsReboot
	expectedNodeOneResp.Node.Status = api.Status_STATUS_NEEDS_REBOOT
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status Decommission
	expectedNodeOneResp.Node.Status = api.Status_STATUS_DECOMMISSION
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeDecommissioned, nodeStatus.Status.Conditions[0].Status)

	// Status Maintenance
	expectedNodeOneResp.Node.Status = api.Status_STATUS_MAINTENANCE
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeMaintenance, nodeStatus.Status.Conditions[0].Status)

	// Status Ok
	expectedNodeOneResp.Node.Status = api.Status_STATUS_OK
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDown
	expectedNodeOneResp.Node.Status = api.Status_STATUS_STORAGE_DOWN
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDegraded
	expectedNodeOneResp.Node.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status StorageRebalance
	expectedNodeOneResp.Node.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDriveReplace
	expectedNodeOneResp.Node.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status Invalid
	expectedNodeOneResp.Node.Status = api.Status(9999)
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeOneResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha1.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha1.NodeUnknown, nodeStatus.Status.Conditions[0].Status)
}

func TestUpdateClusterStatusWithoutPortworxService(t *testing.T) {
	// Fake client without service
	k8sClient := fakeK8sClient()

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestUpdateClusterStatusServiceWithoutClusterIP(t *testing.T) {
	// Fake client with a service that does not have cluster ip
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to get endpoint")
}

func TestUpdateClusterStatusServiceGrpcServerError(t *testing.T) {
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "127.0.0.1",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error connecting to GRPC server")
}

func TestUpdateClusterStatusInspectClusterFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	// Error from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, fmt.Errorf("InspectCurrent error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "InspectCurrent error")

	// Nil response from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to inspect cluster")

	// Nil cluster object in the response of InspectCurrent API
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: nil,
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty ClusterInspect response")
}

func TestUpdateClusterStatusEnumerateNodesFailure(t *testing.T) {
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
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Error from node Enumerate API
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(nil, fmt.Errorf("node Enumerate error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "node Enumerate error")

	// Nil response from node Enumerate API
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(nil, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to enumerate nodes")

	// Empty list of node IDs should not create any StorageNodeStatus objects
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateResponse{
		NodeIds: []string{},
	}
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Nil list of node IDs should not create any StorageNodeStatus objects
	expectedNodeEnumerateResp.NodeIds = nil
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)
}

func TestUpdateClusterStatusInspectNodeFailures(t *testing.T) {
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
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateResponse{
		NodeIds: []string{"node-1", "node-2"},
	}
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Error from node Inspect API should skip creating corresponding node status object
	expectedNodeTwoResp := &api.SdkNodeInspectResponse{
		Node: &api.StorageNode{
			Id:                "node-2",
			SchedulerNodeName: "node-two",
			Status:            api.Status_STATUS_OK,
		},
	}
	gomock.InOrder(
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
			Return(nil, fmt.Errorf("error")).
			Times(1),
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-2"}).
			Return(expectedNodeTwoResp, nil).
			Times(1),
	)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatusList.Items[0].Status.Conditions[0].Status)

	// Nil response from Inspect API should skip creating corresponding node status object
	expectedNodeTwoResp.Node.Status = api.Status_STATUS_DECOMMISSION
	gomock.InOrder(
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
			Return(nil, nil).
			Times(1),
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-2"}).
			Return(expectedNodeTwoResp, nil).
			Times(1),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha1.NodeDecommissioned, nodeStatusList.Items[0].Status.Conditions[0].Status)

	// Nil node object from Inspect API should skip creating corresponding node status object
	expectedNodeTwoResp.Node.Status = api.Status_STATUS_MAINTENANCE
	gomock.InOrder(
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
			Return(&api.SdkNodeInspectResponse{Node: nil}, nil).
			Times(1),
		mockNodeServer.EXPECT().
			Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-2"}).
			Return(expectedNodeTwoResp, nil).
			Times(1),
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha1.NodeMaintenance, nodeStatusList.Items[0].Status.Conditions[0].Status)
}

func TestUpdateClusterStatusShouldUpdateStatusIfChanged(t *testing.T) {
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
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateResponse{
		NodeIds: []string{"node-1"},
	}
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_MAINTENANCE,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	expectedNodeResp := &api.SdkNodeInspectResponse{
		Node: &api.StorageNode{
			Id:                "node-1",
			SchedulerNodeName: "node-one",
			DataIp:            "1.1.1.1",
			Status:            api.Status_STATUS_MAINTENANCE,
		},
	}
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Offline", cluster.Status.Phase)
	nodeStatusList := &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha1.NodeMaintenance, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "1.1.1.1", nodeStatusList.Items[0].Status.Network.DataIP)

	// Update status based on the latest object
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	expectedNodeResp.Node.Status = api.Status_STATUS_OK
	expectedNodeResp.Node.DataIp = "2.2.2.2"
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-1"}).
		Return(expectedNodeResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Running", cluster.Status.Phase)
	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha1.NodeOnline, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "2.2.2.2", nodeStatusList.Items[0].Status.Network.DataIP)
}

func TestUpdateClusterStatusWithoutSchedulerNodeName(t *testing.T) {
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
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateResponse{
		NodeIds: []string{"node-uid"},
	}
	mockNodeServer.EXPECT().
		Enumerate(gomock.Any(), &api.SdkNodeEnumerateRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	expectedNodeResp := &api.SdkNodeInspectResponse{
		Node: &api.StorageNode{
			Id:       "node-uid",
			DataIp:   "1.1.1.1",
			MgmtIp:   "2.2.2.2",
			Hostname: "node-hostname",
		},
	}
	mockNodeServer.EXPECT().
		Inspect(gomock.Any(), &api.SdkNodeInspectRequest{NodeId: "node-uid"}).
		Return(expectedNodeResp, nil).
		AnyTimes()

	// Fake a node object without matching ip address or hostname to storage node
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Fake a node object with matching data ip address
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeInternalIP,
						Address: "1.1.1.1",
					},
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching mgmt ip address
	deleteObj(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeExternalIP,
						Address: "2.2.2.2",
					},
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname
	deleteObj(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeHostName,
						Address: "node-hostname",
					},
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname from labels
	deleteObj(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
				Labels: map[string]string{
					"kubernetes.io/hostname": "node-hostname",
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha1.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)
}

func fakeK8sClient(initObjects ...runtime.Object) client.Client {
	s := scheme.Scheme
	corev1alpha1.AddToScheme(s)
	return fake.NewFakeClientWithScheme(s, initObjects...)
}
